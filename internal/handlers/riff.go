package handlers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/meta/id3"
	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/internal/report"
)

// RIFF formats (WAV, AVI) are cleaned in place: metadata chunks become JUNK
// chunks of the same size with zeroed payloads, so every offset (AVI
// indexes, OpenDML super-indexes, RF64 sizes) stays valid. Files do not
// shrink; bytes_saved is 0.

type riffChunk struct {
	id       string
	off      int64 // offset of the chunk header
	size     int64 // payload size
	listType string
	kids     []*riffChunk
}

func (c *riffChunk) payload() int64 { return c.off + 8 }
func (c *riffChunk) total() int64   { return 8 + c.size + c.size%2 }

const maxRIFFMeta = 64 << 20

// readRIFFChunks lists chunks in [start, end); expand decides which LISTs to
// descend into.
func readRIFFChunks(r io.ReadSeeker, start, end int64, expand func(listType string, depth int) bool, depth int) ([]*riffChunk, error) {
	var out []*riffChunk
	pos := start
	for end-pos >= 8 {
		h, err := readAt(r, pos, 8)
		if err != nil {
			return nil, err
		}
		c := &riffChunk{id: string(h[:4]), off: pos, size: int64(binary.LittleEndian.Uint32(h[4:]))}
		if c.payload()+c.size > end {
			if c.id == "data" || c.id == "movi" || c.id == "LIST" && depth == 0 {
				// Truncated/streamed final chunk: clamp to what exists.
				c.size = end - c.payload()
			} else {
				return nil, fmt.Errorf("riff: chunk %q at %d extends past its parent", c.id, pos)
			}
		}
		if c.id == "LIST" && c.size >= 4 {
			lt, err := readAt(r, c.payload(), 4)
			if err != nil {
				return nil, err
			}
			c.listType = string(lt)
			if expand(c.listType, depth) {
				kids, err := readRIFFChunks(r, c.payload()+4, c.payload()+c.size, expand, depth+1)
				if err != nil {
					return nil, err
				}
				c.kids = kids
			}
		}
		out = append(out, c)
		pos += c.total()
	}
	return out, nil
}

type riffPlan struct {
	rep     report.Report
	patches []patch
	opts    Options
	r       io.ReadSeeker
}

// junk turns a chunk into a zeroed JUNK chunk.
func (p *riffPlan) junk(c *riffChunk) {
	d := make([]byte, 8+c.size)
	copy(d, "JUNK")
	binary.LittleEndian.PutUint32(d[4:], uint32(c.size))
	p.patches = append(p.patches, patch{off: c.off, data: d})
}

// replace writes body (a full chunk) over c, filling the rest with JUNK.
// It returns false when the leftover space is too small for a JUNK header.
func (p *riffPlan) replace(c *riffChunk, body []byte) bool {
	room := 8 + c.size
	left := room - int64(len(body))
	if left != 0 && left < 8 {
		return false
	}
	d := append([]byte{}, body...)
	if left >= 8 {
		j := make([]byte, left)
		copy(j, "JUNK")
		binary.LittleEndian.PutUint32(j[4:], uint32(left-8))
		d = append(d, j...)
	}
	p.patches = append(p.patches, patch{off: c.off, data: d})
	return true
}

func (p *riffPlan) read(c *riffChunk) ([]byte, error) {
	if c.size > maxRIFFMeta {
		return nil, fmt.Errorf("riff: %s chunk too large", c.id)
	}
	return readAt(p.r, c.payload(), int(c.size))
}

func (p *riffPlan) padding(c *riffChunk) {
	if !p.opts.ZeroPad || c.size == 0 {
		return
	}
	b, err := p.read(c)
	if err != nil || allZero(b) {
		return
	}
	p.rep.Add(report.Binary(report.CatCustom, "riff", c.id+" (non-zero padding)", c.size))
	p.junk(c)
}

