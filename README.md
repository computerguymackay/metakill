# metakill

`metakill` is a single, dependency-free binary that strips identifying
metadata from images, documents, audio and video: GPS coordinates,
timestamps, camera and device identifiers, software/encoder strings, authors,
comments, thumbnails and (by default) media ID tags.

- **Pure Go, no cgo, no external tools.** Nothing is shelled out (no
  ffmpeg, exiftool or ImageMagick). Builds for linux/amd64, linux/arm64,
  darwin/amd64, darwin/arm64 and windows/amd64 with `CGO_ENABLED=0`.
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

- **`status`:** one of `cleaned`, `already_clean`, `skipped` or `error`.
  In a dry run, `cleaned` means "would be cleaned".
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
| **DOCX / XLSX / PPTX** (+ macro and template variants) | Full | `core.xml` (all but language; title with `-k`), `app.xml` application/version/company/manager/template/total time, `custom.xml`, thumbnails (unless `-t`), comment and tracked-change authors (→ `Author`), initials (→ `A`) and dates (→ 1980-01-01), `people.xml`/persons user IDs, rsid session IDs, document variables, attached template paths, absolute save paths, revision IDs, Excel `fileVersion` build numbers | Embedded media and nested documents are cleaned recursively. Relationships and content types are pruned to match. ZIP timestamps, extra fields and comments are removed. |
| **ODT / ODS / ODP / ODG** | Full | `meta.xml` (all but language/statistics; title with `-k`), printer name/setup, data-source and rsid settings, tracked-change and annotation authors/dates/initials, `officeooo:rsid` attributes, thumbnails (unless `-t`), digital signatures | The manifest is updated, and `mimetype` stays first and stored. Pictures are cleaned recursively. |
| **EPUB** | Full | Creator (kept with `-k`), contributors, publisher, rights, dates, subjects, descriptions, extra identifiers, Calibre/other `<meta>`, refinements of removed elements, Calibre bookmarks, iTunes plists | The fields EPUB requires are kept: the unique `dc:identifier`, `dc:title` and `dc:language`, plus `dcterms:modified`, which is reset to 2000-01-01. Images are cleaned recursively. |
| **Plain text / Markdown** | Recognised, no-op | Nothing | `.txt`, `.text`, `.md`, `.markdown` |
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
