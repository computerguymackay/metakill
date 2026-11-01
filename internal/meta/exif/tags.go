package exif

import "fmt"

// Pointer tags that link to sub-IFDs.
const (
	TagExifIFD     = 0x8769
	TagGPSIFD      = 0x8825
	TagInteropIFD  = 0xA005
	TagOrientation = 0x0112
	TagThumbOffset = 0x0201
	TagThumbLength = 0x0202
	TagICC         = 0x8773
	TagXMP         = 0x02BC
	TagIPTC        = 0x83BB
	TagPhotoshop   = 0x8649
	TagSubIFDs     = 0x014A
)

var mainNames = map[uint16]string{
	0x000B: "ProcessingSoftware", 0x00FE: "NewSubfileType", 0x00FF: "SubfileType",
	0x0100: "ImageWidth", 0x0101: "ImageLength", 0x0102: "BitsPerSample", 0x0103: "Compression",
	0x0106: "PhotometricInterpretation", 0x010D: "DocumentName", 0x010E: "ImageDescription",
	0x010F: "Make", 0x0110: "Model", 0x0111: "StripOffsets", 0x0112: "Orientation",
	0x0115: "SamplesPerPixel", 0x0116: "RowsPerStrip", 0x0117: "StripByteCounts",
	0x011A: "XResolution", 0x011B: "YResolution", 0x011C: "PlanarConfiguration",
	0x011D: "PageName", 0x0128: "ResolutionUnit", 0x0129: "PageNumber", 0x0131: "Software",
	0x0132: "DateTime", 0x013B: "Artist", 0x013C: "HostComputer", 0x013D: "Predictor",
	0x013E: "WhitePoint", 0x013F: "PrimaryChromaticities", 0x0140: "ColorMap",
	0x0142: "TileWidth", 0x0143: "TileLength", 0x0144: "TileOffsets", 0x0145: "TileByteCounts",
	0x014A: "SubIFDs", 0x0152: "ExtraSamples", 0x0153: "SampleFormat",
	0x0201: "JPEGInterchangeFormat", 0x0202: "JPEGInterchangeFormatLength",
	0x0211: "YCbCrCoefficients", 0x0212: "YCbCrSubSampling", 0x0213: "YCbCrPositioning",
	0x0214: "ReferenceBlackWhite", 0x02BC: "XMLPacket", 0x4746: "Rating", 0x4749: "RatingPercent",
	0x828D: "CFARepeatPatternDim", 0x828E: "CFAPattern", 0x8298: "Copyright",
	0x829A: "ExposureTime", 0x829D: "FNumber", 0x83BB: "IPTC-NAA", 0x8649: "PhotoshopSettings",
	0x8769: "ExifIFDPointer", 0x8773: "ICCProfile", 0x8822: "ExposureProgram",
	0x8824: "SpectralSensitivity", 0x8825: "GPSInfoIFDPointer", 0x8827: "ISOSpeedRatings",
	0x8828: "OECF", 0x8830: "SensitivityType", 0x8832: "RecommendedExposureIndex",
	0x9000: "ExifVersion", 0x9003: "DateTimeOriginal", 0x9004: "DateTimeDigitized",
	0x9010: "OffsetTime", 0x9011: "OffsetTimeOriginal", 0x9012: "OffsetTimeDigitized",
	0x9101: "ComponentsConfiguration", 0x9102: "CompressedBitsPerPixel",
	0x9201: "ShutterSpeedValue", 0x9202: "ApertureValue", 0x9203: "BrightnessValue",
	0x9204: "ExposureBiasValue", 0x9205: "MaxApertureValue", 0x9206: "SubjectDistance",
	0x9207: "MeteringMode", 0x9208: "LightSource", 0x9209: "Flash", 0x920A: "FocalLength",
	0x9214: "SubjectArea", 0x927C: "MakerNote", 0x9286: "UserComment",
	0x9290: "SubSecTime", 0x9291: "SubSecTimeOriginal", 0x9292: "SubSecTimeDigitized",
	0x9C9B: "XPTitle", 0x9C9C: "XPComment", 0x9C9D: "XPAuthor", 0x9C9E: "XPKeywords",
	0x9C9F: "XPSubject", 0xA000: "FlashpixVersion", 0xA001: "ColorSpace",
	0xA002: "PixelXDimension", 0xA003: "PixelYDimension", 0xA004: "RelatedSoundFile",
	0xA005: "InteroperabilityIFDPointer", 0xA20B: "FlashEnergy", 0xA20E: "FocalPlaneXResolution",
	0xA20F: "FocalPlaneYResolution", 0xA210: "FocalPlaneResolutionUnit",
	0xA214: "SubjectLocation", 0xA215: "ExposureIndex", 0xA217: "SensingMethod",
	0xA300: "FileSource", 0xA301: "SceneType", 0xA302: "CFAPattern", 0xA401: "CustomRendered",
	0xA402: "ExposureMode", 0xA403: "WhiteBalance", 0xA404: "DigitalZoomRatio",
	0xA405: "FocalLengthIn35mmFilm", 0xA406: "SceneCaptureType", 0xA407: "GainControl",
	0xA408: "Contrast", 0xA409: "Saturation", 0xA40A: "Sharpness",
	0xA40B: "DeviceSettingDescription", 0xA40C: "SubjectDistanceRange",
	0xA420: "ImageUniqueID", 0xA430: "CameraOwnerName", 0xA431: "BodySerialNumber",
	0xA432: "LensSpecification", 0xA433: "LensMake", 0xA434: "LensModel",
	0xA435: "LensSerialNumber", 0xA460: "CompositeImage", 0xA500: "Gamma",
	0xC4A5: "PrintIM", 0xC612: "DNGVersion", 0xC614: "UniqueCameraModel",
	0xC615: "LocalizedCameraModel", 0xC62F: "CameraSerialNumber", 0xEA1C: "Padding",
	0xEA1D: "OffsetSchema",
}

