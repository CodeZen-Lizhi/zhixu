package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

const searchGORMBindingInvalidCode = "RETRIEVAL_GORM_SQL_BINDING_INVALID"

// GORMSearchRepository 是未接入生产 Composition 的 Search/Evidence GORM sibling。
type GORMSearchRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

var _ application.SearchStore = (*GORMSearchRepository)(nil)

// NewGORMSearchRepository 从同一个平台 Pool 派生 GORM root 与事务边界。
func NewGORMSearchRepository(pool *platformpostgres.Pool) (*GORMSearchRepository, error) {
	if pool == nil {
		return nil, dependency("RETRIEVAL_SEARCH_DATABASE_UNAVAILABLE", errors.New("retrieval PostgreSQL pool is unavailable"))
	}
	database, err := pool.GORM()
	if err != nil {
		return nil, dependency("RETRIEVAL_SEARCH_DATABASE_UNAVAILABLE", errors.New("retrieval GORM database is unavailable"))
	}
	unitOfWork, err := pool.UnitOfWork()
	if err != nil {
		return nil, dependency("RETRIEVAL_SEARCH_DATABASE_UNAVAILABLE", errors.New("retrieval GORM unit of work is unavailable"))
	}
	if !validSearchGORMDatabase(database) || nilSearchGORMDependency(unitOfWork) {
		return nil, dependency("RETRIEVAL_SEARCH_DATABASE_UNAVAILABLE", errors.New("retrieval GORM dependencies are unavailable"))
	}
	return &GORMSearchRepository{database: database, unitOfWork: unitOfWork}, nil
}

// LoadActiveSearchIndex 在同一只读快照中读取 Active Index 与 Embedding 绑定。
func (repository *GORMSearchRepository) LoadActiveSearchIndex(ctx context.Context, workspaceID foundation.ID) (application.SearchIndex, error) {
	if err := repository.ready(ctx); err != nil {
		return application.SearchIndex{}, err
	}
	canonicalWorkspaceID, err := foundation.ParseID(string(workspaceID))
	if err != nil || canonicalWorkspaceID != workspaceID {
		return application.SearchIndex{}, searchInvalid("RETRIEVAL_SEARCH_WORKSPACE_INVALID", errors.New("workspace id is invalid"))
	}

	var result application.SearchIndex
	err = repository.withinRead(
		ctx,
		"RETRIEVAL_ACTIVE_SEARCH_INDEX_QUERY_FAILED",
		"RETRIEVAL_ACTIVE_SEARCH_INDEX_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB) error {
			row, queryErr := gormSearchRawRow(callbackCtx, transaction, `SELECT `+indexColumns+`
				FROM retrieval.index_version WHERE workspace_id=$1 AND status='active'`, string(workspaceID))
			if queryErr != nil {
				return gormSearchClassify(callbackCtx, queryErr, "RETRIEVAL_ACTIVE_SEARCH_INDEX_QUERY_FAILED")
			}
			index, scanErr := scanIndex(row)
			if gormSearchNoRows(scanErr) {
				return notFound("RETRIEVAL_ACTIVE_SEARCH_INDEX_NOT_FOUND", scanErr)
			}
			if scanErr != nil {
				return gormSearchClassify(callbackCtx, scanErr, "RETRIEVAL_ACTIVE_SEARCH_INDEX_QUERY_FAILED")
			}
			result = application.SearchIndex{Index: index}
			if index.EmbeddingVersionID == nil {
				return nil
			}

			embeddingRow, queryErr := gormSearchRawRow(callbackCtx, transaction, `SELECT `+embeddingColumns+`
				FROM retrieval.embedding_version WHERE id=$1`, string(*index.EmbeddingVersionID))
			if queryErr != nil {
				return gormSearchClassify(callbackCtx, queryErr, "RETRIEVAL_ACTIVE_SEARCH_EMBEDDING_QUERY_FAILED")
			}
			embedding, scanErr := scanEmbedding(embeddingRow)
			if gormSearchNoRows(scanErr) {
				return consistency("RETRIEVAL_ACTIVE_SEARCH_EMBEDDING_MISSING", scanErr)
			}
			if scanErr != nil {
				return gormSearchClassify(callbackCtx, scanErr, "RETRIEVAL_ACTIVE_SEARCH_EMBEDDING_QUERY_FAILED")
			}
			fusion, decodeErr := domain.DecodeRRFConfig(index.FusionConfig)
			if decodeErr != nil {
				return consistency("RETRIEVAL_ACTIVE_SEARCH_FUSION_INVALID", decodeErr)
			}
			result.EmbeddingVersion = &embedding
			result.Fusion = &fusion
			return nil
		},
	)
	if err != nil {
		return application.SearchIndex{}, err
	}
	return result, nil
}

