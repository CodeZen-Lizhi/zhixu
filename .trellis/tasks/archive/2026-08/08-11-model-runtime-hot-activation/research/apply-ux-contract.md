# Research: Model Settings 保存与应用 UX 契约

- Query: 追踪当前 Model Settings HTTP/OpenAPI/前端 wire、mutation、轮询/回查、rollout 展示、认证/Secret 处理和测试，并比较（A）保存后自动应用与（B）保存后显式应用的可观察契约。
- Scope: internal
- Date: 2026-08-11

## Findings

### 1. 当前事实：HTTP 与 OpenAPI 只有“读取 / 保存 / 连接测试”

- Handler 只注册 `GET /settings/models`、`PUT /settings/models`、`POST /settings/models/test`，不存在启动应用、取消应用或重试应用的 HTTP mutation（`internal/modelsettings/http/handler.go:71-76`）。OpenAPI 同样只有 Settings GET/PUT（`api/openapi/openapi.json:111-228`）与连接测试 POST（`api/openapi/openapi.json:229-304`）。
- HTTP 面向的 `SettingsManager` 只有 `Snapshot`、`Save`、`Test` 三个方法（`internal/modelsettings/application/ports.go:186-192`）。rollout coordinator 目前是 `modelctl` 的内部边界，不是浏览器可调用能力。
- PUT 的 OpenAPI 描述明确声明：只创建新的 immutable desired revision，保存不改变 active，当前由 `./zhixu restart` 执行 rollout（`api/openapi/openapi.json:156-185`）。因此页面保存成功后仍显示“已关闭”或旧模型，是当前 wire 的明示语义，不是前端漏刷新。
- GET/PUT 返回完整非 Secret 快照：`desired_revision`、`active_revision`、两份 settings summary、API/Worker runtime、rollout、`restart_required`、capabilities（`api/openapi/openapi.json:11993-12036`；`internal/modelsettings/http/handler.go:115-172`）。API Key 只返回 `api_key_configured` 布尔值（`api/openapi/openapi.json:12102-12105`）。
- rollout 对外只有 phase、target revision、最后错误码和 retryable；没有公开的 operation ID、state version、每个角色的候选准备状态、取消能力或起止时间（`api/openapi/openapi.json:12326-12425`）。runtime 每个角色也只有一个 `applied_revision / phase / fresh` 槽位（`api/openapi/openapi.json:12281-12325`）。

### 2. 当前保存 mutation 的原子性、冲突和 rollout 锁

- Handler 在解码 Secret 前先读取 Snapshot 并比较 `expected_revision`，过期直接 409（`internal/modelsettings/http/handler.go:199-229`）；随后调用 Manager Save，成功响应仍是权威 Snapshot（`internal/modelsettings/http/handler.go:240-251`）。
- Service 的 Save 再次检查 desired revision，校验 projected Secret 状态，并只调用 `SaveDesired`；注释明确“never changes active”（`internal/modelsettings/application/service.go:87-115`）。
- PostgreSQL SaveDesired 在 singleton state 行锁内拒绝 active rollout、检查 expected revision、追加 immutable revision、推进 desired、写脱敏 Audit，并在同事务中生成响应 Snapshot（`internal/modelsettings/adapter/postgres/revision.go:110-198`）。当前保存事务没有创建/排队 activation，因此在 PUT 返回后再“顺手启动”自动应用会产生保存成功但 activation 未创建的 crash window。
- rollout 进行中保存会返回 `MODEL_SETTINGS_ROLLOUT_IN_PROGRESS`（`internal/modelsettings/adapter/postgres/revision.go:127-135`；稳定错误码见 `internal/modelsettings/domain/errors.go:5-20`）。数据库还禁止 active rollout 期间改变 desired（`migrations/00064_model_settings.sql:175-181`）。
- BeginRollout 会在行锁内把“当时的 current desired”冻结为 target，并允许从 idle 或 failed 开始；已有未过期 rollout 时拒绝（`internal/modelsettings/adapter/postgres/rollout.go:14-60`）。它目前不接受 `expected target revision`，所以不能直接证明它应用的是刚才 PUT 创建的 revision；新的浏览器应用契约需要增加 exact-target CAS。
- Commit 只在 verifying、lease 未过期且 API/Worker 均 fresh、绑定同一 rollout/target、处于 prepared/verifying 后，原子推进 active 并切 runtime 为 active（`internal/modelsettings/adapter/postgres/rollout.go:151-198`）。失败会保留 active、恢复旧 runtime 或将候选标 unavailable（`internal/modelsettings/adapter/postgres/rollout.go:120-149,236-250`）。
- stale lease 可以恢复为 failed，随后允许重新 begin（`internal/modelsettings/adapter/postgres/rollout.go:201-233`）。但当前恢复入口由 `modelctl recover --stale` 显式调用（`cmd/modelctl/control.go:159-205`），没有 HTTP/API 内常驻协调者保证页面不依赖 launcher 恢复。

