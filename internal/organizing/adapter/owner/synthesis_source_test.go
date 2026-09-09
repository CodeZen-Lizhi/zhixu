package owner

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
)

func TestSynthesisArtifactRequiresExactOwnerAndBytes(t *testing.T) {
	source, artifact := synthesisArtifact([]byte("Original source bytes."))
	if err := validateSynthesisArtifact(source, artifact); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*retrievalapp.EvidenceArtifact)
	}{
		{"workspace", func(value *retrievalapp.EvidenceArtifact) { value.WorkspaceID = ownerID(910) }},
		{"version", func(value *retrievalapp.EvidenceArtifact) { value.SourceVersionID = ownerID(911) }},
		{"artifact", func(value *retrievalapp.EvidenceArtifact) { value.ContentArtifactID = ownerID(912) }},
		{"hash", func(value *retrievalapp.EvidenceArtifact) { value.ContentHash = ownerHash(913) }},
		{"size", func(value *retrievalapp.EvidenceArtifact) { value.ByteSize++ }},
		{"bytes with same size", func(value *retrievalapp.EvidenceArtifact) { value.Bytes[0] = 'X' }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := artifact
			changed.Bytes = append([]byte(nil), artifact.Bytes...)
			test.mutate(&changed)
			assertOwnerError(t, validateSynthesisArtifact(source, changed), foundation.ErrorConsistencyViolation, domain.ErrorCodeSynthesisSourceInvalid)
		})
	}
}

func TestSynthesisExcerptUsesExactRangesAndParserEvidence(t *testing.T) {
	content := []byte("Prefix: exact evidence. Suffix.")
	source, _ := synthesisArtifact(content)
	start := strings.Index(string(content), "exact")
	raw := synthesisSpanRow{ID: string(ownerID(906)), StartByte: int64(start), EndByte: int64(start + len("exact evidence.")), ExcerptHash: synthesisEvidenceHash([]byte("exact evidence.")), EvidenceKind: "raw_bytes"}
	excerpt, err := synthesisExcerpt(source, "Original", raw, content)
	if err != nil || excerpt.Text != "exact evidence." {
		t.Fatalf("raw evidence changed: %+v %v", excerpt, err)
	}
	derived := synthesisSpanRow{ID: raw.ID, EndByte: int64(len(content)), ExcerptHash: synthesisEvidenceHash([]byte("Parsed evidence.")), EvidenceKind: "derived_text", DerivedExcerpt: "Parsed evidence.", DerivedExcerptBytes: int64(len("Parsed evidence."))}
	excerpt, err = synthesisExcerpt(source, "Original", derived, content)
	if err != nil || excerpt.Text != "Parsed evidence." {
		t.Fatalf("parser evidence changed: %+v %v", excerpt, err)
	}
	for _, test := range []struct {
		name   string
		span   synthesisSpanRow
		mutate func(*synthesisSpanRow)
		kind   foundation.ErrorKind
		code   string
	}{
		{"negative offset", raw, func(span *synthesisSpanRow) { span.StartByte = -1 }, foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT"},
		{"past end", raw, func(span *synthesisSpanRow) { span.EndByte = int64(len(content) + 1) }, foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT"},
		{"raw hash drift", raw, func(span *synthesisSpanRow) { span.ExcerptHash = ownerHash(920) }, foundation.ErrorInvalidInput, domain.ErrorCodeSynthesisSourceInvalid},
		{"unknown kind", raw, func(span *synthesisSpanRow) { span.EvidenceKind = "normalized_chunk" }, foundation.ErrorConsistencyViolation, domain.ErrorCodeSynthesisSourceInvalid},
		{"partial parser artifact", derived, func(span *synthesisSpanRow) { span.StartByte = 1 }, foundation.ErrorConsistencyViolation, domain.ErrorCodeSynthesisSourceInvalid},
		{"parser hash drift", derived, func(span *synthesisSpanRow) { span.DerivedExcerpt = "Changed evidence." }, foundation.ErrorInvalidInput, domain.ErrorCodeSynthesisSourceInvalid},
		{"parser exceeds database transfer bound", derived, func(span *synthesisSpanRow) {
			span.DerivedExcerptBytes = organizingapp.MaxSynthesisSourceExcerptBytes + 1
			span.DerivedExcerpt = ""
		}, foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			span := test.span
			test.mutate(&span)
			_, err := synthesisExcerpt(source, "Original", span, content)
			assertOwnerError(t, err, test.kind, test.code)
		})
	}
}

func TestSynthesisExcerptByteLimitAndUTF8Boundary(t *testing.T) {
	for _, kind := range []string{"raw_bytes", "derived_text"} {
		t.Run(kind, func(t *testing.T) {
			for _, length := range []int{organizingapp.MaxSynthesisSourceExcerptBytes, organizingapp.MaxSynthesisSourceExcerptBytes + 1} {
				text := strings.Repeat("a", length)
				source, artifact := synthesisArtifact([]byte(text))
				span := synthesisSpanRow{ID: string(ownerID(906)), EndByte: int64(length), ExcerptHash: source.ContentHash, EvidenceKind: kind, DerivedExcerpt: text, DerivedExcerptBytes: int64(length)}
				excerpt, err := synthesisExcerpt(source, "Original", span, artifact.Bytes)
				if length == organizingapp.MaxSynthesisSourceExcerptBytes {
					if err != nil || excerpt.Text != text {
						t.Fatalf("bounded excerpt changed: length=%d err=%v", len(excerpt.Text), err)
					}
				} else {
					assertOwnerError(t, err, foundation.ErrorInvalidInput, "SYNTHESIS_SOURCE_INPUT_LIMIT")
				}
			}
		})
	}
	source, artifact := synthesisArtifact([]byte("原始证据"))
	span := synthesisSpanRow{ID: string(ownerID(906)), EndByte: 1, ExcerptHash: synthesisEvidenceHash(artifact.Bytes[:1]), EvidenceKind: "raw_bytes"}
	_, err := synthesisExcerpt(source, "Original", span, artifact.Bytes)
	assertOwnerError(t, err, foundation.ErrorInvalidInput, domain.ErrorCodeSynthesisSourceInvalid)
}

func synthesisArtifact(content []byte) (domain.SynthesisSourceVersion, retrievalapp.EvidenceArtifact) {
	source := domain.SynthesisSourceVersion{WorkspaceID: ownerID(901), SourceID: ownerID(902), SourceVersionID: ownerID(903), ContentArtifactID: ownerID(904), ParseProjectionID: ownerID(905), ContentHash: synthesisEvidenceHash(content)}
	return source, retrievalapp.EvidenceArtifact{WorkspaceID: source.WorkspaceID, SourceVersionID: source.SourceVersionID, ContentArtifactID: source.ContentArtifactID, ContentHash: source.ContentHash, ByteSize: int64(len(content)), Bytes: append([]byte(nil), content...)}
}

func synthesisEvidenceHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
