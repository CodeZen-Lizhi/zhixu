# 人工完整稿：domain v2 revision / snapshot 契约

2026-09-15 实施。此文档描述实际 domain 代码，不代表持久化、合并应用服务、发布或前端已接入。

## Mapper 最终裁决与实际限制

接受跨项语境 P2：AST comment/list/comment 和文档骨架相等只能证明结构，不能证明人工句子语义仅限当前项。没有已授权且已实现的显式局部编辑语义契约，因此任何最终全文相对机器全文的变化（包括项内改写、嵌套列表批注、空白变化）均设置 `ContextReviewRequired=true`，`Mappings` 为空；普通项为 `CONTEXT_REVIEW`，历史排除项保持 `PREVIOUSLY_UNTRUSTED`。全文原字节保留，v2 顶层 Items 可为空，不能回退 MachineItems。

全文与服务器核验的机器全文 exact 相等且 AST 边界通过时，保留全部非历史排除项映射。`IneligibleItemIDs` 继承由服务端 owner 保证；恢复原字节和人工审核都不能清除历史排除。当前不能可靠保留人工稿中“字节未改项”的可信状态，不加入关键词黑名单或新模型审查。

后续 UI 接线必须把人工内容的旧来源显示为历史参考，明确待复核，不展示为当前来源核验证据。本切片未实施 UI，也未验证其显示。

## 类型与版本

`SynthesisRevision` 与 `SynthesisNoteSnapshot` 均新增可选 `Manuscript *SynthesisManuscript`，JSON 为 `manuscript,omitempty`。现有字段顺序不变，新字段在末尾。v1 必须没有 envelope；原常量 `SynthesisRendererVersion=synthesis-markdown/v1`、`SynthesisRevisionSchema=synthesis-revision/v1` 不变。

v2 使用 `SynthesisRendererVersionV2=synthesis-markdown/v2` 和 `SynthesisRevisionSchemaV2=synthesis-revision/v2`；envelope 自身仍为 `SynthesisManuscriptVersion=synthesis-manuscript/v2`。v2 必须有 envelope；未知版本拒绝，没有 fallback。`ComputeSynthesisRevisionHash` 按 renderer 选择 hash schema，排除 Hash 自身，其余字段（包括整个 envelope）参与散列。

v1 黄金测试 `TestSynthesisRevisionHashSnapshotAndSafeMarkdown` 原样保留，projection hash 仍为 `72d75c499b47f076afc0e6397bab8ae7d8d613b15a67289fb7b223169463a4de`，content hash 仍为 `b021b6dcf2676f8d9e3df08190f3e26e805074621c761097015f44311d932412`。v1 快照仍走原 `cloneSynthesisItems` 路径，保留历史 slice normalization。

## 纯领域验证与正文读取

新增 `(SynthesisRevision).Content() (string,error)` 与 `(SynthesisNoteSnapshot).Content() (string,error)`：v1 走原 renderer；v2 返回 envelope.FullContent 原字节。读取方法检查版本、内容完整性与 hash，不重算 revision projection hash（该检查由 revision.Validate 完成）。禁止用 v2.Items 重新渲染整篇。

v2 `Validate` / hash / Content 仅调用无依赖 `ValidateIntegrity`，不构造 Goldmark、不引用全局 mapper。检查：

- envelope 版本、内部机器结构、UTF-8/1 MiB 上界、机器/全文/envelope hash、ManualChanges、mapping 字节边界和 block hash、assessment 覆盖完整性；
- envelope 的 WorkspaceID / NoteID 必须与 revision / snapshot 一致；
- 顶层 Items 严格等于 assessment mappings 指定的机器项，按 MachineItems 顺序，逐项深比较；禁止缺项、改写项、重排、附加未映射 MachineItems；允许零可信项（nil 或空切片）；
- 顶层 ContentHash 精确等于 FullContent 的 SHA-256，不能仅更新 envelope 后沿用旧 Article 内容 hash。

