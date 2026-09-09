package postgres

import (
	"errors"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
)

func validWorkspaceAnalysisTimelineVersions(definitionVersion, policyVersion int64) bool {
	return (definitionVersion == 1 && policyVersion == agentdomain.WorkspaceAnalysisPolicyVersionV1) ||
		(definitionVersion == 2 && policyVersion == agentdomain.WorkspaceAnalysisPolicyVersionV2)
}

func workspaceAnalysisTimelineOperationItemV2(
	run workspaceAnalysisTimelineRunRow,
	row workspaceAnalysisTimelineOperationRow,
	sequence int,
) (conversationdomain.WorkspaceAnalysisTimelineItem, error) {
	if run.definitionVersion != 2 || run.policyVersion != agentdomain.WorkspaceAnalysisPolicyVersionV2 {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, errors.New("workspace analysis v2 timeline is not bound to v2 policy")
	}
	key := agentdomain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: run.analysisRunID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeKey(row.nodeKey),
		Kind: agentdomain.WorkspaceAnalysisOperationKind(row.operationKind), Ordinal: row.ordinal,
	}
	contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(key)
	if err != nil || string(contract.CallKind) != row.callKind {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, errors.New("workspace analysis v2 timeline operation slot is invalid")
	}
	phase, err := workspaceAnalysisTimelineOperationPhaseV2(contract)
	if err != nil {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, err
	}
	status, code, err := workspaceAnalysisTimelineOperationStatus(row, run)
	if err != nil {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, err
	}
	item := conversationdomain.WorkspaceAnalysisTimelineItem{
		Sequence: sequence, Kind: conversationdomain.WorkspaceAnalysisTimelineItemModel, Phase: phase, Status: status,
		DurationMS: row.durationMS, ErrorCode: code,
	}
	if contract.CallKind == agentdomain.WorkspaceAnalysisOperationCallTool {
		item.Kind = conversationdomain.WorkspaceAnalysisTimelineItemTool
		ref, refErr := workspaceAnalysisTimelineToolRefV2(contract.Kind)
		if refErr != nil {
			return conversationdomain.WorkspaceAnalysisTimelineItem{}, refErr
		}
		item.ToolRef = &ref
		if row.status != string(agentdomain.WorkspaceAnalysisOperationPending) &&
			(row.toolName == nil || row.toolVersion == nil || *row.toolName != ref.Name || *row.toolVersion != ref.Version || row.toolCallStatus == nil) {
			return conversationdomain.WorkspaceAnalysisTimelineItem{}, errors.New("workspace analysis v2 timeline tool call binding is incomplete")
		}
	} else if row.status != string(agentdomain.WorkspaceAnalysisOperationPending) && row.modelCallStatus == nil {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, errors.New("workspace analysis v2 timeline model call binding is incomplete")
	}
	if err := validateWorkspaceAnalysisTimelineCallStatusV2(row); err != nil {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, err
	}
	if item.Status == conversationdomain.WorkspaceAnalysisTimelineItemSucceeded {
		item.Summary, err = workspaceAnalysisTimelineOperationSummaryForVersion(contract, row, 2)
		if err != nil {
			return conversationdomain.WorkspaceAnalysisTimelineItem{}, err
		}
	}
	if err := item.ValidateV2(); err != nil {
		return conversationdomain.WorkspaceAnalysisTimelineItem{}, err
	}
	return item, nil
}

func workspaceAnalysisTimelineOperationPhaseV2(contract agentdomain.WorkspaceAnalysisOperationContract) (conversationdomain.WorkspaceAnalysisPhase, error) {
	if contract.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeDecideNext {
		switch contract.Kind {
		case agentdomain.WorkspaceAnalysisOperationDecision:
			return conversationdomain.WorkspaceAnalysisPhaseDecideNext, nil
		case agentdomain.WorkspaceAnalysisOperationGitStatus:
			return conversationdomain.WorkspaceAnalysisPhaseInspectWorkspace, nil
		case agentdomain.WorkspaceAnalysisOperationKnowledgeSearch:
			return conversationdomain.WorkspaceAnalysisPhaseRetrieveEvidence, nil
		case agentdomain.WorkspaceAnalysisOperationSourceRead:
			return conversationdomain.WorkspaceAnalysisPhaseReadEvidence, nil
		case agentdomain.WorkspaceAnalysisOperationCitationValidation:
			return conversationdomain.WorkspaceAnalysisPhaseValidateCitations, nil
		}
	}
	switch {
	case contract.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeSynthesizeAnswer && contract.Kind == agentdomain.WorkspaceAnalysisOperationAnswerSynthesis:
		return conversationdomain.WorkspaceAnalysisPhaseSynthesizeAnswer, nil
	case contract.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeValidateCitations && contract.Kind == agentdomain.WorkspaceAnalysisOperationCitationValidation:
		return conversationdomain.WorkspaceAnalysisPhaseValidateCitations, nil
	case contract.NodeKey == agentdomain.WorkspaceAnalysisOperationNodeReviewPublish && contract.Kind == agentdomain.WorkspaceAnalysisOperationFaithfulnessReview:
		return conversationdomain.WorkspaceAnalysisPhaseReviewPublish, nil
	default:
		return "", errors.New("workspace analysis v2 timeline has a v1 or unknown operation")
	}
}

func workspaceAnalysisTimelineToolRefV2(kind agentdomain.WorkspaceAnalysisOperationKind) (conversationdomain.WorkspaceAnalysisTimelineToolRef, error) {
	switch kind {
	case agentdomain.WorkspaceAnalysisOperationGitStatus:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{Name: "ReadGitStatus", Version: 3}, nil
	case agentdomain.WorkspaceAnalysisOperationKnowledgeSearch:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{Name: "SearchKnowledge", Version: 3}, nil
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{Name: "ReadSource", Version: 4}, nil
	case agentdomain.WorkspaceAnalysisOperationCitationValidation:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{Name: "ValidateCitation", Version: 4}, nil
	default:
		return conversationdomain.WorkspaceAnalysisTimelineToolRef{}, errors.New("workspace analysis v2 timeline tool kind is invalid")
	}
}

func validateWorkspaceAnalysisTimelineCallStatusV2(row workspaceAnalysisTimelineOperationRow) error {
	actual, other := row.modelCallStatus, row.toolCallStatus
	if row.callKind == string(agentdomain.WorkspaceAnalysisOperationCallTool) {
		actual, other = row.toolCallStatus, row.modelCallStatus
	}
	if other != nil || (row.status == string(agentdomain.WorkspaceAnalysisOperationPending) && actual != nil) {
		return errors.New("workspace analysis v2 timeline call namespace is inconsistent")
	}
	if row.status == string(agentdomain.WorkspaceAnalysisOperationPending) {
		return nil
	}
	if actual == nil {
		return errors.New("workspace analysis v2 timeline call is missing")
	}
	// A successful external response can still fail its owned publication/receipt gate.
	if row.status == string(agentdomain.WorkspaceAnalysisOperationFailed) && *actual == "SUCCEEDED" && row.errorCode != nil {
		if row.callKind == string(agentdomain.WorkspaceAnalysisOperationCallModel) && *row.errorCode == string(agentdomain.WorkspaceAnalysisRunModelRefused) {
			return nil
		}
		if row.callKind == string(agentdomain.WorkspaceAnalysisOperationCallTool) && *row.errorCode == string(agentdomain.WorkspaceAnalysisRunReceiptInvalid) {
			return nil
		}
	}
	if *actual != row.status {
		return errors.New("workspace analysis v2 timeline call and operation statuses disagree")
	}
	return nil
}
