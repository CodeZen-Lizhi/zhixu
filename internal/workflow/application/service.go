// Package application coordinates durable workflow use cases.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// Service owns workflow application invariants and delegates atomic changes to Repository or RuntimeStarter.
type Service struct {
	repository       domain.Repository
	ids              foundation.IDGenerator
	clock            foundation.Clock
	definitions      *DefinitionRegistry
	runtime          RuntimeStarter
	coordinator      *RuntimeCoordinator
	humanCoordinator *RuntimeHumanCoordinator
}

// SubmitRuntimeHumanDecision completes a Human Node using its persisted Run binding.
func (s *Service) SubmitRuntimeHumanDecision(ctx context.Context, command HumanDecisionCommand) (HumanTransitionResult, error) {
	if s == nil || s.humanCoordinator == nil {
		return HumanTransitionResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_HUMAN_STATE_PORT_MISSING", false, errors.New("workflow human coordinator is unavailable"))
	}
	return s.humanCoordinator.SubmitHuman(ctx, command)
}

// Pause requests a durable workflow pause without accepting a Workspace override.
func (s *Service) Pause(ctx context.Context, command RunControlCommand) (RunControlResult, error) {
	if s == nil || s.coordinator == nil {
		return RunControlResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUNTIME_STATE_PORT_MISSING", false, errors.New("workflow runtime coordinator is unavailable"))
	}
	return s.coordinator.Pause(ctx, command)
}

// Resume restores runnable work for a paused workflow.
func (s *Service) Resume(ctx context.Context, command RunControlCommand) (RunControlResult, error) {
	if s == nil || s.coordinator == nil {
		return RunControlResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUNTIME_STATE_PORT_MISSING", false, errors.New("workflow runtime coordinator is unavailable"))
	}
	return s.coordinator.Resume(ctx, command)
}

// Cancel prevents new workflow work and converges the run at a safe checkpoint.
func (s *Service) Cancel(ctx context.Context, command RunControlCommand) (RunControlResult, error) {
	if s == nil || s.coordinator == nil {
		return RunControlResult{}, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUNTIME_STATE_PORT_MISSING", false, errors.New("workflow runtime coordinator is unavailable"))
	}
	return s.coordinator.Cancel(ctx, command)
}

// NewService constructs a workflow service.
func NewService(repository domain.Repository, ids foundation.IDGenerator, clock foundation.Clock) (*Service, error) {
	if repository == nil || ids == nil || clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_SERVICE_DEPENDENCY_MISSING", false, errors.New("workflow service dependency is nil"))
	}
	return &Service{repository: repository, ids: ids, clock: clock}, nil
}

// Get returns the persisted run state.
func (s *Service) Get(ctx context.Context, id foundation.ID) (domain.Run, error) {
	if id == "" {
		return domain.Run{}, invalid("WORKFLOW_RUN_ID_INVALID")
	}
	return s.repository.GetRun(ctx, id)
}

// ListRuns 返回按更新时间和 ID 倒序排列的 Workflow 摘要页。
func (s *Service) ListRuns(ctx context.Context, query domain.RunListQuery) ([]domain.RunListItem, bool, error) {
	if query.WorkspaceID == "" || query.Limit < 1 || query.Limit > 100 {
		return nil, false, foundation.NewError(foundation.ErrorInvalidInput, "WORKFLOW_LIST_INVALID", false, errors.New("invalid workflow list scope"))
	}
	repository, ok := s.repository.(domain.RunListRepository)
	if !ok {
		return nil, false, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_LIST_UNAVAILABLE", true, errors.New("workflow list repository is unavailable"))
	}
	return repository.ListRuns(ctx, query)
}

// Claim leases a pending node or reclaims an expired running node.
func (s *Service) Claim(ctx context.Context, nodeID foundation.ID, owner string, lease time.Duration) (domain.NodeRun, error) {
	if nodeID == "" || strings.TrimSpace(owner) == "" || lease <= 0 {
		return domain.NodeRun{}, invalid("WORKFLOW_LEASE_INVALID")
	}
	now := s.clock.Now()
	return s.repository.ClaimNode(ctx, nodeID, strings.TrimSpace(owner), now, now.Add(lease))
}

