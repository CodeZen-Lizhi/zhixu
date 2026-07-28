package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

func TestNewBridgeFailsClosedForNilCommander(t *testing.T) {
	bridge, err := NewBridge(nil)
	if err == nil || bridge != nil {
		t.Fatalf("bridge=%#v err=%v", bridge, err)
	}
	var typedNil *commanderFake
	bridge, err = NewBridge(typedNil)
	if err == nil || bridge != nil {
		t.Fatalf("typed nil bridge=%#v err=%v", bridge, err)
	}
}

func TestBridgeCreateDraftRunsArtifactLifecycleAndReturnsExactDraftBinding(t *testing.T) {
	commands := newCommanderFake()
	bridge, err := NewBridge(commands)
	if err != nil {
		t.Fatal(err)
	}
	request := bridgeDraftRequest()
	result, err := bridge.CreateDraft(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	if len(commands.plans) != 1 {
		t.Fatalf("plan commands=%+v", commands.plans)
	}
	plan := commands.plans[0]
	if plan.WorkspaceID != request.WorkspaceID || plan.Type != request.Kind || plan.Title != request.Title ||
		plan.ScopeDefinition != string(request.Scope) || plan.IdempotencyKey != request.IdempotencyBaseKey+":p" ||
		plan.VisibilityHold == nil || plan.VisibilityHold.OwnerType != artifactapplication.VisibilityHoldOwnerInterviewComplete ||
		plan.VisibilityHold.OwnerID != request.VisibilityHold.OwnerID || plan.VisibilityHold.OwnerRole != artifactapplication.VisibilityHoldRoleReport ||
		plan.VisibilityHold.AttemptDigest != request.VisibilityHold.AttemptDigest {
		t.Fatalf("plan command=%+v", plan)
	}
	artifactID := commands.state.Artifact.ID
	if len(commands.outlines) != 1 || commands.outlines[0].ArtifactID != artifactID || commands.outlines[0].ExpectedVersion != 1 ||
		commands.outlines[0].IdempotencyKey != request.IdempotencyBaseKey+":o" || len(commands.outlines[0].Outline) != 1 ||
		commands.outlines[0].Outline[0].Key != request.Section.Key || commands.outlines[0].Outline[0].Title != request.Section.Title {
		t.Fatalf("outline commands=%+v", commands.outlines)
	}
	if len(commands.approvals) != 1 || commands.approvals[0].ArtifactID != artifactID || commands.approvals[0].ExpectedVersion != 2 ||
		commands.approvals[0].IdempotencyKey != request.IdempotencyBaseKey+":a" {
		t.Fatalf("approval commands=%+v", commands.approvals)
	}
	if len(commands.sections) != 1 {
		t.Fatalf("section commands=%+v", commands.sections)
	}
	sectionCommand := commands.sections[0]
	if sectionCommand.ArtifactID != artifactID || sectionCommand.ExpectedVersion != 3 || sectionCommand.IdempotencyKey != request.IdempotencyBaseKey+":s" ||
		sectionCommand.Creator != artifactdomain.CreatorAgent || sectionCommand.Metadata == nil || *sectionCommand.Metadata != *completionGenerationMetadata() ||
		sectionCommand.Section.Content != request.Section.Markdown || sectionCommand.Section.Coverage.Status != artifactdomain.CoverageCovered ||
		len(sectionCommand.Section.Coverage.Gaps) != 0 || len(sectionCommand.Section.Citations) != 1 {
		t.Fatalf("section command=%+v", sectionCommand)
	}
	inputCitation := sectionCommand.Section.Citations[0]
	evidence := request.Section.Evidence[0]
	if inputCitation.IndexVersionID != evidence.IndexVersionID || inputCitation.ChunkID != evidence.ChunkID ||
		inputCitation.SourceVersionID != evidence.SourceVersionID || inputCitation.SourceSpanID != evidence.SourceSpanID {
		t.Fatalf("citation=%+v evidence=%+v", inputCitation, evidence)
	}
	if result.Binding.Kind != request.Kind || result.Binding.ArtifactID != artifactID ||
		result.Binding.RevisionID != commands.state.Revision.ID || result.Binding.ArtifactVersion != 4 ||
		commands.state.Artifact.Status != artifactdomain.StatusDraft || commands.state.Artifact.Version != 4 ||
		commands.state.Revision.RevisionNo != 4 || commands.state.Revision.CreatedBy != artifactdomain.CreatorAgent ||
		commands.state.Revision.Metadata == nil || *commands.state.Revision.Metadata != *completionGenerationMetadata() {
		t.Fatalf("result=%+v state=%+v", result, commands.state)
	}
	if err := artifactapplication.ValidateState(commands.state); err != nil {
		t.Fatalf("final state is invalid: %v", err)
	}
}

func TestBridgeCreateDraftReplaysAllArtifactStages(t *testing.T) {
	commands := newCommanderFake()
	bridge, err := NewBridge(commands)
	if err != nil {
		t.Fatal(err)
	}
	request := bridgeDraftRequest()
	created, err := bridge.CreateDraft(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := bridge.CreateDraft(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Binding != created.Binding || len(commands.plans) != 2 || len(commands.outlines) != 2 || len(commands.approvals) != 2 || len(commands.sections) != 2 {
		t.Fatalf("incomplete lifecycle replay: created=%+v replayed=%+v calls=%d/%d/%d/%d", created, replayed, len(commands.plans), len(commands.outlines), len(commands.approvals), len(commands.sections))
	}
	if commands.replayCount != 4 {
		t.Fatalf("replayed stages=%d want=4", commands.replayCount)
	}
}

func TestBridgeCreateDraftRejectsInvalidRequestBeforeArtifactCommands(t *testing.T) {
	request := bridgeDraftRequest()
	duplicate := request.Section.Evidence[0]
	duplicate.IndexVersionID = bridgeID(20)
	duplicate.ChunkID = bridgeID(21)
	request.Section.Evidence = append(request.Section.Evidence, duplicate)
	commands := newCommanderFake()
	bridge, err := NewBridge(commands)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.CreateDraft(context.Background(), request); err == nil {
		t.Fatal("duplicate final citation identity must fail")
	}
	if len(commands.plans) != 0 {
		t.Fatalf("invalid request reached Artifact commands: %+v", commands.plans)
	}
}

func TestBridgeCreateDraftRejectsInvalidVisibilityHoldBeforeArtifactCommands(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*interviewapplication.ArtifactDraftRequest)
	}{
		{name: "owner", mutate: func(request *interviewapplication.ArtifactDraftRequest) {
			request.VisibilityHold.OwnerID = "invalid"
		}},
		{name: "role", mutate: func(request *interviewapplication.ArtifactDraftRequest) {
			request.VisibilityHold.Role = "OTHER"
		}},
		{name: "kind role mismatch", mutate: func(request *interviewapplication.ArtifactDraftRequest) {
			request.VisibilityHold.Role = interviewapplication.ArtifactVisibilityHoldRolePath
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := bridgeDraftRequest()
			test.mutate(&request)
			commands := newCommanderFake()
			bridge, err := NewBridge(commands)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.CreateDraft(context.Background(), request); err == nil {
				t.Fatal("invalid visibility hold must fail")
			}
			if len(commands.plans) != 0 {
				t.Fatalf("invalid visibility hold reached Artifact commands: %+v", commands.plans)
			}
		})
	}
}

func TestStageKeysRequireNonEmptyBaseWithSuffixCapacity(t *testing.T) {
	for _, base := range []string{"", " leading", strings.Repeat("x", 127)} {
		if _, err := stageKeys(base); err == nil {
			t.Fatalf("base %q must fail", base)
		}
	}
	keys, err := stageKeys(strings.Repeat("x", 126))
	if err != nil || len(keys.section) != 128 {
		t.Fatalf("boundary keys=%+v err=%v", keys, err)
	}
}

func TestBridgeCreateDraftRejectsInconsistentArtifactResult(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*artifactapplication.CommandResult)
	}{
		{name: "workspace", mutate: func(result *artifactapplication.CommandResult) { result.State.Artifact.WorkspaceID = bridgeID(90) }},
		{name: "type", mutate: func(result *artifactapplication.CommandResult) {
			result.State.Artifact.Type = interviewapplication.ArtifactKindLearningPath
		}},
		{name: "title", mutate: func(result *artifactapplication.CommandResult) { result.State.Artifact.Title = "drifted title" }},
		{name: "scope", mutate: func(result *artifactapplication.CommandResult) { result.State.Artifact.ScopeDefinition = `{}` }},
		{name: "status", mutate: func(result *artifactapplication.CommandResult) {
			result.State.Artifact.Status = artifactdomain.StatusDraft
		}},
		{name: "version", mutate: func(result *artifactapplication.CommandResult) { result.State.Artifact.Version++ }},
		{name: "revision", mutate: func(result *artifactapplication.CommandResult) { result.State.Revision.RevisionNo++ }},
		{name: "command type", mutate: func(result *artifactapplication.CommandResult) {
			result.CommandType = artifactapplication.CommandSubmitOutline
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands := newCommanderFake()
			commands.mutate = func(command artifactapplication.CommandType, result *artifactapplication.CommandResult) {
				if command == artifactapplication.CommandPlan {
					test.mutate(result)
				}
			}
			bridge, err := NewBridge(commands)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.CreateDraft(context.Background(), bridgeDraftRequest()); err == nil {
				t.Fatalf("%s-drifted Artifact result must fail", test.name)
			}
			if len(commands.outlines) != 0 {
				t.Fatalf("drifted plan reached outline command: %+v", commands.outlines)
			}
		})
	}
}

