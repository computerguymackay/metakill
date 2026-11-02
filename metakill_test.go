package metakill_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/computerguymackay/metakill"
	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/report"
)

// nonDeterministic lists formats whose cleaned output differs between runs:
// pdfcpu writes a fresh trailer /ID every time.
func nonDeterministic(format string) bool { return format == "pdf" }

// needsTool reports whether a fixture is an archive that default options
// cannot rewrite.
func needsTool(f fixtures.Fixture) bool { return f.Format == "7z" || f.Format == "rar" }

func TestCleanBytesAllFixtures(t *testing.T) {
	for _, f := range fixtures.All() {
		t.Run(f.Name, func(t *testing.T) {
			out, rep, err := metakill.CleanBytes(f.Data, f.Name, metakill.Options{})
			switch {
			case f.Skipped:
				if !errors.Is(err, metakill.ErrSkipped) || rep.Status != report.StatusSkipped {
					t.Fatalf("want ErrSkipped/skipped, got %v / %s", err, rep.Status)
				}
				return
			case needsTool(f) && errors.Is(err, metakill.ErrNeedsTool):
				if rep.Status != report.StatusInspected || rep.TotalFields() == 0 {
					t.Fatalf("want inspected report with fields, got %s with %d", rep.Status, rep.TotalFields())
				}
				return
			case err != nil:
				t.Fatalf("CleanBytes: %v", err)
			}
			if got, _ := metakill.Detect(bytes.NewReader(f.Data), f.Name); got != f.Format {
				t.Errorf("Detect = %q, want %q", got, f.Format)
			}
			if rep.Path != f.Name {
				t.Errorf("path = %q, want %q", rep.Path, f.Name)
			}
			if f.Clean {
				if rep.Status != report.StatusAlreadyClean || !bytes.Equal(out, f.Data) {
					t.Fatalf("clean fixture: status %s, changed=%v", rep.Status, !bytes.Equal(out, f.Data))
				}
				return
			}
			if rep.Status != report.StatusCleaned || rep.TotalFields() == 0 {
				t.Fatalf("status %s with %d fields", rep.Status, rep.TotalFields())
			}
			again, err := metakill.Inspect(bytes.NewReader(out), f.Name, metakill.Options{})
			if err != nil {
				t.Fatalf("Inspect cleaned output: %v", err)
			}
			if n := again.TotalFields(); n != 0 || again.Status != report.StatusAlreadyClean {
				t.Fatalf("cleaned output still has %d fields (status %s)", n, again.Status)
			}
		})
	}
}

func TestInspectDoesNotWrite(t *testing.T) {
	f, _ := fixtures.Get("photo.jpg")
	orig := append([]byte(nil), f.Data...)
	rep, err := metakill.Inspect(bytes.NewReader(f.Data), "", metakill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun || rep.Status != report.StatusCleaned || rep.TotalFields() == 0 {
		t.Fatalf("unexpected report: dry=%v status=%s fields=%d", rep.DryRun, rep.Status, rep.TotalFields())
	}
	if !bytes.Equal(orig, f.Data) {
		t.Fatal("Inspect modified its input")
	}
}

func TestCleanMatchesCleanBytes(t *testing.T) {
	for _, name := range []string{"photo.jpg", "report.pdf", "letter.docx", "notes.md"} {
		f, ok := fixtures.Get(name)
		if !ok {
			t.Fatalf("no fixture %s", name)
		}
		want, _, err := metakill.CleanBytes(f.Data, f.Name, metakill.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if _, err := metakill.Clean(bytes.NewReader(f.Data), &got, f.Name, metakill.Options{}); err != nil {
			t.Fatal(err)
		}
		if nonDeterministic(f.Format) {
			if got.Len() != len(want) {
				t.Errorf("%s: Clean wrote %d bytes, CleanBytes %d", name, got.Len(), len(want))
			}
		} else if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: Clean and CleanBytes outputs differ", name)
		}
	}
}

func TestUnknownFormat(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02, 0xff, 0xfe}
	var w bytes.Buffer
	rep, err := metakill.Clean(bytes.NewReader(data), &w, "blob.bin", metakill.Options{})
	if !errors.Is(err, metakill.ErrUnknownFormat) {
		t.Fatalf("err = %v, want ErrUnknownFormat", err)
	}
	if rep.Status != report.StatusSkipped || w.Len() != 0 {
		t.Fatalf("status %s, wrote %d bytes", rep.Status, w.Len())
	}
	if _, ok := metakill.Detect(bytes.NewReader(data), "blob.bin"); ok {
		t.Fatal("Detect matched random bytes")
	}
}

