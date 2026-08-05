//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	artifactpostgres "github.com/CodeZen-Lizhi/zhixu/internal/artifact/adapter/postgres"
	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	collectionpostgres "github.com/CodeZen-Lizhi/zhixu/internal/collection/adapter/postgres"
	collectiondomain "github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	organizingowner "github.com/CodeZen-Lizhi/zhixu/internal/organizing/adapter/owner"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestRepositoryPostgreSQLDraftTemplateSnapshotAndRuntimeLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newOrganizingIntegrationRepository(t, ctx)
	workspaceA := organizingIntegrationID(1)
	workspaceB := organizingIntegrationID(2)
	seedOrganizingWorkspace(t, ctx, pool, workspaceA, "organizing-a")
	seedOrganizingWorkspace(t, ctx, pool, workspaceB, "organizing-b")
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		t.Fatalf("idempotent built-in installation: %v", err)
	}
	builtInTemplates, builtInRevisions, err := domain.BuiltInTemplates(now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListTemplates(ctx, organizingapp.TemplateListQuery{WorkspaceID: workspaceA, Limit: 10})
	if err != nil || len(page.Items) != len(builtInTemplates) {
		t.Fatalf("built-in page=%#v err=%v", page, err)
	}

	customDeclaration := builtInRevisions[0].Declaration
	customDeclaration.Name = "Java AI 专题整理"
	customDeclaration.Description = "将 Java AI 零散材料整理为专题文章。"
	customTemplate, customRevision1, err := domain.NewCustomTemplate(
		organizingIntegrationID(10), organizingIntegrationID(11), workspaceA, customDeclaration, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	createTemplateBinding := organizingBinding(workspaceA, customTemplate.ID, "create-template", "create-template",
		organizingapp.CommandCreateTemplate, 0)
	createdTemplate, err := repository.CreateTemplate(ctx, organizingapp.CreateTemplateRecord{
		Binding: createTemplateBinding, Template: customTemplate, Revision: customRevision1,
	})
	if err != nil || createdTemplate.Replayed || createdTemplate.Detail.Template.Version != 1 {
		t.Fatalf("create template=%#v err=%v", createdTemplate, err)
	}
	revisedDeclaration := customRevision1.Declaration
	revisedDeclaration.Name = "Java AI 专题整理 v2"
	revisedDeclaration.AdditionalInstructions = "明确区分事实、冲突和知识缺口。"
	customTemplate2, customRevision2, err := domain.ReviseCustomTemplate(
		customTemplate, 1, organizingIntegrationID(12), revisedDeclaration, now.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	reviseTemplateBinding := organizingBinding(workspaceA, customTemplate.ID, "revise-template", "revise-template",
		organizingapp.CommandReviseTemplate, 1)
	revisedTemplate, err := repository.ReviseTemplate(ctx, organizingapp.ReviseTemplateRecord{
		Binding: reviseTemplateBinding, Template: customTemplate2, Revision: customRevision2,
	})
	if err != nil || revisedTemplate.Detail.Template.Version != 2 {
		t.Fatalf("revise template=%#v err=%v", revisedTemplate, err)
	}
	replayedTemplate, err := repository.CreateTemplate(ctx, organizingapp.CreateTemplateRecord{
		Binding: createTemplateBinding, Template: customTemplate, Revision: customRevision1,
	})
	if err != nil || !replayedTemplate.Replayed || replayedTemplate.Detail.Template.Version != 1 ||
		replayedTemplate.Detail.Revision.ID != customRevision1.ID {
		t.Fatalf("historical template replay=%#v err=%v", replayedTemplate, err)
	}
	currentTemplate, err := repository.GetTemplate(ctx, workspaceA, customTemplate.ID)
	if err != nil || currentTemplate.Template.Version != 2 || currentTemplate.Revision.ID != customRevision2.ID {
		t.Fatalf("current template=%#v err=%v", currentTemplate, err)
	}
	frozenTemplate, err := repository.GetFrozenTemplateRevision(ctx, workspaceA, customTemplate.ID, customRevision1.ID)
	if err != nil || frozenTemplate.Template.Version != 2 || frozenTemplate.Revision.ID != customRevision1.ID ||
		frozenTemplate.Revision.DeclarationHash != customRevision1.DeclarationHash {
		t.Fatalf("frozen historical template=%#v err=%v", frozenTemplate, err)
	}
	if _, err := repository.GetFrozenTemplateRevision(
		ctx, workspaceA, organizingIntegrationID(113), customRevision1.ID,
	); !organizingIntegrationError(err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound) {
		t.Fatalf("mismatched frozen template error=%v", err)
	}
	if _, err := repository.GetTemplate(ctx, workspaceB, customTemplate.ID); !organizingIntegrationError(
		err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound,
	) {
		t.Fatalf("cross-workspace template error=%v", err)
	}
	if _, err := repository.ReviseTemplate(ctx, organizingapp.ReviseTemplateRecord{
		Binding: organizingBinding(workspaceA, customTemplate.ID, "stale-template", "stale-template",
			organizingapp.CommandReviseTemplate, 1),
		Template: customTemplate2, Revision: customRevision2,
	}); !organizingIntegrationError(err, foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict) {
		t.Fatalf("stale template error=%v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE organizing.template_revision SET declaration_hash=$1 WHERE id=$2`,
		organizingIntegrationHash("mutated-template"), string(customRevision1.ID))
	organizingIntegrationPostgresCode(t, err, "55000")

	draft, err := domain.NewDraft(organizingIntegrationID(20), workspaceA, "整理 Java AI 知识点", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	createDraftBinding := organizingBinding(workspaceA, draft.ID, "create-draft", "create-draft",
		organizingapp.CommandCreateDraft, 0)
	createdDraft, err := repository.CreateDraft(ctx, organizingapp.CreateDraftRecord{Binding: createDraftBinding, Draft: draft})
	if err != nil || createdDraft.Replayed || createdDraft.Draft.Version != 1 || createdDraft.Draft.TemplateRevisionID != "" {
		t.Fatalf("create draft=%#v err=%v", createdDraft, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO organizing.workflow_input_snapshot(
		id,workspace_id,draft_id,draft_version,template_id,template_revision_id,
		template_hash,intent,snapshot_hash,created_at
	) VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,$9)`, string(organizingIntegrationID(75)), string(workspaceA),
		string(draft.ID), string(customTemplate.ID), string(customRevision1.ID), customRevision1.DeclarationHash,
		draft.Intent, organizingIntegrationHash("stale-template-snapshot"), now.Add(3*time.Minute))
	organizingIntegrationPostgresCode(t, err, "23514")
	findCreateBinding := createDraftBinding
	findCreateBinding.AggregateID = ""
	foundCreate, found, err := repository.FindDraftCommand(ctx, findCreateBinding)
	if err != nil || !found || !foundCreate.Replayed || foundCreate.Draft.ID != draft.ID {
		t.Fatalf("find server-generated create=%#v found=%v err=%v", foundCreate, found, err)
	}
	foreignDraft, err := domain.NewDraft(organizingIntegrationID(114), workspaceB, "cross workspace template", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateDraft(ctx, organizingapp.CreateDraftRecord{
		Binding: organizingBinding(workspaceB, foreignDraft.ID, "create-foreign-draft", "create-foreign-draft",
			organizingapp.CommandCreateDraft, 0),
		Draft: foreignDraft,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: organizingBinding(workspaceB, foreignDraft.ID, "select-cross-workspace-template",
			"select-cross-workspace-template", organizingapp.CommandUpdateDraft, 1),
		Intent: foreignDraft.Intent, TemplateRevisionID: customRevision2.ID, UpdatedAt: now.Add(4 * time.Minute),
	}); !organizingIntegrationError(err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound) {
		t.Fatalf("cross-workspace draft template error=%v", err)
	}
	if _, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: organizingBinding(workspaceB, foreignDraft.ID, "select-missing-template", "select-missing-template",
			organizingapp.CommandUpdateDraft, 1),
		Intent: foreignDraft.Intent, TemplateRevisionID: organizingIntegrationID(115), UpdatedAt: now.Add(4 * time.Minute),
	}); !organizingIntegrationError(err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound) {
		t.Fatalf("missing draft template error=%v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO organizing.draft_version(
		workspace_id,draft_id,version,intent,status,template_revision_id,confirmed_snapshot_id,draft_created_at,updated_at
	) VALUES($1,$2,2,$3,'EDITING',$4,NULL,$5,$6)`, string(workspaceB), string(foreignDraft.ID), foreignDraft.Intent,
		string(customRevision2.ID), foreignDraft.CreatedAt, now.Add(4*time.Minute))
	organizingIntegrationPostgresCode(t, err, "23514")

	sourceReference := domain.MaterialRef{
		Kind: domain.MaterialSourceVersion, SourceVersionID: organizingIntegrationID(21),
		ContentHash: organizingIntegrationHash("source-content"), Evidence: []domain.EvidenceRef{},
	}
	collectionReference := domain.MaterialRef{
		Kind: domain.MaterialSmartCollection, CollectionID: organizingIntegrationID(22), Version: 1,
		QueryHash:         organizingIntegrationHash("collection-query"),
		ReadModelRevision: organizingIntegrationHash("collection-read-model"), Evidence: []domain.EvidenceRef{},
	}
	draftMaterials := []domain.DraftMaterial{
		{
			ID: organizingIntegrationID(23), Ref: sourceReference, Title: "Java AI 基础",
			Reasons: []domain.SuggestionReasonCode{domain.ReasonHybridMatch}, Origin: domain.MaterialOriginSuggested,
			Availability: domain.MaterialAvailable, Score: 0.91, Selected: true,
		},
		{
			ID: organizingIntegrationID(24), Ref: collectionReference, Title: "Java AI 材料集合",
			Reasons: []domain.SuggestionReasonCode{domain.ReasonUserAdded}, Origin: domain.MaterialOriginUser,
			Availability: domain.MaterialAvailable, Score: 1, Selected: true,
		},
	}
	replaceBinding := organizingBinding(workspaceA, draft.ID, "replace-materials", "replace-materials",
		organizingapp.CommandReplaceSuggestions, 1)
	replaced, err := repository.ReplaceMaterials(ctx, organizingapp.ReplaceMaterialsRecord{
		Binding: replaceBinding, Materials: draftMaterials, UpdatedAt: now.Add(4 * time.Minute),
	})
	if err != nil || replaced.Draft.Version != 2 || len(replaced.Draft.Materials) != 2 {
		t.Fatalf("replace materials=%#v err=%v", replaced, err)
	}
	updateBinding := organizingBinding(workspaceA, draft.ID, "update-intent", "update-intent",
		organizingapp.CommandUpdateDraft, 2)
	updated, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: updateBinding, Intent: "整理 Java AI 的 RAG、Agent 和评测知识点",
		TemplateRevisionID: builtInRevisions[0].ID, UpdatedAt: now.Add(5 * time.Minute),
	})
	if err != nil || updated.Draft.Version != 3 || updated.Draft.TemplateRevisionID != builtInRevisions[0].ID ||
		len(updated.Draft.Materials) != 2 {
		t.Fatalf("update intent=%#v err=%v", updated, err)
	}
	replayedUpdate, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: updateBinding, Intent: "整理 Java AI 的 RAG、Agent 和评测知识点",
		TemplateRevisionID: builtInRevisions[0].ID, UpdatedAt: now.Add(5 * time.Minute),
	})
	if err != nil || !replayedUpdate.Replayed || !reflect.DeepEqual(replayedUpdate.Draft, updated.Draft) {
		t.Fatalf("update replay=%#v want=%#v err=%v", replayedUpdate, updated, err)
	}
	replayedReplace, err := repository.ReplaceMaterials(ctx, organizingapp.ReplaceMaterialsRecord{
		Binding: replaceBinding, Materials: draftMaterials, UpdatedAt: now.Add(4 * time.Minute),
	})
	if err != nil || !replayedReplace.Replayed || !reflect.DeepEqual(replayedReplace.Draft, replaced.Draft) {
		t.Fatalf("historical draft replay=%#v want=%#v err=%v", replayedReplace, replaced, err)
	}
	_, err = repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: organizingBinding(workspaceA, draft.ID, "stale-draft", "stale-draft",
			organizingapp.CommandUpdateDraft, 2),
		Intent: "stale", TemplateRevisionID: builtInRevisions[0].ID, UpdatedAt: now.Add(6 * time.Minute),
	})
	if !organizingIntegrationError(err, foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict) {
		t.Fatalf("stale draft error=%v", err)
	}

	expandedReference := domain.MaterialRef{
		Kind: domain.MaterialDocumentRevision, DocumentID: organizingIntegrationID(25),
		ArticleRevisionID: organizingIntegrationID(26), OriginCollectionID: collectionReference.CollectionID,
		Version: 4, ContentHash: organizingIntegrationHash("expanded-document"), Evidence: []domain.EvidenceRef{},
	}
	frozenMaterials := []domain.MaterialRef{sourceReference, collectionReference, expandedReference}
	confirmBinding := organizingBinding(workspaceA, draft.ID, "confirm-draft", "confirm-draft",
		organizingapp.CommandConfirmDraft, 3)
	confirmFence := &organizingBlockingFence{entered: make(chan struct{}), release: make(chan struct{})}
	confirmRecord := organizingapp.ConfirmRecord{
		Binding: confirmBinding, Fence: confirmFence, SnapshotID: organizingIntegrationID(27),
		SnapshotMaterialIDs: []foundation.ID{organizingIntegrationID(28), organizingIntegrationID(29), organizingIntegrationID(30)},
		OutboxID:            organizingIntegrationID(31), TemplateID: builtInTemplates[0].ID,
		TemplateRevisionID: builtInRevisions[0].ID, TemplateHash: builtInRevisions[0].DeclarationHash,
		FrozenMaterials: frozenMaterials, ConfirmedAt: now.Add(6 * time.Minute),
	}
	mismatchedConfirm := confirmRecord
	mismatchedConfirm.Binding = organizingBinding(workspaceA, draft.ID, "confirm-draft-template-mismatch",
		"confirm-draft-template-mismatch", organizingapp.CommandConfirmDraft, 3)
	mismatchedConfirm.TemplateID = builtInTemplates[1].ID
	mismatchedConfirm.TemplateRevisionID = builtInRevisions[1].ID
	mismatchedConfirm.TemplateHash = builtInRevisions[1].DeclarationHash
	if _, err := repository.ConfirmDraft(ctx, mismatchedConfirm); !organizingIntegrationError(
		err, foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict,
	) {
		t.Fatalf("mismatched draft template confirmation error=%v", err)
	}
	type confirmCall struct {
		result organizingapp.ConfirmResult
		err    error
	}
	confirmCalls := make(chan confirmCall, 2)
	go func() {
		result, callErr := repository.ConfirmDraft(ctx, confirmRecord)
		confirmCalls <- confirmCall{result: result, err: callErr}
	}()
	select {
	case <-confirmFence.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		result, callErr := repository.ConfirmDraft(ctx, confirmRecord)
		confirmCalls <- confirmCall{result: result, err: callErr}
	}()
	waitForOrganizingAdvisoryLock(t, ctx, pool)
	close(confirmFence.release)

	var confirmed, concurrentReplay organizingapp.ConfirmResult
	confirmedCalls, replayedCalls := 0, 0
	for range 2 {
		call := <-confirmCalls
		if call.err != nil {
			t.Fatalf("concurrent confirmation error=%v", call.err)
		}
		if call.result.Replayed {
			concurrentReplay = call.result
			replayedCalls++
		} else {
			confirmed = call.result
			confirmedCalls++
		}
	}
	if confirmedCalls != 1 || replayedCalls != 1 || confirmFence.calls.Load() != 1 ||
		concurrentReplay.Snapshot.ID != confirmed.Snapshot.ID || concurrentReplay.OutboxID != confirmed.OutboxID {
		t.Fatalf("concurrent confirmation confirmed=%d replayed=%d fence_calls=%d confirmed=%#v replay=%#v",
			confirmedCalls, replayedCalls, confirmFence.calls.Load(), confirmed, concurrentReplay)
	}
	if confirmed.Replayed || confirmed.Draft.Status != domain.DraftConfirmed ||
		confirmed.Draft.Version != 4 || confirmed.Draft.TemplateRevisionID != builtInRevisions[0].ID ||
		confirmed.Snapshot.DraftVersion != 3 ||
		len(confirmed.Snapshot.Materials) != len(frozenMaterials) ||
		confirmed.Snapshot.Materials[2].OriginCollectionID != collectionReference.CollectionID {
		t.Fatalf("confirm=%#v err=%v", confirmed, err)
	}
	replayedConfirm, err := repository.ConfirmDraft(ctx, confirmRecord)
	if err != nil || !replayedConfirm.Replayed || replayedConfirm.OutboxID != confirmed.OutboxID ||
		!reflect.DeepEqual(replayedConfirm.Draft, confirmed.Draft) ||
		!reflect.DeepEqual(replayedConfirm.Snapshot, confirmed.Snapshot) || confirmFence.calls.Load() != 1 {
		t.Fatalf("confirm replay=%#v want=%#v err=%v", replayedConfirm, confirmed, err)
	}
	pendingProjection, err := repository.GetRunProjection(ctx, workspaceA, confirmed.Snapshot.ID)
	if err != nil || pendingProjection.Status != organizingapp.StartPending || pendingProjection.AttemptCount != 0 ||
		pendingProjection.LastErrorCode != "" || pendingProjection.Binding != nil || pendingProjection.Result != nil {
		t.Fatalf("pending run projection=%#v err=%v", pendingProjection, err)
	}
	if _, err := repository.GetDraft(ctx, workspaceB, draft.ID); !organizingIntegrationError(
		err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound,
	) {
		t.Fatalf("cross-workspace draft error=%v", err)
	}
	if _, err := repository.GetSnapshot(ctx, workspaceB, confirmed.Snapshot.ID); !organizingIntegrationError(
		err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound,
	) {
		t.Fatalf("cross-workspace snapshot error=%v", err)
	}
	if _, err := repository.GetRunProjection(ctx, workspaceB, confirmed.Snapshot.ID); !organizingIntegrationError(
		err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound,
	) {
		t.Fatalf("cross-workspace run projection error=%v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO organizing.workflow_input_material(
		id,workspace_id,snapshot_id,position,kind,source_version_id,origin_collection_id,
		material_version,content_hash,evidence,created_at
	) VALUES($1,$2,$3,3,'SOURCE_VERSION',$4,$5,0,$6,'[]'::jsonb,$7)`,
		string(organizingIntegrationID(32)), string(workspaceA), string(confirmed.Snapshot.ID),
		string(organizingIntegrationID(33)), string(organizingIntegrationID(34)),
		organizingIntegrationHash("orphaned-collection-member"), now)
	organizingIntegrationPostgresCode(t, err, "23514")

	lease, claimed, err := repository.ClaimStart(ctx, "organizing-worker-a", 30*time.Second)
	if err != nil || !claimed || lease.ID != confirmRecord.OutboxID || lease.Version != 2 {
		t.Fatalf("first claim=%#v claimed=%v err=%v", lease, claimed, err)
	}
	if err := repository.RetryStart(ctx, organizingapp.RetryStartRecord{
		Lease: lease, ErrorCode: "WORKFLOW_TEMPORARY_FAILURE", Delay: 0,
	}); err != nil {
		t.Fatalf("retry start: %v", err)
	}
	retryProjection, err := repository.GetRunProjection(ctx, workspaceA, confirmed.Snapshot.ID)
	if err != nil || retryProjection.Status != organizingapp.StartPending || retryProjection.AttemptCount != 1 ||
		retryProjection.LastErrorCode != "WORKFLOW_TEMPORARY_FAILURE" || retryProjection.Binding != nil {
		t.Fatalf("retry run projection=%#v err=%v", retryProjection, err)
	}
	lease, claimed, err = repository.ClaimStart(ctx, "organizing-worker-b", 30*time.Second)
	if err != nil || !claimed || lease.ID != confirmRecord.OutboxID || lease.Version != 4 || lease.AttemptCount != 2 {
		t.Fatalf("second claim=%#v claimed=%v err=%v", lease, claimed, err)
	}
	workflowRunID := organizingIntegrationID(40)
	workflowNodeRunID := organizingIntegrationID(41)
	seedOrganizingWorkflow(t, ctx, pool, workspaceA, confirmed.Snapshot.ID,
		organizingIntegrationID(39), workflowRunID, workflowNodeRunID, now)
	completeRecord := organizingapp.CompleteStartRecord{
		Lease: lease, BindingID: organizingIntegrationID(42), WorkflowRunID: workflowRunID,
		DefinitionKey: "organizing.topic-article", DefinitionVersion: 1,
		StartedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	runBinding, replayed, err := repository.CompleteStart(ctx, completeRecord)
	if err != nil || replayed || runBinding.ID != completeRecord.BindingID || runBinding.SnapshotID != confirmed.Snapshot.ID {
		t.Fatalf("complete start=%#v replayed=%v err=%v", runBinding, replayed, err)
	}
	startedProjection, err := repository.GetRunProjection(ctx, workspaceA, confirmed.Snapshot.ID)
	if err != nil || startedProjection.Status != organizingapp.StartStarted || startedProjection.AttemptCount != 2 ||
		startedProjection.LastErrorCode != "WORKFLOW_TEMPORARY_FAILURE" || startedProjection.Binding == nil ||
		!reflect.DeepEqual(*startedProjection.Binding, runBinding) || startedProjection.Result != nil {
		t.Fatalf("started run projection=%#v err=%v", startedProjection, err)
	}
	replayedRecord := completeRecord
	replayedRecord.BindingID = organizingIntegrationID(43)
	replayedRecord.StartedAt = completeRecord.StartedAt.Add(time.Minute)
	replayedBinding, replayed, err := repository.CompleteStart(ctx, replayedRecord)
	if err != nil || !replayed || !reflect.DeepEqual(replayedBinding, runBinding) {
		t.Fatalf("complete replay=%#v replayed=%v want=%#v err=%v", replayedBinding, replayed, runBinding, err)
	}
	if _, err := repository.GetRunBinding(ctx, workspaceB, confirmed.Snapshot.ID); !organizingIntegrationError(
		err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound,
	) {
		t.Fatalf("cross-workspace run binding error=%v", err)
	}
	wrongDefinitionID := organizingIntegrationID(70)
	wrongWorkflowRunID := organizingIntegrationID(71)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'organizing.wrong-definition',1,'{}',$3)`, string(wrongDefinitionID), string(workspaceA), now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at
	) VALUES($1,$2,$3,'pending','{}',1,$4,$4)`, string(wrongWorkflowRunID), string(workspaceA),
		string(wrongDefinitionID), now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO organizing.run_binding(
		id,workspace_id,snapshot_id,workflow_run_id,definition_key,definition_version,created_at
	) VALUES($1,$2,$3,$4,'organizing.topic-article',1,$5)`, string(organizingIntegrationID(72)),
		string(workspaceA), string(confirmed.Snapshot.ID), string(wrongWorkflowRunID), now)
	organizingIntegrationPostgresCode(t, err, "23514")
	wrongStartRunID := organizingIntegrationID(73)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,version,created_at,updated_at,idempotency_key,request_hash
	) VALUES($1,$2,$3,'running',jsonb_build_object('snapshot_id',$4::text),1,$5,$5,$6,$7)`,
		string(wrongStartRunID), string(workspaceA), string(organizingIntegrationID(39)), string(confirmed.Snapshot.ID), now,
		"snapshot:"+string(organizingIntegrationID(999)), organizingIntegrationHash("wrong-workflow-start")); err != nil {
		organizingIntegrationFatal(t, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO organizing.run_binding(
		id,workspace_id,snapshot_id,workflow_run_id,definition_key,definition_version,created_at
	) VALUES($1,$2,$3,$4,'organizing.topic-article',1,$5)`, string(organizingIntegrationID(74)),
		string(workspaceA), string(confirmed.Snapshot.ID), string(wrongStartRunID), now)
	organizingIntegrationPostgresCode(t, err, "23514")
	wrongGenerationNodeID := organizingIntegrationID(400)
	wrongGenerationAttemptID := organizingIntegrationID(401)
	wrongGenerationID := organizingIntegrationID(402)
	leaseUntil := now.Add(2 * time.Hour)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,attempt,input,idempotency_key,input_schema_version,
		output_schema_version,dispatch_no,lease_owner,lease_until,version,created_at,updated_at
	) VALUES($1,$2,'generate','model.organizing','running',1,'{}','wrong-generation-node',1,1,1,
		'organizing-worker',$3,1,$4,$4)`, string(wrongGenerationNodeID), string(wrongStartRunID), leaseUntil, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at,heartbeat_at
	) VALUES($1,$2,1,1,0,'wrong-generation-delivery','organizing-worker',$3,'running',$4,$4)`,
		string(wrongGenerationAttemptID), string(wrongGenerationNodeID), leaseUntil, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO organizing.generation(
		id,workspace_id,snapshot_id,workflow_run_id,node_run_id,node_attempt_id,generation_kind,
		request_hash,status,retryable,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,'OUTLINE',$7,'RUNNING',false,1,$8,$8)`,
		string(wrongGenerationID), string(workspaceA), string(confirmed.Snapshot.ID), string(wrongStartRunID),
		string(wrongGenerationNodeID), string(wrongGenerationAttemptID), organizingIntegrationHash("wrong-generation"), now)
	organizingIntegrationPostgresCode(t, err, "23514")
	var wrongGenerationCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM organizing.generation WHERE id=$1`, string(wrongGenerationID)).Scan(&wrongGenerationCount); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if wrongGenerationCount != 0 {
		t.Fatalf("mismatched generation persisted count=%d", wrongGenerationCount)
	}
	resultArtifact, resultRevision, err := artifactdomain.PlanArtifact(artifactdomain.PlanInput{
		ArtifactID: organizingIntegrationID(45), InitialRevisionID: organizingIntegrationID(75),
		WorkspaceID: workspaceA, Type: "SUMMARY", Title: "整理结果", ScopeDefinition: "整理工作流冻结结果",
		CreatedAt: completeRecord.StartedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	artifactRepository, err := artifactpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := artifactRepository.Create(ctx, artifactapp.CreateRecord{
		Binding: artifactapp.CommandBinding{
			WorkspaceID: workspaceA, ArtifactID: resultArtifact.ID, IdempotencyKey: "organizing-result-artifact",
			RequestHash: organizingIntegrationHash("organizing-result-artifact"), CommandType: artifactapp.CommandPlan,
		},
		State: artifactapp.State{Artifact: resultArtifact, Revision: resultRevision},
	}); err != nil {
		t.Fatal(err)
	}
	runResult := domain.RunResult{
		ID: organizingIntegrationID(44), WorkspaceID: workspaceA, RunBindingID: runBinding.ID,
		SnapshotID: confirmed.Snapshot.ID, WorkflowRunID: workflowRunID, NodeRunID: workflowNodeRunID,
		Kind: domain.ResultArtifact, ResultRef: resultArtifact.ID,
		ResultHash: resultRevision.ContentHash, CreatedAt: completeRecord.StartedAt.Add(time.Minute),
	}
	wrongKindResult := runResult
	wrongKindResult.ID = organizingIntegrationID(48)
	wrongKindResult.Kind = domain.ResultMergeProposal
	if _, _, err := repository.BindRunResult(ctx, organizingapp.BindRunResultRecord{Result: wrongKindResult}); !organizingIntegrationError(err, foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeResultInvalid) {
		t.Fatalf("wrong result kind error=%v", err)
	}
	missingResult := runResult
	missingResult.ID = organizingIntegrationID(76)
	missingResult.ResultRef = organizingIntegrationID(77)
	if _, _, err := repository.BindRunResult(ctx, organizingapp.BindRunResultRecord{Result: missingResult}); !organizingIntegrationError(err, foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeResultInvalid) {
		t.Fatalf("missing result owner error=%v", err)
	}
	wrongHashResult := runResult
	wrongHashResult.ID = organizingIntegrationID(78)
	wrongHashResult.ResultHash = organizingIntegrationHash("wrong-artifact-result")
	if _, _, err := repository.BindRunResult(ctx, organizingapp.BindRunResultRecord{Result: wrongHashResult}); !organizingIntegrationError(err, foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeResultInvalid) {
		t.Fatalf("wrong result hash error=%v", err)
	}
	boundResult, replayed, err := repository.BindRunResult(ctx, organizingapp.BindRunResultRecord{Result: runResult})
	if err != nil || replayed || !reflect.DeepEqual(boundResult, runResult) {
		t.Fatalf("bind result=%#v replayed=%v err=%v", boundResult, replayed, err)
	}
	resultProjection, err := repository.GetRunProjection(ctx, workspaceA, confirmed.Snapshot.ID)
	if err != nil || resultProjection.Binding == nil || resultProjection.Result == nil ||
		!reflect.DeepEqual(*resultProjection.Binding, runBinding) || !reflect.DeepEqual(*resultProjection.Result, runResult) {
		t.Fatalf("result run projection=%#v err=%v", resultProjection, err)
	}
	resultReplayRequest := runResult
	resultReplayRequest.ID = organizingIntegrationID(46)
	resultReplayRequest.CreatedAt = runResult.CreatedAt.Add(time.Minute)
	replayedResult, replayed, err := repository.BindRunResult(ctx, organizingapp.BindRunResultRecord{Result: resultReplayRequest})
	if err != nil || !replayed || !reflect.DeepEqual(replayedResult, runResult) {
		t.Fatalf("result replay=%#v replayed=%v want=%#v err=%v", replayedResult, replayed, runResult, err)
	}
	if _, err := repository.GetRunResult(ctx, workspaceB, workflowRunID); !organizingIntegrationError(
		err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound,
	) {
		t.Fatalf("cross-workspace run result error=%v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE organizing.run_result SET result_ref=$1 WHERE id=$2`,
		string(organizingIntegrationID(47)), string(runResult.ID))
	organizingIntegrationPostgresCode(t, err, "55000")

	rollbackDraft, err := domain.NewDraft(organizingIntegrationID(50), workspaceA, "确认失败必须整体回滚", now.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateDraft(ctx, organizingapp.CreateDraftRecord{
		Binding: organizingBinding(workspaceA, rollbackDraft.ID, "create-rollback-draft", "create-rollback-draft",
			organizingapp.CommandCreateDraft, 0),
		Draft: rollbackDraft,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO organizing.draft_version(
		workspace_id,draft_id,version,intent,status,confirmed_snapshot_id,draft_created_at,updated_at
	) VALUES($1,$2,2,$3,'CONFIRMED',$4,$5,$6)`, string(workspaceA), string(rollbackDraft.ID), rollbackDraft.Intent,
		string(confirmed.Snapshot.ID), rollbackDraft.CreatedAt, rollbackDraft.UpdatedAt.Add(time.Minute))
	organizingIntegrationPostgresCode(t, err, "23514")
	if _, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: organizingBinding(workspaceA, rollbackDraft.ID, "select-rollback-template", "select-rollback-template",
			organizingapp.CommandUpdateDraft, 1),
		Intent: rollbackDraft.Intent, TemplateRevisionID: builtInRevisions[0].ID,
		UpdatedAt: now.Add(8 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	rollbackMaterial := domain.DraftMaterial{
		ID: organizingIntegrationID(51), Ref: sourceReference, Title: "原子确认材料",
		Reasons: []domain.SuggestionReasonCode{domain.ReasonUserAdded}, Origin: domain.MaterialOriginUser,
		Availability: domain.MaterialAvailable, Score: 1, Selected: true,
	}
	if _, err := repository.ReplaceMaterials(ctx, organizingapp.ReplaceMaterialsRecord{
		Binding: organizingBinding(workspaceA, rollbackDraft.ID, "replace-rollback-draft", "replace-rollback-draft",
			organizingapp.CommandReplaceSuggestions, 2),
		Materials: []domain.DraftMaterial{rollbackMaterial}, UpdatedAt: now.Add(8 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	rollbackSnapshotID := organizingIntegrationID(52)
	_, err = repository.ConfirmDraft(ctx, organizingapp.ConfirmRecord{
		Binding: organizingBinding(workspaceA, rollbackDraft.ID, "confirm-rollback-draft", "confirm-rollback-draft",
			organizingapp.CommandConfirmDraft, 3),
		Fence:      &organizingIntegrationFence{},
		SnapshotID: rollbackSnapshotID, SnapshotMaterialIDs: []foundation.ID{organizingIntegrationID(53)},
		OutboxID: confirmRecord.OutboxID, TemplateID: builtInTemplates[0].ID,
		TemplateRevisionID: builtInRevisions[0].ID, TemplateHash: builtInRevisions[0].DeclarationHash,
		FrozenMaterials: []domain.MaterialRef{sourceReference}, ConfirmedAt: now.Add(9 * time.Minute),
	})
	if !organizingIntegrationError(err, foundation.ErrorConsistencyViolation, organizingapp.ErrorCodeResultInvalid) {
		t.Fatalf("duplicate outbox confirmation error=%v", err)
	}
	rolledBack, err := repository.GetDraft(ctx, workspaceA, rollbackDraft.ID)
	if err != nil || rolledBack.Status != domain.DraftEditing || rolledBack.Version != 3 ||
		rolledBack.TemplateRevisionID != builtInRevisions[0].ID {
		t.Fatalf("draft after confirmation rollback=%#v err=%v", rolledBack, err)
	}
	if _, err := repository.GetSnapshot(ctx, workspaceA, rollbackSnapshotID); !organizingIntegrationError(
		err, foundation.ErrorNotFound, organizingapp.ErrorCodeNotFound,
	) {
		t.Fatalf("rolled-back snapshot error=%v", err)
	}
	var outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizing.workflow_start_outbox`).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox count=%d err=%v", outboxCount, err)
	}
}

func TestConfirmDraftPostgreSQLRejectsOwnerDriftAtomicallyAndReplaysWithoutFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newOrganizingIntegrationRepository(t, ctx)
	workspaceID := organizingIntegrationID(300)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-confirm-fence")
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	templates, revisions, err := domain.BuiltInTemplates(now)
	if err != nil {
		t.Fatal(err)
	}
	sourceVersionID := organizingIntegrationID(303)
	contentHash := organizingIntegrationHash("confirm-fence-source")
	seedOrganizingFenceSource(t, ctx, pool, workspaceID, sourceVersionID, contentHash, now)

	draft, err := domain.NewDraft(organizingIntegrationID(310), workspaceID, "transaction owner fence", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateDraft(ctx, organizingapp.CreateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "fence-create", "fence-create", organizingapp.CommandCreateDraft, 0),
		Draft:   draft,
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "fence-template", "fence-template", organizingapp.CommandUpdateDraft, 1),
		Intent:  draft.Intent, TemplateRevisionID: revisions[0].ID, UpdatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	frozen := domain.MaterialRef{Kind: domain.MaterialSourceVersion, SourceVersionID: sourceVersionID,
		ContentHash: contentHash, Evidence: []domain.EvidenceRef{}}
	if _, err := repository.ReplaceMaterials(ctx, organizingapp.ReplaceMaterialsRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "fence-material", "fence-material", organizingapp.CommandReplaceSuggestions, 2),
		Materials: []domain.DraftMaterial{{ID: organizingIntegrationID(311), Ref: frozen, Title: "frozen source",
			Reasons: []domain.SuggestionReasonCode{domain.ReasonUserAdded}, Origin: domain.MaterialOriginUser,
			Availability: domain.MaterialAvailable, Score: 1, Selected: true}},
		UpdatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}

	countingFence := &organizingDelegatingFence{delegate: new(organizingowner.Adapter)}
	snapshotID, outboxID := organizingIntegrationID(313), organizingIntegrationID(315)
	record := organizingapp.ConfirmRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "fence-confirm", "fence-confirm", organizingapp.CommandConfirmDraft, 3),
		Fence:   countingFence, SnapshotID: snapshotID, SnapshotMaterialIDs: []foundation.ID{organizingIntegrationID(314)},
		OutboxID: outboxID, TemplateID: templates[0].ID, TemplateRevisionID: revisions[0].ID,
		TemplateHash: revisions[0].DeclarationHash, FrozenMaterials: []domain.MaterialRef{frozen}, ConfirmedAt: now.Add(5 * time.Minute),
	}
	staleDraftFence := &organizingIntegrationFence{err: foundation.NewError(
		foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale, false, errors.New("owner drift must not mask draft CAS"),
	)}
	staleDraftRecord := record
	staleDraftRecord.Binding = organizingBinding(workspaceID, draft.ID, "fence-stale-draft", "fence-stale-draft", organizingapp.CommandConfirmDraft, 2)
	staleDraftRecord.Fence = staleDraftFence
	staleDraftRecord.SnapshotID = organizingIntegrationID(318)
	staleDraftRecord.SnapshotMaterialIDs = []foundation.ID{organizingIntegrationID(319)}
	staleDraftRecord.OutboxID = organizingIntegrationID(320)
	if _, err := repository.ConfirmDraft(ctx, staleDraftRecord); !organizingIntegrationError(
		err, foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict,
	) {
		t.Fatalf("stale draft confirmation error=%v", err)
	}
	if staleDraftFence.calls != 0 {
		t.Fatalf("stale draft reached owner fence calls=%d", staleDraftFence.calls)
	}
	insertOrganizingFenceAttempt(t, ctx, pool, workspaceID, sourceVersionID, organizingIntegrationID(312),
		organizingIntegrationID(305), "parse_failed", "fence-drift", now.Add(4*time.Minute))
	if _, err := repository.ConfirmDraft(ctx, record); !organizingIntegrationError(
		err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale,
	) {
		t.Fatalf("owner drift confirmation error=%v", err)
	}
	if countingFence.calls != 1 {
		t.Fatalf("owner fence calls after rejected confirmation=%d", countingFence.calls)
	}
	assertOrganizingConfirmationAbsent(t, ctx, pool, workspaceID, draft.ID, snapshotID, outboxID, record.Binding.IdempotencyKey)
	current, err := repository.GetDraft(ctx, workspaceID, draft.ID)
	if err != nil || current.Status != domain.DraftEditing || current.Version != 3 || current.ConfirmedSnapshotID != "" {
		t.Fatalf("draft after owner drift=%#v err=%v", current, err)
	}

	insertOrganizingFenceAttempt(t, ctx, pool, workspaceID, sourceVersionID, organizingIntegrationID(316),
		organizingIntegrationID(305), "chunked", "fence-restored", now.Add(6*time.Minute))
	confirmed, err := repository.ConfirmDraft(ctx, record)
	if err != nil || confirmed.Replayed || confirmed.Snapshot.ID != snapshotID || confirmed.OutboxID != outboxID || countingFence.calls != 2 {
		t.Fatalf("confirmation after owner recovery=%#v fence_calls=%d err=%v", confirmed, countingFence.calls, err)
	}

	insertOrganizingFenceAttempt(t, ctx, pool, workspaceID, sourceVersionID, organizingIntegrationID(317),
		organizingIntegrationID(305), "parse_failed", "fence-after-commit", now.Add(7*time.Minute))
	replayed, err := repository.ConfirmDraft(ctx, record)
	if err != nil || !replayed.Replayed || replayed.Snapshot.ID != snapshotID || replayed.OutboxID != outboxID || countingFence.calls != 2 {
		t.Fatalf("confirmation replay after later drift=%#v fence_calls=%d err=%v", replayed, countingFence.calls, err)
	}
}

