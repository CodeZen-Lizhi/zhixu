# Proposal Revision 编辑与三方合并实施清单

> **M9 历史升级修复（2026-09-08）**：已补 00093/Atlas runner 兼容，原失败回归、最新所属 Revision、历史保持、重复升级、失败回滚重试及非法回填拒绝均已通过。后续结果见 [M9 修复记录](../../../07-16-product-delivery/research/m9-legacy-upgrade-2026-09-08.md)；早期规划与未执行的其他矩阵保留各自事实。

> 状态：主要代码路径已实现并完成局部门禁；真实 PostgreSQL 迁移/并发验证、目标镜像资源基准和浏览器链路尚未执行，因此任务仍保持 `in_progress`。本清单只把有直接证据的部分标为完成。

## 1. Delivery Order

### Phase 0: Contract freeze and fixtures

- [x] 对齐 `prd.md`、`design.md`、`docs/requirements.md` AC-13 与现有 Change Control 状态机；冻结首期只支持普通 `file_patch/REPLACE`、`ready_for_review|needs_revision`。
- [x] 固定 merge preview、append、history/detail 的 OpenAPI 字段、单正文 1 MiB、preview request 4 KiB、append body 8 MiB、preview response 16 MiB、元数据 64 KiB/项、1024 conflict IDs、Problem code/details 和 cache policy。
- [x] 先更新 `docs/architecture/quality.md` 与 Git 相关 spec，批准固定、无 repository 副作用的 `merge-file` plumbing；继续禁止 branch merge/rebase/raw argv。
- [x] 固定空正文语义：首期只测试空 base/current；最终正文继续遵守现有非空 Markdown 契约，truncate-to-empty 不在本期。
- [x] 建立并覆盖 text merge contract fixtures：clean、same-region conflict、empty base、EOF、adjacent edit、add/delete、CRLF/LF、Unicode、marker-like content、超限和非法 UTF-8。
- [ ] 在目标 API 镜像冻结并验证资源合同：1 MiB/4 MiB/64 KiB 输入输出上限、1024 conflicts、5 秒 deadline、每进程并发 2；用上限与病理 fixture 双并发跑 30 轮，要求全部在 deadline 内且增量峰值 RSS 不超过 128 MiB，并保存 Git version/fingerprint、延迟和 RSS 证据。失败则回到规划审阅，不进入 Phase 2，也不切换算法。

Rollback point: 只含测试 fixture/合同草案，可直接停止，不产生 Schema 或运行时行为变化。

### Phase 1: Forward migration and domain contracts

- [x] 新增前向 migration：Proposal `current_revision_id`、immutable Revision text snapshot、lineage、Revision command receipt、`proposal_revision_dispatch`、FK/unique/check/immutable trigger 和 legacy backfill。
- [x] 将生产 Proposal 创建、Get/List、Approval、Authorization、Preflight、Bootstrap、Writeback 查询切到 exact current pointer；初始 typed Proposal 同事务写入 Revision pointer。
- [x] 为 legacy Proposal Workflow binding 增加一致性审计与 fail-closed 围栏；孤儿/多义绑定不会被猜测投影。
- [x] 扩展 Proposal/Revision domain：aggregate version wire projection、base snapshot/lineage、editable capability、append command/result 和稳定错误。
- [x] 扩展状态迁移事实源与数据库 trigger：只有 `current_revision_id` 严格切换到 `revision_no+1` 时允许 `ready_for_review|needs_revision -> ready_for_review`；保持其它同状态 update/type/status 不变。
- [ ] 添加真实 PostgreSQL migration/并发测试；当前仅有 domain、repository contract/fixture 和 integration compile 证据。

Rollback point: Schema 保持 additive；应用可不开放新 capability。禁止删除已经生成的 Revision 或执行破坏性 Down。

### Phase 2: Three-way merge adapter

- [x] 定义不泄漏 Git 类型的 `ThreeWayMerger` port、versioned input/output/conflict DTO 和 limits。
- [x] 实现固定 `git-merge-file/diff3/myers/marker32/v1` Adapter：无 shell/网络/config、私有临时权限、context timeout、有界 stdout/stderr、并发闸门、退出码分类与必然 cleanup。
- [x] 用 raw-byte 状态机解析固定 marker 为 marker-free candidate + structured conflicts；完整保留 marker 行显式拒绝，普通 marker-like text 不得误判。
- [x] 在 API production composition 增加固定 flags/exit/golden conformance probe；同 contract 不允许 silent fallback 或算法漂移。
- [x] 用 contract fixture 做 adapter tests；同输入结果和 fingerprint 稳定，不记录正文或原始 stderr。
- [x] 将 Adapter 装入 API production composition；探针/依赖缺失时 readiness 与 revision capability fail closed。

Rollback point: Adapter 未接写路径，可关闭 composition capability；Proposal/Workspace 无副作用。

### Phase 3: Application and PostgreSQL UoW

