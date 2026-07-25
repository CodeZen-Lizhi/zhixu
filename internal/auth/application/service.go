// Package application 编排 Session、API Token 与 Capability 授权。
package application

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/auth/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// DefaultSessionTTL 是浏览器 Session 的默认有效期。
	DefaultSessionTTL = 12 * time.Hour
	// MaxSessionTTL 是浏览器 Session 可配置的最长有效期。
	MaxSessionTTL = 30 * 24 * time.Hour
	// DefaultAPITokenTTL 是自动化 Token 的默认有效期。
	DefaultAPITokenTTL = 30 * 24 * time.Hour
	// MaxAPITokenTTL 是自动化 Token 的最长有效期。
	MaxAPITokenTTL  = 365 * 24 * time.Hour
	credentialBytes = 32
)

const (
	// ErrorCodeUnauthorized 表示凭据缺失、失效或不匹配。
	ErrorCodeUnauthorized = "AUTH_UNAUTHORIZED"
	// ErrorCodeForbidden 表示身份缺少所需 Capability。
	ErrorCodeForbidden = "AUTH_CAPABILITY_DENIED"
	// ErrorCodeCSRF 表示 Cookie Session 的 Origin/CSRF 校验失败。
	ErrorCodeCSRF = "AUTH_CSRF_REJECTED"
	// ErrorCodeInvalid 表示认证命令输入无效。
	ErrorCodeInvalid = "AUTH_REQUEST_INVALID"
	// ErrorCodeUnavailable 表示认证持久层或随机源不可用。
	ErrorCodeUnavailable = "AUTH_DEPENDENCY_UNAVAILABLE"
	// ErrorCodeAPITokenNotFound 表示已认证调用方请求的 API Token 不存在。
	ErrorCodeAPITokenNotFound = "AUTH_API_TOKEN_NOT_FOUND"
)

// Repository 隐藏认证表的 PostgreSQL 实现与原子状态更新。
type Repository interface {
	CreateSession(context.Context, domain.SessionIssue, time.Duration) (domain.Session, error)
	RotateSession(context.Context, foundation.ID, domain.SessionIssue, time.Duration) (domain.Session, error)
	AuthenticateSession(context.Context, string) (domain.Session, error)
	RevokeSession(context.Context, foundation.ID) error
	CreateAPIToken(context.Context, domain.APITokenIssue, time.Duration) (domain.APIToken, error)
	ListAPITokens(context.Context, domain.APITokenListQuery) ([]domain.APIToken, bool, error)
	AuthenticateAPIToken(context.Context, string) (domain.APIToken, error)
	RevokeAPIToken(context.Context, foundation.ID) error
}

// Options 定义认证服务的配置和可替换依赖。
type Options struct {
	BootstrapToken string
	SessionTTL     time.Duration
	APITokenTTL    time.Duration
	Random         io.Reader
}

// Service 是单用户认证的应用入口。
type Service struct {
	repository    Repository
	ids           foundation.IDGenerator
	random        io.Reader
	bootstrapHash [sha256.Size]byte
	sessionTTL    time.Duration
	apiTokenTTL   time.Duration
}

// SessionCredential 只在创建时返回 Session 与 CSRF 明文。
type SessionCredential struct {
	Session   domain.Session
	Token     string
	CSRFToken string
}

// APITokenCredential 只在创建时返回 API Token 明文。
type APITokenCredential struct {
	Token domain.APIToken
	Plain string
}