### 3. 当前前端 wire、刷新和状态展示

- 唯一 wire owner 严格解码 exact-key response，并验证 rollout shape、revision 单调关系、`restart_required` 派生值和 capability 一致性；任何新增字段都会被旧客户端拒绝（`web/src/api/model-settings.ts:482-542`）。因此将 `restart_required` 改成 activation 语义必须原子更新 OpenAPI、Handler 与前端 decoder，不能只增加一个兼容字段。
- API client 的 GET 使用 `cache: no-store`；PUT 编码 `expected_revision` 与两个 draft；连接测试只发送目标 draft（`web/src/api/model-settings.ts:758-805`）。所有响应还必须带 `Cache-Control: no-store`，否则 fail closed（`web/src/api/model-settings.ts:721-754`）。
- React Query 首次 GET 不自动 retry；只有 query 当前数据已经是 non-terminal rollout 时，才每 2 秒 refetch（`web/src/features/settings/ModelSettingsPanel.tsx:344-351`）。这意味着：
  - 显式 Apply mutation 必须把 non-terminal activation Snapshot 写入 query cache，或主动 invalidate/refetch，才能启动现有轮询。
  - 若自动应用在 PUT 返回后异步创建，而 PUT response 仍是 idle，则当前页面不会轮询，除非用户手动刷新。
- 保存成功直接用 PUT response 更新 Query cache、重新从 desired 初始化 draft、清理 test 结果（`web/src/features/settings/ModelSettingsPanel.tsx:442-457`）。Secret 输入由新 draft 初始化为空；页面测试覆盖 mutation/query cache、localStorage、sessionStorage 都不保留本次明文（`web/src/features/settings/ModelSettingsPanel.test.tsx:271-316`）。
- 409 后会权威 refetch，保留本地非 Secret 草稿、推进 draft 的 expected revision 并清空 Secret value；若回查低于服务端提示 revision，也不会把旧缓存冒充恢复成功（`web/src/features/settings/ModelSettingsPanel.tsx:388-407`；`web/src/features/settings/ModelSettingsPanel.test.tsx:352-475`）。
- 组件卸载会 abort 所有保存/测试请求（`web/src/features/settings/ModelSettingsPanel.tsx:422-426`），且有测试（`web/src/features/settings/ModelSettingsPanel.test.tsx:511-532`）。但网络 abort 不证明服务端 mutation 未提交；未来 activation 必须由 GET Snapshot 恢复真实结果，不能把 AbortError 渲染为“已取消应用”。
- 页面分别展示 desired、active、API applied 和 Worker applied（`web/src/features/settings/ModelSettingsPanel.tsx:518-523`），并对 active settings 单独渲染“与待应用配置不同”（`web/src/features/settings/ModelSettingsPanel.tsx:265-276`）。能力徽标来自 active capability，所以 desired 已启用而 active 仍 disabled 时显示“已关闭”是正确运行态（`web/src/features/settings/ModelSettingsPanel.tsx:83-90,532-562`）。
- 当前待应用 banner 仍要求执行 `./zhixu restart`（`web/src/features/settings/ModelSettingsPanel.tsx:525`）；进行中 rollout 锁定保存和测试，失败则明确旧 active 保持并重新开放编辑（`web/src/features/settings/ModelSettingsPanel.tsx:490-528`）。保存成功反馈也写死“等待 restart 应用”（`web/src/features/settings/ModelSettingsPanel.tsx:566-572`）。
- rollout UI 已有 validating/draining/applying/verifying/failed 标签（`web/src/features/settings/ModelSettingsPanel.tsx:140-147`），但只有总体 phase；无法满足“API/Worker 分别展示候选准备状态”的新验收要求。

