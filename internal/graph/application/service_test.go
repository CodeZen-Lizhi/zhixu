package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestServiceFailsClosedForMissingDependenciesAndContext(t *testing.T) {
	var typedNil *graphQueryPortStub
	for _, port := range []QueryPort{nil, typedNil} {
		service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
		if service != nil {
			t.Fatal("missing port unexpectedly created service")
		}
		assertServiceError(t, err, foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable)
	}

	var service *Service
	_, err := service.Global(context.Background(), validGlobalRequest())
	assertServiceError(t, err, foundation.ErrorDependencyUnavailable, graphdomain.ErrorCodeDependencyUnavailable)
	service, err = NewService(&graphQueryPortStub{}, mustGraphCursorCodec(t, 'q'))
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Global(nil, validGlobalRequest())
	assertServiceError(t, err, foundation.ErrorInvalidInput, graphdomain.ErrorCodeRequestInvalid)
}

func TestServiceValidatesBeforeCallingPortAndPreservesPortErrors(t *testing.T) {
	port := &graphQueryPortStub{globalError: context.Canceled}
	service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
	if err != nil {
		t.Fatal(err)
	}
	invalid := validGlobalRequest()
	invalid.Limit = graphdomain.MaxLimit + 1
	if _, err := service.Global(context.Background(), invalid); err == nil || port.globalCalls != 0 {
		t.Fatalf("invalid request err=%v calls=%d", err, port.globalCalls)
	}
	_, err = service.Global(context.Background(), validGlobalRequest())
	if !errors.Is(err, context.Canceled) || port.globalCalls != 1 {
		t.Fatalf("port error=%v calls=%d", err, port.globalCalls)
	}
}

func TestServiceRejectsAdapterOverReturnAcrossQueryShapes(t *testing.T) {
	port := &graphQueryPortStub{}
	service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	workspaceID := graphServiceID(90)

	port.global = GlobalResultWindow{Items: make([]graphdomain.GlobalCluster, MaxResultWindowItems+1)}
	request := validGlobalRequest()
	request.Limit = 1
	_, err = service.Global(ctx, request)
	assertServiceError(t, err, foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent)

	port.search = graphdomain.NodeSearchResult{WorkspaceID: workspaceID, Matches: make([]graphdomain.NodeSearchMatch, 2)}
	_, err = service.SearchNodes(ctx, graphdomain.NodeSearchRequest{WorkspaceID: workspaceID, Query: "go", Limit: 1})
	assertServiceError(t, err, foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent)

	center := graphServiceRef(knowledge.NodeTypeTopic, 1)
	_, err = service.Neighborhood(ctx, graphdomain.NeighborhoodRequest{WorkspaceID: workspaceID, Center: center, Depth: 1, Limit: 25, Direction: graphdomain.TraversalBoth, MaxNodes: 10, MaxEdges: 10, MaxFrontier: 10})
	assertServiceError(t, err, foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent)

	_, err = service.FindPath(ctx, graphdomain.PathRequest{WorkspaceID: workspaceID, From: center, To: graphServiceRef(knowledge.NodeTypeClaim, 2), Direction: graphdomain.TraversalBoth, MaxDepth: 6, MaxVisited: 20})
	assertServiceError(t, err, foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent)

	_, err = service.NodeDetail(ctx, workspaceID, center)
	assertServiceError(t, err, foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent)
	_, err = service.RelationDetail(ctx, workspaceID, graphServiceID(3))
	assertServiceError(t, err, foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent)
	port.evidence = RelationEvidenceResultWindow{Items: make([]graphdomain.RelationEvidenceItem, MaxResultWindowItems+1)}
	_, err = service.RelationEvidence(ctx, workspaceID, graphServiceID(3), 20)
	assertServiceError(t, err, foundation.ErrorConsistencyViolation, graphdomain.ErrorCodeProjectionInconsistent)
}

func TestServiceReturnsValidatedProjection(t *testing.T) {
	workspaceID := graphServiceID(90)
	now := time.Now().UTC()
	topic := graphdomain.TopicNode{Ref: graphServiceRef(knowledge.NodeTypeTopic, 1), WorkspaceID: workspaceID, Name: "Go", Status: knowledge.TopicStatusActive, Version: 1, UpdatedAt: now}
	port := &graphQueryPortStub{global: GlobalResultWindow{Items: []graphdomain.GlobalCluster{{Topic: topic, DirectClaimCount: 1, IncidentRelationCount: 1, ClusterScore: 2, UpdatedAt: now}}}}
	service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.Global(context.Background(), validGlobalRequest())
	if err != nil || len(page.Clusters) != 1 || port.globalCalls != 1 {
		t.Fatalf("page=%#v err=%v cause=%v calls=%d", page, err, errors.Unwrap(err), port.globalCalls)
	}
}

