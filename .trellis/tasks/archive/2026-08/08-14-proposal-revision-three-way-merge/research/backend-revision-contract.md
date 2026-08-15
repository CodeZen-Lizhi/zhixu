# Research: Proposal Revision 后端、PostgreSQL 与 API 契约

- Query: 为 append-only Proposal Revision 编辑、服务端三方合并和乐观并发设计领域/Application/PostgreSQL/API 契约；重点核对 Proposal 级 Workflow 绑定迁移、旧 Workflow/Authorization 围栏、状态范围、兼容风险和测试矩阵。
- Scope: internal
- Date: 2026-08-14

## Findings

### 1. 结论摘要

首期应严格限定为普通 `file_patch/REPLACE`；`restore_document`、`CREATE_ONLY` 和三类结构化 Proposal 均 fail closed。一次用户确认必须只产生一个新的不可变 Revision，不修改旧 Revision、Approval、Authorization、Workflow 或 Writeback Execution。

完整实现不能只增加一个 `INSERT proposal_revision` 接口。当前模型还缺少四个必须先补齐的持久事实：

1. Proposal 的显式 `current_revision_id`。当前 Repository 通过 `ORDER BY revision_no DESC LIMIT 1` 推断最新 Revision，数据库无法可靠证明一次 Proposal 状态更新确实伴随 `revision_no+1`。
2. 每个 Revision 的精确 `base` 正文快照。现有 Revision 只有 `base_hash`，目标已经漂移后无法重建三方合并的原始 base。
3. Revision/Approval 级 append-only Workflow binding。现有 `proposal.workflow_run_id` 是 Proposal 级、唯一且绑定后不可更改，第二个 Revision 无法获得自己的 Workflow。
4. Revision 创建命令回执。现有 Proposal 创建幂等键属于整个 Proposal，不能表示多次 Revision 命令或响应丢失后的精确重放。

推荐的支持 source status 只有：

- `ready_for_review`：允许；必须仍是当前 Revision，且不存在其 Approval、Workflow、Authorization 或 Writeback Execution。
- `needs_revision`：有条件允许；必须完成本文第 7 节的旧能力围栏。若存在旧执行，只允许 `writeback_execution.status=needs_revision`、`manual_recovery_required=false`、无 Proposal Commit，且旧 Workflow 已终态、所有未消费 Authorization 已撤销。
- `approved`：不直接允许。先由权威漂移检查将 Proposal 原子推进到 `needs_revision`，请求旧 Workflow 安全取消，再等待满足围栏。
- `draft`、`validating`、`applying`、`applied`、`verifying`、`completed`、`rejected`、`apply_failed`、`verify_failed`、`rolled_back`、`cancelled`、`deferred`：首期不允许。`rejected` 后修订是另一项产品语义，不能借本功能静默放开。

### 2. 已检查文件

- `.trellis/workflow.md`：Trellis 阶段、规范读取和验证流程。
- `.trellis/tasks/08-14-proposal-revision-three-way-merge/prd.md`：本任务 R1-R5、AC1-AC10 和首期范围。
- `.trellis/spec/backend/database-guidelines.md`：append-only、CAS、幂等、前向迁移和跨存储 Saga 约束。
- `.trellis/spec/backend/error-handling.md`：409 details、安全错误和未知结果恢复约束。
- `.trellis/spec/backend/http-boundary.md`：OpenAPI/runtime route inventory 和严格 HTTP 边界。
- `.trellis/spec/backend/document-history-contract.md`：历史 Revision/恢复契约；本期 restore 只用于确认不回归。
- `docs/requirements.md`：稳定 Proposal/Revision、Approval 和 AC-13 三方合并需求。
- `docs/architecture/domain-and-data.md`：Approval 必须绑定 Revision、Change Hash、Target Version 和 Git HEAD。
- `docs/architecture/application-contracts.md`：Command 的 Idempotency Key/CAS 和严格 JSON 约束。
- `docs/architecture/adr/0019-mature-framework-first.md`：合并引擎必须先评估成熟方案，不得先手写 diff3。
- `internal/changecontrol/domain/model.go`：Proposal、Revision、Approval、Change Hash 和状态机。
- `internal/changecontrol/domain/typed_proposal.go`：typed Proposal 范围和 `file_patch` 校验。
- `internal/changecontrol/domain/authorization.go`：一次性 Tool Authorization 的 issued/consumed/revoked/expired 状态。
- `internal/changecontrol/domain/writeback.go`：Writeback 状态机和取消安全判断。
- `internal/changecontrol/domain/repository.go`：当前 Repository 端口没有 append/history/exact-revision 方法。
- `internal/changecontrol/application/service.go`：创建、current-content、审批、preflight、授权签发/消费和 needs_revision 路径。
- `internal/changecontrol/application/writeback_service.go`：Safe Writeback 当前总是重新读取“最新 Proposal”。
- `internal/changecontrol/workflow/bootstrap.go`：Workflow Bootstrap 当前把 Run 与 Proposal 级 Workflow 指针及最新 Revision 比对。
- `internal/changecontrol/adapter/postgres/repository.go`：创建、latest Revision 查询、审批、授权和 Proposal CAS。
- `internal/changecontrol/adapter/postgres/repository_writeback.go`：Atomic Begin、Authorization 锁序和 durable execution。
- `internal/changecontrol/adapter/approvaldispatchpostgres/repository.go`：Approval、Proposal -> Run、Workflow/Outbox 的当前事务边界。
- `internal/changecontrol/adapter/localfs/reader.go`：受控 1 MiB UTF-8 当前正文读取与同快照 SHA-256。
- `internal/changecontrol/adapter/localfs/markdown_validator.go`：统一 Markdown Parser，当前拒绝空最终正文。
- `internal/changecontrol/http/handler.go`：现有路由、响应投影和 Problem details。
- `api/openapi/openapi.json`：当前公开 Proposal/Approval/current-content/preflight wire 契约。
- `web/src/api/business.ts`：前端 Proposal 严格 decoder，新增字段必须同步。
- `migrations/00004_change_control.sql`、`00005_change_control_hardening.sql`、`00008_write_authorization.sql`、`00009_safe_writeback.sql`、`00013_approval_writeback_dispatch.sql`、`00062_m7_timeline_impact_v2.sql`、`00070_change_control_create_only.sql`、`00075_document_file_history.sql`：当前表、触发器和最终覆盖顺序。
- `migrations/00069_document_draft_authoring.sql`、`internal/authoring/adapter/postgres/repository.go`：项目既有 command receipt 与 advisory idempotency lock 模式。

