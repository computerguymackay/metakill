package handlers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/report"
)

// mkvHandler cleans Matroska and WebM files in place, the way mkvpropedit
// does: removed elements are overwritten with Void elements of the same
// size, so SeekHead and Cues offsets stay valid. It blanks Title, DateUTC,
// segment UIDs/filenames, MuxingApp/WritingApp (mandatory, so rewritten as
// empty strings), track names and codec names, removes Tags (essential
// SimpleTags kept with -k), Attachments (kept with -t), the SeekHead entries
// pointing at removed elements, and CRC-32 elements of edited masters.
type mkvHandler struct{}

func init() { Register(mkvHandler{}) }

func (mkvHandler) Name() string { return "mkv" }

func (mkvHandler) CanHandle(r io.ReadSeeker, path string) bool {
	return bytes.Equal(peek(r, 4), []byte{0x1A, 0x45, 0xDF, 0xA3}) || hasExt(path, ".mkv", ".mka", ".mks", ".webm", ".mk3d")
}

const (
	ebmlHeader    = 0x1A45DFA3
	idSegment     = 0x18538067
	idSeekHead    = 0x114D9B74
	idSeek        = 0x4DBB
	idSeekID      = 0x53AB
	idInfo        = 0x1549A966
	idTracks      = 0x1654AE6B
	idTrackEntry  = 0xAE
	idCluster     = 0x1F43B675
	idCues        = 0x1C53BB6B
	idChapters    = 0x1043A770
	idTags        = 0x1254C367
	idTag         = 0x7373
	idTargets     = 0x63C0
	idSimpleTag   = 0x67C8
	idTagName     = 0x45A3
	idTagString   = 0x4487
	idTagBinary   = 0x4485
	idAttachments = 0x1941A469
	idAttached    = 0x61A7
	idFileName    = 0x466E
	idFileMime    = 0x4660
	idFileData    = 0x465C
	idFileDesc    = 0x467E
	idVoid        = 0xEC
	idCRC32       = 0xBF
	idTitle       = 0x7BA9
	idDateUTC     = 0x4461
	idMuxingApp   = 0x4D80
	idWritingApp  = 0x5741
	idName        = 0x536E
	idCodecName   = 0x258688
)

var mkvInfoOptional = map[uint64]struct {
	name string
	cat  report.Category
}{
	0x73A4:   {"SegmentUID", report.CatCustom},
	0x7384:   {"SegmentFilename", report.CatCustom},
	0x3CB923: {"PrevUID", report.CatCustom},
	0x3C83AB: {"PrevFilename", report.CatCustom},
	0x3EB923: {"NextUID", report.CatCustom},
	0x3E83BB: {"NextFilename", report.CatCustom},
	0x4444:   {"SegmentFamily", report.CatCustom},
}

// segmentLevel lists IDs that can follow an unknown-size Cluster.
var segmentLevel = map[uint64]bool{
	idSeekHead: true, idInfo: true, idTracks: true, idCluster: true, idCues: true,
	idChapters: true, idTags: true, idAttachments: true,
}

type ebmlElem struct {
	id        uint64
	idLen     int
	off       int64 // element start
	dataOff   int64
	size      int64 // data size; -1 for unknown
	unknownSz bool
}

func (e ebmlElem) end() int64   { return e.dataOff + e.size }
func (e ebmlElem) total() int64 { return e.end() - e.off }

func readVint(r io.ReadSeeker, keepMarker bool) (uint64, int, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, 0, err
	}
	n := 1
	for mask := byte(0x80); mask != 0 && b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 {
		return 0, 0, errors.New("mkv: invalid vint")
	}
	v := uint64(b[0])
	if !keepMarker {
		v &= uint64(0xFF >> n)
	}
	rest := make([]byte, n-1)
	if _, err := io.ReadFull(r, rest); err != nil {
		return 0, 0, err
	}
	allOnes := v == uint64(0xFF>>n)
	for _, c := range rest {
		v = v<<8 | uint64(c)
		if c != 0xFF {
			allOnes = false
		}
	}
	if !keepMarker && allOnes {
		return ^uint64(0), n, nil // unknown size
	}
	return v, n, nil
}

