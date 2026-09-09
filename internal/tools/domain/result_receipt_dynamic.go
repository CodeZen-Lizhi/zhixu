package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// DynamicWorkspaceAnalysisMaxEvidenceRefs is the frozen tool protocol bound.
// The Agent budget may admit fewer reads, but it cannot enlarge this namespace.
const DynamicWorkspaceAnalysisMaxEvidenceRefs = 32

// WorkspaceAnalysisToolWorkflowVersion selects a contract without changing any
// historical tool definition or its hash. Zero means no analysis contract.
func WorkspaceAnalysisToolWorkflowVersion(ref ToolRef) int64 {
	switch ref {
	case ToolRef{Name: "ReadGitStatus", Version: 2}, ToolRef{Name: "SearchKnowledge", Version: 2}, ToolRef{Name: "ReadSource", Version: 3}, ToolRef{Name: "ValidateCitation", Version: 3}:
		return 1
	case ToolRef{Name: "ReadGitStatus", Version: 3}, ToolRef{Name: "SearchKnowledge", Version: 3}, ToolRef{Name: "ReadSource", Version: 4}, ToolRef{Name: "ValidateCitation", Version: 4}:
		return 2
	default:
		return 0
	}
}

// ValidDynamicEvidenceRef accepts only canonical run-global labels E1..E32.
func ValidDynamicEvidenceRef(ref string) bool {
	if len(ref) < 2 || len(ref) > 3 || ref[0] != 'E' {
		return false
	}
	n, err := strconv.Atoi(ref[1:])
	return err == nil && n >= 1 && n <= DynamicWorkspaceAnalysisMaxEvidenceRefs && ref == "E"+strconv.Itoa(n)
}

func workspaceAnalysisDynamicResultReceiptContract(ref ToolRef) (ResultReceiptContract, bool) {
	if WorkspaceAnalysisToolWorkflowVersion(ref) != 2 {
		return ResultReceiptContract{}, false
	}
	legacy := ref
	legacy.Version--
	c := workspaceAnalysisResultReceiptContract(legacy)
	c.Tool = ref
	if ref.Name != "ReadGitStatus" {
		c.OutputSchema.Version++
		c.PrivateBindingSchema.Version++
	}
	return c, true
}

type readSourceV4PrivateBinding struct {
	SearchReceiptID   foundation.ID `json:"search_receipt_id"`
	SearchReceiptHash string        `json:"search_receipt_hash"`
	SearchEvidenceRef string        `json:"search_evidence_ref"`
	resultReceiptCitationIdentity
}

type validateCitationV4PrivateIdentity struct {
	SearchReceiptID   foundation.ID `json:"search_receipt_id"`
	SearchReceiptHash string        `json:"search_receipt_hash"`
	SearchEvidenceRef string        `json:"search_evidence_ref"`
	ReadReceiptID     foundation.ID `json:"read_receipt_id"`
	ReadReceiptHash   string        `json:"read_receipt_hash"`
	resultReceiptCitationIdentity
}

type validateCitationV4PrivateBinding struct {
	CandidateID   *foundation.ID                      `json:"candidate_id"`
	CandidateHash *string                             `json:"candidate_hash"`
	Results       []validateCitationV4PrivateIdentity `json:"results"`
}

// DynamicEvidenceIdentity is a server-only immutable citation binding. It must
// never be encoded into model messages, logs or public event payloads.
type DynamicEvidenceIdentity struct {
	EvidenceRef     string        `json:"-"`
	CitationID      string        `json:"-"`
	IndexVersionID  foundation.ID `json:"-"`
	ChunkID         foundation.ID `json:"-"`
	SourceVersionID foundation.ID `json:"-"`
	SourceSpanID    foundation.ID `json:"-"`
	ContentHash     string        `json:"-"`
}

func (DynamicEvidenceIdentity) String() string     { return "DynamicEvidenceIdentity{redacted}" }
func (i DynamicEvidenceIdentity) GoString() string { return i.String() }
func (DynamicEvidenceIdentity) LogValue() slog.Value {
	return slog.StringValue("dynamic_evidence:redacted")
}

