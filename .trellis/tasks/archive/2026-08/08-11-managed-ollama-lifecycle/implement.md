# 实施计划

## 1. 执行顺序与依赖

本任务是跨数据库、Go runtime、Compose、launcher、HTTP/OpenAPI 和前端的高风险变更。按以下阶段顺序实施；后续阶段不得绕过前置契约和验证。实施前重新确认最新 migration 编号，当前工作区时点下一号预计为 `00080`。

### Phase A：兼容性原型与架构锁定

依赖：无。该阶段只在 disposable volume/project 上验证，不接触用户现有容器或卷。

1. 选择并 pin Ollama image tag/digest，验证 Chat `/v1/chat/completions`、Embedding `/api/embed`、`/api/tags`、`/api/show`、流式 `/api/pull` 和 shutdown 行为。
2. 用旧 `0.9.6` 模型卷的只读副本验证目标版本存储兼容；记录 model name/digest/size。失败时调整目标版本/迁移策略，不在原卷试验。
3. 原型验证Docker `init: true` + direct-entrypoint supervisor启动/停止 `ollama serve`：先TERM serve PID并等待Ollama主动卸载runner，grace超时才PGID KILL，覆盖exactly-once Wait/reap、stale child generation、child crash与重复Ensure。
4. 验证管理容器non-root HOME/volume-init/chown、same-UID child clean-env allowlist、capability/read-only rootfs可行性；all-online连续60秒证明无serve/runner且idle process/cgroup anonymous RSS不高于64 MiB并相对基线下降至少80%。
5. 在 Docker Desktop和可用Linux环境验证 app/worker anchor namespace relay通过 external network DNS alias访问管理容器，且无host/LAN published port。
6. 新增/修订 ADR 草案，明确为何采用主Compose supervisor而不是host agent、launcher-only profile或Docker Socket controller；Phase A的真实结果必须回填ADR和最终compose契约。

阻断条件：目标镜像无法安全停止全部runner、旧卷副本无法形成可回滚迁移、relay网络不可达或manager空闲仍保留Ollama级内存时，不进入Phase B。

### Phase B：Provider、领域模型与持久生命周期契约

依赖：Phase A锁定 Ollama协议和镜像能力。

7. 新增显式 Chat provider=`ollama` 并原子更新 domain validation、forward migration、audit、Secret AAD/clear规则、OpenAPI与前端provider union；新本地Chat固定Relay、无Key、`chat_completions`。历史 `openai-compatible + exact Relay` 只兼容读取，不改写append-only revision。
8. 集中实现并测试 `RequiresManagedOllama(settings)`，返回 canonical requested model set/hash；operation另存resolved manifest digest/size并以二者绑定ready。覆盖neither、Chat-only、Embedding-only、both、legacy local Chat、`latest`本地稳定副本与零后台repull。
9. 新增 forward migration，建立 managed local runtime ownership/status、generation/test hold及async preparation operation/progress。用constraint/trigger锁定owner shape、DB-time lease、phase/error组合、正revision和model ref边界。
10. 建立supervisor专用最小权限PostgreSQL role/view/function与独立credential-init/secret-file volume交付、轮换和reset契约；manager不能读取model settings Secret、业务表或使用app广权限密码。
11. 增加 application ports 与PostgreSQL adapters：exact requirement CAS、hold acquire/renew/release、manager freshness、preparation start/adopt/progress/terminal与Snapshot投影。外部副作用不得在数据库事务或行锁内执行。
12. 更新Snapshot capability推导和HTTP strict DTO：local active只有在API/Worker serving与local runtime fresh ready同时满足时才available；不得改写desired/active/applied事实。

### Phase C：`local-model-runtime` 管理器与 Compose 集成

依赖：Phase A、Phase B的状态/port契约。

