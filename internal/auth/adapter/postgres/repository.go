// Package postgres 持久化 Session 与 API Token 摘要。
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// DB 是认证 Repository 所需的最小 PostgreSQL 边界。
type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Repository 使用原子 UPDATE ... RETURNING 完成凭据校验与最后使用时间更新。
type Repository struct{ db DB }

// NewRepository 创建认证 PostgreSQL Repository。
func NewRepository(db DB) (*Repository, error) {
	if nilDB(db) {
		return nil, unavailable(errors.New("authentication database is nil"))
	}
	return &Repository{db: db}, nil
}

// Check 验证认证表可被当前数据库身份读取，供进程就绪探针 fail-closed 使用。
func (repository *Repository) Check(ctx context.Context) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	var sessionsExist, tokensExist bool
	if err := repository.db.QueryRow(ctx, authCheckSQL).Scan(&sessionsExist, &tokensExist); err != nil {
		return unavailable(fmt.Errorf("check authentication tables: %w", err))
	}
	return nil
}

// CreateSession 使用 PostgreSQL 当前时间签发 Session，并返回数据库持久化事实。
func (repository *Repository) CreateSession(ctx context.Context, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Session{}, err
	}
	if err := domain.ValidateSessionIssue(issue); err != nil || ttl <= 0 || ttl.Microseconds() <= 0 {
		if err == nil {
			err = errors.New("session ttl is invalid")
		}
		return domain.Session{}, invalid(err)
	}
	scopes, err := json.Marshal(issue.Scopes)
	if err != nil {
		return domain.Session{}, invalid(err)
	}
	return scanSession(repository.db.QueryRow(ctx, createSessionSQL,
		string(issue.ID), issue.TokenHash, issue.CSRFHash, issue.UserLabel, scopes, ttl.Microseconds()))
}

// RotateSession 使用同一 PostgreSQL 时间原子撤销旧 Session 并签发新 Session。
func (repository *Repository) RotateSession(ctx context.Context, previousID foundation.ID, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Session{}, err
	}
	if err := domain.ValidateSessionIssue(issue); err != nil || ttl <= 0 || ttl.Microseconds() <= 0 {
		if err == nil {
			err = errors.New("session ttl is invalid")
		}
		return domain.Session{}, invalid(err)
	}
	if _, err := foundation.ParseID(string(previousID)); err != nil || previousID == issue.ID {
		return domain.Session{}, invalid(errors.New("session rotation identity is invalid"))
	}
	scopes, err := json.Marshal(issue.Scopes)
	if err != nil {
		return domain.Session{}, invalid(err)
	}
	return scanSession(repository.db.QueryRow(ctx, rotateSessionSQL,
		string(previousID), string(issue.ID), issue.TokenHash, issue.CSRFHash, issue.UserLabel, scopes, ttl.Microseconds()))
}

// AuthenticateSession 原子校验未过期未撤销的摘要并更新 last_seen_at。
func (repository *Repository) AuthenticateSession(ctx context.Context, tokenHash string) (domain.Session, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Session{}, err
	}
	return scanSession(repository.db.QueryRow(ctx, authenticateSessionSQL, tokenHash))
}

// RevokeSession 立即撤销活动 Session；重复撤销是幂等成功。
func (repository *Repository) RevokeSession(ctx context.Context, id foundation.ID) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if _, err := foundation.ParseID(string(id)); err != nil {
		return invalid(errors.New("session revocation identity is invalid"))
	}
	tag, err := repository.db.Exec(ctx, `UPDATE auth.session
		SET revoked_at=COALESCE(revoked_at,CURRENT_TIMESTAMP)
		WHERE id=$1`, string(id))
	if err != nil {
		return unavailable(fmt.Errorf("revoke session: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return unauthorized(errors.New("session does not exist"))
	}
	return nil
}

// CreateAPIToken 使用 PostgreSQL 当前时间签发 API Token，并返回数据库持久化事实。
func (repository *Repository) CreateAPIToken(ctx context.Context, issue domain.APITokenIssue, ttl time.Duration) (domain.APIToken, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.APIToken{}, err
	}
	if err := domain.ValidateAPITokenIssue(issue); err != nil || ttl <= 0 || ttl.Microseconds() <= 0 {
		if err == nil {
			err = errors.New("api token ttl is invalid")
		}
		return domain.APIToken{}, invalid(err)
	}
	scopes, err := json.Marshal(issue.Scopes)
	if err != nil {
		return domain.APIToken{}, invalid(err)
	}
	return scanAPIToken(repository.db.QueryRow(ctx, createAPITokenSQL,
		string(issue.ID), issue.TokenHash, issue.Name, scopes, ttl.Microseconds()))
}

