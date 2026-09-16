# 人工全文裁决 Web / OpenAPI 实施（2026-09-15）

本报告保留先前实施记录；最新状态以文末「Monaco P2 本轮修复」为准。当前唯一任务为 Monaco 加载门禁与精确重放回归。真实 PG 浏览器验收由 main 接手，本轮未启动浏览器、采集报文或操作临时 QA 入口。

## 实现

- `web/src/api/synthesis-manuscript.ts`：生成 SynthesisApi Raw transport + strict decoder。summary最多8个真实唯一note身份；detail必须绑定所选summary的完整binding/note/attempt/capture/stage/fingerprint；小写UTF8 conflicts映射，连续ordinal。正文保留合法BOM，拒绝NUL、孤立surrogate、超过1MiB。客户端不附加权限布尔值，不输出或请求Prepared。
- `web/src/features/synthesis/ManuscriptReviewWorkbench.tsx`、`manuscript-review.css`：按note选择后读取单份全文；复用正式MonacoTextEditor/MonacoDiffViewer及Tabs模式，Base/Current、Base/Proposed、逐冲突确认和完整结果编辑。每阶段重新确认。草稿按note保留；失败保留编辑；网络/5xx/不可信成功报文作为结果未知冻结完整命令及原key，精确重试。
- binding/阶段变化保留旧编辑、禁止旧提交；异步detail有取消和active防护，workspace/processing重挂隔离；同processing新task及更新阶段出现时，旧POST响应不能覆盖新summary。stale仅链接真实工作流控制，按已证明的cancel→terminal FAILED/retryable→原ProcessingRecord retry路径恢复，不造reprepare按钮。
- 正常最后Decide依原服务自动SubmitHuman，显示「裁决已保存，正在生成待审核候选」。**没有 `/continue` 或额外必需确认**。
- 仅 `Ready && !Submitted` 的receipt保存/SubmitHuman中断恢复显示「继续生成候选 / 恢复整理」。`POST .../resume`严格发送 `{binding}`，无正文、resolution、hash或新key；未知响应保留同binding重放。刷新仅需重新读summary即可恢复。
- `ProcessingRecord.tsx`：running/pending记录按真实summary发现待裁决。只有专用404 `SYNTHESIS_MANUSCRIPT_REVIEW_NOT_PENDING`算无待审，其他错误可见；不预载全文。主笔记中心 `SynthesisNotesPage.tsx` 通过实际 `/authoring/notes?processing=UUID` 定位工作台；重复/非法参数拒绝，工作台懒加载，普通阅读/Interview未重写。`queries.ts` 增加workspace/processing绑定的summary key。
- `business.ts`、`WorkflowsPage.tsx`：显式识别owner schema，version/processing/run单值enum、1..8 sorted unique UUID noteIds、精确result_hash pattern与exact keys。旧approval/target_path两类行为保持；owner仅提供受控中心链接，普通submit函数明确拒绝owner任务。
- `api/openapi/openapi.json`、`operation-tags.json`、`tag-manifest.mjs`：3个原定review操作+1个恢复resume，受控schema与实际HTTP DTO一致；生成目录由锁定generator重新生成，未手改generated输出。

## 验证命令与日志

- `npm run test --prefix web -- src/api/synthesis-manuscript.test.ts src/api/business.test.ts src/features/synthesis/ManuscriptReviewWorkbench.test.tsx src/features/synthesis/SynthesisPages.test.tsx src/features/synthesis/AnchorFusionPanel.test.tsx src/features/business/ProposalRevisionWorkbench.test.tsx src/features/business/WorkflowsPage.test.ts src/features/business/WorkflowsPage.component.test.tsx`：8文件243项PASS，`/tmp/manuscript-regression.log`。
- 最后增加同任务阶段snapshot守卫后，`npm run test --prefix web -- src/features/synthesis/ManuscriptReviewWorkbench.test.tsx`：9项PASS，`/tmp/manuscript-component-final.log`。覆盖两阶段+两note、unknown精确重放、stale草稿、工作区切换迟到detail、note切换保留草稿、新task迟到POST、workflow owner无普通approve、receipt刷新resume+503同binding重放、非等待空状态。
- `npm run typecheck --prefix web`、`npm run lint --prefix web`、`npm run typecheck:generated --prefix web` PASS；日志分别 `/tmp/manuscript-typecheck.log`、`/tmp/manuscript-lint.log`、`/tmp/manuscript-generated-typecheck.log`。
- `GOCACHE=/tmp/zhixu-go-cache make openapi-check` PASS：Spectral、check.mjs、实际method/path inventory、242 operation tags；`/tmp/manuscript-openapi-check.log`。默认用户Go cache曾EPERM，改用已存在可写/tmp cache。旧Go inventory计数由runtime改为242，本代理只同步OpenAPI tag集合。
- `npm run generate --prefix api/openapi` / `npm run generate:check --prefix api/openapi` PASS，29项既有warning baseline一致；`/tmp/manuscript-openapi-generate.log`、`/tmp/manuscript-generated-check.log`。
- 定向 `git diff --check` PASS。自检修复：普通主笔记中心提前加载Monaco、旧task迟到POST覆盖新summary、tag-manifest固定集合遗漏新操作。未将编译或mock组件结果当成功能全链路。

