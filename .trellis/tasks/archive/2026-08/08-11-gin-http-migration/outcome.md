# 交付结果

## 结果

核心迁移提交为 `9bb5b939`，路线图与系统设计同步提交为 `ab6029c4`，最终边界修复、规范和验收收口提交为
`39bf654e`。

后端 HTTP 已从 Chi 完整迁移到 Gin v1.12.0。生产入口收敛为 `internal/app.NewRouter` 返回的唯一
`*gin.Engine`；所有领域 HTTP 注册使用 `gin.IRouter`，业务 Handler 继续通过 `httpapi.GinHandler` 保持标准库
`http.ResponseWriter`、`*http.Request`、`Request.PathValue` 和 `Request.Context()` 契约。Chi 已从源码、测试、模块依赖和
vendor 中删除。

Gin 默认 redirect、proxy、404/405、binding、logger/recovery 均未隐式接管项目契约。当前 runtime 与 OpenAPI 的 183 条
operation 精确对等，仅 `/metrics` 随依赖可选；严格 JSON、Problem、认证/授权、低基数路由日志、SSE、上传下载与静态
fallback 保持原有 owner。TODO 11 的 `gin-contrib/sessions` 仍是独立 deferred 评估，不属于本交付。

独立审查额外发现并修复两处边界缺陷：不可比较的 panic 值不会再让 recovery 自身二次 panic；SSE 会沿 writer 的
`Unwrap()` 链确认最底层真实支持 flush，避免 Gin wrapper 让 unsupported 分支失效。Application integration smoke 也改为
通过生产 Router 的 `/api/v1` 路径，不再让 Application 测试直接依赖 Gin。

## 已验证证据

- 28 个受影响 app/auth/httpapi/domain HTTP/cmd/api/changecontrol application 包的普通测试和 `-race` 全部通过。
- 全仓 `go test -race -count=1 -timeout 60s ./...` 已运行；除下述两个已证明与 Gin 无关的基线外，其余包通过。
- `go vet ./...`、API build、OpenAPI、tidy、vendor resolution、integration-tag compile-only 和 diff check 通过。
- Compose runtime、netns、workspace、smoke cleanup、image cleanup 与 model secrets init 子合同通过。
- 独立 reviewer 已复核路由/OpenAPI、认证、分层依赖、SSE/上传下载、recovery 与测试覆盖；当前范围问题已修复并重验。

## 已知验证边界

- `internal/platform/config` 的 Compose fake project-name 断言在 Gin 迁移前提交同样失败；本任务未修改该代码。
- `internal/platform/gitcli` 在全仓 60 秒阈值末尾超时；单包普通与 race 使用 120 秒均通过。
- launcher 合同的动态端口 A→B→A fixture 在端口释放窗口返回 `EADDRINUSE`；迁移前提交可复现，其他 Compose 子合同通过。
- 当前未配置 `ZHIXU_TEST_DATABASE_URL`，真实 PostgreSQL integration 仅完成编译验证。后端迁移无用户可见 UI 变化，且未获浏览器
  运行要求，因此未启动服务执行浏览器 smoke。

## 回滚

如需回滚，整体 revert `39bf654e`、`ab6029c4` 中的 Gin 文档增量和核心迁移提交 `9bb5b939`，再重新构建原 Chi
artifact；本任务没有 Schema 或数据回滚步骤。
