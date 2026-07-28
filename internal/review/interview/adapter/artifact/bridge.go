// Package artifact 把 Interview completion 适配到 Artifact 应用命令。
package artifact

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	interviewapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

// Commander 是 Interview 完成一个 DRAFT 所需的最小 Artifact 应用命令面。
type Commander interface {
	Plan(context.Context, artifactapplication.PlanCommand) (artifactapplication.CommandResult, error)
	SubmitOutline(context.Context, artifactapplication.SubmitOutlinePersistentCommand) (artifactapplication.CommandResult, error)
	ApproveOutline(context.Context, artifactapplication.RevisionPersistentCommand) (artifactapplication.CommandResult, error)
	RecordSection(context.Context, artifactapplication.RecordSectionPersistentCommand) (artifactapplication.CommandResult, error)
}

// Bridge 只通过 Artifact 应用命令创建 Interview completion 草稿。
type Bridge struct{ commands Commander }

// NewBridge 创建 fail-closed 的 Interview Artifact bridge。
func NewBridge(commands Commander) (*Bridge, error) {
	if nilDependency(commands) {
		return nil, interviewdomain.UnavailableError(interviewdomain.ErrorCodeDependencyUnavailable, "interview artifact commands are unavailable")
	}
	return &Bridge{commands: commands}, nil
}

// CreateDraft 以固定单节大纲推进到 DRAFT；不会调用 ApproveDraft。
// 大纲审批是内部确定性模板推进，最终 DRAFT 仍保留给正常 Artifact 人工审批语义。
func (bridge *Bridge) CreateDraft(ctx context.Context, request interviewapplication.ArtifactDraftRequest) (interviewapplication.ArtifactDraftResult, error) {
	if bridge == nil || nilDependency(bridge.commands) {
		return interviewapplication.ArtifactDraftResult{}, interviewdomain.UnavailableError(interviewdomain.ErrorCodeDependencyUnavailable, "interview artifact commands are unavailable")
	}
	if err := validateRequest(request); err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}
	keys, err := stageKeys(request.IdempotencyBaseKey)
	if err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}

	planned, err := bridge.commands.Plan(ctx, artifactapplication.PlanCommand{
		WorkspaceID: request.WorkspaceID, Type: request.Kind, Title: request.Title,
		ScopeDefinition: string(request.Scope), IdempotencyKey: keys.plan,
		VisibilityHold: mapVisibilityHold(request.VisibilityHold),
	})
	if err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}
	if err := validateStageResult(request, planned, artifactapplication.CommandPlan, artifactdomain.StatusPlanning, 1, 1, ""); err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}
	artifactID := planned.State.Artifact.ID

	outlined, err := bridge.commands.SubmitOutline(ctx, artifactapplication.SubmitOutlinePersistentCommand{
		WorkspaceID: request.WorkspaceID, ArtifactID: artifactID, ExpectedVersion: 1,
		Outline: []artifactdomain.OutlineSection{{Key: request.Section.Key, Title: request.Section.Title}}, IdempotencyKey: keys.outline,
	})
	if err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}
	if err := validateStageResult(request, outlined, artifactapplication.CommandSubmitOutline, artifactdomain.StatusOutlineReview, 2, 2, artifactID); err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}

	approved, err := bridge.commands.ApproveOutline(ctx, artifactapplication.RevisionPersistentCommand{
		WorkspaceID: request.WorkspaceID, ArtifactID: artifactID, ExpectedVersion: 2, IdempotencyKey: keys.approve,
	})
	if err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}
	if err := validateStageResult(request, approved, artifactapplication.CommandApproveOutline, artifactdomain.StatusGenerating, 3, 3, artifactID); err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}

	citations := make([]artifactapplication.CitationInput, len(request.Section.Evidence))
	for index, evidence := range request.Section.Evidence {
		citations[index] = artifactapplication.CitationInput{
			IndexVersionID: evidence.IndexVersionID, ChunkID: evidence.ChunkID,
			SourceVersionID: evidence.SourceVersionID, SourceSpanID: evidence.SourceSpanID,
		}
	}
	recorded, err := bridge.commands.RecordSection(ctx, artifactapplication.RecordSectionPersistentCommand{
		RevisionPersistentCommand: artifactapplication.RevisionPersistentCommand{
			WorkspaceID: request.WorkspaceID, ArtifactID: artifactID, ExpectedVersion: 3, IdempotencyKey: keys.section,
		},
		Section: artifactapplication.SectionInput{
			Key: request.Section.Key, Title: request.Section.Title, Content: request.Section.Markdown, Citations: citations,
			Coverage: artifactdomain.Coverage{SectionKey: request.Section.Key, Status: artifactdomain.CoverageCovered, Gaps: []artifactdomain.Gap{}},
		},
		Creator:  artifactdomain.CreatorAgent,
		Metadata: completionGenerationMetadata(),
	})
	if err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}
	if err := validateStageResult(request, recorded, artifactapplication.CommandRecordSection, artifactdomain.StatusDraft, 4, 4, artifactID); err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}
	if err := validateFinalDraft(request, recorded.State); err != nil {
		return interviewapplication.ArtifactDraftResult{}, err
	}
	return interviewapplication.ArtifactDraftResult{Binding: interviewdomain.ArtifactBinding{
		Kind: request.Kind, ArtifactID: artifactID, RevisionID: recorded.State.Revision.ID,
		ArtifactVersion: recorded.State.Artifact.Version,
	}}, nil
}