func TestConfirmDraftPostgreSQLRejectsFrozenEvidenceAfterActiveIndexDrift(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newOrganizingIntegrationRepository(t, ctx)
	workspaceID := organizingIntegrationID(330)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-evidence-fence")
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	templates, revisions, err := domain.BuiltInTemplates(now)
	if err != nil {
		t.Fatal(err)
	}
	sourceVersionID := organizingIntegrationID(333)
	contentHash := organizingIntegrationHash("evidence-fence-source")
	fixture := seedOrganizingFenceSource(t, ctx, pool, workspaceID, sourceVersionID, contentHash, now)
	claimReference := seedOrganizingFenceClaim(t, ctx, pool, workspaceID, sourceVersionID, contentHash, fixture, now)

	draft, err := domain.NewDraft(organizingIntegrationID(340), workspaceID, "active index evidence fence", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateDraft(ctx, organizingapp.CreateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "evidence-create", "evidence-create", organizingapp.CommandCreateDraft, 0),
		Draft:   draft,
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "evidence-template", "evidence-template", organizingapp.CommandUpdateDraft, 1),
		Intent:  draft.Intent, TemplateRevisionID: revisions[0].ID, UpdatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := repository.ReplaceMaterials(ctx, organizingapp.ReplaceMaterialsRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "evidence-material", "evidence-material", organizingapp.CommandReplaceSuggestions, 2),
		Materials: []domain.DraftMaterial{{ID: organizingIntegrationID(341), Ref: claimReference, Title: "frozen claim",
			Reasons: []domain.SuggestionReasonCode{domain.ReasonFormalKnowledge}, Origin: domain.MaterialOriginSuggested,
			Availability: domain.MaterialAvailable, Score: 1, Selected: true}},
		UpdatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}

	retireOrganizingFenceIndex(t, ctx, pool, fixture.indexID, now.Add(4*time.Minute))
	fence := &organizingDelegatingFence{delegate: new(organizingowner.Adapter)}
	snapshotID, outboxID := organizingIntegrationID(343), organizingIntegrationID(345)
	record := organizingapp.ConfirmRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "evidence-confirm", "evidence-confirm", organizingapp.CommandConfirmDraft, 3),
		Fence:   fence, SnapshotID: snapshotID, SnapshotMaterialIDs: []foundation.ID{organizingIntegrationID(344)},
		OutboxID: outboxID, TemplateID: templates[0].ID, TemplateRevisionID: revisions[0].ID,
		TemplateHash: revisions[0].DeclarationHash, FrozenMaterials: []domain.MaterialRef{claimReference}, ConfirmedAt: now.Add(5 * time.Minute),
	}
	if _, err := repository.ConfirmDraft(ctx, record); !organizingIntegrationError(
		err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale,
	) {
		t.Fatalf("inactive frozen evidence confirmation error=%v", err)
	}
	if fence.calls != 1 {
		t.Fatalf("evidence fence calls=%d", fence.calls)
	}
	assertOrganizingConfirmationAbsent(t, ctx, pool, workspaceID, draft.ID, snapshotID, outboxID, record.Binding.IdempotencyKey)
	current, err := repository.GetDraft(ctx, workspaceID, draft.ID)
	if err != nil || current.Status != domain.DraftEditing || current.Version != 3 || current.ConfirmedSnapshotID != "" {
		t.Fatalf("draft after active index evidence drift=%#v err=%v", current, err)
	}
}

