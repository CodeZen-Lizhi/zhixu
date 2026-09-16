# 主笔记真实模型质量检查设施

2026-09-16 更新：真实 DeepSeek 默认推理模式在旧 2048 token 验收上限下截断，安全诊断确认为 `finish_reason_length`。生产 Worker 的 `agentStructuredMaxOutputTokens` 原本为 8192；验收工具现与其对齐为 8192，不修改生产配置或降低语义断言。五场景仍最多 12 次调用/10 分钟（总请求输出上限 98304 token），当前全文仍最多 4 次/5 分钟（32768 token）。未启用入口仍不调用外部模型，单次实际用量由 Provider 返回；历史离线验证中的2048预算为当时配置，不是当前上限。

2026-09-15。仅新增 `internal/organizing/adapter/agent/synthesis_live_test.go`；不修改生产算法、runtime、HTTP、Web、SQL 或公共 store。

## 运行边界

`TestSynthesisLiveQuality` 使用 `models.NewConfiguredModelRuntime` 的生产 Chat adapter、真实 Eino structured scheduler、`NewSynthesisModel`、生产 prompt/schema、服务端 S/N/I label 绑定以及另一节点/ModelRun 的真实独立 semantic 调用。模型日志 owner 使用已有 `synthesisMemoryStore`；目标/来源准入、当前修订和选择记录是明确的合成 fixture；四个已有主笔记场景显式设置SourceEvent.Fusion，Goal场景保留独立初版生成路径。

这不是 PG/River/权限/范围建议审批/发布/Git/浏览器验收。Redis 场景故意把 MySQL 原文作为可读取的合成材料交给既有 Redis 目标，检查生成内容是否尊重范围；不冒充真实关联审批。Goal 场景直接提供已选择原文，不验证前置检索/知识目录选择能力。没有读取个人笔记、生产 DB、`.env` 或搜索凭据。

## 命令

默认及离线检查（无外部 provider）：

```sh
env GOCACHE=/tmp/zhixu-synthesis-live-go-cache go test ./internal/organizing/adapter/agent -run '^TestSynthesisLive' -count=1 -v
env GOCACHE=/tmp/zhixu-synthesis-live-go-cache go vet ./internal/organizing/adapter/agent
git diff --check -- internal/organizing/adapter/agent/synthesis_live_test.go .trellis/tasks/09-12-knowledge-anchor-synthesis/research/synthesis-live-quality.md
```

main 通过已知入口将配置放入环境后，可运行：

```sh
ZHIXU_EINO_LIVE_SYNTHESIS_ENABLED=true \
ZHIXU_EINO_LIVE_SYNTHESIS_ARTIFACT=/tmp/synthesis-live-review-0915.json \
GOCACHE=/tmp/zhixu-synthesis-live-go-cache \
go test ./internal/organizing/adapter/agent -run '^TestSynthesisLiveQuality$' -count=1 -v -timeout=11m
```

必需已有环境变量：`ZHIXU_EINO_LIVE_BASE_URL`、`ZHIXU_EINO_LIVE_API_KEY`、`ZHIXU_EINO_LIVE_MODEL`。可选 `ZHIXU_EINO_LIVE_MODEL_VERSION`，默认为 MODEL；生产契约要求与 provider 响应的实际 model 一致。可选 `ZHIXU_EINO_LIVE_TIMEOUT` 为正 duration 且不超过 90s，默认 90s。不读取 `ZHIXU_CHAT_*` 作隐式 fallback，不加载 `.env`。启用值只接受 true/false，未设置/false 明确 Skip；启用但缺配置立即失败，不等待或重试查找。

artifact 文件必须尚不存在；目录须已存在。使用 `O_EXCL|O_CREATE` 和 0600 创建，拒绝覆盖既有文件或 symlink。不要把真实凭据写进命令或报告。

## 场景与人工判据