var gpsNames = map[uint16]string{
	0x00: "GPSVersionID", 0x01: "GPSLatitudeRef", 0x02: "GPSLatitude", 0x03: "GPSLongitudeRef",
	0x04: "GPSLongitude", 0x05: "GPSAltitudeRef", 0x06: "GPSAltitude", 0x07: "GPSTimeStamp",
	0x08: "GPSSatellites", 0x09: "GPSStatus", 0x0A: "GPSMeasureMode", 0x0B: "GPSDOP",
	0x0C: "GPSSpeedRef", 0x0D: "GPSSpeed", 0x0E: "GPSTrackRef", 0x0F: "GPSTrack",
	0x10: "GPSImgDirectionRef", 0x11: "GPSImgDirection", 0x12: "GPSMapDatum",
	0x13: "GPSDestLatitudeRef", 0x14: "GPSDestLatitude", 0x15: "GPSDestLongitudeRef",
	0x16: "GPSDestLongitude", 0x17: "GPSDestBearingRef", 0x18: "GPSDestBearing",
	0x19: "GPSDestDistanceRef", 0x1A: "GPSDestDistance", 0x1B: "GPSProcessingMethod",
	0x1C: "GPSAreaInformation", 0x1D: "GPSDateStamp", 0x1E: "GPSDifferential",
	0x1F: "GPSHPositioningError",
}

var interopNames = map[uint16]string{
	0x0001: "InteroperabilityIndex", 0x0002: "InteroperabilityVersion",
	0x1000: "RelatedImageFileFormat", 0x1001: "RelatedImageWidth", 0x1002: "RelatedImageLength",
}

// TagName returns a readable name for a tag in the given IFD.
func TagName(ifd string, tag uint16) string {
	var m map[uint16]string
	switch ifd {
	case "GPS":
		m = gpsNames
	case "Interop":
		m = interopNames
	default:
		m = mainNames
	}
	if n, ok := m[tag]; ok {
		return n
	}
	return fmt.Sprintf("Tag0x%04X", tag)
}
