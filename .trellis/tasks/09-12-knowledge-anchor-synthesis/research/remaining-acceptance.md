# 剩余验收与证据边界

2026-09-16，已批准PRD的当前收口清单。功能实施、关键业务链路及有界合成资料的真实模型语义验收已有成功证据；用户确认原目录并授权重绑后，本地服务恢复、最新迁移和DeepSeek/BGE配置激活也已完成。当前交付范围无剩余实施或运行阻塞；代码未提交或推送。此前历史恢复遗漏已补齐，包含同正文独立历史发布；implement.md后半部分保留历史进度，旧“待完成”不等于当前缺口。

2026-09-16 最新跟进：用户指定Shenwen的 `deepseek-v4.1-flash`，BGE保留。来源身份、付费前容量、唯一Fusion目标、精确生成格式及原有证据资格修复已实现；迁移131–135、定向Go/隔离PG/River/历史证明升级、vet/Atlas通过。真实全文补源两例按30秒/8192预算通过；最新五类融合也全部通过，10次调用36.71s，无格式repair，main逐项人工核对原文/输出/正文/来源。前序独立审查问题已关闭，133–135最终独立复审无待修确定缺陷。14:22（北京时间）受控rebind成功，binding version由3升4、原Workspace ID和路径保持；正式Schema已到135，8个运行容器healthy、runtime ready。随后激活exact desired10，独立新Session复读确认desired/active/API applied/Worker applied均为10，两角色fresh、rollout idle且无错误；DeepSeek Chat与1024维BGE均生效。恢复后备份中19个文件含Git仍逐字节一致。详细依据见 `deepseek-chat-activation-20260916.md`末节、各版本实施记录及 `fusion-format-final-review.md`；以下Luna/旧网关失败和active8为历史。

2026-09-16：用户随后追加按功能配置强度，现已实现并通过真实PG/HTTP/浏览器保存刷新、实际Worker接线和两代配置协议检查，后续独立审查无待修P1/P2。用户明确授权更新服务并启用 BGE 后，最新程序已部署，实际Schema升至130；BGE真实生产Probe与激活通过，desired/active/API/Worker applied均为8，Chat保持disabled。用户说明本地Chat网关使用Responses，对应最小请求返回502 / Upstream access forbidden；真实语义验收仍未通过。详细证据见 bge-local-activation-20260916.md、bge-embedding-compat.md 和 reasoning-effort-functions.md。协议通用性仅完成研究建议，用户要求先调研，不将其自动扩大为Responses实现。

此前追加授权的统一模型思考强度已完成：模型默认与五档统一强度，保存/应用版本语义、实际参数传递、旧数据迁移和真实设置面板保存刷新均有定向证据；独立检查无待修P1/P2。该轮Schema为129；随后分功能配置已升级到130，路由集合仍258。统一强度的历史证据见 reasoning-effort.md；分功能的最新证据见 reasoning-effort-functions-backend.md。BGE外部向量调用通过不代表Chat语义质量完成。

## 已交付与验证范围

| 能力 | 当前证据 |
| --- | --- |
| 自动发现新增/修改来源、异步简介/标签/知识目录与锚点关联候选 | 实际 FS/PG/River 和失败恢复；见 implement.md 的00103–00114记录、discovery-failure-implementation.md |
| 用户提出主题生成初版、从已有来源提升主笔记、AI范围建议与批量关联审核 | 实际模型调用账本和持久四阶段生成、权限/审批定向回归；见 goal-initial-generation.md、association-dispatch.md 与 implement.md |
| 主笔记集中阅读、当前/历史版本、片段→知识点→来源版本追溯、共享来源图谱与失效提醒 | PG/HTTP/generated Web、定向桌面/手机浏览器；旧资料无历史知识点绑定明确 UNRECORDED，证据按版本冻结 |
| 上游已发布片段驱动下游局部候选，审核后发布 | 实际 A→B→C/相互引用、River/Approval/Git、三次重扫无重复；见 body-roundtrip-validation.md |
| 人工全文保护、两阶段冲突裁决、持久等待与发布 | 实际 PG/River/auth HTTP/Chromium/Approval/Git；见 manual-human-runtime-implementation.md 和 manual-human-ui-implementation.md |
| 候选生成后磁盘再次变化，重新合并与丢响应恢复 | 浏览器到实际发布 PASS407.38s，完整人工内容逐字保留；见 candidate-remerge-browser-verification.md |
| 人工改写后的当前全文独立补源 | 实际发布后手改 LOCAL_FILE、独立核验、全文/来源打开、失效刷新/历史读取；浏览器 PASS314.56s，见 current-source-review-browser-verification.md |
| 补源显式重核、已接受结果恢复、共享证据与恢复失败后继续 | 最终127实库/auth HTTP/generated React通过；真实浏览器丢响应→reload→原key恢复 PASS529.93s；见 current-source-review-command-browser-verification.md、current-source-review-commands-ui-implementation.md |
| 数据库及公共契约 | 正式128空库迁移、schema导出/另空库恢复、Atlas检查；258条OpenAPI/generated契约与实际路由集合一致；见 schema-128-validation.md、history-republish-ui-implementation.md |