- [x] 新建普通 `file_patch/REPLACE` Proposal 时从 Workspace 捕获 exact base snapshot，校验 caller base hash，并与 Revision 同事务持久化；legacy/超限行为按设计返回显式 capability。
- [x] 实现 merge preview：服务端加载 authoritative source/snapshot、读取 current、调用 merger、生成 fingerprint；不保存预览正文。
- [x] 实现 append service：校验完整正文/元数据/conflict acknowledgments，重读 current，重算 preview/fingerprint/change hash/request hash。
- [x] 实现 PostgreSQL append UoW：advisory receipt replay 后沿用 `Authorization -> Proposal -> Workflow` 锁序，通过 current pointer/version/source CAS、snapshot/lineage/event 原子提交。
- [x] 漂移检测使用 expected version/current Revision 推进 `needs_revision`，再经 Workflow owner 请求安全取消；旧 Run 未终态时 append fail closed，Revision Repository 不直接改 Workflow 表。
- [x] 对 `needs_revision` 做副作用 fence：原子 revoke issued Authorization；consumed 必须绑定 exact execution；execution 仅 `needs_revision`、无人工恢复、无 Proposal Commit 时可 supersede，其它状态拒绝。
- [x] 给 Authorization issue/consume、Atomic Begin 和数据库 trigger 增加 `revision_id=current_revision_id`；历史 exact replay 可读但不能恢复旧能力。
- [x] 重构 Approval dispatch 的当前投影与 replay 为 per-Approval binding，并加入第二 Revision 的 current fence。
- [x] 实现 bounded Revision list/detail Repository；历史 Approval/Workflow 只按所选 Revision 返回。
- [ ] 添加真实 PostgreSQL 事务故障/并发和新 Revision 完整 Approval -> Preflight -> Writeback -> Commit 链路测试；局部 service/repository/adapter 测试已通过。

Rollback point: 关闭 append/preview capability，保留只读 history；已提交 Revision 和旧执行事实不可删除。

### Phase 4: HTTP, OpenAPI, composition, and events

- [x] 为 Proposal summary/detail 增加 `version` 和严格 `revision_capability`。
- [x] 实现 preview、append、history/detail routes；正文响应加 `private, no-store`，strict decode 前限制 request wire bytes，编码后限制 preview response bytes，请求字段/路径/Idempotency-Key 有界。
- [x] 实现稳定 Problem 映射和 bounded details；测试错误响应、日志与 event 不含正文、绝对路径、argv/stderr。
- [x] 更新 `api/openapi/openapi.json`、operation 对等、严格 schema 和前端所需 examples。
- [x] 新增 `proposal.revised` append-only event；更新事件 contract，只承载失效元数据。
- [ ] 完成真实 PostgreSQL HTTP integration tests；API composition/readiness 已实现并通过局部测试。

Rollback point: 路由可关闭写 capability；历史读仍可保留。不得把 5xx fallback 成前端 merge。

### Phase 5: Revision workbench and history UI

- [x] 在 `web/src/api/business.ts` 严格解码 Proposal version/capability、Problem errorCode/details、merge preview、append replay、history/detail；拒绝未知字段和语义错绑。
- [x] 抽取共享 `MonacoTextEditor`，让 Authoring 现有 Markdown editor 成为薄 wrapper；保持 worker、model URI、detach/dispose 和 lazy loading 测试。
- [x] 新建 reducer 驱动的 `ProposalRevisionWorkbench`，实现来源轨迹、双向 Diff tabs、冲突确认、完整候选编辑、Evidence/Risk/Rollback 表单和显式创建。
- [x] 在 Proposal detail 接入 eligibility、history/historical read-only；Revision 模式期间隐藏普通 Approval/preflight 主操作。
- [x] 实现 409 authority/stale、本地草稿保留、explicit re-merge、delivery unknown exact-key retry、dirty exit dialog/beforeunload 和成功后的 reapproval 提示。
- [x] 扩展 Proposal/current/history/Workflow 的 query 与 SSE invalidation；成功后保持“先 Proposal 后新 current binding”的读取顺序。
- [x] 完成 desktop/`390x844` scoped CSS；避免多列代码墙、嵌套卡片、横向溢出、fixed footer 覆盖和颜色单独表达状态。
- [x] 添加 API decoder、workbench reducer/component、Proposal integration、event store、Monaco registry/route-cycle、键盘与 focus tests。

Rollback point: 隐藏入口即可退回现有只读 Diff/漂移阻断；服务端 Revision/history 事实继续可读。

### Phase 6: End-to-end verification and documentation

