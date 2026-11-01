package handlers

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/beevik/etree"

	"github.com/computerguymackay/metakill/internal/report"
)

// Placeholder values written where a document format requires an author or
// a date to be present. Inspect treats them as clean.
const (
	anonAuthor   = "Author"
	anonInitials = "A"
	anonDate     = "1980-01-01T00:00:00Z"
)

// zipEntry is one archive member being processed.
type zipEntry struct {
	file *zip.File
	name string
	data []byte // decompressed content
	out  []byte // replacement content; nil means unchanged
	drop bool
}

// zipDoc is a ZIP-based document being cleaned.
type zipDoc struct {
	rep     report.Report
	opts    Options
	entries []*zipEntry
	byName  map[string]*zipEntry
	comment string
}

// zipFormat customises zipDoc for one document family.
type zipFormat interface {
	// process inspects/cleans every entry (set e.out / e.drop).
	process(z *zipDoc) error
}

const maxZipEntry = 512 << 20

// openZip reads every member of a ZIP archive into memory.
func openZip(r io.ReadSeeker) (*zip.Reader, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, err
	}
	ra, ok := r.(io.ReaderAt)
	if !ok {
		b, err := readAll(r)
		if err != nil {
			return nil, err
		}
		ra = bytes.NewReader(b)
	}
	return zip.NewReader(ra, size)
}

// zipHasMember checks for a member name (used by CanHandle).
func zipHasMember(r io.ReadSeeker, pred func(z *zip.Reader) bool) bool {
	if b := peek(r, 4); !bytes.Equal(b, []byte("PK\x03\x04")) {
		return false
	}
	zr, err := openZip(r)
	_, _ = r.Seek(0, io.SeekStart)
	if err != nil {
		return false
	}
	return pred(zr)
}