### 4. 当前认证与 Secret 边界

- OpenAPI 要求 sessionCookie，PUT/Test 还要求 Origin 与 CSRF headers；Bearer API Token 不能访问系统 Secret 管理端点（`api/openapi/openapi.json:111-171,229-245`）。路由层认证后，Model Settings Handler 再次要求 principal kind 为 Session，Bearer 即使有 capability 也返回 403（`internal/modelsettings/http/handler.go:321-330`）。Router 集成测试覆盖匿名、Bearer、错误 Origin、缺失 CSRF 和 no-store（`internal/app/model_settings_router_test.go:59-139`）。未来 Apply/Cancel 必须进入同一 session-only、Origin/CSRF、no-store 边界。
- 浏览器 `authFetch` 对 unsafe method 自动附加当前 CSRF token、携带同源 Cookie，并在本地 API 401 时失效 Session（`web/src/api/auth.ts:245-264`）。Provider 的 401 则由连接测试映射为本地 502，避免误登出；该契约不应被 Apply preflight 破坏。
- Secret wire 是 `keep | replace | clear` tagged union，只有 replace 带 write-only value（`web/src/api/model-settings.ts:35-38,611-623`）。请求 encoder 把 Secret 与 Endpoint 纳入敏感值扫描，错误响应若回显任一值会 fail closed（`web/src/api/model-settings.ts:625-655,711-753`）。
- Handler 使用严格 JSON，解析后主动 clear raw buffers，并在完成 Save/Test 后 Destroy Secret（`internal/modelsettings/http/handler.go:205-246,485-520,793-821`）。Apply 应只携带 revision/operation CAS，从已经持久化的加密 revision 在服务端短暂解密；不得重新发送 Key、draft 或 Endpoint。

### 5. 两种方案共同必须满足的可观察不变量

1. **保存不等于生效。** 成功文案至少区分“已保存”“已开始应用”“已生效”；只有 `active == target` 且 API/Worker 正在服务该 revision 时才显示已生效。
2. **应用绑定 exact revision。** Apply 请求必须带用户观察到的 desired revision；服务端必须原子确认并冻结它。不得在异步 worker 启动时重新读取“最新 desired”并悄悄应用另一个版本。
3. **浏览器不是 coordinator。** 页面关闭、刷新、请求 timeout 或 AbortError 不取消已经持久化的 activation。恢复页面后 GET 必须能继续显示同一 operation。
4. **旧 active 在 prepare 失败时仍是能力事实。** 新候选准备期间，能力徽标应继续反映旧 serving runtime；候选失败不能把旧 capability 显示为 unavailable。当前 `capabilities` 由两个 role 都处于 active 才算 configured（`internal/modelsettings/adapter/postgres/snapshot.go:65-72,108-129`），与“旧 runtime 继续服务、候选并行准备”的新目标不兼容。
5. **Serving 与 candidate 分开。** 当前单个 `runtime.applied_revision/phase` 无法同时表达“版本 2 正在服务”和“版本 6 已准备”。新 read model 至少需要每个 role 的 serving revision/fresh，以及 target preparation phase/prepared revision，不能复用一个字段造成短暂假下线。
6. **失败/取消均保留 desired。** rollback 只恢复/保持 active，不删除 immutable desired revision，也不要求用户重新输入已经安全保存的 Secret。
7. **取消必须是服务端 CAS。** 只有 pre-commit operation 可取消；若取消与 commit 竞态且 commit 已胜出，返回“已经生效”，不能自动回滚。用户要回到旧配置时应保存一个新 revision 再应用。
8. **恢复必须自动收敛。** coordinator 中断或 lease stale 后，服务端常驻恢复逻辑必须把 operation 收敛到 terminal failed/cancelled 或继续执行；不能再次要求 `./zhixu restart`。
9. **所有错误脱敏。** UI 只显示稳定 error code、阶段、role、retryable 与有限操作建议；不返回 Secret、密文、完整原始 Provider body、instance ID 或内部 stack。

