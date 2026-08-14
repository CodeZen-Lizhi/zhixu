package eino

import (
	"context"
	"reflect"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestRAGExecutionSchedulerOwnsRAGBranchRouting(t *testing.T) {
	tests := []struct {
		name          string
		ports         func() *ragSchedulerPorts
		wantPath      []string
		wantReason    agentdomain.RefusalReasonCode
		clarification bool
		answer        bool
	}{
		{
			name: "clarification after plan",
			ports: func() *ragSchedulerPorts {
				return &ragSchedulerPorts{plan: agentdomain.RAGQueryPlanResult{
					ResultType: agentdomain.ResultTypeRAGQueryPlan, SchemaID: agentdomain.RAGQueryPlanSchemaID,
					SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: ragSchedulerID(2),
					Payload: agentdomain.RAGQueryPlanPayload{
						Intent: "scope", RequiresClarification: true, Rewrites: []string{},
						ClarificationReason: "scope missing", ClarificationQuestion: "Which scope?", SuggestedScopes: []string{"project"},
					},
				}}
			},
			wantPath:      []string{ragPlanNode, ragClarificationTerminalNode},
			clarification: true,
		},
		{
			name: "refusal after empty retrieval",
			ports: func() *ragSchedulerPorts {
				return &ragSchedulerPorts{
					plan: agentdomain.RAGQueryPlanResult{
						ResultType: agentdomain.ResultTypeRAGQueryPlan, SchemaID: agentdomain.RAGQueryPlanSchemaID,
						SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: ragSchedulerID(2),
						Payload: agentdomain.RAGQueryPlanPayload{Intent: "answer", Rewrites: []string{"approved evidence"}, SuggestedScopes: []string{}},
					},
					search: agentapplication.ScopedRetrievalResult{
						SearchResult: retrievaldomain.SearchResult{
							WorkspaceID: ragSchedulerID(1), RequestedMode: retrievaldomain.SearchModeHybrid,
							EffectiveMode: retrievaldomain.SearchModeHybrid, IndexVersionID: ragSchedulerID(3),
							Degradations: []retrievaldomain.SearchDegradation{},
						},
						RetrievalBatch: agentapplication.RetrievalBatch{WorkspaceID: ragSchedulerID(1), IndexVersionID: ragSchedulerID(3), Items: []agentapplication.RetrievedEvidence{}},
					},
				}
			},
			wantPath:   []string{ragPlanNode, ragRetrievalNode, ragRetrievalRefusalNode},
			wantReason: agentdomain.RefusalNoRelevantEvidence,
		},
		{
			name: "refusal after evidence gate",
			ports: func() *ragSchedulerPorts {
				ports := approvedRAGSchedulerPorts()
				ports.eligibility = []knowledgedomain.ProvenanceEligibility{{
					Provenance:  ragSchedulerProvenance(ports.search.RetrievalBatch.Items[0].Citation),
					Eligibility: knowledgedomain.EvidenceIneligible,
				}}
				return ports
			},
			wantPath:   []string{ragPlanNode, ragRetrievalNode, ragEvidenceNode, ragEvidenceRefusalNode},
			wantReason: agentdomain.RefusalUnapprovedEvidenceOnly,
		},
		{
			name: "refusal after generation",
			ports: func() *ragSchedulerPorts {
				ports := approvedRAGSchedulerPorts()
				refusal := ragSchedulerRefusal(agentdomain.RefusalValidationExhausted)
				ports.generation = agentapplication.RAGGenerationResult{
					Refusal: &refusal,
					Generation: agentapplication.StructuredRunResult{
						Phase:   agentdomain.ModelCallReduced,
						Runtime: agentapplication.FrozenRuntimeRefs{Schema: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2}},
					},
				}
				return ports
			},
			wantPath:   []string{ragPlanNode, ragRetrievalNode, ragEvidenceNode, ragGenerationNode, ragGenerationRefusalNode},
			wantReason: agentdomain.RefusalValidationExhausted,
		},
		{
			name: "refusal after publication gate",
			ports: func() *ragSchedulerPorts {
				ports := approvedRAGSchedulerPorts()
				refusal := ragSchedulerRefusal(agentdomain.RefusalEvidenceInsufficient)
				ports.publication = agentapplication.AnswerPublicationResult{Refusal: &refusal}
				return ports
			},
			wantPath: []string{
				ragPlanNode, ragRetrievalNode, ragEvidenceNode, ragGenerationNode,
				ragPublicationNode, ragPublicationRefusalNode,
			},
			wantReason: agentdomain.RefusalEvidenceInsufficient,
		},
		{
			name:  "answer after publication gate",
			ports: approvedRAGSchedulerPorts,
			wantPath: []string{
				ragPlanNode, ragRetrievalNode, ragEvidenceNode, ragGenerationNode,
				ragPublicationNode, ragAnswerTerminalNode,
			},
			answer: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			einoPorts := test.ports()
			var path []string
			scheduler, err := newRAGExecutionScheduler(context.Background(), func(node string) {
				path = append(path, node)
			})
			if err != nil {
				t.Fatal(err)
			}
			einoExecutor := newRAGSchedulerExecutor(t, einoPorts, scheduler)
			einoResult, einoErr := einoExecutor.Execute(context.Background(), ragSchedulerRequest())
			if einoErr != nil {
				t.Fatalf("Eino error=%v", einoErr)
			}
			if !reflect.DeepEqual(path, test.wantPath) {
				t.Fatalf("graph path=%v want=%v", path, test.wantPath)
			}
			if test.clarification && (einoResult.Clarification == nil || einoResult.Refusal != nil || einoResult.Answer != nil) {
				t.Fatalf("proposal=%#v", einoResult)
			}
			if test.wantReason != "" && (einoResult.Refusal == nil || einoResult.Refusal.Payload.ReasonCode != test.wantReason) {
				t.Fatalf("proposal=%#v", einoResult)
			}
			if test.answer && (einoResult.Answer == nil || einoResult.Refusal != nil || einoResult.Clarification != nil) {
				t.Fatalf("proposal=%#v", einoResult)
			}
		})
	}
}