var riffInfoNames = map[string]struct {
	cat   report.Category
	canon string
}{
	"INAM": {report.CatIDTag, classify.Title}, "IART": {report.CatIDTag, classify.Artist},
	"IPRD": {report.CatIDTag, classify.Album}, "ITRK": {report.CatIDTag, classify.Track},
	"IPRT": {report.CatIDTag, classify.Track}, "ICRD": {report.CatTimestamp, classify.Year},
	"ISFT": {report.CatSoftware, ""}, "ICOP": {report.CatAuthor, ""}, "IENG": {report.CatAuthor, ""},
	"ITCH": {report.CatAuthor, ""}, "ICMS": {report.CatAuthor, ""}, "IARL": {report.CatGPS, ""},
	"ISRC": {report.CatCustom, ""}, "ICMT": {report.CatCustom, ""}, "IDIT": {report.CatTimestamp, ""},
}

// info handles a LIST INFO chunk: report every sub-chunk, keep essentials
// with -k, otherwise blank the whole list.
func (p *riffPlan) info(c *riffChunk) {
	var kept []byte
	for _, k := range c.kids {
		b, err := p.read(k)
		if err != nil {
			continue
		}
		v := strings.TrimRight(string(b), "\x00 ")
		meta := riffInfoNames[k.id]
		cat := meta.cat
		if cat == "" {
			cat = report.CatCustom
		}
		f := report.Text(cat, "riff.info", k.id, v)
		if meta.canon != "" && keepEssential(p.opts, meta.canon) {
			if meta.canon == classify.Year {
				v = yearOnly(v)
			}
			if v != "" {
				kept = append(kept, riffSub(k.id, []byte(v+"\x00"))...)
				p.rep.Keep(f)
				continue
			}
		}
		p.rep.Add(f)
	}
	if len(c.kids) == 0 {
		p.rep.Add(report.Binary(report.CatCustom, "riff", "LIST INFO (empty)", c.size))
	}
	if kept != nil {
		list := riffSub("LIST", append([]byte("INFO"), kept...))
		if p.replace(c, list) {
			return
		}
		p.rep.Warn("not enough room to keep INFO tags in place; all INFO tags removed")
	}
	if len(c.kids) > 0 || c.size > 4 {
		p.junk(c)
	}
}

