//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	graphdomain "github.com/CodeZen-Lizhi/zhixu/internal/graph/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGraphPublicHTTPIntegration(t *testing.T) {
	database := graphHTTPIntegrationPool(t)
	pool := database.DB()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	fixture, err := testfixture.SeedFunctional(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if cleanupErr := testfixture.Cleanup(cleanupCtx, pool, fixture.WorkspaceID); cleanupErr != nil {
			t.Errorf("cleanup graph fixture: %v", cleanupErr)
		}
	})
	isolationWorkspaceID := seedGraphIsolationWorkspace(t, ctx, pool)

	server := newGraphHTTPIntegrationServer(t, database, 2*time.Second)
	client := server.Client()

	globalBody := mustGraphJSON(t, map[string]any{
		"workspace_id": string(fixture.WorkspaceID),
		"limit":        1,
	})
	firstGlobal := postGraphResponse[graphGlobalWire](t, client, server.URL+"/api/v1/graph/global", globalBody, http.StatusOK)
	assertGlobalFirstPage(t, firstGlobal.Value, fixture)

	replayedGlobal := postGraphResponse[graphGlobalWire](t, client, server.URL+"/api/v1/graph/global", globalBody, http.StatusOK)
	if replayedGlobal.Value.Meta.Fingerprint != firstGlobal.Value.Meta.Fingerprint ||
		replayedGlobal.Value.Meta.NextCursor != firstGlobal.Value.Meta.NextCursor {
		t.Fatalf("response-loss replay changed result identity: first=%#v replay=%#v", firstGlobal.Value.Meta, replayedGlobal.Value.Meta)
	}
	if len(replayedGlobal.Value.Clusters) != 1 || replayedGlobal.Value.Clusters[0] != firstGlobal.Value.Clusters[0] {
		t.Fatalf("response-loss replay changed first page: first=%#v replay=%#v", firstGlobal.Value.Clusters, replayedGlobal.Value.Clusters)
	}

	secondGlobalBody := mustGraphJSON(t, map[string]any{
		"workspace_id": string(fixture.WorkspaceID),
		"limit":        1,
		"cursor":       firstGlobal.Value.Meta.NextCursor,
	})
	secondGlobal := postGraphResponse[graphGlobalWire](t, client, server.URL+"/api/v1/graph/global", secondGlobalBody, http.StatusOK)
	if secondGlobal.Value.WorkspaceID != string(fixture.WorkspaceID) || len(secondGlobal.Value.Clusters) != 1 ||
		secondGlobal.Value.Clusters[0].Topic.ID != string(fixture.SecondaryTopicID) ||
		secondGlobal.Value.Clusters[0].DirectClaimCount != 1 || secondGlobal.Value.Clusters[0].IncidentRelationCount != 3 ||
		secondGlobal.Value.Clusters[0].ClusterScore != 4 || secondGlobal.Value.Meta.NextCursor != "" ||
		!secondGlobal.Value.Meta.Complete || secondGlobal.Value.Meta.Truncated {
		t.Fatalf("unexpected second global page: %#v", secondGlobal.Value)
	}
	assertTopicNodeWire(t, secondGlobal.Value.Clusters[0].Topic, fixture.WorkspaceID, fixture.SecondaryTopicID)

	neighborhoodBody := mustGraphJSON(t, map[string]any{
		"workspace_id": string(fixture.WorkspaceID),
		"center": map[string]any{
			"type": string(knowledge.NodeTypeTopic),
			"id":   string(fixture.PrimaryTopicID),
		},
		"depth":        1,
		"limit":        25,
		"max_nodes":    20,
		"max_edges":    20,
		"max_frontier": 20,
	})
	neighborhood := postGraphResponse[graphNeighborhoodWire](t, client, server.URL+"/api/v1/graph/neighborhood", neighborhoodBody, http.StatusOK)
	assertNeighborhoodClosure(t, neighborhood.Value, fixture)

	pathBody := mustGraphJSON(t, map[string]any{
		"workspace_id": string(fixture.WorkspaceID),
		"from": map[string]any{
			"type": string(knowledge.NodeTypeTopic),
			"id":   string(fixture.PrimaryTopicID),
		},
		"to": map[string]any{
			"type": string(knowledge.NodeTypeTopic),
			"id":   string(fixture.SecondaryTopicID),
		},
		"direction":   string(graphdomain.TraversalBoth),
		"max_depth":   4,
		"max_visited": 20,
	})
	path := postGraphResponse[graphPathWire](t, client, server.URL+"/api/v1/graph/path", pathBody, http.StatusOK)
	assertGraphPath(t, path.Value, fixture)

	evidenceURL, err := url.Parse(path.Value.Edges[1].EvidenceHref)
	if err != nil || evidenceURL.IsAbs() || evidenceURL.Path == "" {
		t.Fatalf("invalid evidence href %q: %v", path.Value.Edges[1].EvidenceHref, err)
	}
	evidence := getGraphResponse[graphEvidencePageWire](t, client, server.URL+evidenceURL.String(), http.StatusOK)
	assertGraphEvidence(t, evidence.Value, fixture, path.Value.Edges[1])

	crossWorkspacePath := fmt.Sprintf("%s/api/v1/graph/relations/%s?workspace_id=%s", server.URL, fixture.SupportRelationID, isolationWorkspaceID)
	crossWorkspace := getGraphResponse[graphProblemWire](t, client, crossWorkspacePath, http.StatusNotFound)
	assertGraphProblem(t, crossWorkspace.Value, graphdomain.ErrorCodeRelationNotFound, false)
	missingRelationID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	missingPath := fmt.Sprintf("%s/api/v1/graph/relations/%s?workspace_id=%s", server.URL, missingRelationID, fixture.WorkspaceID)
	missing := getGraphResponse[graphProblemWire](t, client, missingPath, http.StatusNotFound)
	assertGraphProblem(t, missing.Value, graphdomain.ErrorCodeRelationNotFound, false)
	if crossWorkspace.Value.ErrorCode != missing.Value.ErrorCode || crossWorkspace.Value.Message != missing.Value.Message ||
		crossWorkspace.Value.Retryable != missing.Value.Retryable {
		t.Fatalf("cross-workspace and missing relations diverged: cross=%#v missing=%#v", crossWorkspace.Value, missing.Value)
	}

	if _, err := pool.Exec(ctx, `UPDATE core.topic
		SET description=description || ' changed', version=version+1, updated_at=updated_at+interval '1 second'
		WHERE workspace_id=$1 AND id=$2`, string(fixture.WorkspaceID), string(fixture.SecondaryTopicID)); err != nil {
		t.Fatal(err)
	}
	stale := postGraphResponse[graphProblemWire](t, client, server.URL+"/api/v1/graph/global", secondGlobalBody, http.StatusConflict)
	assertGraphProblem(t, stale.Value, graphdomain.ErrorCodeCursorStale, false)

	timeoutServer := newGraphHTTPIntegrationServer(t, database, time.Nanosecond)
	var stableTimeoutCode string
	for attempt := 0; attempt < 3; attempt++ {
		problem := postGraphResponse[graphProblemWire](t, timeoutServer.Client(), timeoutServer.URL+"/api/v1/graph/global", globalBody, http.StatusServiceUnavailable)
		if problem.Value.ErrorCode != graphdomain.ErrorCodeQueryTimeout && problem.Value.ErrorCode != graphdomain.ErrorCodeQueryCanceled {
			t.Fatalf("short timeout code=%q, want timeout or cancelled", problem.Value.ErrorCode)
		}
		if stableTimeoutCode == "" {
			stableTimeoutCode = problem.Value.ErrorCode
		} else if problem.Value.ErrorCode != stableTimeoutCode {
			t.Fatalf("short timeout error code drifted: first=%q attempt_%d=%q", stableTimeoutCode, attempt+1, problem.Value.ErrorCode)
		}
		assertGraphProblem(t, problem.Value, problem.Value.ErrorCode, problem.Value.ErrorCode == graphdomain.ErrorCodeQueryTimeout)
	}
}