## 真实 HTTP 与浏览器边界

runtime提供 `/tmp/zhixu-manuscript-http-fixtures/human_two_stage/`，来自其真实PG/River/TokenMiddleware/HTTP测试（原采集PASS25.360s见 `/tmp/manuscript-human-http-captures.log`）。后续Resume测试复用了同目录，summary/detail-1/decision-1等文件已被恢复场景覆盖，不能混合旧detail-2称为完整两阶段捕获。

本代理用临时Vitest直接读取**当前一致的**summary、detail-1、decision-5、decision-6，经正式strict decoder验证：Ready=true/Submitted=false、detail无Review，以及Resume成功/精确重放的Ready=true/Submitted=true和同binding。1项PASS，`/tmp/manuscript-real-wire.log`。临时测试已删除；此项证明真实公开报文与Web decoder兼容，不是浏览器驱动活PG。

真实Chromium未成功启动：
- 首次npx用户npm cache EPERM；切换 `/tmp/aether-playwright-npm-cache` 和 `PWTEST_DAEMON_SESSION_DIR=/tmp/manuscript-playwright-daemon` 后，Chrome仍在启动时SIGABRT，crashpad `bootstrap_check_in ... Permission denied (1100)`，用户Crashpad settings.dat EPERM。
- 使用Computer Use Chrome扩展作为替代，创建独立标签页30s超时；没有可验证的页面交互结果。
- 临时报文回放服务5198和正式组件Vite5199已通过session Ctrl-C停止，lsof确认无监听；临时web HTML/TSX已删除，备份在 `/tmp/manuscript-browser-fixture/`，回放脚本 `/tmp/manuscript-browser-server.mjs`。**该脚本读取的两阶段目录已被Resume捕获覆盖，重跑前必须重新采集一套一致的两阶段报文，不可直接当有效场景使用。**

剩余：main需在允许启动Chromium的环境补正式组件真实交互（编辑、两阶段重新确认、390px、焦点/console、刷新及恢复）；接活PG的完整流程与原审批/Git发布也不由本Web报文测试证明。没有真实模型质量结论。

## 协调

`trellis channel send`因沙箱不能写 `~/.trellis/channels/...lock`失败，故使用 `manual-human-ui-coordination.md`协调。最初误提额外continue已按main纠正并全部移除；新增resume仅遵循之后明确授权的中断恢复分支。真实权限仍只来自服务端principal及graph校验。

### 19:31 契约重确认

收到main再次锁定resume后核对现有OpenAPI/client/UI，无需重复修改；API+工作台两文件13项PASS（`/tmp/manuscript-resume-recheck.log`）。当前human_two_stage目录已再次被并行测试覆盖：summary/detail-1绑定一致且为stage1，detail-2/decision-2绑定与summary不同，decision-1为MERGE_INVALID错误。此前真实resume报文验证的历史PASS有效，但当前目录不能复现该快照，也不能直接回放完整两阶段。需要独立run/场景目录重新冻结采集；未伪造或拼接binding。

### Root 门禁复核修复（19:38）

