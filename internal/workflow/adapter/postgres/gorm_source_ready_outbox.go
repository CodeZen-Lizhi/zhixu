package workflowpostgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GORMSourceReadyOutbox stores Ingestion notifications in the existing Workflow
// outbox. Every operation participates in a live caller-owned transaction.
type GORMSourceReadyOutbox struct {
	database *gorm.DB
}

// NewGORMSourceReadyOutbox derives its database from the same pool as Ingestion
// and the consuming Workflow starter. It creates no connection or worker.
func NewGORMSourceReadyOutbox(pool *platformpostgres.Pool) (*GORMSourceReadyOutbox, error) {
	if pool == nil {
		return nil, gormWorkflowUnavailable("SOURCE_READY_OUTBOX_UNAVAILABLE", errors.New("source-ready outbox pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, gormWorkflowUnavailable("SOURCE_READY_OUTBOX_UNAVAILABLE", err)
	}
	if !validGORMWorkflowDatabase(database) {
		return nil, gormWorkflowUnavailable("SOURCE_READY_OUTBOX_UNAVAILABLE", errors.New("source-ready outbox database is unavailable"))
	}
	return &GORMSourceReadyOutbox{database: database}, nil
}

// AppendSourceReadyScoped appends the owner-verified tuple once per source
// version/projection. A repeated parse retains the first Attempt/time metadata.
func (outbox *GORMSourceReadyOutbox) AppendSourceReadyScoped(ctx context.Context, scope foundation.TransactionScope, ready ingestiondomain.SourceReady) error {
	if err := ready.Validate(); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_READY_OUTBOX_INPUT_INVALID", false, err)
	}
	transaction, err := outbox.sourceReadyTransaction(ctx, scope)
	if err != nil {
		return err
	}
	ready.OccurredAt = ready.OccurredAt.UTC().Truncate(time.Microsecond)
	payload, err := json.Marshal(ready)
	if err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_READY_OUTBOX_INPUT_INVALID", false, err)
	}
	id, err := (foundation.UUIDGenerator{}).New()
	if err != nil {
		return err
	}
	key := sourceReadyOutboxKey(ready)
	record := sourceReadyOutboxGORMRecord{
		ID: string(id), WorkspaceID: string(ready.WorkspaceID), EventType: application.SourceReadyEventType,
		IdempotencyKey: key, EventKey: key, SchemaVersion: 1, EventVersion: 1,
		Payload: workflowJSONB(payload), OccurredAt: ready.OccurredAt,
	}
	statement := transaction.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "idempotency_key"}}, DoNothing: true,
	}).Create(&record)
	if statement.Error != nil {
		return classifyGORMWorkflow(ctx, statement.Error, "SOURCE_READY_OUTBOX_APPEND_FAILED")
	}
	if statement.RowsAffected == 1 {
		return nil
	}
	// ON CONFLICT waits for a concurrent winner. Read in a new statement so
	// READ COMMITTED observes that winner rather than a pre-wait snapshot.
	var persisted sourceReadyOutboxGORMRecord
	if err := transaction.Select(sourceReadyOutboxColumns).Where("idempotency_key=?", key).Take(&persisted).Error; err != nil {
		return classifyGORMWorkflow(ctx, err, "SOURCE_READY_OUTBOX_REPLAY_FAILED")
	}
	fact, err := persisted.fact()
	if err != nil {
		return err
	}
	if !sameSourceReadyBinding(fact.Ready, ready) {
		return foundation.NewError(foundation.ErrorVersionConflict, "SOURCE_READY_OUTBOX_BINDING_CONFLICT", false, errors.New("source-ready event key has different source metadata"))
	}
	return nil
}

