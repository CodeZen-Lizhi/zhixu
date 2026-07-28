# Artifact 产物闭环契约

## Scenario: M8-01 Workspace Artifact Lifecycle

### 1. Scope / Trigger

- 修改 `internal/artifact`、Artifact HTTP/OpenAPI、API/Worker composition、Workflow terminal hook、
  `PUBLISH_ARTIFACT` 或迁移 `00039`-`00043` 时应用。
- Artifact 是与正式 Knowledge/Document 隔离的产物事实源；Proposal 批准后的正式写回仍由 Change Control 拥有。

### 2. Signatures

```text
GET|POST /api/v1/artifacts
GET      /api/v1/artifacts/{artifact_id}
POST     /api/v1/artifacts/{artifact_id}/outline
POST     /api/v1/artifacts/{artifact_id}/outline/approve
POST     /api/v1/artifacts/{artifact_id}/revisions
POST     /api/v1/artifacts/{artifact_id}/sections
POST     /api/v1/artifacts/{artifact_id}/sections/generate
GET      /api/v1/artifacts/{artifact_id}/section-generations
POST     /api/v1/artifacts/{artifact_id}/draft/approve
POST     /api/v1/artifacts/{artifact_id}/exports/markdown
GET      /api/v1/artifacts/{artifact_id}/exports/{export_id}
POST     /api/v1/artifacts/{artifact_id}/publish-proposals
```

```go
FindCommand(context.Context, CommandBinding) (CommandResult, bool, error)
ProbeExternalTransition(context.Context, CommandBinding) (State, error)
ReserveExternalTransition(context.Context, CommandBinding) (State, error)
Create(context.Context, CreateRecord) (CommandResult, error)
Transition(context.Context, TransitionRecord) (CommandResult, error)
Get(context.Context, foundation.ID, foundation.ID) (State, error)
List(context.Context, ListQuery) (ArtifactPage, error)
```

- 持久事实表为 `learning.artifact`、`artifact_revision`、`artifact_command`、`artifact_export`、
  `artifact_publication`、`artifact_section_generation` 和 `artifact_external_transition_reservation`。

### 3. Contracts

- Artifact、current Revision、状态和版本只能由 Artifact domain 状态机推进；Revision append-only，读取时重新验证
  Workspace、current pointer、revision number 和 canonical content hash。
- 所有命令要求 `Idempotency-Key`；版本化命令要求正整数 `expected_version`。receipt 完整绑定 Workspace、Artifact、
  command type、request hash 和 expected version，相同 binding 返回原响应并标记 replay，不同 binding 返回冲突。`PLAN`
  例外地把 Artifact ID 视为服务端生成输出：请求 hash 固定后并发 loser 产生的随机候选 ID 不参与 replay identity；receipt 返回
  winner 的 Artifact ID。所有后续 transition 仍必须精确绑定客户端目标 Artifact ID。
- Repository 在读取 receipt 前，以 `workspace_id + ':' + idempotency_key` 取得 transaction-scoped advisory lock；
  乐观锁不能代替同 key 并发序列化。
- Citation 输入只包含 Index/Chunk/Source Version/Source Span identity。Retrieval 返回的 full tuple 必须与请求一一对应，
  不得重复、缺失或替换任一 identity；服务端随后批量验证 Workspace、不可变内容、span、hash/excerpt 与正式知识资格后
  重建 Citation。客户端或模型不能提供可信 `verified`、hash 或 excerpt。
- GAP 必须正文为空且 Citation 为空；PARTIAL 必须保留显式 Gap。全部大纲章节存在后才能进入 Draft。
- `StartRevision` 只允许 DRAFT/APPROVED/EXPORTED 回到 GENERATING；当前 Revision 保持不可变，随后人工 GAP 或受控生成可替换已有章节并创建下一 Revision。
- Section generation 复用受控 Agent/Workflow。Workflow 成功只有在同事务校验 Model Run 与 output receipt 后才可写入
  Revision；缺失或不一致进入 `RECOVERY_REQUIRED`，不能伪造成功。
- Export/Publish 必须先执行 reservation-aware 只读 probe：已有不同 owner 优先返回 409；无 reservation 或完整 owner 才返回
  current state。Application 对该 state 执行完整、无副作用的 Domain preflight（包括状态和时间），通过后才能 durable reserve，
  随后对 Reserve 返回的 state 再做同一完整领域校验。非法状态或非法时钟不得创建 reservation；完整 owner 可恢复，其他 key、
  command 或普通 transition 必须在外部调用前冲突。