13. 新增小型管理器binary/package，封装固定child executable、进程组、stale-generation fence、状态机、singleflight Ensure、health/tags/pull、progress、stop与crash recovery；禁止shell和外部argv拼接，child固定`OLLAMA_NOPRUNE=true`且不继承manager环境。
14. 在manager内提供受限推理proxy：网络端口只允许批准的Chat/Embedding method/path/body；`ollama serve`只绑定loopback child port，pull/show/tags不对其他容器开放。
15. 构建专用pinned runtime image：以批准的official Ollama tag/digest为final（保留runner libraries），复制静态manager；不得把单个Ollama binary复制到当前Alpine image。只挂受管理模型卷与独立DB credential file。
16. 在主 `deploy/compose.yml` 增加 `local-model-runtime`、root one-shot model-volume-init、credential-init及project-owned model/credential volumes；manager固定`init:true`、direct entrypoint、restart、stop signal/grace、non-root HOME，无host port、Docker Socket、Workspace、model-secret、privileged或device。
17. 把app/worker loopback relay上游从host Ollama改为fixed supervisor proxy alias；保持relay listener和基础app/worker ready独立于child ready。
18. 扩展Compose/launcher静态与实际shape检查：service/image/digest/user/restart/network/volume/mount/port/capability/security opts；任何额外mount/port/device或Docker Socket均fail closed。
19. 增加manager控制循环/adapter，将持久requirement version与child状态幂等调和；DB或控制面不确定时fail safe保留已运行child，不把未知当空需求。
20. 更新`deploy/compose.static-models.yml`与rendered contract：base manager固定部署枚举`managed`，static overlay设为不可由Settings伪造的`external-static`并恢复`host.docker.internal:11434`。static manager不连/claim lifecycle DB、不启动child、不pull；managed模式才使用manager DNS proxy。

补充启动恢复项（2026-08-14 已实现）：为同一路径的 Workspace 物理替换增加显式
`workspace rebind --confirm REBIND`。保持 Workspace ID 不变，以 migration `00081` 在单事务内写 binding history、Registry
generation 和统一 Audit，并围栏旧 Runtime；只有 control/gate 归零、Runtime unavailable、无 Git capture checkpoint 时允许执行。
提交后复用普通 switch 发放新 grant，launcher 对 selection 写入前后的响应丢失按精确 history/Audit 前向恢复，禁止隐式接受新 root。

### Phase D：自动准备、Test 与热激活集成

依赖：Phase B、Phase C。

21. 本地Connection Test改为完整持久async operation：Idempotency-Key绑定draft hash，manager start/pull/verify，API侧lease runner执行生产Adapter Probe并持久化terminal result，最后释放test hold；同key同hash join、不同hash conflict，新Test draft自动supersede同Session/target旧Test；远程test保持现有同步路径。
22. 为API/Worker generation factory加入local hold：Build前acquire并等待exact requirement ready；Build/Probe失败exactly-once释放。participant prepared继续只在完整production generation成功后写入。
23. 在preparing/arming防御性校验target requirement ready/fresh；Commit事务不执行外部调用，manager不成为第三个rollout participant。
24. 把active/candidate/retiring/historical generation与hold生命周期对齐；local historical generation零holder时立即evict/release，未来exact acquire先ensure再build。
25. 实现local -> online停止栅栏：Finalize后仍等previous generation holds归零；stop失败不回滚online activation，只更新独立local runtime错误并重试。
26. 冻结preparation预算与调度：每attempt默认5分钟无进度、最多3次自动重试、operation最长6小时；active recovery > activation > test，相同模型共享、不同模型串行。terminal/superseded/TTL exactly-once释放operation hold，Retry创建新operation。
27. 覆盖manager/container restart、child crash、DB outage、stale ownership、hold TTL清理、new-hold-vs-stop竞态和response-loss恢复。

### Phase E：HTTP、OpenAPI 与 Settings UX

依赖：Phase B、Phase D确定最终wire。

28. 扩展现有Settings Snapshot或增加同源preparation projection，严格表达desired_required、active_required/ready、manager fresh、operation phase/target/progress/error/retryability；不暴露Docker细节或完整inventory。
29. 为本地test/preparation提供202/adopt/poll语义；Session-only、Origin/CSRF、no-store，operation只绑定exact draft hash/revision，不携带Secret/Endpoint到轮询资源。
30. 更新`web/src/api/model-settings.ts` strict encoder/decoder、OpenAPI schema/checker和所有fixtures；未知/缺失/非法phase-progress组合fail closed。
31. Settings两个provider均增加“本地 Ollama”；本地模式隐藏可编辑URL和Key，显示“本机（系统管理）”。desired/active/applied与local runtime状态分开展示。
32. 展示starting/pulling/ready/stopping/failed/unavailable/superseded、逐模型有界进度和稳定修复文案；旧active仍服务与target准备中可同时表达。
33. polling覆盖rollout idle但local operation transitional的场景；刷新、聚焦、网络恢复和响应丢失按权威operation恢复，不偷焦点、不显示保存/生效假成功。

