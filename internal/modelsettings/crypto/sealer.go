// Package modelcrypto implements the model settings credential envelope.
package modelcrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const masterKeySize = 32

// Sealer encrypts model credentials with revision- and target-bound AES-256-GCM.
type Sealer struct {
	key    [masterKeySize]byte
	keyID  string
	random io.Reader
}

// NewSealer copies one 32-byte master key into a process-owned sealer.
func NewSealer(key []byte) (*Sealer, error) {
	return NewSealerWithReader(key, rand.Reader)
}

// NewSealerWithReader exposes entropy injection for deterministic failure tests.
func NewSealerWithReader(key []byte, random io.Reader) (*Sealer, error) {
	if len(key) != masterKeySize || nilInterface(random) {
		return nil, secretUnavailable(errors.New("model settings master key is invalid"))
	}
	digest := sha256.Sum256(key)
	sealer := &Sealer{keyID: hex.EncodeToString(digest[:]), random: random}
	copy(sealer.key[:], key)
	return sealer, nil
}

// NewSealerFromFile loads a base64-encoded master key and creates a sealer.
func NewSealerFromFile(path string) (*Sealer, error) {
	key, err := LoadKeyFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return NewSealer(key)
}

// LoadKeyFile loads one base64-encoded 32-byte key from a private regular file.
func LoadKeyFile(path string) ([]byte, error) {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsRune(path, '\x00') {
		return nil, secretUnavailable(errors.New("model settings key path is invalid"))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, secretUnavailable(fmt.Errorf("open model settings key: %w", err))
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return nil, secretUnavailable(fmt.Errorf("stat opened model settings key: %w", err))
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, secretUnavailable(fmt.Errorf("stat model settings key path: %w", err))
	}
	permissions := info.Mode().Perm()
	if !info.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, pathInfo) ||
		(permissions != 0o400 && permissions != 0o600) || info.Size() < 1 || info.Size() > 128 {
		return nil, secretUnavailable(errors.New("model settings key file is not private regular file"))
	}
	encoded, err := io.ReadAll(io.LimitReader(file, 129))
	if err != nil {
		return nil, secretUnavailable(fmt.Errorf("read model settings key: %w", err))
	}
	defer clear(encoded)
	if len(encoded) > 128 {
		return nil, secretUnavailable(errors.New("model settings key file is too large"))
	}
	encoded = bytes.TrimSpace(encoded)
	if len(encoded) == 0 || bytes.ContainsAny(encoded, " \t\r\n") {
		return nil, secretUnavailable(errors.New("model settings key encoding is invalid"))
	}
	key := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	decoded, err := base64.StdEncoding.Decode(key, encoded)
	if err != nil || decoded != masterKeySize {
		clear(key)
		return nil, secretUnavailable(errors.New("model settings key must decode to 32 bytes"))
	}
	return key[:decoded], nil
}

// KeyID returns the non-secret SHA-256 identifier persisted with ciphertext.
func (sealer *Sealer) KeyID() string {
	if sealer == nil {
		return ""
	}
	return sealer.keyID
}

func (sealer Sealer) String() string {
	return fmt.Sprintf("modelcrypto.Sealer{configured:%t}", sealer.keyID != "")
}

func (sealer Sealer) GoString() string { return sealer.String() }

// Seal encrypts one configured credential using a fresh GCM nonce.
func (sealer *Sealer) Seal(secret domain.Secret, context domain.SecretContext) (domain.EncryptedSecret, error) {
	if err := sealer.ready(); err != nil {
		return domain.EncryptedSecret{}, err
	}
	if !secret.Configured() {
		return domain.EncryptedSecret{}, secretUnavailable(errors.New("model settings secret is empty"))
	}
	aad, err := additionalData(context)
	if err != nil {
		return domain.EncryptedSecret{}, err
	}
	gcm, err := sealer.gcm()
	if err != nil {
		return domain.EncryptedSecret{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(sealer.random, nonce); err != nil {
		return domain.EncryptedSecret{}, secretUnavailable(fmt.Errorf("read model settings nonce: %w", err))
	}
	plaintext := secret.Bytes()
	defer clear(plaintext)
	envelope := domain.EncryptedSecret{
		KeyID:      sealer.keyID,
		Nonce:      nonce,
		Ciphertext: gcm.Seal(nil, nonce, plaintext, aad),
	}
	if err := envelope.Validate(); err != nil {
		return domain.EncryptedSecret{}, err
	}
	return envelope, nil
}

// Open authenticates the envelope and returns a short-lived copied credential.
func (sealer *Sealer) Open(envelope domain.EncryptedSecret, context domain.SecretContext) (domain.Secret, error) {
	if err := sealer.ready(); err != nil {
		return domain.Secret{}, err
	}
	if err := envelope.Validate(); err != nil || !envelope.Configured() {
		return domain.Secret{}, secretUnavailable(errors.New("model settings secret envelope is invalid"))
	}
	if len(envelope.KeyID) != len(sealer.keyID) || subtle.ConstantTimeCompare([]byte(envelope.KeyID), []byte(sealer.keyID)) != 1 {
		return domain.Secret{}, secretUnavailable(errors.New("model settings secret key identifier does not match"))
	}
	aad, err := additionalData(context)
	if err != nil {
		return domain.Secret{}, err
	}
	gcm, err := sealer.gcm()
	if err != nil {
		return domain.Secret{}, err
	}
	plaintext, err := gcm.Open(nil, envelope.Nonce, envelope.Ciphertext, aad)
	if err != nil {
		return domain.Secret{}, secretUnavailable(errors.New("model settings secret authentication failed"))
	}
	defer clear(plaintext)
	secret, err := domain.SecretFromBytes(plaintext)
	if err != nil {
		return domain.Secret{}, secretUnavailable(errors.New("model settings decrypted secret is invalid"))
	}
	return secret, nil
}

func (sealer *Sealer) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(sealer.key[:])
	if err != nil {
		return nil, secretUnavailable(errors.New("create model settings cipher"))
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, secretUnavailable(errors.New("create model settings gcm"))
	}
	return gcm, nil
}

func (sealer *Sealer) ready() error {
	if sealer == nil || len(sealer.keyID) != sha256.Size*2 || nilInterface(sealer.random) {
		return secretUnavailable(errors.New("model settings sealer is unavailable"))
	}
	return nil
}

type aadDocument struct {
	Revision      int64                `json:"revision"`
	Purpose       domain.SecretPurpose `json:"purpose"`
	SchemaVersion string               `json:"schema_version"`
	Provider      string               `json:"provider"`
	BaseURL       string               `json:"base_url"`
}

func additionalData(context domain.SecretContext) ([]byte, error) {
	if err := context.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(aadDocument{
		Revision: context.Revision, Purpose: context.Purpose, SchemaVersion: context.SchemaVersion,
		Provider: context.Provider, BaseURL: context.BaseURL,
	})
	if err != nil {
		return nil, secretUnavailable(errors.New("encode model settings secret context"))
	}
	return encoded, nil
}

func secretUnavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeSecretUnavailable, false, cause)
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
