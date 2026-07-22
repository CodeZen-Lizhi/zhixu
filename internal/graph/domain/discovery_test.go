package domain

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestEvaluateSemanticLinkDiscoveryExecutesAllSignalsAndKeepsStableOutput(t *testing.T) {
	workspaceID := discoveryTestID(900)
	first := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 1, "Go Concurrency", []string{"goroutine", "channel"}, 10)
	second := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 2, "go concurrency", []string{"channel", "scheduler"}, 10)
	second.Labels = []string{"Go Concurrency", "channels"}
	semanticHash := strings.Repeat("a", 64)
	ragHash := strings.Repeat("b", 64)
	request := SemanticLinkDiscoveryRequest{
		WorkspaceID: workspaceID,
		Pairs:       []SemanticLinkDiscoveryPair{{Source: first, Target: second}},
		Limit:       100, PerSourceLimit: 100, RuleGeneration: discoveryRuleGeneration(),
		Semantic: SemanticLinkDiscoveryExternalResult{
			Method: SemanticLinkDiscoveryMethodClaimSemanticSimilarity, Supported: true,
			Generation: discoveryModelGeneration(), Hits: []SemanticLinkDiscoveryExternalHit{{
				Source: first.Endpoint.Ref, Target: second.Endpoint.Ref, Score: 0.91, EvidenceHashes: []string{semanticHash},
			}},
		},
		RAG: SemanticLinkDiscoveryExternalResult{
			Method: SemanticLinkDiscoveryMethodRAGCoRetrieval, Supported: true,
			Generation: discoveryModelGeneration(), Hits: []SemanticLinkDiscoveryExternalHit{{
				Source: first.Endpoint.Ref, Target: second.Endpoint.Ref, Score: 0.73, EvidenceHashes: []string{ragHash},
			}},
		},
	}
	result, err := EvaluateSemanticLinkDiscovery(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || len(result.Signals) != 6 || result.ExcludedPairs != 0 || result.Truncated {
		t.Fatalf("unexpected discovery result: %+v", result)
	}
	item := result.Items[0]
	wantMethods := []SemanticLinkDiscoveryMethod{
		SemanticLinkDiscoveryMethodClaimSemanticSimilarity,
		SemanticLinkDiscoveryMethodCommonTopic,
		SemanticLinkDiscoveryMethodRAGCoRetrieval,
		SemanticLinkDiscoveryMethodSharedSource,
		SemanticLinkDiscoveryMethodTermMatch,
		SemanticLinkDiscoveryMethodTitleAlias,
	}
	if !reflect.DeepEqual(item.Methods, wantMethods) {
		t.Fatalf("methods = %#v, want %#v", item.Methods, wantMethods)
	}
	if !containsString(item.EvidenceHashes, semanticHash) || !containsString(item.EvidenceHashes, ragHash) || len(item.EvidenceHashes) < 6 {
		t.Fatalf("evidence hashes = %#v", item.EvidenceHashes)
	}
	for _, signal := range result.Signals {
		if signal.Status != SemanticLinkDiscoverySignalStatusExecuted || signal.HitCount != 1 {
			t.Fatalf("signal = %+v", signal)
		}
	}

	// Reordering the pair input must not change the result.
	replayed := request
	replayed.Pairs = []SemanticLinkDiscoveryPair{{Source: second, Target: first}}
	replayedResult, err := EvaluateSemanticLinkDiscovery(replayed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Items, replayedResult.Items) {
		t.Fatalf("reordered pair changed result: %#v != %#v", result.Items, replayedResult.Items)
	}
}

func TestEvaluateSemanticLinkDiscoveryMarksOptionalSignalsUnsupported(t *testing.T) {
	workspaceID := discoveryTestID(901)
	first := discoveryNode(workspaceID, knowledge.NodeTypeTopic, 1, "Graph", nil, 11)
	second := discoveryNode(workspaceID, knowledge.NodeTypeTopic, 2, "Storage", nil, 12)
	request := SemanticLinkDiscoveryRequest{
		WorkspaceID: workspaceID,
		Pairs:       []SemanticLinkDiscoveryPair{{Source: first, Target: second}},
		Limit:       10, PerSourceLimit: 10, RuleGeneration: discoveryRuleGeneration(),
		Semantic: SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodClaimSemanticSimilarity, UnsupportedReason: "PROVIDER_NOT_CONFIGURED"},
		RAG:      SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodRAGCoRetrieval, UnsupportedReason: "RAG_INDEX_NOT_READY"},
	}
	result, err := EvaluateSemanticLinkDiscovery(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 || len(result.Signals) != 6 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, signal := range result.Signals[4:] {
		if signal.Status != SemanticLinkDiscoverySignalStatusUnsupported || signal.Reason == "" || signal.HitCount != 0 {
			t.Fatalf("unsupported signal = %+v", signal)
		}
	}
}