type ragSchedulerPorts struct {
	plan          agentdomain.RAGQueryPlanResult
	planErr       error
	search        agentapplication.ScopedRetrievalResult
	eligibility   []knowledgedomain.ProvenanceEligibility
	topics        []knowledgedomain.EvidenceTopicBinding
	generation    agentapplication.RAGGenerationResult
	publication   agentapplication.AnswerPublicationResult
	planCalls     int
	searchCalls   int
	evidenceCalls int
	topicCalls    int
	runCalls      int
	publishCalls  int
}

func (ports *ragSchedulerPorts) Plan(context.Context, agentapplication.QueryPlanRequest) (agentapplication.QueryPlanRunResult, error) {
	ports.planCalls++
	if ports.planErr != nil {
		return agentapplication.QueryPlanRunResult{}, ports.planErr
	}
	return agentapplication.QueryPlanRunResult{Plan: ports.plan}, nil
}

func (ports *ragSchedulerPorts) Search(context.Context, retrievaldomain.SearchRequest) (agentapplication.ScopedRetrievalResult, error) {
	ports.searchCalls++
	return ports.search, nil
}

func (ports *ragSchedulerPorts) CheckEvidenceEligibility(context.Context, knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	ports.evidenceCalls++
	return ports.eligibility, nil
}

func (ports *ragSchedulerPorts) ResolveRAGTopics(context.Context, foundation.ID, []knowledgedomain.ProvenanceRef) ([]knowledgedomain.EvidenceTopicBinding, error) {
	ports.topicCalls++
	return ports.topics, nil
}

func (ports *ragSchedulerPorts) Generate(context.Context, agentapplication.RAGGenerationRequest) (agentapplication.RAGGenerationResult, error) {
	ports.runCalls++
	return ports.generation, nil
}

func (ports *ragSchedulerPorts) Publish(context.Context, agentapplication.AnswerPublicationRequest) (agentapplication.AnswerPublicationResult, error) {
	ports.publishCalls++
	return ports.publication, nil
}

func (*ragSchedulerPorts) RecordRAGProgress(context.Context, agentapplication.RAGProgressUpdate) error {
	return nil
}