- Artifact 新版本、对应 export/publication side fact、receipt 和 reservation 删除在同一事务完成。reservation 不可 UPDATE，
  terminal binding 未闭合前不可 DELETE。
- Markdown 文件写入 Workspace managed export 目录，使用原子 rename、`0600` 和 SHA-256/size 记录。Publish 只创建
  冻结 Artifact/Revision/version/hash/coverage 的 typed Proposal，不创建 Document、Git commit 或 Index 事实。
- GET 使用 `READ_LOCAL`，Artifact mutation 使用 `WRITE_PROPOSAL`；正式 Proposal Approval 继续要求 `WRITE_KNOWLEDGE`。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 未知/重复 JSON、非法 UUID/enum、缺 Idempotency-Key、非法 expected version | 400，依赖和外部副作用调用次数为 0 |
| Workspace/Artifact/Revision/Export 绑定不匹配 | 404 防枚举，不返回跨 Workspace 数据 |
| stale version、不同 receipt binding、pending reservation owner 不同 | 409，状态与 side fact 不变 |
| 同 key/hash 的并发 PLAN 生成不同候选 Artifact ID | 一个 original、其余 exact replay winner；只存在一个 Artifact/receipt |
| Export/Publish 状态非法或命令时间早于 current state | 请求失败，不创建 reservation，不调用外部依赖 |
| Citation 伪造、跨 Workspace、span/hash/excerpt 漂移或非正式知识 | 请求失败，不写 Revision/receipt |
| GAP 带正文或 Citation，非 Export/Publish 携带 side fact | 请求失败，Artifact、receipt、side fact、reservation 全部不变 |
| generation dependency disabled | 503 capability unavailable；人工 GAP/section 路径仍按领域规则工作 |
| Export/Publish 外部成功但响应丢失 | 相同完整 binding 恢复并只产生一个外部事实 |
| 00039-00043 存在受保护业务数据时 Down | SQLSTATE `55000`，迁移版本和数据保持不变 |

### 5. Good / Base / Bad Cases

- Good：两章 Artifact 的 COVERED 章节只保存服务端重建 Citation，GAP 章节为空正文；Draft 批准后导出可回读，
  Publish 只产生一个冻结 Proposal。
- Base：模型 capability 未配置时 generation 明确 unavailable，用户仍可记录合法 GAP；空列表返回 `items: []`。
- Bad：在文件写入或 Proposal 创建后才用 CAS 竞争；信任客户端 `verified`；把 `ops.export_job` 或正式 Document 当 Artifact 状态源。

### 6. Tests Required

- Domain/Application：完整状态序列、非法转换、Revision/hash、Coverage/GAP、receipt replay 和错误映射。
- PostgreSQL：Workspace 隔离、CAS、同 key replay、同 key/hash PLAN 不同候选 ID、不同 key race、六类非法 side-fact 组合、reservation owner/recovery/
  terminal close，使用 `-race -tags=integration -count=3 -p 1`。
- Migration：00039-00043 空库 Up、重复 Up、Down-Up、CHECK/FK/trigger、guarded Down；只运行 Artifact 专属测试，
  不以其他里程碑的迁移测试替代。
- HTTP/OpenAPI/Auth/Composition：strict JSON/body/query、Problem Details、Capability、API/Worker wiring 与契约 drift。
- E2E：真实 PostgreSQL、API、River Worker、受控 fake model 与 Vite 完成 outline -> COVERED/GAP -> Draft -> export ->
  Publish Proposal；桌面/移动端断言无控制台错误和横向溢出。
- Canonical：全仓 Go race/vet/tidy、OpenAPI check、前端 lint/typecheck/test/build 和 `git diff --check`。

### 7. Wrong vs Correct

```text
Wrong: 先调用文件系统/Change Control，或未做完整 Domain preflight 就持久化 reservation，随后才判断 transition 是否合法。
Correct: reservation-aware probe -> 完整无副作用 Domain preflight -> durable reserve -> 二次完整领域校验；只有 owner 调用外部依赖，
再原子闭合新版本、side fact、receipt 和 reservation。

Wrong: Workflow 显示 succeeded 就直接接受模型正文和 Citation。
Correct: terminal hook 在 PostgreSQL 事务内校验 Model Run 与 output receipt，Application 再执行服务器证据复核和领域状态机。

Wrong: 把 PLAN 调用内随机生成的候选 Artifact ID 当作客户端请求身份，导致同 key/hash 并发 loser 与 winner 冲突。
Correct: PLAN receipt 以 Workspace/key/hash/command/version 识别请求并返回 winner ID；只有后续 transition 把 Artifact ID 纳入 identity。
```
