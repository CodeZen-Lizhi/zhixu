# OpenAPI 契约门禁实施计划

## Phase 1. 修复并冻结候选契约

- [x] 以结构化脚本枚举 protected/public/session/bootstrap operation，生成并人工复核缺失 `401`、适用 `403` 和项目约定 `405` 的精确列表。
- [x] 修复两个 Media Type Object、冗余 `items: false`、tab、同源 server 和两个 discriminator mapping；同步 `check.mjs` 精确断言。
- [x] 补齐已验证的 response 文档缺口，并把 response policy 收敛成通用结构化 checker，避免继续维护不完整手工 operation 子集。
- [x] 为 Answer Draft SSE 增加 generation/cursor/chunk-reset-end/cache/recovery 项目断言；不改变 SSE wire。
- [x] 保持 Capability extension 当前单值/运行时多值边界，不在本任务设计新 wire extension。

验证：

```bash
node api/openapi/check.mjs
GIN_MODE=test go test -count=1 -timeout 60s ./internal/app -run 'RouterInventory|OpenAPI|Route'
git diff --check
```

回滚点：OpenAPI 只描述现有行为；若某 response 与 runtime policy 证据冲突，停止该项并保留研究记录，不猜测产品语义。

## Phase 2. 接入固定 Spectral 门禁

- [x] 新增独立 `api/openapi/package.json`/lockfile，精确锁定 Spectral 6.16.3 及 resolver 所需直接依赖，关闭 Scarf analytics。
- [x] 新增 `.spectral.yaml`，继承官方 OAS ruleset，逐项记录纯风格关闭理由，并对 Example false positive 使用 unresolved 官方 rule。
- [x] 使用官方 resolver 扩展点实现 internal-only `$ref`；拒绝 HTTP/HTTPS/file/父目录/未知协议且不发网络。
- [x] 新增 `openapi-install`、`openapi-lint`、`openapi-project-check` target，并让 `openapi-check` 先 lint 后运行项目 checker。
- [x] 更新 `.dockerignore` 和 CI npm cache/install/audit，使 API 工具依赖不进入 Web runtime 或 Docker build context。

验证：

```bash
make openapi-install
make openapi-lint
npm audit --prefix api/openapi --audit-level=high
```

回滚点：先撤销 Spectral target/ruleset，现有项目 checker 仍是完整 fail-closed 入口。

## Phase 3. 接入固定 oasdiff 与 Git base

- [x] 新增唯一 POSIX wrapper，固定 oasdiff v1.29.1 multi-arch digest，验证 Docker/版本/base/candidate，使用受限容器和 trap 清理临时目录。
- [x] 固定 WARN+ERR、外部引用关闭、path parameter、`--deprecation-days-stable=180`、`--deprecation-days-beta=180` 和英文稳定输出；不启用 flatten、validate、upload、fetch 或 ignore。
- [x] 增加严格的旧 base bootstrap 规范化，只处理已证明冗余的四元素 tuple `items=false`；形状漂移或其他 boolean `items` schema fail closed，候选不处理。
- [x] 新增 `openapi-breaking-check`，只接受显式 40 位 `OPENAPI_BASE_REVISION` SHA，拒绝 option/缩写/全零值并从 Git blob 提取基线。
- [x] CI checkout 使用完整历史；PR 传 base SHA，`main`/`dev` push 传 before SHA，空值/全零/不可读均失败。
- [x] 记录 intentional break 的 owner 审批/受保护分支 bypass 流程及 branch protection 外部核验项。

验证：

```bash
bash -n api/openapi/breaking-check.sh
make openapi-breaking-check OPENAPI_BASE_REVISION="$(git rev-parse HEAD)"
```

回滚点：移除独立 breaking step 不影响普通 `openapi-check`；不得用空 base 或成功退出替代回滚。

## Phase 4. 保守收敛现有 checker

