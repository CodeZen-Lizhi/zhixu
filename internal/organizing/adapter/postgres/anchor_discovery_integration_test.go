//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
)

func TestAnchorSourceDiscoveryPersistsFrozenProfileAndRequestBatches(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 105)
	ctx := t.Context()
	generation := f.generation(t, 151000, nil, "Redis discovery source.")
	generation.Generation.Notes = []app.SynthesisGeneratedNote{f.newTopic(generation, "redis discovery")}
	applied, err := f.service.ApplyGeneration(ctx, generation.Input, generation.Generation)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.store.GetSynthesisNote(ctx, f.workspace, applied.Publications[0].NoteID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMAnchorStore(f.platform, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.CreateAnchor(ctx, app.CreateAnchorCommand{
		WorkspaceID: f.workspace, IdempotencyKey: "discovery-anchor", NoteID: detail.Note.ID,
		ExpectedNoteVersion: detail.Note.Version, BasisRevisionID: detail.CurrentRevision.ID, Title: "Redis 专项",
		Scope: domain.AnchorScope{Topics: []string{"Redis"}, Audiences: []string{"review"}, Description: "Redis knowledge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	seedDiscoveryProcessing(t, f, generation)

	if count, err := store.SeedAnchorSourceDiscoveries(ctx, 10); err != nil || count != 1 {
		t.Fatalf("seed count=%d err=%v", count, err)
	}
	due, err := store.ListDueAnchorSourceDiscoveries(ctx, 10)
	if err != nil || len(due) != 1 || due[0].Status != app.AnchorDiscoveryWaiting || due[0].Source != generation.Input.SourceEvent.Source {
		t.Fatalf("due=%+v err=%v", due, err)
	}
	profileID := seedDiscoveryProfile(t, f, generation, 151100)
	frozen, err := store.FreezeAnchorSourceDiscovery(ctx, due[0], profileID, []domain.SynthesisSourceRef{generation.Input.Sources[0].Reference})
	if err != nil || frozen.Status != app.AnchorDiscoveryPrepared || frozen.ProfileRevisionID != profileID || len(frozen.Evidence) != 1 {
		t.Fatalf("frozen=%+v err=%v", frozen, err)
	}
	// 仍持有旧 WAITING 读取结果的第二个扫描者应获得第一个扫描者冻结的元组，不能用更新的标题覆盖它。
	other := generation.Input.Sources[0].Reference
	other.Title = "newer scanner title"
	winner, err := store.FreezeAnchorSourceDiscovery(ctx, due[0], profileID, []domain.SynthesisSourceRef{other})
	if err != nil || winner.Status != app.AnchorDiscoveryPrepared || winner.Evidence[0] != frozen.Evidence[0] {
		t.Fatalf("freeze winner=%+v err=%v", winner, err)
	}

	request, err := store.RequestAnchorSourceRecommendation(ctx, app.RequestAnchorSourceRecommendationCommand{
		WorkspaceID: f.workspace, AnchorID: anchor.Anchor.ID, ExpectedScopeVersion: anchor.Anchor.ScopeVersion,
		Source: frozen.Source, Evidence: frozen.Evidence, IdempotencyKey: "discovery-request-0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteAnchorSourceDiscovery(ctx, frozen, []foundation.ID{request.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteAnchorSourceDiscovery(ctx, frozen, []foundation.ID{request.ID}); err != nil {
		t.Fatalf("exact completion replay: %v", err)
	}
	if err := store.DeferAnchorSourceDiscovery(ctx, frozen, "SHOULD_NOT_REGRESS", false); err != nil {
		t.Fatalf("terminal defer changed discovery: %v", err)
	}
	if due, err = store.ListDueAnchorSourceDiscoveries(ctx, 10); err != nil || len(due) != 0 {
		t.Fatalf("terminal discovery due=%+v err=%v", due, err)
	}
	var row anchorSourceDiscoveryModel
	if err := f.db.Where("workspace_id=? AND processing_id=?", string(f.workspace), string(generation.Input.ProcessingID)).Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := row.discovery()
	if err != nil || stored.Status != app.AnchorDiscoveryRequested || len(stored.RequestIDs) != 1 || stored.RequestIDs[0] != request.ID || stored.Evidence[0] != frozen.Evidence[0] {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func seedDiscoveryProcessing(t *testing.T, f *synthesisDBFixture, generation app.SynthesisApplyRecord) {
	t.Helper()
	event := generation.Input.SourceEvent
	key, err := event.ProcessingKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := f.db.Exec(`INSERT INTO organizing.synthesis_processing(
 id,workspace_id,source_event_id,source_id,source_version_id,content_artifact_id,parse_projection_id,source_content_hash,
 ingestion_attempt_id,processor_version,source_occurred_at,processing_key,workflow_run_id,request_hash,status,revision_ids,version,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'PENDING','[]'::jsonb,1,?,?)`,
		string(generation.Input.ProcessingID), string(f.workspace), string(event.ID), string(event.Source.SourceID), string(event.Source.SourceVersionID),
		string(event.Source.ContentArtifactID), string(event.Source.ParseProjectionID), event.Source.ContentHash, string(event.IngestionAttemptID),
		event.ProcessorVersion, event.CreatedAt, key, string(generation.Input.WorkflowRunID), generation.Input.RequestHash, now, now).Error; err != nil {
		t.Fatal(err)
	}
}

func seedDiscoveryProfile(t *testing.T, f *synthesisDBFixture, generation app.SynthesisApplyRecord, seed int) foundation.ID {
	t.Helper()
	id := func(offset int) foundation.ID { return organizingIntegrationID(seed + offset) }
	source := generation.Input.SourceEvent.Source
	now := time.Now().UTC().Truncate(time.Microsecond)
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET CONSTRAINTS ALL DEFERRED").Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO core.capture(
 id,workspace_id,kind,display_name,original_location,original_input_hash,source_id,latest_source_version_id,
 status,fetch_status,ingestion_status,index_status,profile_status,version,captured_at,updated_at
) VALUES(?,?, 'FILE','Discovery profile',?, ?,?,?, 'READY','NOT_APPLICABLE','READY','NOT_APPLICABLE','READY',1,?,?)`,
			string(id(0)), string(f.workspace), "captures/discovery-"+string(id(0)), source.ContentHash, string(source.SourceID), string(source.SourceVersionID), now, now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO learning.document_knowledge_profile(
 id,workspace_id,capture_id,source_version_id,current_revision_id,status,error_code,retryable,version,created_at,updated_at
) VALUES(?,?,?,?,?,'READY','',false,1,?,?)`, string(id(1)), string(f.workspace), string(id(0)), string(source.SourceVersionID), string(id(2)), now, now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO learning.document_knowledge_profile_revision(
 id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,
 prompt_version,schema_version,content,content_digest,created_at
) VALUES(?,?,?,?,?,?,?,'v1','document-knowledge-profile/v1','{}'::jsonb,?,?)`,
			string(id(2)), string(id(1)), string(f.workspace), string(source.SourceVersionID), string(source.ParseProjectionID),
			string(organizingIntegrationID(seed-88)), string(generation.Generation.ModelRunID), source.ContentHash, now).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO learning.document_knowledge_profile_evidence(
 revision_id,workspace_id,source_version_id,source_span_id,created_at
) VALUES(?,?,?,?,?)`, string(id(2)), string(f.workspace), string(source.SourceVersionID), string(generation.Input.Sources[0].Reference.SourceSpanID), now).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	return id(2)
}
