package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/syslog-relay/lib/convert"
	"github.com/katbyte/syslog-relay/lib/rawcopy"
	"github.com/katbyte/syslog-relay/lib/readings"
	"github.com/katbyte/syslog-relay/lib/relay"
	"github.com/katbyte/syslog-relay/lib/sites"
	"github.com/katbyte/syslog-relay/lib/vlogs"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// toolName is what the program is called.
const toolName = "syslog-relay"

// The defaults. Syslog is taken on 1514, out of the way of a VictoriaLogs taking it on 514 beside it, and the readings are served on 9124. A
// hundred thousand rows is about an hour of a busy UniFi site held while VictoriaLogs is away, in a few tens of megabytes.
const (
	defaultListen        = ":1514"
	defaultMetricsListen = ":9124"
	defaultTimezone      = "Local"
	defaultQueueSize     = 100000
	defaultRawMaxMB      = 100
)

type FlagData struct {
	Listen            string `mapstructure:"listen"`
	MetricsListen     string `mapstructure:"metrics-listen"`
	LogsURL           string `mapstructure:"logs-url"`
	MsgField          string `mapstructure:"msg-field"`
	StreamFields      string `mapstructure:"stream-fields"`
	UseLocalTimestamp bool   `mapstructure:"use-local-timestamp"`
	UseRemoteIP       bool   `mapstructure:"use-remote-ip"`
	StripQuotes       bool   `mapstructure:"strip-quotes"`
	ExtraFields       string `mapstructure:"extra-fields"`
	CopyFields        string `mapstructure:"copy-fields"`
	SiteNetworks      string `mapstructure:"site-networks"`
	Timezone          string `mapstructure:"timezone"`
	QueueSize         int    `mapstructure:"queue-size"`
	RawFile           string `mapstructure:"raw-file"`
	RawMaxMB          int    `mapstructure:"raw-max-mb"`
	Labels            string `mapstructure:"labels"`
}

// envNames binds each flag to its environment variable.
var envNames = map[string]string{
	"listen":              "SYSLOG_RELAY_LISTEN",
	"metrics-listen":      "SYSLOG_RELAY_METRICS_LISTEN",
	"logs-url":            "SYSLOG_RELAY_LOGS_URL",
	"msg-field":           "SYSLOG_RELAY_MSG_FIELD",
	"stream-fields":       "SYSLOG_RELAY_STREAM_FIELDS",
	"use-local-timestamp": "SYSLOG_RELAY_USE_LOCAL_TIMESTAMP",
	"use-remote-ip":       "SYSLOG_RELAY_USE_REMOTE_IP",
	"strip-quotes":        "SYSLOG_RELAY_STRIP_QUOTES",
	"extra-fields":        "SYSLOG_RELAY_EXTRA_FIELDS",
	"copy-fields":         "SYSLOG_RELAY_COPY_FIELDS",
	"site-networks":       "SYSLOG_RELAY_SITE_NETWORKS",
	"timezone":            "SYSLOG_RELAY_TIMEZONE",
	"queue-size":          "SYSLOG_RELAY_QUEUE_SIZE",
	"raw-file":            "SYSLOG_RELAY_RAW_FILE",
	"raw-max-mb":          "SYSLOG_RELAY_RAW_MAX_MB",
	"labels":              "SYSLOG_RELAY_LABELS",
}

// persistent is the flag set configureFlags bound, for the one setting that has to be read before viper is: where the settings file is.
var persistent *pflag.FlagSet

