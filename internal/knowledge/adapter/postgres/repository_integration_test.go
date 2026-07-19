//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryKnowledgeLifecycleReplayAndEvidenceHistory(t *testing.T) {
	repository, tx, ctx := integrationRepository(t)
	primary := seedProvenance(t, ctx, tx, "primary")
	other := seedProvenance(t, ctx, tx, "other")
	alternate := seedProvenanceForWorkspace(t, ctx, tx, primary.workspaceID, "primary-alternate", true)
	incomplete := seedProvenanceForWorkspace(t, ctx, tx, primary.workspaceID, "primary-incomplete", false)
	applicability := mustApplicability(t, `{"region":"cn"}`)
	now := time.Now().UTC().Add(-time.Minute)

	topic := newTopic(t, primary.workspaceID, "Go 并发", now)
	topicRequestHash := testHash("topic-create")
	createdTopic, err := repository.CreateTopic(ctx, domain.CreateTopicRecord{Topic: topic, IdempotencyKey: "topic-create", RequestHash: topicRequestHash})
	if err != nil || createdTopic.Replayed || createdTopic.Topic.ID != topic.ID || len(createdTopic.Topic.Aliases) != 1 {
		t.Fatalf("CreateTopic()=%#v err=%v", createdTopic, err)
	}
	lookup, err := repository.LookupCommandReceipt(ctx, domain.CommandReceiptQuery{
		WorkspaceID: primary.workspaceID, IdempotencyKey: "topic-create", RequestHash: topicRequestHash,
		CommandType: domain.CommandCreateTopic, AggregateType: domain.AggregateTopic,
	})
	if err != nil || !lookup.Found || lookup.Receipt.AggregateID != topic.ID || lookup.Receipt.AggregateVersion != 1 {
		t.Fatalf("LookupCommandReceipt()=%#v err=%v", lookup, err)
	}
	if _, err := repository.LookupCommandReceipt(ctx, domain.CommandReceiptQuery{
		WorkspaceID: primary.workspaceID, IdempotencyKey: "topic-create", RequestHash: testHash("wrong-binding"),
		CommandType: domain.CommandCreateTopic, AggregateType: domain.AggregateTopic,
	}); !hasCode(err, domain.ErrorCodeIdempotencyConflict) {
		t.Fatalf("LookupCommandReceipt binding conflict err=%v", err)
	}
	missingLookup, err := repository.LookupCommandReceipt(ctx, domain.CommandReceiptQuery{
		WorkspaceID: primary.workspaceID, IdempotencyKey: "topic-missing", RequestHash: testHash("topic-missing"),
		CommandType: domain.CommandCreateTopic, AggregateType: domain.AggregateTopic,
	})
	if err != nil || missingLookup.Found {
		t.Fatalf("LookupCommandReceipt missing=%#v err=%v", missingLookup, err)
	}
	replayTopic := topic
	replayTopic.ID = newID(t)
	replayedTopic, err := repository.CreateTopic(ctx, domain.CreateTopicRecord{Topic: replayTopic, IdempotencyKey: "topic-create", RequestHash: topicRequestHash})
	if err != nil || !replayedTopic.Replayed || replayedTopic.Topic.ID != topic.ID {
		t.Fatalf("CreateTopic replay=%#v err=%v", replayedTopic, err)
	}

	first := suggestClaim(t, ctx, repository, primary.workspaceID, "channel 发送发生在接收之前", applicability, "claim-first", now)
	second := suggestClaim(t, ctx, repository, primary.workspaceID, "互斥锁保护共享状态", applicability, "claim-second", now.Add(time.Second))
	first = confirmClaim(t, ctx, repository, first, primary, "confirm-first", now.Add(2*time.Second))
	second = confirmClaim(t, ctx, repository, second, primary, "confirm-second", now.Add(3*time.Second))

	duplicateCandidate := first.Claim
	duplicateCandidate.ID = newID(t)
	duplicateCandidate.Status = domain.ClaimStatusSuggested
	duplicateCandidate.Version = 1
	duplicateCandidate.CreatedAt = now.Add(4 * time.Second)
	duplicateCandidate.UpdatedAt = duplicateCandidate.CreatedAt
	longReplay, err := repository.SuggestClaim(ctx, domain.SuggestClaimRecord{
		Claim: duplicateCandidate, IdempotencyKey: "claim-first-late-replay", RequestHash: testHash("claim-first-payload"),
	})
	if err != nil || !longReplay.Replayed || longReplay.Claim.ID != first.Claim.ID || longReplay.Claim.Status != domain.ClaimStatusConfirmed || len(longReplay.Sources) != 1 {
		t.Fatalf("late SuggestClaim replay=%#v err=%v", longReplay, err)
	}

	initialEvidence := relationEvidence(t, primary.workspaceID, "原始材料显示两项知识互补", applicability, primary, now.Add(5*time.Second), nil)
	relation := newRelation(t, primary.workspaceID, domain.RelationComplements,
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: first.Claim.ID},
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: second.Claim.ID}, now.Add(5*time.Second))
	initialEvidence.RelationID = relation.ID
	suggestedRelation, err := repository.SuggestRelation(ctx, domain.SuggestRelationRecord{
		Relation: relation, Evidence: []domain.RelationEvidence{initialEvidence},
		IdempotencyKey: "relation-suggest", RequestHash: testHash("relation-suggest"),
	})
	if err != nil || suggestedRelation.Relation.Status != domain.RelationStatusSuggested || len(suggestedRelation.Evidence) != 1 || suggestedRelation.Evidence[0].EvidenceHash == "" {
		t.Fatalf("SuggestRelation()=%#v err=%v", suggestedRelation, err)
	}

	approvalOne := domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: "approval:knowledge:1"}
	confirmedEvidence := relationEvidence(t, primary.workspaceID, "人工确认第一版关系", applicability, primary, now.Add(6*time.Second), nil)
	confirmedEvidence.RelationID = relation.ID
	confirmed, err := repository.ConfirmRelation(ctx, domain.ConfirmRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 1,
		Evidence: confirmedEvidence, Confirmation: approvalOne,
		IdempotencyKey: "relation-confirm-1", RequestHash: testHash("relation-confirm-1"), At: now.Add(6 * time.Second),
	})
	if err != nil || confirmed.Relation.Status != domain.RelationStatusConfirmed || confirmed.Relation.Version != 2 || len(confirmed.Evidence) != 2 {
		t.Fatalf("ConfirmRelation()=%#v err=%v", confirmed, err)
	}
	replayedConfirm, err := repository.ConfirmRelation(ctx, domain.ConfirmRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 1,
		Evidence: confirmedEvidence, Confirmation: approvalOne,
		IdempotencyKey: "relation-confirm-1", RequestHash: testHash("relation-confirm-1"), At: now.Add(6 * time.Second),
	})
	if err != nil || !replayedConfirm.Replayed || replayedConfirm.Relation.Version != 2 {
		t.Fatalf("ConfirmRelation response-loss replay=%#v err=%v", replayedConfirm, err)
	}
	stale, err := repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 2, Status: domain.RelationStatusStale,
		IdempotencyKey: "relation-stale-1", RequestHash: testHash("relation-stale-1"), At: now.Add(7 * time.Second),
	})
	if err != nil || stale.Relation.Status != domain.RelationStatusStale {
		t.Fatalf("TransitionRelation(stale)=%#v err=%v", stale, err)
	}
	approvalTwo := domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: "approval:knowledge:2"}
	reconfirmedEvidence := relationEvidence(t, primary.workspaceID, "新审批重新确认过期关系", applicability, primary, now.Add(8*time.Second), nil)
	reconfirmedEvidence.RelationID = relation.ID
	reconfirmed, err := repository.ConfirmRelation(ctx, domain.ConfirmRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 3,
		Evidence: reconfirmedEvidence, Confirmation: approvalTwo,
		IdempotencyKey: "relation-confirm-2", RequestHash: testHash("relation-confirm-2"), At: now.Add(8 * time.Second),
	})
	if err != nil || reconfirmed.Relation.Confirmation == nil || *reconfirmed.Relation.Confirmation != approvalTwo || len(reconfirmed.Evidence) != 3 {
		t.Fatalf("ConfirmRelation(stale)=%#v err=%v", reconfirmed, err)
	}
	staleAgain, err := repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 4, Status: domain.RelationStatusStale,
		IdempotencyKey: "relation-stale-2", RequestHash: testHash("relation-stale-2"), At: now.Add(9 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: staleAgain.Relation.Version, Status: domain.RelationStatusRejected,
		IdempotencyKey: "relation-reject", RequestHash: testHash("relation-reject"), At: now.Add(10 * time.Second),
	})
	if err != nil || rejected.Relation.Confirmation == nil || *rejected.Relation.Confirmation != approvalTwo || len(rejected.Evidence) != 3 {
		t.Fatalf("TransitionRelation(rejected)=%#v err=%v", rejected, err)
	}
	confirmationRefs := make([]string, 0, 2)
	var unconfirmed int
	for _, evidence := range rejected.Evidence {
		if evidence.Confirmation == nil {
			unconfirmed++
		} else {
			confirmationRefs = append(confirmationRefs, evidence.Confirmation.Reference)
		}
	}
	sort.Strings(confirmationRefs)
	if unconfirmed != 1 || len(confirmationRefs) != 2 || confirmationRefs[0] != approvalOne.Reference || confirmationRefs[1] != approvalTwo.Reference {
		t.Fatalf("evidence confirmation history unconfirmed=%d refs=%v", unconfirmed, confirmationRefs)
	}
	resuggestEvidence := relationEvidence(t, primary.workspaceID, "拒绝后新增证据再次建议", applicability, primary, now.Add(11*time.Second), nil)
	resuggestEvidence.RelationID = relation.ID
	resuggested, err := repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 6, Status: domain.RelationStatusSuggested,
		Evidence: &resuggestEvidence, IdempotencyKey: "relation-resuggest-1", RequestHash: testHash("relation-resuggest-1"), At: now.Add(11 * time.Second),
	})
	if err != nil || resuggested.Relation.Status != domain.RelationStatusSuggested || resuggested.Relation.Confirmation != nil || resuggested.Relation.Version != 7 || len(resuggested.Evidence) != 4 {
		t.Fatalf("TransitionRelation(resuggest)=%#v err=%v", resuggested, err)
	}
	replayedResuggest, err := repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 6, Status: domain.RelationStatusSuggested,
		Evidence: &resuggestEvidence, IdempotencyKey: "relation-resuggest-1", RequestHash: testHash("relation-resuggest-1"), At: now.Add(11 * time.Second),
	})
	if err != nil || !replayedResuggest.Replayed || replayedResuggest.Relation.Version != 7 || len(replayedResuggest.Evidence) != 4 {
		t.Fatalf("TransitionRelation resuggest replay=%#v err=%v", replayedResuggest, err)
	}
	rejectedAgain, err := repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 7, Status: domain.RelationStatusRejected,
		IdempotencyKey: "relation-reject-again", RequestHash: testHash("relation-reject-again"), At: now.Add(12 * time.Second),
	})
	if err != nil || rejectedAgain.Relation.Version != 8 {
		t.Fatalf("TransitionRelation(reject again)=%#v err=%v", rejectedAgain, err)
	}
	_, err = repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 8, Status: domain.RelationStatusSuggested,
		Evidence: &resuggestEvidence, IdempotencyKey: "relation-resuggest-duplicate", RequestHash: testHash("relation-resuggest-duplicate"), At: now.Add(13 * time.Second),
	})
	if !classifiedAs(err, foundation.ErrorVersionConflict) {
		t.Fatalf("duplicate resuggest evidence err=%#v", err)
	}
	brokenResuggestEvidence := relationEvidence(t, primary.workspaceID, "错绑证据不能留下半状态", applicability, provenanceFixture{
		workspaceID: primary.workspaceID, sourceVersionID: primary.sourceVersionID, sourceSpanID: alternate.sourceSpanID,
	}, now.Add(14*time.Second), nil)
	brokenResuggestEvidence.RelationID = relation.ID
	_, err = repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 8, Status: domain.RelationStatusSuggested,
		Evidence: &brokenResuggestEvidence, IdempotencyKey: "relation-resuggest-broken", RequestHash: testHash("relation-resuggest-broken"), At: now.Add(14 * time.Second),
	})
	if !classifiedAs(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("broken resuggest evidence err=%#v", err)
	}
	afterFailedResuggest, err := repository.BatchGetRelations(ctx, domain.BatchGetRelationsQuery{WorkspaceID: primary.workspaceID, IDs: []foundation.ID{relation.ID}, Limit: 1})
	if err != nil || len(afterFailedResuggest) != 1 || afterFailedResuggest[0].Relation.Status != domain.RelationStatusRejected || afterFailedResuggest[0].Relation.Version != 8 || len(afterFailedResuggest[0].Evidence) != 4 {
		t.Fatalf("failed resuggest left partial state=%#v err=%v", afterFailedResuggest, err)
	}
	var failedResuggestReceipt int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`, string(primary.workspaceID), "relation-resuggest-broken").Scan(&failedResuggestReceipt); err != nil || failedResuggestReceipt != 0 {
		t.Fatalf("failed resuggest receipt=%d err=%v", failedResuggestReceipt, err)
	}
	finalResuggestEvidence := relationEvidence(t, primary.workspaceID, "修复引用后重新建议", applicability, primary, now.Add(15*time.Second), nil)
	finalResuggestEvidence.RelationID = relation.ID
	finalResuggest, err := repository.TransitionRelation(ctx, domain.TransitionRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 8, Status: domain.RelationStatusSuggested,
		Evidence: &finalResuggestEvidence, IdempotencyKey: "relation-resuggest-final", RequestHash: testHash("relation-resuggest-final"), At: now.Add(15 * time.Second),
	})
	if err != nil || finalResuggest.Relation.Version != 9 || len(finalResuggest.Evidence) != 5 {
		t.Fatalf("final resuggest=%#v err=%v", finalResuggest, err)
	}

	batchedRelations, err := repository.BatchGetRelations(ctx, domain.BatchGetRelationsQuery{
		WorkspaceID: primary.workspaceID, IDs: []foundation.ID{relation.ID}, Limit: 10,
	})
	if err != nil || len(batchedRelations) != 1 || len(batchedRelations[0].Evidence) != 5 {
		t.Fatalf("BatchGetRelations()=%#v err=%v", batchedRelations, err)
	}
	batchedClaims, err := repository.BatchGetClaims(ctx, domain.BatchGetClaimsQuery{
		WorkspaceID: primary.workspaceID, IDs: []foundation.ID{first.Claim.ID, second.Claim.ID}, Limit: 10,
	})
	if err != nil || len(batchedClaims) != 2 || len(batchedClaims[0].Sources) != 1 || len(batchedClaims[1].Sources) != 1 {
		t.Fatalf("BatchGetClaims()=%#v err=%v", batchedClaims, err)
	}

	cross := suggestClaim(t, ctx, repository, primary.workspaceID, "跨 Workspace 来源必须失败", applicability, "claim-cross", now.Add(11*time.Second))
	crossSource := newClaimSource(t, primary.workspaceID, cross.Claim.ID, other, now.Add(12*time.Second))
	_, err = repository.ConfirmClaim(ctx, domain.ConfirmClaimRecord{
		WorkspaceID: primary.workspaceID, ClaimID: cross.Claim.ID, ExpectedVersion: 1, Source: crossSource,
		IdempotencyKey: "claim-cross-confirm", RequestHash: testHash("claim-cross-confirm"), At: now.Add(12 * time.Second),
	})
	if !classifiedAs(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("cross-workspace provenance error=%#v", err)
	}
	unchanged, err := repository.BatchGetClaims(ctx, domain.BatchGetClaimsQuery{WorkspaceID: primary.workspaceID, IDs: []foundation.ID{cross.Claim.ID}, Limit: 1})
	if err != nil || len(unchanged) != 1 || unchanged[0].Claim.Status != domain.ClaimStatusSuggested || len(unchanged[0].Sources) != 0 {
		t.Fatalf("failed confirm left partial state=%#v err=%v", unchanged, err)
	}
	mismatched := suggestClaim(t, ctx, repository, primary.workspaceID, "同 Workspace 错 Projection 必须失败", applicability, "claim-mismatched", now.Add(13*time.Second))
	mismatchedSource := newClaimSource(t, primary.workspaceID, mismatched.Claim.ID, provenanceFixture{
		workspaceID: primary.workspaceID, sourceVersionID: primary.sourceVersionID, sourceSpanID: alternate.sourceSpanID,
	}, now.Add(14*time.Second))
	_, err = repository.ConfirmClaim(ctx, domain.ConfirmClaimRecord{
		WorkspaceID: primary.workspaceID, ClaimID: mismatched.Claim.ID, ExpectedVersion: 1, Source: mismatchedSource,
		IdempotencyKey: "claim-mismatched-confirm", RequestHash: testHash("claim-mismatched-confirm"), At: now.Add(14 * time.Second),
	})
	if !classifiedAs(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("same-workspace mismatched projection err=%#v", err)
	}
	damaged := suggestClaim(t, ctx, repository, primary.workspaceID, "缺少 Source Projection 必须失败", applicability, "claim-damaged", now.Add(15*time.Second))
	damagedSource := newClaimSource(t, primary.workspaceID, damaged.Claim.ID, incomplete, now.Add(16*time.Second))
	_, err = repository.ConfirmClaim(ctx, domain.ConfirmClaimRecord{
		WorkspaceID: primary.workspaceID, ClaimID: damaged.Claim.ID, ExpectedVersion: 1, Source: damagedSource,
		IdempotencyKey: "claim-damaged-confirm", RequestHash: testHash("claim-damaged-confirm"), At: now.Add(16 * time.Second),
	})
	if !classifiedAs(err, foundation.ErrorConsistencyViolation) {
		t.Fatalf("missing source-version projection err=%#v", err)
	}
	for _, claimID := range []foundation.ID{mismatched.Claim.ID, damaged.Claim.ID} {
		facts, queryErr := repository.BatchGetClaims(ctx, domain.BatchGetClaimsQuery{WorkspaceID: primary.workspaceID, IDs: []foundation.ID{claimID}, Limit: 1})
		if queryErr != nil || len(facts) != 1 || facts[0].Claim.Status != domain.ClaimStatusSuggested || len(facts[0].Sources) != 0 {
			t.Fatalf("failed provenance left partial claim=%s facts=%#v err=%v", claimID, facts, queryErr)
		}
	}
	if _, err := repository.SuggestClaim(ctx, domain.SuggestClaimRecord{Claim: duplicateCandidate, IdempotencyKey: "claim-first-late-replay", RequestHash: testHash("different")}); !hasCode(err, domain.ErrorCodeIdempotencyConflict) {
		t.Fatalf("idempotency conflict err=%v", err)
	}
}

func TestRepositoryBatchWritesMultipleTopicAliasesAndSuggestedRelationEvidence(t *testing.T) {
	repository, tx, ctx := integrationRepository(t)
	primary := seedProvenance(t, ctx, tx, "batch-primary")
	alternate := seedProvenanceForWorkspace(t, ctx, tx, primary.workspaceID, "batch-alternate", true)
	applicability := mustApplicability(t, `{"region":"cn"}`)
	now := time.Now().UTC().Add(-time.Minute)

	topic := newTopic(t, primary.workspaceID, "批量知识", now)
	secondAlias, secondNormalizedAlias, err := domain.NormalizeTopicText("批量知识 Second Alias")
	if err != nil {
		t.Fatal(err)
	}
	topic.Aliases = append(topic.Aliases, domain.TopicAlias{Name: secondAlias, NormalizedName: secondNormalizedAlias})
	createdTopic, err := repository.CreateTopic(ctx, domain.CreateTopicRecord{
		Topic: topic, IdempotencyKey: "topic-batch-aliases", RequestHash: testHash("topic-batch-aliases"),
	})
	if err != nil || createdTopic.Replayed || createdTopic.Topic.Version != 1 || len(createdTopic.Topic.Aliases) != 2 {
		t.Fatalf("CreateTopic aliases=%#v err=%v", createdTopic.Topic.Aliases, err)
	}
	aliasNames := map[string]bool{}
	for _, alias := range createdTopic.Topic.Aliases {
		aliasNames[alias.NormalizedName] = true
	}
	if !aliasNames[topic.Aliases[0].NormalizedName] || !aliasNames[secondNormalizedAlias] {
		t.Fatalf("persisted aliases=%#v", createdTopic.Topic.Aliases)
	}
	replayedTopic, err := repository.CreateTopic(ctx, domain.CreateTopicRecord{
		Topic: topic, IdempotencyKey: "topic-batch-aliases", RequestHash: testHash("topic-batch-aliases"),
	})
	if err != nil || !replayedTopic.Replayed || len(replayedTopic.Topic.Aliases) != 2 {
		t.Fatalf("CreateTopic replay=%#v err=%v", replayedTopic, err)
	}
	var aliasCount, distinctAliasCount, topicReceiptCount int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(DISTINCT normalized_alias) FROM core.topic_alias WHERE workspace_id=$1 AND topic_id=$2`,
		string(primary.workspaceID), string(topic.ID)).Scan(&aliasCount, &distinctAliasCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(primary.workspaceID), "topic-batch-aliases").Scan(&topicReceiptCount); err != nil {
		t.Fatal(err)
	}
	if aliasCount != 2 || distinctAliasCount != 2 || topicReceiptCount != 1 {
		t.Fatalf("topic batch rows aliases=%d distinct=%d receipts=%d", aliasCount, distinctAliasCount, topicReceiptCount)
	}

	first := suggestClaim(t, ctx, repository, primary.workspaceID, "批量证据主张一", applicability, "batch-claim-first", now.Add(time.Second))
	second := suggestClaim(t, ctx, repository, primary.workspaceID, "批量证据主张二", applicability, "batch-claim-second", now.Add(2*time.Second))
	relation := newRelation(t, primary.workspaceID, domain.RelationComplements,
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: first.Claim.ID},
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: second.Claim.ID}, now.Add(3*time.Second))
	firstEvidence := relationEvidence(t, primary.workspaceID, "第一条批量关系证据", applicability, primary, now.Add(3*time.Second), nil)
	firstEvidence.RelationID = relation.ID
	secondEvidence := relationEvidence(t, primary.workspaceID, "第二条批量关系证据", applicability, alternate, now.Add(4*time.Second), nil)
	secondEvidence.RelationID = relation.ID
	firstEvidence.EvidenceHash = domain.ComputeRelationEvidenceHash(firstEvidence)
	secondEvidence.EvidenceHash = domain.ComputeRelationEvidenceHash(secondEvidence)
	expectedEvidence := []domain.RelationEvidence{firstEvidence, secondEvidence}
	relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint(expectedEvidence)
	suggested, err := repository.SuggestRelation(ctx, domain.SuggestRelationRecord{
		Relation: relation, Evidence: []domain.RelationEvidence{secondEvidence, firstEvidence},
		IdempotencyKey: "relation-batch-evidence", RequestHash: testHash("relation-batch-evidence"),
	})
	if err != nil || suggested.Replayed || suggested.Relation.Version != 1 || suggested.Relation.EvidenceFingerprint != relation.EvidenceFingerprint || len(suggested.Evidence) != 2 {
		t.Fatalf("SuggestRelation evidence=%#v err=%v", suggested.Evidence, err)
	}
	persistedByHash := map[string]domain.RelationEvidence{}
	for _, evidence := range suggested.Evidence {
		persistedByHash[evidence.EvidenceHash] = evidence
	}
	for _, expected := range expectedEvidence {
		persisted, found := persistedByHash[expected.EvidenceHash]
		if !found || persisted.ID != expected.ID || persisted.RelationID != relation.ID ||
			persisted.WorkspaceID != primary.workspaceID || persisted.Provenance != expected.Provenance ||
			persisted.Reason != expected.Reason || persisted.Applicability.Hash != applicability.Hash || persisted.Confirmation != nil {
			t.Fatalf("persisted evidence mismatch expected=%#v actual=%#v found=%v", expected, persisted, found)
		}
	}
	alternateRelation := relation
	alternateRelation.ID = newID(t)
	alternateRelation.CreatedAt = now.Add(5 * time.Second)
	alternateRelation.UpdatedAt = alternateRelation.CreatedAt
	alternateEvidence := []domain.RelationEvidence{firstEvidence, secondEvidence}
	for index := range alternateEvidence {
		alternateEvidence[index].ID = newID(t)
		alternateEvidence[index].RelationID = alternateRelation.ID
		alternateEvidence[index].CreatedAt = now.Add(time.Duration(6+index) * time.Second)
		alternateEvidence[index].EvidenceHash = domain.ComputeRelationEvidenceHash(alternateEvidence[index])
	}
	alternateRelation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint(alternateEvidence)
	fingerprintReplay, err := repository.SuggestRelation(ctx, domain.SuggestRelationRecord{
		Relation: alternateRelation, Evidence: alternateEvidence,
		IdempotencyKey: "relation-batch-evidence-new-key", RequestHash: testHash("relation-batch-evidence"),
	})
	if err != nil || !fingerprintReplay.Replayed || fingerprintReplay.Relation.ID != relation.ID || len(fingerprintReplay.Evidence) != 2 {
		t.Fatalf("SuggestRelation fingerprint replay=%#v err=%v", fingerprintReplay, err)
	}
	replayedRelation, err := repository.SuggestRelation(ctx, domain.SuggestRelationRecord{
		Relation: relation, Evidence: []domain.RelationEvidence{firstEvidence, secondEvidence},
		IdempotencyKey: "relation-batch-evidence", RequestHash: testHash("relation-batch-evidence"),
	})
	if err != nil || !replayedRelation.Replayed || len(replayedRelation.Evidence) != 2 {
		t.Fatalf("SuggestRelation replay=%#v err=%v", replayedRelation, err)
	}
	var relationCount, evidenceCount, distinctEvidenceCount, relationReceiptCount, relationReceiptTotal int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1 AND fingerprint=$2`,
		string(primary.workspaceID), relation.Fingerprint).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*),count(DISTINCT evidence_hash) FROM core.relation_evidence WHERE workspace_id=$1 AND relation_id=$2`,
		string(primary.workspaceID), string(relation.ID)).Scan(&evidenceCount, &distinctEvidenceCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`,
		string(primary.workspaceID), "relation-batch-evidence").Scan(&relationReceiptCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key IN ($2,$3)`,
		string(primary.workspaceID), "relation-batch-evidence", "relation-batch-evidence-new-key").Scan(&relationReceiptTotal); err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 || evidenceCount != 2 || distinctEvidenceCount != 2 || relationReceiptCount != 1 || relationReceiptTotal != 2 {
		t.Fatalf("relation batch rows relations=%d evidence=%d distinct=%d receipt=%d total_receipts=%d", relationCount, evidenceCount, distinctEvidenceCount, relationReceiptCount, relationReceiptTotal)
	}
}

