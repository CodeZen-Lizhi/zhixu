package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// FindDraftCommand 在命令锁下精确重放原 Draft 版本。
func (repository *GORMRepository) FindDraftCommand(ctx context.Context, binding organizingapp.CommandBinding) (organizingapp.DraftResult, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	if err := validateBinding(binding); err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	var result organizingapp.DraftResult
	var found bool
	err := repository.command(ctx, binding, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var err error
		result, found, err = gormReplayDraft(tx, binding)
		return err
	})
	if err != nil {
		return organizingapp.DraftResult{}, false, err
	}
	return result, found, nil
}

// FindConfirmCommand 精确恢复确认事实，不重新读取当前材料 owner。
func (repository *GORMRepository) FindConfirmCommand(ctx context.Context, binding organizingapp.CommandBinding) (organizingapp.ConfirmResult, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	if err := validateBinding(binding); err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	var result organizingapp.ConfirmResult
	var found bool
	err := repository.command(ctx, binding, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var err error
		result, found, err = gormReplayConfirm(tx, binding)
		return err
	})
	if err != nil {
		return organizingapp.ConfirmResult{}, false, err
	}
	return result, found, nil
}

// CreateDraft 原子保存 Draft、初始版本与命令回执。
func (repository *GORMRepository) CreateDraft(ctx context.Context, record organizingapp.CreateDraftRecord) (organizingapp.DraftResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := validateCreateDraftRecord(record); err != nil {
		return organizingapp.DraftResult{}, err
	}
	var result organizingapp.DraftResult
	err := repository.command(ctx, record.Binding, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		replayed, found, err := gormReplayDraft(tx, record.Binding)
		if err != nil || found {
			result = replayed
			return err
		}
		model := draftToModel(record.Draft)
		if err := tx.Create(&model).Error; err != nil {
			return classifyGORM(err)
		}
		if err := gormInsertDraftVersion(tx, record.Draft); err != nil {
			return err
		}
		if err := gormInsertDraftReceipt(tx, record.Binding, record.Draft.ID, record.Draft.Version, "", "", record.Draft.UpdatedAt); err != nil {
			return err
		}
		result = organizingapp.DraftResult{Draft: record.Draft}
		return nil
	})
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	return result, nil
}

// GetDraft 用只读 Repeatable Read 保持 Draft 与材料版本一致。
func (repository *GORMRepository) GetDraft(ctx context.Context, workspaceID, draftID foundation.ID) (domain.Draft, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.Draft{}, err
	}
	if !validID(workspaceID) || !validID(draftID) {
		return domain.Draft{}, invalid(errors.New("organizing draft lookup identity is invalid"))
	}
	var result domain.Draft
	err := repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true},
		func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			var err error
			result, err = gormLoadCurrentDraft(tx, workspaceID, draftID, false)
			return err
		})
	if err != nil {
		return domain.Draft{}, err
	}
	return result, nil
}

// UpdateDraft 对 Draft 当前指针执行 CAS，批量保存新版本材料。
func (repository *GORMRepository) UpdateDraft(ctx context.Context, record organizingapp.UpdateDraftRecord) (organizingapp.DraftResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := validateBinding(record.Binding); err != nil || record.Binding.CommandType != organizingapp.CommandUpdateDraft ||
		record.Binding.AggregateID == "" || !validID(record.TemplateRevisionID) || record.UpdatedAt.IsZero() {
		return organizingapp.DraftResult{}, invalid(errors.New("organizing draft update record is invalid"))
	}
	var result organizingapp.DraftResult
	err := repository.command(ctx, record.Binding, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		replayed, found, err := gormReplayDraft(tx, record.Binding)
		if err != nil || found {
			result = replayed
			return err
		}
		current, err := gormLoadCurrentDraft(tx, record.Binding.WorkspaceID, record.Binding.AggregateID, true)
		if err != nil {
			return err
		}
		if _, err := gormLoadTemplateRevisionVisible(tx, record.Binding.WorkspaceID, record.TemplateRevisionID, true, true); err != nil {
			return err
		}
		next, err := domain.UpdateDraft(current, record.Binding.ExpectedVersion, record.Intent, record.TemplateRevisionID, canonicalTime(record.UpdatedAt))
		if err != nil {
			return err
		}
		if err := gormSaveDraftVersion(tx, record.Binding, next); err != nil {
			return err
		}
		result = organizingapp.DraftResult{Draft: next}
		return nil
	})
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	return result, nil
}

// ReplaceMaterials 以一次有界批量写入替换下一版本材料。
func (repository *GORMRepository) ReplaceMaterials(ctx context.Context, record organizingapp.ReplaceMaterialsRecord) (organizingapp.DraftResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.DraftResult{}, err
	}
	if err := validateBinding(record.Binding); err != nil || !validMaterialReplacementCommand(record.Binding.CommandType) || record.Binding.AggregateID == "" || record.UpdatedAt.IsZero() {
		return organizingapp.DraftResult{}, invalid(errors.New("organizing material replacement record is invalid"))
	}
	var result organizingapp.DraftResult
	err := repository.command(ctx, record.Binding, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		replayed, found, err := gormReplayDraft(tx, record.Binding)
		if err != nil || found {
			result = replayed
			return err
		}
		current, err := gormLoadCurrentDraft(tx, record.Binding.WorkspaceID, record.Binding.AggregateID, true)
		if err != nil {
			return err
		}
		next, err := domain.ReplaceMaterials(current, record.Binding.ExpectedVersion, record.Materials, canonicalTime(record.UpdatedAt))
		if err != nil {
			return err
		}
		if err := gormSaveDraftVersion(tx, record.Binding, next); err != nil {
			return err
		}
		result = organizingapp.DraftResult{Draft: next}
		return nil
	})
	if err != nil {
		return organizingapp.DraftResult{}, err
	}
	return result, nil
}

