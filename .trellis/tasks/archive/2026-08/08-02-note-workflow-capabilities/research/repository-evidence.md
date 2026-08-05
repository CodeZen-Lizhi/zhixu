# Repository Evidence

## Capture And Ingestion

- `internal/workspace/application/service.go:190`：Workspace Scan 原子注册 Source、Content Artifact、Source Version，但明确不解析、不索引。Quick Capture 应复用 owner 写入能力，并单独编排后续处理。
- `internal/workspace/domain/repository.go`：Workspace Repository 已是 Source/Version 写入 owner；新 Capture 模块不应直接复制 SQL。
- `internal/ingestion/http/handler.go:28`：Ingestion 已有 Source Version 幂等 command endpoint。
- `internal/ingestion/application/service.go`：真实解析、Span、Chunk 与 Attempt 状态已经存在；Quick Capture 不应实现第二套 parser/chunker。
- `web/src/features/business/BasicPages.tsx:16`：Inbox 当前以 Source Version 为列表 owner，URL 抓取失败而未产生 Version 时无法展示，需要扩展 Source/Capture read model。

## Retrieval And Profile

- `internal/retrieval/domain/search.go:45`：Search 已支持 Source Version IDs 等过滤。
- `internal/retrieval/http/handler.go` 与 `web/src/api/search.ts`：Search 已有严格请求/响应、Evidence href 和 Workspace 绑定，可作为材料建议召回基础。
- `docs/architecture/retrieval-architecture.md`：Embedding 不可用时 Keyword 继续，Profile/材料发现必须沿用显式降级语义。
- `docs/architecture/database-design.md:163`：Document/Article Revision 已有 Source Version、parent、content hash、Git Commit 等版本字段。

## Artifact And Workflow

- `internal/artifact/domain/model.go:27`：Artifact 已有 Planning、Outline Review、Generating、Draft、Publish Proposal/Published 状态。
- `internal/artifact/domain/model.go:73`：Outline、Citation、Coverage/GAP 和不可变 Revision 已有稳定领域契约。
- `internal/artifact/workflow/executor.go:93`：Artifact Generation 已从冻结上下文加载 Evidence、Model Runtime 和严格输出。
- `internal/workflow/domain/model.go:94`、`internal/workflow/application/runtime_human.go:67`：Workflow 已有持久 Human Task，可承载大纲确认。
- `docs/architecture/database-design.md:642`：Artifact Revision 已要求大纲/正文变化追加 Revision、冻结来源覆盖和生成版本。

## Change Control And History

- `docs/architecture/interfaces-and-adapters.md:161`：Approval 捕获 strict-clean attached HEAD，Safe Writeback 不接受客户端 expected HEAD。
- `docs/architecture/interfaces-and-adapters.md:173`：Change Control GitRepository 明确禁止 push/fetch/remote、reset/checkout/history rewrite。
- `docs/architecture/database-design.md:449`：Writeback Execution 持久化文件/Git checkpoint 与结果绑定。
- `docs/architecture/database-design.md:458`：Proposal Commit 不可变关联 Proposal Revision、Approval、Writeback 和 Git Commit，可用于 History enrichment。
- `docs/product/PRD.md:1507`：用户回滚从已批准 Commit 创建新 Proposal 和反向 Commit，不重写历史。
- `docs/architecture/runbooks/consistency-recovery.md:47`：外部未 Commit 编辑作为新基线；外部编辑需要创建 Source/Revision。

## Git Runtime And Secret

- `internal/platform/gitcli/runner.go:32`：Git Runner 固定禁交互、Hook、replace、pager/editor，环境清理且输出有界。
- `internal/platform/gitcli/status.go`：现有 Git client 只提供 status/init，不具备 remote 状态。
- `internal/modelsettings/crypto/sealer.go`：AES-GCM 已有实现，但业务 AAD 属于 Model Settings；Git Token 需要独立 purpose/schema/table。
- `docs/architecture/security.md:115`：Model API Key 只允许瞬时明文、短生命周期 buffer 和带 AAD 密文；Git Token 应遵循同等 Secret 生命周期。

## Frontend And State

- `web/src/app/AppShell.tsx:190`：AppShell 是全局导航/上下文 owner，适合挂载唯一 Quick Capture Dialog 和快捷键。
- `.trellis/spec/frontend/state-management.md`：REST 是事实源，SSE 只做 invalidation；Draft/Run/Sync 状态不能由本地乐观状态替代。
- `.trellis/spec/frontend/type-safety.md`：所有新 API/SSE/URL/multipart metadata 从 `unknown` 严格校验，Feature 不直接消费 wire DTO。

## Consequences

- Quick Capture 是编排新能力，不替换 Workspace/Ingestion。
- Document Knowledge Profile 必须是独立 derived projection，不写正式 Knowledge。
- Organizing 通过固定 Workflow + Artifact/Change Control bridge 落地。
- Document File History 是 Git/Revision/Proposal 的查询组合，不是 Knowledge Timeline。
- Git Remote Sync 必须使用独立最小权限端口和 Secret Store，不能扩宽 Safe Writeback GitRepository。