type artifactStageKeys struct {
	plan    string
	outline string
	approve string
	section string
}

// stageKeys 给同一内容绑定的四条命令追加稳定短后缀。
func stageKeys(base string) (artifactStageKeys, error) {
	if base == "" || strings.TrimSpace(base) != base || len(base)+2 > 128 || strings.ContainsAny(base, "\r\n") {
		return artifactStageKeys{}, interviewdomain.InvalidError(interviewdomain.ErrorCodeReportInvalid, "interview artifact idempotency key is invalid")
	}
	keys := artifactStageKeys{plan: base + ":p", outline: base + ":o", approve: base + ":a", section: base + ":s"}
	for _, key := range []string{keys.plan, keys.outline, keys.approve, keys.section} {
		if strings.TrimSpace(key) != key || key == "" || len(key) > 128 || strings.ContainsAny(key, "\r\n") {
			return artifactStageKeys{}, interviewdomain.InvalidError(interviewdomain.ErrorCodeReportInvalid, "interview artifact idempotency key is invalid")
		}
	}
	return keys, nil
}

func validateRequest(request interviewapplication.ArtifactDraftRequest) error {
	if !validID(request.WorkspaceID) || !validKind(request.Kind) || strings.TrimSpace(request.Title) == "" ||
		len(request.Scope) == 0 || !json.Valid(request.Scope) || strings.TrimSpace(request.Section.Key) == "" ||
		strings.TrimSpace(request.Section.Title) == "" || request.Section.Markdown == "" ||
		request.Section.Markdown != strings.TrimSpace(request.Section.Markdown) || len(request.Section.Evidence) == 0 ||
		!validVisibilityHold(request.Kind, request.IdempotencyBaseKey, request.VisibilityHold) {
		return interviewdomain.InvalidError(interviewdomain.ErrorCodeReportInvalid, "interview artifact draft request is invalid")
	}
	seen := make(map[string]struct{}, len(request.Section.Evidence))
	seenCitation := make(map[string]struct{}, len(request.Section.Evidence))
	for _, evidence := range request.Section.Evidence {
		if err := interviewdomain.ValidateEvidenceRef(evidence); err != nil {
			return err
		}
		key := string(evidence.IndexVersionID) + "\x00" + string(evidence.ChunkID) + "\x00" + string(evidence.SourceVersionID) + "\x00" + string(evidence.SourceSpanID)
		if _, duplicate := seen[key]; duplicate {
			return interviewdomain.InvalidError(interviewdomain.ErrorCodeReportInvalid, "interview artifact citation tuple is duplicated")
		}
		seen[key] = struct{}{}
		citationKey := string(evidence.SourceVersionID) + "\x00" + string(evidence.SourceSpanID)
		if _, duplicate := seenCitation[citationKey]; duplicate {
			return interviewdomain.InvalidError(interviewdomain.ErrorCodeReportInvalid, "interview artifact citation source span is duplicated")
		}
		seenCitation[citationKey] = struct{}{}
	}
	_, err := stageKeys(request.IdempotencyBaseKey)
	return err
}

func validateStageResult(
	request interviewapplication.ArtifactDraftRequest,
	result artifactapplication.CommandResult,
	command artifactapplication.CommandType,
	status artifactdomain.Status,
	version, revisionNo int64,
	artifactID foundation.ID,
) error {
	if result.CommandType != command || result.CommandVersion != version || result.Export != nil || result.Publication != nil ||
		artifactapplication.ValidateState(result.State) != nil || result.State.Artifact.WorkspaceID != request.WorkspaceID ||
		result.State.Artifact.Type != request.Kind || result.State.Artifact.Title != request.Title ||
		result.State.Artifact.ScopeDefinition != string(request.Scope) || result.State.Artifact.Status != status ||
		result.State.Artifact.Version != version || result.State.Revision.RevisionNo != revisionNo ||
		result.State.Artifact.CurrentRevisionID != result.State.Revision.ID ||
		(artifactID != "" && result.State.Artifact.ID != artifactID) {
		return interviewdomain.InvalidError(interviewdomain.ErrorCodePersistenceInvalid, "interview artifact command result is inconsistent")
	}
	return nil
}

