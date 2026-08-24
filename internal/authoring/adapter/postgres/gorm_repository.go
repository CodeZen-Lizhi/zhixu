package postgres

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// GORMRepository is the staged Authoring implementation. Production remains
// on Repository until the TODO 9 PostgreSQL equivalence gate passes.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

// NewGORMRepository constructs an Authoring repository from the shared Pool.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, unavailable(errors.New("authoring PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, unavailable(err)
	}
	uow, err := pool.UnitOfWork()
	if err != nil {
		return nil, unavailable(err)
	}
	return &GORMRepository{database: database, unitOfWork: uow}, nil
}

func (repository *GORMRepository) ready() error {
	if repository == nil || !validGORMDatabase(repository.database) || nilUnitOfWork(repository.unitOfWork) {
		return unavailable(errors.New("authoring GORM repository is unavailable"))
	}
	return nil
}

func validGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilUnitOfWork(unitOfWork foundation.UnitOfWork) bool {
	if unitOfWork == nil {
		return true
	}
	value := reflect.ValueOf(unitOfWork)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (repository *GORMRepository) within(ctx context.Context, options foundation.TransactionOptions, work func(context.Context, *gorm.DB) error, fallbackCode string) error {
	if err := repository.ready(); err != nil {
		return err
	}
	if ctx == nil {
		return invalidGORM(errors.New("authoring context is nil"))
	}
	callbackSucceeded := false
	err := repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		tx, err := platformpostgres.GORMTransaction(scope)
		if err != nil {
			return err
		}
		if err := work(callbackCtx, tx.WithContext(callbackCtx)); err != nil {
			return err
		}
		callbackSucceeded = true
		return nil
	})
	if callbackSucceeded && err != nil {
		fallbackCode = "AUTHORING_COMMIT_FAILED"
	}
	return classifyGORM(ctx, err, fallbackCode)
}

func (repository *GORMRepository) Create(ctx context.Context, record authoringapp.CreateRecord) (result authoringapp.CreateResult, err error) {
	if err = validateCreateRecord(record); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return err
		}
		receipt, found, err := gormLoadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if err := receipt.matches(record.Binding); err != nil {
				return err
			}
			result, err = receipt.createResult()
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		draft, err := gormScanDraft(tx, `INSERT INTO authoring.working_draft(id,workspace_id,title,target_path,body,status,version,created_at,updated_at) VALUES(?,?,?,?,?,'EDITING',1,?,?) RETURNING `+draftColumns,
			string(record.Draft.ID), string(record.Draft.WorkspaceID), "", "", "", record.Draft.CreatedAt.UTC(), record.Draft.CreatedAt.UTC())
		if err != nil {
			return classifyGORM(ctx, err, "AUTHORING_DRAFT_CREATE_FAILED")
		}
		if e := tx.Exec(`INSERT INTO authoring.working_draft_command(workspace_id,idempotency_key,request_hash,command_type,working_draft_id,expected_version,result_draft_version,response_draft_created_at,created_at) VALUES(?,?,?,'CREATE',?,0,1,?,?)`, string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash, string(draft.ID), draft.CreatedAt.UTC(), draft.CreatedAt.UTC()).Error; e != nil {
			return classifyGORM(ctx, e, "AUTHORING_RECEIPT_CREATE_FAILED")
		}
		result = authoringapp.CreateResult{Draft: draft}
		return nil
	}, "AUTHORING_TRANSACTION_FAILED")
	if err != nil {
		return authoringapp.CreateResult{}, err
	}
	return result, err
}

func (repository *GORMRepository) Get(ctx context.Context, workspaceID, draftID foundation.ID) (domain.WorkingDraft, error) {
	if err := repository.ready(); err != nil {
		return domain.WorkingDraft{}, err
	}
	if !validID(workspaceID) || !validID(draftID) {
		return domain.WorkingDraft{}, invalidGORM(errors.New("working draft query identity is invalid"))
	}
	if err := validateGORMContext(ctx); err != nil {
		return domain.WorkingDraft{}, err
	}
	draft, err := gormScanDraft(repository.database.WithContext(ctx), `SELECT `+draftColumns+` FROM authoring.working_draft WHERE workspace_id=? AND id=?`, string(workspaceID), string(draftID))
	if gormNoRows(err) {
		return domain.WorkingDraft{}, notFound(err)
	}
	if err != nil {
		return domain.WorkingDraft{}, classifyGORM(ctx, err, "AUTHORING_DRAFT_QUERY_FAILED")
	}
	return draft, nil
}

