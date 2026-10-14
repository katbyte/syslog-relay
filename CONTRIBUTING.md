# Contributing

Issues and pull requests are welcome.

- `make check-all` runs everything CI runs. See [development](docs/development.md).
- A line that becomes the wrong row is easiest to fix with the line itself: `syslog-relay parse` shows what it becomes.
- Take the names and the IP and MAC addresses out of a line before attaching it.

## Releases

- Changes collect under "Unreleased" in the [changelog](CHANGELOG.md).
- A version is tagged when a batch is worth upgrading for.
- Breaking changes are grouped into one release, not shipped one at a time.
