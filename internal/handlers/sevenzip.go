package handlers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bodgit/sevenzip"

	"github.com/computerguymackay/metakill/report"
)

// sevenZipHandler cleans 7z archives. Reading is pure Go; repacking needs
// 7-Zip or bsdtar and -x/--external-tools. Without them the archive is only
// inspected: its metadata is listed and the file is left unchanged.
//
// Removed: modification times (set to 1980-01-01), creation and access
// times, OS clutter, and the metadata inside every member a handler
// recognises. Permissions are normalised. Encrypted archives are refused;
// incomplete ones (multi-volume sets) are skipped; links are refused.
type sevenZipHandler struct{}

func init() { Register(sevenZipHandler{}) }

var sevenZipMagic = []byte{'7', 'z', 0xBC, 0xAF, 0x27, 0x1C}

func (sevenZipHandler) Name() string { return "7z" }

func (sevenZipHandler) CanHandle(r io.ReadSeeker, path string) bool {
	return bytes.HasPrefix(peek(r, 6), sevenZipMagic) || hasExt(path, ".7z")
}

func open7z(r io.ReadSeeker) (*sevenzip.Reader, error) {
	ra, size, err := readerAt(r)
	if err != nil {
		return nil, err
	}
	hdr := make([]byte, 32)
	if _, err := ra.ReadAt(hdr, 0); err != nil || !bytes.HasPrefix(hdr, sevenZipMagic) {
		return nil, errors.New("7z: bad signature")
	}
	next := binary.LittleEndian.Uint64(hdr[12:])
	nextSize := binary.LittleEndian.Uint64(hdr[20:])
	if 32+next+nextSize > uint64(size) || 32+next+nextSize < next {
		return nil, fmt.Errorf("7z: archive is incomplete (a multi-volume set or a truncated file); %w", ErrSkip)
	}
	zr, err := sevenzip.NewReader(ra, size)
	if err != nil {
		return nil, sevenZipErr(err)
	}
	return zr, nil
}

func sevenZipErr(err error) error {
	var re *sevenzip.ReadError
	if errors.As(err, &re) && re.Encrypted || strings.Contains(strings.ToLower(err.Error()), "password") {
		return fmt.Errorf("7z: encrypted archives are %w", ErrUnsupported)
	}
	return fmt.Errorf("7z: %w", err)
}

// walk7z feeds every member to x.
func walk7z(zr *sevenzip.Reader, x *extRun) error {
	for _, f := range zr.File {
		attr := f.Attributes
		var mode uint32
		if attr&0x8000 != 0 { // p7zip/7-Zip Unix extension: st_mode in the high 16 bits
			mode = attr >> 16
		}
		isLink := mode&0o170000 == 0o120000 || attr&0x400 != 0 // symlink or Windows reparse point
		e := extEntry{
			name:     f.Name,
			isDir:    f.FileInfo().IsDir(),
			isLink:   isLink,
			exec:     mode&0o111 != 0,
			size:     int64(f.UncompressedSize),
			modified: f.Modified,
			created:  f.Created,
			accessed: f.Accessed,
			open: func() (io.ReadCloser, error) {
				rc, err := f.Open()
				if err != nil {
					return nil, sevenZipErr(err)
				}
				return errMapper{rc}, nil
			},
		}
		if err := x.add(e); err != nil {
			return sevenZipErr(err)
		}
	}
	return nil
}

// errMapper turns encryption read errors into ErrUnsupported.
type errMapper struct{ io.ReadCloser }

func (m errMapper) Read(p []byte) (int, error) {
	n, err := m.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		err = sevenZipErr(err)
	}
	return n, err
}

func (sevenZipHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	zr, err := open7z(r)
	if err != nil {
		return report.Report{}, err
	}
	x := newExtRun("7z", opts, "")
	if err := walk7z(zr, x); err != nil {
		return x.rep, err
	}
	x.finish()
	if x.rep.TotalFields() > 0 {
		_, found := toolLookup("7z")
		if note := toolNote("7z", opts, found); note != "" {
			x.rep.ReportOnly = true
			x.rep.Unclean(note)
		}
	}
	return x.rep, nil
}

func (sevenZipHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	t, found := toolLookup("7z")
	if note := toolNote("7z", opts, found); note != "" {
		return report.Report{}, errors.New("7z: " + note)
	}
	zr, err := open7z(r)
	if err != nil {
		return report.Report{}, err
	}
	rep, err := repack(t, "7z", opts, w, func(x *extRun) error { return walk7z(zr, x) })
	if err == nil {
		rep.Warn("repacked with " + t.String())
	}
	return rep, err
}
