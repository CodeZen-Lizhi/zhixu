# 历史重发布证明审查

观察版本：2026-09-15；只读静态审查 `00128`、HistoricalRepublish owner、Authoring publication reservation 及其直接 SQL/测试上下文。未改业务代码，未重复执行全仓或矩阵。

## Findings

本观察版本未发现可证实的 P1/P2。

- 精确 selected 与当前 L/P/F：`Target` 按 revision ID 读取并核 proven 闭包，冻结 L、Document/P 版本；`Begin` 保存 selected 全文、F capture、P 正文和 fingerprint；`Apply` 重读 target、scope 和 F。见 `internal/organizing/adapter/postgres/synthesis_historical_republish.go:109-165,267-327`、`synthesis_historical_republish_apply.go:50-69`，以及 `00128_synthesis_historical_republish.sql:72-111`。
- selected 的来源/profile 和正文引用：Apply 复制完整 `synthesis_revision_source` 元组；128 的 `freeze_synthesis_source_profile` 对历史分支以 selected tuple 比较，`IS DISTINCT FROM` 保留 `NULL`；body-reference validator 将历史副本的继承父项指到 selected。见 `synthesis_historical_republish_apply.go:174-179`、`00128_synthesis_historical_republish.sql:321-335,370-403`。实库测试覆盖旧 profile 为 NULL、v2 manuscript 和 local body extension。见 `synthesis_historical_republish_integration_test.go:60-115,165-240,273-347`。
- 防伪、不可变和现有正常证明：BEGIN/APPLY event 带 payload hash、唯一键和 update/delete/truncate 拒绝；APPLY 的 deferred closure 同时核新 revision、source、reservation、note CAS 与可退役 binding。历史分支不放宽普通 revision 的 source/manuscript proof，而是先验证 selected 的 generated/receipt 或递归历史闭包。见 `00128_synthesis_historical_republish.sql:2-57,116-158,161-315`。
- 权限、Root、漂移、幂等：每个 owner 方法重新 Authorize/读取 Root；HTTP 需要 principal 和 `READ_LOCAL` + `WRITE_PROPOSAL`；Begin/Apply 均对同 key 完整命令做精确比较，Resume 仅验证既有 reservation 后重用 `synthesis-publish:R3`。见 `synthesis_historical_republish.go:59-103,239-369`、`synthesis_historical_republish_apply.go:21-58`、`synthesis_historical_republish_resume.go:21-84`、`internal/organizing/http/synthesis_historical_republish.go:24-39,81-274`。
- Authoring F/P 双基线：historical apply ID 是 repository 投影的 `PublicationMergeBaseline`，不从 API 取得；SQL view/trigger 和 Go reservation scan/revalidation 都要求 capture、P identity/hash、file base、current document pointer 一致。见 `internal/authoring/application/model.go:90-113`、`internal/authoring/adapter/postgres/gorm_publication_merge.go:12-109`、`publication.go:28-103`、`00128_synthesis_historical_republish.sql:407-462,535-614`。

## 限制

- 静态审查未替代正在由 owner 执行的真实 PostgreSQL/Git 两条主链和 CLI/browser 验收；本报告只复用当前已可读的定向测试证据。
- 审查时 `atlas/schema.sql` 仍是 125 版的 `publication_replaces_revision` / `validate_publication_merge_baseline` / publication-baseline view，未含 128 的 historical branch（见当时 `atlas/schema.sql:6081-6089,6679-6717,26474-26524`）。主 agent 已说明正在执行正式 schema 导出恢复；该工作完成后需确认导出包含 `00128` 的表、函数、约束、触发器和 view。本项是进行中的导出验证缺口，不作为当前中间版本的最终缺陷。

main 验证状态补充（2026-09-15 14:50 UTC）：已用当时128 hash `facda4110cba8aa6fa31a604e8e747af9043c8260a30c9ccdb44bea563ecb7c4` 从空库正式迁移并导出schema，再从独立空库恢复成功，Atlas lint/validate通过。owner随后为“首次发布尚不存在”的边界撤回冻结，故本次只计为中间验证；待该分支与同字节发布修完后最终重导出。未将中间检查标为最终完成。

## 最终128新增首次发布分支窄复核（2026-09-15 23:00）

