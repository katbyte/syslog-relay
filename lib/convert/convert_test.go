package convert

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	_ "time/tzdata" // the tests read times in America/Vancouver, wherever they run

	"github.com/VictoriaMetrics/VictoriaLogs/lib/logstorage"
	"github.com/katbyte/go-kt/pointer"
	"github.com/katbyte/syslog-relay/lib/sites"
)

var update = flag.Bool("update", false, "rewrite testdata/unifi.jsonl from what the lines become now")

// vancouver is the zone the VictoriaLogs this was written for reads times in.
func vancouver(t *testing.T) *time.Location {
	t.Helper()

	tz, err := time.LoadLocation("America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}

	return tz
}

// arrived is when every line in these tests arrives: a moment after the last of them was written.
var arrived = time.Date(2026, 10, 14, 3, 30, 0, 500, time.UTC)

// row is the row a line from that sender becomes, read back as its fields.
func row(t *testing.T, o Options, line, sender string) map[string]string {
	t.Helper()

	b, err := o.Row(nil, line, arrived, sender)
	if err != nil {
		t.Fatalf("Row(%q): %v", line, err)
	}
	if !bytes.HasSuffix(b, []byte("\n")) || bytes.Count(b, []byte("\n")) != 1 {
		t.Fatalf("Row(%q) = %q, want one line ending in a newline", line, b)
	}

	var fields map[string]string
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("Row(%q) = %q, which is not a JSON object of strings: %v", line, b, err)
	}

	return fields
}

// TestUniFi turns lines shaped like the ones UniFi sends into rows, with the settings a real VictoriaLogs runs with, and holds them to what was
// seen to be right. go test ./lib/convert -update rewrites what they are held to, once a change to them is meant.
func TestUniFi(t *testing.T) {
	t.Parallel()

	o := Options{MsgFields: DefaultMsgFields, UseLocalTimestamp: true, UseRemoteIP: true, Timezone: vancouver(t)}

	in, err := os.ReadFile(filepath.Join("testdata", "unifi.log"))
	if err != nil {
		t.Fatal(err)
	}

	var got []byte
	sc := bufio.NewScanner(bytes.NewReader(in))
	for sc.Scan() {
		got, err = o.Row(got, sc.Text(), arrived, "192.0.2.10")
		if err != nil {
			t.Fatalf("Row(%q): %v", sc.Text(), err)
		}
	}

	golden := filepath.Join("testdata", "unifi.jsonl")
	if pointer.From(update) {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(golden) //nolint:gosec // the file the test is held to
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the rows differ from %s (go test ./lib/convert -update rewrites it):\ngot:\n%s\nwant:\n%s", golden, got, want)
	}
}

func TestMessage(t *testing.T) {
	t.Parallel()

	const (
		cefWithMsg = `Oct 13 18:58:31 nvr CEF:0|Ubiquiti|UniFi Protect|7.3.70|2159|motion|3|eventType=motion msg=garage has recorded motion.`
		cefNoMsg   = `Oct 13 18:58:31 nvr CEF:0|Ubiquiti|UniFi Network|10.6.106|400|WiFi Client Connected|1|UNIFIclientMac=00:00:5e:00:53:aa`
		plain      = `<30>Oct 13 18:58:31 cloudkey unifi-core[1234]: the console started a backup`
	)

	tests := []struct {
		name      string
		msgFields []string
		line      string
		msg       string
		moved     string // the field the message was moved from, which the row no longer has
		kept      string // a field the row has to keep
	}{
		{"a CEF event's msg", DefaultMsgFields, cefWithMsg, "garage has recorded motion.", "cef.extension.msg", "cef.name"},
		{"a CEF event without one is called by its name", DefaultMsgFields, cefNoMsg, "WiFi Client Connected", "cef.name", "cef.extension.UNIFIclientMac"},
		{"a line's own message comes first", DefaultMsgFields, plain, "the console started a backup", "message", "app_name"},
		{"VictoriaLogs' own choice leaves a CEF event without one", []string{"message"}, cefWithMsg, "", "", "cef.extension.msg"},
		{"the order given is the order tried", []string{"cef.name", "cef.extension.msg"}, cefWithMsg, "motion", "cef.name", "cef.extension.msg"},
		{"a field that is empty is passed over", []string{"cef.extension.nothing", "cef.extension.msg"}, cefWithMsg, "garage has recorded motion.", "cef.extension.msg", "cef.name"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fields := row(t, Options{MsgFields: tc.msgFields, Timezone: time.UTC}, tc.line, "")

			if fields["_msg"] != tc.msg {
				t.Errorf("_msg = %q, want %q", fields["_msg"], tc.msg)
			}
			if _, ok := fields[tc.moved]; ok && tc.moved != "" {
				t.Errorf("%s is still in the row, when it was to be moved to _msg: %v", tc.moved, fields)
			}
			if _, ok := fields[tc.kept]; !ok {
				t.Errorf("%s is missing from the row: %v", tc.kept, fields)
			}
		})
	}
}

