package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestRelationAnalyzerMapsFiveFixedAssessmentsSafely(t *testing.T) {
	tests := []struct {
		name             string
		assessment       knowledgedomain.RelationAssessment
		withExisting     bool
		wantDecision     knowledgedomain.AssessmentDecision
		wantRelationType knowledgedomain.RelationType
		wantConflict     bool
	}{
		{name: "new", assessment: knowledgedomain.AssessmentNew, wantDecision: knowledgedomain.AssessmentDecisionProposeNewClaim},
		{name: "complementary", assessment: knowledgedomain.AssessmentComplementary, withExisting: true, wantDecision: knowledgedomain.AssessmentDecisionSuggestRelation, wantRelationType: knowledgedomain.RelationComplements},
		{name: "duplicate", assessment: knowledgedomain.AssessmentDuplicate, withExisting: true, wantDecision: knowledgedomain.AssessmentDecisionSuggestRelation, wantRelationType: knowledgedomain.RelationDuplicates},
		{name: "conflict", assessment: knowledgedomain.AssessmentConflict, withExisting: true, wantDecision: knowledgedomain.AssessmentDecisionOpenConflict, wantConflict: true},
		{name: "low confidence", assessment: knowledgedomain.AssessmentLowConfidence, withExisting: true, wantDecision: knowledgedomain.AssessmentDecisionRequireHumanReview},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := relationTestRequest(t, test.withExisting)
			output := relationTestOutput(t, request, test.assessment)
			runner := &relationRunnerFake{output: output}
			knowledge := &relationKnowledgeFake{}
			analyzer, err := NewRelationAnalyzer(runner, knowledge)
			if err != nil {
				t.Fatal(err)
			}
			result, err := analyzer.Analyze(context.Background(), request)
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			if result.Assessment.Payload.Assessment != test.assessment || result.Action.Decision != test.wantDecision || result.Action.OpenConflict != test.wantConflict {
				t.Fatalf("result assessment/action = %#v / %#v", result.Assessment.Payload, result.Action)
			}
			if test.wantRelationType == "" {
				if result.Action.RelationType != nil {
					t.Fatalf("unexpected relation type = %v", *result.Action.RelationType)
				}
			} else if result.Action.RelationType == nil || *result.Action.RelationType != test.wantRelationType {
				t.Fatalf("relation type = %v, want %v", result.Action.RelationType, test.wantRelationType)
			}
			if runner.calls != 1 || knowledge.eligibilityCalls != 1 || knowledge.mapCalls != 1 {
				t.Fatalf("calls runner=%d eligibility=%d map=%d", runner.calls, knowledge.eligibilityCalls, knowledge.mapCalls)
			}
			if len(runner.request.Input) == 0 || len(request.Runtime.Input) != 0 {
				t.Fatalf("analyzer must build model input without mutating request")
			}
		})
	}
}

