package synthesispostgres

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	organizingdomain "github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// jsonValue follows the existing GORM JSONB carrier contract. Raw accepted
// Provider bytes use bytea instead, because replay must preserve exact bytes.
type jsonValue []byte

func (value jsonValue) Value() (driver.Value, error) {
	if len(value) == 0 {
		return nil, nil
	}
	return string(value), nil
}
func (value *jsonValue) Scan(raw any) error {
	switch raw := raw.(type) {
	case nil:
		*value = nil
	case []byte:
		*value = append((*value)[:0], raw...)
	case string:
		*value = append((*value)[:0], raw...)
	default:
		return errors.New("synthesis JSONB value is invalid")
	}
	return nil
}

type processingModel struct {
	ID                 string     `gorm:"column:id;primaryKey"`
	WorkspaceID        string     `gorm:"column:workspace_id"`
	SourceEventID      string     `gorm:"column:source_event_id"`
	SourceID           string     `gorm:"column:source_id"`
	SourceVersionID    string     `gorm:"column:source_version_id"`
	ContentArtifactID  string     `gorm:"column:content_artifact_id"`
	ParseProjectionID  string     `gorm:"column:parse_projection_id"`
	SourceContentHash  string     `gorm:"column:source_content_hash"`
	IngestionAttemptID string     `gorm:"column:ingestion_attempt_id"`
	ProcessorVersion   string     `gorm:"column:processor_version"`
	SourceOccurredAt   time.Time  `gorm:"column:source_occurred_at"`
	ProcessingKey      string     `gorm:"column:processing_key"`
	WorkflowRunID      *string    `gorm:"column:workflow_run_id"`
	ModelRunID         *string    `gorm:"column:model_run_id"`
	RequestHash        string     `gorm:"column:request_hash"`
	Status             string     `gorm:"column:status"`
	RevisionIDs        jsonValue  `gorm:"column:revision_ids;type:jsonb"`
	ErrorCode          *string    `gorm:"column:error_code"`
	Retryable          bool       `gorm:"column:retryable"`
	Version            int64      `gorm:"column:version"`
	CreatedAt          time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	CompletedAt        *time.Time `gorm:"column:completed_at"`
}

func (processingModel) TableName() string { return "organizing.synthesis_processing" }

type executionModel struct {
	WorkflowRunID      string    `gorm:"column:workflow_run_id;primaryKey"`
	WorkspaceID        string    `gorm:"column:workspace_id"`
	ProcessingID       string    `gorm:"column:processing_id"`
	ExecutionNo        int       `gorm:"column:execution_no"`
	ApplyRecovery      bool      `gorm:"column:apply_recovery"`
	InputDocument      jsonValue `gorm:"column:input_document;type:jsonb"`
	InputHash          *string   `gorm:"column:input_hash"`
	ApplyNodeRunID     *string   `gorm:"column:apply_node_run_id"`
	ApplyNodeAttemptID *string   `gorm:"column:apply_node_attempt_id"`
	AppliedResult      jsonValue `gorm:"column:applied_result;type:jsonb"`
	Version            int64     `gorm:"column:version"`
	CreatedAt          time.Time `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt          time.Time `gorm:"column:updated_at;autoUpdateTime:false"`
}

func (executionModel) TableName() string { return "organizing.synthesis_execution" }

type modelStepModel struct {
	ID                    string     `gorm:"column:id;primaryKey"`
	WorkspaceID           string     `gorm:"column:workspace_id"`
	ProcessingID          string     `gorm:"column:processing_id"`
	WorkflowRunID         string     `gorm:"column:workflow_run_id"`
	NodeRunID             string     `gorm:"column:node_run_id"`
	NodeAttemptID         string     `gorm:"column:node_attempt_id"`
	Stage                 string     `gorm:"column:stage"`
	RequestHash           string     `gorm:"column:request_hash"`
	InputRequestHash      string     `gorm:"column:input_request_hash"`
	GenerationOutputHash  *string    `gorm:"column:generation_output_hash"`
	ModelSettingsRevision *int64     `gorm:"column:model_settings_revision"`
	ModelRunID            *string    `gorm:"column:model_run_id"`
	Status                string     `gorm:"column:status"`
	OutputDocument        []byte     `gorm:"column:output_document;type:bytea"`
	OutputHash            *string    `gorm:"column:output_hash"`
	GenerationResult      jsonValue  `gorm:"column:generation_result;type:jsonb"`
	SemanticReceipt       jsonValue  `gorm:"column:semantic_receipt;type:jsonb"`
	ErrorCode             *string    `gorm:"column:error_code"`
	Retryable             bool       `gorm:"column:retryable"`
	Version               int64      `gorm:"column:version"`
	CreatedAt             time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	UpdatedAt             time.Time  `gorm:"column:updated_at;autoUpdateTime:false"`
	CompletedAt           *time.Time `gorm:"column:completed_at"`
}

func (modelStepModel) TableName() string { return "organizing.synthesis_model_step" }

type retryModel struct {
	WorkspaceID    string    `gorm:"column:workspace_id;primaryKey"`
	IdempotencyKey string    `gorm:"column:idempotency_key;primaryKey"`
	RequestHash    string    `gorm:"column:request_hash"`
	ProcessingID   string    `gorm:"column:processing_id"`
	WorkflowRunID  string    `gorm:"column:workflow_run_id"`
	Response       jsonValue `gorm:"column:response;type:jsonb"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime:false"`
}

