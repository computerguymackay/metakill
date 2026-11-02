package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/computerguymackay/metakill/internal/handlers"
	"github.com/computerguymackay/metakill/report"
)

// processOne inspects (dry-run) or strips one file.
//
// Real runs never touch the original until the cleaned copy has been written
// to a temp file in the same directory and re-validated by the same handler.
// Only then is the backup made and the temp file renamed over the original.
func (r *runner) processOne(j job) report.Report {
	fail := func(err error) report.Report {
		rep := errorReport(j.path, err)
		rep.Format = j.handler.Name()
		return rep
	}

	src, err := os.Open(j.path)
	if err != nil {
		return fail(err)
	}
	srcOpen := true
	defer func() {
		if srcOpen {
			_ = src.Close()
		}
	}()
	st, err := src.Stat()
	if err != nil {
		return fail(err)
	}

	pre, err := j.handler.Inspect(src, r.opts)
	if errors.Is(err, handlers.ErrSkip) {
		rep := report.New(j.handler.Name())
		rep.Path = j.path
		rep.Status = report.StatusSkipped
		rep.Warn(err.Error())
		return rep
	}
	if err != nil {
		return fail(handlers.Prefixed(j.handler.Name(), err))
	}
	pre.Path = j.path
	if pre.Format == "" {
		pre.Format = j.handler.Name()
	}
	if pre.ReportOnly {
		pre.Status = report.StatusInspected
		return pre
	}
	convert := pre.ConvertTo
	if convert != "" && r.cfg.Output == "" {
		pre.Output = swapExt(j.path, convert)
	}
	if r.cfg.DryRun {
		pre.Status = statusFor(&pre)
		return pre
	}

	target, inPlace, err := r.target(j.path)
	if err != nil {
		return fail(err)
	}
	writeTarget := target
	if convert != "" {
		if inPlace {
			writeTarget = swapExt(target, convert)
			if _, err := os.Lstat(writeTarget); err == nil {
				return fail(fmt.Errorf("cannot convert: %s already exists", writeTarget))
			}
		} else if !strings.EqualFold(filepath.Ext(target), convert) {
			return fail(fmt.Errorf("the output will be a %s archive; give -o/--output a %s name", strings.TrimPrefix(convert, "."), convert))
		}
	}

	if pre.TotalFields() == 0 {
		pre.Status = report.StatusAlreadyClean
		if !inPlace {
			if err := copyFile(j.path, target, st.Mode().Perm()); err != nil {
				return fail(err)
			}
			pre.Output = r.cfg.Output
		}
		return pre
	}

	backup := ""
	if inPlace && r.cfg.Backup {
		backup = target + ".bak"
		if _, err := os.Lstat(backup); err == nil {
			return fail(fmt.Errorf("backup %s already exists; remove it or pass --backup=false", backup))
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(writeTarget), ".metakill-*.tmp")
	if err != nil {
		return fail(err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	// Strip into the temp file and re-validate it before touching the original.
	rep, newSize, err := handlers.StripVerified(j.handler, src, tmp, r.opts, convert)
	if err != nil {
		cleanup()
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fail(err)
	}
	_ = tmp.Chmod(st.Mode().Perm()) // best effort; not supported everywhere
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fail(err)
	}
	if err := src.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fail(err)
	}
	srcOpen = false

	if convert != "" && inPlace {
		// The original keeps its name as the backup (or is removed); the
		// cleaned archive gets the new extension.
		if backup != "" {
			if err := os.Rename(target, backup); err != nil {
				_ = os.Remove(tmpName)
				return fail(fmt.Errorf("creating backup: %w", err))
			}
		}
		if err := os.Rename(tmpName, writeTarget); err != nil {
			_ = os.Remove(tmpName)
			if backup != "" {
				_ = os.Rename(backup, target)
			}
			return fail(fmt.Errorf("writing converted file: %w", err))
		}
		if backup == "" {
			if err := os.Remove(target); err != nil {
				return fail(fmt.Errorf("removing original after conversion: %w", err))
			}
		}
	} else {
		if backup != "" {
			if err := copyFileExcl(target, backup, st.Mode().Perm()); err != nil {
				_ = os.Remove(tmpName)
				return fail(fmt.Errorf("creating backup: %w", err))
			}
		}
		if err := os.Rename(tmpName, target); err != nil {
			_ = os.Remove(tmpName)
			return fail(fmt.Errorf("replacing file: %w", err))
		}
	}

	rep.Path = j.path
	if rep.Format == "" {
		rep.Format = j.handler.Name()
	}
	rep.Status = report.StatusCleaned
	rep.Backup = backup
	if !inPlace {
		rep.Output = r.cfg.Output
	} else if convert != "" {
		rep.Output = swapExt(j.path, convert)
	}
	rep.Bytes = st.Size() - newSize
	if rep.Bytes < 0 {
		rep.Bytes = 0
	}
	return rep
}

func statusFor(rep *report.Report) report.Status {
	if rep.TotalFields() == 0 {
		return report.StatusAlreadyClean
	}
	return report.StatusCleaned
}

// target resolves where the cleaned file goes. In-place writes follow
// symlinks so the link itself is preserved.
func (r *runner) target(path string) (string, bool, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false, err
	}
	if r.cfg.Output == "" {
		return real, true, nil
	}
	out := filepath.Clean(r.cfg.Output)
	if st, err := os.Stat(out); err == nil {
		if st.IsDir() {
			return "", false, fmt.Errorf("output %s is a directory", out)
		}
		if same, err := sameFile(real, out); err == nil && same {
			return real, true, nil
		}
	}
	return out, false, nil
}

func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	return copyWithFlags(src, dst, perm, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
}

// copyFileExcl copies but refuses to overwrite an existing destination.
func copyFileExcl(src, dst string, perm os.FileMode) error {
	return copyWithFlags(src, dst, perm, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
}

func copyWithFlags(src, dst string, perm os.FileMode, flags int) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, flags, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// swapExt replaces a path's extension (e.g. holiday.rar → holiday.7z).
func swapExt(p, ext string) string {
	return strings.TrimSuffix(p, filepath.Ext(p)) + ext
}
