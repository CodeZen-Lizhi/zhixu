//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	captureprofile "github.com/CodeZen-Lizhi/zhixu/internal/capture/profile"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func synthesisCompositionCounts(t *testing.T, ctx context.Context, pool *platformpostgres.Pool, workspaceID foundation.ID) map[string]int {
	t.Helper()
	queries := map[string]string{
		"captures":             `SELECT count(*) FROM core.capture WHERE workspace_id=$1`,
		"source_events":        `SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1 AND event_type='ingestion.source.ready' AND published_at IS NOT NULL`,
		"processing":           `SELECT count(*) FROM organizing.synthesis_processing WHERE workspace_id=$1`,
		"workflows":            `SELECT count(*) FROM workflow.run run JOIN organizing.synthesis_processing processing ON processing.workflow_run_id=run.id WHERE processing.workspace_id=$1 AND run.status='succeeded'`,
		"nodes":                `SELECT count(*) FROM workflow.node_run node JOIN organizing.synthesis_processing processing ON processing.workflow_run_id=node.run_id WHERE processing.workspace_id=$1 AND node.status='succeeded'`,
		"attempts":             `SELECT count(*) FROM workflow.node_attempt attempt JOIN workflow.node_run node ON node.id=attempt.node_run_id JOIN organizing.synthesis_processing processing ON processing.workflow_run_id=node.run_id WHERE processing.workspace_id=$1`,
		"jobs":                 `SELECT count(*) FROM workflow.river_job job JOIN workflow.node_run node ON job.args->>'node_run_id'=node.id::text JOIN organizing.synthesis_processing processing ON processing.workflow_run_id=node.run_id WHERE processing.workspace_id=$1`,
		"model_steps":          `SELECT count(*) FROM organizing.synthesis_model_step WHERE workspace_id=$1 AND status='READY'`,
		"model_runs":           `SELECT count(*) FROM agent.model_run WHERE workspace_id=$1 AND output_schema_id IN ('` + agentdomain.SynthesisDeltaSchemaID + `','` + agentdomain.SynthesisSemanticReviewSchemaID + `')`,
		"model_calls":          `SELECT count(*) FROM agent.model_call call JOIN agent.model_run run ON run.id=call.model_run_id WHERE run.workspace_id=$1 AND run.output_schema_id IN ('` + agentdomain.SynthesisDeltaSchemaID + `','` + agentdomain.SynthesisSemanticReviewSchemaID + `')`,
		"profile_calls":        `SELECT count(*) FROM agent.model_call call JOIN agent.model_run run ON run.id=call.model_run_id WHERE run.workspace_id=$1 AND run.output_schema_id='` + captureprofile.SchemaID + `'`,
		"notes":                `SELECT count(*) FROM organizing.synthesis_note WHERE workspace_id=$1 AND status='PENDING_APPROVAL'`,
		"revisions":            `SELECT count(*) FROM organizing.synthesis_revision WHERE workspace_id=$1`,
		"bindings":             `SELECT count(*) FROM authoring.document_publication_binding WHERE workspace_id=$1`,
		"proposals":            `SELECT count(*) FROM change_control.proposal WHERE workspace_id=$1`,
		"receipts":             `SELECT count(*) FROM organizing.synthesis_apply_receipt WHERE workspace_id=$1`,
		"changes":              `SELECT count(*) FROM organizing.synthesis_apply_receipt WHERE workspace_id=$1 AND changed`,
		"published":            `SELECT count(*) FROM core.document WHERE workspace_id=$1 AND current_published_revision_id IS NOT NULL`,
		"commits":              `SELECT count(*) FROM change_control.proposal_commit commit JOIN change_control.proposal proposal ON proposal.id=commit.proposal_id WHERE proposal.workspace_id=$1`,
		"unpublished_events":   `SELECT count(*) FROM workflow.outbox_event WHERE workspace_id=$1 AND event_type='ingestion.source.ready' AND published_at IS NULL`,
		"unpublished_captures": `SELECT count(*) FROM ops.capture_outbox WHERE workspace_id=$1 AND published_at IS NULL`,
	}
	counts := make(map[string]int, len(queries))
	for name, query := range queries {
		var count int
		if err := pool.DB().QueryRow(ctx, query, string(workspaceID)).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		counts[name] = count
	}
	return counts
}

func assertSynthesisCompositionCounts(t *testing.T, ctx context.Context, pool *platformpostgres.Pool, workspaceID foundation.ID, imports, revisions int) {
	t.Helper()
	want := map[string]int{
		"captures": imports, "source_events": imports, "processing": imports, "workflows": imports,
		"nodes": imports * 4, "attempts": imports * 4, "jobs": imports * 4,
		"model_steps": imports * 2, "model_runs": imports * 2, "model_calls": imports * 2, "profile_calls": imports,
		"notes": 1, "revisions": revisions, "bindings": revisions, "proposals": revisions,
		"receipts": imports, "changes": revisions, "published": 0, "commits": 0,
		"unpublished_events": 0, "unpublished_captures": 0,
	}
	if got := synthesisCompositionCounts(t, ctx, pool, workspaceID); !reflect.DeepEqual(got, want) {
		t.Fatalf("durable composition counts: got=%v want=%v", got, want)
	}
	var independent int
	if err := pool.DB().QueryRow(ctx, `SELECT count(*) FROM organizing.synthesis_model_step generation
		JOIN organizing.synthesis_model_step review ON review.workflow_run_id=generation.workflow_run_id AND review.stage='VALIDATE'
		WHERE generation.workspace_id=$1 AND generation.stage='GENERATE'
		AND generation.node_run_id<>review.node_run_id AND generation.node_attempt_id<>review.node_attempt_id
		AND generation.model_run_id<>review.model_run_id
		AND generation.output_hash=review.generation_output_hash
		AND review.semantic_receipt->>'generation_model_run_id'=generation.model_run_id::text
		AND review.semantic_receipt->>'accepted'='true'`, string(workspaceID)).Scan(&independent); err != nil {
		t.Fatal(err)
	}
	if independent != imports {
		t.Fatalf("independent generation/review bindings=%d want=%d", independent, imports)
	}
}

