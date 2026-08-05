package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	authoringapp "github.com/CodeZen-Lizhi/zhixu/internal/authoring/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/authoring/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// SearchArticleRevisions returns the latest immutable Revision for each matching, visible Document.
func (repository *Repository) SearchArticleRevisions(
	ctx context.Context,
	query authoringapp.ArticleRevisionSearchQuery,
) ([]authoringapp.ArticleRevisionSearchHit, error) {
	if !validID(query.WorkspaceID) || query.Query == "" || query.Limit < 1 || query.Limit > authoringapp.MaxArticleRevisionSearchLimit {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeDraftInvalid, false, errors.New("article revision search query is invalid"))
	}
	rows, err := repository.db.Query(ctx, `SELECT
		d.id::text,d.workspace_id::text,d.canonical_path,d.title,d.lifecycle_status,
		COALESCE(d.current_published_revision_id::text,''),d.version,d.created_at,d.updated_at,
		r.id::text,r.revision_no,r.content_hash
		FROM core.document AS d
		JOIN LATERAL (
			SELECT id,revision_no,content_hash
			FROM core.article_revision
			WHERE workspace_id=d.workspace_id AND document_id=d.id
			ORDER BY revision_no DESC,id DESC LIMIT 1
		) AS r ON true
		WHERE d.workspace_id=$1 AND d.lifecycle_status<>'DELETED'
		  AND (position(lower($2) in lower(d.title))>0 OR position(lower($2) in lower(d.canonical_path))>0)
		ORDER BY CASE
			WHEN lower(d.title)=lower($2) OR lower(d.canonical_path)=lower($2) THEN 0
			WHEN position(lower($2) in lower(d.title))=1 OR position(lower($2) in lower(d.canonical_path))=1 THEN 1
			ELSE 2 END,
			d.updated_at DESC,d.id DESC
		LIMIT $3`, string(query.WorkspaceID), query.Query, query.Limit)
	if err != nil {
		return nil, classify(err, "AUTHORING_DOCUMENT_SEARCH_FAILED")
	}
	defer rows.Close()
	items := make([]authoringapp.ArticleRevisionSearchHit, 0, query.Limit)
	for rows.Next() {
		var item authoringapp.ArticleRevisionSearchHit
		if err := rows.Scan(
			&item.Document.ID, &item.Document.WorkspaceID, &item.Document.CanonicalPath, &item.Document.Title,
			&item.Document.Lifecycle, &item.Document.CurrentPublishedRevisionID, &item.Document.Version,
			&item.Document.CreatedAt, &item.Document.UpdatedAt, &item.RevisionID, &item.RevisionNo, &item.ContentHash,
		); err != nil {
			return nil, classify(err, "AUTHORING_DOCUMENT_SEARCH_SCAN_FAILED")
		}
		item.Document.CreatedAt = item.Document.CreatedAt.UTC()
		item.Document.UpdatedAt = item.Document.UpdatedAt.UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "AUTHORING_DOCUMENT_SEARCH_FAILED")
	}
	return items, nil
}

