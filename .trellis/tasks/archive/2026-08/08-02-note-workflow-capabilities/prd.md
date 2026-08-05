# 笔记工作流基础能力增强

## Goal

让知序在保持 Evidence-first、Proposal/Approval 和 Markdown + Git 事实源约束的前提下，补齐日常笔记使用中的低摩擦采集、主动创作、材料发现与选择、结构化整理、版本查看和远端同步体验。用户既能从空白 Markdown 草稿开始写作，也能从零散资料快速找到与某个知识点相关的内容，确认本次材料后生成可追溯的新文档，而不必逐篇翻找整个 Workspace。

## Background And Confirmed Facts

- 项目已有 Inbox、Source、Document、Topic、Claim、Smart Collection、Artifact、混合检索和 Git Safe Writeback 等领域能力。
- `docs/product/PRD.md` 已要求知识抽取产生 Topic 候选、Claim 候选、术语与别名、前置知识、文档摘要、关键示例和来源位置，但这些结果尚未形成面向“快速发现整理材料”的完整产品体验。
- Topic 是正式知识主题，不等同于普通标签；AI 抽取结果在确认前不能直接成为正式 Topic、Claim 或 Relation。
- Smart Collection 是保存的动态查询，不复制知识对象；它适合复用长期范围，不等于本次生成使用的固定材料版本。
- 当前 Search 已提供 Keyword、Semantic 和 Hybrid 检索，但正式产品中的 Document、Topic、Claim 等完整过滤与材料整理入口仍需补齐。
- 当前仓库有 Document/Article Revision 数据设计，但没有正式 Document 创建 Application/API；Artifact 发布适配器也明确不会创建正式 Document，因此主动创作必须补齐独立能力。

## Capability Map

| 用户确认的能力 | 本 PRD 对应范围 |
|---|---|
| 快速记录 | R1 快速记录 |
| 主动创作 | R8 创作入口与 Document Draft |
| 本次材料选择 | R2 文档知识发现 + R3 Suggested Material Set、用户确认与 Workflow Input Snapshot |
| 内置整理模板 | R4 内置整理模板 |
| 自定义模板 | R5 自定义模板 |
| 文件历史 | R6 文件历史 |
| Git 远端同步 | R7 Git 远端同步 |

文档知识发现不是额外的独立产品能力，而是“本次材料选择”从手工逐篇查找升级为系统建议、用户确认所必需的发现机制。

## PRD Governance

- 本父任务 PRD、现有四个子任务 PRD 与待创建的 Document Draft 创作子任务 PRD，是本范围在规划、实现和验收期间的研发事实源，负责记录细化需求、边界、取舍和验收证据。
- `docs/product/PRD.md` 继续作为唯一的产品级正式 `v1.0` 总 PRD；本范围不新建 `v2.0`、`PRD-2.0` 或同义副本，也不因本专项单独提升产品大版本。
- 每个子任务只有在实现、验证和 Review 完成后，才把已经稳定交付的产品行为、边界与验收结果回填到总 PRD 的对应章节；不得把专项 PRD 整篇复制过去。
- 尚未实现、验证未通过或实现中被取消的能力不得提前写成总 PRD 的既成事实。技术设计、迁移步骤和内部接口细节继续保留在任务文档与架构文档中。
- 若实现过程中需要实质改变本 PRD 的产品范围，必须先更新专项 PRD 并重新获得规划审批；总 PRD 仍在行为交付稳定后回填。
- 如需记录总 PRD 的文档修订，仅更新日期或修订记录并保持产品版本为 `v1.0`；是否进入真正的产品 `v2.0` 由独立产品版本决策处理，不由本专项隐式触发。

## Requirements

### Cross-Cutting Workbench Entry Requirements

- 已连接首页固定暴露本任务拥有的“快速记录 / 新建文章 / 整理成文”动作，并复用现有 `/search`；工作台任务负责布局与唯一 Inbox 导航，本任务负责三个动作背后的真实能力和状态契约。
- Quick Capture 只创建 Inbox Source；“新建文章 / 整理成文”都创建 Document Draft；Artifact 保持默认不进入正式知识的派生结果。三个身份在跨任务导航中不得混称或互相伪装。

### R1 快速记录

