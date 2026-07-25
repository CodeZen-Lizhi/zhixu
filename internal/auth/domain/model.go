// Package domain 定义单用户认证、Session 与 API Token 的稳定领域契约。
package domain

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// TokenHashBytes 是 SHA-256 十六进制摘要的固定长度。
	TokenHashBytes = 64
	// MaxTokenNameBytes 限制 API Token 的显示名称。
	MaxTokenNameBytes = 256
	// DefaultAPITokenListLimit 是 API Token 元数据列表的默认页大小。
	DefaultAPITokenListLimit = 30
	// MaxAPITokenListLimit 是 API Token 元数据列表允许的最大页大小。
	MaxAPITokenListLimit = 100
)

// PrincipalKind 区分浏览器 Session 与自动化 API Token。
type PrincipalKind string

const (
	// PrincipalSession 表示 Cookie Session 身份。
	PrincipalSession PrincipalKind = "SESSION"
	// PrincipalAPIToken 表示 Bearer API Token 身份。
	PrincipalAPIToken PrincipalKind = "API_TOKEN"
)

// Principal 是认证成功后写入请求 Context 的最小身份事实。
type Principal struct {
	Kind   PrincipalKind
	ID     foundation.ID
	Scopes []capability.Capability
}

// Has 判断身份是否拥有指定普通 Capability。
func (p Principal) Has(required capability.Capability) bool {
	for _, value := range p.Scopes {
		if value == required {
			return true
		}
	}
	return false
}

