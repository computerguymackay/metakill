package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// walk lists regular files under dir in lexicographic order. Without
// recursive it only looks at dir's top level. Symlinks, backups (*.bak) and
// metakill temp files are skipped.
func walk(dir string, recursive bool) ([]string, error) {
	var out []string
	if !recursive {
		entries, err := os.ReadDir(dir) // already sorted by name
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type().IsRegular() && !ignored(e.Name()) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
		return out, nil
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && !ignored(d.Name()) {
			out = append(out, p)
		}
		return nil
	})
	// WalkDir visits entries in lexical order within each directory, so the
	// result is deterministic on every platform.
	return out, err
}

func ignored(name string) bool {
	return strings.HasSuffix(name, ".bak") || isTempName(name)
}

func isTempName(name string) bool {
	return strings.HasPrefix(name, ".metakill-") && strings.HasSuffix(name, ".tmp")
}