func readElem(r io.ReadSeeker, off int64) (ebmlElem, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return ebmlElem{}, err
	}
	id, idLen, err := readVint(r, true)
	if err != nil {
		return ebmlElem{}, err
	}
	if idLen > 4 {
		return ebmlElem{}, errors.New("mkv: invalid element ID")
	}
	size, szLen, err := readVint(r, false)
	if err != nil {
		return ebmlElem{}, err
	}
	e := ebmlElem{id: id, idLen: idLen, off: off, dataOff: off + int64(idLen+szLen)}
	if size == ^uint64(0) {
		e.unknownSz = true
		e.size = -1
	} else {
		e.size = int64(size)
	}
	return e, nil
}

// children lists the children of [start, end). Unknown-size children are
// resolved by scanning until an element at the parent's level appears.
func mkvChildren(r io.ReadSeeker, start, end int64) ([]ebmlElem, error) {
	var out []ebmlElem
	for pos := start; pos < end; {
		e, err := readElem(r, pos)
		if err != nil {
			if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
				break // trailing garbage / truncated stream
			}
			return nil, err
		}
		if e.unknownSz {
			if e.id != idCluster && e.id != idSegment {
				return nil, fmt.Errorf("mkv: unknown-size element %X not supported", e.id)
			}
			e.size = mkvScanUnknown(r, e.dataOff, end) - e.dataOff
		}
		if e.end() > end {
			if e.id == idCluster {
				e.size = end - e.dataOff // truncated final cluster
			} else {
				return nil, fmt.Errorf("mkv: element %X at %d extends past its parent", e.id, pos)
			}
		}
		out = append(out, e)
		pos = e.end()
	}
	return out, nil
}

// mkvScanUnknown finds the end of an unknown-size cluster.
func mkvScanUnknown(r io.ReadSeeker, pos, end int64) int64 {
	for pos < end {
		e, err := readElem(r, pos)
		if err != nil || e.unknownSz || segmentLevel[e.id] {
			return pos
		}
		pos = e.end()
	}
	return end
}

func mkvRead(r io.ReadSeeker, e ebmlElem, max int64) ([]byte, error) {
	if e.size > max {
		return nil, fmt.Errorf("mkv: element %X too large", e.id)
	}
	return readAt(r, e.dataOff, int(e.size))
}

// voidElem builds a Void element of exactly n bytes (n >= 2).
func voidElem(n int64) []byte {
	for l := int64(1); l <= 8; l++ {
		data := n - 1 - l
		if data < 0 {
			break
		}
		if uint64(data) < (uint64(1)<<(7*l))-1 {
			b := make([]byte, n)
			b[0] = idVoid
			v := uint64(data) | uint64(1)<<(7*l)
			for i := int64(0); i < l; i++ {
				b[1+i] = byte(v >> (8 * (l - 1 - i)))
			}
			return b
		}
	}
	return nil
}

// emptyElem writes element id with empty data padded with Void to n bytes.
func emptyElem(id uint64, idLen int, n int64) []byte {
	idb := make([]byte, idLen)
	for i := 0; i < idLen; i++ {
		idb[i] = byte(id >> (8 * (idLen - 1 - i)))
	}
	for l := 1; l <= 8; l++ {
		head := append(append([]byte{}, idb...), sizeVint(0, l)...)
		rem := n - int64(len(head))
		if rem == 0 {
			return head
		}
		if rem >= 2 {
			return append(head, voidElem(rem)...)
		}
	}
	return nil
}

func sizeVint(v uint64, l int) []byte {
	b := make([]byte, l)
	v |= uint64(1) << (7 * l)
	for i := 0; i < l; i++ {
		b[i] = byte(v >> (8 * (l - 1 - i)))
	}
	return b
}

type mkvPlan struct {
	rep     report.Report
	patches []patch
	removed map[uint64]bool // segment-level IDs removed (for SeekHead)
	opts    Options
}

func (p *mkvPlan) void(e ebmlElem) {
	p.patches = append(p.patches, patch{off: e.off, data: voidElem(e.total())})
}