### 3. 当前代码事实与缺口

#### 3.1 Revision 已不可变，但没有“当前指针”和 base 正文

- `proposal_revision` 已有 `(proposal_id, revision_no)` 唯一约束和 `(proposal_id,id,change_hash)` 绑定约束，Revision/Approval 更新删除由触发器拒绝（`migrations/00004_change_control.sql:16`, `migrations/00004_change_control.sql:33`, `migrations/00004_change_control.sql:55`, `migrations/00004_change_control.sql:77`）。
- Domain Revision 只有 `BaseHash` 和建议 `Content`，没有 base 正文（`internal/changecontrol/domain/model.go:102`）。
- `GetProposal` 用 lateral 子查询按最大 `revision_no` 取最新 Revision（`internal/changecontrol/adapter/postgres/repository.go:743`, `internal/changecontrol/adapter/postgres/repository.go:757`, `internal/changecontrol/adapter/postgres/repository.go:767`）。并发虽可依赖 Proposal 行锁，但数据库触发器不能证明“状态回到 ready”与某个新 Revision 是同一原子事实。
- 当前 `CreateProposal` 只写 `revision_no=1`，REPLACE 创建前不读取并保存服务端 base 正文（`internal/changecontrol/adapter/postgres/repository.go:58`, `internal/changecontrol/adapter/postgres/repository.go:152`; `internal/changecontrol/application/service.go:325`）。
- current-content 从同一受控文件快照返回正文和 SHA-256，限定 1 MiB、UTF-8（`internal/changecontrol/adapter/localfs/reader.go:103`, `internal/changecontrol/adapter/localfs/reader.go:121`, `internal/changecontrol/adapter/localfs/reader.go:134`）。这可作为今后快照来源，但不能恢复已经漂移的 legacy base。

#### 3.2 Proposal 状态机没有“append 后 ready”的原子迁移

- 当前领域只允许 `needs_revision -> draft/cancelled`，不允许 `needs_revision -> ready_for_review`、`ready_for_review -> ready_for_review` 或 `draft -> ready_for_review`（`internal/changecontrol/domain/model.go:185`, `internal/changecontrol/domain/model.go:210`）。
- 最终数据库函数同样只有 `needs_revision -> draft/cancelled`；应修改最新定义而不是历史迁移（`migrations/00062_m7_timeline_impact_v2.sql:1061`, `migrations/00062_m7_timeline_impact_v2.sql:1112`）。
- 推荐新增一个“current_revision_id 精确变化”特殊分支：只有新指针属于同一 Proposal、`new.revision_no=old.revision_no+1`、类型为 file_patch、模式/路径不变、source status 属于支持集合时，才允许 `ready|needs_revision -> ready` 且 Proposal `version+1`。普通同状态更新仍应拒绝。

#### 3.3 Workflow 当前是 Proposal 级单例，第二 Revision 会错误重放旧 Run