func TestEvaluateSemanticLinkDiscoveryIsBoundedAndDeterministicallyRanked(t *testing.T) {
	workspaceID := discoveryTestID(902)
	anchor := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 1, "Anchor", []string{"anchor"}, 20)
	tweak := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 2, "Anchor", nil, 21)
	strong := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 3, "Anchor", []string{"anchor"}, 22)
	base := SemanticLinkDiscoveryRequest{
		WorkspaceID: workspaceID, Limit: 1, PerSourceLimit: 100, RuleGeneration: discoveryRuleGeneration(),
		Semantic: SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodClaimSemanticSimilarity, UnsupportedReason: "NOT_CONFIGURED"},
		RAG:      SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodRAGCoRetrieval, UnsupportedReason: "NOT_CONFIGURED"},
	}
	base.Pairs = []SemanticLinkDiscoveryPair{{Source: anchor, Target: tweak}, {Source: anchor, Target: strong}}
	first, err := EvaluateSemanticLinkDiscovery(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Pairs = []SemanticLinkDiscoveryPair{{Source: anchor, Target: strong}, {Source: anchor, Target: tweak}}
	second, err := EvaluateSemanticLinkDiscovery(base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Items, second.Items) || len(first.Items) != 1 || first.Items[0].Target != strong.Endpoint.Ref || !first.Truncated {
		t.Fatalf("ranking/limit is not deterministic: first=%+v second=%+v", first, second)
	}
}

func TestEvaluateSemanticLinkDiscoveryExcludesExistingPairAndRejectsDuplicates(t *testing.T) {
	workspaceID := discoveryTestID(903)
	first := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 1, "same", nil, 30)
	second := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 2, "same", nil, 31)
	request := SemanticLinkDiscoveryRequest{
		WorkspaceID: workspaceID, Pairs: []SemanticLinkDiscoveryPair{{Source: first, Target: second}},
		Exclusions: []SemanticLinkDiscoveryExclusion{{Source: first.Endpoint.Ref, Target: second.Endpoint.Ref, Reason: "CONFIRMED_RELATION"}},
		Limit:      10, PerSourceLimit: 10, RuleGeneration: discoveryRuleGeneration(),
		Semantic: SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodClaimSemanticSimilarity, UnsupportedReason: "NOT_CONFIGURED"},
		RAG:      SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodRAGCoRetrieval, UnsupportedReason: "NOT_CONFIGURED"},
	}
	result, err := EvaluateSemanticLinkDiscovery(request)
	if err != nil || result.ExcludedPairs != 1 || len(result.Items) != 0 {
		t.Fatalf("excluded result=%+v err=%v", result, err)
	}
	request.Exclusions = nil
	request.Pairs = append(request.Pairs, SemanticLinkDiscoveryPair{Source: second, Target: first})
	if _, err := EvaluateSemanticLinkDiscovery(request); err == nil {
		t.Fatal("expected duplicate pair error")
	}
}

func TestEvaluateSemanticLinkDiscoveryRejectsUnboundOrWrongSemanticHits(t *testing.T) {
	workspaceID := discoveryTestID(904)
	first := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 1, "first", nil, 40)
	second := discoveryNode(workspaceID, knowledge.NodeTypeClaim, 2, "second", nil, 41)
	request := SemanticLinkDiscoveryRequest{
		WorkspaceID: workspaceID, Pairs: []SemanticLinkDiscoveryPair{{Source: first, Target: second}},
		Limit: 10, PerSourceLimit: 10, RuleGeneration: discoveryRuleGeneration(),
		Semantic: SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodClaimSemanticSimilarity, Supported: true, Generation: discoveryModelGeneration(), Hits: []SemanticLinkDiscoveryExternalHit{{Source: first.Endpoint.Ref, Target: discoveryTestRef(99), Score: .5, EvidenceHashes: []string{strings.Repeat("c", 64)}}}},
		RAG:      SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodRAGCoRetrieval, UnsupportedReason: "NOT_CONFIGURED"},
	}
	if _, err := EvaluateSemanticLinkDiscovery(request); err == nil {
		t.Fatal("expected unbound external hit error")
	}
	request.Semantic.Hits[0].Target = second.Endpoint.Ref
	request.Semantic.Hits[0].Source = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: first.Endpoint.Ref.ID}
	if _, err := EvaluateSemanticLinkDiscovery(request); err == nil {
		t.Fatal("expected wrong semantic endpoint type error")
	}
}

