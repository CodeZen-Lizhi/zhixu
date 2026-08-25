# Health Repository GORM 迁移实施计划

## 实施顺序

1. [x] 盘点 Health Scan、Schedule、Issue/Read、Detector、Affected-change 与 Collection membership 的旧构造、事务调用方、锁 SQL、错误映射和已有测试入口。
2. [x] 新增共享 Health GORM 依赖、查询/RowsAffected helper 与受控 legacy SQL bridge；从共享 Pool 获取 GORM root/UoW，禁止创建第二连接池。
3. [x] 实现 Scan/Workflow/River/Event scoped 事务路径，保留 idempotency、CAS、SKIP LOCKED、lease、retry、cancel、deadline、response-loss recovery 和数据库时间语义。
4. [x] 实现 Schedule 与 affected-change dispatcher 的短事务 claim/ack/release/poison 路径；保持 Collection 条件和 workspace 锁顺序。
5. [x] 实现 Issue/Detector/Read GORM 读写及分页/历史聚合，补齐 Smart Collection scoped binding verifier；不改 Application/Domain 契约。
6. [x] 运行受影响包 `go test`、`go test -race`、`go vet`、integration compile-only、`gofmt`、`git diff --check`、`task.py validate`；无真实 `ZHIXU_TEST_DATABASE_URL` 时明确记录 TODO 9 门禁未执行。
7. [x] 做 Go/SQL/Trellis review，修复当前范围内缺陷；回填验证记录、legacy 清单、TODO 9 与回滚边界，保持 child `in_progress`。

## 验证命令

```bash
gofmt -w internal/health/adapter/postgres internal/health/adapter/collection
go test -timeout=60s ./internal/health/...
go test -race -timeout=60s ./internal/health/...
go vet ./internal/health/...
go test -run '^$' -tags=integration ./internal/health/adapter/postgres
git diff --check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-health-migration
```

真实 PostgreSQL/TODO 9 锁、并发、River worker、response-loss 和跨 owner 事务门禁只有配置正式 DSN 后执行；本 child 不伪造通过声明。

## 验证记录

- `gofmt -d`、`go test -timeout=60s ./internal/health/...`、`go test -race -timeout=60s ./internal/health/...`、`go vet ./internal/health/...`：通过。
- 工作树默认 vendor 模式曾报告 `vendor/modules.txt` 与 `go.mod` 不一致；按现有仓库可用方式以 `GOFLAGS=-mod=mod` 重跑上述 Go 门禁，结果通过，未修改 vendor 或依赖文件。
- `go test -run '^$' -tags=integration ./internal/health/...`：仅编译，通过；未执行真实 PostgreSQL 测试。
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-health-migration`：通过；仅有大 spec 文件上下文注入警告。
- Go/SQL/Trellis review：通过；修复 GORM Exec 丢失 `RowsAffected` command tag 和 scoped 依赖 typed-nil 两个边界问题，未发现新的 Critical/Required 问题。
- SQL review：新适配器只使用参数化 legacy SQL 与共享 GORM root/UoW；未新增动态标识符、第二连接池或 Schema mutation，真实执行计划与锁竞争仍待 DSN 门禁。
- `ZHIXU_TEST_DATABASE_URL` 未配置，因此 TODO 9 要求的真实锁竞争、取消/deadline、River/Event 协作、response-loss 与跨 owner 事务门禁未执行。

## Legacy 与回滚边界

- 本 child 新增的 GORM 适配器通过包内 `pgx.Tx` 兼容桥调用现有 Health SQL repository；生产 Composition、cmd、Schema、legacy 删除仍由 TODO 9/Final 负责。
- `internal/health/adapter/collection/membership.go` 等旧 pgx owner 代码未删除，确保当前 dev 工作树其他 owner 的 staged 改动不被覆盖。
- 回滚仅需移除本 child 新增的四个 staged GORM 文件及对应 Trellis 记录；不回滚共享 GORM 基础设施、其他 owner staged Port、Schema 或 legacy 实现。

## 回滚点

- 依赖/Model/helper 阶段失败：删除本 child 新增 staged 文件即可，legacy 路径不变。
- scoped Scan/Schedule/Issue 实现阶段失败：仅 revert Health GORM Adapter 变更，不回滚数据库结构或其他 owner 的 staged Port。
- Final 尚未切换 Composition，故本 child 的 staged GORM 实现不会改变生产路径。
