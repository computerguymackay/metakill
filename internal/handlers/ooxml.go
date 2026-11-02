package handlers

import (
	"archive/zip"
	"fmt"
	"io"
	"strings"

	"github.com/beevik/etree"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/report"
)

// ooxmlHandler cleans Word, Excel and PowerPoint (OOXML) documents:
//   - docProps/core.xml: everything except language (and title with -k)
//   - docProps/app.xml: application, version, company, manager, template,
//     editing time, hyperlink base
//   - docProps/custom.xml and people/persons parts: removed or anonymised
//   - comment and tracked-change authors → "Author", initials → "A",
//     dates → 1980-01-01; rsid session IDs removed; attached template paths,
//     absolute save paths and document variables removed
//   - thumbnails removed (kept with -t); embedded media cleaned recursively
//   - relationships and content types pruned to match.
type ooxmlHandler struct{}

func init() { Register(ooxmlHandler{}) }

func (ooxmlHandler) Name() string { return "ooxml" }

func (ooxmlHandler) CanHandle(r io.ReadSeeker, p string) bool {
	if zipHasMember(r, isOOXMLDoc) {
		return true
	}
	return hasExt(p, ".docx", ".docm", ".dotx", ".dotm", ".xlsx", ".xlsm", ".xltx", ".xltm",
		".pptx", ".pptm", ".potx", ".potm", ".ppsx", ".ppsm", ".vsdx")
}

func (h ooxmlHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	return zipInspect(r, "ooxml", ooxmlFormat{}, opts)
}

func (h ooxmlHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	return zipStrip(r, w, "ooxml", ooxmlFormat{}, opts)
}

// isOOXMLDoc reports an Office document: an OPC package that is not one of
// the signed/special packages (NuGet, VSIX, APPX) that share its layout.
func isOOXMLDoc(z *zip.Reader) bool {
	if specialPackage(z) != "" {
		return false
	}
	for _, f := range z.File {
		if f.Name == "[Content_Types].xml" {
			return true
		}
	}
	return false
}

type ooxmlFormat struct{}

// app.xml properties that identify the author, organisation or software.
var appIdentifying = map[string]report.Category{
	"Application": report.CatSoftware, "AppVersion": report.CatSoftware,
	"Company": report.CatAuthor, "Manager": report.CatAuthor,
	"Template": report.CatCustom, "TotalTime": report.CatCustom, "HyperlinkBase": report.CatCustom,
}

func (ooxmlFormat) process(z *zipDoc) error {
	for _, e := range z.entries {
		if strings.HasPrefix(e.name, "_xmlsignatures/") {
			z.rep.Warn("document is digitally signed; stripping invalidates the signature")
			break
		}
	}
	for _, e := range z.entries {
		n := e.name
		ln := strings.ToLower(n)
		switch {
		case n == "[Content_Types].xml" || strings.HasSuffix(ln, ".rels"):
			// handled after drops are known
		case n == "docProps/core.xml":
			if err := ooxmlCore(z, e); err != nil {
				return err
			}
		case n == "docProps/app.xml":
			if err := ooxmlApp(z, e); err != nil {
				return err
			}
		case n == "docProps/custom.xml":
			ooxmlCustom(z, e)
		case strings.HasPrefix(ln, "docprops/thumbnail"):
			f := report.Binary(report.CatThumbnail, "ooxml", n, int64(len(e.data)))
			if z.opts.KeepThumbnails {
				z.rep.Keep(f)
				z.stripMember(e)
			} else {
				z.dropMember(e, f)
			}
		case strings.HasSuffix(ln, ".xml") && ooxmlScrubbable(ln):
			if err := scrubOfficeXML(z, e); err != nil {
				return fmt.Errorf("ooxml: %s: %w", n, err)
			}
		case isMediaName(n):
			z.stripMember(e)
		}
	}
	return ooxmlPrune(z)
}

// ooxmlScrubbable limits the generic author/rsid scrub to parts that can
// carry them, so large worksheets and slides are not re-serialised.
func ooxmlScrubbable(ln string) bool {
	switch {
	case strings.HasPrefix(ln, "word/") && !strings.HasPrefix(ln, "word/media/"):
		return true
	case strings.HasPrefix(ln, "xl/comments"), strings.HasPrefix(ln, "xl/threadedcomments/"),
		strings.HasPrefix(ln, "xl/persons/"), strings.HasPrefix(ln, "xl/revisions/"), ln == "xl/workbook.xml":
		return true
	case ln == "ppt/commentauthors.xml", ln == "ppt/authors.xml", strings.HasPrefix(ln, "ppt/comments/"):
		return true
	}
	return false
}