func TestRelationAnalyzerRejectsInvalidEndpointsBeforeModel(t *testing.T) {
	request := relationTestRequest(t, true)
	request.Existing.Node.Type = knowledgedomain.NodeTypeTopic
	runner := &relationRunnerFake{output: relationTestOutput(t, request, knowledgedomain.AssessmentComplementary)}
	knowledge := &relationKnowledgeFake{}
	analyzer, err := NewRelationAnalyzer(runner, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyzer.Analyze(context.Background(), request); relationTestErrorCode(err) != errorCodeRelationRequestInvalid {
		t.Fatalf("invalid endpoint code = %q, err=%v", relationTestErrorCode(err), err)
	}
	if runner.calls != 0 || knowledge.eligibilityCalls != 0 {
		t.Fatalf("invalid endpoints reached dependencies: runner=%d eligibility=%d", runner.calls, knowledge.eligibilityCalls)
	}
}

func TestRelationAnalyzerRejectsMissingOrWrongSideEvidence(t *testing.T) {
	tests := []struct {
		name     string
		refs     []string
		wantCode string
	}{
		{name: "missing", refs: nil, wantCode: agentdomain.ErrorCodeRelationAssessmentInvalid},
		{name: "wrong side", refs: []string{"candidate"}, wantCode: errorCodeRelationOutputMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := relationTestRequest(t, true)
			output := relationTestOutput(t, request, knowledgedomain.AssessmentDuplicate)
			var decoded agentdomain.RelationAssessmentResult
			if err := json.Unmarshal(output, &decoded); err != nil {
				t.Fatal(err)
			}
			decoded.Payload.ExistingEvidenceRefs = test.refs
			if test.name == "wrong side" {
				decoded.Payload.ExistingEvidenceRefs[0] = request.Candidate.Evidence[0].Citation.ID
			}
			output, _ = json.Marshal(decoded)
			analyzer, err := NewRelationAnalyzer(&relationRunnerFake{output: output}, &relationKnowledgeFake{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := analyzer.Analyze(context.Background(), request); relationTestErrorCode(err) != test.wantCode {
				t.Fatalf("evidence code = %q, want %q, err=%v", relationTestErrorCode(err), test.wantCode, err)
			}
		})
	}
}

func TestRelationAnalyzerRejectsApplicabilityDrift(t *testing.T) {
	request := relationTestRequest(t, true)
	output := relationTestOutput(t, request, knowledgedomain.AssessmentComplementary)
	var decoded agentdomain.RelationAssessmentResult
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Payload.Applicability.Existing = json.RawMessage(`{"environment":"staging"}`)
	output, _ = json.Marshal(decoded)
	analyzer, err := NewRelationAnalyzer(&relationRunnerFake{output: output}, &relationKnowledgeFake{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyzer.Analyze(context.Background(), request); relationTestErrorCode(err) != errorCodeRelationOutputMismatch {
		t.Fatalf("applicability drift code = %q, err=%v", relationTestErrorCode(err), err)
	}
}

func TestRelationAnalyzerLoadsFormalExistingClaimBeforeModel(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RelationAnalyzeRequest, *relationKnowledgeFake)
		code   string
	}{
		{name: "statement drift", mutate: func(request *RelationAnalyzeRequest, _ *relationKnowledgeFake) {
			request.Existing.Statement = "forged existing statement"
		}, code: errorCodeRelationRequestInvalid},
		{name: "applicability drift", mutate: func(request *RelationAnalyzeRequest, _ *relationKnowledgeFake) {
			request.Existing.Applicability, _ = knowledgedomain.ParseApplicability(json.RawMessage(`{"environment":"staging"}`))
		}, code: errorCodeRelationRequestInvalid},
		{name: "source drift", mutate: func(request *RelationAnalyzeRequest, _ *relationKnowledgeFake) {
			request.Existing.Evidence[0].Citation.SourceSpanID = relationTestID(399)
		}, code: errorCodeRelationEvidenceIneligible},
		{name: "eligibility owner mismatch", mutate: func(_ *RelationAnalyzeRequest, knowledge *relationKnowledgeFake) {
			knowledge.existingOwnerMismatch = true
		}, code: errorCodeRelationEvidenceIneligible},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := relationTestRequest(t, true)
			knowledge := &relationKnowledgeFake{}
			test.mutate(&request, knowledge)
			runner := &relationRunnerFake{output: relationTestOutput(t, request, knowledgedomain.AssessmentComplementary)}
			analyzer, err := NewRelationAnalyzer(runner, knowledge)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := analyzer.Analyze(context.Background(), request); relationTestErrorCode(err) != test.code {
				t.Fatalf("code=%s want=%s err=%v", relationTestErrorCode(err), test.code, err)
			}
			if runner.calls != 0 || knowledge.mapCalls != 0 || knowledge.formalCalls != 1 {
				t.Fatalf("calls formal=%d runner=%d map=%d", knowledge.formalCalls, runner.calls, knowledge.mapCalls)
			}
		})
	}
}