最后独立复审的4项P2均已修复，未发现新的确定缺陷，见 source-review-127-final-review.md。编译、类型/lint、Go vet及定向回归按受影响范围执行，不宣称全仓检查通过。临时页面和测试服务已清理；没有提交或推送；用户后来授权的BGE配置已通过现有设置API保存到运行实例desired7，active仍为2，详见顶部最新记录。

## 已完成：历史主笔记重新发布

PRD明确要求历史版本可查看或回滚。历史查看已实现；通用文件历史恢复会创建SYSTEM ArticleRevision，而主笔记正式读取要求精确AGENT/generated/publication binding，恢复后不能继续投影为当前正式主笔记。此前“仅剩真实模型”的结论被本次源码审计纠正，见 history-republish-audit.md。

128专用owner、HTTP/generated/Web已实施；真实PG/Git覆盖旧已发布与未发布候选、人工全文、显式退役、无首次发布、来源/profile NULL保持、后续增量及实际正文依赖传播。main完整主笔记页面恢复/中断/刷新/Resume/审批Git/默认阅读/精确来源通过311.78s，见 history-republish-browser-verification.md；最终128迁移与Schema新库恢复通过。独立初审和无首次P追加复核完成，未发现待修P1/P2。

同正文不同历史身份的真实Git发布已补齐：只由历史人工回执与精确审批执行重建authority，保留原Root/权限/CAS，实际生成独立真实commit与发布事件。连续两次同正文选择、正式读取、Resume不增commit、无授权/错误receipt拒绝、update-ref成功丢响应恢复均有实测；原125兼容与历史合跑50.531s。最终独立窄审无待修P1/P2，见history-republish-proof-review.md末节。原Boundary已知失败复现已替换为成功验收，不按hash伪造完成。

## 已核验的人工内容边界

- 真实生产模型账本和当前执行许可：生成、独立语义核验、合并、apply 跨节点执行；初版与已有 v1 run 仍可重放。
- L 含未发布内容、P 为当前正式版本、F 含人工批注时，新来源只更新相关机器内容，L/F 中应保留的内容逐字保留。候选生成不得移动正式指针或写文件。
- 实际同处冲突进入持久 HumanWait，刷新后可重开三方差异；第一阶段裁决后出现第二阶段冲突，须重新确认对应 fingerprint 与全部冲突块。同一处理中的全部 note 完成后才放行 apply。
- 文件、latest Article、P、范围或根授权变化时，旧预览/裁决不能生成覆盖新内容的候选；提交结果未知时精确重放不重复裁决、生成或调用模型。
- 审核发布后，实际文件、全文阅读、历史重开、来源复核角色一致。人工全文的机器审计项不得重新进入可信片段目录或 Interview。
- 当前 Git clean 门禁仍有效。未提交 F 的发布失败是已验证限制；仅在隔离测试仓提交 F 后证明双基线发布。不据此宣称任意未提交人工改动均可发布。

## 已核验的多轮正文传播边界

- A 的实际发布影响 B 的已包含片段，生成 B 的局部候选；未审核时 B 正文保持。
- B 审核发布后可影响确实包含该片段的 C；共享资料、普通关系、未引用片段和未发布候选不传播。
- 再次扫描同一发布，以及 A/B 相互引用时，不无限生成候选；核对持久事件/候选数量和实际正文，而非只断言 dispatcher 返回值。

