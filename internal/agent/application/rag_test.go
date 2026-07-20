package application

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestRAGExecutorReturnsUnsupportedScopeWithoutProviderCalls(t *testing.T) {
	ports := &ragPorts{}
	executor := newTestRAGExecutor(t, ports)
	request := testRAGRequest()
	request.AllowWeb = true

	proposal, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Refusal == nil || proposal.Refusal.Payload.ReasonCode != domain.RefusalExternalFactUnauthorized {
		t.Fatalf("proposal=%#v", proposal)
	}
	if ports.planCalls != 0 || ports.searchCalls != 0 || ports.runCalls != 0 {
		t.Fatalf("unexpected calls: %#v", ports)
	}
}

func TestRAGExecutorRejectsInvalidScopeBeforeProviderCall(t *testing.T) {
	ports := &ragPorts{}
	executor := newTestRAGExecutor(t, ports)
	request := testRAGRequest()
	request.SearchMode = retrievaldomain.SearchMode("invalid")

	_, err := executor.Execute(context.Background(), request)
	if applicationErrorCode(err) != errorCodeRAGRequestInvalid {
		t.Fatalf("err=%v", err)
	}
	if ports.planCalls != 0 || ports.searchCalls != 0 || ports.runCalls != 0 {
		t.Fatalf("unexpected calls: %#v", ports)
	}
}

func TestRAGExecutorTurnsClarificationPlanIntoCanonicalProposal(t *testing.T) {
	ports := &ragPorts{plan: domain.RAGQueryPlanResult{
		ResultType: domain.ResultTypeRAGQueryPlan, SchemaID: domain.RAGQueryPlanSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: ragID(2),
		Payload: domain.RAGQueryPlanPayload{Intent: "choose environment", RequiresClarification: true, Rewrites: []string{}, ClarificationReason: "environment missing", ClarificationQuestion: "Which environment?", SuggestedScopes: []string{"production"}},
	}}
	executor := newTestRAGExecutor(t, ports)

	proposal, err := executor.Execute(context.Background(), testRAGRequest())
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Clarification == nil || proposal.Clarification.Question != "Which environment?" || proposal.Refusal != nil {
		t.Fatalf("proposal=%#v", proposal)
	}
	if ports.planCalls != 1 || ports.searchCalls != 0 || ports.runCalls != 0 {
		t.Fatalf("unexpected calls: %#v", ports)
	}
}

func TestRAGExecutorFailsClosedWhenRetrievalTupleDriftsAcrossRewrites(t *testing.T) {
	ports := &ragPorts{plan: retrievalPlan(), searches: []ScopedRetrievalResult{
		emptySearch(ragID(3)), emptySearch(ragID(4)),
	}}
	executor := newTestRAGExecutor(t, ports)

	_, err := executor.Execute(context.Background(), testRAGRequest())
	if applicationErrorCode(err) != errorCodeRAGResultDrift {
		t.Fatalf("err=%v", err)
	}
	if ports.searchCalls != 2 || ports.eligibilityCalls != 0 || ports.runCalls != 0 {
		t.Fatalf("unexpected calls: %#v", ports)
	}
}

func TestRAGExecutorClassifiesEmptyRetrievalAsNoRelevantEvidence(t *testing.T) {
	ports := &ragPorts{plan: retrievalPlan(), searches: []ScopedRetrievalResult{emptySearch(ragID(3)), emptySearch(ragID(3))}}
	executor := newTestRAGExecutor(t, ports)

	proposal, err := executor.Execute(context.Background(), testRAGRequest())
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Refusal == nil || proposal.Refusal.Payload.ReasonCode != domain.RefusalNoRelevantEvidence || proposal.Retrieval == nil || len(proposal.Retrieval.Rewrites) != 2 || proposal.Retrieval.Degradations == nil {
		t.Fatalf("proposal=%#v", proposal)
	}
}

func TestRAGExecutorClassifiesExactIneligibleBatchAsUnapprovedOnly(t *testing.T) {
	item := testRetrievedEvidence()
	ref := knowledgedomain.ProvenanceRef{WorkspaceID: item.Citation.WorkspaceID, SourceVersionID: item.Citation.SourceVersionID, SourceSpanID: item.Citation.SourceSpanID}
	search := emptySearch(ragID(3))
	search.RetrievalBatch.Items = []RetrievedEvidence{item}
	ports := &ragPorts{plan: retrievalPlan(), searches: []ScopedRetrievalResult{search, emptySearch(ragID(3))}, eligibility: []knowledgedomain.ProvenanceEligibility{{Provenance: ref, Eligibility: knowledgedomain.EvidenceIneligible, Bindings: []knowledgedomain.EvidenceEligibilityBinding{}}}}
	executor := newTestRAGExecutor(t, ports)

	proposal, err := executor.Execute(context.Background(), testRAGRequest())
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Refusal == nil || proposal.Refusal.Payload.ReasonCode != domain.RefusalUnapprovedEvidenceOnly || proposal.Retrieval.CandidateCount != 1 {
		t.Fatalf("proposal=%#v", proposal)
	}
}

