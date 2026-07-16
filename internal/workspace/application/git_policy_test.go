package application

import (
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestDecideGitSetupUsesExistingRepository(t *testing.T) {
	action, err := DecideGitSetup(domain.GitStatus{Present: true}, false)
	if err != nil || action != GitSetupUseExisting {
		t.Fatalf("action = %q, error = %v", action, err)
	}
}

func TestDecideGitSetupRequiresExplicitInitialization(t *testing.T) {
	action, err := DecideGitSetup(domain.GitStatus{}, true)
	if err != nil || action != GitSetupInitialize {
		t.Fatalf("action = %q, error = %v", action, err)
	}
}

func TestDecideGitSetupRejectsAbsentRepositoryWhenInitializationDeclined(t *testing.T) {
	_, err := DecideGitSetup(domain.GitStatus{}, false)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != foundation.ErrorInvalidInput || classified.Code != "GIT_REQUIRED" || classified.Retryable {
		t.Fatalf("error = %#v", err)
	}
}
