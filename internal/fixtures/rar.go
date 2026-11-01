package fixtures

import (
	"encoding/binary"
	"hash/crc32"
	"time"
)

func init() {
	register("bundle.rar", func() Fixture { return Fixture{Name: "bundle.rar", Format: "rar", Data: BundleRAR5(true)} })
	register("bundle4.rar", func() Fixture { return Fixture{Name: "bundle4.rar", Format: "rar", Data: BundleRAR4()} })
}

// RAR files are written here with stored (uncompressed) members, following
// RARLAB's published format descriptions, so tests need no rar binary.

type rarMember struct {
	name  string
	data  []byte
	dir   bool
	mode  uint32
	owner bool
}

func rarMembers() []rarMember {
	return []rarMember{
		{name: "docs", dir: true, mode: 0o40700},
		{name: "docs/photo.jpg", data: PhotoJPEG(), mode: 0o100600, owner: true},
		{name: "docs/readme.txt", data: []byte(ReadmeBody), mode: 0o100600, owner: true},
		{name: "docs/run.sh", data: []byte(ScriptBody), mode: 0o100700, owner: true},
		{name: "docs/.DS_Store", data: []byte("Bud1" + ArtistName), mode: 0o100600},
		{name: "Users/jane/notes.txt", data: []byte(ReadmeBody), mode: 0o100600},
	}
}

// RARTime is the timestamp planted in RAR fixtures.
var RARTime = time.Date(2023, 4, 12, 18, 33, 2, 0, time.UTC)

