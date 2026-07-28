//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	artifactlearningpath "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/learningpath"
	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	learningpathartifact "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/adapter/artifact"
	learningpathpostgres "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/adapter/postgres"
	learningpathapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReviewLearningPathProductionCompositionCreatesDraftPostgreSQL(t *testing.T) {
	pool := newArtifactHTTPTestDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	workspace := seedArtifactHTTPWorkspace(t, ctx, pool, "review-learning-path-production")
	evidence := seedArtifactHTTPEvidenceIndex(t, ctx, pool, workspace, []artifactHTTPEvidenceSpec{{
		label: "review-learning-path", content: []byte("Formal evidence supports the Review learning path."),
	}})[0]
	claimID := seedArtifactHTTPConfirmedClaim(t, ctx, pool, evidence)
	var evidenceHash string
	if err := pool.QueryRow(ctx, `SELECT evidence_hash FROM core.claim_source
		WHERE workspace_id=$1 AND claim_id=$2 AND source_version_id=$3 AND source_span_id=$4`,
		string(workspace.id), string(claimID), string(evidence.sourceVersionID), string(evidence.sourceSpanID)).Scan(&evidenceHash); err != nil {
		t.Fatal(err)
	}
	server := newReviewLearningPathIntegrationServer(t, pool)
	client := server.Client()

	deck := postArtifactHTTP[map[string]any](t, client, server.URL+"/api/v1/review/decks", "review-path-deck", map[string]any{
		"workspace_id": string(workspace.id), "name": "Review Path", "scope": map[string]any{}, "daily_limit": 5,
	}, http.StatusCreated, workspace.root)
	deckID := reviewPathString(t, deck, "id")
	card := postArtifactHTTP[map[string]any](t, client, server.URL+"/api/v1/review/decks/"+deckID+"/cards", "review-path-card", map[string]any{
		"workspace_id": string(workspace.id), "claim_id": string(claimID),
		"question": "Explain the formal evidence.", "answer_points": []string{"formal fixture answer point"},
		"evidence": []map[string]any{{
			"schema_version": "review-evidence/v1", "claim_id": string(claimID),
			"source_version_id": string(evidence.sourceVersionID), "source_span_id": string(evidence.sourceSpanID),
			"evidence_hash": evidenceHash,
		}},
		"card_type": "SHORT_ANSWER", "difficulty": 0.5, "model_version": "review-path-integration/v1",
	}, http.StatusCreated, workspace.root)
	cardID := reviewPathString(t, card, "id")
	cardVersion := reviewPathInt64(t, card, "version")
	postArtifactHTTP[map[string]any](t, client, server.URL+"/api/v1/review/cards/"+cardID+"/approve", "review-path-approve", map[string]any{
		"workspace_id": string(workspace.id), "expected_version": cardVersion,
	}, http.StatusCreated, workspace.root)
	session := postArtifactHTTP[map[string]any](t, client, server.URL+"/api/v1/review/sessions", "review-path-session", map[string]any{
		"workspace_id": string(workspace.id), "deck_id": deckID, "session_type": "REVIEW", "config": map[string]any{},
	}, http.StatusCreated, workspace.root)
	sessionID := reviewPathString(t, session, "id")
	dueURL := server.URL + "/api/v1/review/due?" + url.Values{
		"workspace_id": {string(workspace.id)}, "session_id": {sessionID}, "deck_id": {deckID}, "limit": {"5"},
	}.Encode()
	due := getArtifactHTTP[map[string]any](t, client, dueURL, http.StatusOK, workspace.root)
	items := reviewPathArray(t, due, "items")
	if len(items) != 1 {
		t.Fatalf("due items=%d want=1", len(items))
	}
	dueItem := reviewPathObject(t, items[0], "due item")
	questionRef := reviewPathString(t, dueItem, "question_ref")
	answer := postArtifactHTTP[map[string]any](t, client, server.URL+"/api/v1/review/sessions/"+sessionID+"/answers", "review-path-answer", map[string]any{
		"workspace_id": string(workspace.id), "card_id": cardID, "question_ref": questionRef,
		"user_answer": "bounded learner response", "rating": 3,
	}, http.StatusCreated, workspace.root)
	answerID := reviewPathString(t, reviewPathObject(t, answer["answer"], "answer"), "id")

	service := newReviewLearningPathIntegrationService(t, pool)
	created, err := service.CreateForReview(ctx, learningpathapplication.CreateReviewCommand{
		WorkspaceID: workspace.id, ReviewAnswerID: foundation.ID(answerID), IdempotencyKey: "review-path-create",
	})
	if err != nil {
		t.Fatalf("create review learning path: %v", err)
	}
	if created.Path.OriginType != "REVIEW" || created.Path.ReviewAnswerID == nil || *created.Path.ReviewAnswerID != foundation.ID(answerID) {
		t.Fatalf("review path binding=%#v answer=%s", created.Path, answerID)
	}
	if len(created.Steps) == 0 {
		t.Fatal("review learning path did not create any steps")
	}
	var holdCount int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM learning.artifact_visibility_hold
		WHERE workspace_id=$1 AND artifact_id=$2`, string(workspace.id), string(created.Path.Artifact.ArtifactID)).Scan(&holdCount); err != nil {
		t.Fatal(err)
	}
	if holdCount != 0 {
		t.Fatalf("completed review path retained %d visibility holds", holdCount)
	}
}

func newReviewLearningPathIntegrationServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	review, err := newReviewHandler(pool, 10*time.Second, reviewCompositionQuestionRefKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.NewRouter(app.Dependencies{
		Version: "review-learning-path-integration", Database: pool, Review: review,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)
	return server
}

func newReviewLearningPathIntegrationService(t *testing.T, pool *pgxpool.Pool) *learningpathapplication.Service {
	t.Helper()
	workspaces, err := workspacepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	files := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	repository, err := learningpathpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	artifactRepository, err := artifactpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := newArtifactCitationVerifier(pool, workspaces, files)
	if err != nil {
		t.Fatal(err)
	}
	commands, err := artifactapplication.NewCommandService(artifactapplication.Dependencies{
		Repository: artifactRepository, Evidence: verifier, IDs: foundation.NewUUIDGenerator(nil), Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := artifactlearningpath.NewCreator(commands)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := learningpathartifact.NewBridge(creator)
	if err != nil {
		t.Fatal(err)
	}
	service, err := learningpathapplication.NewService(learningpathapplication.Dependencies{
		Store: repository, ArtifactBridge: bridge, Clock: foundation.SystemClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func reviewPathString(t *testing.T, value map[string]any, key string) string {
	t.Helper()
	result, ok := value[key].(string)
	if !ok || result == "" {
		t.Fatalf("%s is not a non-empty string in %#v", key, value)
	}
	return result
}

func reviewPathInt64(t *testing.T, value map[string]any, key string) int64 {
	t.Helper()
	number, ok := value[key].(json.Number)
	if !ok {
		t.Fatalf("%s is not a number in %#v", key, value)
	}
	result, err := number.Int64()
	if err != nil {
		t.Fatalf("%s is not an integer: %v", key, err)
	}
	return result
}

func reviewPathObject(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object: %#v", label, value)
	}
	return result
}

func reviewPathArray(t *testing.T, value map[string]any, key string) []any {
	t.Helper()
	result, ok := value[key].([]any)
	if !ok {
		t.Fatalf("%s is not an array in %#v", key, value)
	}
	return result
}
