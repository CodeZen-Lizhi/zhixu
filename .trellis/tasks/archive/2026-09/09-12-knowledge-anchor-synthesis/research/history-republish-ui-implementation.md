# 历史主笔记恢复：HTTP / OpenAPI / Web 实施

实现范围已接线；真实 PG / 浏览器 / Approval / Git 闭环由 main 与 core owner 执行，本报告不将组件或编译检查等同于该验收。

## 修改范围

- `internal/organizing/http/synthesis_historical_republish.go`：五方法、真实 Principal caller、严格路径/query/body/header、全篇恢复与退役确认、响应白名单、冻结范围安全投影。
- `internal/organizing/http/synthesis_handler.go`：注册5条 `/api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}/historical-republish` 路由。
- `internal/organizing/http/synthesis_wire.go`：revision `historical_republish` 白名单来源身份；不公开内部Root/Authority。
- `internal/organizing/http/synthesis_historical_republish_test.go`、`internal/auth/http/handler.go` / `handler_test.go`、`internal/app/router_inventory_test.go`：真实auth中间件到HTTP，零权限/单权限/缺Principal、错误参数、不完整确认、来源身份不符、APPLIED无proposal、scope白名单，以及实际路由集合等价。
- `cmd/api/synthesis_manuscript.go`：组合已有 `HistoricalRepublish(manuscripts caller)` owner。
- `api/openapi/openapi.json` / tags manifests / normalized生成输入及 `web/src/api/generated/**`：5方法、目标/命令/审阅/范围/provenance完整生成合同。
- `web/src/api/synthesis-historical-republish.ts` / tests、`synthesis.ts` / tests：generated operation调用与严格decoder，完整UTF-8正文、selected身份、scope、旧来源provenance；不使用临时生产DTO。
- `web/src/features/synthesis/HistoricalRepublishWorkbench.tsx` / tests / `historical-republish.css`、`SynthesisNotePage.tsx`：历史阅读入口、只读两份全文差异、独立确认、原命令恢复、新候选状态与来源链接、刷新和125隔离。

不改core/domain/application/PG/authoring/SQL/atlas/schema、通用DocumentHistory、live quality和原Monaco实现；不spawn、不调用外部模型、不寻找凭据、不提交。

## 交互与命令合同

Target GET用 `selected_revision_id`。当前core owner要求所有五方法均ReadLocal+WriteProposal，并实际读取Root；本层遵循core成功Target才允许Begin，拒绝显示服务端错误，不编造can/why字段或零权限。

Begin原Target和key在POST前写URL：`restore_begin`仅冻结目标身份/hash/版本，`restore_key`、`restore_workspace`、`restore_selected`限定恢复范围。刷新只GET；Begin尚未落库/响应丢失后仍可明确重试原命令，不获取新Target替换旧参数。

Apply独立勾选整篇恢复；若requires_retirement，必须再次明确替代当前候选。没有任意全文编辑器，结果完全来自selected。原Apply key/attempt/fingerprint/confirm/retire在URL保存，未知结果刷新后只读取原attempt，明确重试使用原参数。Begin/Apply当前版本前进不丢原持久结果。

READY包含完整P→selected、F→selected对照；Monaco只读差异按需加载，失败仍可展开两侧完整原文。范围提示显示冻结的topics/audiences/description；来源当前状态不冒充AI重新核验。每次提供selected历史版本/精确原来源入口。

APPLIED不是发布：显示恢复候选，若无proposal只Resume同attempt/原reservation；有proposal跳原审批页。返回主笔记刷新note/history/list，正式发布状态来自owner当前读。恢复provenance明确“恢复旧内容，未调用AI”，原历史不变。当前候选存在historicalRepublish时整个125 Workbench隐藏，即使URL残留remerge_key；F再变走历史恢复重新预览。

workspace/note/selected切换卸载会话并取消在途请求；begin key变化只接受对应响应。重复/错误恢复参数、selected不符及不完整冻结Apply禁止提交。

