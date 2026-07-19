package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
)

func TestNewServiceRequiresEveryDependency(t *testing.T) {
	dependencies := testDependencies(&fakeRepository{})
	for name, mutate := range map[string]func(*Dependencies){
		"repository": func(d *Dependencies) { d.Repository = nil }, "provenance": func(d *Dependencies) { d.Provenance = nil }, "confirmation": func(d *Dependencies) { d.Confirmation = nil }, "ids": func(d *Dependencies) { d.IDs = nil }, "clock": func(d *Dependencies) { d.Clock = nil },
	} {
		t.Run(name, func(t *testing.T) {
			copy := dependencies
			mutate(&copy)
			if _, err := NewService(copy); errorCode(err) != errorCodeServiceUnavailable {
				t.Fatalf("code=%s err=%v", errorCode(err), err)
			}
		})
	}
}

func TestCreateTopicCanonicalizesAliasesBoundsInputAndHashesOnlyBusinessInput(t *testing.T) {
	command := CreateTopicCommand{WorkspaceID: testID(1), Name: "  Go\t语言 ", Description: "  运行时  与 工具链 ", Aliases: []string{" Golang ", "GO"}, IdempotencyKey: " topic-1 "}
	var records []domain.CreateTopicRecord
	for index := 0; index < 2; index++ {
		repo := &fakeRepository{createTopic: func(_ context.Context, record domain.CreateTopicRecord) (domain.TopicResult, error) {
			records = append(records, record)
			return domain.TopicResult{Topic: record.Topic}, nil
		}}
		dependencies := testDependencies(repo)
		dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(10 + index)}}
		dependencies.Clock = foundation.FixedClock{Value: testTime().Add(time.Duration(index) * time.Hour)}
		service := mustService(t, dependencies)
		if _, err := service.CreateTopic(context.Background(), command); err != nil {
			t.Fatalf("create topic: %v", err)
		}
	}
	if len(records) != 2 || records[0].RequestHash != records[1].RequestHash {
		t.Fatalf("request hash includes generated identity/time: %#v", records)
	}
	if records[0].Topic.Name != "Go 语言" || records[0].Topic.Description != "运行时 与 工具链" || records[0].IdempotencyKey != "topic-1" || records[0].Topic.Aliases[0].NormalizedName != "go" {
		t.Fatalf("topic was not canonicalized: %#v", records[0])
	}
	repo := &fakeRepository{}
	service := mustService(t, testDependencies(repo))
	command.Aliases = make([]string, MaxBatchSize+1)
	if _, err := service.CreateTopic(context.Background(), command); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("alias limit code=%s err=%v", errorCode(err), err)
	}
}

func TestCreateTopicReplayReturnsCurrentFactWithoutDuplicateCreate(t *testing.T) {
	var stored domain.Topic
	var hash string
	creates := 0
	repo := &fakeRepository{createTopic: func(_ context.Context, record domain.CreateTopicRecord) (domain.TopicResult, error) {
		if stored.ID == "" {
			creates++
			stored = record.Topic
			hash = record.RequestHash
			return domain.TopicResult{Topic: stored}, nil
		}
		if record.RequestHash != hash {
			return domain.TopicResult{}, errors.New("hash drift")
		}
		stored.Status = domain.TopicStatusDeprecated
		stored.Version = 2
		stored.UpdatedAt = stored.UpdatedAt.Add(time.Hour)
		return domain.TopicResult{Topic: stored, Replayed: true}, nil
	}}
	dependencies := testDependencies(repo)
	dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(10), testID(11)}}
	service := mustService(t, dependencies)
	command := CreateTopicCommand{WorkspaceID: testID(1), Name: "Go", IdempotencyKey: "topic-replay"}
	if _, err := service.CreateTopic(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateTopic(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if creates != 1 || !result.Replayed || result.Topic.Status != domain.TopicStatusDeprecated || result.Topic.Version != 2 {
		t.Fatalf("creates=%d result=%#v", creates, result)
	}
}

func TestSuggestClaimCanonicalRequestHashExcludesGeneratedFields(t *testing.T) {
	command := SuggestClaimCommand{WorkspaceID: testID(1), Statement: "  Go  很快 ", Applicability: json.RawMessage(`{"b":2,"a":1}`), ConfidenceFactors: json.RawMessage(`{"z":1,"a":2}`), IdempotencyKey: "claim-1"}
	var records []domain.SuggestClaimRecord
	for index := 0; index < 2; index++ {
		repo := &fakeRepository{suggestClaim: func(_ context.Context, record domain.SuggestClaimRecord) (domain.ClaimResult, error) {
			records = append(records, record)
			return domain.ClaimResult{Claim: record.Claim}, nil
		}}
		dependencies := testDependencies(repo)
		dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(20 + index)}}
		dependencies.Clock = foundation.FixedClock{Value: testTime().Add(time.Duration(index) * time.Hour)}
		if _, err := mustService(t, dependencies).SuggestClaim(context.Background(), command); err != nil {
			t.Fatal(err)
		}
	}
	if records[0].RequestHash != records[1].RequestHash || string(records[0].Claim.Applicability.CanonicalJSON) != `{"a":1,"b":2}` || string(records[0].Claim.ConfidenceFactors) != `{"a":2,"z":1}` {
		t.Fatalf("records=%#v", records)
	}
}

