package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	artifactdomain "github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	changecontrolapp "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/application"
	changecontroldomain "github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
)

const artifactTypeDocumentDraft = "DOCUMENT_DRAFT"

// ArtifactCommandPort is the durable Artifact owner command surface used by Organizing.
type ArtifactCommandPort interface {
	Plan(context.Context, artifactapp.PlanCommand) (artifactapp.CommandResult, error)
	SubmitOutline(context.Context, artifactapp.SubmitOutlinePersistentCommand) (artifactapp.CommandResult, error)
	ApproveOutline(context.Context, artifactapp.RevisionPersistentCommand) (artifactapp.CommandResult, error)
	RecordSection(context.Context, artifactapp.RecordSectionPersistentCommand) (artifactapp.CommandResult, error)
}

// ArtifactQueryPort is the workspace-scoped Artifact owner query surface.
type ArtifactQueryPort interface {
	Get(context.Context, foundation.ID, foundation.ID) (artifactapp.State, error)
}

// ArtifactCreateRequest contains the frozen facts needed to write one Artifact draft.
type ArtifactCreateRequest struct {
	RunID             foundation.ID
	Snapshot          organizingdomain.Snapshot
	Template          organizingdomain.TemplateRevision
	DefaultTargetPath string
	Outline           []artifactdomain.OutlineSection
	Sections          []artifactapp.SectionInput
	Metadata          artifactdomain.GenerationMetadata
}

// ArtifactReceipt is the redacted owner result bound to an Organizing Run.
type ArtifactReceipt struct {
	ArtifactID   foundation.ID `json:"artifact_id"`
	RevisionHash string        `json:"revision_hash"`
}

// ArtifactOwner writes complete draft Artifacts through the Artifact Application owner.
type ArtifactOwner struct {
	commands ArtifactCommandPort
	queries  ArtifactQueryPort
}

// NewArtifactOwner constructs the durable Artifact owner adapter.
func NewArtifactOwner(commands ArtifactCommandPort, queries ArtifactQueryPort) (*ArtifactOwner, error) {
	if commands == nil || queries == nil {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_ARTIFACT_OWNER_UNAVAILABLE", true, "artifact owner dependencies are unavailable")
	}
	return &ArtifactOwner{commands: commands, queries: queries}, nil
}

