# 人工冲突 owner 裁决存储（00124 正式目录验收）

状态：owner 存储切片的限定真实 PG 验收通过。main 已将00124草案原字节集成正式 `atlas/migrations/00124_synthesis_manuscript_review.sql` 并统一hash；本轮 `cmp` 确认一致，未修改SQL/schema/hash。没有overlay、迁移替身、历史SQL改写或禁用trigger，没有提交/push/部署。

生产 HumanAuthority、Runtime HumanWait/HTTP/worker/UI及后继编排由另一切片负责；这里不调用 SubmitHuman 或发布。下面的PASS只对应明确列出的owner流程。

## 入口与职责

- `application/synthesis_manuscript_review.go`：`SynthesisManuscriptHumanBinding` 绑定 workspace/processing/实际 task/run/node/target version；`DecideSynthesisManuscript` 另绑定 note/attempt/capture、幂等 key、stage/fingerprint/全部 ordinal/最终正文。没有客户端 envelope、hash 或来源映射输入。
- `NewGORMSynthesisManuscriptReviewStore(store, HumanAuthority)` 缺少任一依赖拒绝。HumanAuthority 明确必需同事务当前 CallerCapabilities、真实 task/schema/processing/目标集、Root、L/P/Document/来源/scope 与历史模型日志证明。测试 fixture 只代表权限 seam，不是生产授权实现。旧 model/merge 节点不要求仍 RUNNING；后继 apply 负责写入候选。
- `Decide` 先授权并锁真实 HumanTask，再锁 attempt；同键同完整命令恢复原 stage result，异参拒绝。精确已存决定恢复在 F/owner 重验之前，首次阶段决定必须 VerifyPending + 当前文件 CAS。实际 pending task 的 workspace/run/node/version/status/expiry 同时由存储和 SQL 结构核对。
- ledger 最多两阶段，每阶段保存完整绑定、序号、前一阶段 hash 与真实 ResolveSynthesisManuscript 结果。重读逐阶段使用真实 Git merger/Mapper 重放全部决定；第一阶段出现外层冲突时只存 ledger，最终完成才同事务写 receipt。
- receipt 只新增 `Review *SynthesisManuscriptReceiptReview`（omitempty），旧 clean JSON 字段顺序与 hash 不变。resolved receipt 内嵌版本化 ledger，SQL 绑定有序不可变 owner 行；decodeReceipt（因此 VerifyReceiptScoped/apply）重算所有阶段再比最终 envelope。119 的 v2 revision/Article/model/apply/source 闭包不变。
- `ReadManifest` 从同 workspace/processing/WorkflowRunID 的真实 attempts 有界读取（复用 MaxSynthesisGeneratedNotes=8），验证同一冻结 input/generation，以 `manuscriptChangedNotes` 重算完整目标集。至少一条原始冲突且完整集合的每个目标都有receipt才 Ready=true；零行拒绝，缺任一目标/receipt 时 Ready=false；同 note 多 attempt 或混合 generation 拒绝，不能任意选择旧结果。clean target 读取原119 receipt；仅预览不产生权限。

## 实库验收结果

正式命令：

```sh
GOCACHE=/tmp/zhixu-manuscript-review-gocache go test -mod=vendor -tags=integration ./internal/organizing/adapter/postgres -run '^TestSynthesisManuscriptReviewPersistence$' -v -timeout=60s
```

最终结果：`TestSynthesisManuscriptReviewPersistence` PASS（12.41s；包13.355s），日志 `/tmp/manuscript-review-124-test.log`。使用Testcontainers隔离真实pgvector PostgreSQL16及真实Git merger，正式Atlas目录和主hash；成功命令为完整包定向测试，并未排除其他源文件。

已实际执行：

1. 既有合法119 capture/attempt与真实已发布P；实际task尚未创建时裁决拒绝。调用Workflow `GORMRepository.CreateHumanTask`，真实事务将node/run转为HumanWait并保存pending task。
2. 单阶段真实Git冲突：未决manifest不ready；当前owner fixture拒绝、缺ordinal、错误fingerprint、F漂移均拒绝；两个并发同key同输入返回完全相同decision；同key异正文与跨workspace拒绝；重开恢复相同receipt。owner/F后来漂移不影响精确已提交decision恢复。
3. 不可变decision UPDATE拒绝；单阶段resolved receipt通过实际VerifyReceiptScoped，再复用119/121合法v2 Article/revision/reservation/apply-receipt闭包持久化含人工全文L，没有绕过trigger。
4. 以上真实v2 L与不同磁盘F、最新机器增量构成真实Git内层冲突。stage1决定保存后返回外层新Review，无最终receipt、不ready；同stage1命令重放一致，stage1旧fingerprint不能裁决stage2。stage2成功后精确链入stage1 hash并产生最终receipt，读取重放两阶段ledger一致。
5. 同一冻结generation/input含第二个实际既有note。第一个两阶段receipt完成、第二个尚未prepare：manifest仍不ready。第二个clean attempt存在但未seal：仍不ready。补齐原119 clean receipt后两个note的完整集合才ready，clean receipt不含Review。重新构造Store/ReviewStore后完整manifest精确相同。
6. 最终两阶段receipt经过实际 `VerifyReceiptScoped` 校验。seal之后F再次漂移，后继apply的scoped校验拒绝；历史manifest/receipt读取仍精确一致。