- Domain 将 `WorkflowRunID` 定义为 Approved Proposal 唯一且绑定后不清除（`internal/changecontrol/domain/model.go:71`, `internal/changecontrol/domain/model.go:80`）。
- 数据库列有唯一索引，且非空后禁止改变（`migrations/00013_approval_writeback_dispatch.sql:3`, `migrations/00013_approval_writeback_dispatch.sql:15`, `migrations/00013_approval_writeback_dispatch.sql:86`）。
- Approval dispatch 看到 `p.workflow_run_id IS NOT NULL` 就直接重放该 Run，与请求 Revision 无关（`internal/changecontrol/adapter/approvaldispatchpostgres/repository.go:74`, `internal/changecontrol/adapter/approvaldispatchpostgres/repository.go:128`）。
- Bootstrap 又要求最新 Proposal 的 `WorkflowRunID`、Revision 和 Approval 都等于当前 Run input（`internal/changecontrol/workflow/bootstrap.go:77`, `internal/changecontrol/workflow/bootstrap.go:201`）。若 append 新 Revision 而旧 Run 尚可运行，旧 Run 会读取到新 Revision 并进入 binding conflict；若新审批仍读取旧 Proposal 指针，则会错误重放旧 Run。
- Writeback Begin/Resume 也用 `GetProposal` 读取最新 Revision（`internal/changecontrol/application/writeback_service.go:150`, `internal/changecontrol/application/writeback_service.go:196`, `internal/changecontrol/application/writeback_service.go:276`）。因此必须同时引入 exact-revision execution projection，不能只改审批响应。

#### 3.4 旧 Authorization 会在新审批后“复活”，必须显式围栏

- Authorization 已支持 `issued -> revoked|expired|consumed`，且 consumed 不可撤销（`internal/changecontrol/domain/authorization.go:17`; `migrations/00008_write_authorization.sql:100`; `internal/changecontrol/adapter/postgres/repository.go:1177`）。
- 当前消费校验只验证 Proposal 当前状态为 approved/applying、显式传入的旧 Revision/Approval 自洽；它不检查该 Revision 是否仍是 Proposal 当前 Revision（`internal/changecontrol/adapter/postgres/repository.go:1232`, `internal/changecontrol/adapter/postgres/repository.go:1247`）。
- Proposal 变成 `needs_revision` 时，issued credential 立即失效，因为消费必须看到 approved/applying；但 append 后再次批准新 Revision，若旧 Authorization 仍为 issued，旧 Revision/Approval 可能重新满足当前校验。必须在 append 前撤销全部 source Revision 的 issued Authorization，并把“Revision 必须是 current_revision_id”加入 Authorization 签发、消费、Atomic Begin 和数据库 trigger。
- 已 consumed Authorization 的精确 replay仍应可返回历史消费事实，不能把它伪装为新能力；但任何新的 Begin 必须要求 source Revision 是当前 Revision。现有“Proposal 状态推进后 consumed replay 仍成功”的语义应保留。

### 4. 目标领域不变量

建议新增领域类型：

- `RevisionMergeInput`：Workspace/Proposal/source Revision ID、source Change Hash、expected Proposal version。
- `RevisionMergePreview`：base/current/proposed 精确摘要、merge algorithm/version、preview hash、候选正文、结构化 conflicts。
- `AppendProposalRevision`：幂等键、expected Proposal version、source Revision ID/hash、expected current hash、preview binding、完整最终正文、Evidence/Risk/Rollback、完整冲突解决集合。
- `AppendProposalRevisionResult`：Proposal、new Revision、replayed。
- `ProposalRevisionExecutionBinding`：exact Revision、Approval、Revision Workflow binding、是否 current、可选 Writeback Execution。

必须冻结以下不变量：

1. 新 Revision 与 source 同 Proposal、同 `target_path`、同 `REPLACE` mode；不接受客户端提交 target/type/risk level 来改写 Proposal identity。
2. `new.revision_no=source.revision_no+1` 且 source 等于 `proposal.current_revision_id`；不存在分叉 latest。
3. 新 `base_hash=server_observed_current_hash`，新 base snapshot 内容的 raw UTF-8 SHA-256 必须等于该 hash。
4. 新 `change_hash` 继续使用现有 REPLACE v1 算法，避免改变历史审批语义（`internal/changecontrol/domain/model.go:231`, `internal/changecontrol/domain/model.go:304`）。
5. Change Hash 会把 CRLF 规范为 LF；Revision command 的 `request_hash` 和 `preview_hash` 必须额外绑定 raw content SHA-256，确保 LF/CRLF 不同请求不会被误判为同一幂等请求。
6. Proposal `risk_level` 仍不可变；用户只能修改 Revision 的自由文本 `risk`。数据库当前明确禁止 risk level 改变（`migrations/00062_m7_timeline_impact_v2.sql:1082`）。
7. 合并 preview 无 Proposal/Revision/Approval/Workflow 写入；它只读受控 Workspace。append command 是唯一新增数据库写命令，且仍不写文件/Git。
8. 后端重新计算 preview/conflict 集合；不信任浏览器提交的 candidate、conflict IDs 或“已解决”布尔值。
9. 冲突使用结构化 ID/范围/三方片段，不以解析 `<<<<<<<` 文本作为事实。最终请求提交所有 expected conflict ID 的显式 resolution；服务端重算并验证集合完整。合法正文中原本存在 marker-like 文本不能被误判。
10. preview/append 的 base/current/proposed/final 每份均采用现有审阅上限 1 MiB；响应和 JSON body 另加明确总上限。超限、非法 UTF-8、NUL/不可存 PostgreSQL text 的输入 fail closed。

