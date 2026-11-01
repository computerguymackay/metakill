package fixtures

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"

	"github.com/computerguymackay/metakill/internal/meta/exif"
)

func init() {
	register("anim.gif", func() Fixture { return Fixture{Name: "anim.gif", Format: "gif", Data: AnimGIF()} })
	register("pic.webp", func() Fixture { return Fixture{Name: "pic.webp", Format: "webp", Data: PicWebP()} })
	register("scan.tiff", func() Fixture { return Fixture{Name: "scan.tiff", Format: "tiff", Data: ScanTIFF()} })
	register("phone.heic", func() Fixture { return Fixture{Name: "phone.heic", Format: "heif", Data: PhoneHEIC(false)} })
	register("drawing.svg", func() Fixture { return Fixture{Name: "drawing.svg", Format: "svg", Data: DrawingSVG()} })
}

// BaseGIF returns a plain 4x4 GIF.
func BaseGIF() []byte {
	img := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White})
	img.SetColorIndex(1, 1, 1)
	var b bytes.Buffer
	_ = gif.Encode(&b, img, nil)
	return b.Bytes()
}

func gifSubBlocks(data []byte) []byte {
	var out []byte
	for len(data) > 0 {
		n := len(data)
		if n > 255 {
			n = 255
		}
		out = append(out, byte(n))
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return append(out, 0)
}

// AnimGIF has a looping extension (kept), a comment, an XMP application
// extension, an ICC extension and an unknown application extension.
func AnimGIF() []byte {
	base := BaseGIF()
	p := 13
	if base[10]&0x80 != 0 {
		p += 3 << (int(base[10]&7) + 1)
	}
	var ext []byte
	ext = append(ext, 0x21, 0xFF, 11)
	ext = append(ext, "NETSCAPE2.0"...)
	ext = append(ext, 3, 1, 0, 0, 0)
	ext = append(ext, 0x21, 0xFE)
	ext = append(ext, gifSubBlocks([]byte(Comment))...)
	// XMP: raw packet followed by the 258-byte "magic trailer".
	ext = append(ext, 0x21, 0xFF, 11)
	ext = append(ext, "XMP DataXMP"...)
	ext = append(ext, SampleXMP...)
	ext = append(ext, 1)
	for i := 0xFF; i >= 0; i-- {
		ext = append(ext, byte(i))
	}
	ext = append(ext, 0) // block terminator
	ext = append(ext, 0x21, 0xFF, 11)
	ext = append(ext, "ICCRGBG1012"...)
	ext = append(ext, gifSubBlocks(FakeICC)...)
	ext = append(ext, 0x21, 0xFF, 11)
	ext = append(ext, "MGKIPTC0000"...)
	ext = append(ext, gifSubBlocks([]byte(ArtistName))...)
	out := append(append(append([]byte{}, base[:p]...), ext...), base[p:]...)
	return append(out, "trailing"...)
}

// TinyWebPLossless is a 1x1 lossless WebP bitstream (VP8L chunk only).
var TinyWebPLossless, _ = base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")

func riffChunk(id string, body []byte) []byte {
	h := make([]byte, 8)
	copy(h, id)
	binary.LittleEndian.PutUint32(h[4:], uint32(len(body)))
	out := append(h, body...)
	if len(body)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// PicWebP is an extended WebP with ICCP, EXIF and XMP chunks.
func PicWebP() []byte {
	vp8l := TinyWebPLossless[12:] // the VP8L chunk
	vp8x := make([]byte, 10)
	vp8x[0] = 0x20 | 0x08 | 0x04 // ICC, EXIF, XMP
	body := append([]byte("WEBP"), riffChunk("VP8X", vp8x)...)
	body = append(body, riffChunk("ICCP", FakeICC)...)
	body = append(body, vp8l...)
	body = append(body, riffChunk("EXIF", SampleEXIF(binary.LittleEndian, nil))...)
	body = append(body, riffChunk("XMP ", []byte(SampleXMP))...)
	out := append([]byte("RIFF\x00\x00\x00\x00"), body...)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	return out
}

// ScanTIFF is a 4x4 RGB TIFF with make/model/software/date/artist, XMP,
// an Exif IFD, a GPS IFD and a reduced-resolution second page.
func ScanTIFF() []byte {
	o := binary.LittleEndian
	pixels := make([]byte, 4*4*3)
	for i := range pixels {
		pixels[i] = byte(i * 5)
	}
	bps := make([]byte, 6)
	for i := 0; i < 3; i++ {
		o.PutUint16(bps[2*i:], 8)
	}
	page := func(w, h uint16, data []byte, subfile uint32) *exif.WIFD {
		sf := make([]byte, 4)
		o.PutUint32(sf, subfile)
		return &exif.WIFD{
			Entries: []exif.WEntry{
				{Tag: 254, Type: exif.TypeLong, Count: 1, Data: sf},
				tag(256, short(o, w)), tag(257, short(o, h)),
				{Tag: 258, Type: exif.TypeShort, Count: 3, Data: bps},
				tag(259, short(o, 1)), tag(262, short(o, 2)),
				tag(277, short(o, 3)), tag(278, short(o, h)), tag(284, short(o, 1)),
			},
			Blobs: map[uint16][][]byte{273: {data}},
		}
	}
	p0 := page(4, 4, pixels, 0)
	p0.Entries = append(p0.Entries,
		tag(0x010F, ascii(Make)), tag(0x0110, ascii(Model)), tag(0x0112, short(o, 1)),
		tag(0x0131, ascii(Software)), tag(0x0132, ascii(DateTime)), tag(0x013B, ascii(ArtistName)),
		exif.WEntry{Tag: 0x02BC, Type: exif.TypeByte, Count: uint32(len(SampleXMP)), Data: []byte(SampleXMP)},
		exif.WEntry{Tag: 0x8773, Type: exif.TypeUndefined, Count: uint32(len(FakeICC)), Data: FakeICC},
	)
	p0.Subs = map[uint16]*exif.WIFD{
		exif.TagExifIFD: {Entries: []exif.WEntry{tag(0x9003, ascii(DateTime)), tag(0xA431, ascii(Serial))}},
		exif.TagGPSIFD:  {Entries: []exif.WEntry{tag(0x0001, ascii("N")), tag(0x0002, rationals(o, [2]uint32{37, 1}, [2]uint32{46, 1}, [2]uint32{2964, 100}))}},
	}
	thumb := page(2, 2, pixels[:12], 1)
	return exif.Encode(o, []*exif.WIFD{p0, thumb})
}

// PhoneHEIC builds a minimal HEIF with a primary item, an Exif item, an XMP
// mime item and (optionally) a thumbnail item. Image payloads are fake.
func PhoneHEIC(withThumb bool) []byte {
	exifPayload := append([]byte{0, 0, 0, 6}, append([]byte("Exif\x00\x00"), SampleEXIF(binary.BigEndian, nil)...)...)
	xmpPayload := []byte(SampleXMP)
	primary := []byte("HEVC-PRIMARY-IMAGE-DATA")
	thumbData := []byte("HEVC-THUMBNAIL-DATA")

	infe := func(id uint16, typ, name, mime string) []byte {
		b := []byte{2, 0, 0, 0}
		b = append(b, u16(id)...)
		b = append(b, 0, 0)
		b = append(b, typ...)
		b = append(b, name...)
		b = append(b, 0)
		if mime != "" {
			b = append(b, mime...)
			b = append(b, 0)
		}
		return box("infe", b)
	}
	entries := [][]byte{infe(1, "hvc1", "", ""), infe(2, "Exif", "", ""), infe(3, "mime", "", "application/rdf+xml")}
	payloads := [][]byte{primary, exifPayload, xmpPayload}
	if withThumb {
		entries = append(entries, infe(4, "hvc1", "", ""))
		payloads = append(payloads, thumbData)
	}
	iinfBody := append([]byte{0, 0, 0, 0}, u16(uint16(len(entries)))...)
	for _, e := range entries {
		iinfBody = append(iinfBody, e...)
	}
	iinf := box("iinf", iinfBody)
	iloc := func(base uint32) []byte {
		b := []byte{1, 0, 0, 0, 0x44, 0x00} // v1, offset_size=4, length_size=4, base_offset_size=0
		b = append(b, u16(uint16(len(payloads)))...)
		off := base
		for i, pl := range payloads {
			b = append(b, u16(uint16(i+1))...)
			b = append(b, 0, 0) // construction_method 0
			b = append(b, 0, 0) // data_reference_index
			b = append(b, u16(1)...)
			b = append(b, u32(off)...)
			b = append(b, u32(uint32(len(pl)))...)
			off += uint32(len(pl))
		}
		return box("iloc", b)
	}
	var iref []byte
	if withThumb {
		iref = box("iref", []byte{0, 0, 0, 0}, box("thmb", u16(4), u16(1), u16(1)))
	}
	ftyp := box("ftyp", []byte("heic"), u32(0), []byte("mif1heic"))
	mk := func(base uint32) []byte {
		return box("meta", []byte{0, 0, 0, 0}, hdlr("pict", ""), box("pitm", []byte{0, 0, 0, 0}, u16(1)), iloc(base), iinf, iref)
	}
	pre := len(ftyp) + len(mk(0)) + 8
	var mdat []byte
	for _, pl := range payloads {
		mdat = append(mdat, pl...)
	}
	out := append(append([]byte{}, ftyp...), mk(uint32(pre))...)
	return append(out, box("mdat", mdat)...)
}

// DrawingSVG is an Inkscape-style SVG with metadata, editor namespaces,
// comments, title/desc and an embedded JPEG data URI carrying EXIF.
func DrawingSVG() []byte {
	img := base64.StdEncoding.EncodeToString(CoverJPEG())
	return []byte(`<?xml version="1.0" encoding="UTF-8" standalone="no"?>
<!-- Created with Inkscape (http://www.inkscape.org/) -->
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"
   xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape"
   xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"
   xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
   xmlns:dc="http://purl.org/dc/elements/1.1/"
   xmlns:cc="http://creativecommons.org/ns#"
   width="100" height="100" viewBox="0 0 100 100"
   inkscape:version="1.2 (dc2aedaf03, 2022-05-15)"
   sodipodi:docname="secret-plans.svg"
   inkscape:export-filename="/home/jane/Desktop/secret-plans.png">
  <title>My Drawing</title>
  <desc>` + Comment + `</desc>
  <metadata>
    <rdf:RDF>
      <cc:Work rdf:about="">
        <dc:creator><cc:Agent><dc:title>` + ArtistName + `</dc:title></cc:Agent></dc:creator>
        <dc:date>2023-04-12</dc:date>
      </cc:Work>
    </rdf:RDF>
  </metadata>
  <sodipodi:namedview id="base" inkscape:window-width="1920" inkscape:current-layer="layer1"/>
  <style><![CDATA[ .a { fill: #f00; } ]]></style>
  <g inkscape:label="Layer 1" inkscape:groupmode="layer" id="layer1">
    <rect class="a" x="10" y="10" width="80" height="80"/>
    <image x="0" y="0" width="8" height="8" xlink:href="data:image/jpeg;base64,` + img + `"/>
  </g>
</svg>
`)
}
