package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestValidateGraphNodeRequiresDiscriminatedTopicOrClaim(t *testing.T) {
	now := time.Now().UTC()
	topic := topicNode(1, 9, now)
	claim := claimNode(2, 9, now)
	for _, node := range []GraphNode{{Topic: &topic}, {Claim: &claim}} {
		if err := ValidateGraphNode(node); err != nil {
			t.Fatalf("ValidateGraphNode() error = %v", err)
		}
	}
	for _, node := range []GraphNode{{}, {Topic: &topic, Claim: &claim}} {
		assertCode(t, ValidateGraphNode(node), ErrorCodeProjectionInconsistent)
	}
}

func TestValidateRequestsEnforceFiltersLimitsAndDirection(t *testing.T) {
	confidence := 0.5
	request := NeighborhoodRequest{WorkspaceID: id(9), Center: ref(knowledge.NodeTypeTopic, 1), Depth: 3, Limit: MaxLimit, Direction: TraversalBoth, MaxNodes: MaxNodes, MaxEdges: MaxEdges, Filter: GraphFilter{NodeTypes: []knowledge.NodeType{knowledge.NodeTypeTopic, knowledge.NodeTypeClaim}, RelationTypes: []knowledge.RelationType{knowledge.RelationBelongsTo}, RelationStatuses: []knowledge.RelationStatus{knowledge.RelationStatusConfirmed, knowledge.RelationStatusStale}, ClaimStatuses: []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed, knowledge.ClaimStatusDisputed}, ClaimMinConfidence: &confidence}}
	if err := ValidateNeighborhoodRequest(request); err != nil {
		t.Fatalf("ValidateNeighborhoodRequest() error = %v", err)
	}
	request.Depth = 4
	assertCode(t, ValidateNeighborhoodRequest(request), ErrorCodeRequestInvalid)
	request.Depth, request.Direction = 1, TraversalDirection("SIDEWAYS")
	assertCode(t, ValidateNeighborhoodRequest(request), ErrorCodeRequestInvalid)
	request.Direction, request.Filter.RelationTypes = TraversalBoth, []knowledge.RelationType{knowledge.RelationBelongsTo, knowledge.RelationBelongsTo}
	assertCode(t, ValidateNeighborhoodRequest(request), ErrorCodeRequestInvalid)

	path := PathRequest{WorkspaceID: id(9), From: ref(knowledge.NodeTypeClaim, 1), To: ref(knowledge.NodeTypeTopic, 2), Direction: TraversalInbound, RelationTypes: []knowledge.RelationType{knowledge.RelationBelongsTo}, MaxDepth: MaxPathDepth, MaxVisited: MaxNodes}
	if err := ValidatePathRequest(path); err != nil {
		t.Fatalf("ValidatePathRequest() error = %v", err)
	}
	path.MaxDepth = MaxPathDepth + 1
	assertCode(t, ValidatePathRequest(path), ErrorCodeRequestInvalid)
}

func TestValidateNeighborhoodEnforcesClosureOrderingAndUniqueness(t *testing.T) {
	now := time.Now().UTC()
	request := NeighborhoodRequest{WorkspaceID: id(9), Center: ref(knowledge.NodeTypeClaim, 1), Depth: 1, Limit: 25, Direction: TraversalBoth, MaxNodes: 10, MaxEdges: 10}
	claim, topic := claimNode(1, 9, now), topicNode(2, 9, now)
	result := Neighborhood{WorkspaceID: id(9), Center: request.Center, Nodes: []GraphNode{{Claim: &claim}, {Topic: &topic}}, Edges: []GraphEdge{edge(3, 9, claim.Ref, topic.Ref, EdgeTraversalForward)}, LayerCounts: []int{1}, CompletedDepth: 1, Meta: completeMeta()}
	if err := ValidateNeighborhood(request, result); err != nil {
		t.Fatalf("ValidateNeighborhood() error = %v", err)
	}

	broken := result
	broken.Nodes = broken.Nodes[:1]
	assertCode(t, ValidateNeighborhood(request, broken), ErrorCodeProjectionInconsistent)
	broken = result
	broken.Nodes = []GraphNode{result.Nodes[1], result.Nodes[0]}
	assertCode(t, ValidateNeighborhood(request, broken), ErrorCodeProjectionInconsistent)
	broken = result
	broken.Edges = append(broken.Edges, broken.Edges[0])
	assertCode(t, ValidateNeighborhood(request, broken), ErrorCodeProjectionInconsistent)

	boundary := result
	boundary.Nodes = boundary.Nodes[:1]
	boundary.BoundaryNodes = []knowledge.NodeRef{topic.Ref}
	boundary.LayerCounts = []int{0}
	if err := ValidateNeighborhood(request, boundary); err != nil {
		t.Fatalf("boundary neighborhood error = %v", err)
	}
}

