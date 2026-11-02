// Package metakill strips identifying metadata (GPS, timestamps, device and
// software strings, authors, comments, thumbnails and media ID tags) from
// images, documents, audio, video and archives.
//
// It is the library behind the metakill command. Every function is safe for
// concurrent use. The format is detected from the content; the file name is
// only used as a fallback for formats without magic bytes (plain text, SVG)
// and in reports, so it may be empty.
//
// Every cleaned output is re-read and checked before it is returned: it must
// parse and contain nothing that would still be removed. A failed check is
// returned as an error and no output is produced.
//
//	rep, err := metakill.CleanFile("holiday.jpg", metakill.Options{})
//	if err != nil {
//		return err
//	}
//	if !rep.FullyClean {
//		log.Printf("%s: some metadata could not be removed: %v", rep.Path, rep.Warnings)
//	}
package metakill

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/computerguymackay/metakill/internal/handlers"
	"github.com/computerguymackay/metakill/internal/meta/memio"
	"github.com/computerguymackay/metakill/report"
)

// Options controls what is removed. The zero value removes everything the
// command line removes by default.
type Options struct {
	// KeepTags keeps Title/Artist/Album/Track/Year media ID tags.
	KeepTags bool
	// KeepThumbnails keeps embedded thumbnails and cover art. It implies KeepTags.
	KeepThumbnails bool
	// KeepICC keeps ICC colour profiles in images.
	KeepICC bool
	// NoZeroPad leaves existing padding and free-space regions as they are
	// instead of zeroing them.
	NoZeroPad bool
	// ExternalTools allows running an installed 7-Zip or rar binary to repack
	// 7z and RAR archives. Without it those archives can be inspected but
	// cleaning them fails with ErrNeedsTool.
	ExternalTools bool
	// ConvertRAR allows converting a RAR archive to 7z when no rar binary is
	// installed. It implies ExternalTools. The report's ConvertTo is then
	// ".7z" and the output is a 7z archive.
	ConvertRAR bool
}

func (o Options) internal() handlers.Options {
	return handlers.Options{
		KeepTags:       o.KeepTags || o.KeepThumbnails,
		KeepThumbnails: o.KeepThumbnails,
		KeepICC:        o.KeepICC,
		ZeroPad:        !o.NoZeroPad,
		ExternalTools:  o.ExternalTools || o.ConvertRAR,
		ConvertRAR:     o.ConvertRAR,
	}
}

var (
	// ErrUnknownFormat means no handler recognised the input.
	ErrUnknownFormat = errors.New("metakill: unrecognized file type")
	// ErrUnsupported is wrapped when the format is recognised but this
	// variant cannot be processed (an encrypted PDF, a DNG raw file, ...).
	ErrUnsupported = handlers.ErrUnsupported
	// ErrSkipped is wrapped when a file is recognised but deliberately left
	// alone because rewriting it would break it (a signed Android package or
	// JAR, for example).
	ErrSkipped = handlers.ErrSkip
	// ErrNeedsTool is wrapped when cleaning an archive needs an external
	// tool that is not allowed (see Options.ExternalTools) or not installed.
	// The report still lists the metadata that was found.
	ErrNeedsTool = errors.New("metakill: cleaning this archive needs an external tool")
)

// Formats returns the names of the supported format families, as used in
// Report.Format ("jpeg", "pdf", "mp4", ...).
func Formats() []string {
	var out []string
	for _, h := range handlers.All() {
		out = append(out, h.Name())
	}
	return out
}

// Detect reports the format name for r, or ok=false if it is not supported.
// It leaves r positioned at the start.
func Detect(r io.ReadSeeker, name string) (format string, ok bool) {
	h := handlers.Detect(r, name)
	if h == nil {
		return "", false
	}
	return h.Name(), true
}

