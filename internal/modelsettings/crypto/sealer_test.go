package modelcrypto

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

func TestSealerRoundTripAndRedaction(t *testing.T) {
	sealer, err := NewSealerWithReader(bytes.Repeat([]byte{1}, 32), bytes.NewReader(bytes.Repeat([]byte{2}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	if formatted := fmt.Sprintf("%v %#v %+v", sealer, sealer, *sealer); strings.Contains(formatted, "1 1 1") || strings.Contains(formatted, sealer.KeyID()) {
		t.Fatalf("formatted sealer leaked key material: %q", formatted)
	}
	secret, err := domain.NewSecret("credential-that-must-not-leak")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	context := testContext(7, domain.SecretPurposeChat)
	envelope, err := sealer.Seal(secret, context)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.KeyID != sealer.KeyID() || len(envelope.Nonce) != 12 || bytes.Contains(envelope.Ciphertext, secret.Bytes()) {
		t.Fatalf("unexpected envelope: %s", envelope)
	}
	opened, err := sealer.Open(envelope, context)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Destroy()
	if got := string(opened.Bytes()); got != "credential-that-must-not-leak" {
		t.Fatalf("opened secret = %q", got)
	}
	for _, formatted := range []string{fmt.Sprint(secret), fmt.Sprintf("%#v", secret), fmt.Sprint(envelope), fmt.Sprintf("%#v", envelope)} {
		if strings.Contains(formatted, "credential-that-must-not-leak") {
			t.Fatalf("formatted secret leaked: %q", formatted)
		}
	}
}

func TestSealerRejectsWrongKeyAndTampering(t *testing.T) {
	context := testContext(8, domain.SecretPurposeEmbedding)
	secret, err := domain.NewSecret("embedding-key")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	sealer, err := NewSealerWithReader(bytes.Repeat([]byte{3}, 32), bytes.NewReader(bytes.Repeat([]byte{4}, 36)))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sealer.Seal(secret, context)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := NewSealer(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	assertSecretUnavailable(t, func() error { _, openErr := wrong.Open(envelope, context); return openErr }())

	tamperedNonce := cloneEnvelope(envelope)
	tamperedNonce.Nonce[0] ^= 0xff
	assertSecretUnavailable(t, func() error { _, openErr := sealer.Open(tamperedNonce, context); return openErr }())
	tamperedCiphertext := cloneEnvelope(envelope)
	tamperedCiphertext.Ciphertext[len(tamperedCiphertext.Ciphertext)-1] ^= 0xff
	assertSecretUnavailable(t, func() error { _, openErr := sealer.Open(tamperedCiphertext, context); return openErr }())
	assertSecretUnavailable(t, func() error {
		_, openErr := sealer.Open(envelope, testContext(context.Revision+1, context.Purpose))
		return openErr
	}())
	assertSecretUnavailable(t, func() error {
		changed := context
		changed.BaseURL = "https://other.example.test/v1"
		_, openErr := sealer.Open(envelope, changed)
		return openErr
	}())
	assertSecretUnavailable(t, func() error {
		changed := context
		changed.Provider = "other-provider"
		_, openErr := sealer.Open(envelope, changed)
		return openErr
	}())
	assertSecretUnavailable(t, func() error {
		changed := context
		changed.Purpose = domain.SecretPurposeChat
		_, openErr := sealer.Open(envelope, changed)
		return openErr
	}())
	assertSecretUnavailable(t, func() error {
		changed := context
		changed.SchemaVersion = "model-settings-secret/v2"
		_, openErr := sealer.Open(envelope, changed)
		return openErr
	}())
	tamperedKeyID := cloneEnvelope(envelope)
	tamperedKeyID.KeyID = strings.Repeat("0", len(tamperedKeyID.KeyID))
	assertSecretUnavailable(t, func() error { _, openErr := sealer.Open(tamperedKeyID, context); return openErr }())
}

func TestSealerRejectsEntropyFailureWithoutReturningPartialEnvelope(t *testing.T) {
	sealer, err := NewSealerWithReader(bytes.Repeat([]byte{6}, 32), bytes.NewReader(bytes.Repeat([]byte{7}, 11)))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := domain.NewSecret("entropy-failure-canary")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	envelope, err := sealer.Seal(secret, testContext(9, domain.SecretPurposeChat))
	assertSecretUnavailable(t, err)
	if envelope.Configured() || strings.Contains(err.Error(), "entropy-failure-canary") {
		t.Fatalf("entropy failure returned sensitive or partial data: envelope=%s err=%v", envelope, err)
	}
}

func TestLoadKeyFileRequiresPrivateBase64Key(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "key")
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := LoadKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, bytes.Repeat([]byte{9}, 32)) {
		t.Fatal("decoded key mismatch")
	}
	clear(key)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	assertSecretUnavailable(t, func() error { _, loadErr := LoadKeyFile(path); return loadErr }())
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString([]byte("short"))), 0o600); err != nil {
		t.Fatal(err)
	}
	assertSecretUnavailable(t, func() error { _, loadErr := LoadKeyFile(path); return loadErr }())
	if err := os.WriteFile(path, []byte(encoded), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyFile(path); err != nil {
		t.Fatalf("read-only private key: %v", err)
	}
	link := filepath.Join(directory, "key-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	assertSecretUnavailable(t, func() error { _, loadErr := LoadKeyFile(link); return loadErr }())
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	cleanEncoding := strings.TrimSpace(encoded)
	if err := os.WriteFile(path, []byte(cleanEncoding[:12]+" "+cleanEncoding[12:]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertSecretUnavailable(t, func() error { _, loadErr := LoadKeyFile(path); return loadErr }())

	oversized := filepath.Join(directory, "oversized-key")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte{'a'}, 129), 0o600); err != nil {
		t.Fatal(err)
	}
	assertSecretUnavailable(t, func() error { _, loadErr := LoadKeyFile(oversized); return loadErr }())
}

func testContext(revision int64, purpose domain.SecretPurpose) domain.SecretContext {
	return domain.SecretContext{
		Revision: revision, Purpose: purpose, SchemaVersion: domain.SecretSchemaVersion,
		Provider: "openai-compatible", BaseURL: "https://models.example.test/v1",
	}
}

func cloneEnvelope(envelope domain.EncryptedSecret) domain.EncryptedSecret {
	return domain.EncryptedSecret{
		KeyID: envelope.KeyID, Nonce: bytes.Clone(envelope.Nonce), Ciphertext: bytes.Clone(envelope.Ciphertext),
	}
}

func assertSecretUnavailable(t *testing.T, err error) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeSecretUnavailable {
		t.Fatalf("error = %v, want %s", err, domain.ErrorCodeSecretUnavailable)
	}
}