### 5. PostgreSQL 前向迁移建议

仓库当前最新迁移为 `00081_workspace_root_rebinding.sql`；实现时应从新的 `00082_*` 前向迁移开始，不能修改历史迁移。

#### 5.1 Proposal 当前 Revision 指针

在 `change_control.proposal` 新增非空 `current_revision_id uuid`：

- 迁移时按每个 Proposal 最大 `revision_no` 回填；没有 Revision 或出现不可唯一解析时中止迁移。
- 给 `proposal_revision` 增加 `(proposal_id,id)` unique binding，再增加 deferred FK `(proposal.id, proposal.current_revision_id) -> proposal_revision(proposal_id,id)`。
- 所有 typed Proposal 创建事务在插入 Proposal 时即写入将要创建的 Revision ID，依靠 deferred FK 在同事务随后插入 Revision；无需把初始 Proposal version 人为增加到 2。
- `GetProposal`/List/Approval/Authorization/Writeback 全部改为 join `p.current_revision_id`，停止用最大 revision 推断执行身份。
- 更新最终 `validate_proposal_transition()`：普通更新要求 current pointer 不变；append 分支要求指针严格从 source 指向 `revision_no+1`，Proposal version 只加一，状态原子归约为 ready。

该指针是防止旧 Approval/Authorization/Workflow 在新审批后复活的数据库锚点，不只是查询优化。

#### 5.2 Append-only base snapshot 表

建议单独建 `change_control.proposal_revision_base_snapshot`，而不是给所有 typed Revision 加大字段：

```text
proposal_id       uuid not null
revision_id       uuid primary key
base_hash         text not null, sha256 pattern
content           text not null, octet_length <= 1 MiB; 允许空字符串
schema_version    text not null = proposal-base-snapshot/v1
created_at        timestamptz not null
```

- composite FK 指向 `(proposal_id,revision_id)`；insert trigger 查询 Proposal/Revision，强制 `proposal_type=file_patch`、`target_mode=REPLACE`、`base_hash=revision.base_hash`，并校验 `encode(sha256(convert_to(content,'UTF8')),'hex')=base_hash`。
- update/delete/truncate 全部拒绝。
- 新 REPLACE Proposal 创建时，Application 先进行现有幂等 lookup，再从服务端读取 base snapshot，校验请求 base hash，和 Proposal/Revision 同事务插入 snapshot。
- legacy Revision 无 snapshot 且 current hash 已漂移：返回 `PROPOSAL_BASE_SNAPSHOT_UNAVAILABLE`，不得从 Git 或当前文件猜测 base。
- legacy Revision 无 snapshot但 current hash 仍等于 source base：preview 可把这次 current 作为已证明的 base；真正 append 时再次读取并在同事务补写 source snapshot 和 new Revision snapshot。Preview 查询本身不应偷偷写库。

#### 5.3 Revision/Approval 级 Workflow binding

新增 append-only `change_control.proposal_revision_dispatch`：

```text
workspace_id      uuid not null
proposal_id       uuid not null
revision_id       uuid primary key
approval_id       uuid not null unique
workflow_run_id   uuid not null unique
created_at        timestamptz not null
```

- composite FK/trigger 必须证明 workspace、Proposal、Revision、approved Approval 和 Workflow Run 完全一致；一 Revision 最多一个 dispatch。
- update/delete/truncate 拒绝。
- `proposal.workflow_run_id` 保留为 legacy 物理列，不再作为多 Revision 的权威事实；新 Domain `Proposal.WorkflowRunID` 从当前 Revision 的 dispatch join 派生。
- 对 legacy `p.workflow_run_id` 的回填必须从该 Workflow 的 safe-writeback node input 中解析 `proposal_id/revision_id/approved_change_hash`，再与 Approval 交叉校验后插入 dispatch；不能简单猜“最新 Approval”。任一不一致应使迁移失败并要求数据修复。
- 新 approval dispatch 按 current Revision 查 `proposal_revision_dispatch` 做 replay；首次批准创建 Workflow 后在同事务插入 dispatch。Revision 1 且 legacy pointer 为空时可双写旧 pointer以支持回滚读取；Revision 2+ 不修改该旧指针。
- Bootstrap、Authorization 和 Writeback 改用 `GetProposalRevisionExecutionBinding(proposalID, revisionID)`；新签发/新 Begin 还要求 `revision_id=current_revision_id`。历史 consumed/execution exact replay可以返回旧事实，但不得创建新能力或副作用。

兼容风险：旧二进制仍会看到 Proposal 级旧指针并错误 replay，因此 Revision 2 功能必须在所有 API/Worker 都升级完成后通过 feature gate 启用。保留旧列只能支持数据库回滚读取，不能让新旧二进制无限期混跑。

#### 5.4 Revision command receipt

新增 immutable `change_control.proposal_revision_command`：