// Create writes Plan, outline approval, and every section with stable receipt keys.
func (owner *ArtifactOwner) Create(ctx context.Context, request ArtifactCreateRequest) (ArtifactReceipt, error) {
	if owner == nil || owner.commands == nil || owner.queries == nil || ctx == nil || !validID(request.RunID) {
		return ArtifactReceipt{}, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_ARTIFACT_REQUEST_INVALID", false, "organizing artifact request is invalid")
	}
	expectedTargetPath, pathErr := defaultOutputPath(request.Snapshot, request.Template)
	if pathErr != nil || expectedTargetPath != request.DefaultTargetPath ||
		len(request.Outline) == 0 || len(request.Sections) != len(request.Outline) || !validGenerationMetadata(request.Metadata) {
		return ArtifactReceipt{}, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_ARTIFACT_REQUEST_INVALID", false, "organizing artifact request is invalid")
	}
	prefix := "organizing:" + string(request.RunID)
	planned, err := owner.commands.Plan(ctx, artifactapp.PlanCommand{
		WorkspaceID: request.Snapshot.WorkspaceID, Type: artifactTypeDocumentDraft,
		Title:           request.Template.Declaration.Name,
		ScopeDefinition: request.Snapshot.Intent + "\n默认输出路径: " + request.DefaultTargetPath,
		IdempotencyKey:  prefix + ":plan",
	})
	if err != nil {
		return ArtifactReceipt{}, err
	}
	artifactID := planned.State.Artifact.ID
	submitted, err := owner.commands.SubmitOutline(ctx, artifactapp.SubmitOutlinePersistentCommand{
		WorkspaceID: request.Snapshot.WorkspaceID, ArtifactID: artifactID,
		ExpectedVersion: planned.State.Artifact.Version, Outline: cloneOutline(request.Outline),
		IdempotencyKey: prefix + ":outline",
	})
	if err != nil {
		return ArtifactReceipt{}, err
	}
	approved, err := owner.commands.ApproveOutline(ctx, artifactapp.RevisionPersistentCommand{
		WorkspaceID: request.Snapshot.WorkspaceID, ArtifactID: artifactID,
		ExpectedVersion: submitted.State.Artifact.Version, IdempotencyKey: prefix + ":approve",
	})
	if err != nil {
		return ArtifactReceipt{}, err
	}
	current := approved
	metadata := request.Metadata
	for index, section := range request.Sections {
		current, err = owner.commands.RecordSection(ctx, artifactapp.RecordSectionPersistentCommand{
			RevisionPersistentCommand: artifactapp.RevisionPersistentCommand{
				WorkspaceID: request.Snapshot.WorkspaceID, ArtifactID: artifactID,
				ExpectedVersion: current.State.Artifact.Version,
				IdempotencyKey:  fmt.Sprintf("%s:section:%02d", prefix, index),
			},
			Section: section, Creator: artifactdomain.CreatorAgent, Metadata: &metadata,
		})
		if err != nil {
			return ArtifactReceipt{}, err
		}
	}
	if current.State.Artifact.Status != artifactdomain.StatusDraft || current.State.Revision.ContentHash == "" {
		return ArtifactReceipt{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_ARTIFACT_RESULT_INVALID", false, "artifact owner returned an incomplete draft")
	}
	receipt := ArtifactReceipt{ArtifactID: artifactID, RevisionHash: current.State.Revision.ContentHash}
	if _, err := owner.GetExact(ctx, request.Snapshot.WorkspaceID, receipt); err != nil {
		return ArtifactReceipt{}, err
	}
	return receipt, nil
}

func validGenerationMetadata(metadata artifactdomain.GenerationMetadata) bool {
	return strings.TrimSpace(metadata.PromptVersion) != "" && strings.TrimSpace(metadata.ModelVersion) != "" &&
		strings.TrimSpace(metadata.WorkflowDefinitionVersion) != "" && strings.TrimSpace(metadata.SchemaVersion) != "" &&
		len(metadata.PromptVersion) <= 256 && len(metadata.ModelVersion) <= 256 &&
		len(metadata.WorkflowDefinitionVersion) <= 256 && len(metadata.SchemaVersion) <= 256
}

// GetExact reloads an Artifact and proves its frozen revision hash.
func (owner *ArtifactOwner) GetExact(ctx context.Context, workspaceID foundation.ID, receipt ArtifactReceipt) (artifactapp.State, error) {
	if owner == nil || owner.queries == nil || !validID(receipt.ArtifactID) || !validHash(receipt.RevisionHash) {
		return artifactapp.State{}, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_ARTIFACT_RECEIPT_INVALID", false, "organizing artifact receipt is invalid")
	}
	state, err := owner.queries.Get(ctx, workspaceID, receipt.ArtifactID)
	if err != nil {
		return artifactapp.State{}, err
	}
	if state.Artifact.WorkspaceID != workspaceID || state.Artifact.ID != receipt.ArtifactID || state.Revision.ContentHash != receipt.RevisionHash || state.Artifact.CurrentRevisionID != state.Revision.ID {
		return artifactapp.State{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_ARTIFACT_RESULT_DRIFT", false, "artifact revision differs from the reviewed stage")
	}
	return state, nil
}

// CreateOnlyProposalPort is the Change Control owner command used after merge confirmation.
type CreateOnlyProposalPort interface {
	CreateCreateOnlyFileProposal(context.Context, changecontrolapp.CreateCreateOnlyFileProposalCommand) (changecontrolapp.CreateResult, error)
	GetProposal(context.Context, foundation.ID) (changecontroldomain.Proposal, error)
}

// ProposalReceipt is the immutable Change Control result bound to Organizing.
type ProposalReceipt struct {
	ProposalID foundation.ID
	ChangeHash string
}

// ProposalOwner creates only CREATE_ONLY file_patch proposals.
type ProposalOwner struct{ service CreateOnlyProposalPort }

// NewProposalOwner constructs the Change Control owner adapter.
func NewProposalOwner(service CreateOnlyProposalPort) (*ProposalOwner, error) {
	if service == nil {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, "ORGANIZING_PROPOSAL_OWNER_UNAVAILABLE", true, "change control owner is unavailable")
	}
	return &ProposalOwner{service: service}, nil
}

// CreateMergeProposal creates a reviewable create-only file patch from a reviewed Artifact.
func (owner *ProposalOwner) CreateMergeProposal(ctx context.Context, workspaceID, runID foundation.ID, targetPath string, state artifactapp.State) (ProposalReceipt, error) {
	if owner == nil || owner.service == nil || ctx == nil || !validID(workspaceID) || !validID(runID) || state.Artifact.WorkspaceID != workspaceID {
		return ProposalReceipt{}, workflowError(foundation.ErrorInvalidInput, "ORGANIZING_PROPOSAL_REQUEST_INVALID", false, "organizing merge proposal request is invalid")
	}
	content, err := RenderArtifactMarkdown(state)
	if err != nil {
		return ProposalReceipt{}, err
	}
	result, err := owner.service.CreateCreateOnlyFileProposal(ctx, changecontrolapp.CreateCreateOnlyFileProposalCommand{
		WorkspaceID: workspaceID, TargetPath: targetPath,
		IdempotencyKey: "organizing-merge:" + string(runID), Content: content,
		EvidenceSummary: "由已确认 Organizing Snapshot 的正式证据生成；原材料保持不变。",
		RiskLevel:       changecontroldomain.ProposalRiskLevelMedium,
		Risk:            "合并草稿可能需要人工确认章节组织与保留的差异。",
		RollbackPlan:    "不批准或关闭此 CREATE_ONLY Proposal；原材料和目标目录不会被修改。",
	})
	if err != nil {
		return ProposalReceipt{}, err
	}
	proposal := result.Proposal
	persisted, err := owner.service.GetProposal(ctx, proposal.ID)
	if err != nil {
		return ProposalReceipt{}, err
	}
	if persisted.ID != proposal.ID || persisted.WorkspaceID != workspaceID || persisted.Revision.ID != proposal.Revision.ID ||
		persisted.Revision.ChangeHash != proposal.Revision.ChangeHash || persisted.TargetPath != proposal.TargetPath ||
		persisted.Type != proposal.Type || persisted.Revision.TargetMode != changecontroldomain.TargetModeCreateOnly ||
		!validID(persisted.ID) || !validHash(persisted.Revision.ChangeHash) {
		return ProposalReceipt{}, workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_PROPOSAL_RESULT_INVALID", false, "change control returned an invalid proposal")
	}
	return ProposalReceipt{ProposalID: persisted.ID, ChangeHash: persisted.Revision.ChangeHash}, nil
}

// RenderArtifactMarkdown serializes only the frozen Artifact revision.
func RenderArtifactMarkdown(state artifactapp.State) (string, error) {
	if artifactdomain.ValidateArtifact(state.Artifact) != nil || artifactdomain.ValidateRevision(state.Revision) != nil ||
		state.Artifact.CurrentRevisionID != state.Revision.ID || state.Artifact.ID != state.Revision.ArtifactID {
		return "", workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_ARTIFACT_RESULT_INVALID", false, "artifact state cannot be rendered")
	}
	sections := make(map[string]artifactdomain.Section, len(state.Revision.Sections))
	for _, section := range state.Revision.Sections {
		sections[section.Key] = section
	}
	var document strings.Builder
	document.WriteString("# ")
	document.WriteString(singleLine(state.Artifact.Title))
	document.WriteString("\n")
	for _, outline := range state.Revision.Outline {
		section, found := sections[outline.Key]
		if !found {
			return "", workflowError(foundation.ErrorConsistencyViolation, "ORGANIZING_ARTIFACT_RESULT_INVALID", false, "artifact outline is incomplete")
		}
		document.WriteString("\n## ")
		document.WriteString(singleLine(outline.Title))
		document.WriteString("\n\n")
		if section.Coverage.Status == artifactdomain.CoverageGap {
			document.WriteString("> GAP: ")
			for index, gap := range section.Coverage.Gaps {
				if index > 0 {
					document.WriteString("; ")
				}
				document.WriteString(singleLine(gap.Description))
			}
			document.WriteByte('\n')
			continue
		}
		document.WriteString(section.Content)
		document.WriteByte('\n')
	}
	content := document.String()
	if strings.TrimSpace(content) == "" {
		return "", errors.New("artifact markdown is empty")
	}
	return content, nil
}

func cloneOutline(input []artifactdomain.OutlineSection) []artifactdomain.OutlineSection {
	result := make([]artifactdomain.OutlineSection, len(input))
	copy(result, input)
	return result
}

func singleLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Join(strings.Fields(value), " ")
}
