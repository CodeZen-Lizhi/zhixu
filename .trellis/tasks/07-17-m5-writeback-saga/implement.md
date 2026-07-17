# M5-04D 实施清单

1. [x] 收敛领域契约：新增 `file_prepared/git_prepared` 状态、Atomic Begin、file/git intent、cleanup 与 lease guard；补正常/非法状态和 Secret 边界测试。
2. [x] 新增 `00010_safe_writeback_saga.sql`，扩展 Execution 字段、状态/不可变/恢复约束；空库、重复迁移和旧数据兼容测试通过。
3. [x] 实现 PostgreSQL Atomic Begin：固定锁顺序校验并消费双授权、验证 running lease、create/replay Execution、Proposal applying 同事务；覆盖 rollback/race/idempotency。
4. [x] 扩展 LocalFS：Prepare 预留 backup locator、持久 PreparedWrite 摘要与 target/result/backup identity token 可 rehydrate、ResumeTarget 覆盖 Base/Result/同内容不同 inode 篡改/进程重启、Cleanup 保留未知证据。
5. [x] 扩展 Git approval snapshot 与 Application Approval：Approved 必须 clean/attached/current HEAD，Rejected 不读 Git；更新 HTTP/OpenAPI response 与测试。
6. [x] 实现 Writeback Application Saga：Begin、按状态 Resume、lease guard、file checkpoint、git intent/lookup/commit、补偿、Publish、Cleanup/finalize；所有分支使用稳定错误码。
7. [x] 实现固定 Safe Writeback Workflow Node 与最小 API/Worker composition；Node input/output 无 Credential/正文/绝对路径，明确无 River dispatcher。
8. [x] 补真实全链路 smoke：PostgreSQL + Workflow Run/claimed Node + Proposal/Approval + 双授权 + LocalFS + Git，覆盖正常、重复 delivery、file/Git checkpoint crash、Publish response loss/replay 和 cleanup finalize response loss/retry。
9. [x] 同步父任务、产品/架构/数据库/API/工具安全/Workflow/恢复/测试文档，修正旧 `git add/git commit` 与旧 TargetLock 示例；创建后续 River runtime 任务。
10. [x] 执行全量门禁与 review：domain/application/FS/Git/PostgreSQL race、迁移、OpenAPI、前端、Compose/Docker readiness、`go-review`、`sql-code-review`、Trellis full-scope check；修复全部 P0/P1。

## Validation

```bash
go test -race ./internal/changecontrol/... ./internal/platform/gitcli ./internal/workflow/...
go test -race -count=20 ./internal/changecontrol/application ./internal/changecontrol/adapter/localfs
go vet ./...
make test
node api/openapi/check.mjs
docker compose -f deploy/compose.yml --env-file .env.example config --quiet
docker compose -f deploy/compose.yml --env-file .env.example up -d --build --wait
curl -fsS http://127.0.0.1:8080/readyz
docker compose -f deploy/compose.yml --env-file .env.example down -v
git diff --check -- . ':!vendor/github.com/yuin/goldmark/README.md'
```

数据库测试必须验证空库全部 Up、重复 Up、Atomic Begin 中间失败全回滚、双并发只有一个 Execution。进程重启测试不得只在同一 Writer 实例内 fault injection，必须销毁旧实例后用 persisted checkpoint/locator 恢复。

## Risky Files And Rollback

- `migrations/00010_safe_writeback_saga.sql`：只做前向 expand/constraint replacement；不得修改 00008/00009。
- `internal/changecontrol/adapter/localfs/writer.go`：现有 contract/security tests 必须全部保留，Resume 不能放宽 symlink/owner/device/hash/mode 检查。
- `internal/platform/gitcli/writeback_inspect.go`：approval snapshot 复用同一严格 runner/安全检查，不新建宽松 Git 路径。
- `internal/changecontrol/application/service.go`：Approval 行为变化必须保持 Rejected/历史查询兼容并更新 OpenAPI。
- `cmd/worker`：只构造 Node，不引入临时 polling；真实 River 接线拆独立任务。
- 回滚时停止新 Begin；未提交 Git 的 file intent 必须补偿/人工恢复，已提交 Git 的 Execution 只允许 Publish reconcile 或严格 Reverse，禁止恢复文件制造 Git/FS 分叉。