func dynamicEvidenceIdentity(i resultReceiptCitationIdentity) DynamicEvidenceIdentity {
	return DynamicEvidenceIdentity{i.EvidenceRef, i.CitationID, i.IndexVersionID, i.ChunkID, i.SourceVersionID, i.SourceSpanID, i.ContentHash}
}

// DynamicReadBinding retains both global and Search-local references.
type DynamicReadBinding struct {
	SearchReceiptID   foundation.ID           `json:"-"`
	SearchReceiptHash string                  `json:"-"`
	SearchEvidenceRef string                  `json:"-"`
	Identity          DynamicEvidenceIdentity `json:"-"`
}

func (DynamicReadBinding) String() string       { return "DynamicReadBinding{redacted}" }
func (b DynamicReadBinding) GoString() string   { return b.String() }
func (DynamicReadBinding) LogValue() slog.Value { return slog.StringValue("dynamic_read:redacted") }

func validateSearchKnowledgeV3PrivateBinding(b searchKnowledgeV2PrivateBinding) error {
	if b.SelectedRefs == nil || len(b.SelectedRefs) != len(b.Items) {
		return errors.New("dynamic search selection is incomplete")
	}
	for i, ref := range b.SelectedRefs {
		if ref != resultReceiptEvidenceRef(i+1) {
			return errors.New("dynamic search selection drifted")
		}
	}
	legacy := b
	legacy.SelectedRefs = b.SelectedRefs[:min(3, len(b.SelectedRefs))]
	return validateSearchKnowledgeV2PrivateBinding(legacy)
}

func validateReadSourceV4ReceiptOutput(o readSourceV3ReceiptOutput) error {
	if !ValidDynamicEvidenceRef(o.EvidenceRef) {
		return errors.New("dynamic source reference is invalid")
	}
	o.EvidenceRef = "E1"
	return validateReadSourceV3ReceiptOutput(o)
}

func validDynamicCitationIdentity(i resultReceiptCitationIdentity) bool {
	if !ValidDynamicEvidenceRef(i.EvidenceRef) {
		return false
	}
	i.EvidenceRef = "E1"
	return validResultReceiptCitationIdentity(i, 1)
}

func validateReadSourceV4PrivateBinding(b readSourceV4PrivateBinding) error {
	if !canonicalResultReceiptID(b.SearchReceiptID) || !lowerHex(b.SearchReceiptHash, 64) || !validResultReceiptEvidenceRef(b.SearchEvidenceRef, 5) || !validDynamicCitationIdentity(b.resultReceiptCitationIdentity) {
		return errors.New("dynamic source binding is invalid")
	}
	return nil
}

func validateValidateCitationV4ReceiptOutput(o validateCitationV3ReceiptOutput) error {
	if o.Results == nil || len(o.Results) < 1 || len(o.Results) > 8 {
		return errors.New("dynamic citation result size is invalid")
	}
	seen := map[string]bool{}
	for _, r := range o.Results {
		if !ValidDynamicEvidenceRef(r.EvidenceRef) || seen[r.EvidenceRef] || r.Valid == nil || !validResultReceiptCitationReason(r.ReasonCode) || *r.Valid != (r.ReasonCode == "OK") {
			return errors.New("dynamic citation result is invalid")
		}
		seen[r.EvidenceRef] = true
	}
	return nil
}

func validateValidateCitationV4PrivateBinding(b validateCitationV4PrivateBinding) error {
	if (b.CandidateID == nil) != (b.CandidateHash == nil) || b.CandidateID != nil && (!canonicalResultReceiptID(*b.CandidateID) || !lowerHex(*b.CandidateHash, 64)) || b.Results == nil || len(b.Results) < 1 || len(b.Results) > 8 {
		return errors.New("dynamic citation authority is invalid")
	}
	seen := map[string]bool{}
	for _, r := range b.Results {
		if validateReadSourceV4PrivateBinding(readSourceV4PrivateBinding{r.SearchReceiptID, r.SearchReceiptHash, r.SearchEvidenceRef, r.resultReceiptCitationIdentity}) != nil || !canonicalResultReceiptID(r.ReadReceiptID) || r.ReadReceiptID == r.SearchReceiptID || !lowerHex(r.ReadReceiptHash, 64) || seen[r.EvidenceRef] {
			return errors.New("dynamic citation receipt binding is invalid")
		}
		seen[r.EvidenceRef] = true
	}
	return nil
}

