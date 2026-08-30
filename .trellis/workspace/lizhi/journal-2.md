# Journal - lizhi (Part 2)

> Continuation from `journal-1.md` (archived at ~2000 lines)
> Started: 2026-08-11

---



## Session 51: 收敛项目文档体系

**Date**: 2026-08-11
**Task**: 收敛项目文档体系
**Branch**: `dev`

### Summary

将 docs 从迁移前 66 份收敛为 32 份，建立用户指南、需求、架构、路线图、运维和 ADR 六类事实源；依据代码审计同步 M9-M11 与架构质量状态，并新增活跃任务上下文和 children done 防漂移门禁。

### Git Commits

| Hash | Message |
|------|---------|
| `49e09138` | (see git log) |

### Status

[OK] **Completed**


## Session 52: 修复 Docker 重启后的运行时恢复

**Date**: 2026-08-11
**Task**: 修复 Docker 重启后的运行时恢复
**Branch**: `dev`

### Summary

新增 PostgreSQL runtime wait，修复共享网络命名空间启动竞态；status 显示全部关键容器和 ready/degraded；完成真实 restart、readiness 与契约验证。

### Git Commits

| Hash | Message |
|------|---------|
| `540f123e` | (see git log) |

### Status

[OK] **Completed**


## Session 53: 完善模型连接测试诊断与协议探针

**Date**: 2026-08-11
**Task**: 完善模型连接测试诊断与协议探针
**Branch**: `dev`

### Summary

为 OpenAI-compatible Chat/Responses 与 Embedding 连接测试增加最小探针、真实 Provider/网络诊断、显式接口样式和安全前端展示；完成回归门禁与真实页面复测。

### Git Commits

| Hash | Message |
|------|---------|
| `ba06d590` | (see git log) |

### Status

[OK] **Completed**


## Session 54: 修复 Docker 项目级重启

**Date**: 2026-08-11
**Task**: 修复 Docker 项目级重启
**Branch**: `dev`

### Summary

以独立 helper Compose 提供稳定网络命名空间 anchor，修复 Docker Desktop 项目级 Restart 的旧 owner 引用失效，并补齐生命周期、安全与回归验证。

### Git Commits

| Hash | Message |
|------|---------|
| `19fdaae2` | (see git log) |

### Status

[OK] **Completed**


## Session 55: 模型运行时热切换与 Gin 路由迁移

**Date**: 2026-08-11
**Task**: 模型运行时热切换与 Gin 路由迁移
**Branch**: `dev`

### Summary

实现 API/Worker 进程内模型 generation 热切换、持久激活状态机、Workflow/Retrieval 版本绑定和保存并应用界面，并将 HTTP 路由统一迁移到 Gin。

### Main Changes

- 模型配置保存后可由 API 与 Worker 原地预检、切换并收敛，无需重启容器。
- Workflow、检索与重建任务按持久 revision/provenance 获取对应 generation，保护在途任务。
- HTTP 路由迁移到 Gin，并同步认证、OpenAPI、前端状态与运维文档。

### Git Commits

| Hash | Message |
|------|---------|
| `9bb5b939d84fe52993a549216a36e6de831c2f71` | (see git log) |

### Testing

- [OK] Go race/vet、真实 PostgreSQL 迁移与仓储集成、OpenAPI、前端 lint/typecheck/build 均通过。
- [OK] 隔离 Compose 热切换烟测通过，API/Worker 容器 ID、StartedAt 与 RestartCount 保持不变。

### Status

[OK] **Completed**


## Session 56: 同步模型热切换与 Gin 文档

**Date**: 2026-08-12
**Task**: 同步模型热切换与 Gin 文档
**Branch**: `dev`

### Summary

收口无容器重启模型热切换与 Gin 迁移的长期文档、Trellis 规范、归档证据和 smoke 入口。

### Main Changes