```text
workspace_id                 uuid not null
idempotency_key              text not null, <=128, no CR/LF
request_hash                 text not null
proposal_id                  uuid not null
source_revision_id           uuid not null
source_change_hash           text not null
expected_proposal_version    bigint not null
expected_current_hash        text not null
preview_hash                 text not null
merge_algorithm              text not null
merge_algorithm_version      text not null
result_revision_id           uuid not null
result_revision_no           integer not null
result_proposal_version      bigint not null
created_at                   timestamptz not null
primary key (workspace_id,idempotency_key)
```

- request hash 使用版本化 canonical schema，绑定完整 final content raw hash、Evidence/Risk/Rollback、source/current/preview/conflict resolution 和算法版本。
- 精确 receipt replay 必须发生在重新读取文件或校验当前 Proposal 状态之前；同键异 request hash 返回稳定 409。
- 同键首次并发沿用项目已有 `pg_advisory_xact_lock(hashtextextended(workspace + separator + key,0))` 模式（`internal/authoring/adapter/postgres/repository.go:384`），再读 receipt。
- 两个不同 key 但同 expected Proposal version/current hash 的命令通过 Proposal 行锁和 `WHERE version=$expected AND current_revision_id=$source` 保证最多一个成功。
- Receipt、new Revision、new snapshot、Proposal current pointer/version/status 和 `proposal.revised` 事件必须同一 PostgreSQL 事务提交；事件不得携带正文或路径。

### 6. Application/Repository 命令流程

#### 6.1 Merge preview

1. 严格校验 Proposal/source ID、Change Hash、expected Proposal version。
2. 用 exact current pointer 查询 source Revision；只接受 file_patch/REPLACE 和支持状态。
3. 读取 base snapshot。legacy 缺失时仅在 server current raw hash 等于 source base hash 时临时使用 current 作为已证明 base。
4. 从受控 Workspace 读取 current 正文+hash；使用持久 source content 作为 proposed。
5. 交给独立 `ThreeWayMerger` port；Adapter 返回确定的 candidate、结构化 conflict、algorithm/version。
6. 计算 preview hash，绑定 raw base/current/proposed hash、Proposal/version、source ID/hash、target/mode、algorithm/version 和 conflict set。
7. 返回 `Cache-Control: private, no-store`；不记录正文、实际路径或合并命令输出。

领域层只依赖小接口，例如 `Merge(ctx, MergeInput) (MergeResult,error)`；第三方或 Git CLI 类型留在 Adapter。仓库仅有间接 `github.com/pmezard/go-difflib v1.0.0`，它是双向 diff，不是三方引擎。开发前必须依 ADR 0019 比较成熟 Go 实现与受控 `git merge-file` Adapter；本机 `git merge-file` 支持 diff3/zdiff3/diff algorithm，但 Git 版本不是项目锁定事实，且 marker 输出本身不能直接提供无歧义结构化 conflicts。

#### 6.2 Append Revision

Application 先用 `(workspace,idempotency key, request hash)` 查 receipt；exact replay直接返回持久结果。首次执行：

1. 重新加载 source/current Proposal version，读取 current 正文+hash；若不等于 `expected_current_hash`，返回 409，保留客户端编辑内容由 UI 自行暂存于内存。
2. 重新计算 preview 和 conflict set；验证 preview hash/algorithm version、全部 conflict resolution 和 final content 限制。
3. 验证 Markdown 与安全输入；不接受客户端 target、mode、type 或 risk level。
4. 调用单一 Repository `AppendRevision` 事务：幂等 advisory lock -> receipt lookup -> 围栏相关 Authorization（固定锁序）-> Proposal `FOR UPDATE` -> exact source/current/version/status -> Workflow/Execution fence -> 插入 Revision/snapshot/receipt -> CAS 更新 Proposal current pointer/status/version -> append event -> commit。

文件系统与 PostgreSQL 无法构成原子事务。`expected_current_hash` 能证明“最后一次服务端受控读取”的版本；任意外部编辑若恰好发生在最后读取之后、数据库提交之前，新 Revision 可能立即 stale，但其 base snapshot 仍精确。后续 Approval/preflight/Safe Writeback 必须再次 CAS 并阻止覆盖，不能声称消除了该 TOCTOU。

### 7. needs_revision、Workflow、Authorization 与副作用围栏

这是本功能的高风险门禁。推荐分成“立即失效”和“允许 append”两个阶段。

#### 7.1 立即失效

- 发现 drift 时，现有 Proposal 状态更新 `approved|ready -> needs_revision` 是第一道原子 fence（当前入口见 `internal/changecontrol/adapter/postgres/repository.go:997`）。一旦提交，Authorization 消费和新 Atomic Begin 均不再满足 approved/applying。
- 必须给 MarkNeedsRevision 增加 expected Proposal version/current Revision binding，避免无条件推进错误版本；现有签名没有 expected version。
- 若 Atomic Begin 已先锁定并把 Proposal 推到 applying，则 MarkNeedsRevision 必须失败；不能在已有 durable execution 后把 Proposal 强行改成可编辑状态。
- Application 随后通过 Workflow 模块请求旧 Run 安全取消。不要由 Change Control Repository 直接篡改 Workflow 表；取消可能异步，append 在 Run 终态前返回 `PROPOSAL_REVISION_WORKFLOW_ACTIVE`。