- [x] 保留 handler/capability source regex；runtime inventory 不足以证明具体 handler 绑定。
- [x] 逐项审计 raw marker 与通用检查；只有成熟工具和现有测试/结构化断言已经覆盖缺失与重复时才局部删除。
- [x] 不新增 mutation fixture、测试文件、测试专用注入入口或临时测试代码，不为删除行数制造测试重构。
- [x] 不在本任务批量删除 operation/status/schema/领域断言，不做无关 checker 重构。
- [x] 新增 `openapi-route-check`，让 canonical target 准确包含现有 Router inventory 测试。

验证：

```bash
make openapi-project-check
make openapi-route-check
make openapi-check
```

回滚点：任何局部删除缺少现有等价覆盖时恢复旧断言，新成熟工具仍可保留为并行门禁。

## Phase 5. 文档、规范与 CI 收口

- [x] 新增 ADR-0028 和 ADR index；记录 ADR-0019 覆盖、版本/许可证、隔离、升级和退出路径。
- [x] 更新 README、CONTRIBUTING、operations，给出安装、lint、breaking、批准破坏、离线镜像与排障命令。
- [x] 更新 backend HTTP/quality 与 frontend type-safety spec，删除 183 operation 漂移并固化四类 owner。
- [x] CI 依次安装工具、跑当前规范门禁和 event base breaking gate，日志保留可定位诊断。
- [x] 所有验收通过后更新 roadmap TODO 7 状态；不提前标记 TODO 8 或生成客户端已完成。

## Phase 6. 最终验证与审查

```bash
make openapi-install
make openapi-lint
make openapi-project-check
make openapi-route-check
make openapi-breaking-check OPENAPI_BASE_REVISION="$(git rev-parse dev)"
make openapi-check
GIN_MODE=test go test -count=1 -timeout 60s ./internal/app -run 'RouterInventory|OpenAPI|Route'
npm audit --prefix api/openapi --audit-level=high
git diff --check
```

- [x] 使用 `code-review-and-quality` 做常规跨层审查；若修改 Go 测试，再追加轻量 `go-review`。
- [x] 核对 manifest/lock、Apache-2.0、镜像 digest、遥测关闭、无远程 `$ref`、无 ignore/baseline snapshot 和 CI/base fail-closed。
- [x] 把 AC1-AC8 映射到实际命令和现有测试输出；未能验证的 mutation 场景与 branch protection 只列为验证盲区/外部发布项，不伪装通过。

## Expected Files

| Area | Expected files |
| --- | --- |
| OpenAPI contract | `api/openapi/openapi.json`, `api/openapi/check.mjs` |
| Spectral | `api/openapi/package.json`, `api/openapi/package-lock.json`, `api/openapi/.spectral.yaml`, local resolver |
| oasdiff | `api/openapi/breaking-check.sh`, optional pinned config |
| Build/CI | `Makefile`, `.github/workflows/ci.yml`, `.dockerignore` |
| Architecture/docs | ADR-0028/index, `README.md`, `CONTRIBUTING.md`, `docs/operations.md`, `docs/roadmap.md` |
| Stable specs | backend HTTP/quality and frontend type-safety documents |

## Review Gates

- [x] Contract: OAS 3.1 validity、direction-aware compatibility、Auth/Capability/Problem/SSE/strict response owners 没有空洞或双事实源。
- [x] Security: PR 输入不能触发外部 `$ref` 网络访问；Docker 无网络、只读挂载；Git revision 和 shell 参数不发生 option/command injection。
- [x] Reproducibility: exact npm lock、image digest、同一 Make target、稳定 locale/color、缺少依赖/base 时 fail closed。
- [x] Compatibility: 当前 runtime route/response 语义不变；有意 breaking 需要显式审批，不能覆盖 snapshot 或扩大 ignore。
- [x] Maintainability: 只删除已有等价覆盖的断言；项目 checker 保留领域语义，工具 wrapper 保持薄且可替换。
