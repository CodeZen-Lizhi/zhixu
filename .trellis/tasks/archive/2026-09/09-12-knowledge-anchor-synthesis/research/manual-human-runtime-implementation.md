# 生产 HumanWait 与人工裁决后继 apply

本切片已实现 Go runtime、生产 HumanAuthority、owner review service、HTTP 与 cmd/api 组合。未提交、push、部署；未改 124 owner 文件、历史 SQL、Atlas schema/hash、v1 图/hash，未新增125。Trellis channel 写锁被当前文件沙箱拒绝，因此通过本研究文件与 manual-human-http-contract.md 协调；main 已读并沿用该方式。

## 接线与边界

- `workflow/synthesis_manuscript_runtime.go`、`synthesis_executor.go`：Prepare 返回真实 HumanWaitResult；merge_review 遍历并准备全部 changed-existing targets，不因第一处冲突提前返回；clean 项保存旧 clean receipt。全 clean 路径不创建 HumanTask。
- `application/synthesis_manuscript_review_service.go`：具名 per-call CallerCapabilities 复制、schema、受控 summary/detail、逐阶段 Decide→ReadManifest→全部ready→RuntimeHumanCoordinator.SubmitHuman。内层裁决揭示外层冲突时任务保持pending；已落决定/receipt但HTTP响应丢失时同命令恢复并补交同一task。
- task schema 仅 owner结果身份 version/processing_id/workflow_run_id/note_ids/result_hash，无全文；note_ids sorted enum绑定完整目标集。result_hash来自排序后全部真实receipt identities。
- `postgres/synthesis_manuscript_human.go`：HTTP每请求创建不可变authority，不往context放布尔权限、不伪造ExecutionContext。按run→node→task锁序核对真实任务/目标版本/注册图/processing/currentrun/schema，调用RootGrantResolver当前能力；真实模型generation及独立semantic journal精确证明由 `synthesispostgres/manuscript_model_proof.go` 的历史入口完成，不要求旧节点仍RUNNING。
- `postgres/synthesis_manuscript_baseline.go` 拆 `readOwners` 供当前 typed runtime 与独立 human proof 复用。第一次决定重新核验 F-independent L/Article/Document/P与祖先/来源/逐span/scope/无anchor/root；124 owner另核验F。runtime apply仍typed lease，重新读取124 resolved receipt并纯重放，当前所有owner/file基线仍重验。
- `postgres/synthesis_manuscript_runtime.go`：apply根据真实receipt重建完整owner结果，精确比对实际submitted HumanTask decision；通用task提交不能替代receipt。无receipt或结果hash不符拒绝创建候选。
- `workflow.SynthesisExecution.HumanWaitDuration` 与 synthesispostgres实际task查询：只从v2真实已提交merge HumanTask计算created→submitted等待时长，扣除该段人工等待，不放宽模型执行预算或历史v1图。未实际等待24小时；长等待预算策略以持久身份读取与代码审查为证据。

## HTTP / API

`http/synthesis_manuscript_review.go`、SynthesisHandler routes、auth capability表、`cmd/api/synthesis_manuscript.go`/原组合：三个独立review GET/GET-note/POST-decisions端点，准确path/DTO见 `manual-human-http-contract.md`。

认证层真实principal.Scopes传NewSynthesisManuscriptCaller，零值/缺principal（包括disabled开发入口）拒绝；要求READ_LOCAL+WRITE_PROPOSAL且复核图能力。生产复用workspaceRuntime.Resolver，同池候选/source/model owner，不要求Provider。

summary只返回binding/ready/submitted/最多8个目标身份；detail只返回所选note真实三方/候选/冲突。HTTP conflicts窄映射小写 `{ordinal,base,current,proposed}` UTF8文本，不暴露内部[]byte base64、Prepared、日志或authority。POST严格schema/Unicode/大小/路径-body/幂等键一致性。无真实待审/已提交task返回404 `SYNTHESIS_MANUSCRIPT_REVIEW_NOT_PENDING`，权限、stale与5xx不伪装为“无冲突”。