func TestEvaluateSemanticLinkDiscoveryRejectsRuleGenerationWithUnusedIndex(t *testing.T) {
	workspaceID := discoveryTestID(906)
	request := SemanticLinkDiscoveryRequest{
		WorkspaceID: workspaceID,
		Pairs: []SemanticLinkDiscoveryPair{{
			Source: discoveryNode(workspaceID, knowledge.NodeTypeClaim, 1, "one", nil, 50),
			Target: discoveryNode(workspaceID, knowledge.NodeTypeClaim, 2, "two", nil, 51),
		}},
		Limit: 10, PerSourceLimit: 10, RuleGeneration: discoveryRuleGeneration(),
		Semantic: SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodClaimSemanticSimilarity, UnsupportedReason: "NOT_CONFIGURED"},
		RAG:      SemanticLinkDiscoveryExternalResult{Method: SemanticLinkDiscoveryMethodRAGCoRetrieval, UnsupportedReason: "NOT_CONFIGURED"},
	}
	indexID := discoveryTestID(999)
	request.RuleGeneration.IndexVersionID = &indexID
	if _, err := EvaluateSemanticLinkDiscovery(request); err == nil {
		t.Fatal("expected unused index version rejection")
	}
}

func TestValidateSemanticLinkScanScopeAndTransition(t *testing.T) {
	scope := SemanticLinkScanScope{Type: SemanticLinkScanScopeTopic, Ref: string(discoveryTestID(905)), Version: 2, SchemaVersion: "topic-scan/v1"}
	if err := ValidateSemanticLinkScanScope(scope); err != nil {
		t.Fatal(err)
	}
	generation := SemanticLinkScanGeneration{Rule: discoveryRuleGeneration(), WorkflowVersion: "workflow/v1"}
	if _, err := ComputeSemanticLinkScanFingerprint(discoveryTestID(900), scope, generation); err != nil {
		t.Fatal(err)
	}
	changed := generation
	semantic := discoveryModelGeneration()
	changed.Semantic = &semantic
	first, err := ComputeSemanticLinkScanFingerprint(discoveryTestID(900), scope, generation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeSemanticLinkScanFingerprint(discoveryTestID(900), scope, changed)
	if err != nil || first == second {
		t.Fatalf("scan fingerprint did not bind external generation: %q %q err=%v", first, second, err)
	}
	for _, unsupported := range []SemanticLinkScanScopeType{SemanticLinkScanScopeDirectory} {
		if err := ValidateSemanticLinkScanScope(SemanticLinkScanScope{Type: unsupported, Ref: string(discoveryTestID(905)), Version: 1, SchemaVersion: "v1"}); err == nil {
			t.Fatalf("expected unsupported scope error for %s", unsupported)
		}
	}
	smartScope := SemanticLinkScanScope{Type: SemanticLinkScanScopeSmartCollection, Ref: string(discoveryTestID(906)), Version: 2, SchemaVersion: "semantic-link-smart-collection-scope/v1", QueryHash: strings.Repeat("a", 64), ReadModelRevision: strings.Repeat("b", 64)}
	if err := ValidateSemanticLinkScanScope(smartScope); err != nil {
		t.Fatalf("smart collection scope should be supported: %v", err)
	}
	for _, transition := range [][2]SemanticLinkScanStatus{{SemanticLinkScanStatusPending, SemanticLinkScanStatusRunning}, {SemanticLinkScanStatusRunning, SemanticLinkScanStatusSucceeded}, {SemanticLinkScanStatusRunning, SemanticLinkScanStatusFailed}, {SemanticLinkScanStatusRunning, SemanticLinkScanStatusCancelled}} {
		if err := ValidateSemanticLinkScanTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("transition %s -> %s: %v", transition[0], transition[1], err)
		}
	}
	if err := ValidateSemanticLinkScanTransition(SemanticLinkScanStatusSucceeded, SemanticLinkScanStatusRunning); err == nil {
		t.Fatal("expected terminal transition rejection")
	}
}

