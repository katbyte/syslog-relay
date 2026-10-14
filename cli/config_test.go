package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// viper is global, so none of these can run in parallel and each resets it.
const (
	testLogsURL  = "http://10.0.0.2:9428"
	settingsFile = ".syslog-relay"
)

// alone cuts a test off from the settings of whoever runs it: viper as it starts, an empty home and working directory, and none of the program's
// variables set.
func alone(t *testing.T) (home, wd string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	home, wd = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(wd)
	t.Setenv(configEnv, "")
	for _, env := range envNames {
		t.Setenv(env, "")
	}

	return home, wd
}

// load drives the real flag wiring with these command-line arguments, after whatever the test put in the environment and the settings files.
func load(t *testing.T, args ...string) (*FlagData, error) {
	t.Helper()

	root := &cobra.Command{Use: toolName}
	if err := configureFlags(root); err != nil {
		t.Fatalf("configureFlags: %v", err)
	}
	if err := root.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if err := loadConfig(); err != nil {
		return nil, err
	}

	return GetFlags(), nil
}

// must is load for a test that expects the settings to be read.
func must(t *testing.T, args ...string) *FlagData {
	t.Helper()

	f, err := load(t, args...)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	return f
}

func write(t *testing.T, dir, file, body string) string {
	t.Helper()

	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

// With nothing set, syslog is taken and the readings served where the README says, and a line is read as VictoriaLogs reads it but for its
// message.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestDefaults(t *testing.T) {
	alone(t)

	want := FlagData{
		Listen:        ":1514",
		MetricsListen: ":9124",
		MsgField:      "message,cef.extension.msg,cef.name",
		StreamFields:  "hostname,app_name,proc_id,cef.device_vendor,cef.device_product,cef.device_event_class_id",
		Timezone:      "Local",
		QueueSize:     100000,
		RawMaxMB:      100,
	}
	if got := *must(t); got != want {
		t.Errorf("defaults = %+v, want %+v", got, want)
	}
}

// Every setting has an environment variable, which is how a container is configured.
func TestEveryFlagHasAnEnvironmentVariable(t *testing.T) {
	alone(t)

	t.Setenv("SYSLOG_RELAY_LISTEN", ":2514")
	t.Setenv("SYSLOG_RELAY_METRICS_LISTEN", "127.0.0.1:9000")
	t.Setenv("SYSLOG_RELAY_LOGS_URL", testLogsURL)
	t.Setenv("SYSLOG_RELAY_MSG_FIELD", "message,cef.extension.msg")
	t.Setenv("SYSLOG_RELAY_STREAM_FIELDS", "hostname,app_name")
	t.Setenv("SYSLOG_RELAY_USE_LOCAL_TIMESTAMP", "true")
	t.Setenv("SYSLOG_RELAY_USE_REMOTE_IP", "true")
	t.Setenv("SYSLOG_RELAY_STRIP_QUOTES", "true")
	t.Setenv("SYSLOG_RELAY_TIMEZONE", "America/Vancouver")
	t.Setenv("SYSLOG_RELAY_QUEUE_SIZE", "10")
	t.Setenv("SYSLOG_RELAY_RAW_FILE", "/data/unifi.raw")
	t.Setenv("SYSLOG_RELAY_RAW_MAX_MB", "5")
	t.Setenv("SYSLOG_RELAY_LABELS", "hostname=dock")

	want := FlagData{
		Listen:            ":2514",
		MetricsListen:     "127.0.0.1:9000",
		LogsURL:           testLogsURL,
		MsgField:          "message,cef.extension.msg",
		StreamFields:      "hostname,app_name",
		UseLocalTimestamp: true,
		UseRemoteIP:       true,
		StripQuotes:       true,
		Timezone:          "America/Vancouver",
		QueueSize:         10,
		RawFile:           "/data/unifi.raw",
		RawMaxMB:          5,
		Labels:            "hostname=dock",
	}
	if got := *must(t); got != want {
		t.Errorf("from the environment = %+v, want %+v", got, want)
	}

	root := &cobra.Command{Use: toolName}
	if err := configureFlags(root); err != nil {
		t.Fatal(err)
	}
	root.PersistentFlags().VisitAll(func(fl *pflag.Flag) {
		if _, ok := envNames[fl.Name]; !ok && fl.Name != "config" {
			t.Errorf("--%s has no environment variable", fl.Name)
		}
	})
}

