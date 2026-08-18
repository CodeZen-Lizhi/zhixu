# Research: OpenAPI 工具集成与批准基线

- Query: 审计仓库的包管理器、锁文件、Makefile、CI、分支/发布模型、现有 OpenAPI 检查、Docker/离线约束和依赖/许可证惯例，并给出 Spectral 与 oasdiff 的最小可复现集成方式。
- Scope: mixed
- Date: 2026-08-18

## Findings

### 1. 仓库现状

#### 包管理与构建

- 根模块是 Go `1.25.4`，依赖由 `go.mod` / `go.sum` 与已提交的 `vendor/` 管理（`go.mod:1-5`）。生产 Docker 构建复制 `vendor/`，并对所有 Go 二进制使用 `-mod=vendor`（`deploy/Dockerfile:8-18`）。
- Node manifest 仅在 `web/`：声明 `npm@11.7.0` 和 Node `>=24.18.0`（`web/package.json:1-9`）；`web/package-lock.json` 是 lockfile v3，记录解析版本、integrity 和许可证（`web/package-lock.json:1-5`, `web/package-lock.json:47-52`）。仓库根目录未找到 `package.json` 或第二份 npm lockfile。
- CI 精确安装 Node `24.18.0`，缓存键只包含 `web/package-lock.json`，随后运行 `make web-install`，即 `npm ci --prefix web`（`.github/workflows/ci.yml:41-48`, `Makefile:17-18`）。`packageManager: npm@11.7.0` 当前只是 manifest 声明；workflow 没有显式安装或断言 npm 版本。
- Web Docker build 同样从 `web/package.json` + lockfile 执行 `npm ci`，最终 runtime 只复制 `web/dist`，不复制 `node_modules`（`deploy/Dockerfile:1-6`, `deploy/Dockerfile:48-54`）。因此放入 `web.devDependencies` 的契约 CLI 会增加构建阶段安装量，但不会进入最终运行镜像。
- `.gitignore` 已全局忽略 `node_modules/` 和 `.cache/`（`.gitignore:22-24`, `.gitignore:57-59`）；`.dockerignore` 只显式忽略 `web/node_modules`（`.dockerignore:9-10`）。若另建 `api/openapi/package.json` 或 `tools/openapi/package.json`，还必须同步扩大 Docker ignore，否则本地工具依赖可能进入 Docker build context。

#### 现有 OpenAPI 门禁

- `make test` 已包含 `openapi-check`（`Makefile:6`）；当前 target 只运行 `node api/openapi/check.mjs`（`Makefile:199-200`）。CI 的统一质量步骤运行 `make test`（`.github/workflows/ci.yml:54-55`）。
- `check.mjs` 只依赖 Node 内建 `fs`，读取 OpenAPI 与少数 Go handler 源码（`api/openapi/check.mjs:1-8`）；它先固定 OpenAPI `3.1.0`（`api/openapi/check.mjs:9-11`），随后维护完整 operation/Schema 及大量领域断言，最终在全部通过时输出成功（`api/openapi/check.mjs:162-180`, `api/openapi/check.mjs:4061-4076`, `api/openapi/check.mjs:4177-4178`）。这不是可由通用 linter 整体替换的逻辑。
- Runtime route 对等已有独立 Go 测试：从 `api/openapi/openapi.json` 读取 method/path（`internal/app/router_inventory_test.go:116-136`），与 `gin.Engine.Routes()` 精确比较并拒绝 duplicate/missing/extra（`internal/app/router_inventory_test.go:139-158`）。当前代码固定 189 个 operation（`internal/app/router_inventory_test.go:39-64`）。
- OpenAPI 文档明确是 3.1.0（`api/openapi/openapi.json:1-6`），共发现 1,751 个 `$ref`，均为内部 `#/...` 引用；未找到外部 `$ref`。这允许 lint/diff 在禁用外部引用网络解析后工作。
- 规范大量使用 JSON Schema 2020-12 组合：41 个 `allOf`、39 个 `if` 和 39 个 `then`。例如条件分支直接位于 `allOf` 子 schema 内（`api/openapi/openapi.json:14961-14968`），另有 `unevaluatedProperties`（`api/openapi/openapi.json:23246-23268`）。这直接影响 oasdiff 的 `--flatten-allof` 选择，见下文。

