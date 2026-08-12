# 交付结果（历史归档）

## 结果

提交 `9bb5b939d84fe52993a549216a36e6de831c2f71` 已交付 managed 模型运行时热切换：管理员保存精确目标后可在运行中的 API 与 Worker 内应用，正常路径不执行 `./zhixu restart`，也不重启 Compose 容器。

切换以 durable desired/active/applied 状态和 API/Worker participant 为准；`arming`/`activating` 仅对新的模型相关 Claim 建立短 admission fence。已经 admitted 的 workflow/reindex 工作和其 generation lease 继续使用原 revision；历史 Index/Embedding 依据持久 provenance 获取兼容 runtime。提交前失败保留旧 active，提交后按目标 revision 向前恢复，而不是静默回退或假报成功。

## 已验证证据

- 相关 Go Runtime、Workflow、Retrieval、API/Worker composition 的定向 `go test -race` 与 `go vet` 已通过。
- PostgreSQL migration/protocol 的真实数据库定向 integration 已通过。
- OpenAPI/前端 Model Settings 合同检查、前端 43/43 定向测试、lint、typecheck 和 build 已通过。
- `go mod tidy -diff`、vendor 可解析与 `git diff --check` 已通过。
- 隔离 Compose smoke 使用 disposable 项目、Workspace、数据库和 loopback fake model，验证 Apply 前后 API/Worker 容器身份不变，以及旧/新 conversation 和持久 Attempt/ModelRun revision 的跨提交点行为。

## 已知验证边界

- 前端完整 suite 有一项与本功能无关的 graph 文案断言不一致；不把该未通过项计入本功能通过。
- 聚合 `make compose-check` 超出当时的执行预算；隔离热切换 smoke 是本交付的 Compose 证据，不能替代所有 Compose 场景。
- `research/` 保存的是实施前的方案和风险分析；其中旧 task 路径与“尚待实现”的描述只具有历史意义。当前可执行契约以 ADR-0022、Model Settings 后端/前端规范、OpenAPI、运行手册和本文件为准。
