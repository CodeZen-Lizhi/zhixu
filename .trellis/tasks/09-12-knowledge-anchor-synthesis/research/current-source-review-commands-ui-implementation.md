# 127 当前全文来源核验 HTTP 与操作 UI

2026-09-15，HTTP/命令 UI owner。实现、定向检查与最终127稳定迁移上的真实HTTP/PG/generated Raw/React组合均已通过。未修改核心 manuscript_source_review store/worker/migration、atlas.sum/schema、Monaco、候选remerge或main浏览器临时文件。

## 实际改动

- `internal/organizing/http/synthesis_source_review_commands.go`：两POST `/source-reviews/{review_id}/recheck` 与 `/recover`，只接 expected_version / idempotency_key。真实Principal经SynthesisManuscriptCaller要求READ_LOCAL+WRITE_PROPOSAL；路径注入workspace/review身份，不接受proof/source/scope/fence/模型/正文。拒绝重复键、null、额外字段、query、异参header。调用核心CommandService；返回安全View且核对同review或直接successor，响应时间统一UTC。
- `internal/organizing/http/synthesis_source_review_read.go`：新增fields与恢复receipt校验；Evidence ID唯一、当前manifest obligations[]完整非空且唯一；open核对集合。READ_LOCAL-only响应can_*强制false，HTTP命令再次真实授权。原SUCCEEDED或独立receipt可证明当前完成；后者要求recovery_completed_at、receipt_hash和真实recovery_workflow_run_id，仍须owner completed/CURRENT/全义务闭合，不能要求workflow已finalize succeeded。原completed_at与新recovery_completed_at分别保留。
- `synthesis_handler.go`、`internal/auth/http/handler.go`：正式路由与两能力inventory；依赖缺失仍保留路由并返回不可用。
- `cmd/api/synthesis_manuscript.go` / `synthesis_components.go`：使用现有Runtime starter与同一冻结DefinitionRegistry配置核心store；注册SourceReviewDefinitions的原始与recovery节点contract。API不构造Provider、不安装模型executor。`synthesis_components_test.go`验证chat开/关均具备这两定义且无模型executor。
- `api/openapi/openapi.json`、operation-tags、tag-manifest、generated客户端/manifest以及`internal/app/router_inventory_test.go`同步253 operation。Evidence公开协议是obligations[]，不再接受singular obligation。
- `web/src/api/synthesis-source-review.ts`：generated Raw两命令与严格响应映射；保留latest/supersedes/version/can_*/recovery状态和独立时间；命令响应绑定workspace/origin processing/原review或直接successor；来源打开核对同一物理证据及完整义务集合。
- `SourceReviewEvidence.tsx` / `source-review-evidence.css`：只显示服务端can_*操作；明确重新核验重新分析当前全文、恢复复用已保存结果。提交前保存原key/version/action/workspace/parent/review到当前URL；未知结果、刷新、完整页面remount均先GET，显式重试同一请求保留原参数。不会在query/refetch/轮询中POST或重跑模型。返回successor按新ID独立读取，旧列表记录保留；任务succeeded本身不显示完成。共享证据只显示一次并保留支持关系数。
- `SynthesisNotePage.tsx` / `SynthesisNotesPage.tsx`：独立source_review_workspace防止工作区漂移，避免workspace_id误触已有来源深链；精确note/revision与origin processing入口。processing深链使用已有getSynthesisProcessing owner加载真实原处理记录，分页列表不重复展示该记录。

## 已验证

- `make openapi-generate`与最终`make openapi-generate-check`均退出0：253 operation、路由集合、tag和generated typecheck一致。日志`/tmp/zhixu-source-review-commands-generated-check.log`。
- 真实auth Middleware→HTTP→CommandService临时overlay验证双能力/零Principal/严格字段/重复JSON键/null/额外proof/source/scope/fence/错误响应身份拒绝、同key/version原样传入；READ_LOCAL-only安全GET不显示写操作。最新HTTP 0.528s；API composition 0.696s。日志`/tmp/zhixu-source-review-commands-http.log`。
- 三项临时generated Raw/React检查通过（171ms）：丢响应后URL冻结→刷新/完整remount只GET→显式同key重放→旧SUCCEEDED历史与successor分开展示；恢复命令strict双字段；STALE原历史+running恢复workflow+真实receipt判据可显示当前完成；缺receipt伪完成拒绝；共享1证据2义务和open集合漂移拒绝。日志`/tmp/zhixu-source-review-commands-web.log`。此组使用模拟HTTP故障，不记作PG或浏览器联调。
- 最终原Synthesis页面/API 49项定向回归通过（2.00s）；web typecheck、相关ESLint通过；Go HTTP/auth/cmd-api限定vet退出0（`/tmp/zhixu-source-review-commands-vet.log`）；定向diff whitespace通过。

- 最终真实PG/River/auth HTTP组合：`TestSynthesisManuscriptSourceReviewRuntime/{multi_obligation,file_drift,terminal_recovery}`全部通过，整体63.31s，三个场景分别26.87s/18.73s/17.72s。测试overlay把core fixture的Recheck/Recover/Read替换为真实auth HTTP，无伪service。日志`/tmp/zhixu-source-review-commands-pg-final.log`。稳定127 SQL SHA256为`2837d55873b983dbe04d9e86ea3b16c03bf64d472197c8a5011ea6625ac8040a`。
- 真实generated Raw/React联调两项通过（53.39s含等待fixture）：multi_obligation验证单一物理证据保留两项当前义务、原文打开与刷新；file_drift验证原STALE+独立receipt完成后，实际修改隔离fixture本地文件，owner GET重新判定失效，React刷新撤下completed，同时保留STALE及独立恢复时间。日志`/tmp/zhixu-source-review-commands-react-live.log`。独立恢复复用原proof且不新增Provider调用；同key重放保持receipt。此处React使用真实HTTP与测试DOM，实际浏览器由main单独负责。

## 可复用验证入口与边界

- 临时Go overlay入口`/tmp/zhixu-source-review-commands-pg-overlay.py`与`/tmp/zhixu-source-review-commands-pg-overlay.json`保留，未把临时测试写入核心源码。实际HTTP权限/strict请求overlay保留于`/tmp/zhixu-source-review-commands-http-overlay.json`。
- 临时Web测试完整副本保留`/tmp/zhixu-source-review-commands-web.test.tsx`与`/tmp/zhixu-source-review-commands-react-live.test.tsx`。本owner的两份仓库临时QA测试已移除；所有短暂loopback服务和测试PG容器均已退出。
- main负责known_failure真实浏览器命令（POST成功但响应丢失→完整刷新→显式同key重放→同successor），使用main独立的浏览器桥。此报告不重复或冒称该浏览器验收；本owner已证明模拟丢响应的generated Raw/React重放与真实PG恢复/再漂移。
- 两次早期PG尝试停在并行修改SQL/sum的中间状态；未改迁移，待main稳定hash后完成上述全部最终组合，早期失败不作为业务失败。
- 所有Provider均为隔离fixture固定实现，不调用外部模型，不据此宣称外部模型输出质量。核心127最终审查、schema与迁移验收由其owner/main负责。
