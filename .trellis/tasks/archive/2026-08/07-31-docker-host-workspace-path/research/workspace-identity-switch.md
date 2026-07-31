# Research: Workspace identity and root switching

- Query: 更换 Workspace Root 时，应创建/切换新的 Workspace 身份，还是把现有 Workspace 身份重绑到新目录；核对领域、应用、数据库、运行时调用方、历史数据与前端 active workspace 语义，并比较一致性、隔离、迁移、回滚和 UI 影响。
- Scope: internal
- Date: 2026-07-31

## Findings

### 结论

默认契约应为：**不同的规范 Workspace Root 对应不同 Workspace 身份**。

1. 用户选择一个从未配置过的新目录时，创建新的 `Workspace.ID`。
2. 用户选择一个已配置过的非活动目录时，恢复并切换到该目录原有的 `Workspace.ID`，不创建重复身份。
3. 普通创建、打开和切换不得更新当前 Workspace 的 `root_path`。
4. 同一知识空间因目录改名、移动，或历史 `/workspace` 容器别名需要改成真实宿主机路径时，属于独立的 **Workspace Root Migration**。只有能证明 Git、受管内容和历史归属连续时才允许保留 ID；证明不足必须失败关闭。
5. `Workspace Root Grant` 是“当前运行时可访问哪个目录”的能力状态，不是 Workspace 身份本身。任一时刻只允许一个 Workspace 获得 Grant；非活动 Workspace 仍保留其 ID、Root 绑定和全部历史事实。

这与当前已落入任务/领域文档的决策一致：任务要求不同 Root 使用不同身份，并把目录移动定义为独立迁移（`.trellis/tasks/07-31-docker-host-workspace-path/prd.md:27`、`:28`）；领域词汇也明确区分 Workspace Switch 与 Workspace Root Migration（`docs/architecture/CONTEXT.md:19`、`:23`）；ADR-0018 已接受该方向（`docs/architecture/adr/0018-workspace-root-identity.md:5`）。

### 为什么 Workspace ID 不是可随意重绑的路径别名

#### 1. 领域语义把 Workspace 定义为知识空间，而不只是当前 mount 地址

- 产品定义是“用户控制的一个知识空间、文件根目录和配置集合”（`docs/product/PRD.md:414`、`:418`）。
- Workspace 聚合维护 Root、Git Repository、活动索引和配置引用（`docs/architecture/domain-model.md:44`、`:50`）。
- 代码将 Workspace 描述为“用户控制文件根的稳定数据库 mapping”，而 Source、ContentArtifact 均持有 `WorkspaceID`（`internal/workspace/domain/model.go:26`、`:38`、`:48`）。

因此，Root 是 Workspace 聚合身份含义的一部分。把同一 ID 从目录 A 改到无关目录 B，不是更新一个部署地址，而是让 B 继承 A 的知识、证据、Git 和历史状态。

#### 2. 数据库把 Workspace ID 作为全系统隔离根

- `core.workspace.id` 是主键，`root_path` 是唯一绑定；`core.source.workspace_id` 直接外键到 Workspace，删除策略为 `RESTRICT`（`migrations/00002_workspace_sources.sql:2`、`:5`、`:22`）。
- 数据库以部分唯一索引保证全局只有一个 `status='active'` 的 Workspace（`migrations/00002_workspace_sources.sql:18`）。
- Workflow Definition/Run 以 Workspace 隔离（`migrations/00003_workflow.sql:4`、`:14`）。
- Topic、Claim、Relation 及其复合外键/唯一键均包含 Workspace（`migrations/00017_knowledge_domain.sql:3`、`:46`、`:101`）。
- Conversation 和幂等键按 Workspace 隔离（`migrations/00020_rag_conversation_sse.sql:32`、`:56`）。
- Smart Collection、Health、Artifact、Review、Memory、Timeline、Export 等继续沿用相同所有权；仓库中有 36 个迁移文件声明 `workspace_id`，说明重绑影响不是 Workspace 模块内的局部更新。
- 新 Workspace 插入还会初始化 Workspace 级 Artifact citation backfill 状态（`migrations/00062_m7_timeline_impact_v2.sql:526`、`:549`），进一步证明新身份拥有独立状态集。

采用新 ID 不需要迁移任何子表：旧数据仍由旧 ID 拥有，新数据从新 ID 开始。若重绑原 ID，则所有上述事实都会被静默解释为属于新目录。

