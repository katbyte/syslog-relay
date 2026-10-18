// Package cli implements the syslog-relay command line interface: the cobra commands, flag and config handling, and the relay they run.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/katbyte/go-kt/version"
	"github.com/katbyte/syslog-relay/lib/listen"
	"github.com/spf13/cobra"
)

func Make() (*cobra.Command, error) {
	root := &cobra.Command{
		Use:   toolName + " [command]",
		Short: toolName + " takes syslog, fills in the message VictoriaLogs leaves out of UniFi's CEF events, and sends it on to VictoriaLogs",
		Long: `Takes syslog over UDP and TCP and sends each line on to VictoriaLogs as the row it would
store, read by VictoriaLogs' own parser, with one difference: the log message is taken from
the field you choose. UniFi sends its events as CEF, which VictoriaLogs splits into fields
and stores with no message, so every one reads "missing _msg field". Here they read as the
event's own msg.
Complete documentation is available at https://github.com/katbyte/syslog-relay`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Run \""+toolName+" help\" for more information about available "+toolName+" commands.")
			return nil
		},
	}

	root.AddCommand(&cobra.Command{
		Use:           "version",
		Short:         "Print the version number of " + toolName,
		Long:          `Print the version number of ` + toolName,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), toolName+" "+version.Version)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "parse [file...]",
		Short: "Print the rows syslog lines become, without sending them",
		Long: `Reads syslog lines from the files named, or else from what is piped in, and prints the
JSON line VictoriaLogs would be sent for each, with the settings given, without sending
anything: a look at what a line becomes. Lines are split as they are over TCP. No sender's
address is known, so --use-remote-ip has nothing to record and --site-networks no site to
name.`,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			conv, err := GetFlags().convert()
			if err != nil {
				return err
			}

			var rejected int
			rows := func(r io.Reader) error {
				return listen.Lines(r, func(line []byte) {
					row, err := conv.Row(nil, string(line), time.Now(), "")
					if err != nil {
						rejected++
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), err)

						return
					}
					_, _ = cmd.OutOrStdout().Write(row)
				})
			}

			if len(args) == 0 {
				if err := rows(cmd.InOrStdin()); err != nil {
					return err
				}
			}
			for _, path := range args {
				if err := rowsOf(path, rows); err != nil {
					return err
				}
			}

			if rejected > 0 {
				return fmt.Errorf("%d lines were dropped, as VictoriaLogs would drop them", rejected)
			}

			return nil
		},
	})

	root.AddCommand(serveCmd())

	if err := configureFlags(root); err != nil {
		return nil, fmt.Errorf("unable to configure flags: %w", err)
	}
	// the settings file is read once the flags are parsed, since a flag can name it; before any command's own checks, which read what it holds
	root.PersistentPreRunE = func(*cobra.Command, []string) error { return loadConfig() }

	return root, nil
}

// rowsOf hands the file at path to rows.
func rowsOf(path string, rows func(io.Reader) error) error {
	f, err := os.Open(path) //nolint:gosec // the path is one the command was given
	if err != nil {
		return err
	}

	err = rows(f)

	return errors.Join(err, f.Close())
}
