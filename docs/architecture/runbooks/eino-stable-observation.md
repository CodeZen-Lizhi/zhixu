# Runbook：Eino Runtime 稳定发布观察

> 此为可选的长期运营观察。按 2026-09-08 用户要求，不作为开发交付或任务归档门禁；未执行不记为 PASS。以下固定窗口、样本量和 verifier 标准仅在主动选择该观察时适用。

## 1. 目的与完成条件

本 Runbook 是 Eino 生产稳定性观察的证据标准。它不替代单元测试、真实 Provider smoke 或安全审查，
也不负责选择或删除 Runtime 实现。

选择执行本观察时，证据必须同时满足：

1. 连续 **7 个自然日**；以及
2. 这 7 个自然日窗口内至少 **100 个非 replay 的 RAG v2 终态**。

自然日以启动 manifest 明确写入的时区计算；只有全部开始前门禁完成后的下一个自然日 00:00 才能开始计时。
预检会验证从首个窗口起、最长 366 天归档所需的全部午夜边界；任一 00:00 不存在或因时区切换出现两次时，
必须在联网前拒绝启动。建议正式观察使用 `UTC`；在其他时间切换夏令时、仍形成 23/25 小时自然日的时区可以使用。
计数只使用 `rag.outcome_total` 的窗口增量。该计数仅在 RAG v2 Finalizer 首次提交终态时发射，replay 不增加计数；
`completed`、`refused` 和 `clarification_required` 都计入，其他运行失败不计入 100 的样本数。

两个条件是并列关系：第 7 天未达到 100 个样本时继续观察；达到 100 个样本但不足 7 个完整自然日时继续观察。

## 2. 不可替代的证据

以下只能证明本地实现或回归，不得作为本 Runbook 的开始、继续或通过证据：

- `httptest` 或本地 OTLP 接收器；
- Compose smoke；
- 本地 Ollama、fixture 或 replay 流量；
- 临时改写指标、人工补数、截图、日志文本或口头确认。

观察环境必须有真实 OTLP Collector，并将 Metrics 转换为 Prometheus-compatible 查询面、Trace 转换为 Tempo-compatible
查询面；采集器只调用这两个只读 API，不把 OTLP Collector 当作查询 API。Collector 必须可独立查询到 API 与 Worker
两个服务的 Metrics 和 Trace。固定查询定义见 `deploy/eino_stable_observation_queries.json`，后端 URL 的批准摘要见
`deploy/eino_stable_observation_backend_trust.json`；两者都必须先由独立发布审批配置为 `configured`。
证据只保存聚合数值和不可逆 hash，不保存 Prompt、正文、任何业务/用户/运行对象 ID、Provider 名称或模型名称。

## 3. 开始前门禁

观察负责人先创建一份脱敏的启动 evidence manifest。所有项目为通过才可冻结窗口：

| 检查项 | 必需证据 | 通过标准 |
|---|---|---|
| 外部 Embedding | `make eino-live-openai-embedding-smoke` 的脱敏结果 | 使用实际外部 HTTPS OpenAI-Compatible Embedding 服务；命令成功且不记录 endpoint、Credential、Provider 或模型 |
| 部署不可变性 | 镜像 digest、配置 SHA-256、部署时间和时区 | API/Worker 使用同一已批准发布物；hash 可由独立操作者复算 |
| Eino 部署 | 配置摘要、镜像 digest 与发布制品证明 | API/Worker 使用同一 Eino-only 发布物；不得存在第二套 AI Runtime |
| Telemetry | API/Worker readiness、Collector 查询结果和查询定义 hash | 两个进程均为 `required`；真实 Collector 同时可见两服务的 Metrics 和 Trace，且查询不依赖本地内存或测试接收器 |
| 基线阈值 | 已批准的阈值表 hash、批准时间与审批人角色 | 失败率、`agent.draft.degradation_total` 增量、首 Token P95、完成 P95 的阈值在开始前冻结 |
| 计数查询 | `rag.outcome_total` 固定查询及其 hash | 查询排除 replay，按 v2 Finalizer 的首次终态聚合；窗口起点的 counter 基线已记录 |

阈值表必须写出统计窗口、分母、百分位算法、告警条件和每项阈值；观察期间不得修改。环境、发布物、选择器、Telemetry mode、Collector 查询或阈值有任一变化，视为本次观察失效。

## 4. 每日执行

每天在固定时间运行同一组只读查询，并归档一个脱敏 evidence manifest。机器可验收格式固定为
`eino-stable-observation-day/v2`，字段至少包含：

```text
window_start/window_end
image_digest_sha256
config_sha256
telemetry_required_api=true|false
telemetry_required_worker=true|false
collector_metrics_api_visible=true|false
collector_metrics_worker_visible=true|false
collector_traces_api_visible=true|false
collector_traces_worker_visible=true|false
rag_v2_non_replay_terminal_delta
failure_rate_ppm
draft_degradation_delta
first_token_p95_ms
completion_p95_ms
backend_policy_sha256
threshold_table_sha256
fixed_query_sha256
collector_evidence_sha256
incident_flags
previous_manifest_sha256
reviewer_role
```