func (repository *GORMRepository) Update(ctx context.Context, record authoringapp.UpdateRecord) (result authoringapp.UpdateResult, err error) {
	if err = validateUpdateRecord(record); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return err
		}
		receipt, found, err := gormLoadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if err := receipt.matches(record.Binding); err != nil {
				return err
			}
			result, err = receipt.updateResult(record.Title, record.TargetPath, record.Body)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		current, err := gormLoadDraft(ctx, tx, record.Binding.WorkspaceID, record.Binding.DraftID, true)
		if err != nil {
			return err
		}
		next, err := domain.ApplyUpdate(current, record.Binding.ExpectedVersion, record.Title, record.TargetPath, record.Body, record.UpdatedAt)
		if err != nil {
			return err
		}
		res := tx.Exec(`UPDATE authoring.working_draft SET title=?,target_path=?,body=?,status=?,version=?,updated_at=? WHERE workspace_id=? AND id=? AND version=?`, next.Title, next.TargetPath, next.Body, string(next.Status), next.Version, next.UpdatedAt.UTC(), string(next.WorkspaceID), string(next.ID), record.Binding.ExpectedVersion)
		if res.Error != nil {
			return classifyGORM(ctx, res.Error, "AUTHORING_DRAFT_UPDATE_FAILED")
		}
		if res.RowsAffected != 1 {
			return versionConflict("working draft compare-and-swap did not update a row")
		}
		if e := tx.Exec(`INSERT INTO authoring.working_draft_command(workspace_id,idempotency_key,request_hash,command_type,working_draft_id,expected_version,result_draft_version,document_id,response_draft_created_at,created_at) VALUES(?,?,?,'UPDATE',?,?,?,?,?,?)`, string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash, string(next.ID), record.Binding.ExpectedVersion, next.Version, nullableID(next.DocumentID), next.CreatedAt.UTC(), next.UpdatedAt.UTC()).Error; e != nil {
			return classifyGORM(ctx, e, "AUTHORING_RECEIPT_CREATE_FAILED")
		}
		result = authoringapp.UpdateResult{Draft: next}
		return nil
	}, "AUTHORING_TRANSACTION_FAILED")
	if err != nil {
		return authoringapp.UpdateResult{}, err
	}
	return result, err
}

