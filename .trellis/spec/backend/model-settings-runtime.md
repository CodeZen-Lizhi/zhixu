# 模型设置与开发运行时契约

> 锁定 managed model settings、冻结模型运行时、受控 restart 与本地 Docker 生命周期的稳定边界。

## Scenario: Managed Model Settings And Restart

### 1. Scope / Trigger

- 修改 `internal/modelsettings`、`internal/platform/secretstore`、模型 Factory/Transport、API/Worker Composition Root、
  Workflow enqueue/attempt、
  `cmd/modelctl`、`deploy/compose.yml` 或根目录 `zhixu` 时，必须应用本规范。
- 本规范只覆盖开发 Compose 的 managed 模式；普通二进制 static Env/YAML 模式继续兼容，revision 固定为 `0`。

### 2. Signatures

- Settings HTTP 只依赖 Settings Manager：`Snapshot`、`Save`、`Test`。Handler 不接收解密后的
  `ResolvedSettings`，也不直接调用模型 Adapter。
- API/Worker 只依赖 Runtime Loader/Session：固定加载当前 active，或有效 rollout 绑定的 target；每个进程只构造
  一个不可变 Model Runtime，并向全部消费者复用同一 Chat/Embedding capability 与 Contract。
- `zhixu-modelctl` 只依赖 Rollout Coordinator/Session：`Open`、`Recover` 与业务阶段动作；CLI 不传
  `expected_phase`，launcher 不拼 PostgreSQL CAS。
- Revision、Rollout、Runtime Store 和 tx-scoped EnqueueFence 是模块内部 seam，不暴露给 HTTP 或 shell。

### 3. Contracts

- `desired` 是最后保存 revision，`active` 是全局已提交 revision，`applied` 是单进程已加载 revision；保存只推进
  desired，只有 API/Worker 都 fresh/prepared 且 revision 等于固定 target 时才能 commit active。
- revision `0` 是无持久行的 canonical disabled。非零 revision append-only；高级 timeout/batch/byte limit 也冻结。
- Secret 只允许请求瞬时明文、短生命周期进程内明文和 AES-256-GCM 密文。AAD 绑定 revision、用途、schema、
  Provider 和规范化 Endpoint；`keep` 必须解密后以新 revision AAD 重加密。
- AES-256-GCM key/nonce/envelope mechanics 只由 `internal/platform/secretstore` 实现；Model Settings wrapper 继续拥有
  自己的 envelope、脱敏错误和 AAD。Model schema 固定为 `model-settings-secret/v1`，purpose 固定为 `chat|embedding`；
  Git Sync 即使复用同一 primitive，也必须独立加载业务主密钥并使用 `git-remote-token` / `git-remote-token/v1` AAD，
  不能合并业务上下文或跨用途打开密文。
- Settings Audit 与 revision 保存同事务，只包含 action、revision、Provider 与 key-configured；禁止 Endpoint、draft、
  密文、Secret、Key 长度或 instance id。
- 远程模型只允许 HTTPS，使用 `Proxy=nil`、禁止 redirect、逐新连接重解析的专用 Transport。A/AAAA 任一地址为
  loopback、私网、link-local、multicast、unspecified 或保留地址时整组拒绝；仅 Ollama preset 可访问精确
  `http://127.0.0.1:11434` relay。
- draining 必须先提交 validating -> draining，让数据库入队围栏生效，再暂停 River queue；所有 producer 在 job insert 同一事务内取得
  EnqueueFence。已 claim attempt 保持原 revision，未 claim job 保留到新 active。
- 每次 `workflow.node_attempt` Claim 都保存冻结的 `model_settings_revision`；重复 delivery 必须核对相同 revision，
  已开始 attempt 不得热切换。
- `./zhixu down` 保留 PostgreSQL、模型主密钥和 Workspace；只有显式 `./zhixu reset` 确认后可删除 Compose volumes。
  应用容器不得挂 Docker Socket。
- Goose 只记录迁移版本、不校验已执行文件内容。历史环境可能已经记录同版本的早期 schema；后续 forward migration
  必须按实际列/约束检测旧形态，在单一事务内 repair 或 fail closed，不得改写已发布迁移并假设会重跑。
- launcher 必须先等待 PostgreSQL healthy，再用 `compose run --rm --no-deps -T` 顺序执行 Key 初始化和迁移；任一步
  非零退出都原样终止。完成即退出的 one-shot 不得交给 `compose up --wait` 或与 `compose wait` 竞态。
- 普通重复 `up` 使用 Compose 的差异检测，不强制重建未变化的 API/Worker；受控 `restart` 仍必须强制替换
  prepared 与 steady runtime，避免候选启动参数或旧实例被误用。
- relay 使用 `network_mode: service:app|worker` 时不拥有独立网络配置；host-gateway 等映射只配置在 app/worker
  namespace owner，relay 不重复声明 `extra_hosts`。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 无持久设置 | revision 0、Chat/Embedding disabled，基础 API/Worker 可启动 |
