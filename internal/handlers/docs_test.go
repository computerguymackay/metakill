package handlers

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/beevik/etree"

	"github.com/computerguymackay/metakill/internal/fixtures"
)

// unzip returns member contents in archive order and checks archive hygiene.
func unzip(t *testing.T, b []byte) (map[string][]byte, []string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("output is not a valid zip: %v", err)
	}
	if zr.Comment != "" {
		t.Errorf("archive comment kept: %q", zr.Comment)
	}
	out := map[string][]byte{}
	var order []string
	for _, f := range zr.File {
		if y := f.Modified.Year(); y != 1980 {
			t.Errorf("%s: timestamp %v", f.Name, f.Modified)
		}
		if len(f.Extra) > 0 {
			t.Errorf("%s: extra field kept", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		out[f.Name] = data
		order = append(order, f.Name)
		if strings.HasSuffix(f.Name, ".xml") || strings.HasSuffix(f.Name, ".rels") || strings.HasSuffix(f.Name, ".opf") {
			if err := etree.NewDocument().ReadFromBytes(data); err != nil {
				t.Errorf("%s is not well-formed: %v", f.Name, err)
			}
		}
	}
	return out, order
}

func TestDOCX(t *testing.T) {
	data := fixtures.LetterDOCX()
	for _, tc := range []struct {
		name       string
		opts       Options
		wantTitle  bool
		wantThumbs bool
	}{
		{"default", defaultOpts, false, false},
		{"keep-thumbnails", Options{ZeroPad: true, KeepTags: true, KeepThumbnails: true}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, pre, _ := roundTrip(t, "letter.docx", "ooxml", data, tc.opts)
			if len(pre.Embedded) == 0 {
				t.Error("embedded image not reported")
			}
			files, _ := unzip(t, out)
			all := bytes.Join(func() [][]byte {
				var v [][]byte
				for _, d := range files {
					v = append(v, d)
				}
				return v
			}(), nil)
			assertAbsent(t, all, fixtures.ArtistName, fixtures.Encoder, fixtures.Serial, fixtures.Model,
				fixtures.Software, "2023-04-12", "rsidR", "Normal.dotm", "jane@example.com", "Apple Inc", "TotalTime")
			if _, ok := files["docProps/custom.xml"]; ok {
				t.Error("custom.xml kept")
			}
			if _, ok := files["docProps/thumbnail.jpeg"]; ok != tc.wantThumbs {
				t.Errorf("thumbnail kept = %v, want %v", ok, tc.wantThumbs)
			}
			if got := bytes.Contains(files["docProps/core.xml"], []byte(fixtures.DocTitle)); got != tc.wantTitle {
				t.Errorf("title kept = %v, want %v", got, tc.wantTitle)
			}
			assertPresent(t, files["word/comments.xml"], `w:author="Author"`, `w:initials="A"`, fixtures.Comment)
			assertPresent(t, files["word/document.xml"], "inserted", "body text")
			assertPresent(t, files["docProps/core.xml"], "en-US")
			// Every internal relationship must point at an existing part.
			for name, d := range files {
				if !strings.HasSuffix(name, ".rels") {
					continue
				}
				doc := etree.NewDocument()
				_ = doc.ReadFromBytes(d)
				for _, rel := range doc.Root().ChildElements() {
					if rel.SelectAttrValue("TargetMode", "") == "External" {
						t.Errorf("%s: external relationship kept: %s", name, rel.SelectAttrValue("Target", ""))
						continue
					}
					target := resolveTarget(relsSource(name), rel.SelectAttrValue("Target", ""))
					if _, ok := files[target]; !ok {
						t.Errorf("%s: dangling relationship to %s", name, target)
					}
				}
			}
			ct := etree.NewDocument()
			_ = ct.ReadFromBytes(files["[Content_Types].xml"])
			for _, o := range ct.Root().SelectElements("Override") {
				p := strings.TrimPrefix(o.SelectAttrValue("PartName", ""), "/")
				if _, ok := files[p]; !ok {
					t.Errorf("content type override for missing part %s", p)
				}
			}
			assertAbsent(t, files["word/media/image1.jpeg"], fixtures.Make)
		})
	}
}

func TestODT(t *testing.T) {
	out, _, _ := roundTrip(t, "notes.odt", "odf", fixtures.NotesODT(), defaultOpts)
	files, order := unzip(t, out)
	if order[0] != "mimetype" {
		t.Fatalf("mimetype is not the first member: %v", order)
	}
	zr, _ := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if zr.File[0].Method != zip.Store {
		t.Fatal("mimetype must be stored uncompressed")
	}
	for name, d := range files {
		assertAbsent(t, d, fixtures.ArtistName, fixtures.Encoder, fixtures.Serial, fixtures.Model, "2023-04-12", "rsid", "/home/jane")
		_ = name
	}
	if _, ok := files["Thumbnails/thumbnail.png"]; ok {
		t.Error("thumbnail kept")
	}
	assertAbsent(t, files["META-INF/manifest.xml"], "Thumbnails/")
	assertPresent(t, files["settings.xml"], "TabsRelativeToIndent")
	assertPresent(t, files["content.xml"], "<dc:creator>Author</dc:creator>", fixtures.Comment)
	assertPresent(t, files["meta.xml"], "en-US", "document-statistic")
	assertAbsent(t, files["Pictures/photo.jpg"], fixtures.Make)
}

func TestEPUB(t *testing.T) {
	for _, tc := range []struct {
		name        string
		opts        Options
		wantCreator bool
	}{{"default", defaultOpts, false}, {"keep-tags", Options{ZeroPad: true, KeepTags: true}, true}} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, _ := roundTrip(t, "book.epub", "epub", fixtures.BookEPUB(), tc.opts)
			files, order := unzip(t, out)
			if order[0] != "mimetype" {
				t.Fatalf("mimetype is not first")
			}
			opf := files["OEBPS/content.opf"]
			assertAbsent(t, opf, "calibre", fixtures.Serial, "Apple Books", "2023-04-12")
			if !tc.wantCreator {
				assertAbsent(t, opf, "file-as") // refinement of the removed creator
			}
			assertPresent(t, opf, "urn:uuid:12345678-1234-1234-1234-123456789abc", fixtures.DocTitle,
				"<dc:language>en</dc:language>", `<meta property="dcterms:modified">2000-01-01T00:00:00Z</meta>`, `name="cover"`)
			if got := bytes.Contains(opf, []byte(fixtures.ArtistName)); got != tc.wantCreator {
				t.Errorf("creator kept = %v, want %v", got, tc.wantCreator)
			}
			if _, ok := files["META-INF/calibre_bookmarks.txt"]; ok {
				t.Error("calibre bookmarks kept")
			}
			assertAbsent(t, files["OEBPS/cover.jpg"], fixtures.Make)
		})
	}
}
