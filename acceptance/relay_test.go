//go:build acceptance

package acceptance_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/go-kt/prometheus/metrics"
	"github.com/katbyte/go-kt/prometheus/metricstest"
)

var (
	// image is the program's own image, as make docker builds it. make testacc builds it and names it here.
	image = os.Getenv("SYSLOG_RELAY_TEST_PROGRAM_IMAGE")
	// logsImage is the VictoriaLogs it is tested beside: the pinned version, from Google's mirror of Docker Hub unless told otherwise.
	logsImage = orElse(os.Getenv("SYSLOG_RELAY_TEST_LOGS_IMAGE"), "mirror.gcr.io/victoriametrics/victoria-logs:v1.53.0")
)

func orElse(s, otherwise string) string {
	if s == "" {
		return otherwise
	}

	return s
}

// patience is how long anything is waited for before a test gives up on it.
const patience = 60 * time.Second

// realSettings are the settings a real VictoriaLogs runs with, given to the relay as its own.
var realSettings = []string{
	"TZ=America/Vancouver",
	"SYSLOG_RELAY_MSG_FIELD=message,cef.extension.msg",
	"SYSLOG_RELAY_USE_LOCAL_TIMESTAMP=true",
	"SYSLOG_RELAY_USE_REMOTE_IP=true",
	"SYSLOG_RELAY_LOG=INFO",
}

// docker runs one docker command and gives back what it printed. It is not stopped when the test ends, so a cleanup can still put things back.
func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()

	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), "docker", args...) //nolint:gosec // every argument is one a test here wrote
	out, err := cmd.CombinedOutput()

	return strings.TrimSpace(string(out)), err
}

func mustDocker(t *testing.T, args ...string) string {
	t.Helper()

	out, err := docker(t, args...)
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, out)
	}

	return out
}

// network is a docker network of the test's own, removed when it ends, for the containers to find each other by name on.
func network(t *testing.T) string {
	t.Helper()

	if image == "" {
		t.Skip("no image to run: make testacc builds one and names it in SYSLOG_RELAY_TEST_PROGRAM_IMAGE")
	}

	name := fmt.Sprintf("syslog-relay-acc-%d-%s", os.Getpid(), strings.ToLower(t.Name()))
	_, _ = docker(t, "network", "rm", name)
	mustDocker(t, "network", "create", name)
	t.Cleanup(func() { _, _ = docker(t, "network", "rm", name) })

	return name
}

// run starts a container under that name on the network, removed when the test ends.
func run(t *testing.T, nw, name string, args ...string) {
	t.Helper()

	_, _ = docker(t, "rm", "--force", name)
	t.Cleanup(func() { _, _ = docker(t, "rm", "--force", name) })

	mustDocker(t, append([]string{"run", "--detach", "--name", name, "--network", nw, "--network-alias", strings.TrimPrefix(name, nw+"-")}, args...)...)
}

// published is the address on this machine that a container's port is published on.
func published(t *testing.T, name, port string) string {
	t.Helper()

	out := mustDocker(t, "port", name, port)

	return strings.TrimSpace(strings.Split(out, "\n")[0])
}

// startLogs starts a stock VictoriaLogs with these flags, and gives back where its HTTP port is published.
func startLogs(t *testing.T, nw, name string, flags ...string) string {
	t.Helper()

	run(t, nw, name, append([]string{"--publish", "127.0.0.1::9428", "--env", "TZ=America/Vancouver", logsImage}, flags...)...)

	base := "http://" + published(t, name, "9428/tcp")
	waitFor(t, "VictoriaLogs "+name+" to answer", func() bool { return get(t, base+"/health") == "OK" })

	return base
}

// startRelay starts the relay's image sending to the VictoriaLogs called logs on the network, with the real settings and these besides.
func startRelay(t *testing.T, nw, name, logs string, env ...string) {
	t.Helper()

	args := make([]string, 0, 2*(len(realSettings)+len(env))+9)
	args = append(args, "--publish", "127.0.0.1::1514/tcp", "--publish", "127.0.0.1::9124", "--health-interval", "2s", "--env", "SYSLOG_RELAY_LOGS_URL=http://"+logs+":9428")
	for _, e := range append(slices.Clone(realSettings), env...) {
		args = append(args, "--env", e)
	}
	run(t, nw, name, append(args, image)...)

	waitFor(t, "the relay's health check", func() bool { return get(t, "http://"+published(t, name, "9124/tcp")+metrics.HealthPath) == "ok" })
}

// client is the one client every request is made with.
var client = &http.Client{Timeout: 10 * time.Second}

