package parser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
)

func TestPDFParserBuildsDerivedTextEvidence(t *testing.T) {
	t.Parallel()
	source := testPDF(t, "Java AI knowledge")
	parser := NewPDFParser()
	got, err := parser.Parse(context.Background(), domain.SourceInput{MediaType: "application/pdf", ImmutableBytes: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Blocks) != 1 {
		t.Fatalf("blocks = %#v", got.Blocks)
	}
	block := got.Blocks[0]
	if block.EvidenceKind != domain.EvidenceDerivedText || block.StartByte != 0 || block.EndByte != int64(len(source)) {
		t.Fatalf("evidence binding = %#v", block)
	}
	if !strings.Contains(block.Content, "Java AI knowledge") || block.Selector["page_start"] != "1" || block.Selector["page_end"] != "1" {
		t.Fatalf("derived content = %#v", block)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Code != "PDF_TEXT_DERIVED" {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
	if got.ParserConfigHash == "" || got.NormalizedContentHash == "" {
		t.Fatalf("hashes = %#v", got)
	}
}

func TestPDFParserRejectsInvalidSignature(t *testing.T) {
	t.Parallel()
	_, err := NewPDFParser().Parse(context.Background(), domain.SourceInput{MediaType: "application/pdf", ImmutableBytes: []byte("not-pdf")})
	if err == nil || !strings.Contains(err.Error(), "SOURCE_PDF_SIGNATURE_INVALID") {
		t.Fatalf("error = %v", err)
	}
}

func TestPDFParserRejectsDerivedTextOverLimitAcrossPages(t *testing.T) {
	t.Parallel()
	pageText := strings.Repeat("x", maxPDFDerivedTextBytes/2+1)
	source := testMultiPagePDF(t, []string{pageText, pageText})

	_, err := NewPDFParser().Parse(context.Background(), domain.SourceInput{MediaType: "application/pdf", ImmutableBytes: source})
	if err == nil || !strings.Contains(err.Error(), "SOURCE_PDF_TEXT_TOO_LARGE") {
		t.Fatalf("error = %v", err)
	}
}

func TestCollectPDFPageTextStopsWhenContextIsCancelledBetweenPages(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	extractedPages := 0

	_, err := collectPDFPageText(ctx, 3, func(pageNumber int) (string, error) {
		extractedPages++
		if pageNumber == 1 {
			cancel()
		}
		return "page", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if extractedPages != 1 {
		t.Fatalf("extracted pages = %d, want 1", extractedPages)
	}
}

func testPDF(t *testing.T, text string) []byte {
	t.Helper()
	return testMultiPagePDF(t, []string{text})
}

func testMultiPagePDF(t *testing.T, pageTexts []string) []byte {
	t.Helper()
	if len(pageTexts) == 0 {
		t.Fatal("PDF fixture requires at least one page")
	}
	fontObject := 3 + len(pageTexts)*2
	objects := make([]string, 0, fontObject)
	objects = append(objects,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", pdfPageKids(pageTexts), len(pageTexts)),
	)
	for index, text := range pageTexts {
		pageObject := 3 + index*2
		contentObject := pageObject + 1
		text = strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
		stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontObject, contentObject),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		)
	}
	objects = append(objects, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index <= len(objects); index++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return output.Bytes()
}

func pdfPageKids(pageTexts []string) string {
	kids := make([]string, 0, len(pageTexts))
	for index := range pageTexts {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+index*2))
	}
	return strings.Join(kids, " ")
}
