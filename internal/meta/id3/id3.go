// Package id3 parses ID3v1 and ID3v2.2/2.3/2.4 tags and writes minimal
// ID3v2.4 tags (used to re-emit the tags kept by -k).
package id3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/report"
)

// Frame is one decoded ID3v2 frame (payload after de-unsynchronisation).
type Frame struct {
	ID   string
	Data []byte
}

// Tag is a parsed ID3v2 tag.
type Tag struct {
	Major  byte
	Size   int // total bytes on disk including header and footer
	Frames []Frame
}

// HeaderSize is the length of an ID3v2 header.
const HeaderSize = 10

// TagSize returns the total size of the ID3v2 tag whose 10-byte header is h.
func TagSize(h []byte) (int, bool) {
	if len(h) < HeaderSize || string(h[:3]) != "ID3" || h[3] < 2 || h[3] > 4 {
		return 0, false
	}
	for _, c := range h[6:10] {
		if c&0x80 != 0 {
			return 0, false
		}
	}
	n := HeaderSize + syncsafe(h[6:10])
	if h[3] == 4 && h[5]&0x10 != 0 {
		n += HeaderSize // footer
	}
	return n, true
}

func syncsafe(b []byte) int {
	return int(b[0])<<21 | int(b[1])<<14 | int(b[2])<<7 | int(b[3])
}

func putSyncsafe(b []byte, n int) {
	b[0] = byte(n>>21) & 0x7F
	b[1] = byte(n>>14) & 0x7F
	b[2] = byte(n>>7) & 0x7F
	b[3] = byte(n) & 0x7F
}

func deunsync(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte{0xFF, 0x00}, []byte{0xFF})
}

// Parse decodes a complete ID3v2 tag (b must hold at least TagSize bytes).
func Parse(b []byte) (*Tag, error) {
	size, ok := TagSize(b)
	if !ok {
		return nil, errors.New("id3: bad header")
	}
	if size > len(b) {
		return nil, errors.New("id3: truncated tag")
	}
	t := &Tag{Major: b[3], Size: size}
	flags := b[5]
	body := b[HeaderSize : HeaderSize+syncsafe(b[6:10])]
	if flags&0x80 != 0 && t.Major < 4 {
		body = deunsync(body)
	}
	if flags&0x40 != 0 && t.Major >= 3 { // extended header
		if len(body) < 4 {
			return t, nil
		}
		var skip int
		if t.Major == 3 {
			skip = 4 + int(binary.BigEndian.Uint32(body))
		} else {
			skip = syncsafe(body[:4])
		}
		if skip > len(body) {
			return t, nil
		}
		body = body[skip:]
	}
	idLen, hdrLen := 4, 10
	if t.Major == 2 {
		idLen, hdrLen = 3, 6
	}
	for len(body) >= hdrLen {
		id := string(body[:idLen])
		if body[0] == 0 || !validID(id) {
			break // padding
		}
		var n int
		var fflags uint16
		switch t.Major {
		case 2:
			n = int(body[3])<<16 | int(body[4])<<8 | int(body[5])
		case 3:
			n = int(binary.BigEndian.Uint32(body[4:]))
			fflags = binary.BigEndian.Uint16(body[8:])
		default:
			n = syncsafe(body[4:8])
			fflags = binary.BigEndian.Uint16(body[8:])
		}
		if n < 0 || hdrLen+n > len(body) {
			break
		}
		data := body[hdrLen : hdrLen+n]
		body = body[hdrLen+n:]
		if t.Major == 4 {
			if fflags&0x0001 != 0 && len(data) >= 4 { // data length indicator
				data = data[4:]
			}
			if fflags&0x0002 != 0 {
				data = deunsync(data)
			}
		}
		if (t.Major == 3 && fflags&0x00C0 != 0) || (t.Major == 4 && fflags&0x000C != 0) {
			id += " (compressed/encrypted)"
		}
		t.Frames = append(t.Frames, Frame{ID: id, Data: data})
	}
	return t, nil
}

