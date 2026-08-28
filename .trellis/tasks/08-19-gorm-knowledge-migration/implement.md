# Knowledge GORM Migration Checklist

## 0. Planning Gate

- [x] 读取 Trellis workflow、backend database/error/logging/quality spec、父任务、Foundation/Change Control/Events/Audit staged contract、目标代码/Schema/测试。
- [x] 盘点 Knowledge core、Timeline/Impact、Projection、Relation Apply、生产构造、Organizing caller-owned transaction与integration suite。
- [x] 固定 receipt/aggregate/endpoint/member/Candidate/Proposal锁序、DB/caller time、CAS、deferred constraint、JSON、错误、response-loss与敏感边界。
- [x] 补齐 PRD、Design、Implement、baseline、implementation contract与manifests。
- [x] 独立 planning review发现的Scoped Impact编译期契约与disposable DB fixture两项P1已修复，task validate通过。
- [x] `task.py start` 后确认task status=`in_progress`，再修改Go文件。

## 1. Scoped Contract And Foundation

- [x] 新增 `ScopedImpactAuditPort`、`ScopedImpactRepository.SaveImpactReportWithScopedAudit`与`NewScopedImpactServiceWithAudit`，现有ImpactRecorder用Audit `RecordScoped`实现；legacy Port/构造完整保留。
- [x] 新增 `GORMRepository`，从单一Pool取得GORM/UoW，提供ready/within/Raw Row/Rows/Exec/no-row/context/SQLSTATE helper与callback/commit阶段分类。
- [x] 增加显式persistence records、JSONB string carrier、nullable/array carrier；禁止Schema ownership与implicit GORM behavior。
- [x] 编译期断言Domain Repository、Claim/Evidence/Timeline/Impact/Projection端口；legacy Repository/DB/constructors与production wiring保持可编译。

## 2. Knowledge Commands And Reads

- [x] 实现receipt、Create/Get Topic、Suggest/Confirm/Transition Claim，保持Workspace key-share、advisory lock、fingerprint exact replay、source与CAS。
- [x] 实现Suggest/Confirm/Transition Relation，保持canonical endpoint锁、fingerprint/evidence/confirmation/deferred fence。
- [x] 实现Open/Transition Conflict，保持Topic/Claim stable lock、成员至少两个、Claim disputed闭包与receipt。
- [x] 实现三类bounded aggregate read的RepeatableRead+ReadOnly快照，以及Evidence eligibility/topic批读；数组单绑定、full hydration、无N+1。

## 3. Timeline, Impact And Projection

- [x] 实现Timeline event read/append/list，保持source-ref advisory/exact replay、Workspace/keyset与严格extension JSON。
- [x] 实现Impact event/report/object读取与save/replay；`SaveImpactReportWithScopedAudit`只调用scoped Audit并与report同一UoW。
- [x] 实现Timeline `ProjectNext`，保持`FOR UPDATE SKIP LOCKED`、单source短事务、project/poison CAS与ManualRecovery语义。

## 4. Approved Relation Apply

- [x] 新增 `GORMApprovedRelationApplyRepository`，构造依赖单Pool、IDs、Clock和optional scoped Events；supplied typed-nil fail closed，zero option保留legacy optional语义。
- [x] 实现ApproveAndApply/Apply：Candidate-before-Proposal，Approval/Proposal、receipt、endpoint/provenance、Relation/Evidence与Event单scope原子写。
- [x] 保持baseline stale仅提交needs_revision、普通错误全回滚、success/stale commit code与Approval+receipt exact replay。

## 5. Static Compatibility And Security

- [x] 所有Raw SQL参数化，Rows Close/Err/no-row按调用点映射；无AutoMigrate/Migrator/Save/Preload/Association/root Transaction/独立gorm.Open。
- [x] context classifier保留canceled/deadline/custom cause、sql.ErrTxDone与原始PgError chain；稳定code/retryability与legacy等价。
- [x] 静态确认API/Worker/Organizing仍只构造legacy pgx，无selector/双写/fallback；migration和其他owner不因本child改变。

## 6. Focused Static Verification

- [x] `go test -mod=vendor ./internal/knowledge/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/knowledge/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/knowledge/... ./internal/platform/postgres ./internal/events/... ./internal/audit/...`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/knowledge/adapter/postgres ./internal/knowledge/adapter/audit ./internal/knowledge/application -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/organizing/... -count=1 -timeout 60s`
- [x] `go list -mod=vendor` scoped packages、`go mod verify`、`go mod tidy -diff` read-only。
- [x] forbidden/static scan、`task.py validate`、`gofmt -d`、scoped/global `git diff --check`。

## 7. Required Review

- [x] Go Review：接口完整性、typed-nil/context、scope/UoW生命周期、Rows、replay/commit ambiguity、生产兼容。
- [x] SQL Review：参数化、Workspace隔离、JSON/array、锁序/deadlock、CAS、time、deferred trigger、Events/Audit原子性、性能/N+1。
- [x] Trellis Check：spec/task范围、dirty worktree保护、验证证据、TODO 9/Final状态门禁。
- [x] 修复范围内明确问题后重跑门禁，并写 `research/static-validation.md`。

## 8. TODO 9 PostgreSQL Gate

- [ ] 仅在现有integration文件建立legacy/GORM fixtures；每个实现独立disposable DB，不新增测试文件。
- [ ] 现有integration文件内建立fixture：管理连接创建唯一DB -> migration pool完整迁移并关闭 -> 一次`platformpostgres.Open`；legacy/GORM独库，GORM seed先commit，再由同一Pool构造Knowledge/Events/Audit GORM与Scoped Impact Service，无全局pending outbox、第二root或no-op。
- [ ] 成对覆盖全部Knowledge command/read、deferred constraint、500批读、Timeline/Projection、Impact+Audit、Relation Apply+Event与失败回滚。
- [ ] 包装被测Repository自身UoW仅首次执行真实Within后注入错误：首次commit failure，后续由同Pool未包装实例按receipt/source/binding exact replay；并发barrier不靠sleep。
- [ ] 验证真实SQLSTATE、DB/caller time、JSONB/uuid/text arrays、cancel/deadline、Rows/连接与关键EXPLAIN。
- [ ] 在用户允许的外部DSN/Testcontainers环境运行受影响integration race gate。

## 9. Completion And Rollback

- [x] TODO 9不可用时保持PRD AC未勾、task status=`in_progress`、legacy production不变，不归档/宣称完成。
- [ ] TODO 9通过后记录Graph/Organizing/Final constructor和legacy删除清单。
- [ ] 回滚只删除本child staged adapter/helper与scoped Impact capability；不回滚Schema/Foundation/Change Control/Events/Audit或用户改动。