func TestServiceGlobalOwnsCursorAndRejectsResultDrift(t *testing.T) {
	workspaceID := graphServiceID(90)
	now := time.Now().UTC()
	clusters := make([]graphdomain.GlobalCluster, 3)
	for index := range clusters {
		topic := graphdomain.TopicNode{Ref: graphServiceRef(knowledge.NodeTypeTopic, index+1), WorkspaceID: workspaceID, Name: fmt.Sprintf("Topic %d", index), Status: knowledge.TopicStatusActive, Version: 1, UpdatedAt: now.Add(-time.Duration(index) * time.Second)}
		clusters[index] = graphdomain.GlobalCluster{Topic: topic, ClusterScore: 3 - index, DirectClaimCount: 3 - index, UpdatedAt: topic.UpdatedAt}
	}
	port := &graphQueryPortStub{global: GlobalResultWindow{Items: clusters}}
	service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
	if err != nil {
		t.Fatal(err)
	}
	request := validGlobalRequest()
	request.Limit = 2
	first, err := service.GlobalPage(context.Background(), GlobalPageRequest{Request: request})
	if err != nil || first.Meta.NextCursor == "" || first.Meta.Truncated {
		t.Fatalf("first=%#v err=%v cause=%v", first, err, errors.Unwrap(err))
	}
	port.global.Items[2].DirectClaimCount = 0
	port.global.Items[2].ClusterScore = 0
	_, err = service.GlobalPage(context.Background(), GlobalPageRequest{Request: request, Cursor: first.Meta.NextCursor})
	assertServiceError(t, err, foundation.ErrorVersionConflict, graphdomain.ErrorCodeCursorStale)
}

func TestServiceGlobalCursorRejectsWindowMetadataDrift(t *testing.T) {
	workspaceID := graphServiceID(90)
	now := time.Now().UTC()
	clusters := make([]graphdomain.GlobalCluster, 3)
	for index := range clusters {
		topic := graphdomain.TopicNode{Ref: graphServiceRef(knowledge.NodeTypeTopic, index+1), WorkspaceID: workspaceID, Name: fmt.Sprintf("Topic %d", index), Status: knowledge.TopicStatusActive, Version: 1, UpdatedAt: now.Add(-time.Duration(index) * time.Second)}
		clusters[index] = graphdomain.GlobalCluster{Topic: topic, ClusterScore: 3 - index, DirectClaimCount: 3 - index, UpdatedAt: topic.UpdatedAt}
	}

	for _, testCase := range []struct {
		name             string
		initialTruncated bool
		initialReason    string
		currentTruncated bool
		currentReason    string
	}{
		{name: "truncation changes", initialTruncated: true, initialReason: "RESULT_WINDOW_LIMIT"},
		{name: "reason changes", initialTruncated: true, initialReason: "RESULT_WINDOW_LIMIT", currentTruncated: true, currentReason: "FILTER_WINDOW_LIMIT"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			port := &graphQueryPortStub{global: GlobalResultWindow{Items: clusters, Truncated: testCase.initialTruncated, Reason: testCase.initialReason}}
			service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
			if err != nil {
				t.Fatal(err)
			}
			request := validGlobalRequest()
			request.Limit = 2
			first, err := service.GlobalPage(context.Background(), GlobalPageRequest{Request: request})
			if err != nil || first.Meta.NextCursor == "" {
				t.Fatalf("first=%#v err=%v", first, err)
			}

			port.global.Truncated = testCase.currentTruncated
			port.global.Reason = testCase.currentReason
			_, err = service.GlobalPage(context.Background(), GlobalPageRequest{Request: request, Cursor: first.Meta.NextCursor})
			assertServiceError(t, err, foundation.ErrorVersionConflict, graphdomain.ErrorCodeCursorStale)
		})
	}
}