func TestBridgeCreateDraftStopsAtArtifactCommandFailure(t *testing.T) {
	tests := []struct {
		stage                                artifactapplication.CommandType
		plans, outlines, approvals, sections int
	}{
		{stage: artifactapplication.CommandPlan, plans: 1},
		{stage: artifactapplication.CommandSubmitOutline, plans: 1, outlines: 1},
		{stage: artifactapplication.CommandApproveOutline, plans: 1, outlines: 1, approvals: 1},
		{stage: artifactapplication.CommandRecordSection, plans: 1, outlines: 1, approvals: 1, sections: 1},
	}
	for _, test := range tests {
		t.Run(string(test.stage), func(t *testing.T) {
			commands := newCommanderFake()
			commands.failAt = test.stage
			commands.failErr = errors.New("artifact command unavailable")
			bridge, err := NewBridge(commands)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.CreateDraft(context.Background(), bridgeDraftRequest()); !errors.Is(err, commands.failErr) {
				t.Fatalf("err=%v", err)
			}
			if len(commands.plans) != test.plans || len(commands.outlines) != test.outlines || len(commands.approvals) != test.approvals || len(commands.sections) != test.sections {
				t.Fatalf("commands continued after %s failure: %d/%d/%d/%d", test.stage, len(commands.plans), len(commands.outlines), len(commands.approvals), len(commands.sections))
			}
		})
	}
}

