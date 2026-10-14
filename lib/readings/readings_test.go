package readings

import (
	"bytes"
	"strings"
	"testing"

	"github.com/katbyte/go-kt/pmetrics"
	"github.com/katbyte/syslog-relay/lib/relay"
	"github.com/katbyte/syslog-relay/lib/vlogs"
)

type stats relay.Stats

func (s stats) Stats() relay.Stats { return relay.Stats(s) }

func TestReadings(t *testing.T) {
	t.Parallel()

	s := stats{
		Received:     map[string]uint64{"udp": 857, "tcp": 3},
		ReadFailures: map[string]uint64{"udp": 0, "tcp": 1},
		Rejected:     2,
		Send:         vlogs.Stats{Sent: 850, Failures: 4, Dropped: 5, Queued: 3},
	}

	p, err := pmetrics.New(pmetrics.Options{Name: "syslog-relay", Namespace: Namespace}, New(s))
	if err != nil {
		t.Fatal(err)
	}

	var page bytes.Buffer
	if err := p.Write(&page); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`syslog_relay_lines_received_total{protocol="tcp"} 3`,
		`syslog_relay_lines_received_total{protocol="udp"} 857`,
		`syslog_relay_read_failures_total{protocol="tcp"} 1`,
		`syslog_relay_read_failures_total{protocol="udp"} 0`,
		`syslog_relay_lines_rejected_total 2`,
		`syslog_relay_rows_sent_total 850`,
		`syslog_relay_send_failures_total 4`,
		`syslog_relay_rows_dropped_total 5`,
		`syslog_relay_rows_queued 3`,
		`syslog_relay_metrics_build_info{`,
	} {
		if !strings.Contains(page.String(), want) {
			t.Errorf("the page has no %s:\n%s", want, page.String())
		}
	}
}
