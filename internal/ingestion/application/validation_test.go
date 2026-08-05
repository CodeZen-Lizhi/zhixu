package application

import (
	"errors"
	"testing"
)

func TestValidateSourceContent(t *testing.T) {
	tests := []struct {
		name       string
		mediaType  string
		content    []byte
		maxBytes   int64
		code       string
		quarantine bool
		warnings   int
	}{
		{name: "markdown", mediaType: "text/markdown", content: []byte("# 标题\n")},
		{name: "html", mediaType: "text/html", content: []byte("<p>可追溯正文</p>")},
		{name: "pdf", mediaType: "application/pdf", content: []byte("%PDF-1.7\n\x00binary")},
		{name: "bom", mediaType: "text/plain", content: append([]byte{0xef, 0xbb, 0xbf}, []byte("text")...), warnings: 1},
		{name: "invalid utf8", mediaType: "text/plain", content: []byte{0xff}, code: "SOURCE_INVALID_UTF8"},
		{name: "nul quarantined", mediaType: "text/plain", content: []byte{'a', 0, 'b'}, code: "SOURCE_BINARY_CONTENT", quarantine: true},
		{name: "control quarantined", mediaType: "text/plain", content: []byte{'a', 1, 'b'}, code: "SOURCE_BINARY_CONTENT", quarantine: true},
		{name: "too large", mediaType: "text/plain", content: []byte("12345"), maxBytes: 4, code: "SOURCE_FILE_TOO_LARGE"},
		{name: "invalid pdf signature", mediaType: "application/pdf", content: []byte("pdf"), code: "SOURCE_PDF_SIGNATURE_INVALID"},
		{name: "unsupported", mediaType: "application/octet-stream", content: []byte("data"), code: "SOURCE_MEDIA_TYPE_UNSUPPORTED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warnings, err := ValidateSourceContent(test.mediaType, test.content, ContentPolicy{MaxBytes: test.maxBytes})
			if test.code == "" {
				if err != nil || len(warnings) != test.warnings {
					t.Fatalf("ValidateSourceContent() = %#v, %v", warnings, err)
				}
				return
			}
			var classified *ContentValidationError
			if !errors.As(err, &classified) || classified.Code != test.code || classified.Quarantine != test.quarantine {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}
