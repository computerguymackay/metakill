package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/meta/memio"
	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/internal/report"
)

// pdfHandler rewrites the PDF with pdfcpu, which drops incremental-update
// history and unreferenced objects. It removes the /Info dictionary (keeping
// /Title with -k), every /Metadata (XMP) stream, /PieceInfo, /LastModified,
// annotation authors and dates, and EXIF inside embedded JPEG images. The
// trailer /ID is regenerated.
//
// pdfcpu always stamps Producer/CreationDate/ModDate into /Info when it
// writes; we blank those entries afterwards by overwriting them with spaces,
// which leaves every byte offset (and so the xref table) valid.
type pdfHandler struct{}

func init() {
	api.DisableConfigDir() // never read or create ~/.config/pdfcpu
	Register(pdfHandler{})
}

func (pdfHandler) Name() string { return "pdf" }

func (pdfHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 1024)
	return bytes.Contains(b, []byte("%PDF-")) || hasExt(path, ".pdf")
}

func pdfConfig() *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.WriteObjectStream = false
	conf.WriteXRefStream = false
	conf.ValidationMode = model.ValidationRelaxed
	conf.Offline = true // pdfcpu must never fetch CRLs, OCSP or linked resources
	return conf
}

func readPDF(r io.ReadSeeker) (*model.Context, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ctx, err := api.ReadContext(r, pdfConfig())
	if err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	if ctx.Encrypt != nil {
		return nil, fmt.Errorf("pdf: encrypted documents are not supported: %w", ErrUnsupported)
	}
	return ctx, nil
}

func (h pdfHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	ctx, err := readPDF(r)
	if err != nil {
		return report.Report{}, err
	}
	rep := report.New("pdf")
	raw, err := readAll(r)
	if err != nil {
		return rep, err
	}
	scrubPDF(ctx, raw, opts, &rep, false)
	if rep.TotalFields() > 0 {
		// Estimate savings by doing the rewrite in memory.
		var buf memio.Buffer
		if _, err := h.Strip(bytes.NewReader(raw), &buf, opts); err == nil {
			if d := int64(len(raw) - buf.Len()); d > 0 {
				rep.Bytes = d
			}
		}
	}
	return rep, nil
}

func (h pdfHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	ctx, err := readPDF(r)
	if err != nil {
		return report.Report{}, err
	}
	rep := report.New("pdf")
	raw, err := readAll(r)
	if err != nil {
		return rep, err
	}
	scrubPDF(ctx, raw, opts, &rep, true)
	ctx.ID = nil // regenerate both halves of the file identifier

	var buf bytes.Buffer
	if err := api.WriteContext(ctx, &buf); err != nil {
		return rep, fmt.Errorf("pdf: write: %w", err)
	}
	out := buf.Bytes()
	if ctx.Info != nil {
		if err := blankInfoStamps(out, ctx.Info.ObjectNumber.Value()); err != nil {
			return rep, err
		}
	}
	_, err = w.Write(out)
	return rep, err
}

// pdfStamps are the entries pdfcpu adds to /Info on every write.
var pdfStamps = []string{"Producer", "CreationDate", "ModDate"}

