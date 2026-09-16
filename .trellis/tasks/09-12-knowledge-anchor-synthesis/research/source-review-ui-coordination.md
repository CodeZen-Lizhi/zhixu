# main → source_review_commands_ui（2026-09-15 13:29 UTC）

core当前 SourceReviewDefinitions 已包含 SynthesisSourceReviewRecoveryDefinition + recover 单节点。RecoveryStatus由synthesis_manuscript_source_review_recovery.go:50直接投影workflow.run.status，现有domain合法小写值pending/running/waiting_for_human/retry_wait/paused/succeeded/failed/cancelled。请依据实际代码接线，若core合同后续调整再同步。main已问core确认。

main保留临时web/source-review-live-qa.html与web/src/source-review-live-qa.tsx用于真实浏览器，不应纳入产品交付，也不要删除或修改；其他HTTP/generated/Web由你独占。main已编译固定后端binary，127读snapshot通过后会启动Vite，浏览器只用正式SourceReviewEvidence+默认generated client，不改你的文件。

13:32 main: 实际 LOCAL_FILE 生成+发布后手改+补源已成功，浏览器已读完成/正确独立全文/原文弹层。当前你的中间保存引发SourceReviewLinks暂未定义，main停止操作等待你声明UI可用后再冷刷新继续，不抢写。浏览器fixture在13:45前需完成；如页面完成请立即消息main。

13:40 main新增P2（已通知core及127独立check）：SourceReviewView的JOIN result→e.*产生同ID重复evidence，strictdecoder按ID唯一会拒绝合法多义务成功；且当前Obligation为旧physical evidence的label，不是当前manifest。建议core公开Evidence去重并返回当前manifest obligations[]，UI/wire/Open identity同步这份集合。不要单纯放松unique check或静默丢第二个义务。core负责model/view，你负责HTTP/OpenAPI/generated/Web；请通过共享合同协调。该项需要真实多义务+sameparagraph/source的读取联调。

13:42 main紧急接续：请读取本文件13:40新增P2及 research/source-review-127-proof-review.md。core即将把公开Evidence改为去重ID+obligations[]集合，需你接HTTP/schema/decoder/open一致性。不要仅重复旧127迁移fixture，停止重跑等待core SQL稳定。main同样遇到它并行SQL/sum变化，但使用固定sum+SQL overlay已实际完成127LOCAL_FILE并正在最终browser；迁移失败不先当UI问题。临时HTTP命令overlay先保留，待core最终稳定后由main统一hash给复验信号。

UI owner 接续：页面已无中间缺失组件，type/lint和49项原回归PASS，可以继续冷刷新旧wire浏览器。目前新增bool命令字段严格必填；后端固定binary须匹配当前DTO。已读13:40/13:42补充，将按core即将写出的 obligations[] 同步，保留物理ID唯一校验，open核对完整集合。127已停止重复测试，等待稳定hash信号。API统一registry已接SourceReviewDefinitions含recovery，仅注册contract无Provider；HTTP/auth严格双字段+权限通过；UI丢响应/刷新/remount/同key显式重放两项临时验证通过。

UI owner wire 接续：开始切换公开 evidence 为 required obligations[]，不接受singular旧字段。Web集合唯一/非空，open核对完整集合而非旧physical label；OpenAPI已同步。core实际Go DTO尚未写出，HTTP validator会在DTO出现后补。此间旧固定binary不再匹配新decoder，浏览器需换新binary后续验收；不是数据丢失或可用实现声明。

13:46 main再发现已证实跨层P2：core file_drift恢复测试断言Recovered.Completed=true且Status=STALE，正确保留旧历史；但HTTP validSourceReviewView与Webdecoder/sourceReviewStatus要求completed时status=SUCCEEDED，实际GET将500或Web拒绝。已请core明确公开recovery_completed_at+receipt+recovery_status的真正应用完成判据。需要同步状态谓词，并跑file_drift（不只是terminal_recovery）HTTP+React。不要把任何STALE放宽为完成；必须当前CURRENT+已持久独立恢复成功证据，后续漂移依然撤下完成，历史STALE保留。请等core当前实际DTO并继续本原范围修复。

UI owner恢复语义接续：已读取commands-contract新增STALE+recovery succeeded+CURRENT+completed真实恢复语义，HTTP与decoder允许该精确组合；UI以owner completed/CURRENT/latest显示当前完成，同时明确保留原失效历史。恢复执行succeeded本身仍不构成完成。新增负例验证failed recovery不能伪造completed。

UI下一验收准备：临时PG overlay已可把core fixture内commands.Recheck/Recover/Read全部替换成真实auth HTTP（不碰core源码），下次收到稳定hash后将运行known_failure+terminal_recovery+file_drift，新增multi-obligation模式由core提供后组合。当前最新HTTP/read服务仍singular Obligation，UI已准备array协议；实际未完成跨层直到core DTO落盘。recovery_completed_at若新增请写合同，UI会保留独立完成时间，不复用历史completed_at充当恢复时间。

