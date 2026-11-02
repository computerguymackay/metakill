package handlers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/computerguymackay/metakill/report"
)

// zipArchiveHandler cleans plain ZIP archives (and .cbz comic archives):
//   - archive level: member timestamps (set to 1980-01-01), extra fields
//     (uid/gid, Unix/NTFS times), archive and member comments, and
//     permission bits beyond directory/symlink/executable
//   - OS clutter: __MACOSX/, AppleDouble "._*" files, .DS_Store, Thumbs.db,
//     desktop.ini
//   - every member a handler recognises (images, documents, audio, video,
//     nested archives) is cleaned recursively; other members are copied
//     byte-for-byte without being decompressed.
//
// Signed or integrity-checked packages that happen to be ZIPs (APK, IPA,
// signed JAR/XPI, wheels, NuGet, VSIX, APPX) are skipped, because repacking
// would break them. Office, OpenDocument and EPUB files have their own
// handlers.
type zipArchiveHandler struct{}

func init() { Register(zipArchiveHandler{}) }

func (zipArchiveHandler) Name() string { return "zip" }

func (zipArchiveHandler) CanHandle(r io.ReadSeeker, p string) bool {
	if zipHasMember(r, func(z *zip.Reader) bool {
		if isOOXMLDoc(z) {
			return false
		}
		mt := zipMimetype(z)
		return mt != "application/epub+zip" && !strings.HasPrefix(mt, "application/vnd.oasis.opendocument")
	}) {
		return true
	}
	return hasExt(p, ".zip", ".cbz")
}

func zipPrecheck(r io.ReadSeeker) error {
	zr, err := openZip(r)
	if err != nil {
		return fmt.Errorf("zip: %w", err)
	}
	if kind := specialPackage(zr); kind != "" {
		return errSpecialPackage(kind)
	}
	return nil
}

func (zipArchiveHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	if err := zipPrecheck(r); err != nil {
		return report.Report{}, err
	}
	return zipInspect(r, "zip", zipArchiveFormat{}, opts)
}

func (zipArchiveHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	if err := zipPrecheck(r); err != nil {
		return report.Report{}, err
	}
	return zipStrip(r, w, "zip", zipArchiveFormat{}, opts)
}

type zipArchiveFormat struct{}

// homePath matches member names that look like absolute or home-directory
// paths, which can reveal a user name.
var homePath = regexp.MustCompile(`^/|^[A-Za-z]:[\\/]|(^|/)(Users|home|Documents and Settings)/[^/]+/`)

// sniffSize is how much of an oversized member is read to identify it.
const sniffSize = 64 << 10

func (zipArchiveFormat) process(z *zipDoc) error {
	warnedPath := false
	for _, e := range z.entries {
		if e.drop || strings.HasSuffix(e.name, "/") {
			continue
		}
		if !warnedPath && homePath.MatchString(e.name) {
			z.rep.Warn(fmt.Sprintf("member names look like absolute or home-directory paths (e.g. %q); names are not changed", e.name))
			warnedPath = true
		}
		if e.file.UncompressedSize64 > maxZipEntry {
			if h := z.sniff(e); h != "" {
				z.rep.Unclean(fmt.Sprintf("%s (%s, %d bytes) is too large to clean in memory; copied unchanged", e.name, h, e.file.UncompressedSize64))
			}
			continue
		}
		if err := z.load(e); err != nil {
			return fmt.Errorf("zip: %w", err)
		}
		if !z.cleanJarMetadata(e) {
			z.stripMember(e)
		}
		if e.out == nil {
			e.data, e.loaded = nil, false // unchanged: copy raw bytes, free memory
		}
	}
	return nil
}

// sniff identifies an oversized member from its first bytes.
func (z *zipDoc) sniff(e *zipEntry) string {
	rc, err := e.file.Open()
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(io.LimitReader(rc, sniffSize))
	if h := Detect(bytes.NewReader(b), ""); h != nil {
		return h.Name()
	}
	return ""
}