func gormSaveDraftVersion(tx *gorm.DB, binding organizingapp.CommandBinding, next domain.Draft) error {
	if err := gormInsertDraftVersion(tx, next); err != nil {
		return err
	}
	if err := gormInsertDraftMaterials(tx, next); err != nil {
		return err
	}
	if err := gormUpdateDraftCurrent(tx, next, binding.ExpectedVersion); err != nil {
		return err
	}
	return gormInsertDraftReceipt(tx, binding, next.ID, next.Version, "", "", next.UpdatedAt)
}

// ConfirmDraft 在 Serializable scope 中重验 owner 并原子冻结所有确认事实。
func (repository *GORMRepository) ConfirmDraft(ctx context.Context, record organizingapp.ConfirmRecord) (organizingapp.ConfirmResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := validateConfirmRecord(record); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	var result organizingapp.ConfirmResult
	err := repository.command(ctx, record.Binding, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationSerializable},
		func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
			var err error
			result, err = gormConfirmDraft(ctx, scope, tx, record.Fence, record)
			return err
		})
	if err == nil {
		return result, nil
	}
	// Serializable winner replay 必须在失败事务已经回滚后读取。
	if platformpostgres.SQLState(err) == "40001" {
		replayed, found, replayErr := repository.FindConfirmCommand(ctx, record.Binding)
		if replayErr != nil {
			return organizingapp.ConfirmResult{}, replayErr
		}
		if found {
			return replayed, nil
		}
	}
	return organizingapp.ConfirmResult{}, err
}

func gormConfirmDraft(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB, fence organizingapp.ScopedFrozenMaterialFence,
	record organizingapp.ConfirmRecord,
) (organizingapp.ConfirmResult, error) {
	if result, found, err := gormReplayConfirm(tx, record.Binding); err != nil || found {
		return result, err
	}
	current, err := gormLoadCurrentDraft(tx, record.Binding.WorkspaceID, record.Binding.AggregateID, true)
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	template, err := gormLoadTemplateRevisionVisible(tx, record.Binding.WorkspaceID, record.TemplateRevisionID, true, true)
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
	confirmed, snapshot, err := domain.Confirm(current, record.Binding.ExpectedVersion, record.SnapshotID, record.TemplateID,
		record.TemplateRevisionID, record.TemplateHash, record.FrozenMaterials, canonicalTime(record.ConfirmedAt))
	if err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := fence.VerifyFrozenScoped(ctx, scope, record.Binding.WorkspaceID, record.FrozenMaterials); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	model := snapshotModel{ID: string(snapshot.ID), WorkspaceID: string(snapshot.WorkspaceID), DraftID: string(snapshot.DraftID),
		DraftVersion: snapshot.DraftVersion, TemplateID: string(snapshot.TemplateID), TemplateRevisionID: string(snapshot.TemplateRevisionID),
		TemplateHash: snapshot.TemplateHash, Intent: snapshot.Intent, SnapshotHash: snapshot.Hash, CreatedAt: snapshot.CreatedAt.UTC()}
	if err := tx.Create(&model).Error; err != nil {
		return organizingapp.ConfirmResult{}, classifyGORM(err)
	}
	if err := gormInsertSnapshotMaterials(tx, snapshot, record.SnapshotMaterialIDs); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := gormInsertDraftVersion(tx, confirmed); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := gormInsertDraftMaterials(tx, confirmed); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	if err := gormUpdateDraftCurrent(tx, confirmed, record.Binding.ExpectedVersion); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	outbox := startOutboxModel{ID: string(record.OutboxID), WorkspaceID: string(snapshot.WorkspaceID), SnapshotID: string(snapshot.ID),
		TemplateRevisionID: string(snapshot.TemplateRevisionID), TemplateKind: string(template.Template.Kind), Status: string(organizingapp.StartPending),
		AvailableAt: snapshot.CreatedAt.UTC(), Version: 1, CreatedAt: snapshot.CreatedAt.UTC(), UpdatedAt: snapshot.CreatedAt.UTC()}
	if err := tx.Create(&outbox).Error; err != nil {
		return organizingapp.ConfirmResult{}, classifyGORM(err)
	}
	if err := gormInsertDraftReceipt(tx, record.Binding, confirmed.ID, confirmed.Version, snapshot.ID, record.OutboxID, snapshot.CreatedAt); err != nil {
		return organizingapp.ConfirmResult{}, err
	}
	return organizingapp.ConfirmResult{Draft: confirmed, Snapshot: snapshot, OutboxID: record.OutboxID}, nil
}

// GetSnapshot 在只读快照事务内读取不可变材料集合。
func (repository *GORMRepository) GetSnapshot(ctx context.Context, workspaceID, snapshotID foundation.ID) (domain.Snapshot, error) {
	if err := repository.validateReady(ctx); err != nil {
		return domain.Snapshot{}, err
	}
	if !validID(workspaceID) || !validID(snapshotID) {
		return domain.Snapshot{}, invalid(errors.New("organizing snapshot lookup identity is invalid"))
	}
	var result domain.Snapshot
	err := repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true},
		func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			var err error
			result, err = gormLoadSnapshot(tx, workspaceID, snapshotID)
			return err
		})
	if err != nil {
		return domain.Snapshot{}, err
	}
	return result, nil
}
