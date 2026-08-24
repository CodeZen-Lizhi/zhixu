# Authoring Go/API、接线与测试基线

## Owner 与能力

- 主 Port `internal/authoring/application/model.go` 的 `Repository` 共 13 个方法：Draft create/get/update/list/freeze、Document list/detail/revision batch/overview，以及 publication reserve/abandon/complete/reconcile。
- 可选 `ArticleRevisionSearchRepository` 提供 bounded latest Revision search。
- Change Control Finalizer 额外要求 `ValidateRestoreWriteback` 与 `FinalizeRestorePublication`；Reconcile 已在主 Port。
- Artifact Verifier 与 Organizing owner reader 只消费 `GetArticleRevisions`。
- Application/Domain 当前不导入 pgx、GORM 或 database/sql，迁移无需改变公开契约。

## Legacy owner 文件

- `repository.go`：Draft、command receipt、Freeze、Document/Revision scanner 与 validation。
- `publication.go`：Reservation、Abandon、Binding、Reconcile、Proposal/commit proof。
- `read.go`：Search、Detail、ordinal Revision batch、Overview。
- `restore_publication.go`：Restore owner check 与 append-only published Revision closure。
- `errors.go`：SQLSTATE/constraint-name 到稳定 Foundation error 的映射。

## 生产与 consumer inventory

生产 legacy 构造共 2 个：API Authoring service/handler 与 Worker Authoring Repository。Worker 同一个 Repository 被交给 Authoring Change Control PublicationFinalizer、Artifact Authoring Verifier 和 Organizing DocumentContentReader。

本 child 不修改这些 Composition 或 consumer；Final 必须把 API helper 当前的 `*pgxpool.Pool` 输入提升到完整平台 Pool，不能机械替换 constructor 名称。

## 核心事务基线

- Create/Update/Freeze 使用 `workspace:idempotencyKey` transaction advisory lock，先查 immutable receipt；Update/Freeze 再锁 Draft，Freeze 继续锁 Document/latest Revision。
- Freeze 首次创建 Document，后续追加 parented Revision；Draft CAS、Revision insert、receipt 在一个事务。
- Reserve/Abandon/Complete 复用 publish command lock；Complete 的 Binding insert、Reservation close、PUBLISH receipt 原子提交。
- Reconcile 先有界取 ID，再逐 Binding 独立事务，锁 Document、Revision、Binding；只根据 exact Proposal/commit 推进状态。
- Restore 使用 `restore-publication:writebackID` lock、稳定 UUIDv5 Revision ID、append-only bridge 和 Document CAS。
- GetDocumentDetail/GetOverview 使用 repeatable-read read-only；List 使用 stable keyset；Revision batch 使用两个 uuid[] + ordinality。

## 现有验证资产

- `repository_integration_test.go`：Draft lifecycle、CAS/replay/freeze、Workspace、keyset、path conflict、并发 command/freeze、CREATE_ONLY、ABANDONED、path guard 和 migration down/up。
- `publication_integration_test.go`：reservation/completion/replay/read、reconcile publish/recovery、Restore append/replay/concurrency。
- `hardening_integration_test.go`：raw SQL forge rejection、deferred closure rollback 与 migration hardening。
- unit tests 覆盖 Domain time/hash/path/state、Application binding/replay、Change Control adapters、HTTP boundary 和 error classification。

fixture 当前创建 raw `*pgxpool.Pool`，运行全 migration 后构造 legacy Repository；`ZHIXU_TEST_DATABASE_URL` 为空时 skip。TODO 9 应原位改成完整 `platformpostgres.Pool` factory，并为 legacy/GORM 每个子用例创建独立 database；Schema-only migration tests 保持单实现。

## 2026-08-19 baseline

已通过：

```text
go test -mod=vendor ./internal/authoring/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/authoring/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/authoring/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/authoring/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s
go list -mod=vendor ./internal/authoring/... ./internal/platform/postgres
go mod verify
git diff --check
```

真实 PostgreSQL URL 当前 absent；integration 只完成编译，不能证明 staged GORM 等价或完成 TODO 9。