func TestValidatePathResultEnforcesContinuityAndNoPartialNotFound(t *testing.T) {
	now := time.Now().UTC()
	a, b, c := claimNode(1, 9, now), claimNode(2, 9, now), topicNode(3, 9, now)
	request := PathRequest{WorkspaceID: id(9), From: a.Ref, To: c.Ref, Direction: TraversalBoth, MaxDepth: 6, MaxVisited: 20}
	result := PathResult{WorkspaceID: id(9), From: a.Ref, To: c.Ref, Status: PathFound, Nodes: []GraphNode{{Claim: &a}, {Claim: &b}, {Topic: &c}}, Edges: []GraphEdge{edge(11, 9, a.Ref, b.Ref, EdgeTraversalForward), edge(12, 9, c.Ref, b.Ref, EdgeTraversalReverse)}, HopCount: 2, ExploredNodes: 3}
	if err := ValidatePathResult(request, result); err != nil {
		t.Fatalf("ValidatePathResult() error = %v", err)
	}

	broken := result
	broken.Edges[1].Traversal = EdgeTraversalForward
	assertCode(t, ValidatePathResult(request, broken), ErrorCodeProjectionInconsistent)
	notFound := PathResult{WorkspaceID: id(9), From: a.Ref, To: c.Ref, Status: PathNotFound, ExploredNodes: 3, CommonTopicSuggestions: []TopicNode{c}}
	if err := ValidatePathResult(request, notFound); err != nil {
		t.Fatalf("not found error = %v", err)
	}
	notFound.Nodes = []GraphNode{{Claim: &a}}
	assertCode(t, ValidatePathResult(request, notFound), ErrorCodeProjectionInconsistent)
}

func TestValidateGlobalAndSearchContracts(t *testing.T) {
	now := time.Now().UTC()
	request := GlobalRequest{WorkspaceID: id(9), Limit: 2}
	a, b := topicNode(1, 9, now), topicNode(2, 9, now.Add(-time.Minute))
	page := GlobalPage{WorkspaceID: id(9), Clusters: []GlobalCluster{{Topic: a, DirectClaimCount: 2, IncidentRelationCount: 1, ClusterScore: 3, UpdatedAt: now}, {Topic: b, DirectClaimCount: 1, IncidentRelationCount: 1, ClusterScore: 2, UpdatedAt: now.Add(-time.Minute)}}, Meta: completeMeta()}
	if err := ValidateGlobalPage(request, page); err != nil {
		t.Fatalf("ValidateGlobalPage() error = %v", err)
	}
	page.Clusters[1].ClusterScore = 4
	assertCode(t, ValidateGlobalPage(request, page), ErrorCodeProjectionInconsistent)

	search := NodeSearchRequest{WorkspaceID: id(9), Query: "go", Limit: 20}
	result := NodeSearchResult{WorkspaceID: id(9), Matches: []NodeSearchMatch{{Kind: NodeSearchExact, Node: GraphNode{Topic: &a}}, {Kind: NodeSearchPrefix, Node: GraphNode{Topic: &b}}}}
	if err := ValidateNodeSearchResult(search, result); err != nil {
		t.Fatalf("ValidateNodeSearchResult() error = %v", err)
	}
	search.Query = " x "
	assertCode(t, ValidateNodeSearchRequest(search), ErrorCodeRequestInvalid)
}

func TestValidateEdgeRequiresCompatibleTypeAndEvidenceSummary(t *testing.T) {
	now := time.Now().UTC()
	claim, topic := claimNode(1, 9, now), topicNode(2, 9, now)
	request := NeighborhoodRequest{WorkspaceID: id(9), Center: claim.Ref, Depth: 1, Limit: 25, Direction: TraversalBoth, MaxNodes: 10, MaxEdges: 10}
	result := Neighborhood{WorkspaceID: id(9), Center: claim.Ref, Nodes: []GraphNode{{Claim: &claim}, {Topic: &topic}}, Edges: []GraphEdge{edge(3, 9, claim.Ref, topic.Ref, EdgeTraversalForward)}, LayerCounts: []int{1}, CompletedDepth: 1, Meta: completeMeta()}
	result.Edges[0].Type = knowledge.RelationSupports
	assertCode(t, ValidateNeighborhood(request, result), ErrorCodeProjectionInconsistent)
	result.Edges[0].Type, result.Edges[0].EvidenceCount, result.Edges[0].EvidenceFingerprint = knowledge.RelationBelongsTo, 1, "bad"
	assertCode(t, ValidateNeighborhood(request, result), ErrorCodeProjectionInconsistent)
}