结论：指定新增分支未发现明确缺陷。复核前后128 SHA-256均为 `4501e97abe705788ade7de68451147b7265e67ad8c6e64f78c305fe4dace9054`。本轮只更新本报告，未编辑产品代码、128、main schema或其他报告；未审CLI core正在实施的同字节Git修复。

- **缺席 capture**：`synthesis_historical_republish.go:195` 根据无P计算 workspace/path absence token，实际 EnsureTargetAbsent 后冻结空字节/无hash capture；`:226` 与 Apply 前后重新检查缺席，不把已有文件当作首次发布。128 `:85` 绑定无P与capture.exists=false、精确root/path；既有119 capture guard保留缺席shape。
- **publication merge**：128 `:28` receipt/historical ID互斥，CREATE_ONLY要求P identity/hash均NULL；`:432` 精确核proof、capture、file base、document version和当前P，`:440` 限CREATE_ONLY+DRAFT+无P。`:603` 缺席分支投影absence token，closure `:149` 同步核reservation。Go `gorm_publication.go:201` 按DRAFT/无P生成CREATE_ONLY；`publication.go:48`、`:65` 重算token并核模式/P形状；`gorm_publication_merge.go:38` 保留当前Document/P及latest候选校验。
- **退役**：128 `:93` 在BEGIN/APPLY锁binding/proposal并排除审批、dispatch、authorization、writeback和commit，要求明确retirement；`:154` deferred closure核旧binding CLOSED、proposal needs_revision和retirement事实。Apply `:105` 走原RetireGeneratedPublicationScoped，与新revision、source、reservation、note CAS同事务，未取消已批准操作。
- **来源/profile/body**：128 `:142` 比较selected与新revision完整source元组集合；`:329` 起精确继承profile（含NULL，不补当前profile）；`:375` 以selected作为body-reference继承依据，仍核真实上游published binding。复用core旧profile NULL、v2全文、body local extension/下游传播证据，不重跑旧P/F场景。

实际执行（非仅编译或Skip），均由testdb新建 `pgvector/pgvector:pg16` Docker隔离库，Config未传ExternalAdminURL，无用户数据库连接；两个PG容器正常终止。GOCACHE=/tmp/zhixu-history-gocache：

```text
go test -tags=integration ./internal/organizing/adapter/postgres -run '^TestSynthesisHistoricalRepublishBeforeFirstPublication$' -count=1 -timeout=120s -v
exit=0；test PASS 12.30s；package 13.377s；进程含编译20.15s
日志 /tmp/zhixu-history-final-check/organizing.log

go test -tags=integration ./internal/authoring/adapter/postgres -run '^TestGeneratedAuthoringPostgreSQLAtomicReplayAndScope$' -count=1 -timeout=120s -v
exit=0；test PASS 7.37s；package 8.484s；进程含编译9.62s
日志 /tmp/zhixu-history-final-check/authoring.log
```

外层subprocess固定180s超时，未触发。首项使用正式128迁移，实际旧未发布候选→明确退役L→新候选保持无P→原审批/Git首次发布→正式全文精确读取。第二项使用94 Authoring专用夹具及typed-empty后续proof依赖，证明原atomic/replay/scope兼容，不冒称它验证128跨owner闭包。未额外运行全仓、独立lint/vet或浏览器。

复用main `history-republish-browser-verification.md` 与已读取原日志 `/tmp/zhixu-history-republish-browser-verified/test.log`：真实浏览器PASS311.78s、main记录exit0。该证据基于facda411中间SQL，仅证明已有P且F≠P、丢响应/刷新/Resume及正式阅读，不扩大到最新首次发布分支的浏览器验收。

尚未验证：首次发布分支专属缺席漂移/并发/直接SQL负例完整矩阵，本轮只静态核门禁；正式schema最终恢复由main负责（当前导出已可见CREATE_ONLY/historical分支，但本轮未运行其恢复验收）；同字节Git最终修复由core独占，未审中间实现且不作为新发现。真实外部模型质量、完整AppShell登录均不在本轮证明范围。

done