### Phase F：Legacy container/volume 一次性迁移

依赖：Phase A镜像兼容结论、Phase C受管理service/volume可用、Phase D production Probe可调用。

34. 在宿主launcher增加exact legacy检测和只读状态报告；无确认时不stop/remove/copy，给出明确迁移命令/确认流程。
35. 实现固定one-shot copy helper：旧卷read-only、新卷write-only、无network/privileged/Socket/host bind；命令、卷名和image均来自编译时allowlist。
36. 迁移顺序固定为shape校验、source fingerprint/tags快照、`statfs`空间检查 -> 显式确认 -> stop legacy -> 创建空managed volume -> copy -> completion marker -> 同版本或已证明兼容pin的tags/digest/size与production Probe -> cutover。非空无匹配marker不盲目merge。
37. 成功后只移除legacy container并保留legacy source volume；旧卷清理是独立显式操作。失败时保留source、停止新runtime并恢复legacy；rollback失败则保留两卷并fail closed。
38. 更新`./zhixu up|restart|status|down|reset`：正常down保留managed与legacy rollback卷；reset仅删除通过ownership证明的managed model/credential volume，不能顺带删除legacy source。

### Phase G：文档、规格与交付门禁

依赖：所有实现阶段完成且行为由测试证实。

39. 最终化ADR、architecture index和system design，保留本任务三层结构图并解释控制面/计算面/数据面、内存/磁盘、窄proxy和取舍；明确修订ADR-0022常驻服务条款。
40. 更新`README.md`的日常Save/Apply与down/reset数据保留、`docs/user-guide.md`的本地Ollama/Save-only/Apply/状态、`docs/operations.md`的migration/status/rollback，以及`docs/requirements.md`的验收；检查四处不再保留旧restart/host-Ollama心智模型。
41. 用`trellis-update-spec`更新backend/frontend model-settings、Compose/launcher安全、migration与质量门禁；只记录已由代码/测试证明的事实。
42. 完成unit/race、真实PostgreSQL、OpenAPI、frontend、Compose contract、真实浏览器和disposable legacy迁移smoke。
43. 依次执行`go-review`、`sql-code-review`、前端/通用`code-review-and-quality`和独立`trellis-check`；修复后重跑受影响门禁。

## 2. Expected File Surface

实施时以实际引用搜索为准，以下仅是影响面地图，不授权无关重构。

### Database/domain/application/runtime

- `migrations/00080_*.sql`（实施前重新分配下一空闲编号）
- `internal/modelsettings/domain/{settings,rollout,errors}.go`
- `internal/modelsettings/application/{ports,service}.go`
- `internal/modelsettings/adapter/postgres/**`
- `internal/modelsettings/runtime/{models,models_host,host,hot_controller}.go`
- `cmd/api/**`、`cmd/worker/**`
- 新的manager composition/package与测试（名称按现有分层收敛）

### Provider/HTTP/OpenAPI/Web

- `internal/platform/models/**`
- `internal/modelsettings/http/**`
- `internal/app/**` route/policy tests
- `api/openapi/{openapi.json,check.mjs}`
- `web/src/api/model-settings.ts`及测试
- `web/src/features/settings/ModelSettingsPanel*`及测试
- `web/e2e/model-settings-*.spec.ts`

### Compose/launcher/migration

- 专用local-runtime Dockerfile（official Ollama pinned base）
- `deploy/compose.yml`、`deploy/compose.static-models.yml`
- `deploy/compose_runtime_check.py`、`deploy/compose_runtime_contract.py`及tests
- 根`zhixu` launcher及其契约tests
- fixed model-volume-init、credential-init与legacy copy helper资产/脚本（优先复用现有image/toolchain，不接受动态shell）

### Docs/specs

- `README.md`、`docs/user-guide.md`
- 新ADR及`docs/architecture/adr/README.md`
- `docs/architecture/{ai-runtime,system-design}.md`
- `docs/{operations,requirements}.md`
- `.trellis/spec/backend/model-settings-runtime.md`
- `.trellis/spec/frontend/model-settings.md`
- 相关Compose/launcher/database/quality specs

## 3. Test Matrix

### Unit/race

