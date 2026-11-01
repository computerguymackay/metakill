// Package exif reads and writes TIFF-structured metadata (EXIF in JPEG, PNG,
// WebP and HEIF, and TIFF files themselves).
package exif

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/report"
)

// TIFF field types.
const (
	TypeByte      = 1
	TypeASCII     = 2
	TypeShort     = 3
	TypeLong      = 4
	TypeRational  = 5
	TypeSByte     = 6
	TypeUndefined = 7
	TypeSShort    = 8
	TypeSLong     = 9
	TypeSRational = 10
	TypeFloat     = 11
	TypeDouble    = 12
	TypeIFD       = 13
)

// TypeSize returns the byte size of one value of type t (0 if unknown).
func TypeSize(t uint16) int {
	switch t {
	case TypeByte, TypeASCII, TypeSByte, TypeUndefined:
		return 1
	case TypeShort, TypeSShort:
		return 2
	case TypeLong, TypeSLong, TypeFloat, TypeIFD:
		return 4
	case TypeRational, TypeSRational, TypeDouble:
		return 8
	}
	return 0
}

// Entry is one IFD entry with its value bytes resolved.
type Entry struct {
	Tag   uint16
	Type  uint16
	Count uint32
	Value []byte
}

// IFD is a parsed image file directory.
type IFD struct {
	Name    string
	Entries []Entry
}

// Find returns the entry for tag.
func (d *IFD) Find(tag uint16) (Entry, bool) {
	if d == nil {
		return Entry{}, false
	}
	for _, e := range d.Entries {
		if e.Tag == tag {
			return e, true
		}
	}
	return Entry{}, false
}

// File is a parsed TIFF structure.
type File struct {
	Order   binary.ByteOrder
	BigTIFF bool
	Chain   []*IFD // IFD0, IFD1, ...
	Exif    *IFD
	GPS     *IFD
	Interop *IFD
	Data    []byte
}

// ErrNotTIFF means the data has no TIFF header.
var ErrNotTIFF = errors.New("not a TIFF structure")

// ErrBigTIFF is returned for BigTIFF, which is not supported.
var ErrBigTIFF = errors.New("BigTIFF is not supported")

const (
	maxEntries = 4096
	maxIFDs    = 256
)

// Parse decodes a TIFF structure (starting at the byte-order mark).
func Parse(b []byte) (*File, error) {
	if len(b) < 8 {
		return nil, ErrNotTIFF
	}
	f := &File{Data: b}
	switch string(b[:2]) {
	case "II":
		f.Order = binary.LittleEndian
	case "MM":
		f.Order = binary.BigEndian
	default:
		return nil, ErrNotTIFF
	}
	switch f.Order.Uint16(b[2:]) {
	case 42:
	case 43:
		return nil, ErrBigTIFF
	default:
		return nil, ErrNotTIFF
	}
	seen := map[uint32]bool{}
	off := f.Order.Uint32(b[4:])
	for i := 0; off != 0 && i < maxIFDs; i++ {
		if seen[off] {
			break
		}
		seen[off] = true
		ifd, next, err := f.readIFD(off, fmt.Sprintf("IFD%d", i))
		if err != nil {
			if i == 0 {
				return nil, err
			}
			break // tolerate a broken link after IFD0
		}
		f.Chain = append(f.Chain, ifd)
		off = next
	}
	if len(f.Chain) == 0 {
		return nil, errors.New("TIFF has no IFD0")
	}
	if e, ok := f.Chain[0].Find(TagExifIFD); ok {
		if o, ok := f.Uint(e, 0); ok && !seen[uint32(o)] {
			seen[uint32(o)] = true
			f.Exif, _, _ = f.readIFD(uint32(o), "Exif")
		}
	}
	if e, ok := f.Chain[0].Find(TagGPSIFD); ok {
		if o, ok := f.Uint(e, 0); ok && !seen[uint32(o)] {
			seen[uint32(o)] = true
			f.GPS, _, _ = f.readIFD(uint32(o), "GPS")
		}
	}
	if e, ok := f.Exif.Find(TagInteropIFD); ok {
		if o, ok := f.Uint(e, 0); ok && !seen[uint32(o)] {
			f.Interop, _, _ = f.readIFD(uint32(o), "Interop")
		}
	}
	return f, nil
}

