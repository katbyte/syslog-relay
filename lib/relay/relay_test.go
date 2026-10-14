package relay

import (
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/syslog-relay/lib/convert"
	"github.com/katbyte/syslog-relay/lib/listen"
	"github.com/katbyte/syslog-relay/lib/vlogs"
)

// queue keeps the rows it is given.
type queue struct {
	mu   sync.Mutex
	rows [][]byte
}

func (q *queue) Add(row []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.rows = append(q.rows, row)
}

func (q *queue) Stats() vlogs.Stats {
	q.mu.Lock()
	defer q.mu.Unlock()

	return vlogs.Stats{Queued: len(q.rows)}
}

// raw keeps the lines it is given.
type raw struct {
	lines []string
}

func (r *raw) Write(line []byte) {
	r.lines = append(r.lines, string(line))
}

var (
	sender  = &net.UDPAddr{IP: net.IPv4(192, 0, 2, 11), Port: 40514}
	arrived = time.Date(2026, 10, 14, 3, 30, 0, 0, time.UTC)
)

const cef = `Oct 13 18:58:31 nvr CEF:0|Ubiquiti|UniFi Protect|7.3.70|2159|motion|3|eventType=motion msg=garage has recorded motion.`

func relayed(o Options) (*Relay, *queue) {
	q := &queue{}
	r := New(o, q)
	r.now = func() time.Time { return arrived }

	return r, q
}

func fields(t *testing.T, row []byte) map[string]string {
	t.Helper()

	var f map[string]string
	if err := json.Unmarshal(row, &f); err != nil {
		t.Fatalf("%q: %v", row, err)
	}

	return f
}

func TestHandle(t *testing.T) {
	t.Parallel()

	keep := &raw{}
	r, q := relayed(Options{Convert: convert.Options{MsgFields: convert.DefaultMsgFields, UseLocalTimestamp: true, Timezone: time.UTC}, UseRemoteIP: true, Raw: keep})

	line := []byte(cef)
	r.Handle(listen.UDP, line, sender)
	// the listener reuses its buffer once the handler returns
	copy(line, "XXXXXXXX")

	if len(q.rows) != 1 {
		t.Fatalf("%d rows queued, want 1", len(q.rows))
	}
	f := fields(t, q.rows[0])
	if f["_msg"] != "garage has recorded motion." || f["remote_ip"] != "192.0.2.11" || f["_time"] != "2026-10-14T03:30:00Z" || f["hostname"] != "nvr" {
		t.Errorf("row = %v, want the msg as _msg, the sender's address and the time it arrived", f)
	}
	if len(keep.lines) != 1 || keep.lines[0] != cef {
		t.Errorf("the raw copy holds %q, want the line as it arrived", keep.lines)
	}

	r.Handle(listen.TCP, []byte(cef), sender)
	r.Handle(listen.TCP, []byte(cef), sender)
	if got := r.Stats(); got.Received[listen.UDP] != 1 || got.Received[listen.TCP] != 2 || got.Send.Queued != 3 {
		t.Errorf("Stats = %+v, want 1 line over UDP, 2 over TCP, and 3 rows queued", got)
	}
}

// Without leave to, the sender's address is not recorded, and no raw copy is no fault.
func TestHandleWithoutRemoteIP(t *testing.T) {
	t.Parallel()

	r, q := relayed(Options{Convert: convert.Options{MsgFields: convert.DefaultMsgFields, Timezone: time.UTC}})
	r.Handle(listen.UDP, []byte(cef), sender)

	if f := fields(t, q.rows[0]); f["remote_ip"] != "" {
		t.Errorf("remote_ip = %q, want it left out", f["remote_ip"])
	}
}

// A line whose time cannot be read is dropped and counted, as VictoriaLogs drops it, and is still in the raw copy.
func TestHandleRejected(t *testing.T) {
	t.Parallel()

	keep := &raw{}
	r, q := relayed(Options{Convert: convert.Options{Timezone: time.UTC}, Raw: keep})
	r.Handle(listen.UDP, []byte(`<14>1 yesterday nvr app - - - hi`), sender)

	if len(q.rows) != 0 || r.Stats().Rejected != 1 || len(keep.lines) != 1 {
		t.Errorf("%d rows queued, %d rejected and %d lines copied: want the line dropped, counted and copied", len(q.rows), r.Stats().Rejected, len(keep.lines))
	}
}

func TestFailed(t *testing.T) {
	t.Parallel()

	r, _ := relayed(Options{})
	r.Failed(listen.TCP, errors.New("cut off"))
	r.Failed(listen.TCP, errors.New("cut off"))
	r.Failed(listen.UDP, errors.New("too long"))

	if got := r.Stats().ReadFailures; got[listen.TCP] != 2 || got[listen.UDP] != 1 {
		t.Errorf("ReadFailures = %v, want 2 over TCP and 1 over UDP", got)
	}
}
