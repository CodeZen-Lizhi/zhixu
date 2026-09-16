# 按功能配置思考强度

2026-09-16，用户在统一强度完成后明确要求按功能分别设置，以控制成本。该要求覆盖前一版“仅统一强度、不提供功能覆盖”的边界。

- 功能的单独设置优先于全局默认。未单独设置时继承全局；功能显式选择“模型默认”时不指定Provider强度，即使全局为高档。继承和模型默认必须在UI及数据中区分。
- 用户设置，不按模型主观判断自动挑选档位；不默认把所有核验改成高档。旧配置升级后所有功能继承原全局值。
- 配置随不可变模型revision保存、应用及回读；在途任务继续原冻结revision。实际业务路径绑定稳定功能标识，不能从用户Prompt正文猜功能，不由任意SDK option覆盖。
- 先根据现有实际消费者划分功能，功能名称面向用户；不引入每功能独立模型、Provider、密钥或Responses协议。
- 后端负责配置/存储130/API/运行时选择和生成客户端；前端负责功能选择及继承/默认的清晰展示；主会话整合实际页面与规范。
- 本轮另有用户授权的实际模型验收与BGE向量模型切换，密钥不记入文档。两项并行，不替代本功能。

固定功能键已确定：`file_profile`、`knowledge_organization`、`anchor_scope`、`main_note_synthesis`、`manuscript_source_review`、`knowledge_qna`、`workspace_analysis`、`note_interview`。

HTTP 字段为 `chat.reasoning_effort_by_function`：对象中缺 key 表示继承，显式空字符串表示模型默认。请求省略整个字段表示全部继承；GET 必须返回对象。未知 key、null 或非法档位拒绝。

状态：按功能配置已实现并完成定向验证。前端56项检查、真实PG/HTTP/浏览器保存刷新、两代配置的真实协议请求、实际Worker导入/融合调用、资源race、API/Worker编译和130迁移/恢复均通过。主会话完成跨层整合复核，随后独立Trellis审查未发现待修P1/P2。未部署实际运行实例，未把本地fixture记为外部模型语义质量完成。

证据：reasoning-effort-functions-backend.md、reasoning-effort-functions-ui.md、reasoning-effort-functions-browser.md、reasoning-effort-functions-review.md。BGE模型实际配置与外部调用见 bge-embedding-compat.md。
