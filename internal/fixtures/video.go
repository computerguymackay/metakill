package fixtures

import (
	"encoding/binary"

	"github.com/computerguymackay/metakill/internal/meta/isobmff"
)

func init() {
	register("clip.mp4", func() Fixture { return Fixture{Name: "clip.mp4", Format: "mp4", Data: ClipMP4()} })
}

// VideoSamples are the planted video sample payloads (one per chunk).
var VideoSamples = [][]byte{
	[]byte("VIDEO-SAMPLE-ONE-0123456789"),
	[]byte("VIDEO-SAMPLE-TWO-abcdefghij"),
}

// TelemetrySample is a fake GoPro GPMF sample with GPS coordinates.
var TelemetrySample = []byte("DEVC-GPS5-37.7749,-122.4194-SECRET-TRACK")

func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

func box(t string, payload ...[]byte) []byte { return isobmff.Make(t, payload...) }

// mvhd/tkhd/mdhd version 0 with creation/modification time 2023-04-12.
const qtTime2023 = 3764169182 // seconds since 1904

func fullTimes(extra int) []byte {
	b := append([]byte{0, 0, 0, 0}, u32(qtTime2023)...)
	b = append(b, u32(qtTime2023)...)
	return append(b, make([]byte, extra)...)
}

func hdlr(handler, name string) []byte {
	return box("hdlr", make([]byte, 8), []byte(handler), make([]byte, 12), []byte(name+"\x00"))
}

func visualEntry(compressor string) []byte {
	b := make([]byte, 78)
	binary.BigEndian.PutUint16(b[6:], 1)   // data_reference_index
	binary.BigEndian.PutUint16(b[24:], 16) // width
	binary.BigEndian.PutUint16(b[26:], 16) // height
	b[42] = byte(len(compressor))
	copy(b[43:], compressor)
	return box("avc1", b)
}

func stbl(entry []byte, sampleSizes []uint32, chunkOffsets []uint32) []byte {
	stsd := box("stsd", u32(0), u32(1), entry)
	stts := box("stts", u32(0), u32(1), u32(uint32(len(sampleSizes))), u32(1))
	stsc := box("stsc", u32(0), u32(1), u32(1), u32(1), u32(1))
	var sz []byte
	for _, s := range sampleSizes {
		sz = append(sz, u32(s)...)
	}
	stsz := box("stsz", u32(0), u32(0), u32(uint32(len(sampleSizes))), sz)
	var co []byte
	for _, o := range chunkOffsets {
		co = append(co, u32(o)...)
	}
	stco := box("stco", u32(0), u32(uint32(len(chunkOffsets))), co)
	return box("stbl", stsd, stts, stsc, stsz, stco)
}

func ilstItem(t string, dtype uint32, v []byte) []byte {
	return box(t, box("data", u32(dtype), u32(0), v))
}

func qtAtom(t, v string) []byte {
	return box(t, u16(uint16(len(v))), u16(0x55C4), []byte(v))
}

// ClipMP4 builds an MP4 with movie header times, an Apple keys meta (make,
// model, location, software), udta (©xyz, iTunes ilst with cover art), a
// GoPro-style telemetry track, an XMP uuid box, non-zero free space and
// mdat after moov (so stripping must shift chunk offsets).
func ClipMP4() []byte {
	ftyp := box("ftyp", []byte("isom"), u32(512), []byte("isomiso2avc1mp41"))

	build := func(mdatStart uint32) []byte {
		v1 := mdatStart + 8
		v2 := v1 + uint32(len(VideoSamples[0]))
		tel := v2 + uint32(len(VideoSamples[1]))
		video := box("trak",
			box("tkhd", fullTimes(72)),
			box("mdia",
				box("mdhd", fullTimes(12)),
				hdlr("vide", "Core Media Video"),
				box("minf", box("vmhd", u32(1), make([]byte, 8)),
					stbl(visualEntry(Encoder), []uint32{uint32(len(VideoSamples[0])), uint32(len(VideoSamples[1]))}, []uint32{v1, v2}))))
		telemetry := box("trak",
			box("tkhd", fullTimes(72)),
			box("mdia",
				box("mdhd", fullTimes(12)),
				hdlr("meta", "GoPro MET"),
				box("minf", box("nmhd", u32(0)),
					stbl(box("gpmd", make([]byte, 8)), []uint32{uint32(len(TelemetrySample))}, []uint32{tel}))))
		keys := box("keys", u32(0), u32(4),
			box("mdta", []byte("com.apple.quicktime.make")),
			box("mdta", []byte("com.apple.quicktime.model")),
			box("mdta", []byte("com.apple.quicktime.location.ISO6709")),
			box("mdta", []byte("com.apple.quicktime.software")))
		appleMeta := box("meta", hdlr("mdta", ""), keys, box("ilst",
			isobmff.Make(string([]byte{0, 0, 0, 1}), box("data", u32(1), u32(0), []byte(Make))),
			isobmff.Make(string([]byte{0, 0, 0, 2}), box("data", u32(1), u32(0), []byte(Model))),
			isobmff.Make(string([]byte{0, 0, 0, 3}), box("data", u32(1), u32(0), []byte("+37.7749-122.4194/"))),
			isobmff.Make(string([]byte{0, 0, 0, 4}), box("data", u32(1), u32(0), []byte(Software)))))
		itunes := box("meta", u32(0), hdlr("mdir", "appl"), box("ilst",
			ilstItem("©nam", 1, []byte(SongTitle)),
			ilstItem("©ART", 1, []byte(SongArtist)),
			ilstItem("©alb", 1, []byte(SongAlbum)),
			ilstItem("trkn", 0, []byte{0, 0, 0, 3, 0, 12, 0, 0}),
			ilstItem("©day", 1, []byte(SongYear)),
			ilstItem("©too", 1, []byte(Encoder)),
			ilstItem("covr", 13, CoverJPEG())))
		udta := box("udta", qtAtom("©xyz", "+37.7749-122.4194/"), qtAtom("©mak", Make), itunes)
		moov := box("moov", box("mvhd", fullTimes(88)), video, telemetry, udta, appleMeta)
		return moov
	}
	xmpBox := box("uuid", xmpUUIDBytes, []byte(SampleXMP))
	free := box("free", []byte("old metadata: "+ArtistName))
	// Two passes: the first computes the size of everything before mdat.
	pre := len(ftyp) + len(build(0)) + len(xmpBox) + len(free)
	moov := build(uint32(pre))
	var payload []byte
	payload = append(payload, VideoSamples[0]...)
	payload = append(payload, VideoSamples[1]...)
	payload = append(payload, TelemetrySample...)
	out := append([]byte{}, ftyp...)
	out = append(out, moov...)
	out = append(out, xmpBox...)
	out = append(out, free...)
	return append(out, box("mdat", payload)...)
}

var xmpUUIDBytes = []byte{0xBE, 0x7A, 0xCF, 0xCB, 0x97, 0xA9, 0x42, 0xE8, 0x9C, 0x71, 0x99, 0x94, 0x91, 0xE3, 0xAF, 0xAC}
