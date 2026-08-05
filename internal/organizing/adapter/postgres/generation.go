package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentapp "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"github.com/jackc/pgx/v5"
)

const generationSelect = `
	id::text,workspace_id::text,snapshot_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
	generation_kind,request_hash,model_settings_revision,COALESCE(model_run_id::text,''),status,
	COALESCE(output_document,''::bytea),COALESCE(output_hash,''),COALESCE(error_code,''),retryable,
	version,created_at,updated_at,completed_at`

// GenerationRepository 原子保存 Organizing 节点输出并终结对应 Agent Model Run。
type GenerationRepository struct {
	db    DB
	agent agentapp.ModelRunTxFinalizer
}

// NewGenerationRepository 创建节点级模型输出恢复仓库。
func NewGenerationRepository(db DB, agent agentapp.ModelRunTxFinalizer) (*GenerationRepository, error) {
	if isNilInterface(db) || isNilInterface(agent) {
		return nil, generationUnavailable(errors.New("organizing generation repository dependencies are incomplete"))
	}
	return &GenerationRepository{db: db, agent: agent}, nil
}

// LookupReady 在任何 Provider 调用前恢复同一 Node Run 已提交的精确输出。
func (repository *GenerationRepository) LookupReady(
	ctx context.Context,
	workspaceID foundation.ID,
	nodeRunID foundation.ID,
	kind organizingworkflow.GenerationKind,
	requestHash string,
) (organizingworkflow.GenerationRecord, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil || !postgresID(workspaceID) || !postgresID(nodeRunID) ||
		!generationKind(kind) || !lowerHash(requestHash) {
		return organizingworkflow.GenerationRecord{}, false, generationInvalid(errors.New("organizing ready generation lookup is invalid"))
	}
	record, err := scanGeneration(repository.db.QueryRow(ctx, `SELECT `+generationSelect+`
		FROM organizing.generation WHERE workspace_id=$1 AND node_run_id=$2 AND status='READY'`,
		string(workspaceID), string(nodeRunID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingworkflow.GenerationRecord{}, false, nil
	}
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	if record.Kind != kind || record.RequestHash != requestHash {
		return organizingworkflow.GenerationRecord{}, false, generationInconsistent(errors.New("ready organizing generation is bound to another request"))
	}
	return record, true, nil
}

// Prepare 创建同 Node Attempt 唯一恢复栅栏；同 Node Run 的未决调用会阻止重复 Provider 请求。
func (repository *GenerationRepository) Prepare(
	ctx context.Context,
	command organizingworkflow.PrepareGenerationCommand,
) (organizingworkflow.GenerationRecord, bool, error) {
	record := command.Record
	if repository == nil || repository.db == nil || ctx == nil || record.Validate() != nil ||
		record.Status != organizingworkflow.GenerationRunning || record.Version != 1 || record.ModelRunID != "" {
		return organizingworkflow.GenerationRecord{}, false, generationInvalid(errors.New("organizing generation preparation is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "organizing-generation:"+string(record.NodeRunID)); err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	existing, found, err := loadGenerationByAttempt(ctx, tx, record.WorkspaceID, record.NodeAttemptID, true)
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, err
	}
	if found {
		if !sameGenerationCreate(existing, record) {
			return organizingworkflow.GenerationRecord{}, false, generationConflict(errors.New("organizing node attempt is bound to another generation request"))
		}
		if err := tx.Commit(ctx); err != nil {
			return organizingworkflow.GenerationRecord{}, false, classify(err)
		}
		return existing, true, nil
	}
	if other, found, err := loadOpenGenerationByNode(ctx, tx, record.WorkspaceID, record.NodeRunID); err != nil {
		return organizingworkflow.GenerationRecord{}, false, err
	} else if found {
		if other.Status == organizingworkflow.GenerationReady && other.Kind == record.Kind && other.RequestHash == record.RequestHash {
			if err := tx.Commit(ctx); err != nil {
				return organizingworkflow.GenerationRecord{}, false, classify(err)
			}
			return other, true, nil
		}
		return organizingworkflow.GenerationRecord{}, false, generationRecovery(errors.New("another organizing generation for this node is not safely replayable"))
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO organizing.generation(
			id,workspace_id,snapshot_id,workflow_run_id,node_run_id,node_attempt_id,generation_kind,
			request_hash,model_settings_revision,model_run_id,status,output_document,output_hash,error_code,
			retryable,version,created_at,updated_at,completed_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULL,'RUNNING',NULL,NULL,NULL,false,1,$10,$10,NULL)`,
		string(record.ID), string(record.WorkspaceID), string(record.SnapshotID), string(record.WorkflowRunID),
		string(record.NodeRunID), string(record.NodeAttemptID), string(record.Kind), record.RequestHash,
		record.ModelSettingsRevision, record.CreatedAt.UTC())
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	return record, false, nil
}

// BindModelRun 在 Provider 调用前把恢复栅栏绑定到唯一 Agent Model Run。
func (repository *GenerationRepository) BindModelRun(
	ctx context.Context,
	command organizingworkflow.BindGenerationModelRunCommand,
) (organizingworkflow.GenerationRecord, error) {
	if repository == nil || repository.db == nil || ctx == nil || !postgresID(command.WorkspaceID) ||
		!postgresID(command.GenerationID) || !postgresID(command.ModelRunID) || command.ExpectedVersion < 1 {
		return organizingworkflow.GenerationRecord{}, generationInvalid(errors.New("organizing generation model run binding is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return organizingworkflow.GenerationRecord{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	record, err := loadGenerationByID(ctx, tx, command.WorkspaceID, command.GenerationID, true)
	if err != nil {
		return organizingworkflow.GenerationRecord{}, err
	}
	replayed := record.ModelRunID == command.ModelRunID && record.Status == organizingworkflow.GenerationRunning
	if !replayed && (record.Status != organizingworkflow.GenerationRunning || record.Version != command.ExpectedVersion || record.ModelRunID != "") {
		return organizingworkflow.GenerationRecord{}, generationConflict(errors.New("organizing generation cannot bind this model run"))
	}
	run, err := repository.agent.GetModelRunTx(ctx, tx, command.WorkspaceID, command.ModelRunID, true)
	if err != nil {
		return organizingworkflow.GenerationRecord{}, err
	}
	if run.WorkflowRunID != record.WorkflowRunID || run.NodeRunID != record.NodeRunID || run.NodeAttemptID != record.NodeAttemptID ||
		run.Status != agentdomain.ModelRunRunning || run.Version != 1 || !sameOptionalRevision(run.ModelSettingsRevision, record.ModelSettingsRevision) ||
		!generationRuntimeMatches(run, record.Kind) {
		return organizingworkflow.GenerationRecord{}, generationInconsistent(errors.New("agent model run differs from the organizing generation fence"))
	}
	if replayed {
		if err := tx.Commit(ctx); err != nil {
			return organizingworkflow.GenerationRecord{}, classify(err)
		}
		return record, nil
	}
	nextVersion := record.Version + 1
	tag, err := tx.Exec(ctx, `UPDATE organizing.generation
		SET model_run_id=$1,version=$2,updated_at=GREATEST(updated_at,$3)
		WHERE id=$4 AND workspace_id=$5 AND status='RUNNING' AND model_run_id IS NULL AND version=$6`,
		string(command.ModelRunID), nextVersion, run.UpdatedAt.UTC(), string(record.ID), string(record.WorkspaceID), record.Version)
	if err != nil {
		return organizingworkflow.GenerationRecord{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return organizingworkflow.GenerationRecord{}, generationConflict(errors.New("organizing generation model run CAS failed"))
	}
	record.ModelRunID = command.ModelRunID
	record.Version = nextVersion
	if run.UpdatedAt.After(record.UpdatedAt) {
		record.UpdatedAt = run.UpdatedAt.UTC()
	}
	if err := record.Validate(); err != nil {
		return organizingworkflow.GenerationRecord{}, generationInconsistent(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return organizingworkflow.GenerationRecord{}, classify(err)
	}
	return record, nil
}

// Complete 在一个事务内保存原始严格 JSON 字节并成功终结 Model Run。
func (repository *GenerationRepository) Complete(
	ctx context.Context,
	command organizingworkflow.CompleteGenerationCommand,
) (organizingworkflow.GenerationRecord, bool, error) {
	if repository == nil || repository.db == nil || ctx == nil || !validCompleteGeneration(command) {
		return organizingworkflow.GenerationRecord{}, false, generationInvalid(errors.New("organizing generation completion is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	record, err := loadGenerationByID(ctx, tx, command.WorkspaceID, command.GenerationID, true)
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, err
	}
	if !generationResultMatches(record.Kind, command.ResultType) {
		return organizingworkflow.GenerationRecord{}, false, generationInconsistent(errors.New("organizing generation result type differs from its kind"))
	}
	if record.Status == organizingworkflow.GenerationReady {
		if record.ModelRunID != command.ModelRunID || record.OutputHash != command.OutputHash || !bytes.Equal(record.Output, command.Output) {
			return organizingworkflow.GenerationRecord{}, false, generationConflict(errors.New("organizing generation completion conflicts with the committed output"))
		}
		if err := tx.Commit(ctx); err != nil {
			return organizingworkflow.GenerationRecord{}, false, classify(err)
		}
		return record, true, nil
	}
	if record.Status != organizingworkflow.GenerationRunning || record.Version != command.ExpectedVersion || record.ModelRunID != command.ModelRunID {
		return organizingworkflow.GenerationRecord{}, false, generationConflict(errors.New("organizing generation is not completable"))
	}
	modelRecord, err := repository.agent.GetModelRunRecordTx(ctx, tx, command.WorkspaceID, command.ModelRunID, true)
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, err
	}
	if err := validateGenerationModelRecord(modelRecord, record, command.ExpectedModelRunVersion, command.OutputHash); err != nil {
		return organizingworkflow.GenerationRecord{}, false, err
	}
	finalRun := modelRecord.Run
	finalRun.Status = agentdomain.ModelRunSucceeded
	finalRun.FinalResultType = command.ResultType
	finalRun.FinalErrorCode = ""
	finalRun.Version++
	finalRun.UpdatedAt = command.CompletedAt.UTC()
	finalRun.CompletedAt = timePointer(command.CompletedAt.UTC())
	if _, _, err := repository.agent.FinalizeModelRunTx(ctx, tx, agentapp.FinalizeModelRunCommand{
		ExpectedVersion: command.ExpectedModelRunVersion, Run: finalRun,
	}); err != nil {
		return organizingworkflow.GenerationRecord{}, false, err
	}
	next := record
	next.Status = organizingworkflow.GenerationReady
	next.Output = append(json.RawMessage(nil), command.Output...)
	next.OutputHash = command.OutputHash
	next.Retryable = false
	next.Version++
	next.UpdatedAt = command.CompletedAt.UTC()
	next.CompletedAt = timePointer(command.CompletedAt.UTC())
	if err := next.Validate(); err != nil {
		return organizingworkflow.GenerationRecord{}, false, generationInconsistent(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE organizing.generation SET
		status='READY',output_document=$1,output_hash=$2,error_code=NULL,retryable=false,
		version=$3,updated_at=$4,completed_at=$4
		WHERE id=$5 AND workspace_id=$6 AND status='RUNNING' AND model_run_id=$7 AND version=$8`,
		[]byte(command.Output), command.OutputHash, next.Version, command.CompletedAt.UTC(),
		string(record.ID), string(record.WorkspaceID), string(command.ModelRunID), record.Version)
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return organizingworkflow.GenerationRecord{}, false, generationConflict(errors.New("organizing generation completion CAS failed"))
	}
	if err := tx.Commit(ctx); err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	return next, false, nil
}

// Fail 原子保存安全失败或不确定终态，并终结对应 Model Run。
func (repository *GenerationRepository) Fail(ctx context.Context, command organizingworkflow.FailGenerationCommand) error {
	if repository == nil || repository.db == nil || ctx == nil || !validFailGeneration(command) {
		return generationInvalid(errors.New("organizing generation failure is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	record, err := loadGenerationByID(ctx, tx, command.WorkspaceID, command.GenerationID, true)
	if err != nil {
		return err
	}
	expectedStatus := organizingworkflow.GenerationFailed
	if command.ModelRunStatus == agentdomain.ModelRunUnknown {
		expectedStatus = organizingworkflow.GenerationRecoveryRequired
	}
	if record.Status == expectedStatus && record.ModelRunID == command.ModelRunID && record.ErrorCode == command.ErrorCode && record.Retryable == command.Retryable {
		return classify(tx.Commit(ctx))
	}
	if record.Status != organizingworkflow.GenerationRunning || record.Version != command.ExpectedVersion || record.ModelRunID != command.ModelRunID {
		return generationConflict(errors.New("organizing generation is not fail-able"))
	}
	modelRecord, err := repository.agent.GetModelRunRecordTx(ctx, tx, command.WorkspaceID, command.ModelRunID, true)
	if err != nil {
		return err
	}
	if !sameGenerationModelBinding(modelRecord.Run, record) || !generationRuntimeMatches(modelRecord.Run, record.Kind) ||
		modelRecord.Run.Status != agentdomain.ModelRunRunning ||
		modelRecord.Run.Version != command.ExpectedModelRunVersion {
		return generationInconsistent(errors.New("organizing failure model run binding is invalid"))
	}
	finalRun := modelRecord.Run
	finalRun.Status = command.ModelRunStatus
	finalRun.FinalResultType = ""
	finalRun.FinalErrorCode = command.ErrorCode
	finalRun.Version++
	finalRun.UpdatedAt = command.CompletedAt.UTC()
	finalRun.CompletedAt = timePointer(command.CompletedAt.UTC())
	if _, _, err := repository.agent.FinalizeModelRunTx(ctx, tx, agentapp.FinalizeModelRunCommand{
		ExpectedVersion: command.ExpectedModelRunVersion, Run: finalRun,
	}); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE organizing.generation SET
		status=$1,output_document=NULL,output_hash=NULL,error_code=$2,retryable=$3,
		version=version+1,updated_at=$4,completed_at=$4
		WHERE id=$5 AND workspace_id=$6 AND status='RUNNING' AND model_run_id=$7 AND version=$8`,
		string(expectedStatus), command.ErrorCode, command.Retryable, command.CompletedAt.UTC(),
		string(record.ID), string(record.WorkspaceID), string(command.ModelRunID), record.Version)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return generationConflict(errors.New("organizing generation failure CAS failed"))
	}
	return classify(tx.Commit(ctx))
}

func loadGenerationByAttempt(ctx context.Context, query queryer, workspaceID, attemptID foundation.ID, forUpdate bool) (organizingworkflow.GenerationRecord, bool, error) {
	queryText := `SELECT ` + generationSelect + ` FROM organizing.generation WHERE workspace_id=$1 AND node_attempt_id=$2`
	if forUpdate {
		queryText += ` FOR UPDATE`
	}
	record, err := scanGeneration(query.QueryRow(ctx, queryText, string(workspaceID), string(attemptID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingworkflow.GenerationRecord{}, false, nil
	}
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	return record, true, nil
}

func loadOpenGenerationByNode(ctx context.Context, query queryer, workspaceID, nodeRunID foundation.ID) (organizingworkflow.GenerationRecord, bool, error) {
	record, err := scanGeneration(query.QueryRow(ctx, `SELECT `+generationSelect+`
		FROM organizing.generation WHERE workspace_id=$1 AND node_run_id=$2 AND status IN ('RUNNING','READY')
		ORDER BY CASE status WHEN 'READY' THEN 0 ELSE 1 END,created_at,id LIMIT 1 FOR UPDATE`,
		string(workspaceID), string(nodeRunID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingworkflow.GenerationRecord{}, false, nil
	}
	if err != nil {
		return organizingworkflow.GenerationRecord{}, false, classify(err)
	}
	return record, true, nil
}

func loadGenerationByID(ctx context.Context, query queryer, workspaceID, generationID foundation.ID, forUpdate bool) (organizingworkflow.GenerationRecord, error) {
	queryText := `SELECT ` + generationSelect + ` FROM organizing.generation WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		queryText += ` FOR UPDATE`
	}
	record, err := scanGeneration(query.QueryRow(ctx, queryText, string(workspaceID), string(generationID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingworkflow.GenerationRecord{}, generationNotFound(err)
	}
	if err != nil {
		return organizingworkflow.GenerationRecord{}, classify(err)
	}
	return record, nil
}

func scanGeneration(scanner scanner) (organizingworkflow.GenerationRecord, error) {
	var (
		record                   organizingworkflow.GenerationRecord
		kind, status, modelRunID string
		output                   []byte
		modelSettingsRevision    *int64
		completedAt              *time.Time
	)
	if err := scanner.Scan(
		&record.ID, &record.WorkspaceID, &record.SnapshotID, &record.WorkflowRunID, &record.NodeRunID,
		&record.NodeAttemptID, &kind, &record.RequestHash, &modelSettingsRevision, &modelRunID, &status,
		&output, &record.OutputHash, &record.ErrorCode, &record.Retryable, &record.Version,
		&record.CreatedAt, &record.UpdatedAt, &completedAt,
	); err != nil {
		return organizingworkflow.GenerationRecord{}, err
	}
	record.Kind = organizingworkflow.GenerationKind(kind)
	record.Status = organizingworkflow.GenerationStatus(status)
	record.ModelSettingsRevision = modelSettingsRevision
	record.ModelRunID = foundation.ID(modelRunID)
	record.Output = append(json.RawMessage(nil), output...)
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	if completedAt != nil {
		completed := completedAt.UTC()
		record.CompletedAt = &completed
	}
	if err := record.Validate(); err != nil {
		return organizingworkflow.GenerationRecord{}, generationInconsistent(fmt.Errorf("stored organizing generation is invalid: %w", err))
	}
	return record, nil
}

func validateGenerationModelRecord(record agentapp.ModelRunRecord, generation organizingworkflow.GenerationRecord, expectedVersion int64, outputHash string) error {
	if !sameGenerationModelBinding(record.Run, generation) || !generationRuntimeMatches(record.Run, generation.Kind) ||
		record.Run.Status != agentdomain.ModelRunRunning ||
		record.Run.Version != expectedVersion || len(record.Calls) == 0 {
		return generationInconsistent(errors.New("organizing generation model run is incomplete"))
	}
	matchedOutput := false
	for _, call := range record.Calls {
		if agentdomain.ValidateModelCall(call) != nil || call.ModelRunID != record.Run.ID || call.Status == agentdomain.ModelCallStarted || call.Status == agentdomain.ModelCallUnknown {
			return generationInconsistent(errors.New("organizing generation model call history is incomplete"))
		}
		if !generationCallMatchesRuntime(call, record.Run) {
			return generationInconsistent(errors.New("organizing generation model call differs from its runtime"))
		}
		if call.Status == agentdomain.ModelCallSucceeded && call.ResponseHash == outputHash {
			matchedOutput = true
		}
	}
	if !matchedOutput {
		return generationInconsistent(errors.New("organizing generation output does not match a successful model response"))
	}
	return nil
}

func generationRuntimeMatches(run agentdomain.ModelRun, kind organizingworkflow.GenerationKind) bool {
	version := organizingworkflow.GenerationRuntimeVersion
	switch kind {
	case organizingworkflow.GenerationOutline:
		return run.Prompt == (agentdomain.PromptRef{ID: organizingworkflow.OutlinePromptID, Version: version}) &&
			run.Schema == (agentdomain.SchemaRef{ID: organizingworkflow.OutlineSchemaID, Version: version}) &&
			run.ReducedSchema == (agentdomain.SchemaRef{ID: organizingworkflow.OutlineReducedSchemaID, Version: version})
	case organizingworkflow.GenerationDocument:
		return run.Prompt == (agentdomain.PromptRef{ID: organizingworkflow.DocumentPromptID, Version: version}) &&
			run.Schema == (agentdomain.SchemaRef{ID: organizingworkflow.DocumentSchemaID, Version: version}) &&
			run.ReducedSchema == (agentdomain.SchemaRef{ID: organizingworkflow.DocumentReducedSchemaID, Version: version})
	default:
		return false
	}
}

func generationCallMatchesRuntime(call agentdomain.ModelCall, run agentdomain.ModelRun) bool {
	if call.Model != run.Model || call.Profile != run.Profile || call.Prompt != run.Prompt {
		return false
	}
	switch call.Phase {
	case agentdomain.ModelCallInitial, agentdomain.ModelCallRepair:
		return call.Schema == run.Schema
	case agentdomain.ModelCallReduced:
		return call.Schema == run.ReducedSchema
	default:
		return false
	}
}

func generationResultMatches(kind organizingworkflow.GenerationKind, resultType string) bool {
	switch kind {
	case organizingworkflow.GenerationOutline:
		return resultType == agentdomain.ResultTypeOrganizingOutline
	case organizingworkflow.GenerationDocument:
		return resultType == agentdomain.ResultTypeOrganizingDocument
	default:
		return false
	}
}

func sameGenerationModelBinding(run agentdomain.ModelRun, generation organizingworkflow.GenerationRecord) bool {
	return run.ID == generation.ModelRunID && run.WorkspaceID == generation.WorkspaceID &&
		run.WorkflowRunID == generation.WorkflowRunID && run.NodeRunID == generation.NodeRunID &&
		run.NodeAttemptID == generation.NodeAttemptID && sameOptionalRevision(run.ModelSettingsRevision, generation.ModelSettingsRevision)
}

func sameGenerationCreate(left, right organizingworkflow.GenerationRecord) bool {
	return left.WorkspaceID == right.WorkspaceID && left.SnapshotID == right.SnapshotID &&
		left.WorkflowRunID == right.WorkflowRunID && left.NodeRunID == right.NodeRunID &&
		left.NodeAttemptID == right.NodeAttemptID && left.Kind == right.Kind && left.RequestHash == right.RequestHash &&
		sameOptionalRevision(left.ModelSettingsRevision, right.ModelSettingsRevision)
}

func validCompleteGeneration(command organizingworkflow.CompleteGenerationCommand) bool {
	return postgresID(command.WorkspaceID) && postgresID(command.GenerationID) && postgresID(command.ModelRunID) &&
		command.ExpectedVersion > 0 && command.ExpectedModelRunVersion > 0 &&
		(command.ResultType == agentdomain.ResultTypeOrganizingOutline || command.ResultType == agentdomain.ResultTypeOrganizingDocument) &&
		len(command.Output) > 0 && len(command.Output) <= 512*1024 && json.Valid(command.Output) &&
		lowerHash(command.OutputHash) && sha256Bytes(command.Output) == command.OutputHash && !command.CompletedAt.IsZero()
}

func validFailGeneration(command organizingworkflow.FailGenerationCommand) bool {
	return postgresID(command.WorkspaceID) && postgresID(command.GenerationID) && postgresID(command.ModelRunID) &&
		command.ExpectedVersion > 0 && command.ExpectedModelRunVersion > 0 &&
		(command.ModelRunStatus == agentdomain.ModelRunFailed || command.ModelRunStatus == agentdomain.ModelRunUnknown) &&
		stableGenerationError(command.ErrorCode) && !command.CompletedAt.IsZero() &&
		(command.ModelRunStatus != agentdomain.ModelRunUnknown || !command.Retryable)
}

func generationKind(kind organizingworkflow.GenerationKind) bool {
	return kind == organizingworkflow.GenerationOutline || kind == organizingworkflow.GenerationDocument
}

func sameOptionalRevision(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func timePointer(value time.Time) *time.Time { return &value }

func stableGenerationError(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func lowerHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func sha256Bytes(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("%x", digest[:])
}

func postgresID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func generationInvalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "ORGANIZING_GENERATION_INVALID", false, err)
}

func generationNotFound(err error) error {
	return foundation.NewError(foundation.ErrorNotFound, "ORGANIZING_GENERATION_NOT_FOUND", false, err)
}

func generationConflict(err error) error {
	return foundation.NewError(foundation.ErrorVersionConflict, "ORGANIZING_GENERATION_CONFLICT", false, err)
}

func generationInconsistent(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, "ORGANIZING_GENERATION_RESULT_INVALID", false, err)
}

func generationRecovery(err error) error {
	return foundation.NewError(foundation.ErrorManualRecoveryRequired, "ORGANIZING_GENERATION_RECOVERY_REQUIRED", false, err)
}

func generationUnavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, "ORGANIZING_GENERATION_REPOSITORY_UNAVAILABLE", true, err)
}

var _ organizingworkflow.GenerationStore = (*GenerationRepository)(nil)