func TestConfirmDraftPostgreSQLReturnsStableConflictAfterConcurrentIdempotencyWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newOrganizingIntegrationRepository(t, ctx)
	workspaceID := organizingIntegrationID(360)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-confirm-idempotency-conflict")
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	templates, revisions, err := domain.BuiltInTemplates(now)
	if err != nil {
		t.Fatal(err)
	}
	fence := &organizingBlockingFence{entered: make(chan struct{}), release: make(chan struct{})}
	winner := seedOrganizingConfirmRecord(t, ctx, repository, workspaceID, templates[0], revisions[0], fence, 370, now)
	conflict := winner
	conflict.Binding.RequestHash = organizingIntegrationHash("concurrent-conflicting-confirm")

	winnerCall := make(chan confirmIntegrationCall, 1)
	conflictCall := make(chan confirmIntegrationCall, 1)
	go func() {
		result, callErr := repository.ConfirmDraft(ctx, winner)
		winnerCall <- confirmIntegrationCall{result: result, err: callErr}
	}()
	select {
	case <-fence.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		result, callErr := repository.ConfirmDraft(ctx, conflict)
		conflictCall <- confirmIntegrationCall{result: result, err: callErr}
	}()
	waitForOrganizingAdvisoryLock(t, ctx, pool)
	close(fence.release)

	winning := <-winnerCall
	if winning.err != nil || winning.result.Replayed || winning.result.Snapshot.ID != winner.SnapshotID {
		t.Fatalf("winning confirmation=%#v err=%v", winning.result, winning.err)
	}
	conflicted := <-conflictCall
	if !organizingIntegrationError(conflicted.err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeIdempotencyConflict) {
		t.Fatalf("concurrent conflicting confirmation=%#v err=%v", conflicted.result, conflicted.err)
	}
	if fence.calls.Load() != 1 {
		t.Fatalf("concurrent conflicting confirmation fence calls=%d", fence.calls.Load())
	}
}

