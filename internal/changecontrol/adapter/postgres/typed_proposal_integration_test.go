package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryKnowledgeChangeProposalRoundTripAndReplay(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("71000000-0000-4000-8000-000000000001")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'Typed Proposal Test',$2,$2,$3,'test',1,$3,$3)`,
		string(workspaceID), "/tmp/typed-proposal-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	change := typedProposalKnowledgeChange()
	changeHash, err := domain.ComputeKnowledgeChangeHash(change, "medium", "create a corrective relation proposal")
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputeKnowledgeChangeRequestHash(workspaceID, change, "medium", "create a corrective relation proposal")
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID:             "71000000-0000-4000-8000-000000000002",
		WorkspaceID:    workspaceID,
		Type:           domain.ProposalTypeKnowledgeChange,
		RiskLevel:      domain.ProposalRiskLevelHigh,
		IdempotencyKey: "typed-proposal-round-trip",
		RequestHash:    requestHash,
		Status:         domain.StatusReady,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision: domain.Revision{
			ID:              "71000000-0000-4000-8000-000000000003",
			ProposalID:      "71000000-0000-4000-8000-000000000002",
			RevisionNo:      1,
			Risk:            "medium",
			RollbackPlan:    "create a corrective relation proposal",
			ChangeHash:      changeHash,
			KnowledgeChange: &change,
			CreatedAt:       now,
		},
	}

	seedHistoricalKnowledgeProposalV1(t, ctx, pool, proposal)
	created, err := repository.CreateKnowledgeChangeProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	assertTypedProposalRoundTrip(t, created, proposal)

	replay := proposal
	replay.ID = "71000000-0000-4000-8000-000000000004"
	replay.Revision.ID = "71000000-0000-4000-8000-000000000005"
	replay.Revision.ProposalID = replay.ID
	replayed, err := repository.CreateKnowledgeChangeProposal(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != proposal.ID {
		t.Fatalf("replayed proposal id = %s, want %s", replayed.ID, proposal.ID)
	}
	assertTypedProposalRoundTrip(t, replayed, proposal)

	v2RequestHash, err := domain.ComputeKnowledgeChangeRequestHashWithRiskLevel(
		workspaceID,
		change,
		domain.ProposalRiskLevelHigh,
		"medium",
		"create a corrective relation proposal",
	)
	if err != nil {
		t.Fatal(err)
	}
	v2Replay := replay
	v2Replay.ID = "71000000-0000-4000-8000-000000000006"
	v2Replay.Revision.ID = "71000000-0000-4000-8000-000000000007"
	v2Replay.Revision.ProposalID = v2Replay.ID
	v2Replay.RiskLevel = domain.ProposalRiskLevelHigh
	v2Replay.RequestHash = v2RequestHash
	replayedV2, err := repository.CreateKnowledgeChangeProposal(ctx, v2Replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayedV2.ID != proposal.ID {
		t.Fatalf("v2 replayed proposal id = %s, want %s", replayedV2.ID, proposal.ID)
	}

	newV1 := proposal
	newV1.ID = "71000000-0000-4000-8000-000000000011"
	newV1.Revision.ID = "71000000-0000-4000-8000-000000000012"
	newV1.Revision.ProposalID = newV1.ID
	newV1.IdempotencyKey = "typed-proposal-v1-new"
	if _, err := repository.CreateKnowledgeChangeProposal(ctx, newV1); !hasCode(err, "PROPOSAL_INVALID") {
		t.Fatalf("new v1 knowledge proposal error = %v", err)
	}

	freshV2 := proposal
	freshV2.ID = "71000000-0000-4000-8000-000000000013"
	freshV2.Revision.ID = "71000000-0000-4000-8000-000000000014"
	freshV2.Revision.ProposalID = freshV2.ID
	freshV2.IdempotencyKey = "typed-proposal-v2-new"
	freshV2.RequestHash = v2RequestHash
	createdFreshV2, err := repository.CreateKnowledgeChangeProposal(ctx, freshV2)
	if err != nil {
		t.Fatalf("new v2 knowledge proposal error = %v", err)
	}
	if createdFreshV2.ID != freshV2.ID || createdFreshV2.RequestHash != v2RequestHash || createdFreshV2.RiskLevel != domain.ProposalRiskLevelHigh {
		t.Fatalf("new v2 knowledge proposal = %#v", createdFreshV2)
	}
	legacyReverse := freshV2
	legacyReverse.ID = "71000000-0000-4000-8000-000000000015"
	legacyReverse.Revision.ID = "71000000-0000-4000-8000-000000000016"
	legacyReverse.Revision.ProposalID = legacyReverse.ID
	legacyReverse.RequestHash = requestHash
	if _, err := repository.CreateKnowledgeChangeProposal(ctx, legacyReverse); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("v1 reverse replay against v2 error = %v", err)
	}

	conflict := replay
	conflict.ID = "71000000-0000-4000-8000-000000000008"
	conflict.Revision.ID = "71000000-0000-4000-8000-000000000009"
	conflict.Revision.ProposalID = conflict.ID
	conflict.RequestHash = strings.Repeat("f", 64)
	if _, err := repository.CreateKnowledgeChangeProposal(ctx, conflict); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("idempotency conflict error = %v", err)
	}

	approval := domain.Approval{
		ID:         "71000000-0000-4000-8000-000000000010",
		ProposalID: proposal.ID,
		RevisionID: proposal.Revision.ID,
		ChangeHash: proposal.Revision.ChangeHash,
		Decision:   domain.DecisionApproved,
		DecidedAt:  now.Add(time.Minute),
	}
	if _, err := repository.Approve(ctx, approval); err != nil {
		t.Fatal(err)
	}
	approved, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTypedProposalRoundTrip(t, approved, proposal)
	if approved.Approval == nil || approved.Approval.ID != approval.ID || approved.Approval.ApprovedGitHead != nil {
		t.Fatalf("approved typed proposal = %#v", approved)
	}
}

func TestRepositoryPublishArtifactProposalRoundTripAndReplay(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("72000000-0000-4000-8000-000000000001")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'Publish Artifact Proposal Test',$2,$2,$3,'test',1,$3,$3)`,
		string(workspaceID), "/tmp/publish-artifact-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	publication := publishArtifactPayload(workspaceID)
	persistedPublication := publication
	persistedPublication.ContentHash = strings.ToUpper(persistedPublication.ContentHash)
	persistedPublication.SourceCoverage = []domain.ArtifactSourceCoverage{publication.SourceCoverage[1], publication.SourceCoverage[0]}
	changeHash, err := domain.ComputePublishArtifactHash(publication, "formal knowledge publication", "retain the isolated artifact")
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputePublishArtifactRequestHash(workspaceID, publication, domain.ProposalRiskLevelHigh, "formal knowledge publication", "retain the isolated artifact")
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID: "72000000-0000-4000-8000-000000000002", WorkspaceID: workspaceID, Type: domain.ProposalTypePublishArtifact,
		RiskLevel: domain.ProposalRiskLevelHigh, IdempotencyKey: "publish-artifact-round-trip", RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: "72000000-0000-4000-8000-000000000003", ProposalID: "72000000-0000-4000-8000-000000000002", RevisionNo: 1,
			Risk: "formal knowledge publication", RollbackPlan: "retain the isolated artifact", ChangeHash: changeHash, PublishArtifact: &persistedPublication, CreatedAt: now,
		},
	}
	created, err := repository.CreatePublishArtifactProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != proposal.ID || created.Type != domain.ProposalTypePublishArtifact || created.Revision.PublishArtifact == nil {
		t.Fatalf("created proposal = %#v", created)
	}
	replay := proposal
	replay.ID = "72000000-0000-4000-8000-000000000004"
	replay.Revision.ID = "72000000-0000-4000-8000-000000000005"
	replay.Revision.ProposalID = replay.ID
	replayed, err := repository.CreatePublishArtifactProposal(ctx, replay)
	if err != nil || replayed.ID != proposal.ID || replayed.Revision.PublishArtifact == nil || replayed.Revision.PublishArtifact.ContentHash != strings.Repeat("a", 64) {
		t.Fatalf("replayed proposal=%#v err=%v", replayed, err)
	}
	loaded, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Type != domain.ProposalTypePublishArtifact || loaded.TargetPath != "" || loaded.Revision.KnowledgeChange != nil || loaded.Revision.PublishArtifact == nil || loaded.Revision.PublishArtifact.ArtifactVersion != publication.ArtifactVersion || len(loaded.Revision.PublishArtifact.SourceCoverage) != 2 {
		t.Fatalf("loaded proposal=%#v", loaded)
	}
	items, hasMore, err := repository.ListProposals(ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, Type: domain.ProposalTypePublishArtifact, Limit: 10})
	if err != nil || hasMore || len(items) != 1 || items[0].Type != domain.ProposalTypePublishArtifact {
		t.Fatalf("list=%#v hasMore=%v err=%v", items, hasMore, err)
	}
	approval := domain.Approval{ID: "72000000-0000-4000-8000-000000000006", ProposalID: proposal.ID, RevisionID: proposal.Revision.ID, ChangeHash: changeHash, Decision: domain.DecisionApproved, DecidedAt: now.Add(time.Minute)}
	if _, err := repository.Approve(ctx, approval); err != nil {
		t.Fatal(err)
	}
	approved, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil || approved.Approval == nil || approved.Approval.ApprovedGitHead != nil || approved.WorkflowRunID != nil || approved.Status != domain.StatusApproved {
		t.Fatalf("approved proposal=%#v err=%v", approved, err)
	}
}

