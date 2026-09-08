package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EnsureBuiltIns 在 Catalog 锁下幂等安装四个服务器模板。
func (repository *GORMRepository) EnsureBuiltIns(ctx context.Context, createdAt time.Time) error {
	if err := repository.validateReady(ctx); err != nil {
		return err
	}
	createdAt = canonicalTime(createdAt)
	if createdAt.IsZero() {
		return invalid(errors.New("built-in template creation time is invalid"))
	}
	templates, revisions, err := domain.BuiltInTemplates(createdAt)
	if err != nil {
		return err
	}
	return repository.within(ctx, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, "organizing:built-in-templates:v1").Error; err != nil {
			return classifyGORM(err)
		}
		for index, template := range templates {
			existing, loadErr := gormScanTemplate(gormCurrentTemplateQuery(tx).Where("t.id = ?", string(template.ID)))
			if loadErr == nil {
				if !sameBuiltIn(existing, template, revisions[index]) {
					return inconsistent(errors.New("stored built-in template differs from the code catalog"))
				}
				continue
			}
			var classified *foundation.Error
			if !errors.As(loadErr, &classified) || classified.Kind != foundation.ErrorNotFound {
				return loadErr
			}
			if err := gormInsertTemplate(tx, template, revisions[index]); err != nil {
				return err
			}
		}
		return nil
	})
}

// FindTemplateCommand 精确恢复回执引用的历史 Template Revision。
func (repository *GORMRepository) FindTemplateCommand(ctx context.Context, binding organizingapp.CommandBinding) (organizingapp.TemplateResult, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	if err := validateBinding(binding); err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	var result organizingapp.TemplateResult
	var found bool
	err := repository.command(ctx, binding, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		var err error
		result, found, err = gormReplayTemplate(tx, binding)
		return err
	})
	if err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	return result, found, nil
}

// CreateTemplate 原子创建自定义 Template、Revision 与回执。
func (repository *GORMRepository) CreateTemplate(ctx context.Context, record organizingapp.CreateTemplateRecord) (organizingapp.TemplateResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	if err := validateCreateTemplateRecord(record); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	var result organizingapp.TemplateResult
	err := repository.command(ctx, record.Binding, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		replayed, found, err := gormReplayTemplate(tx, record.Binding)
		if err != nil || found {
			result = replayed
			return err
		}
		if err := gormInsertTemplate(tx, record.Template, record.Revision); err != nil {
			return err
		}
		if err := gormInsertTemplateReceipt(tx, record.Binding, record.Template, record.Revision); err != nil {
			return err
		}
		result = organizingapp.TemplateResult{Detail: organizingapp.TemplateDetail{Template: record.Template, Revision: record.Revision}}
		return nil
	})
	if err != nil {
		return organizingapp.TemplateResult{}, err
	}
	return result, nil
}

