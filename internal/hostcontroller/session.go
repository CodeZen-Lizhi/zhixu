package hostcontroller

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	ControlSessionCookie = "zhixu_control_session"
	ControlCSRFHeader    = "X-Zhixu-Control-CSRF-Token"
)

// SessionCredential is returned after one-time bootstrap exchange and session recovery.
type SessionCredential struct {
	ControllerInstanceID string    `json:"controller_instance_id"`
	SessionID            string    `json:"session_id"`
	CSRFToken            string    `json:"csrf_token"`
	ExpiresAt            time.Time `json:"expires_at"`
	CookieToken          string    `json:"-"`
}

type controlSession struct {
	ID        string
	CSRFToken string
	ExpiresAt time.Time
}

// SessionAuthority owns process-local controller credentials and invalidates them on restart.
type SessionAuthority struct {
	mu              sync.Mutex
	instanceID      string
	bootstrapDigest [sha256.Size]byte
	bootstrapUsed   bool
	sessions        map[[sha256.Size]byte]controlSession
	ttl             time.Duration
	now             func() time.Time
	random          io.Reader
}

// NewSessionAuthority creates a one-time bootstrap authority for one controller lifetime.
func NewSessionAuthority(instanceID, bootstrapToken string, ttl time.Duration) (*SessionAuthority, error) {
	if !validOpaqueToken(instanceID) || !validOpaqueToken(bootstrapToken) || ttl <= 0 {
		return nil, errors.New("controller session configuration is invalid")
	}
	return &SessionAuthority{
		instanceID: instanceID, bootstrapDigest: sha256.Sum256([]byte(bootstrapToken)),
		sessions: make(map[[sha256.Size]byte]controlSession), ttl: ttl, now: time.Now, random: rand.Reader,
	}, nil
}

// InstanceID returns the public identifier for the current controller lifetime.
func (authority *SessionAuthority) InstanceID() string { return authority.instanceID }

// Exchange atomically consumes the bootstrap token and creates one independent cookie session.
func (authority *SessionAuthority) Exchange(token string) (SessionCredential, error) {
	candidate := sha256.Sum256([]byte(token))
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.bootstrapUsed || subtle.ConstantTimeCompare(candidate[:], authority.bootstrapDigest[:]) != 1 {
		return SessionCredential{}, unauthorizedFault()
	}
	cookieToken, err := authority.randomToken()
	if err != nil {
		return SessionCredential{}, &Fault{Code: "CONTROL_SESSION_UNAVAILABLE", Message: "控制会话暂不可用", Retryable: true, Status: 503}
	}
	sessionID, err := authority.randomToken()
	if err != nil {
		return SessionCredential{}, &Fault{Code: "CONTROL_SESSION_UNAVAILABLE", Message: "控制会话暂不可用", Retryable: true, Status: 503}
	}
	csrfToken, err := authority.randomToken()
	if err != nil {
		return SessionCredential{}, &Fault{Code: "CONTROL_SESSION_UNAVAILABLE", Message: "控制会话暂不可用", Retryable: true, Status: 503}
	}
	authority.bootstrapUsed = true
	expiresAt := authority.now().UTC().Add(authority.ttl)
	authority.sessions[sha256.Sum256([]byte(cookieToken))] = controlSession{ID: sessionID, CSRFToken: csrfToken, ExpiresAt: expiresAt}
	return SessionCredential{
		ControllerInstanceID: authority.instanceID, SessionID: sessionID, CSRFToken: csrfToken,
		ExpiresAt: expiresAt, CookieToken: cookieToken,
	}, nil
}

// Authenticate resolves a controller cookie without exposing it to downstream services.
func (authority *SessionAuthority) Authenticate(cookieToken string) (SessionCredential, error) {
	digest := sha256.Sum256([]byte(cookieToken))
	authority.mu.Lock()
	defer authority.mu.Unlock()
	session, found := authority.sessions[digest]
	if !found {
		return SessionCredential{}, unauthorizedFault()
	}
	if !authority.now().Before(session.ExpiresAt) {
		delete(authority.sessions, digest)
		return SessionCredential{}, unauthorizedFault()
	}
	return SessionCredential{
		ControllerInstanceID: authority.instanceID, SessionID: session.ID,
		CSRFToken: session.CSRFToken, ExpiresAt: session.ExpiresAt,
	}, nil
}

// IsBootstrapAuthorization reports whether an Authorization value contains the controller bootstrap token.
func (authority *SessionAuthority) IsBootstrapAuthorization(value string) bool {
	token, ok := bearerCredential(value)
	if !ok {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return subtle.ConstantTimeCompare(digest[:], authority.bootstrapDigest[:]) == 1
}

func (authority *SessionAuthority) randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := io.ReadFull(authority.random, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func validOpaqueToken(value string) bool {
	if len(value) < 32 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func bearerCredential(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !validOpaqueToken(parts[1]) {
		return "", false
	}
	return parts[1], true
}

func unauthorizedFault() error {
	return &Fault{Code: "CONTROL_SESSION_REQUIRED", Message: "需要有效的控制会话", Retryable: false, Status: 401}
}
