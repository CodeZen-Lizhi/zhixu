# W7 全链路验收记录

记录时间：2026-07-30（Asia/Shanghai）

## 结论

W0-W7 的实现、局部构建、契约测试、真实 Compose 启动和浏览器验收均已完成。当前开发栈运行于
`http://127.0.0.1:8080`，最终模型状态恢复为 revision 2，Chat 与 Embedding 均为 disabled；desired、active、API applied、
Worker applied 四个 revision 投影一致，`restart_required=false`。

本次验收使用本地受控 OpenAI-compatible fixture 验证 Chat 和 Embedding 请求，没有调用真实第三方 Provider。四套完整
Compose smoke 因单套可能超过 120 秒且需要动态 fixture，没有逐套运行；对应的启动顺序、一次性服务、成功/错误/信号清理
由可执行 shell contract 覆盖。

## 实际运行证据

| 验收面 | 结果 |
| --- | --- |
| 首次 `./zhixu up` | build、key init、migration、app/worker、relay、firewall、proxy 全部成功 |
| API health | `/livez` 返回 alive，`/readyz` 返回 ready |
| Worker health | 容器内 `/readyz` 返回 ready |
| 模型应用 | 受控 fixture 完成 Chat、Embedding、test、save、restart |
| 重复 `./zhixu up` | app、worker、两个 relay、proxy 共 5/5 容器 ID 不变；PostgreSQL ID 不变 |
| 数据保留 | PostgreSQL volume `deploy_zhixu-postgres` 与 19/19 workspace 文件保留 |
| 最终状态 | desired=2、active=2、API applied=2、Worker applied=2，rollout idle |

## 故障与恢复矩阵

- optimistic PUT 冲突：HTTP 409 返回 current revision；前端保留所有非 Secret 草稿，只清空 Secret 输入并要求重新测试。
- rollout 并发与租约：rollout id、lease 和 CAS contract 覆盖 begin/preflight/drain/prepared/commit/abort/recover。
- 排空与入队竞态：事务内 EnqueueFence、QueuePause/QueueResume、running drain、queued job 保留和 producer ACK 测试通过。
- 候选角色失败与 stale ownership：API/Worker lifecycle、fixed target、revision mismatch、旧实例 ownership fence 测试通过。
- launcher 中断：真实 TERM fixture 验证退出码 143、abort、previous runtime 恢复和 mutation lock 清理。
- relay/readiness 失败：launcher fail closed 并关闭 ingress；Compose contract 强制 relay `restart: on-failure`。
- Key volume 冲突：随机 owner label、预存卷拒绝、label 二次核验和只清理本次 owner 的 contract 通过。
- Provider 失败：401、429、5xx、timeout、redirect、oversize、model mismatch 由 HTTP/transport contract 覆盖。

## 质量门禁

以下命令均通过：

```bash
go test -race -count=1 ./internal/modelsettings/... ./internal/platform/models ./cmd/modelctl ./cmd/api ./cmd/worker
go vet ./internal/modelsettings/... ./internal/platform/models ./cmd/modelctl ./cmd/api ./cmd/worker
go build ./cmd/api ./cmd/worker ./cmd/modelctl ./cmd/migrate
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- model-settings SettingsPage
npm run build --prefix web
make openapi-check
make compose-check
git diff --check
```

真实 PostgreSQL 18 集成验证覆盖 modelsettings repository、migration 00064-00066 和 00063 Down guard；00063 的 5 条相关
migration tests 全部通过。Go/SQL、前端和部署复核发现的问题均已修复，最终没有遗留 P0/P1/P2。

## 量化结果

| 指标 | 优化前 | 优化后 | 变化 |
| --- | ---: | ---: | ---: |
| 无变化 `up` 的常驻容器替换数 | 5 | 0 | 减少 100% |
| 历史目标 smoke image tag | 282 | 0 | 清理 100% |
| 模型 revision 一致投影 | 无统一投影 | 4/4 一致 | desired/active/API/Worker 可核对 |
| 最终页面 console error/warning | 未建立门禁 | 0/0 | 建立可复核门禁 |
| 桌面/移动横向溢出 | 未建立门禁 | 0/0 | 1440x900 与 390x844 通过 |

重复、无配置差异的 `./zhixu up` 实测耗时 14 秒。该数字只表示当前机器和当前缓存状态，不能外推为生产性能指标。

## 浏览器验收

- 桌面截图：`output/playwright/w7-model-settings/final-disabled-desktop.png`
- 移动截图：`output/playwright/w7-model-settings/final-disabled-mobile.png`
- Console：0 error，0 warning。
- Network：system status 与 model settings 初始化请求均为 HTTP 200。
- Storage：Local Storage 与 Session Storage 均为空；Secret 未进入 URL、响应 model 或浏览器持久化。

## 清理与保留

- 精确移除 282 个无引用的四类知序 smoke image tag，清理后目标数为 0。
- `deploy` runtime、PostgreSQL volume、非目标 image inventory 和其他项目资源保持不变。
- 删除了仅用于验收且含受控测试值的 `.playwright-cli` 临时快照与 Python cache；这些测试临时文件不可恢复。
- 保留最终桌面/移动截图、数据库数据、workspace 和正在运行的开发栈。

## 剩余验证边界

- 未使用真实第三方 Provider；真实账号的限流、计费、区域网络和供应商特有响应不在本次证据内。
- 未逐套执行四个长耗时 Compose auth/search/tool/rag smoke；执行了对应的可执行启动与清理 contract。
- Vite build 仍报告既有 large chunk warning，不影响本次功能和浏览器验收。
- 工作树包含其他任务的并行改动；验收发生在提交前，本任务随后按用户指令提交为 `c14173e`，且没有回滚这些改动。