func (h mkvHandler) plan(r io.ReadSeeker, opts Options) (*mkvPlan, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, err
	}
	top, err := mkvChildren(r, 0, size)
	if err != nil {
		return nil, err
	}
	if len(top) == 0 || top[0].id != ebmlHeader {
		return nil, errors.New("mkv: missing EBML header")
	}
	format := "mkv"
	if hb, err := mkvRead(r, top[0], 4096); err == nil && bytes.Contains(hb, []byte("webm")) {
		format = "webm"
	}
	p := &mkvPlan{rep: report.New(format), removed: map[uint64]bool{}, opts: opts}
	var seekHeads []ebmlElem
	for _, seg := range top[1:] {
		if seg.id != idSegment {
			continue
		}
		kids, err := mkvChildren(r, seg.dataOff, seg.end())
		if err != nil {
			return nil, err
		}
		for _, e := range kids {
			switch e.id {
			case idSeekHead:
				seekHeads = append(seekHeads, e)
			case idInfo:
				if err := p.info(r, e); err != nil {
					return nil, err
				}
			case idTracks:
				if err := p.tracks(r, e); err != nil {
					return nil, err
				}
			case idTags:
				if err := p.tags(r, e); err != nil {
					return nil, err
				}
			case idAttachments:
				if err := p.attachments(r, e); err != nil {
					return nil, err
				}
			case idVoid:
				p.padding(r, e)
			}
		}
	}
	for _, sh := range seekHeads {
		if err := p.seekHead(r, sh); err != nil {
			return nil, err
		}
	}
	p.patches = mergePatches(p.patches)
	for _, pt := range p.patches {
		p.rep.Bytes += int64(len(pt.data))
	}
	return p, nil
}

func (p *mkvPlan) padding(r io.ReadSeeker, e ebmlElem) {
	if !p.opts.ZeroPad || e.size == 0 || e.size > maxMetaBox {
		return
	}
	b, err := mkvRead(r, e, maxMetaBox)
	if err != nil || allZero(b) {
		return
	}
	p.rep.Add(report.Binary(report.CatCustom, "mkv", "Void (non-zero)", e.size))
	p.patches = append(p.patches, patch{off: e.dataOff, data: make([]byte, e.size)})
}

func mkvString(b []byte) string { return strings.TrimRight(string(b), "\x00") }

func (p *mkvPlan) info(r io.ReadSeeker, info ebmlElem) error {
	kids, err := mkvChildren(r, info.dataOff, info.end())
	if err != nil {
		return err
	}
	edited := false
	var crc *ebmlElem
	for i, e := range kids {
		switch e.id {
		case idCRC32:
			crc = &kids[i]
		case idTitle:
			b, err := mkvRead(r, e, 1<<20)
			if err != nil || len(mkvString(b)) == 0 {
				continue
			}
			f := report.Text(report.CatIDTag, "mkv.info", "Title", mkvString(b))
			if p.opts.KeepTags {
				p.rep.Keep(f)
				continue
			}
			p.rep.Add(f)
			p.void(e)
			edited = true
		case idDateUTC:
			b, err := mkvRead(r, e, 8)
			if err != nil || len(b) != 8 {
				continue
			}
			ns := int64(binary.BigEndian.Uint64(b))
			t := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(ns))
			p.rep.Add(report.Text(report.CatTimestamp, "mkv.info", "DateUTC", t.Format(time.RFC3339)))
			p.void(e)
			edited = true
		case idMuxingApp, idWritingApp:
			b, err := mkvRead(r, e, 1<<20)
			if err != nil || len(mkvString(b)) == 0 {
				continue
			}
			name := "MuxingApp"
			if e.id == idWritingApp {
				name = "WritingApp"
			}
			p.rep.Add(report.Text(report.CatSoftware, "mkv.info", name, mkvString(b)))
			p.patches = append(p.patches, patch{off: e.off, data: emptyElem(e.id, e.idLen, e.total())})
			edited = true
		default:
			if meta, ok := mkvInfoOptional[e.id]; ok {
				b, _ := mkvRead(r, e, 1<<20)
				if s := mkvString(b); s != "" && report.Printable(s) && !strings.HasSuffix(meta.name, "UID") {
					p.rep.Add(report.Text(meta.cat, "mkv.info", meta.name, s))
				} else {
					p.rep.Add(report.Binary(meta.cat, "mkv.info", meta.name, e.size))
				}
				p.void(e)
				edited = true
			}
		}
	}
	if edited && crc != nil {
		p.void(*crc)
	}
	return nil
}

