package fixtures

import (
	"encoding/binary"

	"github.com/computerguymackay/metakill/internal/meta/iptc"
)

func init() {
	register("photo.jpg", func() Fixture { return Fixture{Name: "photo.jpg", Format: "jpeg", Data: PhotoJPEG()} })
	register("clean.jpg", func() Fixture {
		return Fixture{Name: "clean.jpg", Format: "jpeg", Data: BaseJPEG(16, 16), Clean: true}
	})
}

// PhotoJPEG is a JPEG with EXIF (incl. GPS and a thumbnail), XMP, ICC,
// IPTC, a comment and trailing data.
func PhotoJPEG() []byte {
	thumb := BaseJPEG(8, 8)
	ex := append([]byte("Exif\x00\x00"), SampleEXIF(binary.BigEndian, thumb)...)
	x := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), SampleXMP...)
	icc := append([]byte("ICC_PROFILE\x00\x01\x01"), FakeICC...)
	ps := append(append([]byte{}, iptc.PhotoshopHeader...), SampleIPTC()...)
	j := InsertAfterSOI(BaseJPEG(16, 16),
		Segment(0xE1, ex),
		Segment(0xE1, x),
		Segment(0xE2, icc),
		Segment(0xED, ps),
		Segment(0xFE, []byte(Comment)),
	)
	return append(j, []byte("TRAILER-motion-photo-data")...)
}

func init() {
	register("image.png", func() Fixture { return Fixture{Name: "image.png", Format: "png", Data: ImagePNG()} })
}

// ImagePNG is a PNG with tEXt, zTXt, iTXt (XMP), eXIf, tIME, iCCP, a private
// ancillary chunk and trailing data.
func ImagePNG() []byte {
	base := BasePNG(16, 16)
	// Split after IHDR (8-byte signature + 25-byte IHDR chunk).
	head, tail := base[:33], base[33:]
	var mid []byte
	mid = PNGChunk(mid, "iCCP", append([]byte("sRGB\x00\x00"), deflate(FakeICC)...))
	mid = PNGChunk(mid, "tEXt", []byte("Author\x00"+ArtistName))
	mid = PNGChunk(mid, "tEXt", []byte("Software\x00"+Software))
	mid = PNGChunk(mid, "zTXt", append([]byte("Comment\x00\x00"), deflate([]byte(Comment))...))
	mid = PNGChunk(mid, "iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"+SampleXMP))
	mid = PNGChunk(mid, "eXIf", SampleEXIF(binary.LittleEndian, nil))
	mid = PNGChunk(mid, "tIME", []byte{0x07, 0xE7, 4, 12, 18, 33, 2})
	mid = PNGChunk(mid, "prVW", []byte("private preview"))
	out := append(append(append([]byte{}, head...), mid...), tail...)
	return append(out, "trailing junk"...)
}
