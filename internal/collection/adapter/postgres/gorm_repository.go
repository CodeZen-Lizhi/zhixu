package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/collection/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// GORMRepository is the staged Collection implementation backed by the
// platform's shared GORM root and UnitOfWork. Production composition remains
// on Repository until the real PostgreSQL equivalence gate passes.
type GORMRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	cursor     *collectionapp.CursorCodec
}

// NewGORMRepository constructs Collection persistence from one complete
// platform Pool. The pool owns both the GORM root and transaction boundary.
func NewGORMRepository(pool *platformpostgres.Pool) (*GORMRepository, error) {
	if pool == nil {
		return nil, unavailable(errors.New("collection PostgreSQL pool is nil"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, unavailable(err)
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, unavailable(err)
	}
	cursor, err := collectionapp.NewRandomCursorCodec()
	if err != nil {
		return nil, unavailable(err)
	}
	if !validCollectionGORMDatabase(database) || !validCollectionGORMUnitOfWork(unitOfWork) {
		return nil, unavailable(errors.New("collection GORM transaction boundary is unavailable"))
	}
	return &GORMRepository{database: database, unitOfWork: unitOfWork, cursor: cursor}, nil
}

// CreateCollection writes the aggregate and immutable command receipt in one
// UnitOfWork-owned transaction.
func (repository *GORMRepository) CreateCollection(ctx context.Context, record collectionapp.CreateRecord) (collectionapp.CommandResult, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.CommandResult{}, err
	}
	if err := validateCreateRecord(record); err != nil {
		return collectionapp.CommandResult{}, err
	}
	var result collectionapp.CommandResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gormDB) error {
		if err := lockWorkspace(callbackCtx, database, record.Collection.WorkspaceID); err != nil {
			return err
		}
		if receipt, found, err := loadCommand(callbackCtx, database, record.Collection.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		} else if found {
			if receipt.requestHash != record.RequestHash || receipt.commandType != "CREATE" {
				return idempotencyConflict(errors.New("collection idempotency key is bound to another request"))
			}
			result = commandResult(receipt.collection, receipt, true)
			return nil
		}
		if _, err := database.Exec(callbackCtx, `INSERT INTO learning.smart_collection(`+collectionColumnsForInsert+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11::jsonb,$12,$13,$14,$15,$16,$17)`, collectionGORMArgs(record.Collection)...); err != nil {
			return err
		}
		if _, err := database.Exec(callbackCtx, `INSERT INTO learning.smart_collection_command(workspace_id,idempotency_key,request_hash,command_type,collection_id,collection_version,receipt,created_at) VALUES($1,$2,$3,'CREATE',$4,$5,$6::jsonb,$7)`, string(record.Collection.WorkspaceID), record.IdempotencyKey, record.RequestHash, string(record.Collection.ID), record.Collection.Version, collectionJSONB(commandReceiptJSON(record.Collection, record.IdempotencyKey, "CREATE", record.RequestHash)), record.Collection.CreatedAt.UTC()); err != nil {
			return err
		}
		receipt := commandReceipt{workspaceID: record.Collection.WorkspaceID, idempotencyKey: record.IdempotencyKey, requestHash: record.RequestHash, commandType: "CREATE", collectionID: record.Collection.ID, collectionVersion: record.Collection.Version}
		result = commandResult(record.Collection, receipt, false)
		return nil
	})
	if err != nil {
		return collectionapp.CommandResult{}, classifyGORM(ctx, err)
	}
	return result, nil
}

// UpdateCollection performs the aggregate lock/CAS and immutable receipt write
// in one shared transaction.
func (repository *GORMRepository) UpdateCollection(ctx context.Context, record collectionapp.UpdateRecord) (collectionapp.CommandResult, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.CommandResult{}, err
	}
	if err := validateUpdateRecord(record); err != nil {
		return collectionapp.CommandResult{}, err
	}
	var result collectionapp.CommandResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gormDB) error {
		if err := lockWorkspace(callbackCtx, database, record.WorkspaceID); err != nil {
			return err
		}
		if receipt, found, err := loadCommand(callbackCtx, database, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		} else if found {
			if receipt.requestHash != record.RequestHash || receipt.commandType != "UPDATE" || receipt.collectionID != record.CollectionID {
				return idempotencyConflict(errors.New("collection idempotency key is bound to another request"))
			}
			result = commandResult(receipt.collection, receipt, true)
			return nil
		}
		current, err := loadCollection(callbackCtx, database, record.WorkspaceID, record.CollectionID, true)
		if err != nil {
			return err
		}
		if current.Status == collectionapp.CollectionStatusArchived {
			return archivedImmutable(errors.New("archived collection cannot be updated"))
		}
		if current.Version != record.ExpectedVersion {
			return versionConflict(collectionapp.ErrorCodeVersionConflict, errors.New("collection expected version mismatch"))
		}
		now := record.At.UTC()
		if now.IsZero() {
			now = record.Collection.UpdatedAt.UTC()
		}
		if now.Before(current.UpdatedAt) {
			now = current.UpdatedAt
		}
		queryVersion := current.QueryVersion
		if current.QueryHash != record.Collection.QueryHash {
			queryVersion++
		}
		if _, err := database.Exec(callbackCtx, `UPDATE learning.smart_collection SET name=$3,normalized_name=$4,description=$5,query_schema_version=$6,query_version=$7,query_definition=$8::jsonb,query_hash=$9,view_type=$10,view_config=$11::jsonb,status='ACTIVE',version=version+1,updated_at=$12 WHERE workspace_id=$1 AND id=$2 AND version=$13`, string(record.WorkspaceID), string(record.CollectionID), record.Collection.Name, record.Collection.NormalizedName, record.Collection.Description, record.Collection.QuerySchemaVersion, queryVersion, collectionJSONB(queryDefinition(record.Collection)), record.Collection.QueryHash, string(record.Collection.ViewType), collectionJSONB(viewConfig(record.Collection)), now, record.ExpectedVersion); err != nil {
			return err
		}
		updated := record.Collection
		updated.QueryVersion = queryVersion
		updated.Version = record.ExpectedVersion + 1
		updated.Status = collectionapp.CollectionStatusActive
		updated.CreatedAt = current.CreatedAt
		updated.UpdatedAt = now
		updated.CachedResultVersion = current.CachedResultVersion
		updated.LastExecutedAt = current.LastExecutedAt
		if _, err := database.Exec(callbackCtx, `INSERT INTO learning.smart_collection_command(workspace_id,idempotency_key,request_hash,command_type,collection_id,collection_version,receipt,created_at) VALUES($1,$2,$3,'UPDATE',$4,$5,$6::jsonb,$7)`, string(record.WorkspaceID), record.IdempotencyKey, record.RequestHash, string(record.CollectionID), updated.Version, collectionJSONB(commandReceiptJSON(updated, record.IdempotencyKey, "UPDATE", record.RequestHash)), now); err != nil {
			return err
		}
		receipt := commandReceipt{workspaceID: record.WorkspaceID, idempotencyKey: record.IdempotencyKey, requestHash: record.RequestHash, commandType: "UPDATE", collectionID: record.CollectionID, collectionVersion: updated.Version}
		result = commandResult(updated, receipt, false)
		return nil
	})
	if err != nil {
		return collectionapp.CommandResult{}, classifyGORM(ctx, err)
	}
	return result, nil
}

// ArchiveCollection transitions an active aggregate to immutable ARCHIVED.
func (repository *GORMRepository) ArchiveCollection(ctx context.Context, record collectionapp.ArchiveRecord) (collectionapp.CommandResult, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.CommandResult{}, err
	}
	if err := validateArchiveRecord(record); err != nil {
		return collectionapp.CommandResult{}, err
	}
	var result collectionapp.CommandResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gormDB) error {
		if err := lockWorkspace(callbackCtx, database, record.WorkspaceID); err != nil {
			return err
		}
		if receipt, found, err := loadCommand(callbackCtx, database, record.WorkspaceID, record.IdempotencyKey); err != nil {
			return err
		} else if found {
			if receipt.requestHash != record.RequestHash || receipt.commandType != "ARCHIVE" || receipt.collectionID != record.CollectionID {
				return idempotencyConflict(errors.New("collection idempotency key is bound to another request"))
			}
			result = commandResult(receipt.collection, receipt, true)
			return nil
		}
		current, err := loadCollection(callbackCtx, database, record.WorkspaceID, record.CollectionID, true)
		if err != nil {
			return err
		}
		if current.Status == collectionapp.CollectionStatusArchived {
			return archivedImmutable(errors.New("collection is already archived"))
		}
		if current.Version != record.ExpectedVersion {
			return versionConflict(collectionapp.ErrorCodeVersionConflict, errors.New("collection expected version mismatch"))
		}
		now := record.At.UTC()
		if now.IsZero() {
			now = time.Now().UTC()
		}
		if now.Before(current.UpdatedAt) {
			now = current.UpdatedAt
		}
		if _, err := database.Exec(callbackCtx, `UPDATE learning.smart_collection SET status='ARCHIVED',version=version+1,updated_at=$3 WHERE workspace_id=$1 AND id=$2 AND version=$4`, string(record.WorkspaceID), string(record.CollectionID), now, record.ExpectedVersion); err != nil {
			return err
		}
		current.Status = collectionapp.CollectionStatusArchived
		current.Version = record.ExpectedVersion + 1
		current.UpdatedAt = now
		if _, err := database.Exec(callbackCtx, `INSERT INTO learning.smart_collection_command(workspace_id,idempotency_key,request_hash,command_type,collection_id,collection_version,receipt,created_at) VALUES($1,$2,$3,'ARCHIVE',$4,$5,$6::jsonb,$7)`, string(record.WorkspaceID), record.IdempotencyKey, record.RequestHash, string(record.CollectionID), current.Version, collectionJSONB(commandReceiptJSON(current, record.IdempotencyKey, "ARCHIVE", record.RequestHash)), now); err != nil {
			return err
		}
		receipt := commandReceipt{workspaceID: record.WorkspaceID, idempotencyKey: record.IdempotencyKey, requestHash: record.RequestHash, commandType: "ARCHIVE", collectionID: record.CollectionID, collectionVersion: current.Version}
		result = commandResult(current, receipt, false)
		return nil
	})
	if err != nil {
		return collectionapp.CommandResult{}, classifyGORM(ctx, err)
	}
	return result, nil
}

// GetCollection reads one Workspace-scoped aggregate through the shared root.
func (repository *GORMRepository) GetCollection(ctx context.Context, workspaceID, collectionID foundation.ID) (collectionapp.Collection, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.Collection{}, err
	}
	if !validID(workspaceID) || !validID(collectionID) {
		return collectionapp.Collection{}, requestInvalid(errors.New("collection identity is invalid"))
	}
	database, err := newGORMDB(repository.database.WithContext(ctx))
	if err != nil {
		return collectionapp.Collection{}, unavailable(err)
	}
	return loadCollection(ctx, database, workspaceID, collectionID, false)
}

// SearchCollections performs the bounded active-name/description search.
func (repository *GORMRepository) SearchCollections(ctx context.Context, query collectionapp.CollectionSearchQuery) ([]collectionapp.Collection, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	if !validID(query.WorkspaceID) || query.Query == "" || query.Limit < 1 || query.Limit > collectionapp.MaxCollectionSearchLimit {
		return nil, requestInvalid(errors.New("collection search request is invalid"))
	}
	database, err := newGORMDB(repository.database.WithContext(ctx))
	if err != nil {
		return nil, unavailable(err)
	}
	rows, err := database.collectionRows(ctx, collectionSelect+`
		WHERE workspace_id=$1 AND status='ACTIVE'
		  AND (position($2 in normalized_name)>0 OR position($2 in lower(description))>0)
		ORDER BY CASE
			WHEN normalized_name=$2 THEN 0
			WHEN position($2 in normalized_name)=1 THEN 1
			ELSE 2 END,
			updated_at DESC,id DESC
		LIMIT $3`, string(query.WorkspaceID), query.Query, query.Limit)
	if err != nil {
		return nil, classifyGORM(ctx, err)
	}
	defer rows.Close()
	items := make([]collectionapp.Collection, 0, query.Limit)
	for rows.Next() {
		item, scanErr := scanCollection(rows)
		if scanErr != nil {
			return nil, classifyGORM(ctx, scanErr)
		}
		if item.WorkspaceID != query.WorkspaceID {
			return nil, inconsistent(errors.New("collection search crossed workspace boundary"))
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyGORM(ctx, err)
	}
	return items, nil
}

// ListCollections executes revision, page and cursor creation in one
// repeatable-read/read-only UnitOfWork snapshot.
func (repository *GORMRepository) ListCollections(ctx context.Context, query collectionapp.ListQuery) (collectionapp.CollectionListPage, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.CollectionListPage{}, err
	}
	if ctx == nil || !validID(query.WorkspaceID) || query.Limit < 1 || query.Limit > 100 {
		return collectionapp.CollectionListPage{}, requestInvalid(errors.New("collection list request is invalid"))
	}
	var page collectionapp.CollectionListPage
	err := repository.within(ctx, foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}, func(callbackCtx context.Context, database *gormDB) error {
		if err := configureCollectionStatementTimeout(callbackCtx, database, defaultCollectionStatementTimeout); err != nil {
			return err
		}
		values := canonicalCollectionStatuses(query.Statuses)
		statusHash := collectionListHash(append([]string{"collection-list-status/v1"}, values...)...)
		sortHash := collectionListHash("collection-list-sort/v1", "updated_at:desc", "id:desc")
		revisionHash, err := collectionListRevision(callbackCtx, database, query.WorkspaceID, values)
		if err != nil {
			return err
		}
		args := []any{string(query.WorkspaceID), values}
		where := ` WHERE workspace_id=$1 AND status=ANY($2::text[])`
		if query.Cursor != "" {
			cursor, decodeErr := repository.cursor.Decode(query.Cursor, collectionapp.CursorBinding{Scope: collectionapp.CursorScopeList, WorkspaceID: query.WorkspaceID, QueryHash: statusHash, SortHash: sortHash, Limit: query.Limit, RevisionHash: revisionHash})
			if decodeErr != nil {
				return decodeErr
			}
			if len(cursor.LastSortValues) != 2 || cursor.LastSortValues[0] == nil || cursor.LastSortValues[1] == nil {
				return foundation.NewError(foundation.ErrorInvalidInput, collectionapp.ErrorCodeCursorInvalid, false, errors.New("collection list cursor key is invalid"))
			}
			lastUpdatedAt, parseErr := time.Parse(time.RFC3339Nano, *cursor.LastSortValues[0])
			if parseErr != nil || *cursor.LastSortValues[1] != string(cursor.LastID) {
				return foundation.NewError(foundation.ErrorInvalidInput, collectionapp.ErrorCodeCursorInvalid, false, errors.New("collection list cursor key is invalid"))
			}
			where += ` AND (updated_at < $3 OR (updated_at = $3 AND id < $4::uuid))`
			args = append(args, lastUpdatedAt.UTC(), string(cursor.LastID))
		}
		limitPosition := len(args) + 1
		args = append(args, query.Limit+1)
		rows, err := database.collectionRows(callbackCtx, collectionSelect+where+fmt.Sprintf(` ORDER BY updated_at DESC,id DESC LIMIT $%d`, limitPosition), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := make([]collectionapp.Collection, 0, query.Limit+1)
		for rows.Next() {
			item, scanErr := scanCollection(rows)
			if scanErr != nil {
				return scanErr
			}
			if item.WorkspaceID != query.WorkspaceID {
				return inconsistent(errors.New("collection list crossed workspace boundary"))
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		page.Items = items
		if len(items) > query.Limit {
			last := items[query.Limit-1]
			page.Items = items[:query.Limit]
			updatedAt := last.UpdatedAt.UTC().Format(time.RFC3339Nano)
			id := string(last.ID)
			cursor, encodeErr := repository.cursor.Encode(collectionapp.ResultCursor{Scope: collectionapp.CursorScopeList, WorkspaceID: query.WorkspaceID, QueryHash: statusHash, SortHash: sortHash, Limit: query.Limit, RevisionHash: revisionHash, LastObjectType: "COLLECTION", LastID: last.ID, LastSortValues: []*string{&updatedAt, &id}})
			if encodeErr != nil {
				return encodeErr
			}
			page.NextCursor = cursor
		}
		return nil
	})
	if err != nil {
		return collectionapp.CollectionListPage{}, classifyGORM(ctx, err)
	}
	return page, nil
}

// ExecuteQuery evaluates a saved Collection in one read-only snapshot.
func (repository *GORMRepository) ExecuteQuery(ctx context.Context, request collectionapp.ResultsQuery) (collectionapp.ResultPage, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.ResultPage{}, err
	}
	if ctx == nil || !validID(request.WorkspaceID) || !validID(request.CollectionID) || request.Limit < 1 || request.Limit > 100 {
		return collectionapp.ResultPage{}, requestInvalid(errors.New("collection result request is invalid"))
	}
	var page collectionapp.ResultPage
	err := repository.within(ctx, collectionReadOptions(), func(callbackCtx context.Context, database *gormDB) error {
		if err := configureCollectionStatementTimeout(callbackCtx, database, defaultCollectionStatementTimeout); err != nil {
			return err
		}
		legacy := &Repository{db: database, cursor: repository.cursor}
		var err error
		page, err = legacy.executeQuerySnapshot(callbackCtx, database, request)
		return err
	})
	if err != nil {
		return collectionapp.ResultPage{}, classifyGORM(ctx, err)
	}
	return page, nil
}

// ExecutePreview evaluates an unsaved canonical Query in the shared read model.
func (repository *GORMRepository) ExecutePreview(ctx context.Context, request collectionapp.PreviewQuery) (collectionapp.ResultPage, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.ResultPage{}, err
	}
	if ctx == nil || !validID(request.WorkspaceID) || request.Limit < 1 || request.Limit > 100 {
		return collectionapp.ResultPage{}, requestInvalid(errors.New("collection preview request is invalid"))
	}
	canonical, err := domain.CanonicalizeQuery(request.Query)
	if err != nil {
		return collectionapp.ResultPage{}, err
	}
	var page collectionapp.ResultPage
	err = repository.within(ctx, collectionReadOptions(), func(callbackCtx context.Context, database *gormDB) error {
		if err := configureCollectionStatementTimeout(callbackCtx, database, defaultCollectionStatementTimeout); err != nil {
			return err
		}
		legacy := &Repository{db: database, cursor: repository.cursor}
		var err error
		page, err = legacy.executePlanSnapshot(callbackCtx, database, queryExecution{workspaceID: request.WorkspaceID, queryHash: canonical.Hash, query: canonical.Definition, limit: request.Limit, cursor: request.Cursor, scope: collectionapp.CursorScopePreview})
		return err
	})
	if err != nil {
		return collectionapp.ResultPage{}, classifyGORM(ctx, err)
	}
	return page, nil
}

// PlanDurableScan freezes the definition, revision and exact count in one
// repeatable-read/read-only snapshot.
func (repository *GORMRepository) PlanDurableScan(ctx context.Context, workspaceID, collectionID foundation.ID) (collectionapp.DurableScanBinding, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.DurableScanBinding{}, err
	}
	if ctx == nil || !validID(workspaceID) || !validID(collectionID) {
		return collectionapp.DurableScanBinding{}, requestInvalid(errors.New("collection durable scan identity is invalid"))
	}
	var binding collectionapp.DurableScanBinding
	err := repository.within(ctx, collectionReadOptions(), func(callbackCtx context.Context, database *gormDB) error {
		if err := configureCollectionStatementTimeout(callbackCtx, database, defaultCollectionStatementTimeout); err != nil {
			return err
		}
		var err error
		binding, err = planDurableScanSnapshot(callbackCtx, database, workspaceID, collectionID)
		return err
	})
	if err != nil {
		return collectionapp.DurableScanBinding{}, classifyGORM(ctx, err)
	}
	return binding, nil
}

// ReadDurableScanPage reads a fixed binding and bounded pair/node projection in
// one repeatable-read/read-only snapshot.
func (repository *GORMRepository) ReadDurableScanPage(ctx context.Context, request collectionapp.DurableScanPageRequest) (collectionapp.DurableScanPage, error) {
	if err := repository.ready(ctx); err != nil {
		return collectionapp.DurableScanPage{}, err
	}
	if ctx == nil || !validDurableScanRequest(request) {
		return collectionapp.DurableScanPage{}, requestInvalid(errors.New("collection durable scan page request is invalid"))
	}
	var page collectionapp.DurableScanPage
	err := repository.within(ctx, collectionReadOptions(), func(callbackCtx context.Context, database *gormDB) error {
		if err := configureCollectionStatementTimeout(callbackCtx, database, defaultCollectionStatementTimeout); err != nil {
			return err
		}
		var err error
		page, err = readDurableScanPageSnapshot(callbackCtx, database, request)
		return err
	})
	if err != nil {
		return collectionapp.DurableScanPage{}, classifyGORM(ctx, err)
	}
	return page, nil
}

// VerifyDurableScanRevision performs the independent no-lock definition and
// revision check without recalculating exact member count.
func (repository *GORMRepository) VerifyDurableScanRevision(ctx context.Context, binding collectionapp.DurableScanBinding) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if ctx == nil || !validDurableScanBinding(binding) {
		return requestInvalid(errors.New("collection durable scan binding is invalid"))
	}
	err := repository.within(ctx, collectionReadOptions(), func(callbackCtx context.Context, database *gormDB) error {
		if err := configureCollectionStatementTimeout(callbackCtx, database, defaultCollectionStatementTimeout); err != nil {
			return err
		}
		collection, plan, _, _, err := loadDurableScanPlan(callbackCtx, database, binding.WorkspaceID, binding.CollectionID)
		if err != nil {
			return err
		}
		return validateDurableScanRevision(callbackCtx, database, binding, collection, plan)
	})
	return classifyGORM(ctx, err)
}

// VerifyDurableScanBindingScoped verifies a binding inside the caller-owned
// active transaction. It never commits, rolls back, or falls back to root DB.
func (repository *GORMRepository) VerifyDurableScanBindingScoped(ctx context.Context, scope foundation.TransactionScope, binding collectionapp.DurableScanBinding) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if ctx == nil || scope == nil || !validDurableScanBinding(binding) {
		return requestInvalid(errors.New("collection scoped durable scan binding is invalid"))
	}
	database, err := gormTransaction(scope)
	if err != nil {
		return classifyGORM(ctx, err)
	}
	_, _, _, _, err = verifyDurableScanBindingSnapshot(ctx, database, binding, true)
	return classifyGORM(ctx, err)
}

func collectionReadOptions() foundation.TransactionOptions {
	return foundation.TransactionOptions{Isolation: foundation.TransactionIsolationRepeatableRead, ReadOnly: true}
}

func collectionGORMArgs(collection collectionapp.Collection) []any {
	return []any{string(collection.ID), string(collection.WorkspaceID), collection.Name, collection.NormalizedName, collection.Description, collection.QuerySchemaVersion, collection.QueryVersion, collectionJSONB(queryDefinition(collection)), collection.QueryHash, string(collection.ViewType), collectionJSONB(viewConfig(collection)), string(collection.Status), collection.CachedResultVersion, collection.LastExecutedAt, collection.Version, collection.CreatedAt.UTC(), collection.UpdatedAt.UTC()}
}

func (repository *GORMRepository) within(ctx context.Context, options foundation.TransactionOptions, work func(context.Context, *gormDB) error) error {
	if repository == nil || !validCollectionGORMDatabase(repository.database) || !validCollectionGORMUnitOfWork(repository.unitOfWork) {
		return unavailable(errors.New("collection GORM repository is unavailable"))
	}
	if ctx == nil || work == nil {
		return requestInvalid(errors.New("collection GORM transaction callback is invalid"))
	}
	err := repository.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		database, err := gormTransaction(scope)
		if err != nil {
			return err
		}
		return work(callbackCtx, database)
	})
	return classifyGORM(ctx, err)
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validCollectionGORMDatabase(repository.database) || !validCollectionGORMUnitOfWork(repository.unitOfWork) || repository.cursor == nil {
		return unavailable(errors.New("collection GORM repository is unavailable"))
	}
	if ctx == nil {
		return requestInvalid(errors.New("collection context is nil"))
	}
	return nil
}

var _ collectionapp.Repository = (*GORMRepository)(nil)
var _ collectionapp.CollectionSearchRepository = (*GORMRepository)(nil)
var _ collectionapp.QueryRepository = (*GORMRepository)(nil)
var _ collectionapp.PreviewRepository = (*GORMRepository)(nil)
var _ collectionapp.DurableScanRepository = (*GORMRepository)(nil)
var _ collectionapp.DurableScanRevisionVerifier = (*GORMRepository)(nil)
var _ collectionapp.ScopedDurableScanBindingVerifier = (*GORMRepository)(nil)