- 用户可以从应用任意页面打开 Quick Capture，并使用应用内快捷键触发；保存后返回原工作上下文。
- 用户可以用文字、URL、Markdown、TXT、PDF、HTML 或图片/截图快速创建 Inbox 资料，不必先确定正式标题、目录、Topic 或处理方式。
- 纯文字输入不强制标题；系统可以生成可编辑的临时显示名称，但不能借此改变原始输入。
- URL 保存原始地址、抓取时间和抓取结果；抓取失败时仍保留 URL Source 并展示可重试状态。
- 图片/截图支持粘贴、拖入或文件选择，并先将原图可靠保存为 Content Artifact；配置视觉/OCR 能力时异步提取可检索内容。
- 视觉/OCR 不可用或失败时，页面必须显示“仅保存原图”或等待/失败状态，不得伪造文档已完成知识画像或索引。
- 快速记录保留不可变原始输入和 Provenance；后续摘要、OCR、转写或整理结果不得覆盖原始资料。

### R2 文档知识发现

- 资料摄取后始终自动完成解析、分块和全文索引；配置并启用 Embedding 时自动完成向量索引，能力不可用时必须明确显示降级状态。
- 基础索引完成后，系统在后台自动形成用于发现材料的轻量 Document Knowledge Profile，至少覆盖文档摘要、候选 Topic、术语与别名、关键知识点、关键示例和对应来源位置。
- Document Knowledge Profile 失败不得伪装为资料处理完成；基础索引可继续使用，但页面必须显示画像缺失、失败原因和重试入口。
- 自动结果必须区分候选信息与已批准正式知识，不能把模型生成标签或摘要直接当作正式 Topic、Claim 或事实。
- 正式 Claim 候选抽取、与已有知识的 NEW/COMPLEMENTARY/DUPLICATE/CONFLICT/LOW_CONFIDENCE 比较、Review 和 Proposal 只在用户主动发起整理或分析时运行，不随每次摄取全量自动执行。
- 用户以自然语言或 Topic 发起整理时，系统可以结合全文、语义、Topic、Claim、别名和来源状态召回相关材料，避免依赖用户逐篇翻找。
- 召回结果必须展示命中原因和可打开来源，不能只返回不透明的“相关文档”列表。
- 自动召回后必须先展示建议材料、默认选择状态、版本/来源状态和冲突提示；用户确认前不得开始生成。

### R3 本次材料与输入快照

- 用户可以从自动召回结果、列表多选或其他知识视图确定本次整理使用的材料。
- 用户可以删除误选材料、补充遗漏材料并查看每项命中原因；系统不得把建议材料静默等同于用户确认。
- 首版不提供全局跨页面材料篮；整理发起页必须允许在同一上下文中检索并补充 Source、Document、Claim 或 Smart Collection 结果。
- 页面刷新或暂时离开时，当前未提交的整理草稿应能恢复；草稿不会自动成为 Smart Collection，也不能被其他整理任务隐式复用。
- 发起整理时，系统冻结实际使用的 Source Version、Document Revision、Claim 和其他证据版本，形成可追溯的工作流输入快照。
- 本次材料不默认成为新的持久领域对象；长期复用的动态范围使用 Smart Collection。

### R4 内置整理模板

- 系统提供一组经过验证的内置整理模板，定义输入要求、输出结构、证据规则、冲突处理和结果去向。
- “专题知识文章”基于用户确认的材料先生成并审批大纲，再分章节生成带来源的新 Document Draft；发布仍须创建并批准 Proposal。
- “多文档合并整理”比较选中材料的重复、互补、冲突和独特内容，生成保留有效来源的合并 Proposal 与 Markdown Diff，不直接覆盖任一原文。
- “知识总结报告”围绕 Topic、Smart Collection 或确认材料生成知识覆盖、主要结论、来源、冲突与缺口 Artifact，不自动成为正式知识。
- “面试复习文档”基于已批准知识生成核心概念、问题、追问、代码示例和来源 Artifact，可修订、导出或通过后续 Publish Proposal 进入正式知识。
- 简单阅读摘要由自动 Document Knowledge Profile 覆盖；首批不提供会议纪要模板。
- 模板输出默认形成草稿、Artifact 或 Proposal，不得绕过现有审批与 Safe Writeback。

### R5 自定义模板

