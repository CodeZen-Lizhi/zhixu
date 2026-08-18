# TODO 8 实施计划

## 执行规则

- 用户已批准全量实现；下列阶段均已按依赖顺序完成并通过最终门禁。
- 实现按下列依赖顺序推进。每一批必须先完成生成/Transport 基础，再完成 API owner、定向测试和门禁，
  不允许让普通 JSON 长期停留在双路径。
- 每次生成都从权威 `api/openapi/openapi.json` 开始，先跑现有契约门禁，再跑 normalizer、generator、
  严格编译和 drift check。生成目录禁止手改。

## 阶段与依赖

### 0. 契约与工具基线

- [x] 为全部 189 个 operation 建立 tag catalog 和 operation-to-tag manifest，补齐缺失 tag/description，
      重新启用 Spectral `operation-tags`，并让 project/route/oasdiff gate 继续通过。
- [x] 将 `@openapitools/openapi-generator-cli@2.40.1`、generator `7.24.0` 配置、normalizer、
      `runtime.mustache` 补丁和 `typescript-fetch` 生成参数写入 `api/openapi` manifest、lock/config。
- [x] 增加清理生成、生成、检查漂移、升级说明和 CI/Makefile 入口；记录 normalizer 的每种允许变换，
      对未知 schema 失败。
- [x] 将 `zod@4.4.3` 加入 `web` manifest/lock；增加 generated 目录的只读约束、lint 例外和 strict
      compile gate，验证公共模型没有 `any` 泄漏。

依赖：无。验收：AC1、AC1a、AC2，且重复生成无 diff。

### 1. 共享 Transport 与安全基线

- [x] 建立生成 `Configuration`、Transport fetch、Problem/error mapping、Abort/timeout 和安全 header
      的唯一实现，保留现有 401 invalidation 与 Workspace owner 语义。
- [x] 迁移 `auth.ts`、`active-workspace.ts`、`workspace.ts`、`system-status.ts`，删除其中重复的
      普通 JSON request/DTO 路径；保持 revoke 204 和 request id 行为。
- [x] 定向覆盖 Cookie/API Token、CSRF、Workspace binding、401、Abort、timeout、Problem redaction。

依赖：阶段 0。验收：AC3、AC5。

### 2. 采集与编写

- [x] 迁移 `captures.ts`、`authoring.ts`、`document-history.ts`、`source-spans.ts`。
- [x] 使用生成 multipart operation，保留必要的 FormData/CSRF/进度 Adapter；严格正文和证据字段只在
      API owner 解码一次。
- [x] 验证上传失败、取消、空正文、Problem 和版本冲突；删除普通 JSON 手写 Transport/DTO。

依赖：阶段 1。验收：AC4、AC5。

### 3. 知识发现

- [x] 迁移 `search.ts`、`graph.ts`、`semantic-links.ts`、`collections.ts`、`health.ts`。
- [x] 保留 Search/Graph 既有 domain projection、cursor 不透明性、URL canonicalization、Evidence lazy
      cache 清理和 workspace query key。
- [x] 为 Graph/Search/Collection/Health 的高风险联合、数值/UUID/RFC3339、边界集合和 invalid cache
      增加 Zod/strict fixture，未知语义 fail closed。

依赖：阶段 1；Graph/Search 共享同一 Transport，但各自 API owner 不互相导入 decoder。验收：AC5、AC6、AC7。

### 4. 工作流与变更控制

- [x] 迁移 `business.ts`、`business-revisions.ts`、`conversation.ts`、`timeline.ts`。
- [x] 为 Workflow/Proposal/Timeline/Conversation 判别联合建立 Zod/strict owner；保留版本、ETag、
      Idempotency-Key、SSE 只做 invalidation 的既有边界。
- [x] 覆盖 accepted/failed/conflict/cancel/retry 和迟到响应，不把服务端状态机搬到组件。

依赖：阶段 1；阶段 0 的 operation tags 必须稳定。验收：AC3、AC5、AC6、AC7。

### 5. 运维与设置