func TestRelationAndConfirmationRequestHashesExcludeGeneratedFields(t *testing.T) {
	app, err := domain.ParseApplicability(json.RawMessage(`{"scope":"all"}`))
	if err != nil {
		t.Fatal(err)
	}
	relationA := testRelation(testID(22), domain.RelationStatusSuggested, 1)
	relationB := relationA
	relationB.ID = testID(23)
	relationB.CreatedAt = relationB.CreatedAt.Add(time.Hour)
	relationB.UpdatedAt = relationB.CreatedAt
	evidenceA := domain.RelationEvidence{ID: testID(24), WorkspaceID: testID(1), RelationID: relationA.ID, Provenance: testProvenance(6), Reason: "依据", Applicability: app, CreatedAt: testTime()}
	evidenceA.EvidenceHash = domain.ComputeRelationEvidenceHash(evidenceA)
	evidenceB := evidenceA
	evidenceB.ID = testID(25)
	evidenceB.RelationID = relationB.ID
	evidenceB.CreatedAt = evidenceB.CreatedAt.Add(time.Hour)
	evidenceB.EvidenceHash = domain.ComputeRelationEvidenceHash(evidenceB)
	if hashSuggestRelationRequest(relationA, []domain.RelationEvidence{evidenceA}) != hashSuggestRelationRequest(relationB, []domain.RelationEvidence{evidenceB}) {
		t.Fatal("suggest relation hash includes generated identity/time")
	}
	confirmation := domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: "approval:1"}
	evidenceA.Confirmation = &confirmation
	evidenceB.Confirmation = &confirmation
	confirmA := domain.ConfirmRelationRecord{WorkspaceID: testID(1), RelationID: testID(26), ExpectedVersion: 1, Evidence: evidenceA, Confirmation: confirmation, At: testTime()}
	confirmB := confirmA
	confirmB.Evidence = evidenceB
	confirmB.At = confirmB.At.Add(time.Hour)
	if hashConfirmRelationRequest(confirmA) != hashConfirmRelationRequest(confirmB) {
		t.Fatal("confirm relation hash includes generated evidence identity/time")
	}
	sourceA := domain.ClaimSource{ID: testID(27), WorkspaceID: testID(1), ClaimID: testID(28), Provenance: testProvenance(7), SupportType: domain.ClaimSupportSupports, Reason: "依据", CreatedAt: testTime()}
	sourceB := sourceA
	sourceB.ID = testID(29)
	sourceB.CreatedAt = sourceB.CreatedAt.Add(time.Hour)
	claimA := domain.ConfirmClaimRecord{WorkspaceID: testID(1), ClaimID: sourceA.ClaimID, ExpectedVersion: 1, Source: sourceA, At: testTime()}
	claimB := claimA
	claimB.Source = sourceB
	claimB.At = claimB.At.Add(time.Hour)
	if hashConfirmClaimRequest(claimA) != hashConfirmClaimRequest(claimB) {
		t.Fatal("confirm claim hash includes generated source identity/time")
	}
}

func TestConfirmClaimValidatesSupportingProvenanceAndReadback(t *testing.T) {
	claim := testClaim(t, testID(30), domain.ClaimStatusSuggested, 1)
	provenance := testProvenance(1)
	verifier := &fakeProvenanceVerifier{}
	repo := &fakeRepository{confirmClaim: func(_ context.Context, record domain.ConfirmClaimRecord) (domain.ClaimResult, error) {
		source := record.Source
		source.EvidenceHash = domain.ComputeClaimSourceEvidenceHash(source, claim.Applicability)
		claim.Status = domain.ClaimStatusConfirmed
		claim.Version = 2
		claim.UpdatedAt = record.At
		return domain.ClaimResult{Claim: claim, Sources: []domain.ClaimSource{source}}, nil
	}}
	dependencies := testDependencies(repo)
	dependencies.Provenance = verifier
	dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(31)}}
	service := mustService(t, dependencies)
	command := ConfirmClaimCommand{WorkspaceID: testID(1), ClaimID: claim.ID, ExpectedVersion: 1, Source: ClaimSourceInput{Provenance: provenance, SupportType: domain.ClaimSupportSupports, Reason: "原文支持"}, IdempotencyKey: "confirm-1"}
	if _, err := service.ConfirmClaim(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if len(verifier.refs) != 1 {
		t.Fatalf("provenance calls=%d", len(verifier.refs))
	}
	command.ExpectedVersion = 0
	if _, err := service.ConfirmClaim(context.Background(), command); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("zero version accepted: %v", err)
	}
	command.ExpectedVersion = 1
	command.Source.SupportType = domain.ClaimSupportRefutes
	if _, err := service.ConfirmClaim(context.Background(), command); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("refuting source accepted: %v", err)
	}
}

func TestConfirmRelationVerifiesBothPortsBeforeRepository(t *testing.T) {
	relation := testRelation(testID(40), domain.RelationStatusSuggested, 1)
	provenance := &fakeProvenanceVerifier{}
	confirmation := &fakeConfirmationVerifier{}
	repo := &fakeRepository{confirmRelation: func(_ context.Context, record domain.ConfirmRelationRecord) (domain.RelationResult, error) {
		relation.Status = domain.RelationStatusConfirmed
		relation.Version = 2
		relation.UpdatedAt = record.At
		relation.Confirmation = &record.Confirmation
		relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint([]domain.RelationEvidence{record.Evidence})
		return domain.RelationResult{Relation: relation, Evidence: []domain.RelationEvidence{record.Evidence}}, nil
	}}
	dependencies := testDependencies(repo)
	dependencies.Provenance = provenance
	dependencies.Confirmation = confirmation
	dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(41)}}
	service := mustService(t, dependencies)
	command := ConfirmRelationCommand{WorkspaceID: testID(1), RelationID: relation.ID, ExpectedVersion: 1, Evidence: RelationEvidenceInput{Provenance: testProvenance(2), Reason: "原文支持关系", Applicability: json.RawMessage(`{"scope":"all"}`)}, Confirmation: domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: "approval:1"}, IdempotencyKey: "confirm-relation"}
	if _, err := service.ConfirmRelation(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if len(provenance.refs) != 1 || len(confirmation.values) != 1 {
		t.Fatalf("port calls=%d/%d", len(provenance.refs), len(confirmation.values))
	}
	confirmation.err = foundation.NewError(foundation.ErrorPermissionDenied, "CONFIRMATION_REJECTED", false, errors.New("rejected"))
	repo.confirmRelation = func(context.Context, domain.ConfirmRelationRecord) (domain.RelationResult, error) {
		t.Fatal("repository called after failed confirmation")
		return domain.RelationResult{}, nil
	}
	dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(42)}}
	service = mustService(t, dependencies)
	if _, err := service.ConfirmRelation(context.Background(), command); errorCode(err) != "CONFIRMATION_REJECTED" {
		t.Fatalf("code=%s err=%v", errorCode(err), err)
	}
}

func TestOpenConflictSortsMembersAndBuildsStableRequestHash(t *testing.T) {
	command := OpenConflictCommand{WorkspaceID: testID(1), Severity: domain.ConflictSeverityHigh, Summary: "结论相互冲突", ApplicabilityAssessment: domain.ApplicabilityAssessmentExact, Members: []ConflictMemberInput{{ClaimID: testID(52), Applicability: json.RawMessage(`{"scope":"all"}`), PositionSummary: "反方"}, {ClaimID: testID(51), Applicability: json.RawMessage(`{"scope":"all"}`), PositionSummary: "正方"}}, IdempotencyKey: "conflict-1"}
	var records []domain.OpenConflictRecord
	for index := 0; index < 2; index++ {
		repo := &fakeRepository{openConflict: func(_ context.Context, record domain.OpenConflictRecord) (domain.ConflictResult, error) {
			records = append(records, record)
			return domain.ConflictResult{Conflict: record.Conflict, Members: record.Members}, nil
		}}
		dependencies := testDependencies(repo)
		dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(53 + index)}}
		dependencies.Clock = foundation.FixedClock{Value: testTime().Add(time.Duration(index) * time.Hour)}
		if _, err := mustService(t, dependencies).OpenConflict(context.Background(), command); err != nil {
			t.Fatal(err)
		}
	}
	if records[0].RequestHash != records[1].RequestHash || records[0].Members[0].ClaimID != testID(51) {
		t.Fatalf("conflict records=%#v", records)
	}
}