func (repository *GORMRepository) List(ctx context.Context, query authoringapp.ListQuery) (page authoringapp.Page, err error) {
	if err = repository.ready(); err != nil {
		return page, err
	}
	if err = validateListQuery(query); err != nil {
		return page, err
	}
	if err = validateGORMContext(ctx); err != nil {
		return page, err
	}
	conditions := []string{"workspace_id=?"}
	args := []any{string(query.WorkspaceID)}
	if query.Status != "" {
		conditions = append(conditions, "status=?")
		args = append(args, string(query.Status))
	}
	if query.After != nil {
		conditions = append(conditions, "(updated_at,id)<(?,?)")
		args = append(args, query.After.UpdatedAt.UTC(), string(query.After.ID))
	}
	args = append(args, query.Limit+1)
	rows, e := gormRows(repository.database.WithContext(ctx), `SELECT `+draftColumns+` FROM authoring.working_draft WHERE `+strings.Join(conditions, " AND ")+` ORDER BY updated_at DESC,id DESC LIMIT ?`, args...)
	if e != nil {
		return page, classifyGORM(ctx, e, "AUTHORING_DRAFT_LIST_FAILED")
	}
	defer rows.Close()
	items := make([]domain.WorkingDraft, 0, query.Limit+1)
	for rows.Next() {
		item, e := scanDraft(rows)
		if e != nil {
			return page, classifyGORM(ctx, e, "AUTHORING_DRAFT_LIST_SCAN_FAILED")
		}
		items = append(items, item)
	}
	if e := rows.Err(); e != nil {
		return page, classifyGORM(ctx, e, "AUTHORING_DRAFT_LIST_FAILED")
	}
	page.Items = items
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &authoringapp.Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

func (repository *GORMRepository) ListDocuments(ctx context.Context, query authoringapp.DocumentListQuery) (page authoringapp.DocumentPage, err error) {
	if err = repository.ready(); err != nil {
		return page, err
	}
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 || (query.After != nil && (!validID(query.After.ID) || query.After.UpdatedAt.IsZero())) {
		return page, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("document draft list query is invalid"))
	}
	if err = validateGORMContext(ctx); err != nil {
		return page, err
	}
	conditions := []string{"workspace_id=?", "lifecycle_status='DRAFT'"}
	args := []any{string(query.WorkspaceID)}
	if query.After != nil {
		conditions = append(conditions, "(updated_at,id)<(?,?)")
		args = append(args, query.After.UpdatedAt.UTC(), string(query.After.ID))
	}
	args = append(args, query.Limit+1)
	rows, e := gormRows(repository.database.WithContext(ctx), `SELECT id::text,workspace_id::text,canonical_path,title,lifecycle_status,COALESCE(current_published_revision_id::text,''),version,created_at,updated_at FROM core.document WHERE `+strings.Join(conditions, " AND ")+` ORDER BY updated_at DESC,id DESC LIMIT ?`, args...)
	if e != nil {
		return page, classifyGORM(ctx, e, "AUTHORING_DOCUMENT_LIST_FAILED")
	}
	defer rows.Close()
	items := make([]domain.Document, 0, query.Limit+1)
	for rows.Next() {
		d, e := scanDocument(rows)
		if e != nil {
			return page, classifyGORM(ctx, e, "AUTHORING_DOCUMENT_LIST_SCAN_FAILED")
		}
		items = append(items, d)
	}
	if e := rows.Err(); e != nil {
		return page, classifyGORM(ctx, e, "AUTHORING_DOCUMENT_LIST_FAILED")
	}
	page.Items = items
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &authoringapp.DocumentCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

func (repository *GORMRepository) Freeze(ctx context.Context, record authoringapp.FreezeRecord) (result authoringapp.FreezeResult, err error) {
	if err = validateFreezeRecord(record); err != nil {
		return result, err
	}
	err = repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, tx *gorm.DB) error {
		if err := gormLockCommand(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey); err != nil {
			return err
		}
		receipt, found, err := gormLoadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if err := receipt.matches(record.Binding); err != nil {
				return err
			}
			result, err = receipt.gormFreezeResult(ctx, tx)
			if err == nil {
				result.Replayed = true
			}
			return err
		}
		current, err := gormLoadDraft(ctx, tx, record.Binding.WorkspaceID, record.Binding.DraftID, true)
		if err != nil {
			return err
		}
		documentID := record.CandidateDocumentID
		var existing *domain.Document
		var parent *domain.ArticleRevision
		if current.DocumentID != "" {
			documentID = current.DocumentID
			d, e := gormLoadDocument(ctx, tx, current.WorkspaceID, current.DocumentID, true)
			if e != nil {
				return e
			}
			p, e := gormLoadLatestRevision(ctx, tx, current.WorkspaceID, current.DocumentID, true)
			if e != nil {
				return e
			}
			existing = &d
			parent = &p
		}
		next, document, revision, err := domain.PrepareFreeze(current, record.Binding.ExpectedVersion, documentID, record.RevisionID, existing, parent, record.FrozenAt)
		if err != nil {
			return err
		}
		if existing == nil {
			e := tx.Exec(`INSERT INTO core.document(id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,version,created_at,updated_at) VALUES(?,?,?,?,?,NULL,?,?,?)`, string(document.ID), string(document.WorkspaceID), document.CanonicalPath, document.Title, string(document.Lifecycle), document.Version, document.CreatedAt.UTC(), document.UpdatedAt.UTC()).Error
			if e != nil {
				return classifyGORM(ctx, e, "AUTHORING_DOCUMENT_CREATE_FAILED")
			}
		} else {
			res := tx.Exec(`UPDATE core.document SET canonical_path=?,title=?,lifecycle_status=?,current_published_revision_id=NULLIF(?, '')::uuid,version=?,updated_at=? WHERE id=? AND workspace_id=? AND version=?`, document.CanonicalPath, document.Title, string(document.Lifecycle), string(document.CurrentPublishedRevisionID), document.Version, document.UpdatedAt.UTC(), string(document.ID), string(document.WorkspaceID), existing.Version)
			if res.Error != nil {
				return classifyGORM(ctx, res.Error, "AUTHORING_DOCUMENT_UPDATE_FAILED")
			}
			if res.RowsAffected != 1 {
				return versionConflict("document compare-and-swap did not update a row")
			}
		}
		if e := tx.Exec(`INSERT INTO core.article_revision(id,workspace_id,document_id,source_version_id,parent_revision_id,revision_no,content,content_hash,status,optimization_mode,git_commit,created_by_type,created_at) VALUES(?,?,?,NULL,NULLIF(?, '')::uuid,?,?,?,?,?,NULL,?,?)`, string(revision.ID), string(revision.WorkspaceID), string(revision.DocumentID), string(revision.ParentRevisionID), revision.RevisionNo, revision.Content, revision.ContentHash, string(revision.Status), revision.OptimizationMode, revision.CreatedByType, revision.CreatedAt.UTC()).Error; e != nil {
			return classifyGORM(ctx, e, "AUTHORING_REVISION_CREATE_FAILED")
		}
		res := tx.Exec(`UPDATE authoring.working_draft SET document_id=?,title=?,target_path=?,body=?,status=?,version=?,updated_at=? WHERE workspace_id=? AND id=? AND version=?`, string(next.DocumentID), next.Title, next.TargetPath, next.Body, string(next.Status), next.Version, next.UpdatedAt.UTC(), string(next.WorkspaceID), string(next.ID), record.Binding.ExpectedVersion)
		if res.Error != nil {
			return classifyGORM(ctx, res.Error, "AUTHORING_DRAFT_FREEZE_FAILED")
		}
		if res.RowsAffected != 1 {
			return versionConflict("working draft changed before freeze completed")
		}
		if e := tx.Exec(`INSERT INTO authoring.working_draft_command(workspace_id,idempotency_key,request_hash,command_type,working_draft_id,expected_version,result_draft_version,document_id,document_version,article_revision_id,revision_no,parent_revision_id,response_draft_created_at,response_document_created_at,response_document_lifecycle,response_published_revision_id,response_title,response_target_path,response_content_hash,created_at) VALUES(?,?,?,'FREEZE',?,?,?,?,?,?,?,NULLIF(?, '')::uuid,?,?,?,NULLIF(?, '')::uuid,?,?,?,?)`, string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash, string(next.ID), record.Binding.ExpectedVersion, next.Version, string(document.ID), document.Version, string(revision.ID), revision.RevisionNo, string(revision.ParentRevisionID), next.CreatedAt.UTC(), document.CreatedAt.UTC(), string(document.Lifecycle), string(document.CurrentPublishedRevisionID), document.Title, document.CanonicalPath, revision.ContentHash, revision.CreatedAt.UTC()).Error; e != nil {
			return classifyGORM(ctx, e, "AUTHORING_RECEIPT_CREATE_FAILED")
		}
		result = authoringapp.FreezeResult{Draft: next, Document: document, Revision: revision}
		return nil
	}, "AUTHORING_TRANSACTION_FAILED")
	if err != nil {
		return authoringapp.FreezeResult{}, err
	}
	return result, err
}

