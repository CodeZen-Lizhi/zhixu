package postgres

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"sync"

	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// gormDB is a package-private compatibility seam for the frozen Health SQL
// implementation. It keeps GORM/database/sql transaction ownership in the
// adapter while application and domain packages continue to see no driver.
type gormDB struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
}

func newHealthGORMDB(database *gorm.DB, unitOfWork foundation.UnitOfWork) (*gormDB, error) {
	if !validHealthGORMDatabase(database) || isNilHealthDependency(unitOfWork) {
		return nil, errors.New("health GORM database is unavailable")
	}
	return &gormDB{database: database, unitOfWork: unitOfWork}, nil
}

func (database *gormDB) Begin(ctx context.Context) (pgx.Tx, error) {
	return database.begin(ctx, foundation.TransactionOptions{})
}

func (database *gormDB) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	isolation := foundation.TransactionIsolationDefault
	switch options.IsoLevel {
	case pgx.Serializable:
		isolation = foundation.TransactionIsolationSerializable
	case pgx.RepeatableRead:
		isolation = foundation.TransactionIsolationRepeatableRead
	case pgx.ReadCommitted:
		isolation = foundation.TransactionIsolationReadCommitted
	case pgx.ReadUncommitted, "":
	default:
		return nil, errors.New("health GORM transaction isolation is unsupported")
	}
	return database.begin(ctx, foundation.TransactionOptions{Isolation: isolation, ReadOnly: options.AccessMode == pgx.ReadOnly})
}

func (database *gormDB) begin(ctx context.Context, options foundation.TransactionOptions) (pgx.Tx, error) {
	if database == nil || !validHealthGORMDatabase(database.database) || isNilHealthDependency(database.unitOfWork) || ctx == nil {
		return nil, errors.New("health GORM transaction is unavailable")
	}
	bridge := &healthGORMTx{ready: make(chan error, 1), done: make(chan error, 1), finished: make(chan struct{})}
	go func() {
		err := database.unitOfWork.Within(ctx, options, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
			transaction, unwrapErr := platformpostgres.GORMTransaction(scope)
			if unwrapErr != nil {
				bridge.signalReady(unwrapErr)
				return unwrapErr
			}
			bridge.database = transaction.WithContext(callbackCtx)
			bridge.scope = scope
			bridge.signalReady(nil)
			return <-bridge.done
		})
		bridge.resultErr = err
		bridge.signalReady(err)
		close(bridge.finished)
	}()
	if err := <-bridge.ready; err != nil {
		return nil, err
	}
	return bridge, nil
}

func (database *gormDB) Exec(ctx context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
	if database == nil || !validHealthGORMDatabase(database.database) || ctx == nil {
		return pgconn.CommandTag{}, errors.New("health GORM statement is unavailable")
	}
	result := database.database.WithContext(ctx).Exec(query, normalizeHealthGORMArgs(arguments)...)
	if result.Error != nil {
		return pgconn.CommandTag{}, result.Error
	}
	return healthGORMCommandTag(result.RowsAffected), nil
}

func (database *gormDB) Query(ctx context.Context, query string, arguments ...any) (pgx.Rows, error) {
	if database == nil || !validHealthGORMDatabase(database.database) || ctx == nil {
		return nil, errors.New("health GORM query is unavailable")
	}
	statement := database.database.WithContext(ctx).Raw(query, normalizeHealthGORMArgs(arguments)...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	return &healthGORMRows{rows: rows}, nil
}

func (database *gormDB) QueryRow(ctx context.Context, query string, arguments ...any) pgx.Row {
	if database == nil || !validHealthGORMDatabase(database.database) || ctx == nil {
		return healthGORMRow{err: errors.New("health GORM row query is unavailable")}
	}
	statement := database.database.WithContext(ctx).Raw(query, normalizeHealthGORMArgs(arguments)...)
	if statement.Error != nil {
		return healthGORMRow{err: statement.Error}
	}
	return healthGORMRow{row: statement.Row()}
}

// healthGORMTx holds a live scoped GORM transaction until the legacy caller
// explicitly commits or rolls it back. Nested transactions are rejected.
type healthGORMTx struct {
	database  *gorm.DB
	scope     foundation.TransactionScope
	ready     chan error
	done      chan error
	finished  chan struct{}
	resultErr error
	once      sync.Once
	readyOnce sync.Once
}

func (transaction *healthGORMTx) signalReady(err error) {
	if transaction != nil {
		transaction.readyOnce.Do(func() { transaction.ready <- err })
	}
}

func (transaction *healthGORMTx) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("health nested transaction is unsupported")
}

func (transaction *healthGORMTx) Commit(context.Context) error {
	if transaction == nil || transaction.database == nil {
		return pgx.ErrTxClosed
	}
	transaction.once.Do(func() { transaction.done <- nil })
	<-transaction.finished
	return transaction.resultErr
}

func (transaction *healthGORMTx) Rollback(context.Context) error {
	if transaction == nil || transaction.database == nil {
		return pgx.ErrTxClosed
	}
	transaction.once.Do(func() { transaction.done <- errors.New("health transaction rollback") })
	<-transaction.finished
	return nil
}

func (*healthGORMTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("health COPY is unsupported")
}