// get reads a URL, "" when it does not answer.
func get(t *testing.T, at string) string {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, at, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)

	return strings.TrimSpace(string(raw))
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(patience)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// rows is every row a VictoriaLogs holds, as its fields.
func rows(t *testing.T, base string) []map[string]string {
	t.Helper()

	out := get(t, base+"/select/logsql/query?"+url.Values{"query": {"*"}}.Encode())

	var all []map[string]string
	for line := range strings.Lines(out) {
		var f map[string]string
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("VictoriaLogs answered %q: %v", line, err)
		}
		all = append(all, f)
	}

	return all
}

// waitRows waits until a VictoriaLogs holds n rows, and gives them back.
func waitRows(t *testing.T, base string, n int) []map[string]string {
	t.Helper()

	var got []map[string]string
	waitFor(t, fmt.Sprintf("%d rows at %s", n, base), func() bool {
		got = rows(t, base)
		return len(got) >= n
	})

	return got
}

// unifi is the lines shaped like what UniFi sends, one to a line.
func unifi(t *testing.T) []string {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "lib", "convert", "testdata", "unifi.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}

	return lines
}

// sendUDP sends each line in a packet of its own, as UniFi does, to host:port on the test's network. They are sent from a container on it: the
// VM docker runs in on a Mac forwards only TCP to the machine, and every receiver sees the same sender.
func sendUDP(t *testing.T, nw, host, port string, lines ...string) {
	t.Helper()

	script := `while IFS= read -r line; do printf '%s' "$line" | nc -u -w1 "$0" "$1" & done; wait`
	cmd := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--interactive", "--network", nw, "--entrypoint", "sh", image, "-c", script, host, port) //nolint:gosec // every argument is one a test here wrote
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sending to %s:%s: %v: %s", host, port, err, out)
	}
}