func newRAGSchedulerExecutor(t *testing.T, ports *ragSchedulerPorts, scheduler agentapplication.RAGExecutionScheduler) *agentapplication.RAGExecutor {
	t.Helper()
	executor, err := agentapplication.NewRAGExecutorWithGenerationScheduler(ports, ports, ports, ports, ports, ports, ports, scheduler)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func approvedRAGSchedulerPorts() *ragSchedulerPorts {
	citation := agentdomain.Citation{
		ID: "cite-1", WorkspaceID: ragSchedulerID(1), IndexVersionID: ragSchedulerID(3),
		ChunkID: ragSchedulerID(4), SourceVersionID: ragSchedulerID(5), SourceSpanID: ragSchedulerID(6),
	}
	provenance := ragSchedulerProvenance(citation)
	topicID := ragSchedulerID(7)
	answer := agentdomain.RAGAnswerResultV2{
		ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2, ModelRunRef: ragSchedulerID(2),
		Payload: agentdomain.RAGAnswerPayloadV2{
			RAGAnswerPayload: agentdomain.RAGAnswerPayload{
				Conclusion: "evidence-bound answer",
				Assertions: []agentdomain.Assertion{{ID: "assertion-1", Text: "supported fact", Kind: agentdomain.AssertionFactual, CitationIDs: []string{citation.ID}}},
				Citations:  []agentdomain.Citation{citation}, ConflictPositions: []agentdomain.ConflictPosition{},
			},
			RelatedTopics:     []agentdomain.RelatedTopic{{TopicID: topicID, Name: "Deployment", CitationIDs: []string{citation.ID}}},
			FollowUpQuestions: []string{"Which deployment stage is next?"},
		},
	}
	base := agentdomain.RAGAnswerResult{
		ResultType: answer.ResultType, SchemaID: answer.SchemaID, SchemaVersion: agentdomain.OutputSchemaVersionV1,
		ModelRunRef: answer.ModelRunRef, Payload: answer.Payload.RAGAnswerPayload,
	}
	review := agentdomain.FaithfulnessReviewResult{}
	return &ragSchedulerPorts{
		plan: agentdomain.RAGQueryPlanResult{
			ResultType: agentdomain.ResultTypeRAGQueryPlan, SchemaID: agentdomain.RAGQueryPlanSchemaID,
			SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: ragSchedulerID(2),
			Payload: agentdomain.RAGQueryPlanPayload{Intent: "answer", Rewrites: []string{"approved evidence"}, SuggestedScopes: []string{}},
		},
		search: agentapplication.ScopedRetrievalResult{
			SearchResult: retrievaldomain.SearchResult{
				WorkspaceID: ragSchedulerID(1), RequestedMode: retrievaldomain.SearchModeHybrid,
				EffectiveMode: retrievaldomain.SearchModeHybrid, IndexVersionID: ragSchedulerID(3),
				Degradations: []retrievaldomain.SearchDegradation{},
			},
			RetrievalBatch: agentapplication.RetrievalBatch{
				WorkspaceID: ragSchedulerID(1), IndexVersionID: ragSchedulerID(3),
				Items: []agentapplication.RetrievedEvidence{{Citation: citation, SearchExcerpt: "approved deployment evidence", CapturedAt: time.Unix(1, 0).UTC()}},
			},
		},
		eligibility: []knowledgedomain.ProvenanceEligibility{{
			Provenance: provenance, Eligibility: knowledgedomain.EvidenceEligible,
			Bindings: []knowledgedomain.EvidenceEligibilityBinding{{
				OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: ragSchedulerID(8), EvidenceID: ragSchedulerID(9),
				ClaimStatus: knowledgedomain.ClaimStatusConfirmed, SupportType: knowledgedomain.ClaimSupportSupports,
			}},
		}},
		topics: []knowledgedomain.EvidenceTopicBinding{{Provenance: provenance, TopicID: topicID, TopicName: "Deployment"}},
		generation: agentapplication.RAGGenerationResult{
			Answer: &answer,
			Generation: agentapplication.StructuredRunResult{
				Phase:   agentdomain.ModelCallInitial,
				Runtime: agentapplication.FrozenRuntimeRefs{Schema: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2}},
			},
		},
		publication: agentapplication.AnswerPublicationResult{Answer: &base, Review: &review},
	}
}

func ragSchedulerProvenance(citation agentdomain.Citation) knowledgedomain.ProvenanceRef {
	return knowledgedomain.ProvenanceRef{
		WorkspaceID: citation.WorkspaceID, SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
	}
}

func ragSchedulerRefusal(reason agentdomain.RefusalReasonCode) agentdomain.RefusalResult {
	return agentdomain.RefusalResult{
		ResultType: agentdomain.ResultTypeRefusal, SchemaID: agentdomain.RefusalSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: ragSchedulerID(2),
		Payload: agentdomain.RefusalPayload{
			ReasonCode: reason, Summary: "approved evidence could not support an answer",
			RetrievalScope:      "current workspace approved knowledge",
			MissingRequirements: []string{"eligible supporting evidence"}, SuggestedActions: []string{"add supporting knowledge"},
		},
	}
}

func ragSchedulerRequest() agentapplication.RAGExecutionRequest {
	return agentapplication.RAGExecutionRequest{
		WorkspaceID: ragSchedulerID(1), ModelRunRef: ragSchedulerID(2),
		PlanInput: []byte(`{"question":"scope"}`), AnswerInput: []byte(`{"question":"scope"}`),
		SearchMode:      retrievaldomain.SearchModeHybrid,
		AnswerSchemaRef: agentdomain.SchemaRef{ID: agentdomain.RAGAnswerMetadataSchemaID, Version: agentdomain.OutputSchemaVersionV2},
		AnswerReducedSchemaRef: agentdomain.SchemaRef{
			ID: agentdomain.RAGAnswerMetadataRefusalSchemaID, Version: agentdomain.OutputSchemaVersionV2,
		},
	}
}

func ragSchedulerID(value int) foundation.ID {
	return foundation.ID("91000000-0000-4000-8000-" + leftPadRAGScheduler(value))
}

func leftPadRAGScheduler(value int) string {
	const digits = "000000000000"
	encoded := []byte(digits)
	for index := len(encoded) - 1; value > 0; index-- {
		encoded[index] = byte('0' + value%10)
		value /= 10
	}
	return string(encoded)
}