func TestConfirmDraftPostgreSQLRevalidatesCollectionWithoutLeakingStatementTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newOrganizingIntegrationRepository(t, ctx)
	workspaceID := organizingIntegrationID(390)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-collection-fence")
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	templates, revisions, err := domain.BuiltInTemplates(now)
	if err != nil {
		t.Fatal(err)
	}
	query, err := collectiondomain.CanonicalizeQuery(collectiondomain.Query{
		SchemaVersion: collectiondomain.QuerySchemaVersionV1,
		Root: collectiondomain.Clause{Kind: collectiondomain.ClauseKindGroup, Operator: string(collectiondomain.GroupOperatorAND),
			Clauses: []collectiondomain.Clause{{Kind: collectiondomain.ClauseKindPredicate, Field: "object_type",
				Operator: string(collectiondomain.OperatorEQ), Value: json.RawMessage(`"CLAIM"`)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectionID := organizingIntegrationID(391)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.smart_collection(
		id,workspace_id,name,normalized_name,description,query_schema_version,query_version,
		query_definition,query_hash,view_type,view_config,status,cached_result_version,last_executed_at,
		version,created_at,updated_at
	) VALUES($1,$2,'Formal claims','formal claims','',$3,1,$4::jsonb,$5,'LIST','{}'::jsonb,'ACTIVE',NULL,NULL,1,$6,$6)`,
		string(collectionID), string(workspaceID), collectiondomain.QuerySchemaVersionV1,
		string(query.CanonicalJSON), query.Hash, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	collectionRepository, err := collectionpostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := collectionRepository.PlanDurableScan(ctx, workspaceID, collectionID)
	if err != nil {
		organizingIntegrationFatal(t, err)
	}
	reference := domain.MaterialRef{Kind: domain.MaterialSmartCollection, CollectionID: collectionID,
		Version: binding.CollectionVersion, QueryHash: binding.QueryHash, ReadModelRevision: binding.ReadModelRevision,
		Evidence: []domain.EvidenceRef{}}
	var baselineTimeout string
	if err := pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&baselineTimeout); err != nil {
		t.Fatal(err)
	}
	fence := &organizingStatementTimeoutFence{delegate: new(organizingowner.Adapter)}
	record := seedOrganizingConfirmRecordWithReference(t, ctx, repository, workspaceID, templates[0], revisions[0], fence, reference, 400, now)
	confirmed, err := repository.ConfirmDraft(ctx, record)
	if err != nil || confirmed.Replayed || confirmed.Snapshot.ID != record.SnapshotID {
		t.Fatalf("collection confirmation=%#v err=%v", confirmed, err)
	}
	if fence.calls != 1 || fence.statementTimeout != baselineTimeout {
		t.Fatalf("collection fence calls=%d statement_timeout=%q want=%q", fence.calls, fence.statementTimeout, baselineTimeout)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace_read_model_revision(
		workspace_id,knowledge_revision,conflict_revision,health_revision,updated_at
	) VALUES($1,1,0,0,$2)
	ON CONFLICT(workspace_id) DO UPDATE SET
		knowledge_revision=core.workspace_read_model_revision.knowledge_revision+1,
		updated_at=EXCLUDED.updated_at`, string(workspaceID), now.Add(5*time.Minute)); err != nil {
		organizingIntegrationFatal(t, err)
	}
	staleFence := &organizingDelegatingFence{delegate: new(organizingowner.Adapter)}
	staleRecord := seedOrganizingConfirmRecordWithReference(t, ctx, repository, workspaceID, templates[0], revisions[0], staleFence, reference, 410, now.Add(6*time.Minute))
	if _, err := repository.ConfirmDraft(ctx, staleRecord); !organizingIntegrationError(
		err, foundation.ErrorVersionConflict, organizingapp.ErrorCodeMaterialStale,
	) {
		t.Fatalf("stale collection confirmation error=%v", err)
	}
	if staleFence.calls != 1 {
		t.Fatalf("stale collection fence calls=%d", staleFence.calls)
	}
	assertOrganizingConfirmationAbsent(t, ctx, pool, workspaceID, staleRecord.Binding.AggregateID,
		staleRecord.SnapshotID, staleRecord.OutboxID, staleRecord.Binding.IdempotencyKey)
}

func TestOrganizingMigrationEmptyDownUpAndGuardedDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newOrganizingIntegrationDatabase(t, ctx)
	provider := organizingMigrationProvider(t, pool)
	if _, err := provider.DownTo(ctx, 73); err != nil {
		organizingIntegrationFatal(t, err)
	}
	var schemaRemoved bool
	if err := pool.QueryRow(ctx, `SELECT to_regnamespace('organizing') IS NULL`).Scan(&schemaRemoved); err != nil || !schemaRemoved {
		t.Fatalf("organizing schema removed=%v err=%v", schemaRemoved, err)
	}
	if _, err := provider.UpTo(ctx, 74); err != nil {
		organizingIntegrationFatal(t, err)
	}
	workspaceID := organizingIntegrationID(60)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-down-guard")
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT organizing.valid_material_shape(
		'SOURCE_VERSION',$1::uuid,NULL,NULL,NULL,NULL,NULL,NULL,0,NULL,NULL,NULL,'[]'::jsonb,false
	)`, string(organizingIntegrationID(62))).Scan(&valid); err != nil || valid {
		t.Fatalf("nullable required material field accepted=%v err=%v", valid, err)
	}
	if err := pool.QueryRow(ctx, `SELECT organizing.valid_reason_codes(ARRAY[NULL]::text[])`).Scan(&valid); err != nil || valid {
		t.Fatalf("nullable material reason accepted=%v err=%v", valid, err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO organizing.command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,aggregate_id,expected_version,created_at
	) VALUES($1,'missing-result',$2,'CREATE_DRAFT',$3,0,$4)`, string(workspaceID),
		organizingIntegrationHash("missing-result"), string(organizingIntegrationID(63)), time.Now().UTC())
	organizingIntegrationPostgresCode(t, err, "23514")
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	draft, err := domain.NewDraft(organizingIntegrationID(61), workspaceID, "guarded down", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateDraft(ctx, organizingapp.CreateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "guarded-down", "guarded-down",
			organizingapp.CommandCreateDraft, 0),
		Draft: draft,
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	_, err = provider.DownTo(ctx, 73)
	organizingIntegrationPostgresCode(t, err, "55000")
}

func TestRepositoryPostgreSQLPoisonsStartAtAttemptLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newOrganizingIntegrationRepository(t, ctx)
	workspaceID := organizingIntegrationID(90)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-start-attempt-limit")
	if _, _, err := repository.ClaimStart(ctx, "attempt-limit-worker", time.Nanosecond); !organizingIntegrationError(
		err, foundation.ErrorInvalidInput, organizingapp.ErrorCodeRequestInvalid,
	) {
		t.Fatalf("sub-microsecond lease error=%v", err)
	}
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	builtIns, revisions, err := domain.BuiltInTemplates(now)
	if err != nil {
		t.Fatal(err)
	}
	outboxID := createOrganizingPendingStart(t, ctx, repository, workspaceID, builtIns[0], revisions[0], 91, now)
	advanceOrganizingStartRetries(t, ctx, pool, outboxID, maxStartAttempts-1)
	lease, claimed, err := repository.ClaimStart(ctx, "attempt-limit-worker", 30*time.Second)
	if err != nil || !claimed || lease.ID != outboxID || lease.AttemptCount != maxStartAttempts {
		t.Fatalf("limit claim lease=%#v claimed=%v err=%v", lease, claimed, err)
	}
	if err := repository.RetryStart(ctx, organizingapp.RetryStartRecord{
		Lease: lease, ErrorCode: "WORKFLOW_TEMPORARY_FAILURE", Delay: 0,
	}); err != nil {
		t.Fatalf("limit retry: %v", err)
	}
	assertOrganizingStartPoisoned(t, ctx, pool, outboxID, "WORKFLOW_TEMPORARY_FAILURE")
	poisonedProjection, err := repository.GetRunProjection(ctx, workspaceID, organizingIntegrationID(94))
	if err != nil || poisonedProjection.Status != organizingapp.StartPoisoned ||
		poisonedProjection.AttemptCount != maxStartAttempts ||
		poisonedProjection.LastErrorCode != "WORKFLOW_TEMPORARY_FAILURE" ||
		poisonedProjection.Binding != nil || poisonedProjection.Result != nil {
		t.Fatalf("poisoned run projection=%#v err=%v", poisonedProjection, err)
	}

	expiredOutboxID := createOrganizingPendingStart(t, ctx, repository, workspaceID, builtIns[0], revisions[0], 101, now)
	advanceOrganizingStartRetries(t, ctx, pool, expiredOutboxID, maxStartAttempts-1)
	expiredLease, claimed, err := repository.ClaimStart(ctx, "expired-limit-worker", time.Microsecond)
	if err != nil || !claimed || expiredLease.ID != expiredOutboxID || expiredLease.AttemptCount != maxStartAttempts {
		t.Fatalf("expired limit claim lease=%#v claimed=%v err=%v", expiredLease, claimed, err)
	}
	time.Sleep(time.Millisecond)
	if lease, claimed, err := repository.ClaimStart(ctx, "next-worker", time.Second); err != nil || claimed {
		t.Fatalf("claim after expired exhaustion lease=%#v claimed=%v err=%v", lease, claimed, err)
	}
	assertOrganizingStartPoisoned(t, ctx, pool, expiredOutboxID, "MAX_ATTEMPTS")
}

func TestRepositoryPostgreSQLSerializesIdempotentCreateAndDraftCAS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, pool := newOrganizingIntegrationRepository(t, ctx)
	workspaceID := organizingIntegrationID(80)
	seedOrganizingWorkspace(t, ctx, pool, workspaceID, "organizing-concurrency")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repository.EnsureBuiltIns(ctx, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	_, revisions, err := domain.BuiltInTemplates(now)
	if err != nil {
		t.Fatal(err)
	}

	draft, err := domain.NewDraft(organizingIntegrationID(81), workspaceID, "concurrent create", now)
	if err != nil {
		t.Fatal(err)
	}
	createRecord := organizingapp.CreateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, "concurrent-create", "concurrent-create",
			organizingapp.CommandCreateDraft, 0),
		Draft: draft,
	}
	type draftCall struct {
		result organizingapp.DraftResult
		err    error
	}
	start := make(chan struct{})
	creates := make(chan draftCall, 2)
	for range 2 {
		go func() {
			<-start
			result, callErr := repository.CreateDraft(ctx, createRecord)
			creates <- draftCall{result: result, err: callErr}
		}()
	}
	close(start)
	replayed := 0
	for range 2 {
		call := <-creates
		if call.err != nil || call.result.Draft.ID != draft.ID || call.result.Draft.Version != 1 {
			t.Fatalf("concurrent create=%#v err=%v", call.result, call.err)
		}
		if call.result.Replayed {
			replayed++
		}
	}
	if replayed != 1 {
		t.Fatalf("concurrent create replay count=%d, want 1", replayed)
	}

	start = make(chan struct{})
	updates := make(chan draftCall, 2)
	for index, intent := range []string{"winner-a", "winner-b"} {
		binding := organizingBinding(workspaceID, draft.ID, fmt.Sprintf("concurrent-update-%d", index),
			fmt.Sprintf("concurrent-update-%d", index), organizingapp.CommandUpdateDraft, 1)
		go func() {
			<-start
			result, callErr := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
				Binding: binding, Intent: intent, TemplateRevisionID: revisions[0].ID, UpdatedAt: now.Add(time.Minute),
			})
			updates <- draftCall{result: result, err: callErr}
		}()
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		call := <-updates
		switch {
		case call.err == nil && call.result.Draft.Version == 2:
			succeeded++
		case organizingIntegrationError(call.err, foundation.ErrorVersionConflict, domain.ErrorCodeVersionConflict):
			conflicted++
		default:
			t.Fatalf("concurrent update=%#v err=%v", call.result, call.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent CAS succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	current, err := repository.GetDraft(ctx, workspaceID, draft.ID)
	if err != nil || current.Version != 2 {
		t.Fatalf("current draft=%#v err=%v", current, err)
	}
	var receipts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizing.command_receipt
		WHERE workspace_id=$1 AND aggregate_id=$2`, string(workspaceID), string(draft.ID)).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("receipt count=%d err=%v", receipts, err)
	}
}

func newOrganizingIntegrationRepository(t *testing.T, ctx context.Context) (*Repository, *pgxpool.Pool) {
	t.Helper()
	pool := newOrganizingIntegrationDatabase(t, ctx)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	return repository, pool
}

func newOrganizingIntegrationDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a disposable PostgreSQL instance")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_organizing_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	runner, err := platformmigration.NewRunner(pool, projectmigrations.FS)
	if err == nil {
		err = runner.Up(ctx)
	}
	if err != nil {
		organizingIntegrationFatal(t, err)
	}
	return pool
}

func organizingMigrationProvider(t *testing.T, pool *pgxpool.Pool) *goose.Provider {
	t.Helper()
	database := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = database.Close() })
	annotated, err := platformmigration.NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, database, annotated, goose.WithTableName("goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func seedOrganizingWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id foundation.ID, name string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	fingerprint := sha256.Sum256([]byte(name))
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,root_fingerprint,binding_version,git_repository_path,git_checked_at,
		status,availability,availability_reason,availability_checked_at,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,1,$3,$5,'inactive','available',NULL,$5,1,$5,$5)`,
		string(id), name, "/tmp/"+name, hex.EncodeToString(fingerprint[:]), now); err != nil {
		organizingIntegrationFatal(t, err)
	}
}

