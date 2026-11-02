// Package iptc summarises Photoshop image resource blocks (APP13 / TIFF tag
// 34377) and the IPTC-IIM records inside them.
package iptc

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/computerguymackay/metakill/internal/meta/classify"
	"github.com/computerguymackay/metakill/report"
)

// PhotoshopHeader prefixes a JPEG APP13 segment.
var PhotoshopHeader = []byte("Photoshop 3.0\x00")

var datasetNames = map[byte]string{
	0: "RecordVersion", 3: "ObjectTypeReference", 5: "ObjectName", 7: "EditStatus",
	10: "Urgency", 12: "SubjectReference", 15: "Category", 20: "SupplementalCategories",
	22: "FixtureIdentifier", 25: "Keywords", 26: "ContentLocationCode", 27: "ContentLocationName",
	30: "ReleaseDate", 35: "ReleaseTime", 37: "ExpirationDate", 38: "ExpirationTime",
	40: "SpecialInstructions", 55: "DateCreated", 60: "TimeCreated", 62: "DigitalCreationDate",
	63: "DigitalCreationTime", 65: "OriginatingProgram", 70: "ProgramVersion",
	75: "ObjectCycle", 80: "By-line", 85: "By-lineTitle", 90: "City", 92: "Sub-location",
	95: "Province-State", 100: "Country-PrimaryLocationCode", 101: "Country-PrimaryLocationName",
	103: "OriginalTransmissionReference", 105: "Headline", 110: "Credit", 115: "Source",
	116: "CopyrightNotice", 118: "Contact", 120: "Caption-Abstract", 122: "Writer-Editor",
}

// Resources lists the fields in a Photoshop image resource block (without the
// "Photoshop 3.0" prefix).
func Resources(b []byte) []report.Field {
	var out []report.Field
	for len(b) >= 12 && bytes.Equal(b[:4], []byte("8BIM")) {
		id := binary.BigEndian.Uint16(b[4:])
		nameLen := int(b[6])
		p := 7 + nameLen
		if p%2 == 1 {
			p++
		}
		if p+4 > len(b) {
			break
		}
		size := int(binary.BigEndian.Uint32(b[p:]))
		p += 4
		if size < 0 || p+size > len(b) {
			break
		}
		data := b[p : p+size]
		switch id {
		case 0x0404:
			out = append(out, Records(data)...)
		case 0x0409, 0x040C:
			out = append(out, report.Binary(report.CatThumbnail, "photoshop", "Thumbnail", int64(size)))
		case 0x040F:
			out = append(out, report.Binary(report.CatICC, "photoshop", "ICCProfile", int64(size)))
		case 0x0422:
			out = append(out, report.Binary(report.CatCustom, "photoshop", "EXIFData", int64(size)))
		case 0x0424:
			out = append(out, report.Binary(report.CatCustom, "photoshop", "XMP", int64(size)))
		default:
			out = append(out, report.Binary(report.CatCustom, "photoshop", fmt.Sprintf("Resource0x%04X", id), int64(size)))
		}
		p += size
		if p%2 == 1 {
			p++
		}
		if p > len(b) {
			break
		}
		b = b[p:]
	}
	return out
}

// Records lists IPTC-IIM datasets.
func Records(b []byte) []report.Field {
	var out []report.Field
	for len(b) >= 5 && b[0] == 0x1C {
		rec, ds := b[1], b[2]
		n := int(binary.BigEndian.Uint16(b[3:]))
		p := 5
		if n&0x8000 != 0 { // extended dataset length
			l := n & 0x7FFF
			if l > 4 || p+l > len(b) {
				break
			}
			n = 0
			for _, c := range b[p : p+l] {
				n = n<<8 | int(c)
			}
			p += l
		}
		if p+n > len(b) {
			break
		}
		val := b[p : p+n]
		b = b[p+n:]
		if rec == 2 && ds == 0 {
			continue // record version: structural
		}
		name := datasetNames[ds]
		if rec != 2 || name == "" {
			name = fmt.Sprintf("%d:%d", rec, ds)
		}
		out = append(out, report.Text(classify.IPTC(rec, ds), "iptc", name, string(val)))
	}
	return out
}
