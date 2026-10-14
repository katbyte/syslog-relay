package listen

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// received is a line a handler was given.
type received struct {
	protocol, line, from string
}

// collector gathers what a listener hands on, for a test to wait for.
type collector struct {
	mu     sync.Mutex
	lines  []received
	faults []error
}

func (c *collector) handle(protocol string, line []byte, from net.Addr) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, received{protocol: protocol, line: string(line), from: RemoteIP(from)})
}

func (c *collector) failed(_ string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.faults = append(c.faults, err)
}

// wait waits for n lines and gives them back.
func (c *collector) wait(t *testing.T, n int) []received {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		got := append([]received(nil), c.lines...)
		c.mu.Unlock()

		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d lines, waited for %d: %v", len(got), n, got)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// serving is a listener on a port of its own, serving into a collector until the test ends; it gives back the collector and a stop that waits for
// Serve to return.
func serving(t *testing.T) (*Listener, *collector, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	l, err := Open(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	c := &collector{}
	done := make(chan struct{})
	go func() {
		l.Serve(ctx, c.handle, c.failed)
		close(done)
	}()

	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Serve did not return once its context ended")
		}
	}
	t.Cleanup(stop)

	return l, c, stop
}

func dial(t *testing.T, network string, addr net.Addr) net.Conn {
	t.Helper()

	var d net.Dialer
	c, err := d.DialContext(t.Context(), network, addr.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	return c
}

func TestUDP(t *testing.T) {
	t.Parallel()

	l, c, _ := serving(t)

	// one packet can hold several lines
	if _, err := dial(t, "udp", l.UDPAddr()).Write([]byte("Oct 13 nvr a: one\nOct 13 nvr a: two")); err != nil {
		t.Fatal(err)
	}

	got := c.wait(t, 2)
	want := []received{{UDP, "Oct 13 nvr a: one", "127.0.0.1"}, {UDP, "Oct 13 nvr a: two", "127.0.0.1"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestTCP(t *testing.T) {
	t.Parallel()

	l, c, _ := serving(t)

	conn := dial(t, "tcp", l.TCPAddr())
	if _, err := conn.Write([]byte("17 Oct 13 nvr a: one<14>Oct 13 nvr a: two\n")); err != nil {
		t.Fatal(err)
	}

	got := c.wait(t, 2)
	want := []received{{TCP, "Oct 13 nvr a: one", "127.0.0.1"}, {TCP, "<14>Oct 13 nvr a: two", "127.0.0.1"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A sender that sends what cannot be read is told of, and the listener goes on taking lines from others.
func TestTCPFault(t *testing.T) {
	t.Parallel()

	l, c, _ := serving(t)

	bad := dial(t, "tcp", l.TCPAddr())
	if _, err := bad.Write([]byte("12x Oct\n")); err != nil {
		t.Fatal(err)
	}

	if _, err := dial(t, "tcp", l.TCPAddr()).Write([]byte("Oct 13 nvr a: fine\n")); err != nil {
		t.Fatal(err)
	}
	c.wait(t, 1)

	deadline := time.Now().Add(10 * time.Second)
	for {
		c.mu.Lock()
		n := len(c.faults)
		c.mu.Unlock()

		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fault was never told of")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Stopping closes the connections still open, so a sender that never hangs up does not hold the program up.
func TestStopClosesConnections(t *testing.T) {
	t.Parallel()

	l, c, stop := serving(t)

	conn := dial(t, "tcp", l.TCPAddr())
	if _, err := conn.Write([]byte("Oct 13 nvr a: one\n")); err != nil {
		t.Fatal(err)
	}
	c.wait(t, 1)

	stop()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the connection is still open after the listener stopped: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.faults) != 0 {
		t.Errorf("stopping was told of as a fault of the sender's: %v", c.faults)
	}
}

func TestOpenTaken(t *testing.T) {
	t.Parallel()

	l, _, _ := serving(t)

	if _, err := Open(t.Context(), l.TCPAddr().String()); err == nil {
		t.Error("Open on a port already taken gave no error")
	}
}

func TestRemoteIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		from net.Addr
		want string
	}{
		{&net.UDPAddr{IP: net.IPv4(192, 0, 2, 11), Port: 514}, "192.0.2.11"},
		{&net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 514}, "[2001:db8::1]"},
		{nil, ""},
	}
	for _, tc := range tests {
		if got := RemoteIP(tc.from); got != tc.want {
			t.Errorf("RemoteIP(%v) = %q, want %q", tc.from, got, tc.want)
		}
	}
}
