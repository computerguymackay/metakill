package handlers

import (
	"fmt"
	"io"
	"strings"

	"github.com/computerguymackay/metakill/report"
)

// StripVerified strips src into dst with h, then reads dst back and checks
// that it parses and that nothing removable is left. convert is the
// ConvertTo value from the Inspect report ("" when the format stays the
// same); the output is then checked with the handler for the new format.
//
// On error the contents of dst are undefined and must be discarded. On
// success dst is left positioned at its end, so the returned size is also
// the output length.
func StripVerified(h Handler, src io.ReadSeeker, dst io.ReadWriteSeeker, opts Options, convert string) (report.Report, int64, error) {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return report.Report{}, 0, err
	}
	rep, err := h.Strip(src, dst, opts)
	if err != nil {
		return report.Report{}, 0, Prefixed(h.Name(), err)
	}
	if rep.Format == "" {
		rep.Format = h.Name()
	}

	// The output must parse with the same handler (or, after a conversion,
	// the handler for the new format) and contain nothing it would remove.
	const failed = "post-write validation failed (original left untouched)"
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return report.Report{}, 0, err
	}
	vh := h
	if convert != "" {
		if vh = Detect(dst, ""); vh == nil || "."+vh.Name() != convert {
			return report.Report{}, 0, fmt.Errorf("%s: output is not a %s archive", failed, convert)
		}
		if _, err := dst.Seek(0, io.SeekStart); err != nil {
			return report.Report{}, 0, err
		}
	}
	check, err := vh.Inspect(dst, opts)
	if err != nil {
		return report.Report{}, 0, fmt.Errorf("%s: %w", failed, err)
	}
	if n := check.TotalFields(); n > 0 {
		return report.Report{}, 0, fmt.Errorf("%s: %d field(s) still present, first: %s", failed, n, firstField(&check))
	}
	size, err := dst.Seek(0, io.SeekEnd)
	if err != nil {
		return report.Report{}, 0, err
	}
	return rep, size, nil
}

// Prefixed adds the handler name to an error unless it already starts with it.
func Prefixed(name string, err error) error {
	if strings.HasPrefix(err.Error(), name+":") {
		return err
	}
	return fmt.Errorf("%s: %w", name, err)
}

func firstField(rep *report.Report) string {
	if len(rep.Fields) > 0 {
		f := rep.Fields[0]
		return f.Namespace + ":" + f.Key
	}
	for i := range rep.Embedded {
		if s := firstField(&rep.Embedded[i]); s != "" {
			return rep.Embedded[i].Path + ": " + s
		}
	}
	return ""
}
