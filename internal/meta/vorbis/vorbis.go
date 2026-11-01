// Package vorbis parses and writes Vorbis comment blocks (used by FLAC,
// Ogg Vorbis, Opus, Theora and Speex) and FLAC PICTURE blocks.
package vorbis

import (
	"encoding/binary"
	"errors"
	"strings"
)

// Comment is one KEY=value entry.
type Comment struct {
	Key, Value string
}

// Block is a parsed comment block.
type Block struct {
	Vendor   string
	Comments []Comment
}

// ErrTruncated is returned for malformed blocks.
var ErrTruncated = errors.New("vorbis comment block truncated")

// Parse decodes a comment block (little-endian lengths). It returns the
// number of bytes consumed so callers can find trailing data (e.g. the
// Vorbis framing bit).
func Parse(b []byte) (Block, int, error) {
	var blk Block
	p := 0
	read := func() (string, error) {
		if p+4 > len(b) {
			return "", ErrTruncated
		}
		n := int(binary.LittleEndian.Uint32(b[p:]))
		p += 4
		if n < 0 || p+n > len(b) {
			return "", ErrTruncated
		}
		s := string(b[p : p+n])
		p += n
		return s, nil
	}
	v, err := read()
	if err != nil {
		return blk, 0, err
	}
	blk.Vendor = v
	if p+4 > len(b) {
		return blk, 0, ErrTruncated
	}
	count := int(binary.LittleEndian.Uint32(b[p:]))
	p += 4
	for i := 0; i < count; i++ {
		s, err := read()
		if err != nil {
			return blk, 0, err
		}
		k, val, ok := strings.Cut(s, "=")
		if !ok {
			k, val = s, ""
		}
		blk.Comments = append(blk.Comments, Comment{Key: k, Value: val})
	}
	return blk, p, nil
}

// Encode serialises a comment block.
func Encode(blk Block) []byte {
	var out []byte
	put := func(s string) {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(s)))
		out = append(out, s...)
	}
	put(blk.Vendor)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(blk.Comments)))
	for _, c := range blk.Comments {
		put(c.Key + "=" + c.Value)
	}
	return out
}

// Picture is a FLAC PICTURE block (also used base64-encoded in
// METADATA_BLOCK_PICTURE comments).
type Picture struct {
	Type                         uint32
	MIME, Description            string
	Width, Height, Depth, Colors uint32
	Data                         []byte
}

// ParsePicture decodes a PICTURE block body (big-endian).
func ParsePicture(b []byte) (Picture, error) {
	var p Picture
	pos := 0
	u32 := func() (uint32, bool) {
		if pos+4 > len(b) {
			return 0, false
		}
		v := binary.BigEndian.Uint32(b[pos:])
		pos += 4
		return v, true
	}
	str := func() (string, bool) {
		n, ok := u32()
		if !ok || pos+int(n) > len(b) {
			return "", false
		}
		s := string(b[pos : pos+int(n)])
		pos += int(n)
		return s, true
	}
	var ok bool
	if p.Type, ok = u32(); !ok {
		return p, ErrTruncated
	}
	if p.MIME, ok = str(); !ok {
		return p, ErrTruncated
	}
	if p.Description, ok = str(); !ok {
		return p, ErrTruncated
	}
	for _, f := range []*uint32{&p.Width, &p.Height, &p.Depth, &p.Colors} {
		if *f, ok = u32(); !ok {
			return p, ErrTruncated
		}
	}
	n, ok := u32()
	if !ok || pos+int(n) > len(b) {
		return p, ErrTruncated
	}
	p.Data = b[pos : pos+int(n)]
	return p, nil
}

// EncodePicture serialises a PICTURE block body.
func EncodePicture(p Picture) []byte {
	var out []byte
	u32 := func(v uint32) { out = binary.BigEndian.AppendUint32(out, v) }
	u32(p.Type)
	u32(uint32(len(p.MIME)))
	out = append(out, p.MIME...)
	u32(uint32(len(p.Description)))
	out = append(out, p.Description...)
	u32(p.Width)
	u32(p.Height)
	u32(p.Depth)
	u32(p.Colors)
	u32(uint32(len(p.Data)))
	return append(out, p.Data...)
}