func TestTime(t *testing.T) {
	t.Parallel()

	tz := vancouver(t)
	arrivedAt := arrived.Format(time.RFC3339Nano)

	tests := []struct {
		name      string
		local     bool
		line      string
		time      string
		timestamp string // the timestamp field, "" when the row has none
	}{
		{"a time written without a zone is read in the one given", false, `Oct 13 18:58:31 nvr app: hi`, "2026-10-14T01:58:31Z", ""},
		{"a time written with its zone is read in it", false, `<14>1 2026-10-13T18:58:31.25-07:00 nvr app - - - hi`, "2026-10-14T01:58:31.25Z", ""},
		{"a nil time is the time it arrived", false, `<14>1 - nvr app - - - hi`, arrivedAt, ""},
		{"a line with no time is the time it arrived", false, `hi`, arrivedAt, ""},
		{"the time it arrived, keeping the one written", true, `Oct 13 18:58:31 nvr app: hi`, arrivedAt, "2026-10-14T01:58:31Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fields := row(t, Options{MsgFields: DefaultMsgFields, UseLocalTimestamp: tc.local, Timezone: tz}, tc.line, "")

			if fields["_time"] != tc.time {
				t.Errorf("_time = %q, want %q", fields["_time"], tc.time)
			}
			if fields["timestamp"] != tc.timestamp {
				t.Errorf("timestamp = %q, want %q", fields["timestamp"], tc.timestamp)
			}
		})
	}
}

func TestTimeNotReadable(t *testing.T) {
	t.Parallel()

	if _, err := (Options{Timezone: time.UTC}).Row(nil, `<14>1 yesterday nvr app - - - hi`, arrived, ""); err == nil {
		t.Error("a line whose time cannot be read gave a row, when VictoriaLogs drops it")
	}

	// the time is never read when the time it arrived is used, so the line is kept
	fields := row(t, Options{UseLocalTimestamp: true, Timezone: time.UTC}, `<14>1 yesterday nvr app - - - hi`, "")
	if fields["timestamp"] != "yesterday" {
		t.Errorf("timestamp = %q, want it kept as written", fields["timestamp"])
	}
}

