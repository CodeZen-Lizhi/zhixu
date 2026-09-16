//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	capturepostgres "github.com/CodeZen-Lizhi/zhixu/internal/capture/adapter/postgres"
	captureapp "github.com/CodeZen-Lizhi/zhixu/internal/capture/application"
	capturedomain "github.com/CodeZen-Lizhi/zhixu/internal/capture/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	owner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	workflowpostgres "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/postgres"
)

func TestSynthesisKnowledgeSnapshotSurvivesProfileRebuildAndSourceRemoval(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 107)
	ctx := t.Context()
	first := f.generation(t, 146000, nil, "Redis expiration controls cache lifetime.")
	// 显式使用有效 Profile 测试数据：本测试证明持久化和历史所属模块读取；实际 Profile 生成由 Worker 测试覆盖。
	seedDiscoveryRuntimeProfile(t, f, first)
	// 重建可变状态不得丢弃已提交的精确 Profile。
	if err := f.db.Exec(`UPDATE learning.document_knowledge_profile SET status='RUNNING',version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(organizingIntegrationID(146101))).Error; err != nil {
		t.Fatal(err)
	}
	first.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(first, "knowledge snapshots")}
	applied, err := f.service.ApplyGeneration(ctx, first.Input, first.Generation)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.store.GetSynthesisNote(ctx, f.workspace, applied.Publications[0].NoteID)
	if err != nil {
		t.Fatal(err)
	}
	reference := first.Input.Sources[0].Reference
	query := app.SynthesisKnowledgeBindingQuery{NoteID: detail.Note.ID, RevisionID: detail.CurrentRevision.ID, Reference: reference}
	frozen, err := f.service.ReadSynthesisKnowledgeBinding(ctx, query)
	if err != nil || frozen != organizingIntegrationID(146102) {
		t.Fatalf("binding=%s error=%v", frozen, err)
	}
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(f.platform, runs)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := owner.NewKnowledgeDirectoryReader(profiles)
	if err != nil {
		t.Fatal(err)
	}
	original, err := directory.ProjectSynthesisKnowledgeSnapshot(ctx, reference, frozen)
	if err != nil || len(original.Points) != 1 {
		t.Fatalf("original=%+v error=%v", original, err)
	}
	revisionView, err := profiles.GetProfileRevision(ctx, captureapp.ProfileRevisionQuery{WorkspaceID: f.workspace, SourceVersionID: reference.Source.SourceVersionID, RevisionID: frozen})
	if err != nil {
		t.Fatal(err)
	}
	changed := revisionView.Revision.Content
	changed.KnowledgePoints[0].Text = "Rebuilt metadata uses a different explanation."
	digest, err := capturedomain.ComputeProfileDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	replacement := organizingIntegrationID(146103)
	if err := f.db.Exec(`INSERT INTO learning.document_knowledge_profile_revision(id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,prompt_version,schema_version,content,content_digest,created_at)
 SELECT ?,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,'rebuilt/v2',schema_version,?::jsonb,?,clock_timestamp() FROM learning.document_knowledge_profile_revision WHERE id=?`, string(replacement), string(encoded), digest, string(frozen)).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO learning.document_knowledge_profile_evidence(revision_id,workspace_id,source_version_id,source_span_id,created_at) SELECT ?,workspace_id,source_version_id,source_span_id,clock_timestamp() FROM learning.document_knowledge_profile_evidence WHERE revision_id=?`, string(replacement), string(frozen)).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`UPDATE learning.document_knowledge_profile SET status='READY',current_revision_id=?,version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(replacement), string(revisionView.Revision.ProfileID)).Error; err != nil {
		t.Fatal(err)
	}
	current, err := directory.GetSourceKnowledgeDirectory(ctx, app.SourceKnowledgeDirectoryQuery{WorkspaceID: f.workspace, SourceVersionID: reference.Source.SourceVersionID})
	if err != nil || current.ProfileRevisionID != replacement || current.Points[0].Text == original.Points[0].Text {
		t.Fatalf("current=%+v error=%v", current, err)
	}

	second := f.generation(t, 147000, []app.SynthesisGenerationNote{{Note: detail.Note, Revision: *detail.CurrentRevision}}, "Independent queue knowledge.")
	second.Generation.Notes = []app.SynthesisGeneratedNote{{NoteID: detail.Note.ID, BaseRevisionID: detail.CurrentRevision.ID, TopicKey: detail.Note.TopicKey, Title: detail.Note.Title, Aliases: detail.Note.Aliases, Delta: f.newTopic(second, "unused").Delta}}
	second.Generation.Notes[0].Delta.Operations[0].Item.Fact.Text = "Independent queue knowledge."
	next, err := f.service.ApplyGeneration(ctx, second.Input, second.Generation)
	if err != nil {
		t.Fatal(err)
	}
	inherited := query
	inherited.RevisionID = next.RevisionIDs[0]
	if got, err := f.service.ReadSynthesisKnowledgeBinding(ctx, inherited); err != nil || got != frozen {
		t.Fatalf("inherited snapshot=%s error=%v", got, err)
	}
	// 新来源没有 Profile 测试数据；绝不替代为将来的元数据。
	unrecorded, err := f.service.ReadSynthesisKnowledgeBinding(ctx, app.SynthesisKnowledgeBindingQuery{NoteID: query.NoteID, RevisionID: inherited.RevisionID, Reference: second.Input.Sources[0].Reference})
	if err != nil || unrecorded != "" {
		t.Fatalf("unrecorded=%s error=%v", unrecorded, err)
	}
	empty, err := directory.ProjectSynthesisKnowledgeSnapshot(ctx, second.Input.Sources[0].Reference, unrecorded)
	if err != nil || empty.Directory.Status != app.KnowledgeDirectoryUnrecorded || len(empty.Points) != 0 {
		t.Fatalf("legacy=%+v error=%v", empty, err)
	}
	if err := f.db.Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE id=?`, string(reference.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	afterRemoval, err := f.service.ReadSynthesisKnowledgeBinding(ctx, query)
	if err != nil || afterRemoval != frozen {
		t.Fatalf("deleted source binding=%s error=%v", afterRemoval, err)
	}
	again, err := directory.ProjectSynthesisKnowledgeSnapshot(ctx, reference, afterRemoval)
	if err != nil || !reflect.DeepEqual(again, original) {
		t.Fatalf("historical snapshot changed: %v", err)
	}
	missing, err := directory.ProjectSynthesisKnowledgeSnapshot(ctx, reference, "")
	if err != nil || missing.Directory.Status != app.KnowledgeDirectoryUnrecorded || len(missing.Points) != 0 {
		t.Fatal("missing historical snapshot substituted current metadata", err)
	}
	foreign := query
	foreign.Reference.Source.WorkspaceID = f.otherWorkspace
	if _, err := f.service.ReadSynthesisKnowledgeBinding(ctx, foreign); err == nil {
		t.Fatal("foreign workspace read succeeded")
	}
	if _, err := profiles.GetProfileRevision(ctx, captureapp.ProfileRevisionQuery{WorkspaceID: f.workspace, SourceVersionID: organizingIntegrationID(147002), RevisionID: frozen}); err == nil {
		t.Fatal("foreign source profile read succeeded")
	}
	if err := f.db.Exec(`UPDATE organizing.synthesis_revision_source SET profile_revision_id=? WHERE revision_id=?`, string(replacement), string(query.RevisionID)).Error; err == nil {
		t.Fatal("historical snapshot mutation succeeded")
	}
}