静态验证：最终 `go vet -mod=vendor -tags=integration ./internal/organizing/application ./internal/organizing/adapter/postgres` 通过（相同临时GOCACHE；`/tmp/manuscript-review-124-vet.log`），`git diff --check`通过。先前application纯两阶段Git测试通过。没有扩大到全仓测试。

## SQL与版本兼容

00124新增不可变decision表及workspace/key、attempt/sequence唯一键；canonical bytea/原字节SHA256；真实pending HumanTask/run/node/version/expiry；全部阶段fingerprint/Ordinal身份；receipt精确绑定有序ledger；最终阶段与receipt deferred原子闭包。119 v2 revision/Article/model/apply/source闭包不变。草案与正式SQL目前完全相同，本轮无需main重新hash。

ReviewTargets直接复用既有MaxSynthesisGeneratedNotes=8。HTTP只能暴露manifest身份/状态投影，全文按选中且授权的note单独返回，不能把内部manifest大对象整包传给前端；SQL与owner payload上限保留。

## Fixture边界与未验证范围

- 当前capabilities/Root授权是显式 `manuscriptHumanAuthorityFixture` seam，严格绑定单一HumanTask；其底层 `manuscriptProofFixture` 重验真实note current及来源fence，但模型/semantic/RootGrant身份是fixture事实，不能当作生产授权证明。
- HumanWait由真实Workflow repository创建并提交其合法状态转换，使用已有legacy fixture run/node；没有运行完整RuntimeHumanCoordinator/River/HTTP/UI用户流程。不会宣称完整UI/运行时或生产HumanAuthority完成。
- 最终两阶段receipt做了实际scoped候选校验；没有再次创建或发布两阶段最终候选。测试中先前v2候选持久化用于建立合法L，不是生产apply编排验收。固定fixture不是全文生产模型质量证明。
- 实际F漂移、跨workspace、owner fixture拒绝有证据；生产capabilities撤销、真实Root替换、L/P/scope变化各自的端到端行为属于生产Proof切片，未在这里冒充已验。

## 并行接线通知

root后续明确授权后，已将 `synthesis_manuscript_human.go` 唯一attempt查询中的历史JSON字段名纠正为 `WorkflowRunID`，保留原workspace/processing/run三个条件。没有改其他runtime逻辑、公共接口或00124 SQL。

初次试跑时另一代理的runtime签名处于更新中导致包编译失败；之后已能使用完整包命令成功跑完上述测试，早期编译阻塞不再是本owner验收限制。曾准备过排除尚未接完runtime测试的显式源文件命令，但其编译也失败，**未用它产生PASS**。

Channel CLI曾因本会话沙箱无法写 `~/.trellis/channels/.../*.lock` 被拒绝；research通知与最终响应承担交接。独立审查如发现owner范围实际问题，按本切片继续修复，不将当前结果扩大为整个产品闭环完成。

## 同processing新run恢复回归（后续修复）

`RecordSynthesisRetryScoped` 保留processing ID但切换run；ReadManifest原先只按processing查询会混入旧run。现已在LIMIT之前附加 `prepared.generation_input.WorkflowRunID=binding.RunID` 精确过滤，字段使用历史PascalCase；之后完整冻结generation的目标重算、一致性比较、全部receipt门禁保持不变。历史attempt/receipt不删不改。

正式相同命令的新增回归PASS（13.47s；包14.338s），日志 `/tmp/manuscript-review-124-retry-test.log`：第二轮generation复用第一轮processing ID、另一个真实run/HumanTask；数据库保存旧run1条与新run2条attempt共3条，新run两阶段/多目标manifest正常完成，旧attempt与旧manifest也保持精确相同。此为真实owner存储/HumanWait恢复验收，未调用完整生产Retry Coordinator；fixture权限边界同上。

本次vet曾被并行 `adapter/synthesispostgres/processing.go:178` 暂缺time导入阻塞；本owner不改该文件。实际PG测试已在可编译快照通过，最终静态检查以随后结果为准。SQL与hash均未变化。
