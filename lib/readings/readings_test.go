package readings

import (
	"bytes"
	"testing"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/prometheus/metricstest"
	"github.com/katbyte/syslog-relay/lib/relay"
	"github.com/katbyte/syslog-relay/lib/vlogs"
)

type stats relay.Stats

func (s stats) Stats() relay.Stats { return relay.Stats(s) }

// pageOf is the page written for these counts.
func pageOf(t *testing.T, s stats) string {
	t.Helper()

	p, err := metrics.New(metrics.Options{Name: "syslog-relay"}, New(s))
	if err != nil {
		t.Fatal(err)
	}

	var page bytes.Buffer
	if err := p.Write(&page); err != nil {
		t.Fatal(err)
	}

	return page.String()
}

// The page is what dashboards and alerts are written against: go test -update rewrites testdata, to be read before it is kept.
func TestReadings(t *testing.T) {
	t.Parallel()

	page := pageOf(t, stats{
		Received:     map[string]uint64{"udp": 857, "tcp": 3},
		ReadFailures: map[string]uint64{"tcp": 1},
		Rejected:     2,
		Send:         vlogs.Stats{Sent: 850, Failures: 4, Dropped: 5, Queued: 3},
	})

	metricstest.Saved(t, page, "testdata/busy.prom")
	metricstest.Lint(t, page)

	// the saved page leaves out which build wrote it, so its name is checked here
	if n, ok := metricstest.One(t, page, "syslog_relay_build_info", nil); !ok || n != 1 {
		t.Errorf("syslog_relay_build_info = %v (on the page: %t), want 1", n, ok)
	}
}

// Before anything is received each protocol is still on the page, at 0, so an alert on the rate of lines fires for a relay never sent any.
func TestReadingsBeforeAnyLine(t *testing.T) {
	t.Parallel()

	page := pageOf(t, stats{})

	metricstest.Saved(t, page, "testdata/quiet.prom")
	for _, protocol := range []string{"udp", "tcp"} {
		if n, ok := metricstest.One(t, page, "syslog_relay_lines_received_total", map[string]string{"protocol": protocol}); !ok || n != 0 {
			t.Errorf("lines received over %s = %v (on the page: %t), want 0", protocol, n, ok)
		}
	}
}
