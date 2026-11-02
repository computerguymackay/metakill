package handlers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/report"
)

// webpHandler drops EXIF, XMP, ICCP (unless -c) and unknown chunks, and
// clears the matching VP8X feature flags.
type webpHandler struct{}

func init() { Register(webpHandler{}) }

func (webpHandler) Name() string { return "webp" }

func (webpHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 12)
	return len(b) == 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" || hasExt(path, ".webp")
}

func (webpHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	_, rep, err := processWebP(data, opts)
	return rep, err
}

func (webpHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	out, rep, err := processWebP(data, opts)
	if err != nil {
		return rep, err
	}
	_, err = w.Write(out)
	return rep, err
}

var webpKeep = map[string]bool{"VP8 ": true, "VP8L": true, "VP8X": true, "ALPH": true, "ANIM": true, "ANMF": true}

const (
	vp8xICC  = 0x20
	vp8xEXIF = 0x08
	vp8xXMP  = 0x04
)

func processWebP(b []byte, opts Options) ([]byte, report.Report, error) {
	rep := report.New("webp")
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return nil, rep, errors.New("webp: bad header")
	}
	end := 8 + int(binary.LittleEndian.Uint32(b[4:]))
	if end > len(b) {
		return nil, rep, errors.New("webp: truncated")
	}
	out := append([]byte{}, b[:12]...)
	vp8x := -1
	keptICC := false
	for p := 12; p+8 <= end; {
		id := string(b[p : p+4])
		n := int(binary.LittleEndian.Uint32(b[p+4:]))
		total := 8 + n + n%2
		if n < 0 || p+8+n > end {
			return nil, rep, fmt.Errorf("webp: chunk %q truncated", id)
		}
		if p+total > end {
			total = 8 + n
		}
		body := b[p+8 : p+8+n]
		chunk := b[p : p+total]
		p += total
		switch {
		case webpKeep[id]:
			if id == "VP8X" {
				vp8x = len(out)
			}
			out = append(out, chunk...)
		case id == "ICCP":
			f := report.Binary(report.CatICC, "webp", "ICCP", int64(n))
			if opts.KeepICC {
				rep.Keep(f)
				keptICC = true
				out = append(out, chunk...)
			} else {
				rep.Add(f)
			}
		case id == "EXIF":
			body = bytes.TrimPrefix(body, exifHeader)
			scrubEXIF(body, Options{}, &rep, false)
		case id == "XMP ":
			rep.Add(xmp.Fields(body)...)
		default:
			rep.Add(report.Binary(report.CatCustom, "webp", id, int64(n)))
		}
	}
	if rest := b[end:]; len(rest) > 0 && !allZero(rest) {
		rep.Add(report.Binary(report.CatCustom, "webp", "TrailingData", int64(len(rest))))
	}
	if vp8x >= 0 && vp8x+9 <= len(out) {
		flags := out[vp8x+8] &^ (vp8xEXIF | vp8xXMP)
		if !keptICC {
			flags &^= vp8xICC
		}
		out[vp8x+8] = flags
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	rep.Bytes = int64(len(b) - len(out))
	if rep.Bytes < 0 {
		rep.Bytes = 0
	}
	return out, rep, nil
}
