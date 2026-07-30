# 模型 Settings 配置研究

## 已确认事实

- API 在 `cmd/api/main.go:120-129` 启动时一次性 `config.Load`；Worker 在 `cmd/worker/main.go:196-205` 一次性 `config.LoadWorker`。
- 配置顺序是 Defaults -> YAML -> Env -> Validate，见 `internal/platform/config/config.go:380-405`。
- API/Worker 的 Adapter、Executor 和 Workflow Definition 在启动 Composition Root 构造并 Freeze；运行时修改 Env/YAML/DB 不会自动生效，见 `cmd/api/main.go:1204-1222`。
- API 和 Worker 必须共享完整 Embedding 配置；Chat 至少共享 Provider，实际交付应共享全套模型身份与限制，见 `deploy/compose.yml:1-24,83-98,141-174`。
- Embedding endpoint/model/dimensions/normalization/distance/batch limits 参与 Config Hash；改变配置必须形成新的 Embedding Version，不能改写旧索引，见 `internal/retrieval/domain/embedding_contract.go` 与 `migrations/00014_retrieval_index_foundation.sql`。
- Chat `model_version` 会与 Provider 响应精确比较，不能只作为展示字段，见 `internal/platform/models/chat_openai.go:105`。
- Settings 当前明确没有模型配置读写契约，见 `web/src/features/business/BasicPages.tsx:150-186`。
- `/api/v1/system/status` 是公共严格契约，不适合承载模型配置，见 `internal/app/router.go:141` 与 `web/src/api/system-status.ts:142-161`。

## 方案比较

### 数据库明文

拒绝。Worker 必须能恢复 Provider API Key，仅保存摘要不可用；明文数据库违反 Secret 边界。

### 宿主 `.env` / 可写 YAML

不采用。网页容器不应写宿主 Compose 环境；Env 优先级会覆盖 YAML，且现有容器无法仅通过 restart 获得新的 Env。

### 版本化数据库配置 + AEAD 密文

采用。数据库保存非敏感字段、revision、nonce、ciphertext、key_id；主密钥仅保存在项目专属 Docker volume，由一次性 init service 生成并只读挂载 API/Worker。GET 只返回 `api_key_configured`。

## 生效结论

- 设置使用 append-only revision，state row 单独保存 desired/active；当前进程只加载 active，PUT 只推进 desired。
- `./zhixu restart` 先以数据库 lease 固定 target，阻止 PUT，预检候选并排空旧队列；候选 API/Worker 都 prepared 后才原子提交 active。失败恢复 previous active。
- 不挂 Docker Socket，不由页面控制 Docker，不做运行中热切换。
- API/Worker 启动时在数据库可用后读取 active 或固定 rollout target、解密、复用现有 Config validation 与 Configured Factory，再构造冻结运行时。
- AEAD AAD 绑定 revision 时，keep 必须在事务内解密旧密文并按新 revision 重加密；改变 Provider/Base URL 必须 replace，不能把已存 Key 隐式发送到新目标。

## API 建议

- `GET /api/v1/settings/models`：非敏感配置、desired/active/applied revision、`restart_required`、Key configured 状态；`Cache-Control: no-store`。
- `PUT /api/v1/settings/models`：完整设置、`expected_revision`、Key `keep|replace|clear` 操作；版本冲突返回 409。
- `POST /api/v1/settings/models/test`：测试指定 Chat 或 Embedding draft，不持久化；从真实容器网络执行，复用正式 Adapter 限制和脱敏错误。
- required auth 下只允许 Cookie Session；Bearer API Token 即使有业务高 Scope 也不能管理系统 Secret。development disabled auth 仅依赖现有 loopback 边界。

## 本地 Ollama 与远程 Endpoint 限制

- 当前 HTTP 只允许 loopback；容器中的 `127.0.0.1` 不是宿主机。
- 不应放宽任意私网 HTTP。开发 Compose 默认运行两个受控 loopback relay，分别共享 app/worker 网络命名空间，再转发到 `host.docker.internal:11434`；Settings 继续配置安全允许的 `http://127.0.0.1:11434`。
- 任意远程 HTTPS Provider 使用无代理、无 redirect、逐次 DNS/IP 检查的专用 Transport，同时保护连接测试和正式 Adapter，拒绝私网、loopback、链路本地及保留地址。

## 风险

- 只重启 API 会导致 RAG/索引能力和 Worker 实际模型不一致，必须统一重启并核验 revision。
- Key volume 丢失时必须 fail closed；即使 init 为新空 volume 生成新 Key，也不能覆盖旧密文或因存在 Key 文件就假称旧配置可用。
- Provider 连接测试会产生真实最小模型请求，必须显式触发、限时限量且不自动保存。
