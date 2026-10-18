// Package convert turns a syslog line into the row VictoriaLogs would store for it, with its message taken from the field it is told. The fields
// come from VictoriaLogs' own parser, so they are the ones it stores today; only where _msg comes from differs.
package convert

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/VictoriaMetrics/VictoriaLogs/lib/logstorage"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/timeutil"
	"github.com/katbyte/syslog-relay/lib/sites"
)

// DefaultStreamFields is VictoriaLogs' own list of the fields that tell one syslog stream from another, used when it is given none.
var DefaultStreamFields = []string{"hostname", "app_name", "proc_id", "cef.device_vendor", "cef.device_product", "cef.device_event_class_id"}

// DefaultMsgFields is where the message is taken from unless told otherwise: the line's own message, then a CEF event's msg, then its name.
var DefaultMsgFields = []string{"message", "cef.extension.msg", "cef.name"}

// Options says how a line is read. All but StripQuotes, CopyFields and Sites match the VictoriaLogs -syslog.* flag of the same name, so a setting
// can be carried across as it is.
type Options struct {
	// MsgFields are the fields the message is taken from: the first with a value is stored as _msg in place of its own name.
	MsgFields []string
	// UseLocalTimestamp stores the time a line arrived in place of the time written in it, which is then kept as an ordinary field.
	UseLocalTimestamp bool
	// UseRemoteIP records the sender's address in remote_ip, and as the hostname of a line that has none.
	UseRemoteIP bool
	// Timezone is the zone a time written without one is read in; nil is the machine's own.
	Timezone *time.Location
	// ExtraFields are added to every row, each in place of any field of the same name the line has. ParseExtraFields reads them from a setting.
	ExtraFields []logstorage.Field
	// StripQuotes takes off a pair of double quotes around the whole message, which UniFi Protect puts around every one. VictoriaLogs has no
	// such setting: CEF says a value is taken as it is written.
	StripQuotes bool
	// CopyFields are fields given the value of others in the line, in place of any of the same name the line, Sites or ExtraFields give, so either
	// stands for a line without the value. ParseCopyFields reads them from a setting. VictoriaLogs has no such setting.
	CopyFields []Copy
	// Sites names the site each sender is at, stored in the site field in place of any the line or ExtraFields give. A UniFi controller sends
	// every site's events from its own address, so a site copied from the event wins. VictoriaLogs has no such setting.
	Sites sites.Networks
}

// Copy is one field given the value of another: the first of From that the line has with a value.
type Copy struct {
	To   string
	From []string
}

