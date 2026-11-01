package handlers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/beevik/etree"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/report"
)

// svgHandler removes <metadata>, <title>, <desc> (title kept with -k),
// comments, processing instructions, and every element and attribute in an
// editor namespace (Inkscape, Sodipodi, Adobe Illustrator, Sketch, Serif,
// ...), plus RDF/Dublin Core/Creative Commons declarations. Raster images
// embedded as data: URIs are cleaned recursively.
type svgHandler struct{}

func init() { Register(svgHandler{}) }

func (svgHandler) Name() string { return "svg" }

func (svgHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := bytes.TrimLeft(peek(r, 4096), "\xef\xbb\xbf \t\r\n")
	if (bytes.HasPrefix(b, []byte("<?xml")) || bytes.HasPrefix(b, []byte("<svg")) || bytes.HasPrefix(b, []byte("<!DOCTYPE svg")) || bytes.HasPrefix(b, []byte("<!--"))) &&
		bytes.Contains(b, []byte("<svg")) {
		return true
	}
	return hasExt(path, ".svg")
}

var svgEditorNS = []string{
	"http://www.inkscape.org/namespaces/inkscape",
	"http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd",
	"http://ns.adobe.com/",
	"http://www.bohemiancoding.com/sketch/ns",
	"http://www.serif.com/",
	"https://boxy-svg.com",
	"http://vectornator.io",
	"http://www.vectornator.io",
	"http://www.figma.com",
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#",
	"http://purl.org/dc/elements/1.1/",
	"http://creativecommons.org/ns#",
	"http://web.resource.org/cc/",
}

func isEditorNS(uri string) bool {
	if uri == "" {
		return false
	}
	for _, ns := range svgEditorNS {
		if strings.HasPrefix(uri, ns) {
			return true
		}
	}
	return false
}

func (h svgHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	_, rep, err := processSVG(data, opts)
	return rep, err
}

func (h svgHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	out, rep, err := processSVG(data, opts)
	if err != nil {
		return rep, err
	}
	_, err = w.Write(out)
	return rep, err
}

func processSVG(data []byte, opts Options) ([]byte, report.Report, error) {
	rep := report.New("svg")
	doc := etree.NewDocument()
	doc.ReadSettings.PreserveCData = true
	if err := doc.ReadFromBytes(data); err != nil {
		return nil, rep, fmt.Errorf("svg: %w", err)
	}
	root := doc.Root()
	if root == nil || root.Tag != "svg" {
		return nil, rep, fmt.Errorf("svg: root element is not <svg>")
	}
	changed := false
	// Top-level comments and processing instructions (keep the XML declaration).
	for _, tok := range append([]etree.Token(nil), doc.Child...) {
		switch t := tok.(type) {
		case *etree.Comment:
			rep.Add(svgComment(t.Data))
			removeNode(&doc.Child, t)
			changed = true
		case *etree.ProcInst:
			if t.Target != "xml" {
				rep.Add(report.Text(report.CatCustom, "svg", "<?"+t.Target+"?>", t.Inst))
				removeNode(&doc.Child, t)
				changed = true
			}
		}
	}
	keptTitle := false
	var walk func(e *etree.Element, depth int)
	walk = func(e *etree.Element, depth int) {
		// Attributes in editor namespaces. Namespace declarations are removed
		// only after the subtree is processed, so prefixes still resolve.
		var attrs []etree.Attr
		var decls []etree.Attr
		for _, a := range e.Attr {
			switch {
			case a.Space == "xmlns" || a.Space == "" && a.Key == "xmlns":
				decls = append(decls, a)
			case a.Space != "" && isEditorNS(a.NamespaceURI()):
				rep.Add(report.Text(svgAttrCategory(a.Key), "svg", a.Space+":"+a.Key, a.Value))
				changed = true
			default:
				if (a.Key == "href" && (a.Space == "" || a.Space == "xlink")) && strings.HasPrefix(a.Value, "data:") {
					if nv, ok := svgDataURI(a.Value, opts, &rep); ok {
						a.Value = nv
						changed = true
					}
				}
				attrs = append(attrs, a)
			}
		}
		defer func() {
			kept := attrs
			for _, a := range decls {
				if isEditorNS(a.Value) {
					rep.Add(report.Text(report.CatSoftware, "svg", "xmlns:"+a.Key, a.Value))
					changed = true
					continue
				}
				kept = append(kept, a)
			}
			e.Attr = reorderDecls(kept)
		}()

		for _, tok := range append([]etree.Token(nil), e.Child...) {
			switch t := tok.(type) {
			case *etree.Comment:
				rep.Add(svgComment(t.Data))
				removeNode(&e.Child, t)
				changed = true
			case *etree.ProcInst:
				rep.Add(report.Text(report.CatCustom, "svg", "<?"+t.Target+"?>", t.Inst))
				removeNode(&e.Child, t)
				changed = true
			case *etree.Element:
				switch {
				case t.Tag == "metadata" && !isEditorNS(t.NamespaceURI()):
					rep.Add(svgMetadataFields(t)...)
					removeNode(&e.Child, t)
					changed = true
				case isEditorNS(t.NamespaceURI()):
					rep.Add(report.Text(report.CatSoftware, "svg", t.Space+":"+t.Tag, svgSummary(t)))
					removeNode(&e.Child, t)
					changed = true
				case t.Tag == "title" && depth == 0 && opts.KeepTags && !keptTitle:
					keptTitle = true
					rep.Keep(report.Text(report.CatIDTag, "svg", "title", strings.TrimSpace(t.Text())))
				case t.Tag == "title" || t.Tag == "desc":
					cat := report.CatCustom
					if t.Tag == "title" {
						cat = report.CatIDTag
					}
					rep.Add(report.Text(cat, "svg", t.Tag, strings.TrimSpace(t.Text())))
					removeNode(&e.Child, t)
					changed = true
				default:
					walk(t, depth+1)
				}
			}
		}
	}
	walk(root, 0)
	if !changed {
		return data, rep, nil
	}
	doc.WriteSettings.CanonicalEndTags = false
	out, err := doc.WriteToBytes()
	if err != nil {
		return nil, rep, err
	}
	rep.Bytes = int64(len(data) - len(out))
	if rep.Bytes < 0 {
		rep.Bytes = 0
	}
	return out, rep, nil
}

