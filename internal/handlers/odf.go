package handlers

import (
	"archive/zip"
	"fmt"
	"io"
	"strings"

	"github.com/beevik/etree"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/report"
)

// odfHandler cleans OpenDocument files (.odt, .ods, .odp, .odg, ...):
//   - meta.xml: everything except language and statistics (title with -k)
//   - settings.xml: printer name/setup, data sources and rsid items
//   - content.xml/styles.xml: tracked-change and annotation authors →
//     "Author", dates → 1980-01-01, initials → "A"; officeooo rsid
//     attributes removed
//   - thumbnails (kept with -t) and digital signatures removed, manifest
//     updated; embedded pictures cleaned recursively.
type odfHandler struct{}

func init() { Register(odfHandler{}) }

func (odfHandler) Name() string { return "odf" }

func (odfHandler) CanHandle(r io.ReadSeeker, p string) bool {
	if zipHasMember(r, func(z *zip.Reader) bool {
		return strings.HasPrefix(zipMimetype(z), "application/vnd.oasis.opendocument")
	}) {
		return true
	}
	return hasExt(p, ".odt", ".ods", ".odp", ".odg", ".odf", ".ott", ".ots", ".otp", ".otg", ".odm")
}

func (odfHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	return zipInspect(r, "odf", odfFormat{}, opts)
}

func (odfHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	return zipStrip(r, w, "odf", odfFormat{}, opts)
}

type odfFormat struct{}

var odfSettingsDrop = map[string]report.Category{
	"PrinterName": report.CatDevice, "PrinterSetup": report.CatDevice, "PrinterPaperFromSetup": report.CatDevice,
	"CurrentDatabaseDataSource": report.CatCustom, "CurrentDatabaseCommand": report.CatCustom,
	"CurrentDatabaseCommandType": report.CatCustom, "Rsid": report.CatCustom, "RsidRoot": report.CatCustom,
}

func (odfFormat) process(z *zipDoc) error {
	for _, e := range z.entries {
		n := e.name
		switch {
		case n == "mimetype", n == "META-INF/manifest.xml":
		case n == "meta.xml":
			if err := odfMeta(z, e); err != nil {
				return err
			}
		case n == "settings.xml":
			if err := odfSettings(z, e); err != nil {
				return err
			}
		case n == "content.xml" || n == "styles.xml":
			if err := odfContent(z, e); err != nil {
				return fmt.Errorf("odf: %s: %w", n, err)
			}
		case strings.HasPrefix(n, "Thumbnails/"):
			f := report.Binary(report.CatThumbnail, "odf", n, int64(len(e.data)))
			if z.opts.KeepThumbnails {
				z.rep.Keep(f)
				z.stripMember(e)
			} else {
				z.dropMember(e, f)
			}
		case strings.HasPrefix(n, "META-INF/") && strings.Contains(strings.ToLower(n), "signatures"):
			z.dropMember(e, report.Binary(report.CatAuthor, "odf", n+" (digital signature)", int64(len(e.data))))
			z.rep.Warn("digital signatures removed; the document is no longer signed")
		case isMediaName(n):
			z.stripMember(e)
		}
	}
	return odfManifest(z)
}