## 已执行证据

- Go：`GOCACHE=/tmp/zhixu-history-ui-go-cache GIN_MODE=test go test ./internal/organizing/http ./internal/auth/http ./cmd/api -run 'TestHistoricalRepublish|TestCandidateRemerge|TestSynthesis' -count=1` PASS（cmd/api编译，筛选无测试）。
- Go vet：同3包 PASS。默认宿主Go缓存一次权限失败，换/tmp缓存后成功；未申请升级权限。
- 路由：`TestRouterRoutesExactlyMatchOpenAPI` / `TestRouterMetricsIsTheOnlyOptionalRuntimeRoute` PASS。5条实际注册新增后总数258；更新旧253库存基准并实际比较集合，非单纯改数字绕过检查。
- OpenAPI：project check、258 operation tags、Spectral无warning/error、生成客户端一致性检查 PASS。
- Web：全部手写受影响TS/TSX定向ESLint PASS；全Web typecheck及generated typecheck PASS。
- 新API+工作台当前版本：2文件6测试 PASS；包含Begin未落库/丢响应→卸载重挂→GET404→明确原命令重试，Apply未知→刷新→原命令重试→同attempt Resume，权限拒绝、范围/selected隔离与严格decoder。
- 旧相关回归分批：CandidateRemergeWorkbench/API、synthesis API及新工作台4文件39测试 PASS；SynthesisPages、ManuscriptReviewWorkbench和新API3文件45测试 PASS；后补provenance与主笔记分流后4文件56测试 PASS。批次有重叠，不相加为唯一测试数。
- scope追加后重跑新API/工作台6测试、Go HTTP/auth、vet、TS/generated types、ESLint和OpenAPI lint PASS。

## 验证边界

本代理未启动真实PG/浏览器流程；main已准备基于corefixture的overlay与Approval/Git闭环。实际业务浏览器中的Monaco加载、真实Root变更、数据库幂等和发布后的正式重读仍须使用main/core证据。

后续core已补同字节历史操作的精确回执授权，真实PG/Git与无证明拒绝验证通过，见history-republish-core-implementation.md。此UI代理原报告不包含该后续实现的独立验证。

## main首轮真实浏览器发现后的修正

main首轮真实PG/browser在完整已发布selected身份的Begin遇到400：通用decodeJSON固定MaxObjectFields=16，而Target全字段加key是17。原定向fixture省略可选发布身份，未覆盖此真实形状；首轮FAIL167.19s见 `/tmp/zhixu-history-republish-browser/test.log`，尚未创建恢复候选。

已在历史HTTP文件内新增专用 `decodeHistoricalBegin`，仅此命令允许17字段，保留16KiB/字符串1024/depth8上限、Content-Type和strictjson未知字段/重复键拒绝；通用Organizing限制不变。新增真实auth公开入口完整17字段Begin成功测试及完整请求额外scope/重复key拒绝。修后该HTTP测试和同包vet PASS。main已获共享文件通知，需要重新启动隔离fixture验证浏览器闭环；不能引用首轮失败为成功证据。

### 后续收口：复用公共decoder

按main最新要求，最终实现移除历史文件重复的Content-Type/读取/strictjson逻辑。在 `internal/organizing/http/handler.go` 提取 `decodeJSONWithObjectFields`，原 `decodeJSON` 始终传16；历史 `decodeHistoricalBegin` 单独传17，其他资源限制和错误处理共用。未全局放宽默认上限。

完整17字段public HTTP不仅断言200，还核对实际owner收到完整原Target和key；额外scope/重复key拒绝保持。相同17字段输入调用默认decoder仍拒绝，证明旧请求默认预算未变化。完整organizing/http测试与vet PASS；新增精确传参/默认上限断言后历史恢复与125回归PASS；diff check PASS。真实浏览器复验继续由main负责。