func TestSuggestRelationCanonicalizesAndProvenanceFailureStopsWrite(t *testing.T) {
	command := SuggestRelationCommand{WorkspaceID: testID(1), Source: domain.NodeRef{Type: domain.NodeTypeClaim, ID: testID(82)}, Target: domain.NodeRef{Type: domain.NodeTypeClaim, ID: testID(81)}, Type: domain.RelationDuplicates, Evidence: []RelationEvidenceInput{{Provenance: testProvenance(3), Reason: "两段内容等价", Applicability: json.RawMessage(`{"scope":"all"}`)}}, IdempotencyKey: "relation-1"}
	verifier := &fakeProvenanceVerifier{}
	repo := &fakeRepository{suggestRelation: func(_ context.Context, record domain.SuggestRelationRecord) (domain.RelationResult, error) {
		return domain.RelationResult{Relation: record.Relation, Evidence: record.Evidence}, nil
	}}
	dependencies := testDependencies(repo)
	dependencies.Provenance = verifier
	dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(83), testID(84)}}
	result, err := mustService(t, dependencies).SuggestRelation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Relation.Source.ID != testID(81) || len(verifier.refs) != 1 {
		t.Fatalf("result=%#v verify=%d", result, len(verifier.refs))
	}
	verifier.err = foundation.NewError(foundation.ErrorConsistencyViolation, "PROVENANCE_BROKEN", false, errors.New("broken"))
	repo.suggestRelation = func(context.Context, domain.SuggestRelationRecord) (domain.RelationResult, error) {
		t.Fatal("repository called after failed provenance")
		return domain.RelationResult{}, nil
	}
	dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(85), testID(86)}}
	if _, err := mustService(t, dependencies).SuggestRelation(context.Background(), command); errorCode(err) != "PROVENANCE_BROKEN" {
		t.Fatalf("code=%s err=%v", errorCode(err), err)
	}
}