- [ ] 新增隔离的 Proposal Revision browser smoke fixture/脚本/Make target，使用真实 PostgreSQL、API、Worker、Vite、Workspace 文件与 Git（当前环境没有可用测试库，且用户未要求启动浏览器）。
- [ ] 桌面和 `390x844` 覆盖：非重叠合并、同区冲突、再次漂移、两个浏览器并发、历史查看、刷新恢复、重新审批/preflight/writeback/Commit。
- [ ] response-loss 在 Repository/API fault seam 验证 exact replay；服务端 receipt/replay 已有局部测试，浏览器 delivery-unknown 尚未在真实链路验证。
- [ ] 执行敏感信息扫描，确认正文不在 logs/events/Problems/URL/Browser Storage（已完成静态代码检查，正式扫描脚本待补）。
- [x] 同步 `docs/requirements.md` AC-13、`docs/user-guide.md`、架构/运行说明和对应 Trellis spec；父任务状态未擅自改为完成。
- [x] 运行 `go-review`、`sql-code-review`、`code-review-and-quality` 与跨层静态复核；发现的当前范围问题已修复，剩余真实环境盲区已列明。

Rollback point: merge/append 没有直接 Workspace 副作用；停止 feature capability 即可阻止新写命令。已经 Safe Writeback 的 Revision 仍按现有 reverse Commit/人工恢复契约处理，不重写 Git 历史。

## 2. Expected File Areas

| Layer | Expected areas |
| --- | --- |
| Migration | `migrations/<next>_proposal_revision_three_way_merge.sql`, migration integration tests |
| Domain/Application | `internal/changecontrol/domain/**`, `internal/changecontrol/application/**` |
| Adapters | `internal/changecontrol/adapter/{postgres,approvaldispatchpostgres,localfs,...}/**`, new merge Adapter under existing platform/adapter conventions |
| HTTP/Composition | `internal/changecontrol/http/**`, `cmd/api/**`, `api/openapi/openapi.json` |
| Events/Workflow | Change Control `proposal.revised` event contract, Workflow cancellation/binding/replay, Safe Writeback validation |
| Web API/UI | `web/src/api/business*`, `web/src/features/business/**`, `web/src/shared/Monaco*`, `web/src/events/**`, scoped CSS |
| E2E/Docs | `web/e2e/**`, `deploy/**`, `Makefile`, `docs/**`, `.trellis/spec/**`, parent task status |

Migration filename must use the next free number at implementation time; current dirty worktree may add migrations before this task starts。

## 3. Verification Commands

先运行直接相关且受限的命令，再扩大到任务完整门禁：

```bash
go test ./internal/changecontrol/domain ./internal/changecontrol/application ./internal/changecontrol/http
go test ./internal/changecontrol/adapter/postgres ./internal/changecontrol/adapter/approvaldispatchpostgres
go test ./internal/platform/migration -run 'ProposalRevision|ThreeWay|ApprovalDispatch'
go test -race ./internal/changecontrol/...
go vet ./internal/changecontrol/... ./cmd/api/...
npm run test --prefix web -- --run src/api/business.test.ts src/features/business/ProposalRevisionWorkbench.test.tsx src/features/business/ProposalsPage.test.tsx src/events/event-store.test.tsx
npm run lint --prefix web
npm run typecheck --prefix web
npm run build --prefix web
make openapi-check
make proposal-revision-merge-browser-smoke
git diff --check
```

测试过滤器和新 Make target 以实现后的真实名称为准；不得把不存在或未运行的命令写成通过。后端单项测试默认 60 秒超时，真实浏览器/Compose 门禁按脚本自己的有界超时执行。

## 4. Review Gates

- [x] Go review：context/timeout、error wrapping、nil dependency、状态机、幂等、并发、资源释放、生产 composition。
- [x] SQL review：参数化、固定锁序、FK/check/partial unique、immutable trigger、receipt replay、legacy backfill、Workspace scope 和事务边界。
- [x] Frontend review：strict decoder、Query/local/SSE ownership、Monaco lifecycle、409/delivery-unknown、响应式、键盘/focus/a11y、无 Browser Storage 正文。
- [x] Cross-layer review：OpenAPI/HTTP/Web 字段与错误完全对等；latest Revision/Approval/Workflow 语义在 list/detail/history/writeback 一致。
- [x] Security review：正文/路径/Git/临时文件/日志/Problem/Event/浏览器缓存和命令注入边界。
- [ ] Final Trellis check：PRD AC 仍有真实 PostgreSQL、浏览器和完整 Writeback 链路证据缺口，不能标为完成。

## 5. Completion Evidence

当前已保存/可复核的证据：

- `00082_proposal_revision_three_way_merge.sql` 的前向 schema/backfill/guarded-down；真实 PostgreSQL 并发、replay、rollback 证据待补。
- Git merge contract tests、固定算法/参数和运行时 conformance probe；目标镜像 30 轮 RSS 基准待补。
- OpenAPI parity、后端/Web 定向测试、typecheck/lint/build 和 `git diff --check`。
- Revision history/detail、409 草稿保留和 conflict-id 处理的组件/API 测试；desktop/`390x844` 浏览器 trace 待补。
- Append receipt、current pointer、snapshot/lineage、dispatch/current fence 的局部可反查事实；完整新 Revision Approval -> Preflight -> Safe Writeback -> Git Commit 链待真实环境补验。
- Go/SQL/frontend/cross-layer/security review 发现、修复项、剩余风险和 capability 回滚开关。
