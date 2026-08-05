package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/secretstore"
)

const masterKeySize = secretstore.KeySize

// CredentialSealer encrypts Git remote tokens with context-bound AES-256-GCM.
type CredentialSealer struct {
	primitive *secretstore.Sealer
}

// NewCredentialSealer copies a 32-byte process-owned master key.
func NewCredentialSealer(key []byte) (*CredentialSealer, error) {
	return NewCredentialSealerWithReader(key, rand.Reader)
}

// NewCredentialSealerWithReader exposes entropy injection for deterministic tests.
func NewCredentialSealerWithReader(key []byte, random io.Reader) (*CredentialSealer, error) {
	primitive, err := secretstore.NewWithReader(key, random)
	if err != nil {
		return nil, secretError(errors.New("Git remote master key is invalid"))
	}
	return &CredentialSealer{primitive: primitive}, nil
}

func (sealer CredentialSealer) String() string {
	return fmt.Sprintf("CredentialSealer{configured:%t}", sealer.primitive != nil)
}

func (sealer CredentialSealer) GoString() string { return sealer.String() }

// Seal encrypts one transient token using fresh authenticated randomness.
func (sealer *CredentialSealer) Seal(token domain.Token, context domain.CredentialContext) (domain.EncryptedCredential, error) {
	if err := sealer.ready(); err != nil {
		return domain.EncryptedCredential{}, err
	}
	if !token.Configured() {
		return domain.EncryptedCredential{}, secretError(errors.New("Git remote token is empty"))
	}
	aad, digest, err := credentialAAD(context)
	if err != nil {
		return domain.EncryptedCredential{}, err
	}
	plaintext := token.Bytes()
	defer clear(plaintext)
	encrypted, err := sealer.primitive.Seal(plaintext, aad)
	if err != nil {
		return domain.EncryptedCredential{}, secretError(err)
	}
	envelope := domain.EncryptedCredential{
		KeyID: encrypted.KeyID, Nonce: encrypted.Nonce, Ciphertext: encrypted.Ciphertext, AADDigest: digest,
	}
	if err := envelope.Validate(); err != nil {
		return domain.EncryptedCredential{}, err
	}
	return envelope, nil
}

// Open authenticates one envelope and returns a short-lived copied token.
func (sealer *CredentialSealer) Open(envelope domain.EncryptedCredential, context domain.CredentialContext) (domain.Token, error) {
	if err := sealer.ready(); err != nil {
		return domain.Token{}, err
	}
	if err := envelope.Validate(); err != nil || !envelope.Configured() {
		return domain.Token{}, secretError(errors.New("Git remote credential envelope is invalid"))
	}
	aad, digest, err := credentialAAD(context)
	if err != nil {
		return domain.Token{}, err
	}
	if len(envelope.AADDigest) != len(digest) || subtle.ConstantTimeCompare([]byte(envelope.AADDigest), []byte(digest)) != 1 {
		return domain.Token{}, secretError(errors.New("Git remote credential context does not match"))
	}
	plaintext, err := sealer.primitive.Open(secretstore.Envelope{
		KeyID: envelope.KeyID, Nonce: envelope.Nonce, Ciphertext: envelope.Ciphertext,
	}, aad)
	if err != nil {
		return domain.Token{}, secretError(err)
	}
	defer clear(plaintext)
	token, err := domain.TokenFromBytes(plaintext)
	if err != nil {
		return domain.Token{}, secretError(errors.New("decrypted Git remote token is invalid"))
	}
	return token, nil
}

func (sealer *CredentialSealer) ready() error {
	if sealer == nil || sealer.primitive == nil || len(sealer.primitive.KeyID()) != sha256.Size*2 {
		return secretError(errors.New("Git remote credential sealer is unavailable"))
	}
	return nil
}

type credentialAADDocument struct {
	Purpose       string        `json:"purpose"`
	WorkspaceID   foundation.ID `json:"workspace_id"`
	Revision      int64         `json:"revision"`
	RemoteURL     string        `json:"remote_url"`
	Branch        string        `json:"branch"`
	SchemaVersion string        `json:"schema_version"`
}

func credentialAAD(context domain.CredentialContext) ([]byte, string, error) {
	if err := context.Validate(); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(credentialAADDocument{
		Purpose: domain.CredentialPurpose, WorkspaceID: context.WorkspaceID, Revision: context.Revision, RemoteURL: context.RemoteURL,
		Branch: context.Branch, SchemaVersion: context.SchemaVersion,
	})
	if err != nil {
		return nil, "", secretError(errors.New("encode Git remote credential context"))
	}
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func secretError(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, domain.ErrorCodeSecretUnavailable, false, cause)
}
