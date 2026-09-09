# 主动创作与合成笔记工作台契约

## Scenario: Authoring Overview And Recoverable Markdown Editor

### 1. Scope / Trigger

- 修改 `web/src/api/authoring.ts`、`web/src/features/authoring`、`/authoring`、`/authoring/new`、
  Authoring 导航或发布恢复时应用。
- HTTP JSON、Active Workspace、Draft/Document/Revision/Publication identity 必须在 API/Query 边界绑定为 UI model。

### 2. Signatures

```ts
getAuthoringOverview(workspaceId, signal?)
listWorkingDrafts(workspaceId, options?, signal?)
listDocumentDrafts(workspaceId, options?, signal?)
createWorkingDraft(input)
getWorkingDraft(workspaceId, draftId, signal?)
updateWorkingDraft(input)
freezeWorkingDraft(input)
getDocumentDraft(workspaceId, documentId, signal?)
publishArticleRevision(input)
```

### 3. Contracts

- `web/src/api/authoring.ts` 是 Authoring wire 唯一 owner；字段集合、UUID、hash、time、版本、状态、href 和
  Workspace/Draft/Document/Revision/Proposal binding 全部严格运行时校验，Feature 不读取 snake_case 或自行 cast。
- Query key 必须包含 Workspace，并按 overview、Working Draft、Document 和 Publication identity 分层；
  SSE 只失效 Query，REST 响应仍是事实源。
- 编辑器保存 Working Draft，不把每次输入变成 Revision。autosave debounce、保存中、已保存、冲突和失败必须分离；
  页面刷新从服务端恢复，不写 Local Storage。
- Freeze 成功后以 mutation 响应中的最新 Revision 为准；旧 Document query 稍后返回时不得覆盖它。
  Publication 只有在 `articleRevisionId` 与当前 Revision 完全一致时才显示或禁用发布按钮。
- Markdown preview 必须经过现有 sanitizer；Monaco model 使用稳定 URI，并在 editor detach 后释放。
- `/authoring` 提供创建文章和恢复最近草稿的主入口；Organizing 未接入时显示明确 unavailable，不能指向假页面。
- 发布操作只显示 Proposal identity 和待审批状态，不宣称 Git、文件或正式知识已经完成。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 未知字段、非法 UUID/hash/time/version/status 或 binding 漂移 | `INVALID_RESPONSE`，不渲染部分事实 |
| autosave 409 | 保留本地输入，显示冲突并回查服务端；不显示已保存 |
| Freeze/Publish response unknown | 重试同一动作 identity 并回查，不生成第二命令 |
| 旧 Revision publication 返回 | 不显示为当前发布状态，当前 Revision 仍可发布 |
| capability unavailable | 保留只读内容与明确原因，不伪装为空或成功 |
| desktop/mobile console error、文本或 document 横向溢出 | browser gate 失败 |

### 5. Tests Required

- Decoder：exact fields、全部状态、Workspace/资源/Proposal binding、Problem/Abort、分页 cursor。
- Query/Mutation：Workspace key、stable idempotency、invalidation、autosave CAS、Freeze 最新响应优先、publication match。
- Component：overview、空态、编辑/预览、保存状态、冲突恢复、连续 Freeze、发布与返回焦点。
- Canonical：ESLint、TypeScript、Vitest、production build 和 `git diff --check`。
- Browser：真实 API/Worker/Vite，在桌面和 `390x844` 创建、恢复、Freeze、发布；无横向溢出和 console error/warning。

### 6. Wrong vs Correct

```text
Wrong: Document query 的旧 Revision 覆盖刚 Freeze 的 mutation 结果。
Correct: 最新 Freeze identity 优先，后续 query 必须与当前 Revision 对齐后才可替换。

Wrong: Publication 存在就禁用当前页面的发布按钮。
Correct: 只有 Publication.articleRevisionId 等于当前 Revision.id 才属于当前版本。
```

## Scenario: Synthesis Notes, Frozen Sources And NOTE Interview Preparation

### 1. Scope / Trigger

- 修改 `/authoring/notes`、`/authoring/notes/:noteId`、合成处理记录、来源深链或 NOTE 面试恢复时应用。
- 合成笔记和准备任务的 wire owner 是 [`web/src/api/synthesis.ts`](../../../web/src/api/synthesis.ts)；
  面试会话、题面、评分、报告与学习步骤的 wire owner 是 [`web/src/api/interview.ts`](../../../web/src/api/interview.ts)。
  两者调用 generated Raw operation，Feature 只消费校验后的 UI model。