// scrubPDF reports (and with mutate, removes) metadata in every object.
func scrubPDF(ctx *model.Context, raw []byte, opts Options, rep *report.Report, mutate bool) {
	if n := bytes.Count(raw, []byte("%%EOF")); n > 1 {
		rep.Add(report.Text(report.CatCustom, "pdf", "PreviousRevisions", strconv.Itoa(n-1)))
	}
	if len(ctx.ID) > 0 {
		rep.Keep(report.Text(report.CatCustom, "pdf", "DocumentID", "regenerated on write"))
	}

	// Document information dictionary.
	if ctx.Info != nil {
		if d, err := ctx.DereferenceDict(*ctx.Info); err == nil && d != nil {
			kept := types.NewDict()
			for _, k := range sortedDictKeys(d) {
				val := pdfText(ctx, d[k])
				if val == "" {
					continue
				}
				cat := classify.DocProp(k)
				f := report.Text(cat, "pdf.info", k, val)
				if k == "Title" && opts.KeepTags {
					rep.Keep(f)
					kept[k] = d[k]
					continue
				}
				rep.Add(f)
			}
			if mutate {
				for k := range d {
					if _, ok := kept[k]; !ok {
						delete(d, k)
					}
				}
			}
		}
	}

	attachments := 0
	signed := false
	for _, nr := range sortedObjNrs(ctx) {
		e := ctx.Table[nr]
		if e == nil || e.Free || e.Object == nil {
			continue
		}
		switch o := e.Object.(type) {
		case types.StreamDict:
			if t := o.Type(); t != nil && *t == "Metadata" || o.Subtype() != nil && *o.Subtype() == "XML" {
				if err := o.Decode(); err == nil {
					rep.Add(xmp.Fields(o.Content)...)
				} else {
					rep.Add(report.Binary(report.CatCustom, "xmp", fmt.Sprintf("obj %d", nr), int64(len(o.Raw))))
				}
				continue
			}
			if t := o.Type(); t != nil && *t == "EmbeddedFile" {
				attachments++
			}
			scrubPDFDict(ctx, o.Dict, nr, rep, mutate)
			if o.Subtype() != nil && *o.Subtype() == "Image" && len(o.FilterPipeline) == 1 && o.FilterPipeline[0].Name == "DCTDecode" {
				if out, sub, ok := StripEmbedded(fmt.Sprintf("obj %d (JPEG image)", nr), o.Raw, opts); ok {
					if sub.TotalFields() > 0 || !sub.FullyClean {
						rep.AddEmbedded(sub)
					}
					if mutate && out != nil && !bytes.Equal(out, o.Raw) {
						o.Raw = out
						l := int64(len(out))
						o.StreamLength = &l
						o.Update("Length", types.Integer(len(out)))
						e.Object = o
					}
				}
			}
		case types.Dict:
			if t := o.Type(); t != nil && *t == "Sig" {
				signed = true
			}
			scrubPDFDict(ctx, o, nr, rep, mutate)
		case types.Array:
			scrubPDFArray(ctx, o, nr, rep, mutate)
		}
	}
	if attachments > 0 {
		rep.Unclean(fmt.Sprintf("%d embedded file attachment(s): their dates were removed but their contents were not scrubbed", attachments))
	}
	if signed {
		rep.Warn("document is digitally signed; stripping invalidates the signature")
	}
}

// scrubPDFDict handles one dictionary and recurses into direct children.
func scrubPDFDict(ctx *model.Context, d types.Dict, nr int, rep *report.Report, mutate bool) {
	del := func(k string, f report.Field) {
		rep.Add(f)
		if mutate {
			delete(d, k)
		}
	}
	if _, ok := d["Metadata"]; ok && mutate {
		delete(d, "Metadata") // the stream itself is reported when visited
	}
	if v, ok := d["PieceInfo"]; ok {
		del("PieceInfo", report.Text(report.CatSoftware, "pdf", "PieceInfo", pieceInfoApps(ctx, v)))
	}
	if v, ok := d["LastModified"]; ok {
		del("LastModified", report.Text(report.CatTimestamp, "pdf", "LastModified", pdfText(ctx, v)))
	}
	if _, ok := d["SpiderInfo"]; ok {
		del("SpiderInfo", report.Text(report.CatSoftware, "pdf", "SpiderInfo", "web capture info"))
	}
	isAnnot := false
	if _, ok := d["Rect"]; ok {
		if st := d.Subtype(); st != nil {
			isAnnot = true
			if *st != "Widget" && *st != "Link" {
				if v, ok := d["T"]; ok {
					del("T", report.Text(report.CatAuthor, "pdf.annot", "T (author)", pdfText(ctx, v)))
				}
			}
		}
	}
	if isAnnot || d.Type() != nil && *d.Type() == "Annot" {
		for _, k := range []string{"M", "CreationDate"} {
			if v, ok := d[k]; ok {
				del(k, report.Text(report.CatTimestamp, "pdf.annot", k, pdfText(ctx, v)))
			}
		}
	}
	if p, ok := d["Params"].(types.Dict); ok {
		for _, k := range []string{"CreationDate", "ModDate"} {
			if v, ok := p[k]; ok {
				rep.Add(report.Text(report.CatTimestamp, "pdf.attachment", k, pdfText(ctx, v)))
				if mutate {
					delete(p, k)
				}
			}
		}
	}
	for _, k := range sortedDictKeys(d) {
		if k == "PieceInfo" {
			continue // removed wholesale; its contents are not reported twice
		}
		switch c := d[k].(type) {
		case types.Dict:
			scrubPDFDict(ctx, c, nr, rep, mutate)
		case types.Array:
			scrubPDFArray(ctx, c, nr, rep, mutate)
		}
	}
}

