package handlers

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/computerguymackay/metakill/internal/meta/ape"
	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/meta/id3"
	"github.com/computerguymackay/metakill/internal/report"
)

// mp3Handler removes ID3v2 tags (leading and appended), ID3v1, ID3v1
// extended ("TAG+"), APE and Lyrics3v2 tags, and blanks the encoder string
// in the LAME/Xing info frame (recomputing its CRC). With -k a fresh minimal
// ID3v2.4 tag carries the kept fields. Audio frames are streamed unchanged.
type mp3Handler struct{}

func init() { Register(mp3Handler{}) }

func (mp3Handler) Name() string { return "mp3" }

func (mp3Handler) CanHandle(r io.ReadSeeker, path string) bool {
	pos := skipID3(r)
	b, err := readAt(r, pos, 4)
	if err == nil && isMPEGAudioFrame(b) {
		return true
	}
	return hasExt(path, ".mp3", ".mp2")
}

// skipID3 returns the offset just past any leading ID3v2 tags.
func skipID3(r io.ReadSeeker) int64 {
	var pos int64
	for i := 0; i < 8; i++ {
		h, err := readAt(r, pos, id3.HeaderSize)
		if err != nil {
			break
		}
		n, ok := id3.TagSize(h)
		if !ok {
			break
		}
		pos += int64(n)
	}
	return pos
}

// isMPEGAudioFrame checks for an MPEG-1/2/2.5 Layer I-III frame header.
func isMPEGAudioFrame(b []byte) bool {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return false
	}
	version := (b[1] >> 3) & 3
	layer := (b[1] >> 1) & 3
	bitrate := b[2] >> 4
	rate := (b[2] >> 2) & 3
	return version != 1 && layer != 0 && bitrate != 15 && rate != 3
}

func readAt(r io.ReadSeeker, off int64, n int) ([]byte, error) {
	if off < 0 {
		return nil, errors.New("negative offset")
	}
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

type patch struct {
	off  int64
	data []byte
}

type mp3Plan struct {
	rep        report.Report
	start, end int64
	newTag     []byte
	patches    []patch
}

const maxTagBytes = 256 << 20

func (mp3Handler) plan(r io.ReadSeeker, opts Options) (*mp3Plan, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, err
	}
	p := &mp3Plan{rep: report.New("mp3"), end: size}
	var kept id3.Kept

	// Leading ID3v2 tags.
	for {
		h, err := readAt(r, p.start, id3.HeaderSize)
		if err != nil {
			break
		}
		n, ok := id3.TagSize(h)
		if !ok {
			break
		}
		if int64(n) > size-p.start || n > maxTagBytes {
			return nil, errors.New("mp3: ID3v2 tag extends past end of file")
		}
		b, err := readAt(r, p.start, n)
		if err != nil {
			return nil, err
		}
		collectID3(b, opts, &p.rep, &kept)
		p.start += int64(n)
	}

	// Trailing tags, in any order.
	for changed := true; changed; {
		changed = false
		if p.end-p.start >= 128 {
			b, _ := readAt(r, p.end-128, 128)
			if v, ok := id3.ParseV1(b); ok {
				for _, f := range v.Fields() {
					canon := strings.ToLower(f.Key)
					if f.Value != nil && keepEssential(opts, canon) {
						val := *f.Value
						if canon == classify.Year {
							val = yearOnly(val)
						}
						if val != "" {
							kept.Set(canon, val)
							p.rep.Keep(f)
							continue
						}
					}
					p.rep.Add(f)
				}
				p.end -= 128
				changed = true
				if p.end-p.start >= 227 {
					if b, _ := readAt(r, p.end-227, 4); string(b) == "TAG+" {
						p.rep.Add(report.Binary(report.CatCustom, "id3v1", "TAG+ (extended)", 227))
						p.end -= 227
					}
				}
				continue
			}
		}
		if p.end-p.start >= ape.FooterSize {
			b, _ := readAt(r, p.end-ape.FooterSize, ape.FooterSize)
			if f, ok := ape.ParseFooter(b); ok && int64(f.TotalSize()) <= p.end-p.start {
				items, err := readAt(r, p.end-int64(f.Size), f.Size-ape.FooterSize)
				if err == nil {
					for _, it := range ape.Items(items, f.Count) {
						field, canon := it.Describe()
						if canon != "" && keepEssential(opts, canon) && field.Value != nil {
							v := *field.Value
							if canon == classify.Year {
								v = yearOnly(v)
							}
							if v != "" {
								kept.Set(canon, v)
								p.rep.Keep(field)
								continue
							}
						}
						p.rep.Add(field)
					}
				}
				if f.Count == 0 {
					p.rep.Add(report.Binary(report.CatCustom, "ape", "APE tag (empty)", int64(f.TotalSize())))
				}
				p.end -= int64(f.TotalSize())
				changed = true
				continue
			}
		}
		if p.end-p.start >= 15 {
			b, _ := readAt(r, p.end-15, 15)
			if string(b[6:]) == "LYRICS200" {
				if n, err := strconv.Atoi(string(b[:6])); err == nil && int64(n+15) <= p.end-p.start {
					if s, _ := readAt(r, p.end-int64(n+15), 11); string(s) == "LYRICSBEGIN" {
						p.rep.Add(report.Binary(report.CatCustom, "lyrics3", "Lyrics3v2", int64(n+15)))
						p.end -= int64(n + 15)
						changed = true
						continue
					}
				}
			}
		}
		if p.end-p.start >= 20 {
			b, _ := readAt(r, p.end-10, 10)
			if string(b[:3]) == "3DI" {
				hb := append([]byte("ID3"), b[3:]...)
				if n, ok := id3.TagSize(hb); ok && int64(n) <= p.end-p.start {
					tag, err := readAt(r, p.end-int64(n), n)
					if err == nil {
						collectID3(tag, opts, &p.rep, &kept)
					}
					p.end -= int64(n)
					changed = true
				}
			}
		}
	}
	if p.end < p.start {
		return nil, errors.New("mp3: tags overlap")
	}

	// LAME / Xing info frame encoder string.
	if hdr, err := readAt(r, p.start, 4); err == nil && isMPEGAudioFrame(hdr) {
		if pt, f := lamePatch(r, p.start, hdr); pt != nil {
			p.rep.Add(f)
			p.patches = append(p.patches, *pt)
		}
	} else if p.end > p.start {
		p.rep.Warn("no MPEG audio frame found right after the tags")
	}

	if !kept.Empty() {
		p.newTag = id3.EncodeV24(kept, 0)
	}
	p.rep.Bytes = p.start + (size - p.end) - int64(len(p.newTag))
	if p.rep.Bytes < 0 {
		p.rep.Bytes = 0
	}
	return p, nil
}

