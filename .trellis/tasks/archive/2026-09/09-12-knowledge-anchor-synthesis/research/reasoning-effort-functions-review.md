# 按功能强度与 BGE 兼容的整合复核

2026-09-16，前半部分记录主会话整合检查；末尾记录随后完成的独立Trellis审查。两者均未发现本轮待修P1/P2，验证范围及未验证项分别列明。

核对了固定8键三态、strict JSON/静态配置、配置map复制、130封闭SQL约束及旧行默认、实际worker初始/重建两处接线、结构化与RuntimeChat同档位构造和生命周期、SDK调用不可覆盖、连接测试的去重/首错停止/共享deadline，以及API读写和UI编码的一致性。

主会话额外使用真实PG/HTTP/浏览器验证不同功能值、显式模型默认、继承以及全局变化后保存/reload的一致性；直接SQL比较全部三条不可变revision，active不被保存推进。移动/桌面截图已查看，详见browser报告。

BGE兼容仅对精确模型名省略不支持的dimensions请求参数，响应合同的1024维要求不变。现有其他模型的维度发送验证、BGE错误宽度拒绝和真实生产Eino向量调用通过。未引入SDK、Provider、重试或维度转换。相关Go vet退出0。

本轮检查未发现待修P1/P2。没有追加全仓测试或重复外部调用。已纠正研究文档中枚举建议与最终wire空字符串的差异，并更正构造temperature不能代表最终wire的旧限制描述。

限制：八个功能并非各自做一次完整端到端流程；实际Worker覆盖文件画像和融合，其他消费者由接线复核与冻结adapter测试覆盖。完整API+Worker热应用未重新跑端到端；生产运行实例尚未升级，BGE仅已保存desired7。聊天网关503使真实全文复核/融合语义质量仍未验收。没有提交、部署、迁移实际用户数据库或宣称完整PRD验收完成。

## 独立 Trellis 审查（2026-09-16）

由 `reasoning_check` 独立完成，范围为按功能思考强度（00130）及 BGE 请求兼容；区别于上方主会话整合自检，也不复用 00129 的“无功能覆盖”结论。已读取本次授权契约、研究/实施报告、实际变更和直接调用方。

### Findings (fixed)

本轮未发现需要修复的 P1/P2，未修改实现代码。

### Findings (not fixed)

无未解决的已证实缺陷。功能配置与 BGE 相关稳定规范已经同步。

### 核对结果

- 固定八键在 domain、静态 YAML/JSON 环境变量、HTTP、SQL、OpenAPI 和前端一致。缺 key/显式空值/显式档位三态没有被合并；JSON 拒绝重复、未知和 null，disabled/Ollama 拒绝任何非空覆盖。环境对象整体替换 YAML，`{}` 可以清除覆盖。
- GORM Save/ResolveDraft 复制输入 map，读取显式解码；00130 非空默认与封闭 CHECK 保持历史行及 append-only 语义。新增字段不改变 Secret AAD，响应仍是无凭据的 desired/active 投影。
- 运行时只持有已构建能力，不保留输入 map；按有效档位复用结构化与普通 Chat 成对实例。正常关闭及部分构建失败会释放全部独占资源；已存在的 generation lease/Attempt revision 选择继续控制历史任务。API 与 Worker 激活探测均使用相同的去重探测方法，在原上下文期限内首错停止。
- Worker 初始和 managed 重建两处均明确选择功能；问答的结构化阶段/工具 Agent/流式回答使用 knowledge_qna，工作区分析的结构化及普通运行时使用 workspace_analysis，访谈构造处选择 note_interview；文件画像、整理、范围建议、融合和当前全文来源复核分别绑定研究中的功能。剩余 `Chat()` 使用位于能力/超时检查和连接探测，未发现业务生成绕过配置。
- Stream/WithTools 继续共用所选能力的 transport，不能用 SDK option 替换冻结强度。所有功能共用原模型身份、预算和超时，没有新增按 Prompt 猜测功能或自动选档。
- UI 保存和测试共用编码器，功能编辑复制 map，继承删除 key，模型默认保留空值；active 摘要来自实际 active。BGE 仅对两个精确模型名省略请求 Dimensions，响应模型/宽度/有限值/归一化及 wire 交叉校验继续生效。

### Verification

复用本次最终代码已有的成功检查，不重跑已通过范围：

- Lint：PASS，Go vet、前端定向 ESLint、OpenAPI/project/routes/生成检查、Atlas hash/validate/lint。
- TypeCheck：PASS，前端与生成客户端 TypeScript、API/Worker 编译。
- Tests：PASS，受影响 Go 包、56 项前端检查；已读取真实 PG/运行时 9.994s、实际 Worker 16.862s、资源 race 1.760s 的成功日志。日志分别为 `/tmp/zhixu-function-reasoning-{unit,runtime,worker,resource-race,binary-compile,openapi-check}.log`。
- 00130 升级旧行保持、非法 JSON 拒绝和 schema 独立恢复通过；浏览器保存/完整刷新 240.54s、三条不可变 revision 与 active0 的 SQL 断言，复用 [真实页面报告](reasoning-effort-functions-browser.md)。BGE 本地正确/错误宽度与真实生产 Eino smoke，复用 [兼容验证报告](bge-embedding-compat.md)。

验证边界：独立审查没有调用外部 Provider、读凭据或操作实际配置。完整 API/Worker 双进程热激活、全部八功能各自完整业务流程和新的 SSE 档位动态流程未另行执行；这些不应由编译/接线检查替代。BGE 成功不证明聊天模型语义质量，也不代表生产实例已升级或配置已激活。
