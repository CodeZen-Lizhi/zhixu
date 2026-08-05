// Package secretstore contains the shared authenticated-encryption primitive
// used by feature-specific secret envelopes.
package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
)

// KeySize is the required byte length for an AES-256 master key.
const KeySize = 32

// Envelope is the transport-neutral AES-GCM ciphertext representation.
// Feature domains remain responsible for validating their persisted shape.
type Envelope struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

// Sealer owns one process-local AES-256 key and never exposes it to callers.
type Sealer struct {
	key    [KeySize]byte
	keyID  string
	random io.Reader
}

// New creates a sealer using crypto/rand for nonces.
func New(key []byte) (*Sealer, error) { return NewWithReader(key, rand.Reader) }

// NewWithReader creates a sealer with an injectable nonce source for tests.
func NewWithReader(key []byte, random io.Reader) (*Sealer, error) {
	if len(key) != KeySize || nilInterface(random) {
		return nil, errors.New("secret store key or nonce source is invalid")
	}
	digest := sha256.Sum256(key)
	sealer := &Sealer{keyID: hex.EncodeToString(digest[:]), random: random}
	copy(sealer.key[:], key)
	return sealer, nil
}

// KeyID returns a non-secret identifier for the process-local key.
func (sealer *Sealer) KeyID() string {
	if sealer == nil {
		return ""
	}
	return sealer.keyID
}

func (sealer Sealer) String() string {
	return fmt.Sprintf("secretstore.Sealer{configured:%t}", sealer.keyID != "")
}

func (sealer Sealer) GoString() string { return sealer.String() }

// Seal encrypts plaintext with authenticated additional data.
func (sealer *Sealer) Seal(plaintext, aad []byte) (Envelope, error) {
	if err := sealer.ready(); err != nil {
		return Envelope{}, err
	}
	gcm, err := sealer.gcm()
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(sealer.random, nonce); err != nil {
		clear(nonce)
		return Envelope{}, errors.New("secret store nonce is unavailable")
	}
	return Envelope{KeyID: sealer.keyID, Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, plaintext, aad)}, nil
}

// Open authenticates an envelope and returns a new plaintext buffer owned by
// the caller, which must clear it after converting it into its domain type.
func (sealer *Sealer) Open(envelope Envelope, aad []byte) ([]byte, error) {
	if err := sealer.ready(); err != nil {
		return nil, err
	}
	if envelope.KeyID == "" || len(envelope.Nonce) == 0 || len(envelope.Ciphertext) == 0 ||
		len(envelope.KeyID) != len(sealer.keyID) || subtle.ConstantTimeCompare([]byte(envelope.KeyID), []byte(sealer.keyID)) != 1 {
		return nil, errors.New("secret store envelope is invalid")
	}
	gcm, err := sealer.gcm()
	if err != nil {
		return nil, err
	}
	if len(envelope.Nonce) != gcm.NonceSize() || len(envelope.Ciphertext) < gcm.Overhead() {
		return nil, errors.New("secret store envelope is invalid")
	}
	plaintext, err := gcm.Open(nil, envelope.Nonce, envelope.Ciphertext, aad)
	if err != nil {
		return nil, errors.New("secret store authentication failed")
	}
	return plaintext, nil
}

func (sealer *Sealer) ready() error {
	if sealer == nil || len(sealer.keyID) != sha256.Size*2 || nilInterface(sealer.random) {
		return errors.New("secret store sealer is unavailable")
	}
	return nil
}

func (sealer *Sealer) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(sealer.key[:])
	if err != nil {
		return nil, errors.New("secret store cipher is unavailable")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("secret store GCM is unavailable")
	}
	return gcm, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