func (p *mkvPlan) tracks(r io.ReadSeeker, tracks ebmlElem) error {
	entries, err := mkvChildren(r, tracks.dataOff, tracks.end())
	if err != nil {
		return err
	}
	for _, te := range entries {
		if te.id != idTrackEntry {
			continue
		}
		kids, err := mkvChildren(r, te.dataOff, te.end())
		if err != nil {
			return err
		}
		edited := false
		var crc *ebmlElem
		for i, e := range kids {
			switch e.id {
			case idCRC32:
				crc = &kids[i]
			case idName, idCodecName:
				b, err := mkvRead(r, e, 1<<20)
				if err != nil || mkvString(b) == "" {
					continue
				}
				key, cat := "track Name", report.CatCustom
				if e.id == idCodecName {
					key, cat = "track CodecName", report.CatSoftware
				}
				p.rep.Add(report.Text(cat, "mkv", key, mkvString(b)))
				p.void(e)
				edited = true
			}
		}
		if edited && crc != nil {
			p.void(*crc)
		}
	}
	return nil
}

// tags reports every SimpleTag; with -k it rebuilds Tags in place holding
// only the essential SimpleTags (and their Targets).
func (p *mkvPlan) tags(r io.ReadSeeker, tags ebmlElem) error {
	tagList, err := mkvChildren(r, tags.dataOff, tags.end())
	if err != nil {
		return err
	}
	var kept []byte
	for _, tg := range tagList {
		if tg.id != idTag {
			continue
		}
		kids, err := mkvChildren(r, tg.dataOff, tg.end())
		if err != nil {
			return err
		}
		var targets, simples []byte
		for _, e := range kids {
			switch e.id {
			case idTargets:
				targets, _ = readAt(r, e.off, int(e.total()))
			case idSimpleTag:
				raw, keep, err := p.simpleTag(r, e)
				if err != nil {
					return err
				}
				if keep {
					simples = append(simples, raw...)
				}
			}
		}
		if simples != nil {
			kept = append(kept, ebmlBuild(idTag, 2, append(targets, simples...))...)
		}
	}
	p.removed[idTags] = kept == nil
	if kept != nil {
		body := ebmlBuild(idTags, 4, kept)
		if rem := tags.total() - int64(len(body)); rem == 0 || rem >= 2 {
			if rem >= 2 {
				body = append(body, voidElem(rem)...)
			}
			p.patches = append(p.patches, patch{off: tags.off, data: body})
			return nil
		}
		p.rep.Warn("not enough room to keep tags in place; all tags removed")
		p.removed[idTags] = true
	}
	p.void(tags)
	return nil
}

// simpleTag reports a SimpleTag (and nested ones) and returns the bytes to
// keep with -k (only for top-level essential tags).
func (p *mkvPlan) simpleTag(r io.ReadSeeker, st ebmlElem) ([]byte, bool, error) {
	kids, err := mkvChildren(r, st.dataOff, st.end())
	if err != nil {
		return nil, false, err
	}
	name, val := "", ""
	isBinary := false
	var binSize int64
	for _, e := range kids {
		switch e.id {
		case idTagName:
			b, _ := mkvRead(r, e, 1<<16)
			name = mkvString(b)
		case idTagString:
			b, _ := mkvRead(r, e, 1<<20)
			val = mkvString(b)
		case idTagBinary:
			isBinary, binSize = true, e.size
		case idSimpleTag:
			if _, _, err := p.simpleTag(r, e); err != nil {
				return nil, false, err
			}
		}
	}
	cat, canon := classify.Vorbis(name)
	f := report.Text(cat, "mkv.tag", name, val)
	if isBinary {
		f = report.Binary(cat, "mkv.tag", name, binSize)
	}
	if canon != "" && keepEssential(p.opts, canon) && !isBinary {
		if canon == classify.Year {
			if y := yearOnly(val); y != "" {
				p.rep.Keep(f)
				body := append(ebmlBuild(idTagName, 2, []byte(name)), ebmlBuild(idTagString, 2, []byte(y))...)
				return ebmlBuild(idSimpleTag, 2, body), true, nil
			}
		} else {
			p.rep.Keep(f)
			raw, err := readAt(r, st.off, int(st.total()))
			return raw, err == nil, err
		}
	}
	p.rep.Add(f)
	return nil, false, nil
}