// ReviseTemplate 追加 Revision 后对自定义模板 current pointer 执行 CAS。
func (repository *GORMRepository) ReviseTemplate(ctx context.Context, record organizingapp.ReviseTemplateRecord) (organizingapp.TemplateResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	if err := validateReviseTemplateRecord(record); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	var result organizingapp.TemplateResult
	err := repository.command(ctx, record.Binding, foundation.TransactionOptions{}, func(_ context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
		replayed, found, err := gormReplayTemplate(tx, record.Binding)
		if err != nil || found {
			result = replayed
			return err
		}
		current, err := gormLoadTemplateCurrentVisible(tx, record.Binding.WorkspaceID, record.Binding.AggregateID, true)
		if err != nil {
			return err
		}
		template, revision, err := domain.ReviseCustomTemplate(current.Template, record.Binding.ExpectedVersion,
			record.Revision.ID, record.Revision.Declaration, record.Revision.CreatedAt)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(template, record.Template) || !reflect.DeepEqual(revision, record.Revision) {
			return invalid(errors.New("organizing template revision record does not match the domain transition"))
		}
		if err := gormInsertTemplateRevision(tx, template.Owner, revision); err != nil {
			return err
		}
		updated := tx.Model(&templateModel{}).Where("id = ? AND workspace_id = ? AND version = ? AND owner = ?",
			string(template.ID), string(template.WorkspaceID), record.Binding.ExpectedVersion, string(domain.TemplateCustom)).
			Updates(map[string]any{"current_revision_id": string(template.CurrentRevisionID), "version": template.Version, "updated_at": template.UpdatedAt.UTC()})
		if updated.Error != nil {
			return classifyGORM(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return versionConflict(errors.New("organizing template compare-and-swap lost"))
		}
		if err := gormInsertTemplateReceipt(tx, record.Binding, template, revision); err != nil {
			return err
		}
		result = organizingapp.TemplateResult{Detail: organizingapp.TemplateDetail{Template: template, Revision: revision}}
		return nil
	})
	if err != nil {
		return organizingapp.TemplateResult{}, err
	}
	return result, nil
}

// GetTemplate 返回 Workspace 可见的当前 Revision。
func (repository *GORMRepository) GetTemplate(ctx context.Context, workspaceID, templateID foundation.ID) (organizingapp.TemplateDetail, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	if !validID(workspaceID) || !validID(templateID) {
		return organizingapp.TemplateDetail{}, invalid(errors.New("organizing template lookup identity is invalid"))
	}
	return gormLoadTemplateCurrentVisible(repository.database.WithContext(ctx), workspaceID, templateID, false)
}

// GetTemplateRevision 读取仍为 current 的可见 Revision。
func (repository *GORMRepository) GetTemplateRevision(ctx context.Context, workspaceID, revisionID foundation.ID) (organizingapp.TemplateDetail, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	if !validID(workspaceID) || !validID(revisionID) {
		return organizingapp.TemplateDetail{}, invalid(errors.New("organizing template revision lookup identity is invalid"))
	}
	return gormLoadTemplateRevisionVisible(repository.database.WithContext(ctx), workspaceID, revisionID, true, false)
}

// GetFrozenTemplateRevision 读取 Snapshot 固定的历史 Revision。
func (repository *GORMRepository) GetFrozenTemplateRevision(ctx context.Context, workspaceID, templateID, revisionID foundation.ID) (organizingapp.TemplateDetail, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	if !validID(workspaceID) || !validID(templateID) || !validID(revisionID) {
		return organizingapp.TemplateDetail{}, invalid(errors.New("organizing frozen template revision lookup identity is invalid"))
	}
	detail, err := gormLoadTemplateRevisionVisible(repository.database.WithContext(ctx), workspaceID, revisionID, false, false)
	if err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	if detail.Template.ID != templateID || detail.Revision.TemplateID != templateID {
		return organizingapp.TemplateDetail{}, notFound(errors.New("organizing frozen template revision was not found"))
	}
	return detail, nil
}

// ListTemplates 返回全局内置与当前 Workspace 自定义模板的有界有序列表。
func (repository *GORMRepository) ListTemplates(ctx context.Context, query organizingapp.TemplateListQuery) (organizingapp.TemplatePage, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplatePage{}, err
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 || (query.Kind != "" && !query.Kind.Valid()) {
		return organizingapp.TemplatePage{}, invalid(errors.New("organizing template list query is invalid"))
	}
	statement := gormCurrentTemplateQuery(repository.database.WithContext(ctx)).Where("t.workspace_id = ? OR t.workspace_id IS NULL", string(query.WorkspaceID))
	if query.Kind != "" {
		statement = statement.Where("t.kind = ?", string(query.Kind))
	}
	rows, err := statement.Order("(t.owner='BUILT_IN') DESC,t.kind,t.updated_at DESC,t.id").Limit(query.Limit).Rows()
	if err != nil {
		return organizingapp.TemplatePage{}, classifyGORM(err)
	}
	defer rows.Close()
	items := make([]organizingapp.TemplateDetail, 0, query.Limit)
	for rows.Next() {
		detail, err := scanTemplateDetail(rows)
		if err != nil {
			return organizingapp.TemplatePage{}, classifyGORM(err)
		}
		items = append(items, detail)
	}
	if err := rows.Err(); err != nil {
		return organizingapp.TemplatePage{}, classifyGORM(err)
	}
	return organizingapp.TemplatePage{Items: items}, nil
}

func gormCurrentTemplateQuery(tx *gorm.DB) *gorm.DB {
	return tx.Table("organizing.template AS t").Select(templateDetailSelect).
		Joins("JOIN organizing.template_revision AS r ON r.id=t.current_revision_id AND r.template_id=t.id")
}

func gormLoadTemplateCurrentVisible(tx *gorm.DB, workspaceID, templateID foundation.ID, lock bool) (organizingapp.TemplateDetail, error) {
	query := gormCurrentTemplateQuery(tx).Where("t.id = ? AND (t.workspace_id = ? OR t.workspace_id IS NULL)", string(templateID), string(workspaceID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "t"}})
	}
	return gormScanTemplate(query)
}