// GetDocumentDetail returns the latest frozen Revision and its publication binding.
func (repository *Repository) GetDocumentDetail(ctx context.Context, workspaceID, documentID foundation.ID) (authoringapp.DocumentDetail, error) {
	if !validID(workspaceID) || !validID(documentID) {
		return authoringapp.DocumentDetail{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("document detail identity is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return authoringapp.DocumentDetail{}, classify(err, "AUTHORING_DOCUMENT_QUERY_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	document, err := loadDocument(ctx, tx, workspaceID, documentID, false)
	if err != nil {
		return authoringapp.DocumentDetail{}, err
	}
	detail := authoringapp.DocumentDetail{Document: document}
	revision, err := scanRevision(tx.QueryRow(ctx, `SELECT `+revisionColumns+`
		FROM core.article_revision WHERE workspace_id=$1 AND document_id=$2
		ORDER BY revision_no DESC LIMIT 1`, string(workspaceID), string(documentID)))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return authoringapp.DocumentDetail{}, err
	}
	if err == nil {
		detail.CurrentRevision = &revision
		publication, found, loadErr := scanPublication(tx.QueryRow(ctx, `SELECT `+publicationColumns+`
			FROM authoring.document_publication_binding
			WHERE workspace_id=$1 AND document_id=$2 AND article_revision_id=$3
			ORDER BY created_at DESC,id DESC LIMIT 1`, string(workspaceID), string(documentID), string(revision.ID)))
		if loadErr != nil {
			return authoringapp.DocumentDetail{}, loadErr
		}
		if found {
			detail.Publication = &publication
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return authoringapp.DocumentDetail{}, classify(err, "AUTHORING_COMMIT_FAILED")
	}
	return detail, nil
}

// GetArticleRevisions 以一次参数化查询批量返回精确 Revision 及其 Document，并保持请求顺序。
func (repository *Repository) GetArticleRevisions(ctx context.Context, query authoringapp.ArticleRevisionBatchQuery) ([]authoringapp.ArticleRevisionSnapshot, error) {
	if err := validateArticleRevisionBatchQuery(query); err != nil {
		return nil, err
	}
	documentIDs := make([]string, len(query.Items))
	revisionIDs := make([]string, len(query.Items))
	for index, item := range query.Items {
		documentIDs[index] = string(item.DocumentID)
		revisionIDs[index] = string(item.RevisionID)
	}
	rows, err := repository.db.Query(ctx, `WITH requested(document_id,revision_id,ordinality) AS (
		SELECT document_id,revision_id,ordinality
		FROM unnest($2::uuid[],$3::uuid[]) WITH ORDINALITY AS input(document_id,revision_id,ordinality)
	)
	SELECT requested.ordinality,
		d.id::text,d.workspace_id::text,d.canonical_path,d.title,d.lifecycle_status,
		COALESCE(d.current_published_revision_id::text,''),d.version,d.created_at,d.updated_at,
		r.id::text,r.workspace_id::text,r.document_id::text,COALESCE(r.source_version_id::text,''),
		COALESCE(r.parent_revision_id::text,''),r.revision_no,r.content,r.content_hash,r.status,
		r.optimization_mode,COALESCE(r.git_commit,''),r.created_by_type,r.created_at
	FROM requested
	JOIN core.document AS d ON d.id=requested.document_id AND d.workspace_id=$1
	JOIN core.article_revision AS r ON r.id=requested.revision_id AND r.document_id=d.id AND r.workspace_id=d.workspace_id
	ORDER BY requested.ordinality`, string(query.WorkspaceID), documentIDs, revisionIDs)
	if err != nil {
		return nil, classify(err, "AUTHORING_REVISION_BATCH_QUERY_FAILED")
	}
	defer rows.Close()
	result := make([]authoringapp.ArticleRevisionSnapshot, len(query.Items))
	seen := make(map[int64]struct{}, len(query.Items))
	for rows.Next() {
		var ordinal int64
		var item authoringapp.ArticleRevisionSnapshot
		if err := rows.Scan(
			&ordinal,
			&item.Document.ID, &item.Document.WorkspaceID, &item.Document.CanonicalPath, &item.Document.Title,
			&item.Document.Lifecycle, &item.Document.CurrentPublishedRevisionID, &item.Document.Version,
			&item.Document.CreatedAt, &item.Document.UpdatedAt,
			&item.Revision.ID, &item.Revision.WorkspaceID, &item.Revision.DocumentID, &item.Revision.SourceVersionID,
			&item.Revision.ParentRevisionID, &item.Revision.RevisionNo, &item.Revision.Content,
			&item.Revision.ContentHash, &item.Revision.Status, &item.Revision.OptimizationMode,
			&item.Revision.GitCommit, &item.Revision.CreatedByType, &item.Revision.CreatedAt,
		); err != nil {
			return nil, classify(err, "AUTHORING_REVISION_BATCH_SCAN_FAILED")
		}
		if ordinal < 1 || ordinal > int64(len(result)) {
			return nil, inconsistent("article revision batch ordinal is invalid")
		}
		index := int(ordinal - 1)
		identity := query.Items[index]
		item.Document.CreatedAt = item.Document.CreatedAt.UTC()
		item.Document.UpdatedAt = item.Document.UpdatedAt.UTC()
		item.Revision.CreatedAt = item.Revision.CreatedAt.UTC()
		if _, duplicate := seen[ordinal]; duplicate || item.Document.Validate() != nil || item.Revision.Validate() != nil ||
			item.Document.WorkspaceID != query.WorkspaceID || item.Revision.WorkspaceID != query.WorkspaceID ||
			item.Document.ID != identity.DocumentID || item.Revision.DocumentID != identity.DocumentID || item.Revision.ID != identity.RevisionID {
			return nil, inconsistent("article revision batch binding is invalid")
		}
		seen[ordinal] = struct{}{}
		result[index] = item
	}
	if err := rows.Err(); err != nil {
		return nil, classify(err, "AUTHORING_REVISION_BATCH_QUERY_FAILED")
	}
	if len(seen) != len(result) {
		return nil, notFound(errors.New("one or more article revisions were not found"))
	}
	return result, nil
}

func validateArticleRevisionBatchQuery(query authoringapp.ArticleRevisionBatchQuery) error {
	if !validID(query.WorkspaceID) || len(query.Items) == 0 || len(query.Items) > authoringapp.MaxArticleRevisionBatchSize {
		return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid, false, errors.New("article revision batch query is invalid"))
	}
	seen := make(map[authoringapp.ArticleRevisionIdentity]struct{}, len(query.Items))
	for _, item := range query.Items {
		if !validID(item.DocumentID) || !validID(item.RevisionID) {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid, false, errors.New("article revision batch identity is invalid"))
		}
		if _, duplicate := seen[item]; duplicate {
			return foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeFreezeInvalid, false, errors.New("article revision batch contains duplicates"))
		}
		seen[item] = struct{}{}
	}
	return nil
}

