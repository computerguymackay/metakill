package fixtures

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	"github.com/computerguymackay/metakill/internal/meta/exif"
)

// Known values planted in fixtures; tests assert these disappear.
const (
	Make       = "Apple"
	Model      = "iPhone 14 Pro"
	Software   = "iOS 16.4"
	DateTime   = "2023:04:12 18:33:02"
	ArtistName = "Jane Photographer"
	Serial     = "SN-0042-SECRET"
	Comment    = "secret comment"
	SongTitle  = "Test Song"
	SongArtist = "Test Artist"
	SongAlbum  = "Test Album"
	SongYear   = "2023-04-12"
	Encoder    = "SecretEncoder 1.0"
)

func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

// BaseJPEG returns a plain baseline JPEG with no metadata segments.
func BaseJPEG(w, h int) []byte {
	var b bytes.Buffer
	_ = jpeg.Encode(&b, testImage(w, h), &jpeg.Options{Quality: 80})
	return b.Bytes()
}

// BasePNG returns a plain PNG with no ancillary chunks.
func BasePNG(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, testImage(w, h))
	return b.Bytes()
}

// Segment builds a JPEG marker segment.
func Segment(marker byte, payload []byte) []byte {
	s := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(s[2:], uint16(len(payload)+2))
	return append(s, payload...)
}

// InsertAfterSOI inserts segments right after the SOI marker (and after a
// JFIF APP0 if present).
func InsertAfterSOI(j []byte, segs ...[]byte) []byte {
	pos := 2
	if len(j) > 6 && j[2] == 0xFF && j[3] == 0xE0 {
		pos = 4 + int(binary.BigEndian.Uint16(j[4:]))
	}
	out := append([]byte{}, j[:pos]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, j[pos:]...)
}

func ascii(s string) exif.WEntry {
	b := append([]byte(s), 0)
	return exif.WEntry{Type: exif.TypeASCII, Count: uint32(len(b)), Data: b}
}

func short(o binary.ByteOrder, v uint16) exif.WEntry {
	b := make([]byte, 2)
	o.PutUint16(b, v)
	return exif.WEntry{Type: exif.TypeShort, Count: 1, Data: b}
}

func rationals(o binary.ByteOrder, vals ...[2]uint32) exif.WEntry {
	b := make([]byte, 8*len(vals))
	for i, v := range vals {
		o.PutUint32(b[8*i:], v[0])
		o.PutUint32(b[8*i+4:], v[1])
	}
	return exif.WEntry{Type: exif.TypeRational, Count: uint32(len(vals)), Data: b}
}

func tag(t uint16, e exif.WEntry) exif.WEntry { e.Tag = t; return e }

// SampleEXIF builds a TIFF-structured EXIF block with GPS, timestamps,
// device strings, an author, a user comment, Orientation=6 and optionally an
// IFD1 thumbnail.
func SampleEXIF(o binary.ByteOrder, thumb []byte) []byte {
	uc := append([]byte("ASCII\x00\x00\x00"), []byte(Comment)...)
	gps := &exif.WIFD{Entries: []exif.WEntry{
		tag(0x0001, ascii("N")),
		tag(0x0002, rationals(o, [2]uint32{37, 1}, [2]uint32{46, 1}, [2]uint32{2964, 100})),
		tag(0x0003, ascii("W")),
		tag(0x0004, rationals(o, [2]uint32{122, 1}, [2]uint32{25, 1}, [2]uint32{984, 100})),
		tag(0x0006, rationals(o, [2]uint32{16, 1})),
	}}
	ex := &exif.WIFD{Entries: []exif.WEntry{
		tag(0x9003, ascii(DateTime)),
		tag(0x9004, ascii(DateTime)),
		tag(0x9286, exif.WEntry{Type: exif.TypeUndefined, Count: uint32(len(uc)), Data: uc}),
		tag(0xA431, ascii(Serial)),
		tag(0xA434, ascii("iPhone 14 Pro back camera")),
	}}
	ifd0 := &exif.WIFD{
		Entries: []exif.WEntry{
			tag(0x010F, ascii(Make)),
			tag(0x0110, ascii(Model)),
			tag(0x0112, short(o, 6)),
			tag(0x0131, ascii(Software)),
			tag(0x0132, ascii(DateTime)),
			tag(0x013B, ascii(ArtistName)),
		},
		Subs: map[uint16]*exif.WIFD{exif.TagExifIFD: ex, exif.TagGPSIFD: gps},
	}
	chain := []*exif.WIFD{ifd0}
	if len(thumb) > 0 {
		chain = append(chain, &exif.WIFD{
			Entries: []exif.WEntry{tag(0x0103, short(o, 6))},
			Blobs:   map[uint16][][]byte{exif.TagThumbOffset: {thumb}},
		})
	}
	return exif.Encode(o, chain)
}

// SampleXMP is an XMP packet with creator, tool, dates and GPS.
const SampleXMP = `<?xpacket begin="` + "\ufeff" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="Adobe XMP Core 5.6">
 <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
  <rdf:Description rdf:about=""
    xmlns:xmp="http://ns.adobe.com/xap/1.0/"
    xmlns:dc="http://purl.org/dc/elements/1.1/"
    xmlns:exif="http://ns.adobe.com/exif/1.0/"
    xmp:CreatorTool="Adobe Photoshop 24.0"
    xmp:CreateDate="2023-04-12T18:33:02">
   <dc:creator><rdf:Seq><rdf:li>` + ArtistName + `</rdf:li></rdf:Seq></dc:creator>
   <exif:GPSLatitude>37,46.494N</exif:GPSLatitude>
  </rdf:Description>
 </rdf:RDF>
</x:xmpmeta>
<?xpacket end="w"?>`

// SampleIPTC is a Photoshop resource block with IPTC by-line and city.
func SampleIPTC() []byte {
	rec := func(ds byte, v string) []byte {
		b := []byte{0x1C, 2, ds, 0, 0}
		binary.BigEndian.PutUint16(b[3:], uint16(len(v)))
		return append(b, v...)
	}
	var iim []byte
	iim = append(iim, rec(80, ArtistName)...)
	iim = append(iim, rec(90, "San Francisco")...)
	res := []byte("8BIM\x04\x04\x00\x00")
	sz := make([]byte, 4)
	binary.BigEndian.PutUint32(sz, uint32(len(iim)))
	res = append(res, sz...)
	res = append(res, iim...)
	if len(iim)%2 == 1 {
		res = append(res, 0)
	}
	return res
}

// FakeICC is a stand-in ICC profile body (handlers treat it as opaque).
var FakeICC = append([]byte("\x00\x00\x01\x00lcms\x02\x10\x00\x00mntrRGB XYZ "), make([]byte, 112)...)

// PNGChunk appends a PNG chunk with a correct CRC.
func PNGChunk(out []byte, typ string, body []byte) []byte {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	copy(hdr[4:], typ)
	out = append(out, hdr[:]...)
	out = append(out, body...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(typ), body...)))
}

func deflate(b []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(b) // writes to a bytes.Buffer cannot fail
	_ = zw.Close()
	return buf.Bytes()
}
