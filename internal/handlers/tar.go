package handlers

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	dbzip2 "github.com/dsnet/compress/bzip2"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"

	"github.com/computerguymackay/metakill/report"
)

// tarHandler cleans TAR archives, plain or wrapped in gzip, xz, zstd or
// bzip2, and single compressed files that are not TARs (e.g. notes.txt.gz,
// drawing.svgz). Everything is streamed: members that no handler recognises
// are copied without being held in memory.
//
// Archive level: modification times are set to the Unix epoch; access and
// change times, uid/gid, user/group names, extended attributes (macOS
// quarantine and where-from data, SELinux labels, ...) and all other PAX
// records are dropped; permissions are normalised (directory/executable
// kept); global PAX headers and OS clutter (AppleDouble, .DS_Store,
// Thumbs.db, desktop.ini) are removed. The gzip header's original file name,
// timestamp and comment are removed.
//
// Members a handler recognises (images, documents, audio, video, nested
// archives) are cleaned recursively. Directories, symlinks, hard links and
// device entries are kept. GNU sparse files are written out in full.
type tarHandler struct{}

func init() { Register(tarHandler{}) }

func (tarHandler) Name() string { return "tar" }

type wrapKind int

const (
	wrapNone wrapKind = iota
	wrapGzip
	wrapXZ
	wrapZstd
	wrapBzip2
)

func (k wrapKind) suffix() string {
	return [...]string{"", "gz", "xz", "zst", "bz2"}[k]
}

func sniffWrap(b []byte) wrapKind {
	switch {
	case len(b) >= 2 && b[0] == 0x1F && b[1] == 0x8B:
		return wrapGzip
	case bytes.HasPrefix(b, []byte{0xFD, '7', 'z', 'X', 'Z', 0x00}):
		return wrapXZ
	case bytes.HasPrefix(b, []byte{0x28, 0xB5, 0x2F, 0xFD}):
		return wrapZstd
	case len(b) >= 10 && b[0] == 'B' && b[1] == 'Z' && b[2] == 'h' && b[3] >= '1' && b[3] <= '9' &&
		bytes.Equal(b[4:10], []byte{0x31, 0x41, 0x59, 0x26, 0x53, 0x59}):
		return wrapBzip2
	}
	return wrapNone
}

// isTarHeader checks a 512-byte block for a ustar magic or, for old V7
// archives, a valid header checksum.
func isTarHeader(b []byte) bool {
	if len(b) < 512 {
		return false
	}
	if bytes.Equal(b[257:262], []byte("ustar")) {
		return true
	}
	stored := strings.TrimRight(strings.TrimSpace(string(b[148:156])), "\x00")
	if stored == "" || b[0] == 0 {
		return false
	}
	var want int64
	for _, c := range stored {
		if c < '0' || c > '7' {
			return false
		}
		want = want*8 + int64(c-'0')
	}
	var sum int64
	for i, c := range b[:512] {
		if i >= 148 && i < 156 {
			c = ' '
		}
		sum += int64(c)
	}
	return sum == want
}

func (tarHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 512)
	if sniffWrap(b) != wrapNone || isTarHeader(b) {
		return true
	}
	return hasExt(path, ".tar", ".tgz", ".gz", ".tbz", ".tbz2", ".bz2", ".txz", ".xz", ".tzst", ".zst", ".svgz")
}

// maxTarMember caps how much of one member is cleaned in memory. It is a
// variable so tests can lower it.
var maxTarMember int64 = 512 << 20

// tarRun carries the state of one pass over the input.
type tarRun struct {
	opts    Options
	rep     report.Report
	wrap    wrapKind
	inBytes int64     // uncompressed bytes read
	tee     io.Writer // optional copy of the uncompressed output
}

// countingReader counts bytes read.
type countingReader struct {
	r io.Reader
	n *int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

func openWrap(kind wrapKind, r io.Reader) (io.Reader, *gzip.Header, func(), error) {
	switch kind {
	case wrapGzip:
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, nil, err
		}
		h := zr.Header
		return zr, &h, func() { _ = zr.Close() }, nil
	case wrapXZ:
		zr, err := xz.NewReader(r)
		return zr, nil, func() {}, err
	case wrapZstd:
		zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, nil, nil, err
		}
		return zr, nil, zr.Close, nil
	case wrapBzip2:
		return bzip2.NewReader(r), nil, func() {}, nil
	}
	return r, nil, func() {}, nil
}

