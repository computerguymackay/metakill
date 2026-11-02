package handlers

import (
	"errors"
	"fmt"
	"io"

	"github.com/computerguymackay/metakill/internal/meta/id3"
	"github.com/computerguymackay/metakill/internal/meta/vorbis"
	"github.com/computerguymackay/metakill/report"
)

// flacHandler rewrites the FLAC metadata blocks: VORBIS_COMMENT (vendor
// blanked; essential tags kept with -k), PICTURE (kept and cleaned with
// -t), APPLICATION blocks removed, PADDING zeroed. Stray ID3 tags before or
// after the stream are removed. Audio frames are streamed unchanged.
type flacHandler struct{}

func init() { Register(flacHandler{}) }

func (flacHandler) Name() string { return "flac" }

func (flacHandler) CanHandle(r io.ReadSeeker, path string) bool {
	pos := skipID3(r)
	b, err := readAt(r, pos, 4)
	return err == nil && string(b) == "fLaC" || hasExt(path, ".flac")
}

const (
	flacStreamInfo = 0
	flacPadding    = 1
	flacApp        = 2
	flacSeekTable  = 3
	flacComment    = 4
	flacCueSheet   = 5
	flacPicture    = 6
)

type flacPlan struct {
	rep        report.Report
	blocks     [][]byte // new metadata blocks (header without last flag + body)
	audioStart int64
	audioEnd   int64
}

func (flacHandler) plan(r io.ReadSeeker, opts Options) (*flacPlan, error) {
	size, err := streamSize(r)
	if err != nil {
		return nil, err
	}
	p := &flacPlan{rep: report.New("flac"), audioEnd: size}
	var kept id3.Kept
	pos := int64(0)
	for {
		h, err := readAt(r, pos, id3.HeaderSize)
		if err != nil {
			break
		}
		n, ok := id3.TagSize(h)
		if !ok {
			break
		}
		b, err := readAt(r, pos, n)
		if err != nil {
			return nil, err
		}
		collectID3(b, Options{}, &p.rep, &kept) // FLAC carries tags in Vorbis comments
		pos += int64(n)
	}
	if b, err := readAt(r, pos, 4); err != nil || string(b) != "fLaC" {
		return nil, errors.New("flac: missing fLaC marker")
	}
	pos += 4
	for last := false; !last; {
		h, err := readAt(r, pos, 4)
		if err != nil {
			return nil, errors.New("flac: truncated metadata")
		}
		last = h[0]&0x80 != 0
		typ := h[0] & 0x7F
		n := int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		if pos+4+int64(n) > size {
			return nil, errors.New("flac: metadata block extends past end of file")
		}
		body, err := readAt(r, pos+4, n)
		if err != nil {
			return nil, err
		}
		pos += 4 + int64(n)
		switch typ {
		case flacStreamInfo, flacSeekTable, flacCueSheet:
			p.add(typ, body)
		case flacPadding:
			if opts.ZeroPad && !allZero(body) {
				p.rep.Add(report.Binary(report.CatCustom, "flac", "PADDING (non-zero)", int64(n)))
				body = make([]byte, n)
			}
			p.add(typ, body)
		case flacApp:
			id := "APPLICATION"
			if n >= 4 && report.Printable(string(body[:4])) {
				id += ":" + string(body[:4])
			}
			p.rep.Add(report.Binary(report.CatCustom, "flac", id, int64(n)))
		case flacComment:
			blk, _, err := vorbis.Parse(body)
			if err != nil {
				p.rep.Add(report.Binary(report.CatCustom, "flac", "VORBIS_COMMENT (unparseable)", int64(n)))
				continue
			}
			if out, keep := scrubVorbis(blk, "vorbis", opts, &p.rep); keep {
				p.add(typ, vorbis.Encode(out))
			}
		case flacPicture:
			pic, err := vorbis.ParsePicture(body)
			f := report.Binary(report.CatThumbnail, "flac", "PICTURE", int64(len(pic.Data)))
			if err != nil || !opts.KeepThumbnails {
				p.rep.Add(f)
				continue
			}
			p.rep.Keep(f)
			p.add(typ, vorbis.EncodePicture(cleanPicture(pic, opts, &p.rep)))
		default:
			p.rep.Add(report.Binary(report.CatCustom, "flac", fmt.Sprintf("block type %d", typ), int64(n)))
		}
	}
	p.audioStart = pos
	if size-pos >= 128 {
		if b, _ := readAt(r, size-128, 128); b != nil {
			if v, ok := id3.ParseV1(b); ok {
				p.rep.Add(v.Fields()...)
				p.audioEnd = size - 128
			}
		}
	}
	newLen := int64(4)
	for _, b := range p.blocks {
		newLen += int64(len(b))
	}
	p.rep.Bytes = p.audioStart - newLen + (size - p.audioEnd)
	if p.rep.Bytes < 0 {
		p.rep.Bytes = 0
	}
	return p, nil
}

func (p *flacPlan) add(typ byte, body []byte) {
	h := []byte{typ, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	p.blocks = append(p.blocks, append(h, body...))
}

func (h flacHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	return p.rep, nil
}

func (h flacHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	p, err := h.plan(r, opts)
	if err != nil {
		return report.Report{}, err
	}
	if len(p.blocks) == 0 || p.blocks[0][0] != flacStreamInfo {
		return p.rep, errors.New("flac: STREAMINFO must be the first block")
	}
	if _, err := w.Write([]byte("fLaC")); err != nil {
		return p.rep, err
	}
	for i, b := range p.blocks {
		if i == len(p.blocks)-1 {
			b[0] |= 0x80
		}
		if _, err := w.Write(b); err != nil {
			return p.rep, err
		}
	}
	return p.rep, copyRange(r, w, p.audioStart, p.audioEnd, nil)
}