| expected revision 过期或 rollout 进行中 PUT | 409，返回当前 revision，不回显设置或 Secret |
| Key 缺失、wrong key、密文/AAD tamper | 模型 capability unavailable、稳定脱敏错误；基础诊断与 Settings 可用 |
| draft 改变 Provider/Endpoint 仍使用 keep | 拒绝；必须 replace 或 clear |
| DNS mixed public/private、redirect 或 remote HTTP | fail closed，不尝试被拒绝地址，不记录完整 Endpoint |
| drain 超时、单 role prepared 失败或 launcher 中断 | active 保持 previous；abort/recover 后恢复旧 runtime 与 queue |
| runtime stale、rollout id 或 applied revision 不匹配 | 进程停止 owner heartbeat/工作并 fail closed |
| attempt 重放使用不同 revision | consistency/version conflict；不得复用旧 attempt |
| 已记录旧迁移版本但 schema 缺列 | forward migration 按 shape 事务修复；非法旧行失败且 schema/数据完整回滚 |
| 历史 Export 表数据量较大 | repair 前评估全表回填与 `ACCESS EXCLUSIVE` 锁窗口，并安排维护窗口；不得宣称在线零停机 |
| Key 初始化或 migration one-shot 失败 | launcher 保留退出码并停止，不运行 modelctl、API 或 Worker |

### 5. Good / Base / Bad Cases

- Good：保存 desired 后页面显示 restart required；`./zhixu restart` 固定 target、排空、启动两个 prepared candidate、
  原子 commit，再恢复 queue/proxy，两个 role 的 applied 与 active 一致。
- Base：全部模型 disabled 时仍完成 Compose、迁移、readiness、Keyword Search 和 Settings 浏览器闭环。
- Bad：保存时热替换进程 Adapter、Handler 持有明文 Key、只检查第一个 DNS 地址、先暂停 queue 后再提交 draining、
  仅凭容器 healthy 就提交 active，或 `down` 隐式删除 volume。

### 6. Tests Required

- Domain/Application：canonical Provider value、Secret action、expected revision、Settings Manager Test 生命周期、
  Rollout Coordinator 合法顺序、lease/stale/recover、runtime ownership 与 attempt revision contract tests。
- Crypto/Transport：通过 Model Settings 与 Git Sync 业务 wrapper 回归共享 `secretstore` primitive，覆盖 round trip、wrong key、
  nonce/AAD tamper、purpose/schema 隔离、replace/keep、safe String/GoString、redirect、mixed DNS、rebind、IPv4/IPv6 fallback、
  TLS hostname/SNI、精确 loopback relay。
- PostgreSQL：fresh migration Up/guarded Down、append-only revision、同事务 Audit、并发 PUT/begin、runtime CAS、
  enqueue/drain 竞态、attempt revision insert/read/replay、历史同版本 schema repair 与失败全回滚；SQL 必须参数化并用真实 PostgreSQL 验证。
- Composition/CLI/Compose：API/Worker disabled/configured/fixed target、单一 Runtime 注入、queue pause/resume、
  launcher Key/migration one-shot fail-fast、secret/smoke cleanup fake-Docker contract，以及真实 `./zhixu up`、`restart`、
  重复 `up` 和 readiness。
- Canonical Go/contract 门禁至少包含受影响 `go test`、`go test -race`、`go vet`、`go mod tidy -diff`、
  `make openapi-check`、`make compose-check`、`git diff --check` 和 Secret 扫描。

### 7. Wrong vs Correct

```text
Wrong: HTTP Handler ResolveDraft 后把 Secret 交给 ConnectionTester。
Correct: Handler 只调用 SettingsManager.Test；Manager 在模块内解析、测试并销毁 Secret。

Wrong: 先暂停 Queue、后提交 draining，或 queued job 在入队时宣称模型 revision。
Correct: 先提交 draining 关闭同事务 fence，再 QueuePause；Worker Claim 时冻结 attempt revision。

Wrong: docker compose down -v、image prune 或宽泛名称匹配被包装进日常 down。
Correct: down 保留数据；reset 单独确认；历史 smoke 只按精确 namespace 且确认无容器引用后删除。

Wrong: 用 compose up --wait 启动 migrate，或在共享 app 网络命名空间的 relay 上重复 extra_hosts。
Correct: postgres healthy 后 compose run --rm 顺序执行 one-shot；网络映射由 app/worker namespace owner 持有。

Wrong: Model Settings 与 Git Sync 各自复制 AES-GCM 实现，或共用一份没有业务 purpose/schema 的 AAD。
Correct: `secretstore` 只拥有加密原语；两个业务 wrapper 分别拥有 envelope、purpose/schema、完整上下文和错误语义。
```
