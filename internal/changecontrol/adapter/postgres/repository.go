package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledge "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"io"
	"reflect"
	"strings"
	"time"
)

func downstreamImpactConflict(message string) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "KNOWLEDGE_IMPACT_CONFLICT", false, errors.New(message))
}

// proposalRequestHashReplayMatches 保持单向兼容：历史 v1 可由同一 v1 或等价 v2 重放，
// 新建 v2 只能由 v2 重放，禁止 v1 请求反向匹配 v2 持久事实。
func proposalRequestHashReplayMatches(storedHash, requestedHash, requestHashV2, requestHashV1 string) bool {
	switch storedHash {
	case requestHashV2:
		return requestedHash == requestHashV2
	case requestHashV1:
		return requestedHash == requestHashV1 || requestedHash == requestHashV2
	default:
		return false
	}
}

func buildProposalListQuery(request domain.ProposalListQuery) (string, []any) {
	query := `SELECT p.id::text,p.workspace_id::text,p.proposal_type,p.status,p.created_at,p.updated_at,p.version,p.current_revision_id::text,
				r.id::text,CASE p.proposal_type
					WHEN 'file_patch' THEN COALESCE(r.target_path,'')
					WHEN 'restore_document' THEN COALESCE(r.target_path,'')
					WHEN 'knowledge_change' THEN '知识关系'
					WHEN 'publish_artifact' THEN 'Artifact 发布'
					WHEN 'downstream_update' THEN COALESCE(r.downstream_target_type || ':' || r.downstream_target_id::text,'下游更新')
					ELSE '' END,
				p.risk_level,r.risk,r.change_hash,r.target_mode,COALESCE(octet_length(r.content),0),
				(CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END)::text,
				wr.status,
		a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at
		FROM change_control.proposal p
		JOIN LATERAL (
				SELECT id,target_path,target_mode,content,risk,change_hash,downstream_target_type,downstream_target_id
			FROM change_control.proposal_revision
				WHERE proposal_id=p.id AND (id=p.current_revision_id OR p.current_revision_id IS NULL) ORDER BY revision_no DESC LIMIT 1
		) r ON true
		LEFT JOIN change_control.approval a ON a.revision_id=r.id
		LEFT JOIN change_control.proposal_revision_dispatch d
		  ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
		LEFT JOIN workflow.run wr
		  ON wr.id=CASE WHEN p.current_revision_id IS NULL THEN p.workflow_run_id ELSE d.workflow_run_id END
		 AND wr.workspace_id=p.workspace_id
		WHERE p.workspace_id=?`
	args := []any{string(request.WorkspaceID)}
	appendFilter := func(column string, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		query += ` AND ` + column + `=?`
	}
	appendFilter("p.status", string(request.Status))
	appendFilter("p.proposal_type", string(request.Type))
	appendFilter("p.risk_level", string(request.RiskLevel))
	if request.CreatedAfter != nil {
		args = append(args, request.CreatedAfter.UTC())
		query += ` AND p.created_at>=?`
	}
	if request.CursorTime != nil {
		args = append(args, request.CursorTime.UTC(), string(request.CursorID))
		query += ` AND (p.updated_at,p.id)<(?,?)`
	}
	query += ` ORDER BY p.updated_at DESC,p.id DESC LIMIT ?`
	args = append(args, request.Limit+1)
	return query, args
}

func scanAuthorization(row interface{ Scan(...any) error }) (domain.ToolAuthorization, error) {
	var authorization domain.ToolAuthorization
	var id, workspaceID, runID, nodeID, proposalID, revisionID, approvalID, capability, status string
	if err := row.Scan(&id, &workspaceID, &runID, &nodeID, &proposalID, &revisionID, &approvalID, &authorization.ToolName, &capability, &authorization.Scope, &authorization.ApprovedChangeHash, &authorization.TargetMode, &authorization.TargetVersion, &authorization.TokenHash, &authorization.IdempotencyKey, &status, &authorization.IssuedAt, &authorization.ExpiresAt, &authorization.RevokedAt, &authorization.ConsumedAt, &authorization.Version); err != nil {
		return domain.ToolAuthorization{}, err
	}
	authorization.ID, authorization.WorkspaceID, authorization.WorkflowRunID, authorization.NodeRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(runID), foundation.ID(nodeID)
	authorization.ProposalID, authorization.RevisionID, authorization.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
	authorization.Capability, authorization.Status = domain.Capability(capability), domain.AuthorizationStatus(status)
	authorization.TargetMode = domain.NormalizeTargetMode(authorization.TargetMode)
	return authorization, nil
}

