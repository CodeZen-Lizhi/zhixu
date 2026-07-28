# M7 Timeline 与 Impact 遗留实施计划

> 当前任务处于 planning。只有规划再次获得明确批准并由 `task.py start` 切换为 in_progress 后，才能修改产品代码。

## 1. Execution Order

| ID | 工作包 | 主要范围 | 前置 | 验收证据 |
| --- | --- | --- | --- | --- |
| M7-R1 | 冻结 additive contract 与 migration | Event/Object/Report/Proposal union、selector、analysis version/supersession、Down guard | 规划批准 | Domain/迁移 contract tests；OpenAPI 草案与错误矩阵一致 |
| M7-R2 | Artifact/Review owner projection | Artifact citation selector 同事务与历史 backfill；复用 Review selector；两类一等事件 source | M7-R1 | 真实 PG fresh/upgrade/repeat、owner rollback/replay/POISONED、EXPLAIN seek |
| M7-R3 | Versioned Impact | Artifact/Review 精确查询、binding、v1->v2 supersession、并发 replay、Audit 原子性 | M7-R2 | Domain/Application/PG race；历史 v1 不变且 v2 可生成 |
| M7-R4 | Typed downstream Proposal | owner factory、新建端点、Change Control union、幂等、Approval-only、Apply/DB 双层 guard | M7-R3 | Proposal create/replay/conflict；批准和 Apply 零副作用集成测试 |
| M7-R5 | API/Composition/OpenAPI | HTTP strict contract、Auth capability、API/Worker readiness、OpenAPI/checker | M7-R2,R3,R4 | HTTP/Auth/timeout/404/409/503 与 `make openapi-check` |
| M7-R6 | Timeline/Impact Web | strict client、query keys/hooks、routes/nav、filters/cursor、详情/报告/选择/Proposal 跳转 | M7-R5 | Vitest/RTL、lint/typecheck/build、桌面与移动浏览器 smoke |
| M7-R7 | 集成、审查与文档 | 全链路动态验收、spec/父任务同步、diff 分范围复核 | M7-R1..R6 | PG/API/Worker/Vite smoke、Review skills、`git diff --check` |

## 2. M7-R1 Contract And Migration

1. 先补 Domain tests，冻结：
   - `ARTIFACT_GENERATED`、`REVIEW_CARD_INVALIDATED`；
   - `ARTIFACT`、`REVIEW_CARD` 与 owner binding；
   - `impact-analysis/v1|v2`、supersession 规则；
   - `downstream_update` / `impact-downstream-update/v1` payload、action、HIGH risk 和非法 union。
2. 设计下一条 migration，只 additive 新增：Artifact selector、report version/supersession、object binding、Proposal typed payload/union 和 owner event source 支撑。
3. 更新迁移 runner/contract tests，覆盖 fresh、从含 v1 report 和历史 Artifact/Review 数据升级、重复 Up、可重入 backfill、约束与有数据 guarded Down。
4. 不修改 `00037`、`00039`、`00040`、`00049`、`00058` 等既有 migration；发现兼容问题通过新 migration forward fix。

## 3. M7-R2 Owner Projection And Events

1. Artifact owner 在 Revision 创建事务内集合化写 citation selector；为历史 backfill 建立冻结高水位、持久 cursor/count、状态和 completion marker，durable Worker 从 canonical Revision 解码器读取，校验 hash 后幂等写 identity，不复制正文。
2. 为 Source Version/Span lookup 建 Workspace-leading 索引；用真实 `EXPLAIN` 证明影响查询不走无界 JSON/全表扫描。
3. Review 直接复用 `review_card_evidence_selector`，补齐 owner batch reader；禁止新增平行 selector 或逐卡 N+1。
4. 在 Artifact generation terminal success 与 Review Card 首次 invalidation 的 owner 事务 enqueue stable Timeline source；补 owner rollback、exact replay、并发 projector、binding drift/POISONED 测试。
5. 覆盖 backfill crash/restart、并发新 Revision、count mismatch/FAILED、完成复扫与 marker 原子提交；marker 前 `impact-analysis/v2` 和 readiness 必须 unavailable。
6. 保留 Review invalidation -> Health 的兼容投影；新一等 Event 与 Health Event 使用不同 source identity，不互相替代。

## 4. M7-R3 Versioned Impact

1. 先写失败测试：历史 v1 event/report、Artifact/Review exact provenance、重复冲突、Workspace 越界、500 上限、stale owner snapshot 和并发 v2 create。
2. 扩展 Repository，集合化查询 Relation/Conflict/Health + Artifact/Review，批量回读 owner binding并 canonical sort/dedupe。
3. 扩展 report fingerprint/codec/HTTP model，冻结 `impact-report/v1|v2` 双版本 discriminator，v2 纳入 `analysis_version`、supersession 与 typed binding；保留 v1 原字段集合/read compatibility。
4. Application 先校验 selector backfill `COMPLETED` marker，再按 current policy 查找/创建：当前 v2 exact replay，只有 v1 时 append v2 并 supersede；唯一冲突回读校验，绝不 UPDATE 旧报告。marker 未完成时稳定 unavailable。
5. 保持首次 Report + Timeline outbox + `IMPACT_ANALYZED` Audit 同事务；对 v2 replay、不同 Idempotency-Key Audit 和回滚路径补证据。

## 5. M7-R4 Formal Proposal Boundary

