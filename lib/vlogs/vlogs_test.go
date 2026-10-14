package vlogs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/katbyte/go-kt/chttp"
)

// fakeLogs is a VictoriaLogs that keeps what it is sent, and answers with the status it is told.
type fakeLogs struct {
	mu       sync.Mutex
	status   int
	requests []*http.Request
	bodies   []string
}

func (f *fakeLogs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, r)
	f.bodies = append(f.bodies, string(body))
	if f.status != 0 {
		http.Error(w, "said no", f.status)

		return
	}
}

func (f *fakeLogs) answer(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.status = status
}

// sent is the requests it was sent so far.
func (f *fakeLogs) sent() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]*http.Request(nil), f.requests...)
}

// rows is every row it was sent, whatever it answered.
func (f *fakeLogs) rows() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	rows := make([]string, 0, len(f.bodies))
	for _, b := range f.bodies {
		rows = append(rows, strings.SplitAfter(b, "\n")...)
	}

	return slicesWithout(rows, "")
}

func slicesWithout(s []string, v string) []string {
	out := s[:0]
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}

	return out
}

// started is a fake VictoriaLogs and a sender to it holding at most queueSize rows.
func started(t *testing.T, queueSize int) (*fakeLogs, *Sender) {
	t.Helper()

	f := &fakeLogs{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	s, err := New(Options{URL: srv.URL, StreamFields: []string{"hostname", "app_name"}, QueueSize: queueSize})
	if err != nil {
		t.Fatal(err)
	}

	return f, s
}

func row(n int) []byte {
	return fmt.Appendf(nil, "{\"_msg\":\"row %d\"}\n", n)
}

func TestNew(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"", "10.0.0.2:9428", "ftp://10.0.0.2", "http://"} {
		if _, err := New(Options{URL: bad, QueueSize: 1}); err == nil {
			t.Errorf("New(%q) gave no error", bad)
		}
	}
	if _, err := New(Options{URL: "http://10.0.0.2:9428", QueueSize: 0}); err == nil {
		t.Error("a queue that can hold nothing gave no error")
	}

	s, err := New(Options{URL: "http://10.0.0.2:9428/", StreamFields: []string{"hostname", "cef.device_product"}, QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://10.0.0.2:9428/insert/jsonline?_stream_fields=hostname%2Ccef.device_product"; s.Endpoint() != want {
		t.Errorf("Endpoint = %q, want %q", s.Endpoint(), want)
	}
}

func TestFlush(t *testing.T) {
	t.Parallel()

	f, s := started(t, 10)
	for i := range 3 {
		s.Add(row(i))
	}

	if err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	if len(f.sent()) != 1 {
		t.Fatalf("%d requests, want the three rows in one", len(f.sent()))
	}
	r := f.sent()[0]
	if r.Method != http.MethodPost || r.URL.Path != Path || r.URL.Query().Get("_stream_fields") != "hostname,app_name" {
		t.Errorf("sent %s %s, want POST %s with the stream fields", r.Method, r.URL, Path)
	}
	if got := strings.Join(f.rows(), ""); got != string(row(0))+string(row(1))+string(row(2)) {
		t.Errorf("sent %q, want the rows in order", got)
	}
	if got := s.Stats(); got != (Stats{Sent: 3}) {
		t.Errorf("Stats = %+v, want 3 sent", got)
	}

	// nothing held is nothing sent
	if err := s.Flush(t.Context()); err != nil || len(f.sent()) != 1 {
		t.Errorf("an empty flush sent %d requests and gave %v, want none and nil", len(f.sent())-1, err)
	}
}

// While VictoriaLogs answers with a fault the rows are held, in order, and sent once it takes them again.
func TestHeldWhileDown(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusServiceUnavailable, http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			f, s := started(t, 10)
			f.answer(status)

			s.Add(row(0))
			err := s.Flush(t.Context())
			if chttp.StatusCode(err) != status || !strings.Contains(err.Error(), "said no") {
				t.Fatalf("Flush = %v while VictoriaLogs answered %d, want an error with its status and what it said", err, status)
			}
			s.Add(row(1))
			if err := s.Flush(t.Context()); err == nil {
				t.Fatal("Flush gave no error while VictoriaLogs answered with a fault")
			}

			if got := s.Stats(); got != (Stats{Failures: 2, Queued: 2}) {
				t.Errorf("while down, Stats = %+v, want 2 failures and both rows held", got)
			}

			f.answer(0)
			if err := s.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}

			rows := f.rows()
			if got := strings.Join(rows[len(rows)-2:], ""); got != string(row(0))+string(row(1)) {
				t.Errorf("once up, sent %q, want the held rows oldest first", got)
			}
			if got := s.Stats(); got != (Stats{Sent: 2, Failures: 2}) {
				t.Errorf("once up, Stats = %+v, want both sent", got)
			}
		})
	}
}

