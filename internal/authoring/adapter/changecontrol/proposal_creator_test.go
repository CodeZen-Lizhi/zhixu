package changecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	authoringdomain "github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestProposalCreatorMapsAndVerifiesFrozenRevision(t *testing.T) {
	request := proposalRequestFixture(t)
	service := &proposalServiceFake{}
	creator, err := NewProposalCreator(service, &proposalTargetReaderFake{hash: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := creator.CreatePublicationProposal(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProposalID != authoringAdapterID(4) || result.ProposalRevisionID != authoringAdapterID(5) ||
		result.TargetMode != request.TargetMode || result.BaseVersion != request.BaseVersion || !result.Replayed {
		t.Fatalf("result=%#v", result)
	}
	command := service.createOnlyCommand
	if command.WorkspaceID != request.WorkspaceID || command.TargetPath != request.TargetPath ||
		command.Content != request.Content || command.IdempotencyKey != request.IdempotencyKey ||
		command.RiskLevel != changecontroldomain.ProposalRiskLevelMedium || command.EvidenceSummary == "" ||
		command.Risk == "" || command.RollbackPlan == "" {
		t.Fatalf("command=%#v", command)
	}
}

func TestProposalCreatorRejectsReturnedBindingDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*changecontroldomain.Proposal)
	}{
		{name: "workspace", mutate: func(value *changecontroldomain.Proposal) { value.WorkspaceID = authoringAdapterID(9) }},
		{name: "path", mutate: func(value *changecontroldomain.Proposal) { value.Revision.TargetPath = "notes/other.md" }},
		{name: "mode", mutate: func(value *changecontroldomain.Proposal) {
			value.Revision.TargetMode = changecontroldomain.TargetModeReplace
		}},
		{name: "base", mutate: func(value *changecontroldomain.Proposal) { value.Revision.BaseHash = strings.Repeat("a", 64) }},
		{name: "content", mutate: func(value *changecontroldomain.Proposal) { value.Revision.Content = "# Other\n" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			creator, err := NewProposalCreator(&proposalServiceFake{mutate: test.mutate}, &proposalTargetReaderFake{hash: strings.Repeat("a", 64)})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := creator.CreatePublicationProposal(context.Background(), proposalRequestFixture(t)); err == nil {
				t.Fatal("expected returned proposal binding drift to fail closed")
			}
		})
	}
}

func TestProposalCreatorRejectsMissingDependencyAndPropagatesError(t *testing.T) {
	if _, err := NewProposalCreator(nil, &proposalTargetReaderFake{}); err == nil {
		t.Fatal("expected nil proposal service rejection")
	}
	cause := errors.New("proposal unavailable")
	creator, err := NewProposalCreator(&proposalServiceFake{err: cause}, &proposalTargetReaderFake{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.CreatePublicationProposal(context.Background(), proposalRequestFixture(t)); !errors.Is(err, cause) {
		t.Fatalf("error=%v", err)
	}
}

func TestProposalCreatorVerifiesReplaceBaselineBeforeProposal(t *testing.T) {
	request := proposalRequestFixture(t)
	request.TargetMode = authoringdomain.ProposalTargetReplace
	request.BaseVersion = strings.Repeat("a", 64)
	reader := &proposalTargetReaderFake{hash: strings.Repeat("b", 64)}
	service := &proposalServiceFake{}
	creator, err := NewProposalCreator(service, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.CreatePublicationProposal(context.Background(), request); err == nil {
		t.Fatal("expected replacement baseline drift to fail closed")
	}
	if reader.calls != 1 || service.command.WorkspaceID != "" {
		t.Fatalf("reader calls=%d proposal command=%#v", reader.calls, service.command)
	}
}

type proposalServiceFake struct {
	command           changecontrolapp.CreateCommand
	createOnlyCommand changecontrolapp.CreateCreateOnlyFileProposalCommand
	err               error
	mutate            func(*changecontroldomain.Proposal)
}

func (fake *proposalServiceFake) CreateProposal(_ context.Context, command changecontrolapp.CreateCommand) (changecontrolapp.CreateResult, error) {
	fake.command = command
	return fake.result(command.WorkspaceID, command.TargetPath, changecontroldomain.TargetModeReplace, command.BaseHash, command.Content)
}

func (fake *proposalServiceFake) CreateCreateOnlyFileProposal(_ context.Context, command changecontrolapp.CreateCreateOnlyFileProposalCommand) (changecontrolapp.CreateResult, error) {
	fake.createOnlyCommand = command
	baseVersion, err := changecontroldomain.ComputeAbsenceToken(command.WorkspaceID, command.TargetPath)
	if err != nil {
		return changecontrolapp.CreateResult{}, err
	}
	return fake.result(command.WorkspaceID, command.TargetPath, changecontroldomain.TargetModeCreateOnly, baseVersion, command.Content)
}

func (fake *proposalServiceFake) result(workspaceID foundation.ID, targetPath string, targetMode changecontroldomain.TargetMode, baseVersion, content string) (changecontrolapp.CreateResult, error) {
	if fake.err != nil {
		return changecontrolapp.CreateResult{}, fake.err
	}
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	proposal := changecontroldomain.Proposal{
		ID: authoringAdapterID(4), WorkspaceID: workspaceID, Type: changecontroldomain.ProposalTypeFilePatch,
		TargetPath: targetPath, Status: changecontroldomain.StatusReady,
		Revision: changecontroldomain.Revision{
			ID: authoringAdapterID(5), ProposalID: authoringAdapterID(4), RevisionNo: 1,
			TargetPath: targetPath, TargetMode: targetMode, BaseHash: baseVersion,
			Content: content, CreatedAt: now,
		},
	}
	if fake.mutate != nil {
		fake.mutate(&proposal)
	}
	return changecontrolapp.CreateResult{Proposal: proposal, Replayed: true}, nil
}

type proposalTargetReaderFake struct {
	hash  string
	err   error
	calls int
}

func (fake *proposalTargetReaderFake) CurrentHash(context.Context, foundation.ID, string) (string, error) {
	fake.calls++
	return fake.hash, fake.err
}

func proposalRequestFixture(t *testing.T) authoringapp.PublicationProposal {
	t.Helper()
	workspaceID := authoringAdapterID(1)
	targetPath := "notes/java-ai.md"
	token, err := authoringdomain.ComputeAbsenceToken(workspaceID, targetPath)
	if err != nil {
		t.Fatal(err)
	}
	content := "# Java AI\n"
	return authoringapp.PublicationProposal{
		WorkspaceID: workspaceID, TargetPath: targetPath, TargetMode: authoringdomain.ProposalTargetCreateOnly,
		BaseVersion: token, Content: content, ContentHash: authoringdomain.ComputeContentHash(content),
		IdempotencyKey: "authoring-publication/v1:" + strings.Repeat("b", 64),
	}
}

func authoringAdapterID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("00000000-0000-4000-8000-%012d", value))
}