func TestDetectAndFormats(t *testing.T) {
	f, _ := fixtures.Get("photo.jpg")
	r := bytes.NewReader(f.Data)
	if got, ok := metakill.Detect(r, ""); !ok || got != "jpeg" {
		t.Fatalf("Detect = %q, %v", got, ok)
	}
	if pos, _ := r.Seek(0, 1); pos != 0 {
		t.Fatalf("Detect left reader at %d", pos)
	}
	found := false
	for _, n := range metakill.Formats() {
		found = found || n == "jpeg"
	}
	if !found {
		t.Fatal("Formats() does not list jpeg")
	}
}

func TestCleanFile(t *testing.T) {
	f, _ := fixtures.Get("photo.jpg")
	dir := t.TempDir()
	path := filepath.Join(dir, f.Name)
	if err := os.WriteFile(path, f.Data, 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jpg")
	haveLink := os.Symlink(f.Name, link) == nil

	target := path
	if haveLink {
		target = link
	}
	rep, err := metakill.CleanFile(target, metakill.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != report.StatusCleaned || rep.Path != target || rep.Bytes <= 0 || rep.Backup != "" {
		t.Fatalf("unexpected report: %+v", rep)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, _, _ := metakill.CleanBytes(f.Data, f.Name, metakill.Options{})
	if !bytes.Equal(got, want) {
		t.Fatal("file on disk differs from CleanBytes output")
	}
	if haveLink {
		if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Fatal("symlink was replaced")
		}
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
			t.Errorf("permissions = %v, want 0640", st.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != f.Name && e.Name() != "link.jpg" {
			t.Errorf("leftover file %s", e.Name())
		}
	}

	// Second run: nothing left to remove.
	rep, err = metakill.CleanFile(path, metakill.Options{})
	if err != nil || rep.Status != report.StatusAlreadyClean {
		t.Fatalf("second run: %v / %s", err, rep.Status)
	}
}

func TestCleanFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := metakill.CleanFile(filepath.Join(dir, "missing.jpg"), metakill.Options{}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
	rep, err := metakill.CleanFile(dir, metakill.Options{})
	if err == nil || rep.Status != report.StatusError {
		t.Errorf("directory: err = %v, status %s", err, rep.Status)
	}
}

// TestConcurrent cleans every fixture from many goroutines at once. Run it
// with -race (make test-race) to check the handlers share no mutable state.
func TestConcurrent(t *testing.T) {
	all := fixtures.All()
	want := make([][]byte, len(all))
	for i, f := range all {
		want[i], _, _ = metakill.CleanBytes(f.Data, f.Name, metakill.Options{})
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i, f := range all {
				got, _, _ := metakill.CleanBytes(f.Data, f.Name, metakill.Options{})
				if nonDeterministic(f.Format) {
					if len(got) != len(want[i]) {
						t.Errorf("%s: output length differs between runs", f.Name)
					}
				} else if !bytes.Equal(got, want[i]) {
					t.Errorf("%s: output differs between runs", f.Name)
				}
				if _, err := metakill.Inspect(bytes.NewReader(f.Data), f.Name, metakill.Options{}); err != nil &&
					!errors.Is(err, metakill.ErrSkipped) {
					t.Errorf("%s: Inspect: %v", f.Name, err)
				}
			}
		}()
	}
	wg.Wait()
}
