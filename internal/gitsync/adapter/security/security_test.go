package security

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/gitsync/domain"
	modelcrypto "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/crypto"
	modeldomain "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/domain"
)

const securityTestWorkspace foundation.ID = "10000000-0000-4000-8000-000000000001"

type staticResolver struct {
	addresses map[string][]net.IPAddr
	err       error
}

func (resolver staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if resolver.err != nil {
		return nil, resolver.err
	}
	return resolver.addresses[host], nil
}

func TestURLPolicyNormalizesPublicHTTPSRemote(t *testing.T) {
	policy := NewURLPolicy(staticResolver{addresses: map[string][]net.IPAddr{
		"git.example.com": {
			{IP: net.ParseIP("8.8.8.8")},
			{IP: net.ParseIP("1.1.1.1")},
			{IP: net.ParseIP("8.8.8.8")},
		},
	}})
	endpoint, err := policy.ResolveHTTPSRemote(context.Background(), " HTTPS://GIT.Example.COM:443/team/repo.git ")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.URL != "https://git.example.com/team/repo.git" || endpoint.Hostname != "git.example.com" || endpoint.Port != 443 {
		t.Fatalf("resolved endpoint = %#v", endpoint)
	}
	wantAddresses := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}
	if len(endpoint.Addresses) != len(wantAddresses) || endpoint.Addresses[0] != wantAddresses[0] || endpoint.Addresses[1] != wantAddresses[1] {
		t.Fatalf("resolved addresses = %v, want %v", endpoint.Addresses, wantAddresses)
	}
	got, err := policy.NormalizeHTTPSRemote(context.Background(), endpoint.URL)
	if err != nil || got != endpoint.URL {
		t.Fatalf("NormalizeHTTPSRemote() = %q, %v", got, err)
	}
}

func TestURLPolicyRejectsCredentialAndNonPublicDNS(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		addresses map[string][]net.IPAddr
	}{
		{name: "userinfo", raw: "https://token@git.example.com/team/repo.git", addresses: map[string][]net.IPAddr{"git.example.com": {{IP: net.ParseIP("8.8.8.8")}}}},
		{name: "query", raw: "https://git.example.com/team/repo.git?token=x", addresses: map[string][]net.IPAddr{"git.example.com": {{IP: net.ParseIP("8.8.8.8")}}}},
		{name: "loopback literal", raw: "https://127.0.0.1/repo.git"},
		{name: "private answer", raw: "https://git.example.com/repo.git", addresses: map[string][]net.IPAddr{"git.example.com": {{IP: net.ParseIP("10.0.0.1")}}}},
		{name: "mixed answers", raw: "https://git.example.com/repo.git", addresses: map[string][]net.IPAddr{"git.example.com": {{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("169.254.169.254")}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := NewURLPolicy(staticResolver{addresses: test.addresses})
			_, err := policy.NormalizeHTTPSRemote(context.Background(), test.raw)
			requireSecurityCode(t, err, domain.ErrorCodeURLInvalid)
		})
	}
}

func TestURLPolicyClassifiesDNSFailureWithoutLeakingHostDetails(t *testing.T) {
	policy := NewURLPolicy(staticResolver{err: errors.New("resolver detail")})
	_, err := policy.NormalizeHTTPSRemote(context.Background(), "https://git.example.com/team/repo.git")
	requireSecurityCode(t, err, domain.ErrorCodeOffline)
}

func TestCredentialSealerBindsTokenToCompleteContext(t *testing.T) {
	key := bytes.Repeat([]byte{1}, masterKeySize)
	sealer, err := NewCredentialSealerWithReader(key, bytes.NewReader(bytes.Repeat([]byte{2}, 24)))
	if err != nil {
		t.Fatal(err)
	}
	tokenValue := "credential-that-must-not-leak"
	token, err := domain.NewToken(tokenValue)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Destroy()
	contextValue := credentialContext()
	aad, _, err := credentialAAD(contextValue)
	if err != nil || !bytes.Contains(aad, []byte(`"purpose":"git-remote-token"`)) || !bytes.Contains(aad, []byte(`"schema_version":"git-remote-token/v1"`)) {
		t.Fatalf("credential AAD lacks purpose/schema binding: %s, %v", aad, err)
	}
	envelope, err := sealer.Seal(token, contextValue)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envelope.Ciphertext), tokenValue) || strings.Contains(envelope.String(), tokenValue) {
		t.Fatal("encrypted credential leaked plaintext")
	}
	opened, err := sealer.Open(envelope, contextValue)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Destroy()
	if got := string(opened.Bytes()); got != tokenValue {
		t.Fatalf("opened token = %q", got)
	}

	for _, test := range []struct {
		name   string
		change func(*domain.CredentialContext)
	}{
		{name: "Workspace", change: func(value *domain.CredentialContext) {
			value.WorkspaceID = "10000000-0000-4000-8000-000000000002"
		}},
		{name: "revision", change: func(value *domain.CredentialContext) { value.Revision++ }},
		{name: "Remote URL", change: func(value *domain.CredentialContext) {
			value.RemoteURL = "https://git.example.com/team/other.git"
		}},
		{name: "branch", change: func(value *domain.CredentialContext) { value.Branch = "release" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := contextValue
			test.change(&changed)
			if _, err := sealer.Open(envelope, changed); err == nil {
				t.Fatal("context drift must fail closed")
			}
		})
	}
}