- 用户可以复制内置模板，或基于系统支持的整理类型创建 Workspace 范围内的自定义模板；内置模板本身保持只读，避免升级时产生不明确的覆盖关系。
- 自定义模板与普通 Prompt 区分：它是受约束、可校验、可版本化的声明，不是只保存一段提示词。
- 用户可以修改模板名称与说明、适用材料条件、必选与可选章节及其顺序、目标读者、语言、语气、篇幅、是否包含代码/示例/FAQ、默认文件路径与文件名，以及补充整理指令。
- 材料条件只用于校验或建议本次输入，不能代替 Suggested Material Set 的用户确认；模板运行仍必须冻结实际材料版本和模板版本。
- 模板不得修改工作流节点、重试与补偿策略、权限规则，不能调用任意文件、数据库、Git、MCP 或脚本工具，也不能关闭引用、证据、冲突与缺口提示规则。
- 模板结果仍必须遵守既有 Proposal、Approval 和 Safe Writeback 约束；自定义模板不能要求系统直接覆盖正式 Markdown。
- 每次保存模板修改都产生不可变版本；新运行默认使用最新版本，已发起的运行继续绑定其启动时版本，历史结果可以反查实际模板版本。
- 系统在保存和运行前校验模板字段、材料条件、输出结构与结果去向；不合法或试图突破治理边界的配置必须明确拒绝并给出可修正原因。

### R6 文件历史

- 用户不需要理解 Git 命令即可查看当前分支上一个 Document 的统一版本时间线；正式历史以影响该文件的 Git Commit 为事实，Article Revision 和数据库映射用于补充产品语义。
- 由 Safe Writeback 创建的版本展示变更时间、摘要、作者/发起者、Diff，以及关联的 Proposal、Approval、Workflow、Article Revision 和 Git Commit。
- 用户或其他 Git 工具创建的 Commit 同样进入时间线，但必须明确标记为“外部变更”；缺少 Proposal、Approval 或 Workflow 时保持为空，不得伪造系统审批关系。
- 工作树中的未提交修改显示为时间线顶部的“当前未提交改动”，可与 HEAD 比较，但不算作历史版本；存在未提交修改时，恢复和其他 Safe Writeback 必须按现有 strict-clean 规则停止。
- 用户可以比较任意两个仍可读取的历史版本，也可以把历史版本与当前工作树比较；Diff 至少提供内容变化、版本身份和变更来源。
- 用户从时间线选择历史内容恢复时，系统先展示相对当前版本的反向 Diff 和影响范围，再创建新的恢复 Proposal；批准后形成新 Commit，不移动分支到旧 Commit，也不删除或重写后续历史。
- 对外部 Commit 发起恢复时，旧内容只作为新 Proposal 的目标快照与来源，不得反向补造旧 Commit 的审批记录。
- 首版时间线只承诺当前分支及当前 Document 路径可验证的历史，不把远端未拉取分支或任意 Git 图谱包装成文档版本。

### R7 Git 远端同步

- 用户可以为 Workspace 配置一个标准 HTTPS Git Remote；GitHub、GitLab、Gitee 和 Gitea 等服务通过标准 Remote URL 接入，首版不依赖提供商专有 API。
- 首版使用访问令牌认证。令牌必须进入服务端专用加密 Secret Store，响应、日志、审计、Workspace、Git Config 和 Remote URL 均不得暴露或持久化明文令牌。
- 用户可以手动触发同步，也可以为 Workspace 开启“批准写回完成后自动同步”；自动同步不得改变 Proposal、Approval、Safe Writeback 或 Retrieval 完成状态的语义。
- 每次同步先 Fetch 并计算本地分支、远端跟踪分支和工作树状态，再展示或执行动作；状态至少包括未配置、待拉取、待推送、同步中、已同步、离线、认证失败、工作区有改动和历史分叉。
- 远端领先、本地分支无独有提交且工作树严格干净时，系统可以 Fast-forward 本地分支，并按外部变更路径创建/刷新 Source、Revision 和索引投影。
- 本地领先、远端没有独有提交且工作树严格干净时，系统可以 Push；Push 后必须重新校验远端跟踪状态，不能仅凭命令退出码宣称已同步。
- 工作树有未提交修改、本地与远端历史分叉、Fetch 后引用漂移或发生非 Fast-forward 时，系统必须停止并展示双方提交与文件差异，不自动 Merge、Rebase、Force Push、Reset 或 Checkout。
- 手动同步失败不改变本地正式知识；批准写回后的自动同步失败只标记远端未同步并允许重试，不能把已经完成的本地 Commit 或 Proposal 伪装为失败或回滚。
- Git 远端同步只代表 Git 跟踪内容的同步，不得伪装成 PostgreSQL 运行状态、原始大文件或完整应用数据已经备份。
- 首版不支持 SSH Key、多 Remote、提供商账号授权、应用内 Merge/Rebase 和自动冲突解决。

