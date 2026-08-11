# 执行计划

## 1. Implementation Order

### Phase A: Freeze contracts and migration behavior

1. 先新增 domain/application tests，锁定 global phase、participant phase、commit-side failure方向、exact target/idempotent Start、runtime owner stale takeover和 Snapshot派生；替换旧 restart-only state tests时保留仍适用的 desired/active/applied、Secret和revision不变量。
2. 新增 `migrations/00079_model_settings_hot_activation.sql` 与 migration integration tests：
   - legacy idle/failed升级并保留数据；
   - legacy live rollout fail closed；
   - participant约束/CAS/guarded Down；
   - runtime mutation gate和Workflow Attempt guard前向更新；
   - active revision只在 arming -> activating改变一次。
3. 扩展 PostgreSQL Repository/ports：Start、participant register/heartbeat/transition、CommitActivation、AcknowledgeActivation、FinalizeActivation、pre-commit recovery和post-commit adoption。所有时间与stale判断使用数据库时钟，锁顺序固定并写入测试。

依赖：后续 RuntimeHost、Workflow Claim和HTTP都以该状态/Store contract为基础，不在 Phase A稳定前并行修改共享 domain/ports。

### Phase B: Build the generation host

4. 在 `internal/modelsettings/runtime` 实现 `RuntimeHost[T]`、immutable generation、acquisition gate、lease/refcount、candidate/active/retiring map、历史 revision resolver和幂等 Close。
5. 扩展 `runtime.Models` / `platform/models` 资源所有权，使自建 model Transport可 `CloseIdleConnections`，失败/abort candidate和最终 retired generation都只关闭一次；保持外部注入 client不被错误关闭，并补 Secret/Endpoint canary。
6. 实现 API/Worker role generation factories。把模型相关依赖打包进 generation；不依赖模型的 Route、Definition、Repository、River client保持稳定。新增 host并发测试覆盖 Acquire/Arm/Activate/Release/Abort、构造失败、context cancel、同 revision重复构造和 `-race`。

依赖：Phase B只消费 Phase A ports，不自行拼数据库 phase；Phase C/D只通过 `RuntimeAcquirer` 使用 generation。

### Phase C: Preserve Workflow and Retrieval provenance

7. 修改 Workflow Claim contract：
   - 新 Attempt由 PostgreSQL从 active state + fresh Worker serving row选择 revision/instance；
   - exact existing delivery返回持久 binding并保持lease/replay限制；
   - `RuntimeNodeWorker`按 Claim返回 binding Acquire一次，持有到 execute/settlement结束；
   - arming/activating期间新 Claim短暂阻塞，running Attempt不中断。
8. 把 Worker model-dependent Executor/Search/Source Processing/Reindex graph按 generation构造；Git Sync、direct Source refresh和Reindex都持有完整 operation lease。修复 Artifact ModelRun遗漏的 `model_settings_revision` 写入与 replay equality。
9. 把 API/Worker Search与Vector Builder从固定 Embedder改为按 Active Index/Embedding Version完整 Contract acquisition；覆盖相同Contract跨revision复用、不兼容重建、旧Index查询、历史pending Reindex和无法重建的fail-closed路径。

依赖：Phase C必须在 RuntimeHost API稳定后进行。Workflow和Retrieval可由不同 implement sub-agent处理，但不得同时修改 `cmd/worker/main.go`；由主会话按顺序整合 Composition Root。

### Phase D: Run activation inside API/Worker

10. 在 API composition启动常驻 `ActivationCoordinator` 与 API RuntimeHost；在 Worker启动 Worker RuntimeHost。普通启动加载 active；preparing/arming恢复旧active+candidate；activating启动先保持fence并加载target后ack。
11. 用新 admission protocol替换旧 process-replacement drain：preparing正常服务旧generation，arming短暂停止新model acquisition/Workflow Claim，activating保持fence直到两端applied+idle；不等待running Job count归零。
12. 保留 `modelctl`/`./zhixu restart` 为运维恢复路径，移除正常Settings页面和launcher对“保存后必须restart”的依赖；restart遇到 `preparing|arming` 只恢复到旧active，遇到 `activating` 向前加载target并完成finalize，`idle` 下不自动应用pending desired；不得删除软件升级/Workspace restart能力。

### Phase E: HTTP, OpenAPI and frontend

13. 新增 Session-only `POST /api/v1/settings/models/activations`：strict body `{expected_revision}`、CSRF/Origin、no-store、202 Snapshot、same-target幂等和409/503稳定Problem。GET Snapshot分别投影 serving与participant；新增 `apply_required`，将 `restart_required`保留为deprecated false兼容字段。
14. 原子更新 Router policy/method tests、OpenAPI path/schema/checker和 `web/src/api/model-settings.ts` strict encoder/decoder。Activation请求只携带revision，不携带draft/Secret/Endpoint。
15. 更新 `ModelSettingsPanel`：
   - dirty draft主按钮“保存并应用”、次级“仅保存”；
   - saved pending主按钮“应用配置”；
   - PUT成功后清Secret并用返回revision Start；
   - non-terminal立即进入2秒polling，response loss/refocus/network recovery权威refetch；
   - role progress、old active仍服务、failed retry和degraded状态独立展示；
   - 删除restart指令，不增加用户cancel/rollback按钮。
