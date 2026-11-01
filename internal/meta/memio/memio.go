// Package memio provides an in-memory io.WriteSeeker.
package memio

import (
	"errors"
	"io"
)

// Buffer is a growable byte slice implementing io.Writer, io.WriterAt,
// io.Seeker and io.Reader.
type Buffer struct {
	buf []byte
	pos int64
}

// Write writes at the current position, growing the buffer as needed.
func (b *Buffer) Write(p []byte) (int, error) {
	n, err := b.WriteAt(p, b.pos)
	b.pos += int64(n)
	return n, err
}

// WriteAt writes p at off without moving the position.
func (b *Buffer) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("memio: negative offset")
	}
	end := off + int64(len(p))
	if end > int64(len(b.buf)) {
		if end > int64(cap(b.buf)) {
			nb := make([]byte, end, end*2)
			copy(nb, b.buf)
			b.buf = nb
		} else {
			b.buf = b.buf[:end]
		}
	}
	copy(b.buf[off:], p)
	return len(p), nil
}

// Read reads from the current position.
func (b *Buffer) Read(p []byte) (int, error) {
	if b.pos >= int64(len(b.buf)) {
		return 0, io.EOF
	}
	n := copy(p, b.buf[b.pos:])
	b.pos += int64(n)
	return n, nil
}

// Seek implements io.Seeker.
func (b *Buffer) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = b.pos + offset
	case io.SeekEnd:
		abs = int64(len(b.buf)) + offset
	default:
		return 0, errors.New("memio: bad whence")
	}
	if abs < 0 {
		return 0, errors.New("memio: negative position")
	}
	b.pos = abs
	return abs, nil
}

// Bytes returns the written contents.
func (b *Buffer) Bytes() []byte { return b.buf }

// Len returns the number of bytes written.
func (b *Buffer) Len() int { return len(b.buf) }
