package handlers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/computerguymackay/metakill/internal/meta/isobmff"
	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/internal/report"
)

// heifBrands are ftyp major brands handled as HEIF still images rather than
// MP4 video.
var heifBrands = map[string]bool{
	"heic": true, "heix": true, "heim": true, "heis": true, "mif1": true, "msf1": true,
	"avif": true, "hevc": true, "hevx": true, "heif": true,
}

// heifHandler zeroes the payload of Exif and XMP items in place (located via
// iinf + iloc), so no offsets move. The item boxes stay but carry no data.
// Embedded thumbnail images (iref 'thmb') cannot be removed without
// rewriting item locations, so they are kept; without -t the file is then
// reported as not fully clean. ICC profiles live in 'colr' properties that
// cannot be dropped in place and are always kept.
type heifHandler struct{}

func init() { Register(heifHandler{}) }

func (heifHandler) Name() string { return "heif" }

func (heifHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 12)
	if len(b) == 12 && string(b[4:8]) == "ftyp" && heifBrands[string(b[8:12])] {
		return true
	}
	return hasExt(path, ".heic", ".heif", ".avif", ".hif")
}

type heifItem struct {
	id      uint32
	typ     string
	content string // MIME type for 'mime' items
}

type heifExtent struct{ off, len int64 }

type heifPlan struct {
	rep     report.Report
	patches []patch
}

func heifExpand(r io.ReadSeeker, path []*isobmff.Box, b *isobmff.Box) (bool, int64) {
	if len(path) == 0 && b.Name() == "meta" {
		return true, 4
	}
	return false, 0
}

func (h heifHandler) plan(r io.ReadSeeker, opts Options) (*heifPlan, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, err
	}
	top, err := isobmff.Read(r, 0, size, heifExpand)
	if err != nil {
		return nil, fmt.Errorf("heif: %w", err)
	}
	p := &heifPlan{rep: report.New("heif")}
	var meta *isobmff.Box
	for _, b := range top {
		switch b.Name() {
		case "meta":
			meta = b
		case "free", "skip":
			if opts.ZeroPad && b.Size > b.Hdr {
				if pl, err := isobmff.ReadPayload(r, b, maxMetaBox); err == nil && !allZero(pl) {
					p.rep.Add(report.Binary(report.CatCustom, "heif", b.Name()+" (non-zero padding)", int64(len(pl))))
					p.patches = append(p.patches, patch{off: b.Payload(), data: make([]byte, len(pl))})
				}
			}
		case "moov":
			p.rep.Unclean("image sequence (moov) metadata is not scrubbed")
		}
	}
	if meta == nil {
		return nil, errors.New("heif: no meta box")
	}
	payload := func(name string) []byte {
		if b := meta.Child(name); b != nil {
			if pl, err := isobmff.ReadPayload(r, b, maxMetaBox); err == nil {
				return pl
			}
		}
		return nil
	}
	items, err := parseIinf(payload("iinf"))
	if err != nil {
		return nil, err
	}
	locs, err := parseIloc(payload("iloc"))
	if err != nil {
		return nil, err
	}
	var idatOff int64 = -1
	if b := meta.Child("idat"); b != nil {
		idatOff = b.Payload()
	}
	thumbs := parseThumbRefs(payload("iref"))

	for _, it := range items {
		isExif := it.typ == "Exif"
		isXMP := it.typ == "mime" && (it.content == "application/rdf+xml" || bytes.Contains([]byte(it.content), []byte("xmp")))
		if !isExif && !isXMP {
			continue
		}
		loc, ok := locs[it.id]
		if !ok {
			p.rep.Unclean(fmt.Sprintf("%s item %d has no location", it.typ, it.id))
			continue
		}
		var data []byte
		var exts []heifExtent
		for _, e := range loc.extents {
			off := loc.base + e.off
			if loc.method == 1 {
				if idatOff < 0 {
					break
				}
				off += idatOff
			} else if loc.method != 0 {
				p.rep.Unclean(fmt.Sprintf("%s item %d uses an unsupported construction method", it.typ, it.id))
				break
			}
			length := e.len
			if length == 0 { // extent runs to end of file
				length = size - off
			}
			if off < 0 || off+length > size || length > maxMetaBox {
				return nil, fmt.Errorf("heif: %s item %d out of range", it.typ, it.id)
			}
			b, err := readAt(r, off, int(length))
			if err != nil {
				return nil, err
			}
			data = append(data, b...)
			exts = append(exts, heifExtent{off, length})
		}
		if len(data) == 0 || allZero(data) {
			continue // already blanked
		}
		before := len(p.rep.Fields)
		if isExif {
			tiff := data
			if len(tiff) >= 4 {
				skip := int(binary.BigEndian.Uint32(tiff))
				if 4+skip <= len(tiff) {
					tiff = tiff[4+skip:]
				}
			}
			tiff = bytes.TrimPrefix(tiff, exifHeader)
			scrubEXIF(tiff, Options{}, &p.rep, false)
		} else {
			p.rep.Add(xmp.Fields(data)...)
		}
		if len(p.rep.Fields) == before {
			p.rep.Add(report.Binary(report.CatCustom, "heif", it.typ+" item", int64(len(data))))
		}
		for _, e := range exts {
			p.patches = append(p.patches, patch{off: e.off, data: make([]byte, e.len)})
		}
		p.rep.Bytes += int64(len(data))
	}
	if len(thumbs) > 0 {
		f := report.Binary(report.CatThumbnail, "heif", fmt.Sprintf("%d thumbnail image item(s)", len(thumbs)), 0)
		if opts.KeepThumbnails {
			p.rep.Keep(f)
		} else {
			p.rep.Unclean(fmt.Sprintf("%d embedded thumbnail image(s) kept: removing them would require rewriting item locations", len(thumbs)))
		}
	}
	if !opts.KeepICC && meta.Child("iprp") != nil {
		if pl := payload("iprp"); bytes.Contains(pl, []byte("colrprof")) || bytes.Contains(pl, []byte("colrrICC")) {
			p.rep.Warn("ICC colour profile kept (HEIF colour properties cannot be removed in place)")
		}
	}
	sort.Slice(p.patches, func(i, j int) bool { return p.patches[i].off < p.patches[j].off })
	return p, nil
}

