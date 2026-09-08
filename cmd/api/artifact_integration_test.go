//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/app"
	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	changecontrollocalfs "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/localfs"
	changecontrolpostgres "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/adapter/postgres"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/config"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/gitcli"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	workflowhttp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/http"
	workspacepostgres "github.com/CodeZen-Lizhi/zhixu/internal/workspace/adapter/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestArtifactPublicHTTPPostgreSQLIntegration(t *testing.T) {
	database := newArtifactHTTPTestDatabase(t)
	pool := database.DB()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	primary := seedArtifactHTTPWorkspace(t, ctx, pool, "artifact-http-primary")
	other := seedArtifactHTTPWorkspace(t, ctx, pool, "artifact-http-other")
	primaryEvidence := seedArtifactHTTPEvidenceIndex(t, ctx, pool, primary, []artifactHTTPEvidenceSpec{
		{label: "eligible", content: []byte("Approved knowledge supports the covered section.")},
		{label: "ineligible", content: []byte("Indexed material is not formal knowledge by itself.")},
	})
	otherEvidence := seedArtifactHTTPEvidenceIndex(t, ctx, pool, other, []artifactHTTPEvidenceSpec{
		{label: "cross-workspace", content: []byte("This evidence belongs to another workspace.")},
	})[0]
	seedArtifactHTTPConfirmedClaim(t, ctx, database, primaryEvidence[0])

	server := newArtifactHTTPIntegrationServer(t, database)
	client := server.Client()
	documentsBefore := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM core.document WHERE workspace_id=$1`, string(primary.id))

	planBody := map[string]any{
		"workspace_id":     string(primary.id),
		"type":             "study-guide",
		"title":            "Artifact HTTP integration",
		"scope_definition": "Only approved local knowledge may support content.",
	}
	planned := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts", "artifact-plan", planBody, http.StatusCreated, primary.root)
	if planned.Replayed || planned.Artifact.Status != string(artifactdomain.StatusPlanning) || planned.Artifact.Version != 1 || planned.Artifact.Revision.RevisionNo != 1 {
		t.Fatalf("planned artifact=%#v", planned)
	}
	artifactID := planned.Artifact.ID

	planReplay := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts", "artifact-plan", planBody, http.StatusOK, primary.root)
	if !planReplay.Replayed || planReplay.Artifact.ID != artifactID || planReplay.Artifact.Revision.ID != planned.Artifact.Revision.ID {
		t.Fatalf("plan replay changed identity: first=%#v replay=%#v", planned, planReplay)
	}
	conflictingPlan := cloneArtifactHTTPMap(planBody)
	conflictingPlan["title"] = "Different request"
	planConflict := postArtifactHTTP[artifactHTTPProblemWire](t, client, server.URL+"/api/v1/artifacts", "artifact-plan", conflictingPlan, http.StatusConflict, primary.root)
	assertArtifactHTTPProblem(t, planConflict, artifactapp.ErrorCodeIdempotencyConflict)

	crossWorkspaceGet := getArtifactHTTP[artifactHTTPProblemWire](t, client,
		fmt.Sprintf("%s/api/v1/artifacts/%s?workspace_id=%s", server.URL, artifactID, other.id), http.StatusNotFound, primary.root)
	assertArtifactHTTPProblem(t, crossWorkspaceGet, artifactapp.ErrorCodeNotFound)

	earlySection := artifactHTTPSectionBody(primary.id, 1, "covered", "Covered", "", nil, "GAP", []map[string]any{{"code": "NOT_READY", "description": "Outline is not approved."}})
	earlyProblem := postArtifactHTTP[artifactHTTPProblemWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections", "section-before-outline", earlySection, http.StatusConflict, primary.root)
	assertArtifactHTTPProblem(t, earlyProblem, artifactdomain.ErrorCodeArtifactTransitionInvalid)

	outlineBody := map[string]any{
		"workspace_id": string(primary.id), "expected_version": int64(1),
		"outline": []map[string]any{{"key": "covered", "title": "Covered"}, {"key": "gap", "title": "Known Gap"}},
	}
	outlined := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/outline", "submit-outline", outlineBody, http.StatusOK, primary.root)
	assertArtifactHTTPState(t, outlined.Artifact, artifactID, primary.id, artifactdomain.StatusOutlineReview, 2, 2)

	staleApprove := postArtifactHTTP[artifactHTTPProblemWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/outline/approve", "approve-outline-stale",
		artifactHTTPVersionBody(primary.id, 1), http.StatusConflict, primary.root)
	assertArtifactHTTPProblem(t, staleApprove, artifactapp.ErrorCodeVersionConflict)
	approvedOutline := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/outline/approve", "approve-outline",
		artifactHTTPVersionBody(primary.id, 2), http.StatusOK, primary.root)
	assertArtifactHTTPState(t, approvedOutline.Artifact, artifactID, primary.id, artifactdomain.StatusGenerating, 3, 3)

	crossCitationBody := artifactHTTPSectionBody(primary.id, 3, "covered", "Covered", "Cross-workspace content must fail.", []artifactHTTPEvidenceFixture{otherEvidence}, "COVERED", []map[string]any{})
	crossCitation := postArtifactHTTP[artifactHTTPProblemWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections", "section-cross-workspace", crossCitationBody, http.StatusNotFound, primary.root)
	assertArtifactHTTPProblem(t, crossCitation, "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND")

	forged := primaryEvidence[0]
	forged.sourceSpanID = primaryEvidence[1].sourceSpanID
	forgedBody := artifactHTTPSectionBody(primary.id, 3, "covered", "Covered", "A forged tuple must fail.", []artifactHTTPEvidenceFixture{forged}, "COVERED", []map[string]any{})
	forgedProblem := postArtifactHTTP[artifactHTTPProblemWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections", "section-forged-tuple", forgedBody, http.StatusNotFound, primary.root)
	assertArtifactHTTPProblem(t, forgedProblem, "RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND")
	if crossCitation.Message != forgedProblem.Message || crossCitation.Retryable != forgedProblem.Retryable {
		t.Fatalf("cross-workspace and forged tuple errors diverged: cross=%#v forged=%#v", crossCitation, forgedProblem)
	}

	ineligibleBody := artifactHTTPSectionBody(primary.id, 3, "covered", "Covered", "Ineligible material must fail.", []artifactHTTPEvidenceFixture{primaryEvidence[1]}, "COVERED", []map[string]any{})
	ineligibleProblem := postArtifactHTTP[artifactHTTPProblemWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections", "section-ineligible", ineligibleBody, http.StatusBadRequest, primary.root)
	assertArtifactHTTPProblem(t, ineligibleProblem, artifactapp.ErrorCodeEvidenceInvalid)

	coveredBody := artifactHTTPSectionBody(primary.id, 3, "covered", "Covered", "This section is backed by approved evidence.", []artifactHTTPEvidenceFixture{primaryEvidence[0]}, "COVERED", []map[string]any{})
	covered := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections", "section-covered", coveredBody, http.StatusOK, primary.root)
	assertArtifactHTTPState(t, covered.Artifact, artifactID, primary.id, artifactdomain.StatusGenerating, 4, 4)
	if len(covered.Artifact.Revision.Sections) != 1 || len(covered.Artifact.Revision.Sections[0].Citations) != 1 {
		t.Fatalf("covered section response=%#v", covered.Artifact.Revision.Sections)
	}
	verified := covered.Artifact.Revision.Sections[0].Citations[0]
	if !verified.Verified || verified.SourceVersionID != string(primaryEvidence[0].sourceVersionID) || verified.SourceSpanID != string(primaryEvidence[0].sourceSpanID) ||
		verified.VerifiedContentHash != primaryEvidence[0].contentHash || verified.Excerpt != string(primaryEvidence[0].content) {
		t.Fatalf("server citation reconstruction=%#v", verified)
	}
	coveredReplay := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections", "section-covered", coveredBody, http.StatusOK, primary.root)
	if !coveredReplay.Replayed || coveredReplay.Artifact.Version != 4 || coveredReplay.Artifact.Revision.ID != covered.Artifact.Revision.ID {
		t.Fatalf("covered section replay=%#v", coveredReplay)
	}

	gapBody := artifactHTTPSectionBody(primary.id, 4, "gap", "Known Gap", "", nil, "GAP", []map[string]any{{"code": "NO_APPROVED_SOURCE", "description": "No approved source covers this section."}})
	draft := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections", "section-gap", gapBody, http.StatusOK, primary.root)
	assertArtifactHTTPState(t, draft.Artifact, artifactID, primary.id, artifactdomain.StatusDraft, 5, 5)
	if len(draft.Artifact.Revision.Sections) != 2 || draft.Artifact.Revision.Sections[1].Content != "" || len(draft.Artifact.Revision.Sections[1].Citations) != 0 || draft.Artifact.Revision.Sections[1].Coverage.Status != "GAP" {
		t.Fatalf("gap section response=%#v", draft.Artifact.Revision.Sections)
	}

	approvedDraft := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/draft/approve", "approve-draft",
		artifactHTTPVersionBody(primary.id, 5), http.StatusOK, primary.root)
	assertArtifactHTTPState(t, approvedDraft.Artifact, artifactID, primary.id, artifactdomain.StatusApproved, 6, 5)

	exported := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/exports/markdown", "export-markdown",
		artifactHTTPVersionBody(primary.id, 6), http.StatusOK, primary.root)
	assertArtifactHTTPState(t, exported.Artifact, artifactID, primary.id, artifactdomain.StatusExported, 7, 5)
	if exported.Export == nil || exported.Publication != nil || exported.Export.RevisionID != exported.Artifact.Revision.ID || exported.Export.RevisionHash != exported.Artifact.Revision.ContentHash {
		t.Fatalf("export response=%#v", exported)
	}
	exportRead := getArtifactHTTP[artifactHTTPExportWire](t, client,
		fmt.Sprintf("%s/api/v1/artifacts/%s/exports/%s?workspace_id=%s", server.URL, artifactID, exported.Export.ID, primary.id), http.StatusOK, primary.root)
	if exportRead != *exported.Export {
		t.Fatalf("export read=%#v command=%#v", exportRead, *exported.Export)
	}
	assertArtifactHTTPExportFile(t, primary.root, artifactID, exportRead, draft.Artifact.Revision.ContentHash)

	published := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/publish-proposals", "publish-artifact",
		artifactHTTPVersionBody(primary.id, 7), http.StatusOK, primary.root)
	assertArtifactHTTPState(t, published.Artifact, artifactID, primary.id, artifactdomain.StatusPublishProposed, 8, 5)
	if published.Publication == nil || published.Export != nil || published.Publication.ArtifactVersion != 8 || published.Publication.RevisionID != published.Artifact.Revision.ID || published.Publication.ContentHash != published.Artifact.Revision.ContentHash {
		t.Fatalf("publication response=%#v", published)
	}
	publishReplay := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/publish-proposals", "publish-artifact",
		artifactHTTPVersionBody(primary.id, 7), http.StatusOK, primary.root)
	if !publishReplay.Replayed || publishReplay.Publication == nil || publishReplay.Publication.ProposalID != published.Publication.ProposalID {
		t.Fatalf("publication replay=%#v", publishReplay)
	}

	finalState := getArtifactHTTP[artifactHTTPArtifactWire](t, client,
		fmt.Sprintf("%s/api/v1/artifacts/%s?workspace_id=%s", server.URL, artifactID, primary.id), http.StatusOK, primary.root)
	assertArtifactHTTPState(t, finalState, artifactID, primary.id, artifactdomain.StatusPublishProposed, 8, 5)
	assertArtifactHTTPPublicationFacts(t, ctx, pool, primary.id, artifactID, *published.Publication)
	if documentsAfter := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM core.document WHERE workspace_id=$1`, string(primary.id)); documentsAfter != documentsBefore {
		t.Fatalf("publish proposal changed formal document count: before=%d after=%d", documentsBefore, documentsAfter)
	}
}

