# M9 Export 附件与 AC-33 收口实施计划

## Ordered Work

1. **恢复 M9-03 安全基线。** 在当前工作树上逐项恢复 create-only `Promote`、`DeletePrepared(expected hash/size)` 与独立 `DeleteOrphan`，补回 kind/field、mismatch preservation、final conflict 和 barrier 并发负测；删除 Evaluation/Audit 占位渲染与公开放宽。运行 Export 定向 race、OpenAPI 和前端定向门禁后再继续。
2. **固化 tagged Domain contract。** 以测试先行增加 `COLLECTION|WORKSPACE_ATTACHMENTS` 穷尽 scope、`ATTACHMENTS_ZIP`、`attachment-export/v1`、`workspace-attachments/v1`、`RAW_USER_OWNED`、状态/result 字段组合、canonical request hash 与错误分类；证明 dummy Collection、跨 scope kind 和 Evaluation/Audit 均被拒绝。
3. **新增前向 migration 与持久启用门禁。** 由主 Agent 取得下一个未占用编号，回填既有 Job 为 Collection scope，增加 attachment facts、nullable/FK/互斥 CHECK、`.zip` path/kind 约束、索引及默认关闭的 contract-version capability gate；覆盖 fresh/repeat/existing-job upgrade/empty Down→Up/business-data guarded Down，保持 `00036` 内容不变。
4. **实现只读附件 Scanner 与确定性 ZIP。** 先补 LocalFS contract tests，再从 Workspace root handle 实现 fd-relative逐组件 no-follow 遍历、regular/link/path/collision/limit/source-mutation 校验、Prepare 前完整树重扫、稳定 manifest 和 `zip.Store` 归档；覆盖 empty、binary、nested、determinism、祖先替换/新增文件等所有 unsafe entry、所有上限和扫描竞态。
5. **扩展可恢复 Application/Repository。** 增加 attachment Create/List/Get/Claim/Prepare/Complete/Fail/Expire/Cleanup dispatch，复用现有 Job/lease/event/download transaction；补 exact replay、双 Worker、每个 crash window、prepared 后源变化、TTL、cleanup retry、mismatch preservation 与源目录不变测试。
6. **接入 River/Composition 与维护循环。** River Args 仍只携带 Workspace/Export ID，Worker 按 persisted scope typed dispatch；gate 关闭时不 claim attachment scope，Collection recovery 显式过滤 scope。复用 startup/periodic recovery、expiry 和 orphan sweep，补混部、dispatch failure、duplicate delivery、restart 和 readiness 回归，避免覆盖其他并行 Worker 接线。
7. **完成 Auth/HTTP/OpenAPI。** 新增四个 Workspace attachment-export 路由、严格 body/header/query/cursor/Problem、READ_LOCAL capability 与安全 ZIP headers；gate 关闭/contract mismatch 返回 unavailable。OpenAPI 使用 tagged schema，checker 锁定新路由并继续拒绝 Evaluation/Audit，既有 Collection endpoint 不变。
8. **完成 Settings 前端。** 新增严格 API decoder、Workspace query/mutation、2 秒轮询、SSE invalidation、cache cleanup、same-key retry 与 authFetch Blob 下载；接入现有 Settings 数据导出区域并覆盖全部状态、raw policy、失败/过期新建、键盘/焦点/响应式。
9. **完成真实动态验收。** 扩展 Export browser smoke，在真实 PostgreSQL/API/Worker/River/Vite 中创建 fixture、刷新和重启恢复、下载解包并逐项校验 manifest/hash；覆盖权限、跨 Workspace、unsafe/source mutation、篡改、expiry/cleanup/source preservation、桌面/390x844、overflow 与 console/network。
10. **审查、同步与提交准备。** 执行 Go/SQL/通用/前端审查并修复发现；同步专项 spec、产品/架构、AC-33 和父任务状态。主 Agent 使用逐 hunk/临时 index 检查混合工作树，确认不混入 M7/M8/M10 或未完成 Evaluation/Audit 改动后才提交和 push。

## Checkpoints

### Checkpoint A: Baseline Restored

- Collection Export 仍只有 `MARKDOWN|METADATA_JSON` 和既有 fields。
- create-only、prepared binding deletion、conflict preservation 与并发测试通过。
- `00036` 与 HEAD 一致；无 Evaluation/Audit unavailable 成功文件。

### Checkpoint B: Typed Persistence And Archive

- tagged Domain 与新 migration 的 fresh/upgrade/guarded Down 通过。
- Scanner/ZIP 的正常、空、二进制、确定性、安全、限额和源变化测试通过。
- attachment prepared result 在 crash/restart/lease/TTL 情况下只有一个权威 binding。
- capability gate 默认关闭，混部时不创建/claim新 scope；全部兼容实例就绪后才启用。

