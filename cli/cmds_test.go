package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // the tests read times in America/Vancouver, wherever they run

	"github.com/katbyte/go-kt/pmetrics"
	"github.com/katbyte/syslog-relay/lib/relay"
	"github.com/katbyte/syslog-relay/lib/vlogs"
	"github.com/spf13/cobra"
)

// The lines the commands are given: an event UniFi Protect sends, which VictoriaLogs alone stores with no message, and an ordinary syslog line.
const (
	motion = `Oct 13 18:58:31 nvr CEF:0|Ubiquiti|UniFi Protect|7.3.70|2159|motion|3|UNIFIcategory=detection eventType=motion msg=garage has recorded motion.`
	backup = `<30>Oct 13 19:51:00 cloudkey unifi-core[1234]: the console started a backup`
)

func newRelay(s *vlogs.Sender) *relay.Relay {
	return relay.New(relay.Options{}, s)
}

// program is the program given these arguments, not yet run, and where what it prints will go.
func program(t *testing.T, stdin string, args ...string) (root *cobra.Command, stdout, stderr *bytes.Buffer) {
	t.Helper()

	root, err := Make()
	if err != nil {
		t.Fatalf("Make: %v", err)
	}

	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)

	return root, stdout, stderr
}

// run executes the program with these arguments and gives back what it printed.
func run(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	root, out, errOut := program(t, stdin, args...)
	err = root.ExecuteContext(t.Context())

	return out.String(), errOut.String(), err
}

// rowsOfJSON reads the JSON lines printed back as fields.
func rowsOfJSON(t *testing.T, s string) []map[string]string {
	t.Helper()

	var rows []map[string]string
	for line := range strings.Lines(s) {
		var f map[string]string
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("%q is not a row: %v", line, err)
		}
		rows = append(rows, f)
	}

	return rows
}

//nolint:paralleltest // viper is global state; these mutate it
func TestVersion(t *testing.T) {
	alone(t)

	out, _, err := run(t, "", "version")
	if err != nil || !strings.HasPrefix(out, "syslog-relay ") {
		t.Errorf("version = %q, %v", out, err)
	}

	out, _, err = run(t, "")
	if err != nil || !strings.Contains(out, "syslog-relay help") {
		t.Errorf("no command = %q, %v; want a pointer to help", out, err)
	}
}

//nolint:paralleltest // viper is global state; these mutate it
func TestParse(t *testing.T) {
	alone(t)

	out, _, err := run(t, motion+"\n"+backup+"\n", "parse", "--timezone", "America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}

	rows := rowsOfJSON(t, out)
	if len(rows) != 2 {
		t.Fatalf("parse printed %d rows, want 2:\n%s", len(rows), out)
	}
	if rows[0]["_msg"] != "garage has recorded motion." || rows[0]["cef.name"] != "motion" || rows[0]["_time"] != "2026-10-14T01:58:31Z" {
		t.Errorf("the motion event = %v, want its msg as _msg and its time read in Vancouver", rows[0])
	}
	if rows[1]["_msg"] != "the console started a backup" || rows[1]["app_name"] != "unifi-core" {
		t.Errorf("the backup line = %v, want its own message", rows[1])
	}
}

// The settings that change a row change it the same way here as in serve.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestParseSettings(t *testing.T) {
	alone(t)

	out, _, err := run(t, motion, "parse", "--msg-field", `["message"]`, "--use-local-timestamp")
	if err != nil {
		t.Fatal(err)
	}

	row := rowsOfJSON(t, out)[0]
	if row["_msg"] != "" || row["cef.extension.msg"] != "garage has recorded motion." || row["timestamp"] == "" {
		t.Errorf("row = %v, want VictoriaLogs' own: no _msg, the msg left where it was, and the time written kept", row)
	}

	out, _, err = run(t, `Oct 13 18:58:31 nvr CEF:0|Ubiquiti|UniFi Protect|7.3.70|2159|motion|3|msg="garage has recorded motion."`, "parse", "--strip-quotes")
	if err != nil {
		t.Fatal(err)
	}
	if row := rowsOfJSON(t, out)[0]; row["_msg"] != "garage has recorded motion." {
		t.Errorf("with --strip-quotes, _msg = %q, want UniFi Protect's quotes off", row["_msg"])
	}

	if _, _, err := run(t, motion, "parse", "--timezone", "Mars/Olympus"); err == nil || !strings.Contains(err.Error(), "SYSLOG_RELAY_TIMEZONE") {
		t.Errorf("a zone there is none of = %v, want an error naming the setting", err)
	}
}