type graphHTTPResponse[T any] struct {
	Value T
}

type graphNodeRefWire struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type graphApplicabilityWire struct {
	SchemaVersion string          `json:"schema_version"`
	Value         json.RawMessage `json:"value"`
	Hash          string          `json:"hash"`
}

type graphNodeWire struct {
	Type          string                  `json:"type"`
	ID            string                  `json:"id"`
	WorkspaceID   string                  `json:"workspace_id"`
	Name          *string                 `json:"name,omitempty"`
	Description   *string                 `json:"description,omitempty"`
	TopicStatus   *string                 `json:"topic_status,omitempty"`
	Statement     *string                 `json:"statement,omitempty"`
	ClaimStatus   *string                 `json:"claim_status,omitempty"`
	Confidence    *float64                `json:"confidence,omitempty"`
	Applicability *graphApplicabilityWire `json:"applicability,omitempty"`
	Version       int64                   `json:"version"`
	UpdatedAt     string                  `json:"updated_at"`
}

type graphEdgeWire struct {
	RelationID          string           `json:"relation_id"`
	WorkspaceID         string           `json:"workspace_id"`
	Source              graphNodeRefWire `json:"source"`
	Target              graphNodeRefWire `json:"target"`
	Type                string           `json:"type"`
	Status              string           `json:"status"`
	Traversal           string           `json:"traversal"`
	Confidence          *float64         `json:"confidence"`
	Version             int64            `json:"version"`
	EvidenceCount       int              `json:"evidence_count"`
	EvidenceFingerprint string           `json:"evidence_fingerprint"`
	EvidenceHref        string           `json:"evidence_href"`
	UpdatedAt           string           `json:"updated_at"`
}