#### CI、分支与发布

- 唯一 workflow 是 `.github/workflows/ci.yml`；它对所有 PR 及 `main`、`dev` push 运行（`.github/workflows/ci.yml:1-8`），权限只有 `contents: read`（`.github/workflows/ci.yml:10-12`）。未找到 tag、`release` 或 `schedule` workflow。
- 当前任务目标分支是 `dev`（`.trellis/tasks/08-18-openapi-contract-gates/task.json:15-16`）。仓库同时把 `main` 与 `dev` 当作 push 质量分支，但仓库文件没有说明二者的 promotion 规则。
- `actions/checkout@v4` 未设置 `fetch-depth`（`.github/workflows/ci.yml:30-31`），因此不能假定 PR base SHA 或多提交 push 的 `before` SHA 在本地对象库中。breaking gate 必须显式 fetch/验证 base，取不到时失败。
- `api/` 与 `api/openapi/` 均有 CODEOWNER（`.github/CODEOWNERS:36-38`）；PR 模板要求公开 API 运行 `openapi-check`，第三方依赖完成漏洞/许可证检查（`.github/PULL_REQUEST_TEMPLATE.md:13-18`, `.github/PULL_REQUEST_TEMPLATE.md:24-29`）。但是 CODEOWNERS 文件本身不证明 GitHub 已启用必需审查或 branch protection。
- Release candidate 目前仍是手工运行 Compose/readiness/fault-recovery 的流程（`README.md:346-351`, `docs/operations.md:485-503`），没有可复用的自动 release workflow。

#### Docker、网络与离线边界

- Go 生产构建在源码依赖层面是 vendor 模式，但 Web build 仍需要 `npm ci`；CI 还需要 npm registry、Playwright 下载、GitHub Actions、PostgreSQL service image 和 runtime image build（`.github/workflows/ci.yml:16-18`, `.github/workflows/ci.yml:41-55`, `.github/workflows/ci.yml:79-80`）。因此仓库不是完全 air-gapped 构建。
- 本地/发布操作明确以 Docker Desktop/Engine 为前提，且 canonical gates 已包含 Compose 与 Docker build（`README.md:293-299`, `docs/operations.md:485-511`）。只让 `openapi-breaking-check` 使用 Docker 不会给完整项目门禁新增一种基础运行时；`make openapi-check` 仍应保持 Node/npm 路径。
- 项目已有按 digest 固定第三方 Docker image 的先例（`deploy/Dockerfile.local-model-runtime:17`），也已有 `shasum -a 256` 的跨平台校验用法（`deploy/export-browser-smoke.sh:341`）。

#### 依赖与许可证惯例

- 项目自身为 MIT（`LICENSE:1-13`）。ADR 要求候选依赖先满足许可证、运行时、部署和可测试性约束，并在 Review 检查 manifest、lockfile、license 与回归证据（`docs/architecture/adr/0019-mature-framework-first.md:19-34`, `docs/architecture/adr/0019-mature-framework-first.md:51-56`）。
- 已有依赖 ADR 会记录精确版本、vendor 体积、上游 license，并要求升级时重新核对（`docs/architecture/adr/0024-layered-eino-adoption.md:49-54`）；需要分发的依赖 license 清单也被明确要求保留（`docs/architecture/adr/0015-river-goose-runtime.md:33-41`）。
- CI 只对 `web/` 执行 `npm audit --audit-level=high`，没有通用 license scanner（`.github/workflows/ci.yml:57-61`）。本任务应记录两个工具的许可证，并让 Spectral 进入既有 npm audit 范围；不应顺带宣称完成全仓 SBOM/license 治理。

### 2. 外部工具事实（截至 2026-08-18）

#### Spectral