func TestRepositoryDownstreamUpdateProposalRoundTripReplayAndApprovalOnly(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("77000000-0000-4000-8000-000000000001")
	eventID := foundation.ID("77000000-0000-4000-8000-000000000002")
	reportID := foundation.ID("77000000-0000-4000-8000-000000000003")
	artifactID := foundation.ID("77000000-0000-4000-8000-000000000004")
	revisionID := foundation.ID("77000000-0000-4000-8000-000000000005")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'Downstream Update Proposal Test',$2,$2,$3,'test',1,$3,$3)`,
		string(workspaceID), "/tmp/downstream-update-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ops.knowledge_event(
			id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,
			event_version,schema_version,summary,payload,correlation,occurred_at,created_at
		) VALUES($1,$2,'RELATION_CONFIRMED','RELATION',$1,'relation:confirmed:v1','relation:v1',
			1,'knowledge-event/v1','relation confirmed','{}','{}',$3,$3)`, eventID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	binding := knowledge.ArtifactImpactBinding{
		ArtifactID: artifactID, ArtifactVersion: 6, RevisionID: revisionID, RevisionNo: 2, ContentHash: strings.Repeat("b", 64),
	}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO learning.artifact(
				id,workspace_id,artifact_type,title,scope,status,version,created_at,updated_at,
				domain_schema_version,scope_definition,source_coverage,current_revision_id
			) VALUES($1,$2,'CUSTOM','Downstream Update Artifact','{}','PLANNING',6,$3,$3,
				'artifact/v1','integration fixture','[]',$4)`, artifactID, workspaceID, now, revisionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO learning.artifact_revision(
				id,artifact_id,workspace_id,revision_no,status,outline,sections,coverage,missing,
				conflicts,content_markdown,provenance,created_at,domain_schema_version,content_hash,
				created_by_type,generation_metadata
			) VALUES($1,$2,$3,2,'SNAPSHOT','[]','[]','[]','[]','[]','','{}',$4,
				'artifact-revision/v1',$5,'HUMAN',NULL)`, revisionID, artifactID, workspaceID, now, binding.ContentHash)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	object := knowledge.ImpactObject{
		Type: knowledge.ImpactObjectArtifact, ID: artifactID, WorkspaceID: workspaceID, Version: 6,
		Action: knowledge.ImpactActionRegenerateArtifact, Reason: "cited source changed", RequiresProposal: true, ArtifactBinding: &binding,
	}
	objects, err := json.Marshal([]knowledge.ImpactObject{object})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := json.Marshal(knowledge.SummarizeImpactObjects([]knowledge.ImpactObject{object}))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := knowledge.ComputeImpactFingerprintForVersion(knowledge.ImpactAnalysisVersionV2, eventID, 1, []knowledge.ImpactObject{object})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ops.impact_report(
			id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,
			source_event_version,fingerprint,version,created_at,analysis_version,supersedes_report_id
		) VALUES($1,$2,$3,'READY',$4,$5,$6,'impact-report/v2',1,$7,1,$6,'impact-analysis/v2',NULL)`,
		reportID, workspaceID, eventID, objects, summary, now, fingerprint); err != nil {
		t.Fatal(err)
	}

	update := domain.DownstreamUpdate{
		WorkspaceID: workspaceID, ReportID: reportID, AnalysisVersion: knowledge.ImpactAnalysisVersionV2,
		ReportFingerprint: fingerprint, SourceEventID: eventID, SourceEventVersion: 1,
		TargetType: knowledge.ImpactObjectArtifact, TargetID: artifactID, BaseVersion: 6,
		Action: knowledge.ImpactActionRegenerateArtifact, OwnerBinding: knowledge.EventOwnerBinding{Artifact: &binding},
		Reason: object.Reason, SchemaVersion: domain.DownstreamUpdateSchemaVersion,
	}
	const risk = "downstream dependency changed"
	const rollback = "no target write has executed"
	changeHash, err := domain.ComputeDownstreamUpdateHash(update, risk, rollback)
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := domain.ComputeDownstreamUpdateRequestHash(workspaceID, update, domain.ProposalRiskLevelHigh, risk, rollback)
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID: "77000000-0000-4000-8000-000000000006", WorkspaceID: workspaceID, Type: domain.ProposalTypeDownstreamUpdate,
		RiskLevel: domain.ProposalRiskLevelHigh, IdempotencyKey: "downstream-update-round-trip", RequestHash: requestHash,
		Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		Revision: domain.Revision{
			ID: "77000000-0000-4000-8000-000000000007", ProposalID: "77000000-0000-4000-8000-000000000006", RevisionNo: 1,
			Risk: risk, RollbackPlan: rollback, ChangeHash: changeHash, DownstreamUpdate: &update, CreatedAt: now,
		},
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.Type != domain.ProposalTypeDownstreamUpdate || created.Revision.DownstreamUpdate == nil {
		t.Fatalf("created proposal=%#v", created)
	}
	replay := proposal
	replay.ID = "77000000-0000-4000-8000-000000000008"
	replay.Revision.ID = "77000000-0000-4000-8000-000000000009"
	replay.Revision.ProposalID = replay.ID
	replayed, err := repository.CreateDownstreamUpdateProposal(ctx, replay)
	if err != nil || replayed.ID != proposal.ID {
		t.Fatalf("replayed proposal=%#v err=%v", replayed, err)
	}
	loaded, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil || loaded.Revision.DownstreamUpdate == nil || loaded.Revision.DownstreamUpdate.ReportID != reportID || loaded.Revision.DownstreamUpdate.OwnerBinding.Artifact == nil || loaded.Revision.DownstreamUpdate.OwnerBinding.Artifact.ContentHash != binding.ContentHash {
		t.Fatalf("loaded proposal=%#v err=%v", loaded, err)
	}
	items, hasMore, err := repository.ListProposals(ctx, domain.ProposalListQuery{WorkspaceID: workspaceID, Type: domain.ProposalTypeDownstreamUpdate, Limit: 10})
	if err != nil || hasMore || len(items) != 1 || items[0].Type != domain.ProposalTypeDownstreamUpdate || items[0].Target != "ARTIFACT:"+string(artifactID) {
		t.Fatalf("list=%#v hasMore=%v err=%v", items, hasMore, err)
	}
	approval := domain.Approval{
		ID: "77000000-0000-4000-8000-000000000010", ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
		ChangeHash: changeHash, Decision: domain.DecisionApproved, DecidedAt: now.Add(time.Minute),
	}
	if _, err := repository.Approve(ctx, approval); err != nil {
		t.Fatal(err)
	}
	approved, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil || approved.Status != domain.StatusApproved || approved.Approval == nil || approved.Approval.ApprovedGitHead != nil || approved.WorkflowRunID != nil {
		t.Fatalf("approved proposal=%#v err=%v", approved, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE change_control.proposal SET status='applying',version=version+1 WHERE id=$1`, proposal.ID); err == nil {
		t.Fatal("database accepted downstream_update applying transition")
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM change_control.proposal WHERE id=$1`, proposal.ID).Scan(&status); err != nil || status != string(domain.StatusApproved) {
		t.Fatalf("status=%s err=%v", status, err)
	}

	unknown := proposal
	unknown.ID = "77000000-0000-4000-8000-000000000011"
	unknown.Revision.ID = "77000000-0000-4000-8000-000000000012"
	unknown.Revision.ProposalID = unknown.ID
	unknown.IdempotencyKey = "unknown-proposal-type"
	unknown.Type = domain.ProposalType("unknown")
	if _, err := repository.CreateProposal(ctx, unknown); !hasCode(err, "PROPOSAL_INVALID") {
		t.Fatalf("unknown proposal type error=%v", err)
	}
}

func TestRepositoryBuildDownstreamReviewCardProposalGuardsBindings(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("78000000-0000-4000-8000-000000000001")
	otherWorkspaceID := foundation.ID("78000000-0000-4000-8000-000000000002")
	claimID := foundation.ID("78000000-0000-4000-8000-000000000003")
	deckID := foundation.ID("78000000-0000-4000-8000-000000000004")
	cardID := foundation.ID("78000000-0000-4000-8000-000000000005")
	eventID := foundation.ID("78000000-0000-4000-8000-000000000006")
	reportID := foundation.ID("78000000-0000-4000-8000-000000000007")
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, workspace := range []struct {
		id   foundation.ID
		name string
	}{
		{id: workspaceID, name: "Review Card Downstream Update Test"},
		{id: otherWorkspaceID, name: "Other Review Card Workspace"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO core.workspace(
				id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
			) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`,
			string(workspace.id), workspace.name, "/tmp/review-card-downstream-"+string(workspace.id), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.claim(
			id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,applicability_hash,
			status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at
		) VALUES($1,$2,'Review card owner claim','review card owner claim','{}','knowledge-applicability/v1',$3,
			'SUGGESTED',0.9,'{}',$4,1,$5,$5)`,
		string(claimID), string(workspaceID), strings.Repeat("a", 64), strings.Repeat("b", 64), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO learning.review_deck(
			id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at
		) VALUES($1,$2,'Review Card Downstream Deck','{}','ACTIVE',20,'fsrs/v1',1,$3,$3)`,
		string(deckID), string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal([]map[string]string{{
		"schema_version":    "review-evidence/v1",
		"claim_id":          string(claimID),
		"source_version_id": "78000000-0000-4000-8000-000000000020",
		"source_span_id":    "78000000-0000-4000-8000-000000000021",
		"evidence_hash":     strings.Repeat("c", 64),
	}})
	if err != nil {
		t.Fatal(err)
	}
	cardFingerprint := strings.Repeat("d", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO learning.review_card(
			id,workspace_id,deck_id,claim_id,question,answer_points,evidence,card_type,difficulty,status,
			fingerprint,model_version,version,created_at,updated_at
		) VALUES($1,$2,$3,$4,'What does the owner binding freeze?','["owner binding"]',$5::jsonb,
			'SHORT_ANSWER',0.5,'DRAFT',$6,'manual',1,$7,$7)`,
		string(cardID), string(workspaceID), string(deckID), string(claimID), string(evidence), cardFingerprint, now); err != nil {
		t.Fatal(err)
	}
	var evidenceBindingFingerprint string
	if err := pool.QueryRow(ctx, `
		SELECT learning.review_card_evidence_binding_fingerprint(evidence)
		FROM learning.review_card WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(cardID)).Scan(&evidenceBindingFingerprint); err != nil {
		t.Fatal(err)
	}
	binding := knowledge.ReviewCardImpactBinding{
		CardID: cardID, CardVersion: 1, Status: "DRAFT", Fingerprint: cardFingerprint,
		ClaimID: claimID, EvidenceBindingFingerprint: evidenceBindingFingerprint,
	}
	object := knowledge.ImpactObject{
		Type: knowledge.ImpactObjectReviewCard, ID: cardID, WorkspaceID: workspaceID, Version: 1,
		Action: knowledge.ImpactActionRevalidateReviewCard, Reason: "review evidence changed", RequiresProposal: true,
		ReviewCardBinding: &binding,
	}
	insertEvent := func(id foundation.ID, suffix string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO ops.knowledge_event(
				id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,
				event_version,schema_version,summary,payload,correlation,occurred_at,created_at
			) VALUES($1,$2,'RELATION_CONFIRMED','RELATION',$1,$3,$4,
				1,'knowledge-event/v1','review card owner changed','{}','{}',$5,$5)`,
			string(id), string(workspaceID), "review-card-owner:"+suffix, "review-card:"+suffix, now); err != nil {
			t.Fatal(err)
		}
	}
	insertV2Report := func(id, sourceEventID foundation.ID, supersedes *foundation.ID) string {
		t.Helper()
		objects, err := json.Marshal([]knowledge.ImpactObject{object})
		if err != nil {
			t.Fatal(err)
		}
		summary, err := json.Marshal(knowledge.SummarizeImpactObjects([]knowledge.ImpactObject{object}))
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := knowledge.ComputeImpactFingerprintForVersion(knowledge.ImpactAnalysisVersionV2, sourceEventID, 1, []knowledge.ImpactObject{object})
		if err != nil {
			t.Fatal(err)
		}
		var supersedesID any
		if supersedes != nil {
			supersedesID = string(*supersedes)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO ops.impact_report(
				id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,
				source_event_version,fingerprint,version,created_at,analysis_version,supersedes_report_id
			) VALUES($1,$2,$3,'READY',$4,$5,$6,'impact-report/v2',1,$7,1,$6,'impact-analysis/v2',$8)`,
			string(id), string(workspaceID), string(sourceEventID), objects, summary, now, fingerprint, supersedesID); err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}
	newProposal := func(id, revisionID foundation.ID, update domain.DownstreamUpdate, idempotencyKey string) domain.Proposal {
		t.Helper()
		const risk = "review card owner changed"
		const rollback = "no review card write has executed"
		changeHash, err := domain.ComputeDownstreamUpdateHash(update, risk, rollback)
		if err != nil {
			t.Fatal(err)
		}
		requestHash, err := domain.ComputeDownstreamUpdateRequestHash(workspaceID, update, domain.ProposalRiskLevelHigh, risk, rollback)
		if err != nil {
			t.Fatal(err)
		}
		return domain.Proposal{
			ID: id, WorkspaceID: workspaceID, Type: domain.ProposalTypeDownstreamUpdate,
			RiskLevel: domain.ProposalRiskLevelHigh, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
			Status: domain.StatusReady, Version: 1, CreatedAt: now, UpdatedAt: now,
			Revision: domain.Revision{
				ID: revisionID, ProposalID: id, RevisionNo: 1, Risk: risk, RollbackPlan: rollback,
				ChangeHash: changeHash, DownstreamUpdate: &update, CreatedAt: now,
			},
		}
	}

	insertEvent(eventID, "current")
	insertV2Report(reportID, eventID, nil)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	update, err := repository.BuildDownstreamUpdate(
		ctx, workspaceID, reportID, knowledge.ImpactObjectReviewCard, cardID, knowledge.ImpactActionRevalidateReviewCard,
	)
	if err != nil {
		t.Fatal(err)
	}
	if update.TargetType != knowledge.ImpactObjectReviewCard || update.Action != knowledge.ImpactActionRevalidateReviewCard || update.OwnerBinding.ReviewCard == nil || *update.OwnerBinding.ReviewCard != binding {
		t.Fatalf("review card factory update=%#v", update)
	}
	proposal := newProposal(
		foundation.ID("78000000-0000-4000-8000-000000000008"),
		foundation.ID("78000000-0000-4000-8000-000000000009"), update, "review-card-downstream-update",
	)
	created, err := repository.CreateDownstreamUpdateProposal(ctx, proposal)
	if err != nil || created.ID != proposal.ID || created.Revision.DownstreamUpdate == nil || created.Revision.DownstreamUpdate.OwnerBinding.ReviewCard == nil {
		t.Fatalf("created review card proposal=%#v err=%v", created, err)
	}

	if _, err := repository.BuildDownstreamUpdate(
		ctx, workspaceID, reportID, knowledge.ImpactObjectReviewCard,
		foundation.ID("78000000-0000-4000-8000-000000000010"), knowledge.ImpactActionRevalidateReviewCard,
	); !hasCode(err, "DOWNSTREAM_UPDATE_TARGET_INVALID") {
		t.Fatalf("missing review card target error=%v", err)
	}
	if _, err := repository.BuildDownstreamUpdate(
		ctx, otherWorkspaceID, reportID, knowledge.ImpactObjectReviewCard, cardID, knowledge.ImpactActionRevalidateReviewCard,
	); !hasCode(err, "KNOWLEDGE_IMPACT_NOT_FOUND") {
		t.Fatalf("cross workspace review card report error=%v", err)
	}

	secondEventID := foundation.ID("78000000-0000-4000-8000-000000000011")
	secondReportID := foundation.ID("78000000-0000-4000-8000-000000000012")
	insertEvent(secondEventID, "idempotency-conflict")
	insertV2Report(secondReportID, secondEventID, nil)
	secondUpdate, err := repository.BuildDownstreamUpdate(
		ctx, workspaceID, secondReportID, knowledge.ImpactObjectReviewCard, cardID, knowledge.ImpactActionRevalidateReviewCard,
	)
	if err != nil {
		t.Fatal(err)
	}
	idempotencyConflict := newProposal(
		foundation.ID("78000000-0000-4000-8000-000000000013"),
		foundation.ID("78000000-0000-4000-8000-000000000014"), secondUpdate, proposal.IdempotencyKey,
	)
	if _, err := repository.CreateDownstreamUpdateProposal(ctx, idempotencyConflict); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("review card idempotency binding conflict error=%v", err)
	}

	historicalEventID := foundation.ID("78000000-0000-4000-8000-000000000015")
	historicalReportID := foundation.ID("78000000-0000-4000-8000-000000000016")
	currentHistoricalReportID := foundation.ID("78000000-0000-4000-8000-000000000017")
	insertEvent(historicalEventID, "historical")
	if _, err := pool.Exec(ctx, `
		INSERT INTO ops.impact_report(
			id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,
			source_event_version,fingerprint,version,created_at,analysis_version,supersedes_report_id
		) VALUES($1,$2,$3,'READY','[]','{}',$4,'impact-report/v1',1,$5,1,$4,'impact-analysis/v1',NULL)`,
		string(historicalReportID), string(workspaceID), string(historicalEventID), now, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	insertV2Report(currentHistoricalReportID, historicalEventID, &historicalReportID)
	if _, err := repository.BuildDownstreamUpdate(
		ctx, workspaceID, historicalReportID, knowledge.ImpactObjectReviewCard, cardID, knowledge.ImpactActionRevalidateReviewCard,
	); !hasCode(err, "KNOWLEDGE_IMPACT_CONFLICT") {
		t.Fatalf("superseded historical report error=%v", err)
	}

	driftEvidence, err := json.Marshal([]map[string]string{{
		"schema_version":    "review-evidence/v1",
		"claim_id":          string(claimID),
		"source_version_id": "78000000-0000-4000-8000-000000000022",
		"source_span_id":    "78000000-0000-4000-8000-000000000023",
		"evidence_hash":     strings.Repeat("f", 64),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, drift := range []struct {
		name           string
		proposalID     foundation.ID
		revisionID     foundation.ID
		idempotencyKey string
		applyMutation  func() error
	}{
		{
			name: "version", proposalID: "78000000-0000-4000-8000-000000000018", revisionID: "78000000-0000-4000-8000-000000000019", idempotencyKey: "review-card-version-drift",
			applyMutation: func() error {
				_, err := pool.Exec(ctx, `UPDATE learning.review_card SET version=2,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(cardID), now.Add(time.Second))
				return err
			},
		},
		{
			name: "fingerprint", proposalID: "78000000-0000-4000-8000-000000000024", revisionID: "78000000-0000-4000-8000-000000000025", idempotencyKey: "review-card-fingerprint-drift",
			applyMutation: func() error {
				_, err := pool.Exec(ctx, `UPDATE learning.review_card SET fingerprint=$3,updated_at=$4 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(cardID), strings.Repeat("f", 64), now.Add(time.Second))
				return err
			},
		},
		{
			name: "evidence binding", proposalID: "78000000-0000-4000-8000-000000000026", revisionID: "78000000-0000-4000-8000-000000000027", idempotencyKey: "review-card-evidence-drift",
			applyMutation: func() error {
				_, err := pool.Exec(ctx, `UPDATE learning.review_card SET evidence=$3::jsonb,updated_at=$4 WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(cardID), string(driftEvidence), now.Add(time.Second))
				return err
			},
		},
	} {
		proposal := newProposal(drift.proposalID, drift.revisionID, update, drift.idempotencyKey)
		if err := drift.applyMutation(); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateDownstreamUpdateProposal(ctx, proposal); !hasCode(err, "KNOWLEDGE_IMPACT_CONFLICT") {
			t.Fatalf("review card %s drift error=%v", drift.name, err)
		}
		var staleProposalCount int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM change_control.proposal
			WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), proposal.IdempotencyKey).Scan(&staleProposalCount); err != nil {
			t.Fatal(err)
		}
		if staleProposalCount != 0 {
			t.Fatalf("%s drift left %d proposal rows", drift.name, staleProposalCount)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE learning.review_card
			SET version=1,fingerprint=$3,evidence=$4::jsonb,updated_at=$5
			WHERE workspace_id=$1 AND id=$2`,
			string(workspaceID), string(cardID), cardFingerprint, string(evidence), now); err != nil {
			t.Fatal(err)
		}
	}
}

func publishArtifactPayload(workspaceID foundation.ID) domain.PublishArtifact {
	publication := domain.PublishArtifact{
		WorkspaceID: workspaceID, ArtifactID: "72000000-0000-4000-8000-000000000007", RevisionID: "72000000-0000-4000-8000-000000000008",
		RevisionNo: 4, ArtifactVersion: 7, ContentHash: strings.ToUpper(strings.Repeat("a", 64)), SchemaVersion: domain.PublishArtifactSchemaVersion,
		SourceCoverage: []domain.ArtifactSourceCoverage{
			{SectionKey: "gaps", Status: domain.ArtifactCoveragePartial, Gaps: []domain.ArtifactCoverageGap{{Code: "NO_SOURCE", Description: "needs a verified source"}}},
			{SectionKey: "summary", Status: domain.ArtifactCoverageCovered},
		},
	}
	canonical, err := domain.ValidatePublishArtifact(publication)
	if err != nil {
		panic(err)
	}
	return canonical
}

func TestRepositoryFilePatchProposalRequestHashCompatibility(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("75000000-0000-4000-8000-000000000001")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'File Proposal Hash Compatibility',$2,$2,$3,'test',1,$3,$3)`,
		string(workspaceID), "/tmp/file-proposal-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	baseHash := strings.Repeat("a", 64)
	content := "updated content"
	legacyHash := domain.ComputeRequestHash(workspaceID, "notes/a.md", baseHash, content, "verified evidence", "low", "restore file")
	proposal := domain.Proposal{
		ID:             "75000000-0000-4000-8000-000000000002",
		WorkspaceID:    workspaceID,
		Type:           domain.ProposalTypeFilePatch,
		RiskLevel:      domain.ProposalRiskLevelLow,
		TargetPath:     "notes/a.md",
		IdempotencyKey: "file-proposal-v1",
		RequestHash:    legacyHash,
		Status:         domain.StatusReady,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision: domain.Revision{
			ID:              "75000000-0000-4000-8000-000000000003",
			ProposalID:      "75000000-0000-4000-8000-000000000002",
			RevisionNo:      1,
			TargetPath:      "notes/a.md",
			BaseHash:        baseHash,
			Content:         content,
			EvidenceSummary: "verified evidence",
			Risk:            "low",
			RollbackPlan:    "restore file",
			ChangeHash:      domain.ComputeChangeHash("notes/a.md", baseHash, content),
			CreatedAt:       now,
		},
	}
	seedHistoricalFileProposalV1(t, ctx, pool, proposal)
	created, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != proposal.ID || created.RequestHash != legacyHash || created.RiskLevel != domain.ProposalRiskLevelLow {
		t.Fatalf("historical v1 replay = %#v", created)
	}

	v2Hash, err := domain.ComputeRequestHashWithRiskLevel(
		workspaceID,
		"notes/a.md",
		baseHash,
		content,
		"verified evidence",
		domain.ProposalRiskLevelLow,
		"low",
		"restore file",
	)
	if err != nil {
		t.Fatal(err)
	}
	v2Replay := proposal
	v2Replay.ID = "75000000-0000-4000-8000-000000000004"
	v2Replay.Revision.ID = "75000000-0000-4000-8000-000000000005"
	v2Replay.Revision.ProposalID = v2Replay.ID
	v2Replay.RiskLevel = domain.ProposalRiskLevelLow
	v2Replay.RequestHash = v2Hash
	replayed, err := repository.CreateProposal(ctx, v2Replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != created.ID || replayed.RequestHash != legacyHash {
		t.Fatalf("v2 replayed proposal = %#v, want historical id=%s hash=%s", replayed, created.ID, legacyHash)
	}

	newV1 := proposal
	newV1.ID = "75000000-0000-4000-8000-000000000012"
	newV1.Revision.ID = "75000000-0000-4000-8000-000000000013"
	newV1.Revision.ProposalID = newV1.ID
	newV1.IdempotencyKey = "file-proposal-v1-new"
	if _, err := repository.CreateProposal(ctx, newV1); !hasCode(err, "PROPOSAL_INVALID") {
		t.Fatalf("new v1 file proposal error = %v", err)
	}

	v2Proposal := proposal
	v2Proposal.ID = "75000000-0000-4000-8000-000000000006"
	v2Proposal.Revision.ID = "75000000-0000-4000-8000-000000000007"
	v2Proposal.Revision.ProposalID = v2Proposal.ID
	v2Proposal.IdempotencyKey = "file-proposal-v2"
	v2Proposal.RiskLevel = domain.ProposalRiskLevelLow
	v2Proposal.RequestHash = v2Hash
	createdV2, err := repository.CreateProposal(ctx, v2Proposal)
	if err != nil {
		t.Fatal(err)
	}
	if createdV2.ID != v2Proposal.ID || createdV2.RequestHash != v2Hash || createdV2.RiskLevel != domain.ProposalRiskLevelLow {
		t.Fatalf("new v2 proposal = %#v", createdV2)
	}
	legacyReplay := v2Proposal
	legacyReplay.ID = "75000000-0000-4000-8000-000000000008"
	legacyReplay.Revision.ID = "75000000-0000-4000-8000-000000000009"
	legacyReplay.Revision.ProposalID = legacyReplay.ID
	legacyReplay.RequestHash = legacyHash
	if _, err := repository.CreateProposal(ctx, legacyReplay); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("v1 reverse replay against v2 error = %v", err)
	}

	highRiskHash, err := domain.ComputeRequestHashWithRiskLevel(
		workspaceID,
		"notes/a.md",
		baseHash,
		content,
		"verified evidence",
		domain.ProposalRiskLevelHigh,
		"low",
		"restore file",
	)
	if err != nil {
		t.Fatal(err)
	}
	riskConflict := legacyReplay
	riskConflict.ID = "75000000-0000-4000-8000-000000000010"
	riskConflict.Revision.ID = "75000000-0000-4000-8000-000000000011"
	riskConflict.Revision.ProposalID = riskConflict.ID
	riskConflict.RiskLevel = domain.ProposalRiskLevelHigh
	riskConflict.RequestHash = highRiskHash
	if _, err := repository.CreateProposal(ctx, riskConflict); !hasCode(err, "IDEMPOTENCY_KEY_REUSED") {
		t.Fatalf("risk-level idempotency conflict error = %v", err)
	}
}

func TestRepositoryProposalRiskLevelIsIndependentFromRevisionNarrative(t *testing.T) {
	pool, ctx := newChangeControlMigrationTestPool(t)
	workspaceID := foundation.ID("76000000-0000-4000-8000-000000000001")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `
		INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,'Proposal Risk Source',$2,$2,$3,'test',1,$3,$3)`,
		string(workspaceID), "/tmp/proposal-risk-source-"+string(workspaceID), now); err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	const narrative = "may change editorial structure"
	baseHash := strings.Repeat("a", 64)
	content := "updated editorial content"
	requestHash, err := domain.ComputeRequestHashWithRiskLevel(
		workspaceID,
		"notes/risk-source.md",
		baseHash,
		content,
		"verified evidence",
		domain.ProposalRiskLevelLow,
		narrative,
		"restore file",
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal := domain.Proposal{
		ID:             "76000000-0000-4000-8000-000000000002",
		WorkspaceID:    workspaceID,
		Type:           domain.ProposalTypeFilePatch,
		RiskLevel:      domain.ProposalRiskLevelLow,
		TargetPath:     "notes/risk-source.md",
		IdempotencyKey: "proposal-risk-source",
		RequestHash:    requestHash,
		Status:         domain.StatusReady,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
		Revision: domain.Revision{
			ID:              "76000000-0000-4000-8000-000000000003",
			ProposalID:      "76000000-0000-4000-8000-000000000002",
			RevisionNo:      1,
			TargetPath:      "notes/risk-source.md",
			BaseHash:        baseHash,
			Content:         content,
			EvidenceSummary: "verified evidence",
			Risk:            narrative,
			RollbackPlan:    "restore file",
			ChangeHash:      domain.ComputeChangeHash("notes/risk-source.md", baseHash, content),
			CreatedAt:       now,
		},
	}
	created, err := repository.CreateProposal(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	if created.RiskLevel != domain.ProposalRiskLevelLow || created.Revision.Risk != narrative {
		t.Fatalf("created proposal risk binding = %#v", created)
	}
	loaded, err := repository.GetProposal(ctx, proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RiskLevel != domain.ProposalRiskLevelLow || loaded.Revision.Risk != narrative {
		t.Fatalf("loaded proposal risk binding = %#v", loaded)
	}
	items, hasMore, err := repository.ListProposals(ctx, domain.ProposalListQuery{
		WorkspaceID: workspaceID,
		RiskLevel:   domain.ProposalRiskLevelLow,
		Limit:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(items) != 1 || items[0].ProposalID != proposal.ID || items[0].RiskLevel != domain.ProposalRiskLevelLow || items[0].Risk != narrative {
		t.Fatalf("LOW proposal list = %#v hasMore=%v", items, hasMore)
	}
	replay := proposal
	replay.ID = "76000000-0000-4000-8000-000000000004"
	replay.Revision.ID = "76000000-0000-4000-8000-000000000005"
	replay.Revision.ProposalID = replay.ID
	replayed, err := repository.CreateProposal(ctx, replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != proposal.ID || replayed.RiskLevel != domain.ProposalRiskLevelLow {
		t.Fatalf("replayed proposal risk binding = %#v", replayed)
	}
}

func seedHistoricalKnowledgeProposalV1(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proposal domain.Proposal) {
	t.Helper()
	change := proposal.Revision.KnowledgeChange
	if change == nil {
		t.Fatal("historical knowledge proposal is missing change payload")
	}
	targetRefs, err := json.Marshal(change.TargetRefs)
	if err != nil {
		t.Fatal(err)
	}
	baseVersions, err := json.Marshal(change.BaseVersions)
	if err != nil {
		t.Fatal(err)
	}
	changeSet, err := json.Marshal(change.ChangeSet)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRefs, err := json.Marshal(change.EvidenceRefs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO change_control.proposal(
			id,workspace_id,proposal_type,idempotency_key,request_hash,risk_level,status,version,created_at,updated_at
		) VALUES($1,$2,'knowledge_change',$3,$4,$5,$6,$7,$8,$9)`,
		string(proposal.ID), string(proposal.WorkspaceID), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.RiskLevel), string(proposal.Status), proposal.Version, proposal.CreatedAt, proposal.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,
			target_refs,base_versions,change_set,evidence_refs,schema_version,created_at
		) VALUES($1,$2,$3,NULL,NULL,NULL,NULL,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		string(proposal.Revision.ID), string(proposal.ID), proposal.Revision.RevisionNo, proposal.Revision.Risk,
		proposal.Revision.RollbackPlan, proposal.Revision.ChangeHash, targetRefs, baseVersions, changeSet,
		evidenceRefs, change.SchemaVersion, proposal.Revision.CreatedAt); err != nil {
		t.Fatal(err)
	}
}

func seedHistoricalFileProposalV1(t *testing.T, ctx context.Context, pool *pgxpool.Pool, proposal domain.Proposal) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO change_control.proposal(
			id,workspace_id,proposal_type,idempotency_key,request_hash,risk_level,status,version,created_at,updated_at
		) VALUES($1,$2,'file_patch',$3,$4,$5,$6,$7,$8,$9)`,
		string(proposal.ID), string(proposal.WorkspaceID), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.RiskLevel), string(proposal.Status), proposal.Version, proposal.CreatedAt, proposal.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		string(proposal.Revision.ID), string(proposal.ID), proposal.Revision.RevisionNo, proposal.Revision.TargetPath,
		proposal.Revision.BaseHash, proposal.Revision.Content, proposal.Revision.EvidenceSummary, proposal.Revision.Risk,
		proposal.Revision.RollbackPlan, proposal.Revision.ChangeHash, proposal.Revision.CreatedAt); err != nil {
		t.Fatal(err)
	}
}

func typedProposalKnowledgeChange() domain.KnowledgeChange {
	return domain.KnowledgeChange{
		TargetRefs: []domain.KnowledgeTargetRef{{
			Type:        domain.KnowledgeTargetRefRelationCandidate,
			ID:          "72000000-0000-4000-8000-000000000001",
			Fingerprint: strings.Repeat("a", 64),
		}},
		BaseVersions: []domain.KnowledgeBaseVersion{
			{NodeType: knowledge.NodeTypeClaim, NodeID: "73000000-0000-4000-8000-000000000001", Version: 3},
			{NodeType: knowledge.NodeTypeTopic, NodeID: "73000000-0000-4000-8000-000000000002", Version: 5},
		},
		ChangeSet: domain.KnowledgeChangeSet{
			Operation:    domain.KnowledgeChangeOperationCreateRelation,
			Source:       knowledge.NodeRef{Type: knowledge.NodeTypeClaim, ID: "73000000-0000-4000-8000-000000000001"},
			Target:       knowledge.NodeRef{Type: knowledge.NodeTypeTopic, ID: "73000000-0000-4000-8000-000000000002"},
			RelationType: knowledge.RelationBelongsTo,
		},
		EvidenceRefs: []domain.KnowledgeEvidenceRef{{
			CandidateEvidenceID: "74000000-0000-4000-8000-000000000001",
			SemanticHash:        strings.Repeat("b", 64),
		}},
		SchemaVersion: domain.KnowledgeChangeSchemaVersion,
	}
}

func assertTypedProposalRoundTrip(t *testing.T, got, want domain.Proposal) {
	t.Helper()
	wantRiskLevel, err := domain.ParseProposalRiskLevel(want.RiskLevel)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.WorkspaceID != want.WorkspaceID || got.Type != domain.ProposalTypeKnowledgeChange || got.RiskLevel != wantRiskLevel || got.RequestHash != want.RequestHash ||
		got.TargetPath != "" || got.Revision.TargetPath != "" || got.Revision.BaseHash != "" ||
		got.Revision.Content != "" || got.Revision.EvidenceSummary != "" || got.Revision.KnowledgeChange == nil ||
		got.Revision.ChangeHash != want.Revision.ChangeHash {
		t.Fatalf("typed proposal = %#v, want binding %#v", got, want)
	}
	if hash, hashErr := domain.ComputeKnowledgeChangeHash(*got.Revision.KnowledgeChange, got.Revision.Risk, got.Revision.RollbackPlan); hashErr != nil || hash != got.Revision.ChangeHash {
		t.Fatalf("typed proposal hash = %s, err = %v", hash, hashErr)
	}
}
