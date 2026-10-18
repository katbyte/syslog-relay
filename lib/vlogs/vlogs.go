// Package vlogs sends rows to VictoriaLogs as JSON lines, in batches, and holds them in memory while it cannot be reached: up to a limit, past
// which the oldest are dropped and counted.
package vlogs

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/httpc"
)

// The defaults: rows are sent every second, a batch is at most a few megabytes, and a VictoriaLogs that does not answer within half a minute has
// not taken the batch.
const (
	DefaultInterval = time.Second
	maxBatchBytes   = 4 * 1024 * 1024
	requestTimeout  = 30 * time.Second
	// finalTimeout is how long what is still held is tried for when the relay stops.
	finalTimeout = 10 * time.Second
	// maxAnswer is the most of VictoriaLogs' answer that is read: it says nothing when it takes rows, and a line or two when it does not.
	maxAnswer = 64 * 1024
)

// Path is where VictoriaLogs takes JSON lines.
const Path = "/insert/jsonline"

// Options says where rows go and how many are held.
type Options struct {
	// URL is VictoriaLogs' own address: http://10.0.0.2:9428.
	URL string
	// StreamFields are the fields VictoriaLogs tells one stream of logs from another by.
	StreamFields []string
	// QueueSize is the most rows held while VictoriaLogs cannot be reached.
	QueueSize int
	// Interval is how often held rows are sent; zero is DefaultInterval.
	Interval time.Duration
}

// Stats are the counts of rows since the sender was made.
type Stats struct {
	// Sent is rows VictoriaLogs took, Failures is requests it did not answer or answered with a fault, and Dropped is rows given up on: the oldest
	// beyond QueueSize, and any VictoriaLogs refused outright.
	Sent, Failures, Dropped uint64
	// Queued is rows waiting to be sent now.
	Queued int
}

// Sender holds rows until they are sent. Add is safe to call from many goroutines while Run sends.
type Sender struct {
	endpoint  string
	queueSize int
	interval  time.Duration
	client    *httpc.Client

	mu    sync.Mutex
	queue [][]byte // oldest first
	down  bool     // the last send failed, so the next to work is worth saying

	sent, failures, dropped atomic.Uint64
}

// New is a sender to the VictoriaLogs at o.URL. An address that is not one, or a queue with no room, is said now rather than at the first send.
func New(o Options) (*Sender, error) {
	u, err := url.Parse(o.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not VictoriaLogs' address: give it as http://host:9428", o.URL)
	}
	if o.QueueSize < 1 {
		return nil, fmt.Errorf("a queue of %d rows cannot hold any", o.QueueSize)
	}

	q := url.Values{}
	if len(o.StreamFields) > 0 {
		q.Set("_stream_fields", strings.Join(o.StreamFields, ","))
	}
	u.Path = strings.TrimRight(u.Path, "/") + Path
	u.RawQuery = q.Encode()

	// each batch is sent once: one that is not taken is held and sent again with the next, so trying again here would only hold the rest up
	client := httpc.New(httpc.Options{Name: "VictoriaLogs", Log: clog.Log, Retry: httpc.Retry{Tries: 1}})

	s := &Sender{endpoint: u.String(), queueSize: o.QueueSize, interval: o.Interval, client: client}
	if s.interval <= 0 {
		s.interval = DefaultInterval
	}

	return s, nil
}

// Endpoint is where rows are sent, with the stream fields in its query.
func (s *Sender) Endpoint() string {
	return s.endpoint
}

// Add queues one row, a JSON line ending in a newline, which the sender then owns. When the queue is full the oldest row is dropped to make room.
func (s *Sender) Add(row []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.queue = append(s.queue, row)
	s.trim()
}

// trim drops the oldest rows beyond the queue's size. The lock is held.
func (s *Sender) trim() {
	if over := len(s.queue) - s.queueSize; over > 0 {
		clear(s.queue[:over])
		s.queue = s.queue[over:]
		s.dropped.Add(uint64(over))
	}
}

// Stats are the counts so far.
func (s *Sender) Stats() Stats {
	s.mu.Lock()
	queued := len(s.queue)
	s.mu.Unlock()

	return Stats{Sent: s.sent.Load(), Failures: s.failures.Load(), Dropped: s.dropped.Load(), Queued: queued}
}

// Run sends what is held every interval until the context ends, then tries once more, for a short while, to send what is left.
func (s *Sender) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			s.finish(ctx)

			return
		case <-t.C:
			_ = s.Flush(ctx)
		}
	}
}

// finish tries, for a short while, to send what is still held once the sender has been stopped.
func (s *Sender) finish(ctx context.Context) {
	last, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalTimeout)
	defer cancel()

	if err := s.Flush(last); err != nil {
		clog.Log.Errorf("stopping with %d rows VictoriaLogs did not take: %v", s.Stats().Queued, err)
	}
}

// Flush sends everything held now, a batch at a time, and stops at the first batch VictoriaLogs does not take, which is held again to be tried
// next time.
func (s *Sender) Flush(ctx context.Context) error {
	for {
		batch := s.take()
		if len(batch) == 0 {
			return nil
		}

		err := s.post(ctx, batch)

		switch {
		case err == nil:
			s.sent.Add(uint64(len(batch)))
			s.recovered()
		case refused(err):
			// sending the same rows again would be refused again, and hold up every row behind them
			s.failures.Add(1)
			s.dropped.Add(uint64(len(batch)))
			clog.Log.Errorf("VictoriaLogs refused %d rows, which are dropped: %v", len(batch), err)
		default:
			s.failures.Add(1)
			s.giveBack(batch, err)

			return err
		}
	}
}

// take removes the oldest rows from the queue, as many as make one batch.
func (s *Sender) take() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	n, size := 0, 0
	for n < len(s.queue) && (n == 0 || size+len(s.queue[n]) <= maxBatchBytes) {
		size += len(s.queue[n])
		n++
	}

	batch := s.queue[:n:n]
	s.queue = s.queue[n:]

	return batch
}

// giveBack puts a batch that was not sent back in front of the rows that arrived meanwhile, and says so the first time in a row it happens.
func (s *Sender) giveBack(batch [][]byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.queue = append(batch, s.queue...)
	s.trim()

	if !s.down {
		s.down = true
		clog.Log.Errorf("VictoriaLogs did not take the rows, which are held to be sent again: %v", err)
	}
}

// recovered says VictoriaLogs takes rows again, once, after it had stopped.
func (s *Sender) recovered() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.down {
		s.down = false
		clog.Log.Warn("VictoriaLogs takes rows again")
	}
}

// refused is whether VictoriaLogs answered that it will not take what it was sent, as a bad request or one too large: the rows themselves are at
// fault, not its being there. A 404 is not one: the address is wrong, which holding the rows gives time to put right.
func refused(err error) bool {
	code := httpc.StatusCode(err)

	return code == http.StatusBadRequest || code == http.StatusRequestEntityTooLarge
}

// post sends one batch.
func (s *Sender) post(ctx context.Context, batch [][]byte) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(bytes.Join(batch, nil)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/stream+json")

	resp, answer, err := s.client.Fetch(req, maxAnswer) //nolint:bodyclose // Fetch hands the response back with its body read and closed
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &httpc.StatusError{Method: req.Method, Path: req.URL.Path, StatusCode: resp.StatusCode, Body: httpc.Preview(answer)}
	}

	return nil
}