type graphPageMetaWire struct {
	Fingerprint string `json:"fingerprint"`
	NextCursor  string `json:"next_cursor,omitempty"`
	Complete    bool   `json:"complete"`
	Truncated   bool   `json:"truncated"`
	Reason      string `json:"reason,omitempty"`
}

type graphTopicWire struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	TopicStatus string `json:"topic_status"`
	Version     int64  `json:"version"`
	UpdatedAt   string `json:"updated_at"`
}

type graphGlobalClusterWire struct {
	Topic                 graphTopicWire `json:"topic"`
	DirectClaimCount      int            `json:"direct_claim_count"`
	IncidentRelationCount int            `json:"incident_relation_count"`
	ClusterScore          int            `json:"cluster_score"`
	UpdatedAt             string         `json:"updated_at"`
}

type graphGlobalWire struct {
	WorkspaceID string                   `json:"workspace_id"`
	Clusters    []graphGlobalClusterWire `json:"clusters"`
	Meta        graphPageMetaWire        `json:"meta"`
}

type graphNeighborhoodWire struct {
	WorkspaceID    string             `json:"workspace_id"`
	Center         graphNodeRefWire   `json:"center"`
	Nodes          []graphNodeWire    `json:"nodes"`
	Edges          []graphEdgeWire    `json:"edges"`
	BoundaryNodes  []graphNodeRefWire `json:"boundary_nodes"`
	LayerCounts    []int              `json:"layer_counts"`
	CompletedDepth int                `json:"completed_depth"`
	Meta           graphPageMetaWire  `json:"meta"`
}

type graphPathWire struct {
	WorkspaceID            string           `json:"workspace_id"`
	From                   graphNodeRefWire `json:"from"`
	To                     graphNodeRefWire `json:"to"`
	Status                 string           `json:"status"`
	Nodes                  []graphNodeWire  `json:"nodes"`
	Edges                  []graphEdgeWire  `json:"edges"`
	HopCount               int              `json:"hop_count"`
	ExploredNodes          int              `json:"explored_nodes"`
	CommonTopicSuggestions []graphTopicWire `json:"common_topic_suggestions"`
}

type graphConfirmationWire struct {
	Method    string `json:"method"`
	Reference string `json:"reference"`
}

type graphProvenanceWire struct {
	WorkspaceID     string `json:"workspace_id"`
	SourceVersionID string `json:"source_version_id"`
	SourceSpanID    string `json:"source_span_id"`
}

type graphEvidenceItemWire struct {
	ID            string                 `json:"id"`
	WorkspaceID   string                 `json:"workspace_id"`
	RelationID    string                 `json:"relation_id"`
	Provenance    graphProvenanceWire    `json:"provenance"`
	Reason        string                 `json:"reason"`
	Applicability graphApplicabilityWire `json:"applicability"`
	Confirmation  *graphConfirmationWire `json:"confirmation"`
	SourceHref    string                 `json:"source_href"`
	SpanHref      string                 `json:"span_href"`
	CreatedAt     string                 `json:"created_at"`
}

type graphEvidencePageWire struct {
	WorkspaceID string                  `json:"workspace_id"`
	RelationID  string                  `json:"relation_id"`
	Items       []graphEvidenceItemWire `json:"items"`
	Meta        graphPageMetaWire       `json:"meta"`
}

type graphProblemWire struct {
	ErrorCode     string                     `json:"error_code"`
	Message       string                     `json:"message"`
	Retryable     bool                       `json:"retryable"`
	WorkflowRunID string                     `json:"workflow_run_id,omitempty"`
	Details       map[string]json.RawMessage `json:"details,omitempty"`
}

