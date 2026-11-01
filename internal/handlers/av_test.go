package handlers

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/meta/vorbis"
)

var keepTagOpts = Options{ZeroPad: true, KeepTags: true}
var keepThumbOpts = Options{ZeroPad: true, KeepTags: true, KeepThumbnails: true}

func TestFLAC(t *testing.T) {
	data := fixtures.TrackFLAC()
	for _, tc := range []struct {
		name     string
		opts     Options
		wantTags bool
		wantPic  bool
	}{{"default", defaultOpts, false, false}, {"keep-tags", keepTagOpts, true, false}, {"keep-thumbnails", keepThumbOpts, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, _ := roundTrip(t, "track.flac", "flac", data, tc.opts)
			assertAbsent(t, out, fixtures.Encoder, fixtures.Comment, fixtures.ArtistName, fixtures.Serial, "libFLAC", "riff", "2023-04-12")
			assertPresent(t, out, "fLaC", string(fixtures.AudioPayload))
			if !bytes.HasSuffix(out, fixtures.AudioPayload) {
				t.Error("ID3v1 or trailing data left after audio")
			}
			if got := bytes.Contains(out, []byte(fixtures.SongTitle)); got != tc.wantTags {
				t.Errorf("title kept = %v, want %v", got, tc.wantTags)
			}
			if tc.wantTags {
				assertPresent(t, out, "DATE=2023\x00"[:9])
			}
			// Walk metadata blocks: last flag must be set exactly once.
			pos, lasts, pic := 4, 0, false
			for {
				h := out[pos]
				n := int(out[pos+1])<<16 | int(out[pos+2])<<8 | int(out[pos+3])
				if h&0x7F == 6 {
					pic = true
					p, err := vorbis.ParsePicture(out[pos+4 : pos+4+n])
					if err != nil || p.Description != "" || bytes.Contains(p.Data, []byte(fixtures.Make)) {
						t.Errorf("kept picture not cleaned: %v", err)
					}
				}
				pos += 4 + n
				if h&0x80 != 0 {
					lasts++
					break
				}
			}
			if pic != tc.wantPic {
				t.Errorf("picture kept = %v, want %v", pic, tc.wantPic)
			}
			if !bytes.Equal(out[pos:pos+2], []byte{0xFF, 0xF8}) {
				t.Error("audio does not follow the last metadata block")
			}
		})
	}
}

// checkOgg verifies page CRCs, contiguous sequence numbers and returns the
// concatenated page payloads.
func checkOgg(t *testing.T, b []byte) []byte {
	t.Helper()
	var all []byte
	next := map[uint32]uint32{}
	r := bytes.NewReader(b)
	for r.Len() > 0 {
		pg, err := readOggPage(r)
		if err != nil {
			t.Fatalf("bad page: %v", err)
		}
		enc := pg.encode()
		start := len(b) - r.Len() - len(enc)
		if !bytes.Equal(enc, b[start:start+len(enc)]) {
			t.Fatalf("page at %d has a wrong CRC", start)
		}
		if want, ok := next[pg.serial]; ok && pg.seq != want {
			t.Fatalf("sequence %d, want %d", pg.seq, want)
		}
		next[pg.serial] = pg.seq + 1
		all = append(all, pg.data...)
	}
	return all
}

func TestOgg(t *testing.T) {
	for _, codec := range []string{"vorbis", "opus"} {
		for _, tc := range []struct {
			name     string
			opts     Options
			wantTags bool
		}{{"default", defaultOpts, false}, {"keep-tags", keepTagOpts, true}, {"keep-thumbnails", keepThumbOpts, true}} {
			t.Run(codec+"/"+tc.name, func(t *testing.T) {
				data := fixtures.VoiceOgg(codec)
				out, _, _ := roundTrip(t, "voice."+codec, "ogg", data, tc.opts)
				payload := checkOgg(t, out)
				assertAbsent(t, payload, fixtures.Encoder, fixtures.Comment, fixtures.ArtistName, "libopus", "libVorbis", "2023-04-12")
				assertPresent(t, payload, string(fixtures.AudioPayload[:20]), string(fixtures.AudioPayload[20:]))
				if codec == "vorbis" {
					assertPresent(t, payload, "SETUP-HEADER")
				}
				if got := bytes.Contains(payload, []byte(fixtures.SongTitle)); got != tc.wantTags {
					t.Errorf("title kept = %v, want %v", got, tc.wantTags)
				}
				if got := bytes.Contains(payload, []byte("METADATA_BLOCK_PICTURE")); got != tc.opts.KeepThumbnails {
					t.Errorf("picture kept = %v, want %v", got, tc.opts.KeepThumbnails)
				}
			})
		}
	}
}

