# 00119 存储第一批：实现与接线边界

## 已实现的窄 API

`application/synthesis_manuscript_store.go` 与 `postgres/synthesis_manuscript_store.go`：

- `Prepare(ctx, PrepareSynthesisManuscript)`：命令只有 workspace/note/processing/idempotency key。必需 `SynthesisManuscriptBaselineReader` 读取 server generation + L/P + scope/owner/path/grant。拒绝 baseline 提供 F；先在事务 scope 核对完整 authority，再通过受信 `CurrentContent/EnsureTargetAbsent` 捕获文件，调用纯 `PreviewSynthesisManuscript`。落库事务重新 proof + F CAS，原子保存 capture 与 attempt。同键重放只恢复原精确 command。
- `GetAttempt(ctx, workspace, id)`：按工作区重读 immutable capture/Input/Preview，校验 canonical payload、hash、身份、实际 merger/fingerprint 和 mapper。当前 scope/F 漂移不影响历史恢复。
- `SealClean(ctx, workspace, attempt)`：锁 attempt；精确已提交 receipt 优先恢复，首次 seal 才重验当前 authority/F。只能接受无 Review 且有 Manuscript 的 preview；冲突 fallback 不能 seal。
- `VerifyReceiptScoped(ctx, scope, workspace, receipt, revision)`：重读 proof/parser/receipt，绑定 v2 全文、可信 Items、L parent/编号、确切 delta/source event/workflow/model，重验当前 authority/F。不是 standalone commit，未来 apply 同一 UoW 使用。

`SynthesisManuscriptAuthority` 使用具名字段；`SynthesisManuscriptPrepared` 保留完整既有 `SynthesisGenerationInput/Result`、独立 semantic ModelRun/hash 和纯 merge input。没有 arbitrary map 或客户端 verified flag。缺任一 reader/proof/mapper/merge/files/clock/IDs 依赖，构造拒绝。没有生产 proof 实现或生产 composition。

## 00119 数据形式

- 三张表：`synthesis_manuscript_capture/attempt/receipt`。payload 用 bytea 保存 canonical JSON 原字节，`payload_hash=SHA256(payload)` 的 SQL CHECK 保证物理字节；不是用 JSONB 重新序列化冒充原 hash。capture 的 `Bytes` 是 JSON base64，SQL decode 后检查 UTF-8/NUL/1 MiB/content hash，present-empty 和 absence token 两个互斥分支。
- capture SQL payload 上限 1,500,000 bytes（含 1 MiB base64 与元数据），attempt/receipt 各 16 MiB；Go 同样限制编码记录大小。所有行 append-only、防 truncate，workspace 复合 FK、同 workspace 幂等 key、每 attempt 唯一 receipt。
- attempt SQL 将 file bytes、path/grant/binding、L 真实 revision 身份、processing 与 preview 绑定；receipt 必须等于对应无冲突 preview manuscript。
- revision 新 nullable `manuscript`、`manuscript_receipt_id`。v1 扩展均 NULL，仍要求至少一个 Items；v2 可零可信 Items，必须有确切 receipt。`validate_synthesis_revision_manuscript` 校验 envelope/trusted subset/parent/编号/delta/event/model/workflow；新的 deferred `verify_synthesis_manuscript_closure` 还核对实际 apply receipt 的 processing/request/output hash 和 Authoring generated content/projection hash。保留旧 source/body reference/Authoring closure。
- `synthesis_models.go` 序列化 envelope，重读用真实 `manuscript.Mapper` 重做映射，domain `Content()/Validate` 重验哈希/子集。receipt ID 是不可变数据库证明索引，不是新的正文来源；SQL 要求对应 envelope 与全部来源身份相同，不能通过换 receipt 改变任何已哈希正文。模型 record 不自动生成或推测 receipt ID，漏绑定 v2 INSERT 必拒绝。