func TestTransitionCommandsAndLongLivedReplayAcceptCurrentFacts(t *testing.T) {
	claim := testClaim(t, testID(91), domain.ClaimStatusSuggested, 1)
	relation := testRelation(testID(92), domain.RelationStatusSuggested, 1)
	conflict, members := testConflict(t, testID(93), domain.ConflictStatusOpen, 1)
	repo := &fakeRepository{
		transitionClaim: func(_ context.Context, record domain.TransitionClaimRecord) (domain.ClaimResult, error) {
			claim.Status = domain.ClaimStatusDeprecated
			claim.Version = 4
			claim.UpdatedAt = record.At
			return domain.ClaimResult{Claim: claim, Replayed: true}, nil
		},
		transitionRelation: func(_ context.Context, record domain.TransitionRelationRecord) (domain.RelationResult, error) {
			relation.Status = domain.RelationStatusRejected
			relation.Version = 4
			relation.UpdatedAt = record.At
			return domain.RelationResult{Relation: relation, Replayed: true}, nil
		},
		transitionConflict: func(_ context.Context, record domain.TransitionConflictRecord) (domain.ConflictResult, error) {
			conflict.Status = domain.ConflictStatusDeferred
			conflict.Version = 4
			conflict.UpdatedAt = record.At
			return domain.ConflictResult{Conflict: conflict, Members: members, Replayed: true}, nil
		},
	}
	service := mustService(t, testDependencies(repo))
	ctx := context.Background()
	if _, err := service.TransitionClaim(ctx, TransitionClaimCommand{WorkspaceID: testID(1), ClaimID: claim.ID, ExpectedVersion: 1, Status: domain.ClaimStatusInvalid, IdempotencyKey: "claim-old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TransitionRelation(ctx, TransitionRelationCommand{WorkspaceID: testID(1), RelationID: relation.ID, ExpectedVersion: 1, Status: domain.RelationStatusRejected, IdempotencyKey: "relation-old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TransitionConflict(ctx, TransitionConflictCommand{WorkspaceID: testID(1), ConflictID: conflict.ID, ExpectedVersion: 1, Status: domain.ConflictStatusInvestigating, IdempotencyKey: "conflict-old"}); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmCommandsLongLivedReplayAcceptsEvolvedAggregates(t *testing.T) {
	claim := testClaim(t, testID(94), domain.ClaimStatusSuggested, 1)
	relation := testRelation(testID(95), domain.RelationStatusSuggested, 1)
	repo := &fakeRepository{
		confirmClaim: func(_ context.Context, record domain.ConfirmClaimRecord) (domain.ClaimResult, error) {
			source := record.Source
			source.EvidenceHash = domain.ComputeClaimSourceEvidenceHash(source, claim.Applicability)
			claim.Status = domain.ClaimStatusDeprecated
			claim.Version = 4
			claim.UpdatedAt = record.At
			return domain.ClaimResult{Claim: claim, Sources: []domain.ClaimSource{source}, Replayed: true}, nil
		},
		confirmRelation: func(_ context.Context, record domain.ConfirmRelationRecord) (domain.RelationResult, error) {
			relation.Status = domain.RelationStatusStale
			relation.Version = 4
			relation.UpdatedAt = record.At
			relation.Confirmation = &record.Confirmation
			relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint([]domain.RelationEvidence{record.Evidence})
			return domain.RelationResult{Relation: relation, Evidence: []domain.RelationEvidence{record.Evidence}, Replayed: true}, nil
		},
	}
	dependencies := testDependencies(repo)
	dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(96), testID(97)}}
	service := mustService(t, dependencies)
	ctx := context.Background()
	if _, err := service.ConfirmClaim(ctx, ConfirmClaimCommand{WorkspaceID: testID(1), ClaimID: claim.ID, ExpectedVersion: 1, Source: ClaimSourceInput{Provenance: testProvenance(4), SupportType: domain.ClaimSupportSupports, Reason: "原文支持"}, IdempotencyKey: "confirm-claim-old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConfirmRelation(ctx, ConfirmRelationCommand{WorkspaceID: testID(1), RelationID: relation.ID, ExpectedVersion: 1, Evidence: RelationEvidenceInput{Provenance: testProvenance(5), Reason: "原文支持关系", Applicability: json.RawMessage(`{"scope":"all"}`)}, Confirmation: domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: "approval:old"}, IdempotencyKey: "confirm-relation-old"}); err != nil {
		t.Fatal(err)
	}
}

func TestTransitionConflictRequiresResolutionReferencePair(t *testing.T) {
	service := mustService(t, testDependencies(&fakeRepository{}))
	resolution := "采用条件化结论"
	_, err := service.TransitionConflict(context.Background(), TransitionConflictCommand{WorkspaceID: testID(1), ConflictID: testID(2), ExpectedVersion: 1, Status: domain.ConflictStatusResolved, Resolution: &resolution, IdempotencyKey: "resolve"})
	if errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("missing reference code=%s err=%v", errorCode(err), err)
	}
}

func TestTransitionConflictTerminalResultCarriesNormalizedResolution(t *testing.T) {
	conflict, members := testConflict(t, testID(98), domain.ConflictStatusResolutionProposed, 1)
	repo := &fakeRepository{transitionConflict: func(_ context.Context, record domain.TransitionConflictRecord) (domain.ConflictResult, error) {
		conflict.Status = record.Status
		conflict.Version = 2
		conflict.UpdatedAt = record.At
		conflict.Resolution = record.Resolution
		conflict.ResolutionReference = record.ResolutionReference
		return domain.ConflictResult{Conflict: conflict, Members: members}, nil
	}}
	service := mustService(t, testDependencies(repo))
	resolution, reference := "  采用 条件化结论 ", " resolution:1 "
	result, err := service.TransitionConflict(context.Background(), TransitionConflictCommand{WorkspaceID: testID(1), ConflictID: conflict.ID, ExpectedVersion: 1, Status: domain.ConflictStatusResolved, Resolution: &resolution, ResolutionReference: &reference, IdempotencyKey: "resolve"})
	if err != nil {
		t.Fatal(err)
	}
	if *result.Conflict.Resolution != "采用 条件化结论" || *result.Conflict.ResolutionReference != "resolution:1" {
		t.Fatalf("result=%#v", result)
	}
}

func TestResuggestRelationRequiresAndPersistsNewEvidence(t *testing.T) {
	relation := testRelation(testID(107), domain.RelationStatusRejected, 1)
	old := testRelationEvidence(t, testID(108), relation.ID, testProvenance(20), nil)
	relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint([]domain.RelationEvidence{old})
	port := &fakeProvenanceVerifier{}
	repo := &fakeRepository{transitionRelation: func(_ context.Context, record domain.TransitionRelationRecord) (domain.RelationResult, error) {
		if record.Evidence == nil {
			t.Fatal("new evidence missing from record")
		}
		relation.Status = domain.RelationStatusSuggested
		relation.Version = 2
		relation.UpdatedAt = record.At
		evidence := []domain.RelationEvidence{old, *record.Evidence}
		relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint(evidence)
		return domain.RelationResult{Relation: relation, Evidence: evidence}, nil
	}}
	dependencies := testDependencies(repo)
	dependencies.Provenance = port
	dependencies.IDs = &sequenceIDGenerator{ids: []foundation.ID{testID(109)}}
	service := mustService(t, dependencies)
	input := RelationEvidenceInput{Provenance: testProvenance(21), Reason: "新增来源证明关系", Applicability: json.RawMessage(`{"scope":"all"}`)}
	if _, err := service.TransitionRelation(context.Background(), TransitionRelationCommand{WorkspaceID: testID(1), RelationID: relation.ID, ExpectedVersion: 1, Status: domain.RelationStatusSuggested, Evidence: &input, IdempotencyKey: "resuggest"}); err != nil {
		t.Fatal(err)
	}
	if len(port.refs) != 1 {
		t.Fatalf("verify calls=%d", len(port.refs))
	}
	if _, err := service.TransitionRelation(context.Background(), TransitionRelationCommand{WorkspaceID: testID(1), RelationID: relation.ID, ExpectedVersion: 1, Status: domain.RelationStatusSuggested, IdempotencyKey: "missing"}); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("missing evidence accepted: %v", err)
	}
	if _, err := service.TransitionRelation(context.Background(), TransitionRelationCommand{WorkspaceID: testID(1), RelationID: relation.ID, ExpectedVersion: 1, Status: domain.RelationStatusRejected, Evidence: &input, IdempotencyKey: "wrong-status"}); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("non-suggest evidence accepted: %v", err)
	}
}

func TestOpenConflictRejectsInsufficientOrAmbiguousMembersBeforeWrite(t *testing.T) {
	service := mustService(t, testDependencies(&fakeRepository{}))
	command := OpenConflictCommand{WorkspaceID: testID(1), Severity: domain.ConflictSeverityHigh, Summary: "冲突", ApplicabilityAssessment: domain.ApplicabilityAssessmentExact, Members: []ConflictMemberInput{{ClaimID: testID(1), Applicability: json.RawMessage(`{"scope":"all"}`), PositionSummary: "单方"}}, IdempotencyKey: "conflict"}
	if _, err := service.OpenConflict(context.Background(), command); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("single member accepted: %v", err)
	}
	command.Members = []ConflictMemberInput{{ClaimID: testID(2), Applicability: json.RawMessage(`{"scope":"a"}`), PositionSummary: "甲"}, {ClaimID: testID(3), Applicability: json.RawMessage(`{"scope":"b"}`), PositionSummary: "乙"}}
	if _, err := service.OpenConflict(context.Background(), command); err == nil {
		t.Fatal("exact assessment accepted different applicability")
	}
}

