//go:build integration

package workflowpostgres

import (
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// NewOrganizingTerminalTestRuntime 与外部跨领域测试共享既有的公开构造函数夹具，
// 不增加生产依赖。
func NewOrganizingTerminalTestRuntime(t *testing.T, pool *platformpostgres.Pool, hooks GORMRuntimeRepositoryHooks) *GORMRuntimeRepository {
	t.Helper()
	return newGORMRuntimeTestRepository(t, pool, hooks)
}

// OrganizingTerminalTestStartFixture 保留终态测试迁移至外部测试包之前
// 使用的运行时身份和事件默认值。
func OrganizingTerminalTestStartFixture(workspaceID foundation.ID, key string, retry domain.RetryPolicy) application.RuntimeStartRequest {
	return runtimeStateStartFixture(workspaceID, key, retry)
}