func (f *File) readIFD(off uint32, name string) (*IFD, uint32, error) {
	b := f.Data
	if int64(off)+2 > int64(len(b)) {
		return nil, 0, fmt.Errorf("%s offset %d out of range", name, off)
	}
	n := int(f.Order.Uint16(b[off:]))
	if n > maxEntries {
		return nil, 0, fmt.Errorf("%s has %d entries", name, n)
	}
	end := int64(off) + 2 + int64(n)*12
	if end+4 > int64(len(b)) {
		if end > int64(len(b)) {
			return nil, 0, fmt.Errorf("%s truncated", name)
		}
	}
	ifd := &IFD{Name: name}
	for i := 0; i < n; i++ {
		p := int(off) + 2 + i*12
		e := Entry{Tag: f.Order.Uint16(b[p:]), Type: f.Order.Uint16(b[p+2:]), Count: f.Order.Uint32(b[p+4:])}
		sz := TypeSize(e.Type)
		if sz == 0 {
			continue
		}
		total := uint64(sz) * uint64(e.Count)
		if total <= 4 {
			e.Value = b[p+8 : p+8+int(total)]
		} else {
			vo := uint64(f.Order.Uint32(b[p+8:]))
			if vo+total > uint64(len(b)) {
				continue // value points outside the data; drop it
			}
			e.Value = b[vo : vo+total]
		}
		ifd.Entries = append(ifd.Entries, e)
	}
	var next uint32
	if end+4 <= int64(len(b)) {
		next = f.Order.Uint32(b[end:])
	}
	return ifd, next, nil
}

// Uint returns the i-th integer value of a BYTE/SHORT/LONG/IFD entry.
func (f *File) Uint(e Entry, i int) (uint64, bool) {
	return Uint(f.Order, e, i)
}

// Uint returns the i-th integer value of a BYTE/SHORT/LONG/IFD entry.
func Uint(o binary.ByteOrder, e Entry, i int) (uint64, bool) {
	sz := TypeSize(e.Type)
	if sz == 0 || (i+1)*sz > len(e.Value) {
		return 0, false
	}
	v := e.Value[i*sz:]
	switch e.Type {
	case TypeByte, TypeUndefined:
		return uint64(v[0]), true
	case TypeShort:
		return uint64(o.Uint16(v)), true
	case TypeLong, TypeIFD:
		return uint64(o.Uint32(v)), true
	}
	return 0, false
}

// Uints returns all integer values of an entry.
func Uints(o binary.ByteOrder, e Entry) []uint64 {
	out := make([]uint64, 0, e.Count)
	for i := 0; i < int(e.Count); i++ {
		v, ok := Uint(o, e, i)
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out
}

// isPointer reports tags that only link to other structures.
func isPointer(tag uint16) bool {
	return tag == TagExifIFD || tag == TagGPSIFD || tag == TagInteropIFD
}

// Format renders an entry value as text. ok is false for binary data.
func Format(o binary.ByteOrder, e Entry) (string, bool) {
	switch e.Tag {
	case 0x9286: // UserComment: 8-byte charset prefix
		if len(e.Value) >= 8 {
			cs := strings.TrimRight(string(e.Value[:8]), "\x00 ")
			body := e.Value[8:]
			switch cs {
			case "ASCII", "":
				s := strings.TrimRight(string(body), "\x00 ")
				return s, report.Printable(s)
			case "UNICODE":
				return decodeUTF16(body, o), true
			}
		}
		return "", false
	case 0x9C9B, 0x9C9C, 0x9C9D, 0x9C9E, 0x9C9F: // XP* tags: UTF-16LE
		return decodeUTF16(e.Value, binary.LittleEndian), true
	case 0x9000, 0xA000: // version strings stored as 4 ASCII bytes
		return string(e.Value), report.Printable(string(e.Value))
	}
	switch e.Type {
	case TypeASCII:
		s := strings.TrimRight(string(e.Value), "\x00 ")
		return s, report.Printable(s)
	case TypeByte, TypeUndefined, TypeSByte:
		if e.Count <= 4 {
			parts := make([]string, len(e.Value))
			for i, c := range e.Value {
				parts[i] = strconv.Itoa(int(c))
			}
			return strings.Join(parts, " "), true
		}
		return "", false
	}
	sz := TypeSize(e.Type)
	if sz == 0 || e.Count > 16 {
		return "", false
	}
	parts := make([]string, 0, e.Count)
	for i := 0; i < int(e.Count) && (i+1)*sz <= len(e.Value); i++ {
		v := e.Value[i*sz:]
		switch e.Type {
		case TypeShort:
			parts = append(parts, strconv.FormatUint(uint64(o.Uint16(v)), 10))
		case TypeSShort:
			parts = append(parts, strconv.FormatInt(int64(int16(o.Uint16(v))), 10))
		case TypeLong, TypeIFD:
			parts = append(parts, strconv.FormatUint(uint64(o.Uint32(v)), 10))
		case TypeSLong:
			parts = append(parts, strconv.FormatInt(int64(int32(o.Uint32(v))), 10))
		case TypeRational:
			parts = append(parts, ratio(float64(o.Uint32(v)), float64(o.Uint32(v[4:]))))
		case TypeSRational:
			parts = append(parts, ratio(float64(int32(o.Uint32(v))), float64(int32(o.Uint32(v[4:])))))
		case TypeFloat:
			parts = append(parts, strconv.FormatFloat(float64(math.Float32frombits(o.Uint32(v))), 'g', 6, 32))
		case TypeDouble:
			parts = append(parts, strconv.FormatFloat(math.Float64frombits(o.Uint64(v)), 'g', 8, 64))
		}
	}
	return strings.Join(parts, " "), true
}

func ratio(n, d float64) string {
	if d == 0 {
		return "0"
	}
	return strconv.FormatFloat(n/d, 'f', -1, 64)
}

func decodeUTF16(b []byte, o binary.ByteOrder) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, o.Uint16(b[i:]))
	}
	s := string(utf16.Decode(u))
	return strings.TrimRight(s, "\x00 ")
}