func TestRelationAndConflictQueriesRejectUnstableOrCrossWorkspaceResults(t *testing.T) {
	newerRelation := testRelation(testID(101), domain.RelationStatusSuggested, 1)
	newerRelation.UpdatedAt = testTime().Add(time.Hour)
	olderRelation := testRelation(testID(102), domain.RelationStatusSuggested, 1)
	newerConflict, newerMembers := testConflict(t, testID(103), domain.ConflictStatusOpen, 1)
	newerConflict.UpdatedAt = testTime().Add(time.Hour)
	olderConflict, olderMembers := testConflict(t, testID(104), domain.ConflictStatusOpen, 1)
	repo := &fakeRepository{batchRelations: func(context.Context, domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error) {
		return []domain.RelationWithEvidence{{Relation: newerRelation}, {Relation: olderRelation}}, nil
	}, batchConflicts: func(context.Context, domain.BatchGetConflictsQuery) ([]domain.ConflictWithMembers, error) {
		return []domain.ConflictWithMembers{{Conflict: newerConflict, Members: newerMembers}, {Conflict: olderConflict, Members: olderMembers}}, nil
	}}
	service := mustService(t, testDependencies(repo))
	ctx := context.Background()
	if _, err := service.GetRelations(ctx, GetRelationsQuery{WorkspaceID: testID(1), Limit: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetConflicts(ctx, GetConflictsQuery{WorkspaceID: testID(1), Limit: 2}); err != nil {
		t.Fatal(err)
	}
	repo.batchRelations = func(context.Context, domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error) {
		return []domain.RelationWithEvidence{{Relation: olderRelation}, {Relation: newerRelation}}, nil
	}
	if _, err := service.GetRelations(ctx, GetRelationsQuery{WorkspaceID: testID(1), Limit: 2}); errorCode(err) != errorCodeResultConsistency {
		t.Fatalf("unstable relation order accepted: %v", err)
	}
	newerConflict.WorkspaceID = testID(99)
	repo.batchConflicts = func(context.Context, domain.BatchGetConflictsQuery) ([]domain.ConflictWithMembers, error) {
		return []domain.ConflictWithMembers{{Conflict: newerConflict, Members: newerMembers}}, nil
	}
	if _, err := service.GetConflicts(ctx, GetConflictsQuery{WorkspaceID: testID(1), Limit: 2}); errorCode(err) != errorCodeResultConsistency {
		t.Fatalf("cross workspace conflict accepted: %v", err)
	}
}

func TestMutationCommandsRejectZeroVersion(t *testing.T) {
	service := mustService(t, testDependencies(&fakeRepository{}))
	ctx := context.Background()
	workspace, id := testID(1), testID(2)
	checks := []func() error{func() error {
		_, err := service.TransitionClaim(ctx, TransitionClaimCommand{WorkspaceID: workspace, ClaimID: id, Status: domain.ClaimStatusInvalid, IdempotencyKey: "x"})
		return err
	}, func() error {
		_, err := service.TransitionRelation(ctx, TransitionRelationCommand{WorkspaceID: workspace, RelationID: id, Status: domain.RelationStatusRejected, IdempotencyKey: "x"})
		return err
	}, func() error {
		_, err := service.TransitionConflict(ctx, TransitionConflictCommand{WorkspaceID: workspace, ConflictID: id, Status: domain.ConflictStatusInvestigating, IdempotencyKey: "x"})
		return err
	}}
	for index, check := range checks {
		if err := check(); errorCode(err) != errorCodeRequestInvalid {
			t.Fatalf("check %d code=%s err=%v", index, errorCode(err), err)
		}
	}
}

func TestBatchQueriesEnforceLimitScopeStableOrderAndAggregateValidity(t *testing.T) {
	newer := testClaim(t, testID(61), domain.ClaimStatusSuggested, 1)
	older := testClaim(t, testID(62), domain.ClaimStatusSuggested, 1)
	newer.UpdatedAt = testTime().Add(time.Hour)
	older.UpdatedAt = testTime()
	repo := &fakeRepository{batchClaims: func(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
		return []domain.ClaimWithSources{{Claim: newer}, {Claim: older}}, nil
	}}
	service := mustService(t, testDependencies(repo))
	query := GetClaimsQuery{WorkspaceID: testID(1), Limit: 2}
	if _, err := service.GetClaims(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	query.Limit = MaxBatchSize + 1
	if _, err := service.GetClaims(context.Background(), query); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("limit accepted: %v", err)
	}
	query.Limit = 2
	repo.batchClaims = func(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
		return []domain.ClaimWithSources{{Claim: older}, {Claim: newer}}, nil
	}
	if _, err := service.GetClaims(context.Background(), query); errorCode(err) != errorCodeResultConsistency {
		t.Fatalf("unstable order accepted: %v", err)
	}
	bad := newer
	bad.WorkspaceID = testID(99)
	repo.batchClaims = func(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
		return []domain.ClaimWithSources{{Claim: bad}}, nil
	}
	if _, err := service.GetClaims(context.Background(), query); errorCode(err) != errorCodeResultConsistency {
		t.Fatalf("cross workspace accepted: %v", err)
	}
}

func TestServiceRejectsNilContextAndCorruptReadback(t *testing.T) {
	repo := &fakeRepository{createTopic: func(_ context.Context, record domain.CreateTopicRecord) (domain.TopicResult, error) {
		record.Topic.WorkspaceID = testID(99)
		return domain.TopicResult{Topic: record.Topic}, nil
	}}
	service := mustService(t, testDependencies(repo))
	command := CreateTopicCommand{WorkspaceID: testID(1), Name: "Go", IdempotencyKey: "topic"}
	if _, err := service.CreateTopic(nil, command); errorCode(err) != errorCodeRequestInvalid {
		t.Fatalf("nil context code=%s", errorCode(err))
	}
	if _, err := service.CreateTopic(context.Background(), command); errorCode(err) != errorCodeResultConsistency {
		t.Fatalf("corrupt readback code=%s err=%v", errorCode(err), err)
	}
}

func TestReceiptFirstReplayBypassesPortsIDAndClockFailures(t *testing.T) {
	t.Run("confirm claim", func(t *testing.T) {
		claim := testClaim(t, testID(201), domain.ClaimStatusConfirmed, 2)
		source := domain.ClaimSource{ID: testID(202), WorkspaceID: testID(1), ClaimID: claim.ID, Provenance: testProvenance(10), SupportType: domain.ClaimSupportSupports, Reason: "原文支持", CreatedAt: testTime()}
		source.EvidenceHash = domain.ComputeClaimSourceEvidenceHash(source, claim.Applicability)
		port := &fakeProvenanceVerifier{err: errors.New("unavailable")}
		repo := &fakeRepository{lookupReceipt: func(_ context.Context, q domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
			return receiptLookup(q, claim.ID, 2), nil
		}, batchClaims: func(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
			return []domain.ClaimWithSources{{Claim: claim, Sources: []domain.ClaimSource{source}}}, nil
		}, confirmClaim: func(context.Context, domain.ConfirmClaimRecord) (domain.ClaimResult, error) {
			t.Fatal("write called on replay")
			return domain.ClaimResult{}, nil
		}}
		dependencies := failingReplayDependencies(repo)
		dependencies.Provenance = port
		service := mustService(t, dependencies)
		result, err := service.ConfirmClaim(context.Background(), ConfirmClaimCommand{WorkspaceID: testID(1), ClaimID: claim.ID, ExpectedVersion: 1, Source: ClaimSourceInput{Provenance: source.Provenance, SupportType: domain.ClaimSupportSupports, Reason: source.Reason}, IdempotencyKey: "claim-replay"})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Replayed || len(port.refs) != 0 {
			t.Fatalf("result=%#v port_calls=%d", result, len(port.refs))
		}
	})
	t.Run("suggest relation", func(t *testing.T) {
		relation := testRelation(testID(203), domain.RelationStatusSuggested, 1)
		evidence := testRelationEvidence(t, testID(204), relation.ID, testProvenance(11), nil)
		relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint([]domain.RelationEvidence{evidence})
		port := &fakeProvenanceVerifier{err: errors.New("unavailable")}
		repo := &fakeRepository{lookupReceipt: func(_ context.Context, q domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
			return receiptLookup(q, relation.ID, 1), nil
		}, batchRelations: func(context.Context, domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error) {
			return []domain.RelationWithEvidence{{Relation: relation, Evidence: []domain.RelationEvidence{evidence}}}, nil
		}, suggestRelation: func(context.Context, domain.SuggestRelationRecord) (domain.RelationResult, error) {
			t.Fatal("write called on replay")
			return domain.RelationResult{}, nil
		}}
		dependencies := failingReplayDependencies(repo)
		dependencies.Provenance = port
		service := mustService(t, dependencies)
		result, err := service.SuggestRelation(context.Background(), SuggestRelationCommand{WorkspaceID: testID(1), Source: relation.Source, Target: relation.Target, Type: relation.Type, Evidence: []RelationEvidenceInput{{Provenance: evidence.Provenance, Reason: evidence.Reason, Applicability: evidence.Applicability.CanonicalJSON}}, IdempotencyKey: "relation-replay"})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Replayed || len(port.refs) != 0 {
			t.Fatalf("result=%#v port_calls=%d", result, len(port.refs))
		}
	})
	t.Run("confirm relation", func(t *testing.T) {
		confirmation := domain.Confirmation{Method: domain.ConfirmationUserApproval, Reference: "approval:replay"}
		relation := testRelation(testID(205), domain.RelationStatusConfirmed, 2)
		relation.Confirmation = &confirmation
		evidence := testRelationEvidence(t, testID(206), relation.ID, testProvenance(12), &confirmation)
		relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint([]domain.RelationEvidence{evidence})
		provenance := &fakeProvenanceVerifier{err: errors.New("unavailable")}
		approval := &fakeConfirmationVerifier{err: errors.New("expired")}
		repo := &fakeRepository{lookupReceipt: func(_ context.Context, q domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
			return receiptLookup(q, relation.ID, 2), nil
		}, batchRelations: func(context.Context, domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error) {
			return []domain.RelationWithEvidence{{Relation: relation, Evidence: []domain.RelationEvidence{evidence}}}, nil
		}, confirmRelation: func(context.Context, domain.ConfirmRelationRecord) (domain.RelationResult, error) {
			t.Fatal("write called on replay")
			return domain.RelationResult{}, nil
		}}
		dependencies := failingReplayDependencies(repo)
		dependencies.Provenance = provenance
		dependencies.Confirmation = approval
		service := mustService(t, dependencies)
		result, err := service.ConfirmRelation(context.Background(), ConfirmRelationCommand{WorkspaceID: testID(1), RelationID: relation.ID, ExpectedVersion: 1, Evidence: RelationEvidenceInput{Provenance: evidence.Provenance, Reason: evidence.Reason, Applicability: evidence.Applicability.CanonicalJSON}, Confirmation: confirmation, IdempotencyKey: "confirm-replay"})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Replayed || len(provenance.refs) != 0 || len(approval.values) != 0 {
			t.Fatalf("result=%#v calls=%d/%d", result, len(provenance.refs), len(approval.values))
		}
	})
	t.Run("resuggest relation", func(t *testing.T) {
		relation := testRelation(testID(207), domain.RelationStatusSuggested, 2)
		evidence := testRelationEvidence(t, testID(208), relation.ID, testProvenance(14), nil)
		relation.EvidenceFingerprint = domain.ComputeRelationEvidenceFingerprint([]domain.RelationEvidence{evidence})
		port := &fakeProvenanceVerifier{err: errors.New("unavailable")}
		repo := &fakeRepository{lookupReceipt: func(_ context.Context, q domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
			return receiptLookup(q, relation.ID, 2), nil
		}, batchRelations: func(context.Context, domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error) {
			return []domain.RelationWithEvidence{{Relation: relation, Evidence: []domain.RelationEvidence{evidence}}}, nil
		}, transitionRelation: func(context.Context, domain.TransitionRelationRecord) (domain.RelationResult, error) {
			t.Fatal("write called on replay")
			return domain.RelationResult{}, nil
		}}
		dependencies := failingReplayDependencies(repo)
		dependencies.Provenance = port
		service := mustService(t, dependencies)
		input := RelationEvidenceInput{Provenance: evidence.Provenance, Reason: evidence.Reason, Applicability: evidence.Applicability.CanonicalJSON}
		result, err := service.TransitionRelation(context.Background(), TransitionRelationCommand{WorkspaceID: testID(1), RelationID: relation.ID, ExpectedVersion: 1, Status: domain.RelationStatusSuggested, Evidence: &input, IdempotencyKey: "resuggest-replay"})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Replayed || len(port.refs) != 0 {
			t.Fatalf("result=%#v calls=%d", result, len(port.refs))
		}
	})
}

func TestReceiptLookupWrongHashCommandOrTargetFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*domain.CommandReceipt)
	}{{name: "hash", mutate: func(r *domain.CommandReceipt) { r.RequestHash = strings.Repeat("a", 64) }}, {name: "command", mutate: func(r *domain.CommandReceipt) {
		r.CommandType = domain.CommandSuggestClaim
		r.AggregateType = domain.AggregateClaim
	}}} {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeRepository{lookupReceipt: func(_ context.Context, q domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
				lookup := receiptLookup(q, testID(210), 1)
				test.mutate(&lookup.Receipt)
				return lookup, nil
			}}
			service := mustService(t, failingReplayDependencies(repo))
			_, err := service.CreateTopic(context.Background(), CreateTopicCommand{WorkspaceID: testID(1), Name: "Go", IdempotencyKey: "wrong-binding"})
			if errorCode(err) != domain.ErrorCodeIdempotencyConflict {
				t.Fatalf("code=%s err=%v", errorCode(err), err)
			}
		})
	}
	claim := testClaim(t, testID(211), domain.ClaimStatusConfirmed, 2)
	source := domain.ClaimSource{ID: testID(212), WorkspaceID: testID(1), ClaimID: claim.ID, Provenance: testProvenance(13), SupportType: domain.ClaimSupportSupports, Reason: "依据", CreatedAt: testTime()}
	source.EvidenceHash = domain.ComputeClaimSourceEvidenceHash(source, claim.Applicability)
	repo := &fakeRepository{lookupReceipt: func(_ context.Context, q domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
		return receiptLookup(q, testID(299), 2), nil
	}}
	service := mustService(t, failingReplayDependencies(repo))
	_, err := service.ConfirmClaim(context.Background(), ConfirmClaimCommand{WorkspaceID: testID(1), ClaimID: claim.ID, ExpectedVersion: 1, Source: ClaimSourceInput{Provenance: source.Provenance, SupportType: domain.ClaimSupportSupports, Reason: source.Reason}, IdempotencyKey: "wrong-target"})
	if errorCode(err) != errorCodeResultConsistency {
		t.Fatalf("target code=%s err=%v", errorCode(err), err)
	}
}

