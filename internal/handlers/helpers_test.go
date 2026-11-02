package handlers

import (
	"bytes"
	"strings"
	"testing"

	"github.com/computerguymackay/metakill/internal/fixtures"
	"github.com/computerguymackay/metakill/internal/meta/memio"
	"github.com/computerguymackay/metakill/report"
)

// defaultOpts mirrors the CLI defaults (strip everything, zero padding).
var defaultOpts = Options{ZeroPad: true}

// secrets are planted in most fixtures and must never survive stripping.
var secrets = []string{
	fixtures.Make, fixtures.Model, fixtures.Software, fixtures.ArtistName,
	fixtures.Serial, fixtures.Comment, fixtures.Encoder, "2023:04:12", "2023-04-12",
}

// roundTrip detects, inspects, strips and re-inspects data. It fails the test
// if the handler is wrong, the strip errors, or anything survives.
func roundTrip(t *testing.T, name, wantFormat string, data []byte, opts Options) (out []byte, pre, post report.Report) {
	t.Helper()
	r := bytes.NewReader(data)
	h := Detect(r, name)
	if h == nil {
		t.Fatalf("%s: not detected", name)
	}
	if h.Name() != wantFormat {
		t.Fatalf("%s: detected as %q, want %q", name, h.Name(), wantFormat)
	}
	pre, err := h.Inspect(r, opts)
	if err != nil {
		t.Fatalf("%s: inspect: %v", name, err)
	}
	var buf memio.Buffer
	if _, err := r.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Strip(r, &buf, opts); err != nil {
		t.Fatalf("%s: strip: %v", name, err)
	}
	out = buf.Bytes()
	post, err = h.Inspect(bytes.NewReader(out), opts)
	if err != nil {
		t.Fatalf("%s: re-inspect of stripped output failed: %v", name, err)
	}
	if n := post.TotalFields(); n > 0 {
		t.Fatalf("%s: %d field(s) survived: %+v", name, n, post.Fields)
	}
	return out, pre, post
}

func assertAbsent(t *testing.T, out []byte, needles ...string) {
	t.Helper()
	for _, s := range needles {
		if bytes.Contains(out, []byte(s)) {
			t.Errorf("stripped output still contains %q", s)
		}
	}
}

func assertPresent(t *testing.T, out []byte, needles ...string) {
	t.Helper()
	for _, s := range needles {
		if !bytes.Contains(out, []byte(s)) {
			t.Errorf("stripped output lost %q", s)
		}
	}
}

// hasField reports whether rep lists a field whose key contains key.
func hasField(fs []report.Field, cat report.Category, key string) bool {
	for _, f := range fs {
		if f.Category == cat && strings.Contains(f.Key, key) {
			return true
		}
	}
	return false
}