// A flag beats the environment, which beats a settings file: a container overrides what an image holds, and a command line overrides both.
func TestFlagThenEnvironmentThenFile(t *testing.T) {
	home, _ := alone(t)
	write(t, home, settingsFile, "LOGS_URL=http://from-file:9428\nMSG_FIELD=cef.name\nUSE_REMOTE_IP=true\nQUEUE_SIZE=7\n")

	f := must(t)
	if f.LogsURL != "http://from-file:9428" || f.MsgField != "cef.name" || !f.UseRemoteIP || f.QueueSize != 7 {
		t.Errorf("from the file = %+v, want every setting it holds, those of several words included", f)
	}

	viper.Reset()
	t.Setenv("SYSLOG_RELAY_LOGS_URL", "http://from-env:9428")
	t.Setenv("SYSLOG_RELAY_QUEUE_SIZE", "8")
	f = must(t)
	if f.LogsURL != "http://from-env:9428" || f.QueueSize != 8 || f.MsgField != "cef.name" {
		t.Errorf("with the environment = %+v, want it over the file, and the file for the rest", f)
	}

	viper.Reset()
	f = must(t, "--logs-url", "http://from-flag:9428", "--queue-size", "9")
	if f.LogsURL != "http://from-flag:9428" || f.QueueSize != 9 {
		t.Errorf("with flags = %+v, want them over the environment", f)
	}
}

// A .syslog-relay in the working directory is found before the one in $HOME, and neither being there is not an error.
//
//nolint:paralleltest // viper is global state; these mutate it
func TestSettingsFilePrecedence(t *testing.T) {
	home, wd := alone(t)

	if f := must(t); f.LogsURL != "" {
		t.Errorf("logs URL = %q with no settings file, want none", f.LogsURL)
	}

	viper.Reset()
	write(t, home, settingsFile, "LOGS_URL=http://from-home:9428\n")
	if f := must(t); f.LogsURL != "http://from-home:9428" {
		t.Errorf("logs URL = %q, want the one in $HOME", f.LogsURL)
	}

	viper.Reset()
	write(t, wd, settingsFile, "LOGS_URL=http://from-here:9428\n")
	if f := must(t); f.LogsURL != "http://from-here:9428" {
		t.Errorf("logs URL = %q, want the working directory's over $HOME's", f.LogsURL)
	}
}

// A settings file that is named is the only one read, and one that cannot be read is an error: a run told where its settings are must not come
// up with someone else's, or with none.
func TestANamedSettingsFile(t *testing.T) {
	home, _ := alone(t)
	write(t, home, settingsFile, "LOGS_URL=http://from-home:9428\n")
	named := write(t, home, "other.env", "LISTEN=:2514\n")

	f := must(t, "--config", named)
	if f.Listen != ":2514" || f.LogsURL != "" {
		t.Errorf("with --config = %+v, want the named file and nothing of $HOME's", f)
	}

	viper.Reset()
	t.Setenv(configEnv, "~/other.env")
	if f := must(t); f.Listen != ":2514" {
		t.Errorf("with %s=~/other.env syslog is taken on %q, want the named file's", configEnv, f.Listen)
	}

	viper.Reset()
	if _, err := load(t, "--config", filepath.Join(home, "missing.env")); err == nil || !strings.Contains(err.Error(), "missing.env") {
		t.Errorf("a named file that is not there = %v, want an error naming it", err)
	}
}