type organizingFenceSourceFixture struct {
	indexID     foundation.ID
	chunkID     foundation.ID
	spanID      foundation.ID
	excerptHash string
}

func seedOrganizingFenceSource(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sourceVersionID foundation.ID,
	contentHash string,
	now time.Time,
) organizingFenceSourceFixture {
	t.Helper()
	sourceID, artifactID := organizingIntegrationID(301), organizingIntegrationID(302)
	spanID := organizingIntegrationID(304)
	projectionID, attemptID := organizingIntegrationID(305), organizingIntegrationID(306)
	indexID, chunkID := organizingIntegrationID(307), organizingIntegrationID(318)
	excerptHash := organizingIntegrationHash("fence-excerpt")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		  VALUES($1,$2,'file','Fence source','docs/fence.md',$3)`, []any{string(sourceID), string(workspaceID), now}},
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at)
		  VALUES($1,$2,$3,1,'.knowledge/sources/' || $3,$4)`, []any{string(artifactID), string(workspaceID), contentHash, now}},
		{`INSERT INTO core.source_version(
			id,source_id,workspace_id,content_artifact_id,content_hash,byte_size,mime_type,
			original_content_location,security_status,captured_at
		  ) VALUES($1,$2,$3,$4,$5,1,'text/markdown','docs/fence.md','passed',$6)`,
			[]any{string(sourceVersionID), string(sourceID), string(workspaceID), string(artifactID), contentHash, now}},
		{`INSERT INTO ingestion.parse_projection(
			id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,
			schema_version,normalized_content_hash,created_at
		  ) VALUES($1,$2,$3,'goldmark','goldmark-v1',$4,'schema-v1',$5,$6)`,
			[]any{string(projectionID), string(workspaceID), string(artifactID), organizingIntegrationHash("fence-parser"), contentHash, now}},
		{`INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at)
		  VALUES($1,$2,$3,$4)`, []any{string(sourceVersionID), string(projectionID), string(workspaceID), now}},
		{`INSERT INTO ingestion.source_span(
			id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,
			start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at
		  ) VALUES($1,$2,$3,$4,'paragraph',1,1,0,1,'{"kind":"paragraph"}',$5,'goldmark-v1','schema-v1',$6)`,
			[]any{string(spanID), string(workspaceID), string(artifactID), string(projectionID), excerptHash, now}},
		{`INSERT INTO ingestion.canonical_chunk(
			id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
			byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at
		  ) VALUES($1,$2,$3,0,'[]','x',$4,$5,1,1,'goldmark-v1','chunk-v1','schema-v1',false,'active',$6)`,
			[]any{string(chunkID), string(workspaceID), string(projectionID), contentHash, string(spanID), now}},
		{`INSERT INTO ingestion.attempt(
			id,workspace_id,source_version_id,parse_projection_id,status,security_status,
			parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,
			idempotency_key,attempt_number,started_at,completed_at
		  ) VALUES($1,$2,$3,$4,'chunked','passed','goldmark','goldmark-v1',$5,'chunk-v1','schema-v1','fence-initial',1,$6,$6)`,
			[]any{string(attemptID), string(workspaceID), string(sourceVersionID), string(projectionID), organizingIntegrationHash("fence-parser"), now}},
		{`INSERT INTO retrieval.index_version(
			id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
			source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,
			degraded_capabilities,version,created_at,updated_at,
			source_manifest_hash,expected_source_count,source_parser_id,source_parser_version,
			source_parser_config_hash,source_chunk_strategy_version,source_schema_version
		  ) VALUES($1,$2,'default','v1',$3,'{}','fence-snapshot',$4,1,'fence-index','building',
			'["vector"]',1,$5,$5,$6,1,'goldmark','goldmark-v1',$3,'chunk-v1','schema-v1')`,
			[]any{string(indexID), string(workspaceID), organizingIntegrationHash("fence-parser"),
				organizingIntegrationHash("fence-manifest"), now, organizingIntegrationHash("fence-source-manifest")}},
		{`INSERT INTO retrieval.index_manifest_source(
			index_version_id,workspace_id,source_id,source_version_id,parse_projection_id,selection_status,created_at
		  ) VALUES($1,$2,$3,$4,$5,'included',$6)`,
			[]any{string(indexID), string(workspaceID), string(sourceID), string(sourceVersionID), string(projectionID), now}},
		{`INSERT INTO retrieval.index_manifest_chunk(
			index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,
			chunk_strategy_version,schema_version,created_at
		  ) VALUES($1,$2,$3,$4,0,'goldmark-v1','chunk-v1','schema-v1',$5)`,
			[]any{string(indexID), string(chunkID), string(workspaceID), contentHash, now}},
		{`INSERT INTO retrieval.chunk_projection(
			index_version_id,chunk_id,workspace_id,search_vector,token_count,lexical_status,vector_status,created_at,updated_at
		  ) VALUES($1,$2,$3,to_tsvector('simple','x'),1,'ready','disabled',$4,$4)`,
			[]any{string(indexID), string(chunkID), string(workspaceID), now}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			organizingIntegrationFatal(t, err)
		}
	}
	builtAt := now.Add(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE retrieval.index_version
		SET status='ready',version=2,built_at=$2,updated_at=$2 WHERE id=$1`, string(indexID), builtAt); err != nil {
		organizingIntegrationFatal(t, err)
	}
	activatedAt := now.Add(2 * time.Second)
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO retrieval.index_activation(
			id,kind,workspace_id,target_index_version_id,target_version,idempotency_key,reason_code,created_at
		) VALUES($1,'activate',$2,$3,3,'fence-activate','organizing-confirm-fence',$4)`,
			string(organizingIntegrationID(308)), string(workspaceID), string(indexID), activatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE retrieval.index_version
			SET status='active',version=3,activated_at=$2,updated_at=$2 WHERE id=$1`, string(indexID), activatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	return organizingFenceSourceFixture{indexID: indexID, chunkID: chunkID, spanID: spanID, excerptHash: excerptHash}
}

func seedOrganizingFenceClaim(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sourceVersionID foundation.ID,
	contentHash string,
	fixture organizingFenceSourceFixture,
	now time.Time,
) domain.MaterialRef {
	t.Helper()
	applicability, err := knowledgedomain.ParseApplicability(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	statement, normalized, err := knowledgedomain.NormalizeStatement("冻结证据只能来自当前活动索引")
	if err != nil {
		t.Fatal(err)
	}
	claim := knowledgedomain.Claim{
		ID: organizingIntegrationID(334), WorkspaceID: workspaceID, Statement: statement, NormalizedStatement: normalized,
		Applicability: applicability, Status: knowledgedomain.ClaimStatusConfirmed,
		ConfidenceFactors: json.RawMessage(`{}`), Version: 2, CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	claim.Fingerprint = knowledgedomain.ComputeClaimFingerprint(claim.WorkspaceID, claim.NormalizedStatement, claim.Applicability)
	source := knowledgedomain.ClaimSource{
		ID: organizingIntegrationID(335), WorkspaceID: workspaceID, ClaimID: claim.ID,
		Provenance:  knowledgedomain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: sourceVersionID, SourceSpanID: fixture.spanID},
		SupportType: knowledgedomain.ClaimSupportSupports, Reason: "原文直接支持", CreatedAt: now,
	}
	source.EvidenceHash = knowledgedomain.ComputeClaimSourceEvidenceHash(source, applicability)
	if err := knowledgedomain.ValidateClaimAggregate(claim, []knowledgedomain.ClaimSource{source}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.claim(
		id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
		applicability_hash,status,confidence_factors,fingerprint,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,'SUGGESTED',$8,$9,1,$10,$10)`,
		string(claim.ID), string(workspaceID), claim.Statement, claim.NormalizedStatement, claim.Applicability.CanonicalJSON,
		claim.Applicability.SchemaVersion, claim.Applicability.Hash, claim.ConfidenceFactors,
		claim.Fingerprint, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.claim_source(
		id,workspace_id,claim_id,source_version_id,source_span_id,support_type,reason,evidence_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, string(source.ID), string(workspaceID), string(claim.ID),
		string(sourceVersionID), string(fixture.spanID), string(source.SupportType), source.Reason, source.EvidenceHash, now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.claim
		SET status='CONFIRMED',version=2,updated_at=$2 WHERE id=$1`, string(claim.ID), claim.UpdatedAt); err != nil {
		organizingIntegrationFatal(t, err)
	}
	return domain.MaterialRef{Kind: domain.MaterialClaim, ClaimID: claim.ID, Version: claim.Version,
		ContentHash: claim.Fingerprint, Evidence: []domain.EvidenceRef{{
			IndexVersionID: fixture.indexID, ChunkID: fixture.chunkID, SourceVersionID: sourceVersionID,
			SourceSpanID: fixture.spanID, ContentHash: contentHash, ExcerptHash: fixture.excerptHash,
		}}}
}

func retireOrganizingFenceIndex(t *testing.T, ctx context.Context, pool *pgxpool.Pool, indexID foundation.ID, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE retrieval.index_version
		SET status='retiring',version=4,retired_at=$2,updated_at=$2 WHERE id=$1`, string(indexID), at); err != nil {
		organizingIntegrationFatal(t, err)
	}
}

