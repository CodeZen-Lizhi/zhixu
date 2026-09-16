# 当前全文重核命令的浏览器恢复验收

2026-09-15 22:08，main 实际 Chromium 验证通过。使用最终 127 SQL（SHA-256 `2837d55873b983dbe04d9e86ea3b16c03bf64d472197c8a5011ea6625ac8040a`）、隔离 PostgreSQL/River、真实 auth middleware/CommandService/read owner，以及正式 SourceReviewEvidence 和默认 generated client。

## 实际流程与结果

1. 首次独立核验发生已知失败，页面读取真实 FAILED 历史和服务端 can_recheck。
2. 点击“重新核验当前全文”。真实 POST 已提交命令并启动后继核验后，测试桥将成功响应替换为一次 503；不是调用业务前拒绝请求。
3. 页面保留原 expected_version=5、幂等键、review/processing/workspace/action 到 URL，显示结果尚未确认；原失败历史不被覆盖。
4. 完整 reload 后仍保留同一命令，只读恢复；列表已能读到第二次核验成功，但原请求结果尚未确认。
5. 点击“重试原请求”，使用同一 key/version 找回后继 `a225f6c5-e30d-4e88-bb71-ff3fcb9f57d3`；URL 保存 sr_result，进度显示当前正文补充来源已完成，同时保留第 1 次失败历史。
6. 实际打开来源，显示精确来源版本和当时原文。关闭弹层后结束浏览器阶段；Go 测试从数据库读回实际浏览器命令，再经真实 HTTP 并发重放两次，断言返回同一个后继，最终完成。

最终 `TestSynthesisManuscriptSourceReviewRuntime/known_failure` PASS 529.93s，进程退出 0；日志 `/tmp/zhixu-source-review-command-browser/test.log`。原生成/语义调用仍为 4 次；独立来源核验共 2 次（首次已知失败 + 显式重核），刷新、浏览器重试和随后并发重放没有追加调用。原历史仍 FAILED，正文、Note/Article 版本和发布指针保持不变；日志末尾 FAILED 指原记录，不表示后继失败。

## 证据与边界

- 丢响应状态：`.playwright-cli/page-2026-09-15T14-00-35-148Z.yml`。
- 完整刷新：`.playwright-cli/page-2026-09-15T14-01-15-439Z.yml`。
- 同 key 恢复与独立历史：`.playwright-cli/page-2026-09-15T14-05-00-129Z.yml`。
- 精确来源弹层：`.playwright-cli/page-2026-09-15T14-06-46-846Z.yml`。
- console 仅记录注入的那一次 HTTP 503 错误及 React 开发 INFO，没有其他观察到的脚本错误；不宣称零 console error。
- Go overlay、编译产物及临时 QA 入口保留于 `/tmp/zhixu-source-review-command-browser/`；启动生成器 `/tmp/zhixu-build-source-review-command-browser.py`。只处理合成资料与隔离仓库，auth provider 是明确测试实现，没有使用用户凭据。
- 这是实际组件浏览器流程，不是完整登录 AppShell。Provider 是固定实现，不能证明真实外部模型对当前全文的语义判断质量。多义务/STALE 恢复再漂移的真实 HTTP/generated React 证据另见 `current-source-review-commands-ui-implementation.md`。

验收后已关闭专用 Chromium、停止 Vite 和测试进程；三个临时 Web 入口移出仓库，未保留到产品路由。