func TestRelationAnalyzerRequiresExactDisputedConflictDisclosure(t *testing.T) {
	request := relationTestRequest(t, true)
	conflictID := relationTestID(90)
	base := relationTestOutput(t, request, knowledgedomain.AssessmentConflict)
	var result agentdomain.RelationAssessmentResult
	if err := json.Unmarshal(base, &result); err != nil {
		t.Fatal(err)
	}
	exact := agentdomain.RelationConflictDisclosure{
		ClaimID: relationTestID(100), ConflictIDs: []foundation.ID{conflictID},
		Applicability: json.RawMessage(`{"environment":"prod"}`), UpdatedAt: time.Unix(1, 0).UTC(),
	}
	tests := []struct {
		name   string
		values []agentdomain.RelationConflictDisclosure
	}{
		{name: "missing", values: []agentdomain.RelationConflictDisclosure{}},
		{name: "extra conflict", values: []agentdomain.RelationConflictDisclosure{{
			ClaimID: exact.ClaimID, ConflictIDs: []foundation.ID{conflictID, relationTestID(91)}, Applicability: exact.Applicability, UpdatedAt: exact.UpdatedAt,
		}}},
		{name: "applicability drift", values: []agentdomain.RelationConflictDisclosure{{
			ClaimID: exact.ClaimID, ConflictIDs: exact.ConflictIDs, Applicability: json.RawMessage(`{"environment":"staging"}`), UpdatedAt: exact.UpdatedAt,
		}}},
		{name: "updated at drift", values: []agentdomain.RelationConflictDisclosure{{
			ClaimID: exact.ClaimID, ConflictIDs: exact.ConflictIDs, Applicability: exact.Applicability, UpdatedAt: time.Unix(2, 0).UTC(),
		}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := result
			candidate.Payload.ConflictDisclosures = test.values
			encoded, _ := json.Marshal(candidate)
			knowledge := &relationKnowledgeFake{disputedProvenance: provenanceFromCitation(request.Candidate.Evidence[0].Citation), conflictID: conflictID}
			analyzer, err := NewRelationAnalyzer(&relationRunnerFake{output: encoded}, knowledge)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := analyzer.Analyze(context.Background(), request); relationTestErrorCode(err) != errorCodeRelationOutputMismatch {
				t.Fatalf("code=%s err=%v", relationTestErrorCode(err), err)
			}
		})
	}
}