func graphHTTPIntegrationPool(t *testing.T) *platformpostgres.Pool {
	t.Helper()
	return testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		MaxConns:         8,
	}).Pool()
}

func seedGraphIsolationWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool) foundation.ID {
	t.Helper()
	workspaceID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	root := "/tmp/graph-http-isolation-" + string(workspaceID)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(workspaceID), "graph-http-isolation", root, now); err != nil {
		t.Fatal(err)
	}
	// testdb removes the isolated database; Workspace identities are retained rows.
	return workspaceID
}

func newGraphHTTPIntegrationServer(t *testing.T, pool *platformpostgres.Pool, timeout time.Duration) *httptest.Server {
	t.Helper()
	handler, err := newGraphHandler(pool, timeout)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.NewRouter(app.Dependencies{
		Version:  "graph-integration",
		Database: pool,
		Graph:    handler,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)
	return server
}

func postGraphResponse[T any](t *testing.T, client *http.Client, target string, body []byte, wantStatus int) graphHTTPResponse[T] {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	return doGraphResponse[T](t, client, request, wantStatus)
}

func getGraphResponse[T any](t *testing.T, client *http.Client, target string, wantStatus int) graphHTTPResponse[T] {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return doGraphResponse[T](t, client, request, wantStatus)
}

func doGraphResponse[T any](t *testing.T, client *http.Client, request *http.Request, wantStatus int) graphHTTPResponse[T] {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 2*1024*1024 {
		t.Fatal("graph response exceeds integration test limit")
	}
	assertGraphResponseRedaction(t, body)
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", request.Method, request.URL, response.StatusCode, wantStatus, body)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		t.Fatalf("content-type=%q: %v", response.Header.Get("Content-Type"), err)
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 2 * 1024 * 1024
	value, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		t.Fatalf("strictly decode graph response: %v; body=%s", err, body)
	}
	return graphHTTPResponse[T]{Value: value}
}

func mustGraphJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertGraphResponseRedaction(t *testing.T, body []byte) {
	t.Helper()
	for _, forbidden := range [][]byte{
		[]byte("/tmp/"),
		[]byte("managed_location"),
		[]byte("root_path"),
		[]byte("postgres://"),
		[]byte("graph integration provenance"),
		[]byte(`"excerpt"`),
	} {
		if bytes.Contains(body, forbidden) {
			t.Fatalf("graph response exposed forbidden internal data %q", forbidden)
		}
	}
}

func assertGlobalFirstPage(t *testing.T, page graphGlobalWire, fixture testfixture.Fixture) {
	t.Helper()
	if page.WorkspaceID != string(fixture.WorkspaceID) || len(page.Clusters) != 1 {
		t.Fatalf("unexpected first global page: %#v", page)
	}
	cluster := page.Clusters[0]
	assertTopicNodeWire(t, cluster.Topic, fixture.WorkspaceID, fixture.PrimaryTopicID)
	if cluster.DirectClaimCount != 2 || cluster.IncidentRelationCount != 4 || cluster.ClusterScore != 6 {
		t.Fatalf("unexpected primary cluster counts: %#v", cluster)
	}
	if len(page.Meta.Fingerprint) != 64 || page.Meta.NextCursor == "" || page.Meta.Complete || page.Meta.Truncated || page.Meta.Reason != "" {
		t.Fatalf("unexpected first global meta: %#v", page.Meta)
	}
}

func assertTopicNodeWire(t *testing.T, node graphTopicWire, workspaceID, topicID foundation.ID) {
	t.Helper()
	if node.Type != string(knowledge.NodeTypeTopic) || node.ID != string(topicID) || node.WorkspaceID != string(workspaceID) ||
		node.Name == "" || node.Description == "" || node.TopicStatus != string(knowledge.TopicStatusActive) || node.Version < 1 ||
		timeValueInvalid(node.UpdatedAt) {
		t.Fatalf("invalid topic node: %#v", node)
	}
}