- API 的准确字段与 HTTP 状态以 [`api/openapi/openapi.json`](../../../api/openapi/openapi.json)、
  [`SynthesisHandler`](../../../internal/organizing/http/synthesis_handler.go) 和
  [`NotePreparationHandler`](../../../internal/review/interview/http/note_preparation.go) 为准。

### 2. Signatures

以下 `S` 表示 `/api/v1/workspaces/{workspace_id}/synthesis`，`N` 表示 `S/notes/{note_id}`：

| Operation | Response |
| --- | --- |
| `GET S/notes`、`GET S/processing` | `200 { workspace_id, items, next_cursor }`，Web 请求 `limit=20` |
| `GET N` | `200 { workspace_id, note, current_revision, published_revision, publication, latest_processing }` |
| `GET N/revisions` | `200 { workspace_id, note_id, items, next_cursor }`，版本号降序 |
| `GET N/revisions/{revision_id}` | `200 { workspace_id, note_id, revision }` |
| `GET N/revisions/{revision_id}/sources/{source_span_id}` | `200 { workspace_id, note_id, revision_id, reference, availability, text }` |
| `GET S/processing/{processing_id}` | `200 { workspace_id, processing }` |
| `POST S/processing/{processing_id}/retry` | `202 { workspace_id, processing, replayed }` |
| `POST N/interviews`、`POST N/interviews/{preparation_id}/retry` | `202 { preparation, replayed }` |
| `GET N/interviews/{preparation_id}` | `200 { preparation, replayed }`；GET 的 `replayed` 为 `false` |
| `GET N/interviews` | `200 { items }`；最近创建的最多 20 个 preparation |

```ts
resolveSynthesisSourceLink(params: URLSearchParams, revision: SynthesisRevision)
// => { kind: "none" | "invalid" | "missing" } | { kind: "resolved", reference }

retrySynthesisProcessing({ workspaceId, processingId, expectedVersion, idempotencyKey, signal })
prepareNoteInterview(workspaceId, noteId, options, idempotencyKey, preparationId?, signal?)
```

### 3. Contracts

- `current_revision` 是当前生成内容，`published_revision` 是已发布版本，两者可不同；`publication` 是当前候选对应的
  Proposal binding，其 `revision_id/article_revision_id/content_hash` 必须与 current 一致。不能因候选已生成或有
  Proposal 就把它显示为正式版本。历史选择由 `revision_id` URL 参数持有，未指定时显示 current；版本切换和资料查看
  都是读取，不触发 Proposal、发布或合成命令。实现见
  [`SynthesisNotePage.tsx`](../../../web/src/features/synthesis/SynthesisNotePage.tsx)。
- FACT、CONFLICT、GAP 是严格判别联合。FACT 与冲突各观点显示自己的适用条件和来源；未解决 GAP 保持“待补充”，
  `resolution` 存在才显示补充结论。不能把冲突裁成单一结论，也不能替缺口补造答案。
- 来源深链只选择所展示 revision 已保存的精确引用。八个参数必须各出现一次：
  `workspace_id/source_id/source_version_id/content_artifact_id/parse_projection_id/source_span_id/content_hash/excerpt_hash`。
  ID 必须规范，hash 必须为小写 64 位 SHA-256；Workspace 必须一致。URL 中的标题不参与来源选择，展示标题来自保存的 ref。
  [`source-link.ts`](../../../web/src/features/synthesis/source-link.ts) 与 decoder 共用 `synthesisSourceIdentity()`：

  ```ts
  JSON.stringify([
    reference.source.workspaceId, reference.source.sourceId, reference.source.sourceVersionId,
    reference.source.contentArtifactId, reference.source.parseProjectionId, reference.source.contentHash,
    reference.sourceSpanId, reference.excerptHash,
  ])
  ```

- 仅用户打开来源或有效来源深链才请求正文。source-open 虽以 revision/span 寻址，响应必须匹配完整保存 tuple；
  `AVAILABLE` 才能有 `text`，`STALE|UNAVAILABLE` 必须为 `null`，不能跳到新版本或其他资料。
  [`NoteContent.tsx`](../../../web/src/features/synthesis/NoteContent.tsx) 关闭弹窗后恢复触发按钮焦点；深链打开时恢复到笔记内容。
  同一展示版本的页内 Query 回查不能重开已关闭弹窗；全页加载仍按 URL 恢复来源选择。