func ooxmlCore(z *zipDoc, e *zipEntry) error {
	doc, err := parseXML(e.data)
	if err != nil {
		return fmt.Errorf("ooxml: core.xml: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return errNoRoot
	}
	changed := false
	for _, c := range root.ChildElements() {
		v := strings.TrimSpace(c.Text())
		switch {
		case c.Tag == "language":
			continue
		case c.Tag == "title" && z.opts.KeepTags:
			z.rep.Keep(report.Text(report.CatIDTag, "ooxml.core", c.FullTag(), v))
			continue
		}
		cat := classify.DocProp(c.Tag)
		if c.Tag == "keywords" || c.Tag == "subject" || c.Tag == "description" || c.Tag == "category" || c.Tag == "revision" || c.Tag == "contentStatus" || c.Tag == "identifier" || c.Tag == "version" {
			cat = report.CatCustom
		}
		z.rep.Add(report.Text(cat, "ooxml.core", c.FullTag(), v))
		root.RemoveChild(c)
		changed = true
	}
	if changed {
		out, err := writeXML(doc)
		if err != nil {
			return err
		}
		e.out = out
	}
	return nil
}

func ooxmlApp(z *zipDoc, e *zipEntry) error {
	doc, err := parseXML(e.data)
	if err != nil {
		return fmt.Errorf("ooxml: app.xml: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return errNoRoot
	}
	changed := false
	for _, c := range root.ChildElements() {
		cat, ok := appIdentifying[c.Tag]
		if !ok {
			continue
		}
		z.rep.Add(report.Text(cat, "ooxml.app", c.Tag, strings.TrimSpace(c.Text())))
		root.RemoveChild(c)
		changed = true
	}
	if changed {
		out, err := writeXML(doc)
		if err != nil {
			return err
		}
		e.out = out
	}
	return nil
}

func ooxmlCustom(z *zipDoc, e *zipEntry) {
	e.drop = true
	doc, err := parseXML(e.data)
	if err != nil || doc.Root() == nil {
		z.rep.Add(report.Binary(report.CatCustom, "ooxml.custom", e.name, int64(len(e.data))))
		return
	}
	n := 0
	for _, p := range doc.Root().ChildElements() {
		val := ""
		for _, v := range p.ChildElements() {
			val = strings.TrimSpace(v.Text())
		}
		z.rep.Add(report.Text(report.CatCustom, "ooxml.custom", p.SelectAttrValue("name", "property"), val))
		n++
	}
	if n == 0 {
		z.rep.Add(report.Binary(report.CatCustom, "ooxml.custom", e.name, int64(len(e.data))))
	}
}

// Elements removed wherever they appear in scrubbed parts.
var officeDropElems = map[string]report.Category{
	"rsids":            report.CatCustom, // w:rsids session list (settings.xml)
	"docVars":          report.CatCustom, // w:docVars macro variables
	"attachedTemplate": report.CatCustom, // w:attachedTemplate (path in rels)
	"absPath":          report.CatCustom, // x15ac:absPath save location
	"revisionPtr":      report.CatCustom, // xr:revisionPtr document/revision IDs
}

// scrubOfficeXML anonymises authors and dates and removes session IDs in a
// WordprocessingML/SpreadsheetML/PresentationML part.
func scrubOfficeXML(z *zipDoc, e *zipEntry) error {
	doc, err := parseXML(e.data)
	if err != nil {
		return err
	}
	root := doc.Root()
	if root == nil {
		return nil
	}
	changed := false
	authors := map[string]bool{}
	dates := map[string]bool{}
	rsids := 0
	var walk func(el *etree.Element)
	walk = func(el *etree.Element) {
		var attrs []etree.Attr
		for _, a := range el.Attr {
			local := a.Key
			switch {
			case strings.HasPrefix(local, "rsid") && a.Space != "xmlns":
				rsids++
				changed = true
				continue
			case local == "author" || local == "userName" ||
				(local == "name" && (el.Tag == "cmAuthor" || el.Tag == "author" || el.Tag == "userInfo")) ||
				(local == "displayName" && el.Tag == "person"):
				if a.Value != anonAuthor && a.Value != "" {
					authors[a.Value] = true
					a.Value = anonAuthor
					changed = true
				}
			case local == "initials":
				if a.Value != anonInitials && a.Value != "" {
					a.Value = anonInitials
					changed = true
				}
			case (local == "userId" || local == "providerId") && (el.Tag == "author" || el.Tag == "person" || el.Tag == "presenceInfo"):
				if a.Value != "" && a.Value != "None" {
					authors[a.Value] = true
					if local == "providerId" {
						a.Value = "None"
					} else {
						a.Value = ""
					}
					changed = true
				}
			case local == "date" || local == "dt" || local == "dateTime" || (local == "created" && el.Tag == "cm") ||
				(local == "dT" && el.Tag == "threadedComment"):
				if a.Value != anonDate {
					dates[a.Value] = true
					a.Value = anonDate
					changed = true
				}
			case el.Tag == "fileVersion" && (local == "lastEdited" || local == "lowestEdited" || local == "rupBuild"):
				z.rep.Add(report.Text(report.CatSoftware, "ooxml", "fileVersion "+local, a.Value))
				changed = true
				continue
			}
			attrs = append(attrs, a)
		}
		el.Attr = attrs
		// Excel legacy comments: <authors><author>Name</author></authors>
		if el.Tag == "author" && el.Parent() != nil && el.Parent().Tag == "authors" {
			if t := el.Text(); t != anonAuthor && t != "" {
				authors[t] = true
				el.SetText(anonAuthor)
				changed = true
			}
		}
		for _, c := range el.ChildElements() {
			if cat, ok := officeDropElems[c.Tag]; ok {
				val := c.SelectAttrValue("url", c.SelectAttrValue("documentId", ""))
				if c.Tag == "rsids" {
					val = fmt.Sprintf("%d session IDs", len(c.ChildElements()))
				}
				z.rep.Add(report.Text(cat, "ooxml", c.FullTag(), val))
				removeNode(&el.Child, c)
				changed = true
				continue
			}
			walk(c)
		}
	}
	walk(root)
	for _, a := range sortedKeys2(authors) {
		z.rep.Add(report.Text(report.CatAuthor, "ooxml", e.name+" author", a))
	}
	for _, d := range sortedKeys2(dates) {
		z.rep.Add(report.Text(report.CatTimestamp, "ooxml", e.name+" date", d))
	}
	if rsids > 0 {
		z.rep.Add(report.Text(report.CatCustom, "ooxml", e.name+" rsid attributes", fmt.Sprint(rsids)))
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

// ooxmlPrune drops relationships and content-type overrides that point at
// removed parts, plus external attached-template relationships.
func ooxmlPrune(z *zipDoc) error {
	dropped := z.dropped()
	for _, e := range z.entries {
		if e.drop {
			continue
		}
		ln := strings.ToLower(e.name)
		switch {
		case strings.HasSuffix(ln, ".rels"):
			doc, err := parseXML(e.data)
			if err != nil || doc.Root() == nil {
				continue
			}
			src := relsSource(e.name)
			changed := false
			for _, rel := range doc.Root().ChildElements() {
				target := rel.SelectAttrValue("Target", "")
				typ := rel.SelectAttrValue("Type", "")
				external := rel.SelectAttrValue("TargetMode", "") == "External"
				switch {
				case external && strings.HasSuffix(typ, "/attachedTemplate"):
					z.rep.Add(report.Text(report.CatCustom, "ooxml", "attachedTemplate path", target))
				case !external && dropped[resolveTarget(src, target)]:
				default:
					continue
				}
				removeNode(&doc.Root().Child, rel)
				changed = true
			}
			if changed {
				out, err := writeXML(doc)
				if err != nil {
					return err
				}
				e.out = out
			}
		case e.name == "[Content_Types].xml":
			doc, err := parseXML(e.data)
			if err != nil || doc.Root() == nil {
				continue
			}
			changed := false
			for _, o := range doc.Root().SelectElements("Override") {
				part := strings.TrimPrefix(o.SelectAttrValue("PartName", ""), "/")
				if dropped[part] {
					removeNode(&doc.Root().Child, o)
					changed = true
				}
			}
			if changed {
				out, err := writeXML(doc)
				if err != nil {
					return err
				}
				e.out = out
			}
		}
	}
	return nil
}
