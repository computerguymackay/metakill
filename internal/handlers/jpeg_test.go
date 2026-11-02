package handlers

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/meta/exif"
	"github.com/computerguymackay/metakill/report"
)

func decodeJPEG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

func samePixels(t *testing.T, a, b image.Image) {
	t.Helper()
	if a.Bounds() != b.Bounds() {
		t.Fatalf("bounds changed: %v vs %v", a.Bounds(), b.Bounds())
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			r1, g1, b1, a1 := a.At(x, y).RGBA()
			r2, g2, b2, a2 := b.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 || a1 != a2 {
				t.Fatalf("pixel (%d,%d) changed", x, y)
			}
		}
	}
}

// orientationOf returns the Orientation in a JPEG's EXIF, or 0.
func orientationOf(t *testing.T, b []byte) (uint16, bool) {
	t.Helper()
	i := bytes.Index(b, []byte("Exif\x00\x00"))
	if i < 0 {
		return 0, false
	}
	f, err := exif.Parse(b[i+6:])
	if err != nil {
		t.Fatalf("parse kept EXIF: %v", err)
	}
	s := f.Summarize()
	return s.Orientation, len(s.Thumbnail) > 0
}

func TestJPEG(t *testing.T) {
	photo := fixtures.PhotoJPEG()
	tests := []struct {
		name      string
		opts      Options
		wantICC   bool
		wantThumb bool
		wantCats  []report.Category
	}{
		{"default", defaultOpts, false, false,
			[]report.Category{report.CatGPS, report.CatTimestamp, report.CatDevice, report.CatAuthor, report.CatSoftware, report.CatThumbnail, report.CatICC, report.CatCustom}},
		{"keep-icc", Options{ZeroPad: true, KeepICC: true}, true, false, nil},
		{"keep-thumbnails", Options{ZeroPad: true, KeepTags: true, KeepThumbnails: true}, false, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "photo.jpg", "jpeg", photo, tc.opts)
			for _, c := range tc.wantCats {
				found := false
				for _, f := range pre.Fields {
					if f.Category == c {
						found = true
					}
				}
				if !found {
					t.Errorf("inspect found no %s field", c)
				}
			}
			assertAbsent(t, out, secrets...)
			assertAbsent(t, out, "San Francisco", "TRAILER", "http://ns.adobe.com/xap")
			if got := bytes.Contains(out, []byte("ICC_PROFILE")); got != tc.wantICC {
				t.Errorf("ICC present = %v, want %v", got, tc.wantICC)
			}
			o, thumb := orientationOf(t, out)
			if o != 6 {
				t.Errorf("orientation = %d, want 6 (must be preserved)", o)
			}
			if thumb != tc.wantThumb {
				t.Errorf("thumbnail present = %v, want %v", thumb, tc.wantThumb)
			}
			samePixels(t, decodeJPEG(t, photo), decodeJPEG(t, out))
			if len(out) >= len(photo) {
				t.Errorf("output not smaller: %d >= %d", len(out), len(photo))
			}
		})
	}
}

func TestJPEGCleanIsUnchanged(t *testing.T) {
	clean := fixtures.BaseJPEG(16, 16)
	out, pre, _ := roundTrip(t, "clean.jpg", "jpeg", clean, defaultOpts)
	if pre.TotalFields() != 0 {
		t.Fatalf("clean JPEG reported fields: %+v", pre.Fields)
	}
	if !bytes.Equal(out, clean) {
		t.Fatal("clean JPEG was modified")
	}
}

func TestJPEGProgressiveScansSurvive(t *testing.T) {
	// A second SOS with DHT between scans (as in progressive JPEGs) must be
	// copied intact, and a COM between scans must still be removed.
	base := fixtures.BaseJPEG(16, 16)
	eoi := bytes.LastIndex(base, []byte{0xFF, 0xD9})
	extra := append(fixtures.Segment(0xFE, []byte(fixtures.Comment)), base[eoi:]...)
	data := append(append([]byte{}, base[:eoi]...), extra...)
	out, _, _ := roundTrip(t, "p.jpg", "jpeg", data, defaultOpts)
	assertAbsent(t, out, fixtures.Comment)
	if !bytes.Equal(out, base) {
		t.Fatal("expected output identical to base image")
	}
}

func TestJPEGTruncatedErrors(t *testing.T) {
	h := jpegHandler{}
	_, err := h.Inspect(bytes.NewReader([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x10}), defaultOpts)
	if err == nil {
		t.Fatal("expected error on truncated segment")
	}
}