func insertOrganizingFenceAttempt(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, sourceVersionID, attemptID, projectionID foundation.ID,
	status, key string,
	startedAt time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO ingestion.attempt(
		id,workspace_id,source_version_id,parse_projection_id,status,security_status,
		parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,
		idempotency_key,attempt_number,started_at,completed_at
	) VALUES($1,$2,$3,$4,$5,'passed','goldmark','goldmark-v1',$6,'chunk-v1','schema-v1',$7,1,$8,$8)`,
		string(attemptID), string(workspaceID), string(sourceVersionID), string(projectionID), status,
		organizingIntegrationHash("fence-parser"), key, startedAt); err != nil {
		organizingIntegrationFatal(t, err)
	}
}

func assertOrganizingConfirmationAbsent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, draftID, snapshotID, outboxID foundation.ID,
	idempotencyKey string,
) {
	t.Helper()
	var snapshots, outboxes, receipts int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM organizing.workflow_input_snapshot WHERE workspace_id=$1 AND id=$2),
		(SELECT count(*) FROM organizing.workflow_start_outbox WHERE workspace_id=$1 AND id=$3),
		(SELECT count(*) FROM organizing.command_receipt
		 WHERE workspace_id=$1 AND aggregate_id=$4 AND idempotency_key=$5)`,
		string(workspaceID), string(snapshotID), string(outboxID), string(draftID), idempotencyKey,
	).Scan(&snapshots, &outboxes, &receipts); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if snapshots != 0 || outboxes != 0 || receipts != 0 {
		t.Fatalf("rolled-back confirmation facts snapshot=%d outbox=%d receipt=%d", snapshots, outboxes, receipts)
	}
}