func validateDynamicResultReceiptDocuments(c ResultReceiptContract, output json.RawMessage, binding *ResultReceiptPrivateBinding) error {
	if c.Tool.Name == "ReadGitStatus" {
		c.Tool.Version = 2
		return validateExactResultReceiptDocuments(c, output, binding)
	}
	if binding == nil {
		return errors.New("dynamic receipt binding is required")
	}
	switch c.Tool.Name {
	case "SearchKnowledge":
		o, err := decodeExactReceiptDocument(output, c.MaxOutputBytes, validateSearchKnowledgeV2ReceiptOutput)
		if err != nil {
			return err
		}
		b, err := decodeExactReceiptDocument(binding.Document, c.MaxPrivateBindingBytes, validateSearchKnowledgeV3PrivateBinding)
		if err != nil || !searchKnowledgeV2ReceiptBindingMatches(o, b) {
			return errors.New("dynamic search output binding drifted")
		}
	case "ReadSource":
		o, err := decodeExactReceiptDocument(output, c.MaxOutputBytes, validateReadSourceV4ReceiptOutput)
		if err != nil {
			return err
		}
		b, err := decodeExactReceiptDocument(binding.Document, c.MaxPrivateBindingBytes, validateReadSourceV4PrivateBinding)
		if err != nil || o.EvidenceRef != b.EvidenceRef || o.ContentHash != b.ContentHash {
			return errors.New("dynamic source output binding drifted")
		}
	case "ValidateCitation":
		o, err := decodeExactReceiptDocument(output, c.MaxOutputBytes, validateValidateCitationV4ReceiptOutput)
		if err != nil {
			return err
		}
		b, err := decodeExactReceiptDocument(binding.Document, c.MaxPrivateBindingBytes, validateValidateCitationV4PrivateBinding)
		if err != nil || len(o.Results) != len(b.Results) {
			return errors.New("dynamic citation output binding drifted")
		}
		for i, r := range o.Results {
			if r.EvidenceRef != b.Results[i].EvidenceRef {
				return errors.New("dynamic citation order drifted")
			}
		}
	default:
		return errors.New("dynamic receipt tool is unsupported")
	}
	return nil
}

func validateDynamicReceiptAuthority(r ResultReceipt, ref ToolRef) error {
	c, ok := WorkspaceAnalysisResultReceiptContract(ref)
	if !ok || WorkspaceAnalysisToolWorkflowVersion(ref) != 2 || r.Tool != ref || r.OutputSchema != c.OutputSchema || r.PersistencePolicy != ResultPersistenceCanonical || r.MaxOutputBytes != c.MaxOutputBytes || r.MaxPrivateBindingBytes != c.MaxPrivateBindingBytes || validateResultReceiptIDs(r) != nil || !lowerHex(r.DefinitionHash, 64) || r.CreatedAt.IsZero() || r.OutputBytes != int64(len(r.Output)) || r.OutputBytes > c.MaxOutputBytes || r.OutputHash != resultReceiptHash(r.Output) || validateResultReceiptPrivateBinding(r.PrivateBinding, c) != nil {
		return inconsistent(ErrorCodeResultReceiptBindingConflict, "dynamic receipt authority is invalid")
	}
	canonical, err := canonicalJSONObject(r.Output, int(c.MaxOutputBytes))
	if err != nil || !bytes.Equal(canonical, r.Output) || validateDynamicResultReceiptDocuments(c, r.Output, r.PrivateBinding) != nil {
		return inconsistent(ErrorCodeResultReceiptBindingConflict, "dynamic receipt documents are invalid")
	}
	return nil
}