//nolint:paralleltest // viper is global state; these mutate it
func TestParseFiles(t *testing.T) {
	_, wd := alone(t)

	one := write(t, wd, "one.log", motion+"\n")
	two := write(t, wd, "two.log", backup+"\n")

	out, _, err := run(t, "", "parse", one, two)
	if err != nil {
		t.Fatal(err)
	}
	if rows := rowsOfJSON(t, out); len(rows) != 2 || rows[0]["cef.name"] != "motion" || rows[1]["app_name"] != "unifi-core" {
		t.Errorf("parse of two files = %v, want a row from each, in order", rows)
	}

	if _, _, err := run(t, "", "parse", filepath.Join(wd, "missing.log")); err == nil {
		t.Error("parse of a file that is not there gave no error")
	}
}

// A line VictoriaLogs would drop is said, the rest are printed, and the command fails.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestParseRejected(t *testing.T) {
	alone(t)

	out, errOut, err := run(t, "<14>1 yesterday nvr app - - - hi\n"+backup+"\n", "parse")
	if err == nil || !strings.Contains(err.Error(), "1 lines were dropped") {
		t.Errorf("parse = %v, want it to say one line was dropped", err)
	}
	if !strings.Contains(errOut, "yesterday") {
		t.Errorf("stderr = %q, want the line that was dropped", errOut)
	}
	if rows := rowsOfJSON(t, out); len(rows) != 1 {
		t.Errorf("parse printed %d rows, want the one that could be read", len(rows))
	}
}

//nolint:paralleltest // viper is global state; these mutate it
func TestServeNeedsVictoriaLogs(t *testing.T) {
	alone(t)

	if _, _, err := run(t, "", "serve"); err == nil || !strings.Contains(err.Error(), "SYSLOG_RELAY_LOGS_URL") {
		t.Errorf("serve with no VictoriaLogs = %v, want an error naming the setting", err)
	}
}

// fakeLogs is a VictoriaLogs that keeps every row it is sent.
type fakeLogs struct {
	mu   sync.Mutex
	rows []string
	urls []string
}

func (f *fakeLogs) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.urls = append(f.urls, r.URL.String())
	for line := range strings.Lines(string(body)) {
		f.rows = append(f.rows, line)
	}
}

