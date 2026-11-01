// Command gen7z rebuilds internal/fixtures/data/bundle.7z with 7-Zip. The 7z
// fixture is checked in because there is no pure-Go 7z writer. Run it from
// the module root: go run ./internal/fixtures/gen7z
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/computerguymackay/metakill/internal/fixtures"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen7z:", err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := os.MkdirTemp("", "gen7z-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	files := map[string]struct {
		data []byte
		mode os.FileMode
	}{
		"docs/photo.jpg":       {fixtures.PhotoJPEG(), 0o600},
		"docs/readme.txt":      {[]byte(fixtures.ReadmeBody), 0o600},
		"docs/run.sh":          {[]byte(fixtures.ScriptBody), 0o700},
		"docs/.DS_Store":       {[]byte("Bud1" + fixtures.ArtistName), 0o600},
		"Users/jane/notes.txt": {[]byte(fixtures.ReadmeBody), 0o600},
	}
	when := time.Date(2023, 4, 12, 18, 33, 2, 0, time.UTC)
	for name, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			return err
		}
		if err := os.Chtimes(p, when, when); err != nil {
			return err
		}
	}
	for _, d := range []string{"docs", "Users/jane", "Users"} {
		if err := os.Chtimes(filepath.Join(dir, d), when, when); err != nil {
			return err
		}
	}
	out, err := filepath.Abs("internal/fixtures/data/bundle.7z")
	if err != nil {
		return err
	}
	_ = os.Remove(out)
	cmd := exec.Command("7z", "a", "-t7z", "-mx=5", "-mtc=on", "-mta=on", "-bd", "-y", "--", out, "docs", "Users")
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