func sameAuthorizationIdentity(existing, requested domain.ToolAuthorization) bool {
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID && existing.NodeRunID == requested.NodeRunID && existing.ProposalID == requested.ProposalID && existing.RevisionID == requested.RevisionID && existing.ApprovalID == requested.ApprovalID && existing.ToolName == requested.ToolName && existing.Capability == requested.Capability && existing.Scope == requested.Scope && existing.ApprovedChangeHash == requested.ApprovedChangeHash && domain.NormalizeTargetMode(existing.TargetMode) == domain.NormalizeTargetMode(requested.TargetMode) && existing.TargetVersion == requested.TargetVersion && existing.IdempotencyKey == requested.IdempotencyKey && existing.ExpiresAt.Sub(existing.IssuedAt) == requested.ExpiresAt.Sub(requested.IssuedAt)
}

func sameAuthorizationBinding(authorization domain.ToolAuthorization, request domain.AuthorizationConsume) bool {
	return domain.ValidateAuthorizationConsumeBinding(authorization, request) == nil
}

func classifyAuthorization(err error, code string) error {
	if err == nil {
		return nil
	}
	if sqlState := platformpostgres.SQLState(err); sqlState != "" {
		switch sqlState {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT", false, err)
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}

func authorizationTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func scanProposal(row interface{ Scan(...any) error }) (domain.Proposal, error) {
	var proposalID, workspaceID, proposalType, idempotencyKey, requestHash, riskLevel, status string
	var currentRevisionID *string
	var workflowRunID, workflowRunStatus *string
	var revisionID, risk, rollback, changeHash string
	var targetPath, targetMode, baseHash, content, evidence, artifactID, artifactRevisionID, artifactContentHash, schemaVersion *string
	var downstreamWorkspaceID, downstreamReportID, downstreamAnalysisVersion, downstreamReportFingerprint *string
	var downstreamSourceEventID, downstreamTargetType, downstreamTargetID, downstreamAction, downstreamReason *string
	var restoreWorkspaceID, restoreDocumentID, restoreTargetCommit, restoreExpectedHead *string
	var restorePreviewHash, restoreCurrentContentHash, restoreTargetContentHash *string
	var artifactRevisionNo, artifactVersion, downstreamSourceEventVersion, downstreamBaseVersion *int64
	var restoreExpectedDocumentVersion *int64
	var targetRefsRaw, baseVersionsRaw, changeSetRaw, evidenceRefsRaw, artifactSourceCoverageRaw, downstreamOwnerBindingRaw []byte
	var approvalID, approvalHash, decision, approvedGitHead *string
	var createdAt, updatedAt, revisionCreatedAt time.Time
	var decidedAt *time.Time
	var revisionNo int
	var version int64
	err := row.Scan(
		&proposalID, &workspaceID, &proposalType, &idempotencyKey, &requestHash, &riskLevel, &workflowRunID, &workflowRunStatus, &status, &version, &createdAt, &updatedAt, &currentRevisionID,
		&revisionID, &revisionNo, &targetPath, &targetMode, &baseHash, &content, &evidence, &risk, &rollback, &changeHash,
		&targetRefsRaw, &baseVersionsRaw, &changeSetRaw, &evidenceRefsRaw,
		&artifactID, &artifactRevisionID, &artifactRevisionNo, &artifactVersion, &artifactContentHash, &artifactSourceCoverageRaw,
		&downstreamWorkspaceID, &downstreamReportID, &downstreamAnalysisVersion, &downstreamReportFingerprint,
		&downstreamSourceEventID, &downstreamSourceEventVersion, &downstreamTargetType, &downstreamTargetID,
		&downstreamBaseVersion, &downstreamAction, &downstreamOwnerBindingRaw, &downstreamReason, &schemaVersion, &revisionCreatedAt,
		&restoreWorkspaceID, &restoreDocumentID, &restoreTargetCommit, &restoreExpectedHead, &restoreExpectedDocumentVersion,
		&restorePreviewHash, &restoreCurrentContentHash, &restoreTargetContentHash,
		&approvalID, &approvalHash, &decision, &approvedGitHead, &decidedAt,
	)
	if err != nil {
		return domain.Proposal{}, err
	}
	proposal := domain.Proposal{
		ID: foundation.ID(proposalID), WorkspaceID: foundation.ID(workspaceID), Type: domain.ProposalType(proposalType),
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, RiskLevel: domain.ProposalRiskLevel(riskLevel),
		Status: domain.ProposalStatus(status), Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt,
		Revision: domain.Revision{
			ID: foundation.ID(revisionID), ProposalID: foundation.ID(proposalID), RevisionNo: revisionNo,
			Risk: risk, RollbackPlan: rollback, ChangeHash: changeHash, CreatedAt: revisionCreatedAt,
		},
	}
	if currentRevisionID != nil {
		proposal.CurrentRevisionID = foundation.ID(*currentRevisionID)
	}
	proposal.Type = domain.NormalizeProposalType(proposal.Type)
	parsedRiskLevel, riskErr := domain.ValidateProposalRiskLevelForType(proposal.Type, domain.ProposalRiskLevel(riskLevel))
	if riskErr != nil {
		return domain.Proposal{}, riskErr
	}
	proposal.RiskLevel = parsedRiskLevel
	downstreamFieldsPresent := downstreamWorkspaceID != nil || downstreamReportID != nil || downstreamAnalysisVersion != nil || downstreamReportFingerprint != nil ||
		downstreamSourceEventID != nil || downstreamSourceEventVersion != nil || downstreamTargetType != nil || downstreamTargetID != nil ||
		downstreamBaseVersion != nil || downstreamAction != nil || downstreamOwnerBindingRaw != nil || downstreamReason != nil
	restoreFieldsPresent := restoreWorkspaceID != nil || restoreDocumentID != nil || restoreTargetCommit != nil || restoreExpectedHead != nil ||
		restoreExpectedDocumentVersion != nil || restorePreviewHash != nil || restoreCurrentContentHash != nil || restoreTargetContentHash != nil
	switch domain.NormalizeProposalType(proposal.Type) {
	case domain.ProposalTypeFilePatch:
		if targetPath == nil || targetMode == nil || baseHash == nil || content == nil || evidence == nil || schemaVersion != nil ||
			targetRefsRaw != nil || baseVersionsRaw != nil || changeSetRaw != nil || evidenceRefsRaw != nil || artifactID != nil || artifactRevisionID != nil || artifactRevisionNo != nil || artifactVersion != nil || artifactContentHash != nil || artifactSourceCoverageRaw != nil || downstreamFieldsPresent || restoreFieldsPresent {
			return domain.Proposal{}, errors.New("file patch proposal revision payload is inconsistent")
		}
		proposal.Type = domain.ProposalTypeFilePatch
		proposal.TargetPath = *targetPath
		proposal.Revision.TargetPath = *targetPath
		mode, modeErr := domain.ValidateTargetMode(domain.TargetMode(*targetMode))
		if modeErr != nil || domain.ValidateTargetBaseVersion(proposal.WorkspaceID, *targetPath, mode, *baseHash) != nil {
			return domain.Proposal{}, errors.New("file patch target mode is inconsistent")
		}
		proposal.Revision.TargetMode = mode
		proposal.Revision.BaseHash = *baseHash
		proposal.Revision.Content = *content
		proposal.Revision.EvidenceSummary = *evidence
	case domain.ProposalTypeRestoreDocument:
		if targetPath == nil || targetMode == nil || *targetMode != string(domain.TargetModeReplace) || baseHash == nil || content == nil || evidence == nil || schemaVersion == nil ||
			targetRefsRaw != nil || baseVersionsRaw != nil || changeSetRaw != nil || evidenceRefsRaw != nil || artifactID != nil || artifactRevisionID != nil || artifactRevisionNo != nil || artifactVersion != nil || artifactContentHash != nil || artifactSourceCoverageRaw != nil || downstreamFieldsPresent ||
			restoreWorkspaceID == nil || restoreDocumentID == nil || restoreTargetCommit == nil || restoreExpectedHead == nil || restoreExpectedDocumentVersion == nil || restorePreviewHash == nil || restoreCurrentContentHash == nil || restoreTargetContentHash == nil {
			return domain.Proposal{}, errors.New("restore document proposal revision payload is inconsistent")
		}
		restore, err := domain.ValidateRestoreDocument(domain.RestoreDocument{
			WorkspaceID: foundation.ID(*restoreWorkspaceID), DocumentID: foundation.ID(*restoreDocumentID),
			TargetCommit: *restoreTargetCommit, ExpectedHead: *restoreExpectedHead,
			ExpectedDocumentVersion: *restoreExpectedDocumentVersion, PreviewHash: *restorePreviewHash,
			CurrentContentHash: *restoreCurrentContentHash, TargetContentHash: *restoreTargetContentHash,
			SchemaVersion: *schemaVersion,
		})
		if err != nil || restore.WorkspaceID != proposal.WorkspaceID {
			return domain.Proposal{}, errors.New("restore document workspace binding is inconsistent")
		}
		proposal.TargetPath = *targetPath
		proposal.Revision.TargetPath = *targetPath
		proposal.Revision.TargetMode = domain.TargetModeReplace
		proposal.Revision.BaseHash = *baseHash
		proposal.Revision.Content = *content
		proposal.Revision.EvidenceSummary = *evidence
		proposal.Revision.RestoreDocument = &restore
	case domain.ProposalTypeKnowledgeChange:
		if targetPath != nil || targetMode == nil || *targetMode != string(domain.TargetModeReplace) || baseHash != nil || content != nil || evidence != nil || schemaVersion == nil ||
			targetRefsRaw == nil || baseVersionsRaw == nil || changeSetRaw == nil || evidenceRefsRaw == nil || artifactID != nil || artifactRevisionID != nil || artifactRevisionNo != nil || artifactVersion != nil || artifactContentHash != nil || artifactSourceCoverageRaw != nil || downstreamFieldsPresent || restoreFieldsPresent {
			return domain.Proposal{}, errors.New("knowledge change proposal revision payload is inconsistent")
		}
		change := domain.KnowledgeChange{SchemaVersion: *schemaVersion}
		if err := json.Unmarshal(targetRefsRaw, &change.TargetRefs); err != nil {
			return domain.Proposal{}, err
		}
		if err := json.Unmarshal(baseVersionsRaw, &change.BaseVersions); err != nil {
			return domain.Proposal{}, err
		}
		if err := json.Unmarshal(changeSetRaw, &change.ChangeSet); err != nil {
			return domain.Proposal{}, err
		}
		if err := json.Unmarshal(evidenceRefsRaw, &change.EvidenceRefs); err != nil {
			return domain.Proposal{}, err
		}
		canonical, err := domain.ValidateKnowledgeChange(change)
		if err != nil {
			return domain.Proposal{}, err
		}
		proposal.Type = domain.ProposalTypeKnowledgeChange
		proposal.Revision.KnowledgeChange = &canonical
	case domain.ProposalTypePublishArtifact:
		if targetPath != nil || targetMode == nil || *targetMode != string(domain.TargetModeReplace) || baseHash != nil || content != nil || evidence != nil || targetRefsRaw != nil || baseVersionsRaw != nil || changeSetRaw != nil || evidenceRefsRaw != nil ||
			artifactID == nil || artifactRevisionID == nil || artifactRevisionNo == nil || artifactVersion == nil || artifactContentHash == nil || artifactSourceCoverageRaw == nil || schemaVersion == nil || downstreamFieldsPresent || restoreFieldsPresent {
			return domain.Proposal{}, errors.New("publish artifact proposal revision payload is inconsistent")
		}
		publication := domain.PublishArtifact{
			WorkspaceID: proposal.WorkspaceID, ArtifactID: foundation.ID(*artifactID), RevisionID: foundation.ID(*artifactRevisionID),
			RevisionNo: *artifactRevisionNo, ArtifactVersion: *artifactVersion, ContentHash: *artifactContentHash, SchemaVersion: *schemaVersion,
		}
		if err := json.Unmarshal(artifactSourceCoverageRaw, &publication.SourceCoverage); err != nil {
			return domain.Proposal{}, err
		}
		canonical, err := domain.ValidatePublishArtifact(publication)
		if err != nil {
			return domain.Proposal{}, err
		}
		proposal.Type = domain.ProposalTypePublishArtifact
		proposal.Revision.PublishArtifact = &canonical
	case domain.ProposalTypeDownstreamUpdate:
		if targetPath != nil || targetMode == nil || *targetMode != string(domain.TargetModeReplace) || baseHash != nil || content != nil || evidence != nil || targetRefsRaw != nil || baseVersionsRaw != nil || changeSetRaw != nil || evidenceRefsRaw != nil ||
			artifactID != nil || artifactRevisionID != nil || artifactRevisionNo != nil || artifactVersion != nil || artifactContentHash != nil || artifactSourceCoverageRaw != nil ||
			downstreamWorkspaceID == nil || downstreamReportID == nil || downstreamAnalysisVersion == nil || downstreamReportFingerprint == nil ||
			downstreamSourceEventID == nil || downstreamSourceEventVersion == nil || downstreamTargetType == nil || downstreamTargetID == nil ||
			downstreamBaseVersion == nil || downstreamAction == nil || downstreamOwnerBindingRaw == nil || downstreamReason == nil || schemaVersion == nil || restoreFieldsPresent {
			return domain.Proposal{}, errors.New("downstream update proposal revision payload is inconsistent")
		}
		var ownerBinding knowledge.EventOwnerBinding
		if err := decodeStrictJSON(downstreamOwnerBindingRaw, &ownerBinding); err != nil {
			return domain.Proposal{}, err
		}
		update := domain.DownstreamUpdate{
			WorkspaceID: foundation.ID(*downstreamWorkspaceID), ReportID: foundation.ID(*downstreamReportID),
			AnalysisVersion: knowledge.ImpactAnalysisVersion(*downstreamAnalysisVersion), ReportFingerprint: *downstreamReportFingerprint,
			SourceEventID: foundation.ID(*downstreamSourceEventID), SourceEventVersion: *downstreamSourceEventVersion,
			TargetType: knowledge.ImpactObjectType(*downstreamTargetType), TargetID: foundation.ID(*downstreamTargetID), BaseVersion: *downstreamBaseVersion,
			Action: knowledge.ImpactAction(*downstreamAction), OwnerBinding: ownerBinding, Reason: *downstreamReason, SchemaVersion: *schemaVersion,
		}
		canonical, err := domain.ValidateDownstreamUpdate(update)
		if err != nil || canonical.WorkspaceID != proposal.WorkspaceID {
			if err == nil {
				err = errors.New("downstream update workspace binding is inconsistent")
			}
			return domain.Proposal{}, err
		}
		proposal.Type = domain.ProposalTypeDownstreamUpdate
		proposal.Revision.DownstreamUpdate = &canonical
	default:
		return domain.Proposal{}, errors.New("proposal type is unsupported")
	}
	if err := domain.ValidateProposalRevisionForType(proposal.Type, proposal.Revision); err != nil {
		return domain.Proposal{}, err
	}
	if workflowRunID != nil {
		if workflowRunStatus == nil {
			return domain.Proposal{}, errors.New("proposal workflow status binding is incomplete")
		}
		if !domain.ProposalSupportsFileWriteback(proposal.Type) {
			return domain.Proposal{}, errors.New("typed proposal has an invalid writeback workflow binding")
		}
		value := foundation.ID(*workflowRunID)
		proposal.WorkflowRunID = &value
		proposal.WorkflowRunStatus = *workflowRunStatus
	} else if workflowRunStatus != nil {
		return domain.Proposal{}, errors.New("proposal workflow status exists without a workflow binding")
	}
	if approvalID != nil && approvalHash != nil && decision != nil && decidedAt != nil {
		if !domain.ProposalSupportsFileWriteback(proposal.Type) && approvedGitHead != nil {
			return domain.Proposal{}, errors.New("typed proposal has an invalid git approval binding")
		}
		proposal.Approval = &domain.Approval{
			ID: foundation.ID(*approvalID), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
			ChangeHash: *approvalHash, Decision: domain.Decision(*decision), ApprovedGitHead: approvedGitHead, DecidedAt: *decidedAt,
		}
	}
	return proposal, nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("json payload contains multiple values")
		}
		return err
	}
	return nil
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(*left, *right)
}

func isNilChangeControlDependency(value any) bool {
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

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	if sqlState := platformpostgres.SQLState(err); sqlState != "" {
		switch sqlState {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_ALREADY_DECIDED", false, err)
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}
