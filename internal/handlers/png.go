package handlers

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"

	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/report"
)

// pngHandler drops text chunks (tEXt, zTXt, iTXt), eXIf (rewritten to keep
// only Orientation), tIME, iCCP (unless -c), unknown ancillary chunks and any
// data after IEND. Rendering chunks (gAMA, sRGB, pHYs, APNG, HDR, ...) stay.
type pngHandler struct{}

func init() { Register(pngHandler{}) }

var pngSig = []byte("\x89PNG\r\n\x1a\n")

func (pngHandler) Name() string { return "png" }

func (pngHandler) CanHandle(r io.ReadSeeker, path string) bool {
	return bytes.Equal(peek(r, 8), pngSig) || hasExt(path, ".png", ".apng")
}

func (pngHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	_, rep, err := processPNG(data, opts)
	return rep, err
}

func (pngHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	out, rep, err := processPNG(data, opts)
	if err != nil {
		return rep, err
	}
	_, err = w.Write(out)
	return rep, err
}

// Chunks that affect how the image renders and carry nothing identifying.
var pngKeep = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true, "gAMA": true,
	"cHRM": true, "sRGB": true, "sBIT": true, "bKGD": true, "hIST": true, "pHYs": true,
	"sPLT": true, "cICP": true, "mDCv": true, "cLLI": true, "acTL": true, "fcTL": true,
	"fdAT": true, "oFFs": true, "sCAL": true, "pCAL": true,
}

const maxInflate = 16 << 20

func processPNG(data []byte, opts Options) ([]byte, report.Report, error) {
	rep := report.New("png")
	if !bytes.HasPrefix(data, pngSig) {
		return nil, rep, errors.New("png: bad signature")
	}
	out := make([]byte, 0, len(data))
	out = append(out, pngSig...)
	pos := len(pngSig)
	sawEnd := false
	for pos+12 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		if n < 0 || pos+12+n > len(data) {
			return nil, rep, fmt.Errorf("png: chunk %q truncated", typ)
		}
		body := data[pos+8 : pos+8+n]
		chunk := data[pos : pos+12+n]
		pos += 12 + n

		keep := false
		switch {
		case pngKeep[typ]:
			keep = true
		case typ == "iCCP":
			f := report.Binary(report.CatICC, "png", "iCCP", int64(n))
			if opts.KeepICC {
				rep.Keep(f)
				keep = true
			} else {
				rep.Add(f)
			}
		case typ == "tEXt", typ == "zTXt", typ == "iTXt":
			pngText(typ, body, &rep)
		case typ == "eXIf":
			if min := scrubEXIF(body, opts, &rep, true); min != nil {
				out = appendPNGChunk(out, "eXIf", min)
			}
		case typ == "tIME":
			if n == 7 {
				v := fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d", binary.BigEndian.Uint16(body), body[2], body[3], body[4], body[5], body[6])
				rep.Add(report.Text(report.CatTimestamp, "png", "tIME", v))
			} else {
				rep.Add(report.Binary(report.CatTimestamp, "png", "tIME", int64(n)))
			}
		case typ[0] >= 'A' && typ[0] <= 'Z':
			// Unknown critical chunk: decoders need it, so it must stay.
			keep = true
			rep.Warn(fmt.Sprintf("unknown critical chunk %q kept", typ))
		default:
			rep.Add(report.Binary(report.CatCustom, "png", typ, int64(n)))
		}
		if keep {
			out = append(out, chunk...)
		}
		if typ == "IEND" {
			sawEnd = true
			break
		}
	}
	if !sawEnd {
		return nil, rep, errors.New("png: missing IEND chunk")
	}
	if trailer := data[pos:]; len(trailer) > 0 && !allZero(trailer) {
		rep.Add(report.Binary(report.CatCustom, "png", "TrailingData", int64(len(trailer))))
	}
	rep.Bytes = int64(len(data) - len(out))
	if rep.Bytes < 0 {
		rep.Bytes = 0
	}
	return out, rep, nil
}

func appendPNGChunk(out []byte, typ string, body []byte) []byte {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	copy(hdr[4:], typ)
	out = append(out, hdr[:]...)
	out = append(out, body...)
	crc := crc32.NewIEEE()
	crc.Write(hdr[4:])
	crc.Write(body)
	return binary.BigEndian.AppendUint32(out, crc.Sum32())
}

// pngText decodes a text chunk and records it as a field.
func pngText(typ string, body []byte, rep *report.Report) {
	k := bytes.IndexByte(body, 0)
	if k < 0 {
		rep.Add(report.Binary(report.CatCustom, "png", typ, int64(len(body))))
		return
	}
	key := string(body[:k])
	rest := body[k+1:]
	var text []byte
	ok := true
	switch typ {
	case "tEXt":
		text = latin1(rest)
	case "zTXt":
		if len(rest) < 1 {
			ok = false
			break
		}
		text, ok = inflate(rest[1:])
	case "iTXt":
		if len(rest) < 2 {
			ok = false
			break
		}
		compressed := rest[0] == 1
		rest = rest[2:]
		// language tag and translated keyword, both NUL-terminated
		for i := 0; i < 2; i++ {
			j := bytes.IndexByte(rest, 0)
			if j < 0 {
				ok = false
				break
			}
			rest = rest[j+1:]
		}
		if !ok {
			break
		}
		if compressed {
			text, ok = inflate(rest)
		} else {
			text = rest
		}
	}
	if !ok {
		rep.Add(report.Binary(pngKeyCategory(key), "png", key, int64(len(body))))
		return
	}
	if key == "XML:com.adobe.xmp" {
		rep.Add(xmp.Fields(text)...)
		return
	}
	if strings.HasPrefix(key, "Raw profile type") {
		rep.Add(report.Binary(report.CatCustom, "png", key, int64(len(text))))
		return
	}
	rep.Add(report.Text(pngKeyCategory(key), "png", key, string(text)))
}

// pngKeyCategory classifies the predefined PNG text keywords.
func pngKeyCategory(key string) report.Category {
	switch strings.ToLower(key) {
	case "author", "copyright", "disclaimer":
		return report.CatAuthor
	case "creation time", "date:create", "date:modify", "date:timestamp":
		return report.CatTimestamp
	case "software":
		return report.CatSoftware
	case "source":
		return report.CatDevice
	case "title":
		return report.CatIDTag
	}
	return report.CatCustom
}

func inflate(b []byte) ([]byte, bool) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(io.LimitReader(zr, maxInflate))
	if err != nil {
		return nil, false
	}
	return out, true
}

func latin1(b []byte) []byte {
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return []byte(sb.String())
}
