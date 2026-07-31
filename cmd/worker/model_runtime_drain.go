package main

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	modelsettingsruntime "github.com/CodeZen-Lizhi/zhixu/internal/modelsettings/runtime"
)

const workerDrainCompensationTimeout = 10 * time.Second

// modelDispatcherLifecycle 是模型 rollout 可暂停并恢复的本地 Reindex producer。
type modelDispatcherLifecycle interface {
	dispatcherLifecycle
	Started() bool
}

type workerQueueLifecycle interface {
	PauseQueue(context.Context) error
	ResumeQueue(context.Context) error
}

// workerModelDrain 统一关闭 Reindex 与周期 producer，并等待已 claim River job 结束。
type workerModelDrain struct {
	// dispatcher 是唯一会主动创建 Reindex delivery 的后台循环。
	dispatcher modelDispatcherLifecycle
	queue      workerQueueLifecycle
	// resumeContext 绑定 Worker 进程生命周期，禁止恢复到独立后台 context。
	resumeContext context.Context
	// runningJobCount 读取已 claim River job 的数据库事实。
	runningJobCount func(context.Context) (int64, error)
	// onDispatcherState 同步 Worker readiness，不参与业务判定。
	onDispatcherState func(bool)
	// producersEnabled 是周期维护循环读取的本地快速 gate。
	producersEnabled atomic.Bool
}

// newWorkerModelDrain 创建初始允许 producer 的排空控制器。
func newWorkerModelDrain(
	dispatcher modelDispatcherLifecycle,
	queue workerQueueLifecycle,
	resumeContext context.Context,
	runningJobCount func(context.Context) (int64, error),
	onDispatcherState func(bool),
) (*workerModelDrain, error) {
	if nilLifecycleDependency(dispatcher) || nilLifecycleDependency(queue) || resumeContext == nil || runningJobCount == nil {
		return nil, errors.New("worker model drain dependencies are unavailable")
	}
	drain := &workerModelDrain{
		dispatcher: dispatcher, queue: queue, resumeContext: resumeContext,
		runningJobCount: runningJobCount, onDispatcherState: onDispatcherState,
	}
	drain.producersEnabled.Store(true)
	return drain, nil
}

// Hooks 返回 Model Runtime Controller 使用的排空边界。
func (drain *workerModelDrain) Hooks() modelsettingsruntime.DrainHooks {
	if drain == nil {
		return modelsettingsruntime.DrainHooks{}
	}
	return modelsettingsruntime.DrainHooks{
		Begin: drain.Begin, IsQuiesced: drain.IsQuiesced, Resume: drain.Resume,
	}
}

// Begin 先阻止周期 producer，再停止会主动入队的 Reindex dispatcher。
func (drain *workerModelDrain) Begin(ctx context.Context) error {
	if drain == nil || ctx == nil {
		return errors.New("worker model drain is unavailable")
	}
	drain.producersEnabled.Store(false)
	drain.reportDispatcherState(false)
	if err := drain.queue.PauseQueue(ctx); err != nil {
		return drain.compensateFailedBegin(ctx, err)
	}
	if err := drain.dispatcher.Stop(ctx); err != nil {
		return drain.compensateFailedBegin(ctx, err)
	}
	if drain.dispatcher.Started() {
		return drain.compensateFailedBegin(ctx, errors.New("worker model dispatcher remained active after drain"))
	}
	return nil
}

func (drain *workerModelDrain) compensateFailedBegin(ctx context.Context, cause error) error {
	compensationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerDrainCompensationTimeout)
	defer cancel()
	var compensationErr error
	if !drain.dispatcher.Started() {
		compensationErr = errors.Join(compensationErr, drain.dispatcher.Start(drain.resumeContext))
	}
	compensationErr = errors.Join(compensationErr, drain.queue.ResumeQueue(compensationContext))
	if compensationErr == nil && drain.dispatcher.Started() {
		drain.producersEnabled.Store(true)
		drain.reportDispatcherState(true)
	}
	return errors.Join(cause, compensationErr)
}

// IsQuiesced 要求本地 producer 已停且数据库中没有正在执行的 River job。
func (drain *workerModelDrain) IsQuiesced(ctx context.Context) (bool, error) {
	if drain == nil || ctx == nil {
		return false, errors.New("worker model drain is unavailable")
	}
	if drain.producersEnabled.Load() || drain.dispatcher.Started() {
		return false, nil
	}
	running, err := drain.runningJobCount(ctx)
	if err != nil {
		return false, err
	}
	return running == 0, nil
}

// Resume 仅在 Controller 已确认旧 revision 仍获授权后恢复本地 producer。
func (drain *workerModelDrain) Resume(ctx context.Context) error {
	if drain == nil || ctx == nil {
		return errors.New("worker model drain is unavailable")
	}
	if drain.producersEnabled.Load() {
		return nil
	}
	if err := drain.dispatcher.Start(drain.resumeContext); err != nil {
		return err
	}
	if !drain.dispatcher.Started() {
		return errors.New("worker model dispatcher did not resume")
	}
	if err := drain.queue.ResumeQueue(ctx); err != nil {
		_ = drain.dispatcher.Stop(context.WithoutCancel(ctx))
		return err
	}
	drain.producersEnabled.Store(true)
	drain.reportDispatcherState(true)
	return nil
}

// ProducersEnabled 报告周期 producer 是否仍可开始新一轮工作。
func (drain *workerModelDrain) ProducersEnabled() bool {
	return drain != nil && drain.producersEnabled.Load()
}

// reportDispatcherState 同步可选的 readiness 投影。
func (drain *workerModelDrain) reportDispatcherState(started bool) {
	if drain.onDispatcherState != nil {
		drain.onDispatcherState(started)
	}
}