func configureFlags(root *cobra.Command) error {
	pflags := root.PersistentFlags()

	pflags.String("listen", defaultListen, "take syslog on this address, over both UDP and TCP")
	pflags.String("metrics-listen", defaultMetricsListen, "serve the relay's own readings and health check on this address")
	pflags.String("logs-url", "", "VictoriaLogs' address, e.g. http://10.0.0.2:9428; rows are sent to its /insert/jsonline")
	pflags.String("msg-field", strings.Join(convert.DefaultMsgFields, ","), "the fields to take the log message from, first to last: the first with a value is stored as _msg (VictoriaLogs' -syslog.msgField)")
	pflags.String("stream-fields", strings.Join(convert.DefaultStreamFields, ","), "the fields VictoriaLogs tells one stream of logs from another by (VictoriaLogs' -syslog.streamFields)")
	pflags.Bool("use-local-timestamp", false, "store the time each line arrived in place of the time written in it, which is kept as the timestamp field (VictoriaLogs' -syslog.useLocalTimestamp)")
	pflags.Bool("use-remote-ip", false, "record the sender's address in remote_ip, and as the hostname of a line without one (VictoriaLogs' -syslog.useRemoteIP)")
	pflags.Bool("strip-quotes", false, "take off a pair of double quotes around the whole message, which UniFi Protect puts around every one")
	pflags.String("extra-fields", "", "fields to add to every row, written name=value,name=value or as a JSON object, e.g. site=home; each replaces any field of that name in the line (VictoriaLogs' -syslog.extraFields)")
	pflags.String("copy-fields", "", "fields to give the value of others in the line, written to=from,to=from, e.g. site=cef.extension.UNIFIsite; each replaces any field of that name in the line or in --extra-fields, which stands for a line without the value")
	pflags.String("site-networks", "", "the site each line comes from, by the network its sender is in, written site=network,site=network, e.g. home=10.0.0.0/16,cabin=203.0.113.7; stored in the site field, the most specific network winning, under a site from --copy-fields and over one from --extra-fields")
	pflags.String("timezone", defaultTimezone, "the zone a time written without one is read in, e.g. America/Vancouver; Local is the machine's, which TZ sets (VictoriaLogs' -syslog.timezone)")
	pflags.Int("queue-size", defaultQueueSize, "the most rows held in memory while VictoriaLogs cannot be reached; past it the oldest are dropped")
	pflags.String("raw-file", "", "also keep every line received, unchanged, in this file: to send them again later. They name people and devices")
	pflags.Int("raw-max-mb", defaultRawMaxMB, "stop adding to --raw-file once it holds this many megabytes")
	pflags.String("labels", "", "labels to put on every reading, written name=value,name=value, e.g. hostname=dock")
	pflags.String("config", "", "read settings from this file and no other, in place of ./.syslog-relay or ~/.syslog-relay (or export SYSLOG_RELAY_CONFIG)")

	persistent = pflags

	for name, env := range envNames {
		if err := viper.BindPFlag(name, pflags.Lookup(name)); err != nil {
			return fmt.Errorf("error binding '%s' flag: %w", name, err)
		}

		if err := viper.BindEnv(name, env); err != nil {
			return fmt.Errorf("error binding '%s' to env '%s' : %w", name, env, err)
		}
	}

	return nil
}

// configEnv names the settings file from the environment, as --config does on the command line.
const configEnv = "SYSLOG_RELAY_CONFIG"

// configPath is the settings file a run was told to read, "" for none: the flag's, then the environment's. A leading ~/ is the home directory, for
// whatever starts the program without a shell to expand it.
func configPath() string {
	path := os.Getenv(configEnv)
	if persistent != nil {
		if fl := persistent.Lookup("config"); fl != nil && fl.Changed {
			path = fl.Value.String()
		}
	}

	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, rest)
		}
	}

	return path
}

// loadConfig reads the settings file, once the flags are parsed: the one --config or SYSLOG_RELAY_CONFIG names and no other, or else the first
// .syslog-relay found in the working directory and then in $HOME. A file that was named and cannot be read is an error; one merely absent from the
// usual places is not.
func loadConfig() error {
	viper.SetConfigType("env")
	if path := configPath(); path != "" {
		viper.SetConfigFile(path)
		if err := viper.ReadInConfig(); err != nil {
			return fmt.Errorf("reading the settings file %s (--config / %s): %w", path, configEnv, err)
		}
	} else {
		viper.SetConfigName(".syslog-relay")
		// viper reads the first file it finds, so the working directory comes first
		viper.AddConfigPath(".")
		if home, err := os.UserHomeDir(); err == nil {
			viper.AddConfigPath(home)
		}

		if err := viper.ReadInConfig(); err != nil {
			if _, ok := errors.AsType[viper.ConfigFileNotFoundError](err); !ok {
				clog.Log.Errorf("Error reading config file: %v", err)
			}
		}
	}

	// a settings file spells a setting the way the environment does, less the prefix (LOGS_URL for --logs-url), and viper only matches a key spelled
	// as the flag is. Carried across as defaults, they still lose to a flag or the environment.
	for name := range envNames {
		if alt := strings.ReplaceAll(name, "-", "_"); alt != name && viper.InConfig(alt) {
			viper.SetDefault(name, viper.Get(alt))
		}
	}

	return nil
}

// GetFlags returns the fully populated FlagData. It is read from viper rather than from the flags themselves, since viper is what lays the
// environment and the settings file under the command line.
func GetFlags() *FlagData {
	var f FlagData
	if err := viper.Unmarshal(&f); err != nil {
		clog.Log.Fatalf("failed to unmarshal configuration: %v", err)
	}

	return &f
}