### R8 创作入口与 Document Draft

- 一级业务导航使用“创作”，不再使用含义宽泛的“产出”。
- “新建文章”创建空白 Document Draft，允许用户从自己的标题与正文开始撰写，不要求先进入 Inbox、选择资料或运行 AI。
- 首版新建文章使用 Markdown 编辑器，提供标题、目标路径、正文、实时预览、草稿自动恢复和明确的发布操作；不提供富文本或块编辑器，也不引入 HTML 与 Markdown 双向转换事实源。
- 自动恢复保存当前 Working Draft，不把每次输入都追加为不可变 Article Revision；显式版本节点与发布治理仍使用 Article Revision。
- “整理成文”进入本 PRD 的 Suggested Material Set、用户确认和 Organizing Template 路径，结果同样是 Document Draft。
- Document Draft 在发布前不是正式知识；无论来自手写还是整理生成，发布都必须经过既有 Proposal、Approval 与 Safe Writeback。
- Proposal、Workflow Run 和 Artifact 是创作生命周期中的审批、执行和派生结果视图，不再作为与“新建文章 / 整理成文”并列的一级创作任务。

## Acceptance Criteria

- [x] 用户可以快速记录一条文字、一条 URL 或一个文件，并在 Inbox 中看到真实处理状态。
- [x] 用户可以在任意业务页面通过应用入口或应用内快捷键打开 Quick Capture，保存后回到原上下文。
- [x] 用户可以粘贴、拖入或选择图片/截图；无视觉/OCR 能力时原图仍可靠保存，页面不会声称内容已经完成分析。
- [x] 一篇已处理文档可以展示摘要、候选 Topic、术语别名、关键知识点和可打开来源，候选状态不会被误标为正式知识。
- [x] 摄取完成后自动建立基础索引和轻量 Document Knowledge Profile；画像失败时基础检索仍可使用，失败状态与重试入口对用户可见。
- [x] 没有用户发起整理或分析时，系统不会自动执行全量 Claim 关系比较或创建 Proposal。
- [x] 用户输入一个知识点后，可以获得带命中原因的相关材料候选，而不必逐篇浏览 Workspace。
- [x] 系统在生成前展示建议材料；只有用户明确确认后才启动整理 Workflow。
- [x] 用户可以在整理发起页内搜索补充或删除建议材料，刷新或暂时离开后能够恢复当前整理草稿。
- [x] 系统不存在全局材料篮入口；长期复用范围通过用户明确创建的 Smart Collection 表达。
- [x] 用户确认材料并发起整理后，生成结果可以反查实际使用的每个材料及其版本。
- [x] 系统提供至少一条内置模板的完整整理路径，结果遵守证据、审批和 Safe Writeback 约束。
- [x] 四个首批内置模板均有稳定输入要求、输出类型、证据规则和可观察的审批/发布路径。
- [x] 专题知识文章在正文生成前要求用户确认大纲；多文档合并整理不会静默丢弃差异或覆盖原文。
- [x] 知识总结报告和面试复习文档默认保持 Artifact 身份，除非用户后续明确创建并批准 Publish Proposal。
- [x] 用户可以从内置模板派生自定义模板，并得到版本化、可校验的整理结果。
- [x] 用户可以在自定义模板中配置材料条件、章节结构、表达偏好与默认结果位置，但不能关闭材料确认、证据引用、冲突提示、审批或 Safe Writeback。
- [x] 修改自定义模板后产生新版本；旧的 Workflow 和结果仍能准确展示运行时冻结的模板版本。
- [x] 用户可以查看并比较 Document 历史版本，通过受控流程恢复旧内容而不删除历史。
- [x] Safe Writeback Commit 能展示 Proposal、Approval、Workflow 和 Revision 关联；外部 Commit 可见且明确标识为外部变更，不伪造缺失关系。
- [x] 未提交工作区变化只显示为当前改动；存在这类变化时恢复操作不会覆盖它们。
- [x] 恢复任一可读取历史内容都会创建新的 Proposal，并在批准后创建新 Commit，不执行 reset、checkout 或历史重写。
- [x] 用户可以配置 Git Remote、执行同步并看到明确状态；冲突不会被自动覆盖或伪装成成功。
- [x] 标准 HTTPS Remote 可以使用加密保存的访问令牌完成 Fetch 与 Push，任何产品响应、日志、Git Config 或 Workspace 文件中都不出现明文令牌。
- [x] 远端单向领先且本地干净时可以 Fast-forward；本地单向领先且远端未变化时可以 Push，并在操作后重新验证同步状态。
- [x] 工作区有改动或本地、远端历史分叉时同步停止，用户能查看双方差异，系统不会自动 Merge、Rebase、Force Push、Reset 或 Checkout。
- [x] 批准写回后自动同步失败只产生可重试的远端同步状态，不会回滚或改写已经完成的本地 Proposal、Commit 和索引状态。
- [x] 删除 Smart Collection、清空本次选择或结束整理不会删除任何 Source、Document、Claim 或正式知识。
- [x] 首页和一级“创作”入口都能明确发起“新建文章”与“整理成文”，且不会把任一动作重定向为资料收件箱。
- [x] 已连接首页固定显示“快速记录 / 新建文章 / 整理成文 / 搜索知识”四项真实入口，不显示不可用假入口，也不提供个性化排序。
- [x] “新建文章”可从空白 Document Draft 开始；“整理成文”要求确认材料与模板，两者发布时使用同一 Proposal、Approval 与 Safe Writeback 路径。
- [x] 新建文章支持标题、目标路径、Markdown 正文、实时预览和刷新/暂时离开后的草稿恢复；自动恢复不会为每次输入制造 Article Revision。
- [x] Quick Capture、Document Draft 与 Artifact 在界面名称、结果身份和发布路径上保持可区分，不出现“采集资料即新建文章”或“Artifact 即正式文章”的假成功语义。
- [x] 本范围没有新建 `v2.0` 总 PRD；`docs/product/PRD.md` 仍是唯一产品级 `v1.0` 总 PRD。
- [x] 每个已交付并归档的子任务都记录总 PRD 回填映射：已更新的章节，或无需更新的章节及理由。
- [x] 总 PRD 只陈述已经交付并验证的最终行为，不把待实现能力、技术设想或失败验收项写成既成事实。

