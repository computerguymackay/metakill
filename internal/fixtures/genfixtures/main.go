// Command genfixtures writes every generated fixture into a directory
// (default testdata/fixtures). Run via `make fixtures` or `go generate`.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/computerguymackay/metakill/internal/fixtures"
)

func main() {
	dir := "testdata/fixtures"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, f := range fixtures.All() {
		if err := os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