// Field converts an entry to a report field.
func Field(o binary.ByteOrder, ifd string, e Entry) report.Field {
	name := TagName(ifd, e.Tag)
	cat := classify.Exif(ifd, name)
	if s, ok := Format(o, e); ok {
		return report.Text(cat, "exif", name, s)
	}
	return report.Binary(cat, "exif", name, int64(len(e.Value)))
}

// Summary describes the metadata found in an EXIF block.
type Summary struct {
	Fields      []report.Field
	Orientation uint16 // 0 if absent
	Thumbnail   []byte // IFD1 JPEG thumbnail, if any
}

// Summarize lists every metadata entry in IFD0, Exif, GPS and Interop, plus
// the IFD1 thumbnail. Orientation is returned separately (it is kept so
// images keep displaying the right way up) and not listed as a field.
func (f *File) Summarize() Summary {
	var s Summary
	for _, ifd := range []*IFD{f.Chain[0], f.Exif, f.GPS, f.Interop} {
		if ifd == nil {
			continue
		}
		name := ifd.Name
		if name == "IFD0" {
			name = "Image"
		}
		for _, e := range ifd.Entries {
			if isPointer(e.Tag) {
				continue
			}
			if ifd == f.Chain[0] && e.Tag == TagOrientation {
				if v, ok := f.Uint(e, 0); ok {
					s.Orientation = uint16(v)
				}
				continue
			}
			s.Fields = append(s.Fields, Field(f.Order, name, e))
		}
	}
	if len(f.Chain) > 1 {
		ifd1 := f.Chain[1]
		off, ok1 := ifd1.Find(TagThumbOffset)
		ln, ok2 := ifd1.Find(TagThumbLength)
		if ok1 && ok2 {
			o, _ := f.Uint(off, 0)
			l, _ := f.Uint(ln, 0)
			if o+l <= uint64(len(f.Data)) && l > 0 {
				s.Thumbnail = f.Data[o : o+l]
			}
		}
	}
	return s
}

// Minimal builds a tiny EXIF TIFF block holding only Orientation (when it is
// not the default) and optionally a JPEG thumbnail. It returns nil when there
// is nothing worth keeping.
func Minimal(order binary.ByteOrder, orientation uint16, thumb []byte) []byte {
	keepOrient := orientation >= 2 && orientation <= 8
	if !keepOrient && len(thumb) == 0 {
		return nil
	}
	ifd0 := &WIFD{}
	if keepOrient {
		v := make([]byte, 2)
		order.PutUint16(v, orientation)
		ifd0.Entries = append(ifd0.Entries, WEntry{Tag: TagOrientation, Type: TypeShort, Count: 1, Data: v})
	}
	chain := []*WIFD{ifd0}
	if len(thumb) > 0 {
		c := make([]byte, 2)
		order.PutUint16(c, 6) // old-style JPEG compression marker for thumbnails
		ifd1 := &WIFD{
			Entries: []WEntry{{Tag: 0x0103, Type: TypeShort, Count: 1, Data: c}},
			Blobs:   map[uint16][][]byte{TagThumbOffset: {thumb}},
		}
		chain = append(chain, ifd1)
	}
	return Encode(order, chain)
}
