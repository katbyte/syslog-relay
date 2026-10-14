# syslog-relay

[![GitHub release](https://img.shields.io/github/v/release/katbyte/syslog-relay?color=blueviolet)](https://github.com/katbyte/syslog-relay/releases/latest)
[![Go Version](https://img.shields.io/github/go-mod/go-version/katbyte/syslog-relay?color=00ADD8)](https://github.com/katbyte/syslog-relay/blob/main/go.mod)
[![License](https://img.shields.io/github/license/katbyte/syslog-relay?color=blue)](https://github.com/katbyte/syslog-relay/blob/main/LICENSE)
![build](https://github.com/katbyte/syslog-relay/actions/workflows/build.yaml/badge.svg)
![tests](https://github.com/katbyte/syslog-relay/actions/workflows/pr-tests.yaml/badge.svg)
![acceptance](https://github.com/katbyte/syslog-relay/actions/workflows/pr-acceptance.yaml/badge.svg)
![lint](https://github.com/katbyte/syslog-relay/actions/workflows/pr-golangci-lint.yaml/badge.svg)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/katbyte/syslog-relay/badges/coverage.json)](https://github.com/katbyte/syslog-relay/actions/workflows/coverage.yaml)

A small program that sits between UniFi and [VictoriaLogs](https://docs.victoriametrics.com/victorialogs/). It takes UniFi's syslog and passes it on, with each event's message filled in.

**Why.** UniFi sends its events as syslog in CEF format. VictoriaLogs splits a CEF line into fields but stores no message, so every UniFi event reads `missing _msg field` and the event's own words sit in a field of their own. This relay puts them back where the message goes. It is a stopgap until VictoriaLogs can be told where to take the message from ([the change](https://github.com/katbyte/VictoriaLogs/tree/syslog-msg-field)); then it can be retired.

**How it behaves.** Each line is read by VictoriaLogs' own parser, from the version it is pinned to, v1.53.0, so the stored fields are the ones VictoriaLogs stores by itself. Only the message differs. While VictoriaLogs cannot be reached, rows are held in memory up to a limit, and the oldest past it are dropped and counted.

Before and after, for one UniFi Protect event, with `SYSLOG_RELAY_STRIP_QUOTES=true`:

```diff
- _msg: missing _msg field; see https://docs.victoriametrics.com/victorialogs/keyconcepts/#message-field
- cef.extension.msg: "garage has recorded motion."
+ _msg: garage has recorded motion.
```

**UniFi Protect's quotes.** UniFi Protect puts double quotes around every message. VictoriaLogs keeps them, since CEF says a value is taken as it is written. `SYSLOG_RELAY_STRIP_QUOTES=true` takes them off. VictoriaLogs has no such setting, so this is a reason to keep the relay once VictoriaLogs can choose the message field itself.

## Install

```bash
docker pull ghcr.io/katbyte/syslog-relay:latest
```

or `go install github.com/katbyte/syslog-relay@latest`, or a ready-built program from the [releases](https://github.com/katbyte/syslog-relay/releases/latest).

## Quick start

[`docker-compose.yml`](docker-compose.yml) runs the released image beside VictoriaLogs. Set `SYSLOG_RELAY_LOGS_URL` to VictoriaLogs' address, then:

```bash
docker compose up -d
curl -s http://127.0.0.1:9124/metrics
```

Then point UniFi's SIEM server setting at the relay's host, port 1514, UDP: UniFi Network's activity logging, and the SIEM setting of each UniFi OS console.

To see what a line becomes without sending anything:

```bash
echo 'Oct 13 18:58:31 nvr CEF:0|Ubiquiti|UniFi Protect|7.3.70|2159|motion|3|msg=garage has recorded motion.' | syslog-relay parse
```

## Matching your VictoriaLogs

For the rows to hold what your VictoriaLogs stores by itself, give the relay the same settings that VictoriaLogs runs with. Each one below does what the VictoriaLogs flag of the same name does.

| VictoriaLogs flag | Relay setting |
|---|---|
| `-syslog.useLocalTimestamp` | `SYSLOG_RELAY_USE_LOCAL_TIMESTAMP=true` |
| `-syslog.useRemoteIP` | `SYSLOG_RELAY_USE_REMOTE_IP=true` |
| `-syslog.streamFields` | `SYSLOG_RELAY_STREAM_FIELDS` |
| `-syslog.timezone` | `SYSLOG_RELAY_TIMEZONE`, or `TZ` for the container |
| `-syslog.msgField` ([the change](https://github.com/katbyte/VictoriaLogs/tree/syslog-msg-field)) | `SYSLOG_RELAY_MSG_FIELD` |

`SYSLOG_RELAY_STRIP_QUOTES` has no VictoriaLogs flag to match: leave it off for rows that are the same as VictoriaLogs' own.

A list of fields can be written with commas, `message,cef.extension.msg`, or as the JSON list VictoriaLogs takes, `["message","cef.extension.msg"]`.

UniFi OS events, from both the UNVR and the Cloud Key, write their time in UTC while UniFi Protect and Network write local time. Read in a local time zone, UniFi OS events land hours out. `SYSLOG_RELAY_USE_LOCAL_TIMESTAMP=true` stores the time each line arrived instead, which is right for all of them.

## Settings

Only `SYSLOG_RELAY_LOGS_URL` has to be given. Each can be a flag, an environment variable, or a line in a settings file.

| Variable | Flag | Default | What it sets |
|---|---|---|---|
| `SYSLOG_RELAY_LOGS_URL` | `--logs-url` | | VictoriaLogs' address, `http://10.0.0.2:9428` |
| `SYSLOG_RELAY_LISTEN` | `--listen` | `:1514` | where syslog is taken, over UDP and TCP |
| `SYSLOG_RELAY_MSG_FIELD` | `--msg-field` | `message,cef.extension.msg,cef.name` | where the message is taken from: the first of these with a value |
| `SYSLOG_RELAY_STREAM_FIELDS` | `--stream-fields` | VictoriaLogs' own | the fields that tell one stream from another |
| `SYSLOG_RELAY_USE_LOCAL_TIMESTAMP` | `--use-local-timestamp` | `false` | store the time a line arrived, keeping the one written in it as `timestamp` |
| `SYSLOG_RELAY_USE_REMOTE_IP` | `--use-remote-ip` | `false` | record the sender's address in `remote_ip` |
| `SYSLOG_RELAY_STRIP_QUOTES` | `--strip-quotes` | `false` | take off the double quotes around a message, which UniFi Protect puts around every one |
| `SYSLOG_RELAY_TIMEZONE` | `--timezone` | `Local` | the zone a time written without one is read in |
| `SYSLOG_RELAY_QUEUE_SIZE` | `--queue-size` | `100000` | rows held while VictoriaLogs cannot be reached |
| `SYSLOG_RELAY_RAW_FILE` | `--raw-file` | none | also keep every line received, unchanged, in this file |
| `SYSLOG_RELAY_RAW_MAX_MB` | `--raw-max-mb` | `100` | stop adding to the raw copy at this size |
| `SYSLOG_RELAY_METRICS_LISTEN` | `--metrics-listen` | `:9124` | where the relay's own readings and health check are served |
| `SYSLOG_RELAY_LABELS` | `--labels` | none | labels for the relay's own readings, as `name=value,name=value` |
| `SYSLOG_RELAY_LOG` | | `WARN` | log level: `INFO` or `DEBUG` say more |
| `SYSLOG_RELAY_CONFIG` | `--config` | | the one settings file to read |

The settings file is `./.syslog-relay` or `~/.syslog-relay`. Its keys are the variable names without `SYSLOG_RELAY_`:

```env
LOGS_URL=http://10.0.0.2:9428
MSG_FIELD=message,cef.extension.msg
```

A flag or an environment variable wins over the file.

## Its own readings

Served at `/metrics` on port 9124, with a health check at `/healthz`. Every reading starts `syslog_relay_`.

| Reading | Labels | What it says |
|---|---|---|
| `syslog_relay_lines_received_total` | `protocol` | syslog lines received, over `udp` or `tcp` |
| `syslog_relay_read_failures_total` | `protocol` | times what was sent could not be read as syslog: a line too long, or cut off |
| `syslog_relay_lines_rejected_total` | | lines dropped because the time written in them could not be read, as VictoriaLogs drops them |
| `syslog_relay_rows_sent_total` | | rows VictoriaLogs took |
| `syslog_relay_send_failures_total` | | times VictoriaLogs could not be reached, or answered with a fault |
| `syslog_relay_rows_dropped_total` | | rows given up on: the oldest past `SYSLOG_RELAY_QUEUE_SIZE`, and any VictoriaLogs refused as bad |
| `syslog_relay_rows_queued` | | rows waiting to be sent |
| `syslog_relay_metrics_build_info` | `version`, `commit`, `goversion` | always 1; the version of the relay |

A few things to ask of them:

```promql
increase(syslog_relay_rows_dropped_total[1h]) > 0   # rows were lost
syslog_relay_rows_queued > 1000                     # VictoriaLogs is not taking rows
rate(syslog_relay_lines_received_total[15m]) == 0   # UniFi has stopped sending
```

## A raw copy, to send again

`SYSLOG_RELAY_RAW_FILE` keeps every line received, unchanged, one to a line, until the file reaches `SYSLOG_RELAY_RAW_MAX_MB`. It is there to send real lines again later: to a stock VictoriaLogs and to a changed one, say, to compare the two.

- What UniFi sends names people and devices and holds their IP and MAC addresses. The file can only be read by its owner. Never commit it.
- In a container, the folder it is written to must be writable by uid 65532.
- It is added to, not replaced, when the relay restarts. It stops at its size, at the end of a line.

To send it again, one line to a packet, the way UniFi sends them, to a syslog port at `host` and `port`:

```bash
while IFS= read -r line; do printf '%s' "$line" | nc -u -w0 "$host" "$port"; done < unifi.raw
```

## How this is built

- **Tested against VictoriaLogs itself.** The live tests start the image beside two stock VictoriaLogs of the pinned version. One is sent UniFi-shaped lines straight; the other through the relay. Every row has to match field for field, apart from the message. CI fails if they don't.
- **The parts are tested alone too.** Splitting lines, holding and dropping rows while VictoriaLogs is down, the raw copy's size, and every setting.
- **What no test reaches.** A real UniFi console. The test lines are made up, shaped like real events from UniFi Protect, Network and OS. A raw copy of real ones is the way to check more.
- **Checks CI runs.** Unit tests under the race detector, and the live tests. golangci-lint, CodeQL, govulncheck and a scan of the built program. Linters for the workflows, shell scripts, YAML, markdown and spelling.

## More

- [Development, tests and the pinned VictoriaLogs](docs/development.md)
- [Contributing and releases](CONTRIBUTING.md)
