package handlers

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bodgit/sevenzip"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/meta/memio"
	"github.com/computerguymackay/metakill/report"
)

var xOpts = Options{ZeroPad: true, ExternalTools: true}

// need7z skips tests that run 7-Zip when it is not installed. CI sets
// METAKILL_REQUIRE_7Z=1 so that a missing tool fails instead of skipping.
func need7z(t *testing.T) extTool {
	t.Helper()
	tool, ok := toolLookup("7z")
	if !ok {
		if os.Getenv("METAKILL_REQUIRE_7Z") != "" {
			t.Fatal("7-Zip is required (METAKILL_REQUIRE_7Z is set) but was not found")
		}
		t.Skip("7-Zip not installed")
	}
	return tool
}

func fieldKeys(fs []report.Field) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Key] = true
	}
	return m
}

type sevenZipMember struct {
	hdr  sevenzip.FileHeader
	data []byte
}

func read7z(t *testing.T, b []byte) map[string]sevenZipMember {
	t.Helper()
	zr, err := sevenzip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("output is not a valid 7z: %v", err)
	}
	out := map[string]sevenZipMember{}
	for _, f := range zr.File {
		var d []byte
		if !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			d, _ = io.ReadAll(rc)
			_ = rc.Close()
		}
		out[f.Name] = sevenZipMember{f.FileHeader, d}
	}
	return out
}

// checkCleaned7z verifies a 7z written by metakill.
func checkCleaned7z(t *testing.T, b []byte) {
	t.Helper()
	m := read7z(t, b)
	if _, ok := m["docs/.DS_Store"]; ok {
		t.Error(".DS_Store kept")
	}
	for name, e := range m {
		if !e.hdr.Modified.Equal(cleanEpoch) {
			t.Errorf("%s: modified %v", name, e.hdr.Modified)
		}
		if !e.hdr.Created.IsZero() || !e.hdr.Accessed.IsZero() {
			t.Errorf("%s: creation/access time kept", name)
		}
	}
	if got := string(m["docs/readme.txt"].data); got != fixtures.ReadmeBody {
		t.Errorf("readme changed: %q", got)
	}
	if got := string(m["Users/jane/notes.txt"].data); got != fixtures.ReadmeBody {
		t.Errorf("notes changed: %q", got)
	}
	// Windows filesystems have no executable bit to carry through.
	if attr := m["docs/run.sh"].hdr.Attributes; runtime.GOOS != "windows" && (attr&0x8000 == 0 || (attr>>16)&0o111 == 0) {
		t.Errorf("run.sh lost its executable bit (attributes %#x)", attr)
	}
	assertAbsent(t, m["docs/photo.jpg"].data, fixtures.Make, fixtures.Model)
	if len(m["docs/photo.jpg"].data) == 0 {
		t.Error("photo missing")
	}
}

func Test7zInspectOnlyWithoutX(t *testing.T) {
	r := bytes.NewReader(fixtures.Bundle7Z())
	h := Detect(r, "")
	if h == nil || h.Name() != "7z" {
		t.Fatalf("detected as %v", h)
	}
	rep, err := h.Inspect(r, defaultOpts)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.ReportOnly || rep.FullyClean {
		t.Fatalf("without -x a 7z must be inspect-only and not fully clean: %+v", rep.Warnings)
	}
	keys := fieldKeys(rep.Fields)
	for _, k := range []string{"member timestamps (latest)", "creation/access times", "docs/.DS_Store"} {
		if !keys[k] {
			t.Errorf("missing field %q", k)
		}
	}
	if len(rep.Embedded) != 1 {
		t.Errorf("expected the photo to be reported, got %d embedded reports", len(rep.Embedded))
	}
	if !strings.Contains(strings.Join(rep.Warnings, " "), "-x/--external-tools") {
		t.Errorf("no hint about -x: %v", rep.Warnings)
	}
}