func vint(v uint64) []byte {
	var b []byte
	for {
		c := byte(v & 0x7F)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func u32le(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

// rar5Block builds one RAR5 header (and appends its data area).
func rar5Block(typ, flags uint64, fields, extra, data []byte) []byte {
	if len(extra) > 0 {
		flags |= 0x0001
	}
	if data != nil {
		flags |= 0x0002
	}
	body := cat(vint(typ), vint(flags))
	if len(extra) > 0 {
		body = append(body, vint(uint64(len(extra)))...)
	}
	if data != nil {
		body = append(body, vint(uint64(len(data)))...)
	}
	body = cat(body, fields, extra)
	hdr := cat(vint(uint64(len(body))), body)
	return cat(u32le(crc32.ChecksumIEEE(hdr)), hdr, data)
}

func rar5Record(typ uint64, data []byte) []byte {
	rec := cat(vint(typ), data)
	return cat(vint(uint64(len(rec))), rec)
}

func rar5FileFields(name string, size int, mode uint32, dir bool, data []byte) []byte {
	ff := uint64(0x0002) // mtime present
	if dir {
		ff |= 0x0001
	} else {
		ff |= 0x0004 // CRC32 present
	}
	f := cat(vint(ff), vint(uint64(size)), vint(uint64(mode)), u32le(uint32(RARTime.Unix())))
	if !dir {
		f = append(f, u32le(crc32.ChecksumIEEE(data))...)
	}
	return cat(f, vint(0) /* stored */, vint(1) /* Unix */, vint(uint64(len(name))), []byte(name))
}

// BundleRAR5 is a RAR5 archive with a comment, Unix owners, modification,
// creation and access times, and the usual metadata-bearing members.
func BundleRAR5(comment bool) []byte {
	out := []byte("Rar!\x1a\x07\x01\x00")
	out = append(out, rar5Block(1, 0, vint(0), nil, nil)...)
	if comment {
		c := []byte("Packed by " + ArtistName)
		out = append(out, rar5Block(3, 0, rar5FileFields("CMT", len(c), 0, false, c), nil, c)...)
	}
	times := cat(vint(0x1|0x2|0x4|0x8), u32le(uint32(RARTime.Unix())), u32le(uint32(RARTime.Unix())), u32le(uint32(RARTime.Unix())))
	for _, m := range rarMembers() {
		extra := rar5Record(3, times)
		if m.owner {
			own := cat(vint(0x1|0x2|0x4|0x8), vint(4), []byte("jane"), vint(5), []byte("staff"), vint(501), vint(20))
			extra = append(extra, rar5Record(6, own)...)
		}
		var data []byte
		if !m.dir {
			data = m.data
		}
		out = append(out, rar5Block(2, 0, rar5FileFields(m.name, len(m.data), m.mode, m.dir, m.data), extra, data)...)
	}
	return append(out, rar5Block(5, 0, vint(0), nil, nil)...)
}

func dosTime(t time.Time) uint32 {
	d := uint32(t.Year()-1980)<<9 | uint32(t.Month())<<5 | uint32(t.Day())
	tm := uint32(t.Hour())<<11 | uint32(t.Minute())<<5 | uint32(t.Second()/2)
	return d<<16 | tm
}

// rar4Block builds a RAR4 block; the CRC covers the header from HEAD_TYPE.
func rar4Block(typ byte, flags uint16, fields []byte) []byte {
	b := make([]byte, 7, 7+len(fields))
	b[2] = typ
	binary.LittleEndian.PutUint16(b[3:], flags)
	binary.LittleEndian.PutUint16(b[5:], uint16(7+len(fields)))
	b = append(b, fields...)
	binary.LittleEndian.PutUint16(b, uint16(crc32.ChecksumIEEE(b[2:])))
	return b
}

func rar4File(typ byte, name string, data []byte, mode uint32, dir bool) []byte {
	f := make([]byte, 25)
	binary.LittleEndian.PutUint32(f[0:], uint32(len(data)))
	binary.LittleEndian.PutUint32(f[4:], uint32(len(data)))
	f[8] = 3 // Unix
	binary.LittleEndian.PutUint32(f[9:], crc32.ChecksumIEEE(data))
	binary.LittleEndian.PutUint32(f[13:], dosTime(RARTime))
	f[17] = 20   // version needed
	f[18] = 0x30 // stored
	binary.LittleEndian.PutUint16(f[19:], uint16(len(name)))
	binary.LittleEndian.PutUint32(f[21:], mode)
	flags := uint16(0x8000)
	if dir {
		flags |= 0x00E0
	}
	return append(rar4Block(typ, flags, append(f, name...)), data...)
}

// BundleRAR4 is a RAR 2.9/4 archive with a comment sub-block and 2023
// timestamps.
func BundleRAR4() []byte {
	out := []byte("Rar!\x1a\x07\x00")
	out = append(out, rar4Block(0x73, 0, make([]byte, 6))...)
	out = append(out, rar4File(0x7A, "CMT", []byte("Packed by "+ArtistName), 0, false)...)
	for _, m := range rarMembers() {
		out = append(out, rar4File(0x74, m.name, m.data, m.mode, m.dir)...)
	}
	return append(out, rar4Block(0x7B, 0, nil)...)
}

// RAR5Variant builds small RAR5 archives for edge-case tests: mainFlags sets
// archive flags (0x1 = multi-volume), encrypted inserts an archive
// encryption header, and names are stored as plain text members.
func RAR5Variant(mainFlags uint64, encrypted bool, names ...string) []byte {
	out := []byte("Rar!\x1a\x07\x01\x00")
	if encrypted {
		// Encryption header: version 0, flags 0, KDF count 15, 16-byte salt.
		out = append(out, rar5Block(4, 0, cat(vint(0), vint(0), []byte{15}, make([]byte, 16)), nil, nil)...)
	}
	out = append(out, rar5Block(1, 0, vint(mainFlags), nil, nil)...)
	for _, n := range names {
		d := []byte(ReadmeBody)
		out = append(out, rar5Block(2, 0, rar5FileFields(n, len(d), 0o100644, false, d), nil, d)...)
	}
	return append(out, rar5Block(5, 0, vint(0), nil, nil)...)
}