func gormLoadTemplateRevisionVisible(tx *gorm.DB, workspaceID, revisionID foundation.ID, requireCurrent, lockCurrent bool) (organizingapp.TemplateDetail, error) {
	query := tx.Table("organizing.template AS t").Select(templateDetailSelect).
		Joins("JOIN organizing.template_revision AS r ON r.template_id=t.id").
		Where("r.id = ? AND (t.workspace_id = ? OR t.workspace_id IS NULL)", string(revisionID), string(workspaceID))
	if requireCurrent {
		query = query.Where("t.current_revision_id=r.id")
	}
	if lockCurrent {
		query = query.Clauses(clause.Locking{Strength: "SHARE", Table: clause.Table{Name: "t"}})
	}
	return gormScanTemplate(query)
}

func gormScanTemplate(query *gorm.DB) (organizingapp.TemplateDetail, error) {
	detail, err := scanTemplateDetail(query.Row())
	if gormNoRows(err) {
		return organizingapp.TemplateDetail{}, notFound(err)
	}
	if err != nil {
		return organizingapp.TemplateDetail{}, classifyGORM(err)
	}
	return detail, nil
}

func gormReplayTemplate(tx *gorm.DB, binding organizingapp.CommandBinding) (organizingapp.TemplateResult, bool, error) {
	receipt, found, err := gormLoadReceipt(tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return organizingapp.TemplateResult{}, found, err
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	if receipt.templateID == "" || receipt.templateRevisionID == "" || receipt.resultTemplateVersion < 1 {
		return organizingapp.TemplateResult{}, false, inconsistent(errors.New("organizing template receipt is incomplete"))
	}
	detail, err := gormScanTemplate(tx.Table("organizing.template AS t").Select(historicalTemplateDetailSelect).
		Joins("JOIN organizing.template_revision AS r ON r.template_id=t.id").
		Where("t.workspace_id = ? AND t.id = ? AND r.id = ? AND r.revision_no = ?", string(binding.WorkspaceID), receipt.templateID, receipt.templateRevisionID, receipt.resultTemplateVersion))
	if err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	return organizingapp.TemplateResult{Detail: detail, Replayed: true}, true, nil
}

func gormInsertTemplate(tx *gorm.DB, template domain.Template, revision domain.TemplateRevision) error {
	model := templateModel{ID: string(template.ID), WorkspaceID: optionalString(string(template.WorkspaceID)), Owner: string(template.Owner),
		Kind: string(template.Kind), CurrentRevisionID: string(template.CurrentRevisionID), Version: template.Version,
		CreatedAt: template.CreatedAt.UTC(), UpdatedAt: template.UpdatedAt.UTC()}
	if err := tx.Create(&model).Error; err != nil {
		return classifyGORM(err)
	}
	return gormInsertTemplateRevision(tx, template.Owner, revision)
}

func gormInsertTemplateRevision(tx *gorm.DB, owner domain.TemplateOwner, revision domain.TemplateRevision) error {
	encoded, err := encodeDeclaration(revision.Declaration)
	if err != nil {
		return invalid(err)
	}
	model := templateRevisionModel{ID: string(revision.ID), TemplateID: string(revision.TemplateID), WorkspaceID: optionalString(string(revision.WorkspaceID)),
		Owner: string(owner), RevisionNo: revision.RevisionNo, Kind: string(revision.Declaration.Kind), SchemaVersion: revision.Declaration.SchemaVersion,
		Declaration: organizingJSONB(encoded), DeclarationHash: revision.DeclarationHash, CreatedAt: revision.CreatedAt.UTC()}
	return classifyGORM(tx.Create(&model).Error)
}

func gormInsertTemplateReceipt(tx *gorm.DB, binding organizingapp.CommandBinding, template domain.Template, revision domain.TemplateRevision) error {
	model := commandReceiptModel{WorkspaceID: string(binding.WorkspaceID), IdempotencyKey: binding.IdempotencyKey, RequestHash: binding.RequestHash,
		CommandType: binding.CommandType, AggregateID: string(template.ID), ExpectedVersion: binding.ExpectedVersion, TemplateID: optionalString(string(template.ID)),
		ResultTemplateVersion: &template.Version, TemplateRevisionID: optionalString(string(revision.ID)), CreatedAt: revision.CreatedAt.UTC()}
	return classifyGORM(tx.Create(&model).Error)
}
