package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/internal/report"
)

// gifHandler drops comment extensions, XMP and other application extensions
// (keeping NETSCAPE/ANIMEXTS looping and, with -c, ICC) and trailing data.
type gifHandler struct{}

func init() { Register(gifHandler{}) }

func (gifHandler) Name() string { return "gif" }

func (gifHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 6)
	return bytes.Equal(b, []byte("GIF87a")) || bytes.Equal(b, []byte("GIF89a")) || hasExt(path, ".gif")
}

func (gifHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	_, rep, err := processGIF(data, opts)
	return rep, err
}

func (gifHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	out, rep, err := processGIF(data, opts)
	if err != nil {
		return rep, err
	}
	_, err = w.Write(out)
	return rep, err
}

var errGIFTruncated = errors.New("gif: truncated")

// subBlocks returns the length of a sub-block chain starting at p
// (including the terminator) and the concatenated data.
func subBlocks(b []byte, p int) (int, []byte, error) {
	start := p
	var data []byte
	for {
		if p >= len(b) {
			return 0, nil, errGIFTruncated
		}
		n := int(b[p])
		p++
		if n == 0 {
			return p - start, data, nil
		}
		if p+n > len(b) {
			return 0, nil, errGIFTruncated
		}
		data = append(data, b[p:p+n]...)
		p += n
	}
}

func processGIF(b []byte, opts Options) ([]byte, report.Report, error) {
	rep := report.New("gif")
	if len(b) < 13 {
		return nil, rep, errGIFTruncated
	}
	p := 13
	if b[10]&0x80 != 0 {
		p += 3 << (int(b[10]&7) + 1)
	}
	if p > len(b) {
		return nil, rep, errGIFTruncated
	}
	out := append([]byte{}, b[:p]...)
	var iccSize int64
	for {
		if p >= len(b) {
			return nil, rep, errors.New("gif: missing trailer")
		}
		switch b[p] {
		case 0x3B: // trailer
			out = append(out, 0x3B)
			if rest := b[p+1:]; len(rest) > 0 && !allZero(rest) {
				rep.Add(report.Binary(report.CatCustom, "gif", "TrailingData", int64(len(rest))))
			}
			if iccSize > 0 {
				f := report.Binary(report.CatICC, "gif", "ICCRGBG1012", iccSize)
				if opts.KeepICC {
					rep.Keep(f)
				} else {
					rep.Add(f)
				}
			}
			rep.Bytes = int64(len(b) - len(out))
			return out, rep, nil
		case 0x2C: // image descriptor
			if p+10 > len(b) {
				return nil, rep, errGIFTruncated
			}
			q := p + 10
			if b[p+9]&0x80 != 0 {
				q += 3 << (int(b[p+9]&7) + 1)
			}
			q++ // LZW minimum code size
			n, _, err := subBlocks(b, q)
			if err != nil {
				return nil, rep, err
			}
			out = append(out, b[p:q+n]...)
			p = q + n
		case 0x21: // extension
			if p+2 > len(b) {
				return nil, rep, errGIFTruncated
			}
			label := b[p+1]
			n, data, err := subBlocks(b, p+2)
			if err != nil {
				return nil, rep, err
			}
			block := b[p : p+2+n]
			keep := false
			switch label {
			case 0xF9, 0x01: // graphic control, plain text (image content)
				keep = true
			case 0xFE:
				rep.Add(report.Text(report.CatCustom, "gif", "Comment", string(data)))
			case 0xFF:
				id := ""
				if len(data) >= 11 {
					id = string(data[:11])
				}
				switch id {
				case "NETSCAPE2.0", "ANIMEXTS1.0":
					keep = true
				case "XMP DataXMP":
					// XMP is stored raw; the sub-block length bytes are part of it.
					raw := b[p+2+12 : p+2+n]
					rep.Add(xmp.Fields(raw)...)
				case "ICCRGBG1012":
					iccSize += int64(len(data) - 11)
					keep = opts.KeepICC
				default:
					name := "Application"
					if report.Printable(id) && id != "" {
						name += " " + id
					}
					rep.Add(report.Binary(report.CatCustom, "gif", name, int64(len(data))))
				}
			default:
				rep.Add(report.Binary(report.CatCustom, "gif", fmt.Sprintf("Extension 0x%02X", label), int64(len(data))))
			}
			if keep {
				out = append(out, block...)
			}
			p += 2 + n
		default:
			return nil, rep, fmt.Errorf("gif: unknown block 0x%02X at %d", b[p], p)
		}
	}
}
