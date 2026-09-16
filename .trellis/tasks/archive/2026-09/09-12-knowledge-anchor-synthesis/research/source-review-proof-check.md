# 126 当前全文来源证明只读审查

2026-09-15。结论：发现 2 项 P2；均未修改。只写本报告，未修改生产 Go、SQL、测试或运行迁移。未派发代理。审查基线为正式 00126（checksum `o1aJ6qEvLEWKJMo18GRVa6Vn0kcpX+4fAyz4alDtVCk=`）；不评价正在接续的 127，也不重复报告 `current-source-recovery-review.md` 已分配的三项恢复/lifecycle问题。

## P2：同一次复核的不同义务共享段落和来源时，合法 SUPPORTED 无法应用

- `internal/organizing/application/synthesis_manuscript_source_review_validation.go:191` 为各历史事实新增来源分别生成义务，同一 SourceRef 共用 S 标签；`:81` 的 BindSourceReviewOutput 要求逐义务完整绑定，但不禁止不同义务选择同一段落，这是合法的当前全文映射。
- `internal/organizing/adapter/postgres/synthesis_manuscript_source_review_apply.go:62` 按每项义务、每个段落分别 INSERT；`:82` 对相同 target 算出相同 target_hash。
- `atlas/migrations/00126_synthesis_manuscript_source_review.sql:82` 的第二个 UNIQUE 为 `(workspace_id,note_id,target_hash,paragraph,source_version_id,parse_projection_id,source_span_id)`，不含 obligation。

触发：原两个独立事实由同一来源片段补源，人工将事实合为一个完整段落；模型对 O001/S001 和 O002/S001 均选择 N001/P00001 且 SUPPORTED。绑定可通过，但第二条证据必然撞唯一键，使整个 apply 事务回滚；真实模型已完成，补源仍不能成功。这是同一个首次 review 内的冲突，不是已另审的 successor/A→B→A 跨 review 复用问题。结论来自确定的循环和唯一键约束，未新增实库复现。

最小修复：明确一条物理证据可满足多个义务，以独立义务→evidence 关联维护完整闭包，并对同 target/paragraph/source 复用同一证据；若保留逐义务证据设计，则前向修正唯一键及相应去重语义。不能仅 ON CONFLICT DO NOTHING，现有 closure 按 obligation 计数，会漏掉第二项义务。

## P2：数据库没有将 evidence.target_hash 绑定到冻结 target

- `atlas/migrations/00126_synthesis_manuscript_source_review.sql:70` 对 target_hash 仅作 64 位 hex 格式检查；`:85` 的 evidence INSERT trigger 校验 note/base revision、全文/段落 hash 和完整来源，却完全未读取或验证 `NEW.target_hash`。
- `...00126...sql:108` 的完成 closure 只核模型证明、义务和证据数量、receipt 非空，不补验 target_hash；导出 `atlas/schema.sql:17850` 保留同样逻辑。
- 正常 Go `synthesis_manuscript_source_review_apply.go:78` 确实计算 `sha256(json.Marshal(target))`，所以这不是已证明的 HTTP 注入或现有正常写入错误。

影响：对一套本来合法的 REVIEWED/output/evidence 完成事务，将 INSERT 中 target_hash 换成另一个合法 hex 值，不会被上述数据库守卫拒绝；其余合法条件不变即可形成错误 target 身份。该字段参与证据全局去重，错误值会破坏同一冻结目标的唯一性，也不能作为后续复用的可信 scope/Root/版本摘要。本轮只读，未执行篡改 SQL；缺失约束本身已由定义核实。

最小修复：前向定义并验证统一的 target 摘要协议，或把复用/唯一键改成数据库可独立核对的显式身份列。不可直接用 PostgreSQL `jsonb::text` hash 替代 Go JSON hash，两者序列化字节不同。新增保存摘要也必须能从冻结 target 独立验证，不能再信任另一份 caller hash。

## 其余审查结论及边界

- 未发现历史 audit 被当作当前支持证明：旧双模型与 delta 仅恢复精确补源意图；baseline 重新读取原文、当前 revision/Article/P/F/Root，并核对当前 admission 与冻结 scope。实际 payload 含全部目标全文、段落和来源，历史 statement 明确标为不可信。当前 scope 已变化会拒绝，不悄悄扩大范围。
- 候选使用完整当前 L，F 必须等于真实 capture；当前 L 已发布时读取真实 F，内容不同标 LOCAL_FILE。发布 proof、祖先关系与 current owner 均有核验。LOCAL_FILE 不冒充旧发布版本正文。实测矩阵主要覆盖候选分支，完整 LOCAL_FILE 组合仍未验证。
- 段落由服务器 AST 生成，保留 UTF-8 字节区间、发生位置标签和 SHA-256；模型仅选标签，不能自报范围。HTML/code 等非段落块不作为支持目标，但仍进入完整全文上下文，这是已知映射范围。
- 应用要求完整义务、正确 source/note/paragraph 且全部 SUPPORTED 才应用；SQL 要求匹配真实成功 ModelRun/Call、完整义务证据，并以 evidence deferred trigger 阻止孤立证据。没有发现正常调用链能凭旧 ModelRun、缺义务或合法非支持结果写成功。ModelSettingsRevision 由共享 `atlas/schema.sql:297` trigger 绑定真实 NodeAttempt，不因局部 verifyModel 未重复比较而误报漏绑。
- INITIAL hash 在真实 model adapter 对完整 ChatRequest 计算，首次 Call hash 和最终 output hash/bytes 均核对。Store 端不重建完整 ChatRequest，只信任受信 runtime adapter 提交的 request hash（应用接口明确为内部受信 port）；当前正常 caller 正确构造请求。该账本不是抵御任意受信 adapter/数据库写入者造假的外部签名证明，不能把真实调用账本等同于真实 LLM 语义正确性。
- 四条 HTTP GET 均要求 READ_LOCAL，按 workspace/parent/cursor 精确查询；只投影 safe view，不序列化内部快照 authority。read owner 在返回前再次核验 Root，来源打开也校验 evidence/source/workspace 并再次授权。本范围未发现跨 workspace 或已撤销 Root 的 HTTP 全文泄漏。内部 GetSourceReview 不作为 HTTP 服务，不能直接暴露。

## 验证依据

已读 check.jsonl、PRD 当前全文决定、design/implement 相关段、core implementation/contract/read contract、schema-126-validation 与既有 recovery review；核对正式迁移、导出 schema 相关对象和直接应用/adapter/HTTP边界。复用正式126八场景及既有读接口证据；抽查 `/tmp/zhixu-source-review-formal126.log`、`final-core`、`root-revoked`、`unknown-provider` 和 `read-integration` 日志的 PASS。迁移/恢复结果依据 schema-126-validation，不扩张为业务正确性证明。

未重跑全套，也未新增编译/vet：本轮只读且已有范围 compile/vet PASS 可复用；这不覆盖正在变动的 127。上述新增两个边界未有专项实库复现；多目标/多义务汇聚、LOCAL_FILE 全组合及真实外部模型语义质量仍未验证。文件读取与数据库提交不能原子化，是已记录设计限制，后续 GET 重验当前有效性。

审查完成：2 项 P2，修复 0；无新增已证实全文泄漏或无完整支持仍成功的问题。done