// send sends each line over a connection from this machine to an address published on it.
func send(t *testing.T, protocol, to string, lines ...string) {
	t.Helper()

	var d net.Dialer
	c, err := d.DialContext(t.Context(), protocol, to)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	for _, line := range lines {
		if protocol == "tcp" {
			line += "\n"
		}
		if _, err := c.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
}

// The check that matters: a UniFi line sent through the relay is stored with exactly the fields and values VictoriaLogs stores for it by itself,
// stream and all, but for its message, which is the event's own msg where VictoriaLogs alone has none.
func TestSameRowsButTheMessage(t *testing.T) {
	nw := network(t)
	stock := startLogs(t, nw, nw+"-stock", "-syslog.listenAddr.udp=:514", "-syslog.useLocalTimestamp.udp=true", "-syslog.useRemoteIP.udp=true")
	fed := startLogs(t, nw, nw+"-fed")
	startRelay(t, nw, nw+"-relay", "fed", "SYSLOG_RELAY_RAW_FILE=/tmp/unifi.raw")

	lines := unifi(t)
	sendUDP(t, nw, "stock", "514", lines...)
	sendUDP(t, nw, "relay", "1514", lines...)

	byTimestamp := func(all []map[string]string) map[string]map[string]string {
		m := map[string]map[string]string{}
		for _, r := range all {
			m[r["timestamp"]] = r
		}

		return m
	}
	stockRows := byTimestamp(waitRows(t, stock, len(lines)))
	fedRows := byTimestamp(waitRows(t, fed, len(lines)))
	if len(stockRows) != len(lines) {
		t.Fatalf("the lines' timestamps are not each their own: %d rows for %d lines", len(stockRows), len(lines))
	}

	withMsg := 0
	for _, ts := range slices.Sorted(maps.Keys(stockRows)) {
		s, f := maps.Clone(stockRows[ts]), maps.Clone(fedRows[ts])
		if f == nil {
			t.Errorf("the line written at %s reached VictoriaLogs alone, not through the relay", ts)
			continue
		}

		// the message is where the rows differ: the event's msg, moved, in place of VictoriaLogs' note that there is none
		if msg, ok := s["cef.extension.msg"]; ok {
			withMsg++
			if f["_msg"] != msg {
				t.Errorf("%s: _msg = %q through the relay, want the event's msg %q", ts, f["_msg"], msg)
			}
			if !strings.HasPrefix(s["_msg"], "missing _msg field") {
				t.Errorf("%s: VictoriaLogs alone stored _msg = %q; the relay is no longer needed for it", ts, s["_msg"])
			}
			delete(s, "cef.extension.msg")
			delete(s, "_msg")
			delete(f, "_msg")
		}

		// each row is stored at the time it arrived, a moment apart
		delete(s, "_time")
		delete(f, "_time")

		if !maps.Equal(s, f) {
			for _, k := range slices.Sorted(maps.Keys(s)) {
				if s[k] != f[k] {
					t.Errorf("%s: %s = %q from VictoriaLogs alone, %q through the relay", ts, k, s[k], f[k])
				}
			}
			for _, k := range slices.Sorted(maps.Keys(f)) {
				if _, ok := s[k]; !ok {
					t.Errorf("%s: %s = %q through the relay, which VictoriaLogs alone does not store", ts, k, f[k])
				}
			}
		}
	}
	if withMsg == 0 {
		t.Error("no line had a msg to fill the message from")
	}

	page := metricstest.Get(t, "http://"+published(t, nw+"-relay", "9124/tcp")+metrics.Path)
	want := float64(len(lines))
	if got, _ := metricstest.One(t, page, "syslog_relay_lines_received_total", map[string]string{"protocol": "udp"}); got != want {
		t.Errorf("lines received over UDP = %v, want %v", got, want)
	}
	if got, _ := metricstest.One(t, page, "syslog_relay_rows_sent_total", nil); got != want {
		t.Errorf("rows sent = %v, want %v", got, want)
	}

	// the raw copy holds the lines as they were sent, in the order they arrived, and only its owner can read it
	copied := strings.Split(mustDocker(t, "exec", nw+"-relay", "cat", "/tmp/unifi.raw"), "\n")
	if !slices.Equal(slices.Sorted(slices.Values(copied)), slices.Sorted(slices.Values(lines))) {
		t.Errorf("the raw copy holds\n%s\nwant the lines sent", strings.Join(copied, "\n"))
	}
	if got := mustDocker(t, "exec", nw+"-relay", "stat", "-c", "%a %u", "/tmp/unifi.raw"); got != "600 65532" {
		t.Errorf("the raw copy's mode and owner = %q, want 600 65532", got)
	}
}

// The image runs as a user of its own, passes its own health check, and takes lines over TCP as well as UDP.
func TestImage(t *testing.T) {
	nw := network(t)
	fed := startLogs(t, nw, nw+"-fed")
	startRelay(t, nw, nw+"-relay", "fed")

	if uid := mustDocker(t, "exec", nw+"-relay", "id", "-u"); uid != "65532" {
		t.Errorf("the relay runs as uid %s, want 65532 rather than root", uid)
	}

	waitFor(t, "the image's own health check to pass", func() bool {
		status, _ := docker(t, "inspect", "--format", "{{.State.Health.Status}}", nw+"-relay")
		return status == "healthy"
	})

	send(t, "tcp", published(t, nw+"-relay", "1514/tcp"), "<30>Oct 13 19:51:00 cloudkey unifi-core[1234]: sent over TCP")
	if got := waitRows(t, fed, 1); got[0]["_msg"] != "sent over TCP" || got[0]["app_name"] != "unifi-core" {
		t.Errorf("rows = %v, want the line sent over TCP", got)
	}
}

// While VictoriaLogs is away rows are held, and once it is back they reach it.
func TestHeldWhileVictoriaLogsIsAway(t *testing.T) {
	nw := network(t)
	startLogs(t, nw, nw+"-fed")
	startRelay(t, nw, nw+"-relay", "fed")
	pageURL := "http://" + published(t, nw+"-relay", "9124/tcp") + metrics.Path

	mustDocker(t, "stop", nw+"-fed")

	lines := unifi(t)
	sendUDP(t, nw, "relay", "1514", lines...)
	held := func(page string) bool {
		queued, _ := metricstest.One(t, page, "syslog_relay_rows_queued", nil)
		failures, _ := metricstest.One(t, page, "syslog_relay_send_failures_total", nil)

		return queued == float64(len(lines)) && failures > 0
	}
	if page, ok := metricstest.Until(t, pageURL, patience, held); !ok {
		t.Fatalf("the rows were never held while VictoriaLogs was away:\n%s", page)
	}

	mustDocker(t, "start", nw+"-fed")
	// started again, it is published on another port
	waitRows(t, "http://"+published(t, nw+"-fed", "9428/tcp"), len(lines))

	page := metricstest.Get(t, pageURL)
	if got, _ := metricstest.One(t, page, "syslog_relay_rows_queued", nil); got != 0 {
		t.Errorf("rows queued = %v once VictoriaLogs is back, want 0", got)
	}
	if got, _ := metricstest.One(t, page, "syslog_relay_rows_dropped_total", nil); got != 0 {
		t.Errorf("rows dropped = %v, want none", got)
	}
}
