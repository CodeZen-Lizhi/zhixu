# 实施计划

## 实施原则

- 这是一个跨启动器、Docker、Go API、前端与文档的原子架构任务；最终状态
  不允许旧 Host Controller 入口与新入口并存。
- 实施前重新读取 `.trellis/spec/` 对应层规则，并以当前脏工作树为基线。
  已有 17 项修改全部视为用户工作，特别保留 Coordinator 恢复修正和 Compose
  依赖修正。
- 不通过清库或新建 Workspace 绕过兼容问题；先锁定现有 ID/切换契约测试。
- 每完成一层立即做相关短时验证，最后再做跨层和真实 Docker smoke。

## 1. 建立行为基线

1. 记录并审阅 `zhixu`、Compose、Dockerfile、Host Controller、Workspace
   ControlService/Repository、前端双入口和 OpenAPI 的直接调用关系。
2. 对 `internal/hostcontroller/coordinator.go` 及测试的已有未提交 diff 建立清单，
   在迁包前运行聚焦测试，确保恢复行为可复核。
3. 保存当前 launcher/compose contract 的基线输出；只改断言，不绕过已有
   Secret、路径、网络和锁安全检查。
4. 确认无数据库 migration 必需；若实现中发现 schema 缺口，先暂停并更新
   设计，不能临时追加未经审查的迁移。

## 2. 提取一次性 Workspace 控制核心

1. 创建中性 `internal/workspacecontrol` 包，迁移 PathValidator、Grant、
   Compose model、ComposeDriver、Coordinator、StateFS 与相关测试。
2. 去除 HTTP/backend URL 相关 Port 和 DTO；把 Coordinator 暴露为一次性
   reconcile/switch 用例，保留持久化 operation 恢复、quiesce、rollback。
3. 从 ComposeDriver 删除随机 app host port 发现和 Host Controller proxy
   backend 维护；保留 exact bind、relay、firewall、proxy sidecar、prepare/
   verify/activate/revoke。
4. 增加同根幂等、A->B、B 失败恢复 A、旧 runtime 不可达恢复、取消/信号与
   generation 对账测试。
5. 新增 `cmd/workspacectl`，所有配置显式注入；数据库 URL 通过 FD 读取，命令
   输出稳定的机器可解析结果与面向用户的错误。

## 3. 重写启动器和本机状态

1. 为 `zhixu` 增加 `up --workspace PATH [--initialize-git]` 与
   `workspace switch PATH [--initialize-git]` 参数解析，扩展 mutation lock 的允许命令；Git 初始化只能显式授权。
2. 实现受保护、版本化、原子提交的 Workspace selection 读写和校验；不同
   PATH 的 `up --workspace` 复用 switch，不直接写 grant。
3. 把 Host Controller bundle 构建替换为仅导出原生 `workspacectl`；移除 Web
   assets、token、PID、log、nohup、curl Controller live 和 stop lifecycle。
4. 调整 `up/restart/status/logs/down/reset` 语义：down 保留 selection，reset
   经确认后清 selection，日志只允许 Compose service。
5. 重写 launcher fake Docker fixture/contract，覆盖 selection 权限、原子更新、
   首次必填、重复恢复、失败不覆盖、fixed URL、down/reset 和 Secret 不泄漏。

## 4. 固定 Docker Web 入口

1. 在 Compose 上固定发布
   `127.0.0.1:${ZHIXU_HTTP_PORT:-8080}:8080`，保留 API 内部 `8081`、共享网络
   namespace proxy 与 firewall。
2. 调整 app/worker/proxy 的启动、健康检查和 restart policy，使日常页面只由
   Docker 生命周期决定；切换期间仍由控制命令有序重建。
3. 更新 Dockerfile Web build 为单一业务构建，删除 host-controller bundle
   stage/target，只保留原生 workspacectl 导出目标。
4. 更新 `compose_runtime_check.py`、`compose_runtime_contract.py`、workspace/auth
   checks 和 fixture，断言 fixed loopback、无随机 app port、exact grant 不变。
5. 验证现有 Compose 脏修改（app/worker 直接依赖 healthy PostgreSQL）保留。

## 5. 增加 Active Workspace 业务契约

1. 在 Workspace domain/application/repository 增加“唯一 Active Workspace”
   查询，managed 模式必须再经 RootGrantResolver 校验。
2. 增加 `GET /api/v1/workspaces/active` handler 和 router 注册，覆盖零个、一个、
   多个 active、grant 不匹配、认证和动态路由优先级。
3. 更新 `api/openapi/openapi.json` 及检查器/契约测试，响应字段与现有 Workspace
   schema 保持一致，不新造 Controller DTO。
4. 检查直接调用方、权限矩阵和 runtime gate，确保只读 bootstrap 可用但不会
   放开 create/root selection。

## 6. 收敛前端为单一运行模式

1. 新增 Active Workspace API decoder/query，以服务端 ID 初始化 App 的
   Workspace Context。
