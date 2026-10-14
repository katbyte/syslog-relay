# Development

```bash
make            # format and build
make test       # the tests, under the race detector
make testacc    # the live tests: the image, beside VictoriaLogs
make cover      # the tests with coverage
make check-all  # build, tests, live tests, every linter and the dependency check
make tools      # install the pinned dev tools into .tools/bin
```

Dependencies are vendored. The dev tools are pinned in `.tools/go.mod` and the makefile.

## Where things are

| Path | What it does |
|---|---|
| `lib/listen` | takes syslog over UDP and TCP and splits it into lines |
| `lib/convert` | turns a line into the row VictoriaLogs would store, with its message |
| `lib/vlogs` | sends rows to VictoriaLogs, holding them while it is away |
| `lib/rawcopy` | keeps the lines as they arrived |
| `lib/relay` | what happens to each line, and the counts |
| `lib/readings` | writes the counts as readings |
| `cli` | the commands and settings |
| `acceptance` | the live tests of the image |

The readings page, `/metrics`, `/healthz` and the labels are [go-kt](https://github.com/katbyte/go-kt)'s `pmetrics`, shared with the other tools that serve one. Rows go to VictoriaLogs through go-kt's `chttp`, so `SYSLOG_RELAY_LOG=TRACE` shows each request and answer.

## The pinned VictoriaLogs

The parser is VictoriaLogs' own, from `github.com/VictoriaMetrics/VictoriaLogs/lib/logstorage`, pinned to the version it was written against: v1.53.0.

- Go's module mirror holds an old copy under that tag, from 2021, made before the repository was VictoriaLogs. `go get ...@v1.53.0` fetches that copy and fails.
- So `go.mod` names v1.53.0's commit by a version made from it, `v1.52.2-0.20261001104708-915d91904bf7`.
- To move to another release, name its commit the same way: `go get github.com/VictoriaMetrics/VictoriaLogs@<commit>` usually works, unless the tag is one the mirror has the old copy of.
- How VictoriaLogs splits a stream into lines is not in a package that can be brought in on its own, so it is copied into [`lib/listen/lines.go`](../lib/listen/lines.go) with its licence notice. Check it against the new version when moving.
- Importing the parser brings in VictoriaLogs' command-line flags, which go unused. Settings come from the environment, so they never get in the way.

## The tests

`make test` needs no UniFi console and no docker.

- The lines are made up, shaped like real events from UniFi Protect, Network and OS. They are in [`unifi.log`](../lib/convert/testdata/unifi.log), with made-up names and addresses.
- The rows they become are kept in [`unifi.jsonl`](../lib/convert/testdata/unifi.jsonl), so a change to any field shows up as a difference. `go test ./lib/convert -update` rewrites it once the change is meant.
- A made-up VictoriaLogs takes the rows in the tests of `serve` and of the sender. It can be made to answer with a fault, or not at all.

## The live tests

`make testacc` builds the image and starts it beside two stock VictoriaLogs of the pinned version. It needs `docker` and takes about half a minute.

- One VictoriaLogs is sent the test lines straight, run with local timestamps, the sender's address and a time zone, as a real one is. The other is sent them through the relay. Every row has to match field for field, stream and all, apart from the message.
- The message has to be the event's own `msg`, where VictoriaLogs alone stored `missing _msg field`.
- The image passes its own health check, does not run as root, and takes lines over TCP as well as UDP.
- VictoriaLogs is stopped while lines arrive. The rows are held, and reach it once it is back.
- The lines are sent over UDP from a container on the test's own network. A Mac's docker runs in a VM, which forwards only TCP to the Mac.
- `SYSLOG_RELAY_TEST_LOGS_IMAGE` names another VictoriaLogs image to test beside.
