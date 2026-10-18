package metricstest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const (
	// starting is how long a tool that has just been started is given to answer at all.
	starting = 5 * time.Second
	// between is how long to wait before asking again.
	between = 50 * time.Millisecond
)

// Get reads url from a tool that is serving, trying again for a few seconds while it starts. An answer other than 200 OK fails the test.
func Get(t *testing.T, url string) string {
	t.Helper()

	body, err := fetch(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

// fetch is Get, with what went wrong as an error.
func fetch(ctx context.Context, url string) (string, error) {
	var last error
	for deadline := time.Now().Add(starting); time.Now().Before(deadline); time.Sleep(between) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
		if err != nil {
			return "", err
		}

		res, err := http.DefaultClient.Do(req)
		if err != nil {
			last = err
			continue
		}

		body, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", url, err)
		}

		if res.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s answered %s:\n%s", url, res.Status, body)
		}

		return string(body), nil
	}

	return "", fmt.Errorf("nothing answered at %s within %s: %w", url, starting, last)
}

// Until reads the page at url until ok says it is what the test waits for, or until within has passed. It gives back the page as it last was and
// whether it got there, and leaves the test to say what the page lacks.
func Until(t *testing.T, url string, within time.Duration, ok func(page string) bool) (string, bool) {
	t.Helper()

	for deadline := time.Now().Add(within); ; time.Sleep(between * 5) {
		page := Get(t, url)
		if ok(page) {
			return page, true
		}

		if time.Now().After(deadline) {
			return page, false
		}
	}
}

// Reading is one reading on a page: its labels, the ones on every reading among them, and its value.
type Reading struct {
	Labels map[string]string
	Value  float64
}

// Readings is every reading of this name on the page. A page that cannot be read fails the test.
func Readings(t *testing.T, page, name string) []Reading {
	t.Helper()

	readings, err := parse(page, name)
	if err != nil {
		t.Fatal(err)
	}

	return readings
}

// One is the value of the one reading of this name whose labels include these, and whether there is exactly one.
func One(t *testing.T, page, name string, labels map[string]string) (float64, bool) {
	t.Helper()

	var found []float64
	for _, r := range Readings(t, page, name) {
		if includes(r.Labels, labels) {
			found = append(found, r.Value)
		}
	}

	if len(found) != 1 {
		return 0, false
	}

	return found[0], true
}

// includes says whether every label in want is in labels, with the same value.
func includes(labels, want map[string]string) bool {
	for name, value := range want {
		if got, found := labels[name]; !found || got != value {
			return false
		}
	}

	return true
}

// parse is the readings of this name on the page, read as Prometheus reads a page.
func parse(page, name string) ([]Reading, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)

	families, err := parser.TextToMetricFamilies(strings.NewReader(page))
	if err != nil {
		return nil, fmt.Errorf("reading the page: %w", err)
	}

	var out []Reading
	for _, m := range families[name].GetMetric() {
		r := Reading{Labels: map[string]string{}}
		for _, l := range m.GetLabel() {
			r.Labels[l.GetName()] = l.GetValue()
		}

		switch {
		case m.GetGauge() != nil:
			r.Value = m.GetGauge().GetValue()
		case m.GetCounter() != nil:
			r.Value = m.GetCounter().GetValue()
		case m.GetUntyped() != nil:
			r.Value = m.GetUntyped().GetValue()
		default:
			continue
		}

		out = append(out, r)
	}

	return out, nil
}