func TestRAGExecutorAcceptsStructuredReducedRefusal(t *testing.T) {
	base, batch := validCitationAnswer(false)
	item := batch.Items[0]
	ref := provenance(item.Citation)
	refusal := domain.RefusalResult{
		ResultType: domain.ResultTypeRefusal, SchemaID: domain.RefusalSchemaID,
		SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: base.ModelRunRef,
		Payload: newRefusalPayload(domain.RefusalValidationExhausted, "structured generation could not be validated"),
	}
	raw, err := json.Marshal(refusal)
	if err != nil {
		t.Fatal(err)
	}
	search := ScopedRetrievalResult{
		SearchResult: retrievaldomain.SearchResult{
			WorkspaceID: base.Payload.Citations[0].WorkspaceID, RequestedMode: retrievaldomain.SearchModeHybrid,
			EffectiveMode: retrievaldomain.SearchModeHybrid, IndexVersionID: batch.IndexVersionID,
		},
		RetrievalBatch: batch,
	}
	ports := &ragPorts{
		plan: domain.RAGQueryPlanResult{
			ResultType: domain.ResultTypeRAGQueryPlan, SchemaID: domain.RAGQueryPlanSchemaID,
			SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: base.ModelRunRef,
			Payload: domain.RAGQueryPlanPayload{Intent: "answer", Rewrites: []string{"approved evidence"}, SuggestedScopes: []string{}},
		},
		searches:    []ScopedRetrievalResult{search},
		eligibility: []knowledgedomain.ProvenanceEligibility{testEligibility(ref, knowledgedomain.EvidenceEligible, 0)},
		topics: []knowledgedomain.EvidenceTopicBinding{{
			Provenance: ref, TopicID: ragID(7), TopicName: "Deployment",
		}},
		generation: StructuredRunResult{
			Output: raw, Phase: domain.ModelCallReduced,
			Runtime: FrozenRuntimeRefs{Schema: domain.SchemaRef{ID: domain.RefusalSchemaID, Version: domain.OutputSchemaVersionV1}},
		},
	}
	executor := newTestRAGExecutor(t, ports)
	request := testRAGRequest()
	request.WorkspaceID = base.Payload.Citations[0].WorkspaceID
	request.ModelRunRef = base.ModelRunRef
	proposal, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Refusal == nil || proposal.Refusal.Payload.ReasonCode != domain.RefusalValidationExhausted || ports.runCalls != 1 {
		t.Fatalf("proposal=%#v calls=%#v", proposal, ports)
	}
	ports.searchCalls = 0
	ports.generation.Phase = domain.ModelCallInitial
	if _, err := executor.Execute(context.Background(), request); applicationErrorCode(err) != errorCodeRAGRequestInvalid {
		t.Fatalf("initial refusal schema err=%v", err)
	}
}

func TestRAGExecutorProducesValidatedV2TerminalProposal(t *testing.T) {
	base, batch := validCitationAnswer(false)
	item := batch.Items[0]
	ref := provenance(item.Citation)
	topicID := ragID(7)
	v2 := domain.RAGAnswerResultV2{
		ResultType: base.ResultType, SchemaID: base.SchemaID, SchemaVersion: domain.OutputSchemaVersionV2,
		ModelRunRef: base.ModelRunRef,
		Payload: domain.RAGAnswerPayloadV2{
			RAGAnswerPayload:  base.Payload,
			RelatedTopics:     []domain.RelatedTopic{{TopicID: topicID, Name: "Deployment", CitationIDs: []string{item.Citation.ID}}},
			FollowUpQuestions: []string{"Which deployment stage should be reviewed next?"},
		},
	}
	raw, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	review := domain.FaithfulnessReviewResult{}
	search := ScopedRetrievalResult{
		SearchResult: retrievaldomain.SearchResult{
			WorkspaceID: item.Citation.WorkspaceID, RequestedMode: retrievaldomain.SearchModeHybrid,
			EffectiveMode: retrievaldomain.SearchModeHybrid, IndexVersionID: batch.IndexVersionID,
		},
		RetrievalBatch: batch,
	}
	ports := &ragPorts{
		plan: domain.RAGQueryPlanResult{
			ResultType: domain.ResultTypeRAGQueryPlan, SchemaID: domain.RAGQueryPlanSchemaID,
			SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: base.ModelRunRef,
			Payload: domain.RAGQueryPlanPayload{Intent: "answer", Rewrites: []string{"approved evidence"}, SuggestedScopes: []string{}},
		},
		searches: []ScopedRetrievalResult{search},
		eligibility: []knowledgedomain.ProvenanceEligibility{
			testEligibility(ref, knowledgedomain.EvidenceEligible, 0),
		},
		topics: []knowledgedomain.EvidenceTopicBinding{{Provenance: ref, TopicID: topicID, TopicName: "Deployment"}},
		generation: StructuredRunResult{
			Output: raw, Phase: domain.ModelCallInitial,
			Runtime: FrozenRuntimeRefs{Schema: domain.SchemaRef{ID: domain.RAGAnswerSchemaID, Version: domain.OutputSchemaVersionV2}},
		},
		publication: AnswerPublicationResult{Answer: &base, Review: &review},
	}
	executor := newTestRAGExecutor(t, ports)
	request := testRAGRequest()
	request.WorkspaceID = item.Citation.WorkspaceID
	request.ModelRunRef = base.ModelRunRef
	proposal, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Answer == nil || proposal.Answer.SchemaVersion != domain.OutputSchemaVersionV2 || proposal.Retrieval == nil ||
		proposal.Retrieval.CandidateCount != 1 || proposal.Retrieval.SelectedCount != 1 || proposal.Retrieval.Degradations == nil || ports.runCalls != 1 || ports.publishCalls != 1 {
		t.Fatalf("proposal=%#v ports=%#v", proposal, ports)
	}
	wantStages := []RAGProgressStage{
		RAGProgressPlanStarted, RAGProgressPlanCompleted, RAGProgressRetrievalStarted,
		RAGProgressRetrievalCompleted, RAGProgressValidationStarted, RAGProgressValidationCompleted,
	}
	if len(ports.progress) != len(wantStages) {
		t.Fatalf("progress=%#v", ports.progress)
	}
	for index, stage := range wantStages {
		if ports.progress[index].Stage != stage {
			t.Fatalf("progress[%d]=%#v want=%s", index, ports.progress[index], stage)
		}
	}
}

