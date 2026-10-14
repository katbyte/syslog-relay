// Package convert turns a syslog line into the row VictoriaLogs would store for it, with its message taken from the field it is told. The fields
// come from VictoriaLogs' own parser, so they are the ones it stores today; only where _msg comes from differs.
package convert

import (
	"fmt"
	"time"

	"github.com/VictoriaMetrics/VictoriaLogs/lib/logstorage"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/timeutil"
)

// DefaultStreamFields is VictoriaLogs' own list of the fields that tell one syslog stream from another, used when it is given none.
var DefaultStreamFields = []string{"hostname", "app_name", "proc_id", "cef.device_vendor", "cef.device_product", "cef.device_event_class_id"}

// DefaultMsgFields is where the message is taken from unless told otherwise: the line's own message, then a CEF event's msg, then its name.
var DefaultMsgFields = []string{"message", "cef.extension.msg", "cef.name"}

// Options says how a line is read. All but StripQuotes match the VictoriaLogs -syslog.* flag of the same name, so a setting can be carried across
// as it is.
type Options struct {
	// MsgFields are the fields the message is taken from: the first with a value is stored as _msg in place of its own name.
	MsgFields []string
	// UseLocalTimestamp stores the time a line arrived in place of the time written in it, which is then kept as an ordinary field.
	UseLocalTimestamp bool
	// Timezone is the zone a time written without one is read in; nil is the machine's own.
	Timezone *time.Location
	// StripQuotes takes off a pair of double quotes around the whole message, which UniFi Protect puts around every one. VictoriaLogs has no
	// such setting: CEF says a value is taken as it is written.
	StripQuotes bool
}

// Row appends to dst the JSON line VictoriaLogs is sent for one syslog line that arrived at that time from that address, "" when the sender is not
// to be recorded. A line whose own time cannot be read is an error, as it is to VictoriaLogs, which drops it.
func (o Options) Row(dst []byte, line string, arrived time.Time, remoteIP string) ([]byte, error) {
	tz := o.Timezone
	if tz == nil {
		tz = time.Local
	}

	p := logstorage.GetSyslogParser(arrived.In(tz).Year(), tz)
	defer logstorage.PutSyslogParser(p)

	p.Parse(line)

	atNano := arrived.UnixNano()
	if !o.UseLocalTimestamp {
		written, err := writtenTime(p.Fields, atNano)
		if err != nil {
			return dst, fmt.Errorf("reading the time written in %q: %w", line, err)
		}
		atNano = written
	}

	// the sender's address, and the hostname when the line has none, as VictoriaLogs records them with -syslog.useRemoteIP
	if remoteIP != "" {
		p.AddField("remote_ip", remoteIP)
		if !hasValue(p.Fields, "hostname") {
			p.AddField("hostname", remoteIP)
		}
	}

	logstorage.RenameField(p.Fields, o.MsgFields, "_msg")
	if o.StripQuotes {
		stripQuotes(p.Fields)
	}

	fields := make([]logstorage.Field, 0, len(p.Fields)+1)
	fields = append(fields, logstorage.Field{Name: "_time", Value: time.Unix(0, atNano).UTC().Format(time.RFC3339Nano)})
	fields = append(fields, p.Fields...)

	dst = logstorage.MarshalFieldsToJSON(dst, fields)

	return append(dst, '\n'), nil
}

// writtenTime is the time written in the line, read the way VictoriaLogs reads it: from the timestamp field, which is then left out of the row. A
// line with no time, or a nil one, is given the time it arrived.
func writtenTime(fields []logstorage.Field, arrived int64) (int64, error) {
	for i := range fields {
		f := &fields[i]
		if f.Name != "timestamp" {
			continue
		}

		s := f.Value
		f.Value = ""

		switch {
		case s == "" || s == "0" || s == "-":
			return arrived, nil
		case len(s) <= len("YYYY") || s[len("YYYY")] != '-':
			nsecs, ok := timeutil.TryParseUnixTimestamp(s)
			if !ok {
				return 0, fmt.Errorf("%q is not a time", s)
			}

			return nonZero(nsecs, arrived), nil
		default:
			nsecs, ok := logstorage.TryParseTimestampRFC3339Nano(s)
			if !ok {
				return 0, fmt.Errorf("%q is not a time", s)
			}

			return nonZero(nsecs, arrived), nil
		}
	}

	return arrived, nil
}

// nonZero is the time, or the time the line arrived when it is the very start of 1970, which VictoriaLogs takes to mean none was given.
func nonZero(nsecs, arrived int64) int64 {
	if nsecs == 0 {
		return arrived
	}

	return nsecs
}

// stripQuotes takes off the double quotes at both ends of the message. A message that is only a pair of quotes is left as it is, so it is not
// stored as having none.
func stripQuotes(fields []logstorage.Field) {
	for i := range fields {
		if f := &fields[i]; f.Name == "_msg" {
			if len(f.Value) > 2 && f.Value[0] == '"' && f.Value[len(f.Value)-1] == '"' {
				f.Value = f.Value[1 : len(f.Value)-1]
			}

			return
		}
	}
}

// hasValue is whether the fields hold one of that name with a value.
func hasValue(fields []logstorage.Field, name string) bool {
	for _, f := range fields {
		if f.Name == name && f.Value != "" {
			return true
		}
	}

	return false
}
