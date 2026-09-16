# Source identity 独立只读审查

2026-09-16。按用户指定范围使用 go-review；未派发、未修改生产代码或测试、未运行模型/live/服务/数据库、未提交。结论针对审查时工作区；实施测试仍在并行完善。

## 发现：1 项 P2，未修复

**P2：全候选补源义务可能超过单次输出上限，使合法输入没有可接受的生成结果。**

- 位置：`internal/organizing/application/synthesis_contract.go:345` 遍历全部输入候选；`:348` 对没有输出的候选使用原 items；`:395` 拒绝任何未完成的补源义务。
- 输入允许 24 个候选，输出只允许 8 个笔记：`internal/organizing/application/synthesis_model.go:14`、`:15`；结果上限在 `synthesis_contract.go:172`，Provider Schema 和 decoder 也使用同一上限（`adapter/agent/synthesis_catalog.go:195`、`synthesis_wire.go:77`）。
- 可触发场景：普通 source-ready 输入含 9 个不同的未锚定笔记，每个已有一个可信 FACT，并共同引用同一已加载原始片段 S001；新来源 S002 与 S001 字节完全相同、身份不同，尚未记录于这 9 个槽位。该形状不需要超过来源、条目或字节预算，也不需要不合法 scope。生产候选读取可返回最多 24 项（`adapter/postgres/synthesis_read.go:23`）。
- 任意最多 8 个输出必然遗漏至少一个候选，触发新门禁；输出 9 个则先被 Schema/decoder/结果上限拒绝。因而该输入不存在能通过的结果，正常同文补源会失败，而不只是某一次模型遗漏。门禁在 Provider 调用后执行业务绑定，无法靠更好的 prompt 消除此冲突。
- 建议 main/实施代理协调必补目标数与批次/输出容量，保留所有已准入来源的补源义务及原有预算约束；不要仅忽略第 9 个候选。具体容量/分批方案涉及设计，故未直接修改。
- 证据等级：静态控制流及集合大小证明，未新增或执行 9 候选复现测试。

## 其余重点结论

- 版本类型配对：application 与 frozen Validate 共同使用精确映射；新普通/anchor/goal/body 分别 v7/v8/v9/v10，semantic v7；refresh generation 为空并实际选择旧 v5。prepare 在候选/admission、goal 或 refresh 完整确定后冻结。
- 生成和 Schema 独立：body v10 仍 delta v2、refresh v5 仍 v3、其余 v1，验证均 v1。owner 从已验证 frozen input 读取 stage 版本，ModelRun/Call proof 校验保持精确。
- 历史兼容：generation 字段 omitempty；空 generation + 空/v6 semantic 保持历史分支，新 generation 只增加生成 stage envelope，semantic stage 独立绑定其版本。READY、FAILED、unknown 的既有账本门禁未见被跳过；未发现本轮引入的额外付费重试路径。完整历史矩阵仍以实现方定向/迁移验证为准，不把源码检查等同实库证明。
- SQL：132 不回写旧输入，只替换契约匹配函数；新旧配对、stage、schema 与 Go 静态一致。已用文本比较确认 `atlas/schema.sql` 中该函数与 132 完全一致（仅 CREATE/CREATE OR REPLACE 声明差异）。未审查整份 schema 的既有 dirty 变更。
- 数组位置/slot：普通 `ApplySynthesisDelta` 只追加条目、原位追加来源或按语义匹配合并冲突来源，保留原 items/alternatives 顺序；REFRESH_ITEM 在普通请求先拒绝，refresh 又豁免新门禁，因此 `updated[index]`/slot 未发现越界或错位路径。
- 当前可信内容：仅扫描 `Revision.Items` 的 FACT/CONFLICT，不扫描 MachineItems；补源去重核对同 item 与 conflict alternative，合法 supplement 在 input.Validate 中另校验。错槽位不能满足义务。
- scope：incoming 必须属于 SourceEvent.Source；anchor 逐精确 IdentityKey 检查 AllowedSources。生产 prepare 会过滤没有准入片段的 anchor，Fusion 先过滤唯一目标；未发现新门禁扩大范围或绕过逐片段 admission。
- published body 候选：生产 ListSynthesisCandidates 返回当前版本，PublicationID 只附加其也可作为发布正文引用来源的证明，并不把候选变成仅可读上游。Fusion 的唯一目标过滤发生在冻结前；因此仅因有 PublicationID 而要求已准入同文补源，当前链路未发现独立误拒绝。9 候选容量问题不依赖该标记。

## 验证与限制