func assertNeighborhoodClosure(t *testing.T, result graphNeighborhoodWire, fixture testfixture.Fixture) {
	t.Helper()
	wantCenter := graphNodeRefWire{Type: string(knowledge.NodeTypeTopic), ID: string(fixture.PrimaryTopicID)}
	if result.WorkspaceID != string(fixture.WorkspaceID) || result.Center != wantCenter || result.CompletedDepth != 1 ||
		len(result.LayerCounts) != 1 || result.LayerCounts[0] != 2 || len(result.Nodes) != 3 || len(result.Edges) != 2 ||
		len(result.BoundaryNodes) != 0 || !result.Meta.Complete || result.Meta.Truncated || len(result.Meta.Fingerprint) != 64 {
		t.Fatalf("unexpected neighborhood: %#v", result)
	}
	nodes := make(map[graphNodeRefWire]struct{}, len(result.Nodes))
	for _, node := range result.Nodes {
		assertGraphNodeWire(t, node, fixture.WorkspaceID)
		ref := graphNodeRefWire{Type: node.Type, ID: node.ID}
		if _, exists := nodes[ref]; exists {
			t.Fatalf("duplicate neighborhood node: %#v", ref)
		}
		nodes[ref] = struct{}{}
	}
	for _, ref := range []graphNodeRefWire{
		wantCenter,
		{Type: string(knowledge.NodeTypeClaim), ID: string(fixture.FirstClaimID)},
		{Type: string(knowledge.NodeTypeClaim), ID: string(fixture.SecondClaimID)},
	} {
		if _, exists := nodes[ref]; !exists {
			t.Fatalf("neighborhood is missing node %#v: %#v", ref, result.Nodes)
		}
	}
	foundKnownRelation := false
	for _, edge := range result.Edges {
		assertGraphEdgeWire(t, edge, fixture.WorkspaceID)
		if _, exists := nodes[edge.Source]; !exists {
			t.Fatalf("edge source outside closure: %#v", edge)
		}
		if _, exists := nodes[edge.Target]; !exists {
			t.Fatalf("edge target outside closure: %#v", edge)
		}
		if edge.Type != string(knowledge.RelationBelongsTo) || edge.Target != wantCenter || edge.Traversal != string(graphdomain.EdgeTraversalReverse) {
			t.Fatalf("unexpected neighborhood edge: %#v", edge)
		}
		if edge.RelationID == string(fixture.MembershipRelationID) {
			foundKnownRelation = true
		}
	}
	if !foundKnownRelation {
		t.Fatalf("neighborhood omitted membership relation %s", fixture.MembershipRelationID)
	}
}

func assertGraphPath(t *testing.T, result graphPathWire, fixture testfixture.Fixture) {
	t.Helper()
	wantFrom := graphNodeRefWire{Type: string(knowledge.NodeTypeTopic), ID: string(fixture.PrimaryTopicID)}
	wantMiddle := graphNodeRefWire{Type: string(knowledge.NodeTypeClaim), ID: string(fixture.SecondClaimID)}
	wantTo := graphNodeRefWire{Type: string(knowledge.NodeTypeTopic), ID: string(fixture.SecondaryTopicID)}
	if result.WorkspaceID != string(fixture.WorkspaceID) || result.From != wantFrom || result.To != wantTo ||
		result.Status != string(graphdomain.PathFound) || result.HopCount != 2 || result.ExploredNodes < 3 ||
		len(result.Nodes) != 3 || len(result.Edges) != 2 || len(result.CommonTopicSuggestions) != 0 {
		t.Fatalf("unexpected path response: %#v", result)
	}
	wantNodes := []graphNodeRefWire{wantFrom, wantMiddle, wantTo}
	for index, node := range result.Nodes {
		assertGraphNodeWire(t, node, fixture.WorkspaceID)
		if ref := (graphNodeRefWire{Type: node.Type, ID: node.ID}); ref != wantNodes[index] {
			t.Fatalf("path node %d=%#v want=%#v", index, ref, wantNodes[index])
		}
	}
	for index, edge := range result.Edges {
		assertGraphEdgeWire(t, edge, fixture.WorkspaceID)
		traversalFrom, traversalTo := edge.Source, edge.Target
		if edge.Traversal == string(graphdomain.EdgeTraversalReverse) {
			traversalFrom, traversalTo = edge.Target, edge.Source
		}
		if traversalFrom != wantNodes[index] || traversalTo != wantNodes[index+1] {
			t.Fatalf("path edge %d is not continuous: %#v", index, edge)
		}
	}
	if result.Edges[0].Traversal != string(graphdomain.EdgeTraversalReverse) ||
		result.Edges[1].Traversal != string(graphdomain.EdgeTraversalForward) ||
		result.Edges[1].RelationID != string(fixture.SecondaryMembershipRelationID) {
		t.Fatalf("unexpected path edges: %#v", result.Edges)
	}
}

