package handlers

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/report"
)

func TestPDF(t *testing.T) {
	data := fixtures.ReportPDF()
	tests := []struct {
		name      string
		opts      Options
		wantTitle bool
	}{
		{"default", defaultOpts, false},
		{"keep-tags", Options{ZeroPad: true, KeepTags: true}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "report.pdf", "pdf", data, tc.opts)
			for _, want := range []struct {
				cat report.Category
				key string
			}{
				{report.CatAuthor, "Author"}, {report.CatSoftware, "Producer"},
				{report.CatTimestamp, "CreationDate"}, {report.CatTimestamp, "LastModified"},
				{report.CatAuthor, "T (author)"}, {report.CatSoftware, "PieceInfo"},
				{report.CatSoftware, "xmp:CreatorTool"}, {report.CatCustom, "PreviousRevisions"},
			} {
				if !hasField(pre.Fields, want.cat, want.key) {
					t.Errorf("inspect missing %s/%s", want.cat, want.key)
				}
			}
			if len(pre.Embedded) != 1 || pre.Embedded[0].Format != "jpeg" {
				t.Errorf("expected one embedded JPEG report, got %+v", pre.Embedded)
			}
			if pre.Bytes <= 0 {
				t.Errorf("dry-run bytes_removable = %d, want > 0", pre.Bytes)
			}
			assertAbsent(t, out, secrets...)
			assertAbsent(t, out, "Old Author Name", "pdfcpu", "PieceInfo", "LastModified",
				"/Metadata", "D:2023", "0123456789ABCDEF", "FEDCBA9876543210")
			if got := bytes.Contains(out, []byte(fixtures.PDFTitle)); got != tc.wantTitle {
				t.Errorf("title present = %v, want %v", got, tc.wantTitle)
			}
			if n := bytes.Count(out, []byte("%%EOF")); n != 1 {
				t.Errorf("output has %d revisions, want 1", n)
			}
			assertPresent(t, out, "/Im1 Do", "/DCTDecode")
			conf := pdfConfig()
			if _, err := api.ReadAndValidate(context.Background(), bytes.NewReader(out), conf); err != nil {
				t.Fatalf("output does not validate: %v", err)
			}
		})
	}
}

func TestPDFGarbageErrors(t *testing.T) {
	if _, err := (pdfHandler{}).Inspect(bytes.NewReader([]byte("%PDF-1.4\ngarbage")), defaultOpts); err == nil {
		t.Fatal("expected error for broken PDF")
	}
}

// pdfcpu must never touch its configuration directory.
func TestPDFDoesNotTouchConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("AppData", home)
	roundTrip(t, "report.pdf", "pdf", fixtures.ReportPDF(), defaultOpts)
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pdfcpu wrote to the config directory: %v", entries)
	}
}