func TestRemoteIP(t *testing.T) {
	t.Parallel()

	o := Options{MsgFields: DefaultMsgFields, UseRemoteIP: true, Timezone: time.UTC}

	fields := row(t, o, `Oct 13 18:58:31 nvr app: hi`, "192.0.2.11")
	if fields["remote_ip"] != "192.0.2.11" || fields["hostname"] != "nvr" {
		t.Errorf("remote_ip = %q and hostname = %q, want 192.0.2.11 and the line's own nvr", fields["remote_ip"], fields["hostname"])
	}

	// a line with no hostname is given the sender's, as VictoriaLogs gives it
	fields = row(t, o, `Oct 13 18:58:31 app: hi`, "192.0.2.11")
	if fields["hostname"] != "192.0.2.11" {
		t.Errorf("hostname = %q, want the sender's address", fields["hostname"])
	}

	fields = row(t, o, `Oct 13 18:58:31 app: hi`, "")
	if _, ok := fields["remote_ip"]; ok {
		t.Errorf("remote_ip is recorded when the sender is not known: %v", fields)
	}
	if _, ok := fields["hostname"]; ok {
		t.Errorf("hostname is made up when the line has none and the sender is not known: %v", fields)
	}

	o.UseRemoteIP = false
	fields = row(t, o, `Oct 13 18:58:31 app: hi`, "192.0.2.11")
	if _, ok := fields["remote_ip"]; ok {
		t.Errorf("remote_ip is recorded when the sender was not to be: %v", fields)
	}
	if _, ok := fields["hostname"]; ok {
		t.Errorf("hostname is made up when the line has none and the sender was not to be recorded: %v", fields)
	}
}

// Extra fields are added to every row, and one the line has of the same name is left out, as VictoriaLogs adds -syslog.extraFields.
func TestExtraFields(t *testing.T) {
	t.Parallel()

	extra, err := ParseExtraFields("site=home,app_name=unifi")
	if err != nil {
		t.Fatal(err)
	}
	o := Options{MsgFields: DefaultMsgFields, Timezone: time.UTC, ExtraFields: extra}

	b, err := o.Row(nil, `Oct 13 18:58:31 nvr app: hi`, arrived, "")
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(b, []byte(`"app_name":`)); n != 1 {
		t.Errorf("row = %s, with app_name %d times, want once", b, n)
	}

	fields := row(t, o, `Oct 13 18:58:31 nvr app: hi`, "")
	if fields["site"] != "home" || fields["app_name"] != "unifi" || fields["_msg"] != "hi" || fields["hostname"] != "nvr" {
		t.Errorf("row = %v, want the site added, app_name replaced and the rest as the line has it", fields)
	}
}

// A copied field holds the first of its sources the line has a value for, in place of the line's own field of its name and of an extra field,
// which stands for a line without one.
func TestCopyFields(t *testing.T) {
	t.Parallel()

	copies, err := ParseCopyFields("site=cef.extension.UNIFIsite,site=cef.extension.UNIFIhost,product=cef.device_product,said=cef.extension.msg")
	if err != nil {
		t.Fatal(err)
	}
	o := Options{MsgFields: DefaultMsgFields, Timezone: time.UTC, CopyFields: copies, ExtraFields: []logstorage.Field{{Name: "site", Value: "elsewhere"}}}

	const header = `Oct 13 18:58:31 cloudkey CEF:0|Ubiquiti|UniFi Network|10.6.106|203|Blocked by Firewall|4|`
	for line, want := range map[string]string{
		header + `UNIFIsite=cabin UNIFIhost=console msg=blocked`: "cabin",
		header + `UNIFIhost=console msg=blocked`:                 "console",
		header + `msg=blocked`:                                   "elsewhere",
		header + `UNIFIsite= msg=blocked`:                        "elsewhere",
		`Oct 13 18:58:31 nvr site: hi`:                           "elsewhere",
	} {
		b, err := o.Row(nil, line, arrived, "")
		if err != nil {
			t.Fatal(err)
		}
		if n := bytes.Count(b, []byte(`"site":`)); n != 1 {
			t.Errorf("%q: row = %s, with site %d times, want once", line, b, n)
		}

		fields := row(t, o, line, "")
		if fields["site"] != want {
			t.Errorf("%q: site = %q, want %q", line, fields["site"], want)
		}
		if fields["cef.extension.UNIFIsite"] == "" && fields["site"] == "cabin" {
			t.Errorf("%q: the field copied from is gone: %v", line, fields)
		}
	}

	// a field copied from one the message is then taken from still gets its value, and so does one with no extra field to stand for it
	fields := row(t, o, header+`UNIFIsite=cabin msg=blocked`, "")
	if fields["said"] != "blocked" || fields["_msg"] != "blocked" || fields["product"] != "UniFi Network" {
		t.Errorf("row = %v, want said and _msg both blocked, and product UniFi Network", fields)
	}
	if fields = row(t, o, `Oct 13 18:58:31 nvr app: hi`, ""); fields["product"] != "" {
		t.Errorf("product = %q from a line without the field, want none", fields["product"])
	}
}