// One VictoriaLogs cannot be reached at all is the same as one answering with a fault.
func TestHeldWhileUnreachable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	s, err := New(Options{URL: url, QueueSize: 10})
	if err != nil {
		t.Fatal(err)
	}

	s.Add(row(0))
	if err := s.Flush(t.Context()); err == nil {
		t.Fatal("Flush gave no error with nothing listening")
	}
	if got := s.Stats(); got != (Stats{Failures: 1, Queued: 1}) {
		t.Errorf("Stats = %+v, want the row held", got)
	}
}

// Past the queue's size the oldest rows are dropped, and counted, both as they arrive and when a batch that was not taken is put back.
func TestDropsOldest(t *testing.T) {
	t.Parallel()

	f, s := started(t, 3)
	f.answer(http.StatusServiceUnavailable)

	for i := range 5 {
		s.Add(row(i))
	}
	if got := s.Stats(); got != (Stats{Dropped: 2, Queued: 3}) {
		t.Fatalf("Stats = %+v, want 2 dropped and 3 held", got)
	}

	// the batch of three is put back behind nothing, then one more arrives and pushes the oldest out
	_ = s.Flush(t.Context())
	s.Add(row(5))
	if got := s.Stats(); got != (Stats{Failures: 1, Dropped: 3, Queued: 3}) {
		t.Fatalf("Stats = %+v, want 3 dropped and 3 held", got)
	}

	f.answer(0)
	if err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	rows := f.rows()
	if got := strings.Join(rows[len(rows)-3:], ""); got != string(row(3))+string(row(4))+string(row(5)) {
		t.Errorf("sent %q, want the newest three", got)
	}
}

// Rows VictoriaLogs refuses as bad are dropped, not sent again and again in front of everything behind them.
func TestRefused(t *testing.T) {
	t.Parallel()

	f, s := started(t, 10)
	f.answer(http.StatusBadRequest)

	s.Add(row(0))
	if err := s.Flush(t.Context()); err != nil {
		t.Fatalf("Flush = %v, want the refused rows given up on without an error", err)
	}
	if got := s.Stats(); got != (Stats{Failures: 1, Dropped: 1}) {
		t.Errorf("Stats = %+v, want the row dropped", got)
	}
}

// A queue larger than one request should carry is sent in several.
func TestBatches(t *testing.T) {
	t.Parallel()

	f, s := started(t, 100)

	big := []byte("{\"_msg\":\"" + strings.Repeat("x", maxBatchBytes/3) + "\"}\n")
	for range 5 {
		s.Add(big)
	}

	if err := s.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.sent()) != 3 {
		t.Errorf("%d requests, want 3: two rows to a batch", len(f.sent()))
	}
	if got := s.Stats(); got != (Stats{Sent: 5}) {
		t.Errorf("Stats = %+v, want 5 sent", got)
	}
}

// Run sends on its own, and when it is stopped sends what is left before it returns.
func TestRun(t *testing.T) {
	t.Parallel()

	f := &fakeLogs{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	s, err := New(Options{URL: srv.URL, QueueSize: 10, Interval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	s.Add(row(0))
	deadline := time.Now().Add(10 * time.Second)
	for s.Stats().Sent != 1 {
		if time.Now().After(deadline) {
			t.Fatal("a row was never sent")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// stopped straight after a row arrives, it still sends it
	s.Add(row(1))
	cancel()
	<-done

	if got := s.Stats(); got != (Stats{Sent: 2}) {
		t.Errorf("Stats = %+v, want both rows sent", got)
	}
}
