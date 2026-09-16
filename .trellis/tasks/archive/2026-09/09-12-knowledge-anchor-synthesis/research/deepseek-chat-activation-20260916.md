# DeepSeek Chat 切换与真实质量验收

2026-09-16。用户最新指定 `https://api.shenwenai.com` 的 `deepseek-v4.1-flash`，继续 Chat Completions。配置已通过既有加密设置保存为 desired revision 10，Embedding 保留 SiliconFlow `BAAI/bge-m3`。本报告不保存凭据。

## 已定位和修复的探测失败

- API 单次真实连接测试通过（1984 ms），但 Worker activation 失败，active 仍安全保留 revision 8。
- 从实际 Worker 容器重放相同 64 token 探测，返回 model 一致、`finish_reason=length`、空 content、有 reasoning、completion_tokens=64。不是协议不支持或密钥拒绝。
- 相同容器/网关改为 2048 探测上限后得到 `finish_reason=stop`、11 字符最终 content、completion_tokens=41。上限不是固定实际消耗。
- `ProbeConnection` 统一使用 2048，兼容未显式设置强度时默认推理的模型，不按模型名猜测。生产业务请求预算、reasoning 参数选择和重试行为不变。
- Eino Adapter 现在保留固定 validation reason；截断优先于空 content。原严格接受/拒绝条件不变，不保留原始响应或推理文本。
- 7 个定向生产 Adapter 测试通过（3.223s），包级 vet、范围内空白检查通过。主会话审查了诊断贯穿、允许条件、资源/预算及脱敏边界，未发现本次残留缺陷。

## 真实全文补源结果

旧验收预算 2048 下，支持案例通过，全文撤销旧结论案例响应截断；新增安全诊断明确为 `finish_reason_length`，不是模型判定支持旧结论。

生产 Worker 原本使用 `agentStructuredMaxOutputTokens=8192`；验收工具上限与其对齐，不增加生产预算，也不改变功能强度。调用数量和总超时仍受原硬限约束。变更后的全部 `TestSynthesisLive*` 离线检查通过（1.098s），真实入口显式 Skip。

按生产上限执行真实两例：32.41s、2/4 次调用、通过。私有合成产物 `.zhixu/diagnostics/deepseek-source_review-c39175bca3.json`：

- 支持案例：当前全文限定快速变化 Redis 值一分钟，来源支持这两段当前内容；两条义务均 SUPPORTED，绑定 P001/P002。
- 拒绝案例：全文第二段明确撤销前段五分钟规则，来源仅复述旧五分钟规则；两条义务均 UNSUPPORTED、无支持段落。

主会话逐项检查了完整合成当前正文、两份来源原文、实际输出与绑定，确认以上结论；不以模型自评代替内容检查。这是合成资料下的真实模型语义证据，不替代此前 PG/River/授权/发布验收，也不证明任意真实资料上的一般准确率。

## 五类融合尚未通过

真实入口按生产 8192 上限执行，首例 `redis_rejects_mysql` 在独立 semantic 阶段失败，72.04s、3/12 次调用，后四例按约定未执行。私有产物 `.zhixu/diagnostics/deepseek-synthesis-f378300631.json`：生成正确返回 N001 空 operations；第一次复核用了不符合固定 Schema 的 `kind`、`source_verdicts` 字段，进入既有有限结构修复；修复调用最终 `finish_reason_length`。不能仅凭生成不改变 Redis 正文就宣称完整范围验收通过，也不能把它记为模型误纳 MySQL。未继续增加业务预算、放宽校验或自动降低强度。

## 服务恢复被目录身份边界阻止

正式 `./zhixu restart` 已完成镜像构建与既有 bootstrap，但在 Workspace control 返回 `WORKSPACE_IDENTITY_CONFLICT`，命令失败。当前 PostgreSQL、local-model-runtime 与 netns helper healthy，API/Worker 未启动，运行态 degraded。不是新 Chat 已启用；最后成功模型 active 仍为 8，desired 10 等待后续精确激活。

