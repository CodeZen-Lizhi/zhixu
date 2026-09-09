# TODO2 v2 / NOTE_REVISION 公共契约与兼容修复

本记录保留 2026-09-09 首次统一部署的兼容性报告，并补充随后按用户要求完成的修复。首次部署证据见 [统一交付记录](final-integration-2026-09-09.md)；后续修复证据见 [兼容修复记录](openapi-compatibility-fix.md)。后续修复尚未重新部署，不能把当前工作树的门禁结果视为已运行镜像的结果。

## 当前修复结果与迁移边界

固定 base `a4c16248ce1082ce500aea2a99d640e4e0195ddf` 与 oasdiff `v1.29.1` 复查为 **0 error / 0 warning，exit 0**。OpenAPI 标准检查、211 个 runtime/tag operation、客户端生成及漂移检查均通过；未改变比较基线、工具版本、normalizer、severity、ignore 或告警基线。

| 入口 | HTTP v1 | HTTP v2 |
| --- | --- | --- |
| Question / Turn / Answer / Analysis Timeline | 保留 RAG、历史 Analysis v1 响应与原幂等重放；动态事实返回版本不支持 409 | 新建动态分析，读取或重放新旧事实 |
| Interview 列表 / 新建 / 详情 / 答题 / 完成 | 只返回原 Claim 响应；操作 NOTE 在副作用前返回版本不支持 409 | 读取/操作 Claim 与 NOTE；直接 Start 仍只创建 Claim |
| Learning Path Step 更新 | 原 Claim 步骤响应 | Claim 与冻结 NOTE 来源 |

- 新增十个显式 `/api/v2` operation。v1 列表在数据库 LIMIT 前过滤新来源/Definition，保持有界完整分页；v2 列出新旧事实。两个版本的 cursor 不混用，切换端点从第一页开始。
- **当前生产 v1 新建 Workspace Analysis 返回 `409 CONVERSATION_API_VERSION_UNSUPPORTED`；新建必须使用 v2。** 当前 starter 只创建动态 Definition，本次没有恢复第二套固定 v1 Runtime。历史 v1 replay 与新建 RAG 保留原业务身份，不新增重复调用。
- v1 访问 NOTE 返回 `409 INTERVIEW_API_VERSION_UNSUPPORTED`，发生在 replay、评分、reservation、Artifact 或步骤写入之前。NOTE 仍由既有 synthesis preparation 创建，不伪装为 Claim。
- Web 已切换相应生成 `*V2Raw` 方法。Conversation 创建、Feedback、SSE、Workflow control、Path status、Memory Candidate 及笔记 preparation 等未变化接口继续使用 v1。
- 后续部署需要同步 API 与 Web；本次没有新增数据库迁移或改变模型设置。旧客户端可以继续使用 v1 的上述范围，访问动态分析/NOTE 或新建分析时须迁移相关 operation；不能将 OpenAPI 检查通过表述为旧客户端全部行为不变。

## 1. 修复前首次交付的门禁结果（历史）

首次交付时，对同一受信任 base，锁定的 oasdiff `v1.29.1` 报告 **21 error / 5 warning**。错误与警告集中于响应新增或重组 `oneOf` 分支；不是新路由本身造成的兼容性失败。下表保留当时尚未进行 HTTP 版本隔离的影响面。

| 影响面 | 既有客户端看到的变化 |
| --- | --- |
| GET Answer、POST Question 的 Answer / replay、GET Conversation Turns | `result` 可返回 Workspace Analysis v2，包含动态调用事实、实际预算及 nullable Git |
| GET Answer Analysis Timeline | 响应为明确的 v1/v2 联合；v2 有 `decide_next`、动态顺序与版本化工具调用 |
| Interview 列表、创建、详情、完成 | Scope 可使用 `note_revision`；Question、Score、Finding、PathStep 可使用 NOTE_REVISION 来源 |
| POST Interview Turn、PUT Learning Path Step | 回答结果与后续题目/步骤可绑定冻结笔记与原始来源，正式 Claim Evidence 为空 |

5 条 warning 涉及 Question / follow-up / next-question 的联合；工具提示 `allOf` 的分析限制。没有修改 gate 参数、severity、normalizer、ignore、可信 base 或 warning baseline 来使其通过。

现有日志：`/tmp/zhixu-public-ui-openapi-breaking.log`；修复 Claim 投影后的复核输出写入 `/tmp/zhixu-public-ui-openapi-breaking-current.log`。交付时以实际命令退出码和最终复核记录为准。

## 2. 首次部署的旧协议与新增数据限制（历史）

以下描述修复前已部署版本；当前工作树以开头的显式 HTTP v1/v2 边界为准。