- [x] 迁移 `organizing.ts`、`git-sync.ts`、`model-settings.ts`。
- [x] 为任务状态和 Model Settings participant/activation 联合保留严格不变量；secret 仅用于请求，
      不进入 Query、URL、Storage、DOM、日志或错误。
- [x] 覆盖 202 polling、409、失败重试、Abort、已保存未确认和刷新恢复。

依赖：阶段 1；Model Settings 复用既有安全质量规范。验收：AC3、AC6、AC7。

### 6. 学习、审核与输出

- [x] 迁移 `review.ts`、`interview.ts`、`memory.ts`、`artifacts.ts`、`exports.ts`、
      `attachment-exports.ts`。
- [x] 普通 JSON 使用生成 API；高风险 Review/Interview/Memory 联合保留既有 strict owner，共享
      Problem/Auth 使用 Zod；Markdown/JSON/ZIP
      下载保留最小 Blob/文件名 Adapter；204 只保留 Void response 映射。
- [x] 覆盖下载媒体类型、大小/文件名安全、取消、错误映射以及缺字段/未知联合值 fail closed。

依赖：阶段 1；输出 Adapter 不得复制通用 Transport。验收：AC4、AC5、AC6。

### 7. 全量收口与文档

- [x] 记录 `server-events.ts`、`answer-draft-stream.ts` 与生成 operation 的对应关系，确认 SSE/流式
      仍只有专用 owner；清除所有普通 JSON 旧路径、未使用 DTO 和未归类 decoder。
- [x] 逐项更新 26 模块迁移矩阵：生成 API、Domain owner、Zod/strict owner、保留 Adapter、测试和最终状态。
- [x] 执行 OpenAPI gate、生成 drift、web lint/typecheck/test/build、关键 Playwright smoke 和 `git diff --check`。
- [x] 更新 `docs/roadmap.md`、前端 `type-safety`/quality 文档、应用契约和 generator 升级说明；只有
      全部 AC 通过才把 TODO 8 标为完成。

依赖：阶段 2-6 全部完成。验收：AC1-AC9。

## 关键验证命令

实现阶段实际执行并记录输出，不提前宣称通过：

```bash
make openapi-install
make openapi-check
make openapi-generate
make openapi-generate-check
npm ci --prefix web
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
git diff --check
```

涉及模块时先运行对应 Vitest/API fixture，再运行全局前端门禁；浏览器 smoke 只在用户要求的页面验证
阶段启动本地服务并实际检查桌面、移动、控制台和网络请求。

## 实际验收结果（2026-08-19）

- `make openapi-install` 与 `npm ci --prefix web` 从 lockfile 清理安装成功；OpenAPI 工具依赖审计为 0。
- `make openapi-generate-check` 通过 Spectral、项目契约、Gin 路由、189 operation tag、29 条 generator
  warning 基线、确定性 drift 与 generated strict typecheck。
- `npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run test --prefix web` 和
  `npm run build --prefix web` 通过；Vitest 为 109 个文件、1,233 项用例。
- 真实 Chromium 本地确定性 smoke 验证生成 Search 请求的 Cookie/CSRF、Workspace A→B URL/cache
  收敛、迟到请求取消和严格联合页面；控制台 0 error、0 warning，mock server 记录旧请求已中止。
- `git diff --check` 与 Trellis task validation 通过；独立 Review 覆盖生成配置、Transport、安全、
  Abort、领域边界和专用媒体路径，发现的 fetch/body-read Abort identity 问题均已修复并回归。

## 完成前审查清单

- [x] 已按文件清单审查本任务 diff；工作树既有的无关改动保持原样，未被覆盖。
- [x] 生成目录没有手工业务逻辑，normalizer 变换有报告，manifest/lock/template 版本一致。
- [x] 所有普通 JSON API 每模块只有一个 production owner；SSE、stream、multipart、Blob 的保留边界
      均为最小且有测试的 Adapter。
- [x] wire/domain/Zod/Store/Component 边界清晰，未出现 `any`、无依据的类型断言、静默 fallback 或
      敏感信息泄露。
- [x] Review skill 覆盖生成配置、TypeScript 边界、错误/安全、缓存和媒体传输；发现的当前范围缺陷先
      修复再交付。