## 已完成：有界真实模型质量

现有 deploy/compose-synthesis-smoke.sh 固定 REAL_PROVIDER=0；compose-rag-smoke.sh 的真实模型路径不能证明主笔记融合质量。固定 Provider 流程已通过，不重新运行相同流程充当质量证据。

已使用现有 organizing/adapter/agent 的真实生成和独立semantic协议，并复用生产Eino/OpenAI-compatible provider。只输入新建合成资料，不读取个人笔记；五项均有真实成功及main人工核验：

1. Redis 专项目标遇到只包含 MySQL 的资料：不得纳入原范围；若给出范围建议，待确认状态不改变正文。
2. 数据库专项目标读取混合面试笔记：只取 Redis/MySQL/Oracle 相关原文，保留语境，不带入无关经历。
3. 已有知识收到重复、带适用条件的新知识及相反观点：分别检查无正文变更、条件保留、双方观点/来源并列及未知条件不被补造。

调用次数、结构化重试和总超时仍有界。最新成功产物为 `.zhixu/diagnostics/deepseek-synthesis-890bca50b5.json`；全文支持/拒绝产物为 `deepseek-source_review-4833cda147.json`，两者已人工核对。以下网关失败为早期记录，不能代替顶部最新结论。模型语义证据、owner许可/发布证明和浏览器交互分别验收，不把模型自评SUPPORTED当作人工检查。

2026-09-16：用户已提供Chat和Embedding配置。Chat真实验收首个调用失败，已核对本地网关返回503、无法构建执行计划，尚无语义质量结果；BGE真实生产调用通过。不能再把“没有配置”列为当前阻塞。凭据未记入报告。

已准备的有界工具见 synthesis-live-quality.md：原五场景覆盖Redis范围、混合面试资料、纯重复、不同适用条件和相反观点；新增独立开关的当前全文两例，走生产SourceReviewModel/Eino/Recording入口，检查“全文已否定旧结论却只有旧来源”和“来源确实支持当前全文”。离线实际请求检查、两义务绑定及4次调用/5分钟硬预算通过；真实入口现已执行并因网关503失败。网关恢复后仍须使用新的artifact路径重新运行并人工核验产物，不能仅相信模型自评SUPPORTED。

早期22:15的shell无配置/未认证GET401仅是历史观察，已被09-16用户提供配置、真实调用以及通过既有Session认证GET的事实替代。最新运行实例active revision8已启用BGE，Chat仍disabled；旧desired revision7的BGE与Chat草稿保存在不可变历史中。此前503来自Chat路径；最新Responses探测为502上游拒绝，不宣称改协议已解决网关可用性。

## 已完成：正式服务恢复与配置激活

- 用户明确确认 `/Users/zhenglizhi/Documents/files/zhixu` 是原笔记目录并允许重绑。`./zhixu workspace rebind --confirm REBIND` 退出0，正常构建、迁移、重绑、启动及ready后提交selection，未直接改Registry。
- 正式数据库确认131–135全部applied=total=1；Registry active/available、binding4，API/Worker相同工作区与binding4，心跳新鲜。`/readyz` 返回HTTP200 / ready。
- 既有模型设置API精确激活desired10，经历preparing→arming→idle；新认证Session复读确认desired/active/API/Worker版本均为10，Chat为deepseek-v4.1-flash / chat_completions，Embedding为BAAI/bge-m3 / 1024 / l2 / cosine。临时Session均已撤销。
- 备份中19个文件恢复后逐字节一致、无缺失或修改。本次未触发个人笔记专项重建或发布；既有真实合成验收不重复付费执行。

## 保留的验证限制

- 当前 Git clean 门禁要求发布前处理未提交的人工文件改动；本次仅在隔离测试仓提交人工文件，未改变该产品边界。
- 浏览器覆盖实际业务组件与真实隔离后端，非完整登录 AppShell。固定 Provider 证明业务流程、账本、幂等与数据约束，不能证明 AI 整理质量。
- 127正式空库迁移/恢复通过；旧126带成功证据数据升级后复用、跨review多义务部分复用组合未作专项实测。最终复审中已有静态合同证据，但不宣称这些矩阵已运行。
