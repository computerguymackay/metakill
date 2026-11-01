package handlers

import (
	"bytes"
	"testing"

	mp4 "github.com/abema/go-mp4"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/report"
)

func TestMP4(t *testing.T) {
	data := fixtures.ClipMP4()
	tests := []struct {
		name      string
		opts      Options
		wantTags  bool
		wantCover bool
	}{
		{"default", defaultOpts, false, false},
		{"keep-tags", Options{ZeroPad: true, KeepTags: true}, true, false},
		{"keep-thumbnails", Options{ZeroPad: true, KeepTags: true, KeepThumbnails: true}, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "clip.mp4", "mp4", data, tc.opts)
			for _, want := range []struct {
				cat report.Category
				key string
			}{
				{report.CatGPS, "©xyz"}, {report.CatGPS, "location.ISO6709"}, {report.CatGPS, "timed metadata"},
				{report.CatTimestamp, "mvhd.creation_time"}, {report.CatTimestamp, "mdhd.modification_time"},
				{report.CatDevice, "com.apple.quicktime.model"}, {report.CatSoftware, "compressorname"},
				{report.CatSoftware, "hdlr:vide name"}, {report.CatSoftware, "©too"},
				{report.CatCustom, "non-zero padding"}, {report.CatSoftware, "xmp:CreatorTool"},
			} {
				if !hasField(pre.Fields, want.cat, want.key) {
					t.Errorf("inspect missing %s/%s", want.cat, want.key)
				}
			}
			assertAbsent(t, out, secrets...)
			assertAbsent(t, out, "37.7749", "GoPro", "Core Media", string(fixtures.TelemetrySample))

			info, err := mp4.Probe(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("go-mp4 cannot probe output: %v", err)
			}
			if len(info.Tracks) != 1 {
				t.Fatalf("got %d tracks, want 1 (telemetry track dropped)", len(info.Tracks))
			}
			chunks := info.Tracks[0].Chunks
			if len(chunks) != len(fixtures.VideoSamples) {
				t.Fatalf("got %d chunks", len(chunks))
			}
			for i, c := range chunks {
				s := fixtures.VideoSamples[i]
				got := out[c.DataOffset : c.DataOffset+uint64(len(s))]
				if !bytes.Equal(got, s) {
					t.Fatalf("chunk %d offset %d points at %q, want %q", i, c.DataOffset, got, s)
				}
			}
			for _, s := range []string{fixtures.SongTitle, fixtures.SongArtist, fixtures.SongAlbum} {
				if got := bytes.Contains(out, []byte(s)); got != tc.wantTags {
					t.Errorf("%q present = %v, want %v", s, got, tc.wantTags)
				}
			}
			// ©day is rewritten as a UTF-8 data atom holding only the year.
			if tc.wantTags && !bytes.Contains(out, []byte("day\x00\x00\x00\x14data\x00\x00\x00\x01\x00\x00\x00\x002023")) {
				t.Error("year not kept as 2023")
			}
			if got := bytes.Contains(out, []byte("covr")); got != tc.wantCover {
				t.Errorf("cover present = %v, want %v", got, tc.wantCover)
			}
		})
	}
}

func TestMP4NoMoovErrors(t *testing.T) {
	data := fixtures.ClipMP4()
	// ftyp + mdat only
	bad := append(append([]byte{}, data[:24]...), []byte("\x00\x00\x00\x08mdat")...)
	if _, err := (mp4Handler{}).Inspect(bytes.NewReader(bad), defaultOpts); err == nil {
		t.Fatal("expected error without moov")
	}
}
