// Package gitoperation serializes Workspace-wide Git and worktree mutations.
package gitoperation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ErrorCodeBusy 表示同一 Workspace 的另一项 Git 操作仍在执行。
	ErrorCodeBusy = "GIT_OPERATION_BUSY"
	// ErrorCodeUnavailable 表示跨进程 Git 操作锁不可用。
	ErrorCodeUnavailable = "GIT_OPERATION_LOCK_UNAVAILABLE"
	// ErrorCodeReleaseFailed 表示锁连接未能被可靠释放。
	ErrorCodeReleaseFailed = "GIT_OPERATION_LOCK_RELEASE_FAILED"

	lockNamespace = "zhixu:git-operation:v1:"
	retryInterval = 100 * time.Millisecond
	// 独立锁会话不占用业务池，但仍必须受进程级数据库连接预算约束。
	maxDedicatedLockSessions = 4
)

var dedicatedLockSessions = make(chan struct{}, maxDedicatedLockSessions)

// Lease 持有一个 Workspace 的跨进程 Git 操作所有权。
type Lease interface {
	Release(context.Context) error
}

// WorkspaceLocker 在整个多步骤 Git 操作期间串行化同一 Workspace。
type WorkspaceLocker interface {
	Acquire(context.Context, foundation.ID) (Lease, error)
}

// PostgresLocker 通过专用 PostgreSQL 会话持有 advisory lock。
type PostgresLocker struct {
	connectionConfig *pgx.ConnConfig
}

type postgresLease struct {
	connection  *pgx.Conn
	key         string
	sessionHeld bool
	once        sync.Once
	err         error
}

// NewPostgresLocker 创建不主动占用连接的 Workspace Git 操作锁。
func NewPostgresLocker(pool *pgxpool.Pool) (*PostgresLocker, error) {
	if pool == nil {
		return nil, dependency(ErrorCodeUnavailable, errors.New("database pool is nil"))
	}
	poolConfig := pool.Config()
	if poolConfig == nil || poolConfig.ConnConfig == nil {
		return nil, dependency(ErrorCodeUnavailable, errors.New("database connection config is unavailable"))
	}
	return &PostgresLocker{
		connectionConfig: poolConfig.ConnConfig.Copy(),
	}, nil
}

// Acquire 使用业务连接池之外的独立数据库会话，直到返回的 Lease 被释放。
func (locker *PostgresLocker) Acquire(ctx context.Context, workspaceID foundation.ID) (Lease, error) {
	if ctx == nil {
		return nil, invalid(errors.New("Git operation context is nil"))
	}
	parsed, err := foundation.ParseID(string(workspaceID))
	if err != nil || parsed != workspaceID {
		return nil, invalid(errors.New("Workspace identity is invalid"))
	}
	if locker == nil || locker.connectionConfig == nil {
		return nil, dependency(ErrorCodeUnavailable, errors.New("Git operation locker is unavailable"))
	}
	select {
	case dedicatedLockSessions <- struct{}{}:
	case <-ctx.Done():
		return nil, foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeBusy, true, ctx.Err())
	}
	key := lockNamespace + string(workspaceID)
	connection, err := pgx.ConnectConfig(ctx, locker.connectionConfig.Copy())
	if err != nil {
		releaseDedicatedSession()
		return nil, classifyAcquire(err)
	}
	for {
		var acquired bool
		if err := connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&acquired); err != nil {
			closeConnection(connection)
			releaseDedicatedSession()
			return nil, classifyAcquire(err)
		}
		if acquired {
			return &postgresLease{connection: connection, key: key, sessionHeld: true}, nil
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			closeConnection(connection)
			releaseDedicatedSession()
			return nil, foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeBusy, true, ctx.Err())
		case <-timer.C:
		}
	}
}

// Release 解锁并归还专用连接；重复调用返回第一次结果。
func (lease *postgresLease) Release(ctx context.Context) error {
	if lease == nil {
		return dependency(ErrorCodeReleaseFailed, errors.New("Git operation lease is nil"))
	}
	lease.once.Do(func() {
		defer func() {
			if lease.sessionHeld {
				releaseDedicatedSession()
				lease.sessionHeld = false
			}
		}()
		if ctx == nil || lease.connection == nil || lease.key == "" {
			closeConnection(lease.connection)
			lease.connection = nil
			lease.err = dependency(ErrorCodeReleaseFailed, errors.New("Git operation lease is incomplete"))
			return
		}
		var unlocked bool
		err := lease.connection.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lease.key).Scan(&unlocked)
		if err == nil && unlocked {
			if closeErr := lease.connection.Close(ctx); closeErr != nil {
				closeConnection(lease.connection)
				lease.err = dependency(ErrorCodeReleaseFailed, closeErr)
			}
			lease.connection = nil
			return
		}
		closeConnection(lease.connection)
		lease.connection = nil
		if err != nil {
			lease.err = dependency(ErrorCodeReleaseFailed, err)
			return
		}
		lease.err = foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeReleaseFailed, false, errors.New("Git operation advisory lock was not held"))
	})
	return lease.err
}

func releaseDedicatedSession() {
	<-dedicatedLockSessions
}

func closeConnection(connection *pgx.Conn) {
	if connection == nil {
		return
	}
	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = connection.Close(closeContext)
}

func classifyAcquire(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return foundation.NewError(foundation.ErrorRetryableFailure, ErrorCodeBusy, true, err)
	}
	return dependency(ErrorCodeUnavailable, err)
}

func invalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, "GIT_OPERATION_LOCK_INVALID", false, err)
}

func dependency(code string, err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, code, true, err)
}
