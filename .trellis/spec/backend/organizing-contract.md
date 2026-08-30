# 材料确认与整理模板契约

## Scenario: Suggested Materials, Frozen Snapshot And Governed Results

### 1. Scope / Trigger

- 修改 `internal/organizing`、迁移 `00074`、Organizing HTTP/OpenAPI、四个固定 Workflow、
  Workflow Human Task review projection 或 Organizing 的 Artifact/Proposal bridge 时应用。
- Draft 是可恢复的候选材料集合；Snapshot、Template Revision、Artifact Revision 和 Run binding 是不可变事实。
  Organizing 不能绕过 Evidence、Human Task、Approval 或 Safe Writeback。

### 2. Signatures

```text
POST     /api/v1/workspaces/{workspace_id}/organizing/drafts
GET|PUT  /api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}
POST     /api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/suggestions
POST     /api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials
PATCH|DELETE /api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/materials/{material_id}
POST     /api/v1/workspaces/{workspace_id}/organizing/drafts/{draft_id}/confirm
GET      /api/v1/workspaces/{workspace_id}/organizing/materials/search?q={query}&kind={kind}&limit={limit}
GET      /api/v1/workspaces/{workspace_id}/organizing/snapshots/{snapshot_id}
GET|POST /api/v1/workspaces/{workspace_id}/organizing/templates
GET      /api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}
POST     /api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/clone
POST     /api/v1/workspaces/{workspace_id}/organizing/templates/{template_id}/revisions
GET      /api/v1/workspaces/{workspace_id}/organizing/runs/{snapshot_id}
```

```go
type FrozenMaterialFence interface {
    VerifyFrozen(context.Context, any, foundation.ID, []domain.MaterialRef) error
}

type MaterialSearchProvider interface {
    SearchMaterials(context.Context, MaterialSearchQuery) ([]MaterialSearchHit, error)
}

type FrozenDocumentContentReader interface {
    OpenFrozenDocumentContents(context.Context, foundation.ID, []domain.MaterialRef) ([]FrozenDocumentContent, error)
}

type HumanTaskReviewProjector interface {
    ProjectHumanTaskReview(context.Context, workflowdomain.Run, workflowdomain.HumanTask) (json.RawMessage, bool, error)
}
```

- `MaterialRef` 是 `SOURCE_VERSION | DOCUMENT_REVISION | CLAIM | SMART_COLLECTION` 判别联合。
- 持久化事实包括 `organizing.draft(_version|_material)`、`workflow_input_snapshot(_material)`、
  `template(_revision)`、`command_receipt`、`workflow_start_outbox` 和 `run_binding`。

### 3. Contracts

- Suggestion 只产生带 reason、owner identity、版本、可用性和 Evidence 的候选；候选默认选择不等于授权。
  只有显式 confirm 才能创建 Snapshot 和 Workflow start outbox。
- 同页补充搜索必须指定单一 Material kind、绑定 Workspace 且有界（默认 12，最大 25）；Source、Document、Claim 和
  Smart Collection 都只返回严格身份选择器、title 与 availability，不返回或冒充已冻结 version/hash/Evidence。
  `AddMaterial` 必须用该身份重新调用 owner resolver，重新确认 Workspace、可见性与当前精确版本后才能写入 Draft。
- Draft mutation 使用 `expected_version` CAS；服务端 Draft 是刷新恢复事实源。相同 Workspace/key/request exact replay
  返回原结果；同 key 不同 request 返回冲突。并发 winner 在 Repository 内出现时也必须返回合法 `replayed=true`，
  不能被 Application 误判为一致性错误。
- confirm 先锁定并校验当前 Draft CAS/状态和当前 Template Revision，再在同一 `SERIALIZABLE` 事务调用
  `FrozenMaterialFence`。Fence 批量锁定并重验 Source/active Index/Profile、Document/Revision、formal Claim/provenance、
  Smart Collection version/query/read-model revision 和完整 Evidence tuple/hash。
- Snapshot、Snapshot Materials、Draft terminal/version、command receipt 和 Workflow start outbox 必须与 Fence 在同一事务提交；
  任一 owner 漂移或写入失败全部回滚。exact replay 在 Fence 前返回，不能重新读取当前 owner facts。
- Snapshot 只持久化 Document/Article Revision/version/content hash，不复制正文。生成节点按 Snapshot 顺序批量调用
  Authoring owner 打开精确 Revision，逐项复核 Workspace、Document、Revision、version 和 content hash；正文只在有界模型请求中
  以服务端 `Dnnn` 标签短暂出现，不进入 Snapshot、Workflow output、Model Run/Call、日志或错误。
- Document 正文与 formal Evidence 是两类不同来源：`Dnnn` 形成 Artifact `DocumentSource` 和 Human Task Document support，
  `Ennn` 才形成 Source Span Citation。两类标签都必须来自服务端本次打开结果，未知、重复、越界或漂移 fail closed。
- `FrozenMaterialFence` 的 `any` 只允许 Repository 传入当前 `pgx.Tx`，用于保持 Application 不依赖 pgx；
  nil、typed-nil、非事务值或跨事务 owner read 必须 fail closed。
