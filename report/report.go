// Package report defines the per-file and per-run result types. The text and
// JSON emitters both consume these types, so dry-run and real-run output can
// never drift apart.
package report

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Category groups metadata fields for display and for keep rules.
type Category string

const (
	CatGPS       Category = "gps"
	CatTimestamp Category = "timestamp"
	CatDevice    Category = "device"
	CatAuthor    Category = "author"
	CatSoftware  Category = "software"
	CatIDTag     Category = "id_tag"
	CatThumbnail Category = "thumbnail"
	CatICC       Category = "icc"
	CatCustom    Category = "custom"
)

// CategoryOrder is the fixed display order used by the text emitter.
var CategoryOrder = []Category{
	CatGPS, CatTimestamp, CatDevice, CatAuthor, CatSoftware,
	CatIDTag, CatThumbnail, CatICC, CatCustom,
}

// Label returns the human-readable heading for a category.
func (c Category) Label() string {
	switch c {
	case CatGPS:
		return "GPS"
	case CatTimestamp:
		return "Timestamps"
	case CatDevice:
		return "Device"
	case CatAuthor:
		return "Author"
	case CatSoftware:
		return "Software"
	case CatIDTag:
		return "ID tags"
	case CatThumbnail:
		return "Thumbnail"
	case CatICC:
		return "ICC profile"
	default:
		return "Custom"
	}
}

// Field is one piece of metadata that was (or would be) removed or kept.
type Field struct {
	Category  Category `json:"category"`
	Namespace string   `json:"namespace"`
	Key       string   `json:"key"`
	Value     *string  `json:"value"`
	Size      int64    `json:"size,omitempty"`
}

// MaxValueRunes caps how much of a value is stored in a report.
const MaxValueRunes = 200

// Text builds a field with a printable value. If the value is not printable it
// is recorded as binary instead.
func Text(cat Category, ns, key, value string) Field {
	value = strings.TrimRight(value, "\x00")
	if !Printable(value) {
		return Binary(cat, ns, key, int64(len(value)))
	}
	v := truncate(value)
	return Field{Category: cat, Namespace: ns, Key: key, Value: &v}
}

// Binary builds a field whose value is not shown, only its size.
func Binary(cat Category, ns, key string, size int64) Field {
	return Field{Category: cat, Namespace: ns, Key: key, Size: size}
}

// Printable reports whether s is valid UTF-8 without control characters
// (tabs and newlines are allowed).
func Printable(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			return false
		}
	}
	return true
}

func truncate(s string) string {
	if utf8.RuneCountInString(s) <= MaxValueRunes {
		return s
	}
	r := []rune(s)
	return string(r[:MaxValueRunes]) + "…"
}

// Status is the outcome for one file.
type Status string

const (
	StatusCleaned      Status = "cleaned"
	StatusAlreadyClean Status = "already_clean"
	StatusSkipped      Status = "skipped"
	StatusError        Status = "error"
	// StatusInspected means metadata was found and reported but the file was
	// deliberately left unchanged (e.g. a 7z archive without -x).
	StatusInspected Status = "inspected"
)

// Report describes one file. Bytes is emitted as bytes_removable in dry-run
// mode and bytes_saved in real runs.
type Report struct {
	Path       string
	Format     string
	DryRun     bool
	Status     Status
	Fields     []Field
	Kept       []Field
	Bytes      int64
	FullyClean bool
	Warnings   []string
	Error      string
	Output     string
	Backup     string
	Embedded   []Report

	// ReportOnly is set by handlers that can list metadata but must not
	// rewrite the file in this run. Not serialised; it drives Status.
	ReportOnly bool
	// ConvertTo is set by handlers that will write a different format
	// (e.g. ".7z" for a RAR converted with -a). Not serialised.
	ConvertTo string
}

// New returns a report with non-nil slices and FullyClean set.
func New(format string) Report {
	return Report{Format: format, FullyClean: true, Fields: []Field{}, Kept: []Field{}, Warnings: []string{}}
}

// Add appends removed fields.
func (r *Report) Add(f ...Field) { r.Fields = append(r.Fields, f...) }

// Keep appends retained fields.
func (r *Report) Keep(f ...Field) { r.Kept = append(r.Kept, f...) }

// Warn records a warning without affecting FullyClean.
func (r *Report) Warn(msg string) { r.Warnings = append(r.Warnings, msg) }

// Unclean records that some metadata could not be removed.
func (r *Report) Unclean(msg string) {
	r.FullyClean = false
	r.Warnings = append(r.Warnings, msg)
}

// AddEmbedded attaches a nested report (e.g. an image inside a document) and
// propagates its cleanliness.
func (r *Report) AddEmbedded(e Report) {
	if !e.FullyClean {
		r.FullyClean = false
	}
	r.Embedded = append(r.Embedded, e)
}

// TotalFields counts removed fields including embedded reports.
func (r *Report) TotalFields() int {
	n := len(r.Fields)
	for i := range r.Embedded {
		n += r.Embedded[i].TotalFields()
	}
	return n
}

// Normalize makes every slice non-nil (recursively) so JSON never has null arrays.
func (r *Report) Normalize() {
	if r.Fields == nil {
		r.Fields = []Field{}
	}
	if r.Kept == nil {
		r.Kept = []Field{}
	}
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	for i := range r.Embedded {
		r.Embedded[i].DryRun = r.DryRun
		r.Embedded[i].Normalize()
	}
}

// Summary aggregates a run.
type Summary struct {
	Files         int
	Cleaned       int
	AlreadyClean  int
	Skipped       int
	Inspected     int
	Errors        int
	NotFullyClean int
	FieldsRemoved int
	Bytes         int64
}

// Run is the whole output document for one invocation.
type Run struct {
	DryRun  bool
	Results []Report
	Summary Summary
}

// Finish computes the summary from Results.
func (run *Run) Finish() {
	s := Summary{}
	for i := range run.Results {
		r := &run.Results[i]
		r.DryRun = run.DryRun
		r.Normalize()
		s.Files++
		switch r.Status {
		case StatusCleaned:
			s.Cleaned++
		case StatusAlreadyClean:
			s.AlreadyClean++
		case StatusInspected:
			s.Inspected++
		case StatusSkipped:
			s.Skipped++
		case StatusError:
			s.Errors++
		}
		if !r.FullyClean && r.Status != StatusSkipped {
			s.NotFullyClean++
		}
		if r.Status == StatusCleaned || r.Status == StatusAlreadyClean {
			s.FieldsRemoved += r.TotalFields()
			s.Bytes += r.Bytes
		}
	}
	if run.Results == nil {
		run.Results = []Report{}
	}
	run.Summary = s
}