func validID(id string) bool {
	for _, c := range id {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// DecodeText decodes an ID3 encoded string (encoding byte + payload).
func DecodeText(enc byte, b []byte) string {
	switch enc {
	case 1, 2:
		order := binary.ByteOrder(binary.BigEndian)
		if enc == 1 && len(b) >= 2 {
			if b[0] == 0xFF && b[1] == 0xFE {
				order = binary.LittleEndian
				b = b[2:]
			} else if b[0] == 0xFE && b[1] == 0xFF {
				b = b[2:]
			}
		}
		u := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			v := order.Uint16(b[i:])
			if v == 0xFEFF {
				continue // BOM of a following value
			}
			u = append(u, v)
		}
		return string(utf16.Decode(u))
	case 3:
		return string(b)
	default:
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		return string(r)
	}
}

// splitTerm splits b at the first string terminator for the encoding.
func splitTerm(enc byte, b []byte) ([]byte, []byte) {
	if enc == 1 || enc == 2 {
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return b[:i], b[i+2:]
			}
		}
		return b, nil
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		return b[:i], b[i+1:]
	}
	return b, nil
}

func clean(s string) string {
	s = strings.TrimRight(s, "\x00")
	return strings.ReplaceAll(s, "\x00", "; ")
}

// Picture is an attached picture frame.
type Picture struct {
	MIME string
	Type byte
	Data []byte
}

// Info is what a frame contributes to a report.
type Info struct {
	Field     report.Field
	Canonical string // "title", "artist", ... for essential frames
	Value     string // decoded text value
	Picture   *Picture
}

// Describe decodes a frame into report information.
func Describe(f Frame, major byte) Info {
	id := f.ID
	cat, canon := classify.ID3(id)
	d := f.Data
	ns := fmt.Sprintf("id3v2.%d", major)
	switch {
	case strings.Contains(id, " "):
		return Info{Field: report.Binary(cat, ns, id, int64(len(d)))}
	case id == "TXXX" || id == "TXX":
		if len(d) < 1 {
			break
		}
		desc, val := splitTerm(d[0], d[1:])
		key := id + ":" + DecodeText(d[0], desc)
		c, _ := classify.Vorbis(DecodeText(d[0], desc))
		return Info{Field: report.Text(c, ns, key, clean(DecodeText(d[0], val)))}
	case id[0] == 'T':
		if len(d) < 1 {
			break
		}
		v := clean(DecodeText(d[0], d[1:]))
		return Info{Field: report.Text(cat, ns, id, v), Canonical: canon, Value: v}
	case id == "COMM" || id == "COM" || id == "USLT" || id == "ULT":
		if len(d) < 4 {
			break
		}
		_, text := splitTerm(d[0], d[4:])
		return Info{Field: report.Text(cat, ns, id, clean(DecodeText(d[0], text)))}
	case id == "WXXX" || id == "WXX":
		if len(d) < 1 {
			break
		}
		_, u := splitTerm(d[0], d[1:])
		return Info{Field: report.Text(cat, ns, id, clean(DecodeText(0, u)))}
	case id[0] == 'W':
		return Info{Field: report.Text(cat, ns, id, clean(string(d)))}
	case id == "APIC" || id == "PIC":
		p := parsePicture(id, d)
		if p == nil {
			break
		}
		return Info{Field: report.Binary(report.CatThumbnail, ns, id, int64(len(p.Data))), Picture: p}
	case id == "PRIV" || id == "UFID" || id == "UFI" || id == "GEOB" || id == "GEO":
		owner := d
		if i := bytes.IndexByte(d, 0); i >= 0 {
			owner = d[:i]
		}
		if id == "GEOB" || id == "GEO" {
			owner = nil
		}
		key := id
		if len(owner) > 0 && report.Printable(string(owner)) {
			key += ":" + string(owner)
		}
		return Info{Field: report.Binary(cat, ns, key, int64(len(d)))}
	}
	return Info{Field: report.Binary(cat, ns, id, int64(len(d)))}
}

func parsePicture(id string, d []byte) *Picture {
	if len(d) < 2 {
		return nil
	}
	enc := d[0]
	p := &Picture{}
	rest := d[1:]
	if id == "PIC" {
		if len(rest) < 4 {
			return nil
		}
		switch strings.ToUpper(string(rest[:3])) {
		case "PNG":
			p.MIME = "image/png"
		default:
			p.MIME = "image/jpeg"
		}
		rest = rest[3:]
	} else {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return nil
		}
		p.MIME = string(rest[:i])
		rest = rest[i+1:]
	}
	if len(rest) < 1 {
		return nil
	}
	p.Type = rest[0]
	_, data := splitTerm(enc, rest[1:])
	p.Data = data
	return p
}