- 当前正式版为 `@stoplight/spectral-cli@6.16.3`，发布日期 2026-08-03；npm metadata 声明 Apache-2.0、Node `^16.20 || ^18.18 || >=20.17`，因此兼容项目 Node 24.18.0。精确 tarball integrity 为 `sha512-corAOQ/WhGoPJOQ3Tcipyn64TgKYDkEhsDplS6vNSGys3kzi9+zzxiVOlbzk9mWuafovzoW+TpGCoNwIZvFf5w==`。
- CLI 官方包名是 `@stoplight/spectral-cli`，`spectral lint` 需要显式 ruleset；Spectral 6 默认不会隐式加载规则，官方 OpenAPI ruleset 名为 `spectral:oas`。
- npm 包包含 `@scarf/scarf` 安装分析；官方文档支持在 package manifest 设置 `scarfSettings.enabled=false` 或设置 `SCARF_ANALYTICS=false`。仓库处理契约工具时应显式 opt out，避免安装阶段产生无关遥测。
- 官方 Docker image `6.16.3` 当前只发布 linux/amd64；项目明显支持 darwin/linux 与 amd64/arm64 host build（`deploy/Dockerfile:25-31`），因此 npm lock 路径比 Spectral Docker 更适合作为唯一跨平台入口。

#### oasdiff

- 当前正式版为 `v1.29.1`，发布日期 2026-08-16，Apache-2.0。其 release 同时提供 darwin universal、linux amd64/arm64 和 windows amd64/arm64 archive 及官方 SHA-256 清单。
- `oasdiff breaking --fail-on WARN base revision` 会在 ERR 或 WARN 级破坏性变化存在时返回 1；`--fail-on ERR` 只阻断 ERR，不满足本任务的 WARN 也阻断要求。
- 外部 `$ref` 默认允许解析；对本仓库应显式使用 `--allow-external-refs=false`，并且禁止 `--open`。官方 `--open` 会把加密后的比较上传到 oasdiff.com，与本任务“不上传规范”的边界不符。
- `v1.29.1` 的 upstream `go.mod` 要求 Go 1.26，而项目固定 Go 1.25.4（`go.mod:3`）。不能用项目工具链 `go install`/`go run` 从源码构建它，也不应为 TODO 7 升级全仓 Go。
- 官方 `tufin/oasdiff:v1.29.1` 是 linux/amd64 + linux/arm64 multi-arch image；manifest digest 为 `sha256:bdba99e5e56558002952aa9a8aa2b91ab5f8e850f5981b5bb9bec732544ff721`。按 digest 运行可让 CI 和两类本地主机使用同一发布内容，并隔离 upstream Go 1.26 构建要求。
- 不应对当前规范启用 `--flatten-allof`。官方说明该 beta flatten 会丢弃位于 `allOf` 子 schema 上的 `if/then/else`、`unevaluatedProperties`、`prefixItems` 等 3.1 关键字；当前规范恰有大量这种结构。`v1.29.x` 在不 flatten 时会将无法精确判定的 `allOf` 变化降为 WARN，因此使用普通比较并 `--fail-on WARN` 更保守；再用项目专属断言和代表性 fixture 补足已知限制。

### 3. 推荐的最小可复现方案

#### Spectral：复用现有 npm lock/install/audit 链

1. 在 `web/package.json` 的 `devDependencies` 中精确加入 `"@stoplight/spectral-cli": "6.16.3"`，同步提交 `web/package-lock.json`；不要使用全局安装、`npx ...@latest`、浮动 semver 或独立未锁定 action。
2. 在同一 manifest 增加 `scarfSettings.enabled=false`，并增加只调用本地 binary 的 npm script，例如 `openapi:lint`。Makefile 调用 `npm run openapi:lint --prefix web`；不要用可能在依赖缺失时临时下载包的 `npx`/`npm exec --package`。
3. Ruleset 放在 `api/openapi/.spectral.yaml`（或同目录显式命名文件），`extends: [spectral:oas]`，所有 severity/disable 都在该文件中带理由接受审查。调用时始终传 `--ruleset`、规范路径和明确 fail severity/format。
4. `make openapi-check` 顺序执行 Spectral lint 和现有 `node api/openapi/check.mjs`。保留现有 target 名，避免 README、规范和调用方漂移；不要把 breaking 比较塞入该 target，因为它需要显式 base revision。
5. 这种放置方式复用当前唯一 lockfile、`npm ci`、CI cache 和 `npm audit`，只增加 Web build stage 的 dev install 量，最终 runtime image 内容不变。单独建立 `api/openapi/package-lock.json` 虽边界更纯，但会增加第二次 install/cache/audit、Docker ignore 与升级面，不是当前最小方案。