#### 3. 同 ID 扫描新目录会直接合并两个目录的 Source 身份

- 扫描从持久 Workspace 读取 `workspace.RootPath`，并用该 Workspace ID 注册 Source 和 Artifact（`internal/workspace/application/service.go:174`、`:178`、`:205`）。
- Repository 以 `(workspace_id, original_location)` 复用 Source（`internal/workspace/adapter/postgres/repository.go:350`、`:354`）。
- Repository 以 `(workspace_id, content_hash)` 复用 ContentArtifact（`internal/workspace/adapter/postgres/repository.go:320`、`:324`）。
- Source Version 又以 `(source_id, content_hash)` 复用（`internal/workspace/adapter/postgres/repository.go:373`、`:379`）。

所以把 ID 从 A 重绑到 B 后，B 中同名相对路径会继承 A 的 Source；相同内容哈希会继承 A 的受管 Artifact；不同内容则被记录为 A 的 Source 新版本。这不是隔离失败后的偶然现象，而是当前幂等键定义下的必然结果。

#### 4. 历史不可变内容仍通过“当前 Workspace Root”读取

- Source Version 是不可变历史（`migrations/00002_workspace_sources.sql:54`）；ContentArtifact 同样禁止更新/删除（`migrations/00006_content_artifact.sql:79`）。
- SourceMaterial 查询把历史 SourceVersion/Artifact 与 `core.workspace` 联接，并返回 Workspace **当前** `w.root_path`（`internal/workspace/adapter/postgres/repository.go:172`、`:186`、`:262`）。
- Ingestion 和 Retrieval 随后用这个当前 Root 加上历史 `managed_location` 重读字节（`internal/ingestion/adapter/workspace/reader.go:46`、`:61`；`internal/retrieval/adapter/workspace/reader.go:54`、`:69`）。
- 受管位置固定为 `.knowledge/sources/<content_hash>`，并按 Workspace + hash 唯一（`migrations/00006_content_artifact.sql:4`、`:7`、`:9`）。

重绑到 B 后，A 的历史 Artifact 会在 B 的 `.knowledge/sources` 下查找：通常变成不可用；若 B 恰好存在同名对象，则只能靠 hash/size 发现部分冲突，仍不能恢复正确的空间归属。当前设计没有独立于 Root 的全局 Artifact Store 可以承接这种重绑。

#### 5. 在途写回、Git、导出和 Worker 会在执行时重新解析当前 Root

以下消费者都接受 `workspace_id`，在操作发生时读取 Repository 返回的当前 `RootPath`：

- Change Control 读取：`internal/changecontrol/adapter/localfs/reader.go:95`、`:101`、`:113`。
- Safe Writeback：`internal/changecontrol/adapter/localfs/writer.go:304`、`:305`、`:312`。
- Git 审批/写回检查：`internal/platform/gitcli/writeback_inspect.go:263`、`:270`、`:277`。
- Artifact Markdown 导出：`internal/artifact/adapter/localfs/exporter.go:122`、`:126`、`:130`。
- Workspace Attachment Export：`internal/export/adapter/localfs/store.go:472`、`:476`、`:480`。
- committed Source capture：`internal/workspace/application/committed_capture.go:23`、`:36`、`:58`。

Safe Writeback 的持久记录同时绑定 Workspace、Workflow、Proposal、Approval、相对 `target_path` 和批准时 Git HEAD（`migrations/00009_safe_writeback.sql:63`、`:65`、`:73`、`:82`）。如果同一 ID 被改到 B，重启恢复的 A 工作会解析到 B。即使 hash/HEAD 检查通常会拒绝，若 B 恰好具有相同相对文件和可匹配状态，仍可能把旧授权用于错误目录。

新 ID 的失败模式更安全：旧任务仍绑定旧 ID；在旧 Grant 被撤销后，Root resolver 应明确拒绝其文件能力，而不是把任务引向新 Root。

### 当前应用契约与缺口