func TestOggFLACRefused(t *testing.T) {
	data := []byte("OggS\x00\x02" + string(make([]byte, 20)) + "\x01\x05\x7fFLAC")
	binary.LittleEndian.PutUint32(data[22:], 0)
	if _, err := (oggHandler{}).Inspect(bytes.NewReader(data), defaultOpts); err == nil {
		t.Fatal("Ogg FLAC should be refused")
	}
}

func TestWAV(t *testing.T) {
	data := fixtures.MemoWAV()
	for _, tc := range []struct {
		name     string
		opts     Options
		wantTags bool
	}{{"default", defaultOpts, false}, {"keep-tags", keepTagOpts, true}} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, _ := roundTrip(t, "memo.wav", "wav", data, tc.opts)
			if len(out) != len(data) {
				t.Fatalf("size changed: WAV is cleaned in place")
			}
			assertAbsent(t, out, fixtures.Encoder, fixtures.Comment, fixtures.ArtistName, fixtures.Serial, fixtures.Model, "2023-04-12", "bext", "iXML")
			assertPresent(t, out, "fmt ", string(fixtures.AudioPayload))
			if got := bytes.Contains(out, []byte(fixtures.SongTitle)); got != tc.wantTags {
				t.Errorf("title kept = %v, want %v", got, tc.wantTags)
			}
		})
	}
}

func TestAVI(t *testing.T) {
	data := fixtures.CamAVI()
	out, pre, _ := roundTrip(t, "cam.avi", "avi", data, defaultOpts)
	if len(out) != len(data) {
		t.Fatal("size changed: AVI is cleaned in place")
	}
	if !pre.FullyClean {
		t.Errorf("unexpected unclean: %v", pre.Warnings)
	}
	assertAbsent(t, out, fixtures.Encoder, fixtures.Comment, fixtures.ArtistName, fixtures.Serial, fixtures.Model, fixtures.Make, "2023", "ncdt", "IDIT")
	assertPresent(t, out, "hdrl", "strh", "movi", string(fixtures.AudioPayload), "idx1")
	// movi must not have moved (indexes are relative to it).
	if bytes.Index(out, []byte("movi")) != bytes.Index(data, []byte("movi")) {
		t.Fatal("movi moved")
	}
}

func TestMKV(t *testing.T) {
	data := fixtures.MovieMKV()
	for _, tc := range []struct {
		name     string
		opts     Options
		wantTags bool
		wantAtt  bool
	}{{"default", defaultOpts, false, false}, {"keep-tags", keepTagOpts, true, false}, {"keep-thumbnails", keepThumbOpts, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, _ := roundTrip(t, "movie.mkv", "mkv", data, tc.opts)
			if len(out) != len(data) {
				t.Fatal("size changed: MKV is cleaned in place")
			}
			assertAbsent(t, out, fixtures.Encoder, fixtures.Comment, fixtures.Software, fixtures.Model, "libebml", "mkvmerge", "0123456789abcdef")
			assertPresent(t, out, "matroska", "V_MPEG4/ISO/AVC", string(fixtures.AudioPayload))
			if got := bytes.Contains(out, []byte(fixtures.SongArtist)); got != tc.wantTags {
				t.Errorf("artist tag kept = %v, want %v", got, tc.wantTags)
			}
			if got := bytes.Contains(out, []byte("cover.jpg")); got != tc.wantAtt {
				t.Errorf("attachment kept = %v, want %v", got, tc.wantAtt)
			}
			// The cleaned file must parse cleanly element by element.
			if _, err := mkvChildren(bytes.NewReader(out), 0, int64(len(out))); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEBMLVoid(t *testing.T) {
	for n := int64(2); n < 300; n++ {
		v := voidElem(n)
		if int64(len(v)) != n {
			t.Fatalf("void(%d) has length %d", n, len(v))
		}
		e, err := readElem(bytes.NewReader(v), 0)
		if err != nil || e.id != idVoid || e.total() != n {
			t.Fatalf("void(%d) does not parse back: %+v %v", n, e, err)
		}
	}
	for n := int64(3); n < 40; n++ {
		b := emptyElem(idMuxingApp, 2, n)
		if int64(len(b)) != n {
			t.Fatalf("emptyElem(%d) length %d", n, len(b))
		}
	}
}
