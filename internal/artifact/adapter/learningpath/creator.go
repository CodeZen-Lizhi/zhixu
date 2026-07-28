// Package learningpath adapts Review Learning Path drafts to Artifact commands.
package learningpath

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	artifactapplication "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	pathapplication "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	pathdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
)

// Commander 是创建一个隐藏 LEARNING_PATH DRAFT 所需的最小 Artifact 命令面。
type Commander interface {
	Plan(context.Context, artifactapplication.PlanCommand) (artifactapplication.CommandResult, error)
	SubmitOutline(context.Context, artifactapplication.SubmitOutlinePersistentCommand) (artifactapplication.CommandResult, error)
	ApproveOutline(context.Context, artifactapplication.RevisionPersistentCommand) (artifactapplication.CommandResult, error)
	RecordSection(context.Context, artifactapplication.RecordSectionPersistentCommand) (artifactapplication.CommandResult, error)
}

// Creator 只通过 Artifact owner 的应用命令创建 Review Learning Path 草稿。
type Creator struct{ commands Commander }

// NewCreator 构造 fail-closed 的 Review Learning Path Artifact 适配器。
func NewCreator(commands Commander) (*Creator, error) {
	if nilDependency(commands) {
		return nil, pathdomain.UnavailableError(pathdomain.ErrorCodeDependencyUnavailable, "learning path artifact commands are unavailable")
	}
	return &Creator{commands: commands}, nil
}

