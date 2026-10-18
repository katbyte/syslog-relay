// Package readings writes the relay's counts as Prometheus readings, read fresh at each scrape.
package readings

import (
	"maps"
	"slices"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/syslog-relay/lib/listen"
	"github.com/katbyte/syslog-relay/lib/relay"
	"github.com/prometheus/client_golang/prometheus"
)

// namespace starts the name of every reading.
const namespace = "syslog_relay"

// list is every reading the page can hold.
var (
	list = metrics.NewList(namespace)

	received     = list.Reading("lines_received_total", "Syslog lines received.", "protocol")
	readFailures = list.Reading("read_failures_total", "Times what a sender sent could not be read as syslog: a line too long, or cut off.", "protocol")
	rejected     = list.Reading("lines_rejected_total", "Lines dropped because the time written in them could not be read, as VictoriaLogs drops them.")
	sent         = list.Reading("rows_sent_total", "Rows VictoriaLogs took.")
	failures     = list.Reading("send_failures_total", "Times VictoriaLogs could not be reached, or answered with a fault.")
	dropped      = list.Reading("rows_dropped_total", "Rows given up on: the oldest beyond what is held while VictoriaLogs cannot be reached, and any it refused.")
	queued       = list.Reading("rows_queued", "Rows waiting to be sent.")
)

// protocols are the ways syslog is taken. Each is on the page from the start, at 0 until a line comes over it, so a rate over it reads 0 rather
// than nothing.
var protocols = []string{listen.UDP, listen.TCP}

// Source is what the readings are read from: a *relay.Relay.
type Source interface {
	Stats() relay.Stats
}

type collector struct {
	source Source
}

// New is the collector of the source's readings.
func New(source Source) prometheus.Collector {
	return collector{source: source}
}

func (collector) Describe(ch chan<- *prometheus.Desc) { list.Describe(ch) }

func (c collector) Collect(ch chan<- prometheus.Metric) {
	s := c.source.Stats()
	w := metrics.NewWriter(ch)

	byProtocol(w, received, s.Received)
	byProtocol(w, readFailures, s.ReadFailures)

	w.Total(rejected, s.Rejected)
	w.Total(sent, s.Send.Sent)
	w.Total(failures, s.Send.Failures)
	w.Total(dropped, s.Send.Dropped)
	w.Gauge(queued, s.Send.Queued)
}

// byProtocol writes a count for each protocol: both of the relay's, and any other it turns out to count.
func byProtocol(w *metrics.Writer, d *prometheus.Desc, counts map[string]uint64) {
	for _, protocol := range protocols {
		w.Total(d, counts[protocol], protocol)
	}

	// the writer leaves off the ones already written
	for _, protocol := range slices.Sorted(maps.Keys(counts)) {
		w.Total(d, counts[protocol], protocol)
	}
}