func (*healthGORMTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (*healthGORMTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (*healthGORMTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("health prepared statements are unsupported")
}

func (transaction *healthGORMTx) Exec(ctx context.Context, query string, arguments ...any) (pgconn.CommandTag, error) {
	if transaction == nil || !validHealthGORMDatabase(transaction.database) || ctx == nil {
		return pgconn.CommandTag{}, errors.New("health GORM transaction is unavailable")
	}
	result := transaction.database.WithContext(ctx).Exec(query, normalizeHealthGORMArgs(arguments)...)
	if result.Error != nil {
		return pgconn.CommandTag{}, result.Error
	}
	return healthGORMCommandTag(result.RowsAffected), nil
}

func (transaction *healthGORMTx) Query(ctx context.Context, query string, arguments ...any) (pgx.Rows, error) {
	if transaction == nil || !validHealthGORMDatabase(transaction.database) || ctx == nil {
		return nil, errors.New("health GORM transaction query is unavailable")
	}
	statement := transaction.database.WithContext(ctx).Raw(query, normalizeHealthGORMArgs(arguments)...)
	if statement.Error != nil {
		return nil, statement.Error
	}
	rows, err := statement.Rows()
	if err != nil {
		return nil, err
	}
	return &healthGORMRows{rows: rows}, nil
}

func (transaction *healthGORMTx) QueryRow(ctx context.Context, query string, arguments ...any) pgx.Row {
	if transaction == nil || !validHealthGORMDatabase(transaction.database) || ctx == nil {
		return healthGORMRow{err: errors.New("health GORM transaction row query is unavailable")}
	}
	statement := transaction.database.WithContext(ctx).Raw(query, normalizeHealthGORMArgs(arguments)...)
	if statement.Error != nil {
		return healthGORMRow{err: statement.Error}
	}
	return healthGORMRow{row: statement.Row()}
}

func (*healthGORMTx) Conn() *pgx.Conn { return nil }

type healthGORMRow struct {
	row *sql.Row
	err error
}

func (row healthGORMRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if row.row == nil {
		return errors.New("health GORM row is unavailable")
	}
	err := row.row.Scan(destinations...)
	if errors.Is(err, sql.ErrNoRows) {
		return pgx.ErrNoRows
	}
	return err
}

type healthGORMRows struct {
	rows *sql.Rows
	err  error
}

func (rows *healthGORMRows) Close() {
	if rows != nil && rows.rows != nil {
		if err := rows.rows.Close(); err != nil && rows.err == nil {
			rows.err = err
		}
	}
}

func (rows *healthGORMRows) Err() error {
	if rows == nil || rows.rows == nil {
		return errors.New("health GORM rows are unavailable")
	}
	if rows.err != nil {
		return rows.err
	}
	return rows.rows.Err()
}

func (*healthGORMRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*healthGORMRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (rows *healthGORMRows) Next() bool {
	if rows == nil || rows.rows == nil || rows.err != nil {
		return false
	}
	if rows.rows.Next() {
		return true
	}
	rows.Close()
	return false
}

func (rows *healthGORMRows) Scan(destinations ...any) error {
	if rows == nil || rows.rows == nil {
		return errors.New("health GORM rows are unavailable")
	}
	return rows.rows.Scan(destinations...)
}

func (rows *healthGORMRows) Values() ([]any, error) {
	if rows == nil || rows.rows == nil {
		return nil, errors.New("health GORM rows are unavailable")
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

func (*healthGORMRows) RawValues() [][]byte { return nil }
func (*healthGORMRows) Conn() *pgx.Conn     { return nil }

func normalizeHealthGORMArgs(arguments []any) []any {
	result := append([]any(nil), arguments...)
	for index, argument := range result {
		switch value := argument.(type) {
		case []string:
			result[index] = pq.Array(value)
		case []foundation.ID:
			values := make([]string, len(value))
			for item := range value {
				values[item] = string(value[item])
			}
			result[index] = pq.Array(values)
		}
	}
	return result
}

func healthGORMCommandTag(rowsAffected int64) pgconn.CommandTag {
	if rowsAffected < 0 {
		return pgconn.NewCommandTag("ROWS 0")
	}
	return pgconn.NewCommandTag("ROWS " + strconv.FormatInt(rowsAffected, 10))
}

func validHealthGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

func isNilHealthDependency(value any) bool {
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

// gormRuntimeStarter adapts the scoped Workflow runtime to the frozen Scan
// SQL seam without opening a second transaction.
type gormRuntimeStarter struct {
	scoped workflowapplication.ScopedRuntimeStarter
}

func (starter gormRuntimeStarter) StartTx(ctx context.Context, transaction pgx.Tx, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	bridge, ok := transaction.(*healthGORMTx)
	if !ok || bridge.scope == nil || isNilHealthDependency(starter.scoped) {
		return workflowapplication.RuntimeStartResult{}, errors.New("health scoped workflow transaction is unavailable")
	}
	return starter.scoped.StartScoped(ctx, bridge.scope, request)
}

// gormEventAppender adapts the scoped Event store to legacy Health calls.
type gormEventAppender struct {
	scoped eventsapplication.ScopedAppender
}

func (appender gormEventAppender) AppendTx(ctx context.Context, transaction any, request domain.AppendRequest) (domain.ServerEvent, bool, error) {
	bridge, ok := transaction.(*healthGORMTx)
	if !ok || bridge.scope == nil || isNilHealthDependency(appender.scoped) {
		return domain.ServerEvent{}, false, errors.New("health scoped event transaction is unavailable")
	}
	return appender.scoped.AppendScoped(ctx, bridge.scope, request)
}

var _ DB = (*gormDB)(nil)
var _ IssueDB = (*gormDB)(nil)
var _ ReadDB = (*gormDB)(nil)
var _ scanDB = (*gormDB)(nil)