func odfMeta(z *zipDoc, e *zipEntry) error {
	doc, err := parseXML(e.data)
	if err != nil {
		return fmt.Errorf("odf: meta.xml: %w", err)
	}
	var meta *etree.Element
	if root := doc.Root(); root != nil {
		meta = root.SelectElement("meta")
	}
	if meta == nil {
		return nil
	}
	changed := false
	for _, c := range meta.ChildElements() {
		v := strings.TrimSpace(c.Text())
		switch c.Tag {
		case "language", "document-statistic":
			continue
		case "title":
			if z.opts.KeepTags {
				z.rep.Keep(report.Text(report.CatIDTag, "odf.meta", c.FullTag(), v))
				continue
			}
		case "template":
			v = c.SelectAttrValue("xlink:href", v)
		case "user-defined":
			z.rep.Add(report.Text(report.CatCustom, "odf.meta", "user-defined:"+c.SelectAttrValue("meta:name", ""), v))
			removeNode(&meta.Child, c)
			changed = true
			continue
		}
		cat := classify.DocProp(c.Tag)
		switch c.Tag {
		case "editing-cycles", "editing-duration", "keyword", "subject", "description", "hyperlink-behaviour", "auto-reload":
			cat = report.CatCustom
		}
		z.rep.Add(report.Text(cat, "odf.meta", c.FullTag(), v))
		removeNode(&meta.Child, c)
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

func odfSettings(z *zipDoc, e *zipEntry) error {
	doc, err := parseXML(e.data)
	if err != nil {
		return fmt.Errorf("odf: settings.xml: %w", err)
	}
	changed := false
	var walk func(el *etree.Element)
	walk = func(el *etree.Element) {
		for _, c := range el.ChildElements() {
			if c.Tag == "config-item" {
				name := c.SelectAttrValue("config:name", "")
				if cat, ok := odfSettingsDrop[name]; ok {
					v := strings.TrimSpace(c.Text())
					if v != "" {
						if name == "PrinterSetup" {
							z.rep.Add(report.Binary(cat, "odf.settings", name, int64(len(v))))
						} else {
							z.rep.Add(report.Text(cat, "odf.settings", name, v))
						}
						removeNode(&el.Child, c)
						changed = true
					}
					continue
				}
			}
			walk(c)
		}
	}
	if doc.Root() != nil {
		walk(doc.Root())
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

// odfContent anonymises change-tracking and annotation metadata.
func odfContent(z *zipDoc, e *zipEntry) error {
	doc, err := parseXML(e.data)
	if err != nil {
		return err
	}
	changed := false
	authors, dates := map[string]bool{}, map[string]bool{}
	rsids := 0
	var walk func(el *etree.Element, inInfo bool)
	walk = func(el *etree.Element, inInfo bool) {
		var attrs []etree.Attr
		for _, a := range el.Attr {
			if strings.Contains(a.Key, "rsid") && a.Space != "xmlns" {
				rsids++
				changed = true
				continue
			}
			attrs = append(attrs, a)
		}
		el.Attr = attrs
		info := inInfo || el.Tag == "change-info" || el.Tag == "annotation"
		for _, c := range el.ChildElements() {
			if info {
				v := strings.TrimSpace(c.Text())
				switch c.Tag {
				case "creator":
					if v != anonAuthor && v != "" {
						authors[v] = true
						c.SetText(anonAuthor)
						changed = true
					}
					continue
				case "date":
					if v != anonDate && v != "" {
						dates[v] = true
						c.SetText(anonDate)
						changed = true
					}
					continue
				case "creator-initials":
					if v != anonInitials && v != "" {
						c.SetText(anonInitials)
						changed = true
					}
					continue
				}
			}
			walk(c, info && c.Tag != "p" && c.Tag != "list")
		}
	}
	if doc.Root() != nil {
		walk(doc.Root(), false)
	}
	for _, a := range sortedKeys2(authors) {
		z.rep.Add(report.Text(report.CatAuthor, "odf", e.name+" author", a))
	}
	for _, d := range sortedKeys2(dates) {
		z.rep.Add(report.Text(report.CatTimestamp, "odf", e.name+" date", d))
	}
	if rsids > 0 {
		z.rep.Add(report.Text(report.CatCustom, "odf", e.name+" rsid attributes", fmt.Sprint(rsids)))
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

// odfManifest removes manifest entries for dropped members.
func odfManifest(z *zipDoc) error {
	m := z.byName["META-INF/manifest.xml"]
	dropped := z.dropped()
	if m == nil || len(dropped) == 0 {
		return nil
	}
	doc, err := parseXML(m.data)
	if err != nil || doc.Root() == nil {
		return nil
	}
	changed := false
	for _, fe := range doc.Root().ChildElements() {
		p := fe.SelectAttrValue("manifest:full-path", "")
		if dropped[p] || (strings.HasSuffix(p, "/") && allDroppedUnder(z, p)) {
			removeNode(&doc.Root().Child, fe)
			changed = true
		}
	}
	if changed {
		out, err := writeXML(doc)
		if err != nil {
			return err
		}
		m.out = out
	}
	return nil
}

func allDroppedUnder(z *zipDoc, dir string) bool {
	any := false
	for _, e := range z.entries {
		if strings.HasPrefix(e.name, dir) {
			any = true
			if !e.drop {
				return false
			}
		}
	}
	return any
}
