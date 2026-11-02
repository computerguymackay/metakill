package handlers

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/report"
)

func TestPNG(t *testing.T) {
	data := fixtures.ImagePNG()
	tests := []struct {
		name    string
		opts    Options
		wantICC bool
	}{
		{"default", defaultOpts, false},
		{"keep-icc", Options{ZeroPad: true, KeepICC: true}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "image.png", "png", data, tc.opts)
			for _, want := range []struct {
				cat report.Category
				key string
			}{
				{report.CatAuthor, "Author"}, {report.CatSoftware, "Software"},
				{report.CatCustom, "Comment"}, {report.CatGPS, "GPSLatitude"},
				{report.CatTimestamp, "tIME"}, {report.CatCustom, "prVW"},
				{report.CatCustom, "TrailingData"}, {report.CatDevice, "Make"},
			} {
				if !hasField(pre.Fields, want.cat, want.key) {
					t.Errorf("inspect missing %s/%s", want.cat, want.key)
				}
			}
			assertAbsent(t, out, secrets...)
			assertAbsent(t, out, "tEXt", "zTXt", "iTXt", "tIME", "prVW", "trailing junk")
			if got := bytes.Contains(out, []byte("iCCP")); got != tc.wantICC {
				t.Errorf("iCCP present = %v, want %v", got, tc.wantICC)
			}
			if !bytes.Contains(out, []byte("eXIf")) {
				t.Error("eXIf with Orientation should be kept")
			}
			a, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			b, err := png.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("stripped PNG does not decode: %v", err)
			}
			samePixels(t, a, b)
		})
	}
}

func TestPNGTruncated(t *testing.T) {
	data := fixtures.BasePNG(4, 4)
	if _, err := (pngHandler{}).Inspect(bytes.NewReader(data[:40]), defaultOpts); err == nil {
		t.Fatal("expected error for truncated PNG")
	}
}