func Test7zRepack(t *testing.T) {
	need7z(t)
	out, pre, _ := roundTrip(t, "bundle.7z", "7z", fixtures.Bundle7Z(), xOpts)
	if pre.ReportOnly {
		t.Fatal("with -x and 7-Zip installed the archive must be repackable")
	}
	checkCleaned7z(t, out)
}

func Test7zTruncatedIsSkipped(t *testing.T) {
	b := fixtures.Bundle7Z()
	_, err := (sevenZipHandler{}).Inspect(bytes.NewReader(b[:len(b)/2]), defaultOpts)
	if !errors.Is(err, ErrSkip) {
		t.Fatalf("got %v, want ErrSkip (incomplete/multi-volume)", err)
	}
}

// make7z runs 7-Zip on the files in dir with extra arguments.
func make7z(t *testing.T, tool extTool, dir string, args ...string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "x.7z")
	cmd := exec.Command(tool.path, append(append([]string{"a", "-t7z", "-bd", "-y"}, args...), "--", out, ".")...)
	cmd.Dir = dir
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("this 7-Zip cannot build the test archive: %v %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func Test7zEncryptedRefused(t *testing.T) {
	tool := need7z(t)
	if tool.kind != "7z" {
		t.Skip("needs 7-Zip itself to create encrypted archives")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-psecret"}, {"-psecret", "-mhe=on"}} {
		b := make7z(t, tool, dir, args...)
		_, err := (sevenZipHandler{}).Inspect(bytes.NewReader(b), defaultOpts)
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("%v: got %v, want ErrUnsupported", args, err)
		}
	}
}

func Test7zLinksRefused(t *testing.T) {
	tool := need7z(t)
	if tool.kind != "7z" || runtime.GOOS == "windows" {
		t.Skip("needs 7-Zip with symlink support")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
		t.Skip("cannot create symlinks here")
	}
	b := make7z(t, tool, dir, "-snl")
	_, err := (sevenZipHandler{}).Inspect(bytes.NewReader(b), defaultOpts)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported for links", err)
	}
}

func TestRARInspect(t *testing.T) {
	cases := []struct {
		name   string
		data   []byte
		owners bool
	}{
		{"rar5", fixtures.BundleRAR5(true), true},
		{"rar4", fixtures.BundleRAR4(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := bytes.NewReader(c.data)
			h := Detect(r, "")
			if h == nil || h.Name() != "rar" {
				t.Fatalf("detected as %v", h)
			}
			rep, err := h.Inspect(r, defaultOpts)
			if err != nil {
				t.Fatal(err)
			}
			keys := fieldKeys(rep.Fields)
			for _, k := range []string{"archive comment", "member timestamps (latest)", "docs/.DS_Store"} {
				if !keys[k] {
					t.Errorf("missing field %q", k)
				}
			}
			if keys["owners"] != c.owners {
				t.Errorf("owners reported = %v, want %v", keys["owners"], c.owners)
			}
			if !rep.ReportOnly {
				t.Error("without -x a RAR must be inspect-only")
			}
			if len(rep.Embedded) != 1 {
				t.Errorf("expected the photo to be reported, got %d", len(rep.Embedded))
			}
		})
	}
}

// withTools replaces tool discovery for one test.
func withTools(t *testing.T, have map[string]bool) {
	t.Helper()
	saved := toolLookup
	toolLookup = func(kind string) (extTool, bool) {
		if have[kind] {
			return extTool{kind: kind, path: "/fake/" + kind}, true
		}
		return extTool{}, false
	}
	t.Cleanup(func() { toolLookup = saved })
}

