// Package postgres persists Authoring Working Drafts and Revision freezes in PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// Repository owns Authoring database transactions.
type Repository struct {
	db *pgxpool.Pool
}

// NewRepository creates a PostgreSQL Authoring repository.
func NewRepository(db *pgxpool.Pool) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, authoringapp.ErrorCodeRepositoryUnavailable, true, errors.New("authoring database is required"))
	}
	return &Repository{db: db}, nil
}

// Create atomically persists an empty Working Draft and its exact command receipt.
func (repository *Repository) Create(ctx context.Context, record authoringapp.CreateRecord) (authoringapp.CreateResult, error) {
	if err := validateCreateRecord(record); err != nil {
		return authoringapp.CreateResult{}, err
	}
	tx, err := repository.beginCommand(ctx, record.Binding)
	if err != nil {
		return authoringapp.CreateResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	receipt, found, err := loadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey)
	if err != nil {
		return authoringapp.CreateResult{}, err
	}
	if found {
		if err := receipt.matches(record.Binding); err != nil {
			return authoringapp.CreateResult{}, err
		}
		result, err := receipt.createResult()
		if err != nil {
			return authoringapp.CreateResult{}, err
		}
		result.Replayed = true
		if err := tx.Commit(ctx); err != nil {
			return authoringapp.CreateResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
		}
		return result, nil
	}

	draft, err := scanDraft(tx.QueryRow(ctx, `
		INSERT INTO authoring.working_draft(
			id,workspace_id,title,target_path,body,status,version,created_at,updated_at
		) VALUES($1,$2,'','','','EDITING',1,$3,$3)
		RETURNING `+draftColumns,
		string(record.Draft.ID), string(record.Draft.WorkspaceID), record.Draft.CreatedAt.UTC()))
	if err != nil {
		return authoringapp.CreateResult{}, classify(err, "AUTHORING_DRAFT_CREATE_FAILED")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO authoring.working_draft_command(
			workspace_id,idempotency_key,request_hash,command_type,working_draft_id,
			expected_version,result_draft_version,response_draft_created_at,created_at
		) VALUES($1,$2,$3,'CREATE',$4,0,1,$5,$5)`,
		string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
		string(draft.ID), draft.CreatedAt.UTC()); err != nil {
		return authoringapp.CreateResult{}, classify(err, "AUTHORING_RECEIPT_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return authoringapp.CreateResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
	}
	return authoringapp.CreateResult{Draft: draft}, nil
}

// Get returns one Working Draft only through its Workspace composite identity.
func (repository *Repository) Get(ctx context.Context, workspaceID, draftID foundation.ID) (domain.WorkingDraft, error) {
	if !validID(workspaceID) || !validID(draftID) {
		return domain.WorkingDraft{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("working draft query identity is invalid"))
	}
	draft, err := scanDraft(repository.db.QueryRow(ctx, `SELECT `+draftColumns+`
		FROM authoring.working_draft WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(draftID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkingDraft{}, notFound(err)
	}
	if err != nil {
		return domain.WorkingDraft{}, classify(err, "AUTHORING_DRAFT_QUERY_FAILED")
	}
	return draft, nil
}

// Update performs one receipt-aware Working Draft CAS without appending a Revision.
func (repository *Repository) Update(ctx context.Context, record authoringapp.UpdateRecord) (authoringapp.UpdateResult, error) {
	if err := validateUpdateRecord(record); err != nil {
		return authoringapp.UpdateResult{}, err
	}
	tx, err := repository.beginCommand(ctx, record.Binding)
	if err != nil {
		return authoringapp.UpdateResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	receipt, found, err := loadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey)
	if err != nil {
		return authoringapp.UpdateResult{}, err
	}
	if found {
		if err := receipt.matches(record.Binding); err != nil {
			return authoringapp.UpdateResult{}, err
		}
		result, err := receipt.updateResult(record.Title, record.TargetPath, record.Body)
		if err != nil {
			return authoringapp.UpdateResult{}, err
		}
		result.Replayed = true
		if err := tx.Commit(ctx); err != nil {
			return authoringapp.UpdateResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
		}
		return result, nil
	}

	current, err := loadDraft(ctx, tx, record.Binding.WorkspaceID, record.Binding.DraftID, true)
	if err != nil {
		return authoringapp.UpdateResult{}, err
	}
	next, err := domain.ApplyUpdate(current, record.Binding.ExpectedVersion, record.Title, record.TargetPath, record.Body, record.UpdatedAt)
	if err != nil {
		return authoringapp.UpdateResult{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE authoring.working_draft
		SET title=$1,target_path=$2,body=$3,status=$4,version=$5,updated_at=$6
		WHERE workspace_id=$7 AND id=$8 AND version=$9`,
		next.Title, next.TargetPath, next.Body, string(next.Status), next.Version, next.UpdatedAt.UTC(),
		string(next.WorkspaceID), string(next.ID), record.Binding.ExpectedVersion)
	if err != nil {
		return authoringapp.UpdateResult{}, classify(err, "AUTHORING_DRAFT_UPDATE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return authoringapp.UpdateResult{}, versionConflict("working draft compare-and-swap did not update a row")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO authoring.working_draft_command(
			workspace_id,idempotency_key,request_hash,command_type,working_draft_id,
			expected_version,result_draft_version,document_id,response_draft_created_at,created_at
		) VALUES($1,$2,$3,'UPDATE',$4,$5,$6,NULLIF($7,'')::uuid,$8,$9)`,
		string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
		string(next.ID), record.Binding.ExpectedVersion, next.Version, string(next.DocumentID),
		next.CreatedAt.UTC(), next.UpdatedAt.UTC()); err != nil {
		return authoringapp.UpdateResult{}, classify(err, "AUTHORING_RECEIPT_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return authoringapp.UpdateResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
	}
	return authoringapp.UpdateResult{Draft: next}, nil
}

// List returns a bounded Workspace-scoped keyset page ordered by update time and ID.
func (repository *Repository) List(ctx context.Context, query authoringapp.ListQuery) (authoringapp.Page, error) {
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 ||
		(query.Status != "" && !query.Status.Valid()) ||
		(query.After != nil && (!validID(query.After.ID) || query.After.UpdatedAt.IsZero())) {
		return authoringapp.Page{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("working draft list query is invalid"))
	}
	arguments := []any{string(query.WorkspaceID)}
	conditions := []string{"workspace_id=$1"}
	if query.Status != "" {
		arguments = append(arguments, string(query.Status))
		conditions = append(conditions, fmt.Sprintf("status=$%d", len(arguments)))
	}
	if query.After != nil {
		arguments = append(arguments, query.After.UpdatedAt.UTC(), string(query.After.ID))
		conditions = append(conditions, fmt.Sprintf("(updated_at,id)<($%d,$%d)", len(arguments)-1, len(arguments)))
	}
	arguments = append(arguments, query.Limit+1)
	rows, err := repository.db.Query(ctx, `SELECT `+draftColumns+`
		FROM authoring.working_draft WHERE `+strings.Join(conditions, " AND ")+
		fmt.Sprintf(` ORDER BY updated_at DESC,id DESC LIMIT $%d`, len(arguments)), arguments...)
	if err != nil {
		return authoringapp.Page{}, classify(err, "AUTHORING_DRAFT_LIST_FAILED")
	}
	defer rows.Close()
	items := make([]domain.WorkingDraft, 0, query.Limit+1)
	for rows.Next() {
		draft, err := scanDraft(rows)
		if err != nil {
			return authoringapp.Page{}, classify(err, "AUTHORING_DRAFT_LIST_SCAN_FAILED")
		}
		items = append(items, draft)
	}
	if err := rows.Err(); err != nil {
		return authoringapp.Page{}, classify(err, "AUTHORING_DRAFT_LIST_FAILED")
	}
	page := authoringapp.Page{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &authoringapp.Cursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

// ListDocuments returns a stable Workspace-scoped keyset page of formal DRAFT Documents.
func (repository *Repository) ListDocuments(ctx context.Context, query authoringapp.DocumentListQuery) (authoringapp.DocumentPage, error) {
	if !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 ||
		(query.After != nil && (!validID(query.After.ID) || query.After.UpdatedAt.IsZero())) {
		return authoringapp.DocumentPage{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("document draft list query is invalid"))
	}
	arguments := []any{string(query.WorkspaceID)}
	conditions := []string{"workspace_id=$1", "lifecycle_status='DRAFT'"}
	if query.After != nil {
		arguments = append(arguments, query.After.UpdatedAt.UTC(), string(query.After.ID))
		conditions = append(conditions, fmt.Sprintf("(updated_at,id)<($%d,$%d)", len(arguments)-1, len(arguments)))
	}
	arguments = append(arguments, query.Limit+1)
	rows, err := repository.db.Query(ctx, `SELECT id::text,workspace_id::text,canonical_path,title,lifecycle_status,
		COALESCE(current_published_revision_id::text,''),version,created_at,updated_at
		FROM core.document WHERE `+strings.Join(conditions, " AND ")+fmt.Sprintf(` ORDER BY updated_at DESC,id DESC LIMIT $%d`, len(arguments)), arguments...)
	if err != nil {
		return authoringapp.DocumentPage{}, classify(err, "AUTHORING_DOCUMENT_LIST_FAILED")
	}
	defer rows.Close()
	items := make([]domain.Document, 0, query.Limit+1)
	for rows.Next() {
		document, scanErr := scanDocument(rows)
		if scanErr != nil {
			return authoringapp.DocumentPage{}, classify(scanErr, "AUTHORING_DOCUMENT_LIST_SCAN_FAILED")
		}
		items = append(items, document)
	}
	if err := rows.Err(); err != nil {
		return authoringapp.DocumentPage{}, classify(err, "AUTHORING_DOCUMENT_LIST_FAILED")
	}
	page := authoringapp.DocumentPage{Items: items}
	if len(items) > query.Limit {
		page.Items = items[:query.Limit]
		last := page.Items[len(page.Items)-1]
		page.Next = &authoringapp.DocumentCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

// Freeze atomically creates or updates a Document, appends an immutable Revision and advances the Draft CAS.
func (repository *Repository) Freeze(ctx context.Context, record authoringapp.FreezeRecord) (authoringapp.FreezeResult, error) {
	if err := validateFreezeRecord(record); err != nil {
		return authoringapp.FreezeResult{}, err
	}
	tx, err := repository.beginCommand(ctx, record.Binding)
	if err != nil {
		return authoringapp.FreezeResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	receipt, found, err := loadReceipt(ctx, tx, record.Binding.WorkspaceID, record.Binding.IdempotencyKey)
	if err != nil {
		return authoringapp.FreezeResult{}, err
	}
	if found {
		if err := receipt.matches(record.Binding); err != nil {
			return authoringapp.FreezeResult{}, err
		}
		result, err := receipt.freezeResult(ctx, tx)
		if err != nil {
			return authoringapp.FreezeResult{}, err
		}
		result.Replayed = true
		if err := tx.Commit(ctx); err != nil {
			return authoringapp.FreezeResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
		}
		return result, nil
	}

	current, err := loadDraft(ctx, tx, record.Binding.WorkspaceID, record.Binding.DraftID, true)
	if err != nil {
		return authoringapp.FreezeResult{}, err
	}
	documentID := record.CandidateDocumentID
	var existing *domain.Document
	var parent *domain.ArticleRevision
	if current.DocumentID != "" {
		documentID = current.DocumentID
		loadedDocument, loadErr := loadDocument(ctx, tx, current.WorkspaceID, current.DocumentID, true)
		if loadErr != nil {
			return authoringapp.FreezeResult{}, loadErr
		}
		loadedParent, loadErr := loadLatestRevision(ctx, tx, current.WorkspaceID, current.DocumentID)
		if loadErr != nil {
			return authoringapp.FreezeResult{}, loadErr
		}
		existing = &loadedDocument
		parent = &loadedParent
	}
	next, document, revision, err := domain.PrepareFreeze(
		current, record.Binding.ExpectedVersion, documentID, record.RevisionID, existing, parent, record.FrozenAt,
	)
	if err != nil {
		return authoringapp.FreezeResult{}, err
	}
	if existing == nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO core.document(
				id,workspace_id,canonical_path,title,lifecycle_status,current_published_revision_id,
				version,created_at,updated_at
			) VALUES($1,$2,$3,$4,$5,NULL,$6,$7,$8)`,
			string(document.ID), string(document.WorkspaceID), document.CanonicalPath, document.Title,
			string(document.Lifecycle), document.Version, document.CreatedAt.UTC(), document.UpdatedAt.UTC()); err != nil {
			return authoringapp.FreezeResult{}, classify(err, "AUTHORING_DOCUMENT_CREATE_FAILED")
		}
	} else {
		tag, err := tx.Exec(ctx, `
			UPDATE core.document
			SET canonical_path=$1,title=$2,lifecycle_status=$3,current_published_revision_id=NULLIF($4,'')::uuid,
				version=$5,updated_at=$6
			WHERE id=$7 AND workspace_id=$8 AND version=$9`,
			document.CanonicalPath, document.Title, string(document.Lifecycle), string(document.CurrentPublishedRevisionID),
			document.Version, document.UpdatedAt.UTC(), string(document.ID), string(document.WorkspaceID), existing.Version)
		if err != nil {
			return authoringapp.FreezeResult{}, classify(err, "AUTHORING_DOCUMENT_UPDATE_FAILED")
		}
		if tag.RowsAffected() != 1 {
			return authoringapp.FreezeResult{}, versionConflict("document compare-and-swap did not update a row")
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO core.article_revision(
			id,workspace_id,document_id,source_version_id,parent_revision_id,revision_no,
			content,content_hash,status,optimization_mode,git_commit,created_by_type,created_at
		) VALUES($1,$2,$3,NULL,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,NULL,$10,$11)`,
		string(revision.ID), string(revision.WorkspaceID), string(revision.DocumentID), string(revision.ParentRevisionID),
		revision.RevisionNo, revision.Content, revision.ContentHash, string(revision.Status), revision.OptimizationMode,
		revision.CreatedByType, revision.CreatedAt.UTC()); err != nil {
		return authoringapp.FreezeResult{}, classify(err, "AUTHORING_REVISION_CREATE_FAILED")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE authoring.working_draft
		SET document_id=$1,title=$2,target_path=$3,body=$4,status=$5,version=$6,updated_at=$7
		WHERE workspace_id=$8 AND id=$9 AND version=$10`,
		string(next.DocumentID), next.Title, next.TargetPath, next.Body, string(next.Status), next.Version, next.UpdatedAt.UTC(),
		string(next.WorkspaceID), string(next.ID), record.Binding.ExpectedVersion)
	if err != nil {
		return authoringapp.FreezeResult{}, classify(err, "AUTHORING_DRAFT_FREEZE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return authoringapp.FreezeResult{}, versionConflict("working draft changed before freeze completed")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO authoring.working_draft_command(
			workspace_id,idempotency_key,request_hash,command_type,working_draft_id,
			expected_version,result_draft_version,document_id,document_version,
			article_revision_id,revision_no,parent_revision_id,response_draft_created_at,
			response_document_created_at,response_document_lifecycle,response_published_revision_id,
			response_title,response_target_path,response_content_hash,created_at
		) VALUES($1,$2,$3,'FREEZE',$4,$5,$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,$12,$13,$14,
			NULLIF($15,'')::uuid,$16,$17,$18,$19)`,
		string(record.Binding.WorkspaceID), record.Binding.IdempotencyKey, record.Binding.RequestHash,
		string(next.ID), record.Binding.ExpectedVersion, next.Version, string(document.ID), document.Version,
		string(revision.ID), revision.RevisionNo, string(revision.ParentRevisionID), next.CreatedAt.UTC(),
		document.CreatedAt.UTC(), string(document.Lifecycle), string(document.CurrentPublishedRevisionID),
		document.Title, document.CanonicalPath, revision.ContentHash, revision.CreatedAt.UTC()); err != nil {
		return authoringapp.FreezeResult{}, classify(err, "AUTHORING_RECEIPT_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return authoringapp.FreezeResult{}, classify(err, "AUTHORING_COMMIT_FAILED")
	}
	return authoringapp.FreezeResult{Draft: next, Document: document, Revision: revision}, nil
}

func (repository *Repository) beginCommand(ctx context.Context, binding authoringapp.CommandBinding) (pgx.Tx, error) {
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, classify(err, "AUTHORING_TRANSACTION_FAILED")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(binding.WorkspaceID)+":"+binding.IdempotencyKey); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, classify(err, "AUTHORING_COMMAND_LOCK_FAILED")
	}
	return tx, nil
}

func loadDraft(ctx context.Context, tx pgx.Tx, workspaceID, draftID foundation.ID, forUpdate bool) (domain.WorkingDraft, error) {
	query := `SELECT ` + draftColumns + ` FROM authoring.working_draft WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	draft, err := scanDraft(tx.QueryRow(ctx, query, string(workspaceID), string(draftID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkingDraft{}, notFound(err)
	}
	if err != nil {
		return domain.WorkingDraft{}, classify(err, "AUTHORING_DRAFT_QUERY_FAILED")
	}
	return draft, nil
}

func loadDocument(ctx context.Context, tx pgx.Tx, workspaceID, documentID foundation.ID, forUpdate bool) (domain.Document, error) {
	query := `SELECT id::text,workspace_id::text,canonical_path,title,lifecycle_status,
		COALESCE(current_published_revision_id::text,''),version,created_at,updated_at
		FROM core.document WHERE workspace_id=$1 AND id=$2`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	return scanDocument(tx.QueryRow(ctx, query, string(workspaceID), string(documentID)))
}

func scanDocument(row pgx.Row) (domain.Document, error) {
	var document domain.Document
	err := row.Scan(
		&document.ID, &document.WorkspaceID, &document.CanonicalPath, &document.Title, &document.Lifecycle,
		&document.CurrentPublishedRevisionID, &document.Version, &document.CreatedAt, &document.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Document{}, notFound(err)
	}
	if err != nil {
		return domain.Document{}, classify(err, "AUTHORING_DOCUMENT_QUERY_FAILED")
	}
	document.CreatedAt = document.CreatedAt.UTC()
	document.UpdatedAt = document.UpdatedAt.UTC()
	if err := document.Validate(); err != nil {
		return domain.Document{}, inconsistent("stored document is invalid")
	}
	return document, nil
}

func loadLatestRevision(ctx context.Context, tx pgx.Tx, workspaceID, documentID foundation.ID) (domain.ArticleRevision, error) {
	revision, err := scanRevision(tx.QueryRow(ctx, `SELECT `+revisionColumns+`
		FROM core.article_revision WHERE workspace_id=$1 AND document_id=$2
		ORDER BY revision_no DESC LIMIT 1 FOR UPDATE`, string(workspaceID), string(documentID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ArticleRevision{}, inconsistent("bound document has no article revision")
	}
	return revision, err
}

const draftColumns = `id::text,workspace_id::text,COALESCE(document_id::text,''),title,target_path,body,status,version,created_at,updated_at`

func scanDraft(row pgx.Row) (domain.WorkingDraft, error) {
	var draft domain.WorkingDraft
	if err := row.Scan(&draft.ID, &draft.WorkspaceID, &draft.DocumentID, &draft.Title, &draft.TargetPath,
		&draft.Body, &draft.Status, &draft.Version, &draft.CreatedAt, &draft.UpdatedAt); err != nil {
		return domain.WorkingDraft{}, err
	}
	draft.CreatedAt = draft.CreatedAt.UTC()
	draft.UpdatedAt = draft.UpdatedAt.UTC()
	if err := draft.Validate(); err != nil {
		return domain.WorkingDraft{}, inconsistent("stored working draft is invalid")
	}
	return draft, nil
}

const revisionColumns = `id::text,workspace_id::text,document_id::text,COALESCE(source_version_id::text,''),
	COALESCE(parent_revision_id::text,''),revision_no,content,content_hash,status,optimization_mode,
	COALESCE(git_commit,''),created_by_type,created_at`

func scanRevision(row pgx.Row) (domain.ArticleRevision, error) {
	var revision domain.ArticleRevision
	if err := row.Scan(&revision.ID, &revision.WorkspaceID, &revision.DocumentID, &revision.SourceVersionID,
		&revision.ParentRevisionID, &revision.RevisionNo, &revision.Content, &revision.ContentHash,
		&revision.Status, &revision.OptimizationMode, &revision.GitCommit, &revision.CreatedByType,
		&revision.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ArticleRevision{}, err
		}
		return domain.ArticleRevision{}, classify(err, "AUTHORING_REVISION_QUERY_FAILED")
	}
	revision.CreatedAt = revision.CreatedAt.UTC()
	if err := revision.Validate(); err != nil {
		return domain.ArticleRevision{}, inconsistent("stored article revision is invalid")
	}
	return revision, nil
}

type commandReceipt struct {
	WorkspaceID                foundation.ID
	IdempotencyKey             string
	RequestHash                string
	CommandType                string
	DraftID                    foundation.ID
	ExpectedVersion            int64
	ResultDraftVersion         int64
	DocumentID                 foundation.ID
	DocumentVersion            int64
	RevisionID                 foundation.ID
	RevisionNo                 int
	ParentRevisionID           foundation.ID
	DraftCreatedAt             sql.NullTime
	DocumentCreatedAt          sql.NullTime
	DocumentLifecycle          string
	CurrentPublishedRevisionID foundation.ID
	Title                      string
	TargetPath                 string
	ContentHash                string
	CreatedAt                  time.Time
}

func loadReceipt(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string) (commandReceipt, bool, error) {
	var receipt commandReceipt
	err := tx.QueryRow(ctx, `
		SELECT workspace_id::text,idempotency_key,request_hash,command_type,
			COALESCE(working_draft_id::text,''),expected_version,COALESCE(result_draft_version,0),
			COALESCE(document_id::text,''),COALESCE(document_version,0),
			COALESCE(article_revision_id::text,''),COALESCE(revision_no,0),COALESCE(parent_revision_id::text,''),
			response_draft_created_at,response_document_created_at,COALESCE(response_document_lifecycle,''),
			COALESCE(response_published_revision_id::text,''),COALESCE(response_title,''),
			COALESCE(response_target_path,''),COALESCE(response_content_hash,''),created_at
		FROM authoring.working_draft_command
		WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), key).Scan(
		&receipt.WorkspaceID, &receipt.IdempotencyKey, &receipt.RequestHash, &receipt.CommandType,
		&receipt.DraftID, &receipt.ExpectedVersion, &receipt.ResultDraftVersion,
		&receipt.DocumentID, &receipt.DocumentVersion, &receipt.RevisionID, &receipt.RevisionNo,
		&receipt.ParentRevisionID, &receipt.DraftCreatedAt, &receipt.DocumentCreatedAt,
		&receipt.DocumentLifecycle, &receipt.CurrentPublishedRevisionID, &receipt.Title,
		&receipt.TargetPath, &receipt.ContentHash, &receipt.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return commandReceipt{}, false, nil
	}
	if err != nil {
		return commandReceipt{}, false, classify(err, "AUTHORING_RECEIPT_QUERY_FAILED")
	}
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	return receipt, true, nil
}

func (receipt commandReceipt) matches(binding authoringapp.CommandBinding) error {
	if receipt.WorkspaceID != binding.WorkspaceID || receipt.IdempotencyKey != binding.IdempotencyKey ||
		receipt.RequestHash != binding.RequestHash || receipt.CommandType != binding.CommandType ||
		receipt.ExpectedVersion != binding.ExpectedVersion ||
		(binding.CommandType != authoringapp.CommandCreate && receipt.DraftID != binding.DraftID) {
		return idempotencyConflict(errors.New("idempotency key is bound to another authoring request"))
	}
	return nil
}

func (receipt commandReceipt) createResult() (authoringapp.CreateResult, error) {
	if receipt.CommandType != authoringapp.CommandCreate || !receipt.DraftCreatedAt.Valid ||
		!validID(receipt.DraftID) || receipt.ResultDraftVersion != 1 {
		return authoringapp.CreateResult{}, inconsistent("create receipt is invalid")
	}
	draft, err := domain.NewBlankWorkingDraft(receipt.DraftID, receipt.WorkspaceID, receipt.DraftCreatedAt.Time.UTC())
	if err != nil {
		return authoringapp.CreateResult{}, inconsistent("create receipt draft is invalid")
	}
	return authoringapp.CreateResult{Draft: draft}, nil
}

func (receipt commandReceipt) updateResult(title, targetPath, body string) (authoringapp.UpdateResult, error) {
	if receipt.CommandType != authoringapp.CommandUpdate || !receipt.DraftCreatedAt.Valid ||
		!validID(receipt.DraftID) || receipt.ResultDraftVersion != receipt.ExpectedVersion+1 {
		return authoringapp.UpdateResult{}, inconsistent("update receipt is invalid")
	}
	draft := domain.WorkingDraft{
		ID: receipt.DraftID, WorkspaceID: receipt.WorkspaceID, DocumentID: receipt.DocumentID,
		Title: title, TargetPath: targetPath, Body: body, Status: domain.WorkingDraftEditing,
		Version: receipt.ResultDraftVersion, CreatedAt: receipt.DraftCreatedAt.Time.UTC(), UpdatedAt: receipt.CreatedAt,
	}
	if err := draft.Validate(); err != nil {
		return authoringapp.UpdateResult{}, inconsistent("update receipt draft is invalid")
	}
	return authoringapp.UpdateResult{Draft: draft}, nil
}

func (receipt commandReceipt) freezeResult(ctx context.Context, tx pgx.Tx) (authoringapp.FreezeResult, error) {
	if receipt.CommandType != authoringapp.CommandFreeze || !receipt.DraftCreatedAt.Valid ||
		!receipt.DocumentCreatedAt.Valid || !validID(receipt.DraftID) || !validID(receipt.DocumentID) ||
		!validID(receipt.RevisionID) || receipt.ResultDraftVersion != receipt.ExpectedVersion+1 ||
		receipt.DocumentVersion < 1 || receipt.RevisionNo < 1 {
		return authoringapp.FreezeResult{}, inconsistent("freeze receipt is invalid")
	}
	var content string
	var createdAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT content,created_at FROM core.article_revision
		WHERE id=$1 AND workspace_id=$2 AND document_id=$3 AND revision_no=$4 AND content_hash=$5`,
		string(receipt.RevisionID), string(receipt.WorkspaceID), string(receipt.DocumentID),
		receipt.RevisionNo, receipt.ContentHash).Scan(&content, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return authoringapp.FreezeResult{}, inconsistent("freeze receipt revision binding is missing")
	}
	if err != nil {
		return authoringapp.FreezeResult{}, classify(err, "AUTHORING_REVISION_QUERY_FAILED")
	}
	draft := domain.WorkingDraft{
		ID: receipt.DraftID, WorkspaceID: receipt.WorkspaceID, DocumentID: receipt.DocumentID,
		Title: receipt.Title, TargetPath: receipt.TargetPath, Body: content, Status: domain.WorkingDraftEditing,
		Version: receipt.ResultDraftVersion, CreatedAt: receipt.DraftCreatedAt.Time.UTC(), UpdatedAt: receipt.CreatedAt,
	}
	document := domain.Document{
		ID: receipt.DocumentID, WorkspaceID: receipt.WorkspaceID, CanonicalPath: receipt.TargetPath,
		Title: receipt.Title, Lifecycle: domain.DocumentLifecycle(receipt.DocumentLifecycle),
		CurrentPublishedRevisionID: receipt.CurrentPublishedRevisionID, Version: receipt.DocumentVersion,
		CreatedAt: receipt.DocumentCreatedAt.Time.UTC(), UpdatedAt: receipt.CreatedAt,
	}
	revision := domain.ArticleRevision{
		ID: receipt.RevisionID, WorkspaceID: receipt.WorkspaceID, DocumentID: receipt.DocumentID,
		ParentRevisionID: receipt.ParentRevisionID, RevisionNo: receipt.RevisionNo, Content: content,
		ContentHash: receipt.ContentHash, Status: domain.RevisionDraft, OptimizationMode: "NONE",
		CreatedByType: "USER", CreatedAt: createdAt.UTC(),
	}
	if err := draft.Validate(); err != nil {
		return authoringapp.FreezeResult{}, inconsistent("freeze receipt draft is invalid")
	}
	if err := document.Validate(); err != nil {
		return authoringapp.FreezeResult{}, inconsistent("freeze receipt document is invalid")
	}
	if err := revision.Validate(); err != nil {
		return authoringapp.FreezeResult{}, inconsistent("freeze receipt revision is invalid")
	}
	return authoringapp.FreezeResult{Draft: draft, Document: document, Revision: revision}, nil
}

func validateCreateRecord(record authoringapp.CreateRecord) error {
	if err := validateBinding(record.Binding, authoringapp.CommandCreate, false); err != nil {
		return err
	}
	if err := record.Draft.Validate(); err != nil || record.Draft.ID != record.Binding.DraftID ||
		record.Draft.WorkspaceID != record.Binding.WorkspaceID || record.Draft.Version != 1 ||
		record.Draft.DocumentID != "" || record.Draft.Title != "" || record.Draft.TargetPath != "" || record.Draft.Body != "" {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("create record is invalid"))
	}
	return nil
}

func validateUpdateRecord(record authoringapp.UpdateRecord) error {
	if err := validateBinding(record.Binding, authoringapp.CommandUpdate, true); err != nil {
		return err
	}
	identity := domain.WorkingDraft{ID: record.Binding.DraftID, WorkspaceID: record.Binding.WorkspaceID}
	hash, err := domain.ComputeUpdateRequestHash(identity, record.Binding.ExpectedVersion, record.Title, record.TargetPath, record.Body)
	if err != nil || hash != record.Binding.RequestHash || record.UpdatedAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("update record is invalid"))
	}
	return nil
}

func validateFreezeRecord(record authoringapp.FreezeRecord) error {
	if err := validateBinding(record.Binding, authoringapp.CommandFreeze, true); err != nil {
		return err
	}
	hash, err := domain.ComputeFreezeRequestHash(record.Binding.WorkspaceID, record.Binding.DraftID, record.Binding.ExpectedVersion)
	if err != nil || hash != record.Binding.RequestHash || !validID(record.CandidateDocumentID) ||
		!validID(record.RevisionID) || record.FrozenAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid, false, errors.New("freeze record is invalid"))
	}
	return nil
}

func validateBinding(binding authoringapp.CommandBinding, commandType string, expectedVersion bool) error {
	if !validID(binding.WorkspaceID) || !validID(binding.DraftID) || binding.CommandType != commandType ||
		binding.IdempotencyKey == "" || binding.IdempotencyKey != strings.TrimSpace(binding.IdempotencyKey) ||
		len(binding.IdempotencyKey) > authoringapp.MaxIdempotencyKeyBytes || strings.ContainsAny(binding.IdempotencyKey, "\r\n\x00") ||
		!validHash(binding.RequestHash) || (expectedVersion && binding.ExpectedVersion < 1) ||
		(!expectedVersion && binding.ExpectedVersion != 0) {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("authoring command binding is invalid"))
	}
	return nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}