func (repository *GORMRepository) GetDocumentDetail(ctx context.Context, workspaceID, documentID foundation.ID) (detail authoringapp.DocumentDetail, err error) {
	if !validID(workspaceID) || !validID(documentID) {
		return detail, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("document detail identity is invalid"))
	}
	err = repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(ctx context.Context, tx *gorm.DB) error {
		d, e := gormLoadDocument(ctx, tx, workspaceID, documentID, false)
		if e != nil {
			return e
		}
		detail.Document = d
		r, e := gormScanRevision(tx, `SELECT `+revisionColumns+` FROM core.article_revision WHERE workspace_id=? AND document_id=? ORDER BY revision_no DESC LIMIT 1`, string(workspaceID), string(documentID))
		if gormNoRows(e) {
			return nil
		}
		if e != nil {
			return e
		}
		detail.CurrentRevision = &r
		p, found, e := gormScanPublication(ctx, tx, `SELECT `+publicationColumns+` FROM authoring.document_publication_binding WHERE workspace_id=? AND document_id=? AND article_revision_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, string(workspaceID), string(documentID), string(r.ID))
		if e != nil {
			return e
		}
		if found {
			detail.Publication = &p
		}
		return nil
	}, "AUTHORING_DOCUMENT_QUERY_FAILED")
	if err != nil {
		return authoringapp.DocumentDetail{}, err
	}
	return detail, err
}

func (repository *GORMRepository) GetArticleRevisions(ctx context.Context, query authoringapp.ArticleRevisionBatchQuery) ([]authoringapp.ArticleRevisionSnapshot, error) {
	if err := repository.ready(); err != nil {
		return nil, err
	}
	if err := validateArticleRevisionBatchQuery(query); err != nil {
		return nil, err
	}
	if err := validateGORMContext(ctx); err != nil {
		return nil, err
	}
	ids := make([]string, len(query.Items))
	rids := make([]string, len(query.Items))
	for i, item := range query.Items {
		ids[i] = string(item.DocumentID)
		rids[i] = string(item.RevisionID)
	}
	rows, e := gormRows(repository.database.WithContext(ctx), `WITH requested(document_id,revision_id,ordinality) AS (SELECT * FROM unnest(?::uuid[],?::uuid[]) WITH ORDINALITY) SELECT requested.ordinality,d.id::text,d.workspace_id::text,d.canonical_path,d.title,d.lifecycle_status,COALESCE(d.current_published_revision_id::text,''),d.version,d.created_at,d.updated_at,r.id::text,r.workspace_id::text,r.document_id::text,COALESCE(r.source_version_id::text,''),COALESCE(r.parent_revision_id::text,''),r.revision_no,r.content,r.content_hash,r.status,r.optimization_mode,COALESCE(r.git_commit,''),r.created_by_type,r.created_at FROM requested JOIN core.document d ON d.id=requested.document_id AND d.workspace_id=? JOIN core.article_revision r ON r.id=requested.revision_id AND r.document_id=d.id AND r.workspace_id=d.workspace_id ORDER BY requested.ordinality`, pq.Array(ids), pq.Array(rids), string(query.WorkspaceID))
	if e != nil {
		return nil, classifyGORM(ctx, e, "AUTHORING_REVISION_BATCH_QUERY_FAILED")
	}
	defer rows.Close()
	result := make([]authoringapp.ArticleRevisionSnapshot, len(query.Items))
	seen := map[int64]bool{}
	for rows.Next() {
		var ordinal int64
		var item authoringapp.ArticleRevisionSnapshot
		if e := rows.Scan(&ordinal, &item.Document.ID, &item.Document.WorkspaceID, &item.Document.CanonicalPath, &item.Document.Title, &item.Document.Lifecycle, &item.Document.CurrentPublishedRevisionID, &item.Document.Version, &item.Document.CreatedAt, &item.Document.UpdatedAt, &item.Revision.ID, &item.Revision.WorkspaceID, &item.Revision.DocumentID, &item.Revision.SourceVersionID, &item.Revision.ParentRevisionID, &item.Revision.RevisionNo, &item.Revision.Content, &item.Revision.ContentHash, &item.Revision.Status, &item.Revision.OptimizationMode, &item.Revision.GitCommit, &item.Revision.CreatedByType, &item.Revision.CreatedAt); e != nil {
			return nil, classifyGORM(ctx, e, "AUTHORING_REVISION_BATCH_SCAN_FAILED")
		}
		if ordinal < 1 || ordinal > int64(len(result)) || seen[ordinal] {
			return nil, inconsistent("article revision batch ordinal is invalid")
		}
		idx := int(ordinal - 1)
		identity := query.Items[idx]
		item.Document.CreatedAt = item.Document.CreatedAt.UTC()
		item.Document.UpdatedAt = item.Document.UpdatedAt.UTC()
		item.Revision.CreatedAt = item.Revision.CreatedAt.UTC()
		if item.Document.Validate() != nil || item.Revision.Validate() != nil ||
			item.Document.WorkspaceID != query.WorkspaceID || item.Revision.WorkspaceID != query.WorkspaceID ||
			item.Document.ID != identity.DocumentID || item.Revision.DocumentID != identity.DocumentID || item.Revision.ID != identity.RevisionID {
			return nil, inconsistent("article revision batch binding is invalid")
		}
		result[idx] = item
		seen[ordinal] = true
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORM(ctx, err, "AUTHORING_REVISION_BATCH_QUERY_FAILED")
	}
	if len(seen) != len(result) {
		return nil, notFound(errors.New("one or more article revisions were not found"))
	}
	return result, nil
}

// SearchArticleRevisions returns the latest immutable Revision for each
// matching visible Document through one bounded query.
func (repository *GORMRepository) SearchArticleRevisions(ctx context.Context, query authoringapp.ArticleRevisionSearchQuery) ([]authoringapp.ArticleRevisionSearchHit, error) {
	if err := repository.ready(); err != nil {
		return nil, err
	}
	if !validID(query.WorkspaceID) || query.Query == "" || query.Limit < 1 || query.Limit > authoringapp.MaxArticleRevisionSearchLimit {
		return nil, invalidGORM(errors.New("article revision search query is invalid"))
	}
	if err := validateGORMContext(ctx); err != nil {
		return nil, err
	}
	rows, err := gormRows(repository.database.WithContext(ctx), `SELECT d.id::text,d.workspace_id::text,d.canonical_path,d.title,d.lifecycle_status,COALESCE(d.current_published_revision_id::text,''),d.version,d.created_at,d.updated_at,r.id::text,r.revision_no,r.content_hash FROM core.document AS d JOIN LATERAL (SELECT id,revision_no,content_hash FROM core.article_revision WHERE workspace_id=d.workspace_id AND document_id=d.id ORDER BY revision_no DESC,id DESC LIMIT 1) AS r ON true WHERE d.workspace_id=? AND d.lifecycle_status<>'DELETED' AND (position(lower(?) in lower(d.title))>0 OR position(lower(?) in lower(d.canonical_path))>0) ORDER BY CASE WHEN lower(d.title)=lower(?) OR lower(d.canonical_path)=lower(?) THEN 0 WHEN position(lower(?) in lower(d.title))=1 OR position(lower(?) in lower(d.canonical_path))=1 THEN 1 ELSE 2 END,d.updated_at DESC,d.id DESC LIMIT ?`, string(query.WorkspaceID), query.Query, query.Query, query.Query, query.Query, query.Query, query.Query, query.Limit)
	if err != nil {
		return nil, classifyGORM(ctx, err, "AUTHORING_DOCUMENT_SEARCH_FAILED")
	}
	defer rows.Close()
	items := make([]authoringapp.ArticleRevisionSearchHit, 0, query.Limit)
	for rows.Next() {
		var item authoringapp.ArticleRevisionSearchHit
		if err := rows.Scan(&item.Document.ID, &item.Document.WorkspaceID, &item.Document.CanonicalPath, &item.Document.Title, &item.Document.Lifecycle, &item.Document.CurrentPublishedRevisionID, &item.Document.Version, &item.Document.CreatedAt, &item.Document.UpdatedAt, &item.RevisionID, &item.RevisionNo, &item.ContentHash); err != nil {
			return nil, classifyGORM(ctx, err, "AUTHORING_DOCUMENT_SEARCH_SCAN_FAILED")
		}
		item.Document.CreatedAt = item.Document.CreatedAt.UTC()
		item.Document.UpdatedAt = item.Document.UpdatedAt.UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORM(ctx, err, "AUTHORING_DOCUMENT_SEARCH_FAILED")
	}
	return items, nil
}

func (repository *GORMRepository) GetOverview(ctx context.Context, workspaceID foundation.ID, limit int) (overview authoringapp.Overview, err error) {
	if !validID(workspaceID) || limit < 1 || limit > 100 {
		return overview, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("authoring overview query is invalid"))
	}
	overview = authoringapp.Overview{WorkspaceID: workspaceID, RecentDrafts: []authoringapp.WorkingDraftSummary{}, PendingPublications: []domain.PublicationBinding{}, CompletedDocuments: []domain.Document{}}
	err = repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(ctx context.Context, tx *gorm.DB) error {
		rows, e := gormRows(tx, `SELECT id::text,workspace_id::text,COALESCE(document_id::text,''),title,target_path,status,version,updated_at FROM authoring.working_draft WHERE workspace_id=? AND status='EDITING' ORDER BY updated_at DESC,id DESC LIMIT ?`, string(workspaceID), limit)
		if e != nil {
			return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_DRAFT_QUERY_FAILED")
		}
		for rows.Next() {
			var d authoringapp.WorkingDraftSummary
			if e := rows.Scan(&d.ID, &d.WorkspaceID, &d.DocumentID, &d.Title, &d.TargetPath, &d.Status, &d.Version, &d.UpdatedAt); e != nil {
				rows.Close()
				return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_DRAFT_QUERY_FAILED")
			}
			d.UpdatedAt = d.UpdatedAt.UTC()
			overview.RecentDrafts = append(overview.RecentDrafts, d)
		}
		if e := rows.Err(); e != nil {
			rows.Close()
			return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_DRAFT_QUERY_FAILED")
		}
		rows.Close()
		rows, e = gormRows(tx, `SELECT `+publicationColumns+` FROM authoring.document_publication_binding WHERE workspace_id=? AND status IN ('PENDING','RECOVERY_REQUIRED') ORDER BY updated_at DESC,id DESC LIMIT ?`, string(workspaceID), limit)
		if e != nil {
			return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_PUBLICATION_QUERY_FAILED")
		}
		for rows.Next() {
			p, found, e := gormScanPublicationRow(ctx, rows)
			if e != nil {
				rows.Close()
				return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_PUBLICATION_QUERY_FAILED")
			}
			if found {
				overview.PendingPublications = append(overview.PendingPublications, p)
			}
		}
		if e := rows.Err(); e != nil {
			rows.Close()
			return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_PUBLICATION_QUERY_FAILED")
		}
		rows.Close()
		rows, e = gormRows(tx, `SELECT id::text,workspace_id::text,canonical_path,title,lifecycle_status,COALESCE(current_published_revision_id::text,''),version,created_at,updated_at FROM core.document WHERE workspace_id=? AND lifecycle_status='PUBLISHED' ORDER BY updated_at DESC,id DESC LIMIT ?`, string(workspaceID), limit)
		if e != nil {
			return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_DOCUMENT_QUERY_FAILED")
		}
		for rows.Next() {
			d, e := scanDocument(rows)
			if e != nil {
				rows.Close()
				return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_DOCUMENT_QUERY_FAILED")
			}
			overview.CompletedDocuments = append(overview.CompletedDocuments, d)
		}
		if e := rows.Err(); e != nil {
			rows.Close()
			return classifyGORM(ctx, e, "AUTHORING_OVERVIEW_DOCUMENT_QUERY_FAILED")
		}
		rows.Close()
		return nil
	}, "AUTHORING_OVERVIEW_QUERY_FAILED")
	if err != nil {
		return authoringapp.Overview{}, err
	}
	return overview, err
}

func (repository *GORMRepository) ReconcilePublications(ctx context.Context, query authoringapp.ReconcileQuery) (int, error) {
	return gormReconcilePublications(repository, ctx, query)
}

func (repository *GORMRepository) ValidateRestoreWriteback(ctx context.Context, check authoringapp.RestoreWritebackCheck) error {
	return gormValidateRestoreWriteback(repository, ctx, check)
}
func (repository *GORMRepository) FinalizeRestorePublication(ctx context.Context, record authoringapp.RestorePublicationRecord) (bool, error) {
	return gormFinalizeRestorePublication(repository, ctx, record)
}

func gormRows(database *gorm.DB, query string, args ...any) (*sql.Rows, error) {
	if database == nil {
		return nil, errors.New("authoring GORM database is nil")
	}
	stmt := database.Raw(query, args...)
	if stmt.Error != nil {
		return nil, stmt.Error
	}
	rows, e := stmt.Rows()
	if e != nil {
		return nil, e
	}
	if rows == nil {
		return nil, errors.New("authoring GORM returned nil rows")
	}
	return rows, nil
}
func gormRow(database *gorm.DB, query string, args ...any) (*sql.Row, error) {
	if database == nil {
		return nil, errors.New("authoring GORM database is nil")
	}
	stmt := database.Raw(query, args...)
	if stmt.Error != nil {
		return nil, stmt.Error
	}
	return stmt.Row(), nil
}
func gormLockCommand(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key string) error {
	return classifyGORM(ctx, tx.WithContext(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(workspaceID)+":"+key).Error, "AUTHORING_COMMAND_LOCK_FAILED")
}
func nullableID(id foundation.ID) any {
	if id == "" {
		return nil
	}
	return string(id)
}
func gormScanDraft(database *gorm.DB, query string, args ...any) (domain.WorkingDraft, error) {
	row, e := gormRow(database, query, args...)
	if e != nil {
		return domain.WorkingDraft{}, e
	}
	return scanDraft(row)
}
func gormScanDocument(database *gorm.DB, query string, args ...any) (domain.Document, error) {
	row, e := gormRow(database, query, args...)
	if e != nil {
		return domain.Document{}, e
	}
	return scanDocument(row)
}
func gormScanRevision(database *gorm.DB, query string, args ...any) (domain.ArticleRevision, error) {
	row, e := gormRow(database, query, args...)
	if e != nil {
		return domain.ArticleRevision{}, e
	}
	return scanRevision(row)
}
func gormLoadDraft(ctx context.Context, tx *gorm.DB, w, d foundation.ID, lock bool) (domain.WorkingDraft, error) {
	q := `SELECT ` + draftColumns + ` FROM authoring.working_draft WHERE workspace_id=? AND id=?`
	if lock {
		q += ` FOR UPDATE`
	}
	x, e := gormScanDraft(tx.WithContext(ctx), q, string(w), string(d))
	if gormNoRows(e) {
		return domain.WorkingDraft{}, notFound(e)
	}
	return x, classifyGORM(ctx, e, "AUTHORING_DRAFT_QUERY_FAILED")
}
func gormLoadDocument(ctx context.Context, tx *gorm.DB, w, d foundation.ID, lock bool) (domain.Document, error) {
	q := `SELECT id::text,workspace_id::text,canonical_path,title,lifecycle_status,COALESCE(current_published_revision_id::text,''),version,created_at,updated_at FROM core.document WHERE workspace_id=? AND id=?`
	if lock {
		q += ` FOR UPDATE`
	}
	x, e := gormScanDocument(tx.WithContext(ctx), q, string(w), string(d))
	if gormNoRows(e) {
		return domain.Document{}, notFound(e)
	}
	return x, classifyGORM(ctx, e, "AUTHORING_DOCUMENT_QUERY_FAILED")
}
func gormLoadRevision(ctx context.Context, tx *gorm.DB, w, d, r foundation.ID, lock bool) (domain.ArticleRevision, error) {
	q := `SELECT ` + revisionColumns + ` FROM core.article_revision WHERE workspace_id=? AND document_id=? AND id=?`
	if lock {
		q += ` FOR UPDATE`
	}
	x, e := gormScanRevision(tx.WithContext(ctx), q, string(w), string(d), string(r))
	if gormNoRows(e) {
		return domain.ArticleRevision{}, notFound(e)
	}
	return x, classifyGORM(ctx, e, "AUTHORING_REVISION_QUERY_FAILED")
}
func gormLoadLatestRevision(ctx context.Context, tx *gorm.DB, w, d foundation.ID, lock bool) (domain.ArticleRevision, error) {
	q := `SELECT ` + revisionColumns + ` FROM core.article_revision WHERE workspace_id=? AND document_id=? ORDER BY revision_no DESC LIMIT 1`
	if lock {
		q += ` FOR UPDATE`
	}
	x, e := gormScanRevision(tx.WithContext(ctx), q, string(w), string(d))
	if gormNoRows(e) {
		return domain.ArticleRevision{}, inconsistent("bound document has no article revision")
	}
	return x, classifyGORM(ctx, e, "AUTHORING_REVISION_QUERY_FAILED")
}
func validateListQuery(q authoringapp.ListQuery) error {
	if !validID(q.WorkspaceID) || q.Limit < 1 || q.Limit > 100 || (q.Status != "" && !q.Status.Valid()) || (q.After != nil && (!validID(q.After.ID) || q.After.UpdatedAt.IsZero())) {
		return invalidGORM(errors.New("working draft list query is invalid"))
	}
	return nil
}
func invalidGORM(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, err)
}
func validateGORMContext(ctx context.Context) error {
	if ctx == nil {
		return invalidGORM(errors.New("authoring context is nil"))
	}
	return nil
}
func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, authoringapp.ErrorCodeRepositoryUnavailable, true, err)
}

type publicationFinalizerRepository interface {
	ReconcilePublications(context.Context, authoringapp.ReconcileQuery) (int, error)
	ValidateRestoreWriteback(context.Context, authoringapp.RestoreWritebackCheck) error
	FinalizeRestorePublication(context.Context, authoringapp.RestorePublicationRecord) (bool, error)
}

var _ authoringapp.Repository = (*GORMRepository)(nil)
var _ authoringapp.ArticleRevisionSearchRepository = (*GORMRepository)(nil)
var _ publicationFinalizerRepository = (*GORMRepository)(nil)
