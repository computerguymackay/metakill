package handlers

import (
	"archive/tar"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"io"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/computerguymackay/metakill/internal/fixtures"
)

// decompress undoes a wrapper with an independent reader.
func decompress(t *testing.T, kind string, b []byte) []byte {
	t.Helper()
	var r io.Reader
	var err error
	switch kind {
	case "gz":
		zr, e := gzip.NewReader(bytes.NewReader(b))
		if e == nil {
			if zr.Name != "" || zr.Comment != "" || zr.ModTime.Unix() > 0 || len(zr.Extra) > 0 {
				t.Errorf("gzip header kept metadata: %+v", zr.Header)
			}
		}
		r, err = zr, e
	case "xz":
		r, err = xz.NewReader(bytes.NewReader(b))
	case "zst":
		zr, e := zstd.NewReader(bytes.NewReader(b))
		if e == nil {
			defer zr.Close()
		}
		r, err = zr, e
	case "bz2":
		r = bzip2.NewReader(bytes.NewReader(b))
	default:
		return b
	}
	if err != nil {
		t.Fatalf("%s: %v", kind, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("%s: output does not decompress: %v", kind, err)
	}
	return out
}

type tarMember struct {
	hdr  *tar.Header
	data []byte
}

func readTar(t *testing.T, b []byte) map[string]tarMember {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(b))
	out := map[string]tarMember{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("output is not a valid tar: %v", err)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			t.Error("global PAX header kept")
		}
		if h.ModTime.Unix() != 0 || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
			t.Errorf("%s: times kept: %v %v %v", h.Name, h.ModTime, h.AccessTime, h.ChangeTime)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: owner kept: %d:%d %s:%s", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		for k := range h.PAXRecords {
			if !paxStructural[k] {
				t.Errorf("%s: PAX record kept: %s", h.Name, k)
			}
		}
		d, _ := io.ReadAll(tr)
		out[h.Name] = tarMember{h, d}
	}
}

func TestTar(t *testing.T) {
	plain := fixtures.BundleTAR()
	for _, kind := range []string{"", "gz", "xz", "zst", "bz2"} {
		name := "bundle.tar"
		if kind != "" {
			name += "." + kind
		}
		t.Run(name, func(t *testing.T) {
			data := fixtures.Compress(kind, plain)
			out, pre, _ := roundTrip(t, name, "tar", data, defaultOpts)
			wantFormat := "tar"
			if kind != "" {
				wantFormat += "." + kind
			}
			if pre.Format != wantFormat {
				t.Errorf("format %q, want %q", pre.Format, wantFormat)
			}
			for _, key := range []string{"owners", "extended attributes", "member timestamps (latest)", "access/change times", "global PAX headers", "docs/.DS_Store", "docs/._photo.jpg"} {
				found := false
				for _, f := range pre.Fields {
					if f.Key == key {
						found = true
					}
				}
				if !found {
					t.Errorf("inspect missing %q", key)
				}
			}
			if len(pre.Embedded) != 3 {
				t.Errorf("expected photo, mp3 and inner zip cleaned, got %d", len(pre.Embedded))
			}
			m := readTar(t, decompress(t, kind, out))
			for _, junk := range []string{"docs/.DS_Store", "docs/._photo.jpg"} {
				if _, ok := m[junk]; ok {
					t.Errorf("junk kept: %s", junk)
				}
			}
			for name, want := range map[string]string{"docs/readme.txt": fixtures.ReadmeBody, "docs/run.sh": fixtures.ScriptBody,
				fixtures.LongName: fixtures.ReadmeBody, "Users/jane/notes.txt": fixtures.ReadmeBody} {
				if got := string(m[name].data); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			checks := []struct {
				name string
				typ  byte
				mode int64
				link string
			}{
				{"docs/", tar.TypeDir, 0o755, ""},
				{"docs/run.sh", tar.TypeReg, 0o755, ""},
				{"docs/readme.txt", tar.TypeReg, 0o644, ""},
				{"docs/latest", tar.TypeSymlink, 0o777, "readme.txt"},
				{"docs/readme-link.txt", tar.TypeLink, 0o644, "docs/readme.txt"},
			}
			for _, c := range checks {
				h := m[c.name].hdr
				if h == nil {
					t.Errorf("%s missing", c.name)
					continue
				}
				if h.Typeflag != c.typ || h.Mode != c.mode || h.Linkname != c.link {
					t.Errorf("%s: type %c mode %o link %q", c.name, h.Typeflag, h.Mode, h.Linkname)
				}
			}
			assertAbsent(t, m["docs/photo.jpg"].data, fixtures.Make, fixtures.Model)
			assertAbsent(t, m["docs/song.mp3"].data, fixtures.Encoder)
			inner := readZip(t, m["docs/inner.zip"].data)
			assertAbsent(t, inner["inner/photo.jpg"].data, fixtures.Make)
		})
	}
}

func TestCompressedSingleFiles(t *testing.T) {
	for _, kind := range []string{"gz", "xz", "zst", "bz2"} {
		t.Run("svg."+kind, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "drawing.svg."+kind, "tar", fixtures.Compress(kind, fixtures.DrawingSVG()), defaultOpts)
			if pre.Format != kind {
				t.Errorf("format %q", pre.Format)
			}
			svg := decompress(t, kind, out)
			assertAbsent(t, svg, "inkscape", fixtures.ArtistName)
			assertPresent(t, svg, `<rect class="a"`)
		})
	}
	// Plain text: only the gzip header is cleaned; content is unchanged.
	out, pre, _ := roundTrip(t, "notes.txt.gz", "tar", fixtures.Compress("gz", []byte(fixtures.ReadmeBody)), defaultOpts)
	if len(pre.Fields) != 3 {
		t.Errorf("expected gzip name, comment and time, got %+v", pre.Fields)
	}
	if got := string(decompress(t, "gz", out)); got != fixtures.ReadmeBody {
		t.Errorf("content changed: %q", got)
	}
	// xz/zst/bz2 wrappers of unrecognised content carry no metadata.
	_, pre, _ = roundTrip(t, "notes.txt.xz", "tar", fixtures.Compress("xz", []byte(fixtures.ReadmeBody)), defaultOpts)
	if pre.TotalFields() != 0 {
		t.Errorf("xz of plain text reported fields: %+v", pre.Fields)
	}
}

