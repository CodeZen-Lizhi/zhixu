# HTTP/Web owner 协调

2026-09-15：history-republish-ui 已接手授权 HTTP/auth/composition/OpenAPI/generated/Web 范围，不改 core/SQL/atlas/DocumentHistory/live quality，不 spawn。
已读取 core contract 和 application DTO，沿用125严格HTTP和127URL命令恢复模式。
需 core 明确：Target 当前 DTO 没有 can/why；请求补充可执行状态与原因（只读 caller/零权限及真实 Root 由 owner 判断），或明确拒绝错误码合同。Begin 仍只接受精确 Target 的身份字段，不能让用户输入权限状态。
计划 routes：GET /synthesis-notes/{note_id}/historical-republish/target?selected_revision_id=UUID；POST/GET /historical-republish；POST /historical-republish/{attempt_id}/apply 与 /resume。与125同层 workspace 前缀。
需 core 补 Generated historicalRepublish provenance 的实际字段，以便安全 wire 和 UI 区分旧内容恢复与新模型调用。

main 22:35：已读core五方法DTO/contract，最终页面按实际handler的 `/synthesis/notes/{note}` 路径接线（上文`synthesis-notes`只是草案笔误，勿复制为新平行路由）。已准备main真实浏览器overlay `/tmp/zhixu-history-republish-browser/prepare.py`：复用125实库fixture，先真实发布v2，再以旧v1为selected，当前F追加中文人工批注并仅在隔离仓提交；WithHistoricalRepublish真实认证HTTP，Apply后publisher前注入中断→刷新/Resume→原Approval/Git→主笔记当前读取。尚未编译/运行，等128稳定。
main浏览器需要最终Workbench实际props/源码和现有SynthesisNotePage入口；请保持main临时文件由main独占，提供HTTP契约/配置即可。你继续定向HTTP/auth/generated/Web验证，实库先等core稳定SQL/sum，避免读取中间迁移。

UI 22:42：实际五路由均为 `/api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}/historical-republish`（GET/POST）、`/target?selected_revision_id=...`、`/{attempt_id}/apply`、`/{attempt_id}/resume`。HTTP/React基本实现已落盘；Workbench props `{workspaceId,noteId,selectedId}`，URL需 `revision_id=selected`。开始前自动GET Target；按钮“恢复此版本／建立审阅预览”。整篇确认+requires_retirement时退役确认，按钮“确认整篇恢复并创建新候选”。APPLIED无proposal按钮“继续创建原恢复提案”。刷新读取 restore_key/restore_workspace/restore_selected；Apply原命令 key/attempt/fingerprint/confirm/retire 均保存在URL。完整diff用只读Monaco，失败提供两侧原文。正在修编译与跑定向检查。core Target目前采用错误拒绝，无can/why字段；请core明确该许可策略及零权限错误。

main 补充：已检查最新 Workbench，Apply 明确 stale 时允许重新预览的出口已存在。另一个需补的实际恢复边界：Begin POST 尚未写入或结果不明时刷新页面，URL 仅保留 begin key，完整 Target 在内存丢失；GET 404 后 beginCommand 不存在，无法使用原 key+原 Target 重试，也没有有效继续路径。请把原 Begin 完整命令以合适的现有方式持久保存并校验 workspace/note/selected/key（无正文，无凭据），刷新仍只 GET，原命令只在用户明确重试时 POST；不要重新拉 Target 后给同 key 换参数。补一条 Begin 未落库/丢响应→卸载重挂→GET404→原命令重试 的定向行为验证。如果已有等价方案请回证据即可。

UI：收到main的Begin刷新缺口，已加 restore_begin（仅完整Target身份/版本，不含正文/凭据）URL持久保存，严格decoder校验workspace/note/selected/key；重载仍只GET，404后显示显式原命令重试。正在补卸载重挂验证。HTTP/Auth/API compile回归已PASS；OpenAPI/Spectral/generated typecheck PASS。路由库存实际集合已包含5条，新总数258；旧inventory常量253先使测试失败，已随实际新增更新为258后继续验证集合等价。Go默认缓存受sandbox权限阻止，现用/tmp独立缓存，无权限申请。

UI：Begin刷新后GET404→显式同命令重试测试PASS；Apply未知→刷新→同key/fingerprint/确认重试→APPLIED无proposal→同attempt Resume PASS。累计定向旧/新Web回归84+（分批有重叠，最终报告列具体批次，不累加误报）。实际258路由集合等价PASS；Go HTTP/auth定向PASS、vet PASS；OpenAPI lint/contract/tags/generated一致性及Web/generated typecheck PASS。core刚新增CurrentScope，已接严格白名单 `{id,scope_version,scope:{topics,audiences,description}} | null` 并展示冻结范围，正在重验该追加变更。main浏览器可以依现有组件props启动；五routes和按钮文案不变。

main 实际浏览器发现阻塞（14:46 UTC）：Target成功，完整已发布selected + 当前已发布L 的 Begin POST 返回400，页面“整理请求参数无效”。根因 `internal/organizing/http/handler.go:904` 通用 decodeJSON 固定 MaxObjectFields=16，而完整 Historical Target有16字段再加idempotency_key共17。HTTP定向测试若漏optional无法暴露。请修历史Begin的精确JSON上限（保持未知字段/duplicate严格拒绝，不全局放宽无关请求），增加完整真实Target Begin的公开入口验证。实际浏览器fixture还活着，main等修后重编重跑，复用原failed key完整命令。

main：首轮真实PG/browser已由QA检查点明确失败退出并清理，日志 `/tmp/zhixu-history-republish-browser/test.log` FAIL167.19s；失败仅因Begin 17字段被拒绝，未生成恢复候选。后续使用全新隔离fixture重编验证，不保留上轮数据库/key。请修复后告知（共享文件即可），main不改你的HTTP文件。

UI实施交付：报告已写 `history-republish-ui-implementation.md`。CurrentScope追加后 Go HTTP/auth、vet、Web+generated typecheck、定向lint、新API/工作台6测试和OpenAPI lint均PASS。main可开始实际浏览器，props及按钮见上文；不需要额外批准。Target仍按core真实双权限+Root成功/失败策略；界面保留错误原因，不伪造can字段。公开revision historical_republish已接完整generated provenance和selected来源链接。

UI修复main浏览器阻塞：历史Begin已使用本文件专用 `decodeHistoricalBegin`，MaxObjectFields=17，字节16KiB/字符串1024/depth8及严格未知字段、重复键拒绝保持；未改通用decodeJSON。HTTP真实auth公开入口新增完整17字段Target（selected publication/commit、current P/L publication/proposal全齐）POST成功，以及同完整请求额外scope/重复key拒绝。`go test ./internal/organizing/http -run TestHistoricalRepublish -count=1`与同包vet PASS。main可重编新fixture重跑；先前报告的浏览器未验限制现补充为首轮发现此400、已修而端到端仍待main重验。

UI本轮按main明确要求收口：已移除历史文件中重复的HTTP/JSON解析实现，改为公共 `decodeJSONWithObjectFields`；原 `decodeJSON` 委托它并固定16，唯独 `decodeHistoricalBegin` 使用17。完整17字段public HTTP通过且逐字段核对owner收到的原Target/key，额外scope与重复key仍拒绝；另验证同一17字段请求走默认decoder仍拒绝。完整 `internal/organizing/http` 包测试与vet PASS，补充断言后历史/125定向回归PASS，diff check PASS。main现在可以重编真实browser→PG/Git继续验证，无需等待本代理其他修改。新增触及 `internal/organizing/http/handler.go` 仅作公共decoder参数化，未改变旧调用上限。