func scrubPDFArray(ctx *model.Context, a types.Array, nr int, rep *report.Report, mutate bool) {
	for _, v := range a {
		switch c := v.(type) {
		case types.Dict:
			scrubPDFDict(ctx, c, nr, rep, mutate)
		case types.Array:
			scrubPDFArray(ctx, c, nr, rep, mutate)
		}
	}
}

func pieceInfoApps(ctx *model.Context, v types.Object) string {
	o, err := ctx.Dereference(v)
	if err != nil {
		return ""
	}
	d, ok := o.(types.Dict)
	if !ok {
		return ""
	}
	keys := sortedDictKeys(d)
	var b bytes.Buffer
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k)
	}
	return b.String()
}

// pdfText renders a PDF object as text for reports.
func pdfText(ctx *model.Context, o types.Object) string {
	o, err := ctx.Dereference(o)
	if err != nil || o == nil {
		return ""
	}
	switch v := o.(type) {
	case types.StringLiteral, types.HexLiteral:
		if s, err := types.StringOrHexLiteral(v); err == nil && s != nil {
			return *s
		}
	case types.Name:
		return string(v)
	case types.Integer:
		return strconv.Itoa(int(v))
	case types.Boolean:
		return strconv.FormatBool(bool(v))
	}
	return o.String()
}

func sortedDictKeys(d types.Dict) []string {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedObjNrs(ctx *model.Context) []int {
	nrs := make([]int, 0, len(ctx.Table))
	for nr := range ctx.Table {
		nrs = append(nrs, nr)
	}
	sort.Ints(nrs)
	return nrs
}

// blankInfoStamps overwrites pdfcpu's Producer/CreationDate/ModDate entries
// in the written /Info object with spaces, preserving all offsets.
func blankInfoStamps(out []byte, objNr int) error {
	hdr := []byte(fmt.Sprintf("%d 0 obj", objNr))
	start := -1
	for i := 0; ; {
		j := bytes.Index(out[i:], hdr)
		if j < 0 {
			break
		}
		at := i + j
		if at == 0 || out[at-1] == '\n' || out[at-1] == '\r' {
			start = at
		}
		i = at + len(hdr)
	}
	if start < 0 {
		return errors.New("pdf: cannot locate written /Info object")
	}
	end := bytes.Index(out[start:], []byte("endobj"))
	if end < 0 {
		return errors.New("pdf: malformed written /Info object")
	}
	obj := out[start : start+end]
	for _, key := range pdfStamps {
		for {
			k := bytes.Index(obj, []byte("/"+key))
			if k < 0 {
				break
			}
			p := k + 1 + len(key)
			for p < len(obj) && (obj[p] == ' ' || obj[p] == '\r' || obj[p] == '\n' || obj[p] == '\t') {
				p++
			}
			vEnd, err := pdfStringEnd(obj, p)
			if err != nil {
				return err
			}
			for i := k; i < vEnd; i++ {
				obj[i] = ' '
			}
		}
	}
	return nil
}

// pdfStringEnd returns the index just past the string object starting at p.
func pdfStringEnd(b []byte, p int) (int, error) {
	if p >= len(b) {
		return 0, errors.New("pdf: missing value")
	}
	switch b[p] {
	case '(':
		depth := 0
		for i := p; i < len(b); i++ {
			switch b[i] {
			case '\\':
				i++
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return i + 1, nil
				}
			}
		}
	case '<':
		if i := bytes.IndexByte(b[p:], '>'); i >= 0 {
			return p + i + 1, nil
		}
	}
	return 0, errors.New("pdf: unexpected /Info value")
}