func TestSymmetricEdgesRemainTraversableInEitherDirection(t *testing.T) {
	now := time.Now().UTC()
	a, b := claimNode(1, 9, now), claimNode(2, 9, now)
	edge := edge(3, 9, a.Ref, b.Ref, EdgeTraversalReverse)
	edge.Type = knowledge.RelationDuplicates
	request := NeighborhoodRequest{WorkspaceID: id(9), Center: b.Ref, Depth: 1, Limit: 25, Direction: TraversalOutbound, MaxNodes: 10, MaxEdges: 10}
	result := Neighborhood{WorkspaceID: id(9), Center: b.Ref, Nodes: []GraphNode{{Claim: &a}, {Claim: &b}}, Edges: []GraphEdge{edge}, LayerCounts: []int{1}, CompletedDepth: 1, Meta: PageMeta{Fingerprint: strings.Repeat("a", 64), Complete: true}}
	if err := ValidateNeighborhood(request, result); err != nil {
		t.Fatalf("symmetric reverse traversal rejected: %v", err)
	}
}

func TestValidateAdapterResultsFailClosedAgainstFiltersAndDirection(t *testing.T) {
	now := time.Now().UTC()
	confidence, threshold := 0.8, 0.7
	claim, topic := claimNode(1, 9, now), topicNode(2, 9, now)
	claim.Confidence = &confidence
	baseRequest := NeighborhoodRequest{WorkspaceID: id(9), Center: claim.Ref, Depth: 1, Limit: 25, Direction: TraversalOutbound, MaxNodes: 10, MaxEdges: 10, Filter: GraphFilter{NodeTypes: []knowledge.NodeType{knowledge.NodeTypeClaim, knowledge.NodeTypeTopic}, RelationTypes: []knowledge.RelationType{knowledge.RelationBelongsTo}, ClaimStatuses: []knowledge.ClaimStatus{knowledge.ClaimStatusConfirmed}, ClaimMinConfidence: &threshold, RelationMinConfidence: &threshold, UpdatedAfter: ptrTime(now.Add(-time.Minute))}}
	makeResult := func() Neighborhood {
		claimValue, topicValue := claim, topic
		edgeValue := edge(3, 9, claimValue.Ref, topicValue.Ref, EdgeTraversalForward)
		edgeValue.Confidence = &confidence
		return Neighborhood{WorkspaceID: id(9), Center: claimValue.Ref, Nodes: []GraphNode{{Claim: &claimValue}, {Topic: &topicValue}}, Edges: []GraphEdge{edgeValue}, LayerCounts: []int{1}, CompletedDepth: 1, Meta: completeMeta()}
	}
	if err := ValidateNeighborhood(baseRequest, makeResult()); err != nil {
		t.Fatalf("valid filtered neighborhood error = %v", err)
	}
	for name, mutate := range map[string]func(*Neighborhood){
		"relation type":       func(v *Neighborhood) { v.Edges[0].Type = knowledge.RelationImpacts },
		"status":              func(v *Neighborhood) { v.Edges[0].Status = knowledge.RelationStatusStale },
		"claim confidence":    func(v *Neighborhood) { low := 0.2; v.Nodes[0].Claim.Confidence = &low },
		"relation confidence": func(v *Neighborhood) { low := 0.2; v.Edges[0].Confidence = &low },
		"updated after":       func(v *Neighborhood) { v.Edges[0].UpdatedAt = now.Add(-time.Hour) },
		"direction":           func(v *Neighborhood) { v.Edges[0].Traversal = EdgeTraversalReverse },
	} {
		t.Run(name, func(t *testing.T) {
			value := makeResult()
			mutate(&value)
			assertCode(t, ValidateNeighborhood(baseRequest, value), ErrorCodeProjectionInconsistent)
		})
	}
	nodeTypeRequest := baseRequest
	nodeTypeRequest.Filter.NodeTypes = []knowledge.NodeType{knowledge.NodeTypeClaim}
	assertCode(t, ValidateNeighborhood(nodeTypeRequest, makeResult()), ErrorCodeProjectionInconsistent)

	pathRequest := PathRequest{WorkspaceID: id(9), From: claim.Ref, To: topic.Ref, Direction: TraversalOutbound, RelationTypes: []knowledge.RelationType{knowledge.RelationBelongsTo}, MaxDepth: 2, MaxVisited: 10}
	path := PathResult{WorkspaceID: id(9), From: claim.Ref, To: topic.Ref, Status: PathFound, Nodes: []GraphNode{{Claim: &claim}, {Topic: &topic}}, Edges: []GraphEdge{makeResult().Edges[0]}, HopCount: 1, ExploredNodes: 2}
	path.Edges[0].Traversal = EdgeTraversalReverse
	assertCode(t, ValidatePathResult(pathRequest, path), ErrorCodeProjectionInconsistent)
	path.Edges[0].Traversal, path.Edges[0].Status = EdgeTraversalForward, knowledge.RelationStatusStale
	assertCode(t, ValidatePathResult(pathRequest, path), ErrorCodeProjectionInconsistent)
}