| 场景 | 自动检查 | 仍需 main 人工核验正文 |
| --- | --- | --- |
| redis_rejects_mysql | 只允许既有 Redis note；无新语义、来源或条目变化 | 没有借 MySQL 扩大 Redis 目标；无正文变化时 artifact 保存原正文 |
| mixed_interview | 一个新主笔记、生产结构与精确来源绑定、真实独立 semantic | Redis/MySQL/Oracle 三模块覆盖，保留面试语境，不混入 ORBIT 招聘经历或帆船比赛；不能仅因引用同一混合原文就视为相关 |
| duplicate | `ApplySynthesisDelta.Changed=false` 且 `SourcesChanged=true` | 原结论/条件不被改写，新增来源只支持相同结论；此处不验证持久正文版本或补证 ledger |
| different_conditions | 新来源在带非空 applicability 的事实/冲突观点中出现 | 稳定值五分钟与快速变化值一分钟分别保留对应条件，不交换、不泛化 |
| opposing_unknown | 未裁决冲突至少双方观点，原/新来源均在冲突中 | 五分钟/一分钟相反观点各自对应正确来源，旧条件保留，新来源未说明条件明确未知，不编造条件、不自动选赢家 |

自动检查不依赖精确自然语言文本；未知条件可以有不同自然语言表达，因此不以空字符串/关键词作为语义真值。模型 SUPPORTED、结构通过、来源 tuple 正确均不能证明上述人工判据。真实调用通过后仍输出 human review REQUIRED；main 须检查实际正文才可更新质量结论。

## 硬预算与失败

五个小场景顺序执行。正常生成+semantic 共 10 次 Chat；同一 wrapper 将全部 INITIAL/REPAIR/REDUCED 计入总计，最多 12 次 Chat，第 13 次在 delegate 前拒绝并取消整个 suite。每个生产 profile/request 最大输出 8192 tokens，总请求输出额度最多 98304 tokens；provider 报告单次输出超限也取消失败。此限制依赖 provider 遵守 wire cap，不能在收到响应后撤销已计费的超额生成。

沿用生产有限三阶段 structured graph，每阶段 runner 总超时 2 分钟；单次 Chat 默认/上限 90s；整个 suite context 10 分钟。取消后 in-memory finalization 使用现有生产最多 5s 收尾 context，故 go test 外层 11 分钟只提供退出余量，不允许额外调用。首个失败场景后停止后续场景，无外围 retry/repair 循环。生产 Chat port 不在内部重试或切换模型。

## artifact 格式

JSON 顶层包括 `HumanReview`、`CallTimeout`、`MaxOutputTokens`、`Calls`、`Cases`、`Responses`、`CallDiagnostics`：

- Cases：场景名、生产 label 输入投影、绑定后的 Generation、独立 Semantic receipt、应用后的 Items 与渲染 Bodies、失败阶段。
- Responses：按实际调用顺序保存返回 content 字符串，包括非 JSON content（若 adapter 返回）。不是 HTTP body、headers 或 request dump；不含配置、endpoint、API key。生产 adapter 若拒绝响应且不返回 content，只能保存失败阶段，不能伪造原始模型响应。
- CallDiagnostics：每次实际调用的毫秒耗时、已校验token用量及固定脱敏错误分类，不包含请求头、原始错误或凭据。
- 真实失败时仍写已收集的部分artifact。`ErrorStage`记录generation/semantic错误；结构合法但业务断言失败时记为quality_assertion，避免产物看起来成功。

合成输入、模型原文和正文只写可选 artifact，不输出到普通测试日志；真实 provider 的 error detail 被隐藏。没有 artifact 时只能证明程序检查，人工内容质量仍未检查。

## 历史离线验证（2026-09-15）

上述定向 go test 最终 PASS（包 0.175s）：LiveQuality 明确 SKIP；Preflight 五套合成输入与调用/token 硬限 PASS；TransportBudget 经本地 httptest + 生产 adapter 验证 json_schema 与 max_tokens=2048，PASS。该假响应只用于传输检查，不作为 live generation 结果。包级 go vet 退出 0，gofmt 已执行；新文件 no-index whitespace check 无诊断（diff 因文件新增退出 1）。

当前未实际调用任何外部 provider；不能宣称真实整理质量通过。系统默认 Go cache 曾被沙箱拒绝访问，后改用上述独立 `/tmp` cache；没有提高权限。本地 transport 夹具初次 model version 不匹配已修正为生产契约要求的实际响应 model，并以上述最终 PASS 覆盖。

## 当前全文补源：独立真实模型验收入口（2026-09-15）

