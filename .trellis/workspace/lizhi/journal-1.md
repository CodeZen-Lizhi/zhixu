# Journal - lizhi (Part 1)

> AI development session journal
> Started: 2026-07-16

---



## Session 1: M1 可运行骨架交付

**Date**: 2026-07-16
**Task**: M1 可运行骨架交付
**Branch**: `dev`

### Summary

完成 Go API/Worker、React Web、PostgreSQL+pgvector、OpenAPI、Compose、CI 与真实烟测；修复 PostgreSQL 18 数据目录、错误分类、405 Problem、DSN 密码转义、迁移幂等和请求日志，并归档 M1。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `2e5610a` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 2: M2 Eino 隔离采用门禁

**Date**: 2026-07-16
**Task**: M2 Eino 隔离采用门禁
**Branch**: `dev`

### Summary

完成 Eino Chat Graph、ToolsNode、Callback、权限/错误/限流/脱敏 PoC；独立 Review 修复副作用误重试和 Secret 泄漏，并对未经过真实 Eino/River/Provider 的门禁明确判定 FAIL/SKIP，最终不正式采用 Eino。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `91b42ce` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 3: 完成 M3 Workspace 事实源闭环

**Date**: 2026-07-16
**Task**: 完成 M3 Workspace 事实源闭环
**Branch**: `dev`

### Summary

完成 Workspace/Source/SourceVersion PostgreSQL 迁移与幂等仓储、Git/文件安全适配器、Create/Open/Scan 应用服务、REST/OpenAPI、React Workspace 页面和真实 Compose 业务烟测；修复运行镜像缺少 Git CLI，并验证重复扫描复用版本、同路径变更创建新版本和 Source Version 不可变。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `3546ef1` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 4: 完成 M4 Workflow 持久化闭环

**Date**: 2026-07-16
**Task**: 完成 M4 Workflow 持久化闭环
**Branch**: `dev`

### Summary

完成 Workflow Definition/Run/Node/Human Task/Outbox 持久化、合法状态、租约与过期回收、节点幂等完成、人工任务版本校验和跨 Workspace 事件作用域校验；接入 202 Run API、查询和 Human Decision API，加入 Idempotency-Key，完成真实 Compose smoke 和独立 PostgreSQL race 集成测试。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `fa5f7a7` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 5: 完成 M5 Proposal 审批与安全预检

**Date**: 2026-07-16
**Task**: 完成 M5 Proposal 审批与安全预检
**Branch**: `dev`

### Summary

实现 Proposal/Revision/Approval 持久化、版本化 Change Hash、服务端真实文件基线预检、稳定 HTTP/OpenAPI 契约和 Compose/PostgreSQL 烟测；未执行正式文件写回。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `315494e` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 6: 加固 M5 幂等与失效状态

**Date**: 2026-07-16
**Task**: 加固 M5 幂等与失效状态
**Branch**: `dev`

### Summary

补齐 Proposal 创建 Idempotency-Key 持久化与重放、审批相同决定重放、审批前真实基线检查，以及目标不可用转 needs_revision；完成 PostgreSQL、Compose 和 API 烟测。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `abc9f5a` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 7: 完成 M5 摄取解析与规范化分块

**Date**: 2026-07-17
**Task**: 完成 M5 摄取解析与规范化分块
**Branch**: `dev`

### Summary

实现不可变 Artifact 两阶段读取、Ingestion Attempt 状态机、Markdown/TXT Parser、Source Span、Canonical Chunk、API/Workflow Adapter 与 PostgreSQL 约束；通过 race、vet、OpenAPI、真实 PostgreSQL、Compose 和 API 幂等烟测。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `2a8e681` | (see git log) |
| `584ec5d` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete


## Session 8: M5 Write Authorization

**Date**: 2026-07-17
**Task**: M5 Write Authorization
**Branch**: `dev`

### Summary

实现服务端短期 Write Authorization：审批与 Workflow 严格绑定、数据库可信时钟、过期/撤销、完整绑定前置校验、并发一次性幂等消费和 PostgreSQL 约束；明确 M5-04 在文件写入点执行最终 Target Version CAS。完成真实 PostgreSQL、全仓 race/vet、Web/Eino、OpenAPI、Compose build/readiness smoke 与 Go Quality Gate。

### Main Changes

- Detailed change bullets were not supplied; see the summary above.

### Git Commits

| Hash | Message |
|------|---------|
| `87ea888` | (see git log) |

### Testing

- Validation was not recorded for this session.

### Status

[OK] **Completed**

### Next Steps

- None - task complete