// ClaimSourceReadyScoped locks the oldest available notification. It does not
// borrow Reindex's per-Workspace delivery blockers or change queue state.
func (outbox *GORMSourceReadyOutbox) ClaimSourceReadyScoped(ctx context.Context, scope foundation.TransactionScope) (application.SourceReadyOutboxFact, bool, error) {
	transaction, err := outbox.sourceReadyTransaction(ctx, scope)
	if err != nil {
		return application.SourceReadyOutboxFact{}, false, err
	}
	var record sourceReadyOutboxGORMRecord
	statement := transaction.Raw(`SELECT `+sourceReadyOutboxColumns+`
		FROM workflow.outbox_event WHERE event_type=? AND published_at IS NULL
		ORDER BY occurred_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, application.SourceReadyEventType).Scan(&record)
	if statement.Error != nil {
		return application.SourceReadyOutboxFact{}, false, classifyGORMWorkflow(ctx, statement.Error, "SOURCE_READY_OUTBOX_CLAIM_FAILED")
	}
	if statement.RowsAffected == 0 {
		return application.SourceReadyOutboxFact{}, false, nil
	}
	fact, err := record.fact()
	if err != nil {
		return application.SourceReadyOutboxFact{}, false, err
	}
	return fact, true, nil
}

// PublishSourceReadyScoped consumes the exact claimed fact in the caller's
// Workflow-start transaction. A changed binding or repeated publish fails CAS.
func (outbox *GORMSourceReadyOutbox) PublishSourceReadyScoped(ctx context.Context, scope foundation.TransactionScope, fact application.SourceReadyOutboxFact) error {
	if !validGORMWorkflowID(fact.EventID) {
		return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_READY_OUTBOX_PUBLISH_INVALID", false, errors.New("source-ready event identity is invalid"))
	}
	if err := fact.Ready.Validate(); err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_READY_OUTBOX_PUBLISH_INVALID", false, err)
	}
	transaction, err := outbox.sourceReadyTransaction(ctx, scope)
	if err != nil {
		return err
	}
	fact.Ready.OccurredAt = fact.Ready.OccurredAt.UTC().Truncate(time.Microsecond)
	payload, err := json.Marshal(fact.Ready)
	if err != nil {
		return foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_READY_OUTBOX_PUBLISH_INVALID", false, err)
	}
	key := sourceReadyOutboxKey(fact.Ready)
	statement := transaction.Exec(`UPDATE workflow.outbox_event SET published_at=CURRENT_TIMESTAMP
		WHERE id=? AND workspace_id=? AND event_type=? AND run_id IS NULL
		  AND idempotency_key=? AND event_key=? AND schema_version=1 AND event_version=1
		  AND payload=?::jsonb AND occurred_at=? AND published_at IS NULL`,
		string(fact.EventID), string(fact.Ready.WorkspaceID), application.SourceReadyEventType,
		key, key, workflowJSONB(payload), fact.Ready.OccurredAt)
	if statement.Error != nil {
		return classifyGORMWorkflow(ctx, statement.Error, "SOURCE_READY_OUTBOX_PUBLISH_FAILED")
	}
	if statement.RowsAffected != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "SOURCE_READY_OUTBOX_PUBLISH_CONFLICT", false, errors.New("source-ready event was consumed or its binding changed"))
	}
	return nil
}

func (outbox *GORMSourceReadyOutbox) sourceReadyTransaction(ctx context.Context, scope foundation.TransactionScope) (*gorm.DB, error) {
	if outbox == nil || !validGORMWorkflowDatabase(outbox.database) {
		return nil, gormWorkflowUnavailable("SOURCE_READY_OUTBOX_UNAVAILABLE", errors.New("source-ready outbox is unavailable"))
	}
	if ctx == nil {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_READY_OUTBOX_CONTEXT_INVALID", false, errors.New("source-ready outbox context is nil"))
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, gormWorkflowUnavailable("SOURCE_READY_OUTBOX_TRANSACTION_UNAVAILABLE", err)
	}
	return transaction.WithContext(ctx), nil
}

const sourceReadyOutboxColumns = `id,workspace_id,run_id,event_type,idempotency_key,event_key,schema_version,event_version,payload,occurred_at,published_at`

type sourceReadyOutboxGORMRecord struct {
	ID             string        `gorm:"column:id;primaryKey"`
	WorkspaceID    string        `gorm:"column:workspace_id"`
	RunID          *string       `gorm:"column:run_id"`
	EventType      string        `gorm:"column:event_type"`
	IdempotencyKey string        `gorm:"column:idempotency_key"`
	EventKey       string        `gorm:"column:event_key"`
	SchemaVersion  int           `gorm:"column:schema_version"`
	EventVersion   int64         `gorm:"column:event_version"`
	Payload        workflowJSONB `gorm:"column:payload;type:jsonb"`
	OccurredAt     time.Time     `gorm:"column:occurred_at;autoCreateTime:false;autoUpdateTime:false"`
	PublishedAt    *time.Time    `gorm:"column:published_at;autoCreateTime:false;autoUpdateTime:false"`
}

func (sourceReadyOutboxGORMRecord) TableName() string { return "workflow.outbox_event" }

func (record sourceReadyOutboxGORMRecord) fact() (application.SourceReadyOutboxFact, error) {
	invalid := func() (application.SourceReadyOutboxFact, error) {
		return application.SourceReadyOutboxFact{}, foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_READY_OUTBOX_RECORD_INVALID", false, errors.New("source-ready outbox fact is invalid"))
	}
	if !validGORMWorkflowID(foundation.ID(record.ID)) || record.RunID != nil ||
		record.EventType != application.SourceReadyEventType || record.SchemaVersion != 1 || record.EventVersion != 1 ||
		len(record.Payload) > 4096 {
		return invalid()
	}
	var ready ingestiondomain.SourceReady
	decoder := json.NewDecoder(bytes.NewReader(record.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ready); err != nil {
		return invalid()
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return invalid()
	}
	if err := ready.Validate(); err != nil {
		return invalid()
	}
	if record.WorkspaceID != string(ready.WorkspaceID) || !record.OccurredAt.Equal(ready.OccurredAt) ||
		record.EventKey != sourceReadyOutboxKey(ready) || record.IdempotencyKey != record.EventKey {
		return invalid()
	}
	return application.SourceReadyOutboxFact{EventID: foundation.ID(record.ID), Ready: ready}, nil
}

func sourceReadyOutboxKey(ready ingestiondomain.SourceReady) string {
	return "ingestion.source-ready:v1:" + string(ready.SourceVersionID) + ":" + string(ready.ParseProjectionID)
}

func sameSourceReadyBinding(left, right ingestiondomain.SourceReady) bool {
	return left.WorkspaceID == right.WorkspaceID && left.SourceID == right.SourceID &&
		left.SourceVersionID == right.SourceVersionID && left.ContentArtifactID == right.ContentArtifactID &&
		left.ParseProjectionID == right.ParseProjectionID && left.ContentHash == right.ContentHash
}

var _ ingestiondomain.SourceReadyAppender = (*GORMSourceReadyOutbox)(nil)
var _ application.ScopedSourceReadyOutbox = (*GORMSourceReadyOutbox)(nil)
