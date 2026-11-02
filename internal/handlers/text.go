package handlers

import (
	"io"

	"github.com/computerguymackay/metakill/report"
)

// textHandler recognises plain text and Markdown. These formats carry no
// embedded metadata, so it never changes anything.
type textHandler struct{}

func init() { Register(textHandler{}) }

func (textHandler) Name() string { return "text" }

func (textHandler) CanHandle(_ io.ReadSeeker, path string) bool {
	return hasExt(path, ".txt", ".text", ".md", ".markdown")
}

func (textHandler) Inspect(io.ReadSeeker, Options) (report.Report, error) {
	return report.New("text"), nil
}

func (textHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, _ Options) (report.Report, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return report.Report{}, err
	}
	_, err := io.Copy(w, r)
	return report.New("text"), err
}
