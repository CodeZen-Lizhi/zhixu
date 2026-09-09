# 2026-09-09 开发集成与本机部署

本记录对应用户已授权的 TODO2/TODO4 开发、必要验证、任务文档核对与本机 Docker 重新部署。本轮开发与本机部署已完成；M11 全产品 E2E、AI Eval、SBOM/发布包继续暂缓。本机原有模型配置仍处于禁用状态，AI 准入限制与部署结果分别记录，不把进程健康等同于现场模型闭环通过。

## 统一集成

- Worker 接入 Synthesis Source-ready dispatcher、真实模型执行器与 NOTE preparation terminal；启动及 managed generation 重建使用同一组持久 owners。
- Workspace Analysis 保留 v1 恢复执行器并接入 v2 动态循环；四个新工具与旧工具并存，工具目录共 15 项。
- API/Worker 热更新使用现有 RuntimeHost 的当前 generation 与 lease，不新增 Models/Host/Pool；安全 terminal/control hooks 失败时不能广告新能力。
- Gin/OpenAPI 路由库存为 201 项，权限与路由精确匹配测试通过。Dispatcher 的固定 RAG v1 同键重放按持久版本恢复。

## 必要验证

- API/Worker 定向构造、Worker 动态注册/预算及 capability 生命周期 race 通过；API managed starter、持久 replay 的 race/vet/build 通过。
- W2 候选生命周期、真实 Git 发布、历史/并发覆盖保护、Authoring 与 SourceReady 实库均通过，见 TODO4 的 `research/synthesis-core-verification.md`。
- W3 九个 PostgreSQL/River 场景通过，包括连续增量、NO_CHANGE、跨 Workspace、语义拒绝、UNKNOWN、lease rescue 和跨 Run 恢复同一 Proposal；见 `research/synthesis-runtime-verification.md`。
- W4 真实发布后的 NOTE preparation、Eino/River、追问、Artifact v2 报告/路径与旧 Claim 回归通过；00097 补齐 v2 DocumentSource 的精确绑定。
- W6 真实生产 Worker + CreateText 连续三次入料 race 通过（20.090s）；生成/语义独立记账，第三篇 NO_CHANGE，未批准零正式文件写回。
- TODO2 数据库成功、无证据拒答、预算第 13 次拒绝、deadline、UNKNOWN、取消/失败与旧 v1 回归已取得正式实库证据；精确范围见 foundation/finalizer 验证记录。
- Web 共享最终套件 112 文件 / 1281 项全部通过；typecheck/lint/build、生成客户端及 OpenAPI 标准检查通过，路由/tag 库存 201 项。新 v2/NOTE 响应产生 OpenAPI oneOf breaking 报告（21 error / 5 warning），没有修改门禁或忽略规则。旧 Claim 响应省略新增可选 discriminator，维持旧投影字段。同步升级范围见 [公共契约升级](public-contract-upgrade.md)。
- TODO4 第三轮真实 Compose 与 Chromium 桌面/390 px 浏览器通过：自动连续三篇、首次批准/Git/Reindex、增量待审、NO_CHANGE/重放、历史来源、冻结已发布版本的 AI 面试/答题/来源/刷新；7 张截图、5 次来源读取、runtime issues 为空。前两轮的 Capture 验收门槛和浏览器定位问题已修复；见 [完整记录](../../archive/2026-09/09-08-evolving-knowledge-notes/research/synthesis-compose-verification.md)。
- TODO2 第四轮真实 Compose 与桌面/390 px 浏览器通过：默认 5 次决策、7 次模型调用、5 次工具调用；扩展场景为 6/8/6，并追加第二次来源读取；预算场景第 13 次决策在新增 ModelCall/reservation/Provider 调用前被拒绝。精确重放、SSE、Token 草稿、Stop 与只读边界通过，见 [完整记录](todo2-v2-compose-verification.md)。
- 三个整栈恢复缺陷已修复：RunStart 与 DecisionReceipt 的数据库时间表示误判，以及已脱敏工具结果重放时错误重算脱敏计数。Tools 保留首次不可重算计数，继续严格核验其他输出、Hash、Schema 与绑定；6 个正例、36 个篡改负例、首次 finalization 漂移、旧 loader、Tools 全包 race/vet 与正式 Schema PostgreSQL 重放均通过。静态模型停用后已有请求的 replay 构造检查也已修复；独立审查无剩余发现。
- 最终 `make persistence-check` 通过 30 个 owner / 1969 个 Go 文件；`make compose-check` 全部通过，包括 launcher contract。两套隔离 Compose 的容器、卷和网络均已按精确项目身份清理。

