package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	"github.com/jackc/pgx/v5"
)

// EnsureBuiltIns 幂等安装服务器拥有的四个内置模板；已存在目录必须与代码 Catalog 一致。
func (repository *Repository) EnsureBuiltIns(ctx context.Context, createdAt time.Time) error {
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
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return classify(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "organizing:built-in-templates:v1"); err != nil {
		return classify(err)
	}
	for index := range templates {
		existing, loadErr := loadTemplateByID(ctx, tx, templates[index].ID, false)
		if loadErr == nil {
			if !sameBuiltIn(existing, templates[index], revisions[index]) {
				return inconsistent(errors.New("stored built-in template differs from the code catalog"))
			}
			continue
		}
		var classified *foundation.Error
		if !errors.As(loadErr, &classified) || classified.Kind != foundation.ErrorNotFound {
			return loadErr
		}
		if err := insertTemplate(ctx, tx, templates[index], revisions[index]); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return classify(err)
	}
	return nil
}

// FindTemplateCommand 查找并严格匹配 Template 命令回执。
func (repository *Repository) FindTemplateCommand(ctx context.Context, binding organizingapp.CommandBinding) (organizingapp.TemplateResult, bool, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	if err := validateBinding(binding); err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	tx, err := repository.beginCommand(ctx, binding)
	if err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	receipt, found, err := loadReceipt(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return organizingapp.TemplateResult{}, false, classify(err)
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	result, err := loadTemplateReceiptResult(ctx, tx, receipt)
	if err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	result.Replayed = true
	if err := tx.Commit(ctx); err != nil {
		return organizingapp.TemplateResult{}, false, classify(err)
	}
	return result, true, nil
}

// CreateTemplate 原子创建自定义 Template、Revision 1 与回执。
func (repository *Repository) CreateTemplate(ctx context.Context, record organizingapp.CreateTemplateRecord) (organizingapp.TemplateResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	if err := validateCreateTemplateRecord(record); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	tx, err := repository.beginCommand(ctx, record.Binding)
	if err != nil {
		return organizingapp.TemplateResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if result, found, err := replayTemplate(ctx, tx, record.Binding); err != nil || found {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return result, classify(err)
	}
	if err := insertTemplate(ctx, tx, record.Template, record.Revision); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	if err := insertTemplateReceipt(ctx, tx, record.Binding, record.Template, record.Revision); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return organizingapp.TemplateResult{}, classify(err)
	}
	return organizingapp.TemplateResult{Detail: organizingapp.TemplateDetail{Template: record.Template, Revision: record.Revision}}, nil
}

// ReviseTemplate 追加不可变 Revision，并对自定义 Template current pointer 执行 CAS。
func (repository *Repository) ReviseTemplate(ctx context.Context, record organizingapp.ReviseTemplateRecord) (organizingapp.TemplateResult, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	if err := validateReviseTemplateRecord(record); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	tx, err := repository.beginCommand(ctx, record.Binding)
	if err != nil {
		return organizingapp.TemplateResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if result, found, err := replayTemplate(ctx, tx, record.Binding); err != nil || found {
		if err == nil {
			err = tx.Commit(ctx)
		}
		return result, classify(err)
	}
	current, err := loadTemplateCurrentVisible(ctx, tx, record.Binding.WorkspaceID, record.Binding.AggregateID, true)
	if err != nil {
		return organizingapp.TemplateResult{}, err
	}
	expectedTemplate, expectedRevision, err := domain.ReviseCustomTemplate(current.Template,
		record.Binding.ExpectedVersion, record.Revision.ID, record.Revision.Declaration, record.Revision.CreatedAt)
	if err != nil {
		return organizingapp.TemplateResult{}, err
	}
	if !reflect.DeepEqual(expectedTemplate, record.Template) || !reflect.DeepEqual(expectedRevision, record.Revision) {
		return organizingapp.TemplateResult{}, invalid(errors.New("organizing template revision record does not match the domain transition"))
	}
	encoded, err := encodeDeclaration(record.Revision.Declaration)
	if err != nil {
		return organizingapp.TemplateResult{}, invalid(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.template_revision(
		id,template_id,workspace_id,owner,revision_no,kind,schema_version,declaration,declaration_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, string(record.Revision.ID), string(record.Revision.TemplateID),
		string(record.Revision.WorkspaceID), string(record.Template.Owner), record.Revision.RevisionNo,
		string(record.Revision.Declaration.Kind), record.Revision.Declaration.SchemaVersion, encoded,
		record.Revision.DeclarationHash, record.Revision.CreatedAt.UTC()); err != nil {
		return organizingapp.TemplateResult{}, classify(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE organizing.template SET current_revision_id=$1,version=$2,updated_at=$3
		WHERE id=$4 AND workspace_id=$5 AND version=$6 AND owner='CUSTOM'`, string(record.Template.CurrentRevisionID),
		record.Template.Version, record.Template.UpdatedAt.UTC(), string(record.Template.ID), string(record.Template.WorkspaceID),
		record.Binding.ExpectedVersion)
	if err != nil {
		return organizingapp.TemplateResult{}, classify(err)
	}
	if tag.RowsAffected() != 1 {
		return organizingapp.TemplateResult{}, versionConflict(errors.New("organizing template compare-and-swap lost"))
	}
	if err := insertTemplateReceipt(ctx, tx, record.Binding, record.Template, record.Revision); err != nil {
		return organizingapp.TemplateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return organizingapp.TemplateResult{}, classify(err)
	}
	return organizingapp.TemplateResult{Detail: organizingapp.TemplateDetail{Template: record.Template, Revision: record.Revision}}, nil
}

// GetTemplate 返回 Workspace 可见的当前自定义或全局内置模板。
func (repository *Repository) GetTemplate(ctx context.Context, workspaceID, templateID foundation.ID) (organizingapp.TemplateDetail, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	if !validID(workspaceID) || !validID(templateID) {
		return organizingapp.TemplateDetail{}, invalid(errors.New("organizing template lookup identity is invalid"))
	}
	return loadTemplateCurrentVisible(ctx, repository.db, workspaceID, templateID, false)
}

// GetTemplateRevision 返回 Workspace 可见且仍为 current 的 Revision。
func (repository *Repository) GetTemplateRevision(ctx context.Context, workspaceID, revisionID foundation.ID) (organizingapp.TemplateDetail, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	if !validID(workspaceID) || !validID(revisionID) {
		return organizingapp.TemplateDetail{}, invalid(errors.New("organizing template revision lookup identity is invalid"))
	}
	return loadTemplateRevisionVisible(ctx, repository.db, workspaceID, revisionID, true, false)
}

// GetFrozenTemplateRevision 返回 Workspace 可见的历史 Revision，供已冻结 Snapshot 执行。
func (repository *Repository) GetFrozenTemplateRevision(
	ctx context.Context,
	workspaceID, templateID, revisionID foundation.ID,
) (organizingapp.TemplateDetail, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	if !validID(workspaceID) || !validID(templateID) || !validID(revisionID) {
		return organizingapp.TemplateDetail{}, invalid(errors.New("organizing frozen template revision lookup identity is invalid"))
	}
	detail, err := loadTemplateRevisionVisible(ctx, repository.db, workspaceID, revisionID, false, false)
	if err != nil {
		return organizingapp.TemplateDetail{}, err
	}
	if detail.Template.ID != templateID || detail.Revision.TemplateID != templateID {
		return organizingapp.TemplateDetail{}, notFound(errors.New("organizing frozen template revision was not found"))
	}
	return detail, nil
}

// ListTemplates 返回全局内置与当前 Workspace 自定义模板的有界列表。
func (repository *Repository) ListTemplates(ctx context.Context, query organizingapp.TemplateListQuery) (organizingapp.TemplatePage, error) {
	if err := repository.validateReady(ctx); err != nil {
		return organizingapp.TemplatePage{}, err
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 || (query.Kind != "" && !query.Kind.Valid()) {
		return organizingapp.TemplatePage{}, invalid(errors.New("organizing template list query is invalid"))
	}
	arguments := []any{string(query.WorkspaceID)}
	condition := "(t.workspace_id=$1 OR t.workspace_id IS NULL)"
	if query.Kind != "" {
		arguments = append(arguments, string(query.Kind))
		condition += fmt.Sprintf(" AND t.kind=$%d", len(arguments))
	}
	arguments = append(arguments, query.Limit)
	rows, err := repository.db.Query(ctx, `SELECT `+templateDetailSelect+`
		FROM organizing.template AS t
		JOIN organizing.template_revision AS r ON r.id=t.current_revision_id AND r.template_id=t.id
		WHERE `+condition+fmt.Sprintf(`
		ORDER BY (t.owner='BUILT_IN') DESC,t.kind,t.updated_at DESC,t.id
		LIMIT $%d`, len(arguments)), arguments...)
	if err != nil {
		return organizingapp.TemplatePage{}, classify(err)
	}
	defer rows.Close()
	items := make([]organizingapp.TemplateDetail, 0, query.Limit)
	for rows.Next() {
		detail, err := scanTemplateDetail(rows)
		if err != nil {
			return organizingapp.TemplatePage{}, classify(err)
		}
		items = append(items, detail)
	}
	if err := rows.Err(); err != nil {
		return organizingapp.TemplatePage{}, classify(err)
	}
	return organizingapp.TemplatePage{Items: items}, nil
}

func replayTemplate(ctx context.Context, tx pgx.Tx, binding organizingapp.CommandBinding) (organizingapp.TemplateResult, bool, error) {
	receipt, found, err := loadReceipt(ctx, tx, binding.WorkspaceID, binding.IdempotencyKey)
	if err != nil || !found {
		return organizingapp.TemplateResult{}, found, err
	}
	if err := receipt.matches(binding); err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	result, err := loadTemplateReceiptResult(ctx, tx, receipt)
	if err != nil {
		return organizingapp.TemplateResult{}, false, err
	}
	result.Replayed = true
	return result, true, nil
}

func loadTemplateReceiptResult(ctx context.Context, db queryer, receipt receiptRow) (organizingapp.TemplateResult, error) {
	if receipt.templateID == "" || receipt.templateRevisionID == "" || receipt.resultTemplateVersion < 1 {
		return organizingapp.TemplateResult{}, inconsistent(errors.New("organizing template receipt is incomplete"))
	}
	detail, err := loadHistoricalTemplateRevision(ctx, db, foundation.ID(receipt.workspaceID),
		foundation.ID(receipt.templateID), foundation.ID(receipt.templateRevisionID), receipt.resultTemplateVersion)
	if err != nil {
		return organizingapp.TemplateResult{}, err
	}
	return organizingapp.TemplateResult{Detail: detail}, nil
}

func loadTemplateCurrentVisible(ctx context.Context, db queryer, workspaceID, templateID foundation.ID, lock bool) (organizingapp.TemplateDetail, error) {
	query := `SELECT ` + templateDetailSelect + `
		FROM organizing.template AS t
		JOIN organizing.template_revision AS r ON r.id=t.current_revision_id AND r.template_id=t.id
		WHERE t.id=$2 AND (t.workspace_id=$1 OR t.workspace_id IS NULL)`
	if lock {
		query += ` FOR UPDATE OF t`
	}
	detail, err := scanTemplateDetail(db.QueryRow(ctx, query, string(workspaceID), string(templateID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingapp.TemplateDetail{}, notFound(err)
	}
	if err != nil {
		return organizingapp.TemplateDetail{}, classify(err)
	}
	return detail, nil
}

func loadTemplateRevisionVisible(ctx context.Context, db queryer, workspaceID, revisionID foundation.ID,
	requireCurrent, lockCurrent bool,
) (organizingapp.TemplateDetail, error) {
	query := `SELECT ` + templateDetailSelect + `
		FROM organizing.template AS t
		JOIN organizing.template_revision AS r ON r.template_id=t.id
		WHERE r.id=$2 AND (t.workspace_id=$1 OR t.workspace_id IS NULL)`
	if requireCurrent {
		query += ` AND t.current_revision_id=r.id`
	}
	if lockCurrent {
		query += ` FOR SHARE OF t`
	}
	detail, err := scanTemplateDetail(db.QueryRow(ctx, query, string(workspaceID), string(revisionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingapp.TemplateDetail{}, notFound(err)
	}
	if err != nil {
		return organizingapp.TemplateDetail{}, classify(err)
	}
	return detail, nil
}

func loadHistoricalTemplateRevision(ctx context.Context, db queryer, workspaceID, templateID, revisionID foundation.ID,
	version int64) (organizingapp.TemplateDetail, error) {
	detail, err := scanTemplateDetail(db.QueryRow(ctx, `SELECT `+historicalTemplateDetailSelect+`
		FROM organizing.template AS t
		JOIN organizing.template_revision AS r ON r.template_id=t.id
		WHERE t.id=$2 AND r.id=$3 AND r.revision_no=$4 AND t.workspace_id=$1`,
		string(workspaceID), string(templateID), string(revisionID), version))
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingapp.TemplateDetail{}, notFound(err)
	}
	if err != nil {
		return organizingapp.TemplateDetail{}, classify(err)
	}
	return detail, nil
}

func loadTemplateByID(ctx context.Context, db queryer, templateID foundation.ID, lock bool) (organizingapp.TemplateDetail, error) {
	query := `SELECT ` + templateDetailSelect + `
		FROM organizing.template AS t
		JOIN organizing.template_revision AS r ON r.id=t.current_revision_id AND r.template_id=t.id
		WHERE t.id=$1`
	if lock {
		query += ` FOR UPDATE OF t`
	}
	detail, err := scanTemplateDetail(db.QueryRow(ctx, query, string(templateID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return organizingapp.TemplateDetail{}, notFound(err)
	}
	if err != nil {
		return organizingapp.TemplateDetail{}, classify(err)
	}
	return detail, nil
}

func insertTemplate(ctx context.Context, tx pgx.Tx, template domain.Template, revision domain.TemplateRevision) error {
	encoded, err := encodeDeclaration(revision.Declaration)
	if err != nil {
		return invalid(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.template(
		id,workspace_id,owner,kind,current_revision_id,version,created_at,updated_at
	) VALUES($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7,$8)`, string(template.ID), string(template.WorkspaceID),
		string(template.Owner), string(template.Kind), string(template.CurrentRevisionID), template.Version,
		template.CreatedAt.UTC(), template.UpdatedAt.UTC()); err != nil {
		return classify(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.template_revision(
		id,template_id,workspace_id,owner,revision_no,kind,schema_version,declaration,declaration_hash,created_at
	) VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10)`, string(revision.ID), string(revision.TemplateID),
		string(revision.WorkspaceID), string(template.Owner), revision.RevisionNo, string(revision.Declaration.Kind),
		revision.Declaration.SchemaVersion, encoded, revision.DeclarationHash, revision.CreatedAt.UTC()); err != nil {
		return classify(err)
	}
	return nil
}

func insertTemplateReceipt(ctx context.Context, tx pgx.Tx, binding organizingapp.CommandBinding, template domain.Template,
	revision domain.TemplateRevision) error {
	if _, err := tx.Exec(ctx, `INSERT INTO organizing.command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,aggregate_id,expected_version,
		draft_id,result_draft_version,snapshot_id,outbox_id,template_id,result_template_version,
		template_revision_id,created_at
	) VALUES($1,$2,$3,$4,$5,$6,NULL,NULL,NULL,NULL,$7,$8,$9,$10)`, string(binding.WorkspaceID),
		binding.IdempotencyKey, binding.RequestHash, binding.CommandType, string(template.ID), binding.ExpectedVersion,
		string(template.ID), template.Version, string(revision.ID), revision.CreatedAt.UTC()); err != nil {
		return classify(err)
	}
	return nil
}

func validateCreateTemplateRecord(record organizingapp.CreateTemplateRecord) error {
	if err := validateBinding(record.Binding); err != nil {
		return err
	}
	if record.Binding.CommandType != organizingapp.CommandCreateTemplate && record.Binding.CommandType != organizingapp.CommandCloneTemplate {
		return invalid(errors.New("organizing template create command type is invalid"))
	}
	if record.Binding.ExpectedVersion != 0 || record.Binding.AggregateID != record.Template.ID ||
		record.Template.Owner != domain.TemplateCustom || record.Template.WorkspaceID != record.Binding.WorkspaceID ||
		record.Template.Version != 1 || record.Revision.RevisionNo != 1 || record.Template.CurrentRevisionID != record.Revision.ID ||
		record.Template.ID != record.Revision.TemplateID || record.Revision.WorkspaceID != record.Binding.WorkspaceID ||
		record.Template.Kind != record.Revision.Declaration.Kind || record.Template.Validate() != nil ||
		record.Revision.Validate(domain.TemplateCustom) != nil {
		return invalid(errors.New("organizing template create record is invalid"))
	}
	return nil
}

func validateReviseTemplateRecord(record organizingapp.ReviseTemplateRecord) error {
	if err := validateBinding(record.Binding); err != nil {
		return err
	}
	if record.Binding.CommandType != organizingapp.CommandReviseTemplate || record.Binding.ExpectedVersion < 1 ||
		record.Binding.AggregateID != record.Template.ID || record.Template.Owner != domain.TemplateCustom ||
		record.Template.WorkspaceID != record.Binding.WorkspaceID || record.Template.Version != record.Binding.ExpectedVersion+1 ||
		record.Revision.RevisionNo != record.Template.Version || record.Template.CurrentRevisionID != record.Revision.ID ||
		record.Template.ID != record.Revision.TemplateID || record.Revision.WorkspaceID != record.Binding.WorkspaceID ||
		record.Template.Kind != record.Revision.Declaration.Kind || record.Template.Validate() != nil ||
		record.Revision.Validate(domain.TemplateCustom) != nil {
		return invalid(errors.New("organizing template revision record is invalid"))
	}
	return nil
}

func sameBuiltIn(detail organizingapp.TemplateDetail, template domain.Template, revision domain.TemplateRevision) bool {
	return detail.Template.ID == template.ID && detail.Template.Owner == domain.TemplateBuiltIn &&
		detail.Template.WorkspaceID == "" && detail.Template.Kind == template.Kind && detail.Template.Version == 1 &&
		detail.Template.CurrentRevisionID == revision.ID && detail.Revision.ID == revision.ID &&
		detail.Revision.TemplateID == template.ID && detail.Revision.WorkspaceID == "" &&
		detail.Revision.RevisionNo == 1 && detail.Revision.DeclarationHash == revision.DeclarationHash &&
		reflect.DeepEqual(detail.Revision.Declaration, revision.Declaration)
}