// zipMimetype returns the content of the "mimetype" member, if any.
func zipMimetype(z *zip.Reader) string {
	for _, f := range z.File {
		if f.Name == "mimetype" {
			rc, err := f.Open()
			if err != nil {
				return ""
			}
			defer func() { _ = rc.Close() }()
			b, _ := io.ReadAll(io.LimitReader(rc, 256))
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

func loadZipDoc(r io.ReadSeeker, format string, opts Options) (*zipDoc, error) {
	zr, err := openZip(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", format, err)
	}
	z := &zipDoc{rep: report.New(format), opts: opts, byName: map[string]*zipEntry{}, comment: zr.Comment}
	dosEpoch := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	var latest time.Time
	extras := 0
	for _, f := range zr.File {
		if f.Flags&1 != 0 {
			return nil, fmt.Errorf("%s: encrypted archive members are %w", format, ErrUnsupported)
		}
		if f.UncompressedSize64 > maxZipEntry {
			return nil, fmt.Errorf("%s: member %s is too large", format, f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", format, f.Name, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxZipEntry))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", format, f.Name, err)
		}
		e := &zipEntry{file: f, name: f.Name, data: data}
		z.entries = append(z.entries, e)
		z.byName[f.Name] = e
		mod := f.Modified.UTC()
		if !f.Modified.IsZero() && !sameDay(mod, dosEpoch) && mod.After(latest) {
			latest = mod
		}
		if len(f.Extra) > 0 {
			extras++
		}
		if f.Comment != "" {
			z.rep.Add(report.Text(report.CatCustom, "zip", "comment:"+f.Name, f.Comment))
		}
	}
	if z.comment != "" {
		z.rep.Add(report.Text(report.CatCustom, "zip", "archive comment", z.comment))
	}
	if !latest.IsZero() {
		z.rep.Add(report.Text(report.CatTimestamp, "zip", "member timestamps (latest)", latest.Format(time.RFC3339)))
	}
	if extras > 0 {
		z.rep.Add(report.Text(report.CatCustom, "zip", "extra fields (uid/gid, timestamps)", fmt.Sprintf("%d member(s)", extras)))
	}
	return z, nil
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

// content returns the current content of an entry.
func (e *zipEntry) content() []byte {
	if e.out != nil {
		return e.out
	}
	return e.data
}

// write serialises the cleaned archive. mimetype (if present) is written
// first and stored uncompressed, as ODF and EPUB require.
func (z *zipDoc) write(w io.Writer) error {
	zw := zip.NewWriter(w)
	order := make([]*zipEntry, 0, len(z.entries))
	for _, e := range z.entries {
		if e.name == "mimetype" {
			order = append([]*zipEntry{e}, order...)
		} else {
			order = append(order, e)
		}
	}
	for _, e := range order {
		if e.drop {
			continue
		}
		data := e.content()
		method := e.file.Method
		if e.name == "mimetype" {
			method = zip.Store
		}
		hdr := &zip.FileHeader{
			Name:   e.name,
			Method: method,
			// 1980-01-01 in MS-DOS format. Setting Modified instead would make
			// archive/zip add an extended-timestamp extra field.
			ModifiedDate: 0x21, //nolint:staticcheck // deliberate, see above
		}
		hdr.SetMode(0o644)
		hdr.ExternalAttrs = 0
		if method == zip.Store {
			hdr.CRC32 = crc32.ChecksumIEEE(data)
			hdr.CompressedSize64 = uint64(len(data))
			hdr.UncompressedSize64 = uint64(len(data))
			fw, err := zw.CreateRaw(hdr)
			if err != nil {
				return err
			}
			if _, err := fw.Write(data); err != nil {
				return err
			}
			continue
		}
		hdr.Method = zip.Deflate
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := fw.Write(data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// stripMember cleans an embedded file (image, nested document) in place.
func (z *zipDoc) stripMember(e *zipEntry) {
	out, sub, ok := StripEmbedded(e.name, e.data, z.opts)
	if !ok {
		return
	}
	if sub.TotalFields() > 0 || !sub.FullyClean {
		z.rep.AddEmbedded(sub)
	}
	if out != nil && !bytes.Equal(out, e.data) {
		e.out = out
	}
}

// dropMember removes an entry and reports it.
func (z *zipDoc) dropMember(e *zipEntry, f report.Field) {
	z.rep.Add(f)
	e.drop = true
}

// dropped returns the set of dropped member names.
func (z *zipDoc) dropped() map[string]bool {
	m := map[string]bool{}
	for _, e := range z.entries {
		if e.drop {
			m[e.name] = true
		}
	}
	return m
}

// parseXML parses an entry as XML.
func parseXML(data []byte) (*etree.Document, error) {
	doc := etree.NewDocument()
	doc.ReadSettings.PreserveCData = true
	if err := doc.ReadFromBytes(data); err != nil {
		return nil, err
	}
	return doc, nil
}

func writeXML(doc *etree.Document) ([]byte, error) {
	doc.WriteSettings.CanonicalEndTags = false
	return doc.WriteToBytes()
}

// isMediaName reports members worth running through the embedded cleaner.
func isMediaName(name string) bool {
	n := strings.ToLower(name)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".tif", ".tiff", ".webp", ".heic", ".svg",
		".docx", ".xlsx", ".pptx", ".docm", ".xlsm", ".pptm", ".odt", ".ods", ".odp", ".pdf",
		".mp3", ".m4a", ".mp4", ".mov", ".wav", ".flac", ".ogg", ".opus", ".jfif"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

// relsSource returns the directory of the part that a .rels file describes.
func relsSource(relsName string) string {
	dir := path.Dir(path.Dir(relsName)) // strip "_rels/x.rels"
	if dir == "." {
		return ""
	}
	return dir
}

// resolveTarget resolves a relationship target against a source directory.
func resolveTarget(srcDir, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(path.Clean(target), "/")
	}
	return strings.TrimPrefix(path.Clean(path.Join("/", srcDir, target)), "/")
}

func sortedKeys2(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// zipInspect / zipStrip are the shared Handler bodies for ZIP documents.
func zipInspect(r io.ReadSeeker, format string, f zipFormat, opts Options) (report.Report, error) {
	z, err := loadZipDoc(r, format, opts)
	if err != nil {
		return report.Report{}, err
	}
	if err := f.process(z); err != nil {
		return z.rep, err
	}
	if z.rep.TotalFields() > 0 {
		var buf bytes.Buffer
		if err := z.write(&buf); err == nil {
			if size, err := streamSize(r); err == nil && size > int64(buf.Len()) {
				z.rep.Bytes = size - int64(buf.Len())
			}
		}
	}
	return z.rep, nil
}

func zipStrip(r io.ReadSeeker, w io.Writer, format string, f zipFormat, opts Options) (report.Report, error) {
	z, err := loadZipDoc(r, format, opts)
	if err != nil {
		return report.Report{}, err
	}
	if err := f.process(z); err != nil {
		return z.rep, err
	}
	return z.rep, z.write(w)
}

var errNoRoot = errors.New("XML part has no root element")