// SearchLexical 在单个只读事务中设置 transaction-local trigram 阈值并执行词法检索。
func (repository *GORMSearchRepository) SearchLexical(ctx context.Context, query application.LexicalSearchQuery) ([]domain.SearchCandidate, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	canonicalFilter, err := validateSearchQuery(query.WorkspaceID, query.IndexVersionID, query.Query, query.Filter, query.Limit)
	if err != nil {
		return nil, err
	}
	arguments := []any{
		string(query.WorkspaceID), string(query.IndexVersionID), strings.TrimSpace(query.Query), query.Limit,
		domain.MaxEvidenceProvenance,
	}
	filterSQL := appendSearchFilter(&arguments, canonicalFilter)

	var result []domain.SearchCandidate
	err = repository.withinRead(
		ctx,
		"RETRIEVAL_LEXICAL_SEARCH_TRANSACTION_FAILED",
		"RETRIEVAL_LEXICAL_SEARCH_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB) error {
			if _, execErr := gormSearchExec(callbackCtx, transaction, `SELECT set_config('pg_trgm.similarity_threshold',$1,true)`, searchTrigramThreshold); execErr != nil {
				return gormSearchClassify(callbackCtx, execErr, "RETRIEVAL_LEXICAL_SEARCH_CONFIG_FAILED")
			}
			rows, queryErr := gormSearchRawRows(callbackCtx, transaction, fmt.Sprintf(
				lexicalCandidateSQL, filterSQL, searchSnippetCharacterLimit, searchRerankCharacterLimit,
			), arguments...)
			if queryErr != nil {
				return gormSearchClassify(callbackCtx, queryErr, "RETRIEVAL_LEXICAL_SEARCH_QUERY_FAILED")
			}
			defer func() { _ = rows.Close() }()

			candidates, scanErr := scanLexicalCandidatesWithClassifier(rows, func(cause error, code string) error {
				return gormSearchClassify(callbackCtx, cause, code)
			})
			closeErr := rows.Close()
			if scanErr != nil {
				return scanErr
			}
			if closeErr != nil {
				return gormSearchClassify(callbackCtx, closeErr, "RETRIEVAL_LEXICAL_SEARCH_QUERY_FAILED")
			}
			result = candidates
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// SearchVector 在单个只读快照中校验 Embedding 绑定并执行固定距离算子的向量检索。
func (repository *GORMSearchRepository) SearchVector(ctx context.Context, query application.VectorSearchQuery) ([]domain.SearchCandidate, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	canonicalFilter, err := validateSearchQuery(query.WorkspaceID, query.IndexVersionID, "vector", query.Filter, query.Limit)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateEmbeddingVector(query.EmbeddingVersion, query.QueryEmbedding); err != nil {
		return nil, err
	}

	var result []domain.SearchCandidate
	err = repository.withinRead(
		ctx,
		"RETRIEVAL_VECTOR_SEARCH_QUERY_FAILED",
		"RETRIEVAL_VECTOR_SEARCH_COMMIT_FAILED",
		func(callbackCtx context.Context, transaction *gorm.DB) error {
			embeddingRow, queryErr := gormSearchRawRow(callbackCtx, transaction, `SELECT `+embeddingColumns+`
				FROM retrieval.embedding_version WHERE id=$1`, string(query.EmbeddingVersion.ID))
			if queryErr != nil {
				return gormSearchClassify(callbackCtx, queryErr, "RETRIEVAL_VECTOR_EMBEDDING_QUERY_FAILED")
			}
			persistedEmbedding, scanErr := scanEmbedding(embeddingRow)
			if gormSearchNoRows(scanErr) {
				return consistency("RETRIEVAL_VECTOR_EMBEDDING_MISSING", scanErr)
			}
			if scanErr != nil {
				return gormSearchClassify(callbackCtx, scanErr, "RETRIEVAL_VECTOR_EMBEDDING_QUERY_FAILED")
			}
			if persistedEmbedding.ID != query.EmbeddingVersion.ID || !domain.SameEmbeddingBinding(persistedEmbedding, query.EmbeddingVersion) {
				return consistency("RETRIEVAL_VECTOR_EMBEDDING_BINDING_INVALID", errors.New("query embedding version differs from persisted binding"))
			}
			distanceExpression, ok := vectorDistanceExpression(persistedEmbedding.DistanceMetric, persistedEmbedding.Dimensions)
			if !ok {
				return searchInvalid("RETRIEVAL_VECTOR_DISTANCE_INVALID", errors.New("unsupported vector distance metric"))
			}
			arguments := []any{
				string(query.WorkspaceID), string(query.IndexVersionID), string(query.EmbeddingVersion.ID),
				pgvector.NewVector(query.QueryEmbedding), query.Limit, domain.MaxEvidenceProvenance,
			}
			filterSQL := appendSearchFilter(&arguments, canonicalFilter)
			nearestEligibilitySQL := ""
			if filterSQL != "" {
				nearestEligibilitySQL = ` AND projection.chunk_id IN (
					SELECT matched_chunks.chunk_id
					FROM matched_chunks
					WHERE matched_chunks.index_version_id=active_index.id
					OFFSET 0
				)`
			}
			rows, queryErr := gormSearchRawRows(callbackCtx, transaction, fmt.Sprintf(
				vectorCandidateSQL, filterSQL, distanceExpression, persistedEmbedding.Dimensions, nearestEligibilitySQL, distanceExpression,
				searchSnippetCharacterLimit, searchRerankCharacterLimit,
			), arguments...)
			if queryErr != nil {
				return gormSearchClassify(callbackCtx, queryErr, "RETRIEVAL_VECTOR_SEARCH_QUERY_FAILED")
			}
			defer func() { _ = rows.Close() }()

			candidates, scanErr := scanVectorCandidatesWithClassifier(rows, func(cause error, code string) error {
				return gormSearchClassify(callbackCtx, cause, code)
			})
			closeErr := rows.Close()
			if scanErr != nil {
				return scanErr
			}
			if closeErr != nil {
				return gormSearchClassify(callbackCtx, closeErr, "RETRIEVAL_VECTOR_SEARCH_QUERY_FAILED")
			}
			result = candidates
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (repository *GORMSearchRepository) ready(ctx context.Context) error {
	if repository == nil || !validSearchGORMDatabase(repository.database) || nilSearchGORMDependency(repository.unitOfWork) {
		return dependency("RETRIEVAL_SEARCH_DATABASE_UNAVAILABLE", errors.New("retrieval GORM search repository is unavailable"))
	}
	if ctx == nil {
		return searchInvalid("RETRIEVAL_SEARCH_CONTEXT_INVALID", errors.New("search context is nil"))
	}
	return nil
}

func (repository *GORMSearchRepository) withinRead(
	ctx context.Context,
	transactionCode string,
	commitCode string,
	work func(context.Context, *gorm.DB) error,
) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if work == nil || transactionCode == "" || commitCode == "" {
		return searchInvalid("RETRIEVAL_SEARCH_TRANSACTION_INVALID", errors.New("search transaction boundary is invalid"))
	}
	callbackSucceeded := false
	err := repository.unitOfWork.Within(ctx, foundation.TransactionOptions{
		Isolation: foundation.TransactionIsolationRepeatableRead,
		ReadOnly:  true,
	}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
		if unwrapErr != nil {
			return dependency("RETRIEVAL_SEARCH_TRANSACTION_UNAVAILABLE", errors.New("retrieval GORM transaction is unavailable"))
		}
		workErr := work(callbackCtx, transaction.WithContext(callbackCtx))
		callbackSucceeded = workErr == nil
		return workErr
	})
	if callbackSucceeded {
		return gormSearchClassify(ctx, err, commitCode)
	}
	return gormSearchClassify(ctx, err, transactionCode)
}

func validSearchGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func nilSearchGORMDependency(value any) bool {
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

func gormSearchRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validSearchGORMDatabase(database) {
		return nil, errors.New("retrieval GORM row query is unavailable")
	}
	rendered, bound, err := renderSearchGORMPositional(query, arguments)
	if err != nil {
		return nil, err
	}
	statement := database.WithContext(ctx).Raw(rendered, bound...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("retrieval GORM row query returned no row handle")
	}
	return row, nil
}

func gormSearchRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validSearchGORMDatabase(database) {
		return nil, errors.New("retrieval GORM rows query is unavailable")
	}
	rendered, bound, err := renderSearchGORMPositional(query, arguments)
	if err != nil {
		return nil, err
	}
	statement := database.WithContext(ctx).Raw(rendered, bound...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, errors.New("retrieval GORM rows query returned no rows handle")
	}
	return rows, nil
}

func gormSearchExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validSearchGORMDatabase(database) {
		return 0, errors.New("retrieval GORM statement is unavailable")
	}
	rendered, bound, err := renderSearchGORMPositional(query, arguments)
	if err != nil {
		return 0, err
	}
	result := database.WithContext(ctx).Exec(rendered, bound...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func gormSearchNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound)
}

func gormSearchClassify(ctx context.Context, cause error, code string) error {
	if cause == nil {
		return nil
	}
	if contextCause := gormSearchContextCause(ctx, cause); contextCause != nil {
		if errors.Is(contextCause, context.Canceled) {
			return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, contextCause)
		}
		if errors.Is(contextCause, context.DeadlineExceeded) {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, contextCause)
		}
	}
	var classified *foundation.Error
	if errors.As(cause, &classified) {
		return cause
	}
	if gormSearchNoRows(cause) {
		return notFound(code, cause)
	}
	if errors.Is(cause, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, cause)
	}
	var postgresError *pgconn.PgError
	if errors.As(cause, &postgresError) {
		switch postgresError.Code {
		case "40001", "40P01", "55P03":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, cause)
		case "23505":
			return conflict(code, cause)
		case "23503", "23514", "55000":
			return consistency(code, cause)
		default:
			return dependency(code, cause)
		}
	}
	return dependency(code, fmt.Errorf("retrieval GORM operation failed: %T", cause))
}