- `CreateWorkspace` 先 canonicalize Root，再生成新 UUID，并把 Root 与 Git baseline 一起写入（`internal/workspace/application/service.go:61`、`:70`、`:101`、`:106`）。当前没有 Update/Rebind use case。
- 创建前会列出 **全部** roots；只要已有任一记录就拒绝，错误为 `ACTIVE_WORKSPACE_LIMIT_REACHED`（`internal/workspace/application/service.go:74`、`:252`、`:261`）。因此当前代码还不能保留多个非活动 Workspace。
- Repository 只提供 create、按 ID/root get 和列举 roots，没有 list/activate/deactivate/rebind 命令（`internal/workspace/domain/repository.go:44`）。
- HTTP 只公开 create、按 UUID get、scan 和 Source Version list；`OpenWorkspace(root)` 虽存在于 Service interface，却没有路由（`internal/workspace/http/handler.go:23`、`:41`）。
- Domain 只定义 `WorkspaceStatusActive`（`internal/workspace/domain/model.go:9`、`:12`）；OpenAPI 和前端 decoder 也只接受 `active`（`api/openapi/openapi.json:11323`；`web/src/api/workspace.ts:94`）。
- 数据库 `status` 实际只校验非空，而不是文档所称的 enum（`migrations/00002_workspace_sources.sql:11`）。实现切换前必须明确 `inactive`/等价状态的唯一 owner 和迁移，不能依赖任意文本。
- `GetWorkspaceByID` 不过滤 active/granted 状态（`internal/workspace/adapter/postgres/repository.go:56`、`:122`）。仅把旧 Workspace 标记 inactive 还不够；所有持久 Root 消费者必须经过共享 Root Grant resolver，拒绝未获 Grant 的 ID。

### 前端 active workspace 语义

前端已经把 `Workspace ID` 当作稳定身份，把 `rootPath` 当作服务端展示字段：

- 活动 Workspace 只保存 UUID 到 `localStorage`；不保存 root/version（`web/src/app/active-workspace.ts:3`、`:8`、`:36`）。
- Workspace Query key 是 `["workspace", workspaceID]`，API 还校验响应 ID 等于请求 ID（`web/src/features/workspace/WorkspacePage.tsx:101`；`web/src/api/workspace.ts:169`、`:174`）。
- Root 只在 Workspace/Settings 页面展示（`web/src/features/workspace/WorkspacePage.tsx:30`、`:35`；`web/src/features/business/BasicPages.tsx:151`、`:162`）。
- Workspace ID 变化时，`WorkspaceCacheBoundary` 清除旧 Workspace 的 Graph、Collection、Health、Search、Timeline、Review、Memory、Interview 和 Export cache（`web/src/app/WorkspaceCacheBoundary.tsx:17`、`:22`）。
- Event Store 按 Workspace ID 建立连接和 Query family；cleanup 会关闭旧连接并删除旧 Workspace cache（`web/src/events/event-store.tsx:52`、`:69`、`:82`、`:344`）。
- SSE 请求、事件验证和游标也绑定 Workspace ID；游标 key 是 `zhixu.event-cursor.<workspaceId>`（`web/src/events/event-store.tsx:23`；`web/src/events/server-events.ts:577`、`:628`）。
- 前端规范明确要求 Workspace switch 先关闭旧连接并清 cache，旧回调不可写入新 Workspace（`.trellis/spec/frontend/state-management.md:148`、`:155`）。

因此：

- **新 ID 切换**会自然触发既有的缓存隔离、旧请求取消、SSE 断开/重连和按 ID 恢复。
- **同 ID 重绑**对前端不可见，不会清 cache、不会换 SSE cursor、不会取消旧 mutation，也不会清理 URL/详情选择。要使其安全，必须把 Root binding generation 加入每个 Query key、cursor、event 和 mutation identity；这实际上是在客户端重新发明一层复合 Workspace 身份。

当前 UI 仍有两项切换缺口：

- 已连接时 Workspace 页面隐藏创建和“打开已有 Workspace ID”表单，顶部虽写“连接或切换 Workspace”，实际上不能直接切换（`web/src/features/workspace/WorkspacePage.tsx:137`、`:148`、`:167`；`web/src/app/AppShell.tsx:187`）。
- Search URL 显式绑定 `scope_workspace`，切换时会清 query/source/cursor（`web/src/features/search/url-state.ts:55`、`:65`）；但 Documents、Proposal、Workflow、Artifact、Chat 等详情路由不携带 Workspace scope（`web/src/routes/AppRoutes.tsx:37`、`:41`、`:43`、`:50`、`:60`）。切换后应导航到 `/workspace` 或 `/dashboard`，不能让旧对象 ID 留在新 Workspace 的详情 URL 中。

### 方案比较