- 将模型设置 Save/Apply、desired/active/applied、短 fence 与故障恢复同步到 README、用户指南和运行手册。
- 同步 Gin 当前基线、legacy River/config 边界，并修复热切换归档 manifest 与结果摘要。

### Git Commits

| Hash | Message |
|------|---------|
| `ab6029c4` | (see git log) |

### Testing

- [OK] 通过 task context、OpenAPI、vendor 解析、Markdown 链接、Makefile/script 和 diff 检查；未运行重型 Compose smoke。

### Status

[OK] **Completed**

### Next Steps

- Gin 任务仍需处理测试 import 并完成全仓、Compose 和浏览器门禁。


## Session 57: 完成 Chi 到 Gin HTTP 迁移收口

**Date**: 2026-08-14
**Task**: 完成 Chi 到 Gin HTTP 迁移收口
**Branch**: `dev`

### Summary

完成唯一 Gin Engine、标准库 Handler bridge、全部领域路由迁移、OpenAPI/runtime 183 operation 对等、Chi 清理、SSE/recovery 边界修复与 Trellis 归档。

### Main Changes

- 以 gin.New 建立唯一生产 HTTP Composition Root，保留严格 JSON、Problem、认证、安全、SSE、上传下载和静态 fallback 契约。
- 修复不可比较/typed-nil panic recovery、底层 Flusher 检查，并让 Application integration smoke 通过 app.NewRouter 走公开路由。
- 新增 Gin HTTP 边界长期规范和完整任务 outcome/回滚/验证记录。

### Git Commits

| Hash | Message |
|------|---------|
| `9bb5b939` | (see git log) |
| `ab6029c4` | (see git log) |
| `39bf654e` | (see git log) |
| `4a90ee9` | (see git log) |

### Testing

- [OK] 受影响 HTTP/app/cmd 包普通测试与 race 通过；全仓 race/vet、API build、OpenAPI、tidy、vendor 解析通过。
- [OK] Compose 子合同与独立 Trellis review 通过；已记录既有 config/gitcli/launcher、数据库 URL 和浏览器验证边界。

### Status

[OK] **Completed**

### Next Steps

- 继续处理独立的 Managed Ollama lifecycle 任务；不要把 TODO 11 gin-contrib/sessions 误认为本任务已采用。


## Session 58: 暂停 TODO 11 Session 框架评估

**Date**: 2026-08-14
**Task**: 暂停 TODO 11 Session 框架评估
**Branch**: `dev`

### Summary

完成 gin-contrib/sessions v1.1.0 的现状审计、强制约束和 6/100 二元覆盖矩阵；用户决定暂不继续，因此未启动实施、未归档任务、未勾选 TODO 11。

### Main Changes

- 冻结 PRD、技术设计、实施计划与两份研究证据，结论为当前不应采用 gin-contrib/sessions。
- 保留任务为 planning，并清除本会话当前任务指针；没有修改生产代码或运行时契约。

### Git Commits

(No commits - planning session)

### Testing

- [OK] Trellis 任务上下文校验通过，矩阵权重 100、得分 6，任务目录 git diff --check 通过。

### Status

[OK] **Completed**

### Next Steps

- 需要时重新激活 08-14-gin-contrib-sessions-evaluation；实施前须明确确认 TODO 10 从关闭前置改为未来重评条件。


## Session 59: 完成受管 Ollama 生命周期与工作区重绑定

**Date**: 2026-08-14
**Task**: 完成受管 Ollama 生命周期与工作区重绑定
**Branch**: `dev`

### Summary

交付按模型设置启停的 managed Ollama supervisor、持久模型卷与生命周期状态，并补齐显式 Workspace root rebind、文档和受控 Compose 验收证据。

### Main Changes

- 新增常驻低内存 local-model-runtime 管理器，按需求启动或停止唯一 Ollama child，并持久化 operation、hold、进度和恢复状态。
- 接入 Chat/Embedding 本地 Ollama 设置、热激活、generation hold、OpenAPI、Settings UI、Compose 与 legacy 迁移工具。
- 新增显式 workspace rebind 与数据库围栏、审计和 launcher 恢复契约。