Title 仍为合法目录元数据，不要求等于 MachineTitle，也不会重写 FullContent 中的标题。MachineTitle 仅决定机器基线 renderer 的字节。

**完整性不等于可信性。** 自洽的客户端 mappings 可被重新 hash，纯 domain 校验不证明 AST/source/scope/receipt。app/store 创建、重读及发布入口仍须用服务端可信 expected machine 调用 `manuscript.Validate(expected, mapper)` 并绑定来源、scope、merge receipt、owner 与 L/P/F CAS。只有完成这些边界验证的 envelope，才能将其内部映射子集作为可信语义。Snapshot.Validate 也不能凭 ProjectionHash 字符串证明发布或重建完整 revision digest；owner 需证明发布身份。

## 深拷贝

`SynthesisSnapshotFromRevision` 先验证 revision。v2 snapshot 经 JSON 值拷贝保存全文 envelope，保留 hash 所依赖的 nil / empty 区别，隔离顶层 Items、MachineItems、所有 item 子指针、Sources、Conflict.Alternatives、Gap.Resolution、BodyReference、IneligibleItemIDs、Mappings、ReviewItems；snapshot 顶层 Items 与自身 envelope 的 MachineItems 也不共享引用。字符串为不可变值。

## 接线限制

- 本次只修改分配的 domain 文件并增加 `synthesis_manuscript_revision_test.go`；没有改 store/workflow/schema/frontend 或 adapter。
- 不新增 revision 构造流程、merge receipt 字段或 DB closure；当前 envelope hash 不是 merge proof。
- 现有 revision 身份、AGENT Article 绑定字段、非空 Delta、SourceEventID/WorkflowRunID/ModelRunID、父链和 UTC 时间限制保留。纯人工无机器 Delta 的独立版本入口本次未放宽，root 如需该入口应单独定义契约，不能伪造模型来源。
- 机器更新输入应使用受信 envelope.MachineItems，公开正文语义/Interview/body inclusion 只可使用经 owner 验证的顶层 Items。零 Items 不应回退机器内容。
- 持久化必须保存/恢复完整 envelope，升级 JSON/SQL 校验和 source closure；旧 reader 直接 Render(Items) 需替换为统一 Content 方法。接线前生产路径仍是 v1，不声称新 v2 已可发布。

## 限定验证

`go test ./internal/organizing/domain -run 'TestSynthesis(Manuscript|RevisionHashSnapshotAndSafeMarkdown)' -count=1`

覆盖 v2 有/零可信项、原全文读取、revision/snapshot 内容与 envelope 篡改、独立重算 envelope 后顶层 hash 不一致、未知版本、缺失 envelope、owner 重绑定、MachineItems 泄漏/缺项/重排、所有主要嵌套切片深拷贝、v1 JSON omission/读取，以及未修改的 v1 黄金测试。测试 fixture 明确仅用于纯领域结构验证，不声称 parser/source 证明。

`go vet ./internal/organizing/domain`

真实 merge→应用→持久化→发布尚未验证，由 root 与持久化接线切片继续。

## 持久回归与最终读取命名

最终正文读取接口统一为 `SynthesisRevision.Content()` 和 `SynthesisNoteSnapshot.Content()`，签名均为 `(string,error)`，不保留 Markdown 别名。

Goldmark 边界现已保留为真实回归文件 `internal/organizing/adapter/manuscript/mapper_test.go`：局部正文/列表批注及跨项否定声明均触发整篇复核；exact 机器全文的 UTF-8 字节偏移和块 SHA-256 精确匹配；外围语境/标题变化、删除/重排/重复块、未知/嵌套/断裂 marker、围栏/缩进代码/引用结构均不冒充可信块；历史排除不能恢复可信；自洽重哈希客户端映射被 parser 重算拒绝。

限定执行 `go test ./internal/organizing/adapter/manuscript ./internal/organizing/domain -run 'TestMapper|TestSynthesis(Manuscript|RevisionHashSnapshotAndSafeMarkdown)' -count=1` 通过；对应两包 `go vet` 通过。原任务目录边界程序仍存在，但回归不再依赖手工运行该程序。

