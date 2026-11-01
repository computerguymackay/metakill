package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/handlers"
	"github.com/computerguymackay/metakill/internal/meta/memio"
)

// SelfTest runs every generated fixture through detection, inspection,
// stripping and re-inspection entirely in memory. It returns an exit code.
func SelfTest(stdout, stderr io.Writer) int {
	failed := 0
	all := fixtures.All()
	opts := handlers.Options{ZeroPad: true}
	for _, fx := range all {
		if err := selfTestOne(fx, opts); err != nil {
			failed++
			fmt.Fprintf(stdout, "FAIL  %-28s %v\n", fx.Name, err)
			continue
		}
		fmt.Fprintf(stdout, "ok    %-28s %s\n", fx.Name, fx.Format)
	}
	fmt.Fprintf(stdout, "%d/%d fixtures passed\n", len(all)-failed, len(all))
	if failed > 0 {
		fmt.Fprintf(stderr, "metakill: error: selftest: %d fixture(s) failed\n", failed)
		return ExitError
	}
	return ExitOK
}

func selfTestOne(fx fixtures.Fixture, opts handlers.Options) error {
	r := bytes.NewReader(fx.Data)
	h := handlers.Detect(r, fx.Name)
	if h == nil {
		return fmt.Errorf("not detected")
	}
	if h.Name() != fx.Format {
		return fmt.Errorf("detected as %s, want %s", h.Name(), fx.Format)
	}
	pre, err := h.Inspect(r, opts)
	if fx.Skipped {
		if !errors.Is(err, handlers.ErrSkip) {
			return fmt.Errorf("expected the handler to skip this file, got %v", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	if !fx.Clean && pre.TotalFields() == 0 {
		return fmt.Errorf("inspect found no metadata")
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var out memio.Buffer
	if _, err := h.Strip(r, &out, opts); err != nil {
		return fmt.Errorf("strip: %w", err)
	}
	post, err := h.Inspect(bytes.NewReader(out.Bytes()), opts)
	if err != nil {
		return fmt.Errorf("re-inspect: %w", err)
	}
	if n := post.TotalFields(); n > 0 {
		return fmt.Errorf("%d field(s) survived stripping", n)
	}
	return nil
}
