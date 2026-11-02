// Package classify is the single place that maps metadata keys from every
// namespace (EXIF, XMP, IPTC, ID3, Vorbis comments, iTunes atoms, PDF Info,
// Office/ODF properties, ...) to report categories, and that decides which
// keys count as essential media ID tags for -k/--keep-tags.
package classify

import (
	"strings"

	"github.com/computerguymackay/metakill/report"
)

// Essential canonical names kept by -k.
const (
	Title  = "title"
	Artist = "artist"
	Album  = "album"
	Track  = "track"
	Year   = "year"
)

// ---------- EXIF ----------

var exifDevice = set("Make", "Model", "SerialNumber", "BodySerialNumber", "CameraSerialNumber",
	"LensMake", "LensModel", "LensSerialNumber", "LensSpecification", "LensInfo",
	"CameraOwnerName", "OwnerName", "ImageUniqueID", "MakerNote", "UniqueCameraModel",
	"LocalizedCameraModel", "CameraLabel")
var exifSoftware = set("Software", "ProcessingSoftware", "HostComputer")
var exifAuthor = set("Artist", "Copyright", "XPAuthor")

// Exif returns the category for an EXIF tag. ifd is "GPS" for the GPS IFD.
func Exif(ifd, name string) report.Category {
	switch {
	case ifd == "GPS":
		return report.CatGPS
	case strings.HasPrefix(name, "DateTime") || strings.HasPrefix(name, "SubSecTime") ||
		strings.HasPrefix(name, "OffsetTime") || name == "PreviewDateTime":
		return report.CatTimestamp
	case exifDevice[name]:
		return report.CatDevice
	case exifSoftware[name]:
		return report.CatSoftware
	case exifAuthor[name]:
		return report.CatAuthor
	}
	return report.CatCustom
}

// ---------- XMP ----------

var xmpTimestamp = set("CreateDate", "ModifyDate", "MetadataDate", "DateTimeOriginal",
	"DateTimeDigitized", "DateCreated", "DateTime", "when", "DateAcquired", "date", "created", "modified")
var xmpDevice = set("Make", "Model", "SerialNumber", "BodySerialNumber", "LensModel", "Lens",
	"LensID", "LensSerialNumber", "LensMake", "LensInfo", "CameraOwnerName", "OwnerName",
	"ImageUniqueID", "CameraSerialNumber")
var xmpSoftware = set("CreatorTool", "Producer", "Software", "softwareAgent")
var xmpAuthor = set("creator", "rights", "Artist", "Copyright", "AuthorsPosition",
	"CaptionWriter", "Credit", "CreatorContactInfo", "Owner", "UsageTerms", "WebStatement",
	"Marked", "Author", "publisher", "contributor")
var xmpLocation = set("City", "State", "Country", "CountryCode", "Location", "Sublocation",
	"ProvinceState", "LocationCreated", "LocationShown", "CountryName")

// XMP returns the category for an XMP property identified by prefix and
// local name (e.g. "exif", "GPSLatitude").
func XMP(prefix, local string) report.Category {
	switch {
	case strings.HasPrefix(local, "GPS") || xmpLocation[local]:
		return report.CatGPS
	case xmpTimestamp[local] || strings.HasSuffix(local, "Date"):
		return report.CatTimestamp
	case xmpDevice[local]:
		return report.CatDevice
	case xmpSoftware[local]:
		return report.CatSoftware
	case xmpAuthor[local] || prefix == "xmpRights":
		return report.CatAuthor
	case prefix == "dc" && local == "title":
		return report.CatIDTag
	}
	return report.CatCustom
}

// ---------- IPTC (IIM record 2) ----------

// IPTC returns the category for an IPTC dataset.
func IPTC(record, dataset byte) report.Category {
	if record != 2 {
		return report.CatCustom
	}
	switch dataset {
	case 80, 85, 110, 115, 116, 118, 122:
		return report.CatAuthor
	case 30, 35, 37, 38, 55, 60, 62, 63:
		return report.CatTimestamp
	case 90, 92, 95, 100, 101:
		return report.CatGPS
	case 65, 70:
		return report.CatSoftware
	}
	return report.CatCustom
}

// ---------- audio tag systems ----------

// ID3 maps an ID3v2 frame ID (2.2 three-letter or 2.3/2.4 four-letter) to a
// category and, for essential frames, the canonical name.
func ID3(id string) (report.Category, string) {
	switch id {
	case "TIT2", "TT2":
		return report.CatIDTag, Title
	case "TPE1", "TP1":
		return report.CatIDTag, Artist
	case "TALB", "TAL":
		return report.CatIDTag, Album
	case "TRCK", "TRK":
		return report.CatIDTag, Track
	case "TYER", "TYE", "TDRC":
		return report.CatTimestamp, Year
	case "TDAT", "TDA", "TIME", "TIM", "TORY", "TOR", "TDOR", "TDEN", "TDTG", "TRDA", "TRD", "TDRL":
		return report.CatTimestamp, ""
	case "TENC", "TEN", "TSSE", "TSS":
		return report.CatSoftware, ""
	case "APIC", "PIC":
		return report.CatThumbnail, ""
	case "TCOP", "TCR", "TOWN", "TPUB", "TPB", "WCOP", "WOAR", "TOLY", "TOL", "TEXT", "TXT",
		"TCOM", "TCM", "TOPE", "TOA":
		return report.CatAuthor, ""
	}
	return report.CatCustom, ""
}