func TestRepositoryCommitResponseLossReplaysWithoutDuplicateSource(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedProvenance(t, ctx, seedTx, "response-loss")
	normalRepository, err := NewRepository(seedTx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	claim := suggestClaim(t, ctx, normalRepository, fixture.workspaceID, "提交成功但响应丢失", mustApplicability(t, `{}`), "response-loss-claim", now)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	source := newClaimSource(t, fixture.workspaceID, claim.Claim.ID, fixture, now.Add(time.Second))
	command := domain.ConfirmClaimRecord{
		WorkspaceID: fixture.workspaceID, ClaimID: claim.Claim.ID, ExpectedVersion: 1, Source: source,
		IdempotencyKey: "response-loss-confirm", RequestHash: testHash("response-loss-confirm"), At: now.Add(time.Second),
	}
	lossRepository, err := NewRepository(commitResponseLossDB{pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lossRepository.ConfirmClaim(ctx, command); err == nil {
		t.Fatal("injected commit response loss returned success")
	}
	normalRepository, err = NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := normalRepository.ConfirmClaim(ctx, command)
	if err != nil || !replayed.Replayed || replayed.Claim.Status != domain.ClaimStatusConfirmed || replayed.Claim.Version != 2 || len(replayed.Sources) != 1 {
		t.Fatalf("response-loss replay=%#v err=%v", replayed, err)
	}
	var sourceCount, receiptCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.claim_source WHERE workspace_id=$1 AND claim_id=$2`, string(fixture.workspaceID), string(claim.Claim.ID)).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`, string(fixture.workspaceID), command.IdempotencyKey).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || receiptCount != 1 {
		t.Fatalf("response-loss duplicate source=%d receipt=%d", sourceCount, receiptCount)
	}
}

func TestRepositoryOpenConflictIsAtomicAndReplayable(t *testing.T) {
	repository, tx, ctx := integrationPoolRepository(t)
	fixture := seedProvenance(t, ctx, tx, "conflict")
	applicability := mustApplicability(t, `{"release":"v1"}`)
	now := time.Now().UTC().Add(-time.Minute)
	first := confirmClaim(t, ctx, repository, suggestClaim(t, ctx, repository, fixture.workspaceID, "配置必须开启", applicability, "conflict-a", now), fixture, "confirm-a", now.Add(time.Second))
	second := confirmClaim(t, ctx, repository, suggestClaim(t, ctx, repository, fixture.workspaceID, "配置必须关闭", applicability, "conflict-b", now.Add(2*time.Second)), fixture, "confirm-b", now.Add(3*time.Second))
	conflict, members := newConflict(t, fixture.workspaceID, first.Claim, second.Claim, now.Add(4*time.Second))
	opened, err := repository.OpenConflict(ctx, domain.OpenConflictRecord{
		Conflict: conflict, Members: members, IdempotencyKey: "conflict-open", RequestHash: testHash("conflict-open"),
	})
	if err != nil || opened.Conflict.Status != domain.ConflictStatusOpen || len(opened.Members) != 2 {
		t.Fatalf("OpenConflict()=%#v err=%v", opened, err)
	}
	claims, err := repository.BatchGetClaims(ctx, domain.BatchGetClaimsQuery{WorkspaceID: fixture.workspaceID, IDs: []foundation.ID{first.Claim.ID, second.Claim.ID}, Limit: 2})
	if err != nil || len(claims) != 2 || claims[0].Claim.Status != domain.ClaimStatusDisputed || claims[1].Claim.Status != domain.ClaimStatusDisputed {
		t.Fatalf("conflict disputed claims=%#v err=%v", claims, err)
	}
	replayed, err := repository.OpenConflict(ctx, domain.OpenConflictRecord{
		Conflict: conflict, Members: members, IdempotencyKey: "conflict-open", RequestHash: testHash("conflict-open"),
	})
	if err != nil || !replayed.Replayed || replayed.Conflict.ID != conflict.ID {
		t.Fatalf("OpenConflict replay=%#v err=%v", replayed, err)
	}
	transitioned, err := repository.TransitionConflict(ctx, domain.TransitionConflictRecord{
		WorkspaceID: fixture.workspaceID, ConflictID: conflict.ID, ExpectedVersion: 1, Status: domain.ConflictStatusInvestigating,
		IdempotencyKey: "conflict-investigate", RequestHash: testHash("conflict-investigate"), At: now.Add(5 * time.Second),
	})
	if err != nil || transitioned.Conflict.Status != domain.ConflictStatusInvestigating {
		t.Fatalf("TransitionConflict()=%#v err=%v", transitioned, err)
	}
	for index, status := range []domain.ClaimStatus{
		domain.ClaimStatusConfirmed, domain.ClaimStatusSuperseded, domain.ClaimStatusDeprecated, domain.ClaimStatusInvalid,
	} {
		key := "blocked-disputed-transition-" + string(rune('a'+index))
		_, transitionErr := repository.TransitionClaim(ctx, domain.TransitionClaimRecord{
			WorkspaceID: fixture.workspaceID, ClaimID: first.Claim.ID, ExpectedVersion: 3, Status: status,
			IdempotencyKey: key, RequestHash: testHash(key), At: now.Add(time.Duration(6+index) * time.Second),
		})
		if !classifiedAs(transitionErr, foundation.ErrorConsistencyViolation) {
			t.Fatalf("unresolved conflict transition to %s err=%#v", status, transitionErr)
		}
		facts, queryErr := repository.BatchGetClaims(ctx, domain.BatchGetClaimsQuery{WorkspaceID: fixture.workspaceID, IDs: []foundation.ID{first.Claim.ID}, Limit: 1})
		if queryErr != nil || len(facts) != 1 || facts[0].Claim.Status != domain.ClaimStatusDisputed || facts[0].Claim.Version != 3 {
			t.Fatalf("blocked transition left claim state=%#v err=%v", facts, queryErr)
		}
		var receiptCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.knowledge_command_receipt WHERE workspace_id=$1 AND idempotency_key=$2`, string(fixture.workspaceID), key).Scan(&receiptCount); err != nil || receiptCount != 0 {
			t.Fatalf("blocked transition left receipt=%d err=%v", receiptCount, err)
		}
	}
	lateReplay := conflict
	lateReplay.ID = newID(t)
	late, err := repository.OpenConflict(ctx, domain.OpenConflictRecord{
		Conflict: lateReplay, Members: remapConflictMembers(members, lateReplay.ID),
		IdempotencyKey: "conflict-open-late", RequestHash: testHash("conflict-open-payload"),
	})
	if err != nil || !late.Replayed || late.Conflict.ID != conflict.ID || late.Conflict.Status != domain.ConflictStatusInvestigating {
		t.Fatalf("late OpenConflict replay=%#v err=%v", late, err)
	}

	failed := conflict
	failed.ID = newID(t)
	failed.Fingerprint = ""
	failedMembers := []domain.ConflictMember{members[0], members[1]}
	failedMembers[0].ConflictID = failed.ID
	failedMembers[1].ConflictID = failed.ID
	failedMembers[1].ClaimID = newID(t)
	failed.Fingerprint = domain.ComputeConflictFingerprint(failed.WorkspaceID, failed.ApplicabilityAssessment, failedMembers)
	_, err = repository.OpenConflict(ctx, domain.OpenConflictRecord{
		Conflict: failed, Members: failedMembers, IdempotencyKey: "conflict-failed", RequestHash: testHash("conflict-failed"),
	})
	if !classifiedAs(err, foundation.ErrorConsistencyViolation) && !classifiedAs(err, foundation.ErrorNotFound) {
		t.Fatalf("missing conflict member err=%#v", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.conflict WHERE id=$1`, string(failed.ID)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed conflict left partial row count=%d err=%v", count, err)
	}
	conflicts, err := repository.BatchGetConflicts(ctx, domain.BatchGetConflictsQuery{WorkspaceID: fixture.workspaceID, IDs: []foundation.ID{conflict.ID}, Limit: 1})
	if err != nil || len(conflicts) != 1 || len(conflicts[0].Members) != 2 {
		t.Fatalf("BatchGetConflicts()=%#v err=%v", conflicts, err)
	}
	assertTerminalConflictAllowsClaimTransition(t, ctx, tx, fixture.workspaceID, conflict.ID, first.Claim.ID, now.Add(20*time.Second))
}

func TestRepositoryEvidenceEligibilityBatchesFiveHundredWithoutNPlusOne(t *testing.T) {
	repository, database, ctx := integrationPoolRepository(t)
	primary := seedProvenance(t, ctx, database, "eligibility-primary")
	alternate := seedProvenanceForWorkspace(t, ctx, database, primary.workspaceID, "eligibility-alternate", true)
	applicability := mustApplicability(t, `{"release":"v1"}`)
	now := time.Now().UTC().Add(-time.Minute)
	first := confirmClaim(t, ctx, repository, suggestClaim(t, ctx, repository, primary.workspaceID, "资格主张一", applicability, "eligibility-claim-a", now), primary, "eligibility-confirm-a", now.Add(time.Second))
	second := confirmClaim(t, ctx, repository, suggestClaim(t, ctx, repository, primary.workspaceID, "资格主张二", applicability, "eligibility-claim-b", now.Add(2*time.Second)), alternate, "eligibility-confirm-b", now.Add(3*time.Second))

	relation := newRelation(t, primary.workspaceID, domain.RelationSupports,
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: first.Claim.ID},
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: second.Claim.ID}, now.Add(4*time.Second))
	initialEvidence := relationEvidence(t, primary.workspaceID, "来源支持候选关系", applicability, primary, now.Add(4*time.Second), nil)
	initialEvidence.RelationID = relation.ID
	if _, err := repository.SuggestRelation(ctx, domain.SuggestRelationRecord{
		Relation: relation, Evidence: []domain.RelationEvidence{initialEvidence},
		IdempotencyKey: "eligibility-relation-suggest", RequestHash: testHash("eligibility-relation-suggest"),
	}); err != nil {
		t.Fatal(err)
	}
	confirmation := domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: "approval:eligibility:1"}
	confirmedEvidence := relationEvidence(t, primary.workspaceID, "审批确认正式关系", applicability, alternate, now.Add(5*time.Second), nil)
	confirmedEvidence.RelationID = relation.ID
	if _, err := repository.ConfirmRelation(ctx, domain.ConfirmRelationRecord{
		WorkspaceID: primary.workspaceID, RelationID: relation.ID, ExpectedVersion: 1,
		Evidence: confirmedEvidence, Confirmation: confirmation,
		IdempotencyKey: "eligibility-relation-confirm", RequestHash: testHash("eligibility-relation-confirm"), At: now.Add(5 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	requested := make([]domain.ProvenanceRef, 0, domain.MaxBatchLimit)
	requested = append(requested,
		domain.ProvenanceRef{WorkspaceID: primary.workspaceID, SourceVersionID: alternate.sourceVersionID, SourceSpanID: alternate.sourceSpanID},
		domain.ProvenanceRef{WorkspaceID: primary.workspaceID, SourceVersionID: primary.sourceVersionID, SourceSpanID: primary.sourceSpanID},
	)
	for len(requested) < domain.MaxBatchLimit {
		requested = append(requested, domain.ProvenanceRef{WorkspaceID: primary.workspaceID, SourceVersionID: newID(t), SourceSpanID: newID(t)})
	}
	eligible, err := repository.BatchCheckEvidenceEligibility(ctx, domain.EvidenceEligibilityQuery{WorkspaceID: primary.workspaceID, Provenance: requested})
	if err != nil || len(eligible) != domain.MaxBatchLimit {
		t.Fatalf("initial eligibility count=%d err=%v", len(eligible), err)
	}
	primaryResult := findEligibility(t, eligible, requested[1])
	alternateResult := findEligibility(t, eligible, requested[0])
	if primaryResult.Eligibility != domain.EvidenceEligible || alternateResult.Eligibility != domain.EvidenceEligible ||
		len(primaryResult.Bindings) != 2 || len(alternateResult.Bindings) != 2 {
		t.Fatalf("confirmed eligibility primary=%#v alternate=%#v", primaryResult, alternateResult)
	}

	conflict, members := newConflict(t, primary.workspaceID, first.Claim, second.Claim, now.Add(6*time.Second))
	if _, err := repository.OpenConflict(ctx, domain.OpenConflictRecord{
		Conflict: conflict, Members: members, IdempotencyKey: "eligibility-conflict-open", RequestHash: testHash("eligibility-conflict-open"),
	}); err != nil {
		t.Fatal(err)
	}
	disputed, err := repository.BatchCheckEvidenceEligibility(ctx, domain.EvidenceEligibilityQuery{WorkspaceID: primary.workspaceID, Provenance: requested})
	if err != nil || len(disputed) != domain.MaxBatchLimit {
		t.Fatalf("disputed eligibility count=%d err=%v", len(disputed), err)
	}
	expectedClaims := map[domain.ProvenanceRef]foundation.ID{requested[0]: second.Claim.ID, requested[1]: first.Claim.ID}
	for _, ref := range requested[:2] {
		result := findEligibility(t, disputed, ref)
		if result.Eligibility != domain.EvidenceEligibleWithConflict {
			t.Fatalf("disputed eligibility=%#v", result)
		}
		foundConflict := false
		foundDisputedClaim := false
		for _, binding := range result.Bindings {
			if binding.OwnerType == domain.EvidenceOwnerClaim && binding.OwnerID == expectedClaims[ref] {
				foundDisputedClaim = binding.ClaimStatus == domain.ClaimStatusDisputed &&
					binding.DisputedApplicability.Hash == applicability.Hash &&
					string(binding.DisputedApplicability.CanonicalJSON) == string(applicability.CanonicalJSON) &&
					binding.DisputedClaimUpdatedAtUTC.Equal(conflict.CreatedAt.UTC())
			}
			for _, conflictID := range binding.ConflictIDs {
				foundConflict = foundConflict || conflictID == conflict.ID
			}
		}
		if !foundConflict || !foundDisputedClaim {
			t.Fatalf("conflict metadata missing: %#v", result)
		}
	}
	unbound := findEligibility(t, disputed, requested[2])
	if unbound.Eligibility != domain.EvidenceIneligible || len(unbound.Bindings) != 0 {
		t.Fatalf("unbound provenance became eligible: %#v", unbound)
	}
}

func TestDatabaseConfirmedRelationRequiresMatchingConfirmationEvidence(t *testing.T) {
	repository, pool, ctx := integrationPoolRepository(t)
	fixture := seedProvenance(t, ctx, pool, "relation-confirmation-guard")
	applicability := mustApplicability(t, `{}`)
	now := time.Now().UTC().Add(-time.Minute)
	first := suggestClaim(t, ctx, repository, fixture.workspaceID, "确认门禁主张一", applicability, "guard-claim-a", now)
	second := suggestClaim(t, ctx, repository, fixture.workspaceID, "确认门禁主张二", applicability, "guard-claim-b", now)
	assertConfirmedRelationRequiresMatchingEvidence(t, ctx, pool, fixture, first.Claim.ID, second.Claim.ID, applicability, now.Add(time.Second))
}

func TestRepositoryCanonicalSymmetricRelationConcurrentReplay(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedProvenance(t, ctx, seedTx, "symmetric")
	repository, err := NewRepository(seedTx)
	if err != nil {
		t.Fatal(err)
	}
	applicability := mustApplicability(t, `{}`)
	now := time.Now().UTC().Add(-time.Minute)
	first := suggestClaim(t, ctx, repository, fixture.workspaceID, "相同知识 A", applicability, "symmetric-a", now)
	second := suggestClaim(t, ctx, repository, fixture.workspaceID, "相同知识 B", applicability, "symmetric-b", now)
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	repository, err = NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	forward := newRelation(t, fixture.workspaceID, domain.RelationDuplicates,
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: first.Claim.ID}, domain.NodeRef{Type: domain.NodeTypeClaim, ID: second.Claim.ID}, now.Add(time.Second))
	reverse := forward
	reverse.ID = newID(t)
	reverse.Source, reverse.Target, err = domain.CanonicalizeRelationEndpoints(domain.RelationDuplicates,
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: second.Claim.ID}, domain.NodeRef{Type: domain.NodeTypeClaim, ID: first.Claim.ID})
	if err != nil {
		t.Fatal(err)
	}
	reverse.Fingerprint = domain.ComputeRelationFingerprint(reverse.WorkspaceID, reverse.Type, reverse.Source, reverse.Target)
	type outcome struct {
		result domain.RelationResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	var start sync.WaitGroup
	start.Add(2)
	for index, candidate := range []domain.Relation{forward, reverse} {
		index, candidate := index, candidate
		go func() {
			start.Done()
			start.Wait()
			result, commandErr := repository.SuggestRelation(ctx, domain.SuggestRelationRecord{
				Relation: candidate, IdempotencyKey: "symmetric-relation-" + string(rune('a'+index)), RequestHash: testHash("symmetric-payload"),
			})
			outcomes <- outcome{result: result, err: commandErr}
		}()
	}
	firstOutcome, secondOutcome := <-outcomes, <-outcomes
	if firstOutcome.err != nil || secondOutcome.err != nil {
		t.Fatalf("concurrent symmetric errors: %v / %v", firstOutcome.err, secondOutcome.err)
	}
	if firstOutcome.result.Relation.ID != secondOutcome.result.Relation.ID || firstOutcome.result.Replayed == secondOutcome.result.Replayed {
		t.Fatalf("concurrent outcomes=%#v %#v", firstOutcome.result, secondOutcome.result)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.relation WHERE workspace_id=$1 AND fingerprint=$2`, string(fixture.workspaceID), forward.Fingerprint).Scan(&count); err != nil || count != 1 {
		t.Fatalf("symmetric relation count=%d err=%v", count, err)
	}
}

type provenanceFixture struct {
	workspaceID, sourceVersionID, sourceSpanID foundation.ID
}

func integrationRepository(t *testing.T) (*Repository, pgx.Tx, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	repository, err := NewRepository(tx)
	if err != nil {
		t.Fatal(err)
	}
	return repository, tx, ctx
}

func integrationPoolRepository(t *testing.T) (*Repository, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a migrated disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repository, err := NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	return repository, pool, ctx
}

type sqlExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func seedProvenance(t *testing.T, ctx context.Context, tx sqlExecer, label string) provenanceFixture {
	t.Helper()
	workspaceID := newID(t)
	now := time.Now().UTC().Add(-2 * time.Minute)
	root := "/tmp/knowledge-" + string(workspaceID)
	if _, err := tx.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,$2,$3,$3,$4,'test',1,$4,$4)`, string(workspaceID), label, root, now); err != nil {
		t.Fatal(err)
	}
	return seedProvenanceForWorkspace(t, ctx, tx, workspaceID, label, true)
}

func seedProvenanceForWorkspace(t *testing.T, ctx context.Context, tx sqlExecer, workspaceID foundation.ID, label string, includeMapping bool) provenanceFixture {
	t.Helper()
	artifactID, sourceID := newID(t), newID(t)
	sourceVersionID, projectionID, sourceSpanID := newID(t), newID(t), newID(t)
	now := time.Now().UTC().Add(-2 * time.Minute)
	contentHash := testHash("content-" + label)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO core.content_artifact(id,workspace_id,content_hash,byte_size,managed_location,created_at) VALUES($1,$2,$3,4,$4,$5)`, []any{string(artifactID), string(workspaceID), contentHash, ".knowledge/sources/" + contentHash, now}},
		{`INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at) VALUES($1,$2,'text',$3,$3,$4)`, []any{string(sourceID), string(workspaceID), label + ".txt", now}},
		{`INSERT INTO core.source_version(id,source_id,content_artifact_id,content_hash,byte_size,mime_type,original_content_location,security_status,captured_at) VALUES($1,$2,$3,$4,4,'text/plain',$5,'pending',$6)`, []any{string(sourceVersionID), string(sourceID), string(artifactID), contentHash, label + ".txt", now}},
		{`INSERT INTO ingestion.parse_projection(id,workspace_id,content_artifact_id,parser_id,parser_version,parser_config_hash,schema_version,normalized_content_hash,warnings,created_at) VALUES($1,$2,$3,'text','v1',$4,'v1',$5,'[]',$6)`, []any{string(projectionID), string(workspaceID), string(artifactID), testHash("parser-" + label), testHash("normalized-" + label), now}},
		{`INSERT INTO ingestion.source_span(id,workspace_id,content_artifact_id,parse_projection_id,span_type,start_line,end_line,start_byte,end_byte,selector,excerpt_hash,parser_version,schema_version,created_at) VALUES($1,$2,$3,$4,'paragraph',1,1,0,4,'{}',$5,'v1','v1',$6)`, []any{string(sourceSpanID), string(workspaceID), string(artifactID), string(projectionID), testHash("excerpt-" + label), now}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if includeMapping {
		if _, err := tx.Exec(ctx, `INSERT INTO ingestion.source_version_projection(source_version_id,parse_projection_id,workspace_id,created_at) VALUES($1,$2,$3,$4)`, string(sourceVersionID), string(projectionID), string(workspaceID), now); err != nil {
			t.Fatal(err)
		}
	}
	return provenanceFixture{workspaceID: workspaceID, sourceVersionID: sourceVersionID, sourceSpanID: sourceSpanID}
}

type transactionQueryer interface {
	Begin(context.Context) (pgx.Tx, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func assertConfirmedRelationRequiresMatchingEvidence(t *testing.T, ctx context.Context, tx transactionQueryer, fixture provenanceFixture, sourceClaimID, targetClaimID foundation.ID, applicability domain.Applicability, at time.Time) {
	t.Helper()
	subTx, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subTx.Rollback(ctx) }()
	relationID := newID(t)
	source, target, err := domain.CanonicalizeRelationEndpoints(domain.RelationCites,
		domain.NodeRef{Type: domain.NodeTypeClaim, ID: sourceClaimID}, domain.NodeRef{Type: domain.NodeTypeClaim, ID: targetClaimID})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := domain.ComputeRelationFingerprint(fixture.workspaceID, domain.RelationCites, source, target)
	confirmationRef := "approval:missing-method-match"
	if _, err := subTx.Exec(ctx, `
		INSERT INTO core.relation (
			id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
			relation_type,status,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,
			version,created_at,updated_at
		) VALUES ($1,$2,'CLAIM',$3,'CLAIM',$4,'CITES','SUGGESTED',$5,$6,NULL,NULL,1,$7,$7)`,
		string(relationID), string(fixture.workspaceID), string(source.ID), string(target.ID), fingerprint,
		testHash("direct-evidence-set"), at,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := subTx.Exec(ctx, `
		INSERT INTO core.relation_evidence (
			id,workspace_id,relation_id,source_version_id,source_span_id,reason,evidence_hash,
			applicability,applicability_schema_version,applicability_hash,
			confirmation_method,confirmed_by,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,'SOURCE_DERIVED',$11,$12)`,
		string(newID(t)), string(fixture.workspaceID), string(relationID), string(fixture.sourceVersionID), string(fixture.sourceSpanID),
		"确认方式不匹配的证据", testHash("direct-evidence"), []byte(applicability.CanonicalJSON), applicability.SchemaVersion,
		applicability.Hash, confirmationRef, at,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := subTx.Exec(ctx, `
		UPDATE core.relation
		SET status='CONFIRMED',confirmation_method='USER_APPROVAL',confirmation_ref=$1,
			version=2,updated_at=$2
		WHERE id=$3 AND workspace_id=$4`, confirmationRef, at.Add(time.Second), string(relationID), string(fixture.workspaceID)); err != nil {
		t.Fatal(err)
	}
	err = subTx.Commit(ctx)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
		t.Fatalf("confirmed relation without matching evidence commit err=%v", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM core.relation WHERE id=$1`, string(relationID)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed confirmed relation persisted count=%d err=%v", count, err)
	}
}