// collectID3 reports every frame of an ID3v2 tag and gathers kept values.
func collectID3(b []byte, opts Options, rep *report.Report, kept *id3.Kept) {
	tag, err := id3.Parse(b)
	if err != nil {
		rep.Add(report.Binary(report.CatCustom, "id3v2", "ID3v2 (unparseable)", int64(len(b))))
		return
	}
	if len(tag.Frames) == 0 {
		rep.Add(report.Binary(report.CatCustom, fmt.Sprintf("id3v2.%d", tag.Major), "ID3v2 (empty)", int64(len(b))))
	}
	for _, fr := range tag.Frames {
		info := id3.Describe(fr, tag.Major)
		if info.Canonical != "" && keepEssential(opts, info.Canonical) {
			v := info.Value
			if info.Canonical == classify.Year {
				v = yearOnly(v)
			}
			if v != "" {
				kept.Set(info.Canonical, v)
				rep.Keep(info.Field)
				continue
			}
		}
		if info.Picture != nil && opts.KeepThumbnails && kept.Picture == nil {
			pic := *info.Picture
			if out, sub, ok := StripEmbedded("cover art", pic.Data, opts); ok {
				if out != nil {
					pic.Data = out
				}
				if sub.TotalFields() > 0 || !sub.FullyClean {
					rep.AddEmbedded(sub)
				}
			}
			kept.Picture = &pic
			rep.Keep(info.Field)
			continue
		}
		rep.Add(info.Field)
	}
}

// lamePatch blanks the encoder version string of a LAME/Xing info frame.
func lamePatch(r io.ReadSeeker, start int64, hdr []byte) (*patch, report.Field) {
	version := (hdr[1] >> 3) & 3
	layer := (hdr[1] >> 1) & 3
	if layer != 1 { // only Layer III carries a Xing/LAME header
		return nil, report.Field{}
	}
	mono := hdr[3]>>6 == 3
	side := 32
	switch {
	case version == 3 && mono:
		side = 17
	case version != 3 && mono:
		side = 9
	case version != 3:
		side = 17
	}
	xo := 4 + side
	if hdr[1]&1 == 0 {
		xo += 2 // CRC follows the header
	}
	buf, err := readAt(r, start, xo+8+120+36)
	if err != nil {
		return nil, report.Field{}
	}
	tag := string(buf[xo : xo+4])
	if tag != "Xing" && tag != "Info" {
		return nil, report.Field{}
	}
	flags := binary.BigEndian.Uint32(buf[xo+4:])
	p := xo + 8
	if flags&1 != 0 {
		p += 4
	}
	if flags&2 != 0 {
		p += 4
	}
	if flags&4 != 0 {
		p += 100
	}
	if flags&8 != 0 {
		p += 4
	}
	if p+36 > len(buf) {
		return nil, report.Field{}
	}
	enc := buf[p : p+9]
	if allZero(enc) {
		return nil, report.Field{}
	}
	f := report.Text(report.CatSoftware, "lame", "EncoderVersion", strings.TrimRight(string(enc), "\x00 "))
	out := append([]byte{}, buf[:p+36]...)
	crcOK := crc16(buf[:p+34]) == binary.BigEndian.Uint16(buf[p+34:])
	for i := p; i < p+9; i++ {
		out[i] = 0
	}
	if crcOK {
		binary.BigEndian.PutUint16(out[p+34:], crc16(out[:p+34]))
	}
	return &patch{off: start, data: out}, f
}

// crc16 is the CRC-16/ARC used by the LAME info tag.
func crc16(b []byte) uint16 {
	var crc uint16
	for _, c := range b {
		crc ^= uint16(c)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func (h mp3Handler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, nil
}

func (h mp3Handler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	if _, err := w.Write(p.newTag); err != nil {
		return p.rep, err
	}
	err = copyRange(r, w, p.start, p.end, p.patches)
	return p.rep, err
}

// copyRange copies [start, end) from r to w, overlaying patches (absolute
// offsets in r).
func copyRange(r io.ReadSeeker, w io.Writer, start, end int64, patches []patch) error {
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return err
	}
	pos := start
	for _, pt := range patches {
		if pt.off < pos || pt.off+int64(len(pt.data)) > end {
			return errors.New("internal: patch out of range")
		}
		if _, err := io.CopyN(w, r, pt.off-pos); err != nil {
			return err
		}
		if _, err := w.Write(pt.data); err != nil {
			return err
		}
		if _, err := r.Seek(int64(len(pt.data)), io.SeekCurrent); err != nil {
			return err
		}
		pos = pt.off + int64(len(pt.data))
	}
	_, err := io.CopyN(w, r, end-pos)
	return err
}
