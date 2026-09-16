# 当前全文补源：中断恢复与显式重核接缝审查

2026-09-15；只读 go-review。仅本报告写入共享目录。依据 check manifest、PRD/design/implement、current-text-supplement-design 第5–7节和 current-text-source-review-contract；读取正式126当前代码及必要 Workflow terminal/lease 调用方。126仍在正式矩阵验证，本报告不将瞬时SQL/编译状态列为问题。atlas/schema.sql尚无本切片对象，实际约束以126为准。

## 结论

首次 origin 唯一、successor/recheck 未实现是已知范围，不重复记为新发现。实施应分开三个概念：不可变模型证明身份、当前恢复执行身份、显式新核验 attempt。不能直接重绑旧 workflow_run_id 或伪造旧 NodeAttempt/LeaseOwner。当前已有同 run 的 REVIEWED 重投递捷径，但没有跨终态 run 的恢复授权。

新增的具体风险为：已知模型失败未终结 ModelRun；prepare 临时错误抢先终结业务行；取消/中断没有 source-review terminal/reconcile 接缝。普通 apply 临时错误已被 REVIEWED 特判保护，不应误报为“所有 apply 重试都会被 Fail 提前终态”。

## 具体证据与最小修复

### P1：已知模型失败也遗留 RUNNING 账本，并被业务层归入未知恢复

- `internal/organizing/adapter/agent/synthesis_manuscript_source_review_model.go:90` 先提交 RUNNING claim，`:96` 另建 ModelRun；`:103–116` Recording/Runner 构造、Runner 执行、输出 bind 任一错误直接返回，没有失败 finalizer。`:120` 只有成功输出进入 scoped Complete。
- `internal/organizing/adapter/postgres/synthesis_manuscript_source_review_apply.go:151` 附近 FailSourceReview 将所有 RUNNING 错误改成 RECOVERY_REQUIRED，不读 Call 事实；因而即使已持久明确失败 Call 或确定协议校验失败，ModelRun 仍 RUNNING，显式 FAILED+retryable 永远无从产生。
- claim→CreateModelRun 之间进程崩溃还可能留下无对应 ModelRun 的 claim。不能仅因“查不到 ModelRun”就允许新调用：必须证明旧 attempt 已失效、创建事务结果已核清，且没有可能迟到的执行。

最小修复：增加 owner scoped failure-finalization 方法，在有界 WithoutCancel 上下文中锁原 review/ModelRun/Calls，区分确定失败、未调用、调用/写入结果未知。确定失败按现有 ModelRun 合同终结并与 review FAILED/error/retryable 原子保存；合法 UNSUPPORTED/UNCERTAIN 仍成功 ModelRun + REJECTED。未知保留 RECOVERY_REQUIRED，禁止普通 retry。不能把任意超时直接认定未调用。126当前 RUNNING 转移不允许 FAILED，需前向迁移配合；终结失败不得反过来释放新调用资格。

### P2：prepare 临时错误绕过实际 Workflow 重试裁决，提前变为不可恢复 FAILED

- `internal/organizing/workflow/synthesis_manuscript_source_review.go:25` 给 prepare/apply 配3次 retry；`:70–74` 却在每次 executor error 后立即调用 FailSourceReview。
- `.../postgres/synthesis_manuscript_source_review_apply.go:122` 起，PENDING/PREPARED 会直接 FAILED。`..._store.go:167` 附近 Prepare 对非PENDING、无snapshot返回 CONFLICT；于是首次临时读取错误即使被 Workflow安排 retry，下一次也无法准备。
- 真正重试裁决在 `internal/workflow/adapter/postgres/gorm_runtime_delivery.go:263–318`，retry_wait 分支未终结 Node，且发生在 Execute 返回之后。`application/synthesis_manuscript_source_review_validation.go:18` 所有 SourceReviewError 均为不可重试 consistency violation，不能用该 helper包住普通瞬时错误。
- 校正：`..._apply.go:161` 附近 REVIEWED 且非STALE会直接保留原行。因此普通 apply读/事务错误不会提前 FAILED；但耗尽后仍孤立 REVIEWED，见下一项。STALE 是安全阻断，不应强行按临时错误恢复。

最小修复：executor不负责抢先落已知业务终态；重试期间保留PENDING/PREPARED/REVIEWED，仅记录可选非终态诊断。真实 Node终态 hook负责最终分类。模型未知窗口仍可通过独立 owner-finalization及时归约。失败分类保留 errors.Is/As 和 retryable，不依据错误码子串决定整个恢复策略。