| 维度 | 新目录创建/切换新身份 | 原身份直接重绑新目录 |
| --- | --- | --- |
| 领域一致性 | Workspace、Root、Git、知识历史保持一一对应 | A 的历史被解释为 B 的历史 |
| 数据隔离 | 直接复用现有 `workspace_id` FK、唯一键、cursor 与 query key | Source/Artifact/Knowledge/Workflow/Review/Memory 等全部混入同一 scope |
| 历史 Artifact | A 继续绑定 A 的 `.knowledge/sources`；未挂载时明确不可用 | A 的 locator 被拿到 B 下读取，历史重读断裂或误读 |
| 在途任务 | 旧任务仍指向旧 ID，可在 Grant resolver 处失败关闭/暂停 | 旧任务按相同 ID 解析到 B，存在错误写回或恢复风险 |
| 前端 | ID 变化自动触发大部分 cache/SSE 隔离 | ID 不变，客户端完全感知不到 Root 世代变化 |
| 数据迁移 | 不迁移子表；只新增/恢复 Workspace 记录与活动状态 | 真正安全需要迁移/复制受管内容、冻结任务、重签 cursor/幂等/缓存，且仍难证明语义 |
| 回滚 | 重新挂载旧 Root 并激活旧 ID；旧历史未改写 | 仅改回路径无法撤销切换期间写入的新 Source、事件、导出或 Git side effect |
| 实现代价 | 需要 lifecycle、list/activate API 和 Host Controller saga | 表面是单行 UPDATE，实际需要 binding generation 覆盖所有后端/前端消费者 |
| 结论 | **默认采用** | **普通流程禁止**；只保留为显式 Root Migration |

### 推荐的切换契约

目标 Root 规范化后按以下顺序分类：

1. 与当前 Root 相同：幂等 no-op，保持当前 ID。
2. 精确匹配一个已知非活动 Workspace，且 Root/Git 绑定验证通过：重新授权并激活原 ID。
3. 未匹配任何 Workspace：创建新 ID，并将它作为目标活动 Workspace。
4. 与当前 Root 不同，但用户声称只是同一 Workspace 移动/改名：普通 switch 返回专用“需要 Root Migration”错误；不得猜测或直接 UPDATE。
5. 同一路径已绑定旧 Workspace，但物理目录已被替换、身份验证失败：不得复用旧 ID。当前唯一 `root_path` 约束下如何保留旧 binding history 并创建新 ID，需要单独设计迁移。

Host Controller、Docker mount 与数据库不能组成单一事务，因此切换必须是可补偿 saga，而不是先写“成功”再重启：

1. 记录/返回可查询的 switching operation，前端进入明确重连状态。
2. 阻止新旧 Workspace 的文件副作用，drain、暂停或取消旧 Workspace 的可恢复写回、reindex 和 Workflow。
3. 停止 API/Worker，撤销旧 Root Grant；遵守 ADR-0017，不让旧、新目录同时可访问（`docs/architecture/adr/0017-exact-workspace-root-grant.md:5`、`:17`）。
4. 只挂载目标 Root，启动受限切换阶段，验证目标 Root/Git。
5. 以数据库事务/CAS 将旧 Workspace 置为非活动，并创建或激活目标 ID；不要更新任何子表的 `workspace_id`。
6. API 与 Worker 均确认目标 ID/Root Grant 一致并 Ready 后，前端才提交新的 active Workspace ID、清理旧页面身份并恢复业务查询。

失败补偿：

- 数据库激活前失败：数据库仍以旧 ID 为 active；恢复旧 mount 并重启旧 runtime。
- 数据库激活后、Ready 前失败：CAS 恢复旧 active ID，再恢复旧 mount；目标 Workspace 记录可保留为 inactive，不能删除其可能已经初始化的历史。
- 任一阶段都不能以“路径 UPDATE 回去”作为完整回滚；切换不改子表，才使恢复旧 ID 成为可靠回滚点。

### 历史 `/workspace` 与显式 Root Migration

任务要求历史 `/workspace` 不得静默解释为宿主机路径（`.trellis/tasks/07-31-docker-host-workspace-path/prd.md:23`）。应区分两种情况：

- **新目录选择**：新建/恢复另一个 Workspace ID，不迁移旧数据。
- **同一物理 Workspace 的地址语义迁移**：可以保留旧 ID，但必须由专用 Root Migration 完成。

