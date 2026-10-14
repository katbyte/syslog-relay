//go:build acceptance

// Package acceptance_test runs the image a deployment runs, as docker runs it, beside two stock VictoriaLogs of the pinned version. One is sent
// UniFi's lines straight, run with the settings a real one runs with; the other is sent them through the relay. Each row the relay sends has to
// hold exactly what VictoriaLogs stores by itself, but for its message.
//
// None of it needs a UniFi console: the lines are the ones in lib/convert/testdata, shaped like what UniFi sends and made up.
//
//	make testacc                                                # builds the image and runs these against it
//	SYSLOG_RELAY_TEST_PROGRAM_IMAGE=syslog-relay make test-live # or against one already built
package acceptance_test
