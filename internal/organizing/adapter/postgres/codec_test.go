package postgres

import (
	"errors"
	"reflect"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

func TestReceiptMatchesServerGeneratedAggregate(t *testing.T) {
	binding := organizingapp.CommandBinding{
		WorkspaceID:    "74100000-0000-4000-8000-000000000001",
		IdempotencyKey: "create-draft",
		RequestHash:    testHash("a"),
		CommandType:    organizingapp.CommandCreateDraft,
	}
	receipt := receiptRow{
		workspaceID:     string(binding.WorkspaceID),
		key:             binding.IdempotencyKey,
		requestHash:     binding.RequestHash,
		commandType:     binding.CommandType,
		aggregateID:     "74100000-0000-4000-8000-000000000002",
		expectedVersion: 0,
	}

	if err := receipt.matches(binding); err != nil {
		t.Fatalf("match before generated aggregate assignment: %v", err)
	}
	binding.AggregateID = foundation.ID(receipt.aggregateID)
	if err := receipt.matches(binding); err != nil {
		t.Fatalf("match after generated aggregate assignment: %v", err)
	}
	binding.RequestHash = testHash("b")
	if err := receipt.matches(binding); !classifiedError(err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeIdempotencyConflict) {
		t.Fatalf("different request error=%v", err)
	}
}

func TestValidateBindingRequiresAggregateForExistingFacts(t *testing.T) {
	binding := organizingapp.CommandBinding{
		WorkspaceID: "74100000-0000-4000-8000-000000000001", IdempotencyKey: "update-draft",
		RequestHash: testHash("binding"), CommandType: organizingapp.CommandUpdateDraft, ExpectedVersion: 1,
	}
	if err := validateBinding(binding); !classifiedError(err, foundation.ErrorInvalidInput, organizingapp.ErrorCodeRequestInvalid) {
		t.Fatalf("missing aggregate error=%v", err)
	}
	binding.AggregateID = "74100000-0000-4000-8000-000000000002"
	if err := validateBinding(binding); err != nil {
		t.Fatalf("complete binding: %v", err)
	}
}

func TestMaterialCodecPreservesFrozenCitationAndCollectionOrigin(t *testing.T) {
	reference := domain.MaterialRef{
		Kind:               domain.MaterialDocumentRevision,
		DocumentID:         "74100000-0000-4000-8000-000000000010",
		ArticleRevisionID:  "74100000-0000-4000-8000-000000000011",
		OriginCollectionID: "74100000-0000-4000-8000-000000000012",
		Version:            3,
		ContentHash:        testHash("content"),
		Evidence: []domain.EvidenceRef{{
			IndexVersionID:  "74100000-0000-4000-8000-000000000013",
			ChunkID:         "74100000-0000-4000-8000-000000000014",
			SourceVersionID: "74100000-0000-4000-8000-000000000015",
			SourceSpanID:    "74100000-0000-4000-8000-000000000016",
			ContentHash:     testHash("citation-content"),
			ExcerptHash:     testHash("citation-excerpt"),
		}},
	}
	encoded, err := encodeEvidence(reference.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeMaterialRef(string(reference.Kind), "", string(reference.DocumentID),
		string(reference.ArticleRevisionID), "", "", string(reference.OriginCollectionID), "",
		reference.Version, reference.ContentHash, "", "", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded reference is invalid: %v", err)
	}
	if !reflect.DeepEqual(decoded, reference) {
		t.Fatalf("decoded=%#v want=%#v", decoded, reference)
	}
}

func TestDecodeEvidenceRejectsUnknownAndTrailingJSON(t *testing.T) {
	valid := `[{"index_version_id":"74100000-0000-4000-8000-000000000021",` +
		`"chunk_id":"74100000-0000-4000-8000-000000000022",` +
		`"source_version_id":"74100000-0000-4000-8000-000000000023",` +
		`"source_span_id":"74100000-0000-4000-8000-000000000024",` +
		`"content_hash":"` + testHash("content") + `","excerpt_hash":"` + testHash("excerpt") + `"}]`
	if _, err := decodeEvidence([]byte(valid)); err != nil {
		t.Fatalf("valid evidence: %v", err)
	}
	if _, err := decodeEvidence([]byte(valid[:len(valid)-2] + `,"unexpected":true}]`)); err == nil {
		t.Fatal("unknown evidence field was accepted")
	}
	if _, err := decodeEvidence([]byte(valid + `{}`)); err == nil {
		t.Fatal("trailing evidence JSON was accepted")
	}
	empty, err := decodeEvidence([]byte(`[]`))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty evidence=%#v err=%v", empty, err)
	}
	if _, err := decodeEvidence([]byte(`null`)); err == nil {
		t.Fatal("null evidence was accepted")
	}
}

func TestDecodeDeclarationRejectsUnknownAndTrailingJSON(t *testing.T) {
	if _, err := decodeDeclaration([]byte(`{"schema_version":"organizing-template/v1","unexpected":true}`)); err == nil {
		t.Fatal("unknown template declaration field was accepted")
	}
	if _, err := decodeDeclaration([]byte(`{} {}`)); err == nil {
		t.Fatal("trailing template declaration JSON was accepted")
	}
}

func classifiedError(err error, kind foundation.ErrorKind, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind && classified.Code == code
}

func testHash(seed string) string {
	const alphabet = "0123456789abcdef"
	if seed == "" {
		seed = "0"
	}
	result := make([]byte, 64)
	for index := range result {
		result[index] = alphabet[(int(seed[index%len(seed)])+index)%len(alphabet)]
	}
	return string(result)
}