func assertGraphEvidence(t *testing.T, page graphEvidencePageWire, fixture testfixture.Fixture, edge graphEdgeWire) {
	t.Helper()
	if page.WorkspaceID != string(fixture.WorkspaceID) || page.RelationID != edge.RelationID || len(page.Items) != 1 ||
		!page.Meta.Complete || page.Meta.Truncated || page.Meta.NextCursor != "" || len(page.Meta.Fingerprint) != 64 {
		t.Fatalf("unexpected evidence page: %#v", page)
	}
	item := page.Items[0]
	if item.ID == "" || item.WorkspaceID != string(fixture.WorkspaceID) || item.RelationID != edge.RelationID ||
		item.Provenance.WorkspaceID != string(fixture.WorkspaceID) || item.Provenance.SourceVersionID == "" || item.Provenance.SourceSpanID == "" ||
		item.Reason != "path evidence for secondary topic membership" || item.Applicability.SchemaVersion == "" || len(item.Applicability.Hash) != 64 ||
		item.Confirmation == nil || item.Confirmation.Method != string(knowledge.ConfirmationSourceDerived) ||
		item.Confirmation.Reference != "graph integration fixture" || timeValueInvalid(item.CreatedAt) {
		t.Fatalf("unexpected evidence item: %#v", item)
	}
	wantSourceHref := fmt.Sprintf("/api/v1/workspaces/%s/source-versions/%s", fixture.WorkspaceID, item.Provenance.SourceVersionID)
	if item.SourceHref != wantSourceHref || item.SpanHref != wantSourceHref+"/spans/"+item.Provenance.SourceSpanID {
		t.Fatalf("unexpected evidence source hrefs: source=%q span=%q", item.SourceHref, item.SpanHref)
	}
}

func assertGraphNodeWire(t *testing.T, node graphNodeWire, workspaceID foundation.ID) {
	t.Helper()
	if node.ID == "" || node.WorkspaceID != string(workspaceID) || node.Version < 1 || timeValueInvalid(node.UpdatedAt) {
		t.Fatalf("invalid graph node: %#v", node)
	}
	switch node.Type {
	case string(knowledge.NodeTypeTopic):
		if node.Name == nil || *node.Name == "" || node.Description == nil || node.TopicStatus == nil ||
			*node.TopicStatus != string(knowledge.TopicStatusActive) || node.Statement != nil || node.ClaimStatus != nil || node.Applicability != nil {
			t.Fatalf("invalid topic union: %#v", node)
		}
	case string(knowledge.NodeTypeClaim):
		if node.Statement == nil || *node.Statement == "" || node.ClaimStatus == nil ||
			(*node.ClaimStatus != string(knowledge.ClaimStatusConfirmed) && *node.ClaimStatus != string(knowledge.ClaimStatusDisputed)) ||
			node.Confidence == nil || node.Applicability == nil || node.Name != nil || node.Description != nil || node.TopicStatus != nil {
			t.Fatalf("invalid claim union: %#v", node)
		}
	default:
		t.Fatalf("unknown graph node type: %#v", node)
	}
}

func assertGraphEdgeWire(t *testing.T, edge graphEdgeWire, workspaceID foundation.ID) {
	t.Helper()
	wantEvidenceHref := fmt.Sprintf("/api/v1/graph/relations/%s/evidence?workspace_id=%s", edge.RelationID, workspaceID)
	if edge.RelationID == "" || edge.WorkspaceID != string(workspaceID) || edge.Source.ID == "" || edge.Target.ID == "" ||
		edge.Status != string(knowledge.RelationStatusConfirmed) ||
		(edge.Traversal != string(graphdomain.EdgeTraversalForward) && edge.Traversal != string(graphdomain.EdgeTraversalReverse)) ||
		edge.Confidence == nil || edge.Version < 1 || edge.EvidenceCount != 1 || len(edge.EvidenceFingerprint) != 64 ||
		edge.EvidenceHref != wantEvidenceHref || timeValueInvalid(edge.UpdatedAt) {
		t.Fatalf("invalid graph edge: %#v", edge)
	}
}

func assertGraphProblem(t *testing.T, problem graphProblemWire, code string, retryable bool) {
	t.Helper()
	if problem.ErrorCode != code || problem.Message == "" || problem.Retryable != retryable || problem.WorkflowRunID != "" || len(problem.Details) != 0 {
		t.Fatalf("unexpected graph problem: %#v", problem)
	}
}

func timeValueInvalid(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err != nil || parsed.Location() != time.UTC
}
