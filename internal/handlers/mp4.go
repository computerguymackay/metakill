package handlers

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/meta/isobmff"
	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/report"
)

// mp4Handler cleans MP4, M4V, M4A and QuickTime MOV files. It removes udta
// and meta boxes (iTunes/QuickTime/3GPP tags, ©xyz GPS, Apple keys such as
// com.apple.quicktime.location.ISO6709), XMP and other uuid boxes, and
// producer-reference-time boxes; zeroes creation/modification times in
// mvhd/tkhd/mdhd, handler names and compressor names; drops timed-metadata
// tracks (GoPro gpmd, camm, Apple mebx, Sony rtmd) and zeroes their samples;
// zeroes free/skip padding. The file is compacted and every chunk offset
// (stco/co64, tfhd base offsets) is rewritten to match.
type mp4Handler struct{}

func init() { Register(mp4Handler{}) }

func (mp4Handler) Name() string { return "mp4" }

var qtTopLevel = map[string]bool{"moov": true, "mdat": true, "free": true, "wide": true, "skip": true, "pnot": true}

func (mp4Handler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 12)
	if len(b) == 12 {
		switch string(b[4:8]) {
		case "ftyp":
			brand := string(b[8:12])
			return !heifBrands[brand] && brand != "crx "
		default:
			if qtTopLevel[string(b[4:8])] {
				return true
			}
		}
	}
	return hasExt(path, ".mp4", ".m4v", ".m4a", ".m4b", ".mov", ".qt", ".3gp", ".3g2")
}

var mp4Containers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true,
	"moof": true, "traf": true, "mvex": true, "edts": true,
}

func mp4Expand(_ io.ReadSeeker, path []*isobmff.Box, b *isobmff.Box) (bool, int64) {
	return mp4Containers[b.Name()], 0
}

// XMP uuid used by Adobe tools in MP4/MOV.
var xmpUUID = []byte{0xBE, 0x7A, 0xCF, 0xCB, 0x97, 0xA9, 0x42, 0xE8, 0x9C, 0x71, 0x99, 0x94, 0x91, 0xE3, 0xAF, 0xAC}

// Sample entry types of timed-metadata tracks that commonly carry GPS.
var telemetryEntries = map[string]bool{"gpmd": true, "camm": true, "mebx": true, "rtmd": true}

const maxMetaBox = 64 << 20

type mp4Plan struct {
	rep      report.Report
	top      []*isobmff.Box
	drop     map[*isobmff.Box]bool
	replace  map[*isobmff.Box][]byte
	patches  []patch
	offsets  []*isobmff.Box // stco/co64 boxes in kept tracks
	tfhds    []*isobmff.Box
	newTotal int64
}

func mp4Format(top []*isobmff.Box, r io.ReadSeeker) string {
	if len(top) == 0 || top[0].Name() != "ftyp" {
		return "mov"
	}
	b, err := readAt(r, top[0].Payload(), 4)
	if err != nil {
		return "mp4"
	}
	switch string(b) {
	case "qt  ":
		return "mov"
	case "M4A ", "M4B ", "M4P ":
		return "m4a"
	}
	return "mp4"
}

