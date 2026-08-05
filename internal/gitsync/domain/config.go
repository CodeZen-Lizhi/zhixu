package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// RemoteConfig 是 Workspace 当前不含密钥的远端配置投影。
type RemoteConfig struct {
	WorkspaceID     foundation.ID
	Configured      bool
	RemoteURL       string
	Branch          string
	AutoSync        bool
	TokenConfigured bool
	Revision        int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Validate 校验已配置和规范未配置两种字段形状。
func (config RemoteConfig) Validate() error {
	if !validID(config.WorkspaceID) || config.Revision < 0 {
		return invalid(ErrorCodeInvalid, "remote configuration identity is invalid")
	}
	if !config.Configured {
		if config.RemoteURL != "" || config.Branch != "" || config.AutoSync || config.TokenConfigured {
			return invalid(ErrorCodeInvalid, "unconfigured remote contains active fields")
		}
		if config.Revision == 0 {
			return nil
		}
	} else if config.Revision == 0 || !validRemoteURL(config.RemoteURL) || !validBranch(config.Branch) {
		return invalid(ErrorCodeInvalid, "configured remote is invalid")
	}
	if config.CreatedAt.IsZero() || config.UpdatedAt.IsZero() || config.UpdatedAt.Before(config.CreatedAt) {
		return invalid(ErrorCodeInvalid, "remote configuration timestamps are invalid")
	}
	return nil
}

// ValidateConfigured 校验当前快照能否用于 Git 认证操作。
func (config RemoteConfig) ValidateConfigured() error {
	if err := config.Validate(); err != nil {
		return err
	}
	if !config.Configured {
		return foundation.NewError(foundation.ErrorNotFound, ErrorCodeConfigNotFound, false, errors.New("Git remote is not configured"))
	}
	if !config.TokenConfigured {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, ErrorCodeSecretUnavailable, false, errors.New("Git remote token is not configured"))
	}
	return nil
}

// CredentialBindingMatches 判断当前 Token 是否可在不替换凭据的情况下继续使用。
func (config RemoteConfig) CredentialBindingMatches(remoteURL, branch string) bool {
	return config.ValidateConfigured() == nil && config.RemoteURL == remoteURL && config.Branch == branch
}

func validRemoteURL(value string) bool {
	if !canonicalText(value, 2048) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.String() == value
}

func validBranch(value string) bool {
	if !canonicalText(value, 255) || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

// ValidateBranch 在进入 Git 适配器前校验完整分支名约束。
func ValidateBranch(value string) error {
	if !validBranch(value) {
		return invalid(ErrorCodeBranchInvalid, "Git branch is invalid")
	}
	return nil
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func invalid(code, message string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New(message))
}