// NewService 创建 fail-closed 的认证服务。
func NewService(repository Repository, ids foundation.IDGenerator, _ foundation.Clock, options Options) (*Service, error) {
	if repository == nil || ids == nil {
		return nil, unavailable(errors.New("authentication dependencies are incomplete"))
	}
	bootstrap := strings.TrimSpace(options.BootstrapToken)
	if len(bootstrap) < credentialBytes || bootstrap != options.BootstrapToken {
		return nil, invalid(errors.New("bootstrap token must contain at least 32 canonical characters"))
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = DefaultSessionTTL
	}
	if options.APITokenTTL == 0 {
		options.APITokenTTL = DefaultAPITokenTTL
	}
	if options.SessionTTL <= 0 || options.SessionTTL > MaxSessionTTL || options.APITokenTTL <= 0 || options.APITokenTTL > MaxAPITokenTTL {
		return nil, invalid(errors.New("authentication credential ttl is invalid"))
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &Service{
		repository: repository, ids: ids, random: options.Random,
		bootstrapHash: sha256.Sum256([]byte(bootstrap)), sessionTTL: options.SessionTTL, apiTokenTTL: options.APITokenTTL,
	}, nil
}

// ExchangeBootstrap 校验进程配置的单用户 Bootstrap Token 并签发 Cookie Session。
func (service *Service) ExchangeBootstrap(ctx context.Context, bootstrap string) (SessionCredential, error) {
	if err := service.ready(ctx); err != nil {
		return SessionCredential{}, err
	}
	provided := sha256.Sum256([]byte(bootstrap))
	if !hmac.Equal(provided[:], service.bootstrapHash[:]) {
		return SessionCredential{}, unauthorized(errors.New("bootstrap token does not match"))
	}
	scopes, err := domain.CanonicalScopes(capability.All())
	if err != nil {
		return SessionCredential{}, unavailable(err)
	}
	return service.issueSession(ctx, "owner", scopes, service.sessionTTL, service.repository.CreateSession)
}

// AuthenticateSession 校验 Cookie Session，并在状态修改请求中同时校验 CSRF 摘要。
func (service *Service) AuthenticateSession(ctx context.Context, token, csrf string, requireCSRF bool) (domain.Principal, error) {
	session, err := service.authenticateSession(ctx, token, csrf, requireCSRF)
	if err != nil {
		return domain.Principal{}, err
	}
	return principalFromSession(session)
}

// CurrentSession 校验 Cookie Session 并返回不含凭据摘要的当前 Session 元数据。
func (service *Service) CurrentSession(ctx context.Context, token, csrf string, requireCSRF bool) (domain.SessionInfo, error) {
	session, err := service.authenticateSession(ctx, token, csrf, requireCSRF)
	if err != nil {
		return domain.SessionInfo{}, err
	}
	return session.Info(), nil
}

// RotateSession 原子撤销当前 Session 并签发具有相同 Scope 的新 Session。
func (service *Service) RotateSession(ctx context.Context, principal domain.Principal, token, csrf string, requireCSRF bool) (SessionCredential, error) {
	if err := service.ready(ctx); err != nil {
		return SessionCredential{}, err
	}
	if err := domain.ValidatePrincipal(principal); err != nil || principal.Kind != domain.PrincipalSession {
		return SessionCredential{}, forbidden(errors.New("session rotation requires a browser session"))
	}
	current, err := service.authenticateSession(ctx, token, csrf, requireCSRF)
	if err != nil {
		return SessionCredential{}, err
	}
	if current.ID != principal.ID {
		return SessionCredential{}, forbidden(errors.New("session identity does not match request principal"))
	}
	credential, err := service.issueSession(ctx, current.UserLabel, current.Scopes, service.sessionTTL, func(_ context.Context, issue domain.SessionIssue, ttl time.Duration) (domain.Session, error) {
		return service.repository.RotateSession(ctx, current.ID, issue, ttl)
	})
	if err != nil {
		return SessionCredential{}, err
	}
	return credential, nil
}

func (service *Service) authenticateSession(ctx context.Context, token, csrf string, requireCSRF bool) (domain.Session, error) {
	if err := service.ready(ctx); err != nil {
		return domain.Session{}, err
	}
	if strings.TrimSpace(token) == "" || token != strings.TrimSpace(token) {
		return domain.Session{}, unauthorized(errors.New("session token is missing"))
	}
	session, err := service.repository.AuthenticateSession(ctx, hashCredential(token))
	if err != nil {
		return domain.Session{}, err
	}
	if err := domain.ValidateSession(session); err != nil {
		return domain.Session{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeUnavailable, false, err)
	}
	if session.RevokedAt != nil {
		return domain.Session{}, unauthorized(errors.New("session is expired or revoked"))
	}
	if requireCSRF && !hmac.Equal([]byte(hashCredential(csrf)), []byte(session.CSRFHash)) {
		return domain.Session{}, csrfRejected(errors.New("csrf token does not match"))
	}
	return session, nil
}

// CreateAPIToken 为已登录 Session 创建一次性返回明文的受限自动化 Token。
func (service *Service) CreateAPIToken(ctx context.Context, principal domain.Principal, name string, scopes []capability.Capability, ttl time.Duration) (APITokenCredential, error) {
	if err := service.ready(ctx); err != nil {
		return APITokenCredential{}, err
	}
	if err := domain.ValidatePrincipal(principal); err != nil || principal.Kind != domain.PrincipalSession {
		return APITokenCredential{}, forbidden(errors.New("api tokens can only be created from a browser session"))
	}
	canonical, err := domain.CanonicalScopes(scopes)
	if err != nil || strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) || len(name) > domain.MaxTokenNameBytes {
		return APITokenCredential{}, invalid(errors.New("api token name or scopes are invalid"))
	}
	for _, scope := range canonical {
		if !principal.Has(scope) {
			return APITokenCredential{}, forbidden(errors.New("api token scope exceeds session capabilities"))
		}
	}
	if ttl == 0 {
		ttl = service.apiTokenTTL
	}
	if ttl <= 0 || ttl > MaxAPITokenTTL {
		return APITokenCredential{}, invalid(errors.New("api token ttl is invalid"))
	}
	plain, tokenHash, err := service.newCredential()
	if err != nil {
		return APITokenCredential{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return APITokenCredential{}, err
	}
	issue := domain.APITokenIssue{ID: id, TokenHash: tokenHash, Name: name, Scopes: canonical}
	if err := domain.ValidateAPITokenIssue(issue); err != nil {
		return APITokenCredential{}, unavailable(err)
	}
	token, err := service.repository.CreateAPIToken(ctx, issue, ttl)
	if err != nil {
		return APITokenCredential{}, err
	}
	if err := validateIssuedAPIToken(issue, ttl, token); err != nil {
		return APITokenCredential{}, err
	}
	return APITokenCredential{Token: token, Plain: plain}, nil
}

// AuthenticateAPIToken 校验 Bearer Token、过期、撤销与 Capability。
func (service *Service) AuthenticateAPIToken(ctx context.Context, plain string) (domain.Principal, error) {
	if err := service.ready(ctx); err != nil {
		return domain.Principal{}, err
	}
	if strings.TrimSpace(plain) == "" || plain != strings.TrimSpace(plain) {
		return domain.Principal{}, unauthorized(errors.New("api token is missing"))
	}
	token, err := service.repository.AuthenticateAPIToken(ctx, hashCredential(plain))
	if err != nil {
		return domain.Principal{}, err
	}
	if err := domain.ValidateAPIToken(token); err != nil {
		return domain.Principal{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeUnavailable, false, err)
	}
	if token.RevokedAt != nil {
		return domain.Principal{}, unauthorized(errors.New("api token is expired or revoked"))
	}
	principal := domain.Principal{Kind: domain.PrincipalAPIToken, ID: token.ID, Scopes: append([]capability.Capability(nil), token.Scopes...)}
	if err := domain.ValidatePrincipal(principal); err != nil {
		return domain.Principal{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeUnavailable, false, err)
	}
	return principal, nil
}

// ListAPITokens 返回当前单用户所有 API Token 的安全元数据。
func (service *Service) ListAPITokens(ctx context.Context, principal domain.Principal, query domain.APITokenListQuery) (domain.APITokenListPage, error) {
	if err := service.ready(ctx); err != nil {
		return domain.APITokenListPage{}, err
	}
	if err := domain.ValidatePrincipal(principal); err != nil || principal.Kind != domain.PrincipalSession {
		return domain.APITokenListPage{}, forbidden(errors.New("api token listing requires a browser session"))
	}
	if err := domain.ValidateAPITokenListQuery(query); err != nil {
		return domain.APITokenListPage{}, invalid(err)
	}
	tokens, hasMore, err := service.repository.ListAPITokens(ctx, query)
	if err != nil {
		return domain.APITokenListPage{}, err
	}
	result := domain.APITokenListPage{Items: make([]domain.APITokenInfo, 0, len(tokens)), HasMore: hasMore}
	for _, token := range tokens {
		if err := domain.ValidateAPIToken(token); err != nil {
			return domain.APITokenListPage{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeUnavailable, false, err)
		}
		result.Items = append(result.Items, token.Info())
	}
	return result, nil
}

// Authorize 要求身份拥有指定 Capability。
func Authorize(principal domain.Principal, required capability.Capability) error {
	if domain.ValidatePrincipal(principal) != nil || capability.Validate(required) != nil || !principal.Has(required) {
		return forbidden(errors.New("required capability is missing"))
	}
	return nil
}

// RevokeSession 立即撤销指定 Session。
func (service *Service) RevokeSession(ctx context.Context, principal domain.Principal, sessionID foundation.ID) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if domain.ValidatePrincipal(principal) != nil || principal.Kind != domain.PrincipalSession || sessionID == "" || principal.ID != sessionID {
		return forbidden(errors.New("session revocation is not authorized"))
	}
	return service.repository.RevokeSession(ctx, sessionID)
}

