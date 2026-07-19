# M6-02 Agent Structured Output And Citation

## Goal

实现 Agent 结构化输出、Schema Repair、关系五分类、引用与 Faithfulness 校验、冲突解释、拒答和模型版本记录，并接入 Retrieval 与 Knowledge Application 稳定 seam。

## Background And Confirmed Facts

- M2 已判定主模块不正式采用 Eino；M6-02 使用项目自有 Interface 和直接 OpenAI-Compatible Adapter。
- Retrieval Search/Evidence 已可返回有界候选和可打开 Source Span，但 Active Index 不等于 Approved Knowledge。
- Knowledge 已冻结 Relation Assessment、Applicability、Provenance、Claim/Relation/Conflict 和写命令 seam。
- 当前没有正式 ChatModel Adapter、Agent 模块、Agent Eval Runner 或 Model Run 持久化。
- M6-02 是 M6-03 Tool Security 与 M6-04 RAG API/SSE 的前置内核，不实现它们的协议/UI 范围。

## Requirements

### R1. Module Boundary And Model Port

- 新增 `internal/agent/{domain,application,adapter}`，Domain 只依赖 `internal/foundation` 和标准库。
- ChatModel 使用项目自有请求/响应、usage、版本与错误类型；Provider/Eino 类型不得进入 Domain/Application。
- 正式 Adapter 为直接 OpenAI-Compatible HTTP；Ollama 通过兼容 endpoint 接入。本期不引入 Eino 或 native Ollama 第二协议。
- Provider disabled 时返回不可用 capability，不构造 Fake 或空成功 Adapter。

### R2. Versioned Task Schemas And Strict Output

- 至少冻结 Relation Assessment、RAG Answer、Refusal、Faithfulness Review 四个独立 Schema；公共 Envelope 保持小而稳定。
- 输出必须是单个严格 JSON document，拒绝非法 UTF-8、重复 key、unknown field、尾随值、错误类型/枚举、越界字符串和数组。
- Schema 通过后继续执行领域、Evidence、Workspace、Applicability 和动作权限校验；JSON 合法不等于业务成功。
- 禁止 regex、markdown fence 抽取、默认字段填充或自由文本 fallback。

### R3. Bounded Repair And Retry Ownership

- 一次 Structured Run 最多三次模型响应：`INITIAL -> REPAIR -> REDUCED`；仍失败返回稳定 Validation Exhausted。
- REPAIR 只接收 redacted 错误摘要；REDUCED 使用任务定义的最小安全 Schema，不把 parser 宽松化。
- Chat Adapter 不自动重试；Workflow 只重试明确 transient provider failure。Schema/领域/Citation 拒绝不触发无界重试。
- 不在同一 Node 内静默切换 Provider/Model；所有显式 profile 选择写入 Model Run。

### R4. Relation Assessment

- 五分类严格使用 Knowledge `RelationAssessment`，不得把它复制为第二套 RelationType。
- NEW/LOW_CONFIDENCE 不创建 Relation；CONFLICT 不直接确认关系或裁决胜负。
- 非 NEW 输出必须绑定新旧两侧 Evidence、Applicability 比较、理由、置信因素和不确定原因。
- Application 只能调用 Knowledge Application/Domain seam 生成候选动作或命令输入，不得直接写 Repository/SQL。

### R5. Citation, Eligibility And Faithfulness

- Citation 身份至少包含 Workspace、Chunk、Source Version 和 Source Span；不能只引用 Chunk 或模型自造 URL/路径。
- 引用依次通过 Identity、Openability、Eligibility 和 Semantic Support 四层校验。
- 新增 Knowledge 批量 Eligibility seam：Confirmed 正式事实可发布；Disputed 可进入上下文但必须披露 Conflict；无正式绑定默认拒绝。
- 每个事实 assertion 必须绑定至少一个 eligible Citation，或显式标记为 `model_inference`；未支持内容不能发布为已验证答案。
- Faithfulness Review 使用任务专用结构化 Schema；Review 不可用或验证失败时不发布高风险结果。

### R6. Conflict And Refusal

- Conflict Answer 分别展示观点、Applicability、来源和更新时间；不允许模型常识静默裁决。
- 可条件化时输出条件化结论；不可条件化、证据不足、引用损坏或未批准时返回稳定 Refusal。
- Refusal reason code 至少覆盖无证据、仅未批准证据、引用不可解析、证据不足、外部事实未授权、冲突不可条件化和验证耗尽。

