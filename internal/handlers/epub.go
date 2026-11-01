package handlers

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/beevik/etree"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/report"
)

// epubHandler cleans the OPF package metadata while keeping what the EPUB
// specification requires: the unique dc:identifier, dc:title, dc:language
// and (EPUB 3) dcterms:modified, which is reset to a fixed date. With -k the
// creator (book author) is kept too. Calibre/iTunes side files are removed
// and images are cleaned recursively.
type epubHandler struct{}

func init() { Register(epubHandler{}) }

func (epubHandler) Name() string { return "epub" }

func (epubHandler) CanHandle(r io.ReadSeeker, p string) bool {
	if zipHasMember(r, func(z *zip.Reader) bool { return zipMimetype(z) == "application/epub+zip" }) {
		return true
	}
	return hasExt(p, ".epub")
}

func (epubHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	return zipInspect(r, "epub", epubFormat{}, opts)
}

func (epubHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	return zipStrip(r, w, "epub", epubFormat{}, opts)
}

type epubFormat struct{}

const epubFixedModified = "2000-01-01T00:00:00Z"

var epubSideFiles = map[string]report.Category{
	"META-INF/calibre_bookmarks.txt":         report.CatCustom,
	"iTunesMetadata.plist":                   report.CatAuthor,
	"iTunesMetadata-original.plist":          report.CatAuthor,
	"META-INF/com.apple.ibooks.purchase.xml": report.CatAuthor,
}

func (epubFormat) process(z *zipDoc) error {
	container := z.byName["META-INF/container.xml"]
	if container == nil {
		return errors.New("epub: missing META-INF/container.xml")
	}
	doc, err := parseXML(container.data)
	if err != nil {
		return fmt.Errorf("epub: container.xml: %w", err)
	}
	opfs := map[string]bool{}
	for _, rf := range doc.FindElements("//rootfile") {
		if p := rf.SelectAttrValue("full-path", ""); p != "" {
			opfs[p] = true
		}
	}
	if len(opfs) == 0 {
		return errors.New("epub: no rootfile in container.xml")
	}
	for _, e := range z.entries {
		switch {
		case opfs[e.name]:
			if err := epubOPF(z, e); err != nil {
				return err
			}
		case epubSideFiles[e.name] != "":
			z.dropMember(e, report.Binary(epubSideFiles[e.name], "epub", e.name, int64(len(e.data))))
		case isMediaName(e.name):
			z.stripMember(e)
		}
	}
	return nil
}

func epubOPF(z *zipDoc, e *zipEntry) error {
	doc, err := parseXML(e.data)
	if err != nil {
		return fmt.Errorf("epub: %s: %w", e.name, err)
	}
	pkg := doc.Root()
	if pkg == nil {
		return errNoRoot
	}
	meta := pkg.SelectElement("metadata")
	if meta == nil {
		return nil
	}
	uid := pkg.SelectAttrValue("unique-identifier", "")
	epub3 := strings.HasPrefix(pkg.SelectAttrValue("version", ""), "3")
	changed := false
	removedIDs := map[string]bool{}
	drop := func(c *etree.Element, f report.Field) {
		z.rep.Add(f)
		if id := c.SelectAttrValue("id", ""); id != "" {
			removedIDs[id] = true
		}
		removeNode(&meta.Child, c)
		changed = true
	}
	for _, c := range meta.ChildElements() {
		v := strings.TrimSpace(c.Text())
		isDC := c.Space == "dc" || c.NamespaceURI() == "http://purl.org/dc/elements/1.1/"
		switch {
		case isDC && c.Tag == "identifier":
			if c.SelectAttrValue("id", "") == uid {
				z.rep.Keep(report.Text(report.CatCustom, "epub", "dc:identifier (required)", v))
				continue
			}
			drop(c, report.Text(report.CatCustom, "epub", "dc:identifier", v))
		case isDC && c.Tag == "title":
			z.rep.Keep(report.Text(report.CatIDTag, "epub", "dc:title (required)", v))
		case isDC && c.Tag == "language":
		case isDC && c.Tag == "creator" && z.opts.KeepTags:
			z.rep.Keep(report.Text(report.CatAuthor, "epub", "dc:creator", v))
		case isDC:
			cat := classify.DocProp(c.Tag)
			if c.Tag == "subject" || c.Tag == "description" || c.Tag == "source" || c.Tag == "coverage" || c.Tag == "relation" || c.Tag == "type" || c.Tag == "format" {
				cat = report.CatCustom
			}
			drop(c, report.Text(cat, "epub", c.FullTag(), v))
		case c.Tag == "meta":
			prop := c.SelectAttrValue("property", "")
			name := c.SelectAttrValue("name", "")
			refines := strings.TrimPrefix(c.SelectAttrValue("refines", ""), "#")
			switch {
			case prop == "dcterms:modified" && epub3 && refines == "":
				if v != epubFixedModified {
					z.rep.Add(report.Text(report.CatTimestamp, "epub", "dcterms:modified", v))
					c.SetText(epubFixedModified)
					changed = true
				}
			case name == "cover":
				// references the cover image in the manifest; not metadata
			case refines != "":
				// resolved after the main pass
			default:
				key := prop
				if key == "" {
					key = name
				}
				val := v
				if val == "" {
					val = c.SelectAttrValue("content", "")
				}
				cat := report.CatCustom
				lk := strings.ToLower(key)
				switch {
				case strings.Contains(lk, "timestamp") || strings.Contains(lk, "date") || strings.Contains(lk, "modified"):
					cat = report.CatTimestamp
				case strings.Contains(lk, "generator") || strings.Contains(lk, "calibre:version"):
					cat = report.CatSoftware
				case strings.Contains(lk, "author"):
					cat = report.CatAuthor
				}
				drop(c, report.Text(cat, "epub.meta", key, val))
			}
		case c.Tag == "link":
			drop(c, report.Text(report.CatCustom, "epub", "link", c.SelectAttrValue("href", "")))
		default:
			drop(c, report.Text(report.CatCustom, "epub", c.FullTag(), v))
		}
	}
	// Refinements of removed elements go too (e.g. file-as, role).
	for _, c := range meta.ChildElements() {
		if c.Tag != "meta" {
			continue
		}
		ref := strings.TrimPrefix(c.SelectAttrValue("refines", ""), "#")
		if ref != "" && removedIDs[ref] {
			drop(c, report.Text(report.CatCustom, "epub.meta", c.SelectAttrValue("property", "refines"), strings.TrimSpace(c.Text())))
		}
	}
	// EPUB 2 opf: attributes on kept dc elements (opf:file-as, opf:role).
	for _, c := range meta.ChildElements() {
		var attrs []etree.Attr
		for _, a := range c.Attr {
			if a.Space == "opf" && (a.Key == "file-as" || a.Key == "role") && (c.Tag != "creator" || !z.opts.KeepTags) {
				z.rep.Add(report.Text(report.CatAuthor, "epub", c.FullTag()+"@opf:"+a.Key, a.Value))
				changed = true
				continue
			}
			attrs = append(attrs, a)
		}
		c.Attr = attrs
	}
	if !changed {
		return nil
	}
	out, err := writeXML(doc)
	if err != nil {
		return err
	}
	e.out = out
	return nil
}
