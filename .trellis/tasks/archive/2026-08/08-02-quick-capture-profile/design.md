# 快速记录与文档知识画像：技术设计

## Boundaries

- 新建 `internal/capture` 编排 Capture 命令与异步状态；Source/Artifact/Version 仍由 Workspace owner 端口写入。
- Ingestion 继续只拥有安全校验、解析、Span 与 Chunk；Capture 不复制 parser 规则。
- Retrieval 继续拥有索引；Profile 通过已验证 Chunk/Span 读取端口取证据。
- Profile 归入 Organizing 的 derived projection，不依赖 Knowledge 正式写表。

## Capture Model

`Capture` 保存 Workspace、kind、原始来源摘要/哈希、Source ID、latest Source Version、状态、失败阶段、idempotency binding 和 version。正文或 Secret 不进入事件与日志。

建议状态：`RECEIVED → SOURCE_SAVED → FETCHING|PROCESSING → READY`，失败为 `FETCH_FAILED|PROCESSING_FAILED`；图片无能力以 `READY_DEGRADED` 表达原件已保存而派生内容不可用。

JSON endpoint 处理 TEXT/URL，multipart endpoint 处理 FILE/IMAGE。服务端嗅探媒体类型和大小；文件名只作显示，不作路径。受控 Artifact Store 使用 create-only content hash。

URL worker 使用独立安全 Transport。Source 在 Fetch 前已提交；成功响应转为 immutable HTML Artifact/Version，失败只更新 Capture attempt。

## Processing Flow

```text
Capture command
  -> atomic Capture + Source (+ Artifact/Version when bytes exist) + Outbox
  -> durable Worker
  -> Ingestion(SourceVersion)
  -> Retrieval index activation/update
  -> Profile generation
  -> REST read model + SSE invalidation
```

各阶段使用独立 attempt 与幂等键。Response loss 通过 command receipt 回放，不重新获取 URL 或重新写 Artifact。

## Profile Contract

Profile output 使用严格 `document-knowledge-profile/v1` Schema。每项知识点、示例和候选 Topic 至少携带一个可验证 Source Span；没有证据的字段被拒绝或明确记为 gap。

Profile Revision 绑定 Source Version、Parse Projection、Index Version、prompt/model/settings/schema。模型 disabled 时不创建假 Profile 内容，只持久化 capability unavailable 状态。

## API And UI

- 后端提供 Capture create/list/detail/retry 与 Profile get/retry。
- Inbox API 从 Source/Capture read model 输出判别联合；旧 Source Version 列表保持兼容期。
- 前端建立唯一 `web/src/api/captures.ts` 严格 Decoder、Workspace-bound Query keys 和 Quick Capture Dialog owner。
- AppShell 挂载单一全局入口；Dialog 草稿为内存状态，关闭确认后清除，不存 Secret 或服务端状态。

## Migration And Rollback

新增 Capture、Attempt、Profile、Profile Revision/Attempt、Outbox 和必要 read projection。不可变输出 append-only；状态以 CAS 更新。关闭 capability 后现有 Source/Artifact/Version 和基础 Inbox 仍可读取。

## Risks

- URL SSRF 与大响应：DNS/redirect/size/time policy fail closed。
- 多阶段假成功：每层独立状态，只有权威完成事实才进入 READY。
- 图片能力缺失：保存原件和派生处理分开。
- Inbox 兼容：新 read model 不改变既有 Source Version ID 和 Evidence URL。