### P1：取消、进程中断、过期/耗尽后缺少业务归约及真实恢复执行

- `cmd/worker/main.go:1695` terminal chain无source-review；既有 `internal/organizing/workflow/scoped_terminal.go:28` 仅处理既有最终节点成功，`internal/organizing/adapter/synthesispostgres/terminal.go:19` 只接受 SynthesisExecutorNodeKinds。
- `internal/workflow/adapter/postgres/gorm_runtime_control.go:393` 和 `gorm_runtime_delivery.go:363,621` 有真实失败/取消终态通知；未进入 executor 或 executor被杀时，FailSourceReview不会发生。dispatcher `..._dispatch.go:21` 仅扫描没有review的origin，不能修复已有PENDING/PREPARED/RUNNING/REVIEWED。
- `gorm_runtime_claim.go:139–177` 将旧attempt标为lease_lost，再创建新attempt。`..._store.go:110–128` live要求真实当前run/node/attempt/lease及创建后30分钟；过期后即使持有已落盘 output也无法apply。
- `..._model.go:51–64` 支持同run已REVIEWED重放不调用provider；`..._store.go:286` Complete仍要求原model节点live及原attempt。`..._apply.go:36–47` Apply要求绑定run内真实apply fence。126 `guard_synthesis_source_review`（`:47–53`）冻结workflow身份和所有终态，RECOVERY_REQUIRED不能恢复为REVIEWED/SUCCEEDED。故“accepted output可恢复”目前只在原run仍可推进的有限窗口成立。

最小修复：新增 scoped terminal hook + 有界 reconcile，两者复用一套分类逻辑。以真实workflow binding和review版本核对事件，忽略旧run/旧attempt迟到通知；retry_wait不终态。对终态/取消且未调用者记FAILED（取消不自动retry）；未知调用记RECOVERY_REQUIRED；REVIEWED保留accepted bytes并投影可恢复/执行已结束，不伪称仍执行。deadline归约以持久deadline及当前workflow事实为准，不只在live调用时才检查。reconcile先锁workflow再锁review，与terminal hook锁序一致，避免新增反向锁死锁。

## 显式 successor 命令：可独立实施的合同

1. 应用命令仅收 workspace、origin/review ID、expected version、幂等键和操作类型；使用真实authenticated capabilities。服务器构建规范化request hash。事务先查同键receipt：同参返回原winner及最新effective view，异参冲突，不能先因“已经有successor”拒绝合法重放。
2. 锁origin序列及最新review；FAILED+retryable允许新attempt；STALE/REJECTED/SUCCEEDED要求真实可读取、可授权的baseline与上次不同。CURRENT成功同baseline直接返回已验证结果；同baseline拒绝/UNCERTAIN不再付费抽奖。RECOVERY_REQUIRED绝不创建模型successor。检查上限10和无active/uncertain竞争行。
3. 服务端捕获允许重核的新baseline，冻结在新review；创建新ID/attempt_no/supersedes_id、命令receipt、RuntimeStart及River binding同UoW。旧output、snapshot、成功/拒绝历史不改。
4. `..._baseline.go:46–57` 原来源义务绑定旧精确版本；`:98` admission仍验证原frozen.Anchor，`:122` snapshot继续用原Anchor。因此新批准scope不能只调用现有current就假定成功：需单独区分“历史generation proof使用原scope”与“新核验使用当前批准scope”，重新校验全部义务准入、禁止扩大原manifest。新source version走新source-ready，不能改绑旧origin义务；来源不可用不是一份可执行的新baseline。
5. snapshot不含请求nonce/attempt时刻；判断baseline变化必须比较owner实际内容/版本、许可scope、来源元组，不能通过新ID或Version制造变化。

## accepted output 恢复：独立执行身份，保留证明

建议新增 apply-recovery 定义（只有恢复/应用节点）及不可变 recovery execution binding，而非重跑三节点模型图。binding含 review ID、原proof workflow/node/attempt/model/output hash、新recovery workflow、恢复命令receipt与deadline。新run通过ScopedRuntimeStarter创建，Apply验证新run真实fence；verifyModel仍验证旧proof身份，不与新run身份混用。