// Inspect lists the metadata that Clean would remove, without writing
// anything. The report has DryRun set and Status set to StatusCleaned (there
// is something to remove), StatusAlreadyClean, or StatusInspected (an archive
// that cannot be cleaned with the given options).
func Inspect(r io.ReadSeeker, name string, opts Options) (report.Report, error) {
	_, pre, err := inspect(r, name, opts.internal())
	if err != nil {
		return pre, err
	}
	switch {
	case pre.ReportOnly:
		pre.Status = report.StatusInspected
	case pre.TotalFields() == 0:
		pre.Status = report.StatusAlreadyClean
	default:
		pre.Status = report.StatusCleaned
	}
	return done(pre, true), nil
}

// Clean writes a cleaned copy of r to w. If there is nothing to remove, r is
// copied to w unchanged and the status is StatusAlreadyClean.
//
// The cleaned output is staged in a temporary file (in os.TempDir) while it
// is checked, so nothing is written to w unless the check passes. If an error
// is returned, nothing has been written to w.
//
// When the report's ConvertTo is non-empty (only with Options.ConvertRAR) the
// output is in that format, and the caller should name it accordingly.
func Clean(r io.ReadSeeker, w io.Writer, name string, opts Options) (report.Report, error) {
	o := opts.internal()
	h, pre, err := inspect(r, name, o)
	if err != nil {
		return pre, err
	}
	if err := cleanable(&pre); err != nil {
		return pre, err
	}
	if pre.TotalFields() == 0 {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return failed(name, h.Name(), err)
		}
		if _, err := io.Copy(w, r); err != nil {
			return failed(name, h.Name(), err)
		}
		pre.Status = report.StatusAlreadyClean
		return done(pre, false), nil
	}

	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return failed(name, h.Name(), err)
	}
	tmp, err := os.CreateTemp("", "metakill-*.tmp")
	if err != nil {
		return failed(name, h.Name(), err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	rep, newSize, err := handlers.StripVerified(h, r, tmp, o, pre.ConvertTo)
	if err != nil {
		return failed(name, h.Name(), err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return failed(name, h.Name(), err)
	}
	if _, err := io.Copy(w, tmp); err != nil {
		return failed(name, h.Name(), err)
	}
	return cleaned(rep, pre, name, size, newSize), nil
}

// CleanBytes is Clean for data held in memory. It returns the cleaned bytes,
// or data itself when there was nothing to remove.
func CleanBytes(data []byte, name string, opts Options) ([]byte, report.Report, error) {
	o := opts.internal()
	r := bytes.NewReader(data)
	h, pre, err := inspect(r, name, o)
	if err != nil {
		return nil, pre, err
	}
	if err := cleanable(&pre); err != nil {
		return nil, pre, err
	}
	if pre.TotalFields() == 0 {
		pre.Status = report.StatusAlreadyClean
		return data, done(pre, false), nil
	}
	var buf memio.Buffer
	rep, newSize, err := handlers.StripVerified(h, r, &buf, o, pre.ConvertTo)
	if err != nil {
		rep, err := failed(name, h.Name(), err)
		return nil, rep, err
	}
	return buf.Bytes(), cleaned(rep, pre, name, int64(len(data)), newSize), nil
}

// CleanFile cleans the file at path in place. Symbolic links are followed and
// the link itself is kept. The cleaned copy is written to a temporary file in
// the same directory, checked, and then renamed over the original, so the
// original is never left half-written. The file's permissions are kept.
// No backup is made.
//
// When the report's ConvertTo is non-empty (a RAR converted to 7z with
// Options.ConvertRAR) the cleaned archive is written next to the original
// with the new extension, the original is removed, and Report.Output holds
// the new path. It fails if a file with the new name already exists.
func CleanFile(path string, opts Options) (report.Report, error) {
	o := opts.internal()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return failed(path, "unknown", err)
	}
	src, err := os.Open(real)
	if err != nil {
		return failed(path, "unknown", err)
	}
	srcOpen := true
	defer func() {
		if srcOpen {
			_ = src.Close()
		}
	}()
	st, err := src.Stat()
	if err != nil {
		return failed(path, "unknown", err)
	}
	if !st.Mode().IsRegular() {
		return failed(path, "unknown", errors.New("not a regular file"))
	}

	h, pre, err := inspect(src, path, o)
	if err != nil {
		return pre, err
	}
	if err := cleanable(&pre); err != nil {
		return pre, err
	}
	if pre.TotalFields() == 0 {
		pre.Status = report.StatusAlreadyClean
		return done(pre, false), nil
	}

	convert := pre.ConvertTo
	writeTarget := real
	if convert != "" {
		writeTarget = swapExt(real, convert)
		if _, err := os.Lstat(writeTarget); err == nil {
			return failed(path, h.Name(), fmt.Errorf("cannot convert: %s already exists", writeTarget))
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(writeTarget), ".metakill-*.tmp")
	if err != nil {
		return failed(path, h.Name(), err)
	}
	tmpName := tmp.Name()
	fail := func(err error) (report.Report, error) {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return failed(path, h.Name(), err)
	}
	rep, newSize, err := handlers.StripVerified(h, src, tmp, o, convert)
	if err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	_ = tmp.Chmod(st.Mode().Perm()) // best effort; not supported everywhere
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	// Close the original before replacing it (required on Windows).
	srcOpen = false
	if err := src.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmpName, writeTarget); err != nil {
		return fail(fmt.Errorf("replacing file: %w", err))
	}
	rep = cleaned(rep, pre, path, st.Size(), newSize)
	if convert != "" {
		rep.Output = swapExt(path, convert)
		if err := os.Remove(real); err != nil {
			return rep, fmt.Errorf("converted to %s but could not remove the original: %w", rep.Output, err)
		}
	}
	return rep, nil
}

// inspect detects the format and runs the handler's Inspect. On error the
// returned report describes the failure.
func inspect(r io.ReadSeeker, name string, o handlers.Options) (handlers.Handler, report.Report, error) {
	h := handlers.Detect(r, name)
	if h == nil {
		rep := report.New("unknown")
		rep.Path = name
		rep.Status = report.StatusSkipped
		rep.Warn("unrecognized file type; skipped")
		return nil, done(rep, false), ErrUnknownFormat
	}
	pre, err := h.Inspect(r, o)
	if errors.Is(err, handlers.ErrSkip) {
		rep := report.New(h.Name())
		rep.Path = name
		rep.Status = report.StatusSkipped
		rep.Warn(err.Error())
		return h, done(rep, false), err
	}
	if err != nil {
		rep, err := failed(name, h.Name(), handlers.Prefixed(h.Name(), err))
		return h, rep, err
	}
	pre.Path = name
	if pre.Format == "" {
		pre.Format = h.Name()
	}
	return h, pre, nil
}

// cleanable turns a report-only result (an archive that needs a tool we may
// not use) into ErrNeedsTool.
func cleanable(pre *report.Report) error {
	if !pre.ReportOnly {
		return nil
	}
	pre.Status = report.StatusInspected
	*pre = done(*pre, false)
	return fmt.Errorf("%w: %s", ErrNeedsTool, strings.Join(pre.Warnings, "; "))
}

func cleaned(rep, pre report.Report, name string, oldSize, newSize int64) report.Report {
	rep.Path = name
	rep.ConvertTo = pre.ConvertTo
	rep.Status = report.StatusCleaned
	rep.Bytes = max(oldSize-newSize, 0)
	return done(rep, false)
}

func failed(name, format string, err error) (report.Report, error) {
	rep := report.New(format)
	rep.Path = name
	rep.Status = report.StatusError
	rep.FullyClean = false
	rep.Error = err.Error()
	return done(rep, false), err
}

func done(rep report.Report, dryRun bool) report.Report {
	rep.DryRun = dryRun
	rep.Normalize()
	return rep
}

// swapExt replaces a path's extension (e.g. holiday.rar → holiday.7z).
func swapExt(p, ext string) string {
	return strings.TrimSuffix(p, filepath.Ext(p)) + ext
}
