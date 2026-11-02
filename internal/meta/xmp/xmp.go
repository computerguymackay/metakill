// Package xmp summarises XMP packets as report fields.
package xmp

import (
	"bytes"
	"strings"

	"github.com/beevik/etree"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/report"
)

const rdfNS = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"

// Fields lists every property in an XMP packet. If the packet cannot be
// parsed it is reported as one binary field so it is never silently ignored.
func Fields(data []byte) []report.Field {
	data = trimPacket(data)
	doc := etree.NewDocument()
	doc.ReadSettings.Permissive = true
	if err := doc.ReadFromBytes(data); err != nil {
		return []report.Field{report.Binary(report.CatCustom, "xmp", "XMPPacket", int64(len(data)))}
	}
	var out []report.Field
	for _, desc := range findAll(doc.Root(), "Description") {
		for _, a := range desc.Attr {
			if a.Space == "xmlns" || a.Key == "xmlns" || a.Space == "rdf" || a.Space == "xml" {
				continue
			}
			out = append(out, field(a.Space, a.Key, a.Value))
		}
		for _, child := range desc.ChildElements() {
			flatten(child, "", &out)
		}
	}
	if len(out) == 0 && len(bytes.TrimSpace(data)) > 0 {
		// A packet with no properties still identifies the toolchain that
		// wrote it (x:xmptk), so report it.
		tk := ""
		if root := doc.Root(); root != nil {
			tk = root.SelectAttrValue("x:xmptk", "")
		}
		if tk != "" {
			out = append(out, report.Text(report.CatSoftware, "xmp", "x:xmptk", tk))
		} else {
			out = append(out, report.Binary(report.CatCustom, "xmp", "XMPPacket", int64(len(data))))
		}
	}
	return out
}

func flatten(e *etree.Element, parent string, out *[]report.Field) {
	name := e.Space + ":" + e.Tag
	if parent != "" {
		name = parent + "/" + e.Tag
	}
	if e.NamespaceURI() == rdfNS && e.Tag == "Description" {
		for _, c := range e.ChildElements() {
			flatten(c, parent, out)
		}
		return
	}
	kids := e.ChildElements()
	if len(kids) == 0 {
		v := strings.TrimSpace(e.Text())
		if v == "" {
			v = e.SelectAttrValue("rdf:resource", "")
		}
		*out = append(*out, fieldNamed(e.Space, e.Tag, name, v))
		return
	}
	// rdf:Seq / rdf:Bag / rdf:Alt of simple values → one joined field.
	if len(kids) == 1 && kids[0].Space == "rdf" && (kids[0].Tag == "Seq" || kids[0].Tag == "Bag" || kids[0].Tag == "Alt") {
		var vals []string
		structured := false
		for _, li := range kids[0].ChildElements() {
			if len(li.ChildElements()) > 0 || len(li.Attr) > 1 {
				structured = true
				break
			}
			if t := strings.TrimSpace(li.Text()); t != "" {
				vals = append(vals, t)
			}
		}
		if !structured {
			*out = append(*out, fieldNamed(e.Space, e.Tag, name, strings.Join(vals, "; ")))
			return
		}
		for _, li := range kids[0].ChildElements() {
			for _, a := range li.Attr {
				if a.Space != "rdf" && a.Space != "xmlns" {
					*out = append(*out, fieldNamed(a.Space, a.Key, name+"/"+a.Space+":"+a.Key, a.Value))
				}
			}
			for _, c := range li.ChildElements() {
				flatten(c, name, out)
			}
		}
		return
	}
	for _, c := range kids {
		flatten(c, name, out)
	}
}

func field(prefix, local, value string) report.Field {
	return fieldNamed(prefix, local, prefix+":"+local, value)
}

func fieldNamed(prefix, local, name, value string) report.Field {
	return report.Text(classify.XMP(prefix, local), "xmp", name, value)
}

func findAll(root *etree.Element, tag string) []*etree.Element {
	if root == nil {
		return nil
	}
	var out []*etree.Element
	var walk func(e *etree.Element)
	walk = func(e *etree.Element) {
		if e.Tag == tag && e.NamespaceURI() == rdfNS {
			out = append(out, e)
			return // nested Descriptions are flattened by their parent
		}
		for _, c := range e.ChildElements() {
			walk(c)
		}
	}
	walk(root)
	return out
}

// trimPacket drops anything after the closing xpacket PI and leading BOMs or
// NULs that some writers include.
func trimPacket(b []byte) []byte {
	b = bytes.TrimLeft(b, "\x00\xef\xbb\xbf \r\n\t")
	if i := bytes.Index(b, []byte("<?xpacket end")); i >= 0 {
		if j := bytes.Index(b[i:], []byte("?>")); j >= 0 {
			return b[:i+j+2]
		}
	}
	return bytes.TrimRight(b, "\x00 \r\n\t")
}
