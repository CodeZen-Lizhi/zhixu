# 历史主笔记恢复的实际浏览器验证

2026-09-15：`TestSynthesisHistoricalRepublishBrowser` PASS 311.78s，进程退出0。日志 `/tmp/zhixu-history-republish-browser-verified/test.log`；Go overlay、实际页面入口和配置均保留在同目录。临时网页文件已移出仓库，浏览器、Vite和测试数据库进程已结束。

使用真实 SynthesisNotePage、HistoricalRepublishWorkbench、MonacoDiffViewer、generated client、认证中间件、HTTP owners、PostgreSQL 和独立 Git 工作区。真实审批/SafeWriteback/Git由已有集成夹具执行，浏览器负责恢复交互及最终阅读；没有操作用户文件或发布用户提案。

1. 原正式版本3含人工全文，工作区文件另有中文及emoji批注，明确选择历史版本1。实际Target→完整Begin后展示 P→selected 与 F→selected 两份差异，当前范围为空如实显示。
2. 明确整篇恢复后，注入“新候选已提交、publication创建前”中断；页面503。完整刷新只读出同一 APPLIED attempt，新候选4已保存但提案未创建。点击继续，得到同一候选对应的唯一Proposal。
3. Go检查完整selected正文、以最新L为parent、所选来源/profile完整行（含NULL）均保持；旧版本不变，P与当前文件不提前改变，恢复过程零额外ModelRun、恰好一个APPLY。
4. 原审批/Git发布后，返回主笔记默认地址看到“正式版本4／已发布”，正文及selected来源保持；可打开 `source-61000` 实际片段 `Scheduling is workload dependent.`。再次完整刷新仍读正式4，恢复来源链可见。
5. 390×844实测无横向溢出；实际来源弹窗及正式阅读截图为 `output/playwright/history-republish-source-mobile.png`、`history-republish-current-mobile.png`。控制台只有注入的503和React开发提示，无额外业务错误或警告。

首轮失败确实暴露并修复了HTTP边界：完整Target16字段加idempotency_key共17，被通用16字段限制拒绝为400。失败记录 `/tmp/zhixu-history-republish-browser/test.log` FAIL167.19s；修复通过共享strict decoder的历史Begin专用17字段上限，保留其他请求16及未知/重复键拒绝。上述PASS是新编译、新隔离数据库的重验结果。

边界：fixture用固定模型输出生成前置版本，未验证外部真实模型质量；未通过完整AppShell登录或浏览器点击Proposal审批页面。本次浏览器编译使用128的facda411中间SQL，验证的是已有P且F≠P的恢复路径；随后新增“从未发布”分支由core实库及最终迁移证据覆盖，不冒充本浏览器已跑最新SQL全部分支。同字节独立Git发布随后已由core实际成功验证，见history-republish-core-implementation.md；本浏览器未重复该分支。
