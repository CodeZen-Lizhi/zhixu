# Web / HTTP 协调 2026-09-15

UI 已开始 business owner schema 与工作台实现，OpenAPI 由本代理独占。

需 runtime 明确并调整的公开 DTO：
1. Review.Conflicts 当前直接用 changeapp.RevisionMergeConflict，无 JSON tags 且 []byte 会编码 Base/Current/Proposed 为 base64。建议公开 Review DTO 明确 conflicts: [{ordinal,base,current,proposed}]，文本 UTF8，不直接暴露内部 merge review；Web/OpenAPI 按此公开 DTO 推进。
2. 已按 main 纠正：Decide 最后一项完成后自动 SubmitHuman；不新增 /continue 或额外确认步骤。未全完成显示下一阶段/其他 note；最后提交文案「保存裁决并继续整理」，submitted 后显示「裁决已保存，正在生成待审核候选」。本轮新增 continue schema/client 已删除。
3. GET summary/detail 与 POST note decisions 沿 manual-human-http-contract 路径；所有返回直接 DTO，不 envelope。本代理不编辑 Go，请 runtime 统一 HTTP/application/cmd-api。

Web 已读取当前 HTTP manuscriptDetailWire：小写文本 DTO 与新增 OpenAPI 一致。浏览器验收需要实际 handler 流程；本代理遵守不编辑 Go。请 runtime 提供固定 owner 的 loopback 实际 handler 测试服务或导出真实 summary/detail/decide fixtures（可用现有 integration HTTP 测试），说明端口/运行命令或 JSON 路径；Web 可用正式组件接入。不能把纯 mock 浏览器说成真实 handler。

OpenAPI 验证：Spectral/check.mjs PASS；GOCACHE=/tmp/zhixu-go-cache make openapi-check 在 internal/app/router_inventory_test.go:59 失败：OpenAPI operations=241, want238。该 Go 文件非本代理范围，请 main/runtime 更新已注册 3 路径的数量；待 resume 完成后实际应再增1。默认 Go cache 沙箱EPERM已用/tmp cache解决。

19:20复读 Go 计数已由并行方更新241，将在resume路径就绪后统一重跑。Web等待resume仅限字段包装：用户已授权 Ready&&!Submitted 的恢复入口，正常Decide仍自动继续。

浏览器阻塞证据：Playwright CLI 改为/tmp daemon和npm cache后，真实Chrome启动仍被沙箱阻止（crashpad Mach bootstrap_check_in Permission denied1100，用户Crashpad settings.dat EPERM，SIGABRT）。Computer Use Chrome扩展创建独立tab也30s超时。已准备真实HTTP采集报文回放服务127.0.0.1:5198、正式组件Vite5199/manuscript-qa.html（临时文件，交付前删除）。main若可在非沙箱启动Chrome可复用；本代理继续resume/测试，不把未运行浏览器写成PASS。

Resume已按锁定包装接入OpenAPI+generated+client+工作台，仅Ready&&!Submitted显示；unknown保存同binding，刷新无需旧Decide正文/key。正常最后Decide自动继续保持。现需Go路由inventory总数随resume从241更新242（本代理不编辑Go）。

供main浏览器接手：报文回放服务 /tmp/manuscript-browser-server.mjs 比对真实HTTP命令（除随机idem键）的binding/note/attempt/capture/stage/fingerprint/ordinals/final_content；web/manuscript-qa.html + web/src/manuscript-qa.tsx 加载正式组件。若不运行浏览器，将清理以上两个临时web文件；生产代码不依赖它们。

交付：manual-human-ui-implementation.md 已写入。代码/243定向回归/最后9组件/typecheck/lint/generated/OpenAPI242路由tag均通过。浏览器因Chrome Mach/Crashpad沙箱拒绝与CUA超时未完成，不能计PASS。5198/5199已停止、临时web文件已删除。注意runtime Resume测试覆盖了此前human_two_stage报文目录；本代理读取当前一致的summary/detail-1/decision-5/decision-6验证真实resume wire PASS，不能混用旧detail-2当两阶段回放。需main重新采集一致fixture或直接活PG补Chromium。

19:31收到恢复接口再次锁定后复核：OpenAPI/generated/client/UI已按同一契约实现，无需重复修改；限定API+工作台13项测试PASS，/tmp/manuscript-resume-recheck.log。当前human_two_stage目录再次变化：summary/detail-1同binding且stage1，但detail-2与summary不同binding，decision-1为SYNTHESIS_MANUSCRIPT_MERGE_INVALID，decision-2也不同binding。不能拿该混合目录回放完整两阶段；请backend采集时按独立run/场景目录冻结后交接。既有Chromium沙箱阻塞仍未解除。

19:38 Root两项门禁已修：unknown使用原command完整binding+note/attempt/capture（不要求stage/fingerprint/ready未推进），新增落库后网络错→摘要stage2→原key/正文重放测试；editor未ready/error和diffError阻止新提交，显式重载保留草稿，unknown恢复独立于编辑器。工作台11项、typecheck/lint通过；旧Proposal/Workflows/API回归日志 /tmp/manuscript-gates-regression.log。实现报告已补证据，不改Decide/resume契约。
