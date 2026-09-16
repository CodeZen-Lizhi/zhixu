# 正文依赖传播与人工编辑的接线边界

本文件区分当前代码事实与后续实施要求，不把规划当作已经完成的能力。

## 当前代码事实

- 00115 的 synthesis_proven_publication / organizing.synthesis.published 保存真实历史发布事实；00116 的 synthesis_revision_body_reference 保存精确已发布片段包含关系。
- SynthesisGenerationInput 与 SynthesisFrozenInput 保留真实 SourceReady provenance；目标生成使用 GoalRequestID，00118 正文刷新使用独立 BodyRefreshRequestID，没有伪造或重发来源事件。
- 普通 delta 保留 ADD_FACT/ADD_CONFLICT/ADD_GAP、ADD_SUPPORT、RESOLVE_GAP；00118 新增仅针对精确冻结引用目标的 REFRESH_ITEM，保留目标身份和无关条目，服务端完整复制真实新发布项。
- prepareInput 会读取普通候选目录；该目录以当前 candidate 为准，只给其当前修订确实已发布的条目提供 PublicationID。传播不能靠普通目录重新选择上游，也不能因上游随后有新草稿而失去事件绑定的历史已发布版本。
- ApplySynthesisGeneration 在应用事务中核验模型记录、来源、锚点范围、目标当前修订/hash/version，再创建候选与发布预约。只有实际变化才创建 revision；正式写回仍经过 Authoring/Change Control。
- appendCandidate 要求 Authoring latest ArticleRevision 等于 Synthesis 的 base ArticleRevision。独立 owner 变化会拒绝继续。入口复核证明当前 generated 文档没有保存 USER Article 的可达入口；人工保留的首版来源是受授权工作区文件，不新增 USER 父版本能力。
- v1 ComputeSynthesisRevisionHash 和 snapshot 要求正文等于固定 renderer。v2 domain envelope 已开始实施完整正文和可信子集，但尚未接持久化/发布；不能简单把合并正文塞入 ArticleRevision 却继续声称旧 Items 精确代表全文。
- gormVerifyGeneratedParent 也要求父修订存在相同 generated owner receipt。生成链不能无证明地跳过人工父版本。
- gorm_publication 的 reservation/finalizer 严格核验 ArticleRevision、ProposalRevision 的完整正文/hash。直接编辑 Change Control Proposal 不能代替新的 Synthesis/Authoring 候选绑定。
- 项目已有成熟的 gitcli.ThreeWayMerger（固定 git merge-file/diff3/myers/marker32/v1）和 Change Control 合并预览/冲突工作台。人工编辑合并应复用这些能力，不另写通用三方合并算法。

## 发布影响记录的当前实施边界

独立持久记录应绑定上游真实发布事件、下游确切基线/片段和旧上游引用；只检查真实正文关系，不能由共享来源或主题相同推断。比较忽略纯 body_reference 版本链变化，以避免同文引用身份变化制造递归传播；完整引用内容不变则不产生正文影响。未发布草稿不会作为上游事件。处理应有界、并发幂等，重复扫描不得重复产生记录。

这一记录只是后续生成的输入，不能宣称候选已经生成。HTTP/UI 应区分待检查、候选生成、失败和待审阅状态，不以有影响记录推断已产生 revision。

## 00118 已落地的局部候选契约

1. 发布影响拥有独立 processing 身份，在旧 SourceReady/Goal/Fusion 之外明确区分，真实来源事件仅用于 provenance。重试仍复用一个请求身份，不自动重跑失败 Provider。
2. prepare 从影响记录读取确切旧/新上游已发布修订、下游最新基线、全部受影响条目和当前锚点范围；不能从普通候选目录补造绑定。
3. 新协议只允许刷新列出的引用目标，或给出可解释的无需修改/需要人工处理结果。不能追加无关章节或全文重写。新来源必须来自事件指定片段的原始证据，来源 owner 核验和独立语义审查继续适用。
4. 局部刷新保留下游 item 身份和全部无关 Items；新 body_reference 精确指向新的上游发布片段。只有内容/条件/冲突/结论发生实质变化时创建新正文版本，纯后补来源继续走补证账本。
5. 当前锚点范围需单独冻结并重验，既有正文关系不能成为静默扩大主题范围的授权。范围外变化应形成待复核/范围调整提示，不能自动改变锚点。
6. 应用时再次核验影响记录、上游历史发布证据、下游基线和 scope。下游已变化时基于最新内容重新准备，不能复用过期差异写回。
7. 无影响、自引用或已传播的同一内容状态应终止；循环主笔记关系不按 revision 数量无限制造新候选。传播只从发布事件继续，候选本身不发发布事件。

## 人工内容合并仍须解决的契约

- 保存人工内容及其归属，既能保持任意无关段落/批注字节，又不把人工改写后的结论错误标成已验证的原始引用。
- 使用原生成正文、当前人工正文和新生成正文进行三方比较，复用现有 Git merger；冲突必须可见并由用户裁决，不能静默保留一方掩盖失败。
- 合并后的正文、人工部分、仍成立的 item/source 映射必须绑定到同一个新的 Synthesis/Authoring revision；只修改最终文件而不更新阅读投影是不完整实现。
- 预览、提交候选、审核与发布之间都核对人工版本/文件基线；人工再次修改后旧候选必须重新合并和审阅。

## 验收矩阵（已验证范围见 implement.md 的 117/118 记录）

- 上游仅追加未引用条目：下游无影响/无候选。
- 上游被引用缺口补充或事实/条件变化：仅对应下游条目出现候选，正式正文不变。
- 同一发布重复/并发处理：影响与候选各只有一次。
- A/B 相互引用：纯引用身份或相同正文不循环传播。
- 目标已有新草稿、人工另存或文件修改：旧候选不能覆盖，重新合并后无关人工段落保持原字节。
- 真实 HTTP/Worker/River/审批/Git 与浏览器连续流程，不仅分别验证各层夹具。

## 已定位的产品联机核验入口

`deploy/compose-synthesis-smoke.sh` 复用 `compose-rag-smoke.sh` 的独立 Compose project、临时 Workspace、测试认证与清理。它使用固定 Provider，已有 Capture→自动画像/生成→HTTP 审批→真实 Git→增量候选/NO_CHANGE→浏览器历史与来源流程，具体逻辑在 `deploy/compose-synthesis-smoke-functions.sh`，浏览器用例在 `web/e2e/synthesis-notes.smoke.spec.ts`。

因此后续完整基础联机验证优先复用这个入口，不重复搭建生产配置或操作当前运行中的 zhixu-app/worker。现有脚本未覆盖跨主笔记正文引用传播与人工合并；通过基础 smoke 也不能直接勾选这两项。2026-09-15 已补固定 Provider v4/v2 精确 schema 分支，完整基础 smoke 退出 0；独立容器/卷清理已核对。此结果不覆盖人工合并、完整多轮传播或真实外部 Provider 质量。
