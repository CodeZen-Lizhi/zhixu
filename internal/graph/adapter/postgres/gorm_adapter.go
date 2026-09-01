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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// gormDB adapts the shared database/sql-backed GORM handle to Graph's private
// pgx-shaped DB boundary. It never begins or completes a transaction.
type gormDB struct {
	database *gorm.DB
}

var _ DB = (*gormDB)(nil)

func newGORMDB(database *gorm.DB) (*gormDB, error) {
	if !validGraphGORMDatabase(database) {
		return nil, errors.New("graph GORM database is unavailable")
	}
	return &gormDB{database: database}, nil
}

func (database *gormDB) Exec(ctx context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
	if database == nil || !validGraphGORMDatabase(database.database) || ctx == nil {
		return pgconn.CommandTag{}, errors.New("graph GORM statement is unavailable")
	}
	rowsAffected, err := gormGraphExec(ctx, database.database, query, arguments...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return pgconn.NewCommandTag("ROWS " + strconv.FormatInt(rowsAffected, 10)), nil
}

func (database *gormDB) Query(ctx context.Context, query string, arguments ...any) (pgx.Rows, error) {
	if database == nil || !validGraphGORMDatabase(database.database) || ctx == nil {
		return nil, errors.New("graph GORM rows query is unavailable")
	}
	rows, err := gormGraphRawRows(ctx, database.database, query, arguments...)
	if err != nil {
		return nil, err
	}
	return &gormDBRows{rows: rows}, nil
}

func (database *gormDB) QueryRow(ctx context.Context, query string, arguments ...any) pgx.Row {
	if database == nil || !validGraphGORMDatabase(database.database) || ctx == nil {
		return gormDBRow{err: errors.New("graph GORM row query is unavailable")}
	}
	row, err := gormGraphRawRow(ctx, database.database, query, arguments...)
	return gormDBRow{row: row, err: err}
}

type gormDBRow struct {
	row *sql.Row
	err error
}

func (row gormDBRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.row == nil {
		return errors.New("graph GORM row query returned no row handle")
	}
	return graphGORMScan(row.row, destinations...)
}

// gormDBRows implements pgx.Rows for the legacy Graph scanners without
// exposing pgx transaction ownership to the staged GORM repository.
type gormDBRows struct {
	rows   *sql.Rows
	err    error
	closed bool
}

func (rows *gormDBRows) Close() {
	if rows == nil || rows.closed {
		return
	}
	rows.closed = true
	if rows.rows != nil {
		if err := rows.rows.Close(); err != nil && rows.err == nil {
			rows.err = err
		}
	}
}

func (rows *gormDBRows) Err() error {
	if rows == nil || rows.rows == nil {
		return errors.New("graph GORM rows query returned no rows handle")
	}
	if rows.err != nil {
		return rows.err
	}
	return rows.rows.Err()
}

func (*gormDBRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }

func (*gormDBRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (rows *gormDBRows) Next() bool {
	return rows != nil && rows.rows != nil && rows.err == nil && rows.rows.Next()
}

func (rows *gormDBRows) Scan(destinations ...any) error {
	if rows == nil || rows.rows == nil {
		return errors.New("graph GORM rows query returned no rows handle")
	}
	return graphGORMScan(rows.rows, destinations...)
}

func (rows *gormDBRows) Values() ([]any, error) {
	if rows == nil || rows.rows == nil {
		return nil, errors.New("graph GORM rows query returned no rows handle")
	}
	columns, err := rows.rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := rows.rows.Scan(destinations...); err != nil {
		return nil, err
	}
	return values, nil
}

func (*gormDBRows) RawValues() [][]byte { return nil }

func (*gormDBRows) Conn() *pgx.Conn { return nil }

type graphSQLScanner interface {
	Scan(...any) error
}

func graphGORMScan(scanner graphSQLScanner, destinations ...any) error {
	if scanner == nil {
		return errors.New("graph GORM scanner is unavailable")
	}
	transformed := append([]any(nil), destinations...)
	for index, destination := range destinations {
		if values, ok := destination.(*[]string); ok {
			transformed[index] = &graphStringArrayDestination{destination: values}
		}
	}
	err := scanner.Scan(transformed...)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.Join(sql.ErrNoRows, pgx.ErrNoRows)
	}
	return err
}

type graphStringArrayDestination struct {
	destination *[]string
}

func (destination *graphStringArrayDestination) Scan(source any) error {
	if destination == nil || destination.destination == nil {
		return errors.New("graph array destination is unavailable")
	}
	var value graphStringArray
	if err := value.Scan(source); err != nil {
		return err
	}
	*destination.destination = append((*destination.destination)[:0], []string(value)...)
	return nil
}

type graphSQLRow struct {
	row *sql.Row
}

func (row graphSQLRow) Scan(destinations ...any) error {
	if row.row == nil {
		return errors.New("graph GORM row query returned no row handle")
	}
	return graphGORMScan(row.row, destinations...)
}

type graphSQLRows struct {
	rows *sql.Rows
	err  error
}

func (rows *graphSQLRows) Close() error {
	if rows == nil || rows.rows == nil {
		return errors.New("graph GORM rows query returned no rows handle")
	}
	if rows.err != nil {
		return rows.err
	}
	rows.err = rows.rows.Close()
	return rows.err
}

func (rows *graphSQLRows) Err() error {
	if rows == nil || rows.rows == nil {
		return errors.New("graph GORM rows query returned no rows handle")
	}
	if rows.err != nil {
		return rows.err
	}
	return rows.rows.Err()
}

func (rows *graphSQLRows) Next() bool {
	return rows != nil && rows.rows != nil && rows.err == nil && rows.rows.Next()
}

func (rows *graphSQLRows) Scan(destinations ...any) error {
	if rows == nil || rows.rows == nil {
		return errors.New("graph GORM rows query returned no rows handle")
	}
	return graphGORMScan(rows.rows, destinations...)
}

func gormRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (graphSQLScanner, error) {
	row, err := gormGraphRawRow(ctx, database, query, arguments...)
	if err != nil {
		return nil, err
	}
	return graphSQLRow{row: row}, nil
}

func gormRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*graphSQLRows, error) {
	rows, err := gormGraphRawRows(ctx, database, query, arguments...)
	if err != nil {
		return nil, err
	}
	return &graphSQLRows{rows: rows}, nil
}

