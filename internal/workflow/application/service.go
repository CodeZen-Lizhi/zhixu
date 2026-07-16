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

// Service owns workflow application invariants and delegates atomic changes to Repository.
type Service struct {
	repository domain.Repository
	ids        foundation.IDGenerator
	clock      foundation.Clock
}

// NewService constructs a workflow service.
func NewService(repository domain.Repository, ids foundation.IDGenerator, clock foundation.Clock) (*Service, error) {
	if repository == nil || ids == nil || clock == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_SERVICE_DEPENDENCY_MISSING", false, errors.New("workflow service dependency is nil"))
	}
	return &Service{repository: repository, ids: ids, clock: clock}, nil
}

// StartCommand starts a deterministic definition at its first node.
type StartCommand struct {
	WorkspaceID                 foundation.ID
	DefinitionKey               string
	DefinitionVersion           int64
	Graph, Input                json.RawMessage
	FirstNodeKey, FirstNodeType string
	IdempotencyKey              string
}

// Start persists the immutable definition, run, first node and outbox event atomically.
func (s *Service) Start(ctx context.Context, command StartCommand) (domain.Run, error) {
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	if command.WorkspaceID == "" || strings.TrimSpace(command.DefinitionKey) == "" || command.DefinitionVersion < 1 || strings.TrimSpace(command.FirstNodeKey) == "" || strings.TrimSpace(command.FirstNodeType) == "" || idempotencyKey == "" || len(idempotencyKey) > 128 || !objectJSON(command.Graph) || !validJSON(command.Input) {
		return domain.Run{}, invalid("WORKFLOW_START_INVALID")
	}
	definitionID, err := s.ids.New()
	if err != nil {
		return domain.Run{}, err
	}
	runID, err := s.ids.New()
	if err != nil {
		return domain.Run{}, err
	}
	nodeID, err := s.ids.New()
	if err != nil {
		return domain.Run{}, err
	}
	eventID, err := s.ids.New()
	if err != nil {
		return domain.Run{}, err
	}
	now := s.clock.Now()
	request := domain.StartRequest{
		Definition: domain.Definition{ID: definitionID, WorkspaceID: command.WorkspaceID, Key: strings.TrimSpace(command.DefinitionKey), Version: command.DefinitionVersion, Graph: command.Graph, CreatedAt: now},
		Run:        domain.Run{ID: runID, WorkspaceID: command.WorkspaceID, DefinitionID: definitionID, Status: domain.StatusPending, Input: normalizedJSON(command.Input), Version: 1, CreatedAt: now, UpdatedAt: now},
		FirstNode:  domain.NodeRun{ID: nodeID, RunID: runID, NodeKey: strings.TrimSpace(command.FirstNodeKey), NodeType: strings.TrimSpace(command.FirstNodeType), Status: domain.StatusPending, Input: normalizedJSON(command.Input), Version: 1, CreatedAt: now, UpdatedAt: now},
		Event:      domain.OutboxEvent{ID: eventID, WorkspaceID: command.WorkspaceID, RunID: &runID, Type: "workflow.run.started", IdempotencyKey: "workflow-start:" + string(command.WorkspaceID) + ":" + idempotencyKey, Payload: json.RawMessage(`{}`), OccurredAt: now},
	}
	return s.repository.Start(ctx, request)
}

// Get returns the persisted run state.
func (s *Service) Get(ctx context.Context, id foundation.ID) (domain.Run, error) {
	if id == "" {
		return domain.Run{}, invalid("WORKFLOW_RUN_ID_INVALID")
	}
	return s.repository.GetRun(ctx, id)
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