新增 `TestSynthesisLiveSourceReviewQuality`，与原五类 v1 生成场景分别启用；原场景、默认开关、12 次预算不变。新入口复用 `synthesisLiveConfig`、生产 `NewConfiguredModelRuntime`、`synthesisLiveChat` 的预算/原始响应收集及 artifact 结构，通过 `RegisterSourceReviewRuntimeCatalog` → `NewSourceReviewModel.Review` → 真实 Eino structured scheduler → `RecordingChatModel` → 生产 Chat adapter 执行。

下列命令供完成当前已批准的质量验收、且可用模型配置已经存在时使用；**本次未运行此真实模型命令**：

```sh
ZHIXU_EINO_LIVE_SOURCE_REVIEW_ENABLED=true \
ZHIXU_EINO_LIVE_SOURCE_REVIEW_ARTIFACT=/tmp/source-review-live-0915-new.json \
GOCACHE=/tmp/zhixu-synthesis-live-go-cache \
go test ./internal/organizing/adapter/agent -run '^TestSynthesisLiveSourceReviewQuality$' -count=1 -v -timeout=6m
```

复用前述 `ZHIXU_EINO_LIVE_BASE_URL/API_KEY/MODEL` 必需配置，以及可选 MODEL_VERSION/TIMEOUT；不读取文件或寻找凭据。新开关未设置/false 时 Skip，仅接受 true/false。新 artifact 环境变量必填，路径不可留空或含首尾空白，目录须已存在；运行前以 `O_EXCL|O_CREATE`、0600 创建，不能覆盖现有文件或 symlink。配置缺失/无效或 artifact 创建失败时不调用模型。

### 两个合成场景及失败条件

每个场景包含完整的两段当前正文、与之分离的历史 statement/applicability、两条独立来源身份及 O001/S001、O002/S002 两条核验义务。来源内容为人工编写的合成材料，不是用户笔记。

- `current_full_text_supported`：历史为稳定缓存五分钟，当前全文改为快速变化缓存一分钟，另一段明确当前范围并排除旧规则；两条新来源完整支持当前全文。每条义务必须 SUPPORTED，并包含实际当前结论段 N001P001；允许同时选择确有证据支持的上下文段。任何拒绝/不确定、漏义务、错来源/段落绑定、只选择上下文却遗漏当前结论，均失败。
- `old_local_claim_overridden_elsewhere`：第一段仍逐字保留旧五分钟结论，但第二段明确对整篇（包括前段）撤销该规则，将前段限定为历史引文，要求当前所有缓存一分钟；两条新来源只有旧五分钟结论。任一义务被判 SUPPORTED 即失败；UNSUPPORTED 或 UNCERTAIN 均只表示不能补源，不冒充完成。绑定失败、结构失败、模型调用失败也失败。

live 路径没有固定 verdict 或 mock Chat。自动结果仍需要人工结合全文、原始来源和模型输出审阅；通过两例不能推断一般语义质量。

### 预算、产物与业务边界

新入口共享最多 **4 次 Chat**（正常两例各一次），INITIAL/REPAIR/REDUCED 均计数；第 5 次在 delegate 前拒绝并取消。每次请求最大输出 **8192 tokens**，总请求输出额度最多 32768 tokens；报告的单次输出超限也取消失败。单次配置超时最多 90s，每个 runner 最多 2 分钟，共享调用 context 最多 **5 分钟**。生产取消后最多 5s 的内存失败收尾不允许新 Chat；外层 6m 只留退出余量。首失败停止后续场景，没有外部 retry；生产 graph 仅保留既有有限结构修复，不为合法拒绝重试。wire cap 的实际计费约束仍依赖 provider 遵守。

artifact 新增 `SourceReviews`，逐例保存 `Input`（全文、历史提示、段落/hash、来源原文/身份和义务）、预期、实际 `Result`、`Bound`、`Accepted` 与失败阶段；共用 `Calls`/`Responses` 按调用顺序保存原始 Chat content（包括返回的非 JSON 内容）。失败时仍写已收集部分。输入和原始输出不进普通测试日志，不保存配置、headers、endpoint、key；adapter 未返回的被拒响应内容无法保存，不伪造。原五例 artifact 多出空的 SourceReviews 字段，不改变原有字段语义。

