# Organizing 跨层影响面

## 结论

Organizing 应是独立 `internal/organizing` 能力。Draft、Material、Template Revision、Snapshot 与 Run binding 由该模块唯一拥有；Retrieval、Profile、Knowledge、Collection、Workflow、Artifact 和 Change Control 只通过公开应用端口接入，不在它们的表中寄存“材料篮”状态。

首版闭环为：

```text
intent / topic / collection
  -> 有界建议与批量补全
  -> Suggested Material Set Draft（expected version CAS）
  -> 用户显式确认
  -> 不可变 Snapshot + start outbox（同一事务）
  -> Worker 以 snapshot ID 幂等启动固定 Workflow
  -> Run binding -> Human Task / Artifact / Proposal
```

确认事务不能直接调用通用 `workflow.Service.Start`：该调用拥有自己的事务，无法与 Draft、Snapshot 和 Outbox 原子闭合。Worker 应以 `snapshot:<id>` 为固定幂等键启动；启动成功但 binding 回写丢失时，重放 Start 修复 binding。

## Material 判别联合

- `SOURCE_VERSION`：Source Version、content hash、精确 span/evidence，可选 Profile Revision。
- `DOCUMENT_REVISION`：Document、不可变 Article Revision 与 content hash；只能经 Authoring 稳定 reader 读取。
- `CLAIM`：Claim version、状态和 ClaimSource/Evidence。
- `SMART_COLLECTION`：Collection version、query hash、read-model revision；确认时有界展开为实际冻结成员。

Snapshot material 保存 kind、identity、version/hash、evidence tuple、顺序和 provenance，不复制正文。Collection 展开必须有 `MaxSnapshotMaterials` 和总页数上限，超限明确拒绝。

## 可复用端口

- Retrieval：`retrieval/application.SearchService.Search` 与 `EvidenceReferenceService.OpenCitationEvidenceBatch`。
- Profile：现有 `capture/application.ProfileReader.GetProfile` 只有单条读取；Organizing 需要新增有界 batch port，禁止循环形成 N+1。
- Knowledge：`knowledge/application.Service.GetClaims` 已支持最多 500 条的批量查询并返回 Sources。
- Collection：建议使用 `Results`；确认使用 `PlanDurableScan` / `ReadDurableScanPage` 冻结实际成员。
- Workflow：使用已注册 Definition、`BuildRuntimeStartRequest` 和 Runtime Human Task；客户端不能提交 Definition。
- Artifact：复用 Plan、Outline、Section、Draft Approval 与 Publish，继续强制 Citation/GAP。
- Change Control：Artifact 发布和合并都只创建 Proposal；正式文件修改仍由 Approval / Safe Writeback 唯一拥有。

## Template 边界

Template Revision 是 append-only 的受限声明。Compiler 只允许输出注册 Definition、Result Kind、章节与表达参数，以及固定 evidence/conflict/GAP policy。声明不得包含 tool、permission、node、retry、script 或 raw system prompt；未知字段 fail closed。

四类固定结果：

- `TOPIC_ARTICLE`：大纲 Human Task 后生成 `DOCUMENT_DRAFT` Artifact，可选发布 Proposal。
- `MERGE_DOCUMENTS`：比较与 Diff 后 Human Task，生成不覆盖输入源的 Merge Proposal。
- `KNOWLEDGE_REPORT`：默认保持 Artifact。
- `INTERVIEW_REVIEW`：默认保持 Artifact。

## 精确影响面

新增 `internal/organizing/{domain,application,adapter/postgres,http,workflow}`、additive migration、OpenAPI、Composition Root、Worker dispatcher、前端 strict decoder / query / pages / tests。

现有模块原则上只新增公开 bridge 或 batch read port；不得让 Organizing 直查其他模块私有表。前端唯一 owner 是整理页，浏览器存储不保存 Draft/Snapshot/Run 第二状态机；SSE 只失效 `['organizing', workspaceId]` 后回查。

## 验证重点

- Workspace 隔离、Draft CAS、same-key replay/conflict、确认并发和 response loss。
- Snapshot / Template append-only 与 canonical hash。
- stale 或不可访问 Evidence fail closed，Suggestion hydration 无 N+1。
- 四个固定 Workflow、Outline/Merge Human Task、重启恢复与 Citation/GAP。
- Artifact/Proposal owner 边界，不覆盖输入 Source。
- 前端 strict decoder、刷新恢复、409 冲突、SSE 回查与桌面/移动无横向溢出。