### Git Commits

| Hash | Message |
|------|---------|
| `e42790f0` | (see git log) |

### Testing

- [OK] 两组指定真实 Compose 验收通过：五种线上/本地模式、单 child、缓存复用、管理容器重启恢复和 60 秒内存采样。
- [OK] 文档 Markdown 解析 7/7、git diff --check 与暂存区密钥模式检查通过。

### Status

[OK] **Completed**

### Next Steps

- 原生 Linux、Docker daemon restart、浏览器闭环和真实 legacy volume 迁移仍按归档任务文档列为发布门禁。


## Session 60: Eino Runtime 生产迁移与路线图收口

**Date**: 2026-08-14
**Task**: Eino Runtime 生产迁移与路线图收口
**Branch**: `dev`

### Summary

Eino/eino-ext 已成为生产唯一 AI Runtime，旧 direct 实现已删除并合并到 dev；路线图与需求优化清单状态已校准。按用户明确决定，真实 7 天/100 终态稳定观察不再阻塞本任务归档。

### Main Changes

- 归档 Eino Runtime 父任务及 Checkpoint、Embedding、RAG、Token Stream、Tool Agent 五个子任务。
- 保留未执行稳定观察的事实记录，不将其误写为已通过。

### Git Commits

| Hash | Message |
|------|---------|
| `64748d92` | (see git log) |
| `5e5c44cf` | (see git log) |
| `3acc760b` | (see git log) |

### Testing

- [OK] 合并前全仓 Go、race、vet、前端、OpenAPI、Compose、Provider/browser 与 Trellis 门禁证据通过。
- [OK] 路线图链接、Markdown 锚点、前端 lint/typecheck 与 git diff --check 通过。

### Status

[OK] **Completed**


## Session 61: Proposal Revision 三方合并交付

**Date**: 2026-08-15
**Task**: Proposal Revision 三方合并交付
**Branch**: `dev`

### Summary

完成 Proposal Revision 不可变历史、服务端三方合并、并发与授权围栏、Workflow 取消协调、HTTP/OpenAPI 及前端编辑工作台；相关 Go 测试、前端类型检查与 OpenAPI 契约检查通过，真实 PostgreSQL 验证因测试数据库不可用未执行。

### Git Commits

| Hash | Message |
|------|---------|
| `b13a183e` | (see git log) |

### Status

[OK] **Completed**


## Session 62: 分离 Docker 初始化容器与稳态项目

**Date**: 2026-08-15
**Task**: 分离 Docker 初始化容器与稳态项目
**Branch**: `dev`

### Summary

将一次性初始化、迁移与 modelctl 从主 Compose 拆到 launcher-only bootstrap 模型，Docker Desktop 稳态项目仅保留六个长期服务。

### Main Changes

- 新增独立 bootstrap Compose，并收敛 launcher、Makefile 与 smoke 入口
- 补齐稳态/bootstrap 服务集合、Workspace grant 隔离与失败阻断契约
- 同步运行手册、架构 ADR 与 Trellis 长期规范

### Git Commits

| Hash | Message |
|------|---------|
| `2d04684` | (see git log) |

### Testing

- [OK] Compose runtime/workspace/smoke 契约通过
- [OK] 相关 Go test、go vet、Shell/Python 语法和 git diff 检查通过

### Status

[OK] **Completed**

### Next Steps

- 下次执行 ./zhixu up 或 restart 时清理旧版 exited one-shot 容器


## Session 63: 交付 OpenAPI 契约门禁

**Date**: 2026-08-18
**Task**: 交付 OpenAPI 契约门禁
**Branch**: `dev`

### Summary

锁定 Spectral 与 oasdiff，补齐 OpenAPI 通用错误响应、SSE 和 discriminator 契约，接入 Makefile/CI，并完成文档、规范与独立审查。

### Git Commits

| Hash | Message |
|------|---------|
| `54c24fce` | (see git log) |