func TestServicePreservesBoundedWindowTruncationAcrossPages(t *testing.T) {
	workspaceID := graphServiceID(90)
	now := time.Now().UTC()
	clusters := make([]graphdomain.GlobalCluster, 3)
	for index := range clusters {
		topic := graphdomain.TopicNode{Ref: graphServiceRef(knowledge.NodeTypeTopic, index+1), WorkspaceID: workspaceID, Name: fmt.Sprintf("Topic %d", index), Status: knowledge.TopicStatusActive, Version: 1, UpdatedAt: now.Add(-time.Duration(index) * time.Second)}
		clusters[index] = graphdomain.GlobalCluster{Topic: topic, ClusterScore: 3 - index, DirectClaimCount: 3 - index, UpdatedAt: topic.UpdatedAt}
	}
	port := &graphQueryPortStub{global: GlobalResultWindow{Items: clusters, Truncated: true, Reason: "RESULT_WINDOW_LIMIT"}}
	service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
	if err != nil {
		t.Fatal(err)
	}
	request := validGlobalRequest()
	request.Limit = 2
	first, err := service.GlobalPage(context.Background(), GlobalPageRequest{Request: request})
	if err != nil || !first.Meta.Truncated || first.Meta.Complete || first.Meta.NextCursor == "" || first.Meta.Reason != "RESULT_WINDOW_LIMIT" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := service.GlobalPage(context.Background(), GlobalPageRequest{Request: request, Cursor: first.Meta.NextCursor})
	if err != nil || !second.Meta.Truncated || second.Meta.Complete || second.Meta.NextCursor != "" || len(second.Clusters) != 1 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

func TestServiceNeighborhoodCursorRejectsWindowMetadataDrift(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		initialTruncated bool
		initialReason    string
		currentTruncated bool
		currentReason    string
	}{
		{name: "truncation changes", initialTruncated: true, initialReason: "RESULT_WINDOW_LIMIT"},
		{name: "reason changes", initialTruncated: true, initialReason: "RESULT_WINDOW_LIMIT", currentTruncated: true, currentReason: "FILTER_WINDOW_LIMIT"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request, neighborhood := validNeighborhoodWindow(t, testCase.initialTruncated, testCase.initialReason)
			port := &graphQueryPortStub{neighborhood: neighborhood}
			service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
			if err != nil {
				t.Fatal(err)
			}
			first, err := service.NeighborhoodPage(context.Background(), NeighborhoodPageRequest{Request: request})
			if err != nil || first.Meta.NextCursor == "" {
				t.Fatalf("first=%#v err=%v", first, err)
			}

			port.neighborhood.Meta.Truncated = testCase.currentTruncated
			port.neighborhood.Meta.Reason = testCase.currentReason
			port.neighborhood.Meta.Complete = !testCase.currentTruncated
			_, err = service.NeighborhoodPage(context.Background(), NeighborhoodPageRequest{Request: request, Cursor: first.Meta.NextCursor})
			assertServiceError(t, err, foundation.ErrorVersionConflict, graphdomain.ErrorCodeCursorStale)
		})
	}
}

func TestServiceRelationEvidenceCursorRejectsWindowMetadataDrift(t *testing.T) {
	workspaceID, relationID := graphServiceID(90), graphServiceID(80)
	items := validRelationEvidenceItems(t, workspaceID, relationID, 3)
	for _, testCase := range []struct {
		name             string
		initialTruncated bool
		initialReason    string
		currentTruncated bool
		currentReason    string
	}{
		{name: "truncation changes", initialTruncated: true, initialReason: "RESULT_WINDOW_LIMIT"},
		{name: "reason changes", initialTruncated: true, initialReason: "RESULT_WINDOW_LIMIT", currentTruncated: true, currentReason: "FILTER_WINDOW_LIMIT"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			port := &graphQueryPortStub{evidence: RelationEvidenceResultWindow{Items: items, Truncated: testCase.initialTruncated, Reason: testCase.initialReason}}
			service, err := NewService(port, mustGraphCursorCodec(t, 'q'))
			if err != nil {
				t.Fatal(err)
			}
			first, err := service.RelationEvidencePage(context.Background(), RelationEvidencePageRequest{WorkspaceID: workspaceID, RelationID: relationID, Limit: 2})
			if err != nil || first.Meta.NextCursor == "" {
				t.Fatalf("first=%#v err=%v", first, err)
			}

			port.evidence.Truncated = testCase.currentTruncated
			port.evidence.Reason = testCase.currentReason
			_, err = service.RelationEvidencePage(context.Background(), RelationEvidencePageRequest{WorkspaceID: workspaceID, RelationID: relationID, Limit: 2, Cursor: first.Meta.NextCursor})
			assertServiceError(t, err, foundation.ErrorVersionConflict, graphdomain.ErrorCodeCursorStale)
		})
	}
}

type graphQueryPortStub struct {
	global       GlobalResultWindow
	globalError  error
	globalCalls  int
	search       graphdomain.NodeSearchResult
	neighborhood graphdomain.Neighborhood
	evidence     RelationEvidenceResultWindow
}