- 本审查定向 `git diff --check` 通过。
- 四包 `go vet` 通过：application、workflow、adapter/agent、adapter/synthesispostgres。首次命令因默认 Go cache 的沙箱写权限失败；改用 `/tmp/zhixu-identity-review-go-cache` 后退出 0。未启动任何服务。
- 已读取 `research/source-identity-implementation.md`，并核对 `/tmp/source-identity-unit.log` 的四包通过结果；复用其定向普通 Go 验证，未重复运行测试。
- 审查时 `/tmp/source-identity-pg.log` 尚有实施中的 fixture 失败：重复 content_artifact hash、普通 processing 不允许 goal binding；`/tmp/source-identity-body-pg.log` 尚无完成结果。这些不归类为生产代码缺陷，但不能据此宣称 PG 验证通过。由实施代理修正夹具并提供最终结果。
- 未执行真实模型、正式 DB、配置/rebind、服务或 commit/push。真实语义验收、最终 131→132 实库迁移/证明及正文版本不变验收由 main 与实施代理完成。

审查完成：确定问题 1 项，修复 0 项，开放 1 项。其余指定疑点未发现确定缺陷。


## 2026-09-16 容量 P2 独立复核（identity-recheck）

结论：原容量 P2 已按已确认的固定预算边界关闭，本次范围内待修确定问题 **0 项**。关闭含义是无法容纳全部必补目标时，在付费副作用前明确返回 `SYNTHESIS_MODEL_INPUT_TOO_LARGE`，不是自动分批支持 9 个目标。未修改代码、测试、服务、凭据或资料；仅追加本节，未派发或运行外部模型。

- 共享义务：`internal/organizing/application/synthesis_contract.go:340` 的计算同时供 preflight（`:410`）和输出 guard（`:465`）使用。遍历所有候选及当前可信 FACT/CONFLICT 槽位，以精确来源身份、incoming source、逐片段 admission、相同原文字节及同槽 supplement 判定缺失；没有截断第 9 个目标。已有 supplements 消除义务，但不被错误计入本次 Apply 的 statement 来源数组容量。
- 固定容量：`:417` 按目标数、需修改的 item 数和每槽已有来源与 missing 的并集检查 8/64/32 上限；`:433` 的动态规划允许逐槽 ADD_SUPPORT 或单次合并 ADD_CONFLICT。后者为全部 alternatives 加一条 relation 的语义检查成本，与 `adapter/agent/synthesis_semantic.go:108` 一致；原位合并行为与 `domain/synthesis_delta.go:152` 一致。最终累计不得超过 512。未发现本次范围内可确定的错误拒绝或集合漏算。
- 副作用：`adapter/agent/synthesis_model.go:162` 在 input.Validate 后调用 preflight；生成和语义入口均先经过 execution，之后才进入 invoke/LookupReady/Prepare/ModelRun/scheduler/Provider。容量拒绝不会建立新的模型步骤或付费调用；没有新增重试分支。
- 历史：共享义务在 generation 为空或 BodyRefresh 非空时返回空集合（`synthesis_contract.go:342`），历史输入不会因本次容量门禁产生新义务。容量修复自身没有改动请求哈希或账本恢复逻辑；8 目标换 attempt 重放由既有测试证明不增加 Provider 调用。并行中的 Fusion 版本化绑定与 live harness 不属于本次结论。

验证复用 `research/source-identity-preflight.md` 记录的两包定向 go test 和 go vet 成功结果，已核对 `adapter/agent/synthesis_model_test.go:629` 的公共入口断言：9 目标、65 FACT、8×33 双分支冲突拒绝时 Provider/scheduler/run/call/step 均为零；8 目标生成、独立语义接受和换 attempt 重放成功。旧槽位/来源身份/legacy 测试继续复用原记录。本轮未出现需新增定向执行的确定疑点，未重复跑测试、lint 或数据库。冲突合并 DP 和 statement 32 来源边界本轮为静态核对，现有新增公共入口用例没有单独覆盖其合法极限正例；不据此声称穷尽容量与输出字节预算的所有组合。

原 PG 临时限制更新：已读取最终 `research/source-identity-implementation.md`，并直接核对 `/tmp/source-identity-pg-corrected.log`（synthesispostgres PASS，16.556s）和 `/tmp/source-identity-body-pg.log`（postgres PASS，106.680s）。原报告“fixture 仍失败、body 尚无结果”的临时限制已失效。夹具修正没有放宽生产约束；最终记录覆盖 SourceReady/SQL 配对，以及 goal/body/manual/refresh 链路。131→132 旧 v6 proof 与 anchor 在初次合跑已通过的事实依据实施记录复用，本轮未重跑迁移。SourceReady 发布指针初始为 nil，不能将其当作已发布 Git 文件补源验收。

限制：固定 Provider/隔离 PG 只证明所执行的契约与调用链，不证明真实模型语义质量；本轮未验证真实模型、正式服务、正式数据库或全类型历史升级。上述两处测试覆盖限制不构成已确认缺陷。复核完成：原 1 项 P2 关闭，新增 0 项，待修 0 项。
