# Changelog

## 0.1.1 (unreleased)

- **Lines received over UDP and over TCP are on the page from the start**, at 0 until one arrives, where neither was until a line came over it. An alert on UniFi going quiet now fires for a relay that was never sent anything.
- **The reading that says which version is running is `syslog_relay_build_info`**, where it was `syslog_relay_metrics_build_info`. A dashboard or alert that reads the old name needs the new one.
- Built on go-kt 0.9.0, which renamed the metrics page and HTTP client the relay uses.

## 0.1.0 (2026-10-13)

The first release: a relay between UniFi and VictoriaLogs that fills in the message VictoriaLogs leaves out of UniFi's events.

- **UniFi events get their message back.** UniFi sends its events in CEF format, which VictoriaLogs stores with no message: every one read `missing _msg field`. Each now reads as the event's own words, taken from its `msg`, or its name when it has none.
- **Everything else is stored as VictoriaLogs stores it.** Each line is read by VictoriaLogs' own parser, from v1.53.0, so the fields, their values and the streams they fall into are the same.
- **UniFi Protect's quotes can be taken off.** Protect puts double quotes around every message; `SYSLOG_RELAY_STRIP_QUOTES=true` stores it without them.
- **The same settings as VictoriaLogs.** The message fields, stream fields, time zone, local timestamps and sender's address are set the way VictoriaLogs' own syslog flags set them, so a setting can be carried across either way.
- **Syslog over UDP and TCP**, split into lines exactly as VictoriaLogs splits them, several lines to a packet included.
- **Nothing lost while VictoriaLogs is away.** Rows are held in memory and sent once it is back, up to a limit; past it the oldest are dropped and counted.
- **A raw copy of what arrives**, when asked for, to send real lines again later. It stops at a size you choose and only its owner can read it.
- **`parse` shows what a line becomes** without sending anything.
- **Its own readings and a health check** for Prometheus or Grafana Alloy: lines received, rows sent, send failures, rows dropped and rows waiting.
- Ships as a Docker image and ready-built programs for Linux.