func TestTarOversizedMember(t *testing.T) {
	saved := maxTarMember
	maxTarMember = 1000
	defer func() { maxTarMember = saved }()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	photo := fixtures.PhotoJPEG()
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "big.jpg", Size: int64(len(photo)), Mode: 0o644})
	_, _ = tw.Write(photo)
	big := bytes.Repeat([]byte("a"), 5000)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "big.txt", Size: int64(len(big)), Mode: 0o644})
	_, _ = tw.Write(big)
	_ = tw.Close()
	out, pre, _ := roundTrip(t, "big.tar", "tar", b.Bytes(), defaultOpts)
	if pre.FullyClean || len(pre.Warnings) != 1 {
		t.Fatalf("oversized recognised member must be reported once: %v", pre.Warnings)
	}
	m := readTar(t, out)
	if !bytes.Equal(m["big.jpg"].data, photo) || !bytes.Equal(m["big.txt"].data, big) {
		t.Fatal("oversized members must be copied unchanged")
	}
}

func TestTarCorruptIsError(t *testing.T) {
	data := fixtures.Compress("gz", fixtures.BundleTAR())
	data = data[:len(data)/2]
	if _, err := (tarHandler{}).Inspect(bytes.NewReader(data), defaultOpts); err == nil {
		t.Fatal("truncated tar.gz should be an error")
	}
}

func TestBzip2VerificationCatchesCorruption(t *testing.T) {
	w := &corruptingWriter{}
	_, err := (tarHandler{}).Strip(bytes.NewReader(fixtures.Compress("bz2", fixtures.BundleTAR())), w, defaultOpts)
	if err == nil {
		t.Fatal("corrupted bzip2 output was not detected")
	}
	if errors.Is(err, ErrSkip) {
		t.Fatal("wrong error kind")
	}
}

// corruptingWriter flips one byte in the middle of what is written.
type corruptingWriter struct {
	buf bytes.Buffer
	r   *bytes.Reader
}

func (c *corruptingWriter) Write(p []byte) (int, error) { return c.buf.Write(p) }

func (c *corruptingWriter) Seek(off int64, whence int) (int64, error) {
	b := c.buf.Bytes()
	if len(b) > 100 {
		b[len(b)/2] ^= 0xFF
	}
	r := bytes.NewReader(b)
	c.r = r
	return r.Seek(off, whence)
}

func (c *corruptingWriter) Read(p []byte) (int, error) { return c.r.Read(p) }