// The site is the one at the most specific network the sender is in, under a site copied from the line and over one from the extra fields, which
// stands for a sender in none.
func TestSites(t *testing.T) {
	t.Parallel()

	at, err := sites.Parse("home=192.0.2.0/24,shed=192.0.2.128/25,cabin=198.51.100.0/24,lab=2001:db8::/32")
	if err != nil {
		t.Fatal(err)
	}
	copies, err := ParseCopyFields("site=cef.extension.UNIFIsite")
	if err != nil {
		t.Fatal(err)
	}
	o := Options{MsgFields: DefaultMsgFields, Timezone: time.UTC, Sites: at, CopyFields: copies, ExtraFields: []logstorage.Field{{Name: "site", Value: "elsewhere"}}}

	const plain = `Oct 13 18:58:31 gateway kernel: link up`
	const event = `Oct 13 18:58:31 cloudkey CEF:0|Ubiquiti|UniFi Network|10.6.106|203|Blocked by Firewall|4|UNIFIsite=cabin msg=blocked`
	for _, c := range []struct{ line, sender, want string }{
		{plain, "192.0.2.11", "home"},
		{plain, "192.0.2.200", "shed"},
		{plain, "198.51.100.7", "cabin"},
		{plain, "[2001:db8::1]", "lab"},
		{plain, "203.0.113.9", "elsewhere"},
		{plain, "", "elsewhere"},
		{plain, "not an address", "elsewhere"},
		// a controller at home sends the cabin's events from its own address
		{event, "192.0.2.11", "cabin"},
		{event, "203.0.113.9", "cabin"},
	} {
		b, err := o.Row(nil, c.line, arrived, c.sender)
		if err != nil {
			t.Fatal(err)
		}
		if n := bytes.Count(b, []byte(`"site":`)); n != 1 {
			t.Errorf("from %q: row = %s, with site %d times, want once", c.sender, b, n)
		}
		if got := row(t, o, c.line, c.sender)["site"]; got != c.want {
			t.Errorf("%q from %q: site = %q, want %q", c.line, c.sender, got, c.want)
		}
	}

	// the extra fields are left as they were for the next row
	if o.ExtraFields[0].Value != "elsewhere" || len(o.ExtraFields) != 1 {
		t.Errorf("ExtraFields = %v after rows, want them unchanged", o.ExtraFields)
	}
}

func TestParseCopyFields(t *testing.T) {
	t.Parallel()

	for s, want := range map[string]string{
		"":                         "null",
		"site = a , b=c, site=d ,": `[{"To":"site","From":["a","d"]},{"To":"b","From":["c"]}]`,
	} {
		got, err := ParseCopyFields(s)
		if err != nil {
			t.Errorf("ParseCopyFields(%q): %v", s, err)
			continue
		}
		if b, _ := json.Marshal(got); string(b) != want {
			t.Errorf("ParseCopyFields(%q) = %s, want %s", s, b, want)
		}
	}

	for _, s := range []string{"site", "site=", "=a", "site=site", "_time=a", "_stream=a", "_stream_id=a"} {
		if got, err := ParseCopyFields(s); err == nil {
			t.Errorf("ParseCopyFields(%q) = %v, want an error", s, got)
		}
	}
}