当前路径仍为 `/Users/zhenglizhi/Documents/files/zhixu`，Workspace ID 仍为 `a68d94c7-17b4-4fa5-8ef6-5747db51af28`。Registry 现为 inactive / migration_required / `WORKSPACE_ROOT_IDENTITY_CHANGED`，原 binding generation 3 保留。旧 fingerprint `4bf4e2f8b02dd4b11315db16c44ee96aefcb3c8bf5755cc61cc2dd7930562c00`，当前宿主实际 fingerprint `8a8b49060459276b1ebf12b7c2f41fe117b3363e9abd4eaedbb504c92e96ea48`。

通过只读校验确认：先前 `bge-upgrade-20260916` 备份中的 19 个文件（含 Git）全部逐字节一致，无缺失或变化；部署开始前记录的普通文件 hash 也未改变。没有自动改写 Registry、重建目录或绕过 Root 授权。

已向用户请求确认此路径仍是原笔记工作区并授权重新绑定。项目运行手册要求确认原工作区恢复身份后使用 `./zhixu workspace rebind --confirm REBIND`；用户确认后先执行这一受控恢复、验证 API/Worker ready，再通过既有设置 API 激活 exact desired 10。未收到确认前不执行 rebind；不是自动审批系统拒绝。


## 版本化格式修复与生产超时复验

后续新增显式冻结的 semantic v6：完整可信 Schema/精确字段进入复核提示，GENERATE 及旧 v1–v5 文本保持；Go/SQL proof 同步使用冻结版本。前向迁移00131、130旧账本升级后 apply、普通/锚点/目标/正文刷新实际持久链路、旧失败/未知不重复付费、定向 Go/vet/Atlas 验证通过。独立审查无待修问题，详见 `semantic-format-implementation.md`、`semantic-format-review.md`。正式数据库尚未执行131。

重新核对 desired10：单次 chat timeout 为30秒，模型默认思考强度为空、功能覆盖为空。本轮未修改这些设置。

按保存配置的30秒上限复验：

- 全文补源两例通过，50.97s、3/4次调用，产物 `.zhixu/diagnostics/deepseek-source_review-4833cda147.json`。正例来源支持当前快速变化值一分钟规则及范围，两义务均绑定当前P001/P002；负例全文撤销旧五分钟规则，来源仅旧规则，两义务均 UNSUPPORTED，并定位当前否定段落P002。主会话逐项检查当前全文、精确来源、raw checks与绑定，认可语义结果。负例首次输出裸数组，既有限定 repair 修正成 checks 对象；不宣称每例只有一次调用。
- 五类融合的首例在 GENERATE 超时，30.01s、1/12次调用，`MODEL_CHAT_TIMEOUT` / `AGENT_OPERATION_DEADLINE_EXCEEDED`；尚未进入v6复核，其余四例停止。产物 `.zhixu/diagnostics/deepseek-synthesis-5ed4c6189b.json`。不能宣称30秒配置可完成融合，也不能把它判为复核格式修复失败。

为区分时延与语义，另做一次90秒等待上限的隔离诊断，保留8192输出/12次调用/10分钟总预算及默认思考强度；这不修改已保存配置，不代表生产30秒验收通过。本轮诊断结果如下。


90秒诊断58.41s、7/12次调用，产物 `.zhixu/diagnostics/deepseek-synthesis-c9ff087e06.json`：

- `redis_rejects_mysql`：12.52s，生成 `notes:[]`；v6复核合法，原Redis结论、适用条件及引用保持。MySQL未纳入。主会话已检查来源、delta和渲染正文。
- `mixed_interview`：37.67s，首次生成把 statement 字段放平，既有repair修正后，三条事实分别是Redis五分钟、MySQL InnoDB聚簇主键、Oracle读一致性，均精确引用原片段并保留数据库面试语境；ORBIT招聘/航海经历未纳入。v6三条独立复核合法。主会话已逐项核对，不只依据模型SUPPORTED。
- `duplicate`：8.22s，来自不同Source/Span的同文S002未记录补充来源，生成 `notes:[]`；独立v6 NO_CHANGE也给出SUPPORTED。验收拒绝这一结果。PRD第40/56行明确要求“仅追加补充来源记录”且补充来源可查，此处缺少新增S002，不是可接受的正文去重。此案例只证实错误接受的语义结果，没有在正式PG/发布链路写入资料。
- `different_conditions` / `opposing_unknown` 尚未执行，按失败即停约定不继续消耗调用。

