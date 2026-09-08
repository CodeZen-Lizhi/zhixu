package postgres

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sourceRefreshLockNamespace     = "zhixu:retrieval:source-refresh:v1:"
	sourceRefreshLockRetryInterval = 100 * time.Millisecond
)

type sourceRefreshConnectionPool interface {
	Acquire(context.Context) (*pgxpool.Conn, error)
}

type sourceRefreshLease struct {
	connection *pgxpool.Conn
	lockKey    string
	once       sync.Once
	err        error
}

func acquireSourceRefreshNative(ctx context.Context, pool sourceRefreshConnectionPool, workspaceID foundation.ID) (application.SourceRefreshLease, error) {
	parsed, err := foundation.ParseID(string(workspaceID))
	if err != nil || parsed != workspaceID {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "SOURCE_REFRESH_WORKSPACE_INVALID", false, errors.New("workspace identity is not canonical"))
	}
	if pool == nil {
		return nil, dependency("SOURCE_REFRESH_LOCK_CONNECTION_UNAVAILABLE", errors.New("database does not support dedicated source refresh connections"))
	}
	lockKey := sourceRefreshLockNamespace + string(workspaceID)
	for {
		connection, err := pool.Acquire(ctx)
		if err != nil {
			return nil, classify(err, "SOURCE_REFRESH_LOCK_CONNECTION_FAILED")
		}
		var acquired bool
		if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, lockKey).Scan(&acquired); err != nil {
			closePooledConnection(connection)
			return nil, classify(err, "SOURCE_REFRESH_LOCK_FAILED")
		}
		if acquired {
			return &sourceRefreshLease{connection: connection, lockKey: lockKey}, nil
		}
		connection.Release()
		if err := waitForSourceRefreshLockRetry(ctx); err != nil {
			return nil, classify(err, "SOURCE_REFRESH_LOCK_FAILED")
		}
	}
}

func waitForSourceRefreshLockRetry(ctx context.Context) error {
	retry := time.NewTimer(sourceRefreshLockRetryInterval)
	defer retry.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-retry.C:
		return nil
	}
}

// Release unlocks the source refresh session and is idempotent for defensive cleanup paths.
func (lease *sourceRefreshLease) Release(ctx context.Context) error {
	if lease == nil {
		return dependency("SOURCE_REFRESH_UNLOCK_FAILED", errors.New("source refresh lease is nil"))
	}
	lease.once.Do(func() {
		if lease.connection == nil || lease.lockKey == "" {
			closePooledConnection(lease.connection)
			lease.connection = nil
			lease.err = dependency("SOURCE_REFRESH_UNLOCK_FAILED", errors.New("source refresh lease is incomplete"))
			return
		}
		var unlocked bool
		err := lease.connection.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lease.lockKey).Scan(&unlocked)
		if err == nil && unlocked {
			lease.connection.Release()
			lease.connection = nil
			return
		}
		closePooledConnection(lease.connection)
		lease.connection = nil
		if err != nil {
			lease.err = classify(err, "SOURCE_REFRESH_UNLOCK_FAILED")
			return
		}
		lease.err = consistency("SOURCE_REFRESH_UNLOCK_INVALID", errors.New("source refresh advisory lock was not held by its lease session"))
	})
	return lease.err
}

func closePooledConnection(connection *pgxpool.Conn) {
	if connection == nil {
		return
	}
	raw := connection.Hijack()
	_ = raw.Close(context.Background())
}
