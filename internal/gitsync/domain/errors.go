// Package domain 定义 Git 远端配置与同步不变量。
package domain

const (
	// ErrorCodeInvalid 标识格式错误的 Git 同步输入或状态。
	ErrorCodeInvalid = "GIT_SYNC_INVALID"
	// ErrorCodeUnavailable 标识不可用的 Git 同步依赖。
	ErrorCodeUnavailable = "GIT_SYNC_UNAVAILABLE"
	// ErrorCodeConfigNotFound 标识没有活动远端配置的 Workspace。
	ErrorCodeConfigNotFound = "GIT_REMOTE_NOT_CONFIGURED"
	// ErrorCodeConfigRevisionConflict 标识过期配置 Revision。
	ErrorCodeConfigRevisionConflict = "GIT_REMOTE_REVISION_CONFLICT"
	// ErrorCodeConfigStale 标识绑定到过期配置的运行。
	ErrorCodeConfigStale = "GIT_SYNC_CONFIG_STALE"
	// ErrorCodeSecretActionInvalid 标识无效的 keep、replace 或 clear 动作。
	ErrorCodeSecretActionInvalid = "GIT_REMOTE_SECRET_ACTION_INVALID"
	// ErrorCodeSecretUnavailable 标识不可用或损坏的远端 Token。
	ErrorCodeSecretUnavailable = "GIT_REMOTE_SECRET_UNAVAILABLE"
	// ErrorCodeURLInvalid 标识被拒绝的远端 URL。
	ErrorCodeURLInvalid = "GIT_REMOTE_URL_INVALID"
	// ErrorCodeBranchInvalid 标识无效的配置分支。
	ErrorCodeBranchInvalid = "GIT_REMOTE_BRANCH_INVALID"
	// ErrorCodeIdempotencyConflict 标识幂等键被其他命令复用。
	ErrorCodeIdempotencyConflict = "GIT_SYNC_IDEMPOTENCY_CONFLICT"
	// ErrorCodeRunNotFound 统一隐藏不存在和跨 Workspace 的运行。
	ErrorCodeRunNotFound = "GIT_SYNC_RUN_NOT_FOUND"
	// ErrorCodeRunActive 标识同一 Workspace 的第二个活动运行。
	ErrorCodeRunActive = "GIT_SYNC_RUN_ACTIVE"
	// ErrorCodeAutoSyncDisabled 标识自动同步在调度或创建期间已关闭。
	ErrorCodeAutoSyncDisabled = "GIT_SYNC_AUTO_DISABLED"
	// ErrorCodeRunConflict 标识需要用户处理的安全 Git 停止。
	ErrorCodeRunConflict = "GIT_SYNC_CONFLICT"
	// ErrorCodeRunTransition 标识过期或非法运行转换。
	ErrorCodeRunTransition = "GIT_SYNC_RUN_TRANSITION_CONFLICT"
	// ErrorCodeLeaseLost 标识过期或被接管的持久租约。
	ErrorCodeLeaseLost = "GIT_SYNC_LEASE_LOST"
	// ErrorCodeAuthenticationFailed 标识被拒绝的 Git 凭据。
	ErrorCodeAuthenticationFailed = "GIT_SYNC_AUTHENTICATION_FAILED"
	// ErrorCodeOffline 标识无法访问的远端。
	ErrorCodeOffline = "GIT_SYNC_OFFLINE"
	// ErrorCodeNonFastForward 标识被拒绝的非强制 Push。
	ErrorCodeNonFastForward = "GIT_SYNC_NON_FAST_FORWARD"
	// ErrorCodeRefDrift 标识比较后发生变化的引用。
	ErrorCodeRefDrift = "GIT_SYNC_REF_DRIFT"
	// ErrorCodeResultUnknown 标识无法证明的外部 Git 结果。
	ErrorCodeResultUnknown = "GIT_SYNC_RESULT_UNKNOWN"
	// ErrorCodeIndexFailed 标识失败的外部变更 follow-up。
	ErrorCodeIndexFailed = "GIT_SYNC_INDEX_FOLLOWUP_FAILED"
	// ErrorCodeCorrupt 标识违反契约的持久 Git 同步事实。
	ErrorCodeCorrupt = "GIT_SYNC_STATE_CORRUPT"
)
