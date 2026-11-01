package handlers

import (
	"bytes"
	"testing"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/meta/id3"
	"github.com/computerguymackay/metakill/internal/report"
)

// audioPayload returns the bytes after the LAME info frame (pure audio).
func audioPayload(b []byte) []byte {
	i := bytes.Index(b, fixtures.MP3FrameHeader)
	if i < 0 {
		return nil
	}
	j := bytes.Index(b[i+4:], fixtures.MP3FrameHeader)
	return b[i+4+j : i+4+j+6*417]
}

func TestMP3(t *testing.T) {
	data := fixtures.SongMP3()
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
			out, pre, _ := roundTrip(t, "song.mp3", "mp3", data, tc.opts)
			if !tc.opts.KeepTags {
				for _, want := range []struct {
					cat report.Category
					key string
				}{
					{report.CatIDTag, "TIT2"}, {report.CatSoftware, "TSSE"}, {report.CatThumbnail, "APIC"},
					{report.CatSoftware, "EncoderVersion"}, {report.CatSoftware, "Encoder"},
					{report.CatIDTag, "Title"}, {report.CatCustom, "PRIV:www.amazon.com"},
				} {
					if !hasField(pre.Fields, want.cat, want.key) {
						t.Errorf("inspect missing %s/%s", want.cat, want.key)
					}
				}
			}
			assertAbsent(t, out, secrets...)
			assertAbsent(t, out, "LAME3.100", "APETAGEX", "TAG"+fixtures.SongTitle, "TDAT")
			if !bytes.Equal(audioPayload(out), audioPayload(data)) {
				t.Fatal("audio frames changed")
			}
			hasTag := bytes.HasPrefix(out, []byte("ID3"))
			if hasTag != tc.wantTags {
				t.Fatalf("leading ID3 present = %v, want %v", hasTag, tc.wantTags)
			}
			if !tc.wantTags {
				if !bytes.HasPrefix(out, fixtures.MP3FrameHeader) {
					t.Fatal("output should start with the first audio frame")
				}
				return
			}
			tag, err := id3.Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			cover := false
			for _, f := range tag.Frames {
				info := id3.Describe(f, tag.Major)
				got[f.ID] = info.Value
				if f.ID == "APIC" {
					cover = true
					assertAbsent(t, info.Picture.Data, fixtures.Make)
				}
			}
			want := map[string]string{"TIT2": fixtures.SongTitle, "TPE1": fixtures.SongArtist,
				"TALB": fixtures.SongAlbum, "TRCK": "3/12", "TDRC": "2023"}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("kept %s = %q, want %q", k, got[k], v)
				}
			}
			if len(got) != len(want)+map[bool]int{true: 1, false: 0}[cover] {
				t.Errorf("unexpected kept frames: %v", got)
			}
			if cover != tc.wantCover {
				t.Errorf("cover kept = %v, want %v", cover, tc.wantCover)
			}
		})
	}
}

func TestMP3LameCRCUpdated(t *testing.T) {
	out, _, _ := roundTrip(t, "song.mp3", "mp3", fixtures.SongMP3(), defaultOpts)
	lame := 4 + 32 + 8 + 4 + 4 + 100 + 4
	got := uint16(out[lame+34])<<8 | uint16(out[lame+35])
	if got != crc16(out[:lame+34]) {
		t.Fatal("LAME info CRC not recomputed")
	}
}