## 跨版本排除 tombstone 修正

`IneligibleItemIDs` 是跨版本墓碑集合，不限于当前 MachineItems；domain 仅要求 ID 有效且唯一，暂时缺席的 ID 仍写入 envelope/hash 并保留在 snapshot。Mapper 在原 ID 再出现时继续排除。Owner 必须持久继承 Next exclusions、Latest.Machine.IneligibleItemIDs、Latest.ReviewItems 的并集，不能与 next.MachineItems 取交集；确定性排序由 owner 完成。

持久回归 `TestMapperTombstoneSurvivesRemovalAndReintroduction` 验证三代 envelope（排除→移除→原 ID/字节重现）、JSON 往返、完整性校验、恢复后仍排除，以及非法/重复 ID 拒绝，限定两包测试与 vet 通过。该测试显式由调用方携带 tombstone，不能替代 application 三次 Preview 的继承回归；已通知 root 修复所持有的 helper 并补该序列。本切片未修改 application。

## NUL 字节边界

完整正文在 `NewSynthesisManuscript` 和 `ValidateIntegrity` 均拒绝 NUL，与 Git merger / Article 正文契约一致；没有增加其他过滤规则。持久回归 `mapper_test.go:TestManuscriptRejectsNULAtCreationAndIntegrityBoundary` 覆盖首/中/尾 NUL，以及重算正文和 envelope hash 后仍拒绝的自校验路径。该定向测试及两包 vet 通过。

## 本批 owner 预览继承与真实 Git 裁决收敛

`PreviewSynthesisManuscript` 现在继承 `NextMachine.IneligibleItemIDs`、`Latest.Manuscript.Machine.IneligibleItemIDs` 和 `Latest.Manuscript.Assessment.ReviewItems` 的完整并集，按 ID 排序，不过滤当前缺席项、不修改输入切片。domain 与应用并集均限制 `MaxSynthesisManuscriptExclusions=MaxSynthesisItems`（128）；超限返回 `SYNTHESIS_MANUSCRIPT_HISTORY_REVIEW_REQUIRED` / `manual_recovery_required`、不可重试，不截断或清空历史。调用方应报告需人工复核，不把失败当成无变化成功。本次未新增重置墓碑的入口。

新增持久真实 Git 回归 `application/synthesis_manuscript_merge_test.go:TestSynthesisManuscriptMergeTombstoneRemovalAndReintroduction`：三次 Preview（继承排除→移除→相同 ID/字节重现），每轮绑定 domain v2 revision，重现项始终不可信；`TestSynthesisManuscriptMergeExclusionOverflowRequiresReview` 覆盖当前合法上限、ReviewItems 合并后超限及 domain 输入超限，不丢历史。

root 提供的 `ResolveSynthesisManuscript` 与 `TestSynthesisManuscriptResolutionRequiresBothConflictStages` 已实际运行真实 Git：第一阶段裁决后仍暴露第二阶段冲突，第二次精确 fingerprint/ordinal 裁决后才产生完整稿；裁决 replay 稳定，错误 ordinal 与变化后的捕获拒绝，结果不共享决定切片。该实现本次无需逻辑修改。

限定三包执行并通过 `go test ./internal/organizing/application ./internal/organizing/adapter/manuscript ./internal/organizing/domain -run 'TestSynthesisManuscript|TestMapper|TestManuscriptRejectsNUL|TestSynthesisRevisionHashSnapshotAndSafeMarkdown' -count=1` 及三包 vet（application 1.718s、mapper 1.496s、domain 1.567s）。Content()、NUL 拒绝、Mapper 整篇复核同时覆盖。此处只验证纯预览/裁决与 domain 契约；没有授权、DB、持久化/发布证明，未修改 synthesis_manuscript_store.go、postgres 或 SQL。此前本文件“应用继承尚未验证”的状态由本节限定证据替代。