### R7. Model Run Persistence And Privacy

- 新增可查询 Model Run 事实，绑定 Workspace、Workflow Run、Node Run、Node Attempt、Adapter/Model、Prompt/Schema、Retrieval 版本和最终状态。
- 每次 INITIAL/REPAIR/REDUCED/REVIEW 调用记录 call no、request/response hash、Token、耗时、状态和稳定错误；不保存完整 Prompt、Evidence、原始响应、Credential 或绝对路径。
- Model Run 在调用前创建，完成使用 CAS；崩溃后的未知结果不得伪装成功。Knowledge `model_run_ref` 指向稳定 Model Run ID。
- Prompt、Schema、Model profile 和 Eval dataset 均有稳定 ID/版本；运行中不读取“最新默认”覆盖已冻结版本。

### R8. Evaluation And Security

- 新增版本化 Agent fixture 和 offline eval runner，计算 Relation 五分类矩阵、Citation Precision/Coverage、Faithfulness、Conflict Disclosure 和 Appropriate Refusal。
- 单元/Contract/E2E 使用 Deterministic Fake；Fake 不是生产 fallback，也不能代替真实模型质量评测。
- Prompt、Source、Tool Result 标记为 untrusted；本任务只输出 Tool Request，不执行或授权工具。
- 日志/Trace/错误只记录 code、count、hash、版本和耗时，Secret/Prompt/Source/raw output canary 必须通过。

## Acceptance Criteria

- [ ] 四类任务 Schema 独立版本化，strict decoder 覆盖 unknown/duplicate/trailing/invalid UTF-8/type/limit 失败路径。
- [ ] INITIAL/REPAIR/REDUCED 总调用数可精确断言；耗尽后无自由文本或 Fake fallback。
- [ ] OpenAI-Compatible Chat Adapter Contract 覆盖成功、429/5xx/4xx、timeout/cancel、redirect、超大/非法响应、版本回显和 Secret canary。
- [ ] 五分类固定样本覆盖，NEW/LOW 不落 Relation，CONFLICT 不误映射 Duplicate/直接确认。
- [ ] 非 NEW Assessment 双侧 Evidence、Applicability、reason、confidence factors 与 uncertainty 完整且可定位。
- [ ] Citation 四层校验覆盖正常、跨 Workspace、损坏 Artifact、未批准、Disputed、无关证据和多 Provenance。
- [ ] 每个事实 assertion 有支持引用或 inference 标记；Faithfulness Fail 会重生成或 Refusal，不返回 publishable success。
- [ ] 冲突回答披露双方条件/来源；不可条件化返回稳定 Refusal reason code。
- [ ] Knowledge Eligibility 500 条单批查询稳定排序、无 N+1，并通过真实 PostgreSQL 测试。
- [ ] Model Run/Call 可反查全部实际版本、Token、耗时、repair、错误和 Node Attempt；crash/重放/Workspace/FK/guarded Down 通过真实 PostgreSQL。
- [ ] Offline Agent Eval 输出版本化指标；真实 Provider 未配置时明确 SKIP，不能计为质量 PASS。
- [ ] Go race、关键包 count、全仓 integration、vet、make test、tidy diff、Docker/Compose、主审查与独立复验通过。

## Out Of Scope

- RAG Conversation、HTTP、SSE、反馈和页面展示（M6-04）。
- Tool Registry 执行、Capability、SSRF、命令/输出安全和审批授权（M6-03）。
- 模型直接确认 Knowledge、写文件、Git 或数据库；正式写入仍走 Knowledge/Change Control。
- Graph/Collection/Artifact/Review/Interview/Memory 业务实现。
- 多 Agent Swarm、模型训练/微调、正式采用 Eino、native Ollama 第二协议。
- 完整成本/Audit UI 和最终真实模型基线阈值；M6-02 提供事实与 Runner，M10/M11 完成产品化门禁。

## Resolved Conflicts

- Repair 次数采用正式 PRD 的 Initial + Repair + Reduced 三次上限；M2 一次 repair 只作为 PoC 基线。
- Model Run 使用专用事实源而非仅日志或 Node output；一个 Node Attempt 可拥有一个 Run 和多个 Model Calls。
- Approved Eligibility 当前以正式 Knowledge Evidence 为 fail-closed 最小集；未来 Article/Document Approval 通过同一 Port 扩展。