func TestValidateSemanticLinkScanRejectsFailureWithoutSummary(t *testing.T) {
	now := time.Date(2026, 7, 21, 1, 0, 0, 0, time.UTC)
	scan := SemanticLinkScan{
		ID: discoveryTestID(1), WorkspaceID: discoveryTestID(900), WorkflowRunID: discoveryTestID(2),
		Scope:       SemanticLinkScanScope{Type: SemanticLinkScanScopeTopic, Ref: string(discoveryTestID(3)), Version: 1, SchemaVersion: "v1"},
		Fingerprint: strings.Repeat("a", 64), IdempotencyKey: "scan-1", RequestHash: strings.Repeat("b", 64),
		Status: SemanticLinkScanStatusFailed, Version: 2, CreatedAt: now, UpdatedAt: now, CompletedAt: ptrDiscoveryTime(now.Add(time.Minute)),
	}
	if err := ValidateSemanticLinkScan(scan); err == nil {
		t.Fatal("expected missing failure summary rejection")
	}
	scan.LastError = &SemanticLinkScanError{Stage: "discover", Code: "PROVIDER_TIMEOUT", Retryable: true}
	if err := ValidateSemanticLinkScan(scan); err != nil {
		t.Fatal(err)
	}
}

func TestClassifySemanticLinkRuleDiscoveryHitUsesProductionRulePolicy(t *testing.T) {
	tests := []struct {
		name         string
		methods      []SemanticLinkDiscoveryMethod
		relationType knowledge.RelationType
		confidence   float64
		eligible     bool
	}{
		{
			name:         "normalized title or alias is a duplicate",
			methods:      []SemanticLinkDiscoveryMethod{SemanticLinkDiscoveryMethodTitleAlias},
			relationType: knowledge.RelationDuplicates,
			confidence:   0.95,
			eligible:     true,
		},
		{
			name:         "two deterministic signals are complementary",
			methods:      []SemanticLinkDiscoveryMethod{SemanticLinkDiscoveryMethodTermMatch, SemanticLinkDiscoveryMethodCommonTopic},
			relationType: knowledge.RelationComplements,
			confidence:   0.65,
			eligible:     true,
		},
		{
			name:     "one deterministic signal plus an external signal remains below the rule threshold",
			methods:  []SemanticLinkDiscoveryMethod{SemanticLinkDiscoveryMethodTermMatch, SemanticLinkDiscoveryMethodClaimSemanticSimilarity},
			eligible: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification := ClassifySemanticLinkRuleDiscoveryHit(SemanticLinkDiscoveryHit{Methods: test.methods})
			if classification.Eligible != test.eligible || classification.RelationType != test.relationType || classification.Confidence != test.confidence {
				t.Fatalf("classification = %+v", classification)
			}
		})
	}
}

func discoveryNode(workspaceID foundation.ID, nodeType knowledge.NodeType, number int, label string, terms []string, topicNumber int) SemanticLinkDiscoveryNode {
	return SemanticLinkDiscoveryNode{
		WorkspaceID: workspaceID,
		Endpoint:    SemanticLinkCandidateEndpoint{Ref: discoveryRef(nodeType, number), Version: 1, Summary: fmt.Sprintf("summary %d", number), Excerpt: fmt.Sprintf("excerpt %d", number)},
		Lifecycle:   knowledge.NodeLifecycleConfirmed,
		Labels:      []string{label}, Terms: append([]string(nil), terms...),
		TopicRefs:        []knowledge.NodeRef{{Type: knowledge.NodeTypeTopic, ID: discoveryTestID(topicNumber)}},
		SourceVersionIDs: []foundation.ID{discoveryTestID(topicNumber + 100)},
	}
}

func discoveryRuleGeneration() SemanticLinkCandidateGeneration {
	ruleID := discoveryTestID(700)
	return SemanticLinkCandidateGeneration{RuleID: &ruleID, RuleVersion: "semantic-rules/v1"}
}

func discoveryModelGeneration() SemanticLinkCandidateGeneration {
	indexID, embeddingID := discoveryTestID(710), discoveryTestID(711)
	return SemanticLinkCandidateGeneration{IndexVersionID: &indexID, EmbeddingVersionID: &embeddingID, ModelVersion: "model/v1", ModelProfileVersion: "profile/v1", PromptVersion: "prompt/v1", SchemaVersion: "schema/v1"}
}

func discoveryRef(nodeType knowledge.NodeType, number int) knowledge.NodeRef {
	return knowledge.NodeRef{Type: nodeType, ID: discoveryTestID(number)}
}

func discoveryTestRef(number int) knowledge.NodeRef {
	return discoveryRef(knowledge.NodeTypeClaim, number)
}

func discoveryTestID(number int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", number))
}

func ptrDiscoveryTime(value time.Time) *time.Time { return &value }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