### 6. 方案 A：保存成功后自动应用

可接受的 A 不是“PUT 成功后浏览器再 best-effort 调一次 Apply”。它需要一个服务端原子命令，同时完成 append desired 与创建 exact-target activation，或用与保存同事务的 durable outbox 保证最终创建。否则进程在 Save commit 与 BeginRollout 之间退出时，用户看到保存成功却永远不应用。

#### A 的可观察契约

| 场景 | 必须结果 |
| --- | --- |
| 点击“保存并应用” | 请求 pending 文案只说“正在保存”；服务端持久化后返回 202/权威 Snapshot，页面变为“版本 N 已保存，正在应用”，不能直接说已生效。 |
| 保存校验或 optimistic revision 冲突 | 不创建 desired 或 activation；保留非 Secret draft、清空明文、显示 current revision 并要求核对。 |
| 保存已提交、后续 preflight/prepare 失败 | desired=N、active 仍为旧版本；展示 role/phase/脱敏错误，提供“重试应用”和“继续编辑”，无需重输已保存 Key。 |
| 重复点击/响应丢失后重试 | 同一幂等键或同一 exact target 返回现有 activation；不能创建两个并发 rollout。GET 可判定是否已提交。 |
| 用户取消 | 只取消 activation，不撤销已保存 desired；终态显示“版本 N 已保存，应用已取消”，之后可再次应用。 |
| 页面关闭/断网 | operation 继续；重开页面通过 GET 恢复并继续轮询。 |
| coordinator 中断 | lease/recovery 自动收敛，旧 active 保持；页面最终显示可重试失败或继续中的同一 operation。 |

#### A 的主要代价

- 每次保存都会触发生产运行态变化，失去 durable draft / 稍后统一应用的能力。
- 当前 `SaveDesired` 与 `BeginRollout` 是两个独立事务和边界；要做成真正可靠的一步语义，需要新增 combined command/outbox，而不仅是前端改按钮。
- PUT response 若在 activation 创建前仍显示 idle，当前“仅 non-terminal 时轮询”的策略不会继续观察；需要 202 response 直接包含 non-terminal activation，或增加 accepted-operation query。
- 自动开始后取消窗口很短，且长任务/Embedding contract 变化可能具有明显运维影响。失败虽能保持旧 active，但用户更容易在无意中触发昂贵 preflight/prepare。

### 7. 方案 B：显式“保存”后再“应用配置”

B 保留现有 PUT 的清晰语义，再新增一个只接受 exact desired revision 的 activation mutation。保存与应用仍是两个服务端事实，但页面不再要求容器 restart。

#### B 的可观察契约