func TestArtifactGenerationCompositionCancellationPostgreSQLIntegration(t *testing.T) {
	database := newArtifactHTTPTestDatabase(t)
	pool := database.DB()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	workspace := seedArtifactHTTPWorkspace(t, ctx, pool, "artifact-generation-cancel")
	server := newArtifactHTTPIntegrationServer(t, database)
	client := server.Client()

	planned := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts", "generation-plan", map[string]any{
		"workspace_id": string(workspace.id), "type": "study-guide", "title": "Generation cancellation",
		"scope_definition": "Generated and human sections share one authoritative Artifact.",
	}, http.StatusCreated, workspace.root)
	artifactID := planned.Artifact.ID
	outlined := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/outline", "generation-outline", map[string]any{
		"workspace_id": string(workspace.id), "expected_version": int64(1),
		"outline": []map[string]any{{"key": "generated", "title": "Generated"}, {"key": "manual", "title": "Manual"}},
	}, http.StatusOK, workspace.root)
	assertArtifactHTTPState(t, outlined.Artifact, artifactID, workspace.id, artifactdomain.StatusOutlineReview, 2, 2)
	approved := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/outline/approve", "generation-outline-approve",
		artifactHTTPVersionBody(workspace.id, 2), http.StatusOK, workspace.root)
	assertArtifactHTTPState(t, approved.Artifact, artifactID, workspace.id, artifactdomain.StatusGenerating, 3, 3)

	generationBody := map[string]any{
		"workspace_id": string(workspace.id), "expected_version": int64(3), "section_key": "generated",
	}
	started := postArtifactHTTP[artifactHTTPGenerationWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections/generate", "generation-cancelled", generationBody, http.StatusAccepted, workspace.root)
	if started.Replayed || started.ArtifactID != artifactID || started.WorkspaceID != string(workspace.id) ||
		started.SourceRevisionID != approved.Artifact.Revision.ID || started.SourceRevisionNo != 3 || started.SourceArtifactVersion != 3 ||
		started.SectionKey != "generated" || started.Status != string(artifactapp.SectionGenerationPending) || started.Version != 1 ||
		started.StatusURL != "/api/v1/workflows/"+started.WorkflowRunID {
		t.Fatalf("started generation=%#v", started)
	}

	run := getArtifactHTTP[artifactHTTPWorkflowRunWire](t, client, server.URL+started.StatusURL, http.StatusOK, workspace.root, workspace.id)
	if run.ID != started.WorkflowRunID || run.WorkspaceID != string(workspace.id) || run.Status != "pending" || run.Version < 1 {
		t.Fatalf("pending workflow=%#v generation=%#v", run, started)
	}
	cancelled := postArtifactHTTP[artifactHTTPWorkflowControlWire](t, client, server.URL+started.StatusURL+"/cancel", "cancel-generation-workflow",
		map[string]any{"expected_version": run.Version}, http.StatusOK, workspace.root, workspace.id)
	if cancelled.WorkflowRunID != started.WorkflowRunID || cancelled.Status != "cancelled" || cancelled.Version <= run.Version ||
		cancelled.StatusURL != started.StatusURL || !cancelled.CancelRequested || cancelled.PauseRequested {
		t.Fatalf("cancelled workflow=%#v pending=%#v", cancelled, run)
	}

	var status, failureClass, errorCode, errorSummary, workflowRunID, nodeRunID string
	var generationVersion int64
	var terminalAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT status,version,failure_class,error_code,error_summary,terminal_at,
		workflow_run_id::text,node_run_id::text
		FROM learning.artifact_section_generation
		WHERE id=$1 AND workspace_id=$2`, started.GenerationID, string(workspace.id)).Scan(
		&status, &generationVersion, &failureClass, &errorCode, &errorSummary, &terminalAt, &workflowRunID, &nodeRunID,
	); err != nil {
		t.Fatal(err)
	}
	if status != string(artifactapp.SectionGenerationCancelled) || generationVersion != 2 || failureClass != "cancelled" ||
		errorCode != "WORKFLOW_CANCELLED" || errorSummary != "WORKFLOW_CANCELLED" || terminalAt == nil ||
		workflowRunID != started.WorkflowRunID || nodeRunID != started.NodeRunID {
		t.Fatalf("terminal generation status=%s version=%d class=%s code=%s summary=%s terminal=%v run=%s node=%s",
			status, generationVersion, failureClass, errorCode, errorSummary, terminalAt, workflowRunID, nodeRunID)
	}

	replayed := postArtifactHTTP[artifactHTTPGenerationWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections/generate", "generation-cancelled", generationBody, http.StatusAccepted, workspace.root)
	if !replayed.Replayed || replayed.GenerationID != started.GenerationID || replayed.WorkflowRunID != started.WorkflowRunID ||
		replayed.NodeRunID != started.NodeRunID || replayed.Status != string(artifactapp.SectionGenerationCancelled) || replayed.Version != 2 ||
		replayed.UpdatedAt != terminalAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("terminal generation replay=%#v started=%#v terminal=%v", replayed, started, terminalAt)
	}

	retry := postArtifactHTTP[artifactHTTPGenerationWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections/generate", "generation-retry", generationBody, http.StatusAccepted, workspace.root)
	if retry.Replayed || retry.GenerationID == started.GenerationID || retry.WorkflowRunID == started.WorkflowRunID || retry.NodeRunID == started.NodeRunID ||
		retry.SourceRevisionID != started.SourceRevisionID || retry.Status != string(artifactapp.SectionGenerationPending) || retry.Version != 1 {
		t.Fatalf("new-key generation retry=%#v cancelled=%#v", retry, replayed)
	}

	manual := postArtifactHTTP[artifactHTTPCommandWire](t, client, server.URL+"/api/v1/artifacts/"+artifactID+"/sections", "generation-manual-section",
		artifactHTTPSectionBody(workspace.id, 3, "manual", "Manual", "", nil, "GAP", []map[string]any{{
			"code": "MANUAL_GAP", "description": "Human workflow remains available while generation is pending.",
		}}), http.StatusOK, workspace.root)
	assertArtifactHTTPState(t, manual.Artifact, artifactID, workspace.id, artifactdomain.StatusGenerating, 4, 4)
	if len(manual.Artifact.Revision.Sections) != 1 || manual.Artifact.Revision.Sections[0].Key != "manual" || manual.Artifact.Revision.Sections[0].Coverage.Status != "GAP" {
		t.Fatalf("manual section after generation retry=%#v", manual.Artifact.Revision.Sections)
	}
}

type artifactHTTPWorkspaceFixture struct {
	id   foundation.ID
	root string
}

type artifactHTTPEvidenceSpec struct {
	label   string
	content []byte
}

type artifactHTTPEvidenceFixture struct {
	workspaceID, indexVersionID, chunkID foundation.ID
	sourceID, sourceVersionID            foundation.ID
	contentArtifactID, projectionID      foundation.ID
	sourceSpanID                         foundation.ID
	content                              []byte
	contentHash                          string
}

func seedArtifactHTTPWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) artifactHTTPWorkspaceFixture {
	t.Helper()
	root, err := filesystem.NewRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := artifactHTTPID(t)
	now := time.Now().UTC().Add(-15 * time.Minute).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,'inactive',1,$4,$4)`, string(workspaceID), name, root.Path(), now); err != nil {
		t.Fatal(err)
	}
	return artifactHTTPWorkspaceFixture{id: workspaceID, root: root.Path()}
}

