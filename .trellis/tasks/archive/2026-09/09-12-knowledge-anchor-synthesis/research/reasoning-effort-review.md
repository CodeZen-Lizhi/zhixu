# 思考强度增量独立审查

2026-09-16，`trellis-check`。只审查本次思考强度增量，未重审既有主笔记功能；没有提交、改动用户配置或运行外部模型。

## Findings (fixed)

没有发现需要自行修复的 P1/P2；本轮未修改实现代码。

## Findings (not fixed)

无未解决的已证实缺陷。规范同步由主会话按本次稳定契约完成。

## 审查结论

- `ModelSettingsPanel` 的 desired 草稿、保存和连接测试共用 encoder；active 摘要只使用服务器 active 投影，单独保存不伪装已经生效。空值与五档枚举、disabled/Ollama 限制在 HTTP/domain/config/SQL/OpenAPI/前端边界一致。旧请求省略字段归一为空值是本次明确的兼容契约。
- `SaveDesired` 参数化写入、`revisionColumns`/scan 顺序及 00129/schema 一致。新增列为空默认，revision 仍 append-only；Secret 的 AAD/provider/endpoint 约束未改变，keep 继续按新 revision 重加密。
- 历史加载及热应用 factory 按精确 revision 加载完整 settings，overlay 同时覆盖两类 Chat capability；generation/Attempt 原有冻结与 admission 协议保持。没有按业务任务自动分档。普通 Runtime 在最终请求边界覆盖 SDK 的 effort，防止调用参数暗中替换已选档位。
- 结构化 Chat 与普通 Generate 使用保存的相同 effort；空值不发送字段。GPT-6 或显式强度使用原数值的 `max_completion_tokens`，移除不兼容采样参数。Stream 和 WithTools 派生模型沿用同一 HTTPClient/transport，此项通过实际调用链审查确认，未额外宣称新强度的 SSE 动态测试已执行。连接探测有独立的 2048 上限，普通业务预算未加大。

## Verification

复用同一最终代码的已执行证据，避免重复运行通过的检查：

- Lint：PASS。后端 `go vet`、前端 4 文件 ESLint、OpenAPI lint/project/routes/tags、Atlas validate/lint；证据见 [后端报告](reasoning-effort-backend.md) 和 [前端报告](reasoning-effort-ui.md)。
- TypeCheck：PASS。Web `npm run typecheck`；受影响 Go 包编译随定向测试通过。
- Tests：PASS。后端六包定向测试；两个 PG 测试分别 10.971s/8.545s；前端 53 项。已读取 `/tmp/zhixu-reasoning-unit.log`、`/tmp/zhixu-reasoning-postgres.log`、`/tmp/zhixu-reasoning-runtime.log` 和迁移日志的成功结果。
- 真实页面：复用 [浏览器报告](reasoning-effort-browser.md) 的 383.428s 通过结果。真实 PG/Service/HTTP/React 保存默认→高→最高→默认后完整刷新，形成四个不可变 revision，active 始终为 0；手机无横向溢出。
- 迁移：真实 128→129 旧行保留、非法值与历史更新拒绝；独立 schema 恢复通过，见后端报告及 `/tmp/zhixu-reasoning-schema-129.log`。

限制：未运行真实外部 Provider，不能判定各档位语义质量、实际成本或任务预算是否足够；未执行完整 API/Worker 双进程 Compose 热激活。GPT-6 工具工作流所需 Responses 接口不在本次范围内，不能由本次 Chat 参数兼容推定已支持。
