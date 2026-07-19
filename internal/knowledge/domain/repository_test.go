package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestComputeRequestHashIsStableAndRejectsUnencodablePayload(t *testing.T) {
	payload := struct {
		WorkspaceID foundation.ID `json:"workspace_id"`
		Name        string        `json:"name"`
	}{WorkspaceID: uuid(2), Name: "Topic"}
	first, err := ComputeRequestHash("topic.create", payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ComputeRequestHash("topic.create", payload)
	if err != nil || first != second || !validSHA256(first) {
		t.Fatalf("request hash is not stable: first=%q second=%q err=%v", first, second, err)
	}
	other, err := ComputeRequestHash("topic.rename", payload)
	if err != nil || other == first {
		t.Fatal("command type must bind request hash")
	}
	if _, err := ComputeRequestHash("Topic Create", payload); err == nil {
		t.Fatal("non-canonical command type must be rejected")
	}
	if _, err := ComputeRequestHash("topic.create", math.NaN()); err == nil {
		t.Fatal("unencodable payload must be rejected")
	}
}

func TestValidateCommandMetadataAndBatchQueryAreBounded(t *testing.T) {
	hash := strings.Repeat("a", 64)
	if err := ValidateCommandMetadata(uuid(2), "claim:confirm:42", hash); err != nil {
		t.Fatalf("valid command metadata rejected: %v", err)
	}
	for _, test := range []struct {
		workspace foundation.ID
		key, hash string
	}{
		{workspace: "bad", key: "key", hash: hash},
		{workspace: uuid(2), key: "", hash: hash},
		{workspace: uuid(2), key: " key", hash: hash},
		{workspace: uuid(2), key: strings.Repeat("k", 129), hash: hash},
		{workspace: uuid(2), key: "key", hash: "bad"},
	} {
		var classified *foundation.Error
		if err := ValidateCommandMetadata(test.workspace, test.key, test.hash); !errors.As(err, &classified) || classified.Code != ErrorCodeRequestInvalid {
			t.Fatalf("invalid metadata accepted: %#v err=%v", test, err)
		}
	}
	if err := ValidateBatchQuery(uuid(2), []foundation.ID{uuid(3), uuid(4)}, MaxBatchLimit); err != nil {
		t.Fatalf("valid batch query rejected: %v", err)
	}
	if err := ValidateBatchQuery(uuid(2), []foundation.ID{uuid(3), uuid(3)}, 10); err == nil {
		t.Fatal("duplicate ids must be rejected")
	}
	if err := ValidateBatchQuery(uuid(2), nil, MaxBatchLimit+1); err == nil {
		t.Fatal("unbounded limit must be rejected")
	}
}

func TestCommandReceiptContractsRejectUnknownOrDamagedBindings(t *testing.T) {
	query := CommandReceiptQuery{WorkspaceID: uuid(2), IdempotencyKey: "claim:confirm:42", RequestHash: strings.Repeat("a", 64), CommandType: CommandConfirmClaim, AggregateType: AggregateClaim}
	if err := ValidateCommandReceiptQuery(query); err != nil {
		t.Fatalf("valid query rejected: %v", err)
	}
	receipt := CommandReceipt{WorkspaceID: query.WorkspaceID, AggregateID: uuid(3), IdempotencyKey: query.IdempotencyKey, RequestHash: query.RequestHash, CommandType: query.CommandType, AggregateType: query.AggregateType, AggregateVersion: 2}
	if err := ValidateCommandReceipt(receipt); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	query.CommandType = CommandType("claim.unknown")
	if err := ValidateCommandReceiptQuery(query); err == nil {
		t.Fatal("unknown command type accepted")
	}
	receipt.AggregateVersion = 0
	if err := ValidateCommandReceipt(receipt); err == nil {
		t.Fatal("zero receipt version accepted")
	}
}

func TestRelationEvidenceFingerprintIsOrderIndependentAndMultiplicitySensitive(t *testing.T) {
	relation := validRelation(t)
	confirmation := Confirmation{Method: ConfirmationUserApproval, Reference: "approval:42"}
	first := validRelationEvidence(t, relation, confirmation)
	second := first
	second.ID = uuid(90)
	second.Reason = "另一处原文支持该关系"
	second.EvidenceHash = ComputeRelationEvidenceHash(second)
	forward := ComputeRelationEvidenceFingerprint([]RelationEvidence{first, second})
	reverse := ComputeRelationEvidenceFingerprint([]RelationEvidence{second, first})
	if forward != reverse || !validSHA256(forward) {
		t.Fatalf("evidence set fingerprint must be order independent: %q %q", forward, reverse)
	}
	if forward == ComputeRelationEvidenceFingerprint([]RelationEvidence{first}) {
		t.Fatal("evidence set fingerprint must bind all members")
	}
}

func TestRelationEvidenceSemanticHashIgnoresPersistenceIdentity(t *testing.T) {
	relation := validRelation(t)
	confirmation := Confirmation{Method: ConfirmationUserApproval, Reference: "approval:42"}
	first := validRelationEvidence(t, relation, confirmation)
	second := first
	second.ID = uuid(90)
	second.RelationID = uuid(91)
	second.CreatedAt = second.CreatedAt.Add(time.Hour)
	second.EvidenceHash = ComputeRelationEvidenceHash(second)

	if ComputeRelationEvidenceSemanticHash(first) != ComputeRelationEvidenceSemanticHash(second) {
		t.Fatal("semantic hash must ignore record, owner, and timestamp identity")
	}
	if first.EvidenceHash == second.EvidenceHash {
		t.Fatal("persistent evidence hash must still bind the owner relation")
	}
	second.Reason = "不同业务语义"
	if ComputeRelationEvidenceSemanticHash(first) == ComputeRelationEvidenceSemanticHash(second) {
		t.Fatal("semantic hash must change with evidence payload")
	}
}