- 四个内置模板固定为 `TOPIC_ARTICLE | MERGE_DOCUMENTS | KNOWLEDGE_REPORT | INTERVIEW_REVIEW`。
  专题文章必须先审阅 Evidence/GAP 大纲；合并必须审阅重复、互补、冲突、独特分类和有界 Diff；报告与面试文档默认停留 Artifact。
- 自定义模板只能 clone/create append-only Revision，并使用受约束声明；未知字段、raw prompt、tool、permission、node、retry、script、
  关闭 Evidence/GAP/Approval/Safe Writeback 的声明全部拒绝。
- Workflow detail 的 `human_task.review` 是必需 nullable。Organizing review 必须精确绑定 Workspace、Definition、Run、Task、Node、
  Snapshot、receipt 和 Evidence；GET detail 与 POST decision 都执行同一 projector。required review 缺失、漂移或不可用时禁止提交。
- 结果发布只创建现有 Artifact Publish Proposal 或 Merge Proposal；文件/Git 写回仍由 Approval 和 Safe Writeback 唯一拥有。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 非法 UUID/hash/union/template declaration、缺 key 或越界数组 | 400；无 Draft/Snapshot/Run 副作用 |
| 搜索 query 非规范、UTF-8 超过 256 bytes、kind/limit 非法 | 400；不执行 owner 搜索 |
| Workspace/资源绑定不一致 | 404 防枚举 |
| Draft CAS、Template/current Revision 或同 key request 漂移 | 409；保留权威 Draft |
| Source/Profile/Document/Claim/Collection/Evidence 在 confirm 前漂移 | `MATERIAL_STALE`；Snapshot、Draft terminal、receipt、outbox 全部不存在 |
| `40001` 且并发 winner 已提交 exact receipt | 新事务回查并返回 replay；不同 request 返回幂等冲突 |
| owner/fence/Workflow/Artifact 依赖不可用 | 503/retryable 按分类返回；不得部分成功 |
| Article Revision batch 缺失、乱序、跨 Workspace 或 content hash 漂移 | consistency failure；不调用模型、不产生 Artifact |
| required Human Task review 缺失、Definition/receipt/Evidence 漂移 | 409 consistency violation；decision service 不被调用 |
| `00074` 旧库存在受保护业务事实 | 前向升级保留历史事实 |

### 5. Good / Base / Bad Cases

- Good：用户增删候选后 confirm；同事务重验全部 owner facts并冻结 Snapshot，Worker exact-once 启动固定 Workflow，
  Human Task 展示可打开 Evidence，最终结果进入 Artifact/Proposal。
- Base：模型或向量能力不可用时仍可用 Keyword/手动材料；没有足够 Evidence 的章节输出 GAP，报告和面试结果保持 Artifact。
- Bad：confirm 前启动 Workflow、在事务外 Freeze 后再写 Snapshot、replay 重新验证当前资料、逐 Collection 查询 3N+1、
  POST decision 绕过 review projector，或让模板声明注入任意工具/权限。

### 6. Tests Required

- Domain/Application：四类 Material union、Draft CAS、建议不授权、模板 allowlist/canonical hash、四模板输出、
  exact replay/conflict、并发 repository replay disposition 和 typed-nil fence。
- PostgreSQL：fresh/repeat/旧版本数据前向升级、append-only、Workspace FK、confirm 半失败回滚、owner drift、active Index drift、
  serializable concurrency/response loss、Collection revision 交叉验证、outbox claim/retry/poison 和 Run binding。
- Workflow/HTTP/Auth/OpenAPI：四 Definition/Worker restart、全局 Run Workspace header、Human Task GET/POST fail closed、
  Artifact/Proposal owner、Capability、严格 Problem 和 production composition。
- Authoring/Generation：Document Revision Workspace batch、稳定请求顺序、hash/版本漂移、正文总量/单项截断、`Dnnn` allowlist、
  Model/Workflow 持久载荷无正文，以及 DocumentSource/Human review round-trip。
- Web：strict decoder、Workspace Query key、Draft refresh、selection/confirm、Template Revision、Evidence/GAP、Merge Diff、
  identity-only 同页搜索、response-loss 和 `review:null` 禁止盲批。
- Canonical gate：受影响 Go race/vet、真实 PostgreSQL、OpenAPI、Web lint/typecheck/test/build、`go mod tidy -diff` 和 `git diff --check`。

### 7. Wrong vs Correct

```text
Wrong: Application 先 Freeze owner facts，随后 Repository 另开事务写 Snapshot。
Correct: Repository 建立 SERIALIZABLE 事务并把同一 pgx.Tx 交给 FrozenMaterialFence；全部事实一次提交。

Wrong: GET 页面展示 review，但 POST decision 直接信任 task_id 和 approved。
Correct: GET 与 POST 都重新读取 pending task 并运行同一 projector；required review 不可验证时 fail closed。

Wrong: 自定义模板接受 raw prompt/tool/node，或普通 exact replay 再读当前 owner facts。
Correct: 模板只编译受约束声明；exact receipt 在 Fence 前恢复原 Snapshot/Run identity。

Wrong: Snapshot 保存 Document 正文，或生成时按当前 Document 读取最新 Revision。
Correct: Snapshot 只保存不可变身份/hash；生成时批量打开 exact Article Revision，复核后仅在有界模型请求中使用正文。
```