| 场景 | 必须结果 |
| --- | --- |
| 本地 draft 未保存 | “保存”可用；“应用配置”禁用并提示先保存，不能应用未持久化字段或浏览器内 Secret。 |
| 保存成功 | desired 推进并清空 Secret；banner 显示“版本 N 已保存，尚未应用”，提供主操作“应用配置”。active capability 仍显示旧运行态。 |
| 点击“应用配置” | 发送仅含 `expected_revision=N` 的 Session/CSRF/no-store mutation；202 response 必须绑定 activation 与 target N，并立即进入轮询。 |
| 同 target 已在进行 | 幂等返回同一 activation；页面聚焦现有进度，不报假失败。不同 target/owner 冲突则 409 后权威 refetch。 |
| target 已 active 且两个 serving role 健康 | 返回 idempotent success，不创建无意义 operation。若 desired=active 但 runtime degraded，可显式提供“重新加载当前配置”，仍走同一安全 activation。 |
| preflight/prepare 失败 | active 和旧 serving runtime 保持；desired 保留；显示“旧版本仍在使用”，并开放“重试应用”和编辑/测试。 |
| 点击取消 | 对当前 activation 做 ID/version CAS；仅 pre-commit 可接受。取消完成后显示 saved-pending，不把 cancel 当红色系统故障。 |
| 页面关闭/断网 | 与 A 一样，operation 继续；重开后恢复。HTTP request abort 只停止本次等待。 |
| stale/recovery | 服务端自动恢复；failed 后重试创建新 activation ID，不能复用失去 lease 的 owner。 |

#### B 的主要代价

- 正常路径多一次显式操作。
- 页面必须清楚区分本地未保存、已保存待应用、正在应用、已生效，避免用户只保存后误以为完成。
- 若未来提供“保存并应用”快捷按钮，快捷按钮应组合两个已有语义并保留中间结果：Save 成功而 Apply 请求失败时，明确显示“已保存、未开始应用”，不能回滚或假装全部失败。

### 8. A / B 对比

| 维度 | A 保存后自动应用 | B 保存后显式应用 |
| --- | --- | --- |
| 正常点击数 | 1 | 2，可后续增加组合快捷操作 |
| durable draft | 不自然；保存即改变运行态 | 原生支持，与当前 desired/active 设计一致 |
| exact-target 原子性 | 需要 combined transaction 或 durable outbox | Apply 自带 expected revision CAS，边界更窄 |
| crash/响应丢失歧义 | 较高，必须证明 save 与 enqueue 不存在窗口 | 较低；GET 可分别证明 desired 与 activation |
| 无意触发运行态变化 | 较高 | 较低，用户明确确认应用 |
| retry/cancel 解释 | 容易混淆“取消保存”与“取消应用” | 保存已完成，retry/cancel 只作用于 activation |
| 与现有前端/后端契约适配 | 改动 Save 事务、OpenAPI 和反馈语义较大 | 保留 Save，新增 activation surface 和状态投影 |
| Secret 生命周期 | 可安全实现，但 combined command 更复杂 | Apply 不含 Secret，直接使用已保存 revision |
| 长任务/Embedding contract 变更 | 保存即触发，操作意图较弱 | 显式应用更符合有运行影响的动作 |

### 9. 推荐默认：B，保留“保存草稿”并新增“应用配置”

这是工程与 UX 的推荐默认，不替代最终产品选择。理由是它最贴合现有 desired/active 两阶段事实、Secret 保存边界和 optimistic revision；也能把“是否改变运行态”变成一次明确操作。用户要消除的是容器 restart，不是必须消除保存与应用之间的安全边界。

推荐的页面行为：

- 保留“保存模型设置”；当服务端返回新 desired 后，替换 restart banner 为“版本 N 已保存，尚未应用”，旁边提供明显的“应用配置”。
- 只有本地 draft 与服务端 desired 一致时允许 Apply。存在 unsaved edit 时 Apply 禁用，避免界面显示的字段与实际 target 不一致。
- Apply 进行中，继续展示旧 active 与“当前仍在服务”；另设 progress 区展示 target N、API 准备状态、Worker 准备状态和总体阶段。不要把候选 prepared 写成 capability 已生效。
- terminal success 的唯一判定是 desired/active/两个 role serving revision 一致且 fresh。旧 runtime 的后台退役可以单独显示，不应让已提交应用继续卡在“未生效”。
- terminal failure 显示“应用失败，当前仍使用版本 X”；retryable 控制“重试应用”，而“编辑配置”和“连接测试”由是否存在 active operation 决定。
- user cancel 使用中性终态“应用已取消，版本 N 仍已保存”；不删除 revision，不恢复 Secret 输入。
- 可在稳定后增加“保存并应用”快捷操作，但 canonical API 仍保持 Save 与 Activation 两个资源/命令，快捷操作失败时保留可观察的中间状态。