因此，v6格式修复已有真实成功证据，但融合质量仍未通过；等待更久不能修复遗漏补源。后续应围绕“不同S-label表示独立来源、相同文字不等于同一来源”的生成和NO_CHANGE复核义务修复，保留纯补源不改正文、人工全文独立核对、不可变提示版本/预算边界。不能放松测试、通过补默认操作伪造模型通过、跳过历史身份或自动改思考强度。当前模型保存/active状态及目录恢复阻塞保持前述事实。

## 来源身份修复及融合目标上下文

同文不同来源修复已实现：新生成v7–v10、semantic v7、冻结版本及迁移132；确定的来源遗漏在业务层拒绝，不代模型补操作。定向Go、真实隔离PG/River补源重读与重放、131→132旧v6账本继续apply、目标/正文/刷新、vet及Atlas通过；见 `source-identity-implementation.md`。独立审查发现必补候选可能超过8个输出上限，现已增加付费前容量拒绝并通过9目标零副作用、8目标完整补源和重放检查；复审结论见 `source-identity-review.md`，实施见 `source-identity-preflight.md`。

按保存的30秒配置进行一次真实新版本融合：`.zhixu/diagnostics/deepseek-synthesis-e28c338166.json`，13.73s、2/12次调用。首例生成了独立MySQL笔记，复核SUPPORTED，测试以“目标外生成”拒绝，后四例未执行。主会话核对了输入与原始输出：此时合成夹具虽带Redis Anchor，却没有SourceEvent.Fusion，等同仍允许创建其他主题笔记的普通source-ready；不能把这一结果写成模型向Redis正文误纳MySQL。进一步确认正式Fusion虽然在业务层限制唯一目标，Provider投影尚未传递Fusion目标标签。夹具已改为真实审批融合形状，并正在补齐版本化目标上下文；不放松原质量断言。

本轮为live产物增加每调用耗时/token用量、实际timeout/输出上限和质量断言失败分类；不会将“模型协议合法但业务断言失败”写成成功。最新fixture及离线live工具检查通过0.669s，未以此替代真实模型证据。

融合唯一目标现已版本化落地（生成v11/v12、复核v8、迁移133），定向Go、实际隔离PG融合、132→133旧账本、人工正文流程、vet和Atlas通过，见 `fusion-target-implementation.md`。主会话核对新字段只传N-label，唯一目标的Note/Anchor/ScopeVersion/来源集合在付费前绑定，旧payload保持omitempty不变；未发现这一范围的确定遗留问题。

真实新调用 `.zhixu/diagnostics/deepseek-synthesis-1dfbcdb8e2.json`：保存的30秒/8192预算，10/12次调用、57.41s，总输入13277/输出7675token。

- Redis范围（6.41s）：notes=[]，原五分钟/稳定值/原引用保留，MySQL未纳入且未另建笔记。
- 混合面试（16.52s）：首次ADD_FACT字段放平，既有repair后合法；正文仅Redis五分钟、MySQL聚簇主键、Oracle读一致性，保留数据库面试语境，无ORBIT或航海内容，三条精确引用S001。
- 同文补源（5.36s）：精确ADD_SUPPORT I001、S002，独立复核通过。domain apply为Changed=false/SourcesChanged=true，原事实与条件不变，两个不同文件片段身份均保留。这次合成内存实验不宣称真实发布文件已更新；持久supplement重读/幂等另有PG证据。
- 不同条件（29.12s）：三次输出语义均为快速变化Redis值一分钟，但依次使用fact对象、放平字段、fact对象，正确wire为statement对象。三阶段结构校验耗尽，未进入semantic，后续相反观点例未执行。

主会话逐项核对了全部原文、raw response与生成正文，不以模型SUPPORTED替代判断。三项成功不等于完整融合验收。格式错误仍是确定缺口：完整Schema目前仅在response_format，消息描述不够明确，repair只有统一错误码；下一步以新冻结生成版本将准确wire/Schema放入可信消息，保持旧提示与严格校验、调用次数、思考强度和预算，不用延长超时或放宽结构来掩盖失败。