func (p *mkvPlan) attachments(r io.ReadSeeker, att ebmlElem) error {
	files, err := mkvChildren(r, att.dataOff, att.end())
	if err != nil {
		return err
	}
	var fields []report.Field
	var keepPatches []patch
	for _, f := range files {
		if f.id != idAttached {
			continue
		}
		kids, err := mkvChildren(r, f.dataOff, f.end())
		if err != nil {
			return err
		}
		name, mime := "", ""
		var data, desc *ebmlElem
		for i, e := range kids {
			switch e.id {
			case idFileName:
				b, _ := mkvRead(r, e, 1<<16)
				name = mkvString(b)
			case idFileMime:
				b, _ := mkvRead(r, e, 1<<16)
				mime = mkvString(b)
			case idFileDesc:
				desc = &kids[i]
			case idFileData:
				data = &kids[i]
			}
		}
		cat := report.CatCustom
		if strings.HasPrefix(mime, "image/") {
			cat = report.CatThumbnail
		}
		var size int64
		if data != nil {
			size = data.size
		}
		fl := report.Binary(cat, "mkv.attachment", name+" ("+mime+")", size)
		if !p.opts.KeepThumbnails {
			fields = append(fields, fl)
			if desc != nil {
				b, _ := mkvRead(r, *desc, 1<<16)
				fields = append(fields, report.Text(report.CatCustom, "mkv.attachment", name+" description", mkvString(b)))
			}
			continue
		}
		// -t: keep the attachment but clean it.
		p.rep.Keep(fl)
		if desc != nil {
			b, _ := mkvRead(r, *desc, 1<<16)
			if s := mkvString(b); s != "" {
				p.rep.Add(report.Text(report.CatCustom, "mkv.attachment", name+" description", s))
				keepPatches = append(keepPatches, patch{off: desc.off, data: voidElem(desc.total())})
			}
		}
		if data != nil && cat == report.CatThumbnail {
			raw, err := mkvRead(r, *data, maxMetaBox)
			if err != nil {
				return err
			}
			out, sub, ok := StripEmbedded(name, raw, p.opts)
			if !ok {
				continue
			}
			if sub.TotalFields() > 0 || !sub.FullyClean {
				p.rep.AddEmbedded(sub)
			}
			if out == nil || bytes.Equal(out, raw) {
				continue
			}
			elem := ebmlBuild(idFileData, 2, out)
			rem := data.total() - int64(len(elem))
			switch {
			case rem == 0:
			case rem >= 2:
				elem = append(elem, voidElem(rem)...)
			default:
				p.rep.Unclean(fmt.Sprintf("attachment %s could not be cleaned in place", name))
				continue
			}
			keepPatches = append(keepPatches, patch{off: data.off, data: elem})
		}
	}
	if p.opts.KeepThumbnails {
		p.patches = append(p.patches, keepPatches...)
		return nil
	}
	p.rep.Add(fields...)
	p.removed[idAttachments] = true
	p.void(att)
	return nil
}

// seekHead voids Seek entries that point at removed elements.
func (p *mkvPlan) seekHead(r io.ReadSeeker, sh ebmlElem) error {
	seeks, err := mkvChildren(r, sh.dataOff, sh.end())
	if err != nil {
		return err
	}
	edited := false
	var crc *ebmlElem
	for i, s := range seeks {
		if s.id == idCRC32 {
			crc = &seeks[i]
			continue
		}
		if s.id != idSeek {
			continue
		}
		kids, err := mkvChildren(r, s.dataOff, s.end())
		if err != nil {
			return err
		}
		for _, k := range kids {
			if k.id != idSeekID {
				continue
			}
			b, err := mkvRead(r, k, 8)
			if err != nil {
				continue
			}
			var id uint64
			for _, c := range b {
				id = id<<8 | uint64(c)
			}
			if p.removed[id] {
				p.void(s)
				edited = true
			}
		}
	}
	if edited && crc != nil {
		p.void(*crc)
	}
	return nil
}

// ebmlBuild encodes an element with a fixed-width ID.
func ebmlBuild(id uint64, idLen int, data []byte) []byte {
	out := make([]byte, 0, idLen+8+len(data))
	for i := 0; i < idLen; i++ {
		out = append(out, byte(id>>(8*(idLen-1-i))))
	}
	l := 1
	for uint64(len(data)) >= (uint64(1)<<(7*l))-1 {
		l++
	}
	out = append(out, sizeVint(uint64(len(data)), l)...)
	return append(out, data...)
}

func (h mkvHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, nil
}

func (h mkvHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	size, err := streamSize(r)
	if err != nil {
		return p.rep, err
	}
	return p.rep, copyRange(r, w, 0, size, p.patches)
}
