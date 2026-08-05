package changecontrol

import (
	"context"
	"errors"
	"strings"
	"testing"

	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/documenthistory/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	gatewayWorkspaceID  = foundation.ID("51000000-0000-4000-8000-000000000001")
	gatewayDocumentID   = foundation.ID("51000000-0000-4000-8000-000000000002")
	gatewayProposalID   = foundation.ID("51000000-0000-4000-8000-000000000003")
	gatewayRevisionID   = foundation.ID("51000000-0000-4000-8000-000000000004")
	gatewayTargetCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gatewayExpectedHead = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestGatewayCreatesAndReplaysCompleteRestoreBinding(t *testing.T) {
	record := gatewayRestoreRecord()
	proposal := gatewayRestoreProposal(t, record, changecontroldomain.StatusCompleted)
	creator := &gatewayCreatorStub{proposal: proposal, replayed: true}
	lookup := &gatewayLookupStub{proposal: proposal, found: true}
	gateway, err := NewGateway(creator, lookup)
	if err != nil {
		t.Fatal(err)
	}

	created, err := gateway.CreateRestoreDocumentProposal(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Replayed || created.Status != string(changecontroldomain.StatusCompleted) || created.TargetCommit != record.TargetCommit || created.PreviewHash != record.PreviewHash {
		t.Fatalf("created receipt=%+v", created)
	}
	if creator.command.WorkspaceID != record.WorkspaceID || creator.command.TargetPath != record.TargetPath || creator.command.Content != record.TargetContent ||
		creator.command.Restore.DocumentID != record.DocumentID || creator.command.Restore.CurrentContentHash != record.CurrentContentHash ||
		creator.command.Restore.TargetContentHash != record.TargetContentHash || creator.command.Restore.SchemaVersion != changecontroldomain.RestoreDocumentSchemaVersion {
		t.Fatalf("creator command=%+v", creator.command)
	}
	if creator.command.EvidenceSummary != restoreEvidence || creator.command.Risk != restoreRisk || creator.command.RollbackPlan != restoreRollback {
		t.Fatalf("server-owned restore governance text drifted: %+v", creator.command)
	}

	replayed, found, err := gateway.FindRestoreProposal(context.Background(), record.WorkspaceID, record.IdempotencyKey)
	if err != nil || !found || replayed.Replayed || replayed.ProposalID != proposal.ID || replayed.Status != string(changecontroldomain.StatusCompleted) {
		t.Fatalf("lookup receipt=%+v found=%v err=%v", replayed, found, err)
	}
}

func TestGatewayPreservesNewCreateAndDependencyOutcomes(t *testing.T) {
	record := gatewayRestoreRecord()
	proposal := gatewayRestoreProposal(t, record, changecontroldomain.StatusReady)
	creator := &gatewayCreatorStub{proposal: proposal}
	lookup := &gatewayLookupStub{}
	gateway, err := NewGateway(creator, lookup)
	if err != nil {
		t.Fatal(err)
	}
	created, err := gateway.CreateRestoreDocumentProposal(context.Background(), record)
	if err != nil || created.Replayed {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	creator.err = errors.New("creator unavailable")
	if _, err := gateway.CreateRestoreDocumentProposal(context.Background(), record); !errors.Is(err, creator.err) {
		t.Fatalf("creator error=%v", err)
	}
	creator.err = nil
	if receipt, found, err := gateway.FindRestoreProposal(context.Background(), record.WorkspaceID, record.IdempotencyKey); err != nil || found || receipt != (application.RestoreProposalReceipt{}) {
		t.Fatalf("missing receipt=%+v found=%v err=%v", receipt, found, err)
	}
	lookup.err = errors.New("lookup unavailable")
	if _, _, err := gateway.FindRestoreProposal(context.Background(), record.WorkspaceID, record.IdempotencyKey); !errors.Is(err, lookup.err) {
		t.Fatalf("lookup error=%v", err)
	}
}

func TestGatewayRejectsMissingDependenciesAndMisboundReplay(t *testing.T) {
	creator := &gatewayCreatorStub{}
	lookup := &gatewayLookupStub{}
	if gateway, err := NewGateway(nil, lookup); err == nil || gateway != nil {
		t.Fatalf("gateway=%#v err=%v", gateway, err)
	}
	if gateway, err := NewGateway(creator, nil); err == nil || gateway != nil {
		t.Fatalf("gateway=%#v err=%v", gateway, err)
	}

	lookup.proposal = changecontroldomain.Proposal{Type: changecontroldomain.ProposalTypeFilePatch}
	lookup.found = true
	gateway, err := NewGateway(creator, lookup)
	if err != nil {
		t.Fatal(err)
	}
	_, found, err := gateway.FindRestoreProposal(context.Background(), gatewayWorkspaceID, "restore-key")
	var classified *foundation.Error
	if found || !errors.As(err, &classified) || classified.Code != application.ErrorCodeIdempotencyReuse {
		t.Fatalf("found=%v err=%v", found, err)
	}

	record := gatewayRestoreRecord()
	lookup.proposal = gatewayRestoreProposal(t, record, changecontroldomain.StatusReady)
	lookup.proposal.WorkspaceID = "51000000-0000-4000-8000-000000000099"
	_, found, err = gateway.FindRestoreProposal(context.Background(), gatewayWorkspaceID, "restore-key")
	classified = nil
	if found || !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation || classified.Code != application.ErrorCodeResultInvalid {
		t.Fatalf("malformed restore found=%v err=%v", found, err)
	}
}

type gatewayCreatorStub struct {
	command  changecontrolapp.CreateRestoreDocumentCommand
	proposal changecontroldomain.Proposal
	replayed bool
	err      error
}

func (stub *gatewayCreatorStub) CreateRestoreDocumentProposal(_ context.Context, command changecontrolapp.CreateRestoreDocumentCommand) (changecontrolapp.CreateResult, error) {
	stub.command = command
	if stub.err != nil {
		return changecontrolapp.CreateResult{}, stub.err
	}
	return changecontrolapp.CreateResult{Proposal: stub.proposal, Replayed: stub.replayed}, nil
}

type gatewayLookupStub struct {
	proposal changecontroldomain.Proposal
	found    bool
	err      error
}

func (stub *gatewayLookupStub) FindProposalByIdempotencyKey(context.Context, foundation.ID, string) (changecontroldomain.Proposal, bool, error) {
	return stub.proposal, stub.found, stub.err
}

func gatewayRestoreRecord() application.CreateRestoreProposalRecord {
	return application.CreateRestoreProposalRecord{
		WorkspaceID: gatewayWorkspaceID, DocumentID: gatewayDocumentID, TargetPath: "notes/java-ai.md",
		TargetCommit: gatewayTargetCommit, ExpectedHead: gatewayExpectedHead, ExpectedDocumentVersion: 7,
		PreviewHash: strings.Repeat("c", 64), CurrentContentHash: changecontroldomain.ComputeContentHash([]byte("current\n")),
		TargetContentHash: changecontroldomain.ComputeContentHash([]byte("target\n")), TargetContent: "target\n", IdempotencyKey: "restore-key",
	}
}

func gatewayRestoreProposal(t *testing.T, record application.CreateRestoreProposalRecord, status changecontroldomain.ProposalStatus) changecontroldomain.Proposal {
	t.Helper()
	restore := changecontroldomain.RestoreDocument{
		WorkspaceID: record.WorkspaceID, DocumentID: record.DocumentID, TargetCommit: record.TargetCommit,
		ExpectedHead: record.ExpectedHead, ExpectedDocumentVersion: record.ExpectedDocumentVersion,
		PreviewHash: record.PreviewHash, CurrentContentHash: record.CurrentContentHash,
		TargetContentHash: record.TargetContentHash, SchemaVersion: changecontroldomain.RestoreDocumentSchemaVersion,
	}
	changeHash, err := changecontroldomain.ComputeChangeHashForTarget(
		record.WorkspaceID, record.TargetPath, changecontroldomain.TargetModeReplace, record.CurrentContentHash, record.TargetContent,
	)
	if err != nil {
		t.Fatal(err)
	}
	return changecontroldomain.Proposal{
		ID: gatewayProposalID, WorkspaceID: record.WorkspaceID, Type: changecontroldomain.ProposalTypeRestoreDocument,
		RiskLevel: changecontroldomain.ProposalRiskLevelHigh, TargetPath: record.TargetPath,
		IdempotencyKey: record.IdempotencyKey, Status: status, Version: 7,
		Revision: changecontroldomain.Revision{
			ID: gatewayRevisionID, ProposalID: gatewayProposalID, RevisionNo: 1,
			TargetPath: record.TargetPath, TargetMode: changecontroldomain.TargetModeReplace,
			BaseHash: record.CurrentContentHash, Content: record.TargetContent,
			EvidenceSummary: restoreEvidence, Risk: restoreRisk, RollbackPlan: restoreRollback,
			ChangeHash: changeHash, RestoreDocument: &restore,
		},
	}
}