1. 扩展 Change Control Domain/Repository，新增 `downstream_update` typed Revision、canonical validator、change/request hash、list/detail codec 和数据库 union 约束。
2. 实现 Artifact/Review proposal factories：只接受当前 READY report 中 exact target，重新读取 owner state并重建 binding；stale/unsupported/unavailable 均在创建前退出。
3. 新增单 target Proposal command/HTTP endpoint；严格绑定 Workspace/report/event/target/action/owner snapshot 与 Idempotency-Key。
4. Approval service 对新类型只持久化 decision，不走 knowledge apply、file dispatch 或 Artifact publication副作用。
5. 在 HTTP/Application/Repository 及 DB 状态约束同时阻止 Apply、Preflight、resume、Workflow binding、Write Authorization 和 writeback execution；固定返回 `DOWNSTREAM_UPDATE_APPLY_UNAVAILABLE`。
6. 真实 PostgreSQL 测试逐表断言批准/Apply 后 Proposal/Approval 以外没有新增或修改事实。

## 6. M7-R5 Public Contract And Composition

1. 同步 Knowledge/Change Control HTTP DTO、Problem mapping、Auth capability、API/Worker composition、readiness 与 `knowledge_timeline` capability。
2. 更新 OpenAPI path/schema/discriminator/enum/security/status 和 checker；旧四端点保持兼容，新 endpoint 只接受一个 target。
3. 覆盖 strict query/body、未知/重复字段、非 JSON、非法 key/UUID/enum、跨 Workspace 404、409、503、deadline cancel、Cookie Origin/CSRF 与 Bearer Scope。
4. 对共享脏文件逐 hunk 合并并由主 Agent 统一检查，禁止覆盖 M8/M9/M10 未提交工作。

## 7. M7-R6 Web Workbench

1. 新增 `web/src/api/timeline.ts` strict decoder/client 及 tests，完整保留 event operator/correlation、Impact binding/version/supersession、Proposal replay/error。
2. 新增 Timeline feature 的 query-key factory、queries/mutation 和页面；Workspace 缺失时禁用，所有 cache key 绑定 Workspace。
3. 实现 URL filters、opaque cursor history、显式刷新、列表/详情/报告、supported target 选择和一次用户意图幂等键；filter/Workspace 变化清除 cursor/selection。
4. 增加 lazy `/timeline`、`/timeline/:eventId` 和 AppShell 导航；只对 allowlist structured refs 渲染链接。
5. Proposal 创建成功跳转 Proposal detail；approved `downstream_update` 显示不可应用状态，不显示假 Apply 成功。
6. 用 Testing Library 覆盖 loading/empty/unavailable/error/retry/404/409/503/unknown network、keyboard/focus、桌面/移动布局与长文本。

## 8. Verification Gates

### 8.1 Focused Static And Unit

```bash
go test -race -count=1 -timeout 60s ./internal/knowledge/... ./internal/artifact/... ./internal/review/... ./internal/changecontrol/... ./internal/auth/http ./cmd/api ./cmd/worker
go vet ./internal/knowledge/... ./internal/artifact/... ./internal/review/... ./internal/changecontrol/... ./internal/auth/http ./cmd/api ./cmd/worker
go mod tidy -diff
make openapi-check
npm run test --prefix web -- --run src/api/timeline.test.ts src/features/timeline src/routes/AppRoutes.test.tsx src/app/AppShell.test.tsx
npm run lint --prefix web
npm run typecheck --prefix web
npm run build --prefix web
git diff --check
```

### 8.2 Real PostgreSQL And Worker

使用 disposable `ZHIXU_TEST_DATABASE_URL`，在单项预计 60 秒范围内执行既有目标及本任务新增的 owner/proposal integration：

```bash
make timeline-impact-integration
make timeline-impact-fault-smoke
make timeline-impact-worker-smoke
```

必须额外证明：migration v1->v2、selector seek、Artifact/Review provenance、owner transaction event、concurrent supersession、Proposal approval/apply 零副作用。若新增 Make target，名称和命令在实现时以仓库实际落地为准，不在规划中伪造已存在入口。

### 8.3 Dynamic Browser Smoke

启动真实 API、Worker、Vite 和 disposable PostgreSQL，至少完成：

1. 打开 `/timeline`，过滤、分页并进入事件详情。
2. 触发/预置 Artifact generated 与 Review invalidated owner 事实，刷新后看到一等事件和结构化关联。
3. 对含 Artifact/Review 的事件生成 v2 report，查看 v1 supersession 与 owner binding。
4. 选择一个 Artifact 和一个 Review Card 分别创建正式 Proposal，跳转详情并批准。
5. 直接调用 Apply 路径，确认稳定拒绝且目标/Workflow/Authorization/Git/文件均无变化。
6. 在桌面和 390x844 下确认无控制台错误、网络失败、横向溢出或文字遮挡。

## 9. Review And Delivery

- Go/共享契约修改后执行 `go-review`。
- Migration、selector、Repository、分页/批量/事务追加 `sql-code-review`。
- 跨后端/OpenAPI/Web 完成后执行 `code-review-and-quality`，重点检查第二事实源、stale binding、幂等、审批越权、N+1 与兼容性。
- Review 发现范围内缺陷先修复再重跑相关门禁；业务语义不确定或 owner executor 需求不得擅自扩展。
- 最后同步 `.trellis/spec/backend/timeline-impact.md`、相关 backend/frontend index/quality spec、父任务 M7-04 状态和必要产品文档，只记录真实动态证据。

## 10. Commit Boundary

建议按可独立复核的范围暂存：

1. migration + Domain contract；
2. owner selector/event + Impact v2；
3. downstream Proposal + API/OpenAPI；
4. Timeline Web；
5. tests/spec/task/journal。

提交前逐组执行 `git diff --cached --check` 和白名单核对；不得整文件误带 M8/M9/M10 的并行未提交改动。生成新 union 事实后，发布回滚仅允许兼容 binary 或 forward fix。