func TestRARToolDecisions(t *testing.T) {
	data := fixtures.BundleRAR5(true)
	cases := []struct {
		name          string
		have          map[string]bool
		opts          Options
		reportOnly    bool
		convert, hint string
	}{
		{"no -x", map[string]bool{"rar": true, "7z": true}, defaultOpts, true, "", "-x/--external-tools"},
		{"-x with rar", map[string]bool{"rar": true}, xOpts, false, "", ""},
		{"-x without rar", map[string]bool{"7z": true}, xOpts, true, "", "-a/--convert-rar"},
		{"-a with 7z", map[string]bool{"7z": true}, Options{ZeroPad: true, ExternalTools: true, ConvertRAR: true}, false, ".7z", ""},
		{"-a without any tool", nil, Options{ZeroPad: true, ExternalTools: true, ConvertRAR: true}, true, "", "rar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withTools(t, c.have)
			rep, err := (rarHandler{}).Inspect(bytes.NewReader(data), c.opts)
			if err != nil {
				t.Fatal(err)
			}
			if rep.ReportOnly != c.reportOnly || rep.ConvertTo != c.convert {
				t.Fatalf("reportOnly=%v convert=%q, want %v %q", rep.ReportOnly, rep.ConvertTo, c.reportOnly, c.convert)
			}
			if c.hint != "" && !strings.Contains(strings.Join(rep.Warnings, " "), c.hint) {
				t.Errorf("warnings %v lack hint %q", rep.Warnings, c.hint)
			}
		})
	}
}

func TestRARConvertTo7z(t *testing.T) {
	need7z(t)
	if _, ok := toolLookup("rar"); ok {
		t.Skip("rar is installed, so RAR is repacked as RAR instead of converted")
	}
	opts := Options{ZeroPad: true, ExternalTools: true, ConvertRAR: true}
	for name, data := range map[string][]byte{"rar5": fixtures.BundleRAR5(true), "rar4": fixtures.BundleRAR4()} {
		t.Run(name, func(t *testing.T) {
			var buf memio.Buffer
			rep, err := (rarHandler{}).Strip(bytes.NewReader(data), &buf, opts)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Format != "7z" {
				t.Errorf("format %q, want 7z", rep.Format)
			}
			out := buf.Bytes()
			h := Detect(bytes.NewReader(out), "")
			if h == nil || h.Name() != "7z" {
				t.Fatalf("output detected as %v, want 7z", h)
			}
			post, err := h.Inspect(bytes.NewReader(out), xOpts)
			if err != nil || post.TotalFields() != 0 {
				t.Fatalf("converted 7z not clean: %v %+v", err, post.Fields)
			}
			checkCleaned7z(t, out)
		})
	}
}

func TestRAREdgeCases(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want error
		msg  string
	}{
		{"multi-volume", fixtures.RAR5Variant(0x1, false, "a.txt"), ErrSkip, "multi-volume"},
		{"encrypted headers", fixtures.RAR5Variant(0, true, "a.txt"), ErrUnsupported, "encrypted"},
		{"path traversal", fixtures.RAR5Variant(0, false, "../evil.txt"), nil, "unsafe member path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := (rarHandler{}).Inspect(bytes.NewReader(c.data), defaultOpts)
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("got %v, want an error mentioning %q", err, c.msg)
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestNestedRARIsNotConverted(t *testing.T) {
	withTools(t, map[string]bool{"7z": true})
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("inner.rar")
	_, _ = w.Write(fixtures.BundleRAR5(true))
	_ = zw.Close()
	opts := Options{ZeroPad: true, ExternalTools: true, ConvertRAR: true}
	rep, err := (zipArchiveHandler{}).Inspect(bytes.NewReader(b.Bytes()), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.FullyClean || len(rep.Embedded) != 1 || rep.Embedded[0].Status != report.StatusInspected {
		t.Fatalf("nested RAR should be reported, left unchanged and mark the ZIP not fully clean: %+v", rep.Embedded)
	}
}

func TestNormaliseTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "x.sh"), []byte("#!/bin/sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "y.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := normaliseTree(dir); err != nil {
		t.Fatal(err)
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		info, _ := d.Info()
		if !info.ModTime().Equal(cleanEpoch) && p != dir {
			t.Errorf("%s: mtime %v", p, info.ModTime())
		}
		return nil
	})
	if st, _ := os.Stat(filepath.Join(dir, "a", "b", "x.sh")); st.Mode().Perm()&0o111 == 0 && os.PathSeparator == '/' {
		t.Error("executable bit lost")
	}
}