// ListAPITokens 返回按创建时间与 ID 倒序排列的有界 API Token 持久化事实页。
func (repository *Repository) ListAPITokens(ctx context.Context, request domain.APITokenListQuery) ([]domain.APIToken, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, false, err
	}
	if err := domain.ValidateAPITokenListQuery(request); err != nil {
		return nil, false, invalid(err)
	}
	query := `SELECT ` + apiTokenListProjection + ` FROM auth.api_token AS token`
	args := make([]any, 0, 3)
	if request.CursorTime != nil {
		query += ` WHERE (token.created_at,token.id)<($1,$2::uuid)`
		args = append(args, request.CursorTime.UTC(), string(request.CursorID))
	}
	args = append(args, request.Limit+1)
	query += fmt.Sprintf(` ORDER BY token.created_at DESC,token.id DESC LIMIT $%d`, len(args))
	rows, err := repository.db.Query(ctx, query, args...)
	if err != nil {
		return nil, false, unavailable(fmt.Errorf("list api tokens: %w", err))
	}
	defer rows.Close()
	result := make([]domain.APIToken, 0, request.Limit+1)
	for rows.Next() {
		token, scanErr := scanAPIToken(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		result = append(result, token)
	}
	if err := rows.Err(); err != nil {
		return nil, false, unavailable(fmt.Errorf("iterate api tokens: %w", err))
	}
	hasMore := len(result) > request.Limit
	if hasMore {
		result = result[:request.Limit]
	}
	return result, hasMore, nil
}

// AuthenticateAPIToken 原子校验未过期未撤销的摘要并更新 last_used_at。
func (repository *Repository) AuthenticateAPIToken(ctx context.Context, tokenHash string) (domain.APIToken, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.APIToken{}, err
	}
	return scanAPIToken(repository.db.QueryRow(ctx, authenticateAPITokenSQL, tokenHash))
}

// RevokeAPIToken 立即撤销自动化 Token；重复撤销是幂等成功。
func (repository *Repository) RevokeAPIToken(ctx context.Context, id foundation.ID) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if _, err := foundation.ParseID(string(id)); err != nil {
		return invalid(errors.New("api token revocation identity is invalid"))
	}
	tag, err := repository.db.Exec(ctx, `UPDATE auth.api_token
		SET revoked_at=COALESCE(revoked_at,CURRENT_TIMESTAMP)
		WHERE id=$1`, string(id))
	if err != nil {
		return unavailable(fmt.Errorf("revoke api token: %w", err))
	}
	if tag.RowsAffected() == 0 {
		return notFound(errors.New("api token does not exist"))
	}
	return nil
}

func decodeScopes(raw string) ([]capability.Capability, error) {
	var values []capability.Capability
	if json.Unmarshal([]byte(raw), &values) != nil {
		return nil, errors.New("scope json is invalid")
	}
	canonical, err := domain.CanonicalScopes(values)
	if err != nil || !slices.Equal(canonical, values) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("scope json is not canonical")
	}
	return canonical, nil
}

func (repository *Repository) ready(ctx context.Context) error {
	if repository == nil || nilDB(repository.db) || ctx == nil {
		return unavailable(errors.New("authentication repository is unavailable"))
	}
	return nil
}

func readError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return unauthorized(errors.New("credential is missing, expired, or revoked"))
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return writeError(err)
	}
	return unavailable(fmt.Errorf("read authentication credential: %w", err))
}

func writeError(err error) error {
	if err == nil {
		return nil
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return foundation.NewError(foundation.ErrorVersionConflict, "AUTH_CREDENTIAL_CONFLICT", false, errors.New("credential identity already exists"))
	}
	return unavailable(fmt.Errorf("write authentication credential: %w", err))
}

func invalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, application.ErrorCodeInvalid, false, err)
}

func unauthorized(err error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, application.ErrorCodeUnauthorized, false, err)
}

func notFound(err error) error {
	return foundation.NewError(foundation.ErrorNotFound, application.ErrorCodeAPITokenNotFound, false, err)
}

func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, application.ErrorCodeUnavailable, true, err)
}

func corrupt(err error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, application.ErrorCodeUnavailable, false, err)
}

func nilDB(db DB) bool {
	if db == nil {
		return true
	}
	value := reflect.ValueOf(db)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ application.Repository = (*Repository)(nil)