// newWrapWriter compresses into w. The gzip header is left empty (no name,
// time 0, OS "unknown").
func newWrapWriter(kind wrapKind, w io.Writer) (io.WriteCloser, error) {
	switch kind {
	case wrapGzip:
		return gzip.NewWriterLevel(w, gzip.DefaultCompression)
	case wrapXZ:
		return xz.NewWriter(w)
	case wrapZstd:
		return zstd.NewWriter(w, zstd.WithEncoderConcurrency(1))
	case wrapBzip2:
		return dbzip2.NewWriter(w, &dbzip2.WriterConfig{Level: 9})
	}
	return nopWriteCloser{w}, nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// process runs one pass. When out is nil it only inspects.
func (t *tarRun) process(r io.Reader, out io.Writer) error {
	br := bufio.NewReaderSize(r, 1<<16)
	head, _ := br.Peek(512)
	t.wrap = sniffWrap(head)
	dec, gzh, closeDec, err := openWrap(t.wrap, br)
	if err != nil {
		return fmt.Errorf("%s: %w", t.wrap.suffix(), err)
	}
	defer closeDec()
	if gzh != nil {
		t.gzipHeader(gzh)
	}
	plain := bufio.NewReaderSize(countingReader{dec, &t.inBytes}, 1<<16)
	inner, _ := plain.Peek(512)
	isTar := isTarHeader(inner)
	switch {
	case isTar && t.wrap == wrapNone:
		t.rep.Format = "tar"
	case isTar:
		t.rep.Format = "tar." + t.wrap.suffix()
	case t.wrap == wrapNone:
		return errors.New("tar: not a TAR archive")
	default:
		t.rep.Format = t.wrap.suffix()
	}

	var comp io.WriteCloser
	dst := io.Discard // io.Writer
	if out != nil {
		comp, err = newWrapWriter(t.wrap, out)
		if err != nil {
			return err
		}
		dst = comp
	}
	cw := &countWriter{}
	if t.tee != nil {
		dst = io.MultiWriter(dst, cw, t.tee)
	} else {
		dst = io.MultiWriter(dst, cw)
	}
	if isTar {
		err = t.tarStream(plain, dst)
	} else {
		err = t.singleFile(plain, dst)
	}
	if err != nil {
		return err
	}
	if comp != nil {
		if err := comp.Close(); err != nil {
			return err
		}
	}
	// For compressed input this is the change in uncompressed bytes.
	if d := t.inBytes - cw.n; d > 0 {
		t.rep.Bytes = d
	}
	return nil
}

func (t *tarRun) gzipHeader(h *gzip.Header) {
	if h.Name != "" {
		t.rep.Add(report.Text(report.CatCustom, "gzip", "original file name", h.Name))
	}
	if h.Comment != "" {
		t.rep.Add(report.Text(report.CatCustom, "gzip", "comment", h.Comment))
	}
	if !h.ModTime.IsZero() && h.ModTime.Unix() != 0 {
		t.rep.Add(report.Text(report.CatTimestamp, "gzip", "modification time", h.ModTime.UTC().Format(time.RFC3339)))
	}
	if len(h.Extra) > 0 {
		t.rep.Add(report.Binary(report.CatCustom, "gzip", "extra field", int64(len(h.Extra))))
	}
}

// singleFile handles a compressed file that is not a TAR.
func (t *tarRun) singleFile(r io.Reader, dst io.Writer) error {
	prefix, err := io.ReadAll(io.LimitReader(r, sniffSize))
	if err != nil {
		return fmt.Errorf("%s: %w", t.wrap.suffix(), err)
	}
	h := Detect(bytes.NewReader(prefix), "")
	if h == nil {
		_, err := dst.Write(prefix)
		if err == nil {
			_, err = io.Copy(dst, r)
		}
		return err
	}
	rest, err := io.ReadAll(io.LimitReader(r, maxTarMember+1))
	if err != nil {
		return fmt.Errorf("%s: %w", t.wrap.suffix(), err)
	}
	data := append(prefix, rest...)
	if int64(len(data)) > maxTarMember {
		t.rep.Unclean(fmt.Sprintf("compressed %s content is too large to clean in memory; copied unchanged", h.Name()))
		if _, err := dst.Write(data); err != nil {
			return err
		}
		_, err := io.Copy(dst, r)
		return err
	}
	cleaned := t.clean("(compressed content)", data)
	_, err = dst.Write(cleaned)
	return err
}

// clean runs a member through the embedded cleaner and records its report.
func (t *tarRun) clean(name string, data []byte) []byte {
	out, sub, ok := StripEmbedded(name, data, t.opts)
	if !ok {
		return data
	}
	if sub.TotalFields() > 0 || !sub.FullyClean || len(sub.Warnings) > 0 {
		t.rep.AddEmbedded(sub)
	}
	if out == nil {
		return data
	}
	return out
}

// Structural PAX keys that the Go writer regenerates as needed; they are not
// metadata. Owner and time keys are reported through the header fields.
var paxStructural = map[string]bool{
	"path": true, "linkpath": true, "size": true, "uid": true, "gid": true,
	"uname": true, "gname": true, "mtime": true, "atime": true, "ctime": true,
}

// normalTarMode keeps only the meaning of a mode: directory, symlink or
// executable. Owner-only bits, set-uid/gid and sticky bits are normalised.
func normalTarMode(typ byte, mode int64) int64 {
	switch typ {
	case tar.TypeDir:
		return 0o755
	case tar.TypeSymlink:
		return 0o777
	case tar.TypeReg, tar.TypeLink:
		if mode&0o111 != 0 {
			return 0o755
		}
	}
	return 0o644
}

func (t *tarRun) tarStream(r io.Reader, dst io.Writer) error {
	tr := tar.NewReader(r)
	tw := tar.NewWriter(dst)
	var latest time.Time
	owners := map[string]bool{}
	xattrs := map[string]bool{}
	otherPAX := map[string]bool{}
	accessTimes, globals := 0, 0
	warnedPath := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if h.ModTime.Unix() > 0 && h.ModTime.After(latest) {
			latest = h.ModTime
		}
		if h.Uname != "" || h.Gname != "" || h.Uid != 0 || h.Gid != 0 {
			owners[fmt.Sprintf("%s:%s (%d:%d)", h.Uname, h.Gname, h.Uid, h.Gid)] = true
		}
		if !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
			accessTimes++
		}
		for k := range h.PAXRecords {
			switch {
			case paxStructural[k] || strings.HasPrefix(k, "GNU.sparse."):
			case strings.HasPrefix(k, "SCHILY.xattr.") || strings.HasPrefix(k, "LIBARCHIVE.xattr."):
				xattrs[k[strings.Index(k, "xattr.")+6:]] = true
			default:
				otherPAX[k] = true
			}
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			globals++
			continue
		}
		if cat, ok := junkKind(h.Name); ok {
			if h.Typeflag != tar.TypeDir {
				t.rep.Add(report.Binary(cat, "tar", h.Name, h.Size))
			}
			continue
		}
		if !warnedPath && homePath.MatchString(h.Name) {
			t.rep.Warn(fmt.Sprintf("member names look like absolute or home-directory paths (e.g. %q); names are not changed", h.Name))
			warnedPath = true
		}
		typ := h.Typeflag
		if typ == tar.TypeRegA || typ == tar.TypeGNUSparse || typ == tar.TypeCont { //nolint:staticcheck // read-side compatibility
			typ = tar.TypeReg
		}
		nh := &tar.Header{
			Typeflag: typ, Name: h.Name, Linkname: h.Linkname, Size: h.Size,
			ModTime: time.Unix(0, 0), Devmajor: h.Devmajor, Devminor: h.Devminor,
		}
		nh.Mode = normalTarMode(typ, h.Mode)
		if typ != tar.TypeReg {
			nh.Size = 0
			if err := tw.WriteHeader(nh); err != nil {
				return fmt.Errorf("tar: %s: %w", h.Name, err)
			}
			continue
		}
		if err := t.member(tr, tw, nh); err != nil {
			return err
		}
	}
	if !latest.IsZero() {
		t.rep.Add(report.Text(report.CatTimestamp, "tar", "member timestamps (latest)", latest.UTC().Format(time.RFC3339)))
	}
	if accessTimes > 0 {
		t.rep.Add(report.Text(report.CatTimestamp, "tar", "access/change times", fmt.Sprintf("%d member(s)", accessTimes)))
	}
	if len(owners) > 0 {
		t.rep.Add(report.Text(report.CatAuthor, "tar", "owners", strings.Join(sortedKeys2(owners), ", ")))
	}
	if len(xattrs) > 0 {
		t.rep.Add(report.Text(report.CatCustom, "tar", "extended attributes", strings.Join(sortedKeys2(xattrs), ", ")))
	}
	if len(otherPAX) > 0 {
		t.rep.Add(report.Text(report.CatCustom, "tar", "PAX records", strings.Join(sortedKeys2(otherPAX), ", ")))
	}
	if globals > 0 {
		t.rep.Add(report.Text(report.CatCustom, "tar", "global PAX headers", fmt.Sprint(globals)))
	}
	return tw.Close()
}

