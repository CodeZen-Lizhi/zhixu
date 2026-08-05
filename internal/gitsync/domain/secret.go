package domain

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// CredentialPurpose 将 Git Token 与其他使用同一加密原语的 Secret 隔离。
	CredentialPurpose = "git-remote-token"
	// CredentialSchemaVersion 将加密 Git Token 绑定到 v1 AAD 结构。
	CredentialSchemaVersion = "git-remote-token/v1"
	maxTokenBytes           = 16 * 1024
)

// Token 持有短生命周期远端凭据，并始终以脱敏形式格式化。
type Token struct{ value []byte }

// NewToken 校验并复制临时远端 Token。
func NewToken(value string) (Token, error) {
	return TokenFromBytes([]byte(value))
}

// TokenFromBytes 校验并复制临时凭据字节。
func TokenFromBytes(value []byte) (Token, error) {
	if len(value) == 0 || len(value) > maxTokenBytes || !utf8.Valid(value) || !bytes.Equal(value, bytes.TrimSpace(value)) {
		return Token{}, secretInvalid("remote token is invalid")
	}
	for remaining := value; len(remaining) > 0; {
		character, size := utf8.DecodeRune(remaining)
		if character < 0x21 || character == 0x7f {
			return Token{}, secretInvalid("remote token is invalid")
		}
		remaining = remaining[size:]
	}
	return Token{value: bytes.Clone(value)}, nil
}

// Configured 报告 Token 是否包含凭据。
func (token Token) Configured() bool { return len(token.value) > 0 }

// Bytes 为凭据会话边界返回防御性副本。
func (token Token) Bytes() []byte { return bytes.Clone(token.value) }

// Destroy 清除自身持有的临时缓冲区。
func (token *Token) Destroy() {
	if token == nil {
		return
	}
	for index := range token.value {
		token.value[index] = 0
	}
	token.value = nil
}

func (token Token) String() string   { return "<redacted>" }
func (token Token) GoString() string { return "domain.Token(<redacted>)" }

// SecretActionKind 标识显式只写 Token 更新动作。
type SecretActionKind string

const (
	// SecretActionKeep 为下一 Revision 重新加密当前 Token。
	SecretActionKeep SecretActionKind = "keep"
	// SecretActionReplace 保存调用方提供的替换 Token。
	SecretActionReplace SecretActionKind = "replace"
	// SecretActionClear 删除已配置 Token。
	SecretActionClear SecretActionKind = "clear"
)

// SecretAction 避免空字符串 Token 更新的歧义。
type SecretAction struct {
	Kind  SecretActionKind
	Value Token
}

// KeepSecret 创建显式保留动作。
func KeepSecret() SecretAction { return SecretAction{Kind: SecretActionKeep} }

// ClearSecret 创建显式清除动作。
func ClearSecret() SecretAction { return SecretAction{Kind: SecretActionClear} }

// ReplaceSecret 创建已校验的替换动作。
func ReplaceSecret(value string) (SecretAction, error) {
	token, err := NewToken(value)
	if err != nil {
		return SecretAction{}, err
	}
	return SecretAction{Kind: SecretActionReplace, Value: token}, nil
}

// Validate 在不暴露 Token 的前提下校验动作和值的形状。
func (action SecretAction) Validate() error {
	switch action.Kind {
	case SecretActionKeep, SecretActionClear:
		if action.Value.Configured() {
			return secretActionInvalid("keep and clear must not carry a token")
		}
	case SecretActionReplace:
		if !action.Value.Configured() {
			return secretActionInvalid("replace requires a token")
		}
	default:
		return secretActionInvalid("remote token action is invalid")
	}
	return nil
}

func (action SecretAction) String() string {
	return fmt.Sprintf("SecretAction{kind:%q value_configured:%t}", action.Kind, action.Value.Configured())
}
func (action SecretAction) GoString() string { return action.String() }

// CredentialContext 会序列化为附加认证数据。
type CredentialContext struct {
	WorkspaceID   foundation.ID
	Revision      int64
	RemoteURL     string
	Branch        string
	SchemaVersion string
}

// Validate 校验不可变凭据绑定。
func (context CredentialContext) Validate() error {
	if !validID(context.WorkspaceID) || context.Revision <= 0 || context.SchemaVersion != CredentialSchemaVersion ||
		!validRemoteURL(context.RemoteURL) || !validBranch(context.Branch) {
		return secretInvalid("remote credential context is invalid")
	}
	return nil
}

// EncryptedCredential 可安全持久化，但不得通过 HTTP 暴露。
type EncryptedCredential struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
	AADDigest  string
}

// Configured 报告完整加密信封是否存在。
func (credential EncryptedCredential) Configured() bool {
	return credential.KeyID != "" && len(credential.Nonce) > 0 && len(credential.Ciphertext) > 0 && credential.AADDigest != ""
}

// Validate 校验已配置或完全为空的加密信封。
func (credential EncryptedCredential) Validate() error {
	allEmpty := credential.KeyID == "" && len(credential.Nonce) == 0 && len(credential.Ciphertext) == 0 && credential.AADDigest == ""
	if allEmpty {
		return nil
	}
	if !canonicalText(credential.KeyID, 128) || len(credential.Nonce) < 12 || len(credential.Nonce) > 32 ||
		len(credential.Ciphertext) < 17 || len(credential.Ciphertext) > maxTokenBytes+256 || !validHash(credential.AADDigest) {
		return secretInvalid("encrypted remote credential is invalid")
	}
	return nil
}

func (credential EncryptedCredential) String() string {
	return fmt.Sprintf("EncryptedCredential{configured:%t}", credential.Configured())
}
func (credential EncryptedCredential) GoString() string { return credential.String() }

func secretInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSecretUnavailable, false, errors.New(message))
}

func secretActionInvalid(message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, ErrorCodeSecretActionInvalid, false, errors.New(message))
}

func canonicalText(value string, maximum int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
