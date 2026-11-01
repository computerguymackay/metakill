package fixtures

import (
	"encoding/base64"
	"encoding/binary"

	"github.com/computerguymackay/metakill/internal/meta/vorbis"
)

func init() {
	register("track.flac", func() Fixture { return Fixture{Name: "track.flac", Format: "flac", Data: TrackFLAC()} })
	register("voice.ogg", func() Fixture { return Fixture{Name: "voice.ogg", Format: "ogg", Data: VoiceOgg("vorbis")} })
	register("voice.opus", func() Fixture { return Fixture{Name: "voice.opus", Format: "ogg", Data: VoiceOgg("opus")} })
	register("memo.wav", func() Fixture { return Fixture{Name: "memo.wav", Format: "wav", Data: MemoWAV()} })
	register("cam.avi", func() Fixture { return Fixture{Name: "cam.avi", Format: "avi", Data: CamAVI()} })
	register("movie.mkv", func() Fixture { return Fixture{Name: "movie.mkv", Format: "mkv", Data: MovieMKV()} })
}

// AudioPayload is fake compressed audio planted in every audio fixture.
var AudioPayload = []byte("AUDIO-FRAMES-0123456789-abcdefghijklmnopqrstuvwxyz")

func sampleComments(vendor string) vorbis.Block {
	pic := vorbis.EncodePicture(vorbis.Picture{Type: 3, MIME: "image/jpeg", Description: Comment, Width: 8, Height: 8, Depth: 24, Data: CoverJPEG()})
	return vorbis.Block{Vendor: vendor, Comments: []vorbis.Comment{
		{Key: "TITLE", Value: SongTitle}, {Key: "ARTIST", Value: SongArtist}, {Key: "ALBUM", Value: SongAlbum},
		{Key: "TRACKNUMBER", Value: "3"}, {Key: "DATE", Value: SongYear}, {Key: "ENCODER", Value: Encoder},
		{Key: "COMMENT", Value: Comment}, {Key: "COMPOSER", Value: ArtistName},
		{Key: "METADATA_BLOCK_PICTURE", Value: base64.StdEncoding.EncodeToString(pic)},
	}}
}