type fakeRepository struct {
	lookupReceipt      func(context.Context, domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error)
	createTopic        func(context.Context, domain.CreateTopicRecord) (domain.TopicResult, error)
	getTopic           func(context.Context, foundation.ID, foundation.ID) (domain.Topic, error)
	suggestClaim       func(context.Context, domain.SuggestClaimRecord) (domain.ClaimResult, error)
	confirmClaim       func(context.Context, domain.ConfirmClaimRecord) (domain.ClaimResult, error)
	transitionClaim    func(context.Context, domain.TransitionClaimRecord) (domain.ClaimResult, error)
	suggestRelation    func(context.Context, domain.SuggestRelationRecord) (domain.RelationResult, error)
	confirmRelation    func(context.Context, domain.ConfirmRelationRecord) (domain.RelationResult, error)
	transitionRelation func(context.Context, domain.TransitionRelationRecord) (domain.RelationResult, error)
	openConflict       func(context.Context, domain.OpenConflictRecord) (domain.ConflictResult, error)
	transitionConflict func(context.Context, domain.TransitionConflictRecord) (domain.ConflictResult, error)
	batchClaims        func(context.Context, domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error)
	batchRelations     func(context.Context, domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error)
	batchConflicts     func(context.Context, domain.BatchGetConflictsQuery) ([]domain.ConflictWithMembers, error)
	batchEligibility   func(context.Context, domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error)
}

