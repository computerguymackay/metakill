package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/computerguymackay/metakill/internal/fixtures"
)

func tempPhoto(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	data := fixtures.PhotoJPEG()
	p := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, data
}

func runWith(path, stdin string, mutate func(*Config)) (int, string, string) {
	var out, errb bytes.Buffer
	cfg := Config{
		Args: []string{path}, Backup: true,
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
		IsTTY: func() bool { return true },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return Run(cfg), out.String(), errb.String()
}

func TestPromptYes(t *testing.T) {
	p, orig := tempPhoto(t)
	code, _, stderr := runWith(p, "y\n", nil)
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "remove 25 metadata fields from 1 file(s) in place (backups on)? [y/N]") {
		t.Errorf("prompt: %q", stderr)
	}
	got, _ := os.ReadFile(p)
	if bytes.Equal(got, orig) {
		t.Fatal("file not modified after answering y")
	}
}

func TestPromptNo(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "", "nope\n"} {
		p, orig := tempPhoto(t)
		code, stdout, stderr := runWith(p, answer, nil)
		if code != ExitError || stdout != "" || !strings.Contains(stderr, "aborted; no files were modified") {
			t.Errorf("answer %q: code %d stdout %q stderr %q", answer, code, stdout, stderr)
		}
		got, _ := os.ReadFile(p)
		if !bytes.Equal(got, orig) {
			t.Fatalf("answer %q modified the file", answer)
		}
	}
}

func TestDryRunNeverPrompts(t *testing.T) {
	p, _ := tempPhoto(t)
	code, _, stderr := runWith(p, "", func(c *Config) { c.DryRun = true })
	if code != ExitOK || strings.Contains(stderr, "[y/N]") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestYesSkipsPrompt(t *testing.T) {
	p, _ := tempPhoto(t)
	code, _, stderr := runWith(p, "", func(c *Config) { c.Yes = true; c.IsTTY = nil })
	if code != ExitOK || strings.Contains(stderr, "[y/N]") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestNothingToDoSkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "clean.jpg")
	if err := os.WriteFile(p, fixtures.BaseJPEG(8, 8), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runWith(p, "", nil)
	if code != ExitOK || strings.Contains(stderr, "[y/N]") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}