2. 合并/简化 `App.tsx`，删除 Controller/direct 构建分支与 runtime-access
   门禁；保留现有业务认证流程和业务路由。
3. 调整 `active-workspace.ts`、WorkspaceCacheBoundary 和 query client，使 ID
   切换时取消旧请求、清理 A 缓存，再挂载 B。
4. 将 Workspace 页面改为只展示服务端当前 Workspace 和命令行切换状态；
   浏览器不再提交任意 root path。
5. 删除 controller API/context/page/css/tests 及无调用的 runtime-access
   invalidation；把仍有价值的缓存隔离断言迁到新 bootstrap 测试。
6. `package.json` 收敛为单一 `build`，更新 Vite 环境类型和测试 setup。

## 7. 删除旧代码并清理引用

1. 删除 `cmd/hostcontroller` 和迁移完成后的整个
   `internal/hostcontroller` 目录。
2. 删除启动器所有 Controller bundle/PID/log/token/session/proxy backend
   路径，删除对应 fixtures 与过时测试。
3. 删除 `/host/v1`、`/control/v1`、`#control`、Controller Cookie/CSRF/Origin
   和随机 backend URL 的生产引用。
4. 使用全仓 `rg` 检查：当前代码和构建中上述引用必须为零；只允许 ADR 的
   历史背景、迁移说明以及明确标记的数据库兼容字段。
5. 运行 `go mod tidy -diff` 或等价只读检查，确认删除未留下无用依赖。

## 8. 文档、ADR 与稳定规范

1. 新增 ADR，supersede ADR 0017 的常驻 Controller/网页入口部分，并明确继承
   exact root grant、无 Docker Socket、loopback 入口和一次性控制命令。
2. 更新 ADR 索引、README、`.env.example`、deployment、technology stack、
   architecture CONTEXT 与需求优化清单，统一首次启动、后续启动、切换、down、
   reset 和升级命令。
3. 更新 `.trellis/spec/backend/workspace-root-grant.md`、后端/前端质量规范中的
   稳定契约；保留这些文件里现有未提交的恢复与质量内容。
4. 文档明确区分 Controller 控制密钥、业务认证 Token 和模型 API Key，避免
   再把“无需控制密钥”误解为删除所有认证。

## 9. 分层验证

### Go 与数据契约

```bash
go test -timeout=60s ./internal/workspacecontrol/... ./cmd/workspacectl/...
go test -timeout=60s ./internal/workspace/... ./internal/app/... ./cmd/api/...
go vet ./internal/workspacecontrol/... ./internal/workspace/... ./cmd/workspacectl/... ./cmd/api/...
```

- 对 Coordinator、Workspace repository/handler/router 的 SQL 与授权路径执行
  `go-review` 和 `sql-code-review`；重点检查 transaction、唯一 active、租约、
  rollback、参数化和 Workspace ID 约束。

### 启动与 Compose

```bash
bash deploy/launcher-contract.sh
python3 deploy/compose_runtime_contract.py
python3 deploy/compose_workspace_contract.py
docker compose --profile workspace-runtime --profile modelctl \
  --project-name zhixu -f deploy/compose.yml --env-file .env.example config --quiet
```

- 检查端口冲突、无 selection、危险路径、A/B 切换失败、down/restart/reset 和
  macOS/Linux 原生构建目标。

### Web 与 OpenAPI

```bash
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- --run
npm run build --prefix web
make openapi-check
```

- 使用 `code-review-and-quality` 检查 bootstrap、缓存隔离、认证保留和路由。

### 真实 Docker 与浏览器 smoke

1. 用临时 A/B 目录和隔离的测试 Compose project/volume 启动，不触碰用户真实
   Workspace 或当前数据库。
2. 验证首次 A 启动、直接刷新、`down -> up`、A->B->A、B 无 A 数据、失败回滚、
   端口占用提示和浏览器控制台/网络请求。
3. 验证页面在没有任何 Host Controller 进程、PID、Cookie 或 fragment token
   时仍持续可访问。

### 最终门禁

```bash
rg -n "zhixu-host-controller|#control=|/host/v1|/control/v1|controller\.pid|controller\.log" \
  --glob '!docs/architecture/adr/**' --glob '!.trellis/tasks/**' .
go mod tidy -diff
git diff --check
```

预期 `rg` 在生产代码、构建、脚本和当前文档中无结果。最终审查同时确认没有
回滚用户原有修改、没有删除 PostgreSQL volume/宿主机文件的隐式路径、没有
把任意 root 或 Docker Socket 暴露给 Web/API。

## 10. 完成条件与回滚

- PRD 的 AC-01 至 AC-11 全部有命令输出或测试证据，才可结束任务。
- 旧 Controller 删除是完成条件，不进入 deferred list。
- 若真实 smoke 失败，修复新架构或整体回滚代码；不得临时恢复双 Web 入口。
- 因无破坏性数据迁移，代码回滚前执行 `./zhixu down` 即可；宿主机 Workspace
  与 PostgreSQL 业务数据保持不变。
