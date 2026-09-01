package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// gormDB adapts the shared database/sql-backed GORM transaction to the small
// pgx-shaped helper boundary used by the legacy Collection SQL builders. The
// adapter owns binding conversion; no application or domain package sees it.
type gormDB struct {
	database *gorm.DB
}

type gormScannerMode uint8

const (
	gormScannerModeDefault gormScannerMode = iota
	gormScannerModeCollection
	gormScannerModeReceipt
	gormScannerModeHydration
	gormScannerModeDurableNode
)

func newGORMDB(database *gorm.DB) (*gormDB, error) {
	if !validCollectionGORMDatabase(database) {
		return nil, errors.New("collection GORM database is unavailable")
	}
	return &gormDB{database: database}, nil
}

func (database *gormDB) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if database == nil || !validCollectionGORMDatabase(database.database) {
		return pgconn.CommandTag{}, errors.New("collection GORM database is unavailable")
	}
	rendered, renderedArgs, err := renderGORMPositional(query, args)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	result := database.database.WithContext(ctx).Exec(rendered, normalizeGORMArgs(renderedArgs)...)
	if result.Error != nil {
		return pgconn.CommandTag{}, classifyGORM(ctx, result.Error)
	}
	return pgconn.CommandTag{}, nil
}

func (database *gormDB) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if database == nil || !validCollectionGORMDatabase(database.database) {
		return nil, errors.New("collection GORM database is unavailable")
	}
	rendered, renderedArgs, err := renderGORMPositional(query, args)
	if err != nil {
		return nil, err
	}
	statement := database.database.WithContext(ctx).Raw(rendered, normalizeGORMArgs(renderedArgs)...)
	if statement.Error != nil {
		return nil, classifyGORM(ctx, statement.Error)
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, classifyGORM(ctx, err)
	}
	if rows == nil {
		return nil, errors.New("collection GORM query returned nil rows")
	}
	return &gormRows{rows: rows, ctx: ctx, mode: gormScannerModeDefault}, nil
}

func (database *gormDB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if database == nil || !validCollectionGORMDatabase(database.database) {
		return gormRow{err: errors.New("collection GORM database is unavailable")}
	}
	rendered, renderedArgs, err := renderGORMPositional(query, args)
	if err != nil {
		return gormRow{err: err}
	}
	statement := database.database.WithContext(ctx).Raw(rendered, normalizeGORMArgs(renderedArgs)...)
	if statement.Error != nil {
		return gormRow{err: classifyGORM(ctx, statement.Error)}
	}
	row := statement.Row()
	if row == nil {
		return gormRow{err: errors.New("collection GORM query returned nil row")}
	}
	return gormRow{row: row, ctx: ctx, mode: gormScannerModeDefault}
}

type gormRow struct {
	row  *sql.Row
	err  error
	ctx  context.Context
	mode gormScannerMode
}

func (row gormRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.row == nil {
		return errors.New("collection GORM row is nil")
	}
	err := scanGORMRow(row.row, row.mode, dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return pgx.ErrNoRows
	}
	return classifyGORM(row.ctx, err)
}

// gormRows bridges database/sql.Rows to pgx.Rows. Collection only needs the
// streaming methods, but implementing the full interface keeps the legacy
// helper boundary explicit and fail-closed for future callers.
type gormRows struct {
	rows  *sql.Rows
	ctx   context.Context
	err   error
	close bool
	mode  gormScannerMode
}

func (rows *gormRows) Close() {
	if rows == nil || rows.close {
		return
	}
	rows.close = true
	if rows.rows != nil {
		if err := rows.rows.Close(); err != nil && rows.err == nil {
			rows.err = classifyGORM(rows.ctx, err)
		}
	}
}

func (rows *gormRows) Err() error {
	if rows == nil {
		return errors.New("collection GORM rows are nil")
	}
	if rows.err != nil {
		return rows.err
	}
	if rows.rows == nil {
		return errors.New("collection GORM rows are nil")
	}
	return classifyGORM(rows.ctx, rows.rows.Err())
}

