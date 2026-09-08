package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

// GORMRepository persists the Auth contract through the shared GORM root.
type GORMRepository struct {
	database *gorm.DB
}

// NewGORMRepository constructs an Auth Repository from the shared platform
// GORM root.
func NewGORMRepository(database *gorm.DB) (*GORMRepository, error) {
	if !validGORMDatabase(database) {
		return nil, unavailable(errors.New("authentication GORM database is unavailable"))
	}
	return &GORMRepository{database: database}, nil
}

// Check verifies that both authentication tables are readable.
func (repository *GORMRepository) Check(ctx context.Context) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	var sessionsExist, tokensExist bool
	if err := repository.database.WithContext(ctx).Raw(authCheckSQL).Row().Scan(&sessionsExist, &tokensExist); err != nil {
		return unavailable(fmt.Errorf("check authentication tables: %w", err))
	}
	return nil
}

// CreateSession persists a Session using PostgreSQL time and returns the stored
// row.
func (repository *GORMRepository) CreateSession(ctx context.Context, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
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
	return scanSession(repository.database.WithContext(ctx).Raw(gormCreateSessionSQL,
		string(issue.ID), issue.TokenHash, issue.CSRFHash, issue.UserLabel, string(scopes), ttl.Microseconds()).Row())
}

// RotateSession atomically revokes the active previous Session and inserts the
// replacement in one PostgreSQL statement.
func (repository *GORMRepository) RotateSession(ctx context.Context, previousID foundation.ID, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
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
	return scanSession(repository.database.WithContext(ctx).Raw(gormRotateSessionSQL,
		string(previousID), string(issue.ID), issue.TokenHash, issue.CSRFHash, issue.UserLabel, string(scopes), ttl.Microseconds()).Row())
}

// AuthenticateSession atomically validates an active digest and advances its
// database-owned last-seen time.
func (repository *GORMRepository) AuthenticateSession(ctx context.Context, tokenHash string) (domain.Session, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.Session{}, err
	}
	return scanSession(repository.database.WithContext(ctx).Raw(gormAuthenticateSessionSQL, tokenHash).Row())
}

// RevokeSession immediately revokes a Session; repeated revocation remains an
// idempotent success.
func (repository *GORMRepository) RevokeSession(ctx context.Context, id foundation.ID) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if _, err := foundation.ParseID(string(id)); err != nil {
		return invalid(errors.New("session revocation identity is invalid"))
	}
	result := repository.database.WithContext(ctx).
		Table(authSessionTable).
		Where("id = ?", string(id)).
		UpdateColumn("revoked_at", gorm.Expr("COALESCE(revoked_at,CURRENT_TIMESTAMP)"))
	if result.Error != nil {
		return unavailable(fmt.Errorf("revoke session: %w", result.Error))
	}
	if result.RowsAffected == 0 {
		return unauthorized(errors.New("session does not exist"))
	}
	return nil
}

// CreateAPIToken persists an API Token using PostgreSQL time and returns the
// stored row.
func (repository *GORMRepository) CreateAPIToken(ctx context.Context, issue domain.APITokenIssue, ttl time.Duration) (domain.APIToken, error) {
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
	return scanAPIToken(repository.database.WithContext(ctx).Raw(gormCreateAPITokenSQL,
		string(issue.ID), issue.TokenHash, issue.Name, string(scopes), ttl.Microseconds()).Row())
}

// ListAPITokens returns a bounded keyset page ordered by database creation time
// and UUID.
func (repository *GORMRepository) ListAPITokens(ctx context.Context, request domain.APITokenListQuery) ([]domain.APIToken, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, false, err
	}
	if err := domain.ValidateAPITokenListQuery(request); err != nil {
		return nil, false, invalid(err)
	}
	query := repository.database.WithContext(ctx).
		Table(authAPITokenTable + " AS token").
		Select(apiTokenListProjection)
	if request.CursorTime != nil {
		query = query.Where("(token.created_at,token.id) < (?,?::uuid)", request.CursorTime.UTC(), string(request.CursorID))
	}
	rows, err := query.
		Order("token.created_at DESC,token.id DESC").
		Limit(request.Limit + 1).
		Rows()
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

// AuthenticateAPIToken atomically validates an active digest and advances its
// database-owned last-used time.
func (repository *GORMRepository) AuthenticateAPIToken(ctx context.Context, tokenHash string) (domain.APIToken, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.APIToken{}, err
	}
	return scanAPIToken(repository.database.WithContext(ctx).Raw(gormAuthenticateAPITokenSQL, tokenHash).Row())
}

// RevokeAPIToken immediately revokes an API Token; repeated revocation remains
// an idempotent success.
func (repository *GORMRepository) RevokeAPIToken(ctx context.Context, id foundation.ID) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if _, err := foundation.ParseID(string(id)); err != nil {
		return invalid(errors.New("api token revocation identity is invalid"))
	}
	result := repository.database.WithContext(ctx).
		Table(authAPITokenTable).
		Where("id = ?", string(id)).
		UpdateColumn("revoked_at", gorm.Expr("COALESCE(revoked_at,CURRENT_TIMESTAMP)"))
	if result.Error != nil {
		return unavailable(fmt.Errorf("revoke api token: %w", result.Error))
	}
	if result.RowsAffected == 0 {
		return notFound(errors.New("api token does not exist"))
	}
	return nil
}

func (repository *GORMRepository) ready(ctx context.Context) error {
	if repository == nil || !validGORMDatabase(repository.database) || ctx == nil {
		return unavailable(errors.New("authentication GORM repository is unavailable"))
	}
	return nil
}

func validGORMDatabase(database *gorm.DB) bool {
	return database != nil && database.Config != nil && database.Statement != nil &&
		database.Config.ConnPool != nil && database.Statement.ConnPool != nil && database.Error == nil
}

var _ application.Repository = (*GORMRepository)(nil)