func flacBlock(typ byte, last bool, body []byte) []byte {
	h := []byte{typ, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	if last {
		h[0] |= 0x80
	}
	return append(h, body...)
}

// TrackFLAC has STREAMINFO, APPLICATION, VORBIS_COMMENT, PICTURE and dirty
// PADDING, followed by fake frames and an ID3v1 tag.
func TrackFLAC() []byte {
	out := []byte("fLaC")
	out = append(out, flacBlock(0, false, make([]byte, 34))...)
	out = append(out, flacBlock(2, false, append([]byte("riff"), ArtistName...))...)
	out = append(out, flacBlock(4, false, vorbis.Encode(vorbis.Block{Vendor: "reference libFLAC 1.4.2 " + Encoder, Comments: sampleComments("").Comments[:8]}))...)
	out = append(out, flacBlock(6, false, vorbis.EncodePicture(vorbis.Picture{Type: 3, MIME: "image/jpeg", Description: Comment, Data: CoverJPEG()}))...)
	out = append(out, flacBlock(1, true, append([]byte("stale "+Serial), make([]byte, 64)...))...)
	out = append(out, 0xFF, 0xF8)
	out = append(out, AudioPayload...)
	return append(out, ID3v1(SongTitle, SongArtist, SongAlbum, "2023", Comment, 3)...)
}

// oggPage builds one Ogg page holding whole packets.
func oggPage(headerType byte, granule uint64, serial, seq uint32, pkts ...[]byte) []byte {
	var segs, data []byte
	for _, p := range pkts {
		n := len(p)
		for n >= 255 {
			segs = append(segs, 255)
			n -= 255
		}
		segs = append(segs, byte(n))
		data = append(data, p...)
	}
	out := make([]byte, 27)
	copy(out, "OggS")
	out[5] = headerType
	binary.LittleEndian.PutUint64(out[6:], granule)
	binary.LittleEndian.PutUint32(out[14:], serial)
	binary.LittleEndian.PutUint32(out[18:], seq)
	out[26] = byte(len(segs))
	out = append(out, segs...)
	out = append(out, data...)
	binary.LittleEndian.PutUint32(out[22:], oggCRC(out))
	return out
}

func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, c := range b {
		crc ^= uint32(c) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// VoiceOgg builds an Ogg Vorbis or Opus stream with a comment header that
// carries tags and a picture, followed by two audio pages.
func VoiceOgg(codec string) []byte {
	const serial = 0x1234
	var id, comment []byte
	var extra [][]byte
	switch codec {
	case "opus":
		id = append([]byte("OpusHead"), 1, 2, 0x38, 1, 0x80, 0xBB, 0, 0, 0, 0, 0)
		comment = append([]byte("OpusTags"), vorbis.Encode(sampleComments("libopus 1.4 "+Encoder))...)
	default:
		id = append([]byte("\x01vorbis"), make([]byte, 23)...)
		comment = append(append([]byte("\x03vorbis"), vorbis.Encode(sampleComments("Xiph.Org libVorbis I 20200704 "+Encoder))...), 1)
		extra = [][]byte{append([]byte("\x05vorbis"), []byte("SETUP-HEADER")...)}
	}
	out := oggPage(2, 0, serial, 0, id)
	// The comment header (with a large picture) spans several pages.
	hdr := append([][]byte{comment}, extra...)
	var all []byte
	var lace []byte
	for _, p := range hdr {
		n := len(p)
		for n >= 255 {
			lace = append(lace, 255)
			n -= 255
		}
		lace = append(lace, byte(n))
		all = append(all, p...)
	}
	seq := uint32(1)
	for i := 0; len(lace) > 0; i++ {
		n := len(lace)
		if n > 4 {
			n = 4 // force multi-page headers
		}
		segs := lace[:n]
		lace = lace[n:]
		size := 0
		for _, s := range segs {
			size += int(s)
		}
		page := make([]byte, 27)
		copy(page, "OggS")
		if i > 0 {
			page[5] = 1
		}
		gran := uint64(0)
		if segs[len(segs)-1] == 255 {
			gran = ^uint64(0)
		}
		binary.LittleEndian.PutUint64(page[6:], gran)
		binary.LittleEndian.PutUint32(page[14:], serial)
		binary.LittleEndian.PutUint32(page[18:], seq)
		page[26] = byte(len(segs))
		page = append(page, segs...)
		page = append(page, all[:size]...)
		all = all[size:]
		binary.LittleEndian.PutUint32(page[22:], oggCRC(page))
		out = append(out, page...)
		seq++
	}
	out = append(out, oggPage(0, 960, serial, seq, AudioPayload[:20])...)
	out = append(out, oggPage(4, 1920, serial, seq+1, AudioPayload[20:])...)
	return out
}

func riffList(typ string, kids ...[]byte) []byte {
	body := []byte(typ)
	for _, k := range kids {
		body = append(body, k...)
	}
	return riffChunk("LIST", body)
}

func riffFile(form string, chunks ...[]byte) []byte {
	body := []byte(form)
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := append([]byte("RIFF\x00\x00\x00\x00"), body...)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	return out
}

func infoList() []byte {
	return riffList("INFO",
		riffChunk("INAM", []byte(SongTitle+"\x00")), riffChunk("IART", []byte(SongArtist+"\x00")),
		riffChunk("ICRD", []byte(SongYear+"\x00")), riffChunk("ISFT", []byte(Encoder+"\x00")),
		riffChunk("ICMT", []byte(Comment+"\x00")), riffChunk("IENG", []byte(ArtistName+"\x00")))
}

// MemoWAV is a Broadcast WAV with LIST INFO, bext, an id3 chunk, iXML and
// a dirty JUNK chunk.
func MemoWAV() []byte {
	fmtChunk := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtChunk, 1)
	binary.LittleEndian.PutUint16(fmtChunk[2:], 1)
	binary.LittleEndian.PutUint32(fmtChunk[4:], 8000)
	binary.LittleEndian.PutUint32(fmtChunk[8:], 8000)
	binary.LittleEndian.PutUint16(fmtChunk[12:], 1)
	binary.LittleEndian.PutUint16(fmtChunk[14:], 8)
	bext := make([]byte, 602)
	copy(bext, Comment)
	copy(bext[256:], ArtistName)
	copy(bext[320:], "2023-04-12")
	copy(bext[330:], "18:33:02")
	bext = append(bext, "A=PCM,F=8000,W=8,M=mono,T="+Encoder...)
	return riffFile("WAVE",
		riffChunk("fmt ", fmtChunk),
		riffChunk("bext", bext),
		riffChunk("data", AudioPayload),
		infoList(),
		riffChunk("id3 ", ID3v23(txt("TIT2", SongTitle), txt("TSSE", Encoder))),
		riffChunk("iXML", []byte("<BWFXML><PROJECT>"+Serial+"</PROJECT></BWFXML>")),
		riffChunk("JUNK", []byte("old "+Model)),
	)
}

// CamAVI is an AVI with IDIT, a stream name, LIST INFO and a vendor LIST.
func CamAVI() []byte {
	strl := riffList("strl",
		riffChunk("strh", make([]byte, 56)),
		riffChunk("strf", make([]byte, 40)),
		riffChunk("strn", []byte(Model+" video\x00")))
	hdrl := riffList("hdrl",
		riffChunk("avih", make([]byte, 56)),
		strl,
		riffChunk("IDIT", []byte("WED APR 12 18:33:02 2023\n\x00")))
	movi := riffList("movi", riffChunk("00dc", AudioPayload))
	return riffFile("AVI ",
		hdrl,
		infoList(),
		riffList("ncdt", riffChunk("nctg", []byte(Make+" "+Serial))),
		riffChunk("JUNK", make([]byte, 16)),
		movi,
		riffChunk("idx1", make([]byte, 16)),
	)
}

// ---------- Matroska ----------

func ebml(id uint64, data ...[]byte) []byte {
	var body []byte
	for _, d := range data {
		body = append(body, d...)
	}
	var idb []byte
	for v := id; v > 0; v >>= 8 {
		idb = append([]byte{byte(v)}, idb...)
	}
	l := 1
	for uint64(len(body)) >= (uint64(1)<<(7*l))-1 {
		l++
	}
	sz := make([]byte, l)
	v := uint64(len(body)) | uint64(1)<<(7*l)
	for i := 0; i < l; i++ {
		sz[i] = byte(v >> (8 * (l - 1 - i)))
	}
	return append(append(idb, sz...), body...)
}

func ebmlUint(id uint64, v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return ebml(id, b)
}

func ebmlStr(id uint64, s string) []byte { return ebml(id, []byte(s)) }

// MovieMKV has Info (title, date, muxing/writing apps, segment UID), a named
// track with a codec name, a cluster, Tags and an image attachment.
func MovieMKV() []byte {
	header := ebml(0x1A45DFA3, ebmlUint(0x4286, 1), ebmlStr(0x4282, "matroska"), ebmlUint(0x4287, 4), ebmlUint(0x4285, 2))
	info := ebml(0x1549A966,
		ebmlUint(0x2AD7B1, 1000000),
		ebmlStr(0x4D80, "libebml v1.4.4 + libmatroska v1.7.1 "+Encoder),
		ebmlStr(0x5741, "mkvmerge v80.0 ('Roundabout') 64-bit "+Software),
		ebmlStr(0x7BA9, SongTitle),
		ebmlUint(0x4461, 734553182000000000), // 2024-04-12 in ns since 2001
		ebml(0x73A4, []byte("0123456789abcdef")),
	)
	tracks := ebml(0x1654AE6B, ebml(0xAE,
		ebmlUint(0xD7, 1), ebmlUint(0x83, 1), ebmlStr(0x86, "V_MPEG4/ISO/AVC"),
		ebmlStr(0x536E, "Recorded on "+Model), ebmlStr(0x258688, Encoder)))
	cluster := ebml(0x1F43B675, ebmlUint(0xE7, 0), ebml(0xA3, append([]byte{0x81, 0, 0, 0x80}, AudioPayload...)))
	simple := func(name, val string) []byte { return ebml(0x67C8, ebmlStr(0x45A3, name), ebmlStr(0x4487, val)) }
	tags := ebml(0x1254C367, ebml(0x7373,
		ebml(0x63C0, ebmlUint(0x68CA, 50)),
		simple("TITLE", SongTitle), simple("ARTIST", SongArtist), simple("DATE_RELEASED", SongYear),
		simple("ENCODER", Encoder), simple("COMMENT", Comment)))
	att := ebml(0x1941A469, ebml(0x61A7,
		ebmlStr(0x466E, "cover.jpg"), ebmlStr(0x4660, "image/jpeg"), ebmlStr(0x467E, ArtistName),
		ebml(0x465C, CoverJPEG()), ebmlUint(0x46AE, 42)))
	seek := func(id []byte) []byte { return ebml(0x4DBB, ebml(0x53AB, id), ebmlUint(0x53AC, 0)) }
	seekHead := ebml(0x114D9B74,
		seek([]byte{0x15, 0x49, 0xA9, 0x66}), seek([]byte{0x12, 0x54, 0xC3, 0x67}), seek([]byte{0x19, 0x41, 0xA4, 0x69}))
	segment := ebml(0x18538067, seekHead, info, tracks, cluster, tags, att)
	return append(header, segment...)
}
