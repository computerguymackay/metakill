package handlers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/computerguymackay/metakill/internal/meta/vorbis"
	"github.com/computerguymackay/metakill/report"
)

// oggHandler rewrites the comment header of every Vorbis, Opus, Theora and
// Speex logical stream (vendor blanked; essential tags kept with -k;
// pictures kept with -t). Header packets after the first page are
// re-paginated and later pages of that stream renumbered with fresh CRCs.
// Other pages are copied unchanged. Ogg FLAC is refused.
type oggHandler struct{}

func init() { Register(oggHandler{}) }

func (oggHandler) Name() string { return "ogg" }

func (oggHandler) CanHandle(r io.ReadSeeker, path string) bool {
	return bytes.Equal(peek(r, 4), []byte("OggS")) || hasExt(path, ".ogg", ".oga", ".ogv", ".opus", ".spx")
}

type oggPage struct {
	headerType byte
	granule    uint64
	serial     uint32
	seq        uint32
	segments   []byte
	data       []byte
}

func readOggPage(r io.Reader) (*oggPage, error) {
	var h [27]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	if string(h[:4]) != "OggS" || h[4] != 0 {
		return nil, errors.New("ogg: lost page sync")
	}
	p := &oggPage{
		headerType: h[5],
		granule:    binary.LittleEndian.Uint64(h[6:]),
		serial:     binary.LittleEndian.Uint32(h[14:]),
		seq:        binary.LittleEndian.Uint32(h[18:]),
	}
	p.segments = make([]byte, h[26])
	if _, err := io.ReadFull(r, p.segments); err != nil {
		return nil, err
	}
	n := 0
	for _, s := range p.segments {
		n += int(s)
	}
	p.data = make([]byte, n)
	if _, err := io.ReadFull(r, p.data); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *oggPage) encode() []byte {
	out := make([]byte, 27, 27+len(p.segments)+len(p.data))
	copy(out, "OggS")
	out[5] = p.headerType
	binary.LittleEndian.PutUint64(out[6:], p.granule)
	binary.LittleEndian.PutUint32(out[14:], p.serial)
	binary.LittleEndian.PutUint32(out[18:], p.seq)
	out[26] = byte(len(p.segments))
	out = append(out, p.segments...)
	out = append(out, p.data...)
	binary.LittleEndian.PutUint32(out[22:], oggCRC(out))
	return out
}

var oggTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04C11DB7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, c := range b {
		crc = crc<<8 ^ oggTable[byte(crc>>24)^c]
	}
	return crc
}

// packets splits page data into packets; complete reports whether the last
// packet ends on this page.
func (p *oggPage) packets() (pkts [][]byte, lastComplete bool) {
	pos, cur := 0, []byte(nil)
	for i, s := range p.segments {
		cur = append(cur, p.data[pos:pos+int(s)]...)
		pos += int(s)
		if s < 255 {
			pkts = append(pkts, cur)
			cur = nil
		}
		if i == len(p.segments)-1 && s == 255 {
			pkts = append(pkts, cur)
			return pkts, false
		}
	}
	return pkts, true
}

type oggCodec struct {
	name    string
	headers int
	// comment returns the comment block within packet 2 and wraps a new one.
	prefix  []byte
	framing bool
}

func identifyOgg(first []byte) (*oggCodec, error) {
	switch {
	case bytes.HasPrefix(first, []byte("\x01vorbis")):
		return &oggCodec{name: "vorbis", headers: 3, prefix: []byte("\x03vorbis"), framing: true}, nil
	case bytes.HasPrefix(first, []byte("OpusHead")):
		return &oggCodec{name: "opus", headers: 2, prefix: []byte("OpusTags")}, nil
	case bytes.HasPrefix(first, []byte("\x80theora")):
		return &oggCodec{name: "theora", headers: 3, prefix: []byte("\x81theora")}, nil
	case bytes.HasPrefix(first, []byte("Speex   ")):
		return &oggCodec{name: "speex", headers: 2}, nil
	case bytes.HasPrefix(first, []byte("\x7fFLAC")):
		return nil, fmt.Errorf("ogg: Ogg FLAC streams are %w", ErrUnsupported)
	}
	return nil, nil
}

type oggStream struct {
	codec      *oggCodec
	collecting bool
	packets    [][]byte
	partial    []byte
	consumed   int // header pages swallowed
	seqDelta   int64
	firstSeq   uint32
}