受保护 Collector 另为每份 evidence 写入 `collected_at`。启动 evidence 必须在首个观察窗口开始前完成；每日 evidence
必须在对应 `window_end` 后、且仍处于该观察时区的同一自然日内完成。`collected_at`、查询响应摘要和完整 manifest
一起进入 HMAC。超过该自然日后禁止补采历史窗口，不能在第 7 天结束后集中回填前 7 天数据。

执行规则：

1. 用冻结的 Collector 查询读取 API、Worker 的 Metrics 与 Trace，确认两个服务均持续可见；仅能记录可公开复核的聚合结果。
2. 将 `rag.outcome_total` 的窗口增量累加为非 replay RAG v2 终态数，不从日志、SSE、截图或手工表格补数。
3. 按冻结阈值检查失败率、`agent.draft.degradation_total` 增量、`agent.answer.first_token.duration_ms` P95 和 `agent.answer.completion.duration_ms` P95。不得为通过观察而在运行中放宽阈值、改变分母或重置 counter。
4. 核对事故台账：不得发生权限绕过、数据泄露、lease/fence 失效、Finalizer 终态错误或 Tool receipt/replay 不一致。疑似事件也必须先判为失败，完成根因结论后才可重新开始。
5. 将 manifest、Collector 查询定义、后端信任策略和阈值表的 hash 写入只追加的归档位置；原始查询导出若含
   Prompt、正文、ID、Provider 或模型，必须先在受控环境脱敏，不能归档。

### 4.1 机器验收格式

- 启动 manifest 使用 `eino-stable-observation-start/v2`；每日 manifest 使用
  `eino-stable-observation-day/v2`；受保护 Collector 作业的最终证明使用
  `eino-stable-observation-attestation/v2`。正式信任根固定为仓库内 canonical
  `deploy/eino_stable_observation_trust.json`（`eino-stable-observation-trust/v2`），命令行不能替换该路径。
- JSON 必须按 UTF-8、对象键字典序、无多余空白、末尾单个换行的 canonical 形式保存。文件 SHA-256 即
  `manifest_sha256`；每日 manifest 的 `previous_manifest_sha256` 必须指向前一份 canonical 文件，第一天指向启动
  manifest。archive SHA-256 固定计算为 ASCII 前缀 `eino-stable-observation-archive/v2\n`，再按时间顺序追加每个
  manifest 的 64 位小写 SHA-256 和换行。字段重排、补写、重复键、未知字段、非有限数值、符号链接、不连续日期或
  重复使用同一 `collector_evidence_sha256` 都会被拒绝。输入路径的任一父目录、每日目录和文件都不能是符号链接；
  受保护作业应传入解析后的真实绝对路径。
- 后端信任策略同时固定 Prometheus/Tempo URL 的 SHA-256 和四个受保护文件的绝对路径 SHA-256；采集器拒绝未批准的
  endpoint/token 文件路径、非 HTTPS、重定向、代理和 URL 查询参数。文件必须是非符号链接、`0400/0600`，其父目录
  不得可被组或其他用户写入。仓库只保存 `unconfigured` 的 null 摘要；真实 URL、Bearer token 和 HMAC key 只进入
  受保护 Secret/CI。
- 失败率在 manifest 内使用整数 ppm，避免浮点规范化歧义；`1%` 写为 `10000`。阈值表同步使用
  `failure_rate_ppm_max`。
- 信任策略默认是 `unconfigured`，此时正式命令即使收到自建 key 和自签 attestation 也只能返回 `incomplete`。
  验收器仍会先校验 attestation/key 可安全读取、schema、archive、时间和 HMAC 自洽；缺失、符号链接、权限错误或
  畸形证据返回失败，不能被 `unconfigured` 状态掩盖。
  开始真实观察前，必须用独立审批的仓库变更将策略设为 `configured`，固定批准签发者角色和一把高熵
  32-128 bytes HMAC key 的 SHA-256；密钥本身只进入受保护 Secret/CI，不进入仓库。启动 manifest 的
  `attestation_policy_sha256` 必须等于这份固定策略 canonical 文件的 SHA-256，策略变更会使当前观察窗口失效。
- 最终 attestation 必须绑定整个 hash 链的 `archive_sha256` 和上述 `attestation_policy_sha256`。签名输入是删除
  `hmac_sha256` 字段后的 attestation 对象，按相同 canonical JSON 编码并保留末尾换行的完整 bytes；验证密钥的
  SHA-256 和 `issuer_role` 必须分别匹配固定策略。密钥文件只允许 `0400` 或 `0600`，不得将密钥、endpoint、
  Provider 或模型写入归档、日志或仓库。