func (f *fakeRepository) LookupCommandReceipt(c context.Context, q domain.CommandReceiptQuery) (domain.CommandReceiptLookup, error) {
	if f.lookupReceipt == nil {
		return domain.CommandReceiptLookup{}, nil
	}
	return f.lookupReceipt(c, q)
}

func (f *fakeRepository) CreateTopic(c context.Context, r domain.CreateTopicRecord) (domain.TopicResult, error) {
	if f.createTopic == nil {
		return domain.TopicResult{}, unexpected()
	}
	return f.createTopic(c, r)
}
func (f *fakeRepository) GetTopic(c context.Context, w, i foundation.ID) (domain.Topic, error) {
	if f.getTopic == nil {
		return domain.Topic{}, unexpected()
	}
	return f.getTopic(c, w, i)
}
func (f *fakeRepository) SuggestClaim(c context.Context, r domain.SuggestClaimRecord) (domain.ClaimResult, error) {
	if f.suggestClaim == nil {
		return domain.ClaimResult{}, unexpected()
	}
	return f.suggestClaim(c, r)
}
func (f *fakeRepository) ConfirmClaim(c context.Context, r domain.ConfirmClaimRecord) (domain.ClaimResult, error) {
	if f.confirmClaim == nil {
		return domain.ClaimResult{}, unexpected()
	}
	return f.confirmClaim(c, r)
}
func (f *fakeRepository) TransitionClaim(c context.Context, r domain.TransitionClaimRecord) (domain.ClaimResult, error) {
	if f.transitionClaim == nil {
		return domain.ClaimResult{}, unexpected()
	}
	return f.transitionClaim(c, r)
}
func (f *fakeRepository) SuggestRelation(c context.Context, r domain.SuggestRelationRecord) (domain.RelationResult, error) {
	if f.suggestRelation == nil {
		return domain.RelationResult{}, unexpected()
	}
	return f.suggestRelation(c, r)
}
func (f *fakeRepository) ConfirmRelation(c context.Context, r domain.ConfirmRelationRecord) (domain.RelationResult, error) {
	if f.confirmRelation == nil {
		return domain.RelationResult{}, unexpected()
	}
	return f.confirmRelation(c, r)
}
func (f *fakeRepository) TransitionRelation(c context.Context, r domain.TransitionRelationRecord) (domain.RelationResult, error) {
	if f.transitionRelation == nil {
		return domain.RelationResult{}, unexpected()
	}
	return f.transitionRelation(c, r)
}
func (f *fakeRepository) OpenConflict(c context.Context, r domain.OpenConflictRecord) (domain.ConflictResult, error) {
	if f.openConflict == nil {
		return domain.ConflictResult{}, unexpected()
	}
	return f.openConflict(c, r)
}
func (f *fakeRepository) TransitionConflict(c context.Context, r domain.TransitionConflictRecord) (domain.ConflictResult, error) {
	if f.transitionConflict == nil {
		return domain.ConflictResult{}, unexpected()
	}
	return f.transitionConflict(c, r)
}
func (f *fakeRepository) BatchGetClaims(c context.Context, q domain.BatchGetClaimsQuery) ([]domain.ClaimWithSources, error) {
	if f.batchClaims == nil {
		return nil, unexpected()
	}
	return f.batchClaims(c, q)
}
func (f *fakeRepository) BatchGetRelations(c context.Context, q domain.BatchGetRelationsQuery) ([]domain.RelationWithEvidence, error) {
	if f.batchRelations == nil {
		return nil, unexpected()
	}
	return f.batchRelations(c, q)
}
func (f *fakeRepository) BatchGetConflicts(c context.Context, q domain.BatchGetConflictsQuery) ([]domain.ConflictWithMembers, error) {
	if f.batchConflicts == nil {
		return nil, unexpected()
	}
	return f.batchConflicts(c, q)
}
func (f *fakeRepository) BatchCheckEvidenceEligibility(c context.Context, q domain.EvidenceEligibilityQuery) ([]domain.ProvenanceEligibility, error) {
	if f.batchEligibility == nil {
		return nil, unexpected()
	}
	return f.batchEligibility(c, q)
}

