// Package relay is what happens to each line received: it is kept in the raw copy when there is one, turned into the row VictoriaLogs would
// store with its message filled in, and queued to be sent.
package relay

import (
	"net"
	"sync/atomic"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/syslog-relay/lib/convert"
	"github.com/katbyte/syslog-relay/lib/listen"
	"github.com/katbyte/syslog-relay/lib/vlogs"
)

// Queue is where rows go to be sent: a *vlogs.Sender.
type Queue interface {
	Add(row []byte)
	Stats() vlogs.Stats
}

// Raw keeps lines as they arrived: a *rawcopy.File.
type Raw interface {
	Write(line []byte)
}

// Relay handles the lines a listener receives.
type Relay struct {
	convert     convert.Options
	useRemoteIP bool
	raw         Raw
	queue       Queue

	// now is the time a line arrived, the clock unless a test says otherwise
	now func() time.Time

	udp, tcp, rejected, udpFailures, tcpFailures atomic.Uint64
}

// Options says how lines are read and recorded.
type Options struct {
	Convert convert.Options
	// UseRemoteIP records the sender's address in remote_ip, and as the hostname of a line that has none, as VictoriaLogs' -syslog.useRemoteIP.
	UseRemoteIP bool
	// Raw is where lines are kept as they arrived, nil for nowhere.
	Raw Raw
}

// New is a relay that queues its rows on q.
func New(o Options, q Queue) *Relay {
	return &Relay{convert: o.Convert, useRemoteIP: o.UseRemoteIP, raw: o.Raw, queue: q, now: time.Now}
}

// Handle is a listen.Handler: it takes one line.
func (r *Relay) Handle(protocol string, line []byte, from net.Addr) {
	arrived := r.now()

	switch protocol {
	case listen.UDP:
		r.udp.Add(1)
	case listen.TCP:
		r.tcp.Add(1)
	}

	if r.raw != nil {
		r.raw.Write(line)
	}

	var remoteIP string
	if r.useRemoteIP {
		remoteIP = listen.RemoteIP(from)
	}

	row, err := r.convert.Row(nil, string(line), arrived, remoteIP)
	if err != nil {
		r.rejected.Add(1)
		clog.Log.Warnf("dropping a line from %s, as VictoriaLogs would: %v", from, err)

		return
	}

	r.queue.Add(row)
}

// Failed is a listen.Failed: something sent could not be read.
func (r *Relay) Failed(protocol string, err error) {
	switch protocol {
	case listen.UDP:
		r.udpFailures.Add(1)
	case listen.TCP:
		r.tcpFailures.Add(1)
	}

	clog.Log.Warnf("reading syslog over %s: %v", protocol, err)
}

// Stats are the counts since the relay started.
type Stats struct {
	// Received is lines received, and ReadFailures what could not be read, by protocol.
	Received, ReadFailures map[string]uint64
	// Rejected is lines dropped because the time written in them could not be read, as VictoriaLogs drops them.
	Rejected uint64
	// Send is how sending them is going.
	Send vlogs.Stats
}

// Stats are the counts so far.
func (r *Relay) Stats() Stats {
	return Stats{
		Received:     map[string]uint64{listen.UDP: r.udp.Load(), listen.TCP: r.tcp.Load()},
		ReadFailures: map[string]uint64{listen.UDP: r.udpFailures.Load(), listen.TCP: r.tcpFailures.Load()},
		Rejected:     r.rejected.Load(),
		Send:         r.queue.Stats(),
	}
}
