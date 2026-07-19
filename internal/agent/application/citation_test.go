package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

const (
	citationWorkspaceID foundation.ID = "52000000-0000-4000-8000-000000000001"
	citationIndexID     foundation.ID = "52000000-0000-4000-8000-000000000002"
	citationChunkID     foundation.ID = "52000000-0000-4000-8000-000000000003"
	citationSourceV1    foundation.ID = "52000000-0000-4000-8000-000000000004"
	citationSpan1       foundation.ID = "52000000-0000-4000-8000-000000000005"
	citationSourceV2    foundation.ID = "52000000-0000-4000-8000-000000000006"
	citationSpan2       foundation.ID = "52000000-0000-4000-8000-000000000007"
	citationModelRunID  foundation.ID = "52000000-0000-4000-8000-000000000008"
)

func TestCitationValidatorAcceptsEligibleAndMultipleConcreteProvenance(t *testing.T) {
	answer, batch := validCitationAnswer(true)
	retrieval := &fakeAgentRetrieval{opened: map[string]OpenedEvidence{
		"cite-1": {Citation: answer.Payload.Citations[0], Excerpt: "first immutable source span"},
		"cite-2": {Citation: answer.Payload.Citations[1], Excerpt: "second immutable source span"},
	}}
	eligibility := &fakeEligibility{classifications: map[string]knowledgedomain.EvidenceEligibility{
		string(citationSourceV1): knowledgedomain.EvidenceEligible,
		string(citationSourceV2): knowledgedomain.EvidenceEligible,
	}}
	validator := newCitationValidatorForTest(t, retrieval, eligibility)
	result, err := validator.Validate(context.Background(), CitationValidationRequest{
		WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refusal != nil || len(result.Evidence) != 2 || eligibility.calls != 1 || len(eligibility.last.Provenance) != 2 {
		t.Fatalf("result=%#v eligibility=%#v", result, eligibility)
	}
	if result.Evidence[0].Citation.SourceVersionID == result.Evidence[1].Citation.SourceVersionID ||
		result.Evidence[0].Excerpt != "first immutable source span" {
		t.Fatalf("evidence=%#v", result.Evidence)
	}
}

func TestCitationValidatorRefusesCrossWorkspaceDamagedAndUnapprovedEvidence(t *testing.T) {
	t.Run("cross workspace", func(t *testing.T) {
		answer, batch := validCitationAnswer(true)
		answer.Payload.Citations[1].WorkspaceID = "52000000-0000-4000-8000-000000000099"
		validator := newCitationValidatorForTest(t, &fakeAgentRetrieval{}, &fakeEligibility{})
		result, err := validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
		if err != nil || result.Refusal == nil || result.Refusal.ReasonCode != domain.RefusalCitationUnresolvable {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("damaged artifact", func(t *testing.T) {
		answer, batch := validCitationAnswer(false)
		damaged := foundation.NewError(foundation.ErrorConsistencyViolation, "RETRIEVAL_EVIDENCE_ARTIFACT_INVALID", false, errors.New("hash mismatch"))
		validator := newCitationValidatorForTest(t, &fakeAgentRetrieval{openErr: damaged}, &fakeEligibility{})
		result, err := validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
		if err != nil || result.Refusal == nil || result.Refusal.ReasonCode != domain.RefusalCitationUnresolvable {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})

	t.Run("unapproved", func(t *testing.T) {
		answer, batch := validCitationAnswer(false)
		retrieval := &fakeAgentRetrieval{opened: map[string]OpenedEvidence{
			"cite-1": {Citation: answer.Payload.Citations[0], Excerpt: "immutable"},
		}}
		validator := newCitationValidatorForTest(t, retrieval, &fakeEligibility{})
		result, err := validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
		if err != nil || result.Refusal == nil || result.Refusal.ReasonCode != domain.RefusalUnapprovedEvidenceOnly {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
}

func TestCitationValidatorRequiresDisputedEvidenceDisclosure(t *testing.T) {
	answer, batch := validCitationAnswer(true)
	retrieval := &fakeAgentRetrieval{opened: map[string]OpenedEvidence{
		"cite-1": {Citation: answer.Payload.Citations[0], Excerpt: "disputed source"},
		"cite-2": {Citation: answer.Payload.Citations[1], Excerpt: "opposing disputed source"},
	}}
	eligibility := &fakeEligibility{classifications: map[string]knowledgedomain.EvidenceEligibility{
		string(citationSourceV1): knowledgedomain.EvidenceEligibleWithConflict,
		string(citationSourceV2): knowledgedomain.EvidenceEligibleWithConflict,
	}}
	validator := newCitationValidatorForTest(t, retrieval, eligibility)
	result, err := validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
	if err != nil || result.Refusal == nil || result.Refusal.ReasonCode != domain.RefusalConflictNotConditionable {
		t.Fatalf("result=%#v err=%v", result, err)
	}

	answer.Payload.ConflictPositions = []domain.ConflictPosition{
		{ClaimID: "52000000-0000-4000-8000-000000000020", Position: "position one", Applicability: json.RawMessage(`{"environment":"prod"}`), CitationIDs: []string{"cite-1"}, UpdatedAt: time.Unix(1, 0).UTC()},
		{ClaimID: "52000000-0000-4000-8000-000000000021", Position: "position two", Applicability: json.RawMessage(`{"environment":"dev"}`), CitationIDs: []string{"cite-2"}, UpdatedAt: time.Unix(2, 0).UTC()},
	}
	answer.Payload.ConflictSummary = "sources disagree under different conditions"
	result, err = validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
	if err != nil || result.Refusal == nil || result.Refusal.ReasonCode != domain.RefusalConflictNotConditionable {
		t.Fatalf("arbitrary claim ids result=%#v err=%v", result, err)
	}

	answer.Payload.ConflictPositions = []domain.ConflictPosition{
		{ClaimID: "52000000-0000-4000-8000-000000000030", Position: "position one", Applicability: json.RawMessage(`{"environment":"staging"}`), CitationIDs: []string{"cite-1"}, UpdatedAt: time.Unix(1, 0).UTC()},
		{ClaimID: "52000000-0000-4000-8000-000000000031", Position: "position two", Applicability: json.RawMessage(`{"environment":"dev"}`), CitationIDs: []string{"cite-2"}, UpdatedAt: time.Unix(2, 0).UTC()},
	}
	result, err = validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
	if err != nil || result.Refusal == nil || result.Refusal.ReasonCode != domain.RefusalConflictNotConditionable {
		t.Fatalf("applicability drift result=%#v err=%v", result, err)
	}

	answer.Payload.ConflictPositions[0].Applicability = json.RawMessage(`{"environment":"prod"}`)
	answer.Payload.ConflictPositions[0].UpdatedAt = time.Unix(9, 0).UTC()
	result, err = validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
	if err != nil || result.Refusal == nil || result.Refusal.ReasonCode != domain.RefusalConflictNotConditionable {
		t.Fatalf("updated time drift result=%#v err=%v", result, err)
	}

	answer.Payload.ConflictPositions[0].UpdatedAt = time.Unix(1, 0).UTC()
	result, err = validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
	if err != nil || result.Refusal != nil || len(result.Evidence) != 2 ||
		result.Evidence[0].Eligibility != knowledgedomain.EvidenceEligibleWithConflict || len(result.Evidence[0].ConflictIDs) != 1 ||
		result.Evidence[1].Eligibility != knowledgedomain.EvidenceEligibleWithConflict || len(result.Evidence[1].ConflictIDs) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCitationValidatorRejectsConflictInventedFromConfirmedEvidence(t *testing.T) {
	answer, batch := validCitationAnswer(true)
	answer.Payload.ConflictPositions = []domain.ConflictPosition{
		{ClaimID: "52000000-0000-4000-8000-000000000030", Position: "invented position one", Applicability: json.RawMessage(`{"environment":"prod"}`), CitationIDs: []string{"cite-1"}, UpdatedAt: time.Unix(1, 0).UTC()},
		{ClaimID: "52000000-0000-4000-8000-000000000031", Position: "invented position two", Applicability: json.RawMessage(`{"environment":"dev"}`), CitationIDs: []string{"cite-2"}, UpdatedAt: time.Unix(2, 0).UTC()},
	}
	answer.Payload.ConflictSummary = "invented conflict"
	retrieval := &fakeAgentRetrieval{opened: map[string]OpenedEvidence{
		"cite-1": {Citation: answer.Payload.Citations[0], Excerpt: "confirmed source one"},
		"cite-2": {Citation: answer.Payload.Citations[1], Excerpt: "confirmed source two"},
	}}
	eligibility := &fakeEligibility{classifications: map[string]knowledgedomain.EvidenceEligibility{
		string(citationSourceV1): knowledgedomain.EvidenceEligible,
		string(citationSourceV2): knowledgedomain.EvidenceEligible,
	}}
	validator := newCitationValidatorForTest(t, retrieval, eligibility)
	result, err := validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
	if err != nil || result.Refusal == nil || result.Refusal.ReasonCode != domain.RefusalConflictNotConditionable {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCitationValidatorPreservesTransientFailures(t *testing.T) {
	answer, batch := validCitationAnswer(false)
	retryable := foundation.NewError(foundation.ErrorRetryableFailure, "RETRIEVAL_TEMPORARY", true, errors.New("temporary"))
	validator := newCitationValidatorForTest(t, &fakeAgentRetrieval{openErr: retryable}, &fakeEligibility{})
	result, err := validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
	if !errors.Is(err, retryable) || result.Refusal != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCitationValidatorDoesNotMaskEligibilityConsistencyFailureAsUnapproved(t *testing.T) {
	answer, batch := validCitationAnswer(false)
	retrieval := &fakeAgentRetrieval{opened: map[string]OpenedEvidence{
		"cite-1": {Citation: answer.Payload.Citations[0], Excerpt: "immutable"},
	}}
	consistency := foundation.NewError(foundation.ErrorConsistencyViolation, "KNOWLEDGE_ELIGIBILITY_INVALID", false, errors.New("incomplete result"))
	validator := newCitationValidatorForTest(t, retrieval, &fakeEligibility{err: consistency})
	result, err := validator.Validate(context.Background(), CitationValidationRequest{WorkspaceID: citationWorkspaceID, Answer: answer, Retrieval: batch})
	if !errors.Is(err, consistency) || result.Refusal != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func newCitationValidatorForTest(t *testing.T, retrieval RetrievalPort, eligibility EvidenceEligibilityPort) *CitationValidator {
	t.Helper()
	validator, err := NewCitationValidator(retrieval, eligibility)
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func validCitationAnswer(multiple bool) (domain.RAGAnswerResult, RetrievalBatch) {
	citations := []domain.Citation{{
		ID: "cite-1", WorkspaceID: citationWorkspaceID, IndexVersionID: citationIndexID, ChunkID: citationChunkID,
		SourceVersionID: citationSourceV1, SourceSpanID: citationSpan1,
	}}
	if multiple {
		citations = append(citations, domain.Citation{
			ID: "cite-2", WorkspaceID: citationWorkspaceID, IndexVersionID: citationIndexID, ChunkID: citationChunkID,
			SourceVersionID: citationSourceV2, SourceSpanID: citationSpan2,
		})
	}
	citationIDs := make([]string, len(citations))
	items := make([]RetrievedEvidence, len(citations))
	for index, citation := range citations {
		citationIDs[index] = citation.ID
		items[index] = RetrievedEvidence{Citation: citation, SearchExcerpt: "search excerpt", CapturedAt: time.Unix(int64(index+1), 0).UTC()}
	}
	answer := domain.RAGAnswerResult{
		ResultType: domain.ResultTypeRAGAnswer, SchemaID: domain.RAGAnswerSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: citationModelRunID,
		Payload: domain.RAGAnswerPayload{
			Conclusion: "evidence-bound conclusion",
			Assertions: []domain.Assertion{{ID: "assertion-1", Text: "supported fact", Kind: domain.AssertionFactual, CitationIDs: citationIDs}},
			Citations:  citations, ConflictPositions: []domain.ConflictPosition{},
		},
	}
	return answer, RetrievalBatch{WorkspaceID: citationWorkspaceID, IndexVersionID: citationIndexID, Items: items}
}

type fakeAgentRetrieval struct {
	opened  map[string]OpenedEvidence
	openErr error
}

func (*fakeAgentRetrieval) Retrieve(context.Context, RetrievalRequest) (RetrievalBatch, error) {
	return RetrievalBatch{}, nil
}

func (fake *fakeAgentRetrieval) Open(_ context.Context, citation domain.Citation) (OpenedEvidence, error) {
	if fake.openErr != nil {
		return OpenedEvidence{}, fake.openErr
	}
	return fake.opened[citation.ID], nil
}

func (fake *fakeAgentRetrieval) OpenBatch(_ context.Context, citations []domain.Citation) ([]OpenedEvidence, error) {
	if fake.openErr != nil {
		return nil, fake.openErr
	}
	result := make([]OpenedEvidence, len(citations))
	for index, citation := range citations {
		opened, exists := fake.opened[citation.ID]
		if !exists {
			return nil, foundation.NewError(foundation.ErrorNotFound, "EVIDENCE_NOT_FOUND", false, errors.New("missing"))
		}
		result[index] = opened
	}
	return result, nil
}

type fakeEligibility struct {
	calls           int
	last            knowledgedomain.EvidenceEligibilityQuery
	classifications map[string]knowledgedomain.EvidenceEligibility
	err             error
}

func (fake *fakeEligibility) CheckEvidenceEligibility(_ context.Context, query knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	fake.calls++
	fake.last = query
	if fake.err != nil {
		return nil, fake.err
	}
	result := make([]knowledgedomain.ProvenanceEligibility, len(query.Provenance))
	for index, ref := range query.Provenance {
		classification := fake.classifications[string(ref.SourceVersionID)]
		result[index] = testEligibility(ref, classification, index)
	}
	return result, nil
}

func testEligibility(ref knowledgedomain.ProvenanceRef, classification knowledgedomain.EvidenceEligibility, index int) knowledgedomain.ProvenanceEligibility {
	result := knowledgedomain.ProvenanceEligibility{Provenance: ref, Eligibility: classification}
	if classification == knowledgedomain.EvidenceIneligible || classification == "" {
		result.Eligibility = knowledgedomain.EvidenceIneligible
		return result
	}
	binding := knowledgedomain.EvidenceEligibilityBinding{
		OwnerType:   knowledgedomain.EvidenceOwnerClaim,
		OwnerID:     foundation.ID(fmt.Sprintf("52000000-0000-4000-8000-%012d", 30+index)),
		EvidenceID:  foundation.ID(fmt.Sprintf("52000000-0000-4000-8000-%012d", 40+index)),
		ClaimStatus: knowledgedomain.ClaimStatusConfirmed,
		SupportType: knowledgedomain.ClaimSupportSupports,
	}
	if classification == knowledgedomain.EvidenceEligibleWithConflict {
		raw := json.RawMessage(`{"environment":"prod"}`)
		if index == 1 {
			raw = json.RawMessage(`{"environment":"dev"}`)
		}
		disputedApplicability, err := knowledgedomain.ParseApplicability(raw)
		if err != nil {
			panic(err)
		}
		binding.ClaimStatus = knowledgedomain.ClaimStatusDisputed
		binding.ConflictIDs = []foundation.ID{"52000000-0000-4000-8000-000000000050"}
		binding.DisputedApplicability = disputedApplicability
		binding.DisputedClaimUpdatedAtUTC = time.Unix(int64(index+1), 0).UTC()
	}
	result.Bindings = []knowledgedomain.EvidenceEligibilityBinding{binding}
	return result
}