// Session 是只保存摘要的浏览器会话事实。
type Session struct {
	ID         foundation.ID
	TokenHash  string
	CSRFHash   string
	UserLabel  string
	Scopes     []capability.Capability
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// SessionIssue 描述由应用层生成、由数据库补齐生命周期时间的 Session 签发请求。
// CreatedAt、LastSeenAt、ExpiresAt 和 RevokedAt 必须由持久化层使用数据库时钟写入。
type SessionIssue struct {
	ID        foundation.ID
	TokenHash string
	CSRFHash  string
	UserLabel string
	Scopes    []capability.Capability
}

// APITokenIssue 描述由应用层生成、由数据库补齐生命周期时间的 API Token 签发请求。
// 生命周期时间不得由调用方时钟写入数据库。
type APITokenIssue struct {
	ID        foundation.ID
	TokenHash string
	Name      string
	Scopes    []capability.Capability
}

// SessionInfo 是可以安全返回给客户端的 Session 元数据，不包含任何凭据摘要。
type SessionInfo struct {
	ID         foundation.ID           `json:"id"`
	UserLabel  string                  `json:"user_label"`
	Scopes     []capability.Capability `json:"scopes"`
	CreatedAt  time.Time               `json:"created_at"`
	LastSeenAt time.Time               `json:"last_seen_at"`
	ExpiresAt  time.Time               `json:"expires_at"`
	RevokedAt  *time.Time              `json:"revoked_at,omitempty"`
}

// APIToken 是只保存摘要的自动化凭据事实。
type APIToken struct {
	ID         foundation.ID
	TokenHash  string
	Name       string
	Scopes     []capability.Capability
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// APITokenInfo 是可以安全返回给客户端的 API Token 元数据，不包含 Token 摘要。
type APITokenInfo struct {
	ID         foundation.ID           `json:"id"`
	Name       string                  `json:"name"`
	Scopes     []capability.Capability `json:"scopes"`
	CreatedAt  time.Time               `json:"created_at"`
	LastUsedAt *time.Time              `json:"last_used_at,omitempty"`
	ExpiresAt  time.Time               `json:"expires_at"`
	RevokedAt  *time.Time              `json:"revoked_at,omitempty"`
}

// APITokenListQuery 描述稳定 keyset 分页边界；CursorTime 与 CursorID 必须同时存在或同时为空。
type APITokenListQuery struct {
	CursorTime *time.Time
	CursorID   foundation.ID
	Limit      int
}

// APITokenListPage 返回安全元数据和是否存在下一页。
type APITokenListPage struct {
	Items   []APITokenInfo
	HasMore bool
}

// Info 返回不含凭据字段的 Session 元数据副本。
func (session Session) Info() SessionInfo {
	return SessionInfo{
		ID: session.ID, UserLabel: session.UserLabel, Scopes: append([]capability.Capability(nil), session.Scopes...),
		CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, ExpiresAt: session.ExpiresAt,
		RevokedAt: cloneTime(session.RevokedAt),
	}
}

// Info 返回不含 Token 摘要的 API Token 元数据副本。
func (token APIToken) Info() APITokenInfo {
	return APITokenInfo{
		ID: token.ID, Name: token.Name, Scopes: append([]capability.Capability(nil), token.Scopes...),
		CreatedAt: token.CreatedAt, LastUsedAt: cloneTime(token.LastUsedAt), ExpiresAt: token.ExpiresAt,
		RevokedAt: cloneTime(token.RevokedAt),
	}
}

// ValidatePrincipal 校验身份与 Capability 集合均为 canonical。
func ValidatePrincipal(principal Principal) error {
	if principal.Kind != PrincipalSession && principal.Kind != PrincipalAPIToken {
		return errors.New("principal kind is invalid")
	}
	if !validID(principal.ID) || validateScopes(principal.Scopes) != nil {
		return errors.New("principal identity or scopes are invalid")
	}
	return nil
}

// ValidateSession 校验持久化 Session 的摘要、时序和撤销状态。
func ValidateSession(session Session) error {
	if !validID(session.ID) || !validHash(session.TokenHash) || !validHash(session.CSRFHash) || session.TokenHash == session.CSRFHash ||
		strings.TrimSpace(session.UserLabel) == "" || session.UserLabel != strings.TrimSpace(session.UserLabel) ||
		validateScopes(session.Scopes) != nil || session.CreatedAt.IsZero() || session.LastSeenAt.Before(session.CreatedAt) ||
		!session.ExpiresAt.After(session.CreatedAt) {
		return errors.New("session is invalid")
	}
	if session.RevokedAt != nil && session.RevokedAt.Before(session.CreatedAt) {
		return errors.New("session revocation time is invalid")
	}
	return nil
}

// ValidateSessionIssue 校验 Session 签发请求中不依赖时钟的字段。
func ValidateSessionIssue(issue SessionIssue) error {
	if !validID(issue.ID) || !validHash(issue.TokenHash) || !validHash(issue.CSRFHash) || issue.TokenHash == issue.CSRFHash ||
		strings.TrimSpace(issue.UserLabel) == "" || issue.UserLabel != strings.TrimSpace(issue.UserLabel) || validateScopes(issue.Scopes) != nil {
		return errors.New("session issue is invalid")
	}
	return nil
}

// ValidateAPIToken 校验持久化 API Token 的摘要、Scope 和生命周期。
func ValidateAPIToken(token APIToken) error {
	if !validID(token.ID) || !validHash(token.TokenHash) || token.Name != strings.TrimSpace(token.Name) || token.Name == "" ||
		len(token.Name) > MaxTokenNameBytes || validateScopes(token.Scopes) != nil || token.CreatedAt.IsZero() ||
		!token.ExpiresAt.After(token.CreatedAt) {
		return errors.New("api token is invalid")
	}
	if token.LastUsedAt != nil && token.LastUsedAt.Before(token.CreatedAt) {
		return errors.New("api token last use time is invalid")
	}
	if token.RevokedAt != nil && token.RevokedAt.Before(token.CreatedAt) {
		return errors.New("api token revocation time is invalid")
	}
	return nil
}

// ValidateAPITokenIssue 校验 API Token 签发请求中不依赖时钟的字段。
func ValidateAPITokenIssue(issue APITokenIssue) error {
	if !validID(issue.ID) || !validHash(issue.TokenHash) || issue.Name != strings.TrimSpace(issue.Name) || issue.Name == "" ||
		len(issue.Name) > MaxTokenNameBytes || validateScopes(issue.Scopes) != nil {
		return errors.New("api token issue is invalid")
	}
	return nil
}

// ValidateAPITokenListQuery 校验页大小与完整 keyset 边界。
func ValidateAPITokenListQuery(query APITokenListQuery) error {
	if query.Limit < 1 || query.Limit > MaxAPITokenListLimit {
		return errors.New("api token list limit is invalid")
	}
	if query.CursorTime == nil {
		if query.CursorID != "" {
			return errors.New("api token list cursor is incomplete")
		}
		return nil
	}
	if query.CursorTime.IsZero() || !validID(query.CursorID) {
		return errors.New("api token list cursor is invalid")
	}
	return nil
}

// CanonicalScopes 返回去重排序后的 Capability 副本。
func CanonicalScopes(values []capability.Capability) ([]capability.Capability, error) {
	if len(values) == 0 || len(values) > len(capability.All()) {
		return nil, errors.New("scope count is invalid")
	}
	result := append([]capability.Capability(nil), values...)
	slices.Sort(result)
	for index, value := range result {
		if capability.Validate(value) != nil || (index > 0 && result[index-1] == value) {
			return nil, errors.New("scopes are invalid")
		}
	}
	return result, nil
}

func validateScopes(values []capability.Capability) error {
	canonical, err := CanonicalScopes(values)
	if err != nil || !slices.Equal(canonical, values) {
		return errors.New("scopes are not canonical")
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func validHash(value string) bool {
	if len(value) != TokenHashBytes {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
