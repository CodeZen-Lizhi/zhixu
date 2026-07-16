// Package application coordinates Workspace use cases through project-owned
// domain interfaces.
package application

import (
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// GitSetupAction describes the explicit next step for Workspace creation.
type GitSetupAction string

const (
	// GitSetupUseExisting records the repository baseline without mutation.
	GitSetupUseExisting GitSetupAction = "use_existing"
	// GitSetupInitialize requires an injected initializer before persistence.
	GitSetupInitialize GitSetupAction = "initialize"
)

// DecideGitSetup enforces the v1 requirement that every Workspace has Git.
// It never treats an absent repository as a successful baseline.
func DecideGitSetup(status domain.GitStatus, initializeAllowed bool) (GitSetupAction, error) {
	if status.Present {
		return GitSetupUseExisting, nil
	}
	if initializeAllowed {
		return GitSetupInitialize, nil
	}
	return "", foundation.NewError(
		foundation.ErrorInvalidInput,
		"GIT_REQUIRED",
		false,
		errors.New("workspace requires a git repository"),
	)
}