func assertTerminalConflictAllowsClaimTransition(t *testing.T, ctx context.Context, tx transactionQueryer, workspaceID, conflictID, claimID foundation.ID, at time.Time) {
	t.Helper()
	subTx, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subTx.Rollback(ctx) }()
	if _, err := subTx.Exec(ctx, `
		UPDATE core.conflict
		SET status='RESOLUTION_PROPOSED',version=version+1,updated_at=$1
		WHERE workspace_id=$2 AND id=$3 AND status='INVESTIGATING' AND version=2`, at, string(workspaceID), string(conflictID)); err != nil {
		t.Fatal(err)
	}
	if _, err := subTx.Exec(ctx, `
		UPDATE core.conflict
		SET status='RESOLVED',resolution=$1,resolution_reference=$2,resolved_at=$3,
			version=version+1,updated_at=$3
		WHERE workspace_id=$4 AND id=$5 AND status='RESOLUTION_PROPOSED' AND version=3`,
		"已核对并选择第一项主张", "resolution:manual:1", at.Add(time.Second), string(workspaceID), string(conflictID)); err != nil {
		t.Fatal(err)
	}
	if _, err := subTx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := subTx.Exec(ctx, `SET CONSTRAINTS ALL DEFERRED`); err != nil {
		t.Fatal(err)
	}
	if _, err := subTx.Exec(ctx, `
		UPDATE core.claim SET status='CONFIRMED',version=version+1,updated_at=$1
		WHERE workspace_id=$2 AND id=$3 AND status='DISPUTED' AND version=3`, at.Add(2*time.Second), string(workspaceID), string(claimID)); err != nil {
		t.Fatal(err)
	}
	if err := subTx.Commit(ctx); err != nil {
		t.Fatalf("terminal conflict then claim transition commit: %v", err)
	}
	var conflictStatus, claimStatus string
	var conflictVersion, claimVersion int64
	if err := tx.QueryRow(ctx, `SELECT status,version FROM core.conflict WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(conflictID)).Scan(&conflictStatus, &conflictVersion); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT status,version FROM core.claim WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(claimID)).Scan(&claimStatus, &claimVersion); err != nil {
		t.Fatal(err)
	}
	if conflictStatus != string(domain.ConflictStatusResolved) || conflictVersion != 4 || claimStatus != string(domain.ClaimStatusConfirmed) || claimVersion != 4 {
		t.Fatalf("terminal transition facts conflict=%s/%d claim=%s/%d", conflictStatus, conflictVersion, claimStatus, claimVersion)
	}
}

type commitResponseLossDB struct{ pool *pgxpool.Pool }

func (d commitResponseLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return commitResponseLossTx{Tx: tx}, nil
}

func (d commitResponseLossDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return d.pool.Query(ctx, sql, args...)
}

func (d commitResponseLossDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return d.pool.QueryRow(ctx, sql, args...)
}

type commitResponseLossTx struct{ pgx.Tx }

func (tx commitResponseLossTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("injected knowledge commit response loss")
}

func newTopic(t *testing.T, workspaceID foundation.ID, name string, at time.Time) domain.Topic {
	t.Helper()
	display, normalized, err := domain.NormalizeTopicText(name)
	if err != nil {
		t.Fatal(err)
	}
	alias, normalizedAlias, err := domain.NormalizeTopicText(name + " Alias")
	if err != nil {
		t.Fatal(err)
	}
	return domain.Topic{ID: newID(t), WorkspaceID: workspaceID, Name: display, NormalizedName: normalized,
		Description: "并发主题", Aliases: []domain.TopicAlias{{Name: alias, NormalizedName: normalizedAlias}},
		Status: domain.TopicStatusActive, Version: 1, CreatedAt: at, UpdatedAt: at}
}

func suggestClaim(t *testing.T, ctx context.Context, repository *Repository, workspaceID foundation.ID, statement string, applicability domain.Applicability, key string, at time.Time) domain.ClaimResult {
	t.Helper()
	canonical, normalized, err := domain.NormalizeStatement(statement)
	if err != nil {
		t.Fatal(err)
	}
	factors, err := domain.NormalizeConfidenceFactors(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	claim := domain.Claim{ID: newID(t), WorkspaceID: workspaceID, Statement: canonical, NormalizedStatement: normalized,
		Applicability: applicability, Status: domain.ClaimStatusSuggested, ConfidenceFactors: factors,
		Version: 1, CreatedAt: at, UpdatedAt: at}
	claim.Fingerprint = domain.ComputeClaimFingerprint(workspaceID, normalized, applicability)
	result, err := repository.SuggestClaim(ctx, domain.SuggestClaimRecord{Claim: claim, IdempotencyKey: key, RequestHash: testHash(key + "-payload")})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func confirmClaim(t *testing.T, ctx context.Context, repository *Repository, claim domain.ClaimResult, fixture provenanceFixture, key string, at time.Time) domain.ClaimResult {
	t.Helper()
	source := newClaimSource(t, fixture.workspaceID, claim.Claim.ID, fixture, at)
	result, err := repository.ConfirmClaim(ctx, domain.ConfirmClaimRecord{
		WorkspaceID: fixture.workspaceID, ClaimID: claim.Claim.ID, ExpectedVersion: claim.Claim.Version,
		Source: source, IdempotencyKey: key, RequestHash: testHash(key + "-payload"), At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newClaimSource(t *testing.T, workspaceID, claimID foundation.ID, fixture provenanceFixture, at time.Time) domain.ClaimSource {
	t.Helper()
	return domain.ClaimSource{ID: newID(t), WorkspaceID: workspaceID, ClaimID: claimID,
		Provenance:  domain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: fixture.sourceVersionID, SourceSpanID: fixture.sourceSpanID},
		SupportType: domain.ClaimSupportSupports, Reason: "原始材料直接支持该主张", CreatedAt: at}
}

func newRelation(t *testing.T, workspaceID foundation.ID, relationType domain.RelationType, source, target domain.NodeRef, at time.Time) domain.Relation {
	t.Helper()
	canonicalSource, canonicalTarget, err := domain.CanonicalizeRelationEndpoints(relationType, source, target)
	if err != nil {
		t.Fatal(err)
	}
	relation := domain.Relation{ID: newID(t), WorkspaceID: workspaceID, Source: canonicalSource, Target: canonicalTarget,
		Type: relationType, Status: domain.RelationStatusSuggested, Version: 1, CreatedAt: at, UpdatedAt: at}
	relation.Fingerprint = domain.ComputeRelationFingerprint(workspaceID, relationType, canonicalSource, canonicalTarget)
	return relation
}

func relationEvidence(t *testing.T, workspaceID foundation.ID, reason string, applicability domain.Applicability, fixture provenanceFixture, at time.Time, confirmation *domain.Confirmation) domain.RelationEvidence {
	t.Helper()
	return domain.RelationEvidence{ID: newID(t), WorkspaceID: workspaceID,
		Provenance: domain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: fixture.sourceVersionID, SourceSpanID: fixture.sourceSpanID},
		Reason:     reason, Applicability: applicability, Confirmation: confirmation, CreatedAt: at}
}

func newConflict(t *testing.T, workspaceID foundation.ID, first, second domain.Claim, at time.Time) (domain.Conflict, []domain.ConflictMember) {
	t.Helper()
	conflictID := newID(t)
	members := []domain.ConflictMember{
		{ConflictID: conflictID, WorkspaceID: workspaceID, ClaimID: first.ID, Applicability: first.Applicability, ApplicabilityHash: first.Applicability.Hash, PositionSummary: "第一项主张成立", CreatedAt: at},
		{ConflictID: conflictID, WorkspaceID: workspaceID, ClaimID: second.ID, Applicability: second.Applicability, ApplicabilityHash: second.Applicability.Hash, PositionSummary: "第二项主张成立", CreatedAt: at},
	}
	conflict := domain.Conflict{ID: conflictID, WorkspaceID: workspaceID, Status: domain.ConflictStatusOpen,
		Severity: domain.ConflictSeverityHigh, Summary: "两项主张不能同时成立", ApplicabilityAssessment: domain.ApplicabilityAssessmentExact,
		Version: 1, CreatedAt: at, UpdatedAt: at}
	conflict.Fingerprint = domain.ComputeConflictFingerprint(workspaceID, conflict.ApplicabilityAssessment, members)
	return conflict, members
}

func remapConflictMembers(members []domain.ConflictMember, conflictID foundation.ID) []domain.ConflictMember {
	result := append([]domain.ConflictMember(nil), members...)
	for index := range result {
		result[index].ConflictID = conflictID
	}
	return result
}

func mustApplicability(t *testing.T, raw string) domain.Applicability {
	t.Helper()
	value, err := domain.ParseApplicability(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func findEligibility(t *testing.T, values []domain.ProvenanceEligibility, ref domain.ProvenanceRef) domain.ProvenanceEligibility {
	t.Helper()
	for _, value := range values {
		if value.Provenance == ref {
			return value
		}
	}
	t.Fatalf("eligibility result missing provenance %#v", ref)
	return domain.ProvenanceEligibility{}
}

func newID(t *testing.T) foundation.ID {
	t.Helper()
	id, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func testHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func classifiedAs(err error, kind foundation.ErrorKind) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == kind
}

func hasCode(err error, code string) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == code
}
