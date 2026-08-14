package checkpoint

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"
)

const (
	maxCheckpointTTL   = 15 * time.Minute
	maxCheckpointBytes = 8 << 20
)

var (
	// ErrInvalidStoreConfig means the PoC store cannot enforce its bounded lifecycle.
	ErrInvalidStoreConfig = errors.New("checkpoint store configuration is invalid")
	// ErrCheckpointTooLarge means Eino attempted to persist an unbounded checkpoint.
	ErrCheckpointTooLarge = errors.New("checkpoint exceeds the PoC size limit")
	// ErrCheckpointCorrupt means authenticated checkpoint state cannot be opened.
	ErrCheckpointCorrupt = errors.New("checkpoint ciphertext is invalid")
	// ErrContextRequired means a caller omitted the execution cancellation boundary.
	ErrContextRequired = errors.New("checkpoint context is required")
)

type storeEntry struct {
	sealed    []byte
	expiresAt time.Time
}

// Store is an encrypted, bounded in-memory CheckPointStore for the isolated PoC.
// It is not a production persistence implementation.
type Store struct {
	mu       sync.Mutex
	randomMu sync.Mutex
	// lifecycle serializes Runner transitions across every Runner sharing this store.
	lifecycle chan struct{}
	aead      cipher.AEAD
	ttl       time.Duration
	now       func() time.Time
	random    io.Reader
	entries   map[string]storeEntry
}

var (
	_ compose.CheckPointStore = (*Store)(nil)
	_ interface {
		Delete(context.Context, string) error
	} = (*Store)(nil)
)

// NewStore creates an AES-256-GCM checkpoint store with a short TTL.
func NewStore(encryptionKey []byte, ttl time.Duration) (*Store, error) {
	return newStore(encryptionKey, ttl, time.Now, rand.Reader)
}

func newStore(encryptionKey []byte, ttl time.Duration, now func() time.Time, random io.Reader) (*Store, error) {
	if len(encryptionKey) != keyBytes || ttl <= 0 || ttl > maxCheckpointTTL || now == nil || random == nil {
		return nil, ErrInvalidStoreConfig
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create checkpoint cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create checkpoint AEAD: %w", err)
	}
	return &Store{
		lifecycle: make(chan struct{}, 1),
		aead:      aead,
		ttl:       ttl,
		now:       now,
		random:    random,
		entries:   make(map[string]storeEntry),
	}, nil
}

// Get decrypts a non-expired checkpoint and returns an isolated copy.
func (store *Store) Get(ctx context.Context, checkpointID string) ([]byte, bool, error) {
	if err := validateStoreCall(store, ctx, checkpointID); err != nil {
		return nil, false, err
	}
	entry, exists := store.activeEntry(checkpointID)
	if !exists {
		return nil, false, nil
	}

	nonceSize := store.aead.NonceSize()
	if len(entry.sealed) <= nonceSize {
		return nil, false, ErrCheckpointCorrupt
	}
	plain, err := store.aead.Open(nil, entry.sealed[:nonceSize], entry.sealed[nonceSize:], []byte(checkpointID))
	if err != nil {
		return nil, false, ErrCheckpointCorrupt
	}
	if err := ctx.Err(); err != nil {
		clear(plain)
		return nil, false, err
	}
	return plain, true, nil
}

// Exists reports whether a scope still owns a non-expired checkpoint without decrypting it.
func (store *Store) Exists(ctx context.Context, checkpointID string) (bool, error) {
	if err := validateStoreCall(store, ctx, checkpointID); err != nil {
		return false, err
	}
	_, exists := store.activeEntry(checkpointID)
	return exists, nil
}

// Set encrypts one checkpoint with a fresh nonce and replaces the current value.
func (store *Store) Set(ctx context.Context, checkpointID string, checkpoint []byte) error {
	if err := validateStoreCall(store, ctx, checkpointID); err != nil {
		return err
	}
	if len(checkpoint) == 0 || len(checkpoint) > maxCheckpointBytes {
		return ErrCheckpointTooLarge
	}

	nonce := make([]byte, store.aead.NonceSize())
	store.randomMu.Lock()
	_, randomErr := io.ReadFull(store.random, nonce)
	store.randomMu.Unlock()
	if randomErr != nil {
		return fmt.Errorf("generate checkpoint nonce: %w", randomErr)
	}
	sealed := store.aead.Seal(append([]byte(nil), nonce...), nonce, checkpoint, []byte(checkpointID))
	entry := storeEntry{sealed: sealed, expiresAt: store.now().UTC().Add(store.ttl)}
	if err := ctx.Err(); err != nil {
		return err
	}

	store.mu.Lock()
	store.entries[checkpointID] = entry
	store.mu.Unlock()
	return nil
}

// Delete removes a checkpoint. Repeated deletion is safe.
func (store *Store) Delete(ctx context.Context, checkpointID string) error {
	if err := validateStoreCall(store, ctx, checkpointID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	delete(store.entries, checkpointID)
	store.mu.Unlock()
	return nil
}

func (store *Store) activeEntry(checkpointID string) (storeEntry, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	entry, exists := store.entries[checkpointID]
	if exists && !store.now().UTC().Before(entry.expiresAt) {
		delete(store.entries, checkpointID)
		return storeEntry{}, false
	}
	entry.sealed = append([]byte(nil), entry.sealed...)
	return entry, exists
}

func validateStoreCall(store *Store, ctx context.Context, checkpointID string) error {
	if store == nil || store.lifecycle == nil || store.aead == nil || store.now == nil || store.random == nil {
		return ErrInvalidStoreConfig
	}
	if ctx == nil {
		return ErrContextRequired
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validCheckpointID(checkpointID) {
		return ErrInvalidScope
	}
	return nil
}