type commanderFake struct {
	state       artifactapplication.State
	plans       []artifactapplication.PlanCommand
	outlines    []artifactapplication.SubmitOutlinePersistentCommand
	approvals   []artifactapplication.RevisionPersistentCommand
	sections    []artifactapplication.RecordSectionPersistentCommand
	failAt      artifactapplication.CommandType
	failErr     error
	mutate      func(artifactapplication.CommandType, *artifactapplication.CommandResult)
	receipts    map[string]artifactapplication.CommandResult
	replayCount int
}

func newCommanderFake() *commanderFake {
	return &commanderFake{receipts: make(map[string]artifactapplication.CommandResult)}
}

func (fake *commanderFake) Plan(_ context.Context, command artifactapplication.PlanCommand) (artifactapplication.CommandResult, error) {
	fake.plans = append(fake.plans, command)
	if fake.failAt == artifactapplication.CommandPlan {
		return artifactapplication.CommandResult{}, fake.failErr
	}
	if result, found := fake.replay(command.IdempotencyKey); found {
		return result, nil
	}
	artifact, revision, err := artifactdomain.PlanArtifact(artifactdomain.PlanInput{
		ArtifactID: bridgeID(2), InitialRevisionID: bridgeID(3), WorkspaceID: command.WorkspaceID,
		Type: command.Type, Title: command.Title, ScopeDefinition: command.ScopeDefinition, CreatedAt: bridgeNow,
	})
	if err != nil {
		return artifactapplication.CommandResult{}, err
	}
	fake.state = artifactapplication.State{Artifact: artifact, Revision: revision}
	return fake.record(command.IdempotencyKey, artifactapplication.CommandPlan), nil
}

func (fake *commanderFake) SubmitOutline(_ context.Context, command artifactapplication.SubmitOutlinePersistentCommand) (artifactapplication.CommandResult, error) {
	fake.outlines = append(fake.outlines, command)
	if fake.failAt == artifactapplication.CommandSubmitOutline {
		return artifactapplication.CommandResult{}, fake.failErr
	}
	if result, found := fake.replay(command.IdempotencyKey); found {
		return result, nil
	}
	artifact, revision, err := artifactdomain.SubmitOutline(fake.state.Artifact, fake.state.Revision, bridgeID(4), command.Outline, bridgeNow)
	if err != nil {
		return artifactapplication.CommandResult{}, err
	}
	fake.state = artifactapplication.State{Artifact: artifact, Revision: revision}
	return fake.record(command.IdempotencyKey, artifactapplication.CommandSubmitOutline), nil
}

