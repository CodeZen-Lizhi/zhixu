# Git 远端同步：实施计划

## Prerequisite Gate

启动前验证 `08-02-quick-capture-profile` 已归档并提供 external-change capture/ingestion/index 契约。若 File History 正在修改 Git CLI Runner，先冻结共同 Runner options/environment 接口并避免并发编辑。

## Order

1. 定义 RemoteConfig/SyncRun/compare/result unknown 状态、错误和幂等契约。
2. 抽取通用 Secret Sealer，先证明 Model Settings 全量回归不变。
3. 增加 Git Remote config/credential/run/attempt/outbox migration 与 Repository tests。
4. 实现 HTTPS URL policy、Secret actions、Config/Test API 和 OpenAPI。
5. 实现受控 AskPass credential session 与 Secret/argv/config/log fault tests。
6. 实现 Fetch/Compare、same/dirty/detached/diverged 状态。
7. 实现 FastForward、Push 和 post-check，覆盖 response loss/ref drift/unknown result。
8. 串接 inbound external-change capture/index follow-up，分离 Git 与 Index 状态。
9. 实现手动同步；稳定后实现 writeback-completed 自动 Outbox，默认关闭。
10. 实现 Settings strict decoder、配置、Secret replace/clear、状态、运行和冲突 UI。
11. 完成真实临时 bare remote、Worker restart、并发、安全和桌面/移动验证。

## Validation

```bash
go test -race -count=1 -timeout 60s ./internal/gitsync/... ./internal/platform/gitcli/... ./internal/modelsettings/... ./internal/changecontrol/... ./internal/workspace/...
go vet ./internal/gitsync/... ./internal/platform/gitcli/... ./internal/modelsettings/... ./internal/changecontrol/... ./internal/workspace/...
go mod tidy -diff
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- git-sync SettingsPage workspace
npm run build --prefix web
git diff --check
```

增加 disposable PostgreSQL migration/concurrency tests、真实 local HTTPS-compatible test harness 或受控 credential fake、bare remote end-to-end、result-unknown fault injection、Secret scan 和 Compose Worker smoke。

## PRD Backfill Gate

实现、验证和 Review 通过后、归档前，将稳定交付行为回填到 `docs/product/PRD.md` 的 `10.1`、`10.9`、`10.22`、`11.8`、`15.4-15.5`、`16.1`、`21.15` 和 `22`；记录实际更新章节或无需更新的理由。不得提前写入未交付行为，不创建 `v2.0` PRD。

## Review

- 使用 `go-review`、`code-review-and-quality`、`sql-code-review`。
- 重点检查凭据生命周期、URL/redirect、安全环境、Git 命令 allowlist、CAS/lease、未知结果、与 Safe Writeback 互斥、post-check 和索引状态分离。

## Rollback

先关闭 auto-sync，再关闭 gitsync capability。保留 encrypted config、Run/Attempt 和 Audit；不得删除本地/远端 Commit 或尝试反向 Push。
