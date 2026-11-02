package handlers

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/beevik/etree"

	"github.com/computerguymackay/metakill/report"
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
	file   *zip.File
	name   string
	data   []byte // decompressed content (nil until loaded)
	loaded bool
	out    []byte // replacement content; nil means unchanged
	drop   bool
}

// zipDoc is a ZIP-based document or archive being cleaned.
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

// maxZipEntry caps how much of a single member is decompressed into memory.
// It is a variable so tests can lower it.
var maxZipEntry uint64 = 512 << 20

// openZip opens a ZIP archive's central directory.
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

// zipHasMember opens r as a ZIP (used by CanHandle) and applies pred.
func zipHasMember(r io.ReadSeeker, pred func(z *zip.Reader) bool) bool {
	if b := peek(r, 4); !bytes.Equal(b, []byte("PK\x03\x04")) && !bytes.Equal(b, []byte("PK\x05\x06")) {
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

// specialPackage recognises ZIP-based packages whose signatures or integrity
// manifests would be broken by repacking. It returns a description, or "".
func specialPackage(z *zip.Reader) string {
	for _, f := range z.File {
		n := f.Name
		ln := strings.ToLower(n)
		switch {
		case path.Base(n) == "AndroidManifest.xml":
			return "Android package (APK/AAB)"
		case strings.HasPrefix(n, "Payload/") && strings.Contains(n, ".app/"):
			return "iOS app package (IPA)"
		case strings.HasPrefix(ln, "meta-inf/") && (strings.HasSuffix(ln, ".sf") || strings.HasSuffix(ln, ".rsa") ||
			strings.HasSuffix(ln, ".dsa") || strings.HasSuffix(ln, ".ec")):
			return "signed Java or browser-extension package (JAR/XPI)"
		case n == "AppxSignature.p7x" || n == "AppxManifest.xml" || strings.HasPrefix(n, "AppxMetadata/"):
			return "Windows app package (APPX/MSIX)"
		case strings.HasSuffix(n, ".dist-info/RECORD"):
			return "Python wheel"
		case strings.HasSuffix(ln, ".nuspec") && !strings.Contains(n, "/"):
			return "NuGet package"
		case n == "extension.vsixmanifest":
			return "Visual Studio extension (VSIX)"
		case strings.HasPrefix(n, "package/services/digital-signature/"):
			return "signed OPC package"
		}
	}
	return ""
}

func errSpecialPackage(kind string) error {
	return fmt.Errorf("%s: repacking would break its signature or integrity checks; %w", kind, ErrSkip)
}

// identifyingExtra reports whether a ZIP extra field holds anything other
// than Zip64 size records (uid/gid, timestamps, NTFS times, ...).
func identifyingExtra(extra []byte) bool {
	for len(extra) >= 4 {
		id := uint16(extra[0]) | uint16(extra[1])<<8
		n := int(uint16(extra[2]) | uint16(extra[3])<<8)
		if id != 0x0001 {
			return true
		}
		if 4+n > len(extra) {
			return true
		}
		extra = extra[4+n:]
	}
	return len(extra) > 0
}

// loadZipDoc reads the central directory and reports archive-level
// metadata. With eager, every member is decompressed into memory (document
// formats); otherwise members are loaded on demand with z.load.
func loadZipDoc(r io.ReadSeeker, format string, opts Options, eager bool) (*zipDoc, error) {
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
		e := &zipEntry{file: f, name: f.Name}
		z.entries = append(z.entries, e)
		z.byName[f.Name] = e
		if eager {
			if err := z.load(e); err != nil {
				return nil, fmt.Errorf("%s: %w", format, err)
			}
		}
		mod := f.Modified.UTC()
		if !f.Modified.IsZero() && !sameDay(mod, dosEpoch) && mod.After(latest) {
			latest = mod
		}
		if identifyingExtra(f.Extra) {
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
	z.dropJunk()
	return z, nil
}

// load decompresses a member into memory (at most maxZipEntry bytes).
func (z *zipDoc) load(e *zipEntry) error {
	if e.loaded {
		return nil
	}
	if e.file.UncompressedSize64 > maxZipEntry {
		return fmt.Errorf("member %s is too large (%d bytes)", e.name, e.file.UncompressedSize64)
	}
	rc, err := e.file.Open()
	if err != nil {
		return fmt.Errorf("%s: %w", e.name, err)
	}
	data, err := io.ReadAll(io.LimitReader(rc, int64(maxZipEntry)+1))
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", e.name, err)
	}
	if uint64(len(data)) > maxZipEntry {
		return fmt.Errorf("member %s is too large", e.name)
	}
	e.data, e.loaded = data, true
	return nil
}

// junkKind classifies operating-system clutter that only carries metadata.
func junkKind(name string) (report.Category, bool) {
	base := strings.ToLower(path.Base(strings.TrimSuffix(name, "/")))
	switch {
	case strings.HasPrefix(name, "__MACOSX/") || name == "__MACOSX/":
		return report.CatCustom, true
	case strings.HasPrefix(base, "._"):
		return report.CatCustom, true // AppleDouble: Finder info, quarantine, where-from
	case base == ".ds_store":
		return report.CatCustom, true
	case base == "thumbs.db" || base == "ehthumbs.db" || base == "ehthumbs_vista.db":
		return report.CatThumbnail, true
	case base == "desktop.ini":
		return report.CatCustom, true
	}
	return "", false
}

// dropJunk removes macOS/Windows clutter files (AppleDouble, .DS_Store,
// Thumbs.db, desktop.ini) from every ZIP-based format.
func (z *zipDoc) dropJunk() {
	for _, e := range z.entries {
		if cat, ok := junkKind(e.name); ok {
			if strings.HasSuffix(e.name, "/") {
				e.drop = true // the __MACOSX/ directory entry itself
				continue
			}
			z.dropMember(e, report.Binary(cat, "zip", e.name, int64(e.file.UncompressedSize64)))
		}
	}
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

// normalMode keeps only what matters for extraction: directory, symlink and
// executable bits. Everything else (owner-only modes, set-uid, MS-DOS
// attributes) is normalised.
func normalMode(m os.FileMode) os.FileMode {
	switch {
	case m.IsDir():
		return os.ModeDir | 0o755
	case m&os.ModeSymlink != 0:
		return os.ModeSymlink | 0o777
	case m&0o111 != 0:
		return 0o755
	}
	return 0o644
}

// write serialises the cleaned archive. Unchanged members are copied as their
// original compressed bytes. mimetype (if present) is written first and
// stored uncompressed, as ODF and EPUB require. Every member gets the same
// 1980-01-01 timestamp, no extra fields and no comment.
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
		f := e.file
		hdr := &zip.FileHeader{
			Name: e.name,
			// 1980-01-01 in MS-DOS format. Setting Modified instead would make
			// archive/zip add an extended-timestamp extra field.
			ModifiedDate: 0x21, //nolint:staticcheck // deliberate, see above
		}
		hdr.SetMode(normalMode(f.Mode()))
		hdr.Flags = f.Flags & 0x800 // keep only the UTF-8 name flag

		mustStore := e.name == "mimetype" && f.Method != zip.Store
		if e.out == nil && !mustStore {
			// Copy the original compressed bytes untouched.
			hdr.Method = f.Method
			hdr.Flags |= f.Flags & 0x6 // deflate option bits
			hdr.CRC32 = f.CRC32
			hdr.CompressedSize64 = f.CompressedSize64
			hdr.UncompressedSize64 = f.UncompressedSize64
			raw, err := f.OpenRaw()
			if err != nil {
				return err
			}
			fw, err := zw.CreateRaw(hdr)
			if err != nil {
				return err
			}
			if _, err := io.Copy(fw, raw); err != nil {
				return err
			}
			continue
		}
		if !e.loaded {
			if err := z.load(e); err != nil {
				return err
			}
		}
		data := e.content()
		if e.name == "mimetype" || strings.HasSuffix(e.name, "/") || len(data) == 0 {
			hdr.Method = zip.Store
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
	if !e.loaded {
		if err := z.load(e); err != nil {
			z.rep.Unclean(err.Error())
			return
		}
	}
	out, sub, ok := StripEmbedded(e.name, e.data, z.opts)
	if !ok {
		return
	}
	if sub.TotalFields() > 0 || !sub.FullyClean || len(sub.Warnings) > 0 {
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
	z, err := loadZipDoc(r, format, opts, format != "zip")
	if err != nil {
		return report.Report{}, err
	}
	if err := f.process(z); err != nil {
		return z.rep, err
	}
	if z.rep.TotalFields() > 0 {
		var cw countWriter // count the rewritten size without holding it in memory
		if err := z.write(&cw); err == nil {
			if size, err := streamSize(r); err == nil && size > cw.n {
				z.rep.Bytes = size - cw.n
			}
		}
	}
	return z.rep, nil
}

func zipStrip(r io.ReadSeeker, w io.Writer, format string, f zipFormat, opts Options) (report.Report, error) {
	z, err := loadZipDoc(r, format, opts, format != "zip")
	if err != nil {
		return report.Report{}, err
	}
	if err := f.process(z); err != nil {
		return z.rep, err
	}
	return z.rep, z.write(w)
}

var errNoRoot = errors.New("XML part has no root element")
