//go:build integration

package postgres

import (
	"reflect"
	"sync"
	"testing"
	"time"

	app "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
)

func TestSynthesisSourceImpactsPersistWithoutChangingPublishedContent(t *testing.T) {
	f := newSynthesisGitFixtureAtVersion(t, 108)
	ctx := t.Context()
	record := f.generation(t, 171000, nil, "Persistent source evidence.")
	record.Generation.Notes = append(record.Generation.Notes, f.newTopic(record, "source impacts"), f.newTopic(record, "shared impacts"))
	result, err := f.service.ApplyGeneration(ctx, record.Input, record.Generation)
	if err != nil {
		t.Fatal(err)
	}
	note, err := f.service.GetNote(ctx, f.workspace, result.Publications[0].NoteID)
	if err != nil {
		t.Fatal(err)
	}
	execution := f.preparePublication(t, note)
	if _, err := f.node.Execute(ctx, execution.input, execution.identity); err != nil {
		t.Fatal(err)
	}
	before, err := f.service.GetNote(ctx, f.workspace, note.Note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.PublishedRevision == nil {
		t.Fatal("expected actual publication")
	}
	query := app.SynthesisSourceImpactQuery{WorkspaceID: f.workspace, NoteID: note.Note.ID, RevisionID: before.PublishedRevision.ID}
	if count, err := f.store.ReconcileSynthesisSourceImpacts(ctx, 1); err != nil || count != 0 {
		t.Fatalf("healthy count=%d err=%v", count, err)
	}
	ref := record.Input.Sources[0].Reference
	if err := f.db.Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE id=?`, string(ref.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	// 并发有界扫描最终收敛，不重复生成评审事实。
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.store.ReconcileSynthesisSourceImpacts(ctx, 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := f.store.ReconcileSynthesisSourceImpacts(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if count, err := f.store.ReconcileSynthesisSourceImpacts(ctx, 1); err != nil || count != 0 {
		t.Fatalf("repeat count=%d err=%v", count, err)
	}
	impacts, err := f.service.ReadSynthesisSourceImpacts(ctx, query)
	if err != nil || len(impacts.Items) != 1 {
		t.Fatalf("impacts=%+v err=%v", impacts, err)
	}
	impact := impacts.Items[0]
	if impact.DetectedAt.Location() != time.UTC {
		t.Fatal("source impact timestamp must satisfy the UTC HTTP contract")
	}
	if impact.Reference != ref || impact.Reason != "SOURCE_REMOVED" || !impact.CurrentlyUnavailable || len(impact.ItemIDs) != 1 || impact.ItemIDs[0] != before.PublishedRevision.Items[0].ID {
		t.Fatalf("wrong affected evidence %+v", impact)
	}
	again, err := f.service.ReadSynthesisSourceImpacts(ctx, query)
	if err != nil || !reflect.DeepEqual(again, impacts) {
		t.Fatal("persistent reread changed", err)
	}
	after, err := f.service.GetNote(ctx, f.workspace, note.Note.ID)
	if err != nil || !reflect.DeepEqual(after.PublishedRevision, before.PublishedRevision) || !reflect.DeepEqual(after.CurrentRevision, before.CurrentRevision) {
		t.Fatal("source loss changed content", err)
	}
	if err := f.db.Exec(`UPDATE core.source SET removed_at=NULL WHERE id=?`, string(ref.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	restored, err := f.service.ReadSynthesisSourceImpacts(ctx, query)
	if err != nil || len(restored.Items) != 1 || restored.Items[0].ID != impact.ID || restored.Items[0].CurrentlyUnavailable {
		t.Fatalf("restoration erased review history %+v %v", restored, err)
	}

	if err := f.db.Exec(`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at)
 SELECT ?,workspace_id,source_version_id,'validating','quarantined',parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,'source-impact-quarantine',2,clock_timestamp(),clock_timestamp()
 FROM ingestion.attempt WHERE id=?`, string(organizingIntegrationID(171050)), string(record.Input.SourceEvent.IngestionAttemptID)).Error; err != nil {
		t.Fatal(err)
	}
	if count, err := f.store.ReconcileSynthesisSourceImpacts(ctx, 100); err != nil || count != 2 {
		t.Fatalf("quarantine count=%d err=%v", count, err)
	}
	quarantined, err := f.service.ReadSynthesisSourceImpacts(ctx, query)
	if err != nil || len(quarantined.Items) != 2 {
		t.Fatalf("quarantine impacts=%+v err=%v", quarantined, err)
	}
	foundQuarantine := false
	for _, observation := range quarantined.Items {
		if observation.Reason == "SOURCE_QUARANTINED" {
			foundQuarantine = true
			if !observation.CurrentlyUnavailable {
				t.Fatal("quarantine incorrectly shown as recovered")
			}
		}
	}
	if !foundQuarantine {
		t.Fatal("quarantine reason missing")
	}

	if err := f.db.Exec(`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at)
 SELECT ?,workspace_id,source_version_id,'validating','pending',parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,'source-impact-recheck',3,clock_timestamp()
 FROM ingestion.attempt WHERE id=?`, string(organizingIntegrationID(171051)), string(record.Input.SourceEvent.IngestionAttemptID)).Error; err != nil {
		t.Fatal(err)
	}
	rechecking, err := f.service.ReadSynthesisSourceImpacts(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range rechecking.Items {
		if observation.Reason == "SOURCE_QUARANTINED" && !observation.CurrentlyUnavailable {
			t.Fatal("pending recheck incorrectly shown as recovered")
		}
	}
	query.WorkspaceID = f.otherWorkspace
	if _, err := f.service.ReadSynthesisSourceImpacts(ctx, query); err == nil {
		t.Fatal("cross-workspace impact read succeeded")
	}
	if err := f.db.Exec(`UPDATE organizing.synthesis_source_impact SET reason='SOURCE_QUARANTINED' WHERE id=?`, string(impact.ID)).Error; err == nil {
		t.Fatal("immutable review reason mutated")
	}
}