// CreateLearningPathDraft 以固定单节大纲推进到 DRAFT，并始终保留
// LEARNING_PATH_CREATE/PATH hold，直到 Path 最终事务精确释放它。
func (creator *Creator) CreateLearningPathDraft(ctx context.Context, request pathapplication.DraftRequest) (pathdomain.ArtifactBinding, error) {
	if creator == nil || nilDependency(creator.commands) {
		return pathdomain.ArtifactBinding{}, pathdomain.UnavailableError(pathdomain.ErrorCodeDependencyUnavailable, "learning path artifact commands are unavailable")
	}
	if err := validateDraftRequest(request); err != nil {
		return pathdomain.ArtifactBinding{}, err
	}
	keys := draftStageKeys{
		plan: request.IdempotencyBaseKey + ":p", outline: request.IdempotencyBaseKey + ":o",
		approve: request.IdempotencyBaseKey + ":a", section: request.IdempotencyBaseKey + ":s",
	}
	planned, err := creator.commands.Plan(ctx, artifactapplication.PlanCommand{
		WorkspaceID: request.WorkspaceID, Type: pathapplication.ArtifactKind, Title: request.Title,
		ScopeDefinition: string(request.Scope), IdempotencyKey: keys.plan,
		VisibilityHold: &artifactapplication.VisibilityHold{
			OwnerType: artifactapplication.VisibilityHoldOwnerLearningPathCreate, OwnerID: request.ReviewAnswerID,
			OwnerRole: artifactapplication.VisibilityHoldRolePath, AttemptDigest: request.ArtifactDigest,
		},
	})
	if err != nil {
		return pathdomain.ArtifactBinding{}, err
	}
	if err := validateStageResult(request, planned, artifactapplication.CommandPlan, artifactdomain.StatusPlanning, 1, 1, ""); err != nil {
		return pathdomain.ArtifactBinding{}, err
	}
	artifactID := planned.State.Artifact.ID

	outlined, err := creator.commands.SubmitOutline(ctx, artifactapplication.SubmitOutlinePersistentCommand{
		WorkspaceID: request.WorkspaceID, ArtifactID: artifactID, ExpectedVersion: 1,
		Outline: []artifactdomain.OutlineSection{{Key: pathapplication.ReviewArtifactSectionKey, Title: request.Title}}, IdempotencyKey: keys.outline,
	})
	if err != nil {
		return pathdomain.ArtifactBinding{}, err
	}
	if err := validateStageResult(request, outlined, artifactapplication.CommandSubmitOutline, artifactdomain.StatusOutlineReview, 2, 2, artifactID); err != nil {
		return pathdomain.ArtifactBinding{}, err
	}

	approved, err := creator.commands.ApproveOutline(ctx, artifactapplication.RevisionPersistentCommand{
		WorkspaceID: request.WorkspaceID, ArtifactID: artifactID, ExpectedVersion: 2, IdempotencyKey: keys.approve,
	})
	if err != nil {
		return pathdomain.ArtifactBinding{}, err
	}
	if err := validateStageResult(request, approved, artifactapplication.CommandApproveOutline, artifactdomain.StatusGenerating, 3, 3, artifactID); err != nil {
		return pathdomain.ArtifactBinding{}, err
	}

	citations := make([]artifactapplication.CitationInput, len(request.Citations))
	for index, citation := range request.Citations {
		citations[index] = artifactapplication.CitationInput{
			IndexVersionID: citation.IndexVersionID, ChunkID: citation.ChunkID,
			SourceVersionID: citation.SourceVersionID, SourceSpanID: citation.SourceSpanID,
		}
	}
	recorded, err := creator.commands.RecordSection(ctx, artifactapplication.RecordSectionPersistentCommand{
		RevisionPersistentCommand: artifactapplication.RevisionPersistentCommand{
			WorkspaceID: request.WorkspaceID, ArtifactID: artifactID, ExpectedVersion: 3, IdempotencyKey: keys.section,
		},
		Section: artifactapplication.SectionInput{
			Key: pathapplication.ReviewArtifactSectionKey, Title: request.Title, Content: request.Markdown, Citations: citations,
			Coverage: artifactdomain.Coverage{SectionKey: pathapplication.ReviewArtifactSectionKey, Status: artifactdomain.CoverageStatus(pathapplication.ReviewArtifactCoverageStatus), Gaps: []artifactdomain.Gap{}},
		},
		Creator: artifactdomain.CreatorType(pathapplication.ReviewArtifactCreator), Metadata: reviewPathGenerationMetadata(),
	})
	if err != nil {
		return pathdomain.ArtifactBinding{}, err
	}
	if err := validateStageResult(request, recorded, artifactapplication.CommandRecordSection, artifactdomain.StatusDraft, 4, 4, artifactID); err != nil {
		return pathdomain.ArtifactBinding{}, err
	}
	if err := validateFinalDraft(request, recorded.State); err != nil {
		return pathdomain.ArtifactBinding{}, err
	}
	return pathdomain.ArtifactBinding{
		ArtifactID: artifactID, ArtifactRevisionID: recorded.State.Revision.ID,
		ArtifactVersion: recorded.State.Artifact.Version,
	}, nil
}

type draftStageKeys struct {
	plan    string
	outline string
	approve string
	section string
}

func validateDraftRequest(request pathapplication.DraftRequest) error {
	wantBaseKey := "lp1:review:" + string(request.ReviewAnswerID) + ":" + pathapplication.ArtifactKind + ":" + request.ArtifactDigest
	if !validID(request.WorkspaceID) || !validID(request.ReviewAnswerID) || request.WorkspaceID == request.ReviewAnswerID ||
		!validHash(request.ArtifactDigest) || request.IdempotencyBaseKey != wantBaseKey || len(request.IdempotencyBaseKey)+2 > 128 ||
		strings.TrimSpace(request.Title) != request.Title || request.Title == "" || len(request.Scope) == 0 || !json.Valid(request.Scope) ||
		strings.TrimSpace(request.Markdown) != request.Markdown || request.Markdown == "" || len(request.Citations) == 0 {
		return pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path artifact draft request is invalid")
	}
	seenTuple := make(map[string]struct{}, len(request.Citations))
	seenSpan := make(map[string]struct{}, len(request.Citations))
	for _, citation := range request.Citations {
		if err := pathdomain.ValidateCitation(citation); err != nil {
			return err
		}
		tuple := string(citation.IndexVersionID) + "\x00" + string(citation.ChunkID) + "\x00" + string(citation.SourceVersionID) + "\x00" + string(citation.SourceSpanID)
		span := string(citation.SourceVersionID) + "\x00" + string(citation.SourceSpanID)
		if _, duplicate := seenTuple[tuple]; duplicate {
			return pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path artifact citation tuple is duplicated")
		}
		if _, duplicate := seenSpan[span]; duplicate {
			return pathdomain.InvalidError(pathdomain.ErrorCodePathInvalid, "learning path artifact citation source span is duplicated")
		}
		seenTuple[tuple] = struct{}{}
		seenSpan[span] = struct{}{}
	}
	return nil
}

