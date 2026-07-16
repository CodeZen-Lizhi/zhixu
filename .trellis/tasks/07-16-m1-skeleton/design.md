# M1 骨架设计

## 目录

```text
cmd/api/                 API 入口
cmd/worker/              Worker 入口
internal/app/            HTTP Router、System Status
internal/platform/config 配置加载和校验
internal/platform/postgres pgxpool、Ping、关闭
internal/platform/observability slog JSON
internal/webassets/      React 生产静态资源嵌入边界
api/openapi/             OpenAPI 契约
web/                     React/Vite/TypeScript
deploy/                  Dockerfile、Compose
migrations/              pgvector/基础 Schema 启用迁移
.github/workflows/       CI
```

## M1 Verified Toolchain Baseline

这些版本已通过当前官方 Node release、npm registry、Go module proxy 或 Docker Hub 查询；它们只锁定 M1 骨架，后续升级仍需运行质量门禁。

- Node `v24.18.0`（官方 LTS Krypton），npm `11.7.0`。
- React/React DOM `19.2.7`。
- Vite `8.1.5`，`@vitejs/plugin-react` `6.0.3`。
- TypeScript `5.9.3`：`typescript-eslint@8.64.0` 的 peer range 为 `>=4.8.4 <6.1.0`，因此不采用 registry 当前 `7.0.2`。
- TanStack Query `5.101.2`，React Router DOM `7.18.1`。
- Vitest `4.1.10`，jsdom `29.1.1`，Testing Library React `16.3.2`，jest-dom `6.9.1`。
- ESLint `10.7.0`，typescript-eslint `8.64.0`，Node types `26.1.1`。
- Go `1.25.4`（本机已安装且可验证），chi `v5.3.1`，pgx `v5.10.0`，yaml.v3 `v3.0.1`。
- PostgreSQL/pgvector Compose image `pgvector/pgvector:0.8.5-pg18-bookworm`（Docker Hub tag 已验证）。

M1 不提前引入 River、Goose、sqlc 或 Eino；它们在 M2/M3/M4 对应任务中按同样方式验证并锁定。

## API

- `GET /livez`：只证明 API 进程可响应。
- `GET /readyz`：检查配置与 PostgreSQL Ping，失败返回 503 和结构化依赖状态。
- `GET /api/v1/system/status`：返回 app version、DB 状态、overall status、request/trace 信息；不得暴露连接串或 Secret。
- 未匹配 API 返回稳定 JSON 错误；非 API 路由回退 React index。

## Worker

- 使用同一 Config 和 pgxpool。
- 启动时 DB 不可用则真实失败退出，由 Compose 重启/健康策略处理。
- 运行中周期 Ping；失败记录 error code，不把业务任务标成功。

## Config

优先级：命令行/显式 config path → 环境变量 → YAML → 默认值。M1 默认值只用于非敏感端口、超时和 localhost；数据库密码、模型 Key 等无默认 Secret。

## Frontend

- React Router 单首页骨架。
- TanStack Query 获取 `/api/v1/system/status`。
- API 边界校验响应；组件不直接解析任意 JSON。
- 展示 loading、ready、degraded、error 和重试。

## Deployment

- Docker 多阶段：Node 构建 Web → Go 构建 API/Worker 并嵌入 Web → distroless/最小 non-root runtime。
- Compose PostgreSQL 使用 pgvector 官方兼容镜像，数据库不暴露公网；本地开发可仅绑定 localhost。
- 所有验证显式使用 `docker compose -f deploy/compose.yml`。

## Testing

- Config unit test。
- API handler test：live、ready success/fail、status、404/problem。
- PostgreSQL Adapter 使用接口 Fake 做 unit；真实连接 smoke 通过 Compose。
- 前端 component/query test 覆盖 loading/success/error。
- Docker smoke 实际访问 API 和页面。