- `RequiresManagedOllama` provider/model truth table及legacy exact Relay。
- manager state machine、process-group/reap、singleflight、child crash、start/stop race、pull resume/progress bounds。
- preparation attempt no-progress/重试/总时限、same-key join/conflict、Test supersede、activation non-supersede、priority/serial pull和terminal hold cleanup。
- requested model ref + resolved digest set binding；`latest`已验证本地副本不后台漂移/repull。
- hold acquire/renew/release、TTL、requirement CAS、manager ownership/freshness、stop fence和exactly-once cleanup。
- RuntimeHost candidate/active/retiring/historical与local hold对齐。
- frontend strict state/progress/error matrix、polling/recovery、focus和Secret生命周期。

### Real PostgreSQL

- fresh/repeat/upgrade/guarded Down migration。
- concurrent Save/Test/Activation/hold、same-operation adoption、stale takeover、DB-time lease和CAS。
- online->local precommit failure保持previous；local->online finalize后等最后hold才stop。
- manager/Coordinatorresponse loss与post-commit fail-forward。
- Workspace root rebind 的漏 Audit deferred rollback、history/Audit 同事务、响应丢失幂等重放、Git checkpoint 拒绝、旧 Runtime heartbeat/register 围栏与 append-only/guarded Down。

### Compose/security

- all-online：manager在、`ollama serve`/runner不在，API/Worker正常；60秒idle process/cgroup anonymous RSS不高于64 MiB且相对基线下降至少80%，raw Docker total仅辅助。
- Chat-only、Embedding-only、both-local只出现一个child，模型集合去重。
- 无host/LAN 11434、无Docker Socket/Workspace/secret mount、无额外device/capability。
- Docker/container restart与manager/child crash自动收敛；remote稳态不误启动。
- launcher 的 stale `workspace-rebind` mutation lock 回收、数据库提交后 selection 写入丢失恢复、binding generation 错配和 Runtime 未就绪时不提交 selection。
- managed relay使用manager DNS窄proxy；static overlay恢复host upstream；proxy拒绝pull/delete/create/push/pprof和非批准path。
- `external-static` manager不连接/claim lifecycle DB、不启动child且零pull；static local smoke只命中host Ollama。
- manager/child同一non-root UID但child环境无DB/model Secret；volume-init/credential-init均无network/Socket/Workspace且只操作精确卷。

### Legacy migration

- exact legacy shape成功复制与Probe；source只读且成功后保留。
- foreign同名、额外mount/port/device、其他container引用、磁盘不足、copy中断、版本不兼容和Probe失败全部fail closed。
- rollback恢复旧runtime；rollback失败保留两卷且不删除source。
- 普通down/reset不删除legacy source；managed model/credential volume仅在显式确认与ownership通过后删除。
- destination为空/匹配completion marker可幂等执行；非空无匹配marker、空间不足或source fingerprint变化均fail closed。

### Browser

- 1440x900与390x844覆盖local provider选择、Save-only、Test自动pull、Apply、刷新恢复、失败修复、switch all-online。
- desired/active/API/Worker applied与local runtime状态可区分；无横向溢出、焦点抢夺、console error或Secret/Endpoint/Docker细节泄漏。
- 全流程记录API/Worker container ID、StartedAt与RestartCount不变。

### Documentation consistency

- PRD/ADR/system design保留同一三层结构和理由；README/user guide只使用产品语言，operations记录launcher migration/rollback。
- README、user guide和operations不再要求正常模型Apply执行`./zhixu restart`，也不把外部host Ollama描述为managed默认路径。
- down/reset、managed model volume、legacy rollback source和镜像磁盘/空闲内存区别在所有文档中一致。

## 4. Validation Commands

根据改动范围先运行短门禁，再执行最终集合；Go测试默认`-timeout 60s`，真实数据库测试串行`-p 1`。

```bash
go test -race -count=1 -timeout 60s ./internal/modelsettings/... ./internal/platform/models/...
go test -race -count=1 -timeout 60s ./cmd/api ./cmd/worker ./cmd/<local-runtime>
go vet ./internal/modelsettings/... ./internal/platform/models/... ./cmd/api ./cmd/worker ./cmd/<local-runtime>

ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" \
  go test -race -tags=integration -count=1 -p 1 -timeout 60s \
  ./internal/modelsettings/adapter/postgres ./internal/platform/migration

make openapi-check
make compose-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
go mod tidy -diff
git diff --check
```