// Vorbis maps a Vorbis comment field name (case-insensitive) to a category
// and canonical name. Also used for APE tags and MKV SimpleTags.
func Vorbis(name string) (report.Category, string) {
	switch strings.ToUpper(strings.ReplaceAll(name, " ", "_")) {
	case "TITLE":
		return report.CatIDTag, Title
	case "ARTIST":
		return report.CatIDTag, Artist
	case "ALBUM":
		return report.CatIDTag, Album
	case "TRACKNUMBER", "TRACK", "PART_NUMBER":
		return report.CatIDTag, Track
	case "DATE", "YEAR", "DATE_RELEASED":
		return report.CatTimestamp, Year
	case "DATE_RECORDED", "DATE_ENCODED", "DATE_TAGGED", "DATE_DIGITIZED", "DATE_WRITTEN",
		"DATE_PURCHASED", "ORIGINALDATE", "ORIGINALYEAR", "CREATION_TIME", "ENCODING_TIME":
		return report.CatTimestamp, ""
	case "ENCODER", "ENCODED-BY", "ENCODEDBY", "ENCODED_BY", "ENCODER_OPTIONS", "ENCODING",
		"ENCODER_SETTINGS", "TOOL", "WRITING_APP", "MUXING_APP":
		return report.CatSoftware, ""
	case "METADATA_BLOCK_PICTURE", "COVERART", "COVER_ART":
		return report.CatThumbnail, ""
	case "COPYRIGHT", "LICENSE", "COMPOSER", "LYRICIST", "PERFORMER", "PUBLISHER",
		"ORGANIZATION", "CONTACT", "AUTHOR", "OWNER", "PURCHASE_OWNER":
		return report.CatAuthor, ""
	case "LOCATION", "RECORDING_LOCATION", "COMPOSITION_LOCATION":
		return report.CatGPS, ""
	}
	return report.CatCustom, ""
}

// Ilst maps an iTunes/QuickTime metadata atom type to a category and
// canonical name. "©" is the 0xA9 byte rendered as a rune.
func Ilst(atom string) (report.Category, string) {
	switch atom {
	case "©nam":
		return report.CatIDTag, Title
	case "©ART":
		return report.CatIDTag, Artist
	case "©alb":
		return report.CatIDTag, Album
	case "trkn":
		return report.CatIDTag, Track
	case "©day":
		return report.CatTimestamp, Year
	case "©too", "©enc", "©swr", "©mak", "©mod", "©fmt", "©req":
		if atom == "©mak" || atom == "©mod" {
			return report.CatDevice, ""
		}
		return report.CatSoftware, ""
	case "covr":
		return report.CatThumbnail, ""
	case "©xyz", "loci":
		return report.CatGPS, ""
	case "©aut", "©wrt", "©cpy", "cprt", "auth", "©prf", "perf", "©dir", "©prd", "ownr", "apID":
		return report.CatAuthor, ""
	case "purd":
		return report.CatTimestamp, ""
	}
	return report.CatCustom, ""
}

// QuickTimeKey maps an mdta key (com.apple.quicktime.*) to a category.
func QuickTimeKey(key string) report.Category {
	k := strings.TrimPrefix(key, "com.apple.quicktime.")
	switch {
	case strings.HasPrefix(k, "location"):
		return report.CatGPS
	case k == "creationdate" || strings.Contains(k, "date"):
		return report.CatTimestamp
	case k == "make" || k == "model" || strings.Contains(k, "serial") || strings.HasPrefix(k, "camera"):
		return report.CatDevice
	case k == "software" || strings.HasPrefix(k, "encoder"):
		return report.CatSoftware
	case k == "author" || k == "artist" || k == "copyright" || k == "publisher":
		return report.CatAuthor
	}
	return report.CatCustom
}

// ---------- documents ----------

// DocProp classifies document properties shared by PDF Info, OOXML core/app,
// ODF meta.xml and EPUB OPF (local names, case-insensitive).
func DocProp(name string) report.Category {
	n := strings.ToLower(name)
	switch n {
	case "title":
		return report.CatIDTag
	case "author", "creator", "initial-creator", "lastmodifiedby", "company", "manager",
		"printed-by", "contributor", "publisher", "rights", "owner":
		return report.CatAuthor
	case "creationdate", "moddate", "created", "modified", "date", "creation-date",
		"lastprinted", "print-date", "metadatadate", "timestamp":
		return report.CatTimestamp
	case "producer", "application", "appversion", "generator", "creatortool":
		return report.CatSoftware
	}
	if strings.Contains(n, "date") || strings.HasSuffix(n, "time") && n != "totaltime" {
		return report.CatTimestamp
	}
	return report.CatCustom
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}