func gormSearchContextCause(ctx context.Context, cause error) error {
	if ctx != nil && ctx.Err() != nil {
		contextCause := context.Cause(ctx)
		if contextCause == nil {
			return ctx.Err()
		}
		if !errors.Is(contextCause, ctx.Err()) {
			return errors.Join(ctx.Err(), contextCause)
		}
		return contextCause
	}
	if errors.Is(cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

// renderSearchGORMPositional 将包内固定 SQL 的 $n 标记转换为 GORM 绑定。
// 重复标记按出现顺序重复绑定，字符串/注释中的标记保持原样。
func renderSearchGORMPositional(query string, arguments []any) (string, []any, error) {
	if query == "" {
		return "", nil, searchGORMBindingError("retrieval SQL is empty")
	}
	if strings.ContainsRune(query, '?') {
		return "", nil, searchGORMBindingError("retrieval SQL contains an ambiguous GORM marker")
	}

	var builder strings.Builder
	builder.Grow(len(query))
	bound := make([]any, 0, len(arguments))
	used := make([]bool, len(arguments))
	for index := 0; index < len(query); {
		switch {
		case query[index] == '\'':
			end, err := copySearchSQLQuoted(query, index, '\'', searchSQLEscapeStringPrefix(query, index), &builder)
			if err != nil {
				return "", nil, err
			}
			index = end
		case query[index] == '"':
			end, err := copySearchSQLQuoted(query, index, '"', false, &builder)
			if err != nil {
				return "", nil, err
			}
			index = end
		case query[index] == '-' && index+1 < len(query) && query[index+1] == '-':
			end := index + 2
			for end < len(query) && query[end] != '\n' {
				end++
			}
			builder.WriteString(query[index:end])
			index = end
		case query[index] == '/' && index+1 < len(query) && query[index+1] == '*':
			end, err := copySearchSQLBlockComment(query, index, &builder)
			if err != nil {
				return "", nil, err
			}
			index = end
		case query[index] == '$':
			if delimiter, ok := searchSQLDollarQuoteDelimiter(query[index:]); ok {
				closing := strings.Index(query[index+len(delimiter):], delimiter)
				if closing < 0 {
					return "", nil, searchGORMBindingError("retrieval SQL dollar quote is unterminated")
				}
				end := index + closing + 2*len(delimiter)
				builder.WriteString(query[index:end])
				index = end
				continue
			}
			if index+1 >= len(query) || !searchSQLDigit(query[index+1]) {
				return "", nil, searchGORMBindingError("retrieval SQL positional marker is malformed")
			}
			end := index + 1
			for end < len(query) && searchSQLDigit(query[end]) {
				end++
			}
			if end < len(query) && searchSQLIdentifierPart(query[end]) {
				return "", nil, searchGORMBindingError("retrieval SQL positional marker is malformed")
			}
			number, err := strconv.ParseUint(query[index+1:end], 10, 64)
			if err != nil || number < 1 || number > uint64(len(arguments)) {
				return "", nil, searchGORMBindingError("retrieval SQL positional marker is out of range")
			}
			builder.WriteByte('?')
			bound = append(bound, arguments[number-1])
			used[number-1] = true
			index = end
		default:
			builder.WriteByte(query[index])
			index++
		}
	}
	for index, wasUsed := range used {
		if !wasUsed {
			return "", nil, searchGORMBindingError(fmt.Sprintf("retrieval SQL argument %d is unused", index+1))
		}
	}
	normalized, err := normalizeSearchGORMArgs(bound)
	if err != nil {
		return "", nil, err
	}
	return builder.String(), normalized, nil
}

func copySearchSQLQuoted(query string, start int, quote byte, backslashEscapes bool, builder *strings.Builder) (int, error) {
	index := start
	builder.WriteByte(query[index])
	index++
	for index < len(query) {
		builder.WriteByte(query[index])
		if query[index] == '\\' && backslashEscapes && index+1 < len(query) {
			index++
			builder.WriteByte(query[index])
			index++
			continue
		}
		if query[index] == quote {
			if index+1 < len(query) && query[index+1] == quote {
				index++
				builder.WriteByte(query[index])
				index++
				continue
			}
			return index + 1, nil
		}
		index++
	}
	return 0, searchGORMBindingError("retrieval SQL quoted value is unterminated")
}

func searchSQLEscapeStringPrefix(query string, quote int) bool {
	if quote < 1 || query[quote-1] != 'E' && query[quote-1] != 'e' {
		return false
	}
	if quote == 1 {
		return true
	}
	return !searchSQLIdentifierPart(query[quote-2])
}

func copySearchSQLBlockComment(query string, start int, builder *strings.Builder) (int, error) {
	depth := 0
	index := start
	for index < len(query) {
		switch {
		case index+1 < len(query) && query[index] == '/' && query[index+1] == '*':
			depth++
			builder.WriteString("/*")
			index += 2
		case index+1 < len(query) && query[index] == '*' && query[index+1] == '/':
			depth--
			builder.WriteString("*/")
			index += 2
			if depth == 0 {
				return index, nil
			}
		default:
			builder.WriteByte(query[index])
			index++
		}
	}
	return 0, searchGORMBindingError("retrieval SQL comment is unterminated")
}

func searchSQLDollarQuoteDelimiter(value string) (string, bool) {
	if len(value) < 2 || value[0] != '$' {
		return "", false
	}
	if value[1] == '$' {
		return "$$", true
	}
	if !searchSQLIdentifierStart(value[1]) {
		return "", false
	}
	index := 2
	for index < len(value) && searchSQLDollarTagPart(value[index]) {
		index++
	}
	if index >= len(value) || value[index] != '$' {
		return "", false
	}
	return value[:index+1], true
}

func searchSQLDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func searchSQLIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func searchSQLDollarTagPart(value byte) bool {
	return searchSQLIdentifierStart(value) || searchSQLDigit(value)
}

func searchSQLIdentifierPart(value byte) bool {
	return searchSQLDollarTagPart(value) || value == '$'
}

func normalizeSearchGORMArgs(arguments []any) ([]any, error) {
	normalized := make([]any, len(arguments))
	for index, argument := range arguments {
		value, err := normalizeSearchGORMArg(argument)
		if err != nil {
			return nil, err
		}
		normalized[index] = value
	}
	return normalized, nil
}

func normalizeSearchGORMArg(argument any) (any, error) {
	if argument == nil {
		return nil, nil
	}
	reflected := reflect.ValueOf(argument)
	if reflected.Kind() == reflect.Pointer && reflected.IsNil() {
		return nil, nil
	}
	if _, ok := argument.(driver.Valuer); ok {
		return argument, nil
	}
	for reflected.Kind() == reflect.Pointer {
		reflected = reflected.Elem()
	}
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return argument, nil
	}
	if reflected.Type().Elem().Kind() == reflect.Uint8 {
		return nil, searchGORMBindingError("retrieval SQL byte slices require an explicit carrier")
	}
	if reflected.Type().Elem().Kind() == reflect.String {
		values := make([]string, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			values[index] = reflected.Index(index).String()
		}
		return pq.Array(values), nil
	}
	return pq.Array(reflected.Interface()), nil
}

func searchGORMBindingError(message string) error {
	return consistency(searchGORMBindingInvalidCode, errors.New(message))
}
