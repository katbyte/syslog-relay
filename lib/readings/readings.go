// Package readings writes the relay's counts as Prometheus readings, read fresh at each scrape.
package readings

import (
	"maps"
	"slices"

	"github.com/katbyte/syslog-relay/lib/relay"
	"github.com/prometheus/client_golang/prometheus"
)

// Namespace starts the name of every reading.
const Namespace = "syslog_relay"

// Source is what the readings are read from: a *relay.Relay.
type Source interface {
	Stats() relay.Stats
}

type collector struct {
	source Source

	received, readFailures, rejected, sent, failures, dropped, queued *prometheus.Desc
}

// New is the collector of the source's readings.
func New(source Source) prometheus.Collector {
	desc := func(name, help string, labels ...string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(Namespace, "", name), help, labels, nil)
	}

	return &collector{
		source:       source,
		received:     desc("lines_received_total", "Syslog lines received.", "protocol"),
		readFailures: desc("read_failures_total", "Times what a sender sent could not be read as syslog: a line too long, or cut off.", "protocol"),
		rejected:     desc("lines_rejected_total", "Lines dropped because the time written in them could not be read, as VictoriaLogs drops them."),
		sent:         desc("rows_sent_total", "Rows VictoriaLogs took."),
		failures:     desc("send_failures_total", "Times VictoriaLogs could not be reached, or answered with a fault."),
		dropped:      desc("rows_dropped_total", "Rows given up on: the oldest beyond what is held while VictoriaLogs cannot be reached, and any it refused."),
		queued:       desc("rows_queued", "Rows waiting to be sent."),
	}
}

func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.received, c.readFailures, c.rejected, c.sent, c.failures, c.dropped, c.queued} {
		ch <- d
	}
}

func (c *collector) Collect(ch chan<- prometheus.Metric) {
	s := c.source.Stats()

	for _, protocol := range slices.Sorted(maps.Keys(s.Received)) {
		ch <- prometheus.MustNewConstMetric(c.received, prometheus.CounterValue, float64(s.Received[protocol]), protocol)
	}
	for _, protocol := range slices.Sorted(maps.Keys(s.ReadFailures)) {
		ch <- prometheus.MustNewConstMetric(c.readFailures, prometheus.CounterValue, float64(s.ReadFailures[protocol]), protocol)
	}

	ch <- prometheus.MustNewConstMetric(c.rejected, prometheus.CounterValue, float64(s.Rejected))
	ch <- prometheus.MustNewConstMetric(c.sent, prometheus.CounterValue, float64(s.Send.Sent))
	ch <- prometheus.MustNewConstMetric(c.failures, prometheus.CounterValue, float64(s.Send.Failures))
	ch <- prometheus.MustNewConstMetric(c.dropped, prometheus.CounterValue, float64(s.Send.Dropped))
	ch <- prometheus.MustNewConstMetric(c.queued, prometheus.GaugeValue, float64(s.Send.Queued))
}