func (*gormRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

func (*gormRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (rows *gormRows) Next() bool {
	if rows == nil || rows.rows == nil || rows.err != nil {
		return false
	}
	if rows.rows.Next() {
		return true
	}
	rows.Close()
	return false
}

func (rows *gormRows) Scan(dest ...any) error {
	if rows == nil || rows.rows == nil {
		return errors.New("collection GORM rows are nil")
	}
	err := scanGORMRow(rows.rows, rows.mode, dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return pgx.ErrNoRows
	}
	return classifyGORM(rows.ctx, err)
}

func (rows *gormRows) Values() ([]any, error) {
	if rows == nil || rows.rows == nil {
		return nil, errors.New("collection GORM rows are nil")
	}
	columns, err := rows.rows.Columns()
	if err != nil {
		return nil, classifyGORM(rows.ctx, err)
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.rows.Scan(destinations...); err != nil {
		return nil, classifyGORM(rows.ctx, err)
	}
	return values, nil
}

func (*gormRows) RawValues() [][]byte { return nil }

func (*gormRows) Conn() *pgx.Conn { return nil }

func (database *gormDB) collectionRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := database.QueryRow(ctx, query, args...)
	if typed, ok := row.(gormRow); ok {
		typed.mode = gormScannerModeCollection
		return typed
	}
	return row
}

func (database *gormDB) receiptRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := database.QueryRow(ctx, query, args...)
	if typed, ok := row.(gormRow); ok {
		typed.mode = gormScannerModeReceipt
		return typed
	}
	return row
}

func (database *gormDB) hydrationRows(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := database.Query(ctx, query, args...)
	if typed, ok := rows.(*gormRows); ok {
		typed.mode = gormScannerModeHydration
	}
	return rows, err
}

func (database *gormDB) collectionRows(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := database.Query(ctx, query, args...)
	if typed, ok := rows.(*gormRows); ok {
		typed.mode = gormScannerModeCollection
	}
	return rows, err
}

func (database *gormDB) durableNodeRows(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	rows, err := database.Query(ctx, query, args...)
	if typed, ok := rows.(*gormRows); ok {
		typed.mode = gormScannerModeDurableNode
	}
	return rows, err
}

type sqlScanner interface {
	Scan(...any) error
}

func scanGORMRow(scanner sqlScanner, mode gormScannerMode, dest ...any) error {
	if scanner == nil {
		return errors.New("collection GORM scanner is nil")
	}
	transformed := append([]any(nil), dest...)
	switch mode {
	case gormScannerModeCollection:
		if len(dest) != 17 {
			return inconsistent(errors.New("collection GORM aggregate projection changed"))
		}
		if err := replaceJSONBScanner(transformed, dest, 7, false); err != nil {
			return err
		}
		if err := replaceJSONBScanner(transformed, dest, 10, false); err != nil {
			return err
		}
	case gormScannerModeReceipt:
		if len(dest) != 6 {
			return inconsistent(errors.New("collection GORM receipt projection changed"))
		}
		if err := replaceJSONBScanner(transformed, dest, 5, false); err != nil {
			return err
		}
	case gormScannerModeHydration:
		if len(dest) != 12 {
			return inconsistent(errors.New("collection GORM hydration projection changed"))
		}
		for position, allowNull := range map[int]bool{2: false, 3: true, 6: false, 7: false, 10: false} {
			if err := replaceJSONBScanner(transformed, dest, position, allowNull); err != nil {
				return err
			}
		}
	case gormScannerModeDurableNode:
		if len(dest) != 9 {
			return inconsistent(errors.New("collection GORM durable node projection changed"))
		}
		for _, position := range []int{6, 7, 8} {
			if err := replaceArrayScanner(transformed, dest, position); err != nil {
				return err
			}
		}
	}
	return scanner.Scan(transformed...)
}

func replaceJSONBScanner(transformed, original []any, position int, allowNull bool) error {
	destination, ok := original[position].(*[]byte)
	if !ok {
		return inconsistent(errors.New("collection GORM JSONB destination changed"))
	}
	value := new(collectionJSONB)
	transformed[position] = &copyJSONBScanner{value: value, destination: destination, allowNull: allowNull}
	return nil
}

func replaceArrayScanner(transformed, original []any, position int) error {
	destination, ok := original[position].(*[]string)
	if !ok {
		return inconsistent(errors.New("collection GORM array destination changed"))
	}
	value := new(pq.StringArray)
	transformed[position] = &copyArrayScanner{value: value, destination: destination}
	return nil
}

type copyJSONBScanner struct {
	value       *collectionJSONB
	destination *[]byte
	allowNull   bool
}

func (scanner *copyJSONBScanner) Scan(source any) error {
	if scanner == nil || scanner.value == nil || scanner.destination == nil {
		return errors.New("collection JSONB copy scanner is invalid")
	}
	if source == nil {
		if !scanner.allowNull {
			return inconsistent(errors.New("persisted collection JSONB value is null"))
		}
		*scanner.destination = nil
		return nil
	}
	if err := scanner.value.Scan(source); err != nil {
		return inconsistent(err)
	}
	*scanner.destination = append((*scanner.destination)[:0], []byte(*scanner.value)...)
	return nil
}

type copyArrayScanner struct {
	value       *pq.StringArray
	destination *[]string
}

func (scanner *copyArrayScanner) Scan(source any) error {
	if scanner == nil || scanner.value == nil || scanner.destination == nil {
		return errors.New("collection array copy scanner is invalid")
	}
	if source == nil {
		return inconsistent(errors.New("persisted collection array value is null"))
	}
	if err := scanner.value.Scan(source); err != nil {
		return inconsistent(err)
	}
	*scanner.destination = append((*scanner.destination)[:0], []string(*scanner.value)...)
	return nil
}

// renderGORMPositional converts PostgreSQL positional parameters to GORM's
// question-mark bindings. It expands repeated markers in occurrence order and
// rejects malformed, zero, out-of-range, or unused arguments.
func renderGORMPositional(query string, args []any) (string, []any, error) {
	if query == "" {
		return "", nil, collectionGORMBindingError("collection SQL is empty")
	}
	var builder strings.Builder
	builder.Grow(len(query))
	bound := make([]any, 0, len(args))
	used := make([]bool, len(args))
	for index := 0; index < len(query); {
		if query[index] == '\'' {
			end, err := copySQLQuoted(query, index, '\'', sqlEscapeStringPrefix(query, index), &builder)
			if err != nil {
				return "", nil, err
			}
			index = end
			continue
		}
		if query[index] == '"' {
			end, err := copySQLQuoted(query, index, '"', false, &builder)
			if err != nil {
				return "", nil, err
			}
			index = end
			continue
		}
		if query[index] == '-' && index+1 < len(query) && query[index+1] == '-' {
			end := index + 2
			for end < len(query) && query[end] != '\n' {
				end++
			}
			builder.WriteString(query[index:end])
			index = end
			continue
		}
		if query[index] == '/' && index+1 < len(query) && query[index+1] == '*' {
			end, err := copySQLBlockComment(query, index, &builder)
			if err != nil {
				return "", nil, err
			}
			index = end
			continue
		}
		if query[index] == '$' {
			if delimiter, ok := sqlDollarQuoteDelimiter(query[index:]); ok {
				end := strings.Index(query[index+len(delimiter):], delimiter)
				if end < 0 {
					return "", nil, collectionGORMBindingError("collection SQL dollar quote is unterminated")
				}
				end += index + 2*len(delimiter)
				builder.WriteString(query[index:end])
				index = end
				continue
			}
			if index+1 >= len(query) || query[index+1] < '0' || query[index+1] > '9' {
				return "", nil, collectionGORMBindingError("collection SQL positional marker is malformed")
			}
			end := index + 1
			for end < len(query) && query[end] >= '0' && query[end] <= '9' {
				end++
			}
			number, err := strconv.Atoi(query[index+1 : end])
			if err != nil || number < 1 || number > len(args) {
				return "", nil, collectionGORMBindingError("collection SQL positional marker is out of range")
			}
			builder.WriteByte('?')
			bound = append(bound, args[number-1])
			used[number-1] = true
			index = end
			continue
		}
		builder.WriteByte(query[index])
		index++
	}
	for index, wasUsed := range used {
		if !wasUsed {
			return "", nil, collectionGORMBindingError(fmt.Sprintf("collection SQL argument %d is unused", index+1))
		}
	}
	return builder.String(), bound, nil
}

func copySQLQuoted(query string, start int, quote byte, backslashEscapes bool, builder *strings.Builder) (int, error) {
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
	return 0, collectionGORMBindingError("collection SQL quoted literal is unterminated")
}

func sqlEscapeStringPrefix(query string, quote int) bool {
	if quote < 1 || (query[quote-1] != 'E' && query[quote-1] != 'e') {
		return false
	}
	if quote == 1 {
		return true
	}
	previous := query[quote-2]
	return !(previous == '_' || previous >= '0' && previous <= '9' || previous >= 'A' && previous <= 'Z' || previous >= 'a' && previous <= 'z')
}

func copySQLBlockComment(query string, start int, builder *strings.Builder) (int, error) {
	depth := 0
	index := start
	for index < len(query) {
		if index+1 < len(query) && query[index] == '/' && query[index+1] == '*' {
			depth++
			builder.WriteString("/*")
			index += 2
			continue
		}
		if index+1 < len(query) && query[index] == '*' && query[index+1] == '/' {
			depth--
			builder.WriteString("*/")
			index += 2
			if depth == 0 {
				return index, nil
			}
			continue
		}
		builder.WriteByte(query[index])
		index++
	}
	return 0, collectionGORMBindingError("collection SQL comment is unterminated")
}

func sqlDollarQuoteDelimiter(value string) (string, bool) {
	if len(value) < 2 || value[0] != '$' {
		return "", false
	}
	if value[1] == '$' {
		return "$$", true
	}
	if !(value[1] == '_' || value[1] >= 'A' && value[1] <= 'Z' || value[1] >= 'a' && value[1] <= 'z') {
		return "", false
	}
	index := 2
	for index < len(value) && (value[index] == '_' || value[index] >= 'A' && value[index] <= 'Z' || value[index] >= 'a' && value[index] <= 'z' || value[index] >= '0' && value[index] <= '9') {
		index++
	}
	if index >= len(value) || value[index] != '$' {
		return "", false
	}
	return value[:index+1], true
}

func collectionGORMBindingError(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, collectionapp.ErrorCodeRequestInvalid, false, errors.New(message))
}