### Status

[OK] **Completed**


## Session 64: 完成 TODO 8 前端 OpenAPI 客户端与 Zod 接入

**Date**: 2026-08-19
**Task**: 完成 TODO 8 前端 OpenAPI 客户端与 Zod 接入
**Branch**: `dev`

### Summary

完成 189 个 OpenAPI operation 的生成客户端与 26 个生产 API 模块全量迁移，保留共享 Transport、严格响应边界及专用流与文件 Adapter；OpenAPI 漂移、前端全量门禁和 Chromium smoke 通过，并同步需求优化清单。

### Git Commits

| Hash | Message |
|------|---------|
| `75e9c569` | (see git log) |
| `22dcd506` | (see git log) |

### Status

[OK] **Completed**


## Session 65: 完成 Artifact GORM 迁移

**Date**: 2026-08-24
**Task**: 完成 Artifact GORM 迁移
**Branch**: `dev`

### Summary

交付 Artifact staged GORM Repository、Citation Backfill、Section Generation 与 scoped terminal hook；真实 PostgreSQL TODO 9、Go/SQL/Trellis review 和 Final handoff 全部通过，生产 Composition 保持 legacy。

### Git Commits

| Hash | Message |
|------|---------|
| `18cfb8f6` | (see git log) |
| `f803e51f` | (see git log) |
| `eb0c31fb` | (see git log) |
| `99970bfa` | (see git log) |

### Status

[OK] **Completed**


## Session 66: GORM 平台与事务基础验收

**Date**: 2026-08-24
**Task**: GORM 平台与事务基础验收
**Branch**: `dev`

### Summary

完成 GORM 事务基础与真实 PostgreSQL/River 平台级验收；因网络级 COMMIT 响应丢失、目标环境连接预算及业务 owner 门禁未关闭，任务保持 in_progress。

### Main Changes

- 提交共享 GORM 根、opaque transaction scope、同事务 River database/sql producer 与资源生命周期控制。
- 新增真实 PostgreSQL commit/rollback、SQLSTATE、取消 cause、Worker 消费、共享池压力和 ACK 丢失重放验收。

### Git Commits

| Hash | Message |
|------|---------|
| `78b63d0c` | (see git log) |

### Testing

- [OK] 干净 `78b63d0c` 快照下 Foundation focused unit/race/vet、integration 编译、go mod verify 与 git diff --check 通过。
- [OK] 独立 HEAD + 暂存区快照的真实 PostgreSQL integration、integration race 与 River Worker smoke 通过。
- [BLOCKED] 干净 `78b63d0c` 快照执行 `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/migrate` 失败；缺失的 Artifact/Workflow 最小依赖定义仍在主工作树未提交改动中。

### Status

[OK] **Session recorded; task remains in_progress**

### Next Steps

- 在专用数据库代理或目标部署环境补跑网络级 COMMIT response-loss、目标连接预算及业务 owner 错误矩阵门禁。


## Session 67: 完成 TODO9 Testcontainers 数据库集成测试工厂

**Date**: 2026-08-26
**Task**: 完成 TODO9 Testcontainers 数据库集成测试工厂
**Branch**: `dev`

### Summary

交付统一 PostgreSQL/pgvector Testcontainers 工厂，支持 ExternalAdminURL 临时库隔离、Goose/River 迁移、GORM/pgx/River/UoW 共享 Pool、并行隔离、失败清理、脱敏和显式 Fail/Skip 策略；完成定向单测、race、vet、tidy、vendor 与默认/禁用 Ryuk smoke。

### Git Commits

| Hash | Message |
|------|---------|
| `109d2cb4` | (see git log) |
| `39f8c768` | (see git log) |

### Status

[OK] **Completed**

### Next Steps

- TODO10 各模块 child 按需迁移现有 ZHIXU_TEST_DATABASE_URL fixture，继续保持业务回归归属不变。


## Session 68: Trellis 收口检查：TODO9 已推送

