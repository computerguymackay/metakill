package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/computerguymackay/metakill/internal/meta/exif"
	"github.com/computerguymackay/metakill/internal/meta/iptc"
	"github.com/computerguymackay/metakill/internal/meta/xmp"
	"github.com/computerguymackay/metakill/internal/report"
)

// tiffHandler rebuilds a TIFF keeping only the tags needed to decode each
// page (a whitelist), relocating strip/tile data. Everything else (EXIF,
// GPS, XMP, IPTC, Photoshop blocks, make/model/software/date/artist,
// GeoTIFF georeferencing, private tags) is dropped. Reduced-resolution
// thumbnail pages are dropped unless -t. BigTIFF and raw variants (DNG, or
// files with SubIFDs) are refused rather than half-cleaned.
type tiffHandler struct{}

func init() { Register(tiffHandler{}) }

func (tiffHandler) Name() string { return "tiff" }

func (tiffHandler) CanHandle(r io.ReadSeeker, path string) bool {
	b := peek(r, 4)
	return bytes.Equal(b, []byte("II*\x00")) || bytes.Equal(b, []byte("MM\x00*")) || hasExt(path, ".tif", ".tiff")
}

// Tags needed to decode an image. 273/279, 324/325 and 513/514 are handled
// as relocatable data.
var tiffStructural = map[uint16]bool{
	254: true, 255: true, 256: true, 257: true, 258: true, 259: true, 262: true, 263: true,
	264: true, 265: true, 266: true, 274: true, 277: true, 278: true, 280: true, 281: true,
	282: true, 283: true, 284: true, 286: true, 287: true, 290: true, 291: true, 292: true,
	293: true, 296: true, 297: true, 301: true, 317: true, 318: true, 319: true, 320: true,
	321: true, 322: true, 323: true, 332: true, 338: true, 339: true, 340: true, 341: true,
	347: true, 512: true, 515: true, 517: true, 518: true, 519: true, 520: true, 521: true,
	529: true, 530: true, 531: true, 532: true,
}

func (tiffHandler) Inspect(r io.ReadSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	_, rep, err := processTIFF(data, opts)
	return rep, err
}

func (tiffHandler) Strip(r io.ReadSeeker, w io.WriteSeeker, opts Options) (report.Report, error) {
	data, err := readAll(r)
	if err != nil {
		return report.Report{}, err
	}
	out, rep, err := processTIFF(data, opts)
	if err != nil {
		return rep, err
	}
	_, err = w.Write(out)
	return rep, err
}