func normalizeGORMArgs(args []any) []any {
	result := make([]any, len(args))
	for index, value := range args {
		result[index] = normalizeGORMArg(value)
	}
	return result
}

func normalizeGORMArg(value any) any {
	if value == nil {
		return nil
	}
	if _, ok := value.(driver.Valuer); ok {
		return value
	}
	switch typed := value.(type) {
	case json.RawMessage:
		return collectionJSONB(typed)
	case []byte:
		return typed
	case []string:
		return pq.Array(typed)
	case []foundation.ID:
		values := make([]string, len(typed))
		for index := range typed {
			values[index] = string(typed[index])
		}
		return pq.Array(values)
	}
	valueOf := reflect.ValueOf(value)
	if valueOf.Kind() == reflect.Slice && valueOf.Type().Elem().Kind() != reflect.Uint8 {
		return pq.Array(value)
	}
	return value
}

// collectionJSONB binds validated JSON as a string so PostgreSQL receives
// jsonb through the explicit SQL cast instead of database/sql bytea inference.
type collectionJSONB []byte

func (value collectionJSONB) Value() (driver.Value, error) {
	if len(value) == 0 || !json.Valid(value) {
		return nil, errors.New("collection JSONB value is invalid")
	}
	return string(value), nil
}

func (value *collectionJSONB) Scan(source any) error {
	if value == nil {
		return errors.New("collection JSONB destination is nil")
	}
	var raw []byte
	switch typed := source.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = append([]byte(nil), typed...)
	default:
		return fmt.Errorf("collection JSONB source has unsupported type %T", source)
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return errors.New("persisted collection JSONB value is invalid")
	}
	*value = collectionJSONB(raw)
	return nil
}

func validCollectionGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func validCollectionGORMUnitOfWork(unitOfWork foundation.UnitOfWork) bool {
	if unitOfWork == nil {
		return false
	}
	value := reflect.ValueOf(unitOfWork)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func classifyGORM(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		cause := ctx.Err()
		if contextCause := context.Cause(ctx); contextCause != nil && contextCause != cause {
			cause = errors.Join(cause, contextCause)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return foundation.NewError(foundation.ErrorDependencyUnavailable, collectionapp.ErrorCodeQueryTimeout, true, cause)
		}
		return foundation.NewError(foundation.ErrorNonRetryableFailure, collectionapp.ErrorCodeDependencyUnavailable, false, cause)
	}
	var existing *foundation.Error
	if errors.As(err, &existing) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, collectionapp.ErrorCodeQueryTimeout, true, err)
	}
	if errors.Is(err, context.Canceled) {
		return foundation.NewError(foundation.ErrorNonRetryableFailure, collectionapp.ErrorCodeDependencyUnavailable, false, err)
	}
	if errors.Is(err, sql.ErrTxDone) {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, collectionapp.ErrorCodeDependencyUnavailable, true, err)
	}
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return notFound(err)
	}
	return classify(err)
}

func gormTransaction(scope foundation.TransactionScope) (*gormDB, error) {
	database, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, classifyGORM(context.Background(), err)
	}
	return newGORMDB(database)
}