type ragPorts struct {
	plan             domain.RAGQueryPlanResult
	searches         []ScopedRetrievalResult
	planCalls        int
	searchCalls      int
	eligibilityCalls int
	runCalls         int
	publishCalls     int
	progress         []RAGProgressUpdate
	eligibility      []knowledgedomain.ProvenanceEligibility
	topics           []knowledgedomain.EvidenceTopicBinding
	generation       StructuredRunResult
	publication      AnswerPublicationResult
}

func (p *ragPorts) Plan(context.Context, QueryPlanRequest) (QueryPlanRunResult, error) {
	p.planCalls++
	return QueryPlanRunResult{Plan: p.plan}, nil
}
func (p *ragPorts) Search(context.Context, retrievaldomain.SearchRequest) (ScopedRetrievalResult, error) {
	result := p.searches[p.searchCalls]
	p.searchCalls++
	return result, nil
}
func (p *ragPorts) CheckEvidenceEligibility(context.Context, knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	p.eligibilityCalls++
	return p.eligibility, nil
}
func (p *ragPorts) ResolveRAGTopics(context.Context, foundation.ID, []knowledgedomain.ProvenanceRef) ([]knowledgedomain.EvidenceTopicBinding, error) {
	return p.topics, nil
}
func (p *ragPorts) Run(context.Context, StructuredRunRequest) (StructuredRunResult, error) {
	p.runCalls++
	return p.generation, nil
}
func (p *ragPorts) Publish(context.Context, AnswerPublicationRequest) (AnswerPublicationResult, error) {
	p.publishCalls++
	return p.publication, nil
}
func (p *ragPorts) RecordRAGProgress(_ context.Context, update RAGProgressUpdate) error {
	p.progress = append(p.progress, update)
	return nil
}

func newTestRAGExecutor(t *testing.T, ports *ragPorts) *RAGExecutor {
	t.Helper()
	executor, err := NewRAGExecutor(ports, ports, ports, ports, ports, ports, ports)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}
func testRAGRequest() RAGExecutionRequest {
	return RAGExecutionRequest{WorkspaceID: ragID(1), ModelRunRef: ragID(2), PlanInput: []byte(`{"question":"deploy?"}`), AnswerInput: []byte(`{"question":"deploy?","answer_depth":"standard","output_format":"markdown"}`), SearchMode: retrievaldomain.SearchModeHybrid}
}
func retrievalPlan() domain.RAGQueryPlanResult {
	return domain.RAGQueryPlanResult{ResultType: domain.ResultTypeRAGQueryPlan, SchemaID: domain.RAGQueryPlanSchemaID, SchemaVersion: domain.OutputSchemaVersionV1, ModelRunRef: ragID(2), Payload: domain.RAGQueryPlanPayload{Intent: "deploy", Rewrites: []string{"deploy one", "deploy two"}, SuggestedScopes: []string{}}}
}
func emptySearch(index foundation.ID) ScopedRetrievalResult {
	return ScopedRetrievalResult{SearchResult: retrievaldomain.SearchResult{WorkspaceID: ragID(1), RequestedMode: retrievaldomain.SearchModeHybrid, EffectiveMode: retrievaldomain.SearchModeHybrid, IndexVersionID: index}, RetrievalBatch: RetrievalBatch{WorkspaceID: ragID(1), IndexVersionID: index}}
}
func ragID(value byte) foundation.ID {
	return foundation.ID("00000000-0000-4000-8000-00000000000" + string(rune('0'+value)))
}

func testRetrievedEvidence() RetrievedEvidence {
	return RetrievedEvidence{Citation: domain.Citation{ID: "citation-1", WorkspaceID: ragID(1), IndexVersionID: ragID(3), ChunkID: ragID(4), SourceVersionID: ragID(5), SourceSpanID: ragID(6)}, SearchExcerpt: "approved deployment evidence"}
}
