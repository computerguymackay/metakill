package handlers

import (
	"bytes"
	"encoding/base64"
	"image/gif"
	"regexp"
	"testing"

	"github.com/beevik/etree"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/report"
)

func TestGIF(t *testing.T) {
	data := fixtures.AnimGIF()
	for _, tc := range []struct {
		name    string
		opts    Options
		wantICC bool
	}{{"default", defaultOpts, false}, {"keep-icc", Options{ZeroPad: true, KeepICC: true}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "anim.gif", "gif", data, tc.opts)
			if !hasField(pre.Fields, report.CatCustom, "Comment") || !hasField(pre.Fields, report.CatAuthor, "dc:creator") {
				t.Errorf("missing fields: %+v", pre.Fields)
			}
			assertAbsent(t, out, fixtures.Comment, fixtures.ArtistName, "XMP DataXMP", "MGKIPTC", "trailing")
			assertPresent(t, out, "NETSCAPE2.0")
			if got := bytes.Contains(out, []byte("ICCRGBG1012")); got != tc.wantICC {
				t.Errorf("ICC present = %v, want %v", got, tc.wantICC)
			}
			a, _ := gif.Decode(bytes.NewReader(data))
			b, err := gif.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("output does not decode: %v", err)
			}
			samePixels(t, a, b)
		})
	}
}

func TestWebP(t *testing.T) {
	data := fixtures.PicWebP()
	for _, tc := range []struct {
		name    string
		opts    Options
		wantICC bool
	}{{"default", defaultOpts, false}, {"keep-icc", Options{ZeroPad: true, KeepICC: true}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "pic.webp", "webp", data, tc.opts)
			if !hasField(pre.Fields, report.CatGPS, "GPSLatitude") || !hasField(pre.Fields, report.CatDevice, "Make") {
				t.Errorf("missing fields: %+v", pre.Fields)
			}
			assertAbsent(t, out, secrets...)
			assertAbsent(t, out, "EXIF", "XMP ")
			if got := bytes.Contains(out, []byte("ICCP")); got != tc.wantICC {
				t.Errorf("ICCP present = %v, want %v", got, tc.wantICC)
			}
			// VP8X flags must match the remaining chunks.
			flags := out[20]
			if flags&(0x08|0x04) != 0 || (flags&0x20 != 0) != tc.wantICC {
				t.Errorf("VP8X flags = %#x", flags)
			}
			if _, err := webp.Decode(bytes.NewReader(out)); err != nil {
				t.Fatalf("output does not decode: %v", err)
			}
		})
	}
}

func TestTIFF(t *testing.T) {
	data := fixtures.ScanTIFF()
	for _, tc := range []struct {
		name      string
		opts      Options
		wantPages int
	}{{"default", defaultOpts, 1}, {"keep-thumbnails", Options{ZeroPad: true, KeepTags: true, KeepThumbnails: true}, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "scan.tiff", "tiff", data, tc.opts)
			for _, k := range []string{"Make", "Software", "Artist", "GPSLatitude", "BodySerialNumber", "xmp:CreatorTool"} {
				found := false
				for _, f := range pre.Fields {
					if f.Key == k {
						found = true
					}
				}
				if !found {
					t.Errorf("inspect missing %s", k)
				}
			}
			assertAbsent(t, out, secrets...)
			assertAbsent(t, out, "x:xmpmeta")
			a, err := tiff.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			b, err := tiff.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("output does not decode: %v", err)
			}
			samePixels(t, a, b)
			// Count IFDs by walking the chain.
			pages := 0
			off := int(uint32(out[4]) | uint32(out[5])<<8 | uint32(out[6])<<16 | uint32(out[7])<<24)
			for off != 0 && pages < 10 {
				pages++
				n := int(out[off]) | int(out[off+1])<<8
				p := off + 2 + 12*n
				off = int(uint32(out[p]) | uint32(out[p+1])<<8 | uint32(out[p+2])<<16 | uint32(out[p+3])<<24)
			}
			if pages != tc.wantPages {
				t.Errorf("pages = %d, want %d", pages, tc.wantPages)
			}
		})
	}
}

func TestTIFFBigTIFFRefused(t *testing.T) {
	big := []byte("II+\x00\x08\x00\x00\x00\x10\x00\x00\x00\x00\x00\x00\x00")
	if _, err := (tiffHandler{}).Inspect(bytes.NewReader(big), defaultOpts); err == nil {
		t.Fatal("BigTIFF should be refused")
	}
}

func TestHEIF(t *testing.T) {
	data := fixtures.PhoneHEIC(false)
	out, pre, _ := roundTrip(t, "phone.heic", "heif", data, defaultOpts)
	if !pre.FullyClean {
		t.Errorf("unexpected unclean: %v", pre.Warnings)
	}
	assertAbsent(t, out, secrets...)
	assertAbsent(t, out, "x:xmpmeta")
	if len(out) != len(data) {
		t.Fatalf("size changed %d -> %d (HEIF is zeroed in place)", len(data), len(out))
	}
	assertPresent(t, out, "HEVC-PRIMARY-IMAGE-DATA", "iloc", "iinf")
}

func TestHEIFThumbnailIsReportedUnclean(t *testing.T) {
	data := fixtures.PhoneHEIC(true)
	_, pre, _ := roundTrip(t, "phone.heic", "heif", data, defaultOpts)
	if pre.FullyClean {
		t.Fatal("thumbnail items are kept, so the file must not be reported fully clean")
	}
	_, pre, _ = roundTrip(t, "phone.heic", "heif", data, Options{ZeroPad: true, KeepTags: true, KeepThumbnails: true})
	if !pre.FullyClean {
		t.Fatalf("with -t the kept thumbnail is intended: %v", pre.Warnings)
	}
}

func TestSVG(t *testing.T) {
	data := fixtures.DrawingSVG()
	for _, tc := range []struct {
		name      string
		opts      Options
		wantTitle bool
	}{{"default", defaultOpts, false}, {"keep-tags", Options{ZeroPad: true, KeepTags: true}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "drawing.svg", "svg", data, tc.opts)
			if len(pre.Embedded) != 1 {
				t.Errorf("embedded data URI image not reported")
			}
			assertAbsent(t, out, fixtures.Comment, fixtures.ArtistName, "inkscape", "sodipodi", "secret-plans",
				"/home/jane", "<metadata", "<desc", "Created with", "rdf:", "dc:")
			assertPresent(t, out, `<rect class="a"`, "<![CDATA[", `xmlns="http://www.w3.org/2000/svg"`, `id="layer1"`)
			if got := bytes.Contains(out, []byte("<title>My Drawing</title>")); got != tc.wantTitle {
				t.Errorf("title present = %v, want %v", got, tc.wantTitle)
			}
			doc := etree.NewDocument()
			if err := doc.ReadFromBytes(out); err != nil {
				t.Fatalf("output is not well-formed XML: %v", err)
			}
			m := regexp.MustCompile(`base64,([A-Za-z0-9+/=]+)`).FindSubmatch(out)
			if m == nil {
				t.Fatal("embedded image lost")
			}
			img, _ := base64.StdEncoding.DecodeString(string(m[1]))
			assertAbsent(t, img, fixtures.Make, fixtures.Model)
			if bytes.Contains(out, []byte("\n  \n")) {
				t.Error("blank lines left where elements were removed")
			}
		})
	}
}