type fakeProvenanceVerifier struct {
	refs []domain.ProvenanceRef
	err  error
}

func (f *fakeProvenanceVerifier) Verify(_ context.Context, ref domain.ProvenanceRef) error {
	f.refs = append(f.refs, ref)
	return f.err
}

type fakeConfirmationVerifier struct {
	values []domain.Confirmation
	err    error
}

func (f *fakeConfirmationVerifier) Verify(_ context.Context, value domain.Confirmation) error {
	f.values = append(f.values, value)
	return f.err
}

type sequenceIDGenerator struct {
	ids   []foundation.ID
	index int
}

func (g *sequenceIDGenerator) New() (foundation.ID, error) {
	if g.index >= len(g.ids) {
		return "", errors.New("id sequence exhausted")
	}
	id := g.ids[g.index]
	g.index++
	return id, nil
}

func testDependencies(repository domain.Repository) Dependencies {
	return Dependencies{Repository: repository, Provenance: &fakeProvenanceVerifier{}, Confirmation: &fakeConfirmationVerifier{}, IDs: &sequenceIDGenerator{ids: []foundation.ID{testID(900), testID(901), testID(902), testID(903)}}, Clock: foundation.FixedClock{Value: testTime()}}
}
func mustService(t *testing.T, dependencies Dependencies) *Service {
	t.Helper()
	service, err := NewService(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return service
}
func testTime() time.Time { return time.Date(2026, 7, 19, 8, 0, 0, 0, time.UTC) }
func testID(seed int) foundation.ID {
	return foundation.ID("00000000-0000-4000-8000-" + strings.Repeat("0", 12-len(strconv.Itoa(seed))) + strconv.Itoa(seed))
}
func testProvenance(seed int) domain.ProvenanceRef {
	return domain.ProvenanceRef{WorkspaceID: testID(1), SourceVersionID: testID(700 + seed*2), SourceSpanID: testID(701 + seed*2)}
}
func testClaim(t *testing.T, id foundation.ID, status domain.ClaimStatus, version int64) domain.Claim {
	t.Helper()
	app, err := domain.ParseApplicability(json.RawMessage(`{"scope":"all"}`))
	if err != nil {
		t.Fatal(err)
	}
	claim := domain.Claim{ID: id, WorkspaceID: testID(1), Statement: "Go 很快", NormalizedStatement: "Go 很快", Applicability: app, Status: status, ConfidenceFactors: json.RawMessage(`{}`), Version: version, CreatedAt: testTime(), UpdatedAt: testTime()}
	claim.Fingerprint = domain.ComputeClaimFingerprint(claim.WorkspaceID, claim.NormalizedStatement, claim.Applicability)
	return claim
}
func testRelation(id foundation.ID, status domain.RelationStatus, version int64) domain.Relation {
	relation := domain.Relation{ID: id, WorkspaceID: testID(1), Source: domain.NodeRef{Type: domain.NodeTypeClaim, ID: testID(801)}, Target: domain.NodeRef{Type: domain.NodeTypeClaim, ID: testID(802)}, Type: domain.RelationSupports, Status: status, Version: version, CreatedAt: testTime(), UpdatedAt: testTime()}
	relation.Fingerprint = domain.ComputeRelationFingerprint(relation.WorkspaceID, relation.Type, relation.Source, relation.Target)
	return relation
}
func testRelationEvidence(t *testing.T, id, relationID foundation.ID, provenance domain.ProvenanceRef, confirmation *domain.Confirmation) domain.RelationEvidence {
	t.Helper()
	app, err := domain.ParseApplicability(json.RawMessage(`{"scope":"all"}`))
	if err != nil {
		t.Fatal(err)
	}
	value := domain.RelationEvidence{ID: id, WorkspaceID: testID(1), RelationID: relationID, Provenance: provenance, Reason: "原文支持关系", Applicability: app, Confirmation: cloneConfirmation(confirmation), CreatedAt: testTime()}
	value.EvidenceHash = domain.ComputeRelationEvidenceHash(value)
	return value
}
func receiptLookup(query domain.CommandReceiptQuery, aggregateID foundation.ID, version int64) domain.CommandReceiptLookup {
	return domain.CommandReceiptLookup{Found: true, Receipt: domain.CommandReceipt{WorkspaceID: query.WorkspaceID, AggregateID: aggregateID, IdempotencyKey: query.IdempotencyKey, RequestHash: query.RequestHash, CommandType: query.CommandType, AggregateType: query.AggregateType, AggregateVersion: version}}
}
func failingReplayDependencies(repository domain.Repository) Dependencies {
	return Dependencies{Repository: repository, Provenance: &fakeProvenanceVerifier{err: errors.New("unavailable")}, Confirmation: &fakeConfirmationVerifier{err: errors.New("unavailable")}, IDs: &sequenceIDGenerator{}, Clock: foundation.FixedClock{}}
}
func testConflict(t *testing.T, id foundation.ID, status domain.ConflictStatus, version int64) (domain.Conflict, []domain.ConflictMember) {
	t.Helper()
	app, err := domain.ParseApplicability(json.RawMessage(`{"scope":"all"}`))
	if err != nil {
		t.Fatal(err)
	}
	members := []domain.ConflictMember{{ConflictID: id, WorkspaceID: testID(1), ClaimID: testID(811), Applicability: app, ApplicabilityHash: app.Hash, PositionSummary: "正方", CreatedAt: testTime()}, {ConflictID: id, WorkspaceID: testID(1), ClaimID: testID(812), Applicability: app, ApplicabilityHash: app.Hash, PositionSummary: "反方", CreatedAt: testTime()}}
	conflict := domain.Conflict{ID: id, WorkspaceID: testID(1), Status: status, Severity: domain.ConflictSeverityHigh, Summary: "结论冲突", ApplicabilityAssessment: domain.ApplicabilityAssessmentExact, Version: version, CreatedAt: testTime(), UpdatedAt: testTime()}
	conflict.Fingerprint = domain.ComputeConflictFingerprint(conflict.WorkspaceID, conflict.ApplicabilityAssessment, members)
	return conflict, members
}
func errorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
func unexpected() error { return errors.New("unexpected repository call") }