// processOgg walks every page. When w is nil it only inspects.
func processOgg(r io.Reader, w io.Writer, opts Options) (report.Report, error) {
	rep := report.New("ogg")
	streams := map[uint32]*oggStream{}
	for {
		pg, err := readOggPage(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return rep, errors.New("ogg: truncated page")
			}
			return rep, err
		}
		st := streams[pg.serial]
		if pg.headerType&2 != 0 { // beginning of stream
			pk, _ := pg.packets()
			if len(pk) == 0 {
				return rep, errors.New("ogg: empty BOS page")
			}
			codec, err := identifyOgg(pk[0])
			if err != nil {
				return rep, err
			}
			st = &oggStream{codec: codec, collecting: codec != nil, firstSeq: pg.seq}
			streams[pg.serial] = st
			if codec == nil && !bytes.HasPrefix(pk[0], []byte("fishead\x00")) { // Skeleton carries no tags
				rep.Unclean(fmt.Sprintf("logical stream %08x has an unrecognised codec; its headers were not inspected", pg.serial))
			} else if len(pk) != 1 {
				return rep, errors.New("ogg: first page must contain only the identification header")
			}
			if w != nil {
				if _, err := w.Write(pg.encode()); err != nil {
					return rep, err
				}
			}
			continue
		}
		if st == nil {
			return rep, fmt.Errorf("ogg: page for unknown stream %08x", pg.serial)
		}
		if st.collecting {
			st.consumed++
			pk, complete := pg.packets()
			for i, p := range pk {
				if i == 0 && st.partial != nil {
					p = append(st.partial, p...)
					st.partial = nil
				}
				if i == len(pk)-1 && !complete {
					st.partial = p
					break
				}
				st.packets = append(st.packets, p)
			}
			if len(st.packets) < st.codec.headers-1 {
				continue
			}
			if len(st.packets) > st.codec.headers-1 || st.partial != nil {
				return rep, fmt.Errorf("ogg: %s audio data shares a page with headers (%w)", st.codec.name, ErrUnsupported)
			}
			st.collecting = false
			newPkts, err := oggComment(st.codec, st.packets, opts, &rep)
			if err != nil {
				return rep, err
			}
			pages := paginate(pg.serial, st.firstSeq+1, newPkts)
			st.seqDelta = int64(len(pages)) - int64(st.consumed)
			if w != nil {
				for _, np := range pages {
					if _, err := w.Write(np.encode()); err != nil {
						return rep, err
					}
				}
			}
			continue
		}
		if w == nil {
			continue
		}
		if st.seqDelta != 0 {
			pg.seq = uint32(int64(pg.seq) + st.seqDelta)
		}
		if _, err := w.Write(pg.encode()); err != nil {
			return rep, err
		}
	}
	for serial, st := range streams {
		if st.collecting {
			return rep, fmt.Errorf("ogg: stream %08x ended before its headers were complete", serial)
		}
	}
	return rep, nil
}

// oggComment rewrites the comment packet (packets[0] is header #2).
func oggComment(c *oggCodec, pkts [][]byte, opts Options, rep *report.Report) ([][]byte, error) {
	pkt := pkts[0]
	if !bytes.HasPrefix(pkt, c.prefix) {
		return nil, fmt.Errorf("ogg: %s comment header missing", c.name)
	}
	body := pkt[len(c.prefix):]
	blk, n, err := vorbis.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("ogg: %s comments: %w", c.name, err)
	}
	rest := body[n:]
	if c.name == "opus" && len(rest) > 0 && !allZero(rest) {
		rep.Add(report.Binary(report.CatCustom, "opus", "OpusTags extra data", int64(len(rest))))
	}
	out, _ := scrubVorbis(blk, c.name, opts, rep)
	np := append(append([]byte{}, c.prefix...), vorbis.Encode(out)...)
	if c.framing {
		np = append(np, 1)
	}
	return append([][]byte{np}, pkts[1:]...), nil
}

// paginate lays header packets out one per page (each packet starts a new
// page and the last page of each packet completes it).
func paginate(serial, seq uint32, pkts [][]byte) []*oggPage {
	var pages []*oggPage
	for _, pkt := range pkts {
		rest := pkt
		first := true
		for {
			pg := &oggPage{serial: serial, seq: seq}
			seq++
			if !first {
				pg.headerType = 1 // continued packet
			}
			first = false
			for len(pg.segments) < 255 {
				n := len(rest)
				if n > 255 {
					n = 255
				}
				pg.segments = append(pg.segments, byte(n))
				pg.data = append(pg.data, rest[:n]...)
				rest = rest[n:]
				if n < 255 {
					rest = nil
					break
				}
			}
			done := rest == nil
			if done {
				pg.granule = 0
			} else {
				pg.granule = ^uint64(0) // no packet ends on this page
			}
			pages = append(pages, pg)
			if done {
				break
			}
		}
	}
	return pages
}

func (oggHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return report.Report{}, err
	}
	rep, err := processOgg(bufReader(r), nil, opts)
	if err != nil {
		return rep, err
	}
	var cw countWriter
	if _, err := r.Seek(0, io.SeekStart); err == nil {
		if _, err := processOgg(bufReader(r), &cw, opts); err == nil {
			if size, err := streamSize(r); err == nil && size > cw.n {
				rep.Bytes = size - cw.n
			}
		}
	}
	return rep, nil
}

func (oggHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return report.Report{}, err
	}
	bw := newBufWriter(w)
	rep, err := processOgg(bufReader(r), bw, opts)
	if err != nil {
		return rep, err
	}
	return rep, bw.Flush()
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