func TestRelationAnalyzerPreservesDisputedConflictEligibility(t *testing.T) {
	request := relationTestRequest(t, true)
	conflictID := relationTestID(90)
	knowledge := &relationKnowledgeFake{disputedProvenance: provenanceFromCitation(request.Candidate.Evidence[0].Citation), conflictID: conflictID}
	output := relationTestOutput(t, request, knowledgedomain.AssessmentConflict)
	var decoded agentdomain.RelationAssessmentResult
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Payload.ConflictDisclosures = []agentdomain.RelationConflictDisclosure{{
		ClaimID: relationTestID(100), ConflictIDs: []foundation.ID{conflictID},
		Applicability: json.RawMessage(`{"environment":"prod"}`), UpdatedAt: time.Unix(1, 0).UTC(),
	}}
	output, _ = json.Marshal(decoded)
	runner := &relationRunnerFake{output: output}
	analyzer, err := NewRelationAnalyzer(runner, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	result, err := analyzer.Analyze(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CandidateEvidence) != 1 || result.CandidateEvidence[0].Eligibility != knowledgedomain.EvidenceEligibleWithConflict ||
		len(result.CandidateEvidence[0].ConflictIDs) != 1 || result.CandidateEvidence[0].ConflictIDs[0] != conflictID {
		t.Fatalf("disputed evidence lost conflict binding: %#v", result.CandidateEvidence)
	}
	if !bytes.Contains(runner.request.Input, []byte(string(conflictID))) {
		t.Fatal("eligible disputed conflict id is missing from model input")
	}
}

func TestRelationAnalyzerFailsClosedForIneligibleEvidence(t *testing.T) {
	request := relationTestRequest(t, false)
	runner := &relationRunnerFake{output: relationTestOutput(t, request, knowledgedomain.AssessmentNew)}
	knowledge := &relationKnowledgeFake{ineligibleProvenance: provenanceFromCitation(request.Candidate.Evidence[0].Citation)}
	analyzer, err := NewRelationAnalyzer(runner, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyzer.Analyze(context.Background(), request); relationTestErrorCode(err) != errorCodeRelationEvidenceIneligible {
		t.Fatalf("ineligible evidence code = %q, err=%v", relationTestErrorCode(err), err)
	}
	if runner.calls != 0 || knowledge.mapCalls != 0 {
		t.Fatalf("ineligible evidence reached model/action: runner=%d map=%d", runner.calls, knowledge.mapCalls)
	}
}

func TestRelationAnalyzerRejectsUnsafeConflictMapping(t *testing.T) {
	request := relationTestRequest(t, true)
	relationType := knowledgedomain.RelationConflictsWith
	knowledge := &relationKnowledgeFake{mapOverride: &knowledgedomain.AssessmentAction{
		Decision: knowledgedomain.AssessmentDecisionOpenConflict, RelationType: &relationType, OpenConflict: true,
	}}
	analyzer, err := NewRelationAnalyzer(&relationRunnerFake{output: relationTestOutput(t, request, knowledgedomain.AssessmentConflict)}, knowledge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := analyzer.Analyze(context.Background(), request); relationTestErrorCode(err) != errorCodeRelationActionUnsafe {
		t.Fatalf("unsafe conflict mapping code = %q, err=%v", relationTestErrorCode(err), err)
	}
}

type relationRunnerFake struct {
	output  []byte
	calls   int
	request StructuredRunRequest
}

func (fake *relationRunnerFake) Run(_ context.Context, request StructuredRunRequest) (StructuredRunResult, error) {
	fake.calls++
	fake.request = request
	return StructuredRunResult{Output: append([]byte(nil), fake.output...), Phase: agentdomain.ModelCallInitial, CallCount: 1}, nil
}

type relationKnowledgeFake struct {
	formalCalls           int
	eligibilityCalls      int
	mapCalls              int
	disputedProvenance    knowledgedomain.ProvenanceRef
	ineligibleProvenance  knowledgedomain.ProvenanceRef
	conflictID            foundation.ID
	mapOverride           *knowledgedomain.AssessmentAction
	existingOwnerMismatch bool
}

func (fake *relationKnowledgeFake) LoadFormalClaims(_ context.Context, workspaceID foundation.ID, claimIDs []foundation.ID) ([]knowledgedomain.ClaimWithSources, error) {
	fake.formalCalls++
	if len(claimIDs) != 1 {
		return nil, errors.New("unexpected formal claim query")
	}
	applicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{"environment":"dev"}`))
	if err != nil {
		return nil, err
	}
	statement, normalized, err := knowledgedomain.NormalizeStatement("existing statement")
	if err != nil {
		return nil, err
	}
	claim := knowledgedomain.Claim{
		ID: claimIDs[0], WorkspaceID: workspaceID, Statement: statement, NormalizedStatement: normalized,
		Applicability: applicability, Status: knowledgedomain.ClaimStatusConfirmed,
		ConfidenceFactors: json.RawMessage(`{}`), Version: 1,
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
	}
	claim.Fingerprint = knowledgedomain.ComputeClaimFingerprint(workspaceID, normalized, applicability)
	citation := relationTestCitation(workspaceID, 30)
	source := knowledgedomain.ClaimSource{
		ID: relationTestID(71), WorkspaceID: workspaceID, ClaimID: claim.ID,
		Provenance: provenanceFromCitation(citation), SupportType: knowledgedomain.ClaimSupportSupports,
		Reason: "formal existing source", CreatedAt: time.Unix(1, 0).UTC(),
	}
	source.EvidenceHash = knowledgedomain.ComputeClaimSourceEvidenceHash(source, applicability)
	return []knowledgedomain.ClaimWithSources{{Claim: claim, Sources: []knowledgedomain.ClaimSource{source}}}, nil
}

func (fake *relationKnowledgeFake) CheckEvidenceEligibility(_ context.Context, query knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	fake.eligibilityCalls++
	disputedApplicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{"environment":"prod"}`))
	if err != nil {
		return nil, err
	}
	results := make([]knowledgedomain.ProvenanceEligibility, 0, len(query.Provenance))
	for index, provenance := range query.Provenance {
		result := knowledgedomain.ProvenanceEligibility{Provenance: provenance, Eligibility: knowledgedomain.EvidenceEligible}
		switch provenance {
		case fake.ineligibleProvenance:
			result.Eligibility = knowledgedomain.EvidenceIneligible
		case fake.disputedProvenance:
			result.Eligibility = knowledgedomain.EvidenceEligibleWithConflict
			result.Bindings = []knowledgedomain.EvidenceEligibilityBinding{{
				OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: relationTestID(100 + index*2), EvidenceID: relationTestID(101 + index*2),
				ClaimStatus: knowledgedomain.ClaimStatusDisputed, SupportType: knowledgedomain.ClaimSupportSupports, ConflictIDs: []foundation.ID{fake.conflictID},
				DisputedApplicability: disputedApplicability, DisputedClaimUpdatedAtUTC: time.Unix(1, 0).UTC(),
			}}
		default:
			ownerID := relationTestID(100 + index*2)
			if !fake.existingOwnerMismatch && provenance == provenanceFromCitation(relationTestCitation(query.WorkspaceID, 30)) {
				ownerID = relationTestID(11)
			}
			result.Bindings = []knowledgedomain.EvidenceEligibilityBinding{{
				OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: ownerID, EvidenceID: relationTestID(101 + index*2),
				ClaimStatus: knowledgedomain.ClaimStatusConfirmed, SupportType: knowledgedomain.ClaimSupportSupports,
			}}
		}
		results = append(results, result)
	}
	return results, nil
}

func (fake *relationKnowledgeFake) MapAssessment(assessment knowledgedomain.RelationAssessment, source, target knowledgedomain.NodeRef) (knowledgedomain.AssessmentAction, error) {
	fake.mapCalls++
	if fake.mapOverride != nil {
		return *fake.mapOverride, nil
	}
	return knowledgedomain.MapAssessment(assessment, source, target, false)
}

func relationTestRequest(t *testing.T, withExisting bool) RelationAnalyzeRequest {
	t.Helper()
	workspace := relationTestID(1)
	candidateApplicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{"environment":"prod"}`))
	if err != nil {
		t.Fatal(err)
	}
	existingApplicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{"environment":"dev"}`))
	if err != nil {
		t.Fatal(err)
	}
	request := RelationAnalyzeRequest{
		WorkspaceID: workspace,
		ModelRunRef: relationTestID(2),
		Retrieval:   agentdomain.RetrievalRef{IndexVersionID: relationTestID(5)},
		Runtime: StructuredRunRequest{
			ProfileRef: domainModelProfileRef(), PromptRef: domainPromptRef(),
			SchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RelationAssessmentSchemaID, Version: agentdomain.OutputSchemaVersionV1},
			ReducedSchemaRef: agentdomain.SchemaRef{ID: RelationAssessmentReducedSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		},
		Candidate: RelationClaimInput{
			Node: knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeClaim, ID: relationTestID(10)}, Statement: "candidate statement",
			Applicability: candidateApplicability, Evidence: []RelationEvidenceInput{{Citation: relationTestCitation(workspace, 20), Excerpt: "candidate evidence"}},
		},
	}
	if withExisting {
		request.Existing = &RelationClaimInput{
			Node: knowledgedomain.NodeRef{Type: knowledgedomain.NodeTypeClaim, ID: relationTestID(11)}, Statement: "existing statement",
			Applicability: existingApplicability, Evidence: []RelationEvidenceInput{{Citation: relationTestCitation(workspace, 30), Excerpt: "existing evidence"}},
		}
	}
	return request
}