- Query key 绑定 Workspace、Note、Revision、Processing 或 Preparation；来源另绑定 span/excerpt hash，`gcTime: 0`。
  [`queries.ts`](../../../web/src/features/synthesis/queries.ts) 只对活动处理/准备任务每 3 秒回查；笔记详情在
  `PENDING_APPROVAL` 时也回查正式版本。查询报错或相应终态停止轮询，所有 mutation 禁止自动重试。
- [`ProcessingRecord.tsx`](../../../web/src/features/synthesis/ProcessingRecord.tsx) 只对
  `status=FAILED && failure.retryable=true` 展示显式重试。POST body 仅含 `{ expected_version }`，携带稳定
  `Idempotency-Key`；响应丢失后重用相同 key/version，不能把 `RECOVERY_REQUIRED` 变成自动重做。
  首次合成失败即使没有 Note，也必须在处理记录中可见；`NO_CHANGE` 不表示产生了新 revision。
- 面试准备只能以已发布版本发起。五个 options 全部必填：`role` 为非空规范文本且不超过 256 字节，
  `difficulty=FOUNDATION|INTERMEDIATE|ADVANCED`，`duration_minutes=1..240`、`question_count=1..20`、
  `max_follow_ups=0..20`。UI 最低题数等于已发布版本包含的条目种类数；最终有效性仍由服务端裁决。
- preparation 保存冻结 `note_revision`、原 options、Workflow ID 与状态；只有 `READY` 才有 `session_id`。
  `202` 只代表接收。`QUEUED|GENERATING` 通过 GET 回查，`FAILED|CAPABILITY_UNAVAILABLE|RECOVERY_REQUIRED`
  保留服务端失败原因；只有可重试且非 `RECOVERY_REQUIRED` 的准备任务提供显式 retry。
- [`NoteInterviewPanel.tsx`](../../../web/src/features/synthesis/NoteInterviewPanel.tsx) 使用 `preparation_id` URL
  恢复选中任务；初始响应丢失时查询最近列表。重试已存在的 preparation 必须使用它的冻结 revision 和原 options
  组成动作 fingerprint，不能用后来发布的版本或表单新值轮换 key。成功后缓存 response 并更新 URL；跨 Workspace
  的迟到结果不得更新当前页面，离开作用域时中止在途命令。
- NOTE 面试 scope 只有 `note_revision`，不能混入 Claim/Topic scope。答前 Question 使用
  `source_kind=NOTE_REVISION`、`claim_id=null` 和 `note_item`，不含答案、追问计划或完整 sources。
  答后 `note_source`、报告与学习步骤必须继续绑定同一冻结 revision/item；正式 Claim Evidence 保持空，不能伪造 Claim。
  旧 Claim 的 Question/Finding/PathStep HTTP 投影省略新增 `source_kind`，新 decoder 缺失时按 Claim 处理。
- [`InterviewPage.tsx`](../../../web/src/features/interview/InterviewPage.tsx) 使用服务端 `next_question` 和已保存 Turn
  恢复进度，显示实际 scorer 回执；`interview-deterministic/v2` 明确说明是确定性规则评分。
  [`SynthesisEvidence.tsx`](../../../web/src/features/synthesis/SynthesisEvidence.tsx) 只在答后、报告或学习步骤打开冻结原始来源，
  不走正式 Claim Evidence 入口。同一 item 可以对应多道题/报告结论，渲染 key 不能只用 item ID。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 未知/重复 JSON 字段、错误 Content-Type/状态、非法联合或资源 binding | `INVALID_RESPONSE`，不渲染部分事实 |
| current/published/document/publication 身份漂移 | `INVALID_RESPONSE`；不能把其他候选显示为已发布 |
| 来源深链缺字段、重复字段、非规范值或跨 Workspace | 显示“无法定位原始来源”，零 source-open 请求 |
| 完整 tuple 不在所选 revision 中 | 同样显式失败，不回退到 current、新来源或另一个历史版本 |
| source-open tuple 改变，或 availability/text 不一致 | `INVALID_RESPONSE`；不展示替代片段 |
| 来源 `STALE|UNAVAILABLE` | 保留原引用，明确显示无法核验，正文不显示 |
| Processing `RECOVERY_REQUIRED` 或不可重试失败 | 显示原因/Workflow 入口，不提供重试按钮 |
| retry 响应 version 不大于 `expected_version` | `INVALID_RESPONSE`；不能宣称重试已接收 |
| 无 published revision | 说明发布前置条件，不展示新面试准备表单 |
| preparation 裸对象、READY/session 不一致、返回 options 改变 | `INVALID_RESPONSE`；保持 `{ preparation, replayed }` envelope |
| NOTE 题面含答案/来源正文，或答后 revision/item 漂移 | `INVALID_RESPONSE`；不通过页面隐藏来替代边界校验 |