func (fake *commanderFake) ApproveOutline(_ context.Context, command artifactapplication.RevisionPersistentCommand) (artifactapplication.CommandResult, error) {
	fake.approvals = append(fake.approvals, command)
	if fake.failAt == artifactapplication.CommandApproveOutline {
		return artifactapplication.CommandResult{}, fake.failErr
	}
	if result, found := fake.replay(command.IdempotencyKey); found {
		return result, nil
	}
	artifact, revision, err := artifactdomain.ApproveOutline(fake.state.Artifact, fake.state.Revision, bridgeID(5), bridgeNow)
	if err != nil {
		return artifactapplication.CommandResult{}, err
	}
	fake.state = artifactapplication.State{Artifact: artifact, Revision: revision}
	return fake.record(command.IdempotencyKey, artifactapplication.CommandApproveOutline), nil
}

func (fake *commanderFake) RecordSection(_ context.Context, command artifactapplication.RecordSectionPersistentCommand) (artifactapplication.CommandResult, error) {
	fake.sections = append(fake.sections, command)
	if fake.failAt == artifactapplication.CommandRecordSection {
		return artifactapplication.CommandResult{}, fake.failErr
	}
	if result, found := fake.replay(command.IdempotencyKey); found {
		return result, nil
	}
	citations := make([]artifactdomain.Citation, len(command.Section.Citations))
	for index, citation := range command.Section.Citations {
		citations[index] = artifactdomain.Citation{
			SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
			VerifiedContentHash: strings.Repeat("c", 64), Excerpt: "Verified Interview evidence.", Verified: true,
		}
	}
	section := artifactdomain.Section{
		Key: command.Section.Key, Title: command.Section.Title, Content: command.Section.Content,
		Citations: citations, Coverage: command.Section.Coverage,
	}
	artifact, revision, err := artifactdomain.RecordSection(
		fake.state.Artifact, fake.state.Revision, bridgeID(6), section, command.Creator, command.Metadata, bridgeNow,
	)
	if err != nil {
		return artifactapplication.CommandResult{}, err
	}
	fake.state = artifactapplication.State{Artifact: artifact, Revision: revision}
	return fake.record(command.IdempotencyKey, artifactapplication.CommandRecordSection), nil
}

func (fake *commanderFake) record(key string, command artifactapplication.CommandType) artifactapplication.CommandResult {
	result := fake.result(command)
	fake.receipts[key] = result
	return result
}

func (fake *commanderFake) replay(key string) (artifactapplication.CommandResult, bool) {
	result, found := fake.receipts[key]
	if !found {
		return artifactapplication.CommandResult{}, false
	}
	result.Replayed = true
	fake.state = result.State
	fake.replayCount++
	return result, true
}

func (fake *commanderFake) result(command artifactapplication.CommandType) artifactapplication.CommandResult {
	result := artifactapplication.CommandResult{
		State: fake.state, CommandType: command, CommandVersion: fake.state.Artifact.Version,
	}
	if fake.mutate != nil {
		fake.mutate(command, &result)
	}
	return result
}

func bridgeDraftRequest() interviewapplication.ArtifactDraftRequest {
	claimID := bridgeID(10)
	ownerID := bridgeID(15)
	attemptDigest := strings.Repeat("f", 64)
	return interviewapplication.ArtifactDraftRequest{
		WorkspaceID: bridgeID(1), Kind: interviewapplication.ArtifactKindInterviewDocument,
		Title: "Interview report", Scope: json.RawMessage(`{"schema_version":"interview-report/v1"}`),
		Section: interviewapplication.ArtifactDraftSection{
			Key: "report", Title: "Interview report", Markdown: "# Interview report\n\nEvidence-bound result.",
			Evidence: []interviewdomain.EvidenceRef{{
				SchemaVersion: interviewdomain.EvidenceSchemaVersion, ClaimID: claimID,
				IndexVersionID: bridgeID(11), ChunkID: bridgeID(12), SourceVersionID: bridgeID(13), SourceSpanID: bridgeID(14),
				EvidenceHash: strings.Repeat("a", 64), SupportType: "SUPPORTS",
			}},
		},
		IdempotencyBaseKey: "iv1:" + string(ownerID) + ":INTERVIEW_DOC:" + attemptDigest,
		VisibilityHold: interviewapplication.ArtifactVisibilityHold{
			OwnerID:       ownerID,
			Role:          interviewapplication.ArtifactVisibilityHoldRoleReport,
			AttemptDigest: attemptDigest,
		},
	}
}

var bridgeNow = time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)

func bridgeID(number int) foundation.ID {
	value, err := foundation.ParseID("a1000000-0000-4000-8000-0000000000" + twoDigits(number))
	if err != nil {
		panic(err)
	}
	return value
}

func twoDigits(number int) string {
	if number < 10 {
		return "0" + string(rune('0'+number))
	}
	return string(rune('0'+number/10)) + string(rune('0'+number%10))
}