func createOrganizingPendingStart(t *testing.T, ctx context.Context, repository *Repository, workspaceID foundation.ID,
	template domain.Template, revision domain.TemplateRevision, idBase int, now time.Time,
) foundation.ID {
	t.Helper()
	key := fmt.Sprintf("attempt-%d", idBase)
	draft, err := domain.NewDraft(organizingIntegrationID(idBase), workspaceID, key, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateDraft(ctx, organizingapp.CreateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, key+"-create", key+"-create", organizingapp.CommandCreateDraft, 0),
		Draft:   draft,
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, key+"-template", key+"-template",
			organizingapp.CommandUpdateDraft, 1),
		Intent: draft.Intent, TemplateRevisionID: revision.ID, UpdatedAt: now.Add(time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	reference := domain.MaterialRef{
		Kind: domain.MaterialSourceVersion, SourceVersionID: organizingIntegrationID(idBase + 1),
		ContentHash: organizingIntegrationHash(key + "-source"), Evidence: []domain.EvidenceRef{},
	}
	if _, err := repository.ReplaceMaterials(ctx, organizingapp.ReplaceMaterialsRecord{
		Binding: organizingBinding(workspaceID, draft.ID, key+"-material", key+"-material",
			organizingapp.CommandReplaceSuggestions, 2),
		Materials: []domain.DraftMaterial{{
			ID: organizingIntegrationID(idBase + 2), Ref: reference, Title: key + " source",
			Reasons: []domain.SuggestionReasonCode{domain.ReasonUserAdded}, Origin: domain.MaterialOriginUser,
			Availability: domain.MaterialAvailable, Score: 1, Selected: true,
		}},
		UpdatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	outboxID := organizingIntegrationID(idBase + 5)
	if _, err := repository.ConfirmDraft(ctx, organizingapp.ConfirmRecord{
		Binding: organizingBinding(workspaceID, draft.ID, key+"-confirm", key+"-confirm",
			organizingapp.CommandConfirmDraft, 3),
		Fence:      &organizingIntegrationFence{},
		SnapshotID: organizingIntegrationID(idBase + 3), SnapshotMaterialIDs: []foundation.ID{organizingIntegrationID(idBase + 4)},
		OutboxID: outboxID, TemplateID: template.ID, TemplateRevisionID: revision.ID,
		TemplateHash: revision.DeclarationHash, FrozenMaterials: []domain.MaterialRef{reference},
		ConfirmedAt: now.Add(3 * time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	return outboxID
}

func advanceOrganizingStartRetries(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	outboxID foundation.ID, retryCount int,
) {
	t.Helper()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `CREATE OR REPLACE FUNCTION pg_temp.advance_organizing_start_retries(
		target_id uuid,retry_count integer
	) RETURNS void LANGUAGE plpgsql AS $$
	DECLARE iteration integer;
	BEGIN
		FOR iteration IN 1..retry_count LOOP
			UPDATE organizing.workflow_start_outbox
			SET lease_owner='attempt-limit-prep',lease_until=clock_timestamp()+interval '1 hour',
				attempt_count=attempt_count+1,version=version+1,updated_at=clock_timestamp()
			WHERE id=target_id AND status='PENDING' AND lease_owner IS NULL;
			IF NOT FOUND THEN RAISE EXCEPTION 'failed to prepare organizing start claim'; END IF;
			UPDATE organizing.workflow_start_outbox
			SET lease_owner=NULL,lease_until=NULL,last_error_code='WORKFLOW_TEMPORARY_FAILURE',
				available_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp()
			WHERE id=target_id AND status='PENDING' AND lease_owner='attempt-limit-prep';
			IF NOT FOUND THEN RAISE EXCEPTION 'failed to prepare organizing start retry'; END IF;
		END LOOP;
	END
	$$`); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := connection.Exec(ctx, `SELECT pg_temp.advance_organizing_start_retries($1,$2)`,
		string(outboxID), retryCount); err != nil {
		organizingIntegrationFatal(t, err)
	}
}

func assertOrganizingStartPoisoned(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	outboxID foundation.ID, expectedError string,
) {
	t.Helper()
	var status, lastError string
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT status,attempt_count,last_error_code
		FROM organizing.workflow_start_outbox WHERE id=$1`, string(outboxID)).Scan(&status, &attempts, &lastError); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if status != "POISONED" || attempts != maxStartAttempts || lastError != expectedError {
		t.Fatalf("exhausted outbox status=%s attempts=%d last_error=%s", status, attempts, lastError)
	}
}

func seedOrganizingWorkflow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, snapshotID,
	definitionID, runID, nodeRunID foundation.ID, now time.Time,
) {
	t.Helper()
	completedAt := now.Add(10 * time.Minute)
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'organizing.topic-article',1,'{}',$3)`, string(definitionID), string(workspaceID), now); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(
		id,workspace_id,definition_id,status,input,output,version,created_at,updated_at,completed_at,
		idempotency_key,request_hash
	) VALUES($1,$2,$3,'succeeded',jsonb_build_object('snapshot_id',$4::text),'{}',1,$5,$6,$6,$7,$8)`,
		string(runID), string(workspaceID), string(definitionID), string(snapshotID), now, completedAt,
		organizingapp.StartIdempotencyKey(snapshotID), organizingIntegrationHash("workflow-start")); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,attempt,input,output,version,created_at,updated_at,completed_at
	) VALUES($1,$2,'result','deterministic.hash','succeeded',1,'{}','{}',1,$3,$4,$4)`,
		string(nodeRunID), string(runID), now, completedAt); err != nil {
		organizingIntegrationFatal(t, err)
	}
}

func organizingBinding(workspaceID, aggregateID foundation.ID, key, requestSeed, commandType string,
	expectedVersion int64,
) organizingapp.CommandBinding {
	return organizingapp.CommandBinding{
		WorkspaceID: workspaceID, AggregateID: aggregateID, IdempotencyKey: key,
		RequestHash: organizingIntegrationHash(requestSeed), CommandType: commandType, ExpectedVersion: expectedVersion,
	}
}

func organizingIntegrationID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("74200000-0000-4000-8000-%012d", value))
}

func organizingIntegrationHash(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:])
}