#### 7.2 Append 前数据库复核

事务内必须：

1. 按既有 `Authorization -> Proposal -> Workflow` 顺序锁定 source Revision 的 Authorization、Proposal、Run/Node，避免与 Atomic Begin 反向死锁（既有锁序注释见 `migrations/00009_safe_writeback.sql:178`）。
2. 把 source Revision 所有 `issued` Authorization 原子转为 `revoked`，保留行和时间；再次查询确保无漏锁 issued 记录。Proposal 已是 needs_revision 后不应再允许新签发。
3. `revoked/expired` 可接受；`consumed` 只有在它被同一 source Revision 的唯一 Writeback Execution 引用时可接受。出现 consumed Authorization 但没有 exact execution，属于一致性/人工恢复冲突，禁止 append。
4. 若无 Writeback Execution：旧 Run 必须为 `failed` 或 `cancelled`，或根本没有 dispatch。`pending/running/waiting_for_human/retry_wait/paused` 均拒绝。
5. 若有 Execution：只接受 `status=needs_revision`、`manual_recovery_required=false`，且无 `proposal_commit`。当前领域把 needs_revision 认定为取消安全（`internal/changecontrol/domain/writeback.go:203`），但这只是必要非充分条件。
6. `prepared`、`file_prepared` 表示仍可能推进，必须先由旧 Workflow 归约到 needs_revision；`file_applied`、`git_prepared`、`git_committed`、`verifying`、`completed`、`publish_recovery_required`、`manual_recovery_required`、`verify_failed`、`rolled_back`、`compensating_file`、`compensated`、`apply_failed` 首期全部禁止 append。尤其 `IsWritebackCancellationSafe(manual_recovery)=true` 只说明 Workflow 可终止，不代表 Revision 可以被 supersede；PRD 明确禁止人工恢复状态下编辑。
7. 所有 Authorization/Writeback insert/consume trigger 加 `revision_id = proposal.current_revision_id` 检查；对既有 consumed/execution 的 exact replay设置明确例外，例外只能返回既有事实，不得签发 credential、创建 execution 或重做副作用。

这样既不删除旧 Approval/Authorization/Workflow/Execution，也防止它们在新 Revision 被批准后恢复能力。

### 8. HTTP/OpenAPI 建议

新增：

- `POST /api/v1/proposals/{proposal_id}/revision-merge-previews`：只读计算，request 包含 source Revision ID/hash 和 expected Proposal version；200 返回三方内容、raw hashes、algorithm/version、preview hash、candidate、结构化 conflicts。`private, no-store`。
- `POST /api/v1/proposals/{proposal_id}/revisions`：`Idempotency-Key` 必填；201 首次、200 exact replay。Body 包含 expected Proposal version、source ID/hash、expected current hash、preview/algorithm binding、完整 final content、Evidence/Risk/Rollback、conflict resolutions。
- `GET /api/v1/proposals/{proposal_id}/revisions?limit=&before_revision_no=`：有界历史摘要。
- `GET /api/v1/proposals/{proposal_id}/revisions/{revision_id}`：单个旧 Revision、其 Approval 和 Revision dispatch；正文响应 `private, no-store`。Base snapshot 是否存在应显式表达；不要对 legacy 缺失返回伪造 base。

现有 Proposal 响应必须暴露数值 `version`，因为 Domain 已有但 HTTP 遗漏（`internal/changecontrol/http/handler.go:306`）。OpenAPI Proposal union 和 Web 严格 decoder 必须同批更新；前端当前 exact 字段列表不接受新增字段（`web/src/api/business.ts:849`, `web/src/api/business.ts:852`）。这是线协议的协调发布点。

请求 body 必须使用显式 byte limit 后再走 strict JSON；通用 `decodeJSON` 当前没有为大正文定义本路由专属上限。冻结合同为 preview request 4 KiB、append wire body 8 MiB、preview 序列化 response 16 MiB；final content 1 MiB，Evidence/Risk/Rollback 各 64 KiB，conflict IDs 最多 1024。响应 conflict 只返回 ranges，不复制 excerpts。具体字段和错误以 `design.md` 第 8 节为唯一事实源。

冻结后的稳定错误：

