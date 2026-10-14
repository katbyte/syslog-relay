package listen

import (
	"strings"
	"testing"
)

// split is every line Lines finds in s, and what stopped it.
func split(s string) ([]string, error) {
	var got []string
	err := Lines(strings.NewReader(s), func(line []byte) { got = append(got, string(line)) })

	return got, err
}

func TestLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"one to a line", "Oct 13 nvr a: one\nOct 13 nvr a: two\n", []string{"Oct 13 nvr a: one", "Oct 13 nvr a: two"}},
		{"the last without a newline", "Oct 13 nvr a: one\nOct 13 nvr a: two", []string{"Oct 13 nvr a: one", "Oct 13 nvr a: two"}},
		{"empty lines skipped", "\n\nOct 13 nvr a: one\n\n\nOct 13 nvr a: two\n\n", []string{"Oct 13 nvr a: one", "Oct 13 nvr a: two"}},
		{"counted, as syslog over TCP frames them", "17 Oct 13 nvr a: one17 Oct 13 nvr a: two", []string{"Oct 13 nvr a: one", "Oct 13 nvr a: two"}},
		{"a counted line can hold a newline", "17 Oct 13 nvr a: o\ne", []string{"Oct 13 nvr a: o\ne"}},
		{"counted and not, mixed", "17 Oct 13 nvr a: one\n<14>Oct 13 nvr a: two\n", []string{"Oct 13 nvr a: one", "<14>Oct 13 nvr a: two"}},
		{"nothing", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := split(tc.in)
			if err != nil {
				t.Fatalf("Lines: %v", err)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") || len(got) != len(tc.want) {
				t.Errorf("Lines = %q, want %q", got, tc.want)
			}
		})
	}
}

// What VictoriaLogs cannot read as syslog stops the reading, with the lines before it handed on.
func TestLinesFaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"a count that is not a number", "Oct 13 nvr a: one\n12x Oct 13 nvr a: two", []string{"Oct 13 nvr a: one"}},
		{"a line cut off before its count", "40 Oct 13 nvr a: one", nil},
		{"a count past the longest line", "999999999 Oct", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := split(tc.in)
			if err == nil {
				t.Fatalf("Lines = %q and no error, want one", got)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("Lines = %q before the error, want %q", got, tc.want)
			}
		})
	}
}

// A line longer than the reader's buffer is read whole.
func TestLinesLong(t *testing.T) {
	t.Parallel()

	long := "Oct 13 nvr a: " + strings.Repeat("x", 200*1024)

	got, err := split(long + "\nOct 13 nvr a: next\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != long {
		t.Errorf("Lines gave %d lines, the first %d bytes long, want 2 and %d", len(got), len(got[0]), len(long))
	}
}