func TestModelAndGitSealersRejectEachOthersEnvelopes(t *testing.T) {
	key := bytes.Repeat([]byte{8}, masterKeySize)
	gitSealer, err := NewCredentialSealerWithReader(key, bytes.NewReader(bytes.Repeat([]byte{9}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	modelSealer, err := modelcrypto.NewSealerWithReader(key, bytes.NewReader(bytes.Repeat([]byte{10}, 12)))
	if err != nil {
		t.Fatal(err)
	}

	gitContext := credentialContext()
	token, err := domain.NewToken("same-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	defer token.Destroy()
	gitEnvelope, err := gitSealer.Seal(token, gitContext)
	if err != nil {
		t.Fatal(err)
	}
	modelEnvelope := modeldomain.EncryptedSecret{KeyID: gitEnvelope.KeyID, Nonce: bytes.Clone(gitEnvelope.Nonce), Ciphertext: bytes.Clone(gitEnvelope.Ciphertext)}
	if _, err := modelSealer.Open(modelEnvelope, modelSecretContext()); err == nil {
		t.Fatal("model sealer must reject a Git envelope with the same master key")
	}

	modelSecret, err := modeldomain.NewSecret("same-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	defer modelSecret.Destroy()
	modelEnvelope, err = modelSealer.Seal(modelSecret, modelSecretContext())
	if err != nil {
		t.Fatal(err)
	}
	_, gitAADDigest, err := credentialAAD(gitContext)
	if err != nil {
		t.Fatal(err)
	}
	gitCredentialEnvelope := domain.EncryptedCredential{
		KeyID: modelEnvelope.KeyID, Nonce: bytes.Clone(modelEnvelope.Nonce), Ciphertext: bytes.Clone(modelEnvelope.Ciphertext), AADDigest: gitAADDigest,
	}
	if _, err := gitSealer.Open(gitCredentialEnvelope, gitContext); err == nil {
		t.Fatal("Git sealer must reject a Model Settings envelope with the same master key")
	}
}

func modelSecretContext() modeldomain.SecretContext {
	return modeldomain.SecretContext{
		Revision: 1, Purpose: modeldomain.SecretPurposeChat, SchemaVersion: modeldomain.SecretSchemaVersion,
		Provider: "openai-compatible", BaseURL: "https://models.example.test/v1",
	}
}

func TestCredentialSealerRejectsWrongKeyAndEntropyFailure(t *testing.T) {
	sealer, err := NewCredentialSealerWithReader(bytes.Repeat([]byte{3}, masterKeySize), bytes.NewReader(bytes.Repeat([]byte{4}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	token, _ := domain.NewToken("secret")
	defer token.Destroy()
	envelope, err := sealer.Seal(token, credentialContext())
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := NewCredentialSealer(bytes.Repeat([]byte{5}, masterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Open(envelope, credentialContext()); err == nil {
		t.Fatal("wrong key must fail closed")
	}
	broken, err := NewCredentialSealerWithReader(bytes.Repeat([]byte{6}, masterKeySize), bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Seal(token, credentialContext()); err == nil {
		t.Fatal("entropy failure must fail closed")
	}
}

func TestCredentialSealerFromFileRequiresPrivateBase64Key(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "git-sync.key")
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, masterKeySize)) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	sealer, err := NewCredentialSealerFromFile(path)
	if err != nil || sealer == nil {
		t.Fatalf("sealer=%#v err=%v", sealer, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	assertCredentialSecretUnavailable(t, func() error { _, loadErr := NewCredentialSealerFromFile(path); return loadErr }(), path)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "git-sync-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	assertCredentialSecretUnavailable(t, func() error { _, loadErr := NewCredentialSealerFromFile(link); return loadErr }(), link)
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString([]byte("short"))), 0o600); err != nil {
		t.Fatal(err)
	}
	assertCredentialSecretUnavailable(t, func() error { _, loadErr := NewCredentialSealerFromFile(path); return loadErr }(), path)
}

func TestCredentialSealerFromFileHidesMissingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git-remote-secret-never-expose.key")
	_, err := NewCredentialSealerFromFile(path)
	assertCredentialSecretUnavailable(t, err, path, filepath.Base(path))
}

func credentialContext() domain.CredentialContext {
	return domain.CredentialContext{
		WorkspaceID: securityTestWorkspace, Revision: 1, RemoteURL: "https://git.example.com/team/repo.git",
		Branch: "main", SchemaVersion: domain.CredentialSchemaVersion,
	}
}

func requireSecurityCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %#v, want code %s", err, code)
	}
}

func assertCredentialSecretUnavailable(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != domain.ErrorCodeSecretUnavailable {
		t.Fatalf("error=%v", err)
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("error leaks %q: %v", value, err)
		}
	}
}
