package rawcopy

import (
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path) //nolint:gosec // a file the test made
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func TestWrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "unifi.raw")
	c, err := Open(path, 1024)
	if err != nil {
		t.Fatal(err)
	}

	c.Write([]byte("Oct 13 nvr CEF:0|one"))
	c.Write([]byte("Oct 13 nvr CEF:0|two"))
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	if got, want := read(t, path), "Oct 13 nvr CEF:0|one\nOct 13 nvr CEF:0|two\n"; got != want {
		t.Errorf("the copy holds %q, want %q", got, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the copy can be read as %v, want by its owner alone", info.Mode().Perm())
	}

	// written to after closing, nothing happens
	c.Write([]byte("late"))
	if got := read(t, path); got != "Oct 13 nvr CEF:0|one\nOct 13 nvr CEF:0|two\n" {
		t.Errorf("the copy holds %q after closing", got)
	}
}

// The copy stops at its size, at the end of a line, and stays stopped though a shorter line would fit.
func TestFull(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "unifi.raw")
	c, err := Open(path, 12)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	c.Write([]byte("12345"))   // 6 bytes
	c.Write([]byte("1234567")) // 8 more would be 14
	c.Write([]byte("1"))       // would fit, but the copy has stopped

	if got := read(t, path); got != "12345\n" {
		t.Errorf("the copy holds %q, want only the line that fitted", got)
	}
}

// Opened again, it adds to what is there and counts it towards its size.
func TestReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "unifi.raw")
	if err := os.WriteFile(path, []byte("12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Open(path, 12)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	c.Write([]byte("abcde"))
	c.Write([]byte("f"))

	if got := read(t, path); got != "12345\nabcde\n" {
		t.Errorf("the copy holds %q, want what was there and one more line", got)
	}
}

func TestOpenFails(t *testing.T) {
	t.Parallel()

	if _, err := Open(filepath.Join(t.TempDir(), "missing", "unifi.raw"), 12); err == nil {
		t.Error("Open in a directory that is not there gave no error")
	}
}