// member copies one regular file, cleaning it if a handler recognises it.
func (t *tarRun) member(tr *tar.Reader, tw *tar.Writer, nh *tar.Header) error {
	prefixLen := nh.Size
	if prefixLen > sniffSize {
		prefixLen = sniffSize
	}
	prefix := make([]byte, prefixLen)
	if _, err := io.ReadFull(tr, prefix); err != nil {
		return fmt.Errorf("tar: %s: %w", nh.Name, err)
	}
	h := Detect(bytes.NewReader(prefix), "")
	streamAsIs := func() error {
		if err := tw.WriteHeader(nh); err != nil {
			return fmt.Errorf("tar: %s: %w", nh.Name, err)
		}
		if _, err := tw.Write(prefix); err != nil {
			return err
		}
		_, err := io.Copy(tw, tr)
		return err
	}
	if h == nil {
		return streamAsIs()
	}
	if nh.Size > maxTarMember {
		t.rep.Unclean(fmt.Sprintf("%s (%s, %d bytes) is too large to clean in memory; copied unchanged", nh.Name, h.Name(), nh.Size))
		return streamAsIs()
	}
	rest, err := io.ReadAll(tr)
	if err != nil {
		return fmt.Errorf("tar: %s: %w", nh.Name, err)
	}
	data := t.clean(nh.Name, append(prefix, rest...))
	nh.Size = int64(len(data))
	if err := tw.WriteHeader(nh); err != nil {
		return fmt.Errorf("tar: %s: %w", nh.Name, err)
	}
	_, err = tw.Write(data)
	return err
}