### 10. 推荐的最小公共 wire 能力

具体路径名仍需 design 决策，但可观察能力至少需要：

1. **Start activation mutation**：输入 `expected_revision`，不接收 settings/Secret；返回 202 和持久化 activation summary。重复请求对同 target 幂等。
2. **Cancel activation mutation**：输入/路径包含 opaque activation ID，并带 expected state/version；仅 pre-commit 成功。请求断开不等同取消。
3. **GET Snapshot 扩展**：将 `restart_required` 改为 activation 语义；分别表达 serving runtime 与 target preparation，并返回 activation ID/version、target、phase/outcome、每 role prepare phase、稳定 error code、retryable、can_cancel。
4. **Terminal outcome 区分**：`failed` 与 user `cancelled` 分开；当前 Handler 将所有 failed 都投影为 retryable=true（`internal/modelsettings/http/handler.go:632-642`），OpenAPI也强制 failed/retryable=true（`api/openapi/openapi.json:12405-12421`），不足以表达不可重试失败或用户取消。
5. **自动恢复 owner**：API/Worker 内常驻、数据库 lease/CAS 驱动的 coordinator/recovery；GET 只观察，不以浏览器轮询推进状态机。

候选 read model 的语义示例（不是最终字段命名）：

```text
desired revision N                         # 最后保存
active revision M                          # 全局已提交
runtime.api/worker.serving_revision M      # 当前服务
activation.target_revision N               # 本次目标
activation.api/worker.prepare_phase        # 候选准备
activation.phase/outcome/error/can_cancel  # 总体操作
activation_required                        # N != M 或 serving degraded
```

### 11. Polling、retry、conflict、cancel、recovery 的前端细化

- Start/Cancel mutation 成功后立即 `setQueryData` 或 invalidate；只要 activation non-terminal 就轮询。不能依赖“下一次偶然 GET”发现 operation。
- 轮询到 terminal 后停止，并做一次最终 refetch 校验 serving/active；页面重新获得焦点、网络恢复和手动刷新时也 refetch。
- 客户端等待超时只显示“仍在后台应用，正在重新获取状态”，不擅自把 operation 标 failed。
- GET 临时失败时保留最后一次标注时间的进度，但明确“状态暂不可确认”；恢复后以服务端为准。不要在未知时重新发 Start。
- Save 409 沿用当前“保留非 Secret draft + 清 Secret + 权威回查”。Apply 409 不需要保留 Secret，因为请求不含 Secret；显示 current desired/active operation并 refetch。
- 同一 running target 的重复 Start 返回现有 operation；different target 返回 409。failed/cancelled 后 retry 创建新 operation，便于审计与 lease owner 隔离。
- Cancel 与 Commit 竞态使用 operation ID + version CAS。若 commit 已完成，Cancel 返回 terminal active Snapshot；UI显示“已生效”，而不是失败或自动回滚。
- 浏览器 `AbortController` 只释放本地请求和瞬时输入；真正 Cancel 必须来自显式按钮的独立 server mutation。

### 12. 测试现状与必须补充的测试

已有覆盖：

- API strict decoder 覆盖 disabled bootstrap、desired/active/runtime 差异、非法 rollout/revision、no-store、Abort identity、write-only Secret 与 409 脱敏（`web/src/api/model-settings.test.ts:125-336`）。
- Component 覆盖 loading、desired/active/applied、测试成功/失败、Secret replace/clear、保存失败、409 合并、dirty draft 刷新、rollout 锁、failed rollout、unmount abort 与 GET retry（`web/src/features/settings/ModelSettingsPanel.test.tsx:116-540`）。
- HTTP/Router 覆盖 Session-only、CSRF、no-store、严格 request、Secret 生命周期、409 current revision、连接测试诊断（测试入口索引见 `internal/modelsettings/http/handler_test.go:60-671`；路由认证见 `internal/app/model_settings_router_test.go:59-139`）。
- PostgreSQL integration 覆盖 concurrent save、rollout begin/phase/commit、prepared 双角色门禁、abort、lease recovery、runtime stale 和 enqueue fence（`internal/modelsettings/adapter/postgres/repository_integration_test.go:29-469`）。