func svgComment(s string) report.Field {
	s = strings.TrimSpace(s)
	l := strings.ToLower(s)
	cat := report.CatCustom
	for _, k := range []string{"generator", "created with", "inkscape", "illustrator", "sketch", "figma", "affinity", "corel"} {
		if strings.Contains(l, k) {
			cat = report.CatSoftware
		}
	}
	return report.Text(cat, "svg", "comment", s)
}

func svgAttrCategory(key string) report.Category {
	switch strings.ToLower(key) {
	case "version":
		return report.CatSoftware
	case "docname", "docbase", "export-filename", "export-xdpi", "export-ydpi":
		return report.CatCustom
	}
	return report.CatSoftware
}

func svgSummary(e *etree.Element) string {
	d := etree.NewDocument()
	d.SetRoot(e.Copy())
	s, _ := d.WriteToString()
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// svgMetadataFields reports the text leaves inside <metadata> (RDF, Dublin
// Core, Creative Commons). Each leaf is classified by the first element on
// its path with a known category (e.g. dc:creator → author).
func svgMetadataFields(m *etree.Element) []report.Field {
	var out []report.Field
	var walk func(e *etree.Element, path []*etree.Element)
	walk = func(e *etree.Element, path []*etree.Element) {
		path = append(path, e)
		kids := e.ChildElements()
		if len(kids) == 0 {
			v := strings.TrimSpace(e.Text())
			if v == "" {
				v = e.SelectAttrValue("rdf:resource", "")
			}
			if v == "" {
				return
			}
			cat := report.CatCustom
			names := make([]string, 0, len(path))
			for _, p := range path[1:] {
				names = append(names, p.FullTag())
				if c := classify.XMP(p.Space, p.Tag); cat == report.CatCustom && c != report.CatCustom {
					cat = c
				}
			}
			out = append(out, report.Text(cat, "svg.metadata", strings.Join(names, "/"), v))
			return
		}
		for _, k := range kids {
			walk(k, path)
		}
	}
	walk(m, nil)
	if len(out) == 0 {
		d := etree.NewDocument()
		d.SetRoot(m.Copy())
		b, _ := d.WriteToBytes()
		out = append(out, report.Binary(report.CatCustom, "svg", "metadata", int64(len(b))))
	}
	return out
}

// reorderDecls puts xmlns declarations before other attributes, matching
// the usual layout of SVG files.
func reorderDecls(attrs []etree.Attr) []etree.Attr {
	var decls, rest []etree.Attr
	for _, a := range attrs {
		if a.Space == "xmlns" || a.Space == "" && a.Key == "xmlns" {
			decls = append(decls, a)
		} else {
			rest = append(rest, a)
		}
	}
	return append(decls, rest...)
}

// svgDataURI cleans a base64 data: URI image.
func svgDataURI(uri string, opts Options, rep *report.Report) (string, bool) {
	comma := strings.IndexByte(uri, ',')
	if comma < 0 || !strings.HasSuffix(uri[:comma], ";base64") {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(uri[comma+1:]), ""))
	if err != nil {
		return "", false
	}
	out, sub, ok := StripEmbedded("data: URI image", raw, opts)
	if !ok {
		return "", false
	}
	if sub.TotalFields() > 0 || !sub.FullyClean {
		rep.AddEmbedded(sub)
	}
	if out == nil || bytes.Equal(out, raw) {
		return "", false
	}
	return uri[:comma+1] + base64.StdEncoding.EncodeToString(out), true
}

// removeNode removes tok from children together with the whitespace-only
// text immediately before it, so no blank lines are left behind.
func removeNode(children *[]etree.Token, tok etree.Token) {
	c := *children
	for i, t := range c {
		if t != tok {
			continue
		}
		start := i
		if i > 0 {
			if cd, ok := c[i-1].(*etree.CharData); ok && cd.IsWhitespace() {
				start = i - 1
			}
		}
		*children = append(c[:start], c[i+1:]...)
		return
	}
}
