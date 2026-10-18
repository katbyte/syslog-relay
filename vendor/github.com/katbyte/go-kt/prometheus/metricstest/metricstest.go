// Package metricstest is the checks a tool's tests make of its metrics page: that the page is the one saved last time, that its names follow
// Prometheus's rules, and what a running tool's page says.
//
// The package adds a -update flag to the tests that use it, which rewrites each saved page: a tool's tests do not declare their own.
package metricstest

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil/promlint"

	"github.com/katbyte/go-kt/pointer"
)

// buildInfo ends the name of the reading every page has that says which build wrote it, which changes with each build.
const buildInfo = "_build_info"

var update = flag.Bool("update", false, "rewrite each saved metrics page from the page as it is written now")

// Saved fails the test when the page is not the one saved in file. The page is what dashboards and alerts are written against, so a renamed reading
// or a changed label shows here first. The line saying which build wrote the page is left out. go test -update rewrites the file; read the difference
// before keeping it.
func Saved(t *testing.T, page, file string) {
	t.Helper()

	if pointer.From(update) {
		if err := save(page, file); err != nil {
			t.Fatal(err)
		}
	}

	if err := compare(page, file); err != nil {
		t.Error(err)
	}
}

// save writes the page to file as Saved keeps it.
func save(page, file string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err != nil {
		return fmt.Errorf("rewriting %s: %w", file, err)
	}

	if err := os.WriteFile(file, []byte(withoutBuild(page)), 0o600); err != nil {
		return fmt.Errorf("rewriting %s: %w", file, err)
	}

	return nil
}

// compare is Saved, with what is wrong as an error.
func compare(page, file string) error {
	page = withoutBuild(page)

	saved, err := os.ReadFile(file) //nolint:gosec // G304: the file is the one the test names for its saved page
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no page is saved in %s: go test -update writes it", file)
	}
	if err != nil {
		return fmt.Errorf("reading the saved page: %w", err)
	}

	if string(saved) == page {
		return nil
	}

	return fmt.Errorf("the page is not the one saved in %s; if the change is meant, go test -update rewrites it\n%s", file, difference(string(saved), page))
}

// withoutBuild is the page less the reading that says which build wrote it.
func withoutBuild(page string) string {
	var b strings.Builder
	for line := range strings.Lines(page) {
		name := line
		if rest, found := strings.CutPrefix(line, "# HELP "); found {
			name = rest
		} else if rest, found := strings.CutPrefix(line, "# TYPE "); found {
			name = rest
		}

		name, _, _ = strings.Cut(name, " ")
		name, _, _ = strings.Cut(name, "{")
		if !strings.HasSuffix(name, buildInfo) {
			b.WriteString(line)
		}
	}

	return b.String()
}

// difference is the lines saved that the page no longer has, marked -, and the lines the page has that were not saved, marked +.
func difference(saved, page string) string {
	left := map[string]int{}
	for line := range strings.Lines(saved) {
		left[strings.TrimSuffix(line, "\n")]++
	}

	var added []string
	for line := range strings.Lines(page) {
		line = strings.TrimSuffix(line, "\n")
		if left[line] > 0 {
			left[line]--
			continue
		}

		added = append(added, "+ "+line)
	}

	var gone []string
	for line := range strings.Lines(saved) {
		line = strings.TrimSuffix(line, "\n")
		if left[line] > 0 {
			left[line]--
			gone = append(gone, "- "+line)
		}
	}

	if len(gone)+len(added) == 0 {
		return "the same lines, in another order"
	}

	return strings.Join(slices.Concat(gone, added), "\n")
}

// Allowed is one complaint the naming check may make of one reading and let pass, for a reading named as it is on purpose.
type Allowed struct {
	// Reading is the reading's name, or the end of it for every reading so named: sas_phy_link_rate_gigabits_per_second, or _gigabits_per_second.
	Reading string
	// Problem is some of the words of the complaint: `instead of "gigabits"`. Left empty, every complaint about the reading passes.
	Problem string
}

// lets says whether the complaint is one this lets pass.
func (a Allowed) lets(p promlint.Problem) bool {
	return strings.HasSuffix(p.Metric, a.Reading) && strings.Contains(p.Text, a.Problem)
}

// Lint fails the test for each way the page's names break Prometheus's rules, but those allowed: a unit at the end of a name, _total on a total,
// help on every reading.
func Lint(t *testing.T, page string, allowed ...Allowed) {
	t.Helper()

	problems, err := complaints(page, allowed)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range problems {
		t.Error(p)
	}
}

// complaints is Lint, with each complaint as a line.
func complaints(page string, allowed []Allowed) ([]string, error) {
	problems, err := promlint.New(strings.NewReader(page)).Lint()
	if err != nil {
		return nil, fmt.Errorf("reading the page: %w", err)
	}

	var out []string
	for _, p := range problems {
		if !slices.ContainsFunc(allowed, func(a Allowed) bool { return a.lets(p) }) {
			out = append(out, p.Metric+": "+p.Text)
		}
	}

	return out, nil
}