- 已有REVIEWED：完整重验原ModelRun/Calls/output、旧双模型、当前baseline后原子apply；无需再finalize已成功ModelRun。
- 仅ModelCall response hash存在：没有accepted原字节不可恢复，留RECOVERY_REQUIRED，零provider。
- 若要覆盖“accepted bytes已独立持久但尚未Complete”窗口，应新增write-once accepted-output journal，与原claim和完整模型调用事实绑定；恢复时通过新fence核验并scoped finalize原ModelRun。当前Complete将output与ModelRun成功同事务落盘，因此当前实现没有独立durable accepted但ModelRun仍RUNNING的正常中间态。
- 原review终态不可变可通过恢复result/receipt独立表保存完成事实并供view归约；或者迁移明确允许持有真实recovery binding的RECOVERY_REQUIRED→REVIEWED/SUCCEEDED。二选一写死合同，不能仅放宽guard全局终态规则。
- 已提交SUCCEEDED receipt的精确恢复应先读验证持久闭包，返回历史事实；漂移仅改变effective_status，不要求复活旧lease或重开source才能承认历史提交。

## 前向 migration 必需范围

不改125，不覆盖本轮正在验证的126；新编号由主会话分配。

- origin唯一改为(workspace,origin_processing,origin_run,attempt_no)，增加attempt_no、supersedes_id同workspace/origin自引用与最新链校验、10次上限；active/uncertain部分唯一索引。已有行回填attempt=1。
- append-only命令receipt唯一(workspace,idempotency_key)，hash/operation/expected identity/result review绑定；恢复execution表独立保存新workflow授权，不修改原模型identity。
- 明确已知RUNNING→FAILED与受约束恢复状态转换；约束必须核对账本失败/accepted proof，禁止只有应用层boolean放行。
- 当前证据唯一键跨review，但Apply总是INSERT，closure按e.review_id计数（126:82、133）。引入successor后全/部分复用需result manifest引用existing evidence/proof review；核对完整source/target tuple，不能ON CONFLICT DO NOTHING充作成功。baseline A→B→A也必须复用A的真实proof或安全拒绝，不能因唯一冲突困住。
- SQL closure把“原模型proof workflow”与“授权本次apply workflow”分开核验；原ModelRun INSERT claim保护仍保留。新receipt/manifest对义务一一闭合，跨workspace、错误predecessor、伪fence、篡改output、缺evidence必须数据库拒绝。

## 实施文件边界

- 模型失败切片：`adapter/agent/synthesis_manuscript_source_review_model.go` + `application/synthesis_manuscript_source_review.go`的新failure port + postgres store scoped finalizer；不碰通用Runner。
- successor切片：新`application/synthesis_manuscript_source_review_commands.go`、postgres同前缀commands文件、既有baseline/read投影、workflow dispatcher启动复用；不改旧125remerge。
- 生命周期/恢复切片：新postgres同前缀terminal/recovery文件、workflow同前缀executor/definition、application恢复port；worker terminal composition和周期dispatcher是必需集成点。已有Workflow runtime提供真实hook、claim和starter，原则上无需改通用lease算法。
- migration/证据复用由单一owner整合，避免三个切片并发改guard/closure。HTTP/Web只消费最终command/view合同。

## 最小真实PG验收（本轮未执行）

1. prepare首次注入retryable存储失败，走真实River retry_wait→成功；冻结一次，只有一份新模型run。apply首次事务失败同样恢复，不新增provider。
2. 固定Provider明确失败/合法协议失败：ModelRun真实终态与review分类一致；FAILED retryable显式新attempt；未知Call/commit结果始终RECOVERY_REQUIRED，反复dispatcher/recover零额外provider。
3. Complete已提交但响应丢失/进程重建：同run重投递；再覆盖原run取消或耗尽后新recovery run实际claim→apply。严格断言新旧NodeAttempt不同、旧proof不改、provider次数不变。
4. claim后、ModelRun创建后、Call完成但accepted未落盘三个断点；实际lease失效/重领、取消pending和running、持久deadline到期。重读业务状态不永远RUNNING；旧worker迟到Complete/Fail不能覆盖新合法状态。
5. 两连接并发同幂等键返回同review/run/receipt，异参冲突；并发不同键只产生一个successor，旧键重放仍返winner；REJECTED/SUCCEEDED同baseline零调用，真实F变化后仅一次新调用，刷新保持旧历史+新有效性。
6. scope改变和source新版分别验证上述不改绑边界；A→B→A证据复用闭包，多义务部分复用与缺一条证据原子拒绝；直接SQL伪造binding/跨workspace/terminal改写拒绝。

固定Provider+真实PG/River只能证明状态、幂等、调用次数、约束和owner链。已知SUPPORTED 17.883s不证明模型语义，本报告也未运行编译、lint、测试或正式126矩阵，不替代core正在产出的最终证据。

审查完成：生产/测试/SQL修改0；以上3项具体风险未修改，已给实施接缝。done