16. 补API与组件测试：unknown/缺字段、202、exact target、重复Start、保存成功但Start失败、刷新恢复、polling terminal stop、API/Worker单边失败、长文本、键盘/焦点、Secret/Endpoint/instance ID扫描和390x844布局。

### Phase F: Architecture, specs and operational proof

17. 新增 ADR-0022并更新ADR索引、`docs/architecture/ai-runtime.md`、`docs/architecture/system-design.md`、`docs/operations.md` 与 `docs/requirements.md`。明确 no-restart正常路径、短fence、commit前后恢复方向、Embedding历史resolver、升级/Down限制和Authorization硬清零限制。
18. 在实现/验证完成后使用 `trellis-update-spec` 更新 backend/frontend Model Settings、Database/Workflow、Artifact provenance和质量门禁；不得在代码证据完成前把计划写成已交付事实。
19. 运行定向门禁、真实PostgreSQL并发/迁移测试、前端门禁和受控fake model浏览器验收。记录API/Worker容器ID与StartedAt在Save/Apply前后不变。
20. 依次执行 `go-review`、`sql-code-review` 和独立 `trellis-check`；修复发现后重跑受影响门禁，最后再提交、push、归档。

## 2. Expected File Surface

### Model Settings domain/application/PostgreSQL/runtime

- `migrations/00079_model_settings_hot_activation.sql`（新增）
- `internal/modelsettings/domain/rollout.go`
- `internal/modelsettings/domain/errors.go`
- `internal/modelsettings/application/ports.go`
- `internal/modelsettings/application/service.go`
- `internal/modelsettings/application/coordinator.go` 及测试
- `internal/modelsettings/adapter/postgres/{repository,rollout,runtime,snapshot,revision}.go` 及 integration tests
- `internal/modelsettings/runtime/{loader,controller,models}.go` 及测试
- `internal/modelsettings/runtime/host.go`、`generation.go`（名称按最终package结构收敛）
- `internal/platform/models/{runtime,model_transport,chat_http,embedding_http}.go` 及资源生命周期测试
- `internal/platform/migration/model_settings*_integration_test.go`

### Workflow, Agent, Artifact and Retrieval

- `internal/workflow/application/runtime_contract.go`
- `internal/workflow/adapter/postgres/runtime_state.go` 及 integration tests
- `internal/workflow/adapter/river/runtime_worker.go` 及 tests
- `internal/agent/adapter/workflow/{executor,rag_executor}.go`（仅在lease/bundle接口需要时）
- `internal/artifact/workflow/executor.go` 及 tests
- `internal/retrieval/application/{search,vector_builder}.go` 及 tests
- `internal/retrieval/adapter/river/worker.go` 及 tests
- 相关 Embedding Contract/domain tests

### Composition, HTTP, OpenAPI and Web

- `cmd/api/main.go`
- `cmd/api/model_runtime_gate.go`
- `cmd/worker/main.go`
- `cmd/worker/model_runtime_drain.go`
- `cmd/modelctl/**` 与根 `zhixu`（仅保留/调整运维恢复语义时）
- `internal/modelsettings/http/{handler,handler_test}.go`
- `internal/app/{router,model_settings_router_test}.go`
- `internal/auth/http/handler_test.go`
- `api/openapi/openapi.json` 与相关 checker/tests
- `web/src/api/model-settings.ts` 及 tests
- `web/src/features/settings/ModelSettingsPanel.tsx` 及 tests/styles
- 受控 model-settings browser smoke（优先复用现有fake model脚本/fixture，不连接用户真实Provider）

### Architecture and specs

- `docs/architecture/adr/0022-model-runtime-hot-activation.md` 与 ADR索引
- `docs/architecture/ai-runtime.md`
- `docs/architecture/system-design.md`
- `docs/operations.md`
- `docs/requirements.md`
- `.trellis/spec/backend/model-settings-runtime.md`
- `.trellis/spec/backend/database-guidelines.md`
- `.trellis/spec/backend/artifact-contract.md`
- `.trellis/spec/backend/quality-guidelines.md`
- `.trellis/spec/frontend/model-settings.md`
- `.trellis/spec/frontend/quality-guidelines.md`

预计文件是影响面地图，不授权无关重构。实施时以实际引用搜索为准，保留用户现有dirty改动并逐段合并。

## 3. Test Matrix

### Unit/race

- Domain phase/participant transition、invalid shape、commit-side recovery。
- RuntimeHost并发 Acquire/Arm/Activate/Release、refcount、exactly-once cleanup、candidate abort、historical build dedup和static revision 0。
- Coordinator idempotent Start/renew/prepare/arm/commit/finalize、pre/post-commit crash matrix。
- Workflow cutover race、exact delivery、higher delivery、business retry、pause/resume和human wait。
- Search/Reindex完整 Embedding Contract resolver、same-contract reuse、mismatch unavailable。
- Frontend strict codec、mutation sequence、polling/recovery、Secret lifecycle和可访问状态。

