# Interview HTTP 兼容边界实施（2026-09-09）

本记录只覆盖 Interview owner 的本次增量。原基线仍为 `a4c16248ce1082ce500aea2a99d640e4e0195ddf`；没有修改 OpenAPI 比较基线、normalizer、忽略规则、迁移 SQL、Schema 或用户数据。列表查询 SQL 增加不可变来源过滤。

## 接口与来源边界

`Handler.Routes` 继续注册原有 8 个 v1 operation。新增 `Handler.RoutesV2` 只注册以下 6 个 operation，供 Composition Root 的 `/api/v2` 分组调用：

- `POST /review/interviews`
- `GET /review/interviews`
- `GET /review/interviews/:session_id`
- `POST /review/interviews/:session_id/turns`
- `POST /review/interviews/:session_id/complete`
- `PUT /review/learning-paths/:path_id/steps/:step_id`

两个分组各自持有 Handler 副本，来源限制不会因注册顺序或并发请求相互覆盖。Path status 与 Memory Candidate 响应未变化，继续使用原 v1 operation。Start 在两个版本中均只接受 Claim 配置，NOTE 从既有后台 preparation 入口创建。

v1 成功响应保留原 Claim 投影；不会新增 `source_kind`、`note_source` 或 nullable Claim 来源。v2 使用现有 Claim/NOTE 联合投影，NOTE 的 `claim_id=null`、冻结 Note Revision 和原始资料来源保持真实。v1 GET NOTE 返回 `409 INTERVIEW_API_VERSION_UNSUPPORTED`、`retryable=false`；HTTP 消息为 `This interview source requires API v2`。

`SubmitTurnCommand`、`CompleteCommand`、`UpdatePathStepCommand` 与 `SessionListQuery` 增加显式 `ClaimOnly` 来源约束；相应 Store records 逐阶段传递同一约束。零值继续支持原有直接应用调用的 NOTE 业务。约束不参与请求 hash、receipt、Completion reservation、Artifact digest 或持久业务身份。

应用在读取任何命令 replay、评分、Begin/Prepare completion、Artifact 创建、步骤更新之前核对来源。Store 的 Submit、Begin/Prepare/Complete、Path Step 入口也在进入命令事务和错误后的 replay 恢复分支之前核对持久来源。判断依据保持不可变：`00059` 的 Review Session shell guard 冻结 Config/StartedAt，`00097` 冻结 Question/Path Step 来源并校验来源与 Session 绑定。原事务、锁顺序、CAS 和提交结果未知的恢复流程没有更改。

## 列表与游标

现有单条 GORM Raw 查询在 `ORDER BY / LIMIT` 前使用参数化 `ClaimOnly` 和冻结 `config.scope.note_revision` 筛选。v1 Query 只返回 Claim Session，v2 Query 返回两种来源；HTTP 不做丢条目的后置过滤。应用与 HTTP 遇到越过来源约束的 Repository/Service 页会报不一致错误。

v1 保留历史 `version=1` 游标；v2 使用 `version=2`。两者仍绑定 Workspace、开始时间和 Session ID，严格拒绝另一个接口版本的游标，返回 `400 INTERVIEW_CURSOR_INVALID`。切换接口版本必须从第一页重新查询，不能以旧集合的边界继续遍历新集合。

## 验证

- `go test -mod=vendor ./internal/review/interview/application ./internal/review/interview/http ./internal/review/interview/adapter/postgres -count=1 -timeout=60s` 通过。
- 新增 HTTP 回归使用真实 Handler、认证 Middleware 和实际 Application Service，Store/Scorer/Artifact 使用可计数测试适配器。覆盖 NOTE v1 读/写/重放的稳定拒绝、拒绝前后完整 Snapshot 不变、零 replay 查询/评分/预留/Artifact/写入，v2 NOTE 读/写/精确重放，Claim 跨版本命令的同一幂等结果，所有写阶段的来源约束传递，以及跨 Workspace、跨版本 cursor 和越界页拒绝。原 Claim 严格投影测试继续通过；原 NOTE 隐私投影测试改由 v2 执行。
- `go test -mod=vendor -race ./internal/review/interview/... -count=1 -timeout=60s` 8 个包全部通过，HTTP 包耗时 4.338 秒。
- `go vet -mod=vendor ./internal/review/interview/...` 通过。
- 隔离 PostgreSQL：`env -u ZHIXU_TEST_DATABASE_URL go test -mod=vendor -tags=integration,testcontainers ./internal/review/interview/adapter/postgres -run '^TestInterviewRepositoryClaimOnlyListFiltersBeforeLimit$' -count=1 -timeout=60s` 通过，包耗时 11.901 秒。3 Claim/3 NOTE 交错且包含同时间记录；Claim 页保持 2+1、完整单查询分页，v2 返回两类来源，其他 Workspace 为空；直接 Store NOTE Begin/Prepare 被拒绝且无 reservation。使用 testdb 自建隔离容器/数据库，不使用用户库。

## 本次文件与后续接线

仅修改 `internal/review/interview/` 下的 Application model/service、HTTP Handler/测试、GORM command/completion/read，并新增各层来源约束与回归测试。既有 NOTE/TODO4 代码保持；本记录是唯一 owner 外修改。

主会话继续处理公共 Router/Auth 接线、OpenAPI 的独立新旧 Schema 和 v2 operation、Web/生成客户端、原基线 breaking 门禁与独立检查。HTTP 新测试已经使用共同认证 Middleware；owner 测试通过不替代整个生产 Router 的 route inventory、CSRF/Capability/Workspace 验证或 OpenAPI 门禁。本次未提交、推送或部署。