// RevokeAPIToken 立即撤销指定自动化 Token。
func (service *Service) RevokeAPIToken(ctx context.Context, principal domain.Principal, tokenID foundation.ID) error {
	if err := service.ready(ctx); err != nil {
		return err
	}
	if domain.ValidatePrincipal(principal) != nil || principal.Kind != domain.PrincipalSession || tokenID == "" {
		return forbidden(errors.New("api token revocation is not authorized"))
	}
	return service.repository.RevokeAPIToken(ctx, tokenID)
}

func (service *Service) ready(ctx context.Context) error {
	if service == nil || service.repository == nil || service.ids == nil || service.random == nil || ctx == nil {
		return unavailable(errors.New("authentication service is unavailable"))
	}
	return nil
}

func (service *Service) newCredential() (string, string, error) {
	buffer := make([]byte, credentialBytes)
	if _, err := io.ReadFull(service.random, buffer); err != nil {
		return "", "", unavailable(err)
	}
	plain := base64.RawURLEncoding.EncodeToString(buffer)
	return plain, hashCredential(plain), nil
}

func (service *Service) issueSession(ctx context.Context, userLabel string, scopes []capability.Capability, ttl time.Duration, save func(context.Context, domain.SessionIssue, time.Duration) (domain.Session, error)) (SessionCredential, error) {
	token, tokenHash, err := service.newCredential()
	if err != nil {
		return SessionCredential{}, err
	}
	csrf, csrfHash, err := service.newCredential()
	if err != nil {
		return SessionCredential{}, err
	}
	id, err := service.ids.New()
	if err != nil {
		return SessionCredential{}, err
	}
	canonical, err := domain.CanonicalScopes(scopes)
	if err != nil || strings.TrimSpace(userLabel) == "" || userLabel != strings.TrimSpace(userLabel) || ttl <= 0 || ttl > MaxSessionTTL {
		return SessionCredential{}, invalid(errors.New("session identity, scope, or ttl is invalid"))
	}
	issue := domain.SessionIssue{ID: id, TokenHash: tokenHash, CSRFHash: csrfHash, UserLabel: userLabel, Scopes: canonical}
	if err := domain.ValidateSessionIssue(issue); err != nil {
		return SessionCredential{}, unavailable(err)
	}
	session, err := save(ctx, issue, ttl)
	if err != nil {
		return SessionCredential{}, err
	}
	if err := validateIssuedSession(issue, ttl, session); err != nil {
		return SessionCredential{}, err
	}
	return SessionCredential{Session: session, Token: token, CSRFToken: csrf}, nil
}