- 受保护作业必须直接执行冻结的 Collector 查询并生成 manifest/attestation，不能签名操作者手工提交的聚合数字。
  Collector evidence 使用 `eino-stable-observation-collector-evidence/v2`，并由独立 verifier 复核受 HMAC 保护的
  `collected_at` 是否落在对应每日宽限期内；即使重新计算有效 HMAC，逾期历史补采仍会被拒绝。
  每份 `evidence/*.json` 都由同一受保护作业用 HMAC 签名，并绑定完整 manifest（发布 hash、窗口、阈值、事故和前一份
  manifest）；每日追加前、最终 attestation 生成前和独立 verifier 验收时都会逐份验证证据目录、摘要、签名和链路。
  缺失、额外、被替换或签名不匹配的 evidence 都会 fail closed。HMAC 只能证明固定信任根批准的作业签发了归档，
  不能替代作业本身的访问控制、受保护分支和审计。

受保护作业按顺序执行 `start`、每天一次 `day`、窗口结束后的 `attest`。以下变量必须来自受保护 CI/Secret，真实值不得
写入命令日志或仓库：

```bash
# 零网络/零写入 readiness：验证策略、受保护文件、空归档、时区和三个发布 hash。
make eino-stable-observation-preflight

# 开始：同时需要 ARCHIVE_DIR、四个 Prometheus/Tempo URL/token 文件、TIMEZONE、
# IMAGE_DIGEST_SHA256、CONFIG_SHA256、EMBEDDING_GATE_SHA256 和 ATTESTATION_KEY_FILE。
# start 会先串行执行同一 preflight，成功后才查询 Prometheus/Tempo 并写 start/evidence。
make eino-stable-observation-start

# 每日：沿用同一归档、发布 hash、后端文件和 key，并提供当天 canonical incident evidence。
ZHIXU_EINO_OBSERVATION_INCIDENT_EVIDENCE=/protected/incidents/2026-08-12.json \
make eino-stable-observation-day

# 七个完整自然日且累计样本满足后，由受保护作业签发最终 attestation。
make eino-stable-observation-attest
```

`preflight` 不证明 Prometheus/Tempo 可达，也不产生观察天数或 evidence；它只证明正式 `start` 的本地输入已经
fail-closed 就绪。即使通过 `make -j` 调用 `start`，preflight 也必须作为有序 prerequisite 完成，不能与真实查询并行。
时区 readiness 包含最长归档范围内每个本地午夜的 UTC 往返与歧义检查，避免作业启动数日后才遇到不存在或重复的 00:00。
预检输出只允许固定的 `operation/status`，不得输出 URL、token、key、路径摘要或发布 hash。

正式稳定性验收使用：

```bash
ZHIXU_EINO_OBSERVATION_START_MANIFEST=/protected/eino-observation/start.json \
ZHIXU_EINO_OBSERVATION_DAILY_DIR=/protected/eino-observation/daily \
ZHIXU_EINO_OBSERVATION_EVIDENCE_DIR=/protected/eino-observation/evidence \
ZHIXU_EINO_OBSERVATION_ATTESTATION=/protected/eino-observation/attestation.json \
ZHIXU_EINO_OBSERVATION_ATTESTATION_KEY_FILE=/run/secrets/eino-observation-hmac \
make eino-stable-observation-verify
```

验收器见 `deploy/eino_stable_observation_verify.py`：它会独立复核仓库固定 backend trust、manifest 链和整个 evidence
目录，而不是只相信 attestation 生成时的检查。`passed` 返回 0，证据合法但天数、样本、已配置固定信任根或可信
attestation 未满足时返回 2，篡改、阈值超限、事故、漂移或格式错误返回 1。正式入口不接受 trust policy
覆盖；本地生成密钥、修改 manifest 或只让 HMAC 自洽，都不能在默认 `unconfigured` 策略下输出 `passed`。

## 5. 失败、重启与通过

下列任一情况立即使观察失败：开始前门禁失效、Collector 任一服务不可见、Telemetry 不再为 `required`、
任一冻结阈值超限、出现规定事故、证据缺失或发现 replay/本地流量被计入。

失败后：

1. 保留现有脱敏 evidence manifest 和故障分类，禁止删除或覆盖；
2. 修复问题并复验全部开始前门禁；
3. 生成新的启动 manifest；
4. 从复验完成后的下一个自然日 00:00 **重新计时**，旧窗口的天数和样本数均不得结转。

采集器在写入证据后、写入 manifest 前崩溃时，不得删除孤立证据；该 archive 必须封存并用新的空 archive 重新开始，
避免把未完成写入拼入下一条链。没有 RAG 终态的自然日允许记录零计数和零延迟，但最终仍必须满足 7 天及 100 个合格终态。

只有第 7 个完整自然日结束后，窗口累计至少 100 个合格终态、每日 manifest 与 evidence 齐全、所有固定检查均通过，独立复核人能复算 hash 和聚合查询，且正式 `make eino-stable-observation-verify` 返回 0 时，观察才通过。本 Runbook 不修改部署、不删除代码、不创建 Runtime 切换结论。

## 6. 与当前迁移状态的关系

项目已经实现 OTLP/HTTP Metrics 与 Trace Provider，并在 API/Worker Composition 中注入；这使真实 Collector 观察成为可能，
但不等同于 Collector 已可见、外部 HTTPS Embedding 已验证或稳定观察已完成；外部 Provider 门禁和本 Runbook
分别记录，不互相替代。
