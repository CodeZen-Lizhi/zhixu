package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

var _ application.WorkspaceAnalysisDecisionRepository = (*GORMWorkspaceAnalysisRepository)(nil)

// FinalizeWorkspaceAnalysisDecision settles one real Call. Intermediate success
// retains the ModelRun; finish, failure and uncertainty terminate that run.
func (repository *GORMWorkspaceAnalysisRepository) FinalizeWorkspaceAnalysisDecision(ctx context.Context, command application.FinalizeWorkspaceAnalysisDecisionCommand) (application.WorkspaceAnalysisDecisionMutationResult, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelInvalid(errors.New("decision context is nil"))
	}
	if err := command.Validate(); err != nil {
		return application.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	var result application.WorkspaceAnalysisDecisionMutationResult
	callbackSucceeded := false
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, scope foundation.TransactionScope) error {
		binding := application.AuthorizeWorkspaceAnalysisModelCallCommand{Identity: command.Identity, OperationKey: command.OperationKey, OperationID: command.OperationID}
		locked, err := gormLockWorkspaceAnalysisModelOperation(callbackCtx, transaction, scope, repository.fence, binding, false)
		if err != nil {
			return err
		}
		if err := validateWorkspaceAnalysisModelDefinition(locked, command.Identity); err != nil {
			return err
		}
		if locked.operation.id != command.OperationID || locked.modelRun == nil || locked.modelCall == nil || locked.reservation == nil ||
			locked.modelRun.NodeAttemptID != command.Identity.NodeAttemptID {
			return workspaceAnalysisModelConflict(errors.New("decision completion has a different durable owner"))
		}
		binding.Run, binding.Call, binding.ReservationID = *locked.modelRun, *locked.modelCall, locked.reservation.ID
		if err := gormValidateWorkspaceAnalysisModelLockedBinding(callbackCtx, transaction, locked, binding); err != nil {
			return err
		}
		if locked.operation.status != domain.WorkspaceAnalysisOperationStarted {
			result = application.WorkspaceAnalysisDecisionMutationResult{Run: *locked.modelRun, Call: *locked.modelCall,
				Operation: locked.operation.domain(&locked.reservation.ID), Replayed: true}
			if locked.operation.status == domain.WorkspaceAnalysisOperationSucceeded {
				receipt, loadErr := gormLoadWorkspaceAnalysisDecision(callbackCtx, transaction, locked.operation.id)
				if loadErr != nil {
					return loadErr
				}
				result.Decision = &receipt
			}
			if err := validateWorkspaceAnalysisDecisionReplay(result, command); err != nil {
				return err
			}
			callbackSucceeded = true
			return nil
		}
		if locked.operation.latestAttemptID == nil || *locked.operation.latestAttemptID != command.Identity.NodeAttemptID ||
			locked.modelCall.Version != command.ExpectedCallVersion || locked.modelRun.Status != domain.ModelRunRunning {
			return workspaceAnalysisModelConflict(errors.New("decision completion attempt or version changed"))
		}
		if command.Status == domain.ModelCallSucceeded {
			if err := validateWorkspaceAnalysisModelFence(locked, command.Identity); err != nil {
				return workspaceAnalysisModelSuccessfulFinalizationFenceError(locked, command.Identity, err)
			}
		} else if err := validateWorkspaceAnalysisModelFailureTerminalFence(locked, command.Identity, command.Status); err != nil {
			return err
		}
		result, err = gormFinalizeWorkspaceAnalysisDecision(callbackCtx, transaction, locked, command)
		if err != nil {
			return err
		}
		if err := gormForceWorkspaceAnalysisModelConstraints(callbackCtx, transaction); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if err != nil && callbackSucceeded {
		journal, recoveryErr := repository.LoadWorkspaceAnalysisJournal(ctx, application.WorkspaceAnalysisJournalQuery{
			WorkspaceID: command.Identity.WorkspaceID, AnalysisRunID: command.OperationKey.AnalysisRunID, WorkflowRunID: command.Identity.WorkflowRunID})
		if recoveryErr == nil {
			for _, entry := range journal.Entries {
				if entry.Operation.ID == command.OperationID && entry.ModelRun != nil && entry.ModelCall != nil {
					recovered := application.WorkspaceAnalysisDecisionMutationResult{Run: *entry.ModelRun, Call: *entry.ModelCall, Operation: entry.Operation, Decision: entry.Decision, Replayed: true}
					if replayErr := validateWorkspaceAnalysisDecisionReplay(recovered, command); replayErr == nil {
						return recovered, nil
					} else {
						recoveryErr = replayErr
					}
					break
				}
			}
			if recoveryErr == nil {
				recoveryErr = errors.New("decision commit has no exact terminal receipt")
			}
		}
		return application.WorkspaceAnalysisDecisionMutationResult{}, workspaceAnalysisModelRecoveryUnknown(err, recoveryErr)
	}
	if err != nil {
		return application.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	return result, nil
}

func gormFinalizeWorkspaceAnalysisDecision(ctx context.Context, transaction *gorm.DB, locked workspaceAnalysisModelLocks, command application.FinalizeWorkspaceAnalysisDecisionCommand) (application.WorkspaceAnalysisDecisionMutationResult, error) {
	at := locked.fence.databaseNow
	run, call := *locked.modelRun, *locked.modelCall
	call.Status, call.Usage, call.LatencyMillis, call.ErrorCode = command.Status, command.Usage, command.LatencyMillis, command.ErrorCode
	call.Version, call.CompletedAt = call.Version+1, &at
	var receipt *domain.WorkspaceAnalysisDecisionReceipt
	var resultRef *domain.WorkspaceAnalysisOperationResultRef
	var document []byte
	operationStatus := domain.WorkspaceAnalysisOperationFailed
	if command.Status == domain.ModelCallSucceeded {
		var err error
		document, err = command.Decision.Canonical()
		if err != nil {
			return application.WorkspaceAnalysisDecisionMutationResult{}, err
		}
		digest := sha256.Sum256(document)
		call.ResponseHash, call.ResponseBytes = hex.EncodeToString(digest[:]), int64(len(document))
		receipt = &domain.WorkspaceAnalysisDecisionReceipt{ID: command.ReceiptID, WorkspaceID: run.WorkspaceID,
			AnalysisRunID: locked.analysisRun.ID, OperationID: locked.operation.id, NodeAttemptID: run.NodeAttemptID,
			ModelRunID: run.ID, ModelCallID: call.ID, Ordinal: locked.operation.ordinal, Decision: *command.Decision,
			DocumentHash: call.ResponseHash, DocumentBytes: call.ResponseBytes, CreatedAt: at}
		if err := receipt.Validate(); err != nil {
			return application.WorkspaceAnalysisDecisionMutationResult{}, err
		}
		resultRef = &domain.WorkspaceAnalysisOperationResultRef{Kind: domain.WorkspaceAnalysisOperationResultDecision, ID: receipt.ID, Hash: receipt.DocumentHash}
		operationStatus = domain.WorkspaceAnalysisOperationSucceeded
		if command.Decision.Action == domain.WorkspaceAnalysisDecisionFinish {
			run.Status, run.FinalResultType = domain.ModelRunSucceeded, domain.ResultTypeWorkspaceAnalysisDecision
		}
	} else {
		run.Status, run.FinalErrorCode = domain.ModelRunFailed, command.ErrorCode
		if command.Status == domain.ModelCallUnknown {
			run.Status, operationStatus = domain.ModelRunUnknown, domain.WorkspaceAnalysisOperationUnknown
		}
	}
	if err := domain.ValidateModelCall(call); err != nil {
		return application.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	if err := gormUpdateWorkspaceAnalysisModelCall(ctx, transaction, command.ExpectedCallVersion, call); err != nil {
		return application.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	if receipt != nil {
		insert := transaction.WithContext(ctx).Exec(`INSERT INTO agent.workspace_analysis_decision(
			id,workspace_id,analysis_run_id,operation_id,node_attempt_id,model_run_id,model_call_id,ordinal,
			document,document_hash,document_bytes,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			string(receipt.ID), string(receipt.WorkspaceID), string(receipt.AnalysisRunID), string(receipt.OperationID), string(receipt.NodeAttemptID),
			string(receipt.ModelRunID), string(receipt.ModelCallID), receipt.Ordinal, document, receipt.DocumentHash, receipt.DocumentBytes, at)
		if insert.Error != nil {
			return application.WorkspaceAnalysisDecisionMutationResult{}, classifyGORM(ctx, insert.Error)
		}
		if insert.RowsAffected != 1 {
			return application.WorkspaceAnalysisDecisionMutationResult{}, consistency(errors.New("decision receipt was not inserted"))
		}
	}
	if run.Status != domain.ModelRunRunning {
		run.Version, run.UpdatedAt, run.CompletedAt = run.Version+1, at, &at
		if err := domain.ValidateModelRun(run); err != nil {
			return application.WorkspaceAnalysisDecisionMutationResult{}, err
		}
		if err := gormUpdateWorkspaceAnalysisModelRun(ctx, transaction, locked.modelRun.Version, run); err != nil {
			return application.WorkspaceAnalysisDecisionMutationResult{}, err
		}
	}
	unknown := call.Status == domain.ModelCallUnknown
	if err := gormSettleWorkspaceAnalysisModelReservation(ctx, transaction, *locked.reservation, call, at, unknown); err != nil {
		return application.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	if err := gormSettleWorkspaceAnalysisModelRunBudget(ctx, transaction, locked.analysisRun, *locked.reservation, call, at, unknown); err != nil {
		return application.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	if err := gormCompleteWorkspaceAnalysisModelOperation(ctx, transaction, locked.operation, command.Identity.NodeAttemptID, operationStatus, resultRef, call.ErrorCode, at); err != nil {
		return application.WorkspaceAnalysisDecisionMutationResult{}, err
	}
	operation := locked.operation.domain(&locked.reservation.ID)
	operation.Status, operation.Result, operation.ErrorCode = operationStatus, resultRef, call.ErrorCode
	operation.Version, operation.UpdatedAt, operation.CompletedAt = operation.Version+1, at, &at
	return application.WorkspaceAnalysisDecisionMutationResult{Run: run, Call: call, Operation: operation, Decision: receipt}, nil
}

func validateWorkspaceAnalysisDecisionReplay(result application.WorkspaceAnalysisDecisionMutationResult, command application.FinalizeWorkspaceAnalysisDecisionCommand) error {
	if result.Operation.ID != command.OperationID || result.Operation.LogicalKey() != command.OperationKey ||
		result.Run.WorkspaceID != command.Identity.WorkspaceID || result.Run.WorkflowRunID != command.Identity.WorkflowRunID || result.Run.NodeRunID != command.Identity.NodeRunID ||
		result.Run.NodeAttemptID != command.Identity.NodeAttemptID || result.Call.ModelRunID != result.Run.ID ||
		result.Call.Status != command.Status || result.Call.Version != command.ExpectedCallVersion+1 || result.Call.Usage != command.Usage ||
		result.Call.ErrorCode != command.ErrorCode || result.Call.LatencyMillis != command.LatencyMillis {
		return workspaceAnalysisModelConflict(errors.New("decision terminal replay differs"))
	}
	if command.Status == domain.ModelCallSucceeded {
		if result.Decision == nil || result.Decision.ID != command.ReceiptID || result.Operation.Status != domain.WorkspaceAnalysisOperationSucceeded {
			return workspaceAnalysisModelConflict(errors.New("decision receipt replay differs"))
		}
		expected, err := command.Decision.Canonical()
		actual, actualErr := result.Decision.Decision.Canonical()
		if err != nil || actualErr != nil || !bytes.Equal(expected, actual) {
			return workspaceAnalysisModelConflict(errors.New("decision document replay differs"))
		}
	} else if result.Decision != nil || result.Operation.Result != nil ||
		(command.Status == domain.ModelCallFailed && result.Operation.Status != domain.WorkspaceAnalysisOperationFailed) ||
		(command.Status == domain.ModelCallUnknown && result.Operation.Status != domain.WorkspaceAnalysisOperationUnknown) {
		return workspaceAnalysisModelConflict(errors.New("decision failure replay differs"))
	}
	return nil
}

// LoadWorkspaceAnalysisJournal takes a shared Run lock so its bounded set reads
// observe one committed journal. Every writer already locks the same Run first.
func (repository *GORMWorkspaceAnalysisRepository) LoadWorkspaceAnalysisJournal(ctx context.Context, query application.WorkspaceAnalysisJournalQuery) (application.WorkspaceAnalysisJournalSnapshot, error) {
	if ctx == nil {
		return application.WorkspaceAnalysisJournalSnapshot{}, workspaceAnalysisModelInvalid(errors.New("journal context is nil"))
	}
	if err := query.Validate(); err != nil {
		return application.WorkspaceAnalysisJournalSnapshot{}, err
	}
	var snapshot application.WorkspaceAnalysisJournalSnapshot
	err := repository.within(ctx, func(callbackCtx context.Context, transaction *gorm.DB, _ foundation.TransactionScope) error {
		row, err := gormRawRow(transaction.WithContext(callbackCtx), `SELECT `+workspaceAnalysisRunColumns+`
			FROM agent.workspace_analysis_run WHERE id=?::uuid AND workspace_id=?::uuid AND workflow_run_id=?::uuid FOR SHARE`,
			string(query.AnalysisRunID), string(query.WorkspaceID), string(query.WorkflowRunID))
		if err != nil {
			return classifyGORM(callbackCtx, err)
		}
		run, err := scanWorkspaceAnalysisRun(row)
		if err != nil {
			return classifyGORM(callbackCtx, err)
		}
		if run.DefinitionVersion != 2 || domain.ValidateWorkspaceAnalysisRun(run) != nil {
			return consistency(errors.New("dynamic journal requires a valid version two run"))
		}
		entries, err := gormLoadWorkspaceAnalysisJournalEntries(callbackCtx, transaction, query)
		if err != nil {
			return err
		}
		if err := gormBindWorkspaceAnalysisJournalModels(callbackCtx, transaction, query, entries); err != nil {
			return err
		}
		snapshot = application.WorkspaceAnalysisJournalSnapshot{Run: run, Entries: entries}
		return nil
	})
	if err != nil {
		return application.WorkspaceAnalysisJournalSnapshot{}, err
	}
	return snapshot, nil
}

type workspaceAnalysisJournalPrefixScanner struct {
	row                       rowScanner
	sequence                  *int64
	decisionID, reservationID **string
}

func (scanner workspaceAnalysisJournalPrefixScanner) Scan(destinations ...any) error {
	return scanner.row.Scan(append([]any{scanner.sequence, scanner.decisionID, scanner.reservationID}, destinations...)...)
}

func gormLoadWorkspaceAnalysisJournalEntries(ctx context.Context, transaction *gorm.DB, query application.WorkspaceAnalysisJournalQuery) ([]application.WorkspaceAnalysisJournalEntry, error) {
	rows, err := gormRawRows(transaction.WithContext(ctx), `SELECT journal.sequence,journal.decision_id::text,reservation.id::text,`+gormWorkspaceAnalysisToolOperationColumns+`
		FROM agent.workspace_analysis_journal journal
		JOIN agent.workspace_analysis_operation operation ON operation.id=journal.operation_id AND operation.analysis_run_id=journal.analysis_run_id AND operation.workspace_id=journal.workspace_id
		LEFT JOIN agent.workspace_analysis_budget_reservation reservation ON reservation.operation_id=operation.id
		WHERE journal.analysis_run_id=?::uuid AND journal.workspace_id=?::uuid AND operation.workflow_run_id=?::uuid
		ORDER BY journal.sequence LIMIT 28`, string(query.AnalysisRunID), string(query.WorkspaceID), string(query.WorkflowRunID))
	if err != nil {
		return nil, classifyGORM(ctx, err)
	}
	defer rows.Close()
	entries := make([]application.WorkspaceAnalysisJournalEntry, 0)
	for rows.Next() {
		var sequence int64
		var decisionID, reservationID *string
		operation, scanErr := scanGORMWorkspaceAnalysisToolOperation(workspaceAnalysisJournalPrefixScanner{rows, &sequence, &decisionID, &reservationID})
		if scanErr != nil {
			return nil, classifyGORM(ctx, scanErr)
		}
		entry := application.WorkspaceAnalysisJournalEntry{Sequence: sequence, Operation: operation.domain(workspaceAnalysisModelOptionalID(reservationID)), DecisionID: workspaceAnalysisModelOptionalID(decisionID)}
		if sequence != int64(len(entries)+1) || sequence > 27 || domain.ValidateWorkspaceAnalysisOperation(entry.Operation) != nil {
			return nil, consistency(errors.New("dynamic journal sequence or operation is invalid"))
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	return entries, nil
}

func gormBindWorkspaceAnalysisJournalModels(ctx context.Context, transaction *gorm.DB, query application.WorkspaceAnalysisJournalQuery, entries []application.WorkspaceAnalysisJournalEntry) error {
	runs := make(map[foundation.ID]domain.ModelRun)
	runRows, err := gormRawRows(transaction.WithContext(ctx), gormModelRunSelect+` WHERE workspace_id=?::uuid AND id IN (
		SELECT call.model_run_id FROM agent.workspace_analysis_operation operation JOIN agent.model_call call ON call.id=operation.model_call_id
		WHERE operation.analysis_run_id=?::uuid)`, string(query.WorkspaceID), string(query.AnalysisRunID))
	if err != nil {
		return classifyGORM(ctx, err)
	}
	for runRows.Next() {
		run, scanErr := scanModelRun(runRows)
		if scanErr != nil {
			runRows.Close()
			return classifyGORM(ctx, scanErr)
		}
		runs[run.ID] = run
	}
	err = runRows.Err()
	runRows.Close()
	if err != nil {
		return classifyGORM(ctx, err)
	}
	calls := make(map[foundation.ID]domain.ModelCall)
	callRows, err := gormRawRows(transaction.WithContext(ctx), gormModelCallSelect+` WHERE call.id IN (
		SELECT model_call_id FROM agent.workspace_analysis_operation WHERE analysis_run_id=?::uuid AND workspace_id=?::uuid)`, string(query.AnalysisRunID), string(query.WorkspaceID))
	if err != nil {
		return classifyGORM(ctx, err)
	}
	for callRows.Next() {
		call, scanErr := scanModelCall(callRows)
		if scanErr != nil {
			callRows.Close()
			return classifyGORM(ctx, scanErr)
		}
		calls[call.ID] = call
	}
	err = callRows.Err()
	callRows.Close()
	if err != nil {
		return classifyGORM(ctx, err)
	}
	decisions := make(map[foundation.ID]domain.WorkspaceAnalysisDecisionReceipt)
	decisionRows, err := gormRawRows(transaction.WithContext(ctx), `SELECT `+workspaceAnalysisDecisionColumns+`
		FROM agent.workspace_analysis_decision decision WHERE analysis_run_id=?::uuid AND workspace_id=?::uuid ORDER BY ordinal`, string(query.AnalysisRunID), string(query.WorkspaceID))
	if err != nil {
		return classifyGORM(ctx, err)
	}
	for decisionRows.Next() {
		decision, scanErr := scanWorkspaceAnalysisDecision(decisionRows)
		if scanErr != nil {
			decisionRows.Close()
			return classifyGORM(ctx, scanErr)
		}
		decisions[decision.OperationID] = decision
	}
	err = decisionRows.Err()
	decisionRows.Close()
	if err != nil {
		return classifyGORM(ctx, err)
	}
	for index := range entries {
		entry := &entries[index]
		operation := entry.Operation
		if operation.Call != nil && operation.Call.Kind == domain.WorkspaceAnalysisOperationCallModel {
			call, foundCall := calls[operation.Call.ID]
			run, foundRun := runs[call.ModelRunID]
			if !foundCall || !foundRun || run.WorkspaceID != query.WorkspaceID || run.WorkflowRunID != query.WorkflowRunID ||
				operation.FirstNodeAttemptID == nil || run.NodeAttemptID != *operation.FirstNodeAttemptID || operation.RequestHash != call.RequestHash {
				return consistency(errors.New("journal model identity is incomplete"))
			}
			entry.ModelRun, entry.ModelCall = &run, &call
		}
		if decision, exists := decisions[operation.ID]; exists {
			if operation.Kind != domain.WorkspaceAnalysisOperationDecision || operation.Status != domain.WorkspaceAnalysisOperationSucceeded ||
				entry.ModelCall == nil || entry.ModelRun == nil || entry.ModelCall.Status != domain.ModelCallSucceeded ||
				decision.ModelCallID != entry.ModelCall.ID || decision.ModelRunID != entry.ModelRun.ID || decision.Ordinal != operation.Ordinal ||
				operation.Result == nil || operation.Result.ID != decision.ID || operation.Result.Hash != decision.DocumentHash ||
				entry.ModelCall.ResponseHash != decision.DocumentHash || entry.ModelCall.ResponseBytes != decision.DocumentBytes {
				return consistency(errors.New("journal decision receipt identity drifted"))
			}
			entry.Decision = &decision
		} else if operation.Kind == domain.WorkspaceAnalysisOperationDecision && operation.Status == domain.WorkspaceAnalysisOperationSucceeded {
			return consistency(errors.New("successful journal decision has no receipt"))
		}
		if operation.NodeKey == domain.WorkspaceAnalysisOperationNodeDecideNext && operation.Call != nil && operation.Call.Kind == domain.WorkspaceAnalysisOperationCallTool ||
			operation.NodeKey == domain.WorkspaceAnalysisOperationNodeDecideNext && operation.Kind != domain.WorkspaceAnalysisOperationDecision {
			if index == 0 || entries[index-1].Decision == nil || entry.DecisionID == nil || *entry.DecisionID != entries[index-1].Decision.ID || entries[index-1].Operation.Ordinal != operation.Ordinal {
				return consistency(errors.New("journal tool has a different predecessor decision"))
			}
		} else if entry.DecisionID != nil {
			return consistency(errors.New("journal model slot unexpectedly owns a tool decision link"))
		}
	}
	return nil
}