func (tarHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return report.Report{}, err
	}
	t := &tarRun{opts: opts, rep: report.New("tar")}
	err := t.process(r, nil)
	return t.rep, err
}

func (tarHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return report.Report{}, err
	}
	start, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return report.Report{}, err
	}
	want := sha256.New()
	t := &tarRun{opts: opts, rep: report.New("tar"), tee: want}
	if err := t.process(r, w); err != nil {
		return t.rep, err
	}
	if t.wrap == wrapBzip2 {
		if err := verifyBzip2(w, start, want.Sum(nil)); err != nil {
			return t.rep, err
		}
	}
	return t.rep, nil
}

// verifyBzip2 decompresses what was just written with the standard library
// and checks it matches the uncompressed stream that was fed to the encoder.
// The bzip2 encoder is third-party and unmaintained, so its output is never
// trusted unchecked.
func verifyBzip2(w io.WriteSeeker, start int64, want []byte) error {
	rs, ok := w.(io.ReadSeeker)
	if !ok {
		return errors.New("bz2: cannot verify output (writer is not readable)")
	}
	end, err := w.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := rs.Seek(start, io.SeekStart); err != nil {
		return err
	}
	got := sha256.New()
	if _, err := io.Copy(got, bzip2.NewReader(io.LimitReader(rs, end-start))); err != nil {
		return fmt.Errorf("bz2: written output does not decompress: %w", err)
	}
	if _, err := w.Seek(end, io.SeekStart); err != nil {
		return err
	}
	if !bytes.Equal(got.Sum(nil), want) {
		return errors.New("bz2: written output does not match the cleaned content")
	}
	return nil
}
