# M10-01 最小审计查询收尾（2026-09-08）

本次复用现有 append-only Audit、Config、共享 Pool 与 GORMStore，仅交付操作员有界查询和镜像打包。不新增生产者、公开 HTTP API、审计 UI、自动归档或产品 `AUDIT_JSON` 导出。

## 查询合同与用法

`cmd/audit` 需要且只接受一个查询作用域：`--workspace UUID` 或 `--global`。后者对应 SQL `workspace_id IS NULL`，不是查询全部 Workspace。授权来自操作者数据库/Docker 权限，不借用浏览器 Session。

包含本次改动的应用镜像提供 `/app/zhixu-audit`；在已准备的 Compose 项目中：

```bash
docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml --env-file .env exec -T app \
  /app/zhixu-audit --workspace WORKSPACE_UUID --limit 50
```

开发环境也可通过既有受保护 YAML 或 `ZHIXU_DATABASE_*` 配置连接：

```bash
go run -mod=vendor ./cmd/audit --workspace WORKSPACE_UUID --limit 50
go run -mod=vendor ./cmd/audit --global --limit 50
go run -mod=vendor ./cmd/audit --workspace WORKSPACE_UUID \
  --before RETURNED_TIMESTAMP --before-id RETURNED_EVENT_UUID --limit 50
go run -mod=vendor ./cmd/audit --help
```

将示例大写占位符换为实际 ID/上一页 `next_cursor` 的值；翻页保持同一作用域并同时传入两个 cursor 字段。无需将密码或 DSN 放入参数。

- 排序固定 `(occurred_at DESC, id DESC)`，使用排他的复合 keyset cursor；时间精度最多微秒。limit 默认 50，合法范围 1..200；满页末尾可能再出现空页。
- `--timeout` 默认 10 秒、最大 1 分钟，覆盖数据库打开/Ping/查询。`--config` 复用 non-API loader，不查询或保留 API-only Bootstrap/Review 密钥；不会执行 migration。
- 连接启动参数强制 `default_transaction_read_only=on`，作用于共享池的每条连接。只暴露 List 接口；所有成功、失败、取消路径关闭 reader/pool，SQL rows 由既有 Store 关闭。
- 先校验整页，再输出 `audit-query/v1` JSON。每个 item 只输出 ID、时间、Actor Type、Action、可选 Resource Type、Outcome、可选安全 Error Code；不输出 ActorRef、ResourceRef、幂等键、Correlation 或 Metadata。
- Action/Resource Type/Error Code 仅允许有界标识符字符；并复用 Audit 领域的 canonical JSON 与 Secret 校验。错误只输出固定 code/message，非法参数、DSN、SQL、原始数据库错误和配置路径不回显。
- 退出码：成功/帮助为 0，配置/查询/输出故障为 1，参数错误为 2。读取到跨作用域、次序错误或损坏事件时整页拒绝。

## 现有生产者覆盖

以下基于生产调用点静态核对，不表示本次重新执行了各 producer 的业务流程：

| 行为 | 作用域 | 代码与装配依据 |
|---|---|---|
| `IMPACT_ANALYZED` | Workspace | `internal/knowledge/adapter/audit/impact.go`；`cmd/api/main.go` 注入 ImpactRecorder |
| `export.download` | Workspace | `internal/export/adapter/postgres/download.go`；API Export Repository 注入 Audit appender，记录 prepared-for-return |
| `model_settings.update` | 全局 | `internal/modelsettings/adapter/postgres/gorm_audit.go`；`internal/modelsettings/runtime/bootstrap_gorm.go` 注入 appender，记录 committed desired revision |
| `workspace.root.rebound` | Workspace | `internal/workspace/adapter/postgres/gorm_rebind_audit.go`；`cmd/workspacectl/main.go` 与 runtimegrant composition 注入 appender |
| `workspace_analysis.run_started`、`workspace_analysis.cancel_requested`、`workspace_analysis.terminated` | Workspace | Conversation 的 `gorm_workspace_analysis_audit.go` / control audit hook；API/Worker 对应 runtime hooks 与 Recorder 装配 |
| `workspace_analysis.tool_refused` | Workspace | Tools 的 `workspace_analysis_refusals.go` / GORM refusal adapter；受限分析 Tool runtime 注入 Recorder |

`workflow.tool_call`、Workspace binding history、Workflow/Approval、Memory 等 owner 独立记录不自动成为 `ops.audit_event`。查询为空不能证明这些 owner 没有操作。未宣称 Auth/Session/API Token、所有 Approval/File/Git/Settings/Memory/Security Block 已统一覆盖。

## 验证与修复

- 复用本轮实施已通过的 `go test -mod=vendor ./cmd/audit ./internal/audit/... -count=1 -timeout=60s` 和同范围 `go vet`；检查未修改 Go 代码，未重复执行。
- 初次集成尝试据实施交接停在 testdb 迁移准备阶段，尚未进入 CLI 断言。93 SQL、runner 与 Atlas checksum 同步后，检查代理只重跑以下单条隔离实库测试：

```bash
env -u ZHIXU_TEST_DATABASE_URL go test -mod=vendor \
  -tags=integration,testcontainers ./cmd/audit \
  -run '^TestAuditCLIReadsIsolatedPostgresWithReadOnlyConnections$' \
  -count=1 -timeout=120s
```

结果通过（包耗时 9.044s）。testdb 自建容器/数据库，覆盖同微秒分页、两个 Workspace 与全局事件隔离、实际 non-API 配置、拒绝 Append，且查询后 Audit 仍恰为原有 5 条；未复用迁移代理容器或用户数据库。

- 修复 `deploy/Dockerfile` 遗漏的 build/COPY 两行，将 CLI 安装到 `/app/zhixu-audit`；保持现有服务、端口、entrypoint 与 Root grant 配置。
- 与 Dockerfile 相同参数执行 Linux 构建通过：`CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags='-s -w' -o <temporary-directory>/zhixu-audit ./cmd/audit`。`file` 确认为 ARM aarch64、statically linked、stripped ELF。
- `gofmt -l cmd/audit` 无输出；受影响 `git diff --check` 通过。

没有重新构建或部署用户镜像；实际运行环境需包含本次打包改动后才能使用容器内 CLI。全域审计/留存平台与完整 M11 验证按用户要求不在本轮。
