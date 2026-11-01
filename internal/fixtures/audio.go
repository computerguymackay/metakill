package fixtures

import (
	"encoding/binary"
)

func init() {
	register("song.mp3", func() Fixture { return Fixture{Name: "song.mp3", Format: "mp3", Data: SongMP3()} })
}

// MP3FrameHeader is MPEG-1 Layer III, 128 kbit/s, 44.1 kHz, joint stereo.
var MP3FrameHeader = []byte{0xFF, 0xFB, 0x90, 0x64}

const mp3FrameLen = 417

// MP3Audio returns n audio frames with deterministic pseudo-random payload,
// preceded by a LAME "Info" frame naming an encoder.
func MP3Audio(n int) []byte {
	info := make([]byte, mp3FrameLen)
	copy(info, MP3FrameHeader)
	xo := 4 + 32
	copy(info[xo:], "Info")
	binary.BigEndian.PutUint32(info[xo+4:], 0x0F)
	lame := xo + 8 + 4 + 4 + 100 + 4
	copy(info[lame:], "LAME3.100")
	binary.BigEndian.PutUint16(info[lame+34:], crc16(info[:lame+34]))
	out := info
	seed := uint32(1)
	for i := 0; i < n; i++ {
		f := make([]byte, mp3FrameLen)
		copy(f, MP3FrameHeader)
		for j := 4; j < len(f); j++ {
			seed = seed*1664525 + 1013904223
			f[j] = byte(seed >> 24)
			if f[j] == 0xFF {
				f[j] = 0xFE // avoid accidental sync words
			}
		}
		out = append(out, f...)
	}
	return out
}

func crc16(b []byte) uint16 {
	var crc uint16
	for _, c := range b {
		crc ^= uint16(c)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// ID3v23 builds an ID3v2.3 tag from frames given as id/payload pairs.
func ID3v23(frames ...[2][]byte) []byte {
	var body []byte
	for _, f := range frames {
		h := make([]byte, 10)
		copy(h, f[0])
		binary.BigEndian.PutUint32(h[4:], uint32(len(f[1])))
		body = append(body, h...)
		body = append(body, f[1]...)
	}
	body = append(body, make([]byte, 64)...) // padding
	n := len(body)
	h := []byte{'I', 'D', '3', 3, 0, 0, byte(n >> 21 & 0x7F), byte(n >> 14 & 0x7F), byte(n >> 7 & 0x7F), byte(n & 0x7F)}
	return append(h, body...)
}

func txt(id, v string) [2][]byte { return [2][]byte{[]byte(id), append([]byte{0}, v...)} }

// CoverJPEG is a small JPEG with EXIF, used as embedded cover art.
func CoverJPEG() []byte {
	return InsertAfterSOI(BaseJPEG(8, 8), Segment(0xE1, append([]byte("Exif\x00\x00"), SampleEXIF(binary.LittleEndian, nil)...)))
}

// SongID3 is the leading tag used by SongMP3.
func SongID3() []byte {
	apic := append([]byte("\x00image/jpeg\x00\x03\x00"), CoverJPEG()...)
	return ID3v23(
		txt("TIT2", SongTitle), txt("TPE1", SongArtist), txt("TALB", SongAlbum),
		txt("TRCK", "3/12"), txt("TYER", "2023"), txt("TDAT", "1204"),
		txt("TSSE", Encoder), txt("TENC", ArtistName),
		[2][]byte{[]byte("COMM"), []byte("\x00eng\x00" + Comment)},
		[2][]byte{[]byte("PRIV"), []byte("www.amazon.com\x00" + Serial)},
		[2][]byte{[]byte("APIC"), apic},
	)
}

// APETag builds an APEv2 tag with header and footer.
func APETag(items ...[2]string) []byte {
	var body []byte
	for _, it := range items {
		h := make([]byte, 8)
		binary.LittleEndian.PutUint32(h, uint32(len(it[1])))
		body = append(body, h...)
		body = append(body, it[0]...)
		body = append(body, 0)
		body = append(body, it[1]...)
	}
	mk := func(isHeader bool) []byte {
		b := make([]byte, 32)
		copy(b, "APETAGEX")
		binary.LittleEndian.PutUint32(b[8:], 2000)
		binary.LittleEndian.PutUint32(b[12:], uint32(len(body)+32))
		binary.LittleEndian.PutUint32(b[16:], uint32(len(items)))
		flags := uint32(1 << 31)
		if isHeader {
			flags |= 1 << 29
		}
		binary.LittleEndian.PutUint32(b[20:], flags)
		return b
	}
	out := mk(true)
	out = append(out, body...)
	return append(out, mk(false)...)
}

// ID3v1 builds a 128-byte ID3v1.1 tag.
func ID3v1(title, artist, album, year, comment string, track byte) []byte {
	b := make([]byte, 128)
	copy(b, "TAG")
	copy(b[3:33], title)
	copy(b[33:63], artist)
	copy(b[63:93], album)
	copy(b[93:97], year)
	copy(b[97:125], comment)
	b[126] = track
	b[127] = 12
	return b
}

// SongMP3 has ID3v2.3 (with cover art carrying EXIF), a LAME encoder string,
// an APEv2 tag and an ID3v1.1 tag.
func SongMP3() []byte {
	out := append([]byte{}, SongID3()...)
	out = append(out, MP3Audio(6)...)
	out = append(out, APETag([2]string{"Title", SongTitle}, [2]string{"Encoder", Encoder}, [2]string{"Comment", Comment})...)
	return append(out, ID3v1(SongTitle, SongArtist, SongAlbum, "2023", Comment, 3)...)
}
