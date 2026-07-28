// Package memory maps effective Memory application facts into non-evidence Interview context.
package memory

import (
	"context"
	"encoding/json"

	memoryapp "github.com/CodeZen-Lizhi/zhixu/internal/memory/application"
	memorydomain "github.com/CodeZen-Lizhi/zhixu/internal/memory/domain"
	interviewapp "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/application"
	interviewdomain "github.com/CodeZen-Lizhi/zhixu/internal/review/interview/domain"
)

// Loader binds Interview context reads to Memory's stable single-user owner.
type Loader struct {
	service *memoryapp.Service
	owner   memorydomain.Principal
}

// NewLoader constructs the only production mapping from effective Memory to Interview context.
func NewLoader(service *memoryapp.Service, owner memorydomain.Principal) (*Loader, error) {
	if service == nil || memorydomain.ValidatePrincipal(owner) != nil || owner != memorydomain.SingleUserOwner() {
		return nil, interviewdomain.UnavailableError(interviewdomain.ErrorCodeDependencyUnavailable, "effective memory context dependency is unavailable")
	}
	return &Loader{service: service, owner: owner}, nil
}

// Load reads ACTIVE, confirmed, unexpired Memory through the application service.
// Preference and contextual documents remain separate from Question Evidence.
func (loader *Loader) Load(ctx context.Context, query interviewapp.ContextQuery) (interviewapp.PersonalContext, error) {
	if loader == nil || loader.service == nil || loader.owner != memorydomain.SingleUserOwner() {
		return interviewapp.PersonalContext{}, interviewdomain.UnavailableError(interviewdomain.ErrorCodeDependencyUnavailable, "effective memory context dependency is unavailable")
	}
	taskScopeID := query.TaskScopeID
	items, err := loader.service.LoadEffective(ctx, memoryapp.EffectiveQuery{
		Scope: memoryapp.Scope{
			WorkspaceID: query.WorkspaceID,
			Owner:       loader.owner,
		},
		TaskScopeID: &taskScopeID,
		Limit:       query.Limit,
	})
	if err != nil {
		return interviewapp.PersonalContext{}, err
	}
	result := interviewapp.PersonalContext{
		Preferences: make([]json.RawMessage, 0, len(items)),
		Context:     make([]json.RawMessage, 0, len(items)),
	}
	for _, item := range items {
		content := append(json.RawMessage(nil), item.Content...)
		if item.Type == memorydomain.TypePreference {
			result.Preferences = append(result.Preferences, content)
			continue
		}
		result.Context = append(result.Context, content)
	}
	return result, nil
}

var _ interviewapp.ContextLoader = (*Loader)(nil)