type synthesisCompositionRevision struct {
	id, noteID, parentID, proposalID, proposalStatus, targetPath string
	items                                                        []organizingdomain.SynthesisItem
}

func readSynthesisCompositionRevision(t *testing.T, ctx context.Context, pool *platformpostgres.Pool, workspaceID foundation.ID, number int) synthesisCompositionRevision {
	t.Helper()
	var revision synthesisCompositionRevision
	var raw []byte
	if err := pool.DB().QueryRow(ctx, `SELECT revision.id::text,revision.note_id::text,COALESCE(revision.parent_revision_id::text,''),
		revision.items,proposal.id::text,proposal.status,document.canonical_path
		FROM organizing.synthesis_revision revision
		JOIN core.article_revision article ON article.id=revision.article_revision_id
		JOIN core.document document ON document.id=revision.document_id
		JOIN authoring.document_publication_binding binding ON binding.article_revision_id=article.id
		JOIN change_control.proposal proposal ON proposal.id=binding.proposal_id
		WHERE revision.workspace_id=$1 AND revision.revision_no=$2`, string(workspaceID), number,
	).Scan(&revision.id, &revision.noteID, &revision.parentID, &raw, &revision.proposalID, &revision.proposalStatus, &revision.targetPath); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &revision.items); err != nil {
		t.Fatal(err)
	}
	return revision
}

func assertSynthesisCompositionRevisions(t *testing.T, ctx context.Context, pool *platformpostgres.Pool,
	workspaceID foundation.ID, firstSource, secondSource capturedomain.Capture, root string,
) {
	t.Helper()
	first := readSynthesisCompositionRevision(t, ctx, pool, workspaceID, 1)
	second := readSynthesisCompositionRevision(t, ctx, pool, workspaceID, 2)
	if first.noteID != second.noteID || first.parentID != "" || second.parentID != first.id ||
		first.proposalID == second.proposalID || first.proposalStatus != "needs_revision" || second.proposalStatus != "ready_for_review" {
		t.Fatalf("revision or Proposal lineage changed: first=%+v second=%+v", first, second)
	}
	if len(first.items) != 2 || len(second.items) != 4 || first.items[0].Fact == nil || second.items[0].Fact == nil ||
		first.items[1].Gap == nil || second.items[3].Conflict == nil {
		t.Fatal("expected initial fact/gap and updated fact/gap/fact/conflict")
	}
	before, after := first.items[0], second.items[0]
	if before.ID != after.ID || before.Fact.Text != synthesisCompositionFact || after.Fact.Text != before.Fact.Text ||
		after.Fact.Applicability != before.Fact.Applicability || len(before.Fact.Sources) != 1 || len(after.Fact.Sources) != 2 ||
		!reflect.DeepEqual(first.items[1], second.items[1]) || first.items[1].Gap.Question != synthesisCompositionGap {
		t.Fatal("support addition rewrote stable fact or unrelated gap")
	}
	versions := map[foundation.ID]bool{}
	for _, source := range after.Fact.Sources {
		if source.Source.WorkspaceID != workspaceID || source.Source.ContentArtifactID == "" || source.Source.ParseProjectionID == "" ||
			source.SourceSpanID == "" || source.ExcerptHash == "" || source.Source.ContentHash == "" {
			t.Fatal("fact lost its complete original source tuple")
		}
		versions[source.Source.SourceVersionID] = true
	}
	if !versions[firstSource.LatestSourceVersionID] || !versions[secondSource.LatestSourceVersionID] {
		t.Fatal("updated fact does not cite both original imports")
	}
	conflict := second.items[3].Conflict
	if len(conflict.Alternatives) != 2 || len(conflict.Alternatives[0].Sources) != 1 || len(conflict.Alternatives[1].Sources) != 1 ||
		conflict.Alternatives[0].Sources[0].Source.SourceVersionID != firstSource.LatestSourceVersionID ||
		conflict.Alternatives[1].Sources[0].Source.SourceVersionID != secondSource.LatestSourceVersionID ||
		second.items[2].Fact == nil || second.items[2].Fact.Text != "Refreshing invalidates the cache key." {
		t.Fatal("complement or conditional conflict lost original evidence")
	}
	var current string
	if err := pool.DB().QueryRow(ctx, `SELECT current_revision_id::text FROM organizing.synthesis_note WHERE id=$1`, second.noteID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != second.id {
		t.Fatal("NO_CHANGE replaced the current revision")
	}
	if first.targetPath == "" || first.targetPath != second.targetPath {
		t.Fatal("generated publication target is not stable")
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(second.targetPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved synthesis wrote its publication target: %v", err)
	}
}
