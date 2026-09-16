//go:build integration

package postgres

import (
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
)

func TestGoalCatalogPagesCurrentMetadataWithoutOpeningOriginals(t *testing.T) {
	f := newSynthesisDBFixtureAtVersion(t, 108)
	ctx := t.Context()
	first := f.generation(t, 187000, nil, "Redis expiration controls cache lifetime.")
	second := f.generation(t, 188000, nil, "Oracle transactions preserve data integrity.")
	seedGoalCatalogProfile(t, f, first, 187000, "Redis")
	seedGoalCatalogProfile(t, f, second, 188000, "Oracle")
	runs, err := agentpostgres.NewGORMRepository(f.platform)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := capturepostgres.NewGORMProfileRepository(f.platform, runs)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := owner.NewSynthesisGoalCatalogReader(profiles, f.sources)
	if err != nil {
		t.Fatal(err)
	}
	// 产物接口不提供字节，元数据发现仍须正常工作。
	f.artifacts.items = nil
	read := func(after foundation.ID) app.SynthesisGoalCatalogPage {
		t.Helper()
		page, err := catalog.ReadSynthesisGoalCatalog(ctx, app.SynthesisGoalCatalogQuery{WorkspaceID: f.workspace, AfterSourceID: after, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	one := read("")
	if len(one.Items) != 1 || one.NextAfterSourceID == nil || one.Items[0].Source != first.Input.SourceEvent.Source || len(one.Items[0].Directory.Points) != 1 {
		t.Fatalf("first page %+v", one)
	}
	frozen := one.Items[0]
	two := read(*one.NextAfterSourceID)
	if len(two.Items) != 1 || two.NextAfterSourceID != nil || two.Items[0].Source != second.Input.SourceEvent.Source {
		t.Fatalf("second page %+v", two)
	}
	other, err := profiles.ListProfileDirectory(ctx, captureapp.ProfileDirectoryQuery{WorkspaceID: f.otherWorkspace, Limit: 1})
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("cross workspace %+v %v", other, err)
	}
	// 当前原始来源尚无已提交 Profile 时，目标应延后处理，不能将 Profile 目录中缺失的行当作空目录。
	firstProfileID, firstRevisionID := organizingIntegrationID(187101), organizingIntegrationID(187102)
	if err := f.db.Exec(`UPDATE learning.document_knowledge_profile SET status='PENDING',current_revision_id=NULL,version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(firstProfileID)).Error; err != nil {
		t.Fatal(err)
	}
	waiting := read("")
	if waiting.DeferredCode != "SYNTHESIS_GOAL_SOURCE_PENDING" || len(waiting.Items) != 0 || waiting.NextAfterSourceID != nil {
		t.Fatalf("pending profile froze an incomplete catalog %+v", waiting)
	}
	if err := f.db.Exec(`UPDATE core.capture SET ingestion_status='FAILED',version=version+1,updated_at=clock_timestamp() WHERE source_id=?`, string(first.Input.SourceEvent.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	ingestionFailed := read("")
	if ingestionFailed.DeferredCode != "SYNTHESIS_GOAL_SOURCE_PROCESSING_FAILED" {
		t.Fatalf("failed ingestion appeared pending %+v", ingestionFailed)
	}
	if err := f.db.Exec(`UPDATE core.capture SET ingestion_status='READY',version=version+1,updated_at=clock_timestamp() WHERE source_id=?`, string(first.Input.SourceEvent.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	// 重试处于 PENDING 或 RUNNING 时可能仍保留先前不可变版本；该版本仍适用于这个精确来源版本。
	if err := f.db.Exec(`UPDATE learning.document_knowledge_profile SET current_revision_id=?,version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(firstRevisionID), string(firstProfileID)).Error; err != nil {
		t.Fatal(err)
	}
	retrying := read("")
	if retrying.DeferredCode != "" || len(retrying.Items) != 1 || !reflect.DeepEqual(retrying.Items[0].Directory.Points, frozen.Directory.Points) {
		t.Fatalf("retrying profile blocked its usable revision %+v", retrying)
	}
	if err := f.db.Exec(`UPDATE learning.document_knowledge_profile SET status='FAILED',error_code='CAPTURE_PROFILE_FAILED',version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(firstProfileID)).Error; err != nil {
		t.Fatal(err)
	}
	failed := read("")
	if failed.DeferredCode != "SYNTHESIS_GOAL_PROFILE_FAILED" || len(failed.Items) != 0 {
		t.Fatalf("profile failure was hidden as an empty catalog %+v", failed)
	}
	// 重建可变 Profile 时，保留已提交的同版本目录。
	if err := f.db.Exec(`UPDATE learning.document_knowledge_profile SET status='RUNNING',error_code='',current_revision_id=?,version=version+1,updated_at=clock_timestamp() WHERE id=?`, string(firstRevisionID), string(firstProfileID)).Error; err != nil {
		t.Fatal(err)
	}
	rebuilding := read("")
	if len(rebuilding.Items) != 1 || !reflect.DeepEqual(rebuilding.Items[0].Directory.Points, frozen.Directory.Points) {
		t.Fatalf("rebuilding lost immutable points %+v", rebuilding)
	}
	// 先排除再应用 LIMIT，避免排序靠前的已移除来源遮挡后续来源。
	if err := f.db.Exec(`UPDATE core.source SET removed_at=clock_timestamp() WHERE id=?`, string(first.Input.SourceEvent.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	remaining := read("")
	if len(remaining.Items) != 1 || remaining.Items[0].Source != second.Input.SourceEvent.Source || remaining.NextAfterSourceID != nil {
		t.Fatalf("removed source blocked page %+v", remaining)
	}
	if err := f.db.Exec(`UPDATE core.source SET removed_at=NULL WHERE id=?`, string(first.Input.SourceEvent.Source.SourceID)).Error; err != nil {
		t.Fatal(err)
	}
	// 新登记版本尚无 Profile 时，即使 Capture 尚未跟进，也不能用旧 Profile 充当当前文件的元数据。
	if err := f.db.Exec(`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES(?,?,repeat('e',64),1,'.knowledge/sources/'||repeat('e',64),clock_timestamp())`, string(organizingIntegrationID(187189)), string(f.workspace)).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`INSERT INTO core.source_version(id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at)
 SELECT ?,source_id,workspace_id,?,repeat('e',64),1,mime_type,original_content_location,'pending',clock_timestamp() FROM core.source_version WHERE id=?`, string(organizingIntegrationID(187190)), string(organizingIntegrationID(187189)), string(first.Input.SourceEvent.Source.SourceVersionID)).Error; err != nil {
		t.Fatal(err)
	}
	newer := read("")
	if newer.DeferredCode != "SYNTHESIS_GOAL_SOURCE_PENDING" || len(newer.Items) != 0 {
		t.Fatalf("new source version without Capture was omitted from goal readiness %+v", newer)
	}
	if err := f.db.Exec(`INSERT INTO ingestion.attempt(id,workspace_id,source_version_id,status,security_status,parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,idempotency_key,attempt_number,started_at,completed_at)
 SELECT ?,workspace_id,source_version_id,'validating','quarantined',parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,'goal-catalog-quarantine',2,clock_timestamp(),clock_timestamp() FROM ingestion.attempt WHERE id=?`, string(organizingIntegrationID(188191)), string(second.Input.SourceEvent.IngestionAttemptID)).Error; err != nil {
		t.Fatal(err)
	}
	if page := read(""); len(page.Items) != 0 {
		t.Fatalf("quarantined catalog %+v", page)
	}
	// 已返回的定位信息仍指向原始不可变元数据。
	snapshot, err := profiles.GetProfileRevision(ctx, captureapp.ProfileRevisionQuery{WorkspaceID: f.workspace, SourceVersionID: frozen.Source.SourceVersionID, RevisionID: frozen.Directory.ProfileRevisionID})
	if err != nil || snapshot.Revision.Content.KnowledgePoints[0].Text != frozen.Directory.Points[0].Text {
		t.Fatalf("frozen point drifted %+v %v", snapshot, err)
	}
}

func seedGoalCatalogProfile(t *testing.T, f *synthesisDBFixture, record app.SynthesisApplyRecord, base int, topic string) {
	t.Helper()
	source := record.Input.SourceEvent.Source
	span := record.Input.Sources[0].Reference.SourceSpanID
	captureID, profileID, revisionID := organizingIntegrationID(base+100), organizingIntegrationID(base+101), organizingIntegrationID(base+102)
	content, err := capturedomain.NormalizeProfileContent(capturedomain.ProfileContent{Summary: topic + " knowledge", Topics: []capturedomain.ProfileCandidate{{Label: topic, SourceSpanIDs: []foundation.ID{span}}}, KnowledgePoints: []capturedomain.ProfilePoint{{Text: record.Input.Sources[0].Text, SourceSpanIDs: []foundation.ID{span}}}})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := capturedomain.ComputeProfileDigest(content)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.capture(id,workspace_id,kind,display_name,original_location,original_input_hash,source_id,latest_source_version_id,status,fetch_status,ingestion_status,index_status,profile_status,captured_at,updated_at) VALUES(?,?,'FILE',?,?,?, ?,?,'READY','NOT_APPLICABLE','READY','READY','READY',clock_timestamp(),clock_timestamp())`, []any{captureID, source.WorkspaceID, topic, "catalog-" + string(captureID) + ".md", source.ContentHash, source.SourceID, source.SourceVersionID}},
		{`INSERT INTO learning.document_knowledge_profile(id,workspace_id,capture_id,source_version_id,status,created_at,updated_at) VALUES(?,?,?,?,'PENDING',clock_timestamp(),clock_timestamp())`, []any{profileID, source.WorkspaceID, captureID, source.SourceVersionID}},
		{`INSERT INTO learning.document_knowledge_profile_revision(id,profile_id,workspace_id,source_version_id,parse_projection_id,index_version_id,model_run_id,prompt_version,schema_version,content,content_digest,created_at) VALUES(?,?,?,?,?,?,?,'v1','document-knowledge-profile/v1',?::jsonb,?,clock_timestamp())`, []any{revisionID, profileID, source.WorkspaceID, source.SourceVersionID, source.ParseProjectionID, organizingIntegrationID(base + 12), record.Generation.ModelRunID, string(raw), digest}},
		{`INSERT INTO learning.document_knowledge_profile_evidence(revision_id,workspace_id,source_version_id,source_span_id,created_at) VALUES(?,?,?,?,clock_timestamp())`, []any{revisionID, source.WorkspaceID, source.SourceVersionID, span}},
		{`UPDATE learning.document_knowledge_profile SET status='READY',current_revision_id=?,version=version+1,updated_at=clock_timestamp() WHERE id=?`, []any{revisionID, profileID}},
	}
	for _, statement := range statements {
		if err := f.db.Exec(statement.query, statement.args...).Error; err != nil {
			t.Fatal(err)
		}
	}
}