**Date**: 2026-08-26
**Task**: Trellis 收口检查：TODO9 已推送
**Branch**: `dev`

### Summary

按 trellis-finish-work 核对当前状态：无 active task；TODO9 已完成提交、归档并推送到 origin/dev。其余工作区脏改属于其他 GORM/Atlas/Trellis 任务，未在本轮处理、提交或归档。

### Git Commits

(No commits - planning session)

### Status

[OK] **Completed**

### Next Steps

- 继续由各自 GORM child 任务推进迁移；归档其他任务前先逐项确认其验收状态。


## Session 69: TODO10 模块真实 PostgreSQL 门禁筛选与归档

**Date**: 2026-08-27
**Task**: TODO10 模块真实 PostgreSQL 门禁筛选与归档
**Branch**: `dev`

### Summary

同步 TODO9 工厂完成事实，按 fixture-only 条件审计 30 个 TODO10 child，并完成可验证模块的 shared Testcontainers legacy/GORM 回归。父任务保持 in_progress。

### Main Changes

- 更新 docs/roadmap.md 与 TODO10 父任务文档，明确 TODO9 工厂已完成但不等同模块回归或生产切换。
- Auth、Audit、DocumentHistory、Memory、Git Sync 已完成各自真实 PostgreSQL 门禁并归档；Events、Collection、LocalModelRuntime、Review Core、Review Interview 因实证缺口或前置阻断保持 in_progress。
- Audit 完成后重新筛选 Workspace，确认其多个外部 DSN/手工建库 fixture 与大范围真实门禁缺口仍不满足 fixture-only。

### Git Commits

(No commits - planning session)

### Testing

- [OK] shared Testcontainers integration/race、局部 go test/go vet、Trellis task validate 与 git diff --check 均按模块执行；未执行全仓测试。

### Status

[OK] **Completed**

### Next Steps

- 继续处理仍有产品实现、跨模块前置或性能证据缺口的 child；TODO3/Final Composition 仍待后续任务。


## Session 70: 完成 TODO3 Atlas 迁移与文档状态同步

**Date**: 2026-08-30
**Task**: 完成 TODO3 Atlas 迁移与文档状态同步
**Branch**: `dev`

### Summary

完成 Goose 到 Atlas 全量迁移、验证与归档，并同步 Roadmap、需求优化清单、架构/运维文档、Trellis 任务状态和开发者会话记录。

### Main Changes

- Atlas 成为唯一 Schema 迁移事实源，91 个历史迁移转换并追加 00092，Goose 运行时和依赖删除。
- 需求优化清单同步 TODO 2/3/9/10/11 的当前状态；TODO3 归档任务补齐 P0 与交付提交元数据。
- 修正 Change Control 与 Knowledge GORM 设计中的旧 TODO3 阻断表述，同时保留各模块自身交付门禁。

### Git Commits

| Hash | Message |
|------|---------|
| `2a581f99` | (see git log) |
| `c7558f0a` | (see git log) |

### Testing

- [OK] PG16/PG18 空库迁移和声明式 drift、9 项接管/续跑矩阵、3 项 advisory-lock、testdb Testcontainers、全仓 build/vet/test/integration compile 均通过。

### Status

[OK] **Completed**

### Next Steps

- TODO10 可在已交付 Testcontainers 与 Atlas 前置上继续模块迁移和最终 Composition/pgx 收口。


## Session 71: 同步 TODO3 Atlas 文档状态并推送

**Date**: 2026-08-30
**Task**: 同步 TODO3 Atlas 文档状态并推送
**Branch**: `dev`

### Summary

核对并补齐项目与 Trellis 文档中的 TODO3、TODO9 和 TODO10 状态，修正活跃 GORM 设计中的过期 Atlas 阻断表述，并将本地 dev 提交推送到 origin/dev。

### Git Commits

| Hash | Message |
|------|---------|
| `beab02bc` | (see git log) |

### Status

[OK] **Completed**
