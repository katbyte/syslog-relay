// Package main implements syslog-relay, which takes syslog, fills in the message VictoriaLogs leaves out of UniFi's CEF events, and sends it on
// to VictoriaLogs.
package main

import (
	"os"

	"github.com/gookit/color"
	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/syslog-relay/cli"
)

func main() {
	// the log level comes from SYSLOG_RELAY_LOG; read it once here, before anything logs
	clog.SetLevelFromEnv("SYSLOG_RELAY_LOG")

	cmd, err := cli.Make()
	if err != nil {
		clog.Log.Error(color.Sprintf("<red>syslog-relay: building cmd</> %v", err))

		os.Exit(1)
	}

	if err := cmd.Execute(); err != nil {
		clog.Log.Error(color.Sprintf("<red>syslog-relay:</> %v", err))

		os.Exit(1)
	}

	os.Exit(0)
}