缺失/新增测试建议：

- API client：Start/Cancel exact encoder、202/terminal strict decoder、operation ID/version、role preparation、cancelled vs failed、Problem 不含 Secret/Endpoint/instance ID、Abort 不变成 cancel。
- Component：saved-pending 的 Apply CTA、unsaved draft 禁止 Apply、Start success 立即进入轮询、每 2 秒从 validating 到 success/failure/cancelled、页面重挂恢复、焦点/网络恢复 refetch、GET 短暂失败、重复点击幂等、Cancel/Commit race。
- 当前没有测试实际证明 `refetchInterval` 会轮询并在 terminal 停止；现有 rollout component test 只断言控件锁定（`web/src/features/settings/ModelSettingsPanel.test.tsx:477-509`）。这是上线前必须补的回归。
- Handler/Router/OpenAPI：新 mutation 的 Session-only、Origin/CSRF、no-store、method matrix、expected revision conflict、active-operation conflict、idempotent replay、cancel CAS、bounded redacted error；`make openapi-check`。
- Application/PostgreSQL：Save 与 Start 的边界按选择的 A/B 验证；exact target、same-target replay、different-target conflict、failed retry new ID、cancel before commit、cancel after commit、coordinator crash/lease expiry自动恢复、API/Worker 单边 prepare failure。
- Browser：真实页面保存 -> Apply，全程记录 API/Worker 容器 ID/StartedAt 不变；刷新/关闭/重开页面后 operation 继续；扫描 Network、DOM、console、Storage 无 Secret；桌面与 390x844 无溢出。

## Files Found

- `.trellis/spec/backend/model-settings-runtime.md` - desired/active/applied、rollout、Secret、drain/attempt 与当前 restart 总契约。
- `.trellis/spec/frontend/model-settings.md` - Settings strict wire、状态所有权、Secret 生命周期和现有 restart UX。
- `api/openapi/openapi.json` - 当前 GET/PUT/Test paths、Snapshot/rollout/runtime schemas 和 Session/no-store 契约。
- `internal/modelsettings/http/handler.go` - 当前三条路由、Session 二次校验、strict JSON、Secret destroy、Snapshot/Problem projection。
- `internal/modelsettings/application/ports.go` - SettingsManager 与内部 Rollout/Runtime ports。
- `internal/modelsettings/application/service.go` - Save 只推进 desired、Test/Secret 生命周期与 rollout facade。
- `internal/modelsettings/application/coordinator.go` - leased rollout session、abort、commit、wait/recover 语义。
- `internal/modelsettings/adapter/postgres/revision.go` - append-only SaveDesired 事务、rollout 锁和 expected revision CAS。
- `internal/modelsettings/adapter/postgres/rollout.go` - begin/advance/fail/commit/recover 的 PostgreSQL 状态机。
- `internal/modelsettings/adapter/postgres/snapshot.go` - restartRequired、active capability、runtime freshness 的当前派生方式。
- `internal/modelsettings/domain/rollout.go` - 当前 rollout/runtime phases 与单槽 RuntimeSummary。
- `internal/modelsettings/domain/errors.go` - 稳定 Model Settings 错误码。
- `migrations/00064_model_settings.sql` - immutable revision、singleton rollout state、runtime role state 与 transition guards。
- `cmd/modelctl/control.go` - 当前唯一 rollout 操作入口与 abort/recover 行为。
- `web/src/api/model-settings.ts` - strict codec、Save/Test request、Secret leak detection 和 no-store enforcement。
- `web/src/api/auth.ts` - Cookie/CSRF/authFetch 与本地 401 Session invalidation。
- `web/src/features/settings/ModelSettingsPanel.tsx` - Query polling、mutation、conflict merge、状态/rollout/Secret UI。
- `web/src/api/model-settings.test.ts` - frontend wire/Secret/Problem tests。
- `web/src/features/settings/ModelSettingsPanel.test.tsx` - component states、conflict、rollout、unmount abort tests。
- `internal/modelsettings/http/handler_test.go` - Handler request/response、Session、Secret 与诊断测试。
- `internal/app/model_settings_router_test.go` - route registration、Session/CSRF/no-store tests。
- `internal/modelsettings/adapter/postgres/repository_integration_test.go` - revision/rollout/runtime/concurrency/recovery integration tests。