#### oasdiff：固定 multi-arch Docker digest

1. 新增仓库内 wrapper（建议 `api/openapi/breaking-check.sh`）作为唯一入口，内部固定可读版本名 `v1.29.1` 和不可变 manifest digest；Makefile 只调用 wrapper，不在 CI 复制另一套 flags。
2. Wrapper 必须要求一个非空、可解析的 `OPENAPI_BASE_REVISION`（或单个位置参数），先验证 commit/object 与该 revision 下的 `api/openapi/openapi.json` 可读，再把历史 blob 写入权限受限的临时目录；任何失败都非零退出。
3. Docker 只读挂载 base 与当前 spec，并使用 `--network none`。核心命令固定为：

   ```text
   oasdiff breaking \
     --fail-on WARN \
     --allow-external-refs=false \
     --format text \
     --color never \
     -- <base-file> <revision-file>
   ```

   不使用 `--open`、ignore 文件、`--flatten-allof`、远程 URL 或自动 upgrade。`--` 防止变量生成的输入被解析为 CLI flag。
4. Docker image 必须写为 `tufin/oasdiff@sha256:bdba99e5e56558002952aa9a8aa2b91ab5f8e850f5981b5bb9bec732544ff721`，不能只写 `stable`、`latest` 或 `v1.29.1` tag。首次运行需要 pull；之后 Docker content cache 可离线复用。Air-gapped 环境应由镜像镜像库预置同一 digest，不增加联网 fallback。
5. `make openapi-breaking-check OPENAPI_BASE_REVISION=<immutable-sha>` 是本地与 CI 的共同入口。`make test` 继续只包含无需历史上下文的 `openapi-check`；CI 在其外显式增加 breaking step。

#### 批准基线：使用不可变 Git 历史，不提交重复快照

- 批准基线的逻辑位置是 `api/openapi/openapi.json`，物理版本是 CI 事件给出的不可变 base commit 中的该 blob，即 `<base-sha>:api/openapi/openapi.json`。不要新增 `baseline.json`、复制 spec 或 `make update-baseline`；它们可在同一 PR 中被覆盖，正是路线图禁止的静默接受路径。
- PR 使用 `${{ github.event.pull_request.base.sha }}`；`main`/`dev` push 使用 `${{ github.event.before }}`。CI 必须验证它是非零 SHA，并显式 fetch 该对象（精确 shallow fetch 或 `fetch-depth: 0`），然后调用同一 Make target。新分支/历史重写导致全零或不可读 SHA 时直接失败。
- 普通非破坏性契约变更合并后，受保护分支的新 commit 自然成为后续 PR 的基线；无需改第二份文件。
- 有意接受破坏性变化时，breaking check 仍应保持红色，由 API owner 在关联 ADR/任务、迁移/弃用/回滚说明和明确 review 后，通过受保护分支的显式管理绕过完成。合并后的 commit 才成为下一轮基线。不要为了让该 PR 变绿而添加 wildcard ignore、改写 baseline 或降低 `WARN` severity。
- 仓库内没有 branch-protection 配置证据。若 GitHub 未把该 check 设为 required，CODEOWNERS 也不能单独保证上述审批边界；这是上线门禁前必须由仓库管理员核实的外部配置。

#### CI 事件接线

- 在 checkout 后解析 event base，拒绝空值/全零值，再显式获取该 SHA。不要使用 `HEAD^`：PR checkout 可能是 merge commit，push 也可能一次包含多个 commit。
- 先运行 `make openapi-check`（现已由 `make test` 间接运行），再独立运行 `make openapi-breaking-check OPENAPI_BASE_REVISION=<sha>`，诊断直接留在同一 quality job 日志。
- 不采用 `oasdiff/oasdiff-action@v0`：它既是浮动 action，又会让 CI 调用与本地 Docker wrapper 分叉。现有 GitHub Actions 本身仍用 major tags，但扩大到全仓 action SHA pinning 属于独立供应链任务，不应混入 TODO 7。