func processTIFF(data []byte, opts Options) ([]byte, report.Report, error) {
	rep := report.New("tiff")
	f, err := exif.Parse(data)
	if err != nil {
		if errors.Is(err, exif.ErrBigTIFF) {
			return nil, rep, fmt.Errorf("tiff: %w (BigTIFF)", ErrUnsupported)
		}
		return nil, rep, fmt.Errorf("tiff: %w", err)
	}
	if _, ok := f.Chain[0].Find(0xC612); ok {
		return nil, rep, fmt.Errorf("tiff: DNG raw files are %w", ErrUnsupported)
	}
	var chain []*exif.WIFD
	for i, ifd := range f.Chain {
		if _, ok := ifd.Find(exif.TagSubIFDs); ok {
			return nil, rep, fmt.Errorf("tiff: files with SubIFDs (raw/pyramidal) are %w", ErrUnsupported)
		}
		name := "Image"
		if i > 0 {
			name = fmt.Sprintf("Page%d", i+1)
		}
		if i > 0 {
			if e, ok := ifd.Find(254); ok {
				if v, _ := f.Uint(e, 0); v&1 == 1 && !opts.KeepThumbnails {
					rep.Add(report.Binary(report.CatThumbnail, "tiff", name+" (reduced-resolution)", int64(tiffDataSize(f, ifd))))
					continue
				}
			}
		}
		w := &exif.WIFD{Blobs: map[uint16][][]byte{}}
		for _, e := range ifd.Entries {
			switch {
			case e.Tag == 273 || e.Tag == 324 || e.Tag == 513:
				pieces, err := tiffPieces(f, ifd, e.Tag)
				if err != nil {
					return nil, rep, err
				}
				w.Blobs[e.Tag] = pieces
			case e.Tag == 279 || e.Tag == 325 || e.Tag == 514:
				// written with their offsets
			case tiffStructural[e.Tag]:
				w.Entries = append(w.Entries, exif.WEntry{Tag: e.Tag, Type: e.Type, Count: e.Count, Data: e.Value})
			case e.Tag == exif.TagICC:
				fl := report.Binary(report.CatICC, "tiff", "ICCProfile", int64(len(e.Value)))
				if opts.KeepICC {
					rep.Keep(fl)
					w.Entries = append(w.Entries, exif.WEntry{Tag: e.Tag, Type: e.Type, Count: e.Count, Data: e.Value})
				} else {
					rep.Add(fl)
				}
			case e.Tag == exif.TagXMP:
				rep.Add(xmp.Fields(e.Value)...)
			case e.Tag == exif.TagIPTC:
				rep.Add(iptc.Records(e.Value)...)
			case e.Tag == exif.TagPhotoshop:
				rep.Add(iptc.Resources(e.Value)...)
			case e.Tag == exif.TagExifIFD || e.Tag == exif.TagGPSIFD || e.Tag == exif.TagInteropIFD:
				// sub-IFD contents are reported below
			case e.Tag >= 33550 && e.Tag <= 34737 && isGeoTag(e.Tag):
				rep.Add(report.Binary(report.CatGPS, "geotiff", exif.TagName(name, e.Tag), int64(len(e.Value))))
			default:
				rep.Add(exif.Field(f.Order, name, e))
			}
		}
		chain = append(chain, w)
	}
	for _, sub := range []*exif.IFD{f.Exif, f.GPS, f.Interop} {
		if sub == nil {
			continue
		}
		for _, e := range sub.Entries {
			if e.Tag == exif.TagInteropIFD {
				continue
			}
			rep.Add(exif.Field(f.Order, sub.Name, e))
		}
	}
	if len(chain) == 0 {
		return nil, rep, errors.New("tiff: no image pages")
	}
	out := exif.Encode(f.Order, chain)
	rep.Bytes = int64(len(data) - len(out))
	if rep.Bytes < 0 {
		rep.Bytes = 0
	}
	return out, rep, nil
}

func isGeoTag(t uint16) bool {
	switch t {
	case 33550, 33922, 34264, 34735, 34736, 34737:
		return true
	}
	return false
}

// tiffPieces returns the data pieces referenced by an offset tag.
func tiffPieces(f *exif.File, ifd *exif.IFD, offTag uint16) ([][]byte, error) {
	off, _ := ifd.Find(offTag)
	cnt, ok := ifd.Find(exif.BlobPairs[offTag])
	if !ok {
		return nil, fmt.Errorf("tiff: tag %d without byte counts", offTag)
	}
	offs := exif.Uints(f.Order, off)
	cnts := exif.Uints(f.Order, cnt)
	if len(offs) != len(cnts) {
		return nil, fmt.Errorf("tiff: %d offsets but %d byte counts", len(offs), len(cnts))
	}
	pieces := make([][]byte, len(offs))
	for i := range offs {
		if offs[i]+cnts[i] > uint64(len(f.Data)) {
			return nil, fmt.Errorf("tiff: image data out of range")
		}
		pieces[i] = f.Data[offs[i] : offs[i]+cnts[i]]
	}
	return pieces, nil
}

func tiffDataSize(f *exif.File, ifd *exif.IFD) int {
	n := 0
	for _, t := range []uint16{273, 324} {
		if p, err := tiffPieces(f, ifd, t); err == nil {
			for _, x := range p {
				n += len(x)
			}
		}
	}
	return n
}
