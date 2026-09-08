package postgres

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

type rowScanner interface {
	Scan(...any) error
}

type sessionRecord struct {
	ID         string     `gorm:"column:id;type:uuid;primaryKey"`
	TokenHash  string     `gorm:"column:token_hash"`
	CSRFHash   string     `gorm:"column:csrf_hash"`
	UserLabel  string     `gorm:"column:user_label"`
	ScopesJSON string     `gorm:"column:scopes;type:jsonb"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	LastSeenAt time.Time  `gorm:"column:last_seen_at;autoUpdateTime:false"`
	ExpiresAt  time.Time  `gorm:"column:expires_at"`
	RevokedAt  *time.Time `gorm:"column:revoked_at"`
}

func (sessionRecord) TableName() string { return authSessionTable }

type apiTokenRecord struct {
	ID         string     `gorm:"column:id;type:uuid;primaryKey"`
	TokenHash  string     `gorm:"column:token_hash"`
	Name       string     `gorm:"column:name"`
	ScopesJSON string     `gorm:"column:scopes;type:jsonb"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime:false"`
	LastUsedAt *time.Time `gorm:"column:last_used_at;autoUpdateTime:false"`
	ExpiresAt  time.Time  `gorm:"column:expires_at"`
	RevokedAt  *time.Time `gorm:"column:revoked_at"`
}

func (apiTokenRecord) TableName() string { return authAPITokenTable }

func scanSession(row rowScanner) (domain.Session, error) {
	var record sessionRecord
	if err := row.Scan(&record.ID, &record.TokenHash, &record.CSRFHash, &record.UserLabel, &record.ScopesJSON,
		&record.CreatedAt, &record.LastSeenAt, &record.ExpiresAt, &record.RevokedAt); err != nil {
		return domain.Session{}, readError(err)
	}
	return record.toDomain()
}

func (record sessionRecord) toDomain() (domain.Session, error) {
	id, err := foundation.ParseID(record.ID)
	if err != nil || string(id) != record.ID {
		return domain.Session{}, corrupt(errors.New("session id is corrupt"))
	}
	scopes, err := decodeScopes(record.ScopesJSON)
	if err != nil {
		return domain.Session{}, corrupt(errors.New("session row is corrupt"))
	}
	session := domain.Session{
		ID: id, TokenHash: record.TokenHash, CSRFHash: record.CSRFHash, UserLabel: record.UserLabel,
		Scopes: scopes, CreatedAt: record.CreatedAt, LastSeenAt: record.LastSeenAt,
		ExpiresAt: record.ExpiresAt, RevokedAt: record.RevokedAt,
	}
	if domain.ValidateSession(session) != nil {
		return domain.Session{}, corrupt(errors.New("session row is corrupt"))
	}
	return session, nil
}

func scanAPIToken(row rowScanner) (domain.APIToken, error) {
	var record apiTokenRecord
	if err := row.Scan(&record.ID, &record.TokenHash, &record.Name, &record.ScopesJSON, &record.CreatedAt,
		&record.LastUsedAt, &record.ExpiresAt, &record.RevokedAt); err != nil {
		return domain.APIToken{}, readError(err)
	}
	return record.toDomain()
}

func (record apiTokenRecord) toDomain() (domain.APIToken, error) {
	id, err := foundation.ParseID(record.ID)
	if err != nil || string(id) != record.ID {
		return domain.APIToken{}, corrupt(errors.New("api token id is corrupt"))
	}
	scopes, err := decodeScopes(record.ScopesJSON)
	if err != nil {
		return domain.APIToken{}, corrupt(errors.New("api token row is corrupt"))
	}
	token := domain.APIToken{
		ID: id, TokenHash: record.TokenHash, Name: record.Name, Scopes: scopes,
		CreatedAt: record.CreatedAt, LastUsedAt: record.LastUsedAt,
		ExpiresAt: record.ExpiresAt, RevokedAt: record.RevokedAt,
	}
	if domain.ValidateAPIToken(token) != nil {
		return domain.APIToken{}, corrupt(errors.New("api token row is corrupt"))
	}
	return token, nil
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
