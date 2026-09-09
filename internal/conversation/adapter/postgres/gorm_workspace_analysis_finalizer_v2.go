package postgres

import (
	"context"
	"encoding/json"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

func gormValidateWorkspaceAnalysisPersistedDefinition(ctx context.Context, tx *gorm.DB, workspaceID, workflowRunID foundation.ID, version int64, policy int, definitionHash string) error {
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	if version == 2 {
		definition = conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
	}
	if version != definition.Version || int64(policy) != version || definitionHash != definition.GraphHash {
		return workspaceAnalysisTerminationAuthorityError("workspace analysis persisted definition or policy differs")
	}
	var key string
	var storedVersion int64
	var document json.RawMessage
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT d.key,d.version,d.graph
		FROM workflow.run r JOIN workflow.definition d ON d.id=r.definition_id AND d.workspace_id=r.workspace_id
		WHERE r.id=? AND r.workspace_id=? FOR SHARE OF d`, string(workflowRunID), string(workspaceID))).Scan(&key, &storedVersion, &document); err != nil {
		return workspaceAnalysisFinalizerQueryError(err, "workspace analysis persisted definition")
	}
	graph, err := workflowapplication.DecodeCanonicalGraph(document)
	if err != nil {
		return consistency(ErrorCodeWorkspaceAnalysisFinalizeCorrupt, err)
	}
	hash, err := workflowapplication.ComputeCanonicalGraphHash(graph)
	if err != nil || key != definition.Key || storedVersion != version || hash != definitionHash {
		return workspaceAnalysisTerminationAuthorityError("workspace analysis workflow definition binding differs")
	}
	return nil
}

func gormCloseWorkspaceAnalysisDecisionRuns(ctx context.Context, tx *gorm.DB, analysisRunID foundation.ID, version int64, now time.Time) error {
	if version != 2 {
		return nil
	}
	// The decision ModelRun may own a successful prefix when its next operation
	// is denied or cancelled. Its deferred closure is proved by this transaction's
	// terminal proof; constraints must not be forced before that proof is inserted.
	if err := tx.WithContext(ctx).Exec(`SELECT agent.close_workspace_analysis_v2_decision_runs(?::uuid,?)`, string(analysisRunID), now).Error; err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	return nil
}

func gormValidateWorkspaceAnalysisV2SuccessFacts(ctx context.Context, tx *gorm.DB, command conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand) error {
	var closed bool
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		agent.workspace_analysis_v2_decisions_complete(?::uuid)
		AND agent.workspace_analysis_v2_validation_all_valid(?::uuid)
		AND EXISTS (SELECT 1 FROM agent.workspace_analysis_operation
			WHERE analysis_run_id=? AND node_key='validate_citations' AND operation_kind='CITATION_VALIDATION'
			AND ordinal=1 AND status='SUCCEEDED' AND result_id=? AND result_hash=?)`,
		string(command.AnalysisRunID), string(command.AnalysisRunID), string(command.AnalysisRunID), string(command.ValidationReceiptID), command.ValidationReceiptHash)).Scan(&closed); err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if !closed {
		return workspaceAnalysisTerminationAuthorityError("dynamic publication is missing completed decisions or its independent final citation gate")
	}
	var receiptID, receiptHash *string
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT
		(SELECT result_id::text FROM agent.workspace_analysis_operation WHERE analysis_run_id=? AND operation_kind='GIT_STATUS' AND node_key='decide_next' AND status='SUCCEEDED' ORDER BY ordinal DESC LIMIT 1),
		(SELECT result_hash FROM agent.workspace_analysis_operation WHERE analysis_run_id=? AND operation_kind='GIT_STATUS' AND node_key='decide_next' AND status='SUCCEEDED' ORDER BY ordinal DESC LIMIT 1)`, string(command.AnalysisRunID), string(command.AnalysisRunID))).Scan(&receiptID, &receiptHash); err != nil {
		return classify(err, ErrorCodeWorkspaceAnalysisFinalizeUnavailable)
	}
	if (receiptID == nil) != (receiptHash == nil) || (receiptID == nil && command.GitReceiptID != "") ||
		(receiptID != nil && (*receiptID != string(command.GitReceiptID) || *receiptHash != command.GitReceiptHash)) {
		return workspaceAnalysisTerminationAuthorityError("dynamic publication does not use its latest actual Git receipt")
	}
	return nil
}

func gormValidateWorkspaceAnalysisV2EvidenceInsufficient(ctx context.Context, tx *gorm.DB, command conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand, authority workspaceAnalysisTerminationAuthority) (time.Time, error) {
	if authority.Operation == nil || authority.Call.ModelRunID == nil || authority.Operation.ModelCallID == nil || command.Artifact == nil {
		return time.Time{}, workspaceAnalysisTerminationAuthorityError("dynamic evidence-insufficient decision authority is absent")
	}
	var document []byte
	var createdAt time.Time
	var complete, readable bool
	if err := gormScanRow(tx.WithContext(ctx).Raw(`SELECT d.document,d.created_at,
		agent.workspace_analysis_v2_decisions_complete(d.analysis_run_id),
		EXISTS (SELECT 1 FROM agent.workspace_analysis_operation o
		 JOIN workflow.tool_result_receipt receipt ON receipt.id=o.result_id AND receipt.tool_call_id=o.tool_call_id AND receipt.output_hash=o.result_hash
		 WHERE o.analysis_run_id=d.analysis_run_id AND o.node_key='decide_next' AND o.operation_kind='SOURCE_READ' AND o.status='SUCCEEDED'
		 AND receipt.workspace_id=d.workspace_id AND receipt.workflow_run_id=o.workflow_run_id AND receipt.tool_name='ReadSource' AND receipt.tool_version=4
		 AND convert_from(receipt.output_document,'UTF8')::jsonb->'truncated'='false'::jsonb
		 AND agent.workspace_analysis_v2_ref_read(d.analysis_run_id,convert_from(receipt.output_document,'UTF8')::jsonb->>'evidence_ref'))
		FROM agent.workspace_analysis_decision d
		WHERE d.id=? AND d.document_hash=? AND d.workspace_id=? AND d.analysis_run_id=? AND d.operation_id=? AND d.model_run_id=? AND d.model_call_id=?
		FOR SHARE OF d`, string(command.Artifact.ID), command.Artifact.Hash, string(command.WorkspaceID), string(command.AnalysisRunID), string(authority.Operation.ID), string(*authority.Call.ModelRunID), string(*authority.Operation.ModelCallID))).Scan(&document, &createdAt, &complete, &readable); err != nil {
		return time.Time{}, workspaceAnalysisFinalizerQueryError(err, "dynamic evidence-insufficient decision receipt")
	}
	decision, err := agentdomain.DecodeWorkspaceAnalysisDecision(document)
	if err != nil || decision.Action != agentdomain.WorkspaceAnalysisDecisionFinish || workspaceAnalysisDocumentHash(document) != command.Artifact.Hash || !complete || readable {
		return time.Time{}, workspaceAnalysisTerminationAuthorityError("dynamic finish does not prove insufficient readable evidence")
	}
	return createdAt, nil
}