最后一份裁决自动提交同task，不新增/continue或额外审批步骤。正式正文与发布指针不变；生成结果仍是原审批链处理的候选。

## stale恢复

现有Retry只接受FAILED且retryable；RecoveryRequired不可重试。已实际证明：pending旧裁决因F漂移拒绝 → 既有RuntimeCoordinator.Cancel取消真实task/run → terminal hook记录FAILED/retryable → 读取新processing.version → 既有RetryProcessing创建新run → 同processing的新task/attempt/capture重新prepare → 人工裁决后apply。旧task/attempt不能再提交，旧历史保留；124按精确WorkflowRunID过滤manifest。

这不是同一task内偷偷改schema/attempt，亦不把RecoveryRequired改成可重试。两阶段正常裁决仍同task。

## 正式124实际验收

所有PG测试均用现有testdb、正式Atlas目录（main维护最终hash），真实PG + River + 固定Provider；该链HumanTask、generation/semantic model journals和RootResolver为生产实现，不使用overlay/禁trigger/假context。初始业务数据沿用已有publication fixture，最终新链从真实source/bodyrefresh dispatcher开始。

1. `/tmp/manuscript-human-unanchored.log` PASS33.247s：普通无anchor source-ready真实冲突→HumanWait→裁决→apply，非审批发布。
2. `/tmp/manuscript-human-two-stage.log` PASS21.164s：先生成真实人工v2，再下一代生成同时触发内层与外层冲突；第一阶段保存不释放task，第二阶段用新fingerprint、同task；4次模型调用（两轮各生成+semantic），无重复调用。
3. `/tmp/manuscript-human-recovery.log` PASS37.249s：真实取消→FAILED/retryable→新run/task/attempt重备→apply；通用SubmitHuman使用格式合法但无receipt的伪result_hash，后继FAILED，零receipt/候选。
4. `/tmp/manuscript-human-http-integration.log` PASS40.359s：BodyRefresh冲突及两阶段，经真实PG Auth bootstrap/session→API Token→middleware→principal→HTTP GET/POST；READ_LOCAL-only Token403；跨workspace、未知note、伪capture、文件漂移、root inactive均拒绝；落库后响应丢失恢复，不同命令复用key拒绝；发布指针和F字节保持不变。
5. `/tmp/manuscript-human-owner-multi.log` PASS56.409s：pending Document版本漂移及无anchor→真实CreateAnchor范围变化均零decision/receipt；两个真实生成targets中冲突项排第一、后续clean项仍prepared/sealed，summary仅部分ready，最后全部receipt齐备后原子生成两份候选。
6. `/tmp/manuscript-human-http-captures.log` PASS25.360s：两阶段均实际HTTP提交；采集 `/tmp/zhixu-manuscript-http-fixtures/human_two_stage/`。detail-1=CANDIDATE_MANUAL_CONTENT、detail-2=WORKSPACE_MANUAL_CONTENT；decision-1 ready=false/submitted=false，decision-2 ready=true/submitted=true。无凭据输出。这些是实际handler报文，可供Web固定数据核对，不等于浏览器已驱动活PG。
7. `/tmp/manuscript-human-local-regression.log`：application/workflow/organizing HTTP/auth HTTP/cmd-api 的 Synthesis|Manuscript 定向测试PASS。`/tmp/manuscript-human-vet.log`：相关8包vet PASS；integration tags两PG包vet日志 `/tmp/manuscript-human-integration-vet.log`。

测试入口复用 `synthesis_manuscript_runtime_integration_test.go` 的 `testSynthesisManuscriptRuntime`、`resolveRuntimeHuman`、`manuscriptAuthenticatedHTTP`；后续浏览器或原审批Git可以在真实候选处接已有fixture，无需另造HumanAuthority或model日志。未全仓test。

## 审查与剩余边界

已按Go review检查当前权限、锁序、typed执行/历史日志分离、receipt重放、API边界和资源释放。独立 `manual-human-core-review.md` 除通用schema validator不执行enum/pattern外无新增P1/P2；main已将该通用validator补强交独立human-schema-validation代理，不在本切片改其状态机。当前owner/apply已经拒绝无真实receipt或错误结果身份；schema约束不是receipt证明。