## Atlas 声明结构

从独立 pgvector PostgreSQL 16 实例执行正式 `cmd/migrate` 至 00099，再以 `pg_dump --schema-only --no-owner --no-privileges` 导出 `atlas/schema.sql`，保留 00080 的数据库级 ACL。只移除 pg_dump 客户端 restrict 元命令；以二进制读写保持 SQL 字面量中的 CR/LF，不作通用换行规范化。

`make atlas-migrate-lint`（99 文件）、hash/validate，以及 `make atlas-schema-drift` 的 Atlas diff 和 PostgreSQL catalog fingerprint 均通过。声明重放使用同一一次性实例的另一个空数据库，未重置用户数据库。00098 在验证时的 SHA-256 为 `3a461f1a3552373f5403953f88c924389728592062ce95a6f9fa54d22f1e5f85`。

一次性 Schema 校验容器 `zhixu-schema-delivery-4e95f535` 及其匿名卷已精确清理；未操作原安装的数据卷。

## 原安装与升级前备份

- 原项目 PostgreSQL/API 已停止、Worker 与两个 relay 不健康；只处理 `zhixu` 的精确容器，未操作其他项目或重启 Docker daemon。
- 已停止 app/worker/relay/local-model-runtime，核对数据库中可运行 Workflow 和 running Attempt 均为 0；保留原 namespace anchors 和全部旧数据卷。
- 启动的是原 `zhixu-postgres-1`，实际数据为 Goose 81（0..81 连续 history）、3 个登记 Workspace。当前选定 Root 是合法 unborn Git，不创建首次 Commit。
- 旧库备份兼容修复的 27 项保护测试通过；实际 `create` 与 `verify` 已通过。选定 Root 归档 9,595 bytes，整库 custom dump 1,583,495 bytes，`pg_restore --list` 通过。
- 备份位于 `.zhixu/backups/2026-09-09-before-delivery-v6xupyyq/`，目录 0700、文件 0600；磁盘 FileVault 已开启。另行保护 `.env`、selection/control identity、grant、模型主密钥、local-model-runtime credentials 与原镜像/卷身份，并保存校验和。
- 备份只覆盖选定 Root 的文件和整个数据库；另外两个登记 Root 的文件不在该包内。未执行数据库还原或跨域一致性演练，不将 `verify` 等同于灾备验收。

## 已完成的部署准备

备份确认后，保留原 `.env` 其他字节，只显式启用 API/Worker 的 Workspace Analysis flags，并设置相同 `config_revision=2`。原安装仍为 managed models、固定 8080、Worker job timeout 15m、lease 2m / heartbeat 30s。

TODO4 已完成 AC1–AC10 开发验收并使用 `task.py archive --no-commit` 归档；M11 留在产品交付父任务。归档引用已修复，没有提交或推送代码。

## 禁用 Git 同步的部署修复

首次现场日志检查发现，未配置 `ZHIXU_GIT_SYNC_KEY_FILE` 时仍不断报告 `GIT_SYNC_INVALID` / `GIT_SYNC_UNAVAILABLE`。真实构造函数返回两个空指针，但在 Worker 生命周期入口转成接口后，原 `== nil` 判断失效，启动了本应关闭的调度循环；错误在数据库或 Git 调用之前产生。

