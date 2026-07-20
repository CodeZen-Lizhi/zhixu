//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	graphapp "github.com/CodeZen-Lizhi/zhixu/internal/graph/application"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryGlobalSearchAndDetailsUseCanonicalKnowledgeFacts(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)

	window, err := repository.GlobalWindow(ctx, graphdomain.GlobalRequest{WorkspaceID: fixture.workspaceID, Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if window.Truncated || len(window.Items) != 2 || window.Items[0].Topic.Ref.ID != fixture.primaryTopicID || window.Items[0].DirectClaimCount != 2 || window.Items[0].IncidentRelationCount != 3 || window.Items[0].ClusterScore != 5 {
		t.Fatalf("window=%#v", window)
	}
	codec, err := graphapp.NewCursorCodec(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := graphapp.NewService(repository, codec)
	if err != nil {
		t.Fatal(err)
	}
	pageRequest := graphdomain.GlobalRequest{WorkspaceID: fixture.workspaceID, Limit: 1}
	firstPage, err := service.GlobalPage(ctx, graphapp.GlobalPageRequest{Request: pageRequest})
	if err != nil || len(firstPage.Clusters) != 1 || firstPage.Meta.NextCursor == "" {
		t.Fatalf("first page=%#v err=%v", firstPage, err)
	}
	secondPage, err := service.GlobalPage(ctx, graphapp.GlobalPageRequest{Request: pageRequest, Cursor: firstPage.Meta.NextCursor})
	if err != nil || len(secondPage.Clusters) != 1 || secondPage.Meta.NextCursor != "" {
		t.Fatalf("second page=%#v err=%v", secondPage, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.topic SET description='changed',version=version+1,updated_at=updated_at+interval '1 second' WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.secondaryTopicID)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GlobalPage(ctx, graphapp.GlobalPageRequest{Request: pageRequest, Cursor: firstPage.Meta.NextCursor}); !hasGraphCode(err, graphdomain.ErrorCodeCursorStale) {
		t.Fatalf("stale cursor err=%v", err)
	}
	filtered, err := repository.GlobalWindow(ctx, graphdomain.GlobalRequest{WorkspaceID: fixture.workspaceID, Limit: 25, Filter: graphdomain.GraphFilter{TopicIDs: []foundation.ID{fixture.secondaryTopicID}}})
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Topic.Ref.ID != fixture.secondaryTopicID {
		t.Fatalf("filtered=%#v err=%v", filtered, err)
	}

	search, err := repository.SearchNodes(ctx, graphdomain.NodeSearchRequest{WorkspaceID: fixture.workspaceID, Query: "golang", Limit: 20})
	if err != nil || len(search.Matches) != 1 || search.Matches[0].Kind != graphdomain.NodeSearchExact || search.Matches[0].Node.Ref().ID != fixture.primaryTopicID {
		t.Fatalf("search=%#v err=%v", search, err)
	}
	claimSearch, err := repository.SearchNodes(ctx, graphdomain.NodeSearchRequest{WorkspaceID: fixture.workspaceID, Query: "Graph", Limit: 20})
	if err != nil || len(claimSearch.Matches) != 1 || claimSearch.Matches[0].Node.Ref().ID != fixture.longClaimID || len(claimSearch.Matches[0].Node.Claim.Statement) > 512 {
		t.Fatalf("claim search=%#v err=%v", claimSearch, err)
	}

	node, err := repository.NodeDetail(ctx, fixture.workspaceID, knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.longClaimID})
	if err != nil || node.Claim == nil || len(node.Claim.Statement) > 512 {
		t.Fatalf("node=%#v err=%v", node, err)
	}
	detail, err := repository.RelationDetail(ctx, fixture.workspaceID, fixture.supportRelationID)
	if err != nil || detail.Edge.EvidenceCount != 1 || detail.Edge.EvidenceFingerprint == "" || detail.Confirmation == nil || detail.Edge.EvidenceHref == "" {
		t.Fatalf("detail=%#v err=%v", detail, err)
	}

	otherWorkspace := graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'other',$2,$2,$3,'test',1,$3,$3)`, string(otherWorkspace), "/tmp/graph-other-"+string(otherWorkspace), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.NodeDetail(ctx, otherWorkspace, knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID}); !hasGraphCode(err, graphdomain.ErrorCodeNodeNotFound) {
		t.Fatalf("cross workspace err=%v", err)
	}
	if _, err := repository.RelationDetail(ctx, otherWorkspace, fixture.supportRelationID); !hasGraphCode(err, graphdomain.ErrorCodeRelationNotFound) {
		t.Fatalf("cross workspace relation err=%v", err)
	}
}

func TestRepositoryGlobalFiltersRecomputeClusterFromEligibleFacts(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Add(-time.Hour)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	topicID := seedGraphTopic(t, ctx, tx, workspaceID, "Filter Topic", "filter topic", now)
	confirmedHigh := seedGraphClaim(t, ctx, tx, workspaceID, "Confirmed high", knowledge.ClaimStatusConfirmed, floatPointer(0.9), now)
	confirmedLow := seedGraphClaim(t, ctx, tx, workspaceID, "Confirmed low", knowledge.ClaimStatusConfirmed, floatPointer(0.4), now)
	disputedNull := seedGraphClaim(t, ctx, tx, workspaceID, "Disputed null", knowledge.ClaimStatusDisputed, nil, now)
	highMembership := seedGraphRelation(t, ctx, tx, workspaceID, confirmedHigh, knowledge.NodeTypeClaim, topicID, knowledge.NodeTypeTopic, knowledge.RelationBelongsTo, knowledge.RelationStatusConfirmed, floatPointer(0.95), now)
	seedGraphRelation(t, ctx, tx, workspaceID, confirmedLow, knowledge.NodeTypeClaim, topicID, knowledge.NodeTypeTopic, knowledge.RelationBelongsTo, knowledge.RelationStatusConfirmed, floatPointer(0.3), now)
	seedGraphRelation(t, ctx, tx, workspaceID, disputedNull, knowledge.NodeTypeClaim, topicID, knowledge.NodeTypeTopic, knowledge.RelationBelongsTo, knowledge.RelationStatusConfirmed, nil, now)
	seedGraphRelation(t, ctx, tx, workspaceID, confirmedHigh, knowledge.NodeTypeClaim, confirmedLow, knowledge.NodeTypeClaim, knowledge.RelationSupports, knowledge.RelationStatusConfirmed, floatPointer(0.8), now)
	seedGraphRelation(t, ctx, tx, workspaceID, confirmedHigh, knowledge.NodeTypeClaim, disputedNull, knowledge.NodeTypeClaim, knowledge.RelationComplements, knowledge.RelationStatusStale, floatPointer(0.7), now)

	assertGlobalCluster := func(t *testing.T, filter graphdomain.GraphFilter, directClaims, incidentRelations int) {
		t.Helper()
		window, err := repository.GlobalWindow(ctx, graphdomain.GlobalRequest{WorkspaceID: workspaceID, Limit: 25, Filter: filter})
		if err != nil || len(window.Items) != 1 {
			t.Fatalf("window=%#v err=%v", window, err)
		}
		cluster := window.Items[0]
		if cluster.Topic.Ref.ID != topicID || cluster.DirectClaimCount != directClaims || cluster.IncidentRelationCount != incidentRelations || cluster.ClusterScore != directClaims+incidentRelations {
			t.Fatalf("cluster=%#v", cluster)
		}
	}

	assertGlobalCluster(t, graphdomain.GraphFilter{ClaimMinConfidence: floatPointer(0.8)}, 1, 2)
	assertGlobalCluster(t, graphdomain.GraphFilter{RelationMinConfidence: floatPointer(0.75)}, 3, 2)
	assertGlobalCluster(t, graphdomain.GraphFilter{ClaimStatuses: []knowledge.ClaimStatus{knowledge.ClaimStatusDisputed}}, 1, 1)
	assertGlobalCluster(t, graphdomain.GraphFilter{RelationStatuses: []knowledge.RelationStatus{knowledge.RelationStatusStale}}, 3, 1)
	assertGlobalCluster(t, graphdomain.GraphFilter{RelationTypes: []knowledge.RelationType{knowledge.RelationSupports}}, 3, 1)

	claimOnly, err := repository.GlobalWindow(ctx, graphdomain.GlobalRequest{WorkspaceID: workspaceID, Limit: 25, Filter: graphdomain.GraphFilter{NodeTypes: []knowledge.NodeType{knowledge.NodeTypeClaim}}})
	if err != nil || len(claimOnly.Items) != 0 {
		t.Fatalf("claim-only window=%#v err=%v", claimOnly, err)
	}
	topicOnly, err := repository.GlobalWindow(ctx, graphdomain.GlobalRequest{WorkspaceID: workspaceID, Limit: 25, Filter: graphdomain.GraphFilter{NodeTypes: []knowledge.NodeType{knowledge.NodeTypeTopic}}})
	if err != nil || len(topicOnly.Items) != 1 || topicOnly.Items[0].DirectClaimCount != 0 || topicOnly.Items[0].IncidentRelationCount != 3 {
		t.Fatalf("topic-only window=%#v err=%v", topicOnly, err)
	}

	detail, err := repository.RelationDetail(ctx, workspaceID, highMembership)
	if err != nil || detail.Edge.Confidence == nil || *detail.Edge.Confidence != 0.95 {
		t.Fatalf("relation detail=%#v err=%v", detail, err)
	}
}

func TestRepositoryUpdatedAfterIncludesOldClaimWithNewMembership(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	old := time.Now().UTC().Add(-2 * time.Hour)
	cutoff := old.Add(time.Hour)
	newer := cutoff.Add(time.Minute)
	workspaceID := seedGraphWorkspace(t, ctx, tx, old)
	topicID := seedGraphTopic(t, ctx, tx, workspaceID, "Fresh Membership", "fresh membership", old)
	claimID := seedGraphClaim(t, ctx, tx, workspaceID, "Old claim", knowledge.ClaimStatusConfirmed, floatPointer(0.8), old)
	seedGraphRelation(t, ctx, tx, workspaceID, claimID, knowledge.NodeTypeClaim, topicID, knowledge.NodeTypeTopic, knowledge.RelationBelongsTo, knowledge.RelationStatusConfirmed, floatPointer(0.8), newer)

	window, err := repository.GlobalWindow(ctx, graphdomain.GlobalRequest{WorkspaceID: workspaceID, Limit: 25, Filter: graphdomain.GraphFilter{UpdatedAfter: &cutoff}})
	if err != nil || len(window.Items) != 1 || window.Items[0].DirectClaimCount != 1 || window.Items[0].IncidentRelationCount != 1 || !window.Items[0].UpdatedAt.Equal(newer) {
		t.Fatalf("window=%#v err=%v", window, err)
	}
}

func TestRepositoryCountsIncidentRelationOncePerCluster(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Add(-time.Hour)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	topicID := seedGraphTopic(t, ctx, tx, workspaceID, "Dedup Topic", "dedup topic", now)
	firstClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "First member", knowledge.ClaimStatusConfirmed, floatPointer(0.9), now)
	secondClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "Second member", knowledge.ClaimStatusConfirmed, floatPointer(0.9), now)
	seedGraphRelation(t, ctx, tx, workspaceID, firstClaimID, knowledge.NodeTypeClaim, topicID, knowledge.NodeTypeTopic, knowledge.RelationBelongsTo, knowledge.RelationStatusConfirmed, floatPointer(0.9), now)
	seedGraphRelation(t, ctx, tx, workspaceID, secondClaimID, knowledge.NodeTypeClaim, topicID, knowledge.NodeTypeTopic, knowledge.RelationBelongsTo, knowledge.RelationStatusConfirmed, floatPointer(0.9), now)
	seedGraphRelation(t, ctx, tx, workspaceID, firstClaimID, knowledge.NodeTypeClaim, secondClaimID, knowledge.NodeTypeClaim, knowledge.RelationSupports, knowledge.RelationStatusConfirmed, floatPointer(0.9), now)

	window, err := repository.GlobalWindow(ctx, graphdomain.GlobalRequest{WorkspaceID: workspaceID, Limit: 25})
	if err != nil || len(window.Items) != 1 || window.Items[0].DirectClaimCount != 2 || window.Items[0].IncidentRelationCount != 3 {
		t.Fatalf("window=%#v err=%v", window, err)
	}
}

func TestRepositorySearchDistinguishesAliasAndClaimExactPrefix(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Add(-time.Hour)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	topicID := seedGraphTopic(t, ctx, tx, workspaceID, "Concurrency", "concurrency", now)
	if _, err := tx.Exec(ctx, `INSERT INTO core.topic_alias(id,workspace_id,topic_id,alias,normalized_alias,created_at) VALUES($1,$2,$3,'Golang Runtime','golang runtime',$4)`, string(graphTestID(t)), string(workspaceID), string(topicID), now); err != nil {
		t.Fatal(err)
	}
	exactClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "Graph query", knowledge.ClaimStatusConfirmed, floatPointer(0.9), now)
	prefixClaimID := seedGraphClaim(t, ctx, tx, workspaceID, "Graph query projection", knowledge.ClaimStatusDisputed, nil, now)
	seedGraphClaim(t, ctx, tx, workspaceID, "Graph query hidden", knowledge.ClaimStatusSuggested, floatPointer(0.9), now)

	aliasMatches, err := repository.SearchNodes(ctx, graphdomain.NodeSearchRequest{WorkspaceID: workspaceID, Query: "gola", Limit: 20})
	if err != nil || len(aliasMatches.Matches) != 1 || aliasMatches.Matches[0].Kind != graphdomain.NodeSearchPrefix || aliasMatches.Matches[0].Node.Ref().ID != topicID {
		t.Fatalf("alias matches=%#v err=%v", aliasMatches, err)
	}
	claimMatches, err := repository.SearchNodes(ctx, graphdomain.NodeSearchRequest{WorkspaceID: workspaceID, Query: "Graph query", Limit: 20})
	if err != nil || len(claimMatches.Matches) != 2 || claimMatches.Matches[0].Kind != graphdomain.NodeSearchExact || claimMatches.Matches[0].Node.Ref().ID != exactClaimID || claimMatches.Matches[1].Kind != graphdomain.NodeSearchPrefix || claimMatches.Matches[1].Node.Ref().ID != prefixClaimID {
		t.Fatalf("claim matches=%#v err=%v", claimMatches, err)
	}
}

func TestRepositoryGlobalWindowTruncatesAtFiveHundredClusters(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	now := time.Now().UTC().Add(-time.Hour)
	workspaceID := seedGraphWorkspace(t, ctx, tx, now)
	batch := &pgx.Batch{}
	for index := 0; index < graphapp.MaxResultWindowItems+1; index++ {
		batch.Queue(`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,'','ACTIVE',1,$5,$5)`, string(graphTestID(t)), string(workspaceID), fmt.Sprintf("Topic %03d", index), fmt.Sprintf("topic %03d", index), now.Add(time.Duration(index)*time.Microsecond))
	}
	results := tx.SendBatch(ctx, batch)
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}

	window, err := repository.GlobalWindow(ctx, graphdomain.GlobalRequest{WorkspaceID: workspaceID, Limit: 25})
	if err != nil || !window.Truncated || window.Reason != "RESULT_WINDOW_LIMIT" || len(window.Items) != graphapp.MaxResultWindowItems {
		t.Fatalf("items=%d truncated=%v reason=%q err=%v", len(window.Items), window.Truncated, window.Reason, err)
	}
}

func TestRepositoryDepthOneNeighborhoodUsesBothCanonicalEndpoints(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)

	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.workspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID},
		Depth:       1,
		Limit:       25,
		Direction:   graphdomain.TraversalOutbound,
		MaxNodes:    10,
		MaxEdges:    10,
		MaxFrontier: 10,
	}
	result, err := repository.NeighborhoodWindow(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Edges) != 2 || len(result.Nodes) != 3 || result.CompletedDepth != 1 || !result.Meta.Complete {
		t.Fatalf("outbound=%#v", result)
	}
	for _, edge := range result.Edges {
		if edge.Traversal != graphdomain.EdgeTraversalForward || edge.EvidenceCount != 1 || edge.EvidenceFingerprint == "" {
			t.Fatalf("edge=%#v", edge)
		}
	}

	request.Center = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID}
	request.Direction = graphdomain.TraversalInbound
	result, err = repository.NeighborhoodWindow(ctx, request)
	if err != nil || len(result.Edges) != 2 || len(result.Nodes) != 3 {
		t.Fatalf("inbound=%#v err=%v", result, err)
	}
	for _, edge := range result.Edges {
		if edge.Traversal != graphdomain.EdgeTraversalReverse {
			t.Fatalf("edge=%#v", edge)
		}
	}

	request.Direction = graphdomain.TraversalOutbound
	result, err = repository.NeighborhoodWindow(ctx, request)
	if err != nil || len(result.Edges) != 0 || len(result.Nodes) != 1 {
		t.Fatalf("topic outbound=%#v err=%v", result, err)
	}
}

func TestRepositoryDepthOneNeighborhoodAppliesNodeAndRelationFilters(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	threshold := 0.85
	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.workspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID},
		Depth:       1,
		Limit:       25,
		Direction:   graphdomain.TraversalBoth,
		MaxNodes:    10,
		MaxEdges:    10,
		MaxFrontier: 10,
		Filter: graphdomain.GraphFilter{
			NodeTypes:             []knowledge.NodeType{knowledge.NodeTypeClaim},
			RelationTypes:         []knowledge.RelationType{knowledge.RelationSupports},
			ClaimStatuses:         []knowledge.ClaimStatus{knowledge.ClaimStatusDisputed, knowledge.ClaimStatusConfirmed},
			ClaimMinConfidence:    &threshold,
			RelationMinConfidence: &threshold,
		},
	}
	result, err := repository.NeighborhoodWindow(ctx, request)
	if err != nil || len(result.Edges) != 0 || len(result.Nodes) != 1 {
		t.Fatalf("filtered=%#v err=%v", result, err)
	}

	request.Filter.ClaimMinConfidence = nil
	result, err = repository.NeighborhoodWindow(ctx, request)
	if err != nil || len(result.Edges) != 1 || len(result.Nodes) != 2 || result.Nodes[0].Topic != nil || result.Nodes[1].Topic != nil {
		t.Fatalf("filtered=%#v err=%v", result, err)
	}
}

func TestRepositoryDeepNeighborhoodCompletesWholeLayersAcrossCycle(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.workspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID},
		Depth:       3,
		Limit:       25,
		Direction:   graphdomain.TraversalBoth,
		MaxNodes:    10,
		MaxEdges:    10,
		MaxFrontier: 10,
	}
	result, err := repository.NeighborhoodWindow(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.CompletedDepth != 3 || !result.Meta.Complete || result.Meta.Truncated || len(result.Nodes) != 3 || len(result.Edges) != 3 || len(result.LayerCounts) != 3 || result.LayerCounts[0] != 2 || result.LayerCounts[1] != 0 || result.LayerCounts[2] != 0 {
		t.Fatalf("result=%#v", result)
	}
	if err := graphdomain.ValidateNeighborhood(request, result); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryDeepNeighborhoodExpandsSecondAndThirdFrontiers(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	now := time.Now().UTC()
	third := seedGraphClaim(t, ctx, tx, fixture.workspaceID, "Third layer branch A", knowledge.ClaimStatusConfirmed, floatPointer(0.9), now)
	fourth := seedGraphClaim(t, ctx, tx, fixture.workspaceID, "Third layer branch B", knowledge.ClaimStatusConfirmed, floatPointer(0.9), now)
	fifth := seedGraphClaim(t, ctx, tx, fixture.workspaceID, "Fourth layer node", knowledge.ClaimStatusConfirmed, floatPointer(0.9), now)
	seedConfirmedRelationEvidence(t, ctx, tx, fixture.workspaceID, fixture.longClaimID, third, knowledge.RelationSupports, now)
	seedConfirmedRelationEvidence(t, ctx, tx, fixture.workspaceID, fixture.longClaimID, fourth, knowledge.RelationSupports, now.Add(time.Microsecond))
	seedConfirmedRelationEvidence(t, ctx, tx, fixture.workspaceID, third, fifth, knowledge.RelationSupports, now.Add(2*time.Microsecond))

	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.workspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID},
		Depth:       3,
		Limit:       25,
		Direction:   graphdomain.TraversalOutbound,
		MaxNodes:    10,
		MaxEdges:    10,
		MaxFrontier: 10,
		Filter:      graphdomain.GraphFilter{NodeTypes: []knowledge.NodeType{knowledge.NodeTypeClaim}, RelationTypes: []knowledge.RelationType{knowledge.RelationSupports}},
	}
	result, err := repository.NeighborhoodWindow(ctx, request)
	if err != nil || result.CompletedDepth != 3 || !result.Meta.Complete || len(result.Nodes) != 5 || len(result.Edges) != 4 || len(result.LayerCounts) != 3 || result.LayerCounts[0] != 1 || result.LayerCounts[1] != 2 || result.LayerCounts[2] != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if err := graphdomain.ValidateNeighborhood(request, result); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name                         string
		maxNodes, maxEdges, frontier int
		reason                       string
	}{
		{name: "node", maxNodes: 2, maxEdges: 10, frontier: 10, reason: "NODE_BUDGET"},
		{name: "edge", maxNodes: 10, maxEdges: 1, frontier: 10, reason: "EDGE_BUDGET"},
		{name: "frontier", maxNodes: 10, maxEdges: 10, frontier: 1, reason: "FRONTIER_BUDGET"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			limited := request
			limited.MaxNodes, limited.MaxEdges, limited.MaxFrontier = testCase.maxNodes, testCase.maxEdges, testCase.frontier
			truncated, queryErr := repository.NeighborhoodWindow(ctx, limited)
			if queryErr != nil || truncated.CompletedDepth != 1 || len(truncated.LayerCounts) != 1 || truncated.LayerCounts[0] != 1 || len(truncated.Nodes) != 2 || len(truncated.Edges) != 1 || !truncated.Meta.Truncated || truncated.Meta.Reason != testCase.reason {
				t.Fatalf("result=%#v err=%v", truncated, queryErr)
			}
			if err := graphdomain.ValidateNeighborhood(limited, truncated); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepositoryDeepNeighborhoodDropsIncompleteBudgetLayer(t *testing.T) {
	for _, testCase := range []struct {
		name               string
		maxNodes, maxEdges int
		maxFrontier        int
		reason             string
	}{
		{name: "node", maxNodes: 2, maxEdges: 10, maxFrontier: 10, reason: "NODE_BUDGET"},
		{name: "edge", maxNodes: 10, maxEdges: 1, maxFrontier: 10, reason: "EDGE_BUDGET"},
		{name: "frontier", maxNodes: 10, maxEdges: 10, maxFrontier: 1, reason: "FRONTIER_BUDGET"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository, tx, ctx := graphIntegrationRepository(t)
			fixture := seedGraphFixture(t, ctx, tx)
			request := graphdomain.NeighborhoodRequest{
				WorkspaceID: fixture.workspaceID,
				Center:      knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID},
				Depth:       2,
				Limit:       25,
				Direction:   graphdomain.TraversalBoth,
				MaxNodes:    testCase.maxNodes,
				MaxEdges:    testCase.maxEdges,
				MaxFrontier: testCase.maxFrontier,
			}
			result, err := repository.NeighborhoodWindow(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if result.CompletedDepth != 0 || len(result.Nodes) != 1 || len(result.Edges) != 0 || len(result.LayerCounts) != 0 || !result.Meta.Truncated || result.Meta.Reason != testCase.reason {
				t.Fatalf("result=%#v", result)
			}
			if err := graphdomain.ValidateNeighborhood(request, result); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepositoryDepthOneNeighborhoodCursorPaginatesAndBecomesStale(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	codec, err := graphapp.NewCursorCodec(bytes.Repeat([]byte{0x4c}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := graphapp.NewService(repository, codec)
	if err != nil {
		t.Fatal(err)
	}
	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.workspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID},
		Depth:       1,
		Limit:       1,
		Direction:   graphdomain.TraversalOutbound,
		MaxNodes:    10,
		MaxEdges:    10,
		MaxFrontier: 10,
	}
	first, err := service.NeighborhoodPage(ctx, graphapp.NeighborhoodPageRequest{Request: request})
	if err != nil || len(first.Edges) != 1 || first.Meta.NextCursor == "" || first.Meta.Complete {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := service.NeighborhoodPage(ctx, graphapp.NeighborhoodPageRequest{Request: request, Cursor: first.Meta.NextCursor})
	if err != nil || len(second.Edges) != 1 || second.Meta.NextCursor != "" || !second.Meta.Complete || second.Edges[0].RelationID == first.Edges[0].RelationID {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.relation SET version=version+1,updated_at=updated_at+interval '1 second' WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.supportRelationID)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.NeighborhoodPage(ctx, graphapp.NeighborhoodPageRequest{Request: request, Cursor: first.Meta.NextCursor}); !hasGraphCode(err, graphdomain.ErrorCodeCursorStale) {
		t.Fatalf("stale err=%v", err)
	}
}

func TestRepositoryDepthOneNeighborhoodTraversesSymmetricRelationBothWays(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	sourceID, targetID := fixture.firstClaimID, fixture.longClaimID
	if sourceID > targetID {
		sourceID, targetID = targetID, sourceID
	}
	relationID := graphTestID(t)
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at) VALUES($1,$2,'CLAIM',$3,'CLAIM',$4,'DUPLICATES','CONFIRMED',0.9,$5,$6,'SOURCE_DERIVED','symmetric fixture',1,$7,$7)`, string(relationID), string(fixture.workspaceID), string(sourceID), string(targetID), graphHash("symmetric-relation"+string(relationID)), graphHash("symmetric-evidence"+string(relationID)), now); err != nil {
		t.Fatal(err)
	}
	var sourceVersionID, sourceSpanID string
	var applicability []byte
	var schemaVersion, applicabilityHash string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text,applicability,applicability_schema_version,applicability_hash FROM core.relation_evidence WHERE workspace_id=$1 LIMIT 1`, string(fixture.workspaceID)).Scan(&sourceVersionID, &sourceSpanID, &applicability, &schemaVersion, &applicabilityHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,'symmetric evidence',$6,$7,$8,$9,'SOURCE_DERIVED','fixture',$10)`, string(graphTestID(t)), string(fixture.workspaceID), string(relationID), sourceVersionID, sourceSpanID, graphHash("symmetric-evidence-row"+string(relationID)), applicability, schemaVersion, applicabilityHash, now); err != nil {
		t.Fatal(err)
	}
	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.workspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: targetID},
		Depth:       1,
		Limit:       25,
		Direction:   graphdomain.TraversalOutbound,
		MaxNodes:    10,
		MaxEdges:    10,
		MaxFrontier: 10,
		Filter:      graphdomain.GraphFilter{RelationTypes: []knowledge.RelationType{knowledge.RelationDuplicates}},
	}
	result, err := repository.NeighborhoodWindow(ctx, request)
	if err != nil || len(result.Edges) != 1 || result.Edges[0].RelationID != relationID || result.Edges[0].Traversal != graphdomain.EdgeTraversalReverse {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestRepositoryNeighborhoodHonorsCanceledContext(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.workspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID},
		Depth:       2,
		Limit:       25,
		Direction:   graphdomain.TraversalBoth,
		MaxNodes:    10,
		MaxEdges:    10,
		MaxFrontier: 10,
	}
	if _, err := repository.NeighborhoodWindow(canceled, request); !hasGraphCode(err, graphdomain.ErrorCodeDependencyUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestRepositoryDepthOneNeighborhoodTruncatesFiveHundredEdgeWindow(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	var sourceVersionID, sourceSpanID string
	var applicability []byte
	var schemaVersion, applicabilityHash string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text,applicability,applicability_schema_version,applicability_hash FROM core.relation_evidence WHERE workspace_id=$1 LIMIT 1`, string(fixture.workspaceID)).Scan(&sourceVersionID, &sourceSpanID, &applicability, &schemaVersion, &applicabilityHash); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	batch := &pgx.Batch{}
	for index := 0; index < graphapp.MaxResultWindowItems+1; index++ {
		topicID, relationID := graphTestID(t), graphTestID(t)
		batch.Queue(`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,'','ACTIVE',1,$5,$5)`, string(topicID), string(fixture.workspaceID), fmt.Sprintf("Neighbor %03d", index), fmt.Sprintf("neighbor %03d", index), now)
		batch.Queue(`INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at) VALUES($1,$2,'TOPIC',$3,'TOPIC',$4,'IMPACTS','CONFIRMED',0.9,$5,$6,'SOURCE_DERIVED','window fixture',1,$7,$7)`, string(relationID), string(fixture.workspaceID), string(fixture.primaryTopicID), string(topicID), graphHash(fmt.Sprintf("window-relation-%d", index)), graphHash(fmt.Sprintf("window-evidence-%d", index)), now)
		batch.Queue(`INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,'window evidence',$6,$7,$8,$9,'SOURCE_DERIVED','fixture',$10)`, string(graphTestID(t)), string(fixture.workspaceID), string(relationID), sourceVersionID, sourceSpanID, graphHash(fmt.Sprintf("window-evidence-row-%d", index)), applicability, schemaVersion, applicabilityHash, now)
	}
	results := tx.SendBatch(ctx, batch)
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
	request := graphdomain.NeighborhoodRequest{
		WorkspaceID: fixture.workspaceID,
		Center:      knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID},
		Depth:       1,
		Limit:       100,
		Direction:   graphdomain.TraversalOutbound,
		MaxNodes:    graphdomain.MaxNodes,
		MaxEdges:    graphdomain.MaxEdges,
		MaxFrontier: graphdomain.MaxNodes,
		Filter:      graphdomain.GraphFilter{RelationTypes: []knowledge.RelationType{knowledge.RelationImpacts}},
	}
	result, err := repository.NeighborhoodWindow(ctx, request)
	if err != nil || len(result.Edges) != graphapp.MaxResultWindowItems || !result.Meta.Truncated || result.Meta.Reason != "RESULT_WINDOW_LIMIT" || result.Meta.Complete {
		t.Fatalf("edges=%d meta=%#v err=%v", len(result.Edges), result.Meta, err)
	}
}

func TestRepositoryFindPathIsDeterministicAndDirectionAware(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedGraphFixture(t, ctx, seedTx)
	now := time.Now().UTC()
	left := seedConfirmedClaimWithSource(t, ctx, seedTx, fixture.workspaceID, "Diamond left", now)
	right := seedConfirmedClaimWithSource(t, ctx, seedTx, fixture.workspaceID, "Diamond right", now.Add(time.Microsecond))
	leftSupport := seedConfirmedRelationEvidence(t, ctx, seedTx, fixture.workspaceID, fixture.firstClaimID, left, knowledge.RelationSupports, now)
	rightSupport := seedConfirmedRelationEvidence(t, ctx, seedTx, fixture.workspaceID, fixture.firstClaimID, right, knowledge.RelationSupports, now.Add(time.Microsecond))
	leftMembership := seedConfirmedClaimTopicRelationEvidence(t, ctx, seedTx, fixture.workspaceID, left, fixture.secondaryTopicID, now.Add(2*time.Microsecond))
	rightMembership := seedConfirmedClaimTopicRelationEvidence(t, ctx, seedTx, fixture.workspaceID, right, fixture.secondaryTopicID, now.Add(3*time.Microsecond))
	duplicateSource, duplicateTarget := left, right
	if duplicateTarget < duplicateSource {
		duplicateSource, duplicateTarget = duplicateTarget, duplicateSource
	}
	duplicateRelation := seedConfirmedRelationEvidence(t, ctx, seedTx, fixture.workspaceID, duplicateSource, duplicateTarget, knowledge.RelationDuplicates, now.Add(4*time.Microsecond))
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupCommittedGraphFixture(pool, fixture.workspaceID) })
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	request := graphdomain.PathRequest{
		WorkspaceID: fixture.workspaceID,
		From:        knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID},
		To:          knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID},
		Direction:   graphdomain.TraversalOutbound,
		RelationTypes: []knowledge.RelationType{
			knowledge.RelationBelongsTo,
		},
		MaxDepth:   4,
		MaxVisited: 20,
	}
	result, err := repository.FindPath(ctx, request)
	if err != nil || result.Status != graphdomain.PathFound || result.HopCount != 1 || len(result.Nodes) != 2 || len(result.Edges) != 1 || result.Edges[0].Traversal != graphdomain.EdgeTraversalForward {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if err := graphdomain.ValidatePathResult(request, result); err != nil {
		t.Fatal(err)
	}
	request.From = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID}
	request.To = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.secondaryTopicID}
	request.Direction = graphdomain.TraversalBoth
	request.RelationTypes = []knowledge.RelationType{knowledge.RelationImpacts}
	result, err = repository.FindPath(ctx, request)
	if err != nil || result.Status != graphdomain.PathNotFound || len(result.Nodes) != 0 || len(result.Edges) != 0 {
		t.Fatalf("not found=%#v err=%v", result, err)
	}
	request.From = knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID}
	request.To = knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.longClaimID}
	result, err = repository.FindPath(ctx, request)
	if err != nil || result.Status != graphdomain.PathNotFound || len(result.CommonTopicSuggestions) != 1 || result.CommonTopicSuggestions[0].Ref.ID != fixture.primaryTopicID {
		t.Fatalf("common topics=%#v err=%v", result, err)
	}

	request.From = knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID}
	request.To = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.secondaryTopicID}
	request.Direction = graphdomain.TraversalOutbound
	request.RelationTypes = []knowledge.RelationType{knowledge.RelationSupports, knowledge.RelationBelongsTo}
	request.MaxDepth = 1
	request.MaxVisited = 20
	result, err = repository.FindPath(ctx, request)
	if err != nil || result.Status != graphdomain.PathNotFound {
		t.Fatalf("max depth one=%#v err=%v", result, err)
	}
	request.MaxDepth = 2
	result, err = repository.FindPath(ctx, request)
	wantMiddle := left
	leftKey := fmt.Sprintf("CLAIM\x00%s\x00%s\x00CLAIM\x00%s\x00%s\x00TOPIC\x00%s", fixture.firstClaimID, leftSupport, left, leftMembership, fixture.secondaryTopicID)
	rightKey := fmt.Sprintf("CLAIM\x00%s\x00%s\x00CLAIM\x00%s\x00%s\x00TOPIC\x00%s", fixture.firstClaimID, rightSupport, right, rightMembership, fixture.secondaryTopicID)
	if rightKey < leftKey {
		wantMiddle = right
	}
	if err != nil || result.Status != graphdomain.PathFound || result.HopCount != 2 || len(result.Nodes) != 3 || result.Nodes[1].Ref().ID != wantMiddle {
		t.Fatalf("diamond=%#v want_middle=%s err=%v", result, wantMiddle, err)
	}

	request.From, request.To = request.To, request.From
	request.Direction = graphdomain.TraversalInbound
	result, err = repository.FindPath(ctx, request)
	if err != nil || result.Status != graphdomain.PathFound || result.HopCount != 2 || result.Edges[0].Traversal != graphdomain.EdgeTraversalReverse || result.Edges[1].Traversal != graphdomain.EdgeTraversalReverse {
		t.Fatalf("inbound=%#v err=%v", result, err)
	}
	request.Direction = graphdomain.TraversalOutbound
	result, err = repository.FindPath(ctx, request)
	if err != nil || result.Status != graphdomain.PathNotFound {
		t.Fatalf("reverse outbound=%#v err=%v", result, err)
	}
	request.From = knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: duplicateTarget}
	request.To = knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: duplicateSource}
	request.Direction = graphdomain.TraversalOutbound
	request.RelationTypes = []knowledge.RelationType{knowledge.RelationDuplicates}
	request.MaxDepth = 1
	result, err = repository.FindPath(ctx, request)
	if err != nil || result.Status != graphdomain.PathFound || result.HopCount != 1 || result.Edges[0].RelationID != duplicateRelation || result.Edges[0].Traversal != graphdomain.EdgeTraversalReverse {
		t.Fatalf("symmetric=%#v err=%v", result, err)
	}

	request.From = knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID}
	request.To = knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.secondaryTopicID}
	request.Direction = graphdomain.TraversalOutbound
	request.RelationTypes = []knowledge.RelationType{knowledge.RelationSupports, knowledge.RelationBelongsTo}
	request.MaxDepth = 2
	request.MaxVisited = 3
	if _, err := repository.FindPath(ctx, request); !hasGraphCode(err, graphdomain.ErrorCodeQueryBudgetExceeded) {
		t.Fatalf("budget err=%v", err)
	}
}

func TestRepositoryFindPathKeepsRepeatableReadSnapshotDuringConcurrentChange(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedGraphFixture(t, ctx, seedTx)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupCommittedGraphFixture(pool, fixture.workspaceID) })
	entered, release := make(chan struct{}), make(chan struct{})
	repository, err := NewRepository(&pathBarrierDB{Pool: pool, entered: entered, release: release})
	if err != nil {
		t.Fatal(err)
	}
	request := graphdomain.PathRequest{
		WorkspaceID: fixture.workspaceID,
		From:        knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID},
		To:          knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID},
		Direction:   graphdomain.TraversalOutbound,
		RelationTypes: []knowledge.RelationType{
			knowledge.RelationBelongsTo,
		},
		MaxDepth: 2, MaxVisited: 10,
	}
	type pathOutcome struct {
		result graphdomain.PathResult
		err    error
	}
	outcome := make(chan pathOutcome, 1)
	go func() {
		result, queryErr := repository.FindPath(ctx, request)
		outcome <- pathOutcome{result: result, err: queryErr}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("path query did not reach frontier barrier")
	}
	if _, err := pool.Exec(ctx, `UPDATE core.relation SET status='STALE',version=version+1,updated_at=updated_at+interval '1 second' WHERE workspace_id=$1 AND id=$2`, string(fixture.workspaceID), string(fixture.firstMembershipID)); err != nil {
		t.Fatal(err)
	}
	close(release)
	first := <-outcome
	if first.err != nil || first.result.Status != graphdomain.PathFound || first.result.HopCount != 1 {
		t.Fatalf("snapshot result=%#v err=%v", first.result, first.err)
	}
	freshRepository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	second, err := freshRepository.FindPath(ctx, request)
	if err != nil || second.Status != graphdomain.PathNotFound {
		t.Fatalf("fresh result=%#v err=%v", second, err)
	}
}

func TestRepositoryFindPathReturnsFrontierCancellationAndTimeoutWithoutPartial(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedGraphFixture(t, ctx, seedTx)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupCommittedGraphFixture(pool, fixture.workspaceID) })
	request := graphdomain.PathRequest{
		WorkspaceID: fixture.workspaceID,
		From:        knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: fixture.firstClaimID},
		To:          knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: fixture.primaryTopicID},
		Direction:   graphdomain.TraversalOutbound,
		MaxDepth:    2,
		MaxVisited:  10,
	}
	for _, testCase := range []struct {
		name string
		err  error
		code string
	}{
		{name: "cancel", err: context.Canceled, code: graphdomain.ErrorCodeDependencyUnavailable},
		{name: "deadline", err: context.DeadlineExceeded, code: graphdomain.ErrorCodeQueryTimeout},
		{name: "statement timeout", err: &pgconn.PgError{Code: "57014", Message: "statement timeout"}, code: graphdomain.ErrorCodeQueryTimeout},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repository, createErr := NewRepository(&pathFrontierErrorDB{Pool: pool, frontierErr: testCase.err})
			if createErr != nil {
				t.Fatal(createErr)
			}
			result, queryErr := repository.FindPath(ctx, request)
			if !hasGraphCode(queryErr, testCase.code) || result.Status != "" || len(result.Nodes) != 0 || len(result.Edges) != 0 {
				t.Fatalf("result=%#v err=%v", result, queryErr)
			}
		})
	}
}

func TestRepositoryRelationEvidenceWindowPaginatesLazyProvenance(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	var sourceVersionID, sourceSpanID string
	var applicability []byte
	var schemaVersion, applicabilityHash string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text,applicability,applicability_schema_version,applicability_hash FROM core.relation_evidence WHERE workspace_id=$1 AND relation_id=$2 LIMIT 1`, string(fixture.workspaceID), string(fixture.supportRelationID)).Scan(&sourceVersionID, &sourceSpanID, &applicability, &schemaVersion, &applicabilityHash); err != nil {
		t.Fatal(err)
	}
	insertEvidence := func(reason string, createdAt time.Time) {
		t.Helper()
		if _, err := tx.Exec(ctx, `INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'SOURCE_DERIVED','fixture',$11)`, string(graphTestID(t)), string(fixture.workspaceID), string(fixture.supportRelationID), sourceVersionID, sourceSpanID, reason, graphHash(reason+createdAt.String()), applicability, schemaVersion, applicabilityHash, createdAt); err != nil {
			t.Fatal(err)
		}
	}
	insertEvidence("second evidence", time.Now().UTC())
	codec, err := graphapp.NewCursorCodec(bytes.Repeat([]byte{0x3e}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := graphapp.NewService(repository, codec)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.RelationEvidencePage(ctx, graphapp.RelationEvidencePageRequest{WorkspaceID: fixture.workspaceID, RelationID: fixture.supportRelationID, Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Meta.NextCursor == "" || first.Items[0].Reason == "" || first.Items[0].Applicability.SchemaVersion == "" || first.Items[0].Confirmation == nil || first.Items[0].SourceHref != "/api/v1/workspaces/"+string(fixture.workspaceID)+"/source-versions/"+sourceVersionID || first.Items[0].SpanHref != first.Items[0].SourceHref+"/spans/"+sourceSpanID {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := service.RelationEvidencePage(ctx, graphapp.RelationEvidencePageRequest{WorkspaceID: fixture.workspaceID, RelationID: fixture.supportRelationID, Limit: 1, Cursor: first.Meta.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Meta.NextCursor != "" || !second.Meta.Complete || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	insertEvidence("third evidence", time.Now().UTC().Add(time.Second))
	if _, err := service.RelationEvidencePage(ctx, graphapp.RelationEvidencePageRequest{WorkspaceID: fixture.workspaceID, RelationID: fixture.supportRelationID, Limit: 1, Cursor: first.Meta.NextCursor}); !hasGraphCode(err, graphdomain.ErrorCodeCursorStale) {
		t.Fatalf("stale err=%v", err)
	}
	otherWorkspace := graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'evidence other',$2,$2,$3,'test',1,$3,$3)`, string(otherWorkspace), "/tmp/graph-evidence-other-"+string(otherWorkspace), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RelationEvidenceWindow(ctx, otherWorkspace, fixture.supportRelationID); !hasGraphCode(err, graphdomain.ErrorCodeRelationNotFound) {
		t.Fatalf("cross workspace err=%v", err)
	}
	suggestedRelation := seedGraphRelation(t, ctx, tx, fixture.workspaceID, fixture.firstClaimID, knowledge.NodeTypeClaim, fixture.longClaimID, knowledge.NodeTypeClaim, knowledge.RelationComplements, knowledge.RelationStatusSuggested, nil, time.Now().UTC())
	if _, err := repository.RelationEvidenceWindow(ctx, fixture.workspaceID, suggestedRelation); !hasGraphCode(err, graphdomain.ErrorCodeRelationNotFound) {
		t.Fatalf("non-formal relation err=%v", err)
	}
}

func TestRepositoryRelationEvidenceWindowTruncatesAtFiveHundred(t *testing.T) {
	repository, tx, ctx := graphIntegrationRepository(t)
	fixture := seedGraphFixture(t, ctx, tx)
	var sourceVersionID, sourceSpanID string
	var applicability []byte
	var schemaVersion, applicabilityHash string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text,applicability,applicability_schema_version,applicability_hash FROM core.relation_evidence WHERE workspace_id=$1 AND relation_id=$2 LIMIT 1`, string(fixture.workspaceID), string(fixture.supportRelationID)).Scan(&sourceVersionID, &sourceSpanID, &applicability, &schemaVersion, &applicabilityHash); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	batch := &pgx.Batch{}
	for index := 0; index < graphapp.MaxResultWindowItems; index++ {
		batch.Queue(`INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'SOURCE_DERIVED','fixture',$11)`, string(graphTestID(t)), string(fixture.workspaceID), string(fixture.supportRelationID), sourceVersionID, sourceSpanID, fmt.Sprintf("window evidence %03d", index), graphHash(fmt.Sprintf("relation-window-evidence-%03d", index)), applicability, schemaVersion, applicabilityHash, now.Add(time.Duration(index)*time.Microsecond))
	}
	results := tx.SendBatch(ctx, batch)
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
	window, err := repository.RelationEvidenceWindow(ctx, fixture.workspaceID, fixture.supportRelationID)
	if err != nil || len(window.Items) != graphapp.MaxResultWindowItems || !window.Truncated || window.Reason != "RESULT_WINDOW_LIMIT" {
		t.Fatalf("items=%d truncated=%v reason=%q err=%v", len(window.Items), window.Truncated, window.Reason, err)
	}
}

type pathBarrierDB struct {
	*pgxpool.Pool
	entered chan struct{}
	release chan struct{}
}

func (database *pathBarrierDB) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := database.Pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &pathBarrierTx{Tx: tx, entered: database.entered, release: database.release}, nil
}

type pathBarrierTx struct {
	pgx.Tx
	entered chan struct{}
	release chan struct{}
	once    bool
}

func (tx *pathBarrierTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if !tx.once && query == pathFrontierSQL {
		tx.once = true
		close(tx.entered)
		select {
		case <-tx.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx.Tx.Query(ctx, query, args...)
}

type pathFrontierErrorDB struct {
	*pgxpool.Pool
	frontierErr error
}

func (database *pathFrontierErrorDB) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := database.Pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &pathFrontierErrorTx{Tx: tx, frontierErr: database.frontierErr}, nil
}

type pathFrontierErrorTx struct {
	pgx.Tx
	frontierErr error
}

func (tx *pathFrontierErrorTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if query == pathFrontierSQL {
		return nil, tx.frontierErr
	}
	return tx.Tx.Query(ctx, query, args...)
}

func cleanupCommittedGraphFixture(pool *pgxpool.Pool, workspaceID foundation.ID) {
	ctx := context.Background()
	statements := []string{
		`DELETE FROM core.relation_evidence WHERE workspace_id=$1`,
		`DELETE FROM core.relation WHERE workspace_id=$1`,
		`DELETE FROM core.claim_source WHERE workspace_id=$1`,
		`DELETE FROM core.claim WHERE workspace_id=$1`,
		`DELETE FROM core.topic_alias WHERE workspace_id=$1`,
		`DELETE FROM core.topic WHERE workspace_id=$1`,
		`DELETE FROM ingestion.canonical_chunk WHERE workspace_id=$1`,
		`DELETE FROM ingestion.source_version_projection WHERE workspace_id=$1`,
		`DELETE FROM ingestion.source_span WHERE workspace_id=$1`,
		`DELETE FROM ingestion.parse_projection WHERE workspace_id=$1`,
		`DELETE FROM core.source_version WHERE source_id IN (SELECT id FROM core.source WHERE workspace_id=$1)`,
		`DELETE FROM core.source WHERE workspace_id=$1`,
		`DELETE FROM core.content_artifact WHERE workspace_id=$1`,
		`DELETE FROM core.workspace WHERE id=$1`,
	}
	for _, statement := range statements {
		_, _ = pool.Exec(ctx, statement, string(workspaceID))
	}
}

type graphFixture struct {
	workspaceID, primaryTopicID, secondaryTopicID                   foundation.ID
	firstClaimID, longClaimID, firstMembershipID, supportRelationID foundation.ID
}

func seedGraphWorkspace(t *testing.T, ctx context.Context, tx pgx.Tx, now time.Time) foundation.ID {
	t.Helper()
	workspaceID := graphTestID(t)
	root := "/tmp/graph-" + string(workspaceID)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'graph',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), root, now); err != nil {
		t.Fatal(err)
	}
	return workspaceID
}

func seedGraphTopic(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, name, normalizedName string, now time.Time) foundation.ID {
	t.Helper()
	topicID := graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,$3,$4,'','ACTIVE',1,$5,$5)`, string(topicID), string(workspaceID), name, normalizedName, now); err != nil {
		t.Fatal(err)
	}
	return topicID
}

func seedGraphClaim(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, statement string, status knowledge.ClaimStatus, confidence *float64, now time.Time) foundation.ID {
	t.Helper()
	applicability, err := knowledge.ParseApplicability([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claimID := graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,$5,$6,'SUGGESTED',$7,'{}',$8,1,$9,$9)`, string(claimID), string(workspaceID), statement, string(applicability.CanonicalJSON), applicability.SchemaVersion, applicability.Hash, confidence, graphHash("claim-"+string(claimID)), now); err != nil {
		t.Fatal(err)
	}
	if status != knowledge.ClaimStatusSuggested {
		if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=version+1,updated_at=updated_at+interval '1 microsecond' WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(claimID)); err != nil {
			t.Fatal(err)
		}
		if status != knowledge.ClaimStatusConfirmed {
			if _, err := tx.Exec(ctx, `UPDATE core.claim SET status=$3,version=version+1,updated_at=updated_at+interval '1 microsecond' WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(claimID), string(status)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return claimID
}

func seedGraphRelation(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, sourceID foundation.ID, sourceType knowledge.NodeType, targetID foundation.ID, targetType knowledge.NodeType, relationType knowledge.RelationType, status knowledge.RelationStatus, confidence *float64, now time.Time) foundation.ID {
	t.Helper()
	relationID := graphTestID(t)
	var confirmationMethod, confirmationRef, evidenceFingerprint any
	storedStatus := status
	if status == knowledge.RelationStatusStale || status == knowledge.RelationStatusDeprecated {
		storedStatus = knowledge.RelationStatusConfirmed
	}
	if storedStatus == knowledge.RelationStatusConfirmed {
		confirmationMethod = string(knowledge.ConfirmationSourceDerived)
		confirmationRef = "integration fixture"
		evidenceFingerprint = graphHash("evidence-" + string(relationID))
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,1,$14,$14)`, string(relationID), string(workspaceID), string(sourceType), string(sourceID), string(targetType), string(targetID), string(relationType), string(storedStatus), confidence, graphHash("relation-"+string(relationID)), evidenceFingerprint, confirmationMethod, confirmationRef, now); err != nil {
		t.Fatal(err)
	}
	if status != storedStatus {
		if _, err := tx.Exec(ctx, `UPDATE core.relation SET status=$3,version=version+1,updated_at=updated_at+interval '1 microsecond' WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(relationID), string(status)); err != nil {
			t.Fatal(err)
		}
	}
	return relationID
}

func seedConfirmedRelationEvidence(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, sourceID, targetID foundation.ID, relationType knowledge.RelationType, now time.Time) foundation.ID {
	t.Helper()
	var sourceVersionID, sourceSpanID string
	var applicability []byte
	var schemaVersion, applicabilityHash string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text,applicability,applicability_schema_version,applicability_hash FROM core.relation_evidence WHERE workspace_id=$1 LIMIT 1`, string(workspaceID)).Scan(&sourceVersionID, &sourceSpanID, &applicability, &schemaVersion, &applicabilityHash); err != nil {
		t.Fatal(err)
	}
	relationID := graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at) VALUES($1,$2,'CLAIM',$3,'CLAIM',$4,$5,'CONFIRMED',0.9,$6,$7,'SOURCE_DERIVED','deep fixture',1,$8,$8)`, string(relationID), string(workspaceID), string(sourceID), string(targetID), string(relationType), graphHash("deep-relation-"+string(relationID)), graphHash("deep-evidence-"+string(relationID)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,'deep evidence',$6,$7,$8,$9,'SOURCE_DERIVED','deep fixture',$10)`, string(graphTestID(t)), string(workspaceID), string(relationID), sourceVersionID, sourceSpanID, graphHash("deep-evidence-row-"+string(relationID)), applicability, schemaVersion, applicabilityHash, now); err != nil {
		t.Fatal(err)
	}
	return relationID
}

