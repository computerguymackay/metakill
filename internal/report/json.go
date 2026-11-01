package report

import (
	"encoding/json"
	"io"
)

type reportJSON struct {
	Path           string   `json:"path"`
	Format         string   `json:"format"`
	DryRun         bool     `json:"dry_run"`
	Status         Status   `json:"status"`
	FullyClean     bool     `json:"fully_clean"`
	Fields         []Field  `json:"fields"`
	Kept           []Field  `json:"kept"`
	BytesRemovable *int64   `json:"bytes_removable,omitempty"`
	BytesSaved     *int64   `json:"bytes_saved,omitempty"`
	Warnings       []string `json:"warnings"`
	Error          string   `json:"error,omitempty"`
	Output         string   `json:"output,omitempty"`
	Backup         string   `json:"backup,omitempty"`
	Embedded       []Report `json:"embedded,omitempty"`
}

// MarshalJSON emits bytes_removable for dry runs and bytes_saved otherwise;
// every other key is identical between the two modes.
func (r Report) MarshalJSON() ([]byte, error) {
	r.Normalize()
	b := r.Bytes
	j := reportJSON{
		Path: r.Path, Format: r.Format, DryRun: r.DryRun, Status: r.Status,
		FullyClean: r.FullyClean, Fields: r.Fields, Kept: r.Kept,
		Warnings: r.Warnings, Error: r.Error, Output: r.Output, Backup: r.Backup,
		Embedded: r.Embedded,
	}
	if r.DryRun {
		j.BytesRemovable = &b
	} else {
		j.BytesSaved = &b
	}
	return json.Marshal(j)
}

type summaryJSON struct {
	Files          int    `json:"files"`
	Cleaned        int    `json:"cleaned"`
	AlreadyClean   int    `json:"already_clean"`
	Skipped        int    `json:"skipped"`
	Errors         int    `json:"errors"`
	NotFullyClean  int    `json:"not_fully_clean"`
	FieldsRemoved  int    `json:"fields_removed"`
	BytesRemovable *int64 `json:"bytes_removable,omitempty"`
	BytesSaved     *int64 `json:"bytes_saved,omitempty"`
}

type runJSON struct {
	DryRun  bool        `json:"dry_run"`
	Results []Report    `json:"results"`
	Summary summaryJSON `json:"summary"`
}

// WriteJSON writes the whole run as one indented JSON document.
func WriteJSON(w io.Writer, run *Run) error {
	s := run.Summary
	b := s.Bytes
	sj := summaryJSON{
		Files: s.Files, Cleaned: s.Cleaned, AlreadyClean: s.AlreadyClean,
		Skipped: s.Skipped, Errors: s.Errors, NotFullyClean: s.NotFullyClean,
		FieldsRemoved: s.FieldsRemoved,
	}
	if run.DryRun {
		sj.BytesRemovable = &b
	} else {
		sj.BytesSaved = &b
	}
	results := run.Results
	if results == nil {
		results = []Report{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(runJSON{DryRun: run.DryRun, Results: results, Summary: sj})
}
