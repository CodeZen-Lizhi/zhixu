package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	healthapp "github.com/CodeZen-Lizhi/zhixu/internal/health/application"
)

// healthRow、healthRows 只保留 Health SQL 实际使用的读取能力。
type healthRow interface{ Scan(...any) error }

type healthRows interface {
	healthRow
	Next() bool
	Err() error
	Close()
}

type healthQuery interface {
	Query(context.Context, string, ...any) (healthRows, error)
}

type healthReadDB interface {
	healthQuery
	QueryRow(context.Context, string, ...any) healthRow
}

type healthResult int64

func (result healthResult) RowsAffected() int64 { return int64(result) }

type healthSQL interface {
	healthReadDB
	Exec(context.Context, string, ...any) (healthResult, error)
}

// healthTransaction 不拥有提交能力；跨 owner 写入只能使用调用方的同一 scope。
type healthTransaction struct {
	healthSQL
	scope foundation.TransactionScope
}

type healthTransactor interface {
	Within(context.Context, foundation.TransactionOptions, func(context.Context, healthTransaction) error) error
}

type healthIssueDB interface {
	healthReadDB
	healthTransactor
}

type healthStore interface {
	healthSQL
	healthTransactor
}

type scopedHealthBindingVerifier interface {
	VerifyBindingScoped(context.Context, foundation.TransactionScope, healthapp.SmartCollectionBinding) error
}

type healthTransactionPhase uint8

const (
	healthTransactionBegin healthTransactionPhase = iota
	healthTransactionWork
	healthTransactionCommit
)

// healthTransactionFailure 区分执行失败与提交响应丢失，防止业务失败触发提交回查。
type healthTransactionFailure struct {
	phase healthTransactionPhase
	cause error
}

func (failure *healthTransactionFailure) Error() string { return failure.cause.Error() }
func (failure *healthTransactionFailure) Unwrap() error { return failure.cause }

func withHealthTransaction[T any](ctx context.Context, database healthTransactor, options foundation.TransactionOptions, work func(context.Context, healthTransaction) (T, error)) (T, error) {
	var result T
	phase := healthTransactionBegin
	err := database.Within(ctx, options, func(callbackCtx context.Context, transaction healthTransaction) error {
		phase = healthTransactionWork
		var err error
		result, err = work(callbackCtx, transaction)
		if err == nil {
			phase = healthTransactionCommit
		}
		return err
	})
	if err != nil {
		return result, &healthTransactionFailure{phase: phase, cause: healthContextError(ctx, err)}
	}
	return result, nil
}

func healthCommitFailed(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var failure *healthTransactionFailure
	return errors.As(err, &failure) && failure.phase == healthTransactionCommit
}

func classifyHealthTransactionError(err error, beginCode, commitCode string) error {
	var failure *healthTransactionFailure
	if errors.As(err, &failure) {
		switch failure.phase {
		case healthTransactionBegin:
			return classifyScanError(err, beginCode)
		case healthTransactionCommit:
			return classifyScanError(err, commitCode)
		}
	}
	return err
}

func healthContextError(ctx context.Context, err error) error {
	if err == nil || ctx == nil || ctx.Err() == nil {
		return err
	}
	if errors.Is(err, sql.ErrTxDone) && !errors.Is(err, ctx.Err()) {
		err = errors.Join(err, ctx.Err())
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(err, cause) && errors.Is(err, ctx.Err()) {
		return errors.Join(err, cause)
	}
	return err
}
