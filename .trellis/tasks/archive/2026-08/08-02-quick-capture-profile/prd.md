# 快速记录与文档知识画像

## Goal

让用户在应用任意页面低摩擦保存文字、URL、文件和图片，并自动得到可靠的基础索引与可追溯 Document Knowledge Profile，为后续按知识点发现整理材料提供基础。

## Dependencies

- 本任务没有其他子任务硬依赖。
- 必须复用现有 Workspace Source/Content Artifact/Source Version、Ingestion、Retrieval、Agent Workflow 与模型设置能力。
- 本任务交付的 Profile v1 与外部变更重新捕获契约，是 `08-02-organizing-material-templates` 和 `08-02-git-remote-sync` 的前置依赖。

## Requirements

### Capture

- 应用任意业务页面提供 Quick Capture 入口和应用内快捷键；保存或关闭后恢复原页面与焦点。
- 支持文字、URL、Markdown/TXT/PDF/HTML、图片/截图；文件/图片支持选择、拖入和粘贴。
- 用户不必先填正式标题、目录或 Topic；系统生成的临时名称可编辑但不改变原始输入。
- 原始输入、来源类型、捕获时间和 Provenance 不可变；后续抓取、OCR、摘要或整理不得覆盖原件。
- 文字和文件/图片先可靠创建 Source、Content Artifact 和 Source Version，再启动异步处理。
- URL 先创建 Source 和持久 Capture 状态；保留原始 URL。抓取成功后创建 HTML Source Version，失败时 Source 仍在 Inbox 且可重试。
- URL 抓取必须限制协议、DNS/私网、重定向、响应大小、媒体类型和超时。
- 图片先保存原图；只有视觉/OCR 能力可用时才异步提取可检索内容。能力不可用或失败时显示“仅保存原图”，不得伪造解析或画像完成。

### Automatic Processing

- Capture 成功后自动启动有持久状态的 Ingestion、基础全文索引和可用时的向量索引；用户不需要再手动逐条发起。
- 解析、索引、模型能力和 Profile 各自显示状态和稳定错误；单层失败不得把其他已完成层降成假失败。
- 模型/Embedding 不可用时 Keyword 和已完成解析继续可用，并显示明确降级。

### Document Knowledge Profile

- 基础解析完成后自动生成绑定 Source Version 的 Profile，至少包含摘要、候选 Topic、术语与别名、关键知识点、关键示例和 Source Span 引用。
- Profile 是可重建候选投影，不得写入正式 Topic、Claim 或 Relation，也不得把模型输出标为已批准知识。
- Profile 冻结 Source Version、Parse/Index、Prompt、Model/Settings 和 Schema 版本；依赖变化后旧 Profile 保留并标记 stale 或创建新 Revision。
- Profile 支持 READY、FAILED、CAPABILITY_UNAVAILABLE、STALE 等可观察状态和幂等重试。
- 正式 Claim 抽取、关系比较、冲突判断和 Proposal 不在自动 Profile 阶段运行。

### Inbox And Detail

- Inbox 展示 Capture/Source 级别记录，覆盖无 Source Version 的 URL 抓取失败，并兼容既有 Workspace Scan 来源。
- 详情页展示原始来源、处理阶段、Profile、候选与可打开 Evidence；候选和正式知识必须有明显语义区分。
- 所有刷新恢复以服务端状态为准；SSE 只触发 Query invalidation。

## Acceptance Criteria

- [x] 从任意业务页面记录文字、URL、文件或图片后可回到原上下文，Inbox 立即出现真实记录。
- [x] URL 抓取失败时原始 URL 和 Source 不丢失，可查看失败原因并用相同 Capture 身份重试。
- [x] 图片无视觉/OCR 能力时原图可靠保存，状态显示“仅保存原图”，没有虚假正文、索引或 Profile。
- [x] 支持类型的资料自动完成解析与基础索引；Embedding 不可用时 Keyword 仍可检索且显示降级。
- [x] READY Profile 含摘要、候选 Topic、别名、关键知识点、示例和可打开 Source Span。
- [x] Profile 失败不阻断基础检索；失败、能力不可用、stale 和重试状态刷新后可恢复。
- [x] Profile 候选不会出现在正式 Topic/Claim/Relation 查询中，摄取不会自动创建 Proposal。
- [x] 相同幂等命令重试不会创建重复 Capture、Source、Source Version、Artifact 或 Profile Revision。
- [x] 桌面和 `390x844` 下 Quick Capture、Inbox、详情状态无重叠、横向溢出，并可键盘操作。

## Out Of Scope

- 录音、语音转写和操作系统全局快捷键。
- 自动确认正式 Topic、Claim、Relation 或 Proposal。
- 无视觉/OCR 能力时在客户端伪造图片文本。
- 通用网页爬虫、登录态网页抓取和浏览器扩展。