使用最小内存 SourceReview owner fixture 和既有 synthesisMemoryStore；fixture 的 Complete 只存原始输出并运行生产 Bind，不预置判断。它不模拟 PG 原子终结 ModelRun、不校验真实文件授权/版本、不 apply evidence，内存 REVIEWED **不是业务补源完成**。这不是 PG/River/权限/发布/Git 或正文版本不变的验收。

### 最终离线证据及退出码

```sh
env ZHIXU_EINO_LIVE_SYNTHESIS_ENABLED=false ZHIXU_EINO_LIVE_SOURCE_REVIEW_ENABLED=false \
  GOCACHE=/tmp/zhixu-synthesis-live-go-cache \
  go test ./internal/organizing/adapter/agent -run '^TestSynthesisLive' -count=1 -v
env GOCACHE=/tmp/zhixu-synthesis-live-go-cache go vet ./internal/organizing/adapter/agent
```

最终定向 test **退出 0**（包 3.223s），两个真实模型入口明确 Skip；包级 vet **退出 0**，gofmt 已执行。初次 test/vet 因测试 usage 的 int/int64 类型不匹配退出 1，已修复并由最终结果覆盖。

- `TestSynthesisLiveSourceReviewPlumbing`：仅本地固定 Chat，走同一个生产 Review/catalog/Eino/Recording 路径；检查实际发出的 Chat message 包含完整 payload，逐字核对 `full_content`（含未选择的第二段）及独立的 `untrusted_historical_statement`。支持和拒绝各一次 Chat，ModelCall 日志存在，两条义务的 source/paragraph/verdict 均绑定正确，schema 和 2048 输出预算保留。固定输出仅证明工具接线，绝不作为真实语义质量证据。
- `TestSynthesisLiveSourceReviewBudget`：4 次后第 5 次零 delegate；超额请求零 delegate；provider 报告超额在一次后取消；已过 deadline 零 delegate，且共享 deadline 不超过 5 分钟。原五例 preflight 及本地生产 transport 的 json_schema/max_tokens 检查继续通过。
- 缺配置拒绝检查：显式设置 `ZHIXU_EINO_LIVE_SOURCE_REVIEW_ENABLED=true`、`ZHIXU_EINO_LIVE_BASE_URL=`，定向运行新 Quality 入口，**预期退出 1**，报 `ZHIXU_EINO_LIVE_BASE_URL must be set and canonical`。在读取后续凭据、构造 runtime 或创建 artifact 前失败，无网络调用。

已按 go-review 轻量自检调用/取消/资源关闭、预算与失败路径、固定输出和真实入口隔离、artifact 不覆盖及不泄漏配置，未发现剩余范围内问题。**本次没有运行外部模型，没有真实语义质量结论。** 未修改 remaining-acceptance、journal 或任何生产代码。

### main 复核后的两点收口

正例使用 `slices.Contains(check.Targets, "N001P001")`，合法的 P001+P002 不会因多选上下文而误报；其他标签继续由生产 Bind 校验。Plumbing 进一步直接在同一条实际 Chat message 上断言完整 FullContent 的 JSON 字符串编码和历史 statement 的 JSON 字符串编码均存在，且两者不同；不只检查字段名或同一个 Build 函数的结果。

本次定向复验 `-run '^TestSynthesisLiveSourceReview' -count=1 -v`，两个 live 开关显式 false、GOCACHE 使用上述 /tmp 路径，最终退出 **0**（包 0.927s）：Quality Skip、Plumbing/Budget PASS。包级 vet 退出 **0**。未运行外部模型，未新增文件或测试矩阵。

## 2026-09-16：DeepSeek 实际配置与有界诊断

最新结果详见 `deepseek-chat-activation-20260916.md`。按保存的30秒、8192输出及原思考强度配置：全文补源两例已通过；最新五类融合也全部通过，10/12次调用36.71s，无格式repair。main已人工检查原文、模型输出、实际条目和正文；私有产物 `.zhixu/diagnostics/deepseek-synthesis-890bca50b5.json`。生成使用明确完整Schema的冻结新版本，Fusion复核补充唯一目标、scope与当前可信既有来源资格；旧版本保持原身份。此前超时、格式及来源误判的失败产物保留在上述报告中。

通过这些合成场景不等于正式服务已启用或用户文件已写入。API/Worker仍因目录身份冲突停止，重新绑定待用户确认；新迁移尚未部署，模型desired10未激活。