#### 升级流程

1. 单独升级 PR 同时修改 Spectral 精确版本 + npm lock、oasdiff 可读版本 + Docker digest、ruleset/fixture 期望和版本文档；不允许自动 updater 只改其中一半。
2. 核对两个上游 release notes、Node/Go/Docker 兼容、Apache-2.0、Spectral lockfile 的新增许可证/体积、`npm audit --audit-level=high` 及 Docker multi-arch manifest。
3. 运行固定 fixture：相同输入通过、非破坏性加法通过、移除 operation/成功响应失败、收紧 request 失败、放宽或破坏 response schema 失败、OpenAPI 结构错误 lint 失败、缺失/非法 base revision 失败。
4. 对当前规范特别保留含 `allOf + if/then/else + unevaluatedProperties` 的回归 fixture；升级后若 oasdiff 已完整支持，再凭上游文档和 fixture 决定是否调整 flags，不能仅因新版本存在就启用 flatten。
5. 记录版本输出和 fixture 结果后由 `/api/openapi/` owner 审查；不提供自动更新 baseline 或自动生成 ignore 的命令。

## Files Found

| Path | Description |
|---|---|
| `Makefile` | `make test`、`openapi-check`、npm install 和 Docker/Compose canonical targets。 |
| `.github/workflows/ci.yml` | 唯一 CI workflow；PR 与 `main`/`dev` push、Node/Go/cache/install/audit/build。 |
| `web/package.json` | 当前唯一 npm manifest 与 Node/npm 版本声明。 |
| `web/package-lock.json` | 当前唯一 npm lockfile，含 integrity/license metadata。 |
| `go.mod`, `go.sum`, `vendor/modules.txt` | Go 1.25.4 与 vendor 构建基线；不适合直接加入要求 Go 1.26 的 oasdiff CLI。 |
| `api/openapi/openapi.json` | OpenAPI 3.1 公开契约事实源；当前只有内部 `$ref`，大量使用 3.1 conditional/allOf。 |
| `api/openapi/check.mjs` | 4,178 行项目 checker；混合通用结构与不可替代的项目领域/安全断言。 |
| `internal/app/router_inventory_test.go` | Runtime Gin routes 与 OpenAPI operation 的精确集合门禁。 |
| `deploy/Dockerfile` | Web npm build 与 Go vendor build；最终 runtime 只接收 `web/dist`。 |
| `deploy/Dockerfile.local-model-runtime` | 第三方 image digest pinning 的项目先例。 |
| `.gitignore`, `.dockerignore` | 本地工具 cache/node_modules 与 Docker context 边界。 |
| `CONTRIBUTING.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `.github/CODEOWNERS` | API 高风险审查、本地 canonical gate、依赖/许可证与 owner 约定。 |

## Code Patterns

- Canonical Make target 聚合：`make test` 依赖多个窄 target（`Makefile:4-6`）；TODO 7 应延续 `openapi-check` + 独立 `openapi-breaking-check`，不在 workflow 内复制业务命令。
- 锁定安装：Web 统一由 `npm ci --prefix web`（`Makefile:17-18`），Docker 也先复制 manifest/lock 再 `npm ci`（`deploy/Dockerfile:1-6`）。
- 项目专属 fail-closed checker：`check.mjs` 以 `throw new Error(...)` 固定不变量并最终单点成功输出（`api/openapi/check.mjs:9-18`, `api/openapi/check.mjs:4177-4178`）。
- 跨层 route 事实校验：测试从 OpenAPI 动态建立 route set，而非复制 route allowlist（`internal/app/router_inventory_test.go:116-158`）。
- 高风险 API owner：`/api/` 与 `/api/openapi/` 均明确归属同一 owner（`.github/CODEOWNERS:14`, `.github/CODEOWNERS:36-38`）。

## External References

- Spectral `v6.16.3` release: https://github.com/stoplightio/spectral/releases/tag/v6.16.3
- Spectral CLI official README, install/ruleset/license/Scarf opt-out: https://github.com/stoplightio/spectral/blob/v6.16.3/packages/cli/README.md
- Spectral ruleset documentation: https://github.com/stoplightio/spectral/blob/v6.16.3/docs/getting-started/3-rulesets.md
- Spectral exact npm metadata (version, engines, license, integrity): https://registry.npmjs.org/@stoplight%2Fspectral-cli/6.16.3
- Spectral Apache-2.0 license: https://github.com/stoplightio/spectral/blob/v6.16.3/LICENSE
- oasdiff `v1.29.1` release: https://github.com/oasdiff/oasdiff/releases/tag/v1.29.1
- oasdiff breaking/fail severity/ignore/upload behavior: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/BREAKING-CHANGES.md
- oasdiff Docker invocation: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/DOCKER.md
- oasdiff variable argument `--` guidance: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/USAGE_EXAMPLES.md
- oasdiff OpenAPI 3.1 support/limitations: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/OPENAPI-31.md
- oasdiff `allOf` flatten limitations: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/ALLOF.md
- oasdiff upstream Go 1.26 requirement: https://github.com/oasdiff/oasdiff/blob/v1.29.1/go.mod
- oasdiff release checksums: https://github.com/oasdiff/oasdiff/releases/download/v1.29.1/checksums.txt
- oasdiff Docker manifest metadata: https://hub.docker.com/v2/repositories/tufin/oasdiff/tags/v1.29.1
- oasdiff Apache-2.0 license: https://github.com/oasdiff/oasdiff/blob/v1.29.1/LICENSE

## Related Specs

- `.trellis/spec/backend/http-boundary.md:18-20` - OpenAPI method/path 是公开路由事实源，Runtime inventory 必须精确对等。
- `.trellis/spec/backend/http-boundary.md:66-83` - 路由/共享 HTTP 边界的 OpenAPI、Go、vendor 与 diff 门禁。
- `.trellis/spec/backend/index.md:156-175` - 后端质量检查与 canonical OpenAPI/Compose gates。
- `.trellis/spec/frontend/type-safety.md:14-20` - 后续生成客户端、breaking check 与 wire/domain 边界。
- `.trellis/spec/frontend/index.md:135` - Web canonical npm ci/lint/typecheck/test/build 链。
- `docs/roadmap.md:78-90` - TODO 7 的版本/rules/baseline/CI 边界及 TODO 8 依赖顺序。
- `docs/architecture/adr/0019-mature-framework-first.md:19-34` - 成熟工具、强制约束、薄适配和依赖审查原则。

## Caveats / Not Found

- `.trellis/spec/backend/http-boundary.md:18-19` 仍写“当前 183 个 operation”，但代码已固定并验证 189 个（`internal/app/router_inventory_test.go:39-64`）。这是已有规范漂移，TODO 7 完成后的 spec update 应同步修正；不要让 Spectral/oasdiff 设计继续引用 183。
- 仓库文件不能证明 GitHub branch protection、required status check、CODEOWNERS required review 或管理员 bypass 审计是否启用。若这些外部设置缺失，Git 历史 baseline 虽不可被同一 commit 改写，审批仍不是强制边界。
- oasdiff 的 OpenAPI 3.1 支持仍有明确已知限制，尤其 `allOf` flatten 与 conditional/unevaluated 关键字。项目专属 checker 与 fixture 不能删除。
- Spectral npm 方案依赖先完成 `npm ci --prefix web`，与当前所有 Web Make targets 一致；`make openapi-check` 不应偷偷联网安装缺失依赖。
- Docker digest 方案首次运行需要 registry 网络或预置镜像；“digest 已缓存后的离线可运行”不等于仓库已提供完整 air-gap 供应链。
- 当前 Actions 仍以 `actions/checkout@v4`、`setup-go@v5`、`setup-node@v4` 浮动 major tag 调用。TODO 7 可以精确锁定 Spectral/oasdiff，但不能据此宣称整个 CI 供应链已经完全不可变。