真实Compose、浏览器与legacy迁移smoke必须使用disposable project/volume或经过用户明确授权的精确legacy资源；不得在普通测试中执行`down -v`、全局prune、daemon restart或宽泛删除。

### 4.1 2026-08-14 已执行的必要验收

- 直接运行 `./deploy/managed-ollama-compose-smoke.sh`，隔离 project 中的受控 HTTPS 线上 fixture、Chat 本地、Embedding 本地、
  两者本地、切回受控线上 fixture 全部通过；本地始终一个 child，最终线上模式无 child。fixture 只证明受控 TLS/Compose
  网络和切换，不代表公网 Provider 出口。
- 同一脚本完成管理容器重启、模型 identity/卷复用和零新增 pull 验证；重启后恢复唯一 child，durable `child_epoch` 单调递增。
- 全线上连续 60 秒采样：anonymous RSS 最大 6.39 MiB，manager process RSS 最大 9.69 MiB，相对 700 MiB 基线下降
  99.1%，达到 PRD 数值门槛。
- 本轮按用户“开发阶段无历史数据”的明确授权，精确删除旧 standalone 容器 `zhixu-eino-live-ollama`、卷
  `zhixu-eino-live-models` 和镜像 `ollama/ollama:0.9.6`；保留新的 managed model volume 与全部 PostgreSQL 卷。该操作不是
  production legacy migration 测试，也不修改 Phase F 的保留/回滚契约。
- 主项目 `./zhixu restart` 成功恢复健康，但 desired revision 6 的全线上 Apply 因容器到远程 Provider 的 TLS 连接被重置而在
  commit 前失败（`MODEL_CHAT_REQUEST_FAILED`）；active revision 2（原 disabled 配置）保持不变，本地 runtime stopped。恢复出口后需要重新 Apply，
  不能把 restart 当成应用 pending desired。

以下扩展矩阵未执行；2026-09-08 按用户要求移出开发交付门禁，保留支持边界说明：原生 Linux、真实公网 Provider 出口、Docker daemon restart、显式强制 child crash/signal/reap、
浏览器闭环、旧 generation 在途停止栅栏、真实 `0.9.6 -> 0.32.9` legacy migration 和多架构验收。

## 5. Review Gates

- `go-review`：context/cancel、goroutine、child process group、signal/reap、lease、CAS、race、HTTP transport和错误链。
- `sql-code-review`：migration shape、constraint/trigger、锁序、DB-time、参数化、CAS、least privilege和Down guard。
- `code-review-and-quality`：OpenAPI/strict client、React Query owner、polling/Abort、可访问性、Compose/launcher安全与跨层一致性。
- `trellis-check`：PRD/Design/Spec一致、manifest完整、真实Compose/迁移证据、dirty worktree保护和无容器重启验收。

## 6. Rollback Points

- Phase A失败：不接触产品数据，调整设计或停止任务。
- schema已部署但未产生runtime/hold/preparation数据：仅在guard通过时允许Down；否则forward fix。
- online->local precommit失败：释放target holds、保留previous active/applied和旧runtime。
- active已提交local：只能修复manager/child并向前完成，不自动回滚。
- local->online已提交：online activation保持成功；stop失败独立重试，不反向切换settings。
- legacy迁移copy/verify失败：source volume不变，恢复legacy；不得删除source。
- Workspace root rebind 一旦 history/Audit 提交只允许向前完成普通 switch；不得回写旧 fingerprint、删除 history 或复制成新 Workspace。存在 Git capture checkpoint 时保持 fail-closed，等待独立 rebaseline 设计。
- 需要业务恢复旧模型配置：保存旧值为新的immutable revision并正常Apply，不手改active指针。

## 7. Before `task.py start`

- `prd.md`无Open Questions并完成lossless convergence pass。
- `design.md`、`implement.md`已吸收最终research和用户确认的三层结构/legacy迁移。
- `implement.jsonl`与`check.jsonl`均包含真实spec/research entries并通过task validator。
- 用户审阅本轮最终规划摘要，并在后续消息中明确批准开始实现。
- 主会话重新记录dirty worktree和并行任务状态；实施agent不得覆盖用户或其他任务改动。
- 本规划turn不运行`task.py start`、不执行migration、不启停容器、不操作legacy数据。
