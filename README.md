# metakill

`metakill` is a single, dependency-free binary that strips identifying
metadata from images, documents, audio and video: GPS coordinates,
timestamps, camera and device identifiers, software/encoder strings, authors,
comments, thumbnails and (by default) media ID tags.

- **Pure Go, no cgo, no external tools by default.** Nothing is shelled out
  (no ffmpeg, exiftool or ImageMagick). The one opt-in exception is
  repacking 7z and RAR archives, which needs 7-Zip or rar and the
  `-x/--external-tools` flag; see [7z and RAR](#7z-and-rar). Builds for
  linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and windows/amd64
  with `CGO_ENABLED=0`.
- **No network access, no telemetry.** See [Network](#network) for details.
- **Aggressive by default.** Everything that is not needed to display or
  play the file is removed unless you ask to keep it.
- **Safe.** Output is written to a temp file and re-validated before the
  original is replaced. A `.bak` backup is made by default.

```console
$ metakill -n photo.jpg          # see what would be removed
$ metakill -y photo.jpg          # remove it (photo.jpg.bak is kept)
$ metakill -d -r -y ~/Pictures   # a whole tree
```

> [!WARNING]
> **Media ID tags are removed by default.** Title, artist, album, track number
> and year in your music and video files are deleted unless you pass
> `-k/--keep-tags`. Players and libraries that rely on them will show
> "Unknown artist" afterwards. Cover art is also removed unless you pass
> `-t/--keep-thumbnails`. Keep tags are rewritten from scratch: only those five
> fields survive, the year is reduced to `YYYY`, and kept cover art has its
> own EXIF stripped.

## Install

```console
go install github.com/computerguymackay/metakill/cmd/metakill@latest
```

Building needs Go 1.26 or newer. An older `go` command downloads the right
toolchain automatically.

Or download a binary from the releases page, or build one yourself with
`make build` (output in `bin/`).

## Usage

```text
metakill [flags] FILE...
metakill -d [flags] DIR...
```

In **file mode** each argument is a file. In **directory mode** (`-d`) each
argument must be a directory, and file arguments are rejected. Without `-r`
only the top level of each directory is processed. Files are processed in
lexicographic order. Unrecognised files are skipped with a warning (silenced
by `-q`). Backups (`*.bak`) and metakill's own temp files are never
processed.

### Flags

Every flag has a short and a long form.

| Short | Long                | Type   | Default | Purpose |
|-------|---------------------|--------|---------|---------|
| `-d`  | `--directory`       | bool   | false   | Treat arguments as directories, not files |
| `-r`  | `--recursive`       | bool   | false   | Recurse into subdirectories (only with `-d`) |
| `-n`  | `--dry-run`         | bool   | false   | Report only; modify nothing; never prompts |
| `-j`  | `--json`            | bool   | false   | One JSON document on stdout instead of text |
| `-q`  | `--quiet`           | bool   | false   | Errors only (JSON is still printed with `-j`) |
| `-o`  | `--output`          | string | ""      | Write the cleaned file to this path (single file only; no backup, no prompt) |
| `-y`  | `--yes`             | bool   | false   | Skip the confirmation prompt (required without a TTY) |
| `-b`  | `--backup`          | bool   | true    | Create `FILE.bak` before editing in place; `--backup=false` disables |
| `-k`  | `--keep-tags`       | bool   | false   | Keep Title/Artist/Album/Track/Year |
| `-t`  | `--keep-thumbnails` | bool   | false   | Keep embedded thumbnails and cover art (implies `-k`) |
| `-c`  | `--keep-icc`        | bool   | false   | Keep ICC colour profiles in images |
| `-z`  | `--no-zero-pad`     | bool   | false   | Do not zero pre-existing padding/free-space regions |
| `-x`  | `--external-tools`  | bool   | false   | Allow running 7-Zip or rar to repack 7z and RAR archives |
| `-a`  | `--convert-rar`     | bool   | false   | Convert RAR to 7z when no rar tool is installed (implies `-x`) |
| `-v`  | `--verbose`         | bool   | false   | One line per field, with its namespace |
| `-h`  | `--help`            |        |         | Help |
|       | `--version`         |        |         | Print the version |

`--version` has no short form because `-v` is `--verbose`. A hidden
`--selftest` flag runs the built-in smoke test on generated fixtures.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success, and every processed file is fully clean |
| 1 | At least one file failed, or you answered "no" at the prompt |
| 2 | Usage error (unknown flag, bad arguments, `-y` missing without a TTY); nothing was touched |
| 3 | No failures, but some metadata could not be removed (see `fully_clean` in JSON) |

Skipped files never cause a non-zero exit code. This covers unrecognised
files and recognised packages that metakill deliberately won't repack
(such as a signed APK); the reason is printed as a warning.

Errors always go to stderr. `-q` hides warnings but never hides a file
that is not fully clean: that is reported as an error line on stderr and
with exit code 3.

### Confirmation

When stdin is a terminal and `-y` is not given, metakill inspects
everything first and asks once:

```text
metakill: remove 25 metadata fields from 1 file(s) in place (backups on)? [y/N]
```

Without a terminal, `-y` is required (exit 2 otherwise). Dry runs never
prompt, and neither do `-o` runs, since the original is not modified.

### Paths in output

Paths are printed **as given** on the command line, cleaned with
`filepath.Clean`. Directory-mode paths are the directory argument joined
with the file's relative path. This keeps output reproducible (and testable)
regardless of where it runs.

## Output

### Dry run, text

```text
$ metakill -n photo.jpg
photo.jpg  [JPEG]
  GPS:             GPSLatitudeRef = "N", GPSLatitude = "37 46 29.64", GPSLongitudeRef = "W", GPSLongitude = "122 25 9.84", GPSAltitude = "16", exif:GPSLatitude = "37,46.494N", City = "San Francisco"
  Timestamps:      DateTime = "2023:04:12 18:33:02", DateTimeOriginal = "2023:04:12 18:33:02", DateTimeDigitized = "2023:04:12 18:33:02", xmp:CreateDate = "2023-04-12T18:33:02"
  Device:          Make = "Apple", Model = "iPhone 14 Pro", BodySerialNumber = "SN-0042-SECRET", LensModel = "iPhone 14 Pro back camera"
  Author:          Artist = "Jane Photographer", dc:creator = "Jane Photographer", By-line = "Jane Photographer"
  Software:        Software = "iOS 16.4", xmp:CreatorTool = "Adobe Photoshop 24.0"
  Thumbnail:       ThumbnailImage = <binary, 631 bytes>
  ICC profile:     ICC_PROFILE = <binary, 150 bytes>
  Custom:          UserComment = "secret comment", Comment = "secret comment", TrailingData = <binary, 25 bytes>
  Kept:            Orientation = "6"
  --- 25 fields, ~2.0 KiB removable
```

Fields are grouped into categories: GPS, timestamps, device, author,
software/encoder strings, ID tags, thumbnails, ICC profiles and custom (everything else).
Printable values are shown quoted; binary values as `<binary, N bytes>`.
With `-v` each field gets its own line as `namespace:key = value` (for
example `exif:GPSLatitude = "37 46 29.64"`). Files nested inside other files
(images in a DOCX, a JPEG inside a PDF, cover art) are shown indented with
`>`. After a real run the footer reads `--- N fields removed, X saved`.

### Dry run, JSON

`-j` prints one document per run. The example is abridged to five fields.

```json
{
  "dry_run": true,
  "results": [
    {
      "path": "photo.jpg",
      "format": "jpeg",
      "dry_run": true,
      "status": "cleaned",
      "fully_clean": true,
      "fields": [
        {"category": "device",    "namespace": "exif", "key": "Make",     "value": "Apple"},
        {"category": "device",    "namespace": "exif", "key": "Model",    "value": "iPhone 14 Pro"},
        {"category": "software",  "namespace": "exif", "key": "Software", "value": "iOS 16.4"},
        {"category": "timestamp", "namespace": "exif", "key": "DateTime", "value": "2023:04:12 18:33:02"},
        {"category": "thumbnail", "namespace": "exif", "key": "ThumbnailImage", "value": null, "size": 631}
      ],
      "kept": [
        {"category": "custom", "namespace": "exif", "key": "Orientation", "value": "6"}
      ],
      "bytes_removable": 2059,
      "warnings": []
    }
  ],
  "summary": {
    "files": 1, "cleaned": 1, "already_clean": 0, "skipped": 0, "errors": 0,
    "not_fully_clean": 0, "fields_removed": 25, "bytes_removable": 2059
  }
}
```

The schema is identical for real runs except for `dry_run` and the byte
count label: `bytes_removable` becomes `bytes_saved`. Real runs also add
`backup` or `output` where relevant. Other fields:

- **`status`:** one of `cleaned`, `already_clean`, `skipped`, `inspected`
  or `error`. In a dry run, `cleaned` means "would be cleaned". `inspected`
  means metadata was found and listed but the file was deliberately left
  unchanged (a 7z or RAR without `-x`, or without a tool to repack it); such
  files count as not fully clean.
- **`error`:** present only when `status` is `error`.
- **`embedded`:** present only when a nested file was found.
- **Arrays** are always arrays, never `null`.

`bytes_removable` is the size of the metadata found. `bytes_saved` is the
actual change in file size. Formats cleaned in place (HEIF, MKV/WebM, AVI,
WAV) zero their metadata without shrinking, so they report `bytes_saved: 0`.

## Format support

**Full** means everything identifying that metakill knows about is removed,
and the round-trip tests prove it. **Partial** means some metadata is kept by
design and the file is reported with `fully_clean: false` (exit 3) when that
happens.

| Format | Status | What is removed | Notes |
|---|---|---|---|
| **JPEG** | Full | EXIF (incl. GPS, MakerNote), XMP + extended XMP, IPTC/Photoshop, ICC (unless `-c`), JFXX/EXIF thumbnails (unless `-t`), COM, APP3–APP15 (incl. C2PA/JUMBF, MPF), trailing data (motion photos) | EXIF is replaced by a minimal block holding only Orientation, so photos stay upright. JFIF and Adobe APP14 (needed for CMYK) are kept. |
| **PNG / APNG** | Full | tEXt, zTXt, iTXt (incl. XMP), eXIf (Orientation kept), tIME, iCCP (unless `-c`), private ancillary chunks, trailing data | Rendering chunks (gAMA, sRGB, pHYs, cICP, APNG frames…) are kept. |
| **GIF** | Full | Comments, XMP, unknown application extensions, ICC (unless `-c`), trailing data | Loop extension kept. |
| **WebP** | Full | EXIF, XMP, ICCP (unless `-c`), unknown chunks | VP8X flags updated. EXIF Orientation is not kept (most decoders ignore it). |
| **TIFF** | Full | Every tag not needed to decode (whitelist), EXIF/GPS IFDs, XMP, IPTC, Photoshop, GeoTIFF tags, ICC (unless `-c`), reduced-resolution pages (unless `-t`) | Rebuilt from scratch with relocated strips/tiles. BigTIFF, DNG and files with SubIFDs (raw/pyramidal) are refused. |
| **HEIC / HEIF / AVIF** | Partial | Exif and XMP items (zeroed in place) | Thumbnail image items are kept, because removing them means rewriting item locations: reported not fully clean unless `-t`. The ICC profile (`colr`) is always kept (warning). Image sequences (`moov`) are not scrubbed. |
| **SVG** | Full | `<metadata>`, `<title>` (kept with `-k`), `<desc>`, comments, processing instructions, all Inkscape/Sodipodi/Illustrator/Sketch/Serif/RDF/DC/CC elements, attributes and namespace declarations (incl. `sodipodi:docname`, export paths) | Embedded `data:` images are cleaned recursively. SVGZ is not supported, and Illustrator files that use custom DOCTYPE entities may fail to parse. |
| **PDF** | Full\* | `/Info` (Title kept with `-k`), every `/Metadata` XMP stream, `/PieceInfo`, `/LastModified`, `/SpiderInfo`, annotation authors and dates, attachment dates, incremental-update history (old revisions), EXIF in embedded JPEG images | The trailer `/ID` is regenerated. \*Embedded file attachments are not scanned (reported not fully clean). Encrypted PDFs are refused. Digital signatures are invalidated (warning). |
| **DOCX / XLSX / PPTX** (+ macro and template variants) | Full | `core.xml` (all but language; title with `-k`), `app.xml` application/version/company/manager/template/total time, `custom.xml`, thumbnails (unless `-t`), comment and tracked-change authors (→ `Author`), initials (→ `A`) and dates (→ 1980-01-01), `people.xml`/persons user IDs, rsid session IDs, document variables, attached template paths, absolute save paths, revision IDs, Excel `fileVersion` build numbers | Embedded media and nested documents are cleaned recursively. Relationships and content types are pruned to match. ZIP timestamps, extra fields, comments and OS clutter are removed. NuGet, VSIX and APPX packages share this layout but are skipped (see ZIP). |
| **ODT / ODS / ODP / ODG** | Full | `meta.xml` (all but language/statistics; title with `-k`), printer name/setup, data-source and rsid settings, tracked-change and annotation authors/dates/initials, `officeooo:rsid` attributes, thumbnails (unless `-t`), digital signatures | The manifest is updated, and `mimetype` stays first and stored. Pictures are cleaned recursively. |
| **EPUB** | Full | Creator (kept with `-k`), contributors, publisher, rights, dates, subjects, descriptions, extra identifiers, Calibre/other `<meta>`, refinements of removed elements, Calibre bookmarks, iTunes plists | The fields EPUB requires are kept: the unique `dc:identifier`, `dc:title` and `dc:language`, plus `dcterms:modified`, which is reset to 2000-01-01. Images are cleaned recursively. |
| **Plain text / Markdown** | Recognised, no-op | Nothing | `.txt`, `.text`, `.md`, `.markdown` |
| **ZIP** (`.zip`, `.cbz`, unsigned `.jar`) | Full\* | Member timestamps (→ 1980-01-01), extra fields (uid/gid, Unix/NTFS times), archive and member comments, OS clutter (`__MACOSX/`, `._*` AppleDouble files, `.DS_Store`, `Thumbs.db`, `desktop.ini`), and the metadata inside every member a handler recognises, including nested archives | Unrecognised members are copied byte-for-byte without being decompressed. Permissions are normalised to 644/755, keeping directories, symlinks and executable bits. Member names are never changed, but home-directory-looking paths produce a warning. \*Members over 512 MB that a handler recognises are copied unchanged and reported not fully clean. Encrypted archives are refused. Signed or integrity-checked packages (APK/AAB, IPA, signed JAR/XPI, Python wheels, NuGet, VSIX, APPX/MSIX) are skipped, because repacking would break them. |
| **TAR** (`.tar`, `.tar.gz`/`.tgz`, `.tar.xz`, `.tar.zst`, `.tar.bz2`) | Full\* | Modification times (→ Unix epoch), access and change times, owner user/group names and IDs, extended attributes (e.g. macOS quarantine and download-source "where-from" data, SELinux labels), all other PAX records, global PAX headers, OS clutter (`._*` AppleDouble files, `.DS_Store`, `Thumbs.db`, `desktop.ini`), the gzip header's original file name/time/comment, and the metadata inside every member a handler recognises, including nested archives | Streamed, so unrecognised members are never held in memory. Permissions are normalised to 644/755, keeping executable bits, and directories, symlinks, hard links and device entries are kept. Member names are never changed (home-directory-looking paths produce a warning). GNU sparse files are written out in full. \*Recognised members over 512 MB are copied unchanged and reported not fully clean. bzip2 output is decompressed again with Go's standard library and compared before it is accepted, because the bzip2 encoder is an older third-party library. For compressed archives, `bytes_removable` is measured on the uncompressed data. |
| **Compressed single files** (`.gz`, `.xz`, `.zst`, `.bz2`, `.svgz`) | Full | The gzip header's original file name, time and comment, plus the metadata in the content if a handler recognises it (e.g. `.svgz` is cleaned as SVG) | xz, zstd and bzip2 wrappers carry no metadata of their own, so they are only rewritten when the content changes. |
| **7z** | Full with `-x`\*, inspect-only without | Modification times (→ 1980-01-01), creation and access times, OS clutter, and the metadata inside every member a handler recognises | Read in pure Go; repacked with 7-Zip (`7zz`, `7z`, `7za`) or `bsdtar`. See [7z and RAR](#7z-and-rar). \*Encrypted archives and archives containing links are refused; incomplete ones (multi-volume sets, truncated files) are skipped. |
| **RAR** (RAR4 and RAR5) | Full with `-x` + rar, or converted to 7z with `-a`; inspect-only otherwise | Modification times (→ 1980-01-01), creation and access times, Unix owner names and IDs, the archive comment, owner/ACL/NTFS-stream records, authenticity data, the locked flag, OS clutter, and the metadata inside every member a handler recognises | Read in pure Go; repacked with rar/WinRAR. Encrypted and multi-volume archives and links are refused or skipped as for 7z. |
| **MP3** | Full | ID3v2.2/2.3/2.4 (leading and appended), ID3v1 and v1 extended, APEv1/v2, Lyrics3v2, the LAME/Xing encoder version string (CRC recomputed) | With `-k`, a fresh minimal ID3v2.4 tag holds the kept fields. Audio frames are copied unchanged. |
| **FLAC** | Full | Vorbis comments (vendor string blanked), PICTURE (unless `-t`), APPLICATION blocks, stray ID3v2/ID3v1 | PADDING is kept but zeroed. |
| **Ogg Vorbis / Opus / Theora / Speex** | Full | Comment headers (vendor blanked), pictures (unless `-t`), Opus extra tag data | Header pages are re-paginated, and later page sequence numbers and CRCs are rewritten. Ogg FLAC is refused. Streams with unknown codecs are reported not fully clean. |
| **M4A / AAC** (MP4 audio) | Full | Handled by the MP4 handler | |
| **WAV / BWF / RF64** | Full | LIST INFO (essentials kept with `-k`), `bext` (originator, dates, UMID, coding history), `id3 ` chunk, iXML, aXML, cart, DISP, XMP, umid | Cleaned in place: chunks become zeroed `JUNK`. |
| **MP4 / M4V / MOV / 3GP** | Full\* | `udta` and `meta` boxes (iTunes, QuickTime, 3GPP, `©xyz` GPS, Apple `com.apple.quicktime.*` keys incl. location, make, model, software), XMP and other `uuid` boxes, `prft`, `mvhd`/`tkhd`/`mdhd` creation and modification times, handler names, compressor names, timed-metadata tracks (GoPro `gpmd`, `camm`, Apple `mebx`, Sony `rtmd`) and their samples | The file is compacted and all `stco`/`co64`/`tfhd` offsets are rewritten. \*Encoder settings embedded inside the video bitstream (e.g. x264 SEI) are not touched. |
| **MKV / WebM** | Full | Title (kept with `-k`), DateUTC, segment UIDs/filenames, MuxingApp/WritingApp (blanked), track names and codec names, Tags (essentials kept with `-k`), Attachments (kept but cleaned with `-t`), matching SeekHead entries | Cleaned in place with Void elements (like `mkvpropedit`). CRC-32 elements of edited masters are voided. |
| **AVI** | Partial | LIST INFO, `IDIT`/`ISMP` dates, stream names, vendor LISTs (e.g. Nikon `ncdt`) | Cleaned in place with `JUNK`. `strd` codec/camera data is kept, because removing it can break decoding (reported not fully clean when present). |

### What -k, -t and -c keep

| Format family | `-k` keeps | `-t` also keeps |
|---|---|---|
| Audio and video tags | Title, Artist, Album, Track, Year (as `YYYY`) | Cover art (cleaned) |
| PDF, Office, ODF, SVG | Title | Thumbnails (cleaned) |
| EPUB | Creator (the book's author); title is always kept | Images are content and are always kept |
| Images | Nothing. Orientation is always kept in JPEG/PNG | EXIF/JFXX thumbnails, TIFF reduced-resolution pages |

`-c` keeps ICC profiles in JPEG, PNG, GIF, WebP and TIFF.

These are always removed regardless of flags: GPS, timestamps (except
the kept year), device identifiers (make, model, serial numbers, lens,
owner), and software/encoder strings.

### Zero padding

Removed metadata bytes never stay in the output. Formats cleaned in place
always zero what they remove. `-z/--no-zero-pad` only affects padding that
already existed: MP4 `free`/`skip`, MKV `Void`, FLAC `PADDING`, RIFF
`JUNK`/`PAD `, HEIF `free`. These often contain leftovers from earlier
edits, and by default they are zeroed and reported.

## Safety model

1. Each file is inspected first. Nothing is written for files with nothing
   to remove.
2. The cleaned copy is written to a temp file (`.metakill-*.tmp`) in the
   same directory.
3. **Post-write validation:** the same handler re-opens the temp file. It
   must parse, and it must contain nothing that handler would remove.
   Otherwise the temp file is deleted and the original is left untouched.
4. Only then is `FILE.bak` created (never overwriting an existing backup:
   an existing `.bak` is an error) and the temp file atomically renamed
   over the original. File permissions are preserved, and symlinks are
   followed so the link itself survives.

Because the original is never modified before validation succeeds, there
is nothing to restore when validation fails.

## 7z and RAR

metakill reads 7z and RAR archives in pure Go, so dry runs, reports and
post-write validation never run anything external. Writing them is
different: there is no mature pure-Go 7z writer, and RAR can only be
created by RARLAB's own `rar`/WinRAR. Repacking therefore uses a tool that
is already installed, and only when you allow it:

| Format | Tools found | Without `-x` | With `-x` | With `-a` |
|---|---|---|---|---|
| 7z | 7-Zip or `bsdtar` | Inspected only (exit 3) | Cleaned and repacked as 7z | Same as `-x` |
| RAR | rar/WinRAR | Inspected only (exit 3) | Cleaned and repacked as RAR | Same as `-x` |
| RAR | Only 7-Zip or `bsdtar` | Inspected only (exit 3) | Inspected only (exit 3) | Cleaned and written as `name.7z`; the original is kept as `name.rar.bak` (deleted with `--backup=false`) |
| Either | None | Inspected only (exit 3) | Inspected only (exit 3) | Inspected only (exit 3) |

How a repack works:

1. Members are extracted into a private temporary directory (owner-only
   permissions), refusing any name that would escape it.
2. Each member is cleaned with the normal handlers (nested archives
   included) and OS clutter is dropped.
3. Every file and directory gets the timestamp 1980-01-01 and permissions
   644/755, so the new archive stores only those values. 7-Zip is also told
   not to store creation or access times; rar is never asked to store
   owners, comments or extra times.
4. The tool runs by absolute path with its arguments passed directly (never
   through a shell). The report names the tool used.
5. The new archive is read back and validated in pure Go before it replaces
   the original, as for every other format. The temporary directory is
   always deleted.

Conversion never overwrites an existing `name.7z`. With `-o`, the output
name must end in `.7z`. A RAR nested inside another file is never converted
(a member cannot change its name); it is reported and the outer file is
marked not fully clean.

Limitations: on Windows, executable bits of 7z/RAR members are not kept
(Windows filesystems have no such bit). RAR repacking with a real `rar` is
not covered by CI, because rar needs a paid licence; RAR reading and
RAR → 7z conversion are.

## Network

metakill makes no network calls and has no telemetry. Its own packages
import nothing from `net`, and a test enforces this. pdfcpu, the PDF
library, links `net/http` for features metakill never uses: signature
revocation checks, link validation and remote images. metakill runs pdfcpu
with `Offline` set and in its stateless configuration mode, so pdfcpu also
never reads or creates `~/.config/pdfcpu` (a test checks this).

## cgo

None. Every dependency is pure Go and release builds use `CGO_ENABLED=0`.
The only cgo use is optional, during development: `make test-race` runs the
race detector, which requires cgo.

## Dependencies

| Module | Why |
|---|---|
| `github.com/spf13/cobra` (+ `pflag`) | CLI, short/long flags, grouped help |
| `golang.org/x/term` | TTY detection for the confirmation prompt |
| `github.com/pdfcpu/pdfcpu` | PDF parsing and rewriting |
| `github.com/abema/go-mp4` | ISO-BMFF box parsing (MP4/MOV/M4A/HEIF) |
| `github.com/beevik/etree` | Namespace-preserving XML editing (SVG, OOXML, ODF, EPUB, XMP) |
| `github.com/ulikunitz/xz` | xz compression (`.tar.xz`, `.xz`) |
| `github.com/klauspost/compress` | zstd compression (`.tar.zst`, `.zst`) |
| `github.com/bodgit/sevenzip` | Reading 7z (plus the pure-Go decompressors it uses: brotli, lz4, PPMd) |
| `github.com/nwaples/rardecode/v2` | Reading RAR4 and RAR5. Archive comments, owner records and similar headers are found by a small scanner in metakill itself |
| `github.com/dsnet/compress` | bzip2 *writing* (Go's standard library can only read bzip2). Its last release was in 2019, so every output is verified with the standard-library reader before it is accepted |

EXIF/TIFF, IPTC, ID3, APE, Vorbis comments, FLAC, Ogg, RIFF and EBML
(Matroska) are handled by small in-repo parsers in `internal/meta`. This is
because metakill needs to write those formats, not just read them.
`golang.org/x/image` is used only in tests, to decode TIFF and WebP output.

## Development

```console
make build      # bin/metakill
make test       # unit, round-trip, golden and CLI integration tests
make lint       # gofmt, go vet, golangci-lint (if installed), govulncheck
make fixtures   # regenerate testdata/fixtures from internal/fixtures
make golden     # regenerate testdata/golden after an intentional output change
make release    # cross-compile all targets into dist/ with SHA256SUMS
make selftest   # run the built binary's --selftest
```

Fixtures are generated by Go code in `internal/fixtures` rather than
checked in as opaque samples, so every planted value is visible in the
source. See [CONTRIBUTING.md](CONTRIBUTING.md) for adding a format.

## Licence

MIT. See [LICENSE](LICENSE).