## 生成格式修复后的四项成功与旧来源复核缺口

新生成v13–v18在可信消息包含完整准确Schema，semantic仍普通v7/Fusion v8；旧v1–v12 PromptDefinition及Schema字节经临时catalog对比保持一致，迁移134、定向Go/PG/River/旧证明兼容、vet及Atlas通过，见 `generation-format-implementation.md`。

真实产物 `.zhixu/diagnostics/deepseek-synthesis-c4e99ee8b5.json`，30秒/8192预算，10次调用50.57s，输入20833/输出6081token，全部生成与复核均首答为合法结构：

- 范围排除、混合面试及同文补源继续通过；本轮混合三项均精确引用S001，保留原数据库面试上下文，未纳入个人经历。
- 不同条件通过：新增“快速变化Redis值，一分钟”，只引用S002；原“稳定值，五分钟”和S001保留，没有合并或抹掉条件。
- 相反观点生成正确ADD_CONFLICT：旧稳定值五分钟引用S001，新一分钟、非五分钟引用S002，并明确新来源未给适用条件。独立复核却将第一项及冲突关系判UNSUPPORTED，返回 `SYNTHESIS_SEMANTIC_REJECTED`，未应用。

主会话检查发现：新semantic传递 `scope.allowed_sources=[S002]`，但非NO_CHANGE请求没有说明S001来自目标现有可信条目。通过两次隔离对照补入 `existing_sources=[S001]` 和资格规则：原五分钟/新一分钟/冲突关系均SUPPORTED；将第一项人为改为十五分钟后，该项和S001仍UNSUPPORTED。产物 `.zhixu/diagnostics/deepseek-fusion-review-9fc9ea6e3b.json`，18.54s、2调用，仍30秒/8192。该证据支持“旧证据资格信息缺失”的诊断，不证明任意模型判断绝对准确；没有新增持久模型证明或发布事实。

后续以独立semantic版本补充从当前目标可信items/有效supplements与已加载来源精确相交的旧来源标签；只说明引用资格，不跳过逐来源内容与条件核验，不从MachineItems或incoming=false推断。正式运行及目录重绑仍保持前述阻塞。

## 五类真实融合最终通过

Fusion独立复核v9加入精确existing_sources后，真实产物 `.zhixu/diagnostics/deepseek-synthesis-890bca50b5.json` 的五类全部通过：保存的30秒/8192配置，10/12次调用，36.71s，输入22252/输出4145/总26397token。每例生成与独立复核各一次，没有格式repair；没有临时降低思考强度或增加生产超时/预算。

主会话已读取并逐项人工核对五例完整合成输入、原始十次输出、最终条目及正文：

| 场景 | 人工核验结果 |
| --- | --- |
| Redis拒绝MySQL | notes=[]；原稳定值五分钟、条件、S001保持；未纳入MySQL、未另建主题 |
| 混合面试 | 只生成一篇数据库面试主笔记；Redis五分钟、MySQL聚簇主键、Oracle读一致性三项均引S001，保留模块语境，无ORBIT/航海内容 |
| 同文不同来源 | ADD_SUPPORT精确绑定I001/S002；原事实与稳定条件保持，原S001和新S002身份均保留；Changed=false/SourcesChanged=true |
| 不同适用条件 | 保留稳定值五分钟/S001，增加快速变化值一分钟/S002，未把两种条件合并或抹掉 |
| 相反观点、条件未知 | 原五分钟稳定值保留；CONFLICT双方分别引用S001/S002，新观点明确未给适用条件；另记待确认问题，不补造条件、不裁定哪方正确。GAP以S002缺少条件为问题依据，旧规则在既有事实/冲突分支可追溯S001 |

这些是指定模型在有界合成场景下的实际质量证据；不是任意笔记上的准确率保证，也不是正式API/Worker已启用、已写入用户笔记或已完成发布的证明。此前独立全文支持/撤销旧结论两例的30秒真实成功证据仍有效，相关SourceReview协议本轮未变。正式启用仍等待确认原Workspace目录身份并执行受控rebind；最后成功active8、desired10状态未改变。