func validateIssuedSession(issue domain.SessionIssue, ttl time.Duration, session domain.Session) error {
	if err := domain.ValidateSession(session); err != nil || session.ID != issue.ID || session.TokenHash != issue.TokenHash ||
		session.CSRFHash != issue.CSRFHash || session.UserLabel != issue.UserLabel || !slices.Equal(session.Scopes, issue.Scopes) ||
		session.RevokedAt != nil || !session.LastSeenAt.Equal(session.CreatedAt) || session.ExpiresAt.Sub(session.CreatedAt) != ttl.Truncate(time.Microsecond) {
		if err == nil {
			err = errors.New("persisted session does not match the issue request")
		}
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeUnavailable, false, err)
	}
	return nil
}

func validateIssuedAPIToken(issue domain.APITokenIssue, ttl time.Duration, token domain.APIToken) error {
	if err := domain.ValidateAPIToken(token); err != nil || token.ID != issue.ID || token.TokenHash != issue.TokenHash || token.Name != issue.Name ||
		!slices.Equal(token.Scopes, issue.Scopes) || token.LastUsedAt != nil || token.RevokedAt != nil ||
		token.ExpiresAt.Sub(token.CreatedAt) != ttl.Truncate(time.Microsecond) {
		if err == nil {
			err = errors.New("persisted api token does not match the issue request")
		}
		return foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeUnavailable, false, err)
	}
	return nil
}

func principalFromSession(session domain.Session) (domain.Principal, error) {
	principal := domain.Principal{Kind: domain.PrincipalSession, ID: session.ID, Scopes: append([]capability.Capability(nil), session.Scopes...)}
	if err := domain.ValidatePrincipal(principal); err != nil {
		return domain.Principal{}, foundation.NewError(foundation.ErrorConsistencyViolation, ErrorCodeUnavailable, false, err)
	}
	return principal, nil
}

func hashCredential(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func invalid(err error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeInvalid, false, err)
}

func unauthorized(err error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, ErrorCodeUnauthorized, false, err)
}

func forbidden(err error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, ErrorCodeForbidden, false, err)
}

func csrfRejected(err error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, ErrorCodeCSRF, false, err)
}

func unavailable(err error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeUnavailable, true, err)
}