func gormGraphRawRow(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Row, error) {
	if ctx == nil || !validGraphGORMDatabase(database) {
		return nil, errors.New("graph GORM row query is unavailable")
	}
	rendered, bound, err := renderGORMPositional(query, arguments)
	if err != nil {
		return nil, err
	}
	statement := database.WithContext(ctx).Raw(rendered, bound...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	row := statement.Row()
	if row == nil {
		return nil, errors.New("graph GORM row query returned no row handle")
	}
	return row, nil
}

func gormGraphRawRows(ctx context.Context, database *gorm.DB, query string, arguments ...any) (*sql.Rows, error) {
	if ctx == nil || !validGraphGORMDatabase(database) {
		return nil, errors.New("graph GORM rows query is unavailable")
	}
	rendered, bound, err := renderGORMPositional(query, arguments)
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
		return nil, errors.New("graph GORM rows query returned no rows handle")
	}
	return rows, nil
}

func gormGraphExec(ctx context.Context, database *gorm.DB, query string, arguments ...any) (int64, error) {
	if ctx == nil || !validGraphGORMDatabase(database) {
		return 0, errors.New("graph GORM statement is unavailable")
	}
	rendered, bound, err := renderGORMPositional(query, arguments)
	if err != nil {
		return 0, err
	}
	result := database.WithContext(ctx).Exec(rendered, bound...)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// renderGORMPositional converts Graph's package-owned PostgreSQL $n markers to
// GORM bindings. Repeated markers are rebound in occurrence order.
func renderGORMPositional(query string, arguments []any) (string, []any, error) {
	if query == "" {
		return "", nil, graphGORMBindingError("graph SQL is empty")
	}
	if strings.ContainsRune(query, '?') {
		return "", nil, graphGORMBindingError("graph SQL contains an ambiguous GORM marker")
	}

	var builder strings.Builder
	builder.Grow(len(query))
	bound := make([]any, 0, len(arguments))
	used := make([]bool, len(arguments))
	for index := 0; index < len(query); {
		switch {
		case query[index] == '\'':
			end, err := copyGraphSQLQuoted(query, index, '\'', graphSQLEscapeStringPrefix(query, index), &builder)
			if err != nil {
				return "", nil, err
			}
			index = end
		case query[index] == '"':
			end, err := copyGraphSQLQuoted(query, index, '"', false, &builder)
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
			end, err := copyGraphSQLBlockComment(query, index, &builder)
			if err != nil {
				return "", nil, err
			}
			index = end
		case query[index] == '$':
			if delimiter, ok := graphSQLDollarQuoteDelimiter(query[index:]); ok {
				closing := strings.Index(query[index+len(delimiter):], delimiter)
				if closing < 0 {
					return "", nil, graphGORMBindingError("graph SQL dollar quote is unterminated")
				}
				end := index + closing + 2*len(delimiter)
				builder.WriteString(query[index:end])
				index = end
				continue
			}
			if index+1 >= len(query) || !graphSQLDigit(query[index+1]) {
				return "", nil, graphGORMBindingError("graph SQL positional marker is malformed")
			}
			end := index + 1
			for end < len(query) && graphSQLDigit(query[end]) {
				end++
			}
			if end < len(query) && graphSQLIdentifierPart(query[end]) {
				return "", nil, graphGORMBindingError("graph SQL positional marker is malformed")
			}
			number, err := strconv.ParseUint(query[index+1:end], 10, 64)
			if err != nil || number < 1 || number > uint64(len(arguments)) {
				return "", nil, graphGORMBindingError("graph SQL positional marker is out of range")
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
			return "", nil, graphGORMBindingError(fmt.Sprintf("graph SQL argument %d is unused", index+1))
		}
	}
	normalized, err := normalizeGraphGORMArgs(bound)
	if err != nil {
		return "", nil, err
	}
	return builder.String(), normalized, nil
}

func copyGraphSQLQuoted(query string, start int, quote byte, backslashEscapes bool, builder *strings.Builder) (int, error) {
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
	return 0, graphGORMBindingError("graph SQL quoted value is unterminated")
}

func graphSQLEscapeStringPrefix(query string, quote int) bool {
	if quote < 1 || query[quote-1] != 'E' && query[quote-1] != 'e' {
		return false
	}
	if quote == 1 {
		return true
	}
	return !graphSQLIdentifierPart(query[quote-2])
}

func copyGraphSQLBlockComment(query string, start int, builder *strings.Builder) (int, error) {
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
	return 0, graphGORMBindingError("graph SQL comment is unterminated")
}

func graphSQLDollarQuoteDelimiter(value string) (string, bool) {
	if len(value) < 2 || value[0] != '$' {
		return "", false
	}
	if value[1] == '$' {
		return "$$", true
	}
	if !graphSQLIdentifierStart(value[1]) {
		return "", false
	}
	index := 2
	for index < len(value) && graphSQLDollarTagPart(value[index]) {
		index++
	}
	if index >= len(value) || value[index] != '$' {
		return "", false
	}
	return value[:index+1], true
}

func graphSQLDigit(value byte) bool { return value >= '0' && value <= '9' }

func graphSQLIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func graphSQLDollarTagPart(value byte) bool {
	return graphSQLIdentifierStart(value) || graphSQLDigit(value)
}

func graphSQLIdentifierPart(value byte) bool {
	return graphSQLDollarTagPart(value) || value == '$'
}

func normalizeGraphGORMArgs(arguments []any) ([]any, error) {
	normalized := make([]any, len(arguments))
	for index, argument := range arguments {
		value, err := normalizeGraphGORMArg(argument)
		if err != nil {
			return nil, err
		}
		normalized[index] = value
	}
	return normalized, nil
}

func normalizeGraphGORMArg(argument any) (any, error) {
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
	if raw, ok := argument.(json.RawMessage); ok {
		return graphJSONB(append([]byte(nil), raw...)), nil
	}

	for reflected.Kind() == reflect.Pointer {
		reflected = reflected.Elem()
	}
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return argument, nil
	}
	if reflected.Type().Elem().Kind() == reflect.Uint8 {
		return nil, graphGORMBindingError("graph SQL byte slices require an explicit carrier")
	}
	if reflected.Type().Elem().Kind() == reflect.String {
		values := make(graphStringArray, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			values[index] = reflected.Index(index).String()
		}
		return values, nil
	}
	return pq.Array(reflected.Interface()), nil
}

func graphGORMBindingError(message string) error {
	return inconsistent(errors.New(message))
}