## 本轮最终代码核验与启用边界

来源身份容量P2已独立复审关闭，133–135最终独立复审新增/开放确定问题均为0，见 `fusion-format-final-review.md`。最新135四包Go、四包integration-tag vet、新Fusion真实PG/River/proof/apply、134旧semantic8账本升级后继续apply、未知结果零额外调用、旧Prompt/Schema字节比较及Atlas hash/validate/lint均通过；详见 `fusion-existing-evidence-implementation.md`。main文档diff检查和live harness格式检查通过。没有重复全仓测试、修改前端或额外消费模型调用。

最终只读 `./zhixu status` 仍为runtime degraded：PostgreSQL/local-model-runtime/netns healthy，API/Worker未启动。正式数据库仍未部署131–135，desired10未激活。本轮未重绑目录、修改正式配置、写用户文件、提交或推送。模型配置所需凭据已具备，剩余阻塞是原目录身份确认，不是缺少API配置。

## 用户授权后：原目录恢复与模型正式启用

2026-09-16 14:22–14:25（北京时间）。用户明确回复“是原笔记目录，允许重新绑定恢复服务”，此前目录确认阻塞解除。

1. 执行运行手册规定的 `./zhixu workspace rebind --confirm REBIND`，退出0。launcher构建最新镜像、正常bootstrap迁移、受控重绑并在API/Worker ready后提交selection；未修改Registry SQL、删除volume或重建工作区。
2. 正式数据库读取确认00131–00135全部applied=total=1。Workspace `a68d94c7-17b4-4fa5-8ef6-5747db51af28` 及Root `/Users/zhenglizhi/Documents/files/zhixu` 保持；Registry active/available、availability_reason为空。持久审计记录binding3→4，旧fingerprint `4bf4e2f8b02dd4b11315db16c44ee96aefcb3c8bf5755cc61cc2dd7930562c00` → `8a8b49060459276b1ebf12b7c2f41fe117b3363e9abd4eaedbb504c92e96ea48`。API与Worker均为同一Workspace、binding4、active，新鲜心跳约1.7秒。
3. `./zhixu status` 显示8个运行容器healthy、runtime ready、Workspace selected、grant active。`GET /readyz` HTTP200 / `{"status":"ready"}`。
4. 通过现有认证/CSRF设置API精确激活已加密保存的desired10。经历preparing→arming→idle，active/API applied/Worker applied均为10。随后另建认证Session独立GET复读，仍desired=active=10、API/Worker active且fresh、rollout idle且last_error_code为空；两个临时Session均正常撤销。
5. 正式Chat为 `https://api.shenwenai.com/v1`、`deepseek-v4.1-flash`、`chat_completions`；Embedding继续为SiliconFlow `BAAI/bge-m3`、1024/l2/cosine。未调整原30秒超时或默认/按功能思考强度，也未创建新配置revision。
6. 恢复后再次只读比较 `bge-upgrade-20260916/workspace.tar.gz`：19个普通文件含Git全部逐字节一致，missing=0、changed=0。没有对个人笔记触发专项重建索引、融合或发布。

部署日志 `/tmp/zhixu-rebind-20260916.log`，激活日志 `/tmp/zhixu-deepseek-activation-20260916.log`；最终公开DTO快照 `.zhixu/diagnostics/deepseek-active.json` 权限0600，无明文API Key。运行API镜像 `sha256:ac3e8a8cc7bcb79b4e4bc07dfec30a0f2e50280fe3fec7b0a9db93356625aea7`，Worker镜像 `sha256:8b57a48979b10d1e3163eb7e87e9486c5a639a018b28122e9f51f3adb69c553c`。

本次实际运行证据补齐此前仅有独立模型调用/隔离PG的边界，正式服务和指定模型已生效。此前全文两例与五类融合质量证据保持，不重复付费运行相同场景；不把配置激活解释为任意用户资料质量已验收。当前批准交付范围已完成，保留remaining-acceptance.md列出的验证限制；未提交或推送代码。
