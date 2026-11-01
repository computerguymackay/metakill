package fixtures

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"time"

	dbzip2 "github.com/dsnet/compress/bzip2"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

func init() {
	register("bundle.tar.gz", func() Fixture {
		return Fixture{Name: "bundle.tar.gz", Format: "tar", Data: Compress("gz", BundleTAR())}
	})
	register("drawing.svgz", func() Fixture {
		return Fixture{Name: "drawing.svgz", Format: "tar", Data: Compress("gz", DrawingSVG())}
	})
	register("notes.txt.gz", func() Fixture {
		return Fixture{Name: "notes.txt.gz", Format: "tar", Data: Compress("gz", []byte(ReadmeBody))}
	})
}

// LongName is a member path longer than the 100-byte ustar name field.
var LongName = "docs/" + strings.Repeat("very-long-directory-name/", 5) + "file.txt"

// BundleTAR is an uncompressed TAR with owner names, extended attributes,
// access times, a global PAX header, OS clutter and metadata-bearing members.
func BundleTAR() []byte {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	mod := time.Date(2023, 4, 12, 18, 33, 2, 0, time.UTC)
	add := func(h *tar.Header, data []byte) {
		h.ModTime, h.AccessTime, h.ChangeTime = mod, mod, mod
		h.Uname, h.Gname, h.Uid, h.Gid = "jane", "staff", 501, 20
		h.Format = tar.FormatPAX
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(data))
		}
		if h.Mode == 0 {
			h.Mode = 0o600
		}
		_ = tw.WriteHeader(h)
		_, _ = tw.Write(data) // in-memory; cannot fail
	}
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header",
		PAXRecords: map[string]string{"comment": "packed by " + ArtistName}})
	add(&tar.Header{Typeflag: tar.TypeDir, Name: "docs/", Mode: 0o700}, nil)
	add(&tar.Header{Typeflag: tar.TypeReg, Name: "docs/photo.jpg", PAXRecords: map[string]string{
		"SCHILY.xattr.com.apple.quarantine":                 "0083;64367d2e;Safari;",
		"SCHILY.xattr.com.apple.metadata:kMDItemWhereFroms": "https://example.com/" + Serial,
	}}, PhotoJPEG())
	add(&tar.Header{Typeflag: tar.TypeReg, Name: "docs/song.mp3"}, SongMP3())
	add(&tar.Header{Typeflag: tar.TypeReg, Name: "docs/inner.zip"}, InnerZIP())
	add(&tar.Header{Typeflag: tar.TypeReg, Name: "docs/readme.txt"}, []byte(ReadmeBody))
	add(&tar.Header{Typeflag: tar.TypeReg, Name: "docs/run.sh", Mode: 0o700}, []byte(ScriptBody))
	add(&tar.Header{Typeflag: tar.TypeSymlink, Name: "docs/latest", Linkname: "readme.txt", Mode: 0o777}, nil)
	add(&tar.Header{Typeflag: tar.TypeLink, Name: "docs/readme-link.txt", Linkname: "docs/readme.txt"}, nil)
	add(&tar.Header{Typeflag: tar.TypeReg, Name: LongName}, []byte(ReadmeBody))
	add(&tar.Header{Typeflag: tar.TypeReg, Name: "Users/jane/notes.txt"}, []byte(ReadmeBody))
	add(&tar.Header{Typeflag: tar.TypeReg, Name: "docs/._photo.jpg"}, []byte("Mac OS X where-from https://example.com/"+Serial))
	add(&tar.Header{Typeflag: tar.TypeReg, Name: "docs/.DS_Store"}, []byte("Bud1"+ArtistName))
	_ = tw.Close()
	return b.Bytes()
}

// Compress wraps data with gz, xz, zst or bz2. The gzip header carries an
// original file name, a timestamp and a comment.
func Compress(kind string, data []byte) []byte {
	var b bytes.Buffer
	switch kind {
	case "gz":
		zw := gzip.NewWriter(&b)
		zw.Name = "secret-plans.tar"
		zw.Comment = "made by " + ArtistName
		zw.ModTime = time.Date(2023, 4, 12, 18, 33, 2, 0, time.UTC)
		_, _ = zw.Write(data)
		_ = zw.Close()
	case "xz":
		zw, _ := xz.NewWriter(&b)
		_, _ = zw.Write(data)
		_ = zw.Close()
	case "zst":
		zw, _ := zstd.NewWriter(&b, zstd.WithEncoderConcurrency(1))
		_, _ = zw.Write(data)
		_ = zw.Close()
	case "bz2":
		zw, _ := dbzip2.NewWriter(&b, &dbzip2.WriterConfig{Level: 9})
		_, _ = zw.Write(data)
		_ = zw.Close()
	default:
		return data
	}
	return b.Bytes()
}