// Heartbeat extends a lease only for its current owner before expiration.
func (s *Service) Heartbeat(ctx context.Context, nodeID foundation.ID, owner string, lease time.Duration) (domain.NodeRun, error) {
	if nodeID == "" || strings.TrimSpace(owner) == "" || lease <= 0 {
		return domain.NodeRun{}, invalid("WORKFLOW_LEASE_INVALID")
	}
	now := s.clock.Now()
	return s.repository.HeartbeatNode(ctx, nodeID, strings.TrimSpace(owner), now, now.Add(lease))
}

// Complete idempotently completes a node while its lease is valid.
func (s *Service) Complete(ctx context.Context, nodeID foundation.ID, owner string, output json.RawMessage, workspaceID, runID foundation.ID) (domain.NodeRun, error) {
	if nodeID == "" || workspaceID == "" || runID == "" || strings.TrimSpace(owner) == "" || !validJSON(output) {
		return domain.NodeRun{}, invalid("WORKFLOW_COMPLETION_INVALID")
	}
	eventID, err := s.ids.New()
	if err != nil {
		return domain.NodeRun{}, err
	}
	now := s.clock.Now()
	return s.repository.CompleteNode(ctx, domain.Completion{NodeID: nodeID, LeaseOwner: strings.TrimSpace(owner), Output: normalizedJSON(output), At: now, Event: domain.OutboxEvent{ID: eventID, WorkspaceID: workspaceID, RunID: &runID, Type: "workflow.node.succeeded", IdempotencyKey: "node-complete:" + string(nodeID), Payload: json.RawMessage(`{}`), OccurredAt: now}})
}

// CreateHumanTask moves a running node/run into the human wait state.
func (s *Service) CreateHumanTask(ctx context.Context, task domain.HumanTask, workspaceID foundation.ID) (domain.HumanTask, error) {
	if task.ID == "" || task.RunID == "" || task.NodeRunID == "" || workspaceID == "" || task.TargetVersion < 1 || !objectJSON(task.ExpectedInputSchema) {
		return domain.HumanTask{}, invalid("HUMAN_TASK_INVALID")
	}
	eventID, err := s.ids.New()
	if err != nil {
		return domain.HumanTask{}, err
	}
	now := s.clock.Now()
	task.Status = domain.HumanTaskPending
	task.CreatedAt = now
	return s.repository.CreateHumanTask(ctx, task, domain.OutboxEvent{ID: eventID, WorkspaceID: workspaceID, RunID: &task.RunID, Type: "workflow.human.requested", IdempotencyKey: "human-task:" + string(task.ID), Payload: json.RawMessage(`{}`), OccurredAt: now}, now)
}

// SubmitHumanDecision atomically accepts a matching, unexpired Human Task once.
func (s *Service) SubmitHumanDecision(ctx context.Context, taskID foundation.ID, targetVersion int64, decision json.RawMessage, workspaceID, runID foundation.ID) (domain.HumanTask, error) {
	if taskID == "" || workspaceID == "" || runID == "" || targetVersion < 1 || !objectJSON(decision) {
		return domain.HumanTask{}, invalid("HUMAN_DECISION_INVALID")
	}
	eventID, err := s.ids.New()
	if err != nil {
		return domain.HumanTask{}, err
	}
	now := s.clock.Now()
	return s.repository.SubmitHumanTask(ctx, taskID, targetVersion, decision, now, domain.OutboxEvent{ID: eventID, WorkspaceID: workspaceID, RunID: &runID, Type: "workflow.human.submitted", IdempotencyKey: "human-submit:" + string(taskID), Payload: json.RawMessage(`{}`), OccurredAt: now})
}

func invalid(code string) error {
	return foundation.NewError(foundation.ErrorInvalidInput, code, false, errors.New("invalid workflow input"))
}
func validJSON(value json.RawMessage) bool { return len(value) == 0 || json.Valid(value) }
func objectJSON(value json.RawMessage) bool {
	if !json.Valid(value) {
		return false
	}
	var v any
	return json.Unmarshal(value, &v) == nil && func() bool { _, ok := v.(map[string]any); return ok }()
}
func normalizedJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}