## Code Patterns

- **Strict boundary owner**：前端 API 模块从 `unknown` exact decode，组件只消费 typed Snapshot（`web/src/api/model-settings.ts:482-542`）。
- **Optimistic revision + server refetch**：Save 使用 expected revision；409 只回 current revision，前端回查后保留非 Secret draft（`api/openapi/openapi.json:11859-11900`; `web/src/features/settings/ModelSettingsPanel.tsx:388-407`）。
- **Server-side Secret ownership**：HTTP/Application 内解析、解密、测试、销毁；Apply 应复用 revision loader，不能扩张 Secret wire（`internal/modelsettings/application/ports.go:186-198`）。
- **Database-owned rollout truth**：target/phase/lease/version 存 PostgreSQL，进程只持 ownership 并 heartbeat（`migrations/00064_model_settings.sql:108-138,228-244`）。
- **UI observes, not advances**：当前 Query 轮询 Snapshot；新 activation 也应由服务端 coordinator 推进，浏览器只发 Start/Cancel intent。

## External References

- 未使用外部资料；本研究是现有仓库契约与新 UX 需求的内部对照。
- 当前前端依赖版本：React 19.2.7、TanStack Query 5.101.2、Vitest 4.1.10（`web/package.json:25-49`）。

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md:16-27` - Settings HTTP/Runtime/Rollout seam 与 desired/active/applied 定义。
- `.trellis/spec/backend/model-settings-runtime.md:26-43` - Secret、Audit、drain、attempt revision 不变量。
- `.trellis/spec/backend/model-settings-runtime.md:60-83` - rollout 失败、stale、restart 与 Good/Bad cases。
- `.trellis/spec/frontend/model-settings.md:13-30` - strict wire、expected revision、Session-only、Secret 和状态区分。
- `.trellis/spec/frontend/model-settings.md:33-52` - conflict、rollout、restart required、stale/mismatch UX。
- `.trellis/spec/backend/auth-security.md` - Cookie Session、Origin/CSRF、Capability 与 Secret 管理边界。
- `.trellis/spec/frontend/state-management.md` - Query key、mutation 后权威 invalidation/refetch 与业务状态所有权。

## Caveats / Not Found

- 没有现成 HTTP Apply/Cancel endpoint、OpenAPI schema 或前端 client，可以复用的只有内部 rollout coordinator/state machine。
- 当前 runtime data model 每个 role 只有一条记录；它不能直接证明同一进程同时保留旧 serving runtime 与新 candidate。本文只定义 UX 所需观察面，不判断热切换内部实现是否采用双 runtime slot、generation registry 或其他机制。
- 当前 `failed => retryable=true` 是硬编码投影，无法表达 user-cancelled 或明确不可重试错误；设计阶段需先扩展 domain/read model。
- 当前 2 秒轮询没有组件测试，也没有 backoff、焦点恢复或长时间 operation UX；真实时序仍需浏览器验证。
- 本文推荐 B 作为默认，但是否提供首屏“保存并应用”快捷操作仍是产品选择；无论选择，canonical 保存与 activation 事实不应合并成模糊成功状态。