// A list of fields can be written with commas, or as the JSON list VictoriaLogs' own flags take.
func TestFieldList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"message,cef.extension.msg", "message|cef.extension.msg"},
		{" message , cef.extension.msg ,", "message|cef.extension.msg"},
		{`["message","cef.extension.msg"]`, "message|cef.extension.msg"},
		{"", ""},
	}
	for _, tc := range tests {
		got, err := fieldList(tc.in)
		if err != nil {
			t.Errorf("fieldList(%q): %v", tc.in, err)
		}
		if strings.Join(got, "|") != tc.want {
			t.Errorf("fieldList(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if _, err := fieldList(`["message",`); err == nil {
		t.Error("a JSON list cut off gave no error")
	}
}

// Settings that cannot be used are said before anything is listened on, naming the setting.
func TestSettingsThatCannotBeUsed(t *testing.T) {
	t.Parallel()

	good := FlagData{LogsURL: testLogsURL, MsgField: "message", Timezone: "UTC", QueueSize: 10, RawMaxMB: 1}

	tests := []struct {
		name  string
		edit  func(*FlagData)
		check func(*FlagData) error
		says  string
	}{
		{"no message field", func(f *FlagData) { f.MsgField = " , " }, convertErr, "SYSLOG_RELAY_MSG_FIELD"},
		{"a message field list cut off", func(f *FlagData) { f.MsgField = `["message"` }, convertErr, "SYSLOG_RELAY_MSG_FIELD"},
		{"a zone there is none of", func(f *FlagData) { f.Timezone = "Mars/Olympus" }, convertErr, "SYSLOG_RELAY_TIMEZONE"},
		{"no VictoriaLogs", func(f *FlagData) { f.LogsURL = "" }, senderErr, "SYSLOG_RELAY_LOGS_URL"},
		{"VictoriaLogs without a scheme", func(f *FlagData) { f.LogsURL = "10.0.0.2:9428" }, senderErr, "SYSLOG_RELAY_LOGS_URL"},
		{"a queue that holds nothing", func(f *FlagData) { f.QueueSize = 0 }, senderErr, "SYSLOG_RELAY_QUEUE_SIZE"},
		{"a stream field list cut off", func(f *FlagData) { f.StreamFields = `["hostname"` }, senderErr, "SYSLOG_RELAY_STREAM_FIELDS"},
		{"a raw copy of nothing", func(f *FlagData) { f.RawFile, f.RawMaxMB = "unifi.raw", 0 }, rawErr, "SYSLOG_RELAY_RAW_MAX_MB"},
		{"a raw copy where it cannot go", func(f *FlagData) { f.RawFile = "/no/such/dir/unifi.raw" }, rawErr, "SYSLOG_RELAY_RAW_FILE"},
		{"labels written wrongly", func(f *FlagData) { f.Labels = "hostname" }, pageErr, "SYSLOG_RELAY_LABELS"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := new(good)
			tc.edit(f)

			err := tc.check(f)
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("got %v, want an error naming %s", err, tc.says)
			}
		})
	}

	for name, check := range map[string]func(*FlagData) error{"convert": convertErr, "sender": senderErr, "raw": rawErr, "page": pageErr} {
		if err := check(new(good)); err != nil {
			t.Errorf("%s of good settings: %v", name, err)
		}
	}
}

func convertErr(f *FlagData) error { _, err := f.convert(); return err }
func senderErr(f *FlagData) error  { _, err := f.sender(); return err }
func rawErr(f *FlagData) error     { _, err := f.raw(); return err }

func pageErr(f *FlagData) error {
	s, err := f.sender()
	if err != nil {
		return err
	}
	_, err = f.page(newRelay(s))

	return err
}

// The zone given is the one times are read in.
func TestTimezone(t *testing.T) {
	t.Parallel()

	f := FlagData{MsgField: "message", Timezone: "America/Vancouver"}
	o, err := f.convert()
	if err != nil {
		t.Fatal(err)
	}
	if o.Timezone.String() != "America/Vancouver" {
		t.Errorf("zone = %s, want America/Vancouver", o.Timezone)
	}
	if _, offset := time.Date(2026, 10, 13, 0, 0, 0, 0, o.Timezone).Zone(); offset != -7*3600 {
		t.Errorf("offset = %d, want Vancouver's in October", offset)
	}
}
