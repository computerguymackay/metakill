package handlers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/computerguymackay/metakill/internal/meta/iptc"
	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/internal/report"
)

// jpegHandler removes APPn metadata segments, comments and trailing data.
// It keeps JFIF, Adobe (APP14, needed for CMYK decoding) and, with -c, ICC
// profiles. EXIF is replaced by a minimal block holding only Orientation
// (and the thumbnail with -t) so images stay the right way up.
type jpegHandler struct{}

func init() { Register(jpegHandler{}) }

func (jpegHandler) Name() string { return "jpeg" }

func (jpegHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 3)
	if len(b) == 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF {
		return true
	}
	return hasExt(path, ".jpg", ".jpeg", ".jpe", ".jfif")
}

func (h jpegHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	_, rep, err := processJPEG(data, opts)
	return rep, err
}

func (h jpegHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	out, rep, err := processJPEG(data, opts)
	if err != nil {
		return rep, err
	}
	_, err = w.Write(out)
	return rep, err
}

var (
	exifHeader    = []byte("Exif\x00\x00")
	xmpHeader     = []byte("http://ns.adobe.com/xap/1.0/\x00")
	xmpExtHeader  = []byte("http://ns.adobe.com/xmp/extension/\x00")
	iccHeader     = []byte("ICC_PROFILE\x00")
	errJPEGMarker = errors.New("jpeg: invalid marker")
)

// processJPEG returns the cleaned file and the report. The same code path
// serves Inspect and Strip so they can never disagree.
func processJPEG(data []byte, opts Options) ([]byte, report.Report, error) {
	rep := report.New("jpeg")
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, rep, errors.New("jpeg: missing SOI marker")
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8)
	pos := 2
	var iccSize int64
	var xmpExtSize int64
	sawEOI := false

	for pos < len(data) {
		if data[pos] != 0xFF {
			return nil, rep, fmt.Errorf("%w at offset %d", errJPEGMarker, pos)
		}
		for pos < len(data) && data[pos] == 0xFF {
			pos++
		}
		if pos >= len(data) {
			break
		}
		m := data[pos]
		pos++
		if m == 0xD9 { // EOI
			out = append(out, 0xFF, 0xD9)
			sawEOI = true
			break
		}
		if (m >= 0xD0 && m <= 0xD7) || m == 0x01 {
			out = append(out, 0xFF, m)
			continue
		}
		if pos+2 > len(data) {
			return nil, rep, errors.New("jpeg: truncated segment header")
		}
		length := int(binary.BigEndian.Uint16(data[pos:]))
		if length < 2 || pos+length > len(data) {
			return nil, rep, fmt.Errorf("jpeg: segment 0x%02X has bad length %d", m, length)
		}
		payload := data[pos+2 : pos+length]
		segment := data[pos-2 : pos+length]
		pos += length

		keep := true
		var replacement []byte
		switch {
		case m == 0xE0: // APP0
			switch {
			case bytes.HasPrefix(payload, []byte("JFIF\x00")), bytes.HasPrefix(payload, []byte("AVI1")):
			case bytes.HasPrefix(payload, []byte("JFXX\x00")):
				f := report.Binary(report.CatThumbnail, "jfif", "JFXXThumbnail", int64(len(payload)))
				if opts.KeepThumbnails {
					rep.Keep(f)
				} else {
					rep.Add(f)
					keep = false
				}
			default:
				rep.Add(report.Binary(report.CatCustom, "jpeg", appName(m, payload), int64(len(payload))))
				keep = false
			}
		case m == 0xE1 && bytes.HasPrefix(payload, exifHeader):
			keep = false
			if min := scrubEXIF(payload[len(exifHeader):], opts, &rep, true); min != nil {
				replacement = append(append([]byte{}, exifHeader...), min...)
			}
		case m == 0xE1 && bytes.HasPrefix(payload, xmpHeader):
			keep = false
			rep.Add(xmp.Fields(payload[len(xmpHeader):])...)
		case m == 0xE1 && bytes.HasPrefix(payload, xmpExtHeader):
			keep = false
			xmpExtSize += int64(len(payload))
		case m == 0xE2 && bytes.HasPrefix(payload, iccHeader):
			iccSize += int64(len(payload))
			keep = opts.KeepICC
		case m == 0xED: // APP13 Photoshop / IPTC
			keep = false
			if bytes.HasPrefix(payload, iptc.PhotoshopHeader) {
				rep.Add(iptc.Resources(payload[len(iptc.PhotoshopHeader):])...)
			} else {
				rep.Add(report.Binary(report.CatCustom, "jpeg", appName(m, payload), int64(len(payload))))
			}
		case m == 0xEE && bytes.HasPrefix(payload, []byte("Adobe")):
			// APP14 Adobe: colour transform flag needed to decode CMYK/YCCK.
		case m >= 0xE1 && m <= 0xEF:
			keep = false
			cat := report.CatCustom
			rep.Add(report.Binary(cat, "jpeg", appName(m, payload), int64(len(payload))))
		case m == 0xFE: // COM
			keep = false
			rep.Add(report.Text(report.CatCustom, "jpeg", "Comment", string(payload)))
		}
		if keep {
			out = append(out, segment...)
		} else if replacement != nil {
			out = appendSegment(out, 0xE1, replacement)
		}

		if m == 0xDA { // SOS: copy entropy-coded data up to the next marker
			start := pos
			for pos < len(data)-1 {
				if data[pos] != 0xFF {
					pos++
					continue
				}
				n := data[pos+1]
				if n == 0x00 || (n >= 0xD0 && n <= 0xD7) {
					pos += 2
					continue
				}
				if n == 0xFF {
					pos++
					continue
				}
				break
			}
			if pos >= len(data)-1 {
				pos = len(data)
			}
			out = append(out, data[start:pos]...)
		}
	}

	if iccSize > 0 {
		f := report.Binary(report.CatICC, "icc", "ICC_PROFILE", iccSize)
		if opts.KeepICC {
			rep.Keep(f)
		} else {
			rep.Add(f)
		}
	}
	if xmpExtSize > 0 {
		rep.Add(report.Binary(report.CatCustom, "xmp", "ExtendedXMP", xmpExtSize))
	}
	if !sawEOI {
		rep.Warn("file is truncated (no EOI marker); image data copied as-is")
	} else if trailer := data[pos:]; len(trailer) > 0 && !allZero(trailer) {
		rep.Add(report.Binary(report.CatCustom, "jpeg", "TrailingData", int64(len(trailer))))
	}
	rep.Bytes = int64(len(data) - len(out))
	if rep.Bytes < 0 {
		rep.Bytes = 0
	}
	return out, rep, nil
}

func appendSegment(out []byte, marker byte, payload []byte) []byte {
	out = append(out, 0xFF, marker, 0, 0)
	binary.BigEndian.PutUint16(out[len(out)-2:], uint16(len(payload)+2))
	return append(out, payload...)
}

// appName labels an APPn segment by number and identifier string.
func appName(m byte, payload []byte) string {
	id := payload
	if i := bytes.IndexByte(id, 0); i >= 0 {
		id = id[:i]
	}
	if len(id) > 24 {
		id = id[:24]
	}
	name := fmt.Sprintf("APP%d", m-0xE0)
	if len(id) > 0 && report.Printable(string(id)) {
		name += " " + string(id)
	}
	return name
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