## Out Of Scope For The Current Decision Round

- 多用户实时协作和 CRDT 编辑。
- 两套独立 PostgreSQL 实例的多主双向合并。
- 完整移动端客户端、通用 Canvas 和通用无代码工作流设计器。
- 富文本编辑器、块编辑器、协同光标以及 HTML 与 Markdown 双向无损转换。
- 首页快捷入口个性化、排序、使用频率学习和跨设备同步。
- 首版录音、语音转写和操作系统全局快速记录快捷键。
- 全局跨页面材料篮以及多个并行材料篮的生命周期管理。
- 让 AI 自动确认正式 Topic、Claim、Relation 或直接覆盖正式 Markdown。
- 允许自定义模板执行任意脚本、MCP、数据库、文件或 Git 操作，以及建设通用工作流节点编辑器。
- 首版跨分支文档历史、任意 Git 图谱浏览和历史重写。
- 首版 SSH Key、多 Git Remote、提供商专有 API、应用内 Merge/Rebase 和自动冲突解决。

## Key Decisions

- 一级“产出”改为“创作”。“新建文章”和“整理成文”是两条并列创作路径：前者从空白 Document Draft 开始，后者从确认材料与模板开始；两者最终共享 Document Draft 与发布治理链。
- 新建文章首版采用 Markdown 编辑器与实时预览，草稿自动恢复使用 Working Draft；富文本、块编辑器和逐输入 Article Revision 不在首版范围。
- 首页常用动作首版固定为“快速记录 / 新建文章 / 整理成文 / 搜索知识”，最近资料和待办只承担上下文续接。
