// This file is VictoriaLogs' syslog line reader (syslogLineReader in app/vlinsert/syslog/syslog.go at v1.53.0), so lines are split exactly as
// VictoriaLogs splits them. VictoriaLogs is Copyright VictoriaMetrics, Inc. and licensed under the Apache License 2.0, a copy of which is in
// vendor/github.com/VictoriaMetrics/VictoriaLogs/LICENSE. Changed from the original: written to this repo's lint rules, with its pool left out.

package listen

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
)

// maxLineSize is the longest line a length in front of it may promise, VictoriaLogs' own default for -insert.maxLineSizeBytes.
const maxLineSize = 256 * 1024

// lineReader splits syslog into lines the way VictoriaLogs does. A line that starts with a number and a space is that many bytes long, the way
// syslog over TCP frames them; any other runs to the end of the line.
type lineReader struct {
	line []byte
	br   *bufio.Reader
	err  error
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{br: bufio.NewReaderSize(r, 64*1024)}
}

// reset points the reader at r, keeping its buffers.
func (lr *lineReader) reset(r io.Reader) {
	lr.line = lr.line[:0]
	lr.br.Reset(r)
	lr.err = nil
}

// Err is what stopped the reader: nil when it simply ran out of lines.
func (lr *lineReader) Err() error {
	if errors.Is(lr.err, io.EOF) {
		return nil
	}

	return lr.err
}

// next reads the next line into lr.line, which holds it until the next call. False is no more lines: Err says whether that was a fault.
func (lr *lineReader) next() bool {
	if lr.err != nil {
		return false
	}

	var prefix []byte
	for len(prefix) == 0 {
		var err error
		prefix, err = lr.br.ReadSlice(' ')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				lr.err = fmt.Errorf("cannot read message frame prefix: %w", err)
				return false
			}
			if len(prefix) == 0 {
				lr.err = err
				return false
			}
		}

		// empty lines are skipped
		prefix = bytes.TrimLeft(prefix, "\n")
	}

	if prefix[0] >= '0' && prefix[0] <= '9' {
		// octet counting: https://www.ietf.org/archive/id/draft-gerhards-syslog-plain-tcp-07.html#msgxfer
		sizeStr := string(prefix[:len(prefix)-1])
		size, err := strconv.ParseUint(sizeStr, 10, 64)
		if err != nil {
			lr.err = fmt.Errorf("cannot parse message length from %q: %w", sizeStr, err)
			return false
		}
		if size > maxLineSize {
			lr.err = fmt.Errorf("cannot read message longer than %d bytes; msgLen=%d", maxLineSize, size)
			return false
		}

		lr.line = slices.Grow(lr.line[:0], int(size))[:size]
		if _, err := io.ReadFull(lr.br, lr.line); err != nil {
			lr.err = fmt.Errorf("cannot read message with size %d bytes: %w", size, err)
			return false
		}

		return true
	}

	// octet stuffing: https://www.ietf.org/archive/id/draft-gerhards-syslog-plain-tcp-07.html#octet-stuffing-legacy
	lr.line = append(lr.line[:0], prefix...)
	for {
		rest, err := lr.br.ReadSlice('\n')
		switch {
		case err == nil:
			lr.line = append(lr.line, rest[:len(rest)-1]...)
			return true
		case errors.Is(err, io.EOF):
			lr.line = append(lr.line, rest...)
			return true
		case errors.Is(err, bufio.ErrBufferFull):
			lr.line = append(lr.line, rest...)
		default:
			lr.err = fmt.Errorf("cannot read message in octet-stuffing method: %w", err)
			return false
		}
	}
}

// Lines hands each syslog line in r to each, split as VictoriaLogs splits them. The line is only good until each returns.
func Lines(r io.Reader, each func(line []byte)) error {
	lr := newLineReader(r)
	for lr.next() {
		each(lr.line)
	}

	return lr.Err()
}