func riffSub(id string, body []byte) []byte {
	h := make([]byte, 8)
	copy(h, id)
	binary.LittleEndian.PutUint32(h[4:], uint32(len(body)))
	out := append(h, body...)
	if len(body)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// bext reports a Broadcast Wave extension chunk.
func (p *riffPlan) bext(c *riffChunk) {
	b, err := p.read(c)
	if err != nil || len(b) < 602 {
		p.rep.Add(report.Binary(report.CatCustom, "bext", "bext", c.size))
		p.junk(c)
		return
	}
	str := func(x []byte) string { return strings.TrimRight(string(x), "\x00 ") }
	add := func(cat report.Category, k, v string) {
		if v != "" {
			p.rep.Add(report.Text(cat, "bext", k, v))
		}
	}
	add(report.CatCustom, "Description", str(b[0:256]))
	add(report.CatAuthor, "Originator", str(b[256:288]))
	add(report.CatAuthor, "OriginatorReference", str(b[288:320]))
	add(report.CatTimestamp, "OriginationDate", str(b[320:330]))
	add(report.CatTimestamp, "OriginationTime", str(b[330:338]))
	if !allZero(b[348:412]) {
		p.rep.Add(report.Binary(report.CatCustom, "bext", "UMID", 64))
	}
	add(report.CatSoftware, "CodingHistory", str(b[602:]))
	p.junk(c)
}

// id3chunk handles an ID3 tag embedded in a RIFF chunk. With -k the tag is
// rewritten in place (padded to the same size) holding only essentials.
func (p *riffPlan) id3chunk(c *riffChunk) {
	b, err := p.read(c)
	if err != nil {
		p.rep.Add(report.Binary(report.CatCustom, "riff", c.id, c.size))
		p.junk(c)
		return
	}
	var kept id3.Kept
	collectID3(b, p.opts, &p.rep, &kept)
	if !kept.Empty() {
		tag := id3.EncodeV24(kept, int(c.size))
		if int64(len(tag)) == c.size {
			p.patches = append(p.patches, patch{off: c.payload(), data: tag})
			return
		}
		p.rep.Warn("kept ID3 tags do not fit in the original chunk; removed")
	}
	p.junk(c)
}

func (p *riffPlan) simple(c *riffChunk, cat report.Category, ns string) {
	b, err := p.read(c)
	if err == nil && c.id == "_PMX" || err == nil && c.id == "XMP " {
		p.rep.Add(xmp.Fields(b)...)
	} else if err == nil && c.id == "DISP" && len(b) > 4 && report.Printable(strings.TrimRight(string(b[4:]), "\x00")) {
		p.rep.Add(report.Text(cat, ns, c.id, strings.TrimRight(string(b[4:]), "\x00")))
	} else if err == nil && (c.id == "IDIT" || c.id == "ISMP" || c.id == "strn") && report.Printable(strings.TrimRight(string(b), "\x00\n ")) {
		p.rep.Add(report.Text(cat, ns, c.id, strings.TrimRight(string(b), "\x00\n ")))
	} else {
		p.rep.Add(report.Binary(cat, ns, strings.TrimSpace(c.id), c.size))
	}
	p.junk(c)
}

func (p *riffPlan) finish(r io.ReadSeeker, w io.Writer) error {
	sort.Slice(p.patches, func(i, j int) bool { return p.patches[i].off < p.patches[j].off })
	ps := mergePatches(p.patches)
	size, err := streamSize(r)
	if err != nil {
		return err
	}
	return copyRange(r, w, 0, size, ps)
}

func riffMetaBytes(ps []patch) int64 {
	var n int64
	for _, p := range ps {
		n += int64(len(p.data))
	}
	return n
}

// ---------- WAV ----------

type wavHandler struct{}

func init() { Register(wavHandler{}) }

func (wavHandler) Name() string { return "wav" }

func (wavHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 12)
	return len(b) == 12 && (string(b[:4]) == "RIFF" || string(b[:4]) == "RF64") && string(b[8:]) == "WAVE" ||
		hasExt(path, ".wav", ".wave", ".bwf")
}

var wavMeta = map[string]report.Category{
	"bext": report.CatAuthor, "iXML": report.CatCustom, "axml": report.CatCustom, "aXML": report.CatCustom,
	"cart": report.CatAuthor, "DISP": report.CatCustom, "_PMX": report.CatCustom, "XMP ": report.CatCustom,
	"umid": report.CatCustom, "LGWV": report.CatSoftware, "ResU": report.CatSoftware, "id3 ": report.CatCustom,
	"ID3 ": report.CatCustom,
}

var riffPads = map[string]bool{"JUNK": true, "junk": true, "PAD ": true, "pad ": true, "FLLR": true}

func (h wavHandler) plan(r io.ReadSeeker, opts Options) (*riffPlan, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, err
	}
	hdr, err := readAt(r, 0, 12)
	if err != nil || string(hdr[8:]) != "WAVE" {
		return nil, errors.New("wav: bad header")
	}
	end := 8 + int64(binary.LittleEndian.Uint32(hdr[4:]))
	if string(hdr[:4]) == "RF64" || end > size {
		end = size
	}
	chunks, err := readRIFFChunks(r, 12, end, func(lt string, d int) bool { return lt == "INFO" }, 0)
	if err != nil {
		return nil, fmt.Errorf("wav: %w", err)
	}
	p := &riffPlan{rep: report.New("wav"), opts: opts, r: r}
	hasData := false
	for _, c := range chunks {
		switch {
		case c.id == "data":
			hasData = true
		case c.id == "LIST" && c.listType == "INFO":
			p.info(c)
		case c.id == "LIST" && c.listType == "adtl":
		case c.id == "LIST":
			p.simple(c, report.CatCustom, "riff")
		case c.id == "bext":
			p.bext(c)
		case c.id == "id3 " || c.id == "ID3 ":
			p.id3chunk(c)
		case riffPads[c.id]:
			p.padding(c)
		default:
			if cat, ok := wavMeta[c.id]; ok {
				p.simple(c, cat, "riff")
			}
		}
	}
	if !hasData {
		return nil, errors.New("wav: no data chunk")
	}
	p.rep.Bytes = riffMetaBytes(p.patches)
	return p, nil
}

