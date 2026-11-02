package handlers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nwaples/rardecode/v2"

	"github.com/computerguymackay/metakill/report"
)

// rarHandler cleans RAR4 and RAR5 archives. Reading is pure Go. With
// -x/--external-tools and rar/WinRAR installed it repacks as RAR; with
// -a/--convert-rar and only 7-Zip installed it writes a 7z instead (the CLI
// gives the result a .7z name). Otherwise the archive is only inspected.
//
// Removed: modification times (set to 1980-01-01), creation/access times,
// Unix owner names and IDs, the archive comment, owner/ACL/NTFS-stream
// records, authenticity data, OS clutter, and the metadata inside every
// member a handler recognises. Encrypted archives and links are refused;
// multi-volume sets are skipped.
type rarHandler struct{}

func init() { Register(rarHandler{}) }

var (
	rar4Sig = []byte("Rar!\x1a\x07\x00")
	rar5Sig = []byte("Rar!\x1a\x07\x01\x00")
)

func (rarHandler) Name() string { return "rar" }

func (rarHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 8)
	return bytes.HasPrefix(b, rar4Sig) || bytes.HasPrefix(b, rar5Sig) || hasExt(path, ".rar")
}

// rarInfo is archive-level data the decoder library does not expose.
type rarInfo struct {
	volume, headerEncrypted, locked bool
	comment                         int64
	authenticity, owners, streams   int
}

// scanRAR walks the block headers of a RAR4 or RAR5 archive.
func scanRAR(r io.ReadSeeker) (rarInfo, error) {
	var info rarInfo
	sig := peek(r, 8)
	size, err := streamSize(r)
	if err != nil {
		return info, err
	}
	switch {
	case bytes.HasPrefix(sig, rar5Sig):
		return info, scanRAR5(r, size, &info)
	case bytes.HasPrefix(sig, rar4Sig):
		return info, scanRAR4(r, size, &info)
	}
	return info, errors.New("rar: bad signature")
}