func seedArtifactHTTPEvidenceIndex(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspace artifactHTTPWorkspaceFixture,
	specs []artifactHTTPEvidenceSpec,
) []artifactHTTPEvidenceFixture {
	t.Helper()
	if len(specs) == 0 {
		t.Fatal("evidence fixture requires at least one source")
	}
	managedSources := filepath.Join(workspace.root, ".knowledge", "sources")
	if err := os.MkdirAll(managedSources, 0o700); err != nil {
		t.Fatal(err)
	}
	parserID, parserVersion := "artifact-http-parser", "v1"
	parserConfigHash := artifactHTTPHash([]byte("artifact-http-parser-config/v1"))
	schemaVersion, chunkVersion := "artifact-http-schema/v1", "artifact-http-chunk/v1"
	now := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	fixtures := make([]artifactHTTPEvidenceFixture, len(specs))
	for index, spec := range specs {
		if strings.TrimSpace(spec.label) == "" || len(spec.content) == 0 || !utf8.Valid(spec.content) {
			t.Fatalf("invalid evidence spec=%#v", spec)
		}
		fixture := artifactHTTPEvidenceFixture{
			workspaceID: workspace.id, sourceID: artifactHTTPID(t), sourceVersionID: artifactHTTPID(t),
			contentArtifactID: artifactHTTPID(t), projectionID: artifactHTTPID(t), sourceSpanID: artifactHTTPID(t),
			chunkID: artifactHTTPID(t), content: append([]byte(nil), spec.content...), contentHash: artifactHTTPHash(spec.content),
		}
		fixtures[index] = fixture
		managedPath := filepath.Join(managedSources, fixture.contentHash)
		if err := os.WriteFile(managedPath, fixture.content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(managedPath, 0o600); err != nil {
			t.Fatal(err)
		}
		relativePath := "docs/" + spec.label + ".md"
		statements := []struct {
			query string
			args  []any
		}{
			{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
			  VALUES($1,$2,'file',$3,$4,$5)`, []any{string(fixture.sourceID), string(workspace.id), spec.label, relativePath, now}},
			{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
			  VALUES($1,$2,$3,$4,$5,$6)`, []any{string(fixture.contentArtifactID), string(workspace.id), fixture.contentHash, int64(len(fixture.content)), ".knowledge/sources/" + fixture.contentHash, now}},
			{`INSERT INTO core.source_version(id,source_id,workspace_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at,content_artifact_id)
			  VALUES($1,$2,$3,$4,$5,'text/markdown',$6,'passed',$7,$8)`, []any{string(fixture.sourceVersionID), string(fixture.sourceID), string(workspace.id), fixture.contentHash, int64(len(fixture.content)), relativePath, now, string(fixture.contentArtifactID)}},
			{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at)
			  VALUES($1,$2,$3,$4,$5,$6,$7,$8,'[]',$9)`, []any{string(fixture.projectionID), string(workspace.id), string(fixture.contentArtifactID), parserID, parserVersion, parserConfigHash, schemaVersion, fixture.contentHash, now}},
			{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
			  VALUES($1,$2,$3,$4)`, []any{string(fixture.sourceVersionID), string(fixture.projectionID), string(workspace.id), now}},
			{`INSERT INTO ingestion.attempt(
				id,workspace_id,source_version_id,parse_projection_id,status,security_status,parser_id,parser_version,
				parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,warnings,
				started_at,completed_at,version
			  ) VALUES($1,$2,$3,$4,'chunked','passed',$5,$6,$7,$8,$9,$10,1,'[]',$11,$11,1)`, []any{
				string(artifactHTTPID(t)), string(workspace.id), string(fixture.sourceVersionID), string(fixture.projectionID),
				parserID, parserVersion, parserConfigHash, chunkVersion, schemaVersion,
				"artifact-http-attempt-" + string(fixture.sourceVersionID), now,
			}},
			{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at)
			  VALUES($1,$2,$3,$4,'paragraph',1,1,0,$5,'{"kind":"paragraph"}'::jsonb,$6,$7,$8,$9)`, []any{string(fixture.sourceSpanID), string(workspace.id), string(fixture.contentArtifactID), string(fixture.projectionID), int64(len(fixture.content)), fixture.contentHash, parserVersion, schemaVersion, now}},
			{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
			  VALUES($1,$2,$3,$4,'[]',$5,$6,$7,$8,$9,$10,$11,$12,false,'active',$13)`, []any{string(fixture.chunkID), string(workspace.id), string(fixture.projectionID), index, string(fixture.content), fixture.contentHash, string(fixture.sourceSpanID), int64(len(fixture.content)), utf8.RuneCount(fixture.content), parserVersion, chunkVersion, schemaVersion, now}},
		}
		for _, statement := range statements {
			if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
				t.Fatalf("seed %s evidence: %v", spec.label, err)
			}
		}
	}

	indexID := artifactHTTPID(t)
	for index := range fixtures {
		fixtures[index].indexVersionID = indexID
	}
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,source_snapshot_ref,
		manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at,
		source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,source_parser_config_hash,
		source_chunk_strategy_version,source_schema_version
	) VALUES($1,$2,'unicode','v1',$3,'{}','artifact-http-fixture',$4,$5,$6,'building','["vector"]',1,$7,$7,$8,$5,$9,$10,$3,$11,$12)`,
		string(indexID), string(workspace.id), parserConfigHash, artifactHTTPHash([]byte("manifest-"+string(indexID))), len(fixtures),
		"artifact-http-index-"+string(indexID), now, artifactHTTPHash([]byte("source-manifest-"+string(indexID))), parserID, parserVersion, chunkVersion, schemaVersion); err != nil {
		t.Fatal(err)
	}
	for index, fixture := range fixtures {
		if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(indexID), string(fixture.chunkID), string(workspace.id), fixture.contentHash, index, parserVersion, chunkVersion, schemaVersion, now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_manifest_source(
			index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
		) VALUES($1,$2,$3,$4,$5,'included',$6)`, string(indexID), string(workspace.id), string(fixture.sourceID), string(fixture.sourceVersionID), string(fixture.projectionID), now); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO retrieval.chunk_projection(
			index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at
		) VALUES($1,$2,$3,to_tsvector('simple',$4::text),cardinality(tsvector_to_array(to_tsvector('simple',$4::text))),'ready','disabled',$5,$5)`,
			string(indexID), string(fixture.chunkID), string(workspace.id), string(fixture.content), now); err != nil {
			t.Fatal(err)
		}
	}
	builtAt := now.Add(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE retrieval.index_version SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`, string(indexID), builtAt); err != nil {
		t.Fatal(err)
	}
	activatedAt := now.Add(2 * time.Minute)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(
			id,kind,workspace_id,target_index_version_id,target_version,idempotency_key,reason_code,created_at
		) VALUES($1,'activate',$2,$3,3,$4,'artifact-http-test',$5)`, string(artifactHTTPID(t)), string(workspace.id), string(indexID), "activate-"+string(indexID), activatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version SET status='active',version=3,activated_at=$2,updated_at=$2 WHERE id=$1`, string(indexID), activatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func seedArtifactHTTPConfirmedClaim(t *testing.T, ctx context.Context, pool *platformpostgres.Pool, evidence artifactHTTPEvidenceFixture) foundation.ID {
	t.Helper()
	repository, err := knowledgepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	applicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	statement, normalized, err := knowledgedomain.NormalizeStatement("Approved evidence supports the Artifact section.")
	if err != nil {
		t.Fatal(err)
	}
	factors, err := knowledgedomain.NormalizeConfidenceFactors(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Microsecond)
	claim := knowledgedomain.Claim{
		ID: artifactHTTPID(t), WorkspaceID: evidence.workspaceID, Statement: statement, NormalizedStatement: normalized,
		Applicability: applicability, Status: knowledgedomain.ClaimStatusSuggested, ConfidenceFactors: factors,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	claim.Fingerprint = knowledgedomain.ComputeClaimFingerprint(claim.WorkspaceID, claim.NormalizedStatement, claim.Applicability)
	suggested, err := repository.SuggestClaim(ctx, knowledgedomain.SuggestClaimRecord{
		Claim: claim, IdempotencyKey: "artifact-http-claim-suggest", RequestHash: artifactHTTPHash([]byte("artifact-http-claim-suggest")),
	})
	if err != nil {
		t.Fatal(err)
	}
	source := knowledgedomain.ClaimSource{
		ID: artifactHTTPID(t), WorkspaceID: evidence.workspaceID, ClaimID: claim.ID,
		Provenance:  knowledgedomain.ProvenanceRef{WorkspaceID: evidence.workspaceID, SourceVersionID: evidence.sourceVersionID, SourceSpanID: evidence.sourceSpanID},
		SupportType: knowledgedomain.ClaimSupportSupports, Reason: "The immutable source directly supports this claim.", CreatedAt: now.Add(time.Second),
	}
	confirmed, err := repository.ConfirmClaim(ctx, knowledgedomain.ConfirmClaimRecord{
		WorkspaceID: evidence.workspaceID, ClaimID: claim.ID, ExpectedVersion: suggested.Claim.Version, Source: source,
		IdempotencyKey: "artifact-http-claim-confirm", RequestHash: artifactHTTPHash([]byte("artifact-http-claim-confirm")), At: now.Add(time.Second),
	})
	if err != nil || confirmed.Claim.Status != knowledgedomain.ClaimStatusConfirmed {
		t.Fatalf("confirm evidence claim=%#v err=%v", confirmed, err)
	}
	return confirmed.Claim.ID
}

func newArtifactHTTPIntegrationServer(t *testing.T, pool *platformpostgres.Pool) *httptest.Server {
	t.Helper()
	workspaces, err := workspacepostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	changeRepository, err := changecontrolpostgres.NewGORMRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := changecontrollocalfs.NewReader(workspaces)
	if err != nil {
		t.Fatal(err)
	}
	gitInspector, err := gitcli.NewWritebackClient(gitcli.New(""), workspaces)
	if err != nil {
		t.Fatal(err)
	}
	proposals, err := changecontrolapp.NewService(changeRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{}, targets, gitInspector)
	if err != nil {
		t.Fatal(err)
	}
	ids := foundation.NewUUIDGenerator(nil)
	clock := foundation.SystemClock{}
	files := filesystem.Scanner{Options: filesystem.ScanOptions{MaxBytes: filesystem.DefaultMaxBytes}}
	cfg := config.Defaults()
	cfg.ChatProvider = config.ChatProviderOpenAICompatible
	cfg.ChatBaseURL = "http://127.0.0.1:11434/v1"
	cfg.ChatModel = "artifact-http-integration"
	cfg.ChatModelVersion = "artifact-http-integration-v1"
	workflowService, _, generation, err := newAPIArtifactWorkflowComponents(pool, cfg, workspaces, files, nil, ids, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	generationDependencies := artifactGenerationDependencies(generation)
	handler, err := newArtifactHandlerWithDependencies(pool, workspaces, files, proposals, 10*time.Second, ids, clock, generationDependencies...)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.NewRouter(app.Dependencies{
		Version: "artifact-http-integration", Database: pool, Artifact: handler,
		Workflow: workflowhttp.NewHandler(workflowService),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	t.Cleanup(server.Close)
	return server
}
func newArtifactHTTPTestDatabase(t *testing.T) *platformpostgres.Pool {
	t.Helper()
	return testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		MaxConns:         8,
	}).Pool()
}

func artifactHTTPSectionBody(
	workspaceID foundation.ID,
	expectedVersion int64,
	key, title, content string,
	citations []artifactHTTPEvidenceFixture,
	coverageStatus string,
	gaps []map[string]any,
) map[string]any {
	wireCitations := make([]map[string]any, len(citations))
	for index, citation := range citations {
		wireCitations[index] = map[string]any{
			"index_version_id": string(citation.indexVersionID), "chunk_id": string(citation.chunkID),
			"source_version_id": string(citation.sourceVersionID), "source_span_id": string(citation.sourceSpanID),
		}
	}
	return map[string]any{
		"workspace_id": string(workspaceID), "expected_version": expectedVersion,
		"section": map[string]any{
			"key": key, "title": title, "content": content, "citations": wireCitations,
			"coverage": map[string]any{"section_key": key, "status": coverageStatus, "gaps": gaps},
		},
	}
}

func artifactHTTPVersionBody(workspaceID foundation.ID, expectedVersion int64) map[string]any {
	return map[string]any{"workspace_id": string(workspaceID), "expected_version": expectedVersion}
}

func cloneArtifactHTTPMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func postArtifactHTTP[T any](t *testing.T, client *http.Client, target, key string, body any, wantStatus int, forbiddenRoot string, workspaceIDs ...foundation.ID) T {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	return doArtifactHTTP[T](t, client, request, wantStatus, forbiddenRoot, workspaceIDs...)
}

func getArtifactHTTP[T any](t *testing.T, client *http.Client, target string, wantStatus int, forbiddenRoot string, workspaceIDs ...foundation.ID) T {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return doArtifactHTTP[T](t, client, request, wantStatus, forbiddenRoot, workspaceIDs...)
}

func doArtifactHTTP[T any](t *testing.T, client *http.Client, request *http.Request, wantStatus int, forbiddenRoot string, workspaceIDs ...foundation.ID) T {
	t.Helper()
	if len(workspaceIDs) > 1 {
		t.Fatal("artifact HTTP fixture accepts at most one Workspace header")
	}
	if len(workspaceIDs) == 1 {
		request.Header.Set("X-Workspace-ID", string(workspaceIDs[0]))
	}
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
		t.Fatal("artifact response exceeds integration test limit")
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", request.Method, request.URL, response.StatusCode, wantStatus, body)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		t.Fatalf("content-type=%q: %v", response.Header.Get("Content-Type"), err)
	}
	if forbiddenRoot != "" && strings.Contains(string(body), forbiddenRoot) {
		t.Fatalf("workspace root leaked in artifact response: %s", body)
	}
	if strings.Contains(string(body), `"output_path"`) || strings.Contains(string(body), `.knowledge/sources/`) {
		t.Fatalf("managed path leaked in artifact response: %s", body)
	}
	limits := strictjson.DefaultLimits()
	limits.MaxDocumentBytes = 2 * 1024 * 1024
	value, err := strictjson.DecodeObject[T](body, limits, nil)
	if err != nil {
		t.Fatalf("strictly decode artifact response: %v; body=%s", err, body)
	}
	return value
}

func assertArtifactHTTPState(t *testing.T, state artifactHTTPArtifactWire, artifactID string, workspaceID foundation.ID, status artifactdomain.Status, version, revisionNo int64) {
	t.Helper()
	if state.ID != artifactID || state.WorkspaceID != string(workspaceID) || state.Status != string(status) || state.Version != version ||
		state.Revision.ArtifactID != artifactID || state.Revision.RevisionNo != revisionNo || state.CurrentRevisionID != state.Revision.ID {
		t.Fatalf("artifact state=%#v want id=%s workspace=%s status=%s version=%d revision=%d", state, artifactID, workspaceID, status, version, revisionNo)
	}
}

func assertArtifactHTTPProblem(t *testing.T, problem artifactHTTPProblemWire, code string) {
	t.Helper()
	if problem.ErrorCode != code || strings.TrimSpace(problem.Message) == "" {
		t.Fatalf("problem=%#v want code=%s", problem, code)
	}
}

func assertArtifactHTTPExportFile(t *testing.T, root, artifactID string, export artifactHTTPExportWire, approvedRevisionHash string) {
	t.Helper()
	path := filepath.Join(root, ".knowledge", "exports", "artifacts", artifactID, export.ID+".md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || int64(len(content)) != export.OutputSize || artifactHTTPHash(content) != export.OutputHash || export.RevisionHash != approvedRevisionHash {
		t.Fatalf("export file mode=%o size=%d hash=%s response=%#v", info.Mode().Perm(), len(content), artifactHTTPHash(content), export)
	}
	for _, expected := range []string{"Formal Knowledge: `false`", "Status: `COVERED`", "Status: `GAP`", "NO_APPROVED_SOURCE"} {
		if !strings.Contains(string(content), expected) {
			t.Fatalf("export is missing %q: %s", expected, content)
		}
	}
}

func assertArtifactHTTPPublicationFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, artifactID string, publication artifactHTTPPublicationWire) {
	t.Helper()
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1 AND proposal_type='publish_artifact'`, string(workspaceID)); count != 1 {
		t.Fatalf("publish_artifact proposal count=%d want=1", count)
	}
	if count := artifactHTTPCount(t, ctx, pool, `SELECT count(*) FROM learning.artifact_publication WHERE workspace_id=$1 AND artifact_id=$2`, string(workspaceID), artifactID); count != 1 {
		t.Fatalf("artifact publication binding count=%d want=1", count)
	}
	var proposalID, proposalType, proposalStatus, frozenArtifactID, revisionID, contentHash, schemaVersion, targetPath string
	var artifactVersion, revisionNo int64
	if err := pool.QueryRow(ctx, `SELECT
		p.id::text,p.proposal_type,p.status,r.artifact_id::text,r.artifact_revision_id::text,
		r.artifact_revision_no,r.artifact_version,r.artifact_content_hash,r.schema_version,COALESCE(r.target_path,'')
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id
		WHERE p.workspace_id=$1 AND p.proposal_type='publish_artifact'`, string(workspaceID)).Scan(
		&proposalID, &proposalType, &proposalStatus, &frozenArtifactID, &revisionID,
		&revisionNo, &artifactVersion, &contentHash, &schemaVersion, &targetPath,
	); err != nil {
		t.Fatal(err)
	}
	if proposalID != publication.ProposalID || proposalType != "publish_artifact" || proposalStatus != "ready_for_review" ||
		frozenArtifactID != artifactID || revisionID != publication.RevisionID || revisionNo != publication.RevisionNo ||
		artifactVersion != publication.ArtifactVersion || contentHash != publication.ContentHash || schemaVersion != "artifact-publication/v1" || targetPath != "" {
		t.Fatalf("frozen proposal binding proposal=%s type=%s status=%s artifact=%s revision=%s/%d version=%d hash=%s schema=%s path=%q publication=%#v",
			proposalID, proposalType, proposalStatus, frozenArtifactID, revisionID, revisionNo, artifactVersion, contentHash, schemaVersion, targetPath, publication)
	}
}

func artifactHTTPCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func artifactHTTPID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func artifactHTTPHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

type artifactHTTPCommandWire struct {
	Artifact    artifactHTTPArtifactWire     `json:"artifact"`
	Export      *artifactHTTPExportWire      `json:"export,omitempty"`
	Publication *artifactHTTPPublicationWire `json:"publication,omitempty"`
	Replayed    bool                         `json:"replayed"`
}

type artifactHTTPGenerationWire struct {
	GenerationID          string `json:"generation_id"`
	WorkspaceID           string `json:"workspace_id"`
	ArtifactID            string `json:"artifact_id"`
	SourceRevisionID      string `json:"source_revision_id"`
	SourceRevisionNo      int64  `json:"source_revision_no"`
	SourceArtifactVersion int64  `json:"source_artifact_version"`
	SectionKey            string `json:"section_key"`
	WorkflowRunID         string `json:"workflow_run_id"`
	NodeRunID             string `json:"node_run_id"`
	Status                string `json:"status"`
	Version               int64  `json:"version"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
	Replayed              bool   `json:"replayed"`
	StatusURL             string `json:"status_url"`
}

