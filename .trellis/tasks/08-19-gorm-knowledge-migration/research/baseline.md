# Knowledge GORM Migration Baseline

## Code Surface

- Domain Repository：`internal/knowledge/domain/repository.go`，15个command/read方法，批量上限500，无具体数据库类型。
- PostgreSQL legacy：`repository.go`、`claim_commands.go`、`relation_commands.go`、`conflict_commands.go`、`read.go`、`eligibility.go`、`evidence_topic.go`、`timeline.go`、`timeline_projection.go`、`relation_apply.go`。
- Application专用端口：Claim Query、Evidence Topic、Timeline Reader/Event Projector、Impact Repository/Audit、Timeline Projection、Approved Relation Apply/Approval。
- Impact Audit adapter：`internal/knowledge/adapter/audit/impact.go`；legacy透传`any`到Audit Recorder。

## Production And Consumer Inventory

- API legacy构造：`cmd/api/main.go:589,984,1096,1590`。
- Worker legacy构造：`cmd/worker/main.go:1855,2308,2764,3105`。
- Organizing在caller-owned `pgx.Tx`内构造legacy Repository：`internal/organizing/adapter/owner/transaction_fence.go:313`；本child不得拆分该事务。
- Relation Apply真实行为基线主要在`internal/graph/adapter/postgres/candidate_approval_apply_integration_test.go`。
- Knowledge自身repository/timeline integration覆盖command、constraint、batch、impact、projection和response loss；环境由`ZHIXU_TEST_DATABASE_URL`门控。

## Schema And Transaction Facts

- `migrations/00017_knowledge_domain.sql`唯一拥有Topic/Alias、Claim/Source、Relation/Evidence、Conflict/Member、Knowledge receipt。
- Confirmed Claim必须有SUPPORTS来源；Confirmed Relation必须有confirmed evidence；Conflict member/Claim状态、Topic/Claim retirement存在deferred constraint。
- 普通command统一Workspace `FOR KEY SHARE` -> advisory receipt lock -> receipt -> aggregate -> receipt -> commit。
- Relation endpoint按canonical `(type,id)`排序`FOR SHARE`；Conflict members按ID `FOR UPDATE`。
- Relation Apply固定Candidate-before-Proposal，再Approval、receipt、endpoint/provenance、Relation、Proposal、Event。
- Batch aggregate read使用RepeatableRead+ReadOnly；Timeline projection使用`FOR UPDATE SKIP LOCKED`。
- Impact Report与Audit当前同pgx transaction；GORM需新的opaque scoped capability。

## Verified Baseline And Blind Spots

- Existing package tests/compile are the regression baseline; no staged Knowledge GORM implementation exists at planning time.
- `ZHIXU_TEST_DATABASE_URL` is not configured, so PostgreSQL locks, trigger/deferred constraints, driver bindings, SQLSTATE, response-loss and EXPLAIN remain TODO 9 evidence, not completed acceptance.
