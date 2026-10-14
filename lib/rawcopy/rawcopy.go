// Package rawcopy keeps the syslog lines the relay receives, unchanged and one to a line, in a file that stops growing at a size it is given, so
// they can be sent again later: to a stock VictoriaLogs and to a patched one, say, to compare the two. What UniFi sends names people and devices
// and holds their IP and MAC addresses, so the file is only ever readable by its owner.
package rawcopy

import (
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/katbyte/go-kt/clog"
)

// File is the copy being written. Its methods are safe to call from many goroutines.
type File struct {
	mu   sync.Mutex
	f    *os.File
	path string
	left int64
	done bool
}

// Open adds to the file at path, making it when it is not there, and writes until it holds size bytes. A file already that full is opened and
// left as it is.
func Open(path string, size int64) (*File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the path is the one the settings name
	if err != nil {
		return nil, fmt.Errorf("opening the raw copy: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("reading the size of the raw copy %s: %w", path, err)
	}

	clog.Log.Infof("keeping a raw copy of every line received in %s, up to %d bytes", path, size)

	return &File{f: f, path: path, left: size - info.Size()}, nil
}

// Write adds the line, and a newline after it, unless that would take the file past its size. Once one line does not fit no more are written, so
// the copy is never cut off part way into a run of lines.
func (c *File) Write(line []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.done {
		return
	}

	if int64(len(line))+1 > c.left {
		c.done = true
		clog.Log.Warnf("the raw copy %s is full: no more lines are written to it", c.path)

		return
	}

	if _, err := c.f.Write(append(slices.Clip(line), '\n')); err != nil {
		c.done = true
		clog.Log.Errorf("writing the raw copy %s, which is now stopped: %v", c.path, err)

		return
	}

	c.left -= int64(len(line)) + 1
}

// Close closes the file.
func (c *File) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.done = true

	return c.f.Close()
}