func validateStageResult(
	request pathapplication.DraftRequest,
	result artifactapplication.CommandResult,
	command artifactapplication.CommandType,
	status artifactdomain.Status,
	version, revisionNo int64,
	artifactID foundation.ID,
) error {
	if result.CommandType != command || result.CommandVersion != version || result.Export != nil || result.Publication != nil ||
		artifactapplication.ValidateState(result.State) != nil || result.State.Artifact.WorkspaceID != request.WorkspaceID ||
		result.State.Artifact.Type != pathapplication.ArtifactKind || result.State.Artifact.Title != request.Title ||
		result.State.Artifact.ScopeDefinition != string(request.Scope) || result.State.Artifact.Status != status ||
		result.State.Artifact.Version != version || result.State.Revision.RevisionNo != revisionNo ||
		result.State.Artifact.CurrentRevisionID != result.State.Revision.ID ||
		(artifactID != "" && result.State.Artifact.ID != artifactID) {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path artifact command result is inconsistent")
	}
	return nil
}

func validateFinalDraft(request pathapplication.DraftRequest, state artifactapplication.State) error {
	expectedMetadata := reviewPathGenerationMetadata()
	if len(state.Revision.Outline) != 1 || len(state.Revision.Sections) != 1 || state.Revision.CreatedBy != artifactdomain.CreatorType(pathapplication.ReviewArtifactCreator) ||
		state.Revision.Metadata == nil || *state.Revision.Metadata != *expectedMetadata {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path artifact draft is incomplete")
	}
	outline := state.Revision.Outline[0]
	section := state.Revision.Sections[0]
	if outline.Key != pathapplication.ReviewArtifactSectionKey || outline.Title != request.Title || section.Key != pathapplication.ReviewArtifactSectionKey ||
		section.Title != request.Title || section.Content != request.Markdown || section.Coverage.SectionKey != pathapplication.ReviewArtifactSectionKey ||
		section.Coverage.Status != artifactdomain.CoverageStatus(pathapplication.ReviewArtifactCoverageStatus) || len(section.Coverage.Gaps) != 0 ||
		len(section.Citations) != len(request.Citations) {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path artifact draft content is inconsistent")
	}
	wantCitations := make(map[string]struct{}, len(request.Citations))
	for _, citation := range request.Citations {
		wantCitations[string(citation.SourceVersionID)+"\x00"+string(citation.SourceSpanID)] = struct{}{}
	}
	for _, citation := range section.Citations {
		key := string(citation.SourceVersionID) + "\x00" + string(citation.SourceSpanID)
		if !citation.Verified {
			return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path artifact citation is not verified")
		}
		if _, found := wantCitations[key]; !found {
			return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path artifact citation drifted")
		}
		delete(wantCitations, key)
	}
	if len(wantCitations) != 0 {
		return pathdomain.InvalidError(pathdomain.ErrorCodePersistenceInvalid, "learning path artifact citation result is incomplete")
	}
	return nil
}

func reviewPathGenerationMetadata() *artifactdomain.GenerationMetadata {
	return &artifactdomain.GenerationMetadata{
		PromptVersion:             pathapplication.ReviewArtifactPromptVersion,
		ModelVersion:              pathapplication.ReviewArtifactModelVersion,
		WorkflowDefinitionVersion: pathapplication.ReviewArtifactWorkflowVersion,
		SchemaVersion:             pathapplication.ReviewArtifactSchemaVersion,
	}
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
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
