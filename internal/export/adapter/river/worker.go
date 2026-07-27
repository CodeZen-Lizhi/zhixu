package river

import (
	"context"
	"errors"
	"fmt"
	"strings"

	exportapp "github.com/CodeZen-Lizhi/zhixu/internal/export/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	riverlib "github.com/riverqueue/river"
)

type executor interface {
	Execute(context.Context, foundation.ID, foundation.ID, string) error
}

// Worker 将 River delivery 归约到 ops.export_job 事实源。
type Worker struct {
	riverlib.WorkerDefaults[Args]
	executor executor
	owner    string
}

var (
	_ riverlib.Worker[Args] = (*Worker)(nil)
	_ executor              = (*exportapp.Service)(nil)
)

// NewWorker 创建带稳定 owner 前缀的 Export Worker。
func NewWorker(executor executor, owner string) (*Worker, error) {
	owner = strings.TrimSpace(owner)
	if executor == nil || owner == "" || len(owner) > 80 || strings.ContainsAny(owner, "\r\n\t/") {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, "EXPORT_RIVER_WORKER_INVALID", false, errors.New("export River worker dependencies are invalid"))
	}
	return &Worker{executor: executor, owner: owner}, nil
}

// Work 校验持久 Args 与 trace metadata 后执行一次带租约的导出生成。
func (worker *Worker) Work(ctx context.Context, job *riverlib.Job[Args]) error {
	if worker == nil || worker.executor == nil {
		return foundation.NewError(foundation.ErrorDependencyUnavailable, "EXPORT_RIVER_WORKER_UNAVAILABLE", true, errors.New("export River worker is unavailable"))
	}
	if ctx == nil || job == nil || job.JobRow == nil || job.ID < 1 || job.Attempt < 1 {
		return riverlib.JobCancel(foundation.NewError(foundation.ErrorInvalidInput, "EXPORT_RIVER_JOB_INVALID", false, errors.New("export River job row is invalid")))
	}
	if err := ValidateArgs(job.Args); err != nil {
		return riverlib.JobCancel(err)
	}
	if err := ValidateEncodedArgs(job.EncodedArgs, job.Args); err != nil {
		return riverlib.JobCancel(err)
	}
	decoded, err := workflowriver.DecodeTraceMetadata(ctx, job.Metadata)
	if err != nil {
		return riverlib.JobCancel(err)
	}
	leaseOwner := fmt.Sprintf("%s:river:%d:attempt:%d", worker.owner, job.ID, job.Attempt)
	return worker.executor.Execute(decoded, job.Args.WorkspaceID, job.Args.ExportID, leaseOwner)
}