func (h wavHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, nil
}

func (h wavHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, p.finish(r, w)
}

// ---------- AVI ----------

type aviHandler struct{}

func init() { Register(aviHandler{}) }

func (aviHandler) Name() string { return "avi" }

func (aviHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 12)
	return len(b) == 12 && string(b[:4]) == "RIFF" && string(b[8:]) == "AVI " || hasExt(path, ".avi")
}

func (h aviHandler) plan(r io.ReadSeeker, opts Options) (*riffPlan, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, err
	}
	p := &riffPlan{rep: report.New("avi"), opts: opts, r: r}
	expand := func(lt string, d int) bool { return lt == "hdrl" || lt == "strl" || lt == "INFO" }
	var pos int64
	first := true
	for size-pos >= 12 {
		hdr, err := readAt(r, pos, 12)
		if err != nil {
			return nil, err
		}
		if string(hdr[:4]) != "RIFF" {
			if allZeroAt(r, pos, size) {
				break
			}
			return nil, fmt.Errorf("avi: unexpected data at %d", pos)
		}
		form := string(hdr[8:])
		if first && form != "AVI " || !first && form != "AVIX" {
			return nil, fmt.Errorf("avi: unexpected RIFF form %q", form)
		}
		end := pos + 8 + int64(binary.LittleEndian.Uint32(hdr[4:]))
		if end > size {
			end = size
		}
		chunks, err := readRIFFChunks(r, pos+12, end, expand, 0)
		if err != nil {
			return nil, fmt.Errorf("avi: %w", err)
		}
		for _, c := range chunks {
			switch {
			case c.id == "LIST" && c.listType == "hdrl":
				p.aviHeader(c)
			case c.id == "LIST" && (c.listType == "movi" || c.listType == "odml"):
			case c.id == "LIST" && c.listType == "INFO":
				p.info(c)
			case c.id == "idx1" || c.id == "ix00":
			case riffPads[c.id]:
				p.padding(c)
			case c.id == "LIST":
				p.rep.Add(report.Binary(report.CatCustom, "avi", "LIST "+c.listType, c.size))
				p.junk(c)
			default:
				p.simple(c, report.CatCustom, "avi")
			}
		}
		pos = end + (end-pos)%2
		first = false
	}
	if first {
		return nil, errors.New("avi: bad header")
	}
	p.rep.Bytes = riffMetaBytes(p.patches)
	return p, nil
}

func allZeroAt(r io.ReadSeeker, pos, size int64) bool {
	if size-pos > 1<<20 {
		return false
	}
	b, err := readAt(r, pos, int(size-pos))
	return err == nil && allZero(b)
}

func (p *riffPlan) aviHeader(hdrl *riffChunk) {
	for _, c := range hdrl.kids {
		switch {
		case c.id == "IDIT" || c.id == "ISMP":
			p.simple(c, report.CatTimestamp, "avi")
		case c.id == "LIST" && c.listType == "strl":
			for _, s := range c.kids {
				switch {
				case s.id == "strn":
					p.simple(s, report.CatCustom, "avi")
				case s.id == "strd":
					if b, err := p.read(s); err == nil && !allZero(b) {
						p.rep.Unclean(fmt.Sprintf("strd codec/camera data chunk (%d bytes) kept: removing it can break decoding", s.size))
					}
				case riffPads[s.id]:
					p.padding(s)
				}
			}
		case riffPads[c.id]:
			p.padding(c)
		}
	}
}

func (h aviHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, nil
}

func (h aviHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, p.finish(r, w)
}
