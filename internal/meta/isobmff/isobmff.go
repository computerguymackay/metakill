// Package isobmff provides box-tree helpers for ISO base media files (MP4,
// MOV, M4A, HEIF). Box headers are decoded with github.com/abema/go-mp4.
package isobmff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// Box is one box in the file.
type Box struct {
	Type  [4]byte
	Off   int64 // absolute offset of the box header
	Size  int64 // total size including header
	Hdr   int64 // header size (8 or 16, plus 16 for uuid boxes)
	Large bool  // header uses the 64-bit size field
	ToEOF bool  // size field was 0
	UUID  []byte
	Pre   int64 // payload bytes between the header and the first child
	Kids  []*Box
	// Expanded is true when Kids were parsed.
	Expanded bool
}

// Name returns the box type, rendering 0xA9 as ©.
func (b *Box) Name() string { return TypeName(b.Type[:]) }

// End is the offset just past the box.
func (b *Box) End() int64 { return b.Off + b.Size }

// Payload is the offset of the payload (after the header).
func (b *Box) Payload() int64 { return b.Off + b.Hdr }

// TypeName renders a four-character code, mapping 0xA9 to ©.
func TypeName(t []byte) string {
	if len(t) == 4 && t[0] == 0xA9 {
		return "©" + string(t[1:])
	}
	return string(t)
}

// ExpandFunc decides whether to descend into b, and how many payload bytes
// precede its children. path holds the ancestors.
type ExpandFunc func(r io.ReadSeeker, path []*Box, b *Box) (expand bool, pre int64)

// ErrOverrun is returned when a box claims to extend past its parent.
var ErrOverrun = errors.New("box extends past its parent")

const maxDepth = 32

// Read parses the boxes in [start, end) of r.
func Read(r io.ReadSeeker, start, end int64, expand ExpandFunc) ([]*Box, error) {
	return read(r, start, end, nil, expand)
}

func read(r io.ReadSeeker, start, end int64, path []*Box, expand ExpandFunc) ([]*Box, error) {
	if len(path) > maxDepth {
		return nil, errors.New("boxes nested too deeply")
	}
	var out []*Box
	pos := start
	for end-pos >= 8 {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return nil, fmt.Errorf("box at %d: %w", pos, err)
		}
		b := &Box{Off: pos, Size: int64(bi.Size), Hdr: int64(bi.HeaderSize), ToEOF: bi.ExtendToEOF}
		copy(b.Type[:], bi.Type[:])
		b.Large = bi.HeaderSize == 16
		if b.ToEOF {
			b.Size = end - pos // a nested size-0 box runs to the end of its parent
		}
		if b.Size < b.Hdr || b.End() > end {
			return nil, fmt.Errorf("%s at %d: %w", b.Name(), pos, ErrOverrun)
		}
		if b.Name() == "uuid" {
			if b.Size < b.Hdr+16 {
				return nil, fmt.Errorf("uuid box at %d too small", pos)
			}
			u := make([]byte, 16)
			if _, err := io.ReadFull(r, u); err != nil {
				return nil, err
			}
			b.UUID = u
			b.Hdr += 16
		}
		if expand != nil {
			if ok, pre := expand(r, path, b); ok && b.Hdr+pre <= b.Size {
				b.Pre = pre
				kids, err := read(r, b.Payload()+pre, b.End(), append(path, b), expand)
				if err != nil {
					return nil, err
				}
				b.Kids = kids
				b.Expanded = true
			}
		}
		out = append(out, b)
		pos = b.End()
	}
	return out, nil
}

// ReadPayload reads up to max bytes of b's payload.
func ReadPayload(r io.ReadSeeker, b *Box, max int64) ([]byte, error) {
	n := b.Size - b.Hdr
	if n > max {
		return nil, fmt.Errorf("%s box too large (%d bytes)", b.Name(), n)
	}
	if _, err := r.Seek(b.Payload(), io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

// ReadAll reads up to max bytes of the whole box, header included.
func ReadAll(r io.ReadSeeker, b *Box, max int64) ([]byte, error) {
	if b.Size > max {
		return nil, fmt.Errorf("%s box too large (%d bytes)", b.Name(), b.Size)
	}
	if _, err := r.Seek(b.Off, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, b.Size)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

// Child finds the first direct child with the given type.
func (b *Box) Child(name string) *Box {
	for _, k := range b.Kids {
		if k.Name() == name {
			return k
		}
	}
	return nil
}

// Find follows a path of child types, e.g. Find("mdia", "minf", "stbl").
func (b *Box) Find(names ...string) *Box {
	cur := b
	for _, n := range names {
		if cur = cur.Child(n); cur == nil {
			return nil
		}
	}
	return cur
}

// ---------- in-memory boxes ----------

// MemBox is a box parsed from a byte slice.
type MemBox struct {
	Type    string // with 0xA9 rendered as ©
	Raw     []byte // the full box including header
	Payload []byte
}

// Children parses consecutive boxes in b. Malformed trailing bytes are
// returned as rest.
func Children(b []byte) (boxes []MemBox, rest []byte) {
	for len(b) >= 8 {
		size := int64(binary.BigEndian.Uint32(b))
		hdr := int64(8)
		switch size {
		case 0:
			size = int64(len(b))
		case 1:
			if len(b) < 16 {
				return boxes, b
			}
			size = int64(binary.BigEndian.Uint64(b[8:]))
			hdr = 16
		}
		if size < hdr || size > int64(len(b)) {
			return boxes, b
		}
		boxes = append(boxes, MemBox{Type: TypeName(b[4:8]), Raw: b[:size], Payload: b[hdr:size]})
		b = b[size:]
	}
	return boxes, b
}

// Make builds a box with a 32-bit header. typ may contain ©.
func Make(typ string, payload ...[]byte) []byte {
	t := []byte(typ)
	if len(typ) > 0 && typ[0] != 0xA9 && len(t) == 5 && t[0] == 0xC2 && t[1] == 0xA9 { // "©xyz" as UTF-8
		t = append([]byte{0xA9}, t[2:]...)
	}
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out, uint32(n))
	copy(out[4:], t)
	for _, p := range payload {
		out = append(out, p...)
	}
	return out
}

// MetaChildrenOffset returns how many payload bytes precede the children of a
// 'meta' box: 4 for ISO (FullBox) meta, 0 for QuickTime meta.
func MetaChildrenOffset(payload []byte) int {
	if len(payload) >= 8 && string(payload[4:8]) == "hdlr" {
		return 0
	}
	return 4
}