func parseIinf(pl []byte) ([]heifItem, error) {
	if len(pl) < 6 {
		return nil, errors.New("heif: missing iinf")
	}
	p := 6
	if pl[0] != 0 {
		p = 8
	}
	if p > len(pl) {
		return nil, errors.New("heif: bad iinf")
	}
	boxes, _ := isobmff.Children(pl[p:])
	var out []heifItem
	for _, b := range boxes {
		if b.Type != "infe" || len(b.Payload) < 4 {
			continue
		}
		v := b.Payload[0]
		d := b.Payload[4:]
		if v < 2 {
			continue
		}
		var it heifItem
		if v == 2 {
			if len(d) < 8 {
				continue
			}
			it.id = uint32(binary.BigEndian.Uint16(d))
			d = d[4:]
		} else {
			if len(d) < 10 {
				continue
			}
			it.id = binary.BigEndian.Uint32(d)
			d = d[6:]
		}
		it.typ = string(d[:4])
		d = d[4:]
		if i := bytes.IndexByte(d, 0); i >= 0 { // item_name
			d = d[i+1:]
			if it.typ == "mime" {
				if j := bytes.IndexByte(d, 0); j >= 0 {
					it.content = string(d[:j])
				} else {
					it.content = string(d)
				}
			}
		}
		out = append(out, it)
	}
	return out, nil
}

type heifLoc struct {
	method  int
	base    int64
	extents []heifExtent
}

func parseIloc(pl []byte) (map[uint32]heifLoc, error) {
	if len(pl) < 8 {
		return nil, errors.New("heif: missing iloc")
	}
	v := pl[0]
	offSize := int(pl[4] >> 4)
	lenSize := int(pl[4] & 15)
	baseSize := int(pl[5] >> 4)
	idxSize := 0
	if v == 1 || v == 2 {
		idxSize = int(pl[5] & 15)
	}
	p := 6
	var count int
	if v < 2 {
		count = int(binary.BigEndian.Uint16(pl[p:]))
		p += 2
	} else {
		if len(pl) < p+4 {
			return nil, errors.New("heif: bad iloc")
		}
		count = int(binary.BigEndian.Uint32(pl[p:]))
		p += 4
	}
	readN := func(n int) (int64, bool) {
		if p+n > len(pl) {
			return 0, false
		}
		var x int64
		for _, c := range pl[p : p+n] {
			x = x<<8 | int64(c)
		}
		p += n
		return x, true
	}
	out := map[uint32]heifLoc{}
	for i := 0; i < count; i++ {
		idLen := 2
		if v == 2 {
			idLen = 4
		}
		id, ok := readN(idLen)
		if !ok {
			return nil, errors.New("heif: truncated iloc")
		}
		var loc heifLoc
		if v == 1 || v == 2 {
			m, ok := readN(2)
			if !ok {
				return nil, errors.New("heif: truncated iloc")
			}
			loc.method = int(m & 15)
		}
		if _, ok := readN(2); !ok { // data_reference_index
			return nil, errors.New("heif: truncated iloc")
		}
		if loc.base, ok = readN(baseSize); !ok {
			return nil, errors.New("heif: truncated iloc")
		}
		n, ok := readN(2)
		if !ok {
			return nil, errors.New("heif: truncated iloc")
		}
		for j := 0; j < int(n); j++ {
			if idxSize > 0 {
				if _, ok := readN(idxSize); !ok {
					return nil, errors.New("heif: truncated iloc")
				}
			}
			o, ok1 := readN(offSize)
			l, ok2 := readN(lenSize)
			if !ok1 || !ok2 {
				return nil, errors.New("heif: truncated iloc")
			}
			loc.extents = append(loc.extents, heifExtent{o, l})
		}
		out[uint32(id)] = loc
	}
	return out, nil
}

// parseThumbRefs returns the item IDs that are thumbnails of another item.
func parseThumbRefs(pl []byte) []uint32 {
	if len(pl) < 4 {
		return nil
	}
	wide := pl[0] != 0
	boxes, _ := isobmff.Children(pl[4:])
	var out []uint32
	for _, b := range boxes {
		if b.Type != "thmb" {
			continue
		}
		d := b.Payload
		w := 2
		if wide {
			w = 4
		}
		if len(d) < w+2 {
			continue
		}
		var from uint32
		if wide {
			from = binary.BigEndian.Uint32(d)
		} else {
			from = uint32(binary.BigEndian.Uint16(d))
		}
		out = append(out, from)
	}
	return out
}

func (h heifHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, nil
}

func (h heifHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	size, err := streamSize(r)
	if err != nil {
		return p.rep, err
	}
	p.rep.Bytes = 0 // data is zeroed in place, the file does not shrink
	return p.rep, copyRange(r, w, 0, size, mergePatches(p.patches))
}

// mergePatches drops patches fully contained in an earlier one.
func mergePatches(ps []patch) []patch {
	sort.Slice(ps, func(i, j int) bool { return ps[i].off < ps[j].off })
	var out []patch
	for _, pt := range ps {
		if n := len(out); n > 0 {
			last := out[n-1]
			if pt.off < last.off+int64(len(last.data)) {
				continue
			}
		}
		out = append(out, pt)
	}
	return out
}