// Row appends to dst the JSON line VictoriaLogs is sent for one syslog line that arrived at that time from the sender at that address, "" when it
// is not known. A line whose own time cannot be read is an error, as it is to VictoriaLogs, which drops it.
func (o Options) Row(dst []byte, line string, arrived time.Time, sender string) ([]byte, error) {
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
	if o.UseRemoteIP && sender != "" {
		p.AddField("remote_ip", sender)
		if !hasValue(p.Fields, "hostname") {
			p.AddField("hostname", sender)
		}
	}

	// read before the message is moved, in case a field is copied from one that becomes _msg
	var copied []logstorage.Field
	for _, c := range o.CopyFields {
		if v := firstValue(p.Fields, c.From); v != "" {
			copied = append(copied, logstorage.Field{Name: c.To, Value: v})
		}
	}

	logstorage.RenameField(p.Fields, o.MsgFields, "_msg")
	if o.StripQuotes {
		stripQuotes(p.Fields)
	}

	// a copied field over the sender's site, and both over an extra field of the same name
	extra := o.ExtraFields
	if site := o.Sites.Site(sender); site != "" {
		extra = replaced(extra, logstorage.Field{Name: sites.Field, Value: site})
	}
	for _, f := range copied {
		extra = replaced(extra, f)
	}

	// the added fields go last, and the line's own of the same names are left out, as VictoriaLogs adds -syslog.extraFields
	fields := make([]logstorage.Field, 0, len(p.Fields)+len(extra)+1)
	fields = append(fields, logstorage.Field{Name: "_time", Value: time.Unix(0, atNano).UTC().Format(time.RFC3339Nano)})
	for _, f := range p.Fields {
		if !named(extra, f.Name) {
			fields = append(fields, f)
		}
	}
	fields = append(fields, extra...)

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

// firstValue is the value of the first of the names that one of the fields has with a value, "" when none has.
func firstValue(fields []logstorage.Field, names []string) string {
	for _, name := range names {
		for _, f := range fields {
			if f.Name == name && f.Value != "" {
				return f.Value
			}
		}
	}

	return ""
}

// named is whether one of the fields has that name.
func named(fields []logstorage.Field, name string) bool {
	return slices.ContainsFunc(fields, func(f logstorage.Field) bool { return f.Name == name })
}

// replaced is the fields with f in place of the one of its name, or added at the end when there is none. The fields given are left as they were.
func replaced(fields []logstorage.Field, f logstorage.Field) []logstorage.Field {
	out := slices.DeleteFunc(slices.Clone(fields), func(g logstorage.Field) bool { return g.Name == f.Name })

	return append(out, f)
}

// ParseExtraFields reads the fields to add to every row from a setting written name=value,name=value, or as the JSON object VictoriaLogs'
// -syslog.extraFields takes, {"name":"value"}, for a value with a comma in it. Nothing at all is no fields.
func ParseExtraFields(s string) ([]logstorage.Field, error) {
	var fields []logstorage.Field
	if s = strings.TrimSpace(s); strings.HasPrefix(s, "{") {
		var m map[string]string
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			return nil, fmt.Errorf("%q is not a JSON object of names and values: %w", s, err)
		}

		for _, name := range slices.Sorted(maps.Keys(m)) {
			fields = append(fields, logstorage.Field{Name: name, Value: m[name]})
		}
	} else {
		for pair := range strings.SplitSeq(s, ",") {
			if pair = strings.TrimSpace(pair); pair == "" {
				continue
			}

			name, value, found := strings.Cut(pair, "=")
			if !found {
				return nil, fmt.Errorf("%q is not name=value", pair)
			}

			fields = append(fields, logstorage.Field{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
		}
	}

	for i, f := range fields {
		switch {
		case f.Name == "":
			return nil, fmt.Errorf("a field with the value %q has no name", f.Value)
		case f.Value == "":
			return nil, fmt.Errorf("%s has no value, and VictoriaLogs leaves out a field without one", f.Name)
		case ownField(f.Name):
			return nil, fmt.Errorf("%s is VictoriaLogs' own, and cannot be added", f.Name)
		case named(fields[:i], f.Name):
			return nil, fmt.Errorf("%s is given twice", f.Name)
		}
	}

	return fields, nil
}

// ParseCopyFields reads the fields to give the value of others from a setting written to=from,to=from: site=cef.extension.UNIFIsite gives each row
// a site field holding the line's cef.extension.UNIFIsite. A field given more than once takes the first of its sources that the line has a value
// for. Nothing at all is no fields.
func ParseCopyFields(s string) ([]Copy, error) {
	var copies []Copy
	for pair := range strings.SplitSeq(s, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}

		to, from, found := strings.Cut(pair, "=")
		to, from = strings.TrimSpace(to), strings.TrimSpace(from)
		switch {
		case !found || to == "" || from == "":
			return nil, fmt.Errorf("%q is not to=from, e.g. site=cef.extension.UNIFIsite", pair)
		case to == from:
			return nil, fmt.Errorf("%q copies %s to itself", pair, to)
		case ownField(to):
			return nil, fmt.Errorf("%s is VictoriaLogs' own, and cannot be copied to", to)
		}

		if i := slices.IndexFunc(copies, func(c Copy) bool { return c.To == to }); i >= 0 {
			copies[i].From = append(copies[i].From, from)
			continue
		}

		copies = append(copies, Copy{To: to, From: []string{from}})
	}

	return copies, nil
}

// ownField is whether VictoriaLogs sets the field itself, so a row cannot be given it.
func ownField(name string) bool {
	return name == "_time" || name == "_stream" || name == "_stream_id"
}