// waitRows waits until it holds n rows, and gives them back as fields.
func (f *fakeLogs) waitRows(t *testing.T, n int) []map[string]string {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	for {
		f.mu.Lock()
		rows := strings.Join(f.rows, "")
		f.mu.Unlock()

		if got := rowsOfJSON(t, rows); len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("VictoriaLogs holds %q, waited for %d rows", rows, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// freeAddr is an address on this machine nothing is listening on, over TCP or UDP, a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()

	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	return addr
}

// scrape reads one path of the relay's own page, "" until it answers.
func scrape(t *testing.T, url string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	b, _ := io.ReadAll(resp.Body)

	return string(b)
}

// TestServe runs the relay as a deployment runs it: lines sent over UDP and TCP reach a VictoriaLogs as rows with their message, its own readings
// count them, the raw copy keeps them, and a signal stops it cleanly.
func TestServe(t *testing.T) {
	_, wd := alone(t)

	logs := &fakeLogs{}
	srv := httptest.NewServer(logs)
	t.Cleanup(srv.Close)

	syslog, metrics := freeAddr(t), freeAddr(t)
	raw := filepath.Join(wd, "unifi.raw")
	t.Setenv("SYSLOG_RELAY_LOGS_URL", srv.URL)
	t.Setenv("SYSLOG_RELAY_USE_REMOTE_IP", "true")
	t.Setenv("SYSLOG_RELAY_USE_LOCAL_TIMESTAMP", "true")
	t.Setenv("SYSLOG_RELAY_RAW_FILE", raw)

	ctx, cancel := context.WithCancel(t.Context())
	root, _, _ := program(t, "", "serve", "--listen", syslog, "--metrics-listen", metrics, "--labels", "hostname=dock")
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	deadline := time.Now().Add(15 * time.Second)
	for scrape(t, "http://"+metrics+pmetrics.HealthPath) != "ok\n" {
		if time.Now().After(deadline) {
			t.Fatal("the relay never answered its health check")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var d net.Dialer
	udp, err := d.DialContext(t.Context(), "udp", syslog)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = udp.Close() }()
	if _, err := udp.Write([]byte(motion)); err != nil {
		t.Fatal(err)
	}

	tcp, err := d.DialContext(t.Context(), "tcp", syslog)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tcp.Close() }()
	if _, err := tcp.Write([]byte(backup + "\n")); err != nil {
		t.Fatal(err)
	}

	rows := logs.waitRows(t, 2)
	byMsg := map[string]map[string]string{}
	for _, r := range rows {
		byMsg[r["_msg"]] = r
	}
	if r := byMsg["garage has recorded motion."]; r == nil || r["remote_ip"] != "127.0.0.1" || r["timestamp"] == "" {
		t.Errorf("rows = %v, want the motion event with its msg, the sender's address and the time written kept", rows)
	}
	if byMsg["the console started a backup"] == nil {
		t.Errorf("rows = %v, want the line sent over TCP", rows)
	}

	logs.mu.Lock()
	sentTo := logs.urls[0]
	logs.mu.Unlock()
	if !strings.HasPrefix(sentTo, vlogs.Path+"?_stream_fields=hostname%2Capp_name%2Cproc_id") {
		t.Errorf("rows sent to %s, want %s with VictoriaLogs' own stream fields", sentTo, vlogs.Path)
	}

	page := scrape(t, "http://"+metrics+pmetrics.Path)
	for _, want := range []string{
		`syslog_relay_lines_received_total{hostname="dock",protocol="tcp"} 1`,
		`syslog_relay_lines_received_total{hostname="dock",protocol="udp"} 1`,
		`syslog_relay_rows_sent_total{hostname="dock"} 2`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %s:\n%s", want, page)
		}
	}

	cancel()
	select {
	case ended := <-done:
		if ended != nil {
			t.Errorf("serve ended with %v once stopped, want nil", ended)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}

	copied, err := os.ReadFile(raw) //nolint:gosec // the copy the test asked for
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(copied), motion+"\n") || !strings.Contains(string(copied), backup+"\n") {
		t.Errorf("the raw copy holds %q, want both lines as they were sent", copied)
	}
}

// A port that is taken stops the relay, rather than leaving it taking syslog with no health check to show it is there.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestServePortTaken(t *testing.T) {
	alone(t)

	var lc net.ListenConfig
	taken, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taken.Close() })

	_, _, err = run(t, "", "serve", "--logs-url", testLogsURL, "--listen", freeAddr(t), "--metrics-listen", taken.Addr().String())
	if err == nil {
		t.Error("serve with its metrics port taken gave no error")
	}

	_, _, err = run(t, "", "serve", "--logs-url", testLogsURL, "--listen", taken.Addr().String(), "--metrics-listen", freeAddr(t))
	if err == nil {
		t.Error("serve with its syslog port taken gave no error")
	}
}
