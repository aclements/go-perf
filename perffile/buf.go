// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package perffile

import (
	"errors"
	"io"
)

// bufferedSectionReader is a buffered io.SectionReader with offset
// tracking.
//
// This is based on bufio.Reader. This could apply to an arbitrary
// io.Reader, but it's specialized for our one current use so the
// linker can statically resolve the method calls.
type bufferedSectionReader struct {
	buf  []byte
	rd   *io.SectionReader
	r, w int // buf read and write positions
	err  error
	pos  int64 // file position of read
}

func newBufferedSectionReader(rd *io.SectionReader) *bufferedSectionReader {
	pos, err := rd.Seek(0, 1)
	return &bufferedSectionReader{
		buf: make([]byte, 16<<10),
		rd:  rd,
		pos: pos,
		err: err,
	}
}

var errNegativeRead = errors.New("reader returned negative count from Read")

func (b *bufferedSectionReader) readErr() error {
	err := b.err
	b.err = nil
	return err
}

func (b *bufferedSectionReader) Seek(offset int64, whence int) (int64, error) {
	if whence == 0 && offset == b.pos || whence == 1 && offset == 0 {
		return b.pos, nil
	}

	var err error
	b.pos, err = b.rd.Seek(offset, whence)
	if err == nil {
		b.r, b.w = 0, 0
	}
	return b.pos, err
}

func (b *bufferedSectionReader) Read(p []byte) (n int, err error) {
	n = len(p)
	if n == 0 {
		return 0, b.readErr()
	}
	if b.r == b.w {
		if b.err != nil {
			return 0, b.readErr()
		}
		if len(p) >= len(b.buf) {
			// Large read, empty buffer.
			// Read directly into p to avoid copy.
			n, b.err = b.rd.Read(p)
			if n < 0 {
				panic(errNegativeRead)
			}
			b.pos += int64(n)
			return n, b.readErr()
		}
		b.fill() // buffer is empty
		if b.r == b.w {
			return 0, b.readErr()
		}
	}

	// copy as much as we can
	n = copy(p, b.buf[b.r:b.w])
	b.r += n
	b.pos += int64(n)
	return n, nil
}

// fill reads a new chunk into the buffer.
func (b *bufferedSectionReader) fill() {
	// Slide existing data to beginning.
	if b.r > 0 {
		copy(b.buf, b.buf[b.r:b.w])
		b.w -= b.r
		b.r = 0
	}

	if b.w >= len(b.buf) {
		panic("tried to fill full buffer")
	}

	// Read new data: try a limited number of times.
	for i := 0; i < 100; i++ {
		n, err := b.rd.Read(b.buf[b.w:])
		if n < 0 {
			panic(errNegativeRead)
		}
		b.w += n
		if err != nil {
			b.err = err
			return
		}
		if n > 0 {
			return
		}
	}
	b.err = io.ErrNoProgress
}