### 5. Good / Base / Bad Cases

- Good：已发布 v1 保持可读，当前候选 v2 有新事实/冲突，准备面试仍冻结 v1；之后发布 v2 不改变既有面试与失败重试。
- Base：首次合成失败没有 Note 时仍展示 Processing 和可恢复原因；未解决 GAP 可以没有直接来源，并明确资料不足。
- Bad：按 span ID 单独信任 URL、把候选当正式版本、向旧 Claim 投影强加字段、根据本地索引猜下一题，或每次失败创建新命令。

### 6. Tests Required

- Decoder：[`synthesis.test.ts`](../../../web/src/api/synthesis.test.ts) 验证三个 item 分支、版本/Workspace/publication
  binding、完整 source tuple、availability/text、幂等 retry、preparation envelope 与选项保持；
  [`interview.test.ts`](../../../web/src/api/interview.test.ts) 验证答前无答案、NOTE scope/item 与答后来源绑定。
- Component：[`SynthesisPages.test.tsx`](../../../web/src/features/synthesis/SynthesisPages.test.tsx) 覆盖首次失败、
  current/history、惰性来源/焦点、来源参数负例、丢响应回查与发布新版后的同 key retry；retry 成功必须清除错误并恢复
  `preparation_id`，不能只断言最近列表出现“继续面试”。
  [`InterviewPage.test.tsx`](../../../web/src/features/interview/InterviewPage.test.tsx) 覆盖答前题面和同 item 的多条报告结论。
- HTTP：[`legacy_projection_test.go`](../../../internal/review/interview/http/legacy_projection_test.go) 对隐式/显式
  Claim 的旧 Scope、Question、Score、Finding、PathStep 比较完整旧 JSON；
  [`note_projection_test.go`](../../../internal/review/interview/http/note_projection_test.go) 保证 NOTE 投影无答案泄漏。

```bash
npm run test --prefix web -- src/api/synthesis.test.ts src/api/interview.test.ts src/features/synthesis/SynthesisPages.test.tsx src/features/interview/InterviewPage.test.tsx
GIN_MODE=release go test -race -mod=vendor ./internal/review/interview/http -count=1 -timeout=60s
npm run typecheck --prefix web
npm run lint --prefix web
node api/openapi/check.mjs
git diff --check
```

- 真实浏览器入口是 [`web/e2e/synthesis-notes.smoke.spec.ts`](../../../web/e2e/synthesis-notes.smoke.spec.ts)：
  `npm run test:e2e --prefix web -- synthesis-notes.smoke.spec.ts`。必须连接隔离的真实 API/Worker/Web，
  环境变量统一使用前缀 `ZHIXU_SYNTHESIS_SMOKE_`，必填后缀为 `BASE_URL`、`SESSION_TOKEN`、`CSRF_TOKEN`、
  `WORKSPACE_ID`、`NOTE_ID`；`BASE_URL` 为 loopback HTTP origin。
  可选 `ZHIXU_SYNTHESIS_SMOKE_ARTIFACT_DIR` 必须为规范绝对路径；凭据不能输出到报告。
  桌面和 `390x844` 对照 REST 核验 current/published/history、来源全文 SHA-256、篡改深链零请求、
  准备刷新恢复、答前隔离、答后来源和 Turn 恢复，并检查焦点、console/network 与横向溢出。
  脚本存在或 `--list` 成功只证明可加载，实际 browser 是否通过必须以该次运行结果为准。

### 7. Wrong vs Correct

```text
Wrong: 来源 span 相同就展示响应正文，或使用 URL 的标题拼一个新 ref。
Correct: URL 必须命中所选 revision 已保存的完整 tuple，响应再次匹配 tuple 后才展示 AVAILABLE 正文。

Wrong: 新版已发布后，重试旧 preparation 使用当前表单/新版 revision 并生成新 key。
Correct: 重试使用旧 preparation 的冻结 revision、原 options 和同一动作 key；成功回执仍严格解码 envelope。

Wrong: 重试测试只看到“继续面试”就算 mutation 成功，忽略 decoder 错误被 recent-list 回查掩盖。
Correct: 使用真实 envelope，并同时断言成功 URL、错误消失、原 key/options 保持。
```
