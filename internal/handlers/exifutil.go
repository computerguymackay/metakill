package handlers

import (
	"github.com/computerguymackay/metakill/internal/meta/exif"
	"github.com/computerguymackay/metakill/internal/report"
)

// scrubEXIF reports the fields in a TIFF-structured EXIF block and returns
// the minimal block to write back (Orientation, plus the thumbnail with -t),
// or nil to drop EXIF entirely. keepOrientation is false for formats where
// orientation lives elsewhere (HEIF).
func scrubEXIF(tiff []byte, opts Options, rep *report.Report, keepOrientation bool) []byte {
	f, err := exif.Parse(tiff)
	if err != nil {
		rep.Add(report.Binary(report.CatCustom, "exif", "EXIF (unparseable)", int64(len(tiff))))
		return nil
	}
	s := f.Summarize()
	rep.Add(s.Fields...)
	var orient uint16
	if keepOrientation && s.Orientation >= 2 && s.Orientation <= 8 {
		orient = s.Orientation
		rep.Keep(report.Text(report.CatCustom, "exif", "Orientation", itoa(int(orient))))
	} else if s.Orientation != 0 && s.Orientation != 1 {
		rep.Add(report.Text(report.CatCustom, "exif", "Orientation", itoa(int(s.Orientation))))
	}
	var thumb []byte
	if len(s.Thumbnail) > 0 {
		f := report.Binary(report.CatThumbnail, "exif", "ThumbnailImage", int64(len(s.Thumbnail)))
		if opts.KeepThumbnails {
			rep.Keep(f)
			thumb = s.Thumbnail
			if out, sub, ok := StripEmbedded("exif thumbnail", s.Thumbnail, opts); ok {
				if out != nil {
					thumb = out
				}
				if sub.TotalFields() > 0 || !sub.FullyClean {
					rep.AddEmbedded(sub)
				}
			}
		} else {
			rep.Add(f)
		}
	}
	return exif.Minimal(f.Order, orient, thumb)
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = digits[n%10]
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
