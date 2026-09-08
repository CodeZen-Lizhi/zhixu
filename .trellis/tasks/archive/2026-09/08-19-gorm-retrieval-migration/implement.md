# Retrieval GORM Migration Implementation Plan

更新：2026-09-08。实现与本 child 门禁已完成，等待主会话处理 Final 和任务归档。
2026-08-21 的规划、ordinary GORM/native 基础实现及用户已有改动全部保留；本次按父任务
`research/lean-test-policy-2026-09-01.md` 补齐缺失依赖和验证。

## 1. Scope And Contracts

- [x] 读取 child PRD/design/implement、父任务与 Foundation、Workflow、Change Control、Model Settings 契约。
- [x] 只修改 `internal/retrieval/**` 和本任务工件；不改 cmd、migration、其他 owner、active pointer。
- [x] GORM root、UoW、native capability 与 River 使用同一完整 platform Pool。
- [x] Domain/Application 只暴露 opaque TransactionScope；legacy any seam 保留到 Final。
- [x] 普通 GORM 路径无 pgx Tx/Row/Rows、独立 pool、Schema 管理、nested transaction 或 legacy fallback。

## 2. Ordinary Repository And Native Capability

- [x] Index/Embedding、lexical/vector batch、ready/activate/replay、Search/Evidence、Regression、Delivery 与 Processor Context。
- [x] Search/Evidence 复用固定 SQL、scanner、validation、bounded batch 与 ordinality；Scoped SourceVersion read 可供 Organizing 使用。
- [x] Manifest COPY、Workspace Snapshot session/temp/COPY、Source Refresh session lease 三个 native interface 保持窄边界。
- [x] Completion 激活复用已经排序取得的 Index 锁，保留 DB clock、CAS、deferred closure 和历史 replay。
- [x] 修复 GORM Delivery.Fail 中 CASE/NULL 时间参数的 SQLSTATE 42804：显式 `::timestamptz`。
- [x] 修复 shared Snapshot 增量 Source 查询的同名列歧义，SELECT 全部限定为 source_manifest。

## 3. Dispatcher And River

- [x] NewGORMDispatcher 接收同 Pool、IDs、River options、scoped fence、Workflow outbox、CC verifier。
- [x] 拒绝 legacy options.EnqueueFence 和 nil/typed-nil 依赖，内部组装 official database/sql producer。
- [x] First 顺序：Workflow claim -> workspace xact lock -> CC binding -> Delivery -> River -> Workflow publish -> commit。
- [x] Retry 保持 due/FIFO、workspace try-lock、dispatch generation 与 River 同事务。
- [x] Workflow NewGORMReindexOutbox 与 CC NewGORMRepository 真实 owner 实现已接入，无跨 owner mutation 副本。
- [x] 实库确认真实 River insert 后注错使 Delivery/Job/published_at 全回滚；正常重试共同提交。
- [x] 实库确认持有 workspace lock 时 Retry 不等待，释放后只增加一次 dispatch generation，published_at 不变。

## 4. Completion

- [x] 独立 NewGORMCompletionRepository 使用真实 CC 两阶段 Lock/Complete scoped capability。
- [x] 唯一 outer UoW，锁序 workspace -> Proposal -> Execution -> Delivery -> Attempt -> sorted Index。
- [x] final lease、cleanup、succeeded Workflow 与 immutable binding/manifest closure 检查。
- [x] Activation、Attempt/Delivery、Execution/Proposal CAS 同一 scope；owner 最后一步注错全回滚。
- [x] 首次 commit response-loss 返回错误，后续 exact/historical replay 使用已持久化事实。
- [x] Completion fixture 使用合法 Revision Dispatch binding，移除重复的 replica-role Proposal 绑定写入。

## 5. Real PostgreSQL Evidence

全部原位复用既有 integration 文件。testdb.Require 默认 Testcontainers，Availability=FailWhenUnavailable，
外部 admin URL 为可选；每个 legacy/GORM 变体独立迁移数据库。

- [x] FTS build/ready/activate/replay 与 hybrid vector batch legacy/GORM。
- [x] Active Search、Source/path/time filters、bounded provenance、incremental Snapshot 与既有 EXPLAIN legacy/GORM。
- [x] Regression pass/replay 与 structural mismatch legacy/GORM，Processor Context GORM。
- [x] Dispatcher 真 River rollback/commit 与 Retry try-lock/generation。
- [x] Completion response-loss/historical replay、CC owner rollback、prerequisite 和锁等待后的 lease recheck。
- [x] 仅接受所选场景真实 PASS；此前 SQL/fixture 失败及修复记入验证记录。

未执行完整 SQLSTATE/pgvector/connection-fault 矩阵、pgx Worker 进程烟测、HTTP smoke、500k/384 维容量。
这些是按风险或 Final 按需运行的独立范围，不用小 fixture 伪造完成证据，不再默认阻断本 child。

## 6. Review And Delivery

- [x] gofmt 仅作用于本模块相关 Go 文件。
- [x] Retrieval 既有 go test、局部 race、integration-tag go vet。
- [x] Go Review 与 SQL Review 自检：类型/错误/context、状态机、Workspace、锁序、CAS、批量、资源生命周期。
- [x] Trellis Check 自检完成；本角色不递归分派 check agent。
- [x] 最终 task validate 与 git diff --check；仅有既有大文件注入截断 warning。
- [x] 更新 PRD/design、验证记录、JSONL context；修复已归档 CC design 引用。
- [x] 写 final-handoff.md，注明独立 Completion、同 Pool 构造及不可整文件盲删的共享/native helpers。

## Rollback

本 child 没有修改生产 Composition 或 migration；生产行为不因 staged constructor 自动切换。
只撤销本次范围内改动，不回滚用户其他改动或历史事实。Final 负责生产接线、legacy 删除与发布回滚。
