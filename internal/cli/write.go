package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/computerguymackay/metakill/internal/report"
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
			src.Close()
		}
	}()
	st, err := src.Stat()
	if err != nil {
		return fail(err)
	}

	pre, err := j.handler.Inspect(src, r.opts)
	if err != nil {
		return fail(prefixed(j.handler.Name(), err))
	}
	pre.Path = j.path
	if pre.Format == "" {
		pre.Format = j.handler.Name()
	}
	if r.cfg.DryRun {
		pre.Status = statusFor(&pre)
		return pre
	}

	target, inPlace, err := r.target(j.path)
	if err != nil {
		return fail(err)
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

	tmp, err := os.CreateTemp(filepath.Dir(target), ".metakill-*.tmp")
	if err != nil {
		return fail(err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return fail(err)
	}
	rep, err := j.handler.Strip(src, tmp, r.opts)
	if err != nil {
		cleanup()
		return fail(prefixed(j.handler.Name(), err))
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fail(err)
	}

	// Post-write validation: the output must parse with the same handler and
	// contain nothing that handler would still remove.
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return fail(err)
	}
	check, err := j.handler.Inspect(tmp, r.opts)
	if err != nil {
		cleanup()
		return fail(fmt.Errorf("post-write validation failed (original left untouched): %w", err))
	}
	if n := check.TotalFields(); n > 0 {
		cleanup()
		return fail(fmt.Errorf("post-write validation failed (original left untouched): %d field(s) still present, first: %s", n, firstField(&check)))
	}
	newSize, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		cleanup()
		return fail(err)
	}
	_ = tmp.Chmod(st.Mode().Perm()) // best effort; not supported everywhere
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fail(err)
	}
	src.Close()
	srcOpen = false

	if backup != "" {
		if err := copyFileExcl(target, backup, st.Mode().Perm()); err != nil {
			os.Remove(tmpName)
			return fail(fmt.Errorf("creating backup: %w", err))
		}
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fail(fmt.Errorf("replacing file: %w", err))
	}

	rep.Path = j.path
	if rep.Format == "" {
		rep.Format = j.handler.Name()
	}
	rep.Status = report.StatusCleaned
	rep.Backup = backup
	if !inPlace {
		rep.Output = r.cfg.Output
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
	defer in.Close()
	out, err := os.OpenFile(dst, flags, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// prefixed adds the handler name to an error unless it already starts with it.
func prefixed(name string, err error) error {
	if strings.HasPrefix(err.Error(), name+":") {
		return err
	}
	return fmt.Errorf("%s: %w", name, err)
}