func readVintFrom(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < len(b) && i < 10; i++ {
		v |= uint64(b[i]&0x7F) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}

func scanRAR5(r io.ReadSeeker, size int64, info *rarInfo) error {
	pos := int64(len(rar5Sig))
	for pos+7 <= size {
		head, err := readAt(r, pos, int(min64(16, size-pos)))
		if err != nil {
			return err
		}
		hsize, n := readVintFrom(head[4:])
		if n == 0 || hsize == 0 || hsize > 2<<20 {
			return errors.New("rar: bad header size")
		}
		hstart := pos + 4 + int64(n)
		if hstart+int64(hsize) > size {
			return errors.New("rar: truncated header")
		}
		h, err := readAt(r, hstart, int(hsize))
		if err != nil {
			return err
		}
		p := 0
		next := func() uint64 {
			v, n := readVintFrom(h[p:])
			p += n
			return v
		}
		typ := next()
		flags := next()
		if flags&1 != 0 {
			next() // extra area size
		}
		var dataSize uint64
		if flags&2 != 0 {
			dataSize = next()
		}
		switch typ {
		case 1: // main
			af := next()
			info.volume = info.volume || af&0x1 != 0
			info.locked = info.locked || af&0x10 != 0
		case 3: // service header: same layout as a file header
			ff := next()
			next() // unpacked size
			next() // attributes
			if ff&2 != 0 {
				p += 4
			}
			if ff&4 != 0 {
				p += 4
			}
			next() // compression info
			next() // host OS
			nl := int(next())
			if p+nl <= len(h) {
				switch string(h[p : p+nl]) {
				case "CMT":
					info.comment += int64(dataSize)
				case "ACL", "STM":
					info.streams++
				}
			}
		case 4: // archive encryption: headers are unreadable
			info.headerEncrypted = true
			return nil
		case 5: // end of archive
			return nil
		}
		pos = hstart + int64(hsize) + int64(dataSize)
	}
	return nil
}

func scanRAR4(r io.ReadSeeker, size int64, info *rarInfo) error {
	pos := int64(len(rar4Sig))
	for pos+7 <= size {
		h, err := readAt(r, pos, int(min64(64, size-pos)))
		if err != nil {
			return err
		}
		typ := h[2]
		flags := binary.LittleEndian.Uint16(h[3:])
		hsize := int64(binary.LittleEndian.Uint16(h[5:]))
		if hsize < 7 {
			return errors.New("rar: bad block size")
		}
		var add int64
		if (flags&0x8000 != 0 || typ == 0x74 || typ == 0x7A) && len(h) >= 11 {
			add = int64(binary.LittleEndian.Uint32(h[7:]))
		}
		switch typ {
		case 0x73: // main header
			info.volume = info.volume || flags&0x0001 != 0
			if flags&0x0002 != 0 {
				info.comment++
			}
			info.locked = info.locked || flags&0x0004 != 0
			if flags&0x0020 != 0 {
				info.authenticity++
			}
			if flags&0x0080 != 0 {
				info.headerEncrypted = true
				return nil
			}
		case 0x75: // old comment block
			info.comment += hsize
		case 0x76, 0x79: // authenticity information / signature
			info.authenticity++
		case 0x77: // old subblock (EA, NTFS ACL, streams, owner)
			info.streams++
		case 0x7A: // new subblock: name follows a file-header layout
			off := 7 + 25
			if flags&0x100 != 0 {
				off += 8
			}
			if off <= len(h) {
				nl := int(binary.LittleEndian.Uint16(h[7+19:]))
				if nb, err := readAt(r, pos+int64(off), nl); err == nil {
					switch string(nb) {
					case "CMT":
						info.comment += add
					case "UOW":
						info.owners++
					case "ACL", "STM", "EA2", "EABE":
						info.streams++
					case "AV":
						info.authenticity++
					}
				}
			}
		case 0x7B: // end
			return nil
		}
		pos += hsize + add
	}
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (i rarInfo) fields(rep *report.Report) {
	if i.comment > 0 {
		rep.Add(report.Binary(report.CatCustom, "rar", "archive comment", i.comment))
	}
	if i.owners > 0 {
		rep.Add(report.Text(report.CatAuthor, "rar", "owner records", fmt.Sprint(i.owners)))
	}
	if i.authenticity > 0 {
		rep.Add(report.Text(report.CatAuthor, "rar", "authenticity/signature data", fmt.Sprint(i.authenticity)))
	}
	if i.streams > 0 {
		rep.Add(report.Text(report.CatCustom, "rar", "ACL/NTFS stream records", fmt.Sprint(i.streams)))
	}
	if i.locked {
		rep.Add(report.Text(report.CatCustom, "rar", "locked flag", "set"))
	}
}

func rarErr(err error) error {
	switch {
	case errors.Is(err, rardecode.ErrArchiveEncrypted), errors.Is(err, rardecode.ErrArchivedFileEncrypted),
		errors.Is(err, rardecode.ErrBadPassword):
		return fmt.Errorf("rar: encrypted archives are %w", ErrUnsupported)
	case errors.Is(err, rardecode.ErrMultiVolume):
		return fmt.Errorf("rar: multi-volume archives are not supported; %w", ErrSkip)
	case errors.Is(err, ErrUnsupported), errors.Is(err, ErrSkip):
		return err
	}
	return fmt.Errorf("rar: %w", err)
}

// walkRAR checks archive-level data and feeds every member to x.
func walkRAR(r io.ReadSeeker, x *extRun) error {
	info, err := scanRAR(r)
	if err != nil {
		return err
	}
	if info.headerEncrypted {
		return fmt.Errorf("rar: encrypted archives are %w", ErrUnsupported)
	}
	if info.volume {
		return fmt.Errorf("rar: multi-volume archives are not supported; %w", ErrSkip)
	}
	info.fields(&x.rep)
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	rr, err := rardecode.NewReader(bufReader(r))
	if err != nil {
		return rarErr(err)
	}
	for {
		h, err := rr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return rarErr(err)
		}
		if h.Encrypted || h.HeaderEncrypted {
			return fmt.Errorf("rar: encrypted archives are %w", ErrUnsupported)
		}
		e := extEntry{
			name:     h.Name,
			isDir:    h.IsDir,
			isLink:   h.LinkType != 0,
			size:     h.UnPackedSize,
			modified: h.ModificationTime,
			created:  h.CreationTime,
			accessed: h.AccessTime,
			open:     func() (io.ReadCloser, error) { return io.NopCloser(rarReadErr{rr}), nil },
		}
		if h.HostOS == rardecode.HostOSUnix {
			e.exec = h.Attributes&0o111 != 0
		}
		var owner []string
		if h.UnixOwnerUserName != nil || h.UnixOwnerGroupName != nil || h.UnixOwnerUserID != nil || h.UnixOwnerGroupID != nil {
			str := func(p *string) string {
				if p == nil {
					return ""
				}
				return *p
			}
			num := func(p *uint64) string {
				if p == nil {
					return "?"
				}
				return fmt.Sprint(*p)
			}
			owner = append(owner, fmt.Sprintf("%s:%s (%s:%s)", str(h.UnixOwnerUserName), str(h.UnixOwnerGroupName), num(h.UnixOwnerUserID), num(h.UnixOwnerGroupID)))
		}
		e.owner = strings.Join(owner, "")
		if err := x.add(e); err != nil {
			return rarErr(err)
		}
	}
}

type rarReadErr struct{ r io.Reader }

func (m rarReadErr) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if err != nil && err != io.EOF {
		err = rarErr(err)
	}
	return n, err
}

func (rarHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	x := newExtRun("rar", opts, "")
	if err := walkRAR(r, x); err != nil {
		return x.rep, err
	}
	x.finish()
	if x.rep.TotalFields() == 0 {
		return x.rep, nil
	}
	_, haveRar := toolLookup("rar")
	_, have7z := toolLookup("7z")
	switch {
	case opts.ExternalTools && haveRar:
	case opts.ExternalTools && opts.ConvertRAR && have7z:
		x.rep.ConvertTo = ".7z"
	default:
		x.rep.ReportOnly = true
		x.rep.Unclean(toolNote("RAR", opts, haveRar))
	}
	return x.rep, nil
}

func (rarHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	if !opts.ExternalTools {
		return report.Report{}, errors.New("rar: " + toolNote("RAR", opts, false))
	}
	t, ok := toolLookup("rar")
	if !ok {
		if !opts.ConvertRAR {
			return report.Report{}, errors.New("rar: " + toolNote("RAR", opts, false))
		}
		if t, ok = toolLookup("7z"); !ok {
			return report.Report{}, errors.New("rar: converting to 7z needs 7-Zip (7zz, 7z, 7za) or bsdtar")
		}
	}
	rep, err := repack(t, "rar", opts, w, func(x *extRun) error { return walkRAR(r, x) })
	if err == nil {
		if t.kind == "rar" {
			rep.Warn("repacked with " + t.String())
		} else {
			rep.Format = "7z"
			rep.Warn("converted to 7z with " + t.String())
		}
	}
	return rep, err
}
