package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRuntimeModeFromEnvironmentRequiresDeploymentOwnedEnum(t *testing.T) {
	for name, lookup := range map[string]func(string) (string, bool){
		"missing": func(string) (string, bool) { return "", false },
		"invalid": func(string) (string, bool) { return "local", true },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runtimeModeFromEnvironment(lookup); err == nil {
				t.Fatal("runtimeModeFromEnvironment unexpectedly accepted invalid mode")
			}
		})
	}
	for _, expected := range []string{"managed", "external-static"} {
		mode, err := runtimeModeFromEnvironment(func(string) (string, bool) { return expected, true })
		if err != nil || string(mode) != expected {
			t.Fatalf("mode=%q err=%v", mode, err)
		}
	}
}

func TestWaitForReconcilerWaitsForCompletion(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if err := waitForReconciler(done, time.Second); err != nil {
		t.Fatalf("waitForReconciler() error = %v", err)
	}

	pending := make(chan struct{})
	if err := waitForReconciler(pending, 5*time.Millisecond); err == nil {
		t.Fatal("waitForReconciler() unexpectedly succeeded for a pending reconciler")
	} else if !errors.Is(err, errReconcilerShutdownTimeout) {
		t.Fatalf("waitForReconciler() error = %v", err)
	}
}

func TestServeShutsDownAfterContextCancellation(t *testing.T) {
	listener := newFakeListener()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := serve(ctx, listener, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("serve() error = %v", err)
	}
	if !listener.closed() {
		t.Fatal("listener was not closed")
	}
}

func TestLoadRuntimeDatabaseConfigRestoresAllEnvironmentOnEarlyFileError(t *testing.T) {
	t.Setenv("ZHIXU_DATABASE_USER", "app-user")
	t.Setenv("ZHIXU_DATABASE_PASSWORD", "app-password")
	t.Setenv("ZHIXU_DATABASE_USER_FILE", filepath.Join(t.TempDir(), "missing-user"))
	t.Setenv("ZHIXU_DATABASE_PASSWORD_FILE", "relative-password-file")
	_, restore, err := loadRuntimeDatabaseConfig()
	if err == nil {
		t.Fatal("loadRuntimeDatabaseConfig() unexpectedly succeeded")
	}
	if restore == nil {
		t.Fatal("loadRuntimeDatabaseConfig() returned no restore function")
	}
	// The first file is read before the second is validated; restoration must
	// still preserve both original application credentials.
	_ = os.Setenv("ZHIXU_DATABASE_USER", "mutated")
	_ = os.Setenv("ZHIXU_DATABASE_PASSWORD", "mutated")
	restore()
	if got := os.Getenv("ZHIXU_DATABASE_USER"); got != "app-user" {
		t.Fatalf("restored database user = %q", got)
	}
	if got := os.Getenv("ZHIXU_DATABASE_PASSWORD"); got != "app-password" {
		t.Fatalf("restored database password = %q", got)
	}
}

type fakeListener struct {
	mu       sync.Mutex
	isClosed bool
	wake     chan struct{}
	once     sync.Once
}

func newFakeListener() *fakeListener {
	return &fakeListener{wake: make(chan struct{})}
}

func (listener *fakeListener) Accept() (net.Conn, error) {
	<-listener.wake
	return nil, net.ErrClosed
}

func (listener *fakeListener) Close() error {
	listener.mu.Lock()
	listener.isClosed = true
	listener.mu.Unlock()
	listener.once.Do(func() { close(listener.wake) })
	return nil
}

func (listener *fakeListener) Addr() net.Addr { return fakeAddress("fixture") }

func (listener *fakeListener) closed() bool {
	listener.mu.Lock()
	defer listener.mu.Unlock()
	return listener.isClosed
}

type fakeAddress string

func (address fakeAddress) Network() string { return string(address) }
func (address fakeAddress) String() string  { return string(address) }
