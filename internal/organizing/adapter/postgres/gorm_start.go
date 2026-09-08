package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation/strictjson"
	organizingapp "github.com/CodeZen-Lizhi/zhixu/internal/organizing/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/organizing/domain"
	organizingworkflow "github.com/CodeZen-Lizhi/zhixu/internal/organizing/workflow"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapp "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"gorm.io/gorm"
)

// GORMStartRepository 将 Organizing Outbox 与 Workflow/River Start 置于同一事务。
type GORMStartRepository struct {
	*GORMRepository
	workflows workflowapp.ScopedRuntimeStarter
	bindings  workflowapp.ScopedRuntimeBindingReader
}

// NewGORMStartRepository 注入 Workflow owner 的 scoped 启动与运行身份读取能力。
func NewGORMStartRepository(pool *platformpostgres.Pool, workflows workflowapp.ScopedRuntimeStarter, bindings workflowapp.ScopedRuntimeBindingReader) (*GORMStartRepository, error) {
	if isNilInterface(workflows) || isNilInterface(bindings) {
		return nil, unavailable(errors.New("organizing scoped workflow dependencies are unavailable"))
	}
	repository, err := NewGORMRepository(pool)
	if err != nil {
		return nil, err
	}
	return &GORMStartRepository{GORMRepository: repository, workflows: workflows, bindings: bindings}, nil
}

// StartWorkflow 原子启动/重放 Workflow、验证 owner identity 并完成精确 Outbox lease。
func (repository *GORMStartRepository) StartWorkflow(ctx context.Context, record organizingworkflow.AtomicStartRecord) (domain.RunBinding, bool, error) {
	if repository == nil || isNilInterface(repository.workflows) || isNilInterface(repository.bindings) {
		return domain.RunBinding{}, false, unavailable(errors.New("organizing scoped workflow start is unavailable"))
	}
	if err := repository.validateReady(ctx); err != nil {
		return domain.RunBinding{}, false, err
	}
	if err := validateStartLease(record.Lease); err != nil || !validID(record.BindingID) || record.StartedAt.IsZero() {
		return domain.RunBinding{}, false, invalid(errors.New("organizing atomic start record is invalid"))
	}
	key, version := definitionForTemplateKind(record.Lease.TemplateKind)
	if record.Request.Definition.Key != key || record.Request.Definition.Version != version ||
		record.Request.Run.WorkspaceID != record.Lease.WorkspaceID || record.Request.Run.IdempotencyKey != organizingapp.StartIdempotencyKey(record.Lease.SnapshotID) ||
		!gormSnapshotInputMatches(record.Request.Run.Input, record.Lease.SnapshotID) {
		return domain.RunBinding{}, false, invalid(errors.New("organizing atomic start request does not match its lease"))
	}
	var result domain.RunBinding
	var replayed bool
	err := repository.within(ctx, foundation.TransactionOptions{}, func(ctx context.Context, scope foundation.TransactionScope, tx *gorm.DB) error {
		_, found, err := gormLoadRunBinding(tx, record.Lease.WorkspaceID, record.Lease.SnapshotID, true)
		if err != nil {
			return err
		}
		if !found {
			matched, err := gormMatchStartLease(tx, record.Lease)
			if err != nil {
				return err
			}
			if !matched {
				return leaseLost(errors.New("organizing start outbox lease was lost"))
			}
		}
		started, err := repository.workflows.StartScoped(ctx, scope, record.Request)
		if err != nil {
			return err
		}
		if !validID(started.Run.ID) || started.Run.WorkspaceID != record.Lease.WorkspaceID || started.FirstNode.RunID != started.Run.ID ||
			started.Run.IdempotencyKey != record.Request.Run.IdempotencyKey || started.Run.RequestHash != record.Request.RequestHash ||
			!gormSnapshotInputMatches(started.Run.Input, record.Lease.SnapshotID) {
			return inconsistent(errors.New("organizing workflow start returned an invalid binding"))
		}
		binding, err := repository.bindings.LoadRuntimeBindingScoped(ctx, scope, workflowapp.ScopedRuntimeBindingQuery{
			WorkspaceID: record.Lease.WorkspaceID, WorkflowRunID: started.Run.ID, NodeRunID: started.FirstNode.ID,
		})
		if err != nil {
			return err
		}
		if binding.DefinitionKey != key || binding.DefinitionVersion != version || !gormSnapshotInputMatches(binding.RunInput, record.Lease.SnapshotID) ||
			!gormSnapshotInputMatches(binding.NodeInput, record.Lease.SnapshotID) {
			return inconsistent(errors.New("organizing workflow owner facts do not match its snapshot"))
		}
		result, replayed, err = gormCompleteStart(tx, organizingapp.CompleteStartRecord{Lease: record.Lease, BindingID: record.BindingID,
			WorkflowRunID: started.Run.ID, DefinitionKey: key, DefinitionVersion: version, StartedAt: record.StartedAt})
		return err
	})
	if err != nil {
		return domain.RunBinding{}, false, err
	}
	return result, replayed, nil
}

func gormSnapshotInputMatches(raw []byte, snapshotID foundation.ID) bool {
	input, err := strictjson.DecodeObject[organizingworkflow.StartInput](raw, strictjson.Limits{MaxDocumentBytes: 256, MaxDepth: 2, MaxStringBytes: 64, MaxArrayItems: 1, MaxObjectFields: 1}, nil)
	return err == nil && input.SnapshotID == snapshotID
}

var _ organizingworkflow.AtomicStartRepository = (*GORMStartRepository)(nil)