type artifactHTTPWorkflowRunWire struct {
	ID              string                            `json:"id"`
	WorkspaceID     string                            `json:"workspace_id"`
	DefinitionID    string                            `json:"definition_id"`
	Status          string                            `json:"status"`
	Input           json.RawMessage                   `json:"input"`
	Output          json.RawMessage                   `json:"output,omitempty"`
	Version         int64                             `json:"version"`
	CreatedAt       string                            `json:"created_at"`
	UpdatedAt       string                            `json:"updated_at"`
	CompletedAt     *string                           `json:"completed_at,omitempty"`
	PauseRequested  bool                              `json:"pause_requested"`
	CancelRequested bool                              `json:"cancel_requested"`
	HumanTask       *artifactHTTPPendingHumanTaskWire `json:"human_task"`
}

type artifactHTTPPendingHumanTaskWire struct {
	ID                  string          `json:"id"`
	RunID               string          `json:"run_id"`
	NodeRunID           string          `json:"node_run_id"`
	Status              string          `json:"status"`
	ExpectedInputSchema json.RawMessage `json:"expected_input_schema"`
	TargetVersion       int64           `json:"target_version"`
	ExpiresAt           *string         `json:"expires_at"`
	CreatedAt           string          `json:"created_at"`
	Review              json.RawMessage `json:"review"`
}

