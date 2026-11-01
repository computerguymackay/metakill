package fixtures

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

func init() {
	register("report.pdf", func() Fixture { return Fixture{Name: "report.pdf", Format: "pdf", Data: ReportPDF()} })
}

// PDFTitle is the document title planted in the PDF fixture.
const PDFTitle = "Quarterly Report"

type pdfWriter struct {
	buf     bytes.Buffer
	offsets map[int]int
}

func (w *pdfWriter) obj(nr int, body string) {
	w.offsets[nr] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n%s\nendobj\n", nr, body)
}

func (w *pdfWriter) stream(nr int, dict string, data []byte) {
	w.offsets[nr] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", nr, dict, len(data))
	w.buf.Write(data)
	w.buf.WriteString("\nendstream\nendobj\n")
}

// xref writes an xref section for the objects written since the last call
// and a trailer, then resets the offset map.
func (w *pdfWriter) xref(size int, trailer string) int {
	start := w.buf.Len()
	nrs := make([]int, 0, len(w.offsets))
	for nr := range w.offsets {
		nrs = append(nrs, nr)
	}
	sort.Ints(nrs)
	w.buf.WriteString("xref\n")
	if _, ok := w.offsets[1]; ok { // full table
		fmt.Fprintf(&w.buf, "0 %d\n0000000000 65535 f \n", size)
		for _, nr := range nrs {
			fmt.Fprintf(&w.buf, "%010d 00000 n \n", w.offsets[nr])
		}
	} else {
		for _, nr := range nrs {
			fmt.Fprintf(&w.buf, "%d 1\n%010d 00000 n \n", nr, w.offsets[nr])
		}
	}
	fmt.Fprintf(&w.buf, "trailer\n<< %s >>\nstartxref\n%d\n%%%%EOF\n", trailer, start)
	w.offsets = map[int]int{}
	return start
}

// ReportPDF builds a one-page PDF with an /Info dictionary, an XMP stream,
// /PieceInfo, /LastModified, an annotation with author and date, a JPEG
// image carrying EXIF, and an incremental update that rewrites /Info.
func ReportPDF() []byte {
	w := &pdfWriter{offsets: map[int]int{}}
	w.buf.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	img := InsertAfterSOI(BaseJPEG(16, 16), Segment(0xE1, append([]byte("Exif\x00\x00"), SampleEXIF(binary.BigEndian, nil)...)))
	content := []byte("q 100 0 0 100 50 50 cm /Im1 Do Q")

	w.obj(1, "<< /Type /Catalog /Pages 2 0 R /Metadata 7 0 R /PieceInfo << /Illustrator << /LastModified (D:20230412183302Z) /Private << /CreatorVersion 27 >> >> >> >>")
	w.obj(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	w.obj(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> /Annots [6 0 R] /LastModified (D:20230412183302Z) >>")
	w.stream(4, "", content)
	w.stream(5, "/Type /XObject /Subtype /Image /Width 16 /Height 16 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", img)
	w.obj(6, "<< /Type /Annot /Subtype /Text /Rect [10 10 30 30] /Contents (note) /T ("+ArtistName+") /M (D:20230412183302Z) >>")
	w.stream(7, "/Type /Metadata /Subtype /XML", []byte(SampleXMP))
	w.obj(8, "<< /Title ("+PDFTitle+") /Author (Old Author Name) /Producer ("+Software+") /CreationDate (D:20230412183302Z) >>")
	first := w.xref(9, "/Size 9 /Root 1 0 R /Info 8 0 R /ID [<0123456789ABCDEF0123456789ABCDEF> <0123456789ABCDEF0123456789ABCDEF>]")

	// Incremental update: a new /Info. The old one stays in the file bytes.
	w.obj(8, "<< /Title ("+PDFTitle+") /Author ("+ArtistName+") /Creator ("+Encoder+") /Producer ("+Software+") /CreationDate (D:20230412183302Z) /ModDate (D:20230412183302Z) >>")
	w.xref(9, fmt.Sprintf("/Size 9 /Root 1 0 R /Info 8 0 R /Prev %d /ID [<0123456789ABCDEF0123456789ABCDEF> <FEDCBA9876543210FEDCBA9876543210>]", first))
	return w.buf.Bytes()
}