func (h mp4Handler) plan(r io.ReadSeeker, opts Options) (*mp4Plan, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, err
	}
	top, err := isobmff.Read(r, 0, size, mp4Expand)
	if err != nil {
		return nil, fmt.Errorf("mp4: %w", err)
	}
	p := &mp4Plan{rep: report.New(mp4Format(top, r)), top: top,
		drop: map[*isobmff.Box]bool{}, replace: map[*isobmff.Box][]byte{}}
	hasMoov := false
	for _, b := range top {
		switch b.Name() {
		case "moov":
			hasMoov = true
			if err := p.moov(r, b, opts); err != nil {
				return nil, err
			}
		case "moof":
			p.moof(b)
		case "uuid":
			p.uuid(r, b)
		case "meta":
			p.metaBox(r, b, "file")
		case "prft":
			p.rep.Add(report.Binary(report.CatTimestamp, "mp4", "prft (producer reference time)", b.Size))
			p.drop[b] = true
		case "free", "skip":
			p.padding(r, b, opts)
		case "cmov":
			return nil, fmt.Errorf("mp4: compressed movie header: %w", ErrUnsupported)
		}
	}
	if !hasMoov {
		return nil, errors.New("mp4: no moov box")
	}
	if err := p.layout(r, size); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *mp4Plan) moov(r io.ReadSeeker, moov *isobmff.Box, opts Options) error {
	for _, b := range moov.Kids {
		switch b.Name() {
		case "mvhd":
			p.times(r, b, "mvhd")
		case "trak":
			if err := p.trak(r, b, opts); err != nil {
				return err
			}
		case "udta":
			p.udta(r, b, opts, true)
		case "meta":
			p.metaBox(r, b, "movie")
		case "uuid":
			p.uuid(r, b)
		case "free", "skip":
			p.padding(r, b, opts)
		}
	}
	return nil
}