| HTTP/code | 语义 |
| --- | --- |
| 400 `PROPOSAL_MERGE_CONTENT_INVALID` | UTF-8、Markdown、字段或算法 binding 非法 |
| 409 `PROPOSAL_BASE_SNAPSHOT_UNAVAILABLE` | legacy source 已漂移且没有可信 base 正文 |
| 409 `PROPOSAL_REVISION_NOT_EDITABLE` | type/mode/status 或副作用阶段不允许追加 |
| 409 `PROPOSAL_REVISION_STALE` | Proposal version、source Revision、current hash 或 preview binding 已变化 |
| 409 `PROPOSAL_REVISION_WORKFLOW_ACTIVE` | 旧 Run 尚未终态 |
| 409 `PROPOSAL_REVISION_SIDE_EFFECT_STARTED` | 旧执行已越过允许 supersede 的边界 |
| 409 `PROPOSAL_REVISION_AUTHORIZATION_CONFLICT` | consumed Authorization 无 exact execution 或不能安全撤销 |
| 409 `IDEMPOTENCY_KEY_REUSED` | 同键异请求 |
| 413 `PROPOSAL_REVISION_INPUT_TOO_LARGE` | append body、正文或元数据字段超限 |
| 422 `PROPOSAL_REVISION_CONFLICTS_UNRESOLVED` | 服务端重算后仍有未确认 conflict |
| 422 `PROPOSAL_MERGE_RESULT_TOO_LARGE` | candidate/stdout/response/conflict 集合超过固定上限 |
| 503 `PROPOSAL_MERGE_BUSY` | 并发闸门超时，可重试 |
| 503 `PROPOSAL_MERGE_TIMEOUT` | 固定 Git deadline 超时，可重试 |
| 503 `PROPOSAL_MERGE_TEMPORARY_STORAGE_UNAVAILABLE` | 私有临时存储创建、写入或清理失败 |
| 503 `PROPOSAL_MERGE_ENGINE_UNAVAILABLE` | 受控合并 Adapter 暂不可用 |
| 503 `PROPOSAL_MERGE_ENGINE_UNSUPPORTED` | Git 不满足固定 flags/golden contract |
| 503 `PROPOSAL_MERGE_ENGINE_OUTPUT_INVALID` | 退出码、marker 或输出不符合合同 |

409 details 只返回 expected/current version/hash、current Revision ID/no、conflict type 和 bounded resolution actions；不得返回正文、target path、Git 命令或绝对路径。现有 HashConflict details 模式可扩展（`internal/changecontrol/http/handler.go:997`, `internal/changecontrol/http/handler.go:1020`）。

新增 `proposal.revised` SSE event，resource version 使用新 Proposal version，source ref 绑定 new Revision ID；payload 只含 status/current revision identity。前端收到后只失效 Proposal detail/list/revision history并 REST 回查，不从事件拼正文。

### 9. 空文件与换行兼容

- base/current 空文件可支持：local reader 会对零字节计算 SHA-256，snapshot `content` 必须允许空。
- final 空文件当前不能安全写回：DB `content` 要求 `btrim(content)<>''`（`migrations/00004_change_control.sql:27`），file_patch domain 校验拒绝空白（`internal/changecontrol/domain/typed_proposal.go:697`），Application 创建拒绝（`internal/changecontrol/application/service.go:332`），OpenAPI `minLength:1`，Markdown Parser 返回 `SOURCE_CONTENT_EMPTY`（`internal/changecontrol/adapter/localfs/markdown_validator.go:62`），Safe Writeback 还要求 byte size > 0（`internal/changecontrol/domain/workspace_store.go:168`, `internal/changecontrol/domain/workspace_store.go:297`）。
- 因此 AC7 必须明确：若“空文件”仅指 empty base/current，首期保持 final empty 的稳定拒绝；若要求把文件截断为零字节，则需单独修改上述全链路不变量和回归测试，不能只放宽 Revision 表。
- current/base hash 基于 raw bytes；不得在哈希前把 CRLF 改为 LF。现有 Change Hash 对 proposed/final 做 CRLF -> LF 规范化，所以 preview/receipt 必须另绑 raw content hash，避免换行差异在幂等层碰撞。

### 10. 测试矩阵

#### Domain/merge adapter

- source status 全矩阵；仅 ready 和满足 fence 的 needs_revision 成功。
- target/type/mode/risk level 不可变，revision no 严格 +1，source 必须 current。
- non-overlap、same edit、overlap、相邻 edit、delete/edit、同点插入、EOF/no-final-newline、empty base/current、CRLF/LF、正文原含 marker-like 行。
- 算法/version 和 preview hash 确定；相同输入重复结果相同；raw LF/CRLF 的 receipt hash 不同。
- 全 conflict ID 必须显式解决；缺失、重复、未知 ID、超限 custom resolution 拒绝。
- 1 MiB 边界、非法 UTF-8/NUL、取消/超时、引擎不可用分类。

#### PostgreSQL/integration

- `00082` 从空库执行、从 legacy 数据升级、重复 migrate；回填遇到孤儿 Revision/Workflow binding 时 fail closed。
- current_revision deferred FK、snapshot hash/type/mode trigger、dispatch binding、receipt/update/delete/truncate immutability。
- legacy snapshot 缺失：current==base 可 append 并补快照；current!=base 稳定冲突。
- 同 key 同 request并发只一条 receipt/revision；同 key异 request 409；两个 key同 expected version只有一个新 Revision。
- 事务任一点失败回滚 Revision/snapshot/pointer/receipt/event；响应丢失后 exact replay同一 ID/no/version。
- old Approval/dispatch 保留可查；current Get/List 只显示新 Revision 的 nullable Approval/Workflow。
- Workflow backfill从 node input精确绑定；旧物理 pointer 与 dispatch 不一致时迁移失败。