func (retryModel) TableName() string { return "organizing.synthesis_retry_receipt" }

func modelProcessing(value organizingapp.SynthesisProcessing) (processingModel, error) {
	key, err := value.SourceEvent.ProcessingKey()
	if err != nil {
		return processingModel{}, err
	}
	revisions, err := marshal(value.RevisionIDs)
	if err != nil {
		return processingModel{}, err
	}
	source := value.SourceEvent.Source
	row := processingModel{ID: string(value.ID), WorkspaceID: string(source.WorkspaceID), SourceEventID: string(value.SourceEvent.ID),
		SourceID: string(source.SourceID), SourceVersionID: string(source.SourceVersionID), ContentArtifactID: string(source.ContentArtifactID),
		ParseProjectionID: string(source.ParseProjectionID), SourceContentHash: source.ContentHash, IngestionAttemptID: string(value.SourceEvent.IngestionAttemptID),
		ProcessorVersion: value.SourceEvent.ProcessorVersion, SourceOccurredAt: canonical(value.SourceEvent.CreatedAt), ProcessingKey: key,
		WorkflowRunID: optionalID(value.WorkflowRunID), ModelRunID: optionalID(value.ModelRunID), RequestHash: value.RequestHash,
		Status: string(value.Status), RevisionIDs: revisions, Version: value.Version, CreatedAt: canonical(value.CreatedAt), UpdatedAt: canonical(value.UpdatedAt), CompletedAt: value.CompletedAt}
	if value.Failure != nil {
		row.ErrorCode, row.Retryable = optionalString(value.Failure.Code), value.Failure.Retryable
	}
	return row, nil
}