func TestValidateDisplayMetaLayersAndSuggestions(t *testing.T) {
	now := time.Now().UTC()
	claim := claimNode(1, 9, now)
	claim.Statement = strings.Repeat("界", 171)
	assertCode(t, ValidateGraphNode(GraphNode{Claim: &claim}), ErrorCodeProjectionInconsistent)
	claim.Statement = string([]byte{0xff, 'x'})
	assertCode(t, ValidateGraphNode(GraphNode{Claim: &claim}), ErrorCodeProjectionInconsistent)

	request := GlobalRequest{WorkspaceID: id(9), Limit: 1}
	topic := topicNode(2, 9, now)
	page := GlobalPage{WorkspaceID: id(9), Clusters: []GlobalCluster{{Topic: topic, UpdatedAt: now}}, Meta: PageMeta{Complete: true}}
	assertCode(t, ValidateGlobalPage(request, page), ErrorCodeProjectionInconsistent)
	page.Meta = PageMeta{Fingerprint: strings.Repeat("a", 64), Complete: true, NextCursor: "unexpected"}
	assertCode(t, ValidateGlobalPage(request, page), ErrorCodeProjectionInconsistent)

	pathRequest := PathRequest{WorkspaceID: id(9), From: ref(knowledge.NodeTypeClaim, 1), To: ref(knowledge.NodeTypeClaim, 3), Direction: TraversalBoth, MaxDepth: 2, MaxVisited: 10}
	suggestions := make([]TopicNode, 6)
	for i := range suggestions {
		suggestions[i] = topicNode(i+10, 9, now)
	}
	result := PathResult{WorkspaceID: id(9), From: pathRequest.From, To: pathRequest.To, Status: PathNotFound, CommonTopicSuggestions: suggestions}
	assertCode(t, ValidatePathResult(pathRequest, result), ErrorCodeProjectionInconsistent)
	result.CommonTopicSuggestions = []TopicNode{topicNode(11, 9, now), topicNode(10, 9, now)}
	assertCode(t, ValidatePathResult(pathRequest, result), ErrorCodeProjectionInconsistent)
}

func topicNode(n, workspace int, now time.Time) TopicNode {
	return TopicNode{Ref: ref(knowledge.NodeTypeTopic, n), WorkspaceID: id(workspace), Name: fmt.Sprintf("topic-%d", n), Status: knowledge.TopicStatusActive, Version: 1, UpdatedAt: now}
}
func claimNode(n, workspace int, now time.Time) ClaimNode {
	applicability, err := knowledge.ParseApplicability([]byte(`{}`))
	if err != nil {
		panic(err)
	}
	return ClaimNode{Ref: ref(knowledge.NodeTypeClaim, n), WorkspaceID: id(workspace), Statement: fmt.Sprintf("claim-%d", n), Status: knowledge.ClaimStatusConfirmed, Applicability: applicability, Version: 1, UpdatedAt: now}
}
func edge(n, workspace int, source, target knowledge.NodeRef, traversal EdgeTraversal) GraphEdge {
	return GraphEdge{RelationID: id(n), WorkspaceID: id(workspace), Source: source, Target: target, Type: relationType(source, target), Status: knowledge.RelationStatusConfirmed, Traversal: traversal, Version: 1, EvidenceHref: "/evidence", UpdatedAt: time.Now().UTC()}
}
func completeMeta() PageMeta             { return PageMeta{Fingerprint: strings.Repeat("a", 64), Complete: true} }
func ptrTime(value time.Time) *time.Time { return &value }
func relationType(source, target knowledge.NodeRef) knowledge.RelationType {
	if source.Type == knowledge.NodeTypeClaim && target.Type == knowledge.NodeTypeTopic {
		return knowledge.RelationBelongsTo
	}
	if source.Type == knowledge.NodeTypeTopic && target.Type == knowledge.NodeTypeClaim {
		return knowledge.RelationImpacts
	}
	return knowledge.RelationSupports
}
func ref(nodeType knowledge.NodeType, n int) knowledge.NodeRef {
	return knowledge.NodeRef{Type: nodeType, ID: id(n)}
}
func id(n int) foundation.ID { return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", n)) }
func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	var target *foundation.Error
	if !errors.As(err, &target) || target.Code != want {
		t.Fatalf("error = %v, want code %s", err, want)
	}
}
