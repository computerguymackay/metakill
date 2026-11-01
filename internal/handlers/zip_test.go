package handlers

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/computerguymackay/metakill/internal/fixtures"
)

type zipMember struct {
	file *zip.File
	data []byte
}

// readZip returns members by name and fails on unexpected archive metadata.
func readZip(t *testing.T, b []byte) map[string]zipMember {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}
	if zr.Comment != "" {
		t.Errorf("archive comment kept: %q", zr.Comment)
	}
	out := map[string]zipMember{}
	for _, f := range zr.File {
		if f.Modified.Year() != 1980 {
			t.Errorf("%s: timestamp %v", f.Name, f.Modified)
		}
		if identifyingExtra(f.Extra) {
			t.Errorf("%s: extra field kept", f.Name)
		}
		if f.Comment != "" {
			t.Errorf("%s: comment kept", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		d, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		out[f.Name] = zipMember{f, d}
	}
	return out
}

func TestZipArchive(t *testing.T) {
	data := fixtures.BundleZIP()
	out, pre, _ := roundTrip(t, "bundle.zip", "zip", data, defaultOpts)
	if len(pre.Embedded) != 4 {
		t.Errorf("expected 4 cleaned members (photo, mp3, docx, inner zip), got %d", len(pre.Embedded))
	}
	if len(pre.Warnings) != 1 {
		t.Errorf("expected one home-path warning, got %v", pre.Warnings)
	}
	m := readZip(t, out)

	for _, junk := range []string{"docs/.DS_Store", "docs/Thumbs.db", "__MACOSX/", "__MACOSX/docs/._photo.jpg"} {
		if _, ok := m[junk]; ok {
			t.Errorf("junk member kept: %s", junk)
		}
	}
	// Plain members pass through unchanged, names included.
	for name, want := range map[string]string{"docs/readme.txt": fixtures.ReadmeBody, "docs/run.sh": fixtures.ScriptBody,
		"Users/jane/notes.txt": fixtures.ReadmeBody, "docs/latest": "readme.txt"} {
		if got := string(m[name].data); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// Permissions: directories and executables keep their meaning.
	modes := map[string]os.FileMode{
		"docs/":           os.ModeDir | 0o755,
		"docs/run.sh":     0o755,
		"docs/readme.txt": 0o644,
		"docs/latest":     os.ModeSymlink | 0o777,
	}
	for name, want := range modes {
		if got := m[name].file.Mode(); got != want {
			t.Errorf("%s mode %v, want %v", name, got, want)
		}
	}
	// Unchanged members are copied as their original compressed bytes.
	orig := readZipRaw(t, data, "docs/readme.txt")
	if got := readZipRaw(t, out, "docs/readme.txt"); !bytes.Equal(got, orig) {
		t.Error("unchanged member was recompressed")
	}
	// Metadata-bearing members are cleaned, including nested archives.
	assertAbsent(t, m["docs/photo.jpg"].data, fixtures.Make, fixtures.Model)
	assertAbsent(t, m["docs/song.mp3"].data, fixtures.Encoder, "ID3")
	docx := readZip(t, m["docs/letter.docx"].data)
	assertAbsent(t, docx["docProps/core.xml"].data, fixtures.ArtistName)
	inner := readZip(t, m["docs/inner.zip"].data)
	assertAbsent(t, inner["inner/photo.jpg"].data, fixtures.Make)
}

func readZipRaw(t *testing.T, b []byte, name string) []byte {
	t.Helper()
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	for _, f := range zr.File {
		if f.Name == name {
			r, err := f.OpenRaw()
			if err != nil {
				t.Fatal(err)
			}
			d, _ := io.ReadAll(r)
			return d
		}
	}
	t.Fatalf("no member %s", name)
	return nil
}

func plainZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("x"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestZipSpecialPackagesAreSkipped(t *testing.T) {
	cases := map[string][]byte{
		"signed jar": fixtures.SignedJAR(),
		"apk":        plainZip(t, "AndroidManifest.xml", "classes.dex"),
		"ipa":        plainZip(t, "Payload/App.app/Info.plist"),
		"wheel":      plainZip(t, "pkg/__init__.py", "pkg-1.0.dist-info/RECORD"),
		"nupkg":      plainZip(t, "[Content_Types].xml", "pkg.nuspec"),
		"vsix":       plainZip(t, "[Content_Types].xml", "extension.vsixmanifest"),
		"appx":       plainZip(t, "[Content_Types].xml", "AppxManifest.xml", "AppxSignature.p7x"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			r := bytes.NewReader(data)
			h := Detect(r, "")
			if h == nil || h.Name() != "zip" {
				t.Fatalf("detected as %v, want the zip handler (not Office)", h)
			}
			if _, err := h.Inspect(r, defaultOpts); !errors.Is(err, ErrSkip) {
				t.Fatalf("got %v, want ErrSkip", err)
			}
		})
	}
}

func TestZipNestedSkippedPackageIsReported(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("app.jar")
	_, _ = w.Write(fixtures.SignedJAR())
	_ = zw.Close()
	out, pre, _ := roundTrip(t, "outer.zip", "zip", b.Bytes(), defaultOpts)
	if len(pre.Embedded) != 1 || len(pre.Embedded[0].Warnings) == 0 {
		t.Fatalf("nested signed jar not reported: %+v", pre.Embedded)
	}
	m := readZip(t, out)
	if !bytes.Equal(m["app.jar"].data, fixtures.SignedJAR()) {
		t.Fatal("nested signed jar was modified")
	}
}

func TestZipEncryptedRefused(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.CreateRaw(&zip.FileHeader{Name: "secret.txt", Method: zip.Store, Flags: 1, CompressedSize64: 1, UncompressedSize64: 1})
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	if _, err := (zipArchiveHandler{}).Inspect(bytes.NewReader(b.Bytes()), defaultOpts); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

func TestZipEmptyArchive(t *testing.T) {
	data := plainZip(t)
	_, pre, _ := roundTrip(t, "empty.zip", "zip", data, defaultOpts)
	if pre.TotalFields() != 0 {
		t.Fatalf("empty archive reported fields: %+v", pre.Fields)
	}
}

func TestZipOversizedMember(t *testing.T) {
	saved := maxZipEntry
	maxZipEntry = 1000
	defer func() { maxZipEntry = saved }()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("big-photo.jpg")
	_, _ = w.Write(fixtures.PhotoJPEG()) // ~2.7 KB, over the lowered cap
	w, _ = zw.Create("big.txt")
	_, _ = w.Write(bytes.Repeat([]byte("a"), 5000))
	_ = zw.Close()
	out, pre, _ := roundTrip(t, "big.zip", "zip", b.Bytes(), defaultOpts)
	if pre.FullyClean {
		t.Fatal("an oversized recognised member must make the archive not fully clean")
	}
	if len(pre.Warnings) != 1 {
		t.Fatalf("expected one warning (the photo; plain text is fine), got %v", pre.Warnings)
	}
	m := readZip(t, out)
	if !bytes.Equal(m["big-photo.jpg"].data, fixtures.PhotoJPEG()) {
		t.Fatal("oversized member should be copied unchanged")
	}
}

func TestDocumentJunkRemoved(t *testing.T) {
	// OS clutter is removed from Office documents too.
	src := fixtures.LetterDOCX()
	zr, _ := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, f := range zr.File {
		if err := zw.Copy(f); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := zw.Create("__MACOSX/word/._document.xml")
	_, _ = w.Write([]byte("AppleDouble"))
	_ = zw.Close()
	out, _, _ := roundTrip(t, "letter.docx", "ooxml", b.Bytes(), defaultOpts)
	if _, ok := readZip(t, out)["__MACOSX/word/._document.xml"]; ok {
		t.Fatal("AppleDouble file kept in DOCX")
	}
}
