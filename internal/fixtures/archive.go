package fixtures

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"time"
)

func init() {
	register("bundle.zip", func() Fixture { return Fixture{Name: "bundle.zip", Format: "zip", Data: BundleZIP()} })
	register("plugin.jar", func() Fixture {
		return Fixture{Name: "plugin.jar", Format: "zip", Data: SignedJAR(), Skipped: true}
	})
}

// ScriptBody and ReadmeBody are member contents that must survive unchanged.
const (
	ScriptBody = "#!/bin/sh\necho hello\n"
	ReadmeBody = "Plain text needs no cleaning.\n"
)

type archMember struct {
	name    string
	data    []byte
	mode    os.FileMode
	extra   []byte
	comment string
}

// unixExtra is an Info-ZIP "ux" extra field carrying uid/gid 1000.
func unixExtra() []byte {
	b := []byte{0x75, 0x78, 11, 0, 1, 4}
	b = binary.LittleEndian.AppendUint32(b, 1000)
	b = append(b, 4)
	return binary.LittleEndian.AppendUint32(b, 1000)
}

func buildArchive(comment string, members ...archMember) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	mod := time.Date(2023, 4, 12, 18, 33, 2, 0, time.UTC)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.name, Method: zip.Deflate, Modified: mod, Comment: m.comment}
		mode := m.mode
		if mode == 0 {
			mode = 0o600
		}
		h.SetMode(mode)
		if mode.IsDir() {
			h.Method = zip.Store
		}
		h.Extra = append(h.Extra, m.extra...)
		w, _ := zw.CreateHeader(h)
		_, _ = w.Write(m.data) // in-memory; cannot fail
	}
	if comment != "" {
		_ = zw.SetComment(comment)
	}
	_ = zw.Close()
	return b.Bytes()
}

// InnerZIP is a nested archive holding one photo with EXIF.
func InnerZIP() []byte {
	return buildArchive("", archMember{name: "inner/photo.jpg", data: CoverJPEG()})
}

// BundleZIP is a ZIP with metadata-bearing members (a photo, an MP3, a DOCX,
// a nested ZIP), plain members that must pass through unchanged (text, an
// executable script, a symlink, a directory), OS clutter, and archive-level
// metadata (timestamps, uid/gid extra fields, comments).
func BundleZIP() []byte {
	return buildArchive("Packed by "+ArtistName,
		archMember{name: "docs/", mode: os.ModeDir | 0o700},
		archMember{name: "docs/photo.jpg", data: PhotoJPEG(), extra: unixExtra(), comment: "taken at home"},
		archMember{name: "docs/song.mp3", data: SongMP3()},
		archMember{name: "docs/letter.docx", data: LetterDOCX()},
		archMember{name: "docs/inner.zip", data: InnerZIP()},
		archMember{name: "docs/readme.txt", data: []byte(ReadmeBody)},
		archMember{name: "docs/run.sh", data: []byte(ScriptBody), mode: 0o700},
		archMember{name: "docs/latest", data: []byte("readme.txt"), mode: os.ModeSymlink | 0o777},
		archMember{name: "Users/jane/notes.txt", data: []byte(ReadmeBody)},
		archMember{name: "docs/.DS_Store", data: []byte("Bud1" + ArtistName)},
		archMember{name: "docs/Thumbs.db", data: []byte("thumbnail cache")},
		archMember{name: "__MACOSX/", mode: os.ModeDir | 0o755},
		archMember{name: "__MACOSX/docs/._photo.jpg", data: []byte("Mac OS X where-from https://example.com/" + Serial)},
	)
}

// SignedJAR is a signed Java archive; metakill must skip it.
func SignedJAR() []byte {
	return buildArchive("",
		archMember{name: "META-INF/MANIFEST.MF", data: []byte("Manifest-Version: 1.0\nBuilt-By: " + ArtistName + "\n")},
		archMember{name: "META-INF/CERT.SF", data: []byte("Signature-Version: 1.0\n")},
		archMember{name: "META-INF/CERT.RSA", data: []byte("not a real signature")},
		archMember{name: "com/example/App.class", data: []byte("\xca\xfe\xba\xbe")},
	)
}