func (row processingModel) projection() (organizingapp.SynthesisProcessing, error) {
	result := organizingapp.SynthesisProcessing{ID: foundation.ID(row.ID), SourceEvent: organizingdomain.SynthesisSourceReady{ID: foundation.ID(row.SourceEventID),
		Source: organizingdomain.SynthesisSourceVersion{WorkspaceID: foundation.ID(row.WorkspaceID), SourceID: foundation.ID(row.SourceID), SourceVersionID: foundation.ID(row.SourceVersionID),
			ContentArtifactID: foundation.ID(row.ContentArtifactID), ParseProjectionID: foundation.ID(row.ParseProjectionID), ContentHash: row.SourceContentHash},
		IngestionAttemptID: foundation.ID(row.IngestionAttemptID), ProcessorVersion: row.ProcessorVersion, CreatedAt: canonical(row.SourceOccurredAt)},
		WorkflowRunID: idValue(row.WorkflowRunID), ModelRunID: idValue(row.ModelRunID), RequestHash: row.RequestHash,
		Status: organizingapp.SynthesisProcessingStatus(row.Status), Version: row.Version, CreatedAt: canonical(row.CreatedAt), UpdatedAt: canonical(row.UpdatedAt), CompletedAt: row.CompletedAt}
	if err := json.Unmarshal(row.RevisionIDs, &result.RevisionIDs); err != nil {
		return organizingapp.SynthesisProcessing{}, invalid("synthesis processing revision IDs are invalid")
	}
	if row.ErrorCode != nil {
		result.Failure = &organizingdomain.SynthesisFailure{Code: *row.ErrorCode, Retryable: row.Retryable}
	}
	key, err := result.SourceEvent.ProcessingKey()
	if err != nil || key != row.ProcessingKey || result.RequestHash != processingSourceHash(key) || !validID(result.ID) || !result.Status.Valid() || result.Version < 1 || result.CreatedAt.IsZero() || result.UpdatedAt.Before(result.CreatedAt) {
		return organizingapp.SynthesisProcessing{}, invalid("synthesis processing has an invalid durable binding")
	}
	if result.RevisionIDs == nil || len(result.RevisionIDs) > organizingapp.MaxSynthesisGeneratedNotes ||
		(result.WorkflowRunID != "" && !validID(result.WorkflowRunID)) || (result.ModelRunID != "" && !validID(result.ModelRunID)) ||
		(result.CompletedAt != nil && !result.CompletedAt.Equal(result.UpdatedAt)) || (result.Failure == nil && row.Retryable) ||
		(result.Failure != nil && result.Failure.Validate() != nil) {
		return organizingapp.SynthesisProcessing{}, invalid("synthesis processing terminal projection is invalid")
	}
	seen := make(map[foundation.ID]bool, len(result.RevisionIDs))
	for _, id := range result.RevisionIDs {
		if !validID(id) || seen[id] {
			return organizingapp.SynthesisProcessing{}, invalid("synthesis processing revision binding is invalid")
		}
		seen[id] = true
	}
	valid := false
	switch result.Status {
	case organizingapp.SynthesisProcessingPending, organizingapp.SynthesisProcessingRunning:
		valid = result.WorkflowRunID != "" && result.CompletedAt == nil && result.Failure == nil && len(result.RevisionIDs) == 0
	case organizingapp.SynthesisProcessingSkipped:
		valid = result.WorkflowRunID == "" && result.ModelRunID == "" && result.CompletedAt != nil && result.Failure == nil && len(result.RevisionIDs) == 0
	case organizingapp.SynthesisProcessingSucceeded, organizingapp.SynthesisProcessingNoChange:
		valid = result.WorkflowRunID != "" && result.CompletedAt != nil && result.Failure == nil && (result.Status == organizingapp.SynthesisProcessingSucceeded) == (len(result.RevisionIDs) > 0)
	case organizingapp.SynthesisProcessingFailed, organizingapp.SynthesisProcessingRecoveryRequired:
		valid = result.WorkflowRunID != "" && result.CompletedAt != nil && result.Failure != nil && len(result.RevisionIDs) == 0 && (result.Status != organizingapp.SynthesisProcessingRecoveryRequired || !result.Failure.Retryable)
	}
	if !valid {
		return organizingapp.SynthesisProcessing{}, invalid("synthesis processing status and result disagree")
	}
	return result, nil
}