- unknown恢复改以已冻结 `draft.command` 的完整binding及note/attempt/capture核对当前摘要，明确要求原命令存在。stage/fingerprint/ready推进不阻止同owner的精确重放，不据摘要推进自行宣布原命令成功；新裁决仍要求完整预览匹配。
- Draft新增editorStatus/loading-ready-error、diffError及显式重载epoch。编辑器未ready/失败、差异失败均阻止新裁决，按钮和save函数双重门禁；重新加载编辑器与差异保留正文及确认。unknown原命令重放独立于编辑器和差异状态。
- 新组件测试实际走生成transport：POST模拟落库后网络失败→refetch真实summary到stage2→旧正文保持→编辑器故障→原key/完整命令精确重放→加载stage2，必须重新确认。另测试未ready、编辑器失败、diff失败及显式重载后恢复可提交；失败期间无POST。
- 工作台11项PASS：`/tmp/manuscript-gates-test.log`；typecheck/lint PASS：`/tmp/manuscript-gates-typecheck.log`、`/tmp/manuscript-gates-lint.log`。旧ProposalRevisionWorkbench/Workflows及manuscript API限定回归见 `/tmp/manuscript-gates-regression.log`。定向diff检查通过。未修改HTTP/OpenAPI、正常Decide自动提交或binding-only resume。


### Monaco P2 本轮修复（2026-09-15 19:43）

- 新裁决同时受 HTTP/绑定状态、编辑器 ready/error 和已加载 diff 的 error 门禁约束；不要求打开两个 diff Tab。失败提供「重新加载编辑器与差异」，保留全文和冲突确认。
- Monaco 回调核对所属 detail 对象、完整 task binding 与加载 epoch。旧阶段/旧 draft/旧加载轮次的迟到 ready/error 不得改变新编辑器状态；同轮次 error 后迟到 ready 也不能绕过显式重试。切回已访问笔记时重挂编辑器并重新等待 ready。
- editorError 与 HTTP readError、提交 message 分离。Monaco ready 不清 HTTP 错误，重载也不清 unknown 的结果提示或已冻结 command。unknown 精确恢复不依赖 Monaco ready/error/diff 状态。
- canReplay 定向回归通过：POST 模拟持久成功后网络 throw → 同 task/attempt/capture 摘要推进下一 stage → 使用原 idempotency key 和完整 body 重放 → 加载下一阶段且重新确认；不同 task 拒绝重放。
- `npm run test --prefix web -- src/features/synthesis/ManuscriptReviewWorkbench.test.tsx`：14/14 PASS，日志 `/tmp/manuscript-p2-test.log`。新增真实组件状态序列覆盖迟到回调、重载、HTTP 错误隔离、不同 task；unknown 重载未 ready 仍可精确重放。
- `npm run typecheck --prefix web`：PASS，`/tmp/manuscript-p2-typecheck.log`。
- web 目录执行 `./node_modules/.bin/eslint src/features/synthesis/ManuscriptReviewWorkbench.tsx src/features/synthesis/ManuscriptReviewWorkbench.test.tsx`：PASS，`/tmp/manuscript-p2-lint.log`。仅最小定向 lint，不引用旧全量结果。
- 本轮仅修改工作台 TSX、对应组件测试和本报告；CSS 无需修改。未改 Go/OpenAPI/SQL/其他生产文件，未提交/push；未改删 `web/manuscript-live-qa.html` 或 `web/src/manuscript-live-qa.tsx`。
- 组件测试中的 Monaco 使用受控 ready/error 回调，证明正式工作台状态与请求行为，不证明真实 Monaco worker 加载和活 PG 浏览器流程；该部分由 main 的正式组件浏览器验收覆盖。

## root 复验补记（2026-09-15）

main 在隔离临时 PG/River/Auth/HTTP bridge 上启动正式 Vite 组件，并通过真实 Playwright Chromium 完成两阶段人工流程：第一阶段编辑完整正文并确认冲突，第二阶段再次编辑并确认全部冲突；进入候选页检查当前 revision 与 published revision 分离，之后执行隔离夹具中的既有审批/Git 写回，再重新读取正式主笔记，正文与版本标识一致。测试过程没有把浏览器凭据暴露给页面；loopback bridge 只在服务端转发真实认证 handler。最终 Go 集成测试在 `/tmp/zhixu-manuscript-live-verified/test.log` 以 379.53s PASS，日志包含 `live browser two-stage review, actual candidate, approval/Git and refreshed reading passed`。页面窗口宽度和 console 结果均在本次检查中观察；Monaco 在阶段切换时曾记录一次 `AbstractContextKeyService ... disposed` / `no diff result available`，但未阻断编辑、裁决、候选、发布或重读，已单独交给 UI loading-fix 代理核对，不把 console 零错误作为已证明条件。

此前“浏览器被沙箱阻断”的状态仅适用于 UI 代理早期的独立尝试；本补记覆盖了当前正式组件的隔离端到端证据。临时入口文件已清理，未进入产品交付。