// SearchKnowledgeV3ReceiptIdentities returns all five-or-fewer authorized local
// references. Global aliases are allocated only after this receipt is committed.
func SearchKnowledgeV3ReceiptIdentities(r ResultReceipt) ([]DynamicEvidenceIdentity, error) {
	if err := validateDynamicReceiptAuthority(r, ToolRef{Name: "SearchKnowledge", Version: 3}); err != nil {
		return nil, err
	}
	b, err := decodeExactReceiptDocument(r.PrivateBinding.Document, r.MaxPrivateBindingBytes, validateSearchKnowledgeV3PrivateBinding)
	if err != nil {
		return nil, err
	}
	items := make([]DynamicEvidenceIdentity, len(b.Items))
	for i, item := range b.Items {
		items[i] = dynamicEvidenceIdentity(item)
	}
	return items, nil
}

// SearchKnowledgeV3ReceiptModelOutput replaces local references only after the
// caller has proved the immutable global mapping for this exact receipt.
func SearchKnowledgeV3ReceiptModelOutput(r ResultReceipt, refs map[string]string) (json.RawMessage, error) {
	items, err := SearchKnowledgeV3ReceiptIdentities(r)
	if err != nil {
		return nil, err
	}
	if len(refs) != len(items) {
		return nil, inconsistent(ErrorCodeResultReceiptBindingConflict, "dynamic search alias count differs")
	}
	o, err := decodeExactReceiptDocument(r.Output, r.MaxOutputBytes, validateSearchKnowledgeV2ReceiptOutput)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i, item := range o.Items {
		ref, ok := refs[item.EvidenceRef]
		if !ok || !ValidDynamicEvidenceRef(ref) || seen[ref] {
			return nil, inconsistent(ErrorCodeResultReceiptBindingConflict, "dynamic search alias is invalid")
		}
		seen[ref] = true
		o.Items[i].EvidenceRef = ref
	}
	return json.Marshal(o)
}

// ReadGitStatusV3ReceiptModelOutput omits repository identities and Git object
// hashes. Only the aggregate state is needed for the model's next decision.
func ReadGitStatusV3ReceiptModelOutput(r ResultReceipt) (json.RawMessage, error) {
	if err := validateDynamicReceiptAuthority(r, ToolRef{Name: "ReadGitStatus", Version: 3}); err != nil {
		return nil, err
	}
	o, err := decodeExactReceiptDocument(r.Output, r.MaxOutputBytes, validateReadGitStatusV2ReceiptOutput)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Clean          bool `json:"clean"`
		StagedCount    int  `json:"staged_count"`
		UnstagedCount  int  `json:"unstaged_count"`
		UntrackedCount int  `json:"untracked_count"`
		ConflictCount  int  `json:"conflict_count"`
	}{*o.Clean, *o.StagedCount, *o.UnstagedCount, *o.UntrackedCount, *o.ConflictCount})
}

// ReadSourceV4ReceiptBinding exposes only a validated server-side receipt tuple.
func ReadSourceV4ReceiptBinding(r ResultReceipt) (DynamicReadBinding, error) {
	if err := validateDynamicReceiptAuthority(r, ToolRef{Name: "ReadSource", Version: 4}); err != nil {
		return DynamicReadBinding{}, err
	}
	b, err := decodeExactReceiptDocument(r.PrivateBinding.Document, r.MaxPrivateBindingBytes, validateReadSourceV4PrivateBinding)
	if err != nil {
		return DynamicReadBinding{}, err
	}
	return DynamicReadBinding{b.SearchReceiptID, b.SearchReceiptHash, b.SearchEvidenceRef, dynamicEvidenceIdentity(b.resultReceiptCitationIdentity)}, nil
}