func (port *graphQueryPortStub) GlobalWindow(context.Context, graphdomain.GlobalRequest) (GlobalResultWindow, error) {
	port.globalCalls++
	return port.global, port.globalError
}
func (port *graphQueryPortStub) SearchNodes(context.Context, graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error) {
	return port.search, nil
}
func (port *graphQueryPortStub) NeighborhoodWindow(context.Context, graphdomain.NeighborhoodRequest) (graphdomain.Neighborhood, error) {
	return port.neighborhood, nil
}
func (*graphQueryPortStub) FindPath(context.Context, graphdomain.PathRequest) (graphdomain.PathResult, error) {
	return graphdomain.PathResult{}, nil
}
func (*graphQueryPortStub) NodeDetail(context.Context, foundation.ID, knowledge.NodeRef) (graphdomain.GraphNode, error) {
	return graphdomain.GraphNode{}, nil
}
func (*graphQueryPortStub) RelationDetail(context.Context, foundation.ID, foundation.ID) (graphdomain.RelationDetail, error) {
	return graphdomain.RelationDetail{}, nil
}
func (port *graphQueryPortStub) RelationEvidenceWindow(context.Context, foundation.ID, foundation.ID) (RelationEvidenceResultWindow, error) {
	return port.evidence, nil
}

func validGlobalRequest() graphdomain.GlobalRequest {
	return graphdomain.GlobalRequest{WorkspaceID: graphServiceID(90), Limit: 25}
}

func graphServiceRef(nodeType knowledge.NodeType, value int) knowledge.NodeRef {
	return knowledge.NodeRef{Type: nodeType, ID: graphServiceID(value)}
}

func graphServiceID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", value))
}

func validNeighborhoodWindow(t *testing.T, truncated bool, reason string) (graphdomain.NeighborhoodRequest, graphdomain.Neighborhood) {
	t.Helper()
	workspaceID := graphServiceID(90)
	now := time.Now().UTC()
	applicability, err := knowledge.ParseApplicability([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	center := graphdomain.ClaimNode{Ref: graphServiceRef(knowledge.NodeTypeClaim, 1), WorkspaceID: workspaceID, Statement: "center claim", Status: knowledge.ClaimStatusConfirmed, Applicability: applicability, Version: 1, UpdatedAt: now}
	nodes := []graphdomain.GraphNode{{Claim: &center}}
	edges := make([]graphdomain.GraphEdge, 3)
	for index := range edges {
		topic := graphdomain.TopicNode{Ref: graphServiceRef(knowledge.NodeTypeTopic, index+2), WorkspaceID: workspaceID, Name: fmt.Sprintf("Topic %d", index), Status: knowledge.TopicStatusActive, Version: 1, UpdatedAt: now}
		nodes = append(nodes, graphdomain.GraphNode{Topic: &topic})
		edges[index] = graphdomain.GraphEdge{RelationID: graphServiceID(index + 10), WorkspaceID: workspaceID, Source: center.Ref, Target: topic.Ref, Type: knowledge.RelationBelongsTo, Status: knowledge.RelationStatusConfirmed, Traversal: graphdomain.EdgeTraversalForward, Version: 1, EvidenceHref: "/evidence", UpdatedAt: now}
	}
	request := graphdomain.NeighborhoodRequest{WorkspaceID: workspaceID, Center: center.Ref, Depth: 1, Limit: 2, Direction: graphdomain.TraversalBoth, MaxNodes: 10, MaxEdges: 10, MaxFrontier: 10}
	result := graphdomain.Neighborhood{WorkspaceID: workspaceID, Center: center.Ref, Nodes: nodes, Edges: edges, LayerCounts: []int{3}, CompletedDepth: 1, Meta: graphdomain.PageMeta{Fingerprint: strings.Repeat("a", 64), Complete: !truncated, Truncated: truncated, Reason: reason}}
	return request, result
}

func validRelationEvidenceItems(t *testing.T, workspaceID, relationID foundation.ID, count int) []graphdomain.RelationEvidenceItem {
	t.Helper()
	applicability, err := knowledge.ParseApplicability([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	items := make([]graphdomain.RelationEvidenceItem, count)
	for index := range items {
		items[index] = graphdomain.RelationEvidenceItem{
			ID: graphServiceID(index + 20), WorkspaceID: workspaceID, RelationID: relationID,
			Provenance: knowledge.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: graphServiceID(index + 30), SourceSpanID: graphServiceID(index + 40)},
			Reason:     "evidence reason", Applicability: applicability, SourceHref: "/source", SpanHref: "/span", CreatedAt: time.Unix(int64(index+1), 0).UTC(),
		}
	}
	return items
}

func assertServiceError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var target *foundation.Error
	if !errors.As(err, &target) || target.Kind != kind || target.Code != code {
		t.Fatalf("error=%v, want %s/%s", err, kind, code)
	}
}
