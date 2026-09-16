# 重新合并候选：真实 HTTP/浏览器/审批写回验收

2026-09-15 main。正式125迁移、实际PG/River/固定Recording Provider生成原候选，实际Root/auth/HTTP owner；验证使用临时Web入口挂生产CandidateRemergeWorkbench与NoteContent，并非完整登录AppShell。

最终 TestSynthesisCandidateRemergeBrowser PASS 407.38s（包410.802s），日志 `/tmp/zhixu-candidate-remerge-browser-verified2/test.log`。实际浏览器：Target→Begin冲突→刷新原URL恢复预览→Monaco输入完整3067字节与确认冲突→Apply提交R2后、真正创建P2前注入一次503→刷新丢弃内存命令→GET仍显示“继续创建更新提案”→Resume创建同一P2→读取新候选→隔离真实Approval/Git写回→刷新读取当前发布R2。

数据库断言：R2正文精确等于 F1 + 原R1完整正文 + 人工批注；R1历史JSON原样保留；发布前F1和P不变；模型run数量不变且只一条APPLY事件；最终Authoring/Synthesis/P/ProposalCommit/Git正文闭合。精确观察正文保存在上述目录observed-content.md。无额外模型调用。

本次实际发现并修复：

- 原临时浏览器夹具遗漏Processing依赖，HTTP读笔记返回503；现构造真实Processing Store/Service，bridge仅暴露该note路由，不开放processing retry，执行定义保持不可用，不伪造重试能力。
- Monaco默认自动缩进使输入3067字节变成3169字节。共享Editor新增preserveWhitespace，只有两个人工合并入口启用autoIndent=none且关闭格式化；实跑保存后的正文逐字一致。
- 已APPLIED或未编辑READY仍挂beforeunload提示；现仅对尚未保存的实际编辑/未知Apply保留提醒。发布后刷新无多余对话框。
- 重新合并结果卡此前固定显示“尚未发布”，在该版本已经发布后仍出现；现只陈述本owner证明的“重新合并结果已保存”，正式发布状态继续读取主笔记owner。

26个已有Web定向测试、3文件ESLint、Web typecheck通过；最后两处文字/离页条件轻量复验另见收口日志。前一次失败已定位为编辑器自动缩进，最终从新PG/Git夹具完整重跑通过。

最终发布读取快照 `.playwright-cli/page-2026-09-15T13-10-09-851Z.yml`（刷新后的动态文本另由13:10 snapshot证实）。console包含预期Apply故障503、临时QA入口在并发generated文件更新时的HMR重复createRoot提示，以及验证结束/服务清理后的502；未声称本次console全零。未复现共享Diff的disposed/no diff result异常，但本次重合并使用TextEditor，并不替代已有Diff专项证据。

临时HTML/TSX已移至 `/tmp/zhixu-candidate-remerge-browser-verified2` 留存并从工作树移除；Vite5214、浏览器会话和Testcontainers已停止/清理。没有操作用户业务库/真实知识文件、没有提交/push/部署。

收口：26项Web定向复验PASS2.10s；限定ESLint、Go integration vet及diff whitespace均通过。日志/tmp/zhixu-remerge-editor-final-{tests,lint}.log和/tmp/zhixu-remerge-final-vet.log。