func (p *mp4Plan) trak(r io.ReadSeeker, trak *isobmff.Box, opts Options) error {
	handler, hdlrName := "", ""
	if hdlr := trak.Find("mdia", "hdlr"); hdlr != nil {
		if pl, err := isobmff.ReadPayload(r, hdlr, 4096); err == nil && len(pl) >= 12 {
			handler = string(pl[8:12])
			if len(pl) > 24 {
				hdlrName = strings.Trim(string(pl[24:]), "\x00 ")
				if len(hdlrName) > 0 && int(pl[24]) == len(pl)-25 { // QuickTime Pascal string
					hdlrName = strings.Trim(string(pl[25:]), "\x00 ")
				}
			}
		}
	}
	entry := ""
	if stsd := trak.Find("mdia", "minf", "stbl", "stsd"); stsd != nil {
		if b, err := readAt(r, stsd.Payload()+12, 4); err == nil {
			entry = string(b)
		}
	}
	if telemetryEntries[entry] {
		p.dropTelemetry(r, trak, entry, handler)
		return nil
	}
	for _, b := range trak.Kids {
		switch b.Name() {
		case "tkhd":
			p.times(r, b, "tkhd")
		case "udta":
			p.udta(r, b, opts, false)
		case "meta":
			p.metaBox(r, b, "track")
		case "uuid":
			p.uuid(r, b)
		case "mdia":
			for _, m := range b.Kids {
				switch m.Name() {
				case "mdhd":
					p.times(r, m, "mdhd")
				case "hdlr":
					if hdlrName != "" && report.Printable(hdlrName) {
						p.rep.Add(report.Text(report.CatSoftware, "mp4", "hdlr:"+handler+" name", hdlrName))
						p.zeroFrom(m, m.Payload()+24)
					}
				case "minf":
					stbl := m.Child("stbl")
					if stbl == nil {
						continue
					}
					for _, s := range stbl.Kids {
						switch s.Name() {
						case "stco", "co64":
							p.offsets = append(p.offsets, s)
						case "stsd":
							if handler == "vide" {
								p.compressorNames(r, s)
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// times zeroes the creation and modification times of mvhd/tkhd/mdhd.
func (p *mp4Plan) times(r io.ReadSeeker, b *isobmff.Box, name string) {
	pl, err := readAt(r, b.Payload(), 20)
	if err != nil {
		return
	}
	width := 4
	if pl[0] == 1 {
		width = 8
	}
	for i, label := range []string{"creation_time", "modification_time"} {
		off := 4 + i*width
		var v uint64
		if width == 4 {
			v = uint64(binary.BigEndian.Uint32(pl[off:]))
		} else {
			v = binary.BigEndian.Uint64(pl[off:])
		}
		if v == 0 {
			continue
		}
		p.rep.Add(report.Text(report.CatTimestamp, "mp4", name+"."+label, mp4Time(v)))
		p.patches = append(p.patches, patch{off: b.Payload() + int64(off), data: make([]byte, width)})
	}
}

// mp4Time converts seconds since 1904-01-01 UTC.
func mp4Time(v uint64) string {
	epoch := time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	if v > 1<<40 {
		return fmt.Sprint(v)
	}
	return epoch.Add(time.Duration(v) * time.Second).Format(time.RFC3339)
}

func (p *mp4Plan) zeroFrom(b *isobmff.Box, from int64) {
	if n := b.End() - from; n > 0 {
		p.patches = append(p.patches, patch{off: from, data: make([]byte, n)})
	}
}

// compressorNames zeroes the 32-byte compressorname of visual sample entries.
func (p *mp4Plan) compressorNames(r io.ReadSeeker, stsd *isobmff.Box) {
	pl, err := isobmff.ReadPayload(r, stsd, 1<<20)
	if err != nil || len(pl) < 8 {
		return
	}
	entries, _ := isobmff.Children(pl[8:])
	pos := stsd.Payload() + 8
	for _, e := range entries {
		const at = 8 + 42 // header + fields before compressorname
		if len(e.Raw) >= at+32 {
			name := e.Raw[at : at+32]
			if !allZero(name) {
				n := int(name[0])
				s := ""
				if n > 0 && n < 32 {
					s = string(name[1 : 1+n])
				}
				p.rep.Add(report.Text(report.CatSoftware, "mp4", e.Type+" compressorname", s))
				p.patches = append(p.patches, patch{off: pos + at, data: make([]byte, 32)})
			}
		}
		pos += int64(len(e.Raw))
	}
}

func (p *mp4Plan) padding(r io.ReadSeeker, b *isobmff.Box, opts Options) {
	if !opts.ZeroPad || b.Size == b.Hdr {
		return
	}
	pl, err := isobmff.ReadPayload(r, b, maxMetaBox)
	if err != nil || allZero(pl) {
		return
	}
	p.rep.Add(report.Binary(report.CatCustom, "mp4", b.Name()+" (non-zero padding)", int64(len(pl))))
	p.zeroFrom(b, b.Payload())
}

func (p *mp4Plan) uuid(r io.ReadSeeker, b *isobmff.Box) {
	p.drop[b] = true
	if bytes.Equal(b.UUID, xmpUUID) {
		if pl, err := isobmff.ReadPayload(r, b, maxMetaBox); err == nil {
			p.rep.Add(xmp.Fields(pl[16:])...)
			return
		}
	}
	p.rep.Add(report.Binary(report.CatCustom, "mp4", "uuid:"+hex.EncodeToString(b.UUID), b.Size))
}

func (p *mp4Plan) moof(moof *isobmff.Box) {
	for _, traf := range moof.Kids {
		if traf.Name() != "traf" {
			continue
		}
		if tfhd := traf.Child("tfhd"); tfhd != nil {
			p.tfhds = append(p.tfhds, tfhd)
		}
	}
}

// metaBox drops a meta box, reporting its items.
func (p *mp4Plan) metaBox(r io.ReadSeeker, b *isobmff.Box, level string) {
	p.drop[b] = true
	raw, err := isobmff.ReadAll(r, b, maxMetaBox)
	if err != nil {
		p.rep.Add(report.Binary(report.CatCustom, "mp4", level+" meta", b.Size))
		return
	}
	boxes, _ := isobmff.Children(raw)
	if len(boxes) == 1 {
		for _, it := range parseMeta(boxes[0].Payload, nil) {
			p.rep.Add(it.field)
		}
	}
}

// ---------- udta / meta / ilst ----------

type mp4Item struct {
	field     report.Field
	canonical string
	raw       []byte // the whole item box, for copying
	isCover   bool
	images    [][]byte
	dataType  uint32
}

// parseMeta reads the children of a meta box payload (hdlr, keys, ilst).
func parseMeta(payload []byte, _ []byte) []mp4Item {
	off := isobmff.MetaChildrenOffset(payload)
	if off > len(payload) {
		return nil
	}
	kids, _ := isobmff.Children(payload[off:])
	var keys []string
	handler := ""
	for _, k := range kids {
		switch k.Type {
		case "hdlr":
			if len(k.Payload) >= 12 {
				handler = string(k.Payload[8:12])
			}
		case "keys":
			keys = parseKeys(k.Payload)
		}
	}
	var out []mp4Item
	for _, k := range kids {
		switch k.Type {
		case "ilst":
			items, _ := isobmff.Children(k.Payload)
			for _, it := range items {
				out = append(out, parseIlstItem(it, keys, handler))
			}
		case "hdlr", "keys", "free", "skip":
		default:
			out = append(out, mp4Item{field: report.Binary(report.CatCustom, "mp4.meta", k.Type, int64(len(k.Raw)))})
		}
	}
	return out
}

func parseKeys(pl []byte) []string {
	if len(pl) < 8 {
		return nil
	}
	n := int(binary.BigEndian.Uint32(pl[4:]))
	b := pl[8:]
	var out []string
	for i := 0; i < n && len(b) >= 8; i++ {
		sz := int(binary.BigEndian.Uint32(b))
		if sz < 8 || sz > len(b) {
			break
		}
		out = append(out, string(b[8:sz]))
		b = b[sz:]
	}
	return out
}

func parseIlstItem(it isobmff.MemBox, keys []string, handler string) mp4Item {
	name := it.Type
	cat, canon := classify.Ilst(name)
	ns := "itunes"
	if handler == "mdta" || len(keys) > 0 {
		ns = "quicktime"
		if len(it.Raw) >= 8 {
			idx := int(binary.BigEndian.Uint32(it.Raw[4:8]))
			if idx >= 1 && idx <= len(keys) {
				name = keys[idx-1]
			}
		}
		cat, canon = classify.QuickTimeKey(name), ""
	}
	kids, _ := isobmff.Children(it.Payload)
	var vals []string
	var images [][]byte
	var dtype uint32
	binarySize := 0
	for _, k := range kids {
		switch k.Type {
		case "name":
			if len(k.Payload) > 4 {
				name = "----:" + string(k.Payload[4:])
				cat, canon = classify.Vorbis(string(k.Payload[4:]))
			}
		case "data":
			if len(k.Payload) < 8 {
				continue
			}
			dtype = binary.BigEndian.Uint32(k.Payload) & 0xFFFFFF
			v := k.Payload[8:]
			switch dtype {
			case 1:
				vals = append(vals, string(v))
			case 2:
				u := make([]uint16, 0, len(v)/2)
				for i := 0; i+1 < len(v); i += 2 {
					u = append(u, binary.BigEndian.Uint16(v[i:]))
				}
				vals = append(vals, string(utf16.Decode(u)))
			case 21, 22:
				var n int64
				for _, c := range v {
					n = n<<8 | int64(c)
				}
				if dtype == 21 && len(v) > 0 && len(v) < 8 && v[0]&0x80 != 0 {
					n -= 1 << (8 * len(v))
				}
				vals = append(vals, fmt.Sprint(n))
			case 13, 14, 27:
				images = append(images, v)
				binarySize += len(v)
			case 0:
				if (it.Type == "trkn" || it.Type == "disk") && len(v) >= 6 {
					vals = append(vals, fmt.Sprintf("%d/%d", binary.BigEndian.Uint16(v[2:]), binary.BigEndian.Uint16(v[4:])))
				} else {
					binarySize += len(v)
				}
			default:
				binarySize += len(v)
			}
		}
	}
	item := mp4Item{raw: it.Raw, canonical: canon, images: images, dataType: dtype}
	switch {
	case len(images) > 0:
		item.isCover = true
		item.field = report.Binary(report.CatThumbnail, ns, name, int64(binarySize))
	case len(vals) > 0:
		item.field = report.Text(cat, ns, name, strings.Join(vals, "; "))
	default:
		item.field = report.Binary(cat, ns, name, int64(len(it.Raw)))
	}
	return item
}

// 3GPP asset boxes in udta: FullBox + language + string.
var threeGPP = map[string]struct {
	cat   report.Category
	canon string
}{
	"titl": {report.CatIDTag, classify.Title}, "perf": {report.CatIDTag, classify.Artist},
	"albm": {report.CatIDTag, classify.Album}, "auth": {report.CatAuthor, ""},
	"dscp": {report.CatCustom, ""}, "cprt": {report.CatAuthor, ""}, "gnre": {report.CatCustom, ""},
	"kywd": {report.CatCustom, ""}, "loci": {report.CatGPS, ""}, "yrrc": {report.CatTimestamp, classify.Year},
	"rtng": {report.CatCustom, ""}, "clsf": {report.CatCustom, ""},
}

// udta reports every child of a user-data box. At movie level with -k it
// rebuilds udta with only the essential tags; otherwise udta is dropped.
func (p *mp4Plan) udta(r io.ReadSeeker, b *isobmff.Box, opts Options, movieLevel bool) {
	p.drop[b] = true
	raw, err := isobmff.ReadAll(r, b, maxMetaBox)
	if err != nil {
		p.rep.Add(report.Binary(report.CatCustom, "mp4", "udta", b.Size))
		return
	}
	outer, _ := isobmff.Children(raw)
	if len(outer) != 1 {
		p.rep.Add(report.Binary(report.CatCustom, "mp4", "udta", b.Size))
		return
	}
	kids, rest := isobmff.Children(outer[0].Payload)
	keep := movieLevel && opts.KeepTags
	var keptAtoms, keptItems []byte

	for _, k := range kids {
		switch {
		case k.Type == "meta":
			for _, it := range parseMeta(k.Payload, nil) {
				switch {
				case keep && it.canonical != "" && keepEssential(opts, it.canonical) && it.raw != nil:
					if it.canonical == classify.Year {
						y := ""
						if it.field.Value != nil {
							y = yearOnly(*it.field.Value)
						}
						if y == "" {
							p.rep.Add(it.field)
							continue
						}
						keptItems = append(keptItems, ilstText("©day", y)...)
					} else {
						keptItems = append(keptItems, it.raw...)
					}
					p.rep.Keep(it.field)
				case keep && it.isCover && opts.KeepThumbnails:
					keptItems = append(keptItems, p.cleanCover(it, opts)...)
					p.rep.Keep(it.field)
				default:
					p.rep.Add(it.field)
				}
			}
		case k.Type == "free" || k.Type == "skip":
		case len(k.Type) == 5 && strings.HasPrefix(k.Type, "©"): // QuickTime text atom
			text := qtText(k.Payload)
			cat, canon := classify.Ilst(k.Type)
			f := report.Text(cat, "quicktime", k.Type, text)
			if text == "" {
				f = report.Binary(cat, "quicktime", k.Type, int64(len(k.Payload)))
			}
			if keep && canon != "" && keepEssential(opts, canon) && text != "" {
				if canon == classify.Year {
					if y := yearOnly(text); y != "" {
						keptAtoms = append(keptAtoms, qtTextAtom(k.Type, y)...)
						p.rep.Keep(f)
						continue
					}
				} else {
					keptAtoms = append(keptAtoms, k.Raw...)
					p.rep.Keep(f)
					continue
				}
			}
			p.rep.Add(f)
		default:
			if info, ok := threeGPP[k.Type]; ok {
				text := gppText(k.Type, k.Payload)
				f := report.Text(info.cat, "3gpp", k.Type, text)
				if keep && info.canon != "" && keepEssential(opts, info.canon) {
					keptAtoms = append(keptAtoms, k.Raw...)
					p.rep.Keep(f)
					continue
				}
				p.rep.Add(f)
				continue
			}
			cat, _ := classify.Ilst(k.Type)
			if k.Type == "XMP_" {
				p.rep.Add(xmp.Fields(k.Payload)...)
				continue
			}
			if s := strings.Trim(string(k.Payload), "\x00 "); s != "" && len(s) < 256 && report.Printable(s) {
				p.rep.Add(report.Text(cat, "mp4.udta", k.Type, s))
			} else {
				p.rep.Add(report.Binary(cat, "mp4.udta", k.Type, int64(len(k.Payload))))
			}
		}
	}
	if len(rest) > 0 && !allZero(rest) {
		p.rep.Add(report.Binary(report.CatCustom, "mp4.udta", "trailing data", int64(len(rest))))
	}
	if keptAtoms == nil && keptItems == nil {
		return
	}
	var payload []byte
	payload = append(payload, keptAtoms...)
	if keptItems != nil {
		hdlr := isobmff.Make("hdlr", make([]byte, 8), []byte("mdirappl"), make([]byte, 9))
		payload = append(payload, isobmff.Make("meta", make([]byte, 4), hdlr, isobmff.Make("ilst", keptItems))...)
	}
	delete(p.drop, b)
	p.replace[b] = isobmff.Make("udta", payload)
}

// cleanCover rebuilds a covr item with EXIF stripped from each image.
func (p *mp4Plan) cleanCover(it mp4Item, opts Options) []byte {
	var datas []byte
	for _, img := range it.images {
		if out, sub, ok := StripEmbedded("cover art", img, opts); ok {
			if out != nil {
				img = out
			}
			if sub.TotalFields() > 0 || !sub.FullyClean {
				p.rep.AddEmbedded(sub)
			}
		}
		hdr := make([]byte, 8)
		binary.BigEndian.PutUint32(hdr, it.dataType)
		datas = append(datas, isobmff.Make("data", hdr, img)...)
	}
	return isobmff.Make("covr", datas)
}

func ilstText(typ, v string) []byte {
	hdr := make([]byte, 8)
	hdr[3] = 1 // UTF-8
	return isobmff.Make(typ, isobmff.Make("data", hdr, []byte(v)))
}

// qtText decodes a QuickTime international text atom (len, lang, text).
func qtText(pl []byte) string {
	if len(pl) < 4 {
		return ""
	}
	n := int(binary.BigEndian.Uint16(pl))
	if 4+n > len(pl) {
		n = len(pl) - 4
	}
	s := string(pl[4 : 4+n])
	if !report.Printable(s) {
		return ""
	}
	return s
}

func qtTextAtom(typ, v string) []byte {
	hdr := []byte{0, 0, 0x55, 0xC4} // length placeholder + "und" language
	binary.BigEndian.PutUint16(hdr, uint16(len(v)))
	return isobmff.Make(typ, hdr, []byte(v))
}

// gppText decodes a 3GPP asset (FullBox, language, string).
func gppText(typ string, pl []byte) string {
	if typ == "yrrc" && len(pl) >= 6 {
		return fmt.Sprint(binary.BigEndian.Uint16(pl[4:]))
	}
	if len(pl) < 6 {
		return ""
	}
	s := pl[6:]
	if typ == "loci" || typ == "rtng" || typ == "clsf" {
		return ""
	}
	if i := bytes.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return string(s)
}

// ---------- timed-metadata tracks ----------

func (p *mp4Plan) dropTelemetry(r io.ReadSeeker, trak *isobmff.Box, entry, handler string) {
	p.drop[trak] = true
	stbl := trak.Find("mdia", "minf", "stbl")
	ranges, err := chunkRanges(r, stbl)
	var total int64
	for _, c := range ranges {
		total += c[1]
	}
	p.rep.Add(report.Binary(report.CatGPS, "mp4", fmt.Sprintf("timed metadata track (%s/%s)", handler, entry), total))
	if err != nil {
		p.rep.Unclean(fmt.Sprintf("could not locate samples of %s track: %v", entry, err))
		return
	}
	for _, c := range ranges {
		if c[1] > 0 {
			p.patches = append(p.patches, patch{off: c[0], data: make([]byte, c[1])})
		}
	}
}

// chunkRanges returns (offset, size) for every chunk of a track.
func chunkRanges(r io.ReadSeeker, stbl *isobmff.Box) ([][2]int64, error) {
	if stbl == nil {
		return nil, errors.New("no stbl")
	}
	get := func(name string) []byte {
		if b := stbl.Child(name); b != nil {
			pl, err := isobmff.ReadPayload(r, b, 64<<20)
			if err == nil {
				return pl
			}
		}
		return nil
	}
	var offs []int64
	if pl := get("stco"); len(pl) >= 8 {
		n := int(binary.BigEndian.Uint32(pl[4:]))
		for i := 0; i < n && 8+4*i+4 <= len(pl); i++ {
			offs = append(offs, int64(binary.BigEndian.Uint32(pl[8+4*i:])))
		}
	} else if pl := get("co64"); len(pl) >= 8 {
		n := int(binary.BigEndian.Uint32(pl[4:]))
		for i := 0; i < n && 8+8*i+8 <= len(pl); i++ {
			offs = append(offs, int64(binary.BigEndian.Uint64(pl[8+8*i:])))
		}
	} else {
		return nil, errors.New("no chunk offsets")
	}
	stsz := get("stsz")
	if len(stsz) < 12 {
		return nil, errors.New("no stsz")
	}
	fixed := int64(binary.BigEndian.Uint32(stsz[4:]))
	count := int(binary.BigEndian.Uint32(stsz[8:]))
	sampleSize := func(i int) int64 {
		if fixed != 0 {
			return fixed
		}
		if 12+4*i+4 > len(stsz) {
			return 0
		}
		return int64(binary.BigEndian.Uint32(stsz[12+4*i:]))
	}
	stsc := get("stsc")
	if len(stsc) < 8 {
		return nil, errors.New("no stsc")
	}
	type run struct{ first, per int }
	var runs []run
	n := int(binary.BigEndian.Uint32(stsc[4:]))
	for i := 0; i < n && 8+12*i+12 <= len(stsc); i++ {
		e := stsc[8+12*i:]
		runs = append(runs, run{int(binary.BigEndian.Uint32(e)), int(binary.BigEndian.Uint32(e[4:]))})
	}
	var out [][2]int64
	sample := 0
	for ci := range offs {
		per := 0
		for _, rr := range runs {
			if rr.first <= ci+1 {
				per = rr.per
			}
		}
		var sz int64
		for j := 0; j < per && sample < count; j++ {
			sz += sampleSize(sample)
			sample++
		}
		out = append(out, [2]int64{offs[ci], sz})
	}
	return out, nil
}

// ---------- layout and writing ----------

func (p *mp4Plan) newSize(b *isobmff.Box) int64 {
	if p.drop[b] {
		return 0
	}
	if rep, ok := p.replace[b]; ok {
		return int64(len(rep))
	}
	if !b.Expanded {
		return b.Size
	}
	n := b.Hdr + b.Pre
	for _, k := range b.Kids {
		n += p.newSize(k)
	}
	return n
}

// layout computes new offsets and rewrites chunk offsets.
func (p *mp4Plan) layout(r io.ReadSeeker, size int64) error {
	type span struct{ old, end, new int64 }
	var spans []span
	var pos int64
	for _, b := range p.top {
		n := p.newSize(b)
		if n > 0 {
			spans = append(spans, span{b.Off, b.End(), pos})
		}
		pos += n
	}
	p.newTotal = pos
	p.rep.Bytes = size - pos
	if p.rep.Bytes < 0 {
		p.rep.Bytes = 0
	}
	mapOff := func(x int64) (int64, error) {
		for _, s := range spans {
			if x >= s.old && x < s.end {
				return x - s.old + s.new, nil
			}
		}
		return 0, fmt.Errorf("mp4: chunk offset %d is outside the kept data", x)
	}
	shifted := false
	for _, s := range spans {
		if s.old != s.new {
			shifted = true
		}
	}
	if !shifted {
		return nil
	}
	for _, b := range p.top {
		if b.Name() == "mfra" && !p.drop[b] {
			p.drop[b] = true // random-access index with absolute offsets
			p.rep.Warn("dropped mfra random-access index (offsets changed)")
		}
	}
	for _, b := range p.offsets {
		pl, err := isobmff.ReadPayload(r, b, 256<<20)
		if err != nil || len(pl) < 8 {
			return fmt.Errorf("mp4: reading %s: %v", b.Name(), err)
		}
		n := int(binary.BigEndian.Uint32(pl[4:]))
		w := 4
		if b.Name() == "co64" {
			w = 8
		}
		if 8+n*w > len(pl) {
			return fmt.Errorf("mp4: %s truncated", b.Name())
		}
		for i := 0; i < n; i++ {
			at := pl[8+i*w:]
			var x int64
			if w == 4 {
				x = int64(binary.BigEndian.Uint32(at))
			} else {
				x = int64(binary.BigEndian.Uint64(at))
			}
			nx, err := mapOff(x)
			if err != nil {
				return err
			}
			if w == 4 {
				binary.BigEndian.PutUint32(at, uint32(nx))
			} else {
				binary.BigEndian.PutUint64(at, uint64(nx))
			}
		}
		p.patches = append(p.patches, patch{off: b.Payload(), data: pl})
	}
	for _, b := range p.tfhds {
		pl, err := readAt(r, b.Payload(), 16)
		if err != nil {
			continue
		}
		if pl[3]&1 == 0 {
			continue // no explicit base_data_offset
		}
		x := int64(binary.BigEndian.Uint64(pl[8:]))
		nx, err := mapOff(x)
		if err != nil {
			return err
		}
		d := make([]byte, 8)
		binary.BigEndian.PutUint64(d, uint64(nx))
		p.patches = append(p.patches, patch{off: b.Payload() + 8, data: d})
	}
	return nil
}

func (h mp4Handler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, nil
}

func (h mp4Handler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	sort.Slice(p.patches, func(i, j int) bool { return p.patches[i].off < p.patches[j].off })
	for i := 1; i < len(p.patches); i++ {
		prev := p.patches[i-1]
		if prev.off+int64(len(prev.data)) > p.patches[i].off {
			return p.rep, errors.New("mp4: overlapping edits")
		}
	}
	for _, b := range p.top {
		if err := p.emit(r, w, b); err != nil {
			return p.rep, err
		}
	}
	return p.rep, nil
}

func (p *mp4Plan) emit(r io.ReadSeeker, w io.Writer, b *isobmff.Box) error {
	if p.drop[b] {
		return nil
	}
	if rep, ok := p.replace[b]; ok {
		_, err := w.Write(rep)
		return err
	}
	if !b.Expanded {
		return copyRange(r, w, b.Off, b.End(), p.patchesIn(b.Off, b.End()))
	}
	n := p.newSize(b)
	var hdr []byte
	if b.Large {
		hdr = make([]byte, 16)
		binary.BigEndian.PutUint32(hdr, 1)
		binary.BigEndian.PutUint64(hdr[8:], uint64(n))
	} else {
		hdr = make([]byte, 8)
		binary.BigEndian.PutUint32(hdr, uint32(n))
	}
	copy(hdr[4:8], b.Type[:])
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if b.Pre > 0 {
		if err := copyRange(r, w, b.Payload(), b.Payload()+b.Pre, p.patchesIn(b.Payload(), b.Payload()+b.Pre)); err != nil {
			return err
		}
	}
	for _, k := range b.Kids {
		if err := p.emit(r, w, k); err != nil {
			return err
		}
	}
	return nil
}

func (p *mp4Plan) patchesIn(start, end int64) []patch {
	var out []patch
	for _, pt := range p.patches {
		if pt.off >= start && pt.off+int64(len(pt.data)) <= end {
			out = append(out, pt)
		}
	}
	return out
}
