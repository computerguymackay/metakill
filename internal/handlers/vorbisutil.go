package handlers

import (
	"encoding/base64"
	"strings"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/internal/meta/vorbis"
	"github.com/computerguymackay/metakill/internal/report"
)

// scrubVorbis reports a comment block and returns what to write back: an
// empty vendor string plus the essential tags (with -k) and pictures (with
// -t, re-encoded after cleaning). keep is false when nothing survives.
func scrubVorbis(blk vorbis.Block, ns string, opts Options, rep *report.Report) (out vorbis.Block, keep bool) {
	if v := strings.TrimSpace(blk.Vendor); v != "" {
		rep.Add(report.Text(report.CatSoftware, ns, "vendor", v))
	}
	for _, c := range blk.Comments {
		cat, canon := classify.Vorbis(c.Key)
		if strings.EqualFold(c.Key, "METADATA_BLOCK_PICTURE") {
			raw, err := base64.StdEncoding.DecodeString(c.Value)
			f := report.Binary(report.CatThumbnail, ns, c.Key, int64(len(raw)))
			if err == nil && opts.KeepThumbnails {
				if pic, err := vorbis.ParsePicture(raw); err == nil {
					pic = cleanPicture(pic, opts, rep)
					out.Comments = append(out.Comments, vorbis.Comment{Key: c.Key, Value: base64.StdEncoding.EncodeToString(vorbis.EncodePicture(pic))})
					rep.Keep(f)
					continue
				}
			}
			rep.Add(f)
			continue
		}
		f := report.Text(cat, ns, c.Key, c.Value)
		if canon != "" && keepEssential(opts, canon) {
			v := c.Value
			if canon == classify.Year {
				v = yearOnly(v)
			}
			if v != "" {
				out.Comments = append(out.Comments, vorbis.Comment{Key: c.Key, Value: v})
				rep.Keep(f)
				continue
			}
		}
		rep.Add(f)
	}
	return out, len(out.Comments) > 0
}

// cleanPicture strips metadata from the image inside a picture block and
// clears its description.
func cleanPicture(pic vorbis.Picture, opts Options, rep *report.Report) vorbis.Picture {
	if pic.Description != "" {
		rep.Add(report.Text(report.CatCustom, "picture", "description", pic.Description))
		pic.Description = ""
	}
	if out, sub, ok := StripEmbedded("cover art", pic.Data, opts); ok {
		if out != nil {
			pic.Data = out
		}
		if sub.TotalFields() > 0 || !sub.FullyClean {
			rep.AddEmbedded(sub)
		}
	}
	return pic
}