func TestParseExtraFields(t *testing.T) {
	t.Parallel()

	for s, want := range map[string]string{
		"":                           "null",
		" site = home , rack=a ,":    `[{"Name":"site","Value":"home"},{"Name":"rack","Value":"a"}]`,
		"note=a=b":                   `[{"Name":"note","Value":"a=b"}]`,
		`{"zone":"b, c","area":"a"}`: `[{"Name":"area","Value":"a"},{"Name":"zone","Value":"b, c"}]`,
		`  {"site":"home"}  `:        `[{"Name":"site","Value":"home"}]`,
	} {
		got, err := ParseExtraFields(s)
		if err != nil {
			t.Errorf("ParseExtraFields(%q): %v", s, err)
			continue
		}
		if b, _ := json.Marshal(got); string(b) != want {
			t.Errorf("ParseExtraFields(%q) = %s, want %s", s, b, want)
		}
	}

	// a comma in a value needs the JSON form: written plainly, what follows it is a field without a value
	for _, s := range []string{"site", "=home", "site=", "note=a, b", "_time=now", "_stream=x", "_stream_id=x", "site=a,site=b", `{"a":1}`, `{"a":""}`, "{"} {
		if got, err := ParseExtraFields(s); err == nil {
			t.Errorf("ParseExtraFields(%q) = %v, want an error", s, got)
		}
	}
}

func TestAppends(t *testing.T) {
	t.Parallel()

	o := Options{MsgFields: DefaultMsgFields, Timezone: time.UTC}

	b, err := o.Row([]byte("before\n"), `Oct 13 18:58:31 nvr app: hi`, arrived, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("before\n{")) {
		t.Errorf("Row = %q, want the row after what was there", b)
	}
}

// The local zone is the machine's when none is given.
func TestNoZone(t *testing.T) {
	t.Parallel()

	fields := row(t, Options{MsgFields: DefaultMsgFields}, `Oct 13 18:58:31 nvr app: hi`, "")

	want := time.Date(2026, 10, 13, 18, 58, 31, 0, time.Local).UTC().Format(time.RFC3339Nano)
	if fields["_time"] != want {
		t.Errorf("_time = %q, want %q", fields["_time"], want)
	}
}

func TestStripQuotes(t *testing.T) {
	t.Parallel()

	const protect = `Oct 13 18:58:31 nvr CEF:0|Ubiquiti|UniFi Protect|7.3.70|2159|motion|3|eventType=motion msg=%s severity=3`

	tests := []struct {
		name  string
		strip bool
		msg   string
		want  string
	}{
		{"UniFi Protect's quotes are taken off", true, `"garage has recorded motion."`, `garage has recorded motion.`},
		{"left on unless asked", false, `"garage has recorded motion."`, `"garage has recorded motion."`},
		{"a message without them is left as it is", true, `garage has recorded motion.`, `garage has recorded motion.`},
		{"only a pair is taken off", true, `"garage has recorded motion.`, `"garage has recorded motion.`},
		{"quotes inside stay", true, `"alex named the camera "garage"."`, `alex named the camera "garage".`},
		{"a message of only quotes is kept, not left empty", true, `""`, `""`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fields := row(t, Options{MsgFields: DefaultMsgFields, Timezone: time.UTC, StripQuotes: tc.strip}, fmt.Sprintf(protect, tc.msg), "")
			if fields["_msg"] != tc.want {
				t.Errorf("_msg = %q, want %q", fields["_msg"], tc.want)
			}
		})
	}
}

// Only the message loses its quotes: every other field is stored as it was written.
func TestStripQuotesOnlyTheMessage(t *testing.T) {
	t.Parallel()

	line := `<30>Oct 13 19:51:00 cloudkey unifi-core[1234]: "the console started a backup"`
	fields := row(t, Options{MsgFields: []string{"cef.extension.msg"}, Timezone: time.UTC, StripQuotes: true}, line, "")
	if fields["message"] != `"the console started a backup"` {
		t.Errorf("message = %q, want it as written, since it is not the one stored as _msg", fields["message"])
	}

	fields = row(t, Options{MsgFields: DefaultMsgFields, Timezone: time.UTC, StripQuotes: true}, line, "")
	if fields["_msg"] != "the console started a backup" {
		t.Errorf("_msg = %q, want the quotes off whichever field the message came from", fields["_msg"])
	}
}