Root Migration 至少应：

1. 显式提供 old binding 与 new canonical host path，不允许自动把 `/workspace/x` 拼接到任意宿主机父目录。
2. 证明旧 Docker bind source/target 映射、Git top-level/HEAD 和受管 ContentArtifact 足以说明是同一知识空间；不确定即返回 `WORKSPACE_ROOT_LEGACY_UNSUPPORTED`/等价稳定错误。
3. 在无文件副作用和无在途恢复任务的维护窗口执行。
4. 使用 `workspace.version` CAS，在一个数据库事务中同时更新 `root_path`、`git_repository_path`、Git baseline、`version`、`updated_at`，并保存不可变审计/旧新映射。
5. 切换后强制 API/Worker 重新取得 Root capability。即使 ID 不变，前端也必须把这次操作当 binding generation 变化，清理全部 Workspace cache、SSE cursor 和详情 URL。
6. 回滚必须使用保存的反向映射与对应 mount；无法证明旧目录仍是同一空间时不得自动反向重绑。

普通切换无需、也不应复制旧 Source、ContentArtifact、Knowledge、Workflow、Conversation、Review、Memory、Timeline 或 Export 数据到新 ID。

### UI 影响

采用新身份切换时，UI 应明确表现为“切换 Workspace”，而不是“修改路径”：

- Workspace 管理页列出当前身份和可重新授权的既有身份，至少展示 name、ID、host root、active/inactive、最后验证结果；是否保留最近列表仍是任务当前开放产品决策（`.trellis/tasks/07-31-docker-host-workspace-path/prd.md:56`）。
- 新路径确认文案说明将进入新的知识空间，旧 Source/Knowledge/Review 历史不会自动出现；已知路径则说明将恢复其原身份。
- 切换 operation 独立展示 validating、revoking、recreating、waiting-ready、failed/rolled-back，不把 API 断线解释为业务成功或数据丢失。
- 目标 API Ready 并返回 canonical target Workspace ID 后，才调用 `setActiveWorkspaceId(targetID)`。
- ID 变化后导航到 `/workspace` 或 `/dashboard`；不要保留旧 Workspace 的 detail route 参数。
- 保留旧 ID 的 `sessionStorage` SSE cursor 是合理的，未来切回旧 Workspace 时可恢复；游标过期继续走现有权威回查流程。不要把旧 cursor 移到新 ID。

### Files Found

| File | Description |
| --- | --- |
| `.trellis/tasks/07-31-docker-host-workspace-path/prd.md` | 当前精确 Root Grant、身份切换和历史兼容要求。 |
| `docs/architecture/CONTEXT.md` | Workspace、Root、Grant、Switch、Root Migration 的统一领域语言。 |
| `docs/architecture/adr/0017-exact-workspace-root-grant.md` | 已接受的单精确目录授权与 Host Controller 决策。 |
| `docs/architecture/adr/0018-workspace-root-identity.md` | 已接受的不同 Root 使用不同 Workspace 身份决策。 |
| `internal/workspace/domain/model.go` | Workspace/Source/Artifact 的领域身份与 Root 字段。 |
| `internal/workspace/domain/repository.go` | 当前 Repository 只有 create/get/root list，没有 lifecycle/rebind。 |
| `internal/workspace/application/service.go` | 创建 UUID、Root canonicalization、单 Workspace 限制、扫描链路。 |
| `internal/workspace/adapter/postgres/repository.go` | Workspace 持久化、Source/Artifact/Version 幂等归属和 SourceMaterial Root 联接。 |
| `internal/workspace/http/handler.go` | 当前公开 Workspace HTTP surface。 |
| `migrations/00002_workspace_sources.sql` | Workspace 主键、Root 唯一、单 active、Source FK 和不可变版本基础。 |
| `migrations/00006_content_artifact.sql` | Workspace-scoped 不可变受管内容。 |
| `migrations/00003_workflow.sql` | Workspace-scoped Workflow 历史。 |
| `migrations/00009_safe_writeback.sql` | Workspace/Git/目标路径绑定的持久写回事实。 |
| `migrations/00017_knowledge_domain.sql` | Workspace-scoped Topic/Claim/Relation 与复合隔离约束。 |
| `internal/changecontrol/adapter/localfs/writer.go` | 执行时由 Workspace ID 解析当前 Root。 |
| `internal/platform/gitcli/writeback_inspect.go` | Git 操作执行时由 Workspace ID 解析当前 Root。 |
| `internal/ingestion/adapter/workspace/reader.go` | 历史 SourceMaterial 通过当前 Root 重读 Artifact。 |
| `web/src/app/active-workspace.ts` | 浏览器 active Workspace ID 的唯一持久 owner。 |
| `web/src/app/WorkspaceCacheBoundary.tsx` | Workspace ID 切换时的 Feature cache 清理。 |
| `web/src/events/event-store.tsx` | Workspace-scoped SSE、cursor recovery 和旧连接 cleanup。 |
| `web/src/features/workspace/WorkspacePage.tsx` | 当前创建/按 UUID 打开 UI 及其切换缺口。 |
| `web/src/api/workspace.ts` | `status='active'` 的严格 Workspace wire decoder。 |

