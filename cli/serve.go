package cli

import (
	"context"
	"os/signal"
	"sync"
	"syscall"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/syslog-relay/lib/listen"
	"github.com/katbyte/syslog-relay/lib/relay"
	"github.com/spf13/cobra"
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Take syslog and send it on to VictoriaLogs",
		Long: `Takes syslog on --listen, over both UDP and TCP, and sends every line on to the
VictoriaLogs at --logs-url as the row VictoriaLogs would store for it, read by its own
parser. The log message is taken from the first of the --msg-field fields that has a value:
the line's own message, then a CEF event's msg, then its name.

Rows are sent every second. While VictoriaLogs cannot be reached they are held in memory, up
to --queue-size, and the oldest beyond that are dropped and counted.

--use-local-timestamp, --use-remote-ip, --stream-fields and --timezone do what the
VictoriaLogs -syslog.* flags of the same names do: give them the values your VictoriaLogs
runs with, and a row from the relay holds the same fields as one VictoriaLogs stores itself.

--strip-quotes takes off the pair of double quotes UniFi Protect puts around every message.

--raw-file also keeps every line received, unchanged, to send again later, until it holds
--raw-max-mb. What UniFi sends names people and devices and holds IP and MAC addresses.

The relay's own readings are served at /metrics on --metrics-listen, with a health probe at
/healthz.

Every setting is also an environment variable: SYSLOG_RELAY_LISTEN, SYSLOG_RELAY_LOGS_URL,
SYSLOG_RELAY_MSG_FIELD, SYSLOG_RELAY_STREAM_FIELDS, SYSLOG_RELAY_USE_LOCAL_TIMESTAMP,
SYSLOG_RELAY_USE_REMOTE_IP, SYSLOG_RELAY_STRIP_QUOTES, SYSLOG_RELAY_TIMEZONE, SYSLOG_RELAY_QUEUE_SIZE,
SYSLOG_RELAY_RAW_FILE, SYSLOG_RELAY_RAW_MAX_MB, SYSLOG_RELAY_METRICS_LISTEN and
SYSLOG_RELAY_LABELS.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			f := GetFlags()
			conv, err := f.convert()
			if err != nil {
				return err
			}

			sender, err := f.sender()
			if err != nil {
				return err
			}

			opts := relay.Options{Convert: conv, UseRemoteIP: f.UseRemoteIP}
			raw, err := f.raw()
			if err != nil {
				return err
			}
			// set only when there is one: a nil file in the interface would not read as none
			if raw != nil {
				defer func() { _ = raw.Close() }()
				opts.Raw = raw
			}

			r := relay.New(opts, sender)
			page, err := f.page(r)
			if err != nil {
				return err
			}

			// one signal stops the listening, the sending and the serving
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			l, err := listen.Open(ctx, f.Listen)
			if err != nil {
				return err
			}
			clog.Log.Infof("taking syslog on %s over UDP and TCP, and sending it to %s", f.Listen, sender.Endpoint())

			// the sender stops after the listener, so its last send holds every line received
			sendCtx, stopSending := context.WithCancel(context.WithoutCancel(ctx))
			defer stopSending()

			var sending, listening sync.WaitGroup
			sending.Go(func() { sender.Run(sendCtx) })
			listening.Go(func() { l.Serve(ctx, r.Handle, r.Failed) })

			err = metrics.Serve(ctx, f.MetricsListen, page.Handler())
			// serving can end by itself, on a port already taken say, and the rest has to end with it
			stop()
			listening.Wait()
			stopSending()
			sending.Wait()

			return err
		},
	}
}