修复限于 `cmd/worker/main.go` 的三个 Git Sync 调度入口，复用既有 `nilLifecycleDependency`。禁用组合直接返回，单组件缺失仍在调用前报错；已配置组件的执行顺序、错误脱敏、独立预算和取消协议保持原行为。

`git_sync_worker_test.go` 新增两个测试、五个子场景，使用真实空密钥构造结果。修复前确定性复现后台误启动、三条错误路径以及部分组合误调用；修复后 `go test -mod=vendor -race ./cmd/worker -run 'Test.*GitSync|Test.*Lifecycle|Test.*WorkerRuntime' -count=1 -timeout=120s` 通过（2.527s），`go vet -mod=vendor ./cmd/worker` 通过。定向 Go 自检覆盖接口空值、closed channel、取消/排空和单组件拒绝，未发现剩余问题。永久 Git Sync 规范已记录此边界。

修复后再次执行受支持的完整重建部署，退出码 0。最终 Worker 的启动及后续日志中，两类 Git Sync 误报均为 0；剩余 `AGENT_WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE` 对应下文已核对的原有禁用模型状态。

## 本机实际部署

`./zhixu restart` 已完成重新构建、前向迁移与启动；首次升级和 Git Sync 修复后的最终重建均退出 0。最终 `./zhixu status` 退出码 0，显示 `Runtime: ready`、选定 Workspace available / grant active；API、Worker、PostgreSQL、local-model-runtime、两个 relay 与两个 namespace anchors 均 healthy。固定入口为 `http://127.0.0.1:8080/`。

| 交付物 | 实际运行镜像 |
| --- | --- |
| API / Web | `sha256:f15ddce436fcf5937a1e4e630191b9254157861fd5d817736389a41a6e464930` |
| Worker | `sha256:1aec641162bb951858f3a9f361ef392a1862740828d5216673124c428a862567` |
| PostgreSQL | `sha256:ac7cb07620a70d091bd1acf8bf50d62978458c95a32f80c848ad621cbcac5da6` |

- 实际库从原 Goose 81 升级到 Atlas `00099`；`atlas_schema_revisions.atlas_schema_revisions` 的 head 完整执行，无未完成或带 error 的 revision，旧 Goose history 已移除。
- 原命名数据卷 `zhixu_zhixu-postgres` 继续挂在 `/var/lib/postgresql/data`；launcher 重建 PostgreSQL 容器属于正常升级，未以容器 ID 是否相同代替数据身份核验。
- 升级前后逐项比较原业务 ID 集合：Workspace 3/3、Proposal 1/1、Source 0/0、SourceVersion 0/0、Claim 0/0、Workflow Run 0/0，全部保留。
- 选定 Workspace ID、canonical root、root fingerprint、grant 与 control identity 均保留；selection 仅由 launcher 刷新 `committed_at` 时间。知识 Root 的 Git 状态与备份一致，仍为合法 unborn、`head=null`，未创建首次 Commit 或清理原文件。
- 原 `.env` 字节完整保留，仅追加三个已授权的 analysis 配置；实际 API/Worker 均读取启用 flag 与 `config_revision=2`。模型主密钥、local-model-runtime 凭据、全部 6 版模型选型与加密凭据逐字节/字段比对一致。

## HTTP 与实际页面