main 最终Schema状态（2026-09-15）：128最终4501e97版本正式空库迁移/导出/另空库恢复及Atlas lint/validate均通过，见schema-128-validation.md。上面的facda411中间结果保留作历史，当前正式atlas/schema.sql已包含无首次P/F分支。同字节Git窄修仍由core接续，须独立审查其新增权限和commit校验，不能沿用本次Go/SQL静态结论冒称已审。


## 同字节最终窄审补结论（2026-09-15，恢复中断会话）

结论：冻结的同字节历史重发布分支未发现可证实的 P1/P2。补完此前剩余的 Root 门禁与恢复调用链判断；没有修改业务代码、SQL/schema，没有重跑已通过检查，也没有扩大验收矩阵。

- **授权与 Root 门禁**：`internal/changecontrol/application/writeback_service.go:183` 的 Resume 先核 execution/workspace/workflow/node，再于 `:203` 从 Authoring owner 重读精确历史证明。`internal/authoring/adapter/postgres/gorm_historical_republish_git.go:13` 绑定 immutable receipt/reservation、proposal revision、approval、execution、target、HEAD、change hash，要求 F=result 并重算正文 hash；该能力只放宽同字节 Git delta，不替代 Workspace Root 授权。生产 Worker `cmd/worker/main.go:484,1478` 使用 runtime repository 构造 Git client；`internal/workspace/runtimegrant/gorm_composition.go:76` 注入 RootGrantResolver。Git Diff/Find/Commit 均经 `internal/platform/gitcli/writeback_inspect.go:307` → `internal/workspace/adapter/postgres/gorm_repository.go:67,319` 的 Resolve、精确 ID/root 比较和 Revalidate；managed resolver 缺席失败关闭。新增 authority 不携带任意 root，不绕过该链。FILE_APPLIED/GIT_PREPARED 仍先通过 `writeback_service.go:605` 验证 lease 并 ResumeTarget。
- **空 diff 的边界**：`internal/platform/gitcli/writeback_inspect.go:211` 只接受精确 workspace/path/HEAD/result authority 下的 REPLACE、全仓 clean、空 diff 与同 blob；`internal/changecontrol/domain/historical_republish_git.go:36` 进一步绑定完整 execution/approval/workflow/node 身份。commit/tree 与 lookup 的最终检查仍核目标 blob/mode、parent、完整固定 message，历史 receipt trailer 必须一致（`internal/platform/gitcli/writeback_commit.go:425,463,811`）。普通无授权或错 receipt 不能用相同正文冒认历史提交。
- **丢响应恢复**：`writeback_service.go:444` 先 Find 后 Commit，仅 NotFound 进入提交，其余不确定结果保留恢复门禁。`internal/platform/gitcli/writeback_commit.go:926` 在 update-ref 命令报错后重查原 commit，要求 HEAD 精确匹配并 Inspect 后返回 Recovered；`:975` 的有界恢复 context 使用 WithoutCancel，保留进程内 authority，未因请求取消丢失 receipt。跨 Resume 则从 owner 重新读取，不依赖上一进程 context。查得提交后沿原 checkpoint/publication 路径推进，不另造同字节提交。

证据复用：本代理中断前实际 `TestSynthesisHistoricalRepublishSameBytesPostgreSQLGit` PASS **12.65s**，package **13.274s**，日志 `/tmp/zhixu-history-final-check/samebytes.log` 已重新读取核对。core 限定 PG/Git 回归日志 `/tmp/zhixu-history-git-regression.log` 为 PASS **50.531s**；五包普通单测日志 `/tmp/zhixu-history-git-unit.log` 均通过。core 报告记录 git-unit 的无授权/错 execution/错 receipt 拒绝、update-ref 成功丢响应找回同 commit 和重复恢复不增 commit；vet PASS 沿用 core 执行结论（`/tmp/zhixu-history-git-vet.log` 为空输出，本次未重跑，不单凭空日志推断退出码）。

限制：Root 漂移/撤权与同字节发布交错的专属故障注入、完整并发矩阵及同字节专属浏览器流程未在本轮执行；静态调用链结论不冒称这些场景已有动态证明。main 的 browser 311.78s 与最终128 schema恢复沿用其独立报告，不扩大浏览器证据到未执行分支。原报告中“同字节修复尚未独立审查”的待办由本节关闭；原有其他验收限制继续有效。

done
