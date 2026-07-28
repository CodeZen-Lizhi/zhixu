// Package artifact bridges Learning Path drafts to the Artifact owner without leaking Artifact commands into Review.
package artifact

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/review/learningpath/domain"
)

// HiddenDraftCreator is the narrow Artifact-owned operation required by a
// Review-created Learning Path. Implementations must atomically create the
// LEARNING_PATH draft and its LEARNING_PATH_CREATE/PATH hidden hold.
type HiddenDraftCreator interface {
	CreateLearningPathDraft(context.Context, application.DraftRequest) (domain.ArtifactBinding, error)
}

// Bridge keeps the Review module independent from Artifact command details.
type Bridge struct{ creator HiddenDraftCreator }

// NewBridge constructs a fail-closed Learning Path Artifact bridge.
func NewBridge(creator HiddenDraftCreator) (*Bridge, error) {
	if creator == nil {
		return nil, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "learning path artifact creator is required")
	}
	return &Bridge{creator: creator}, nil
}

// CreateDraft delegates a fully server-constructed request to the Artifact owner.
func (bridge *Bridge) CreateDraft(ctx context.Context, request application.DraftRequest) (domain.ArtifactBinding, error) {
	if bridge == nil || bridge.creator == nil {
		return domain.ArtifactBinding{}, domain.UnavailableError(domain.ErrorCodeDependencyUnavailable, "learning path artifact bridge is unavailable")
	}
	return bridge.creator.CreateLearningPathDraft(ctx, request)
}