type artifactHTTPWorkflowControlWire struct {
	WorkflowRunID   string `json:"workflow_run_id"`
	Status          string `json:"status"`
	Version         int64  `json:"version"`
	StatusURL       string `json:"status_url"`
	PauseRequested  bool   `json:"pause_requested"`
	CancelRequested bool   `json:"cancel_requested"`
}

type artifactHTTPArtifactWire struct {
	ID                string                     `json:"id"`
	WorkspaceID       string                     `json:"workspace_id"`
	Type              string                     `json:"type"`
	Title             string                     `json:"title"`
	Status            string                     `json:"status"`
	ScopeDefinition   string                     `json:"scope_definition"`
	SourceCoverage    []artifactHTTPCoverageWire `json:"source_coverage"`
	CurrentRevisionID string                     `json:"current_revision_id"`
	Version           int64                      `json:"version"`
	CreatedAt         string                     `json:"created_at"`
	UpdatedAt         string                     `json:"updated_at"`
	Revision          artifactHTTPRevisionWire   `json:"revision"`
}

type artifactHTTPRevisionWire struct {
	ID          string                    `json:"id"`
	ArtifactID  string                    `json:"artifact_id"`
	RevisionNo  int64                     `json:"revision_no"`
	Outline     []artifactHTTPOutlineWire `json:"outline"`
	Sections    []artifactHTTPSectionWire `json:"sections"`
	CreatedBy   string                    `json:"created_by"`
	Metadata    *artifactHTTPMetadataWire `json:"metadata,omitempty"`
	ContentHash string                    `json:"content_hash"`
	CreatedAt   string                    `json:"created_at"`
}