// ReadSourceV4ReceiptEvidenceForSearch checks exact Workspace, Run, tuple and hash;
// it never reopens a newer source to replace the historical read.
func ReadSourceV4ReceiptEvidenceForSearch(r, search ResultReceipt) (ReadSourceV3ReceiptEvidence, error) {
	b, err := ReadSourceV4ReceiptBinding(r)
	if err != nil {
		return ReadSourceV3ReceiptEvidence{}, err
	}
	items, err := SearchKnowledgeV3ReceiptIdentities(search)
	if err != nil {
		return ReadSourceV3ReceiptEvidence{}, err
	}
	if r.WorkspaceID != search.WorkspaceID || r.WorkflowRunID != search.WorkflowRunID || b.SearchReceiptID != search.ID || b.SearchReceiptHash != search.OutputHash {
		return ReadSourceV3ReceiptEvidence{}, inconsistent(ErrorCodeResultReceiptBindingConflict, "dynamic source search owner drifted")
	}
	want := b.Identity
	want.EvidenceRef = b.SearchEvidenceRef
	matched := false
	for _, item := range items {
		if item == want {
			matched = true
			break
		}
	}
	if !matched {
		return ReadSourceV3ReceiptEvidence{}, inconsistent(ErrorCodeResultReceiptBindingConflict, "dynamic source search tuple drifted")
	}
	o, err := decodeExactReceiptDocument(r.Output, r.MaxOutputBytes, validateReadSourceV4ReceiptOutput)
	if err != nil {
		return ReadSourceV3ReceiptEvidence{}, err
	}
	return ReadSourceV3ReceiptEvidence{EvidenceRef: o.EvidenceRef, Excerpt: o.Excerpt, Truncated: *o.Truncated}, nil
}

// ValidateCitationV4ReceiptResults admits a loop receipt only with no candidate,
// and a publication receipt only with the complete immutable candidate binding.
func ValidateCitationV4ReceiptResults(r ResultReceipt, candidateID foundation.ID, candidateHash string, refs []string) ([]ValidateCitationV3ReceiptResult, error) {
	if err := validateDynamicReceiptAuthority(r, ToolRef{Name: "ValidateCitation", Version: 4}); err != nil {
		return nil, err
	}
	b, err := decodeExactReceiptDocument(r.PrivateBinding.Document, r.MaxPrivateBindingBytes, validateValidateCitationV4PrivateBinding)
	if err != nil {
		return nil, err
	}
	if candidateID == "" {
		if candidateHash != "" || b.CandidateID != nil {
			return nil, inconsistent(ErrorCodeResultReceiptBindingConflict, "loop citation candidate binding drifted")
		}
	} else if b.CandidateID == nil || *b.CandidateID != candidateID || *b.CandidateHash != candidateHash {
		return nil, inconsistent(ErrorCodeResultReceiptBindingConflict, "publication citation candidate binding drifted")
	}
	o, err := decodeExactReceiptDocument(r.Output, r.MaxOutputBytes, validateValidateCitationV4ReceiptOutput)
	if err != nil || len(o.Results) != len(refs) {
		return nil, inconsistent(ErrorCodeResultReceiptBindingConflict, "dynamic citation references differ")
	}
	results := make([]ValidateCitationV3ReceiptResult, len(refs))
	for i, ref := range refs {
		if ref != o.Results[i].EvidenceRef {
			return nil, inconsistent(ErrorCodeResultReceiptBindingConflict, "dynamic citation order differs")
		}
		results[i] = ValidateCitationV3ReceiptResult{EvidenceRef: ref, Valid: *o.Results[i].Valid, ReasonCode: o.Results[i].ReasonCode}
	}
	return results, nil
}

// ValidateCitationV4ReceiptCitations returns exact identities for final publication.
func ValidateCitationV4ReceiptCitations(r ResultReceipt, candidateID foundation.ID, candidateHash string, refs []string) ([]ValidateCitationV3ReceiptCitation, error) {
	if candidateID == "" {
		return nil, invalid(ErrorCodeResultReceiptInvalid, "publication citation candidate is required")
	}
	if _, err := ValidateCitationV4ReceiptResults(r, candidateID, candidateHash, refs); err != nil {
		return nil, err
	}
	b, _ := decodeExactReceiptDocument(r.PrivateBinding.Document, r.MaxPrivateBindingBytes, validateValidateCitationV4PrivateBinding)
	result := make([]ValidateCitationV3ReceiptCitation, len(b.Results))
	for i, r := range b.Results {
		result[i] = ValidateCitationV3ReceiptCitation{EvidenceRef: r.EvidenceRef, CitationID: r.CitationID, IndexVersionID: r.IndexVersionID, ChunkID: r.ChunkID, SourceVersionID: r.SourceVersionID, SourceSpanID: r.SourceSpanID}
	}
	return result, nil
}
