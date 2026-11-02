// Package ape parses APEv1/APEv2 tags (found at the end of MP3 and other
// audio files).
package ape

import (
	"bytes"
	"encoding/binary"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/report"
)

// FooterSize is the size of an APE tag footer (and header).
const FooterSize = 32

// Footer describes an APE tag found by its footer.
type Footer struct {
	Version   uint32
	Size      int // items + footer, excluding the optional header
	Count     int
	HasHeader bool
}

// ParseFooter decodes a 32-byte footer.
func ParseFooter(b []byte) (Footer, bool) {
	if len(b) < FooterSize || !bytes.Equal(b[:8], []byte("APETAGEX")) {
		return Footer{}, false
	}
	f := Footer{
		Version:   binary.LittleEndian.Uint32(b[8:]),
		Size:      int(binary.LittleEndian.Uint32(b[12:])),
		Count:     int(binary.LittleEndian.Uint32(b[16:])),
		HasHeader: binary.LittleEndian.Uint32(b[20:])&(1<<31) != 0,
	}
	if f.Size < FooterSize {
		return Footer{}, false
	}
	return f, true
}

// TotalSize is the number of bytes the tag occupies on disk.
func (f Footer) TotalSize() int {
	if f.HasHeader {
		return f.Size + FooterSize
	}
	return f.Size
}

// Item is one APE tag item.
type Item struct {
	Key    string
	Value  []byte
	Binary bool
}

// Items parses the item area (the tag bytes without header and footer).
func Items(b []byte, count int) []Item {
	var out []Item
	for i := 0; i < count && len(b) >= 9; i++ {
		n := int(binary.LittleEndian.Uint32(b))
		flags := binary.LittleEndian.Uint32(b[4:])
		rest := b[8:]
		k := bytes.IndexByte(rest, 0)
		if k < 0 || n < 0 || k+1+n > len(rest) {
			break
		}
		out = append(out, Item{
			Key:    string(rest[:k]),
			Value:  rest[k+1 : k+1+n],
			Binary: (flags>>1)&3 == 1,
		})
		b = rest[k+1+n:]
	}
	return out
}

// Describe returns the report field and canonical name for an item.
func (it Item) Describe() (report.Field, string) {
	cat, canon := classify.Vorbis(it.Key)
	if it.Binary {
		if bytes.HasPrefix([]byte(it.Key), []byte("Cover Art")) {
			cat = report.CatThumbnail
		}
		return report.Binary(cat, "ape", it.Key, int64(len(it.Value))), ""
	}
	return report.Text(cat, "ape", it.Key, string(bytes.ReplaceAll(it.Value, []byte{0}, []byte("; ")))), canon
}