### Checkpoint C: Public And Browser Closure

- Auth/HTTP/OpenAPI/Web 严格契约和全状态恢复通过。
- 真实浏览器下载能被标准 ZIP 解包并逐字节匹配源 fixture。
- expiry/cleanup 后 source tree 的 path、hash、size 和 metadata 快照不变；下载 Audit actor/binding 正确。

### Checkpoint D: Delivery Gate

- 全部定向与受影响全量命令通过，`git diff --check` 无错误。
- Go、SQL、通用和前端审查无未解决问题，独立 reviewer 复核恢复、安全和 AC-33 证据。
- 文档只关闭 AC-33；Evaluation/Audit 继续标记后续，候选提交不含其他脏改。

## Test Matrix

| Layer | Required evidence |
| --- | --- |
| Baseline Domain/LocalFS | M9 two-kind/field gate、no-replace promote、唯一 winner、final/staging preservation、prepared mismatch、orphan namespace |
| Attachment Domain/Application | tagged scope、canonical hash、policy/schema、status/result shape、empty/binary/nested、deterministic ZIP/manifest、limits、source mutation、prepared replay |
| PostgreSQL/Migration | fresh/repeat/upgrade、scope CHECK/FK、guarded Down、same-key concurrency/response-loss、DB-time lease/TTL、crash windows、cleanup retry、download+Audit atomicity |
| Security/File | Workspace/capability、CSRF/Origin/Token、root/ancestor symlink、hardlink/device/FIFO/socket、Zip Slip、UTF-8/case/NFC collision、hash/size tamper、no path/content leak |
| River/Composition | capability disabled/mixed-version/activation、DB success + dispatch failure、duplicate delivery、restart、lease reclaim、startup/periodic expiry/orphan sweep、typed dispatch |
| HTTP/OpenAPI | strict body/header/query/cursor、202/200/400/403/404/409/410/500/503、ZIP headers、tagged DTO、Collection compatibility、Evaluation/Audit rejection |
| Frontend | unknown strict decode、scope/status binding、Workspace keys/cleanup、same-key retry、poll stop、SSE invalidation、Blob validation、all states/a11y |
| Browser | real services、refresh/restart、download+unzip+hash、permission/cross Workspace、unsafe/mutation/tamper/expiry/cleanup、desktop/390x844、keyboard/focus/overflow/console |

## Validation Commands

```bash
go test -race -count=1 -timeout 60s ./internal/export/... ./internal/events/... ./internal/audit/... ./internal/auth/http ./internal/app ./cmd/api ./cmd/worker
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s ./internal/export/adapter/postgres
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s -run '^(TestExport|TestM9|TestAttachment)' ./internal/platform/migration
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" bash deploy/export-browser-smoke.sh
go test -race -count=1 -timeout 60s ./...
go vet ./...
go mod tidy -diff
git diff --check
```

后端集成/迁移单次命令保持 60 秒超时；若全仓 race、浏览器或环境启动超过项目约定时限，先保留定向门禁证据并如实记录未完成范围，不用重跑掩盖失败。

## Risk Gates

- Baseline Gate 未通过，不新增 attachment kind/scope。
- 新 migration 编号或共享 OpenAPI/Worker 文件 ownership 未确认，不编辑或整文件暂存共享文件。
- 不改 `00036`，不以 dummy Collection、nullable 字段堆叠或 unknown enum 兼容 tagged scope。
- 不用 `Lstat -> Rename` 声称 create-only，不用无 hash/size 的删除处理 prepared 文件。
- 不在 Prepare 后重新扫描附件，不在源变化、上限或 unsafe path 时截断/跳过后成功。
- 不在 capability gate 默认关闭或仍有旧 contract 实例时创建/claim attachment Job；启用后不回滚到不识别 tagged union 的二进制。
- 不将 `RAW_USER_OWNED` 标成 MASKED，不在 Audit/日志/Problem 中记录路径列表或正文。
- 不以单测/mock 截图关闭 AC-33；必须有真实服务下载解包和 source preservation 证据。
- 不暂存 Evaluation/Audit 占位、已提交 migration 改写或其他模块的并行改动。

## Rollback Points

- Baseline 修复失败：停在现有 M9-03 两 kind，不开放附件入口。
- Migration/Archive contract 失败：保留新路由未接线，修复前不让 Worker claim attachment Job。
- 混部/激活检查失败：保持持久 gate 关闭；已启用后需要止血时先关闭 gate并等待在途 lease，继续运行兼容二进制做 forward fix。
- Recovery/Auth/Download gate 失败：Settings 入口保持不可达，不把枚举或数据库行声明为交付。
- Browser/source-preservation 失败：不关闭 AC-33、不提交；保留 Job/Audit，使用 forward fix，绝不执行破坏性 Down 或删除源附件。
