package hostcontroller

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionAuthorityConsumesBootstrapOnceAndExpiresCookie(t *testing.T) {
	t.Parallel()
	bootstrap := "b" + string(bytes.Repeat([]byte("a"), 42))
	authority, err := NewSessionAuthority("i"+string(bytes.Repeat([]byte("n"), 42)), bootstrap, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionAuthority() error: %v", err)
	}
	now := time.Date(2026, 7, 31, 8, 0, 0, 0, time.UTC)
	authority.now = func() time.Time { return now }
	authority.random = bytes.NewReader(bytes.Repeat([]byte{7}, 96))

	if _, err := authority.Exchange("x" + string(bytes.Repeat([]byte("z"), 42))); err == nil {
		t.Fatal("wrong bootstrap token succeeded")
	}
	credential, err := authority.Exchange(bootstrap)
	if err != nil {
		t.Fatalf("Exchange() error: %v", err)
	}
	if credential.CookieToken == "" || credential.CSRFToken == "" || credential.SessionID == "" || credential.ExpiresAt != now.Add(time.Hour) {
		t.Fatalf("unexpected credential: %+v", credential)
	}
	if _, err := authority.Exchange(bootstrap); err == nil {
		t.Fatal("bootstrap token was reusable")
	}
	restored, err := authority.Authenticate(credential.CookieToken)
	if err != nil || restored.SessionID != credential.SessionID || restored.CSRFToken != credential.CSRFToken {
		t.Fatalf("Authenticate() = %+v, %v", restored, err)
	}
	if !authority.IsBootstrapAuthorization("Bearer " + bootstrap) {
		t.Fatal("bootstrap Authorization was not recognized for proxy redaction")
	}
	now = now.Add(time.Hour)
	if _, err := authority.Authenticate(credential.CookieToken); err == nil {
		t.Fatal("expired controller cookie succeeded")
	}
}

func TestSessionAuthorityConcurrentExchangeHasOneWinner(t *testing.T) {
	t.Parallel()
	bootstrap := "bootstrap_token_abcdefghijklmnopqrstuvwxyz0123456789"
	authority, err := NewSessionAuthority("instance_token_abcdefghijklmnopqrstuvwxyz0123456789", bootstrap, time.Hour)
	if err != nil {
		t.Fatalf("NewSessionAuthority() error: %v", err)
	}
	var winners atomic.Int64
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, exchangeErr := authority.Exchange(bootstrap); exchangeErr == nil {
				winners.Add(1)
			}
		}()
	}
	wait.Wait()
	if winners.Load() != 1 {
		t.Fatalf("exchange winners=%d, want 1", winners.Load())
	}
}
