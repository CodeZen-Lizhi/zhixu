package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Organizing Repository 所需的最小 pgx 边界。
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// Repository 原子持久化 Organizing Draft、Template、Snapshot 与 Start Outbox。
type Repository struct{ db DB }

var (
	_ organizingapp.DraftRepository    = (*Repository)(nil)
	_ organizingapp.TemplateRepository = (*Repository)(nil)
	_ organizingapp.StartRepository    = (*Repository)(nil)
)

// NewRepository 创建 PostgreSQL Organizing Repository。
func NewRepository(db DB) (*Repository, error) {
	if isNilInterface(db) {
		return nil, unavailable(errors.New("organizing database is required"))
	}
	return &Repository{db: db}, nil
}

func isNilInterface(value any) bool {
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

// FindDraftCommand 查找并严格匹配 Draft 命令回执。
func (repository *Repository) FindDraftCommand(ctx context.Context, binding organizingapp.CommandBinding) (organizingapp.DraftResult, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	if err := validateBinding(binding); err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	tx, err := repository.beginCommand(ctx, binding)
	if err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	receipt, found, err := loadReceipt(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return organizingapp.DraftResult{}, false, classify(err)
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	result, err := loadDraftReceiptResult(ctx, tx, receipt)
	if err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	result.Replayed = true
	if err := tx.Commit(ctx); err != nil {
		return organizingapp.DraftResult{}, false, classify(err)
	}
	return result, true, nil
}

// FindConfirmCommand 查找并严格匹配确认命令回执。
func (repository *Repository) FindConfirmCommand(ctx context.Context, binding organizingapp.CommandBinding) (organizingapp.ConfirmResult, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	if err := validateBinding(binding); err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	tx, err := repository.beginCommand(ctx, binding)
	if err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	receipt, found, err := loadReceipt(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return organizingapp.ConfirmResult{}, false, classify(err)
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	result, err := loadConfirmReceiptResult(ctx, tx, receipt)
	if err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	result.Replayed = true
	if err := tx.Commit(ctx); err != nil {
		return organizingapp.ConfirmResult{}, false, classify(err)
	}
	return result, true, nil
}

// CreateDraft 原子创建 Draft version 1 与命令回执。
func (repository *Repository) CreateDraft(ctx context.Context, record organizingapp.CreateDraftRecord) (organizingapp.DraftResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := validateCreateDraftRecord(record); err != nil {
		return organizingapp.DraftResult{}, err
	}
	tx, err := repository.beginCommand(ctx, record.Binding)
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if result, found, err := replayDraft(ctx, tx, record.Binding); err != nil || found {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return result, classify(err)
	}
	draft := record.Draft
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.draft(
		id,workspace_id,intent,status,template_revision_id,confirmed_snapshot_id,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,NULL,$6,$7,$8)`, string(draft.ID), string(draft.WorkspaceID), draft.Intent,
		string(draft.Status), string(draft.TemplateRevisionID), draft.Version, draft.CreatedAt.UTC(), draft.UpdatedAt.UTC()); err != nil {
		return organizingapp.DraftResult{}, classify(err)
	}
	if err := insertDraftVersion(ctx, tx, draft); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := insertDraftReceipt(ctx, tx, record.Binding, draft.ID, draft.Version, "", "", draft.UpdatedAt); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return organizingapp.DraftResult{}, classify(err)
	}
	return organizingapp.DraftResult{Draft: draft}, nil
}

// GetDraft 返回当前 Workspace Draft 与其同版本材料。
func (repository *Repository) GetDraft(ctx context.Context, workspaceID, draftID foundation.ID) (domain.Draft, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.Draft{}, err
	}
	if !validID(workspaceID) || !validID(draftID) {
		return domain.Draft{}, invalid(errors.New("organizing draft lookup identity is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.Draft{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	draft, err := loadCurrentDraft(ctx, tx, workspaceID, draftID, false)
	if err != nil {
		return domain.Draft{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Draft{}, classify(err)
	}
	return draft, nil
}

// UpdateDraft 对 intent 与模板修订执行一次 expected-version CAS，并复制该版本材料。
func (repository *Repository) UpdateDraft(ctx context.Context, record organizingapp.UpdateDraftRecord) (organizingapp.DraftResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := validateBinding(record.Binding); err != nil || record.Binding.CommandType != organizingapp.CommandUpdateDraft ||
		record.Binding.AggregateID == "" || !validID(record.TemplateRevisionID) || record.UpdatedAt.IsZero() {
		return organizingapp.DraftResult{}, invalid(errors.New("organizing draft update record is invalid"))
	}
	tx, err := repository.beginCommand(ctx, record.Binding)
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if result, found, err := replayDraft(ctx, tx, record.Binding); err != nil || found {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return result, classify(err)
	}
	current, err := loadCurrentDraft(ctx, tx, record.Binding.WorkspaceID, record.Binding.AggregateID, true)
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	if _, err := loadTemplateRevisionVisible(ctx, tx, record.Binding.WorkspaceID, record.TemplateRevisionID, true, true); err != nil {
		return organizingapp.DraftResult{}, err
	}
	next, err := domain.UpdateDraft(current, record.Binding.ExpectedVersion, record.Intent, record.TemplateRevisionID,
		canonicalTime(record.UpdatedAt))
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := insertDraftVersion(ctx, tx, next); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := copyDraftMaterials(ctx, tx, current, next.Version); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := updateDraftCurrent(ctx, tx, next, record.Binding.ExpectedVersion); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := insertDraftReceipt(ctx, tx, record.Binding, next.ID, next.Version, "", "", next.UpdatedAt); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return organizingapp.DraftResult{}, classify(err)
	}
	return organizingapp.DraftResult{Draft: next}, nil
}

// ReplaceMaterials 以批量写入持久化下一 Draft 版本的完整材料集合。
func (repository *Repository) ReplaceMaterials(ctx context.Context, record organizingapp.ReplaceMaterialsRecord) (organizingapp.DraftResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := validateBinding(record.Binding); err != nil || !validMaterialReplacementCommand(record.Binding.CommandType) ||
		record.Binding.AggregateID == "" || record.UpdatedAt.IsZero() {
		return organizingapp.DraftResult{}, invalid(errors.New("organizing material replacement record is invalid"))
	}
	tx, err := repository.beginCommand(ctx, record.Binding)
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if result, found, err := replayDraft(ctx, tx, record.Binding); err != nil || found {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return result, classify(err)
	}
	current, err := loadCurrentDraft(ctx, tx, record.Binding.WorkspaceID, record.Binding.AggregateID, true)
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	next, err := domain.ReplaceMaterials(current, record.Binding.ExpectedVersion, record.Materials, canonicalTime(record.UpdatedAt))
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := insertDraftVersion(ctx, tx, next); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := insertDraftMaterials(ctx, tx, next); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := updateDraftCurrent(ctx, tx, next, record.Binding.ExpectedVersion); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := insertDraftReceipt(ctx, tx, record.Binding, next.ID, next.Version, "", "", next.UpdatedAt); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return organizingapp.DraftResult{}, classify(err)
	}
	return organizingapp.DraftResult{Draft: next}, nil
}

// ConfirmDraft 在一个事务内冻结 Snapshot、材料、Draft terminal、回执和 Start Outbox。
func (repository *Repository) ConfirmDraft(ctx context.Context, record organizingapp.ConfirmRecord) (organizingapp.ConfirmResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := validateConfirmRecord(record); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	tx, err := repository.beginConfirmCommand(ctx, record.Binding)
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, err := confirmDraftInTransaction(ctx, tx, record)
	if err != nil {
		_ = tx.Rollback(context.Background())
		return repository.resolveConfirmSerialization(ctx, record.Binding, err)
	}
	if err := tx.Commit(ctx); err != nil {
		_ = tx.Rollback(context.Background())
		return repository.resolveConfirmSerialization(ctx, record.Binding, err)
	}
	return result, nil
}

func confirmDraftInTransaction(ctx context.Context, tx pgx.Tx, record organizingapp.ConfirmRecord) (organizingapp.ConfirmResult, error) {
	if result, found, err := replayConfirm(ctx, tx, record.Binding); err != nil || found {
		return result, err
	}
	current, err := loadCurrentDraft(ctx, tx, record.Binding.WorkspaceID, record.Binding.AggregateID, true)
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	template, err := loadTemplateRevisionVisible(ctx, tx, record.Binding.WorkspaceID, record.TemplateRevisionID, true, true)
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if current.TemplateRevisionID != record.TemplateRevisionID {
		return organizingapp.ConfirmResult{}, versionConflict(errors.New("organizing draft template revision changed before confirmation"))
	}
	if template.Template.ID != record.TemplateID || template.Revision.DeclarationHash != record.TemplateHash {
		return organizingapp.ConfirmResult{}, versionConflict(errors.New("organizing template revision changed before confirmation"))
	}
	if err := verifyTemplatePolicy(template.Revision.Declaration.Materials, record.FrozenMaterials); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	confirmed, snapshot, err := domain.Confirm(current, record.Binding.ExpectedVersion, record.SnapshotID,
		record.TemplateID, record.TemplateRevisionID, record.TemplateHash, record.FrozenMaterials,
		canonicalTime(record.ConfirmedAt))
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := record.Fence.VerifyFrozen(ctx, tx, record.Binding.WorkspaceID, record.FrozenMaterials); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.workflow_input_snapshot(
		id,workspace_id,draft_id,draft_version,template_id,template_revision_id,
		template_hash,intent,snapshot_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, string(snapshot.ID), string(snapshot.WorkspaceID),
		string(snapshot.DraftID), snapshot.DraftVersion, string(snapshot.TemplateID), string(snapshot.TemplateRevisionID),
		snapshot.TemplateHash, snapshot.Intent, snapshot.Hash, snapshot.CreatedAt.UTC()); err != nil {
		return organizingapp.ConfirmResult{}, classify(err)
	}
	if err := insertSnapshotMaterials(ctx, tx, snapshot, record.SnapshotMaterialIDs); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := insertDraftVersion(ctx, tx, confirmed); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := copyDraftMaterials(ctx, tx, current, confirmed.Version); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := updateDraftCurrent(ctx, tx, confirmed, record.Binding.ExpectedVersion); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.workflow_start_outbox(
		id,workspace_id,snapshot_id,template_revision_id,template_kind,status,available_at,
		attempt_count,last_error_code,lease_owner,lease_until,run_binding_id,version,
		created_at,updated_at,started_at,poisoned_at
	) VALUES($1,$2,$3,$4,$5,'PENDING',$6,0,NULL,NULL,NULL,NULL,1,$6,$6,NULL,NULL)`,
		string(record.OutboxID), string(snapshot.WorkspaceID), string(snapshot.ID), string(snapshot.TemplateRevisionID),
		string(template.Template.Kind), snapshot.CreatedAt.UTC()); err != nil {
		return organizingapp.ConfirmResult{}, classify(err)
	}
	if err := insertDraftReceipt(ctx, tx, record.Binding, confirmed.ID, confirmed.Version, snapshot.ID, record.OutboxID, snapshot.CreatedAt); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	return organizingapp.ConfirmResult{Draft: confirmed, Snapshot: snapshot, OutboxID: record.OutboxID}, nil
}

func (repository *Repository) resolveConfirmSerialization(
	ctx context.Context,
	binding organizingapp.CommandBinding,
	cause error,
) (organizingapp.ConfirmResult, error) {
	classified := classify(cause)
	var postgresError *pgconn.PgError
	if !errors.As(classified, &postgresError) || postgresError.Code != "40001" {
		return organizingapp.ConfirmResult{}, classified
	}
	replayed, found, err := repository.FindConfirmCommand(ctx, binding)
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if found {
		return replayed, nil
	}
	return organizingapp.ConfirmResult{}, classified
}

// GetSnapshot 返回不可变 Snapshot 与有序材料。
func (repository *Repository) GetSnapshot(ctx context.Context, workspaceID, snapshotID foundation.ID) (domain.Snapshot, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.Snapshot{}, err
	}
	if !validID(workspaceID) || !validID(snapshotID) {
		return domain.Snapshot{}, invalid(errors.New("organizing snapshot lookup identity is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.Snapshot{}, classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot, err := loadSnapshot(ctx, tx, workspaceID, snapshotID)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Snapshot{}, classify(err)
	}
	return snapshot, nil
}

func (repository *Repository) validateReady(ctx context.Context) error {
	if repository == nil || isNilInterface(repository.db) {
		return unavailable(errors.New("organizing repository is unavailable"))
	}
	if ctx == nil {
		return invalid(errors.New("organizing context is nil"))
	}
	return nil
}

func (repository *Repository) beginCommand(ctx context.Context, binding organizingapp.CommandBinding) (pgx.Tx, error) {
	return repository.beginCommandWithOptions(ctx, binding, pgx.TxOptions{})
}

func (repository *Repository) beginConfirmCommand(ctx context.Context, binding organizingapp.CommandBinding) (pgx.Tx, error) {
	return repository.beginCommandWithOptions(ctx, binding, pgx.TxOptions{IsoLevel: pgx.Serializable})
}

func (repository *Repository) beginCommandWithOptions(ctx context.Context, binding organizingapp.CommandBinding, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := repository.db.BeginTx(ctx, options)
	if err != nil {
		return nil, classify(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1 || chr(31) || $2,0))`,
		string(binding.WorkspaceID), binding.IdempotencyKey); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, classify(err)
	}
	return tx, nil
}

func validateBinding(binding organizingapp.CommandBinding) error {
	key := strings.TrimSpace(binding.IdempotencyKey)
	if !validID(binding.WorkspaceID) || key == "" || key != binding.IdempotencyKey ||
		len(key) > organizingapp.MaxIdempotencyKeyBytes || strings.ContainsAny(key, "\r\n\x00") ||
		!validHash(binding.RequestHash) || !validCommandType(binding.CommandType) || binding.ExpectedVersion < 0 ||
		(binding.AggregateID != "" && !validID(binding.AggregateID)) ||
		(binding.AggregateID == "" && !serverGeneratedAggregateCommand(binding.CommandType)) {
		return invalid(errors.New("organizing command binding is invalid"))
	}
	return nil
}

func serverGeneratedAggregateCommand(commandType string) bool {
	return commandType == organizingapp.CommandCreateDraft || commandType == organizingapp.CommandCreateTemplate ||
		commandType == organizingapp.CommandCloneTemplate
}

func validCommandType(value string) bool {
	switch value {
	case organizingapp.CommandCreateDraft, organizingapp.CommandUpdateDraft, organizingapp.CommandAddMaterial,
		organizingapp.CommandRemoveMaterial, organizingapp.CommandSetMaterialSelection,
		organizingapp.CommandReplaceSuggestions, organizingapp.CommandConfirmDraft,
		organizingapp.CommandCreateTemplate, organizingapp.CommandCloneTemplate, organizingapp.CommandReviseTemplate:
		return true
	default:
		return false
	}
}

func validMaterialReplacementCommand(value string) bool {
	return value == organizingapp.CommandAddMaterial || value == organizingapp.CommandRemoveMaterial ||
		value == organizingapp.CommandSetMaterialSelection || value == organizingapp.CommandReplaceSuggestions
}

func validHash(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validateCreateDraftRecord(record organizingapp.CreateDraftRecord) error {
	if err := validateBinding(record.Binding); err != nil {
		return err
	}
	if record.Binding.CommandType != organizingapp.CommandCreateDraft || record.Binding.ExpectedVersion != 0 ||
		record.Binding.AggregateID != record.Draft.ID || record.Draft.Version != 1 || record.Draft.Status != domain.DraftEditing ||
		record.Draft.WorkspaceID != record.Binding.WorkspaceID || len(record.Draft.Materials) != 0 || record.Draft.Validate() != nil {
		return invalid(errors.New("organizing create draft record is invalid"))
	}
	return nil
}

func validateConfirmRecord(record organizingapp.ConfirmRecord) error {
	if err := validateBinding(record.Binding); err != nil {
		return err
	}
	if record.Binding.CommandType != organizingapp.CommandConfirmDraft || record.Binding.AggregateID == "" || isNilInterface(record.Fence) ||
		!validID(record.SnapshotID) || !validID(record.OutboxID) || !validID(record.TemplateID) ||
		!validID(record.TemplateRevisionID) || !validHash(record.TemplateHash) || record.ConfirmedAt.IsZero() ||
		len(record.FrozenMaterials) < 1 || len(record.FrozenMaterials) != len(record.SnapshotMaterialIDs) ||
		len(record.FrozenMaterials) > domain.MaxSnapshotMaterials {
		return invalid(errors.New("organizing confirmation record is invalid"))
	}
	seenIDs := make(map[foundation.ID]struct{}, len(record.SnapshotMaterialIDs))
	for index, id := range record.SnapshotMaterialIDs {
		if !validID(id) {
			return invalid(errors.New("organizing snapshot material identity is invalid"))
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return invalid(errors.New("organizing snapshot material identity is duplicated"))
		}
		seenIDs[id] = struct{}{}
		if err := record.FrozenMaterials[index].Validate(); err != nil {
			return invalid(errors.New("organizing frozen material is invalid"))
		}
	}
	return nil
}

func loadReceipt(ctx context.Context, db queryer, workspaceID foundation.ID, key string) (receiptRow, bool, error) {
	receipt, err := scanReceipt(db.QueryRow(ctx, `SELECT `+receiptSelect+`
		FROM organizing.command_receipt AS r
		WHERE r.workspace_id=$1 AND r.idempotency_key=$2 FOR UPDATE`, string(workspaceID), key))
	if errors.Is(err, pgx.ErrNoRows) {
		return receiptRow{}, false, nil
	}
	if err != nil {
		return receiptRow{}, false, classify(err)
	}
	return receipt, true, nil
}

func replayDraft(ctx context.Context, tx pgx.Tx, binding organizingapp.CommandBinding) (organizingapp.DraftResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return organizingapp.DraftResult{}, found, err
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	result, err := loadDraftReceiptResult(ctx, tx, receipt)
	if err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func replayConfirm(ctx context.Context, tx pgx.Tx, binding organizingapp.CommandBinding) (organizingapp.ConfirmResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return organizingapp.ConfirmResult{}, found, err
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	result, err := loadConfirmReceiptResult(ctx, tx, receipt)
	if err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func loadDraftReceiptResult(ctx context.Context, db queryer, receipt receiptRow) (organizingapp.DraftResult, error) {
	if receipt.draftID == "" || receipt.resultDraftVersion < 1 {
		return organizingapp.DraftResult{}, inconsistent(errors.New("organizing draft receipt is incomplete"))
	}
	draft, err := loadDraftVersion(ctx, db, foundation.ID(receipt.workspaceID), foundation.ID(receipt.draftID), receipt.resultDraftVersion)
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	return organizingapp.DraftResult{Draft: draft}, nil
}

func loadConfirmReceiptResult(ctx context.Context, db queryer, receipt receiptRow) (organizingapp.ConfirmResult, error) {
	if receipt.commandType != organizingapp.CommandConfirmDraft || receipt.snapshotID == "" || receipt.outboxID == "" {
		return organizingapp.ConfirmResult{}, inconsistent(errors.New("organizing confirmation receipt is incomplete"))
	}
	draft, err := loadDraftVersion(ctx, db, foundation.ID(receipt.workspaceID), foundation.ID(receipt.draftID), receipt.resultDraftVersion)
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	snapshot, err := loadSnapshot(ctx, db, foundation.ID(receipt.workspaceID), foundation.ID(receipt.snapshotID))
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	return organizingapp.ConfirmResult{Draft: draft, Snapshot: snapshot, OutboxID: foundation.ID(receipt.outboxID)}, nil
}

func loadCurrentDraft(ctx context.Context, db queryer, workspaceID, draftID foundation.ID, lock bool) (domain.Draft, error) {
	query := `SELECT ` + draftSelect + ` FROM organizing.draft AS d WHERE d.workspace_id=$1 AND d.id=$2`
	if lock {
		query += ` FOR UPDATE OF d`
	}
	draft, err := scanDraft(db.QueryRow(ctx, query, string(workspaceID), string(draftID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Draft{}, notFound(err)
	}
	if err != nil {
		return domain.Draft{}, classify(err)
	}
	materials, err := loadDraftMaterials(ctx, db, workspaceID, draftID, draft.Version)
	if err != nil {
		return domain.Draft{}, err
	}
	draft.Materials = materials
	if err := draft.Validate(); err != nil {
		return domain.Draft{}, inconsistent(fmt.Errorf("stored organizing draft is invalid: %w", err))
	}
	return draft, nil
}

func loadDraftVersion(ctx context.Context, db queryer, workspaceID, draftID foundation.ID, version int64) (domain.Draft, error) {
	var draft domain.Draft
	var status, templateRevisionID, confirmedSnapshotID string
	err := db.QueryRow(ctx, `SELECT draft_id::text,workspace_id::text,intent,status,
		COALESCE(template_revision_id::text,''),COALESCE(confirmed_snapshot_id::text,''),
		version,draft_created_at,updated_at
		FROM organizing.draft_version WHERE workspace_id=$1 AND draft_id=$2 AND version=$3`,
		string(workspaceID), string(draftID), version).Scan(&draft.ID, &draft.WorkspaceID, &draft.Intent, &status,
		&templateRevisionID, &confirmedSnapshotID, &draft.Version, &draft.CreatedAt, &draft.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Draft{}, notFound(err)
	}
	if err != nil {
		return domain.Draft{}, classify(err)
	}
	draft.Status = domain.DraftStatus(status)
	draft.TemplateRevisionID = foundation.ID(templateRevisionID)
	draft.ConfirmedSnapshotID = foundation.ID(confirmedSnapshotID)
	draft.CreatedAt = draft.CreatedAt.UTC()
	draft.UpdatedAt = draft.UpdatedAt.UTC()
	materials, err := loadDraftMaterials(ctx, db, workspaceID, draftID, version)
	if err != nil {
		return domain.Draft{}, err
	}
	draft.Materials = materials
	if err := draft.Validate(); err != nil {
		return domain.Draft{}, inconsistent(fmt.Errorf("stored organizing draft version is invalid: %w", err))
	}
	return draft, nil
}

func loadDraftMaterials(ctx context.Context, db queryer, workspaceID, draftID foundation.ID, version int64) ([]domain.DraftMaterial, error) {
	rows, err := db.Query(ctx, `SELECT `+draftMaterialSelect+`
		FROM organizing.draft_material AS m
		WHERE m.workspace_id=$1 AND m.draft_id=$2 AND m.draft_version=$3
		ORDER BY m.position`, string(workspaceID), string(draftID), version)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	materials := make([]domain.DraftMaterial, 0)
	for rows.Next() {
		material, err := scanDraftMaterial(rows)
		if err != nil {
			return nil, classify(err)
		}
		if material.Position != len(materials) {
			return nil, inconsistent(errors.New("stored organizing material positions are not contiguous"))
		}
		materials = append(materials, material)
		if len(materials) > domain.MaxDraftMaterials {
			return nil, inconsistent(errors.New("stored organizing material set exceeds the bound"))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err)
	}
	return materials, nil
}

func loadSnapshot(ctx context.Context, db queryer, workspaceID, snapshotID foundation.ID) (domain.Snapshot, error) {
	snapshot, err := scanSnapshot(db.QueryRow(ctx, `SELECT `+snapshotSelect+`
		FROM organizing.workflow_input_snapshot AS s WHERE s.workspace_id=$1 AND s.id=$2`,
		string(workspaceID), string(snapshotID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Snapshot{}, notFound(err)
	}
	if err != nil {
		return domain.Snapshot{}, classify(err)
	}
	rows, err := db.Query(ctx, `SELECT `+snapshotMaterialSelect+`
		FROM organizing.workflow_input_material AS m
		WHERE m.workspace_id=$1 AND m.snapshot_id=$2 ORDER BY m.position`, string(workspaceID), string(snapshotID))
	if err != nil {
		return domain.Snapshot{}, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		reference, position, materialWorkspace, materialSnapshot, err := scanSnapshotMaterial(rows)
		if err != nil {
			return domain.Snapshot{}, classify(err)
		}
		if position != len(snapshot.Materials) || materialWorkspace != string(workspaceID) || materialSnapshot != string(snapshotID) {
			return domain.Snapshot{}, inconsistent(errors.New("stored snapshot material binding is invalid"))
		}
		snapshot.Materials = append(snapshot.Materials, reference)
		if len(snapshot.Materials) > domain.MaxSnapshotMaterials {
			return domain.Snapshot{}, inconsistent(errors.New("stored snapshot material set exceeds the bound"))
		}
	}
	if err := rows.Err(); err != nil {
		return domain.Snapshot{}, classify(err)
	}
	if err := snapshot.Validate(); err != nil {
		return domain.Snapshot{}, inconsistent(fmt.Errorf("stored organizing snapshot is invalid: %w", err))
	}
	return snapshot, nil
}

func insertDraftVersion(ctx context.Context, tx pgx.Tx, draft domain.Draft) error {
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.draft_version(
		workspace_id,draft_id,version,intent,status,template_revision_id,confirmed_snapshot_id,draft_created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,$8,$9)`,
		string(draft.WorkspaceID), string(draft.ID), draft.Version, draft.Intent, string(draft.Status),
		string(draft.TemplateRevisionID), string(draft.ConfirmedSnapshotID), draft.CreatedAt.UTC(), draft.UpdatedAt.UTC()); err != nil {
		return classify(err)
	}
	return nil
}

func updateDraftCurrent(ctx context.Context, tx pgx.Tx, draft domain.Draft, expectedVersion int64) error {
	tag, err := tx.Exec(ctx, `UPDATE organizing.draft SET
		intent=$1,status=$2,template_revision_id=NULLIF($3,'')::uuid,
		confirmed_snapshot_id=NULLIF($4,'')::uuid,version=$5,updated_at=$6
		WHERE workspace_id=$7 AND id=$8 AND version=$9`, draft.Intent, string(draft.Status),
		string(draft.TemplateRevisionID), string(draft.ConfirmedSnapshotID), draft.Version, draft.UpdatedAt.UTC(),
		string(draft.WorkspaceID), string(draft.ID), expectedVersion)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return versionConflict(errors.New("organizing draft compare-and-swap lost"))
	}
	return nil
}

func copyDraftMaterials(ctx context.Context, tx pgx.Tx, current domain.Draft, nextVersion int64) error {
	tag, err := tx.Exec(ctx, `INSERT INTO organizing.draft_material(
		id,workspace_id,draft_id,draft_version,position,kind,source_version_id,document_id,
		article_revision_id,claim_id,collection_id,origin_collection_id,profile_revision_id,
		material_version,content_hash,query_hash,read_model_revision,evidence,title,reasons,
		origin,availability,score,selected,created_at
	) SELECT id,workspace_id,draft_id,$4,position,kind,source_version_id,document_id,
		article_revision_id,claim_id,collection_id,origin_collection_id,profile_revision_id,
		material_version,content_hash,query_hash,read_model_revision,evidence,title,reasons,
			origin,availability,score,selected,created_at
	FROM organizing.draft_material
	WHERE workspace_id=$1 AND draft_id=$2 AND draft_version=$3`, string(current.WorkspaceID),
		string(current.ID), current.Version, nextVersion)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != int64(len(current.Materials)) {
		return inconsistent(errors.New("organizing draft material copy count is inconsistent"))
	}
	return nil
}

func insertDraftMaterials(ctx context.Context, tx pgx.Tx, draft domain.Draft) error {
	if len(draft.Materials) == 0 {
		return nil
	}
	const columnsPerRow = 25
	var builder strings.Builder
	builder.WriteString(`INSERT INTO organizing.draft_material(
		id,workspace_id,draft_id,draft_version,position,kind,source_version_id,document_id,
		article_revision_id,claim_id,collection_id,origin_collection_id,profile_revision_id,
		material_version,content_hash,query_hash,read_model_revision,evidence,title,reasons,
		origin,availability,score,selected,created_at) VALUES `)
	arguments := make([]any, 0, len(draft.Materials)*columnsPerRow)
	for index, material := range draft.Materials {
		if err := material.Validate(); err != nil || material.DraftID != draft.ID || material.Position != index {
			return invalid(errors.New("organizing draft material batch is invalid"))
		}
		encodedEvidence, err := encodeEvidence(material.Ref.Evidence)
		if err != nil {
			return invalid(err)
		}
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('(')
		for column := 0; column < columnsPerRow; column++ {
			if column > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(fmt.Sprintf("$%d", index*columnsPerRow+column+1))
		}
		builder.WriteByte(')')
		reasons := make([]string, len(material.Reasons))
		for reasonIndex, reason := range material.Reasons {
			reasons[reasonIndex] = string(reason)
		}
		ref := material.Ref
		arguments = append(arguments, string(material.ID), string(draft.WorkspaceID), string(draft.ID), draft.Version,
			material.Position, string(ref.Kind), nullableID(ref.SourceVersionID), nullableID(ref.DocumentID),
			nullableID(ref.ArticleRevisionID), nullableID(ref.ClaimID), nullableID(ref.CollectionID),
			nullableID(ref.OriginCollectionID), nullableID(ref.ProfileRevisionID), ref.Version,
			nullableText(ref.ContentHash), nullableText(ref.QueryHash), nullableText(ref.ReadModelRevision), encodedEvidence,
			material.Title, reasons, string(material.Origin), string(material.Availability), material.Score,
			material.Selected, material.CreatedAt.UTC())
	}
	if _, err := tx.Exec(ctx, builder.String(), arguments...); err != nil {
		return classify(err)
	}
	return nil
}

func verifyTemplatePolicy(policy domain.MaterialPolicy, materials []domain.MaterialRef) error {
	if len(materials) < policy.MinMaterials || len(materials) > policy.MaxMaterials || len(materials) > domain.MaxSnapshotMaterials {
		return invalid(errors.New("organizing confirmation material count violates template policy"))
	}
	allowed := make(map[domain.MaterialKind]struct{}, len(policy.AllowedKinds))
	for _, kind := range policy.AllowedKinds {
		allowed[kind] = struct{}{}
	}
	for _, material := range materials {
		if _, ok := allowed[material.Kind]; !ok {
			return invalid(errors.New("organizing confirmation material kind violates template policy"))
		}
	}
	return nil
}

func insertSnapshotMaterials(ctx context.Context, tx pgx.Tx, snapshot domain.Snapshot, ids []foundation.ID) error {
	if len(snapshot.Materials) == 0 || len(snapshot.Materials) != len(ids) {
		return invalid(errors.New("organizing snapshot material batch is invalid"))
	}
	const columnsPerRow = 18
	var builder strings.Builder
	builder.WriteString(`INSERT INTO organizing.workflow_input_material(
		id,workspace_id,snapshot_id,position,kind,source_version_id,document_id,article_revision_id,
		claim_id,collection_id,origin_collection_id,profile_revision_id,material_version,content_hash,
		query_hash,read_model_revision,evidence,created_at) VALUES `)
	arguments := make([]any, 0, len(snapshot.Materials)*columnsPerRow)
	for index, reference := range snapshot.Materials {
		encodedEvidence, err := encodeEvidence(reference.Evidence)
		if err != nil {
			return invalid(err)
		}
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('(')
		for column := 0; column < columnsPerRow; column++ {
			if column > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(fmt.Sprintf("$%d", index*columnsPerRow+column+1))
		}
		builder.WriteByte(')')
		arguments = append(arguments, string(ids[index]), string(snapshot.WorkspaceID), string(snapshot.ID), index,
			string(reference.Kind), nullableID(reference.SourceVersionID), nullableID(reference.DocumentID),
			nullableID(reference.ArticleRevisionID), nullableID(reference.ClaimID), nullableID(reference.CollectionID),
			nullableID(reference.OriginCollectionID), nullableID(reference.ProfileRevisionID), reference.Version,
			nullableText(reference.ContentHash), nullableText(reference.QueryHash), nullableText(reference.ReadModelRevision),
			encodedEvidence, snapshot.CreatedAt.UTC())
	}
	if _, err := tx.Exec(ctx, builder.String(), arguments...); err != nil {
		return classify(err)
	}
	return nil
}

func insertDraftReceipt(ctx context.Context, tx pgx.Tx, binding organizingapp.CommandBinding, draftID foundation.ID,
	resultVersion int64, snapshotID, outboxID foundation.ID, createdAt time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,aggregate_id,expected_version,
		draft_id,result_draft_version,snapshot_id,outbox_id,template_id,result_template_version,
		template_revision_id,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,NULLIF($10,'')::uuid,NULL,NULL,NULL,$11)`,
		string(binding.WorkspaceID), binding.IdempotencyKey, binding.RequestHash, binding.CommandType,
		string(draftID), binding.ExpectedVersion, string(draftID), resultVersion, string(snapshotID), string(outboxID),
		canonicalTime(createdAt)); err != nil {
		return classify(err)
	}
	return nil
}

func nullableID(value foundation.ID) any {
	if value == "" {
		return nil
	}
	return string(value)
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