type artifactHTTPOutlineWire struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type artifactHTTPSectionWire struct {
	Key             string                           `json:"key"`
	Title           string                           `json:"title"`
	Content         string                           `json:"content"`
	Citations       []artifactHTTPCitationWire       `json:"citations"`
	DocumentSources []artifactHTTPDocumentSourceWire `json:"document_sources"`
	Coverage        artifactHTTPCoverageWire         `json:"coverage"`
}

type artifactHTTPCitationWire struct {
	SourceVersionID     string `json:"source_version_id"`
	SourceSpanID        string `json:"source_span_id"`
	VerifiedContentHash string `json:"verified_content_hash"`
	Excerpt             string `json:"excerpt"`
	Verified            bool   `json:"verified"`
}

type artifactHTTPDocumentSourceWire struct {
	DocumentID          string `json:"document_id"`
	ArticleRevisionID   string `json:"article_revision_id"`
	RevisionNo          int64  `json:"revision_no"`
	VerifiedContentHash string `json:"verified_content_hash"`
	Verified            bool   `json:"verified"`
}

type artifactHTTPCoverageWire struct {
	SectionKey string                `json:"section_key"`
	Status     string                `json:"status"`
	Gaps       []artifactHTTPGapWire `json:"gaps"`
}

type artifactHTTPGapWire struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