func validateFinalDraft(request interviewapplication.ArtifactDraftRequest, state artifactapplication.State) error {
	expectedMetadata := completionGenerationMetadata()
	if len(state.Revision.Outline) != 1 || len(state.Revision.Sections) != 1 || state.Revision.CreatedBy != artifactdomain.CreatorAgent ||
		state.Revision.Metadata == nil || *state.Revision.Metadata != *expectedMetadata {
		return interviewdomain.InvalidError(interviewdomain.ErrorCodePersistenceInvalid, "interview artifact draft is incomplete")
	}
	outline := state.Revision.Outline[0]
	section := state.Revision.Sections[0]
	if outline.Key != request.Section.Key || outline.Title != request.Section.Title || section.Key != request.Section.Key ||
		section.Title != request.Section.Title || section.Content != request.Section.Markdown || section.Coverage.SectionKey != request.Section.Key ||
		section.Coverage.Status != artifactdomain.CoverageCovered || len(section.Coverage.Gaps) != 0 ||
		len(section.Citations) != len(request.Section.Evidence) {
		return interviewdomain.InvalidError(interviewdomain.ErrorCodePersistenceInvalid, "interview artifact draft content is inconsistent")
	}
	wantCitations := make(map[string]struct{}, len(request.Section.Evidence))
	for _, evidence := range request.Section.Evidence {
		wantCitations[string(evidence.SourceVersionID)+"\x00"+string(evidence.SourceSpanID)] = struct{}{}
	}
	for _, citation := range section.Citations {
		if !citation.Verified {
			return interviewdomain.InvalidError(interviewdomain.ErrorCodePersistenceInvalid, "interview artifact citation is not verified")
		}
		key := string(citation.SourceVersionID) + "\x00" + string(citation.SourceSpanID)
		if _, found := wantCitations[key]; !found {
			return interviewdomain.InvalidError(interviewdomain.ErrorCodePersistenceInvalid, "interview artifact citation drifted")
		}
		delete(wantCitations, key)
	}
	if len(wantCitations) != 0 {
		return interviewdomain.InvalidError(interviewdomain.ErrorCodePersistenceInvalid, "interview artifact citation result is incomplete")
	}
	return nil
}

func completionGenerationMetadata() *artifactdomain.GenerationMetadata {
	return &artifactdomain.GenerationMetadata{
		PromptVersion:             "interview-completion-renderer/v1",
		ModelVersion:              "interview-deterministic-renderer/v1",
		WorkflowDefinitionVersion: "interview-completion/v1",
		SchemaVersion:             "interview-completion-artifact/v1",
	}
}

func validKind(kind string) bool {
	return kind == interviewapplication.ArtifactKindInterviewDocument || kind == interviewapplication.ArtifactKindLearningPath
}

func validVisibilityHold(kind, idempotencyBaseKey string, hold interviewapplication.ArtifactVisibilityHold) bool {
	if !validID(hold.OwnerID) || !validHash(hold.AttemptDigest) {
		return false
	}
	roleMatches := (kind == interviewapplication.ArtifactKindInterviewDocument && hold.Role == interviewapplication.ArtifactVisibilityHoldRoleReport) ||
		(kind == interviewapplication.ArtifactKindLearningPath && hold.Role == interviewapplication.ArtifactVisibilityHoldRolePath)
	wantBaseKey := "iv1:" + string(hold.OwnerID) + ":" + kind + ":" + hold.AttemptDigest
	return roleMatches && idempotencyBaseKey == wantBaseKey
}

func mapVisibilityHold(hold interviewapplication.ArtifactVisibilityHold) *artifactapplication.VisibilityHold {
	role := artifactapplication.VisibilityHoldRoleReport
	if hold.Role == interviewapplication.ArtifactVisibilityHoldRolePath {
		role = artifactapplication.VisibilityHoldRolePath
	}
	return &artifactapplication.VisibilityHold{
		OwnerType:     artifactapplication.VisibilityHoldOwnerInterviewComplete,
		OwnerID:       hold.OwnerID,
		OwnerRole:     role,
		AttemptDigest: hold.AttemptDigest,
	}
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validID(value foundation.ID) bool {
	_, err := foundation.ParseID(string(value))
	return err == nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