// fieldList reads a list of field names written with commas between them, or as the JSON list VictoriaLogs' own flags take, so a setting can be
// carried across either way.
func fieldList(s string) ([]string, error) {
	if s = strings.TrimSpace(s); strings.HasPrefix(s, "[") {
		var fields []string
		if err := json.Unmarshal([]byte(s), &fields); err != nil {
			return nil, fmt.Errorf("%q is not a JSON list of field names: %w", s, err)
		}

		return fields, nil
	}

	var fields []string
	for name := range strings.SplitSeq(s, ",") {
		if name = strings.TrimSpace(name); name != "" {
			fields = append(fields, name)
		}
	}

	return fields, nil
}

// convert is how the settings say lines are read.
func (f *FlagData) convert() (convert.Options, error) {
	msgFields, err := fieldList(f.MsgField)
	if err != nil {
		return convert.Options{}, fmt.Errorf("--msg-field (%s): %w", envNames["msg-field"], err)
	}
	if len(msgFields) == 0 {
		return convert.Options{}, fmt.Errorf("--msg-field (%s) names no field to take the message from: message is VictoriaLogs' own", envNames["msg-field"])
	}

	tz, err := time.LoadLocation(f.Timezone)
	if err != nil {
		return convert.Options{}, fmt.Errorf("--timezone (%s): %w", envNames["timezone"], err)
	}

	extra, err := convert.ParseExtraFields(f.ExtraFields)
	if err != nil {
		return convert.Options{}, fmt.Errorf("--extra-fields (%s): %w", envNames["extra-fields"], err)
	}

	copies, err := convert.ParseCopyFields(f.CopyFields)
	if err != nil {
		return convert.Options{}, fmt.Errorf("--copy-fields (%s): %w", envNames["copy-fields"], err)
	}

	at, err := sites.Parse(f.SiteNetworks)
	if err != nil {
		return convert.Options{}, fmt.Errorf("--site-networks (%s): %w", envNames["site-networks"], err)
	}

	return convert.Options{MsgFields: msgFields, UseLocalTimestamp: f.UseLocalTimestamp, UseRemoteIP: f.UseRemoteIP, Timezone: tz, ExtraFields: extra, StripQuotes: f.StripQuotes, CopyFields: copies, Sites: at}, nil
}

// sender is what sends rows to VictoriaLogs, not yet started.
func (f *FlagData) sender() (*vlogs.Sender, error) {
	if f.LogsURL == "" {
		return nil, fmt.Errorf("--logs-url (%s) has to say where VictoriaLogs is, e.g. http://10.0.0.2:9428", envNames["logs-url"])
	}

	streamFields, err := fieldList(f.StreamFields)
	if err != nil {
		return nil, fmt.Errorf("--stream-fields (%s): %w", envNames["stream-fields"], err)
	}

	s, err := vlogs.New(vlogs.Options{URL: f.LogsURL, StreamFields: streamFields, QueueSize: f.QueueSize})
	if err != nil {
		return nil, fmt.Errorf("--logs-url (%s) or --queue-size (%s): %w", envNames["logs-url"], envNames["queue-size"], err)
	}

	return s, nil
}

// raw is the raw copy the settings ask for, nil when they ask for none.
func (f *FlagData) raw() (*rawcopy.File, error) {
	if f.RawFile == "" {
		return nil, nil //nolint:nilnil // no file is asked for, which is no fault
	}
	if f.RawMaxMB < 1 {
		return nil, fmt.Errorf("--raw-max-mb (%s) has to be at least 1", envNames["raw-max-mb"])
	}

	c, err := rawcopy.Open(f.RawFile, int64(f.RawMaxMB)*1024*1024)
	if err != nil {
		return nil, fmt.Errorf("--raw-file (%s): %w", envNames["raw-file"], err)
	}

	return c, nil
}

// page is the page of the relay's readings, with the labels the settings ask for on every one.
func (f *FlagData) page(r *relay.Relay) (*metrics.Page, error) {
	labels, err := metrics.ParseLabels(f.Labels)
	if err != nil {
		return nil, fmt.Errorf("--labels (%s): %w", envNames["labels"], err)
	}

	p, err := metrics.New(metrics.Options{Name: toolName, Labels: labels}, readings.New(r))
	if err != nil {
		return nil, fmt.Errorf("--labels (%s): %w", envNames["labels"], err)
	}

	return p, nil
}
