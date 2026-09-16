# 当前全文补源浏览器验证

2026-09-15，main。正式只读链路及 LOCAL_FILE 浏览器流程已通过；127 新命令及后续多义务修复另行验收。

## 实际链路

复用正式 PG/River source-review fixture，通过临时 Go overlay 给原生成的 v2 候选实际审批、执行 Safe Writeback/Git，再在磁盘添加独立人工段落（含中文与 emoji）。原 L/P 由真实发布 owner 更新后读取。随后实际 source-review dispatcher/model ledger/apply 冻结 LOCAL_FILE；固定 Provider 只用于验证管线，不证明内容判断正确。

Loopback httptest 运行正式 SynthesisHandler.Routes、auth Middleware、真实 read owner 与 SourceReader。认证 provider 是隔离的固定测试凭证，不记为真实认证仓库验收；Root 拒绝另有实际核心证据。临时 Vite 页面只组合真实 SourceReviewEvidence、默认 generated Raw client、QueryClient 与 BrowserRouter；没有固定业务 HTTP 响应。

## 已观察

- 后端返回 SUCCEEDED / CURRENT / completed=true，target_kind=LOCAL_FILE；实际磁盘人工段落进入独立全文，页面“查看基线版本”链接没有把该快照当作已发布正文。
- 点击完整正文和精确来源，显示完整中文/emoji、正确证据来源版本与原文。
- 经隔离 DB 将真实来源 removed_at 置位，刷新返回原 SUCCEEDED、SYNTHESIS_SOURCE_REVIEW_STALE_SOURCE、completed=false、can_recheck=false。页面撤下“补充完成”，来源弹层显示“已保存的历史原文”，保持历史证据。
- 390×844 实际 Chromium 页面：document.scrollWidth=375≤390；快照摘要与原文弹层无横向溢出，截图已人工查看。
- Go hook 内最终不变量通过：原调用4次+独立复核1次、1证据，补源过程中正文/Note/Article/P均未改变，3次dispatcher无新增调用，后续F变化使旧成功快照失效。

截图：output/playwright/source-review-local-file-mobile-summary.png、source-review-historical-mobile.png。初始/失效API原始响应：/tmp/zhixu-source-review-browser-0915/initial-api.json 与 stale-api.json。实际浏览器snapshot见.playwright-cli/page-2026-09-15T13-33-32-066Z.yml及13-34-25-732Z.yml。

## 尚未通过的外层结果

/tmp/zhixu-source-review-browser-0915/test-attempt2.log：hook和浏览器完成，但外层旧用例仍要求磁盘等于“尚未发布候选前”的manual正文，因本次夹具主动执行实际发布而失败。已仅在临时overlay中保存发布后人工全文的预期字节，并让外层继续精确比较该预期；没有移除文件断言、关闭SQL约束或改生产代码。

下一次 /tmp/zhixu-source-review-browser-verified/test.log 因并行127 SQL与atlas.sum中间状态不一致，在迁移阶段失败，未重复浏览器。等待127稳定后统一hash和重新编译。正式schema目前仍126。

前次开发过程中另观察到并行保存UI导致短暂 SourceReviewLinks 未定义以及临时QA入口的HMR duplicate createRoot；最终冷刷新后读取正常。后续临时Vite验证配置关闭HMR，避免把编辑中间态计入产品浏览器结论。不声称该次console全程零错误。

本页只验证补源读取/失效/原文，不包括127 recheck/recover命令，不包括真实外部模型语义质量或完整AppShell导航。

## 最终复验通过（13:45 UTC）

/tmp/zhixu-source-review-browser-verified/test.log：TestSynthesisManuscriptSourceReviewRuntime/supported PASS 314.56s，退出0。临时overlay保存明确发布后人工全文，外层逐字节断言仍执行；固定127 SQL+sum字节在同目录，migration127-sha256.txt记录该次基线，不把后续127改动自动计为已验证。真实Go业务链和全部读接口断言通过。

新Chromium会话 source-review-final 冷启动，默认generated client读取LOCAL_FILE、打开完整全文并比对API full_content完全相同（含数据库复习 🧭）；精确当前来源打开；标记来源失效、刷新撤下完成、整页reload保持失效、手机尺寸历史原文再次打开。最终 document.scrollWidth=390、viewport=390。截图 output/playwright/source-review-historical-mobile-final.png；snapshot .playwright-cli/page-2026-09-15T13-43-34-754Z.yml。该会话console仅React DevTools两条INFO，无error/warning。临时Vite关闭HMR只为冻结验证期间页面，不改产品配置。

浏览器显式点击验证结束后，测试实际完成并退出；浏览器会话、Vite与隔离测试资源已结束。3个临时Web入口/config移至上述/tmp目录，未保留在产品源码。前两次失败是保留的诊断历史，最终PASS不删除或伪装那些失败。