// GetOverview returns stable, bounded Authoring workbench projections.
func (repository *Repository) GetOverview(ctx context.Context, workspaceID foundation.ID, limit int) (authoringapp.Overview, error) {
	if !validID(workspaceID) || limit < 1 || limit > 100 {
		return authoringapp.Overview{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodePublicationInvalid, false, errors.New("authoring overview query is invalid"))
	}
	tx, err := repository.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return authoringapp.Overview{}, classify(err, "AUTHORING_OVERVIEW_QUERY_FAILED")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	overview := authoringapp.Overview{
		WorkspaceID: workspaceID, RecentDrafts: make([]authoringapp.WorkingDraftSummary, 0),
		PendingPublications: make([]domain.PublicationBinding, 0), CompletedDocuments: make([]domain.Document, 0),
	}

	draftRows, err := tx.Query(ctx, `SELECT id::text,workspace_id::text,COALESCE(document_id::text,''),
		title,target_path,status,version,updated_at
		FROM authoring.working_draft
		WHERE workspace_id=$1 AND status='EDITING'
		ORDER BY updated_at DESC,id DESC LIMIT $2`, string(workspaceID), limit)
	if err != nil {
		return authoringapp.Overview{}, classify(err, "AUTHORING_OVERVIEW_DRAFT_QUERY_FAILED")
	}
	for draftRows.Next() {
		var draft authoringapp.WorkingDraftSummary
		scanErr := draftRows.Scan(&draft.ID, &draft.WorkspaceID, &draft.DocumentID, &draft.Title,
			&draft.TargetPath, &draft.Status, &draft.Version, &draft.UpdatedAt)
		if scanErr != nil {
			draftRows.Close()
			return authoringapp.Overview{}, classify(scanErr, "AUTHORING_OVERVIEW_DRAFT_QUERY_FAILED")
		}
		draft.UpdatedAt = draft.UpdatedAt.UTC()
		overview.RecentDrafts = append(overview.RecentDrafts, draft)
	}
	if err := draftRows.Err(); err != nil {
		draftRows.Close()
		return authoringapp.Overview{}, classify(err, "AUTHORING_OVERVIEW_DRAFT_QUERY_FAILED")
	}
	draftRows.Close()

	publicationRows, err := tx.Query(ctx, `SELECT `+publicationColumns+`
		FROM authoring.document_publication_binding
		WHERE workspace_id=$1 AND status IN ('PENDING','RECOVERY_REQUIRED')
		ORDER BY updated_at DESC,id DESC LIMIT $2`, string(workspaceID), limit)
	if err != nil {
		return authoringapp.Overview{}, classify(err, "AUTHORING_OVERVIEW_PUBLICATION_QUERY_FAILED")
	}
	for publicationRows.Next() {
		publication, found, scanErr := scanPublication(publicationRows)
		if scanErr != nil {
			publicationRows.Close()
			return authoringapp.Overview{}, classify(scanErr, "AUTHORING_OVERVIEW_PUBLICATION_QUERY_FAILED")
		}
		if !found {
			publicationRows.Close()
			return authoringapp.Overview{}, inconsistent("overview publication row is missing")
		}
		overview.PendingPublications = append(overview.PendingPublications, publication)
	}
	if err := publicationRows.Err(); err != nil {
		publicationRows.Close()
		return authoringapp.Overview{}, classify(err, "AUTHORING_OVERVIEW_PUBLICATION_QUERY_FAILED")
	}
	publicationRows.Close()

	documentRows, err := tx.Query(ctx, `SELECT id::text,workspace_id::text,canonical_path,title,lifecycle_status,
		COALESCE(current_published_revision_id::text,''),version,created_at,updated_at
		FROM core.document
		WHERE workspace_id=$1 AND lifecycle_status='PUBLISHED'
		ORDER BY updated_at DESC,id DESC LIMIT $2`, string(workspaceID), limit)
	if err != nil {
		return authoringapp.Overview{}, classify(err, "AUTHORING_OVERVIEW_DOCUMENT_QUERY_FAILED")
	}
	for documentRows.Next() {
		document, scanErr := scanDocument(documentRows)
		if scanErr != nil {
			documentRows.Close()
			return authoringapp.Overview{}, classify(scanErr, "AUTHORING_OVERVIEW_DOCUMENT_QUERY_FAILED")
		}
		overview.CompletedDocuments = append(overview.CompletedDocuments, document)
	}
	if err := documentRows.Err(); err != nil {
		documentRows.Close()
		return authoringapp.Overview{}, classify(err, "AUTHORING_OVERVIEW_DOCUMENT_QUERY_FAILED")
	}
	documentRows.Close()
	if err := tx.Commit(ctx); err != nil {
		return authoringapp.Overview{}, classify(err, "AUTHORING_COMMIT_FAILED")
	}
	return overview, nil
}