func (row modelStepModel) projection() (organizingapp.SynthesisModelStepRecord, error) {
	result := organizingapp.SynthesisModelStepRecord{ID: foundation.ID(row.ID), WorkspaceID: foundation.ID(row.WorkspaceID), ProcessingID: foundation.ID(row.ProcessingID),
		WorkflowRunID: foundation.ID(row.WorkflowRunID), NodeRunID: foundation.ID(row.NodeRunID), NodeAttemptID: foundation.ID(row.NodeAttemptID),
		Stage: organizingapp.SynthesisModelStage(row.Stage), RequestHash: row.RequestHash, InputRequestHash: row.InputRequestHash, GenerationOutputHash: stringValue(row.GenerationOutputHash),
		ModelSettingsRevision: row.ModelSettingsRevision, ModelRunID: idValue(row.ModelRunID), Status: organizingapp.SynthesisModelStepStatus(row.Status),
		Output: append(json.RawMessage(nil), row.OutputDocument...), OutputHash: stringValue(row.OutputHash), ErrorCode: stringValue(row.ErrorCode), Retryable: row.Retryable,
		Version: row.Version, CreatedAt: canonical(row.CreatedAt), UpdatedAt: canonical(row.UpdatedAt), CompletedAt: row.CompletedAt}
	if len(row.GenerationResult) > 0 {
		result.Generation = new(organizingapp.SynthesisGenerationResult)
		if err := json.Unmarshal(row.GenerationResult, result.Generation); err != nil {
			return result, invalid("synthesis stored generation is invalid")
		}
	}
	if len(row.SemanticReceipt) > 0 {
		result.Semantic = new(organizingapp.SynthesisSemanticReceipt)
		if err := json.Unmarshal(row.SemanticReceipt, result.Semantic); err != nil {
			return result, invalid("synthesis stored semantic review is invalid")
		}
	}
	if err := result.Validate(); err != nil {
		return organizingapp.SynthesisModelStepRecord{}, err
	}
	return result, nil
}

func modelStep(record organizingapp.SynthesisModelStepRecord) modelStepModel {
	return modelStepModel{ID: string(record.ID), WorkspaceID: string(record.WorkspaceID), ProcessingID: string(record.ProcessingID), WorkflowRunID: string(record.WorkflowRunID),
		NodeRunID: string(record.NodeRunID), NodeAttemptID: string(record.NodeAttemptID), Stage: string(record.Stage), RequestHash: record.RequestHash,
		InputRequestHash: record.InputRequestHash, GenerationOutputHash: optionalString(record.GenerationOutputHash), ModelSettingsRevision: record.ModelSettingsRevision,
		Status: string(record.Status), Version: record.Version, CreatedAt: canonical(record.CreatedAt), UpdatedAt: canonical(record.UpdatedAt)}
}

func loadProcessing(tx *gorm.DB, workspaceID, id foundation.ID, lock bool) (processingModel, error) {
	query := tx.Where("workspace_id=? AND id=?", string(workspaceID), string(id))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row processingModel
	if err := query.Take(&row).Error; err != nil {
		return row, classify(tx.Statement.Context, err)
	}
	return row, nil
}

func loadExecution(tx *gorm.DB, workspaceID, processingID, runID foundation.ID, lock bool) (executionModel, error) {
	query := tx.Where("workspace_id=? AND processing_id=? AND workflow_run_id=?", string(workspaceID), string(processingID), string(runID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row executionModel
	if err := query.Take(&row).Error; err != nil {
		return row, classify(tx.Statement.Context, err)
	}
	return row, nil
}

func (row executionModel) frozen() (*organizingworkflow.SynthesisFrozenInput, error) {
	if len(row.InputDocument) == 0 {
		if row.InputHash != nil {
			return nil, invalid("synthesis input hash has no snapshot")
		}
		return nil, nil
	}
	var frozen organizingworkflow.SynthesisFrozenInput
	if err := json.Unmarshal(row.InputDocument, &frozen); err != nil {
		return nil, invalid("synthesis input snapshot cannot be decoded")
	}
	if err := frozen.Validate(); err != nil {
		return nil, err
	}
	if row.InputHash == nil || frozen.RequestHash != *row.InputHash || string(frozen.ProcessingID) != row.ProcessingID || string(frozen.WorkflowRunID) != row.WorkflowRunID || string(frozen.SourceEvent.Source.WorkspaceID) != row.WorkspaceID {
		return nil, invalid("synthesis frozen input does not match its execution")
	}
	return &frozen, nil
}
