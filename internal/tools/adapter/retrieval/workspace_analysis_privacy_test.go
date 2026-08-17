package retrieval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestWorkspaceAnalysisPrivateBindingWireBytesRemainFrozen(t *testing.T) {
	identity := ReadSourceV3Identity{
		EvidenceRef: "E1", CitationID: "cite-" + strings.Repeat("a", 64),
		IndexVersionID:  "10000000-0000-4000-8000-000000000001",
		ChunkID:         "20000000-0000-4000-8000-000000000002",
		SourceVersionID: "30000000-0000-4000-8000-000000000003",
		SourceSpanID:    "40000000-0000-4000-8000-000000000004",
		ContentHash:     strings.Repeat("b", 64),
	}
	searchDocument, err := json.Marshal(searchKnowledgeV2PrivateBinding{
		Items: []readSourceV3IdentityDocument{readSourceV3IdentityDocumentFrom(identity)}, SelectedRefs: []string{"E1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readDocument, err := json.Marshal(readSourceV3PrivateBinding{
		SearchReceiptID: "50000000-0000-4000-8000-000000000005", SearchReceiptHash: strings.Repeat("c", 64),
		readSourceV3IdentityDocument: readSourceV3IdentityDocumentFrom(identity),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantSearch := `{"items":[{"evidence_ref":"E1","citation_id":"cite-` + strings.Repeat("a", 64) +
		`","index_version_id":"10000000-0000-4000-8000-000000000001","chunk_id":"20000000-0000-4000-8000-000000000002",` +
		`"source_version_id":"30000000-0000-4000-8000-000000000003","source_span_id":"40000000-0000-4000-8000-000000000004",` +
		`"content_hash":"` + strings.Repeat("b", 64) + `"}],"selected_refs":["E1"]}`
	wantRead := `{"search_receipt_id":"50000000-0000-4000-8000-000000000005","search_receipt_hash":"` + strings.Repeat("c", 64) +
		`","evidence_ref":"E1","citation_id":"cite-` + strings.Repeat("a", 64) +
		`","index_version_id":"10000000-0000-4000-8000-000000000001","chunk_id":"20000000-0000-4000-8000-000000000002",` +
		`"source_version_id":"30000000-0000-4000-8000-000000000003","source_span_id":"40000000-0000-4000-8000-000000000004",` +
		`"content_hash":"` + strings.Repeat("b", 64) + `"}`
	tests := []struct {
		name, want, wantHash string
		document             []byte
	}{
		{name: "search", document: searchDocument, want: wantSearch, wantHash: "6803adb19b34e38b9ca79e68d38586695fa74db03a658e624fed8dbcd1dfaada"},
		{name: "read source", document: readDocument, want: wantRead, wantHash: "5eb2f7da21eb46b632dda1ea50c2732f83f033fc93acbbfdc31a7e0d181f1a1d"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if string(test.document) != test.want || hashDocument(test.document) != test.wantHash {
				t.Fatalf("private binding wire drifted: document=%s hash=%s", test.document, hashDocument(test.document))
			}
		})
	}
}

func TestWorkspaceAnalysisRuntimeIdentitiesNeverSerializeOrLogPrivateFacts(t *testing.T) {
	identity := ReadSourceV3Identity{
		EvidenceRef: "E1", CitationID: "runtime-citation-canary",
		IndexVersionID: "runtime-index-canary", ChunkID: "runtime-chunk-canary",
		SourceVersionID: "runtime-source-canary", SourceSpanID: "runtime-span-canary",
		ContentHash: "runtime-content-hash-canary",
	}
	readResolution := ReadSourceV3Resolution{
		WorkspaceID: "runtime-workspace-canary", WorkflowRunID: "runtime-workflow-canary",
		SearchReceiptID: "runtime-receipt-canary", SearchReceiptHash: "runtime-receipt-hash-canary",
		Identity: identity,
	}
	validateResolution := ValidateCitationV3Resolution{
		WorkspaceID: "runtime-workspace-canary", WorkflowRunID: "runtime-workflow-canary",
		CandidateID: "runtime-candidate-canary", CandidateHash: "runtime-candidate-hash-canary",
		Identities: []ReadSourceV3Identity{identity},
	}
	values := []struct {
		name  string
		value any
	}{
		{name: "identity", value: identity},
		{name: "identity pointer", value: &identity},
		{name: "read resolution", value: readResolution},
		{name: "read resolution pointer", value: &readResolution},
		{name: "validation resolution", value: validateResolution},
		{name: "validation resolution pointer", value: &validateResolution},
	}
	canaries := []string{
		"runtime-citation-canary", "runtime-index-canary", "runtime-chunk-canary", "runtime-source-canary",
		"runtime-span-canary", "runtime-content-hash-canary", "runtime-workspace-canary", "runtime-workflow-canary",
		"runtime-receipt-canary", "runtime-receipt-hash-canary", "runtime-candidate-canary", "runtime-candidate-hash-canary",
	}
	for _, test := range values {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != `{}` {
				t.Fatalf("runtime identity JSON must be empty: %s", encoded)
			}
			var logOutput bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
			logger.Info("workspace analysis runtime", slog.Any("value", test.value))
			outputs := []string{
				string(encoded), fmt.Sprintf("%v", test.value), fmt.Sprintf("%+v", test.value),
				fmt.Sprintf("%#v", test.value), logOutput.String(),
			}
			for _, output := range outputs {
				for _, canary := range canaries {
					if strings.Contains(output, canary) {
						t.Fatalf("runtime identity leaked %q through %q", canary, output)
					}
				}
			}
		})
	}
}
