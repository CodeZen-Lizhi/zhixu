package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"gorm.io/gorm"
)

func validateGORMWorkspaceAnalysisToolDecision(ctx context.Context, db *gorm.DB, c application.PrepareWorkspaceAnalysisToolOperationCommand) error {
	if c.Identity.DefinitionVersion != 2 {
		return nil
	}
	if c.OperationKey.NodeKey != domain.WorkspaceAnalysisOperationNodeDecideNext {
		if c.OperationKey.NodeKey != domain.WorkspaceAnalysisOperationNodeValidateCitations || c.OperationKey.Kind != domain.WorkspaceAnalysisOperationCitationValidation || c.OperationKey.Ordinal != 1 {
			return workspaceAnalysisModelConflict(errors.New("dynamic tool node is not allowed"))
		}
		return nil
	}
	var row struct {
		Document []byte `gorm:"column:document"`
	}
	result := db.Table("agent.workspace_analysis_decision AS decision").Select("decision.document").Joins("JOIN agent.workspace_analysis_operation AS operation ON operation.id=decision.operation_id AND operation.analysis_run_id=decision.analysis_run_id AND operation.workspace_id=decision.workspace_id").Where("decision.workspace_id=? AND decision.analysis_run_id=? AND decision.ordinal=? AND operation.workflow_run_id=? AND operation.node_run_id=? AND operation.status=?", string(c.Identity.WorkspaceID), string(c.OperationKey.AnalysisRunID), c.OperationKey.Ordinal, string(c.Identity.WorkflowRunID), string(c.Identity.NodeRunID), string(domain.WorkspaceAnalysisOperationSucceeded)).Take(&row)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return workspaceAnalysisModelConflict(errors.New("dynamic tool has no successful decision"))
		}
		return classifyGORM(ctx, result.Error)
	}
	decision, err := domain.DecodeWorkspaceAnalysisDecision(row.Document)
	if err != nil {
		return consistency(err)
	}
	var input struct {
		Query         string   `json:"query"`
		Mode          string   `json:"mode"`
		Limit         int      `json:"limit"`
		EvidenceRef   string   `json:"evidence_ref"`
		EvidenceRefs  []string `json:"evidence_refs"`
		CandidateID   *string  `json:"candidate_id"`
		CandidateHash *string  `json:"candidate_hash"`
	}
	if len(c.Arguments) == 0 || json.Unmarshal(c.Arguments, &input) != nil {
		return workspaceAnalysisModelConflict(errors.New("dynamic tool arguments are required"))
	}
	valid := false
	switch c.OperationKey.Kind {
	case domain.WorkspaceAnalysisOperationGitStatus:
		valid = decision.Action == domain.WorkspaceAnalysisDecisionGitStatus
	case domain.WorkspaceAnalysisOperationKnowledgeSearch:
		valid = decision.Action == domain.WorkspaceAnalysisDecisionKnowledgeSearch && decision.Query != nil && *decision.Query == input.Query && input.Limit >= 1 && input.Limit <= 5 && (input.Mode == "hybrid" || input.Mode == "keyword" || input.Mode == "semantic")
		if valid {
			var count int64
			result := db.Table("agent.workspace_analysis_evidence AS evidence").Joins("JOIN agent.workspace_analysis_operation AS search_operation ON search_operation.id=evidence.search_operation_id").Where("evidence.workspace_id=? AND evidence.analysis_run_id=? AND search_operation.ordinal<?", string(c.Identity.WorkspaceID), string(c.OperationKey.AnalysisRunID), c.OperationKey.Ordinal).Count(&count)
			if result.Error != nil {
				return classifyGORM(ctx, result.Error)
			}
			// Zero capacity is a durable admission denial after PENDING insertion.
			valid = count == domain.WorkspaceAnalysisV2MaxEvidenceRefs || count >= 0 && count < domain.WorkspaceAnalysisV2MaxEvidenceRefs && input.Limit <= min(5, domain.WorkspaceAnalysisV2MaxEvidenceRefs-int(count))
		}
	case domain.WorkspaceAnalysisOperationSourceRead:
		valid = decision.Action == domain.WorkspaceAnalysisDecisionSourceRead && decision.EvidenceRef != nil && *decision.EvidenceRef == input.EvidenceRef
	case domain.WorkspaceAnalysisOperationCitationValidation:
		valid = decision.Action == domain.WorkspaceAnalysisDecisionCitationValidation && slices.Equal(decision.EvidenceRefs, input.EvidenceRefs) && input.CandidateID == nil && input.CandidateHash == nil
	}
	if !valid {
		return workspaceAnalysisModelConflict(errors.New("dynamic tool request differs from the durable model decision"))
	}
	return nil
}
