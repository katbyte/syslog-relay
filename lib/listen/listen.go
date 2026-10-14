// Package listen takes syslog over UDP and TCP on one address and hands each line to whoever asked, split into lines as VictoriaLogs splits them.
package listen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// The protocols a line can arrive over, as handed to a Handler.
const (
	UDP = "udp"
	TCP = "tcp"
)

// udpBuffer is how much the kernel holds of what arrives over UDP before it is read, so a burst from a console is not lost while one line is sent
// on.
const udpBuffer = 4 * 1024 * 1024

// Handler is given each line received, the protocol it came over and who sent it. The line is only good until it returns.
type Handler func(protocol string, line []byte, from net.Addr)

// Failed is told of what went wrong reading from a sender: a line too long, a frame cut off. Reading carries on.
type Failed func(protocol string, err error)

// Listener takes syslog on one address over both UDP and TCP.
type Listener struct {
	udp net.PacketConn
	tcp net.Listener
}

// Open starts listening on addr over UDP and TCP. Nothing is read until Serve.
func Open(ctx context.Context, addr string) (*Listener, error) {
	var lc net.ListenConfig

	tcp, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listening for syslog over TCP on %s: %w", addr, err)
	}

	udp, err := lc.ListenPacket(ctx, "udp", addr)
	if err != nil {
		_ = tcp.Close()
		return nil, fmt.Errorf("listening for syslog over UDP on %s: %w", addr, err)
	}

	// a buffer the kernel will not grant is no reason not to run: the default is still there
	if conn, ok := udp.(*net.UDPConn); ok {
		_ = conn.SetReadBuffer(udpBuffer)
	}

	return &Listener{udp: udp, tcp: tcp}, nil
}

// UDPAddr is where it listens over UDP, with the port filled in when it was given as 0.
func (l *Listener) UDPAddr() net.Addr {
	return l.udp.LocalAddr()
}

// TCPAddr is where it listens over TCP, with the port filled in when it was given as 0.
func (l *Listener) TCPAddr() net.Addr {
	return l.tcp.Addr()
}

// Serve hands every line received to handle until the context ends, then stops listening, closes every connection and returns.
func (l *Listener) Serve(ctx context.Context, handle Handler, failed Failed) {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		conns  = map[net.Conn]struct{}{}
		closed bool
	)

	wg.Go(func() {
		<-ctx.Done()
		_ = l.udp.Close()
		_ = l.tcp.Close()

		mu.Lock()
		closed = true
		for c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})

	wg.Go(func() { l.serveUDP(ctx, handle, failed) })

	wg.Go(func() {
		for {
			c, err := l.tcp.Accept()
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
					return
				}
				failed(TCP, err)
				pause(ctx)

				continue
			}

			mu.Lock()
			if closed {
				mu.Unlock()
				_ = c.Close()

				return
			}
			conns[c] = struct{}{}
			mu.Unlock()

			wg.Go(func() {
				serveConn(c, handle, failed)

				mu.Lock()
				delete(conns, c)
				mu.Unlock()
				_ = c.Close()
			})
		}
	})

	wg.Wait()
}

// serveUDP reads one packet at a time; a packet can hold several lines.
func (l *Listener) serveUDP(ctx context.Context, handle Handler, failed Failed) {
	buf := make([]byte, 64*1024)
	var packet bytes.Reader
	lr := newLineReader(&packet)

	for {
		n, from, err := l.udp.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			failed(UDP, err)
			pause(ctx)

			continue
		}

		packet.Reset(buf[:n])
		lr.reset(&packet)
		for lr.next() {
			handle(UDP, lr.line, from)
		}
		if err := lr.Err(); err != nil {
			failed(UDP, fmt.Errorf("from %s: %w", from, err))
		}
	}
}

// serveConn reads one sender's TCP connection until it ends or sends something that cannot be read as syslog.
func serveConn(c net.Conn, handle Handler, failed Failed) {
	from := c.RemoteAddr()
	lr := newLineReader(c)
	for lr.next() {
		handle(TCP, lr.line, from)
	}

	// a connection closed under it at shutdown is not a fault of the sender's
	if err := lr.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		failed(TCP, fmt.Errorf("from %s: %w", from, err))
	}
}

// pause waits a moment after a fault in listening itself, so one that repeats does not spin.
func pause(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
	}
}

// RemoteIP is the sender's address without its port, written as VictoriaLogs writes it in remote_ip.
func RemoteIP(from net.Addr) string {
	if from == nil {
		return ""
	}

	ip, _, found := strings.CutLast(from.String(), ":")
	if !found {
		return ""
	}

	return ip
}