### Real PostgreSQL

- 00079 fresh/repeat/legacy upgrade/guarded Down/failed rollback。
- concurrent Save/Start、same-target replay、different revision conflict、participant CAS和stale owner takeover。
- 两role armed单次commit、ack/finalize、coordinator response loss与post-commit adoption。
- Claim/commit高并发：每个 Attempt完全old或new，零半binding、零重复provider execution。
- Embedding/Index维度/Contract隔离与历史 Reindex恢复。

### HTTP/OpenAPI/frontend

- Session-only、Bearer forbidden、Origin/CSRF、method matrix、no-store。
- 202/409/503、安全Problem、unknown字段和operation/role projection。
- Save-and-Apply中间失败、刷新重挂、轮询停止、focus/network恢复和degraded显示。
- DOM/Network/Storage/log扫描不包含Secret、完整Endpoint、Authorization或instance ID。

### End-to-end no-restart proof

使用disposable数据库/Workspace和受控loopback fake model：

1. 记录 `docker compose ps -q app worker` 对应容器ID和 `docker inspect .State.StartedAt`。
2. 创建/保存一个target revision，启动activation并观察 preparing -> arming -> activating -> idle。
3. 在旧generation上阻塞一个Chat/Workflow操作，commit后启动新操作；断言旧操作仍旧revision、新操作target revision。
4. 再次读取容器ID/StartedAt，必须与步骤1完全相同。
5. 注入API或Worker candidate probe失败，断言active/applied保持previous且旧能力可用。
6. 刷新浏览器并以1440x900、390x844检查状态、按钮、无溢出、零console/network错误。

不得访问用户真实OpenAI/Ollama，不执行 `down -v`、`reset`、Docker daemon restart或宽泛资源删除。

## 4. Validation Commands

实施 agent应先按改动范围运行短门禁，再执行最终集合；所有后端测试默认 `-timeout 60s`，真实数据库测试串行 `-p 1`。

```bash
go test -race -count=1 -timeout 60s ./internal/modelsettings/...
go test -race -count=1 -timeout 60s ./internal/workflow/... ./internal/retrieval/... ./internal/agent/... ./internal/artifact/...
go test -race -count=1 -timeout 60s ./cmd/api ./cmd/worker ./cmd/modelctl
go vet ./internal/modelsettings/... ./internal/workflow/... ./internal/retrieval/... ./internal/agent/... ./internal/artifact/... ./cmd/api ./cmd/worker ./cmd/modelctl

ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" \
  go test -race -tags=integration -count=1 -p 1 -timeout 60s \
  ./internal/modelsettings/adapter/postgres ./internal/workflow/adapter/postgres ./internal/platform/migration

make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
go mod tidy -diff
git diff --check
```

若组合package超过120秒，按新增测试名和受影响package拆分执行并记录未覆盖范围，不静默扩大到全仓长任务。最终独立check agent根据实际diff决定是否追加 `make test`、Compose contracts或更广integration gate。

## 5. Review Gates

- `go-review`：Go API契约、context/cancel、goroutine、atomic/RWMutex、lease释放、ownership、race和错误链。
- `sql-code-review`：参数化、锁顺序、CAS、trigger/constraint、Down guard、tenant/workspace scope、事务与并发。
- 前端/通用 review：strict decoder、React Query owner、Abort、Secret、可访问性和desktop/mobile状态。
- `trellis-check`：PRD/Design/Spec一致性、跨层数据流、测试证据、无容器重启验收和dirty worktree保护。

Review发现当前范围内确定缺陷时先修复再重跑门禁。业务语义超出PRD时停止并回到规划，不在实现中自行扩scope。

## 6. Rollback Points

- Phase A migration未部署：可直接回退code/doc diff，不改数据。
- Migration已部署但无participant历史：Down只在guard通过时恢复legacy shape。
- 已产生activation历史：禁止强制Down；保留schema/data并用forward fix。
- pre-commit operation故障：转failed、清candidate、恢复old admission。
- post-commit故障：保持target active并fail-forward；不得把active SQL手改回previous。
- 需要业务回到旧模型：读取旧settings值，保存为新的immutable revision并正常Apply。
- 所有回滚均保留PostgreSQL volume、Secret key、Workspace、revision、Attempt、ModelRun、Embedding和Index历史。

## 7. Before `task.py start`

- `prd.md`无Open Questions，主“保存并应用”+次“仅保存”已由用户确认。
- `design.md`/`implement.md`、research和implement/check manifests完整且 `task.py validate`通过。
- 用户审阅并在本规划摘要之后明确批准开始实现。
- 主会话记录当前dirty worktree；实施sub-agent按阶段分派，不覆盖并行任务或用户改动。
- 不在本规划turn运行 `task.py start`、不修改product code、不启动数据库migration或容器。