func TestProfiledSynthesisClaimSkipsUnanalyzedSourcesInSameWorkspace(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 107)
	ctx := t.Context()
	missing := f.generation(t, 145000, nil, "Pending analysis.")
	ready := f.generation(t, 146000, nil, "Redis expiration controls cache lifetime.")
	seedDiscoveryRuntimeProfile(t, f, ready)
	// 保证未就绪事件最先出现，不依赖测试数据时钟。
	if err := f.db.Exec(`UPDATE workflow.outbox_event SET occurred_at=occurred_at-interval '1 day' WHERE id=?`, string(missing.Input.SourceEvent.ID)).Error; err != nil {
		t.Fatal(err)
	}
	base, err := workflowpostgres.NewGORMSourceReadyOutbox(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := owner.NewProfiledSynthesisOutbox(base)
	if err != nil {
		t.Fatal(err)
	}
	claim := func(excluded []foundation.ID, expected foundation.ID) {
		t.Helper()
		if err := f.uow.Within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope) error {
			fact, found, err := outbox.ClaimSourceReadyExcludingScoped(ctx, scope, excluded)
			if err != nil {
				return err
			}
			if found != (expected != "") || (found && fact.EventID != expected) {
				t.Fatalf("claim found=%v fact=%+v expected=%s", found, fact, expected)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	claim(nil, ready.Input.SourceEvent.ID)
	claim([]foundation.ID{f.workspace}, "")
	if err := f.db.Exec(`UPDATE learning.document_knowledge_profile SET status='RUNNING',version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(organizingIntegrationID(146101))).Error; err != nil {
		t.Fatal(err)
	}
	claim(nil, ready.Input.SourceEvent.ID)
}
