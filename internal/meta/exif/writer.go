package exif

import (
	"encoding/binary"
	"sort"
)

// WEntry is an entry to write. Data holds the value bytes already encoded in
// the target byte order.
type WEntry struct {
	Tag   uint16
	Type  uint16
	Count uint32
	Data  []byte
}

// WIFD is an IFD to write.
type WIFD struct {
	Entries []WEntry
	// Blobs maps an offset tag (StripOffsets, TileOffsets,
	// JPEGInterchangeFormat) to the data pieces it points at. The writer
	// emits both the offset tag and its matching byte-count tag.
	Blobs map[uint16][][]byte
	// Subs maps a pointer tag (e.g. ExifIFDPointer) to a sub-IFD.
	Subs map[uint16]*WIFD
}

// BlobPairs maps offset tags to their byte-count tags.
var BlobPairs = map[uint16]uint16{
	0x0111: 0x0117, // StripOffsets → StripByteCounts
	0x0144: 0x0145, // TileOffsets → TileByteCounts
	0x0201: 0x0202, // JPEGInterchangeFormat → Length
}

type writer struct {
	o   binary.ByteOrder
	buf []byte
}

// Encode writes a complete TIFF structure with the given IFD chain.
func Encode(o binary.ByteOrder, chain []*WIFD) []byte {
	w := &writer{o: o}
	if o == binary.LittleEndian {
		w.buf = append(w.buf, 'I', 'I', 42, 0)
	} else {
		w.buf = append(w.buf, 'M', 'M', 0, 42)
	}
	w.buf = append(w.buf, 0, 0, 0, 0)
	link := 4
	for _, d := range chain {
		off, next := w.writeIFD(d)
		w.o.PutUint32(w.buf[link:], uint32(off))
		link = next
	}
	return w.buf
}

func (w *writer) align() {
	if len(w.buf)%2 == 1 {
		w.buf = append(w.buf, 0)
	}
}

func (w *writer) writeIFD(d *WIFD) (offset, nextLink int) {
	skip := map[uint16]bool{}
	var entries []WEntry
	blobTags := sortedKeys(d.Blobs)
	for _, t := range blobTags {
		n := uint32(len(d.Blobs[t]))
		ct := BlobPairs[t]
		skip[t], skip[ct] = true, true
		counts := make([]byte, 4*n)
		for i, p := range d.Blobs[t] {
			w.o.PutUint32(counts[4*i:], uint32(len(p)))
		}
		entries = append(entries,
			WEntry{Tag: t, Type: TypeLong, Count: n, Data: make([]byte, 4*n)},
			WEntry{Tag: ct, Type: TypeLong, Count: n, Data: counts})
	}
	subTags := sortedKeys(d.Subs)
	for _, t := range subTags {
		skip[t] = true
		entries = append(entries, WEntry{Tag: t, Type: TypeLong, Count: 1, Data: make([]byte, 4)})
	}
	for _, e := range d.Entries {
		if !skip[e.Tag] {
			entries = append(entries, e)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Tag < entries[j].Tag })

	w.align()
	start := len(w.buf)
	n := len(entries)
	w.buf = append(w.buf, make([]byte, 2+12*n+4)...)
	w.o.PutUint16(w.buf[start:], uint16(n))
	valuePos := map[uint16]int{}
	for i, e := range entries {
		p := start + 2 + 12*i
		w.o.PutUint16(w.buf[p:], e.Tag)
		w.o.PutUint16(w.buf[p+2:], e.Type)
		w.o.PutUint32(w.buf[p+4:], e.Count)
		if len(e.Data) <= 4 {
			copy(w.buf[p+8:p+12], e.Data)
			valuePos[e.Tag] = p + 8
			continue
		}
		w.align()
		vo := len(w.buf)
		w.buf = append(w.buf, e.Data...)
		w.o.PutUint32(w.buf[p+8:], uint32(vo))
		valuePos[e.Tag] = vo
	}
	for _, t := range blobTags {
		for k, piece := range d.Blobs[t] {
			w.align()
			off := len(w.buf)
			w.buf = append(w.buf, piece...)
			w.o.PutUint32(w.buf[valuePos[t]+4*k:], uint32(off))
		}
	}
	for _, t := range subTags {
		off, _ := w.writeIFD(d.Subs[t])
		w.o.PutUint32(w.buf[valuePos[t]:], uint32(off))
	}
	return start, start + 2 + 12*n
}

func sortedKeys[V any](m map[uint16]V) []uint16 {
	keys := make([]uint16, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