#### Authorization/Workflow/Writeback

- needs_revision 无 Approval；有 terminal cancelled/failed Run；Run pending/running/waiting/retry/paused。
- issued Authorization 被原子 revoked；revoked/expired 幂等；consumed+exact needs_revision execution 可接受；consumed orphan拒绝。
- MarkNeedsRevision 与 Atomic Begin 并发：最多一个状态路径成功，无死锁；Begin 先赢则 append 不可用，Mark 先赢则 Begin 不消费/不创建 execution。
- append 与 authorization issue/consume并发；新审批后旧 issued credential 不复活。
- exact consumed replay和 exact existing execution replay仍返回旧事实，但不能创建新执行。
- execution 每个状态的 supersede 矩阵；manual recovery和任何 Proposal Commit 永远拒绝。
- 新 Approval/Authorization/Bootstrap/Preflight/Writeback/Git Commit 全部绑定 new Revision；旧 Run input不能驱动新 Revision，旧 Approval不能批准新 Revision。

#### HTTP/OpenAPI/安全

- Content-Type、未知/重复字段、多 JSON、超限 body、非法 UUID/hash/version、缺失 Idempotency-Key。
- 200 replay、201 create、400/404/409/413/503；Problem details 有 expected/current 摘要且无正文/路径/内部命令。
- `Cache-Control: private, no-store`；请求正文、三方内容、凭据不进入日志、Audit/SSE/URL/telemetry。
- OpenAPI/runtime route inventory同步；所有 Proposal union 增加 version 后 Go response、Web strict decoder同步。
- 历史 list 有界且稳定排序，不返回无界正文；跨 Workspace/不可见与 not found 同语义。
- 现有 file_patch/CREATE_ONLY/restore_document/structured Proposal/Approval/current-content/preflight/Safe Writeback 全回归。

### 11. Related specs

- `.trellis/spec/backend/database-guidelines.md:33`：数据库约束必须承担版本、幂等和一致性事实。
- `.trellis/spec/backend/database-guidelines.md:53`：可变 Aggregate 使用 version 乐观锁。
- `.trellis/spec/backend/database-guidelines.md:60`：禁止覆盖 Revision 修复冲突。
- `.trellis/spec/backend/error-handling.md:15`：版本冲突必须返回 expected/current/conflict/actions。
- `.trellis/spec/backend/error-handling.md:55`：details 不得包含正文、路径或 Secret。
- `.trellis/spec/backend/http-boundary.md:18`：OpenAPI 与 runtime route inventory 必须精确一致。
- `docs/requirements.md:84`、`docs/requirements.md:86`：修改 Proposal 创建新 Revision，旧 Revision 保留。
- `docs/requirements.md:91`、`docs/requirements.md:93`：Approval 固定绑定 Revision，stale/double submission 幂等或拒绝。
- `docs/requirements.md:225`、`docs/requirements.md:279`：漂移时显示三方并禁止覆盖，AC-13 进入三方合并。
- `docs/architecture/domain-and-data.md:146`：Approval 绑定 Revision/Change Hash/Target Version/Git HEAD。
- `docs/architecture/application-contracts.md:9`：Command 使用幂等和 expected version/CAS。
- `docs/architecture/adr/0019-mature-framework-first.md:21`：合并引擎须先按既有实现/标准库/成熟框架顺序调研。

### 12. External references / versions

- 本地可执行文件报告 `git version 2.54.0 (Apple Git-157)`；`git merge-file -h` 提供 `--diff3`、`--zdiff3` 和 `--diff-algorithm`。这仅是研究环境事实，不是仓库部署版本或已批准技术选型。
- `go.mod` 当前只有间接 `github.com/pmezard/go-difflib v1.0.0`；vendored 包自述为 Python difflib 的 partial port，提供双向 diff，不构成三方 merge 方案。

## Caveats / Not Found

- 仓库没有已批准的三方合并引擎，也没有可直接复用的结构化 conflict 模型。实现前仍需完成成熟方案、许可证、维护状态、确定性、marker collision、资源限制和跨平台部署评估；本研究只冻结后端端口和数据绑定。
- PostgreSQL 与用户直接编辑的 Workspace 文件不能原子提交；最后一次服务端读取之后仍存在不可消除的外部 TOCTOU。安全保证来自精确 base snapshot 加 Approval/preflight/writeback 的重复 CAS，而不是声称 append 事务锁住了文件。
- Workflow 取消是独立模块的异步动作。首期应 fail closed并等待 terminal，不应在 Revision Repository 中直接更新 Workflow 状态。
- `proposal.workflow_run_id` 的迁移需要协调发布/feature gate；保留 legacy 列不代表旧 Worker 可以安全处理 Revision 2。
- AC7 对“final content 为空”是否必须成功未明确。当前全链路禁止空最终正文，需产品在实现前明确是“测试并稳定拒绝”还是“支持 truncate-to-empty”。
- 本研究未修改任何产品代码、迁移、OpenAPI 或 spec，也未运行测试。