type artifactHTTPMetadataWire struct {
	PromptVersion             string `json:"prompt_version"`
	ModelVersion              string `json:"model_version"`
	WorkflowDefinitionVersion string `json:"workflow_definition_version"`
	SchemaVersion             string `json:"schema_version"`
}

type artifactHTTPExportWire struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	ArtifactID      string `json:"artifact_id"`
	RevisionID      string `json:"revision_id"`
	ArtifactVersion int64  `json:"artifact_version"`
	RevisionNo      int64  `json:"revision_no"`
	RevisionHash    string `json:"revision_hash"`
	OutputHash      string `json:"output_hash"`
	OutputSize      int64  `json:"output_size"`
	ExportedAt      string `json:"exported_at"`
}

type artifactHTTPPublicationWire struct {
	WorkspaceID     string `json:"workspace_id"`
	ArtifactID      string `json:"artifact_id"`
	RevisionID      string `json:"revision_id"`
	ArtifactVersion int64  `json:"artifact_version"`
	RevisionNo      int64  `json:"revision_no"`
	ContentHash     string `json:"content_hash"`
	ProposalID      string `json:"proposal_id"`
	CreatedAt       string `json:"created_at"`
}

type artifactHTTPProblemWire struct {
	ErrorCode     string                     `json:"error_code"`
	Message       string                     `json:"message"`
	Retryable     bool                       `json:"retryable"`
	WorkflowRunID string                     `json:"workflow_run_id,omitempty"`
	Details       map[string]json.RawMessage `json:"details,omitempty"`
}
