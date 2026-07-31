# Artifact 工作台契约

## Scenario: M8-01 Artifact Wire And Recovery

### 1. Scope / Trigger

- 修改 `web/src/api/artifacts.ts`、`web/src/features/artifacts`、Artifact 路由/导航/样式、
  `publish_artifact` Proposal 展示或 Artifact 浏览器 smoke 时应用。
- Network JSON、Active Workspace、路由 Artifact ID 和 generation Workflow 状态必须在 API/Query 边界形成绑定后的 UI model。

### 2. Signatures

```ts
listArtifacts(workspaceId, options?, signal?)
getArtifact(workspaceId, artifactId, signal?)
getArtifactSectionGenerations(workspaceId, artifactId, signal?)
planArtifact(input)
submitArtifactOutline(input)
approveArtifactOutline(input)
startArtifactRevision(input)
recordArtifactGapSection(input)
generateArtifactSection(input)
approveArtifactDraft(input)
exportArtifactMarkdown(input)
createArtifactPublishProposal(input)
```

- 路由为 `/artifacts` 和 `/artifacts/:artifactId`；Query key 固定包含 Workspace，并按 list/detail/
  section-generations/Workflow identity 分层。

### 3. Contracts

- `web/src/api/artifacts.ts` 是 Artifact wire 唯一 owner。UUID、SHA-256、RFC3339、正整数、状态枚举、字段集合和
  Workspace/Artifact/Revision binding 全部运行时校验；Feature 不读取 raw snake_case 或自行 cast。
- Artifact 列表、详情和 generation 响应必须绑定当前 Active Workspace；详情 ID 必须绑定路由 ID。任何漂移都以
  `INVALID_RESPONSE` fail closed，不渲染部分 Artifact。
- Artifact status、current Revision、Coverage、Citation `verified`、export hash/size 和 publication Proposal binding 只显示
  服务端事实；客户端不重新判定 Citation 资格，也不把 mutation optimistic state 当权威状态。
- Mutation 成功后失效 Workspace list、Artifact detail 和 generation queries；409 保留明确冲突并回查服务端，不显示成功。
- generation acceptance 可以是 `PENDING|COMPLETED|FAILED|CANCELLED|RECOVERY_REQUIRED`；持久 generation 列表不得包含
  `COMPLETED`，因为完成结果已由 Artifact Revision/receipt 拥有。Workflow 终态后仍轮询 receipt，缺失时显示恢复状态。
- GAP 章节显示显式 gap，正文和 Citation 必须为空；COVERED/PARTIAL Citation 只展示服务器返回的 version/span/excerpt。
- DRAFT/APPROVED/EXPORTED 可启动修订；进入 GENERATING 后，已有章节和未完成章节都可重新生成或由人工 GAP 替换，成功后只展示服务端返回的新 Revision。
- 浏览器不显示 managed export 文件路径，只展示可公开的 revision/hash/size 和可回读记录；Publish 成功只展示
  `publish_artifact` Proposal，不宣称正式知识已经写入。
- Idempotency key 在一次用户动作及 response-loss retry 中保持稳定；Workspace、Artifact、expected version 或 payload
  变化必须产生新的 command identity。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 未知字段、非法 UUID/hash/time/version/status | `ArtifactApiError(INVALID_RESPONSE)`，不渲染部分结果 |
| 响应 Workspace/Artifact/Revision/Export/Proposal binding 漂移 | fail closed，相关命令不可继续 |
| Loading/Empty/Unavailable | 分离显示，不把依赖故障当空列表或成功 |
| mutation 409 | 显示冲突并重新查询；不 optimistic 推进状态 |
| generation Workflow terminal 但 receipt 未出现 | 有界轮询后显示 recovery，不自行生成章节 |
| GAP 带正文/Citation，或 persisted generation 为 COMPLETED | decoder 拒绝整个响应 |
| desktop/mobile console error 或 document 横向溢出 | browser gate 失败 |

### 5. Good / Base / Bad Cases

- Good：刷新后从 REST 恢复 Artifact 与 generation；COVERED/GAP 两章、导出记录和 Proposal binding 与服务端一致。
- Base：没有 Artifact 显示空态；模型 capability unavailable 时保留人工 GAP 流程和明确错误。
- Bad：组件读取 snake_case、相信 Query key 足以证明 Workspace、把 Workflow succeeded 当章节已持久化，或展示本地文件路径。

### 6. Tests Required

- Decoder：字段精确性、所有枚举、Workspace/资源绑定、GAP/Citation、generation acceptance/persisted 区分、Problem/Abort。
- Query：Workspace key、mutation invalidation、response-loss key、Workflow terminal 后 receipt polling 与 recovery limit。
- Component：列表/详情、outline、COVERED/GAP、Draft、冲突、generation unavailable/recovery、export 与 Publish Proposal。
- Canonical：ESLint、TypeScript、全部 Vitest、production build。
- Browser：真实 API/Worker/Vite，桌面与 390x844 移动端完成完整业务链；断言 console 零 error/warning、文本不溢出、
  document 和 Artifact 容器无横向 overflow。

### 7. Wrong vs Correct

```text
Wrong: Workflow 返回 SUCCEEDED 后在本地拼出 COVERED 章节，或把客户端 Citation 标成 verified。
Correct: Workflow 只触发 receipt/Artifact REST 回查；只有严格 decoder 接受的服务器 Revision 才进入 UI。

Wrong: Publish mutation 成功后显示“已写入知识库”。
Correct: 显示冻结的 PUBLISH_ARTIFACT Proposal ID；正式 Document/Git/Index 状态等待 Change Control 后续事实。
```