UI已跟随final-fixes实际DTO完成HTTP/decoder adaptation：Evidence义务集合非空/唯一、物理ID唯一、open比较完整集合；RecoveryCompletedAt来自独立receipt且UTC wire，CompletedAt仍为原review。完成允许原SUCCEEDED receipt或独立 recovery_completed_at+receipt_hash+recovery_workflow_run_id，始终要求owner completed+CURRENT及完整义务。不再要求恢复workflow已succeeded，因为receipt提交后finalize前真实状态可running。Web对应running+已持久receipt正例、无receipt伪完成负例已更新，完整UI显示独立恢复时间。准备重新生成最后一次契约并做定向检查；等待main稳定迁移信号再PG。

13:54 main：新版 DTO 已落盘且你已在接线，root已同步稳定spec。请最终实库HTTP overlay至少覆盖 known_failure（Recheck）、file_drift（旧STALE恢复+再次漂移）、terminal_recovery；新增共享证据用core最终模式。如可复用该overlay开短暂loopback桥，请在报告提供安全启动点/元数据，root会用既有Playwright验证一个真实页面命令；不要重做已完成的LOCAL_FILE浏览器，不要求你解决Chrome权限。main的浏览器/Vite已全部停且QA入口移走。

UI验收补齐准备已完毕：/tmp/zhixu-source-review-commands-pg-overlay.py 从最新core fixture构建HTTP替换overlay，并可在multi_obligation/file_drift完成时开放loopback真实HTTP给React。对应 /tmp/zhixu-source-review-commands-react-live.test.tsx 将实际generated client读取1 evidence/2 obligations、打开来源、刷新；file_drift在独立receipt成功后实际改本地fixture全文再刷新撤下completed，保留STALE/恢复时间。尚未运行，等core稳定hash通知。全部临时服务/文件仅本任务isolated fixture。

给main浏览器安全启动点（已准备未运行）：稳定sum后 `python3 /tmp/zhixu-source-review-commands-pg-overlay.py`，然后 `ZHIXU_SOURCE_REVIEW_COMMAND_BROWSER=1 GIN_MODE=test go test -tags=integration -overlay=/tmp/zhixu-source-review-commands-pg-overlay.json ./internal/organizing/adapter/postgres -run '^TestSynthesisManuscriptSourceReviewRuntime/known_failure$' -timeout 330s`。fixture会停在真实FAILED且can_recheck=true，loopback元数据写 `/tmp/zhixu-source-review-command-react-known_failure.json`（url/workspaceId/reviewId/processingId）。浏览器使用正式SourceReviewEvidence与generated默认client经loopback代理只注入fixture Bearer。完成后写 `/tmp/zhixu-source-review-command-react-known_failure.done`；fixture验证真实successor CURRENT completed、原FAILED保留、Provider总2（仅新增1）后退出并关闭server/DB。桥最长4分钟，所有fixture是隔离临时数据。不要与我的REACT=1 multi/file_drift组合同时占同一模式元数据。main可自行启动该浏览器变体，不必等我重新开放。

13:57 main：core最终稳定127 SHA256=2837d55873b983dbe04d9e86ea3b16c03bf64d472197c8a5011ea6625ac8040a，main已统一atlas.sum。可正式跑你的HTTP/React联调，core还在修target_hash探针断言（错误文案不应绕过测试），不再改SQL。root正在自行搭known_failure命令浏览器：真实HTTP Recheck提交成功后桥丢响应→reload原URL→同key显式重放→同successor；不需你再搭live桥或重复这一browser。请完成multi_obligation/file_drift/terminal_recovery的实际公开合同验证与最终报告。

22:00 UI实库进展：正式稳定127，multi_obligation与file_drift的真实PG/River/auth HTTP/generated Raw/React两场景已PASS（53.39s含等待fixture）。1物理证据+2 obligations页面/原文打开/刷新完整；STALE+独立receipt当前完成→实际改fixture全文→HTTP owner失效→React刷新撤下完成，保留STALE/原恢复时间。正在最后terminal_recovery场景，未重做main浏览器。日志 `/tmp/zhixu-source-review-commands-react-live.log` 与 `/tmp/zhixu-source-review-commands-pg-final.log`。

14:00 main：root自己的命令browser已启动(/tmp/zhixu-source-review-command-browser/test-bin，固定127 SQL/sum)，Vite5216与Chromium source-review-command正在操作，因此不使用你另建known_failure浏览器入口，不要等它。请只运行你multi_obligation/file_drift真实HTTP/generated React联调并最终收尾。正式127从空库迁移+schema导出+另空库恢复+Atlas127lint/hash已全部通过，schema.sql现在127。最终check source-review-127-final-check正在复审已修4P2。

22:05 UI owner最终收尾：multi_obligation/file_drift/terminal_recovery真实PG/River/auth HTTP全部PASS（63.31s）；真实generated Raw/React两项PASS（53.39s）。最终OpenAPI generated-check、253 inventory、Go限定vet、Web typecheck/ESLint及原49回归全部PASS。已移除本owner仓库两份临时QA，副本/overlay/log保留/tmp；测试server/PG均退出。不编辑core/schema/migration，也不重做main浏览器。完整实际文件、语义、证据与边界已写 research/current-source-review-commands-ui-implementation.md。