// V1 is a parsed 128-byte ID3v1 tag.
type V1 struct {
	Title, Artist, Album, Year, Comment string
	Track                               int
	Genre                               byte
}

// ParseV1 decodes a 128-byte "TAG" block.
func ParseV1(b []byte) (V1, bool) {
	if len(b) != 128 || string(b[:3]) != "TAG" {
		return V1{}, false
	}
	s := func(x []byte) string {
		if i := bytes.IndexByte(x, 0); i >= 0 {
			x = x[:i]
		}
		return strings.TrimSpace(DecodeText(0, x))
	}
	v := V1{Title: s(b[3:33]), Artist: s(b[33:63]), Album: s(b[63:93]), Year: s(b[93:97]), Genre: b[127]}
	if b[125] == 0 && b[126] != 0 {
		v.Track = int(b[126])
		v.Comment = s(b[97:125])
	} else {
		v.Comment = s(b[97:127])
	}
	return v, true
}

// Fields lists the populated ID3v1 fields.
func (v V1) Fields() []report.Field {
	var out []report.Field
	add := func(cat report.Category, k, val string) {
		if val != "" {
			out = append(out, report.Text(cat, "id3v1", k, val))
		}
	}
	add(report.CatIDTag, "Title", v.Title)
	add(report.CatIDTag, "Artist", v.Artist)
	add(report.CatIDTag, "Album", v.Album)
	add(report.CatTimestamp, "Year", v.Year)
	add(report.CatCustom, "Comment", v.Comment)
	if v.Track > 0 {
		add(report.CatIDTag, "Track", fmt.Sprint(v.Track))
	}
	if len(out) == 0 {
		out = append(out, report.Binary(report.CatCustom, "id3v1", "ID3v1 (empty)", 128))
	}
	return out
}

// Kept holds the essential values to re-emit with -k.
type Kept struct {
	Title, Artist, Album, Track, Year string
	Picture                           *Picture
}

// Set stores a canonical value if not already set.
func (k *Kept) Set(canonical, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	switch canonical {
	case classify.Title:
		if k.Title == "" {
			k.Title = value
		}
	case classify.Artist:
		if k.Artist == "" {
			k.Artist = value
		}
	case classify.Album:
		if k.Album == "" {
			k.Album = value
		}
	case classify.Track:
		if k.Track == "" {
			k.Track = value
		}
	case classify.Year:
		if k.Year == "" {
			k.Year = value
		}
	}
}

// Empty reports whether nothing is kept.
func (k *Kept) Empty() bool {
	return k.Title == "" && k.Artist == "" && k.Album == "" && k.Track == "" && k.Year == "" && k.Picture == nil
}

// EncodeV24 writes a minimal ID3v2.4 tag holding the kept values. If padTo is
// larger than the natural size, zero padding is added to reach it.
func EncodeV24(k Kept, padTo int) []byte {
	var frames []byte
	text := func(id, v string) {
		if v == "" {
			return
		}
		frames = appendFrame(frames, id, append([]byte{3}, v...))
	}
	text("TIT2", k.Title)
	text("TPE1", k.Artist)
	text("TALB", k.Album)
	text("TRCK", k.Track)
	text("TDRC", k.Year)
	if k.Picture != nil {
		d := []byte{3}
		d = append(d, k.Picture.MIME...)
		d = append(d, 0, k.Picture.Type, 0)
		d = append(d, k.Picture.Data...)
		frames = appendFrame(frames, "APIC", d)
	}
	size := len(frames)
	if padTo-HeaderSize > size {
		size = padTo - HeaderSize
	}
	out := make([]byte, HeaderSize, HeaderSize+size)
	copy(out, "ID3\x04\x00\x00")
	putSyncsafe(out[6:], size)
	out = append(out, frames...)
	return append(out, make([]byte, size-len(frames))...)
}

func appendFrame(out []byte, id string, data []byte) []byte {
	h := make([]byte, 10)
	copy(h, id)
	putSyncsafe(h[4:], len(data))
	out = append(out, h...)
	return append(out, data...)
}
