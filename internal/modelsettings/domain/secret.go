package domain

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const SecretSchemaVersion = "model-settings-secret/v1"

// Secret contains a transient credential and always formats as redacted.
type Secret struct{ value []byte }

// NewSecret validates and copies a transient credential.
func NewSecret(value string) (Secret, error) {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 16*1024 || !utf8.ValidString(value) {
		return Secret{}, secretInvalid("secret value is invalid")
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return Secret{}, secretInvalid("secret value is invalid")
		}
	}
	return Secret{value: []byte(value)}, nil
}

// SecretFromBytes validates and copies credential bytes.
func SecretFromBytes(value []byte) (Secret, error) {
	if len(value) == 0 || len(value) > 16*1024 || !utf8.Valid(value) || !bytes.Equal(value, bytes.TrimSpace(value)) {
		return Secret{}, secretInvalid("secret value is invalid")
	}
	for remaining := value; len(remaining) > 0; {
		character, size := utf8.DecodeRune(remaining)
		if character < 0x21 || character == 0x7f {
			return Secret{}, secretInvalid("secret value is invalid")
		}
		remaining = remaining[size:]
	}
	return Secret{value: bytes.Clone(value)}, nil
}

// Configured reports whether a credential is present.
func (secret Secret) Configured() bool { return len(secret.value) > 0 }

// Bytes returns a defensive copy for the narrow provider boundary.
func (secret Secret) Bytes() []byte { return bytes.Clone(secret.value) }

// Destroy clears the owned transient buffer.
func (secret *Secret) Destroy() {
	if secret == nil {
		return
	}
	for index := range secret.value {
		secret.value[index] = 0
	}
	secret.value = nil
}

func (secret Secret) String() string   { return "<redacted>" }
func (secret Secret) GoString() string { return "domain.Secret(<redacted>)" }

// SecretActionKind is an explicit write-only credential operation.
type SecretActionKind string

const (
	SecretActionKeep    SecretActionKind = "keep"
	SecretActionReplace SecretActionKind = "replace"
	SecretActionClear   SecretActionKind = "clear"
)

// SecretAction avoids an ambiguous empty-string update.
type SecretAction struct {
	Kind  SecretActionKind
	Value Secret
}

func KeepSecret() SecretAction  { return SecretAction{Kind: SecretActionKeep} }
func ClearSecret() SecretAction { return SecretAction{Kind: SecretActionClear} }

func ReplaceSecret(value string) (SecretAction, error) {
	secret, err := NewSecret(value)
	if err != nil {
		return SecretAction{}, err
	}
	return SecretAction{Kind: SecretActionReplace, Value: secret}, nil
}

func (action SecretAction) Validate() error {
	switch action.Kind {
	case SecretActionKeep, SecretActionClear:
		if action.Value.Configured() {
			return secretActionInvalid("keep and clear must not carry a secret value")
		}
	case SecretActionReplace:
		if !action.Value.Configured() {
			return secretActionInvalid("replace requires a secret value")
		}
	default:
		return secretActionInvalid("secret action is invalid")
	}
	return nil
}

func (action SecretAction) String() string {
	return fmt.Sprintf("SecretAction{kind:%q value_configured:%t}", action.Kind, action.Value.Configured())
}
func (action SecretAction) GoString() string { return action.String() }

// SecretPurpose prevents chat and embedding ciphertext substitution.
type SecretPurpose string

const (
	SecretPurposeChat      SecretPurpose = "chat"
	SecretPurposeEmbedding SecretPurpose = "embedding"
)

// SecretContext is serialized as AEAD additional authenticated data.
type SecretContext struct {
	Revision      int64
	Purpose       SecretPurpose
	SchemaVersion string
	Provider      string
	BaseURL       string
}

func (context SecretContext) Validate() error {
	if context.Revision <= 0 || context.SchemaVersion != SecretSchemaVersion ||
		(context.Purpose != SecretPurposeChat && context.Purpose != SecretPurposeEmbedding) ||
		!canonicalText(context.Provider, 64) {
		return secretInvalid("secret context is invalid")
	}
	normalized, err := NormalizeBaseURL(context.BaseURL)
	if err != nil || normalized != context.BaseURL {
		return secretInvalid("secret context endpoint is invalid")
	}
	return nil
}

// EncryptedSecret is safe to persist but never safe to expose through Settings APIs.
type EncryptedSecret struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

func (secret EncryptedSecret) Configured() bool {
	return secret.KeyID != "" && len(secret.Nonce) > 0 && len(secret.Ciphertext) > 0
}

func (secret EncryptedSecret) Validate() error {
	allEmpty := secret.KeyID == "" && len(secret.Nonce) == 0 && len(secret.Ciphertext) == 0
	if allEmpty {
		return nil
	}
	if !canonicalText(secret.KeyID, 64) || len(secret.Nonce) != 12 || len(secret.Ciphertext) < 17 || len(secret.Ciphertext) > 20*1024 {
		return secretInvalid("encrypted secret envelope is invalid")
	}
	return nil
}

func (secret EncryptedSecret) String() string {
	return fmt.Sprintf("EncryptedSecret{configured:%t}", secret.Configured())
}
func (secret EncryptedSecret) GoString() string { return secret.String() }

// ValidateSecretChange enforces explicit clearing and prevents keep from rebinding a stored key.
func ValidateSecretChange(previousProvider, previousBaseURL string, previousConfigured bool, nextProvider, nextBaseURL string, action SecretAction) error {
	if err := action.Validate(); err != nil {
		return err
	}
	if action.Kind != SecretActionKeep || !previousConfigured {
		return nil
	}
	previousURL, previousErr := NormalizeBaseURL(previousBaseURL)
	nextURL, nextErr := NormalizeBaseURL(nextBaseURL)
	if previousErr != nil || nextErr != nil || previousProvider != nextProvider || previousURL != nextURL {
		return foundation.NewError(foundation.ErrorVersionConflict, ErrorCodeSecretTargetChanged, false, errors.New("configured secret target changed"))
	}
	return nil
}

func secretInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSecretUnavailable, false, errors.New(message))
}

func secretActionInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSecretActionInvalid, false, errors.New(message))
}