func relationTestOutput(t *testing.T, request RelationAnalyzeRequest, assessment knowledgedomain.RelationAssessment) []byte {
	t.Helper()
	payload := agentdomain.RelationAssessmentPayload{
		Assessment: assessment, CandidateEvidenceRefs: []string{request.Candidate.Evidence[0].Citation.ID},
		ExistingEvidenceRefs: []string{}, ConflictDisclosures: []agentdomain.RelationConflictDisclosure{},
		Applicability: agentdomain.ApplicabilityComparison{Candidate: request.Candidate.Applicability.CanonicalJSON},
		Reason:        "fixed assessment reason", ConfidenceFactors: []string{"fixed evidence alignment"}, UncertaintyReasons: []string{"bounded fixture uncertainty"},
	}
	if assessment != knowledgedomain.AssessmentNew {
		if request.Existing == nil {
			t.Fatal("non-new fixture requires existing claim")
		}
		payload.ExistingEvidenceRefs = []string{request.Existing.Evidence[0].Citation.ID}
		payload.Applicability.Existing = request.Existing.Applicability.CanonicalJSON
		payload.Applicability.Summary = "conditions compared"
	}
	result := agentdomain.RelationAssessmentResult{
		ResultType: agentdomain.ResultTypeRelationAssessment, SchemaID: agentdomain.RelationAssessmentSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: request.ModelRunRef, Payload: payload,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func relationTestCitation(workspace foundation.ID, seed int) agentdomain.Citation {
	return agentdomain.Citation{
		ID: "cite-" + string(rune('a'+seed%26)), WorkspaceID: workspace, IndexVersionID: relationTestID(5),
		ChunkID: relationTestID(seed + 1), SourceVersionID: relationTestID(seed + 2), SourceSpanID: relationTestID(seed + 3),
	}
}

func relationTestID(seed int) foundation.ID {
	return foundation.ID(fmt.Sprintf("%08d-0000-4000-8000-%012d", seed, seed))
}

func domainModelProfileRef() agentdomain.ModelProfileRef {
	return agentdomain.ModelProfileRef{ID: "relation-profile", Version: "v1"}
}

func domainPromptRef() agentdomain.PromptRef {
	return agentdomain.PromptRef{ID: "relation-prompt", Version: "v1"}
}

func relationTestErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return ""
	}
	return classified.Code
}
