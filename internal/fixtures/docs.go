package fixtures

import (
	"archive/zip"
	"bytes"
	"time"
)

func init() {
	register("letter.docx", func() Fixture { return Fixture{Name: "letter.docx", Format: "ooxml", Data: LetterDOCX()} })
	register("notes.odt", func() Fixture { return Fixture{Name: "notes.odt", Format: "odf", Data: NotesODT()} })
	register("book.epub", func() Fixture { return Fixture{Name: "book.epub", Format: "epub", Data: BookEPUB()} })
}

// DocTitle is the title planted in document fixtures.
const DocTitle = "Secret Letter Title"

type zipMember struct {
	name   string
	data   string
	stored bool
}

// buildZip writes members with 2023 timestamps (which also makes Go add an
// extended-timestamp extra field) and an archive comment.
func buildZip(comment string, members ...zipMember) []byte {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	mod := time.Date(2023, 4, 12, 18, 33, 2, 0, time.UTC)
	for _, m := range members {
		h := &zip.FileHeader{Name: m.name, Method: zip.Deflate, Modified: mod}
		if m.stored {
			h.Method = zip.Store
		}
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(m.data))
	}
	if comment != "" {
		zw.SetComment(comment)
	}
	zw.Close()
	return b.Bytes()
}

const wNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:w15="http://schemas.microsoft.com/office/word/2012/wordml"`

// LetterDOCX is a Word document with core/app/custom properties, a
// thumbnail, tracked changes, comments, people.xml, rsids, an attached
// template path and an image carrying EXIF.
func LetterDOCX() []byte {
	return buildZip("Created by "+Software,
		zipMember{name: "[Content_Types].xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="jpeg" ContentType="image/jpeg"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/comments.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.comments+xml"/><Override PartName="/word/people.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.people+xml"/><Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/><Override PartName="/docProps/custom.xml" ContentType="application/vnd.openxmlformats-officedocument.custom-properties+xml"/></Types>`},
		zipMember{name: "_rels/.rels", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/><Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/custom-properties" Target="docProps/custom.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/thumbnail" Target="docProps/thumbnail.jpeg"/><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		zipMember{name: "word/document.xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document ` + wNS + `><w:body><w:p w:rsidR="00A1B2C3" w:rsidRDefault="00A1B2C3"><w:r><w:t xml:space="preserve">Hello </w:t></w:r><w:ins w:id="1" w:author="` + ArtistName + `" w:date="2023-04-12T18:33:02Z"><w:r><w:t>inserted</w:t></w:r></w:ins><w:commentRangeStart w:id="0"/><w:r><w:t>body text</w:t></w:r><w:commentRangeEnd w:id="0"/></w:p><w:sectPr w:rsidR="00A1B2C3"/></w:body></w:document>`},
		zipMember{name: "word/_rels/document.xml.rels", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/comments" Target="comments.xml"/><Relationship Id="rId2" Type="http://schemas.microsoft.com/office/2011/relationships/people" Target="people.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image1.jpeg"/></Relationships>`},
		zipMember{name: "word/comments.xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:comments ` + wNS + `><w:comment w:id="0" w:author="` + ArtistName + `" w:date="2023-04-12T18:33:02Z" w:initials="JP"><w:p><w:r><w:t>` + Comment + `</w:t></w:r></w:p></w:comment></w:comments>`},
		zipMember{name: "word/people.xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w15:people ` + wNS + `><w15:person w15:author="` + ArtistName + `"><w15:presenceInfo w15:providerId="AD" w15:userId="S::jane@example.com::1234"/></w15:person></w15:people>`},
		zipMember{name: "word/settings.xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:settings ` + wNS + `><w:attachedTemplate r:id="rId1"/><w:docVars><w:docVar w:name="ClientName" w:val="` + Serial + `"/></w:docVars><w:rsids><w:rsidRoot w:val="00A1B2C3"/><w:rsid w:val="00A1B2C3"/></w:rsids></w:settings>`},
		zipMember{name: "word/_rels/settings.xml.rels", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/attachedTemplate" Target="file:///C:\Users\jane\AppData\Roaming\Microsoft\Templates\Normal.dotm" TargetMode="External"/></Relationships>`},
		zipMember{name: "word/media/image1.jpeg", data: string(CoverJPEG())},
		zipMember{name: "docProps/core.xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><dc:title>` + DocTitle + `</dc:title><dc:creator>` + ArtistName + `</dc:creator><cp:lastModifiedBy>` + ArtistName + `</cp:lastModifiedBy><cp:revision>7</cp:revision><dcterms:created xsi:type="dcterms:W3CDTF">2023-04-12T18:33:02Z</dcterms:created><dcterms:modified xsi:type="dcterms:W3CDTF">2023-04-12T18:33:02Z</dcterms:modified><dc:language>en-US</dc:language></cp:coreProperties>`},
		zipMember{name: "docProps/app.xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Template>Normal.dotm</Template><TotalTime>42</TotalTime><Pages>1</Pages><Application>` + Encoder + `</Application><Company>` + Make + ` Inc</Company><AppVersion>16.0000</AppVersion></Properties>`},
		zipMember{name: "docProps/custom.xml", data: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/custom-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes"><property fmtid="{D5CDD505-2E9C-101B-9397-08002B2CF9AE}" pid="2" name="ClientId"><vt:lpwstr>` + Model + `</vt:lpwstr></property></Properties>`},
		zipMember{name: "docProps/thumbnail.jpeg", data: string(BaseJPEG(8, 8))},
	)
}

// NotesODT is an OpenDocument text with meta.xml, settings (printer),
// tracked changes, an annotation, a thumbnail and a picture with EXIF.
func NotesODT() []byte {
	const ns = `xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0" xmlns:config="urn:oasis:names:tc:opendocument:xmlns:config:1.0" xmlns:xlink="http://www.w3.org/1999/xlink" xmlns:officeooo="http://openoffice.org/2009/office" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0"`
	return buildZip("",
		zipMember{name: "mimetype", data: "application/vnd.oasis.opendocument.text", stored: true},
		zipMember{name: "META-INF/manifest.xml", data: `<?xml version="1.0" encoding="UTF-8"?>
<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.3"><manifest:file-entry manifest:full-path="/" manifest:media-type="application/vnd.oasis.opendocument.text"/><manifest:file-entry manifest:full-path="Thumbnails/thumbnail.png" manifest:media-type="image/png"/><manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/><manifest:file-entry manifest:full-path="meta.xml" manifest:media-type="text/xml"/><manifest:file-entry manifest:full-path="settings.xml" manifest:media-type="text/xml"/><manifest:file-entry manifest:full-path="Pictures/photo.jpg" manifest:media-type="image/jpeg"/></manifest:manifest>`},
		zipMember{name: "content.xml", data: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content ` + ns + `><office:automatic-styles><style:style style:name="P1"><style:text-properties officeooo:rsid="0012ab34" officeooo:paragraph-rsid="0012ab34"/></style:style></office:automatic-styles><office:body><office:text><text:tracked-changes><text:changed-region text:id="ct1"><text:insertion><office:change-info><dc:creator>` + ArtistName + `</dc:creator><dc:date>2023-04-12T18:33:02</dc:date></office:change-info></text:insertion></text:changed-region></text:tracked-changes><text:p text:style-name="P1">Body <office:annotation><dc:creator>` + ArtistName + `</dc:creator><dc:date>2023-04-12T18:33:02</dc:date><meta:creator-initials>JP</meta:creator-initials><text:p>` + Comment + `</text:p></office:annotation>text</text:p></office:text></office:body></office:document-content>`},
		zipMember{name: "meta.xml", data: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-meta ` + ns + `><office:meta><meta:generator>` + Encoder + `</meta:generator><dc:title>` + DocTitle + `</dc:title><meta:initial-creator>` + ArtistName + `</meta:initial-creator><dc:creator>` + ArtistName + `</dc:creator><meta:creation-date>2023-04-12T18:33:02</meta:creation-date><dc:date>2023-04-12T18:33:02</dc:date><meta:editing-cycles>3</meta:editing-cycles><meta:template xlink:href="file:///home/jane/Templates/secret.ott"/><meta:user-defined meta:name="Client">` + Model + `</meta:user-defined><dc:language>en-US</dc:language><meta:document-statistic meta:page-count="1"/></office:meta></office:document-meta>`},
		zipMember{name: "settings.xml", data: `<?xml version="1.0" encoding="UTF-8"?>
<office:document-settings ` + ns + `><office:settings><config:config-item-set config:name="ooo:configuration-settings"><config:config-item config:name="PrinterName" config:type="string">` + Serial + ` LaserJet</config:config-item><config:config-item config:name="PrinterSetup" config:type="base64Binary">c2VjcmV0</config:config-item><config:config-item config:name="Rsid" config:type="int">1234</config:config-item><config:config-item config:name="TabsRelativeToIndent" config:type="boolean">true</config:config-item></config:config-item-set></office:settings></office:document-settings>`},
		zipMember{name: "Thumbnails/thumbnail.png", data: string(BasePNG(4, 4))},
		zipMember{name: "Pictures/photo.jpg", data: string(CoverJPEG())},
	)
}

// BookEPUB is an EPUB 3 with creator, contributor (calibre), dates, extra
// identifiers, calibre metadata, refinements and a calibre bookmarks file.
func BookEPUB() []byte {
	return buildZip("",
		zipMember{name: "mimetype", data: "application/epub+zip", stored: true},
		zipMember{name: "META-INF/container.xml", data: `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		zipMember{name: "OEBPS/content.opf", data: `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf"><dc:identifier id="uid">urn:uuid:12345678-1234-1234-1234-123456789abc</dc:identifier><dc:identifier id="calibre_id">calibre:` + Serial + `</dc:identifier><dc:title>` + DocTitle + `</dc:title><dc:language>en</dc:language><dc:creator id="creator1">` + ArtistName + `</dc:creator><meta refines="#creator1" property="file-as">Photographer, Jane</meta><dc:contributor>calibre (6.0.0) [https://calibre-ebook.com]</dc:contributor><dc:publisher>` + Make + ` Books</dc:publisher><dc:date>2023-04-12</dc:date><meta property="dcterms:modified">2023-04-12T18:33:02Z</meta><meta name="calibre:timestamp" content="2023-04-12T18:33:02+00:00"/><meta name="cover" content="cover-img"/></metadata><manifest><item id="cover-img" href="cover.jpg" media-type="image/jpeg" properties="cover-image"/><item id="c1" href="c1.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c1"/></spine></package>`},
		zipMember{name: "OEBPS/c1.xhtml", data: `<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>c1</title></head><body><p>Chapter one.</p></body></html>`},
		zipMember{name: "OEBPS/cover.jpg", data: string(CoverJPEG())},
		zipMember{name: "META-INF/calibre_bookmarks.txt", data: "encoding=json+base64:\n" + ArtistName},
	)
}