func seedConfirmedClaimWithSource(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, statement string, now time.Time) foundation.ID {
	t.Helper()
	claimID := seedGraphClaim(t, ctx, tx, workspaceID, statement, knowledge.ClaimStatusSuggested, floatPointer(0.9), now)
	var sourceVersionID, sourceSpanID string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text FROM core.relation_evidence WHERE workspace_id=$1 LIMIT 1`, string(workspaceID)).Scan(&sourceVersionID, &sourceSpanID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at) VALUES($1,$2,$3,$4,$5,'SUPPORTS','path fixture',$6,$7)`, string(graphTestID(t)), string(workspaceID), string(claimID), sourceVersionID, sourceSpanID, graphHash("path-claim-source-"+string(claimID)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=version+1,updated_at=updated_at+interval '1 microsecond' WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(claimID)); err != nil {
		t.Fatal(err)
	}
	return claimID
}

func seedConfirmedClaimTopicRelationEvidence(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, claimID, topicID foundation.ID, now time.Time) foundation.ID {
	t.Helper()
	var sourceVersionID, sourceSpanID string
	var applicability []byte
	var schemaVersion, applicabilityHash string
	if err := tx.QueryRow(ctx, `SELECT source_version_id::text,source_span_id::text,applicability,applicability_schema_version,applicability_hash FROM core.relation_evidence WHERE workspace_id=$1 LIMIT 1`, string(workspaceID)).Scan(&sourceVersionID, &sourceSpanID, &applicability, &schemaVersion, &applicabilityHash); err != nil {
		t.Fatal(err)
	}
	relationID := graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at) VALUES($1,$2,'CLAIM',$3,'TOPIC',$4,'BELONGS_TO','CONFIRMED',0.9,$5,$6,'SOURCE_DERIVED','path fixture',1,$7,$7)`, string(relationID), string(workspaceID), string(claimID), string(topicID), graphHash("path-membership-"+string(relationID)), graphHash("path-membership-evidence-"+string(relationID)), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,'path membership evidence',$6,$7,$8,$9,'SOURCE_DERIVED','path fixture',$10)`, string(graphTestID(t)), string(workspaceID), string(relationID), sourceVersionID, sourceSpanID, graphHash("path-membership-row-"+string(relationID)), applicability, schemaVersion, applicabilityHash, now); err != nil {
		t.Fatal(err)
	}
	return relationID
}