- Workspace Analysis v1 的 result、timeline、budget 和 item Schema 保持原定义，新客户端继续读取旧快照；固定 RAG 继续使用其原合同。
- Claim Scope/Question/Score/Finding/PathStep 保留旧字段形状。Question、Finding、PathStep 仅在 NOTE 分支发送 `source_kind=NOTE_REVISION`；历史隐式 Claim 与显式 Claim 都省略该新增字段。新 Web decoder 在缺失时按 Claim 解码。`legacy_projection_test.go` 对两个 Claim 来源状态的五类公开投影比较完整旧 JSON 字段与值，防止“只增加一个字段”破坏严格旧客户端。
- NOTE Question 的 `claim_id=null`，答前只发送 `note_item`；答案要点、追问计划与原文不在题面响应中。答后 `note_source`、报告与学习步骤绑定同一冻结版本。不能把 NOTE 来源转换为虚假 Claim 来兼容旧客户端。
- **保留旧协议不等于旧客户端能读取新数据。** 旧客户端的已知联合、字段白名单、Git 非空假设或固定阶段假设可能拒绝 v2 / NOTE。含 NOTE 的 Interview 列表、含 v2 的 Conversation Turns 也可能使旧客户端整页拒绝，而不只是单个新详情打不开。
- 路径仍为 `/api/v1`，Interview 的既有 `schema_version` 字符串也继续保留；不能只根据这些名称判断兼容。当前没有客户端版本协商、按旧客户端隐藏新事实或把 v2 自动降级为 v1 的公共机制。

| API / Web 组合 | 支持边界 |
| --- | --- |
| 新 API + 新 Web | 明确解码旧 v1/Claim 与新 v2/NOTE；仍需匹配的 Worker 与 capability 才能创建新工作 |
| 新 API + 旧 Web / 自有严格客户端 | 只保留旧协议读取；读取含新联合分支的数据之前必须升级客户端 |
| 旧 API + 新 Web | 可使用其原有协议范围；不能承诺新端点、v2 准入或 NOTE 工作可用 |
| 含新持久事实后回退 API | 没有已验收的兼容保证，不作为支持的回退路径 |

## 3. 首次统一部署的步骤与结果（历史）

1. 保存实际检查结果与准备部署的代码版本；确认数据库备份和当前迁移位置。不能用推进比较 base 的方式抹掉本次 breaking 记录。
2. 暂停新 Workspace Analysis 提交、自动合成输入与新 NOTE 面试准备，记录存量工作状态。使用同一代码版本构建 API、Worker、Web 与 Migrate，生成客户端来自同一份权威 OpenAPI。
3. 用当前 Migrate 应用追加迁移，保留历史 v1、Claim、Receipt、候选、审批及学习记录。使用当前 API/Worker 的 Definition、Tool catalog、policy、model generation / capability 校验确认可以处理新工作；仅有进程 `/readyz` 不够。
4. 将 API、Worker 和 Web 作为同一交付组启用。旧浏览器标签页应重新加载新版 Web；自有 SDK/严格客户端应先生成并接入独立 v2 / NOTE 分支，再访问新事实。不要让旧客户端依靠吞掉 decoder 错误继续运行。
5. 在隔离实例验证旧 v1/Claim 可读，以及新动态分析和合成笔记的实际 API/Worker/浏览器闭环。TODO4 fixture 的已发布 v1 只有真实 FACT/GAP，当前候选 v2 才包含 CONFLICT；面试必须继续冻结已发布 v1。
6. 记录最终迁移 head、各交付物版本、capability、必要浏览器结果及已知限制，再恢复准入。本轮 M11 全产品最终验收仍单独保留，不用这次 smoke 代替它。

本机已通过受支持的 `./zhixu restart` 执行同版构建、迁移与启动，Atlas head 为 00099；新 Web 路由与认证后列表可用。API/Worker flags 已启用且 config revision 同为 2，但原有模型 active revision 2 为 disabled，desired revision 6 的激活失败状态与升级前备份一致，因此尚未恢复现场 AI 准入。模型可用性不能由进程 ready 代替；本机未执行付费 Provider 请求，动态与 NOTE 闭环证据来自隔离 Compose。

## 4. 回退限制

已有 v2 / NOTE 持久事实后，应保留能够读取这些事实的当前 API 与 Web。先关闭相应准入，排空或稳定终止新 Definition 的可执行节点；仍有 NOTE/v2 工作时，不把它们交给不认识这些 Definition 的旧 Worker。

只有确认工作已安全收敛且旧 Worker 能承担剩余队列后，才评估 Worker 版本回退。数据库继续保留追加 Schema、来源与审批历史；不删除新记录、不改写新 Answer 为 v1、不伪造 Claim、不重复模型付费调用以回到旧版本。Workspace Analysis 的已有顺序见 `docs/architecture/runbooks/workspace-analysis-rollout.md`。

## 5. 受保护分支处理

修复前曾评估按 ADR-0028 的 Approved Breaking Changes 流程处理失败；没有执行审批或管理员绕过。当前已通过独立 v2 operation 恢复原基线门禁，无需为这 21 个 error / 5 个 warning 申请绕过。实际提交、推送、合入和发布仍遵守原授权与受保护分支规则；本次未执行这些操作。