Web/OpenAPI由human-ui负责；本切片证明真实HTTP到PG/Worker，不声称完成浏览器实际产品闭环，也未重新执行人工新候选的最终审批Git发布。已有clean发布证据不冒充本次完整人工发布验收。SOURCE_REVIEW_REQUIRED审计重复来源仍是明确剩余业务接线，未恢复MachineItems可信性来绕过。全任务尚未完成。

补记：integration tags 的 postgres/synthesispostgres 两包vet已PASS，定向diff检查PASS；HTTP文本冲突wire已核对实际捕获字段与两阶段false→true状态。

最终HTTP路由核对：新增三端点后inventory基线238→241（仅修改Go测试常量），`TestRouterRoutesExactlyMatchOpenAPI` PASS3.320s，日志 `/tmp/manuscript-human-routes.log`；OpenAPI生成与Web仍由UI owner维护。此后未继续扩大测试范围。

## Resume：最后 receipt 已存而 SubmitHuman 前退出（2026-09-15）

已新增 owner `Resume(ctx, binding)` 及认证 `POST processing/:processing_id/manuscript-review/resume`，严格 body 为 `{"binding":完整SynthesisManuscriptHumanBinding}`，200直接返回既有Summary。详情见 manual-human-http-contract.md 的Resume稳定契约。正常Decide最后一项仍自动Submit；二者共用submitReady，Resume不调用Decide、不保存receipt、不调用模型，仅ReadManifest真实授权完整集合→owner结果身份→SubmitHuman原task。没有放宽processing Retry、Proposal编辑或修改apply receipt/结果身份复核。

生产改动：application/synthesis_manuscript_review_service.go；HTTP synthesis_manuscript_review.go、synthesis_handler.go；auth/http/handler.go。测试沿用postgres/synthesis_manuscript_runtime_integration_test.go原helpers，twoStage分支直接保存最后receipt后清空旧command，重建service与认证HTTP组合，仅使用GET获得的完整binding恢复。其他分支保留正常Decide和原命令精确重放。router_inventory_test.go操作数随UI已落盘Resume路由241→242。未编辑Web、OpenAPI、runtime_validation.go。

验收：`GOCACHE=/tmp/zhixu-human-go-cache go test -tags integration ./internal/organizing/adapter/postgres -run 'TestSynthesisManuscriptRuntimeBodyRefresh/human_two_stage$' -count=1 -timeout=5m` PASS23.487s，日志 `/tmp/manuscript-resume-integration.log`。真实PG+River+认证Token HTTP，两轮真实生成、第二轮两阶段；GET验证ready=true/submitted=false且detail.Review=nil，binding-only Resume→原task/apply成功，重复Resume及apply后owner Resume成功，模型调用数不变、decision/receipt计数保持既有预期。未ready、跨workspace、错task、篡改target_version、READ_LOCAL-only、Root inactive均拒绝。原apply候选与发布指针/F不变断言继续通过。

局部验证：organizing/application、organizing/http、auth/http包测试PASS（`/tmp/manuscript-resume-local.log`）；上述包及postgres带integration tag的vet PASS（`/tmp/manuscript-resume-vet.log`）；TestRouterRoutesExactlyMatchOpenAPI PASS（`/tmp/manuscript-resume-routes.log`）；本轮文件diff检查PASS。默认Go cache受沙箱限制，改用已有/tmp cache后完成，未申请额外权限。Go review自检未发现本轮新增问题。

交回main：`internal/organizing/adapter/postgres/synthesis_manuscript_runtime_integration_test.go` 现已停止编辑，resolveRuntimeHuman、manuscriptAuthenticatedHTTP及全部既有helpers保留，可由main加入仅测试checkpoint并接Browser→原审批Git→阅读验收。本轮不声称已完成真实浏览器或新候选审批发布验收。无commit/push/部署。
