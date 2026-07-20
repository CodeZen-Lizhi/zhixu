package application

import (
	"context"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// ModelRunTxFinalizer 允许跨领域调用方在其拥有的数据库事务内复用 Agent 终结规则。
// transaction 必须是 Adapter 支持的真实事务；该端口不提交或回滚事务。
type ModelRunTxFinalizer interface {
	// GetModelRunTx 在调用方事务内读取并可选锁定 Model Run。
	GetModelRunTx(context.Context, any, foundation.ID, foundation.ID, bool) (domain.ModelRun, error)
	// GetModelRunByAttemptTx 按唯一 Node Attempt 查找 Model Run；不存在时返回 found=false。
	GetModelRunByAttemptTx(context.Context, any, foundation.ID, foundation.ID, bool) (domain.ModelRun, bool, error)
	// FinalizeModelRunTx 以 CAS 终结 Model Run，并返回是否为精确重放。
	FinalizeModelRunTx(context.Context, any, FinalizeModelRunCommand) (domain.ModelRun, bool, error)
}

// ModelRunRecord 返回 Model Run 及按 call_no 排序的全部调用历史。
type ModelRunRecord struct {
	Run   domain.ModelRun
	Calls []domain.ModelCall
}

// CompleteModelCallCommand 以乐观锁归约一次已持久化的 STARTED 调用。
type CompleteModelCallCommand struct {
	WorkspaceID     foundation.ID
	ExpectedVersion int64
	Call            domain.ModelCall
}

// FinalizeModelRunCommand 以乐观锁把 RUNNING Model Run 归约到唯一终态。
type FinalizeModelRunCommand struct {
	ExpectedVersion int64
	Run             domain.ModelRun
}

// UnknownRecoveryQuery 是 crash 恢复的有界扫描条件。
type UnknownRecoveryQuery struct {
	Before time.Time
	At     time.Time
	Limit  int
}

// ModelRunRepository 是 Agent 模型运行历史的唯一持久化边界。
type ModelRunRepository interface {
	// CreateModelRun 在首次 Provider 调用前创建 RUNNING 事实；同 Node Attempt 精确重放返回既有记录。
	CreateModelRun(context.Context, domain.ModelRun) (domain.ModelRun, bool, error)
	// GetModelRun 按 Workspace 返回 Run 和稳定排序的全部 Calls。
	GetModelRun(context.Context, foundation.ID, foundation.ID) (ModelRunRecord, error)
	// StartModelCall 在 Provider 请求前持久化 STARTED；同 run/call_no 精确重放返回既有记录。
	StartModelCall(context.Context, foundation.ID, domain.ModelCall) (domain.ModelCall, bool, error)
	// CompleteModelCall 使用 CAS 把 STARTED 归约为 SUCCEEDED、FAILED 或 UNKNOWN。
	CompleteModelCall(context.Context, CompleteModelCallCommand) (domain.ModelCall, bool, error)
	// FinalizeModelRun 使用 CAS 把 RUNNING 归约为 SUCCEEDED、REFUSED、FAILED 或 UNKNOWN。
	FinalizeModelRun(context.Context, FinalizeModelRunCommand) (domain.ModelRun, bool, error)
	// MarkStaleModelCallsUnknown 批量把 crash 后遗留的 STARTED 调用归约为 UNKNOWN。
	MarkStaleModelCallsUnknown(context.Context, UnknownRecoveryQuery) ([]domain.ModelCall, error)
	// MarkStaleModelRunsUnknown 批量把没有活动调用的过期 RUNNING 归约为 UNKNOWN。
	MarkStaleModelRunsUnknown(context.Context, UnknownRecoveryQuery) ([]domain.ModelRun, error)
}