- `/livez`、`/readyz` 均返回 HTTP 200 JSON，分别为 `alive` / `ready`；`/healthz` 是 SPA 回退路径，不作为健康证据。
- 认证后的 system status、active Workspace、模型设置、synthesis notes 与 processing 均为 HTTP 200；所有 system status 模块为 ready，active Workspace 为原身份且 available。笔记与处理记录为空，符合该 Workspace 尚无 Source 的实际状态。
- 独立 Chromium session 加载当前安装的 Web，验证 `/authoring` 的“合成笔记”入口可点击进入 `/authoring/notes`；原有 4 篇草稿仍可列出。笔记页与 `/chat` 均完成 1440×1000 和 390×844 截图目检，页面空状态、导航及布局正常。
- 浏览器 console 为 0 error / 0 warning；创作概览、笔记、处理列表和 Workspace 请求实际返回 200。截图位于 `output/playwright/delivery-{notes,chat}-{desktop,mobile}.png`。
- Git Sync 修复后的最终镜像已再次通过认证 API、原 Workspace/模型状态和真实 Chromium 笔记页核验，console 仍为 0 error / 0 warning。该修复未修改 Web，复用前述桌面/窄屏业务证据。
- 私有部署证据保存在 `.zhixu/delivery-restart-20260909.log`、`delivery-restart-final-20260909.log`、`delivery-final-runtime-verification.json`、`delivery-data-verification.json`、`delivery-api-verification.json`、`delivery-config-verification.json`、`delivery-model-verification.json`；不提交凭据、原库 dump 或浏览器认证状态。

## 模型配置与验证限制

本机 `desired_revision=6`，但 `active_revision=2` 的 Chat / Embedding 都为 `disabled`；revision 6 的先前激活失败为 `MODEL_CHAT_REQUEST_FAILED`。已直接读取升级前 dump 中的模型状态，与当前 `phase=failed`、target 6、version 49 及加密配置逐项比对，确认这些状态在升级前就存在。当前 API/Worker 均已应用 revision 2，RuntimeHost 为 active 且心跳新鲜。

因此当前安装没有有效 Workspace Analysis Worker capability 广告，AI 分析、合成生成与笔记面试尚不能调用模型。这是保留原模型配置后的依赖状态，不能写成“现场 AI 闭环 PASS”，也不能仅凭容器 healthy 忽略它。启用这些能力需要在模型设置中成功应用有效配置；本轮未更换用户模型或触发付费请求。

新功能执行、审批、Git、重放与浏览器闭环的证据来自前述隔离 Compose 的确定性 Provider；它们证明生产代码路径和恢复行为，不代表当前保存的外部 Provider 可用或回答质量达标。未补跑完整 Worker kill / OTLP、全域 RAG Compose、灾备/容量及 M11 矩阵，未执行项不记 PASS。

## 文档与任务结果

原始 12 项优化清单已核对：TODO2 的动态缺口已补齐；TODO4 开发任务已归档；TODO10 随本轮实际部署；TODO11 以 ADR-0030 的不采用决策关闭。原清单、路线图、父任务 PRD/实施表和相关 runbook 已同步。永久规范已覆盖 v1/v2 Analysis、Synthesis 与 NOTE 前后端合同，并补充 Git Sync 禁用组合的接口空值保护；本次模型禁用事实只记录为安装状态，不改写产品契约。

产品父任务继续为 `in_progress`，仅承载暂缓的 M11 最终验收；本次开发与本机部署交付记录为 completed。首次部署时 OpenAPI breaking gate 为 21 error / 5 warning，未获得或使用合入豁免。本轮未 commit、push 或 merge；后续修复见下节，不改写上述首次部署事实。

## 后续 OpenAPI 兼容修复（2026-09-09，尚未重新部署）

用户随后要求处理这 21 个 error / 5 个 warning。已通过十个显式 HTTP v2 operation 隔离动态 Analysis/NOTE 响应，保留 v1 的旧响应；相同固定 base 与 oasdiff v1.29.1 复查为 **0 error / 0 warning**。Router/tag 现为 211 项，生成漂移、受影响 Go race/隔离数据库、Web API 边界和 lint/typecheck/build 均通过，详见 [修复记录](openapi-compatibility-fix.md)。

当前生产 starter 只创建动态分析，因此 v1 新建分析在副作用前返回 `409 CONVERSATION_API_VERSION_UNSUPPORTED`；新建须使用 v2，历史 v1 replay 与 RAG 保留。Web 已切换相应 v2 方法。此处只记录代码结果，未重新运行 launcher/Compose 部署、未修改模型配置；上文 image digest、201 route 和原失败结果仍对应首次部署。