func floatPointer(value float64) *float64 { return &value }

func seedGraphFixture(t *testing.T, ctx context.Context, tx pgx.Tx) graphFixture {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	workspaceID := graphTestID(t)
	root := "/tmp/graph-" + string(workspaceID)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'graph',$2,$2,$3,'test',1,$3,$3)`, string(workspaceID), root, now); err != nil {
		t.Fatal(err)
	}
	provenance := seedGraphProvenance(t, ctx, tx, workspaceID, now)
	primaryTopicID, secondaryTopicID := graphTestID(t), graphTestID(t)
	if _, err := tx.Exec(ctx, `INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at) VALUES($1,$2,'Go Concurrency','go concurrency','goroutine','ACTIVE',1,$3,$3),($4,$2,'Databases','databases','','ACTIVE',1,$3,$3)`, string(primaryTopicID), string(workspaceID), now, string(secondaryTopicID)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.topic_alias(id,workspace_id,topic_id,alias,normalized_alias,created_at) VALUES($1,$2,$3,'Golang','golang',$4)`, string(graphTestID(t)), string(workspaceID), string(primaryTopicID), now); err != nil {
		t.Fatal(err)
	}
	applicability, err := knowledge.ParseApplicability([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	firstClaimID, longClaimID := graphTestID(t), graphTestID(t)
	longStatement := "Graph " + strings.Repeat("knowledge projection ", 60)
	if _, err := tx.Exec(ctx, `INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at) VALUES
		($1,$2,'Channels coordinate goroutines','Channels coordinate goroutines',$3,$4,$5,'SUGGESTED',0.9,'{}',$6,1,$7,$7),
		($8,$2,$9,$9,$3,$4,$5,'SUGGESTED',0.8,'{}',$10,1,$7,$7)`, string(firstClaimID), string(workspaceID), string(applicability.CanonicalJSON), applicability.SchemaVersion, applicability.Hash, graphHash("claim-1"), now, string(longClaimID), longStatement, graphHash("claim-2")); err != nil {
		t.Fatal(err)
	}
	for index, claimID := range []foundation.ID{firstClaimID, longClaimID} {
		if _, err := tx.Exec(ctx, `INSERT INTO core.claim_source(id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at) VALUES($1,$2,$3,$4,$5,'SUPPORTS','fixture support',$6,$7)`, string(graphTestID(t)), string(workspaceID), string(claimID), string(provenance.sourceVersionID), string(provenance.sourceSpanID), graphHash(fmt.Sprintf("claim-source-%d", index)), now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='CONFIRMED',version=2,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(claimID), now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE core.claim SET status='DISPUTED',version=3,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(longClaimID), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	relations := []struct {
		id, source, target foundation.ID
		typeName           string
	}{
		{graphTestID(t), firstClaimID, primaryTopicID, "BELONGS_TO"},
		{graphTestID(t), longClaimID, primaryTopicID, "BELONGS_TO"},
		{graphTestID(t), firstClaimID, longClaimID, "SUPPORTS"},
	}
	for index, relation := range relations {
		if _, err := tx.Exec(ctx, `INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,version,created_at,updated_at) VALUES($1,$2,'CLAIM',$3,$4,$5,$6,'CONFIRMED',0.9,$7,$8,'SOURCE_DERIVED',$9,1,$10,$10)`, string(relation.id), string(workspaceID), string(relation.source), targetType(relation.typeName), string(relation.target), relation.typeName, graphHash(fmt.Sprintf("relation-%d", index)), graphHash(fmt.Sprintf("evidence-set-%d", index)), "fixture", now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO core.relation_evidence(id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,applicability,applicability_schema_version,applicability_hash,confirmation_method,confirmed_by,created_at) VALUES($1,$2,$3,$4,$5,'fixture evidence',$6,$7,$8,$9,'SOURCE_DERIVED','fixture',$10)`, string(graphTestID(t)), string(workspaceID), string(relation.id), string(provenance.sourceVersionID), string(provenance.sourceSpanID), graphHash(fmt.Sprintf("relation-evidence-%d", index)), string(applicability.CanonicalJSON), applicability.SchemaVersion, applicability.Hash, now.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return graphFixture{workspaceID: workspaceID, primaryTopicID: primaryTopicID, secondaryTopicID: secondaryTopicID, firstClaimID: firstClaimID, longClaimID: longClaimID, firstMembershipID: relations[0].id, supportRelationID: relations[2].id}
}

type graphProvenance struct{ sourceVersionID, sourceSpanID foundation.ID }

func seedGraphProvenance(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, now time.Time) graphProvenance {
	t.Helper()
	artifactID, sourceID, sourceVersionID := graphTestID(t), graphTestID(t), graphTestID(t)
	projectionID, sourceSpanID := graphTestID(t), graphTestID(t)
	contentHash := graphHash("content")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,4,$4,$5)`, []any{string(artifactID), string(workspaceID), contentHash, ".knowledge/sources/" + contentHash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text','graph.txt','graph.txt',$3)`, []any{string(sourceID), string(workspaceID), now}},
		{`INSERT INTO core.source_version(id,source_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,4,'text/plain','graph.txt','pending',$5)`, []any{string(sourceVersionID), string(sourceID), string(artifactID), contentHash, now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, []any{string(projectionID), string(workspaceID), string(artifactID), graphHash("parser"), graphHash("normalized"), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,4,'{}',$5,'v1','v1',$6)`, []any{string(sourceSpanID), string(workspaceID), string(artifactID), string(projectionID), graphHash("excerpt"), now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, []any{string(sourceVersionID), string(projectionID), string(workspaceID), now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return graphProvenance{sourceVersionID: sourceVersionID, sourceSpanID: sourceSpanID}
}

func targetType(relationType string) string {
	if relationType == "BELONGS_TO" {
		return "TOPIC"
	}
	return "CLAIM"
}

func graphIntegrationRepository(t *testing.T) (*Repository, pgx.Tx, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	return repository, tx, ctx
}

func graphTestID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func graphHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func hasGraphCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