### Code Patterns

- **Workspace-first ownership**: 数据模型、查询键、事件游标、幂等键和复合 FK 都以 `WorkspaceID` 开头；Root 不参与这些身份键。
- **Late root resolution**: 文件/Git消费者在执行时通过 `workspaceID -> Repository -> RootPath` 取得能力地址，而不是在任务创建时冻结 Root binding。
- **Immutable history**: Source Version、ContentArtifact、Review Answer/Command、Timeline 等历史采用不可变或 append-only 契约，不能靠批量改写归属修复重绑。
- **Frontend switch by ID**: `setActiveWorkspaceId` 是缓存、SSE 与页面 Server State 隔离的触发点；Root 字符串变化不是触发点。
- **Single active, incomplete lifecycle**: 数据库已有单 active 索引，但 Domain/API/UI 还没有非活动 Workspace 和切换命令。

### External References

- 未使用外部资料。本研究只回答仓库内 Workspace 身份与数据契约；Docker 挂载安全边界以已接受的 ADR-0017 和当前任务 PRD 为准。

### Related Specs

- `.trellis/spec/backend/database-guidelines.md:1361`：Repository 查询必须显式携带 Workspace scope。
- `.trellis/spec/backend/database-guidelines.md:1372`：Source Version 的 Workspace 是归属镜像，复合 FK 防止漂移。
- `.trellis/spec/frontend/state-management.md:145`：SSE cursor 按 Workspace 保存。
- `.trellis/spec/frontend/state-management.md:155`：Workspace 切换先关闭旧连接并移除旧 cache。
- `.trellis/spec/frontend/state-management.md:169`：A -> B 必须关闭 A、只建立一个 B 连接。
- `docs/architecture/frontend-architecture.md:221`：查询按 Workspace 隔离，Workspace Switch 清理 cache。
- `docs/architecture/database-design.md:117`：不可变 Source Version 的历史内容不允许被新路径内容冒充。
- `docs/product/PRD.md:737`：正式 v1 只允许一个活动 Workspace，但数据模型保留 Workspace ID。

## Caveats / Not Found

- 未发现 `ListWorkspaces`、`ActivateWorkspace`、`DeactivateWorkspace`、`SwitchWorkspace`、`UpdateWorkspaceRoot` 或 Root binding generation 的生产实现。
- 未发现 Worker 在 claim/recovery 时统一校验“任务 Workspace ID 当前拥有 Root Grant”的边界。当前 `GetWorkspaceByID` 不过滤 status，后续实现不能只增加 inactive 字符串。
- 未检查真实 PostgreSQL 实例中的 Workspace 行数、历史 status 或 `/workspace` 数据量；迁移规模和实际 legacy 分布未知。本结论基于 schema、代码和文档契约。
- 同一路径被删除后由无关目录占用时，单靠 canonical path 不能安全恢复旧 ID；需要持久 Root identity/fingerprint 或明确的新身份冲突处理。仓库当前没有该契约。
- `.trellis/tasks/07-31-docker-host-workspace-path/research/host-path-contract.md:11` 推荐“父目录同路径绑定”，但已接受的 ADR-0017 明确禁止父目录授权并要求精确 Root 重建（`docs/architecture/adr/0017-exact-workspace-root-grant.md:7`）。两者权限模型冲突；实现应以 ADR-0017 和当前 PRD 为准，前一研究需标记为被取代或更新，不能同时作为实现依据。
- Workspace Root Migration 的完整证明算法、审计 schema 与反向迁移命令不在当前任务范围；本研究只确定普通切换不得隐式重绑。