func organizingIntegrationError(err error, kind foundation.ErrorKind, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind && classified.Code == code
}

type organizingIntegrationFence struct {
	calls int
	err   error
}

func (fence *organizingIntegrationFence) VerifyFrozen(
	context.Context,
	any,
	foundation.ID,
	[]domain.MaterialRef,
) error {
	fence.calls++
	return fence.err
}

type organizingDelegatingFence struct {
	delegate organizingapp.FrozenMaterialFence
	calls    int
}

type organizingBlockingFence struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

type confirmIntegrationCall struct {
	result organizingapp.ConfirmResult
	err    error
}

type organizingStatementTimeoutFence struct {
	delegate         organizingapp.FrozenMaterialFence
	statementTimeout string
	calls            int
}

func (fence *organizingStatementTimeoutFence) VerifyFrozen(
	ctx context.Context,
	transaction any,
	workspaceID foundation.ID,
	references []domain.MaterialRef,
) error {
	fence.calls++
	if err := fence.delegate.VerifyFrozen(ctx, transaction, workspaceID, references); err != nil {
		return err
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok {
		return errors.New("organizing test fence did not receive pgx transaction")
	}
	return tx.QueryRow(ctx, `SHOW statement_timeout`).Scan(&fence.statementTimeout)
}

func (fence *organizingBlockingFence) VerifyFrozen(
	ctx context.Context,
	_ any,
	_ foundation.ID,
	_ []domain.MaterialRef,
) error {
	if fence.calls.Add(1) == 1 {
		close(fence.entered)
		select {
		case <-fence.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (fence *organizingDelegatingFence) VerifyFrozen(
	ctx context.Context,
	transaction any,
	workspaceID foundation.ID,
	references []domain.MaterialRef,
) error {
	fence.calls++
	return fence.delegate.VerifyFrozen(ctx, transaction, workspaceID, references)
}

func organizingIntegrationPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("PostgreSQL error=%v, want SQLSTATE %s", err, code)
	}
}

func organizingIntegrationFatal(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		t.Fatalf("error=%v PostgreSQL code=%s message=%s detail=%s constraint=%s",
			err, postgresError.Code, postgresError.Message, postgresError.Detail, postgresError.ConstraintName)
	}
	t.Fatal(err)
}

func waitForOrganizingAdvisoryLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid()
			  AND state='active' AND wait_event_type='Lock'
			  AND position('pg_advisory_xact_lock' in query)>0
		)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("concurrent confirmation did not block on the idempotency advisory lock")
}

func seedOrganizingConfirmRecord(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	workspaceID foundation.ID,
	template domain.Template,
	revision domain.TemplateRevision,
	fence organizingapp.FrozenMaterialFence,
	seed int,
	now time.Time,
) organizingapp.ConfirmRecord {
	reference := domain.MaterialRef{Kind: domain.MaterialSourceVersion, SourceVersionID: organizingIntegrationID(seed + 1),
		ContentHash: organizingIntegrationHash(fmt.Sprintf("confirm-source-%d", seed)), Evidence: []domain.EvidenceRef{}}
	return seedOrganizingConfirmRecordWithReference(t, ctx, repository, workspaceID, template, revision, fence, reference, seed, now)
}

func seedOrganizingConfirmRecordWithReference(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	workspaceID foundation.ID,
	template domain.Template,
	revision domain.TemplateRevision,
	fence organizingapp.FrozenMaterialFence,
	reference domain.MaterialRef,
	seed int,
	now time.Time,
) organizingapp.ConfirmRecord {
	t.Helper()
	draft, err := domain.NewDraft(organizingIntegrationID(seed), workspaceID, "concurrent confirmation", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateDraft(ctx, organizingapp.CreateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, fmt.Sprintf("confirm-create-%d", seed), fmt.Sprintf("confirm-create-%d", seed), organizingapp.CommandCreateDraft, 0),
		Draft:   draft,
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := repository.UpdateDraft(ctx, organizingapp.UpdateDraftRecord{
		Binding: organizingBinding(workspaceID, draft.ID, fmt.Sprintf("confirm-template-%d", seed), fmt.Sprintf("confirm-template-%d", seed), organizingapp.CommandUpdateDraft, 1),
		Intent:  draft.Intent, TemplateRevisionID: revision.ID, UpdatedAt: now.Add(2 * time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	if _, err := repository.ReplaceMaterials(ctx, organizingapp.ReplaceMaterialsRecord{
		Binding: organizingBinding(workspaceID, draft.ID, fmt.Sprintf("confirm-material-%d", seed), fmt.Sprintf("confirm-material-%d", seed), organizingapp.CommandReplaceSuggestions, 2),
		Materials: []domain.DraftMaterial{{ID: organizingIntegrationID(seed + 2), Ref: reference, Title: "concurrent material",
			Reasons: []domain.SuggestionReasonCode{domain.ReasonUserAdded}, Origin: domain.MaterialOriginUser,
			Availability: domain.MaterialAvailable, Score: 1, Selected: true}},
		UpdatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		organizingIntegrationFatal(t, err)
	}
	return organizingapp.ConfirmRecord{
		Binding: organizingBinding(workspaceID, draft.ID, fmt.Sprintf("confirm-command-%d", seed), fmt.Sprintf("confirm-command-%d", seed), organizingapp.CommandConfirmDraft, 3),
		Fence:   fence, SnapshotID: organizingIntegrationID(seed + 3), SnapshotMaterialIDs: []foundation.ID{organizingIntegrationID(seed + 4)},
		OutboxID: organizingIntegrationID(seed + 5), TemplateID: template.ID, TemplateRevisionID: revision.ID,
		TemplateHash: revision.DeclarationHash, FrozenMaterials: []domain.MaterialRef{reference}, ConfirmedAt: now.Add(4 * time.Minute),
	}
}
