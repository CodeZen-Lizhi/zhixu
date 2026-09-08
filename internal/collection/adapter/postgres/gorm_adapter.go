package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	collectionapp "github.com/CodeZen-Lizhi/zhixu/internal/collection/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"gorm.io/gorm"
)

// collectionJSONB binds validated JSON as text instead of database/sql bytea;
// the explicit model column type or SQL cast owns its PostgreSQL jsonb type.
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
		return inconsistent(fmt.Errorf("collection JSONB source has unsupported type %T", source))
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return inconsistent(errors.New("persisted collection JSONB value is invalid"))
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
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return notFound(err)
	}
	return classify(err)
}

func gormTransaction(scope foundation.TransactionScope) (*gorm.DB, error) {
	database, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, classifyGORM(context.Background(), err)
	}
	return database, nil
}