PG 不执行 Goldmark 或当前 owner/source/model proof。它约束 immutable 数据的闭包，受信 owner 在写入时重新校验；这不证明拥有任意数据库写权限者不能伪造一整套 proof。没有以数据库 JSON CHECK 代替应用语义验证。

## 未接线，不能宣称已启用的能力

1. 没有新的生产 Workflow definition、HTTP、HumanWait/resolve、Authoring F/P 双基线预约、自动 v2 candidate apply。首批仅已有 L；新建初始 note 仍 v1。冲突可持久恢复，但不能裁决/发布。
2. 必需的 BaselineReader/Proof 尚无生产实现。未来必须组合真实 generation/semantic journal、source/span/scope fence、L/latest/Document/P 祖先与 publication proof、pending Proposal/retirement 和 root grant，而不是复用测试 fixture。
3. **当前执行许可与冻结模型身份必须拆开**：Prepared.GenerationInput 的 NodeRunID/NodeAttemptID 是冻结模型身份；将来 `merge_review → HumanWait → apply` 时不能要求那个旧 attempt 仍 RUNNING。现有 proof port 只是未接线接口，不能从 context 或 DTO 假造当前许可；生产接线前应向 scoped 调用另传具名当前 `workflowapp.ExecutionContext`（或等价窄 execution binding），分别验证当前 caller lease/capabilities 和旧 immutable model journal。当前 fixture 未覆盖跨节点恢复。
4. CurrentContent 是安全文件读取原语，不取代业务授权。Store 在读取前与持久化前调用 scope proof，并比较独立返回的 path/grant/root/binding authority；真实 runtime grant resolver/lease 必须由生产 composition 提供。
5. runtime repository 读取新增列后，integration runtime fixture 最低迁移版本已升至119；历史专用迁移夹具保持自身版本，不修改历史 v1 数据/hash 契约。主 schema 导出与主 atlas.sum 由 root 协调；下文保留初次临时目录证据，并由后续正式目录验收补足。

## 本轮验证范围

定向真实 PostgreSQL + 文件 + Git merge：`TestSynthesisManuscriptStorageCaptureReceiptAndV2` 与 `TestSynthesisManuscriptCaptureAbsenceShape`。跨 owner Proof 为明确 fixture（真实 source fence、真实已发布 P；模型/semantic/RootGrant 身份来自 fixture），所以只证明保存恢复和文件/数据库闭包，不证明生产模型或授权执行图接线。

覆盖 v1 原投影重读；clean attempt 重启 exact 恢复；F/proof 首次 seal 漂移拒绝；模拟提交成功响应丢失后精确 replay；已 seal 后 F/proof 变化仍可恢复旧 receipt；跨 workspace/同键不同 note 拒绝；真实 Git 同处冲突可恢复且不 seal；空文件/NUL/超限/已发布文件消失；无 P 真实 absence 与之后文件碰撞；不可变 mutation/truncate、校验和篡改、重新计算 hash 的伪 preview 拒绝；fixture 内 v2 + generated Article + reservation + apply receipt 完整事务、缺 receipt 拒绝、全文/零可信项重读。

最终命令/结果以 `/tmp/manuscript-store-119-final.log`、`/tmp/manuscript-store-119-vet.log` 为准。测试使用 `/tmp/zhixu-manuscript-119-overlay.json` 将 Atlas embedded directory 指向复制的 1..119 与独立 checksum。未修改历史迁移、未导出主 schema、未提交/发布。

后续正式目录验收：修复并发 Prepare 的23505提前分类问题，并新增 barrier 控制的实际并发同键测试。正式迁移目录与主 checksum（119+120）下存储两项测试通过15.211s，日志 `/tmp/manuscript-store-119-review-fixed.log`；BodyRefresh 回归48.762s，日志 `/tmp/manuscript-store-119-body-refresh.log`；integration vet通过。独立复核关闭原P2，见 manual-storage-review.md。该结果替代临时目录作为当前最终验证依据，生产接线限制仍然适用。
