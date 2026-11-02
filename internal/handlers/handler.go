// Package handlers holds one metadata handler per format family plus the
// registry that picks a handler for a file (magic bytes first, extension second).
package handlers

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/computerguymackay/metakill/internal/meta/memio"
	"github.com/computerguymackay/metakill/report"
)

// Options controls what a handler removes. The zero value strips everything
// except ZeroPad, which callers normally set to true.
type Options struct {
	KeepTags       bool // -k: keep Title/Artist/Album/Track/Year
	KeepThumbnails bool // -t: keep thumbnails and cover art (the CLI also sets KeepTags)
	KeepICC        bool // -c: keep ICC colour profiles
	ZeroPad        bool // zero existing padding regions (disabled by -z)
	DryRun         bool // informational; Strip is never called in dry-run mode
	ExternalTools  bool // -x: allow running 7-Zip / rar to repack 7z and RAR archives
	ConvertRAR     bool // -a: allow converting RAR to 7z when no rar tool exists
	Depth          int  // nesting level for embedded media
}

// MaxDepth limits recursion into embedded media (e.g. a JPEG inside a DOCX
// inside an EPUB).
const MaxDepth = 4

// Handler strips metadata from one format family.
type Handler interface {
	// Name is the short format name used in reports ("jpeg", "pdf", ...).
	Name() string
	// CanHandle reports whether the content (magic bytes) matches, or, when
	// path is non-empty, whether the extension matches. The registry calls it
	// first with an empty path so magic bytes always win.
	CanHandle(r io.ReadSeeker, path string) bool
	// Inspect lists what Strip would remove without writing anything.
	Inspect(r io.ReadSeeker, opts Options) (report.Report, error)
	// Strip writes a cleaned copy of r to w and reports what was removed.
	Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error)
}

var registry []Handler

// Register adds a handler. Order matters only between handlers whose magic
// bytes overlap; more specific handlers must register first.
func Register(h Handler) { registry = append(registry, h) }

// All returns the registered handlers in registration order.
func All() []Handler { return append([]Handler(nil), registry...) }

// ByName finds a handler by its Name.
func ByName(name string) Handler {
	for _, h := range registry {
		if h.Name() == name {
			return h
		}
	}
	return nil
}

// Detect returns the handler for r, trying magic bytes across all handlers
// before falling back to the file extension. It returns nil if none match.
func Detect(r io.ReadSeeker, path string) Handler {
	for _, h := range registry {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return nil
		}
		if h.CanHandle(r, "") {
			_, _ = r.Seek(0, io.SeekStart)
			return h
		}
	}
	if path == "" {
		return nil
	}
	for _, h := range registry {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return nil
		}
		if h.CanHandle(r, path) {
			_, _ = r.Seek(0, io.SeekStart)
			return h
		}
	}
	_, _ = r.Seek(0, io.SeekStart)
	return nil
}

// ErrUnsupported is returned for recognised-but-unsupported variants.
var ErrUnsupported = errors.New("unsupported format variant")

// ErrSkip is wrapped by handlers that recognise a file but deliberately leave
// it alone (for example a signed Android package, which repacking would
// break). The CLI reports such files as skipped with the error text as the
// warning, rather than as failures.
var ErrSkip = errors.New("skipped")

// StripEmbedded cleans a nested file (an image inside a document, say). It
// returns ok=false when no handler recognises the data or the depth limit is
// reached; the caller should then keep the original bytes.
func StripEmbedded(name string, data []byte, opts Options) (out []byte, rep report.Report, ok bool) {
	if opts.Depth >= MaxDepth {
		return nil, report.Report{}, false
	}
	r := bytes.NewReader(data)
	h := Detect(r, "")
	if h == nil {
		return nil, report.Report{}, false
	}
	sub := opts
	sub.Depth++
	pre, err := h.Inspect(r, sub)
	if errors.Is(err, ErrSkip) {
		rep = report.New(h.Name())
		rep.Path = name
		rep.Status = report.StatusSkipped
		rep.Warn(err.Error())
		return nil, rep, true
	}
	if err != nil {
		rep = report.New(h.Name())
		rep.Path = name
		rep.Unclean(fmt.Sprintf("embedded %s could not be parsed: %v", h.Name(), err))
		rep.Status = report.StatusError
		return nil, rep, true
	}
	pre.Path = name
	if pre.ReportOnly || pre.ConvertTo != "" {
		// Nested archives that need conversion or tools we may not use are
		// reported but left as they are (a member cannot change its name).
		if pre.ConvertTo != "" {
			pre.Unclean("nested RAR archives cannot be converted inside another file; left unchanged")
		}
		pre.Status = report.StatusInspected
		return data, pre, true
	}
	if len(pre.Fields) == 0 && len(pre.Embedded) == 0 {
		pre.Status = report.StatusAlreadyClean
		return data, pre, true
	}
	if opts.DryRun {
		pre.Status = report.StatusCleaned
		return data, pre, true
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, report.Report{}, false
	}
	var buf memio.Buffer
	rep, err = h.Strip(r, &buf, sub)
	rep.Path = name
	if err != nil {
		rep = pre
		rep.Unclean(fmt.Sprintf("embedded %s could not be stripped: %v", h.Name(), err))
		rep.Status = report.StatusError
		return nil, rep, true
	}
	rep.Status = report.StatusCleaned
	rep.Bytes = int64(len(data) - buf.Len())
	if rep.Bytes < 0 {
		rep.Bytes = 0
	}
	return buf.Bytes(), rep, true
}

// --- small helpers shared by handlers ---

// peek reads up to n bytes from the start of r and rewinds.
func peek(r io.ReadSeeker, n int) []byte {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	b := make([]byte, n)
	m, _ := io.ReadFull(r, b)
	_, _ = r.Seek(0, io.SeekStart)
	return b[:m]
}

// hasExt reports whether path ends in one of exts (lower-case, with dot).
func hasExt(path string, exts ...string) bool {
	if path == "" {
		return false
	}
	e := strings.ToLower(filepath.Ext(path))
	for _, x := range exts {
		if e == x {
			return true
		}
	}
	return false
}

// readAll reads the whole stream from the start.
func readAll(r io.ReadSeeker) ([]byte, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// streamSize returns the total size of r and rewinds it.
func streamSize(r io.ReadSeeker) (int64, error) {
	n, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	_, err = r.Seek(0, io.SeekStart)
	return n, err
}

// keepEssential reports whether a canonical tag name survives -k.
func keepEssential(opts Options, canonical string) bool {
	if !opts.KeepTags {
		return false
	}
	switch canonical {
	case "title", "artist", "album", "track", "year":
		return true
	}
	return false
}

// yearOnly reduces a date string to its leading four-digit year, or "" if
// there is none.
func yearOnly(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 4 {
		y := s[:4]
		for _, c := range y {
			if c < '0' || c > '9' {
				return ""
			}
		}
		if len(s) == 4 || s[4] < '0' || s[4] > '9' {
			return y
		}
	}
	return ""
}

func bufReader(r io.Reader) *bufio.Reader { return bufio.NewReaderSize(r, 1<<16) }

func newBufWriter(w io.Writer) *bufio.Writer { return bufio.NewWriterSize(w, 1<<16) }
