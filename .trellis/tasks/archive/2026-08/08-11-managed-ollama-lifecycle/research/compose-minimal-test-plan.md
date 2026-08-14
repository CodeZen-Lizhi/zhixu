# Research: 最小真实 Compose 验收执行清单

- Query: 盘点 launcher、Compose、模型设置 API、生命周期表和现有 smoke 工具，形成覆盖全线上、两个单本地组合、双本地、退出、卷复用、不重复 pull、管理容器重启和内存门禁的最小真实 Compose 计划；补充 exact legacy Ollama 资源的只读判定。
- Scope: internal
- Date: 2026-08-14

## Execution Update: 2026-08-14

本清单形成后已收敛为可重复脚本 `./deploy/managed-ollama-compose-smoke.sh`，并在当前 Docker Desktop 完整通过一次。脚本以
受控 HTTPS 线上 fixture 依次验证线上、Chat 本地、Embedding 本地、两者本地和切回线上；本地阶段始终只有一个
`ollama serve`，最终线上阶段无 serve/runner。cached model identity 保持不变，管理容器重启没有新增 pull，恢复后的
`child_epoch` 大于重启前；这证明 manager container restart recovery，不等同于 Docker daemon restart。

全线上连续 60 秒采样的 cgroup anonymous RSS 峰值为 6.39 MiB，manager process RSS 峰值为 9.69 MiB，相对 700 MiB
基线下降 99.1%。脚本使用随机 disposable project/DB/model volume 并在成功后精确清理。下文是执行前的研究和完整发布矩阵；
其中关于 stop/restart epoch 可能阻断的预测已由后续修复和本次运行关闭，但原生 Linux、真实公网 Provider 出口、Docker daemon
restart、显式 child crash/signal/reap、浏览器、长在途 generation 栅栏和真实 legacy migration 仍未执行。

## Findings

### 1. 结论与安全边界

最小且能闭合证据链的顺序是：

```text
双线上基线
  -> Chat 本地 / Embedding 线上
  -> Chat 线上 / Embedding 本地
  -> Chat 本地 / Embedding 本地
  -> 恢复双线上并证明 child 退出和内存下降
  -> 再次双本地并证明卷复用、attempt_no=0
  -> 双本地下重启 local-model-runtime
  -> 最终再次恢复双线上（最新 desired=active=api=worker applied）
```

必须使用随机、一次性的 Compose project；现有 smoke 清理器只接受
`^zhixu-(auth|rag|search|tool)-smoke-[0-9a-f]{12}$`，因此可复用
`zhixu-rag-smoke-<12hex>` 命名和随机 netns helper，不要用固定 `zhixu` 项目做首次验收
（`deploy/compose-smoke-cleanup.sh:5-39,51-75`）。只有这个随机项目的 cleanup 才可以执行
`down --volumes`；真实项目和 legacy 资源均不得执行 `down -v`、`prune`、`migrate` 或宽泛删除
（`.trellis/tasks/08-11-managed-ollama-lifecycle/implement.md:201`）。

设置只能通过 Session-only HTTP API 改动：`PUT /api/v1/settings/models` 保存完整 draft，随后
`POST /api/v1/settings/models/activations` 应用返回的新 revision。不能直接 UPDATE
`ops.model_settings_state`、revision、runtime、hold 或 operation 表；这些表由不可逆 revision、trigger、lease 和
CAS 约束共同维护（`internal/modelsettings/http/handler.go:113-118,300-394`，
`migrations/00064_model_settings.sql:108-150,228-270`，
`migrations/00080_managed_ollama_runtime.sql:150-219`）。数据库只用于 `BEGIN READ ONLY` 观测。

`GET /api/v1/settings/models` 只返回 `api_key_configured`，不会返回明文密钥
（`internal/modelsettings/http/handler.go:191-214`）。而 Ollama revision 会 `clear` 线上密钥；从 Ollama 再切回
线上时，`keep` 无法恢复已经清空的密钥（`internal/modelsettings/adapter/postgres/revision.go:280-300`）。因此在第一次
PUT 前必须满足以下硬门禁：

1. 已准备两个可由隔离容器访问的 `openai-compatible` HTTPS target，并接受本次最小 production probe 的调用成本；
2. 已通过权限受控的密钥文件准备好 Chat、Embedding 测试/恢复密钥；不得只依赖 GET 快照；
3. 各次重新引入线上 target 均使用 `api_key.action=replace`，密钥不放 argv、环境摘要、日志或普通响应；
4. 最终再保存一次基线内容并 Apply。revision 数字会增加，但恢复后的双线上内容必须成为最新 desired、active、API/Worker applied。

若无法提供两个真实 HTTPS Provider 和恢复密钥，不能声称完成“最终最新双线上”。`disabled` 可验证 AC 中
“线上或 disabled”的停服分支，但不能替代本清单要求的双线上恢复。新线上配置必须是 HTTPS；本地 Ollama 只能用固定
`http://127.0.0.1:11434` relay（`internal/modelsettings/domain/settings.go:19-20,183-205`）。

### 2. 隔离栈准备

复用 `deploy/model-runtime-hot-activation-smoke.sh` 的以下框架，而不是直接执行该 smoke：它当前把两个 relay
改到宿主 fixture，会绕过 managed Ollama，不能作为本任务的本地生命周期证据
（`deploy/model-runtime-hot-activation-smoke.sh:410-430`）。可复用部分包括随机 project/netns、Session 登录、
CSRF 请求 helper、容器身份记录和最终收敛断言（同文件 `:47-55,180-217,357-381,391-407`）。

以 Bash 数组保存 Compose 命令，避免字符串重解析；示意中的 `<runtime-override>` 只应发布随机 API/PostgreSQL
端口和挂载一次性 workspace，**不要覆盖** `app-model-relay`/`worker-model-relay` entrypoint：

```bash
run_id="$(random_hex 6)"
PROJECT="zhixu-rag-smoke-${run_id}"
# 复用 prepare_compose_smoke_netns 生成随机 anchor/network override。
COMPOSE=(docker compose --project-name "$PROJECT" \
  -f deploy/compose.yml -f "$RUNTIME_OVERRIDE" -f "$MAIN_NETNS_OVERRIDE" \
  --env-file "$PRIVATE_ENV")
NETNS=(docker compose --project-name "${PROJECT}-netns" \
  -f deploy/compose.netns.yml -f "$NETNS_OVERRIDE" --env-file "$PRIVATE_ENV")

"${COMPOSE[@]}" --profile workspace-runtime config --quiet
"${COMPOSE[@]}" --profile workspace-runtime build \
  model-settings-key-init migrate local-model-runtime-credential-init \
  local-model-runtime app worker app-model-relay worker-model-relay
"${NETNS[@]}" config --quiet
"${NETNS[@]}" build --quiet
"${NETNS[@]}" up --detach --wait

"${COMPOSE[@]}" up --detach --wait postgres
"${COMPOSE[@]}" run --rm --no-deps -T model-settings-key-init
"${COMPOSE[@]}" run --rm --no-deps -T migrate
"${COMPOSE[@]}" --profile workspace-runtime run --rm --no-deps -T local-model-runtime-credential-init
"${COMPOSE[@]}" --profile workspace-runtime run --rm --no-deps -T local-model-volume-init
"${COMPOSE[@]}" --profile workspace-runtime up --detach --wait local-model-runtime
"${COMPOSE[@]}" --profile workspace-runtime up --detach --no-deps --wait app worker
"${COMPOSE[@]}" --profile workspace-runtime up --detach --no-deps --wait app-model-relay worker-model-relay
```

顺序与 launcher 相同：PostgreSQL、key init、migration、runtime credential init、volume init、manager
（`zhixu:1130-1141`）。managed service 是 non-root、read-only rootfs，模型卷挂在
`/var/lib/zhixu/ollama/.ollama`；两个 relay 的原始 upstream 都是 `local-model-runtime:11434`
（`deploy/compose.yml:128-185,228-243,307-322`）。

建议本次空卷使用生产迁移 helper 已固定验证的两个模型，避免另造模型契约：

```text
Chat:      qwen2.5:0.5b
Embedding: all-minilm:latest, dimensions=384, normalization=l2, distance_metric=cosine
Relay:     http://127.0.0.1:11434
```

这两个模型也是 legacy migration 的生产 probe target（`deploy/local-model-legacy-migrate.sh:15-16,389-392`）；
既有真实 image probe 还证明 `all-minilm:latest` 输出 384 维
（`.trellis/tasks/08-11-managed-ollama-lifecycle/research/phase-a-image-probe.md:189-227`）。需要更小的 Chat 下载时可改为
已真实探测过的 `smollm2:135m`，但必须先确认当前生产 Chat adapter probe 仍通过，不能只以 `/api/tags` 存在代替。

### 3. API 切换模板

先按现有 smoke 的方式建立 Session：POST `/api/v1/auth/sessions` 带 `Origin` 和 bootstrap bearer，保存
`zhixu_session` cookie 和响应中的 CSRF token。之后 GET 带 cookie；PUT/POST 额外带同一 `Origin`、
`X-CSRF-Token`、`Content-Type: application/json`。现成的 `request_json` 可直接复用
（`deploy/model-runtime-hot-activation-smoke.sh:180-217`）。

新建隔离数据库从 revision 0 的双 disabled 开始，并不会天然存在“双线上基线”。因此认证后必须先用下述 PUT+Apply
模板提交受控的双线上 fixture，等待其成为第一个 desired=active=applied revision，再把这份响应保存为 baseline。
若是在已预置设置的隔离库执行，则先逐字段确认它确实是双线上；不满足时仍应显式提交受控 fixture，不能把 disabled
或本地 revision 误称为双线上。

所有请求 body 写入 `umask 077` 的临时文件，并用 `curl --data-binary @file`；线上密钥从 `0600` 文件以
`jq --rawfile` 注入，禁止打印 body。每轮函数都必须：

```bash
# 1. GET，取得当前 desired_revision=N。
curl --fail --silent --cookie "$COOKIE_JAR" \
  "$API_BASE/api/v1/settings/models" > "$STATE/current.json"

# 2. 生成包含 expected_revision=N、完整 chat、完整 embedding 的 body。
# 3. PUT 保存；从响应读取新的 desired_revision=R。
curl --fail --silent --request PUT --cookie "$COOKIE_JAR" \
  --header "Origin: $ORIGIN" --header "X-CSRF-Token: $CSRF" \
  --header 'Content-Type: application/json' --data-binary @"$STATE/update.json" \
  "$API_BASE/api/v1/settings/models" > "$STATE/saved.json"
R="$(jq -er '.desired_revision' "$STATE/saved.json")"

# 4. POST exact R；202 只代表启动，不代表完成。
jq -cn --argjson revision "$R" '{expected_revision:$revision}' > "$STATE/activate.json"
curl --fail --silent --request POST --cookie "$COOKIE_JAR" \
  --header "Origin: $ORIGIN" --header "X-CSRF-Token: $CSRF" \
  --header 'Content-Type: application/json' --data-binary @"$STATE/activate.json" \
  "$API_BASE/api/v1/settings/models/activations" > "$STATE/activation.json"

# 5. GET 轮询权威 Snapshot，超时建议首次 pull 20 分钟，复用/停止 3 分钟。
```

本清单不额外调用同步 `/test`：Activation 自身会完成准备和 API/Worker production probe；先 Test 会预下载模型，
反而破坏“首次 activation 的 pull 证据”。如果另测 Test，必须使用唯一 `Idempotency-Key`，且同 key 重放应 join 同一
operation（`.trellis/spec/backend/model-settings-runtime.md:233-243,253-260`）。

四种 target 的字段如下。每次 PUT 都提交完整两侧；线上侧复用基线非 Secret 字段，且只要上一 revision 的同侧曾切到
Ollama，就必须 `replace` 恢复密钥：

```json
{
  "chat_online": {
    "provider": "openai-compatible",
    "api_style": "<responses-or-chat_completions>",
    "base_url": "https://<controlled-chat-provider>/v1",
    "model": "<chat-model>",
    "model_version": "<pinned-version>",
    "adapter_version": "<adapter-version>",
    "api_key": {"action": "replace", "value": "<read-from-private-file>"}
  },
  "embedding_online": {
    "provider": "openai-compatible",
    "base_url": "https://<controlled-embedding-provider>/v1",
    "model": "<embedding-model>",
    "dimensions": 384,
    "normalization": "l2",
    "distance_metric": "cosine",
    "api_key": {"action": "replace", "value": "<read-from-private-file>"}
  },
  "chat_local": {
    "provider": "ollama",
    "api_style": "chat_completions",
    "base_url": "http://127.0.0.1:11434",
    "model": "qwen2.5:0.5b",
    "model_version": "qwen2.5:0.5b",
    "adapter_version": "v1",
    "api_key": {"action": "clear"}
  },
  "embedding_local": {
    "provider": "ollama",
    "base_url": "http://127.0.0.1:11434",
    "model": "all-minilm:latest",
    "dimensions": 384,
    "normalization": "l2",
    "distance_metric": "cosine",
    "api_key": {"action": "clear"}
  }
}
```

四个命名对象都是 schema 骨架，不得把尖括号 placeholder 当真实值，也不得把真实密钥落到证据文件。
`chat_local`/`embedding_local` 等只是片段，不是可直接 PUT 的根对象。根对象必须精确为
`expected_revision, chat, embedding`；字段编码契约见 `web/src/api/model-settings.ts:728-772`。

每轮成功的统一 API 断言：

```jq
.desired_revision == $r and .active_revision == $r and
.runtime.api.applied_revision == $r and .runtime.api.phase == "active" and .runtime.api.fresh and
.runtime.worker.applied_revision == $r and .runtime.worker.phase == "active" and .runtime.worker.fresh and
.rollout.phase == "idle" and (.apply_required | not) and (.restart_required | not)
```

本地 target 还要断言 `.local_runtime.mode=="managed"`、`.local_runtime.fresh==true`、
`.local_runtime.phase=="ready"`、非空 `requirement_hash` 且等于 `ready_hash`；全线上最终要断言 phase=`stopped`、
两个 hash 均空。Snapshot 字段来自 `internal/modelsettings/http/handler.go:162-188,762-812`。

### 4. 只读观测 helper

以下 helper 均只读；实际脚本应让任何解析失败立即失败，不能以空值当通过。

#### 4.1 进程计数

不假设 image 安装了 `ps`，从 `/proc/*/cmdline` 精确读取：

```bash
CID="$("${COMPOSE[@]}" ps -q local-model-runtime)"
docker exec "$CID" /bin/bash -ec '
  serve=0; runner=0
  for f in /proc/[0-9]*/cmdline; do
    [ -r "$f" ] || continue
    cmd="$(tr "\000" " " < "$f")"
    case "$cmd" in
      "/usr/bin/ollama serve"|"/usr/bin/ollama serve ")
        serve=$((serve+1))
        pid="${f#/proc/}"; pid="${pid%/cmdline}"
        starttime="$(awk "{print \$22}" "/proc/$pid/stat")"
        printf "serve_identity=%s:%s\n" "$pid" "$starttime"
        ;;
    esac
    case "$cmd" in
      /usr/lib/ollama/llama-server\ *) runner=$((runner+1));;
    esac
  done
  printf "serve=%s runner=%s\n" "$serve" "$runner"
'
```

child 的命令和路径是固定的 `/usr/bin/ollama serve`，并在独立 process group 中运行
（`internal/localmodelruntime/runner_unix.go:13-27,30-56`）。本地各阶段硬条件是 `serve=1`；runner 数取决于模型加载，
不要求等于一。全线上稳定态硬条件是 `serve=0 runner=0`。同时用 Compose label 证明只有一个 manager 容器：

```bash
docker ps -aq \
  --filter "label=com.docker.compose.project=$PROJECT" \
  --filter 'label=com.docker.compose.service=local-model-runtime'
# 精确一行；不要用名称模糊匹配。
```

#### 4.2 数据库状态与不重复 pull

```bash
"${COMPOSE[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -At \
  --username "$ZHIXU_POSTGRES_USER" --dbname "$ZHIXU_POSTGRES_DB" <<'SQL'
BEGIN READ ONLY;
SELECT desired_revision,active_revision,phase,target_revision,previous_active_revision,version
FROM ops.model_settings_state WHERE singleton;
SELECT role,applied_revision,phase,instance_id,heartbeat_at
FROM ops.model_settings_runtime ORDER BY role;
SELECT owner_id,observed_phase,requirement_hash,ready_requirement_hash,required_models,
       child_epoch,owner_epoch,heartbeat_at,lease_expires_at,version
FROM ops.managed_ollama_runtime WHERE singleton;
SELECT count(*) FROM ops.managed_ollama_holds
WHERE released_at IS NULL AND lease_expires_at > clock_timestamp();
SELECT operation_id,kind,target_revision,phase,attempt_no,required_models,resolved_models,
       completed_bytes,total_bytes,error_code,created_at,terminal_at
FROM ops.managed_ollama_operations ORDER BY created_at,operation_id;
COMMIT;
SQL
```

operation 的 `attempt_no` 初值是 0，`resolved_models` 持久保存 model/digest/bytes
（`migrations/00080_managed_ollama_runtime.sql:104-143`）；实现只在真正调用每一次 `/api/pull` 前递增
（`internal/localmodelruntime/reconciler.go:733-754`）。因此：

- 空卷首次 Chat-local activation：目标 revision 的成功 activation operation 预期 `attempt_no>=1`；
- 首次 Embedding-local activation：预期 `attempt_no>=1`；
- 两个模型都已存在后的双本地 activation：必须 `attempt_no=0`；
- 停服后再次双本地：必须 `attempt_no=0`，且 resolved digest/bytes 与首次相同；
- 管理容器重启前后所有既有 operation 的 `attempt_no` 不得增加；若出现 recovery operation，它也必须为 0。

日志里没有出现 pull 只能作辅助证据，不能替代 `attempt_no=0`。反过来，首次模型可能已经被 image/缓存准备好，若空卷
实际得到 0 也不是失败；必须以 preflight 的空 manifest 和 operation 的 resolved digest 解释，不能强行要求发生下载。

#### 4.3 卷身份与内容复用

随机项目的卷名是 `${PROJECT}_zhixu-local-models`。记录 inspect JSON 中的 Name、Driver、CreatedAt、Labels。必需标签是
`com.docker.compose.project=$PROJECT`、`com.docker.compose.volume=zhixu-local-models`、
`com.zhixu.owner=local-model-runtime`、`com.zhixu.schema=local-model-store/v1`；只额外允许格式合法的
`com.docker.compose.version`（`deploy/compose.yml:354-364`，`zhixu:1628-1668`）。在 child 已停止后仍通过常驻
manager 容器只读卷内容：

```bash
VOL="${PROJECT}_zhixu-local-models"
docker volume inspect "$VOL" > "$STATE/volume-before.json"
docker exec "$CID" /bin/bash -ec '
  root=/var/lib/zhixu/ollama/.ollama
  test "$(cat "$root/.zhixu-managed-model-store-v1")" = local-model-store/v1
  if [ ! -d "$root/models/manifests" ]; then
    printf "EMPTY\n"
    exit 0
  fi
  find "$root/models/manifests" -type f -exec sha256sum {} + | sort | sha256sum
' > "$STATE/manifests-before.sha256"
```

marker 是初始化器的正式 schema 身份（`deploy/local-model-volume-init.sh:5-34`）。在双线上停服、manager 重启、再次双本地
之后重复 inspect 和 manifest 指纹：卷 Name/CreatedAt/Labels、marker、manifest 指纹均不得变化；再结合 operation
`resolved_models` digest/bytes 相同和 `attempt_no=0`，构成模型数据复用证据。不要对全部大 blob 做全量 hash，manifest
指纹已经足够识别引用内容，且不会制造不必要 I/O。

#### 4.4 内存采样

每个采样阶段连续 60 秒、每 5 秒一次，记录以下三个口径：

1. supervisor `/proc/<pid>/smaps_rollup` 的 `Anonymous` 和 `Rss`；
2. 容器 cgroup v2 `/sys/fs/cgroup/memory.stat` 的 `anon`；
3. `docker stats --no-stream` 的 total 仅作辅助，不能用于 64 MiB 硬门禁。

```bash
docker exec "$CID" /bin/bash -ec '
  supervisor=""
  for f in /proc/[0-9]*/cmdline; do
    [ -r "$f" ] || continue
    cmd="$(tr "\000" " " < "$f")"
    case "$cmd" in
      /usr/local/bin/zhixu-local-model-runtime|/usr/local/bin/zhixu-local-model-runtime\ *)
        supervisor="${f#/proc/}"; supervisor="${supervisor%/cmdline}"; break;;
    esac
  done
  [ -n "$supervisor" ]
  awk '\''$1=="Rss:" || $1=="Anonymous:" {print "process_" $1, $2*1024}'\'' \
    "/proc/$supervisor/smaps_rollup"
  test -r /sys/fs/cgroup/memory.stat
  awk '\''$1=="anon" {print "cgroup_anon:", $2; found=1} END {exit !found}'\'' \
    /sys/fs/cgroup/memory.stat
'
docker stats --no-stream --format '{{.MemUsage}}' "$CID"
```

至少采三组：初始双线上 idle、双本地完成 production probe 后的 active 基线、最终双线上 idle。每个 idle 窗口的
**最大值**都必须满足 process anonymous <= 64 MiB 且 cgroup anon <= 64 MiB；为保守证明相对下降，最终 idle
cgroup anon 的最大值还必须不高于双本地 active 窗口 cgroup anon **最小值**的 20%。如果环境不是 cgroup v2或
`anon` 不可读，应标为该平台未验，不得以 Docker total
代替。PRD 的硬口径见 `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:103-111,119-128`。

### 5. 最小执行矩阵与逐步通过条件

表中的 R1-R6 只是脚本在每轮 PUT 响应中捕获的实际 revision 别名，不是假定数据库 revision 固定从 1 开始。

#### 执行前已发现的 child epoch 阻塞

当前源码有两个确定的静态契约冲突，真实 Compose 执行前应先由 implement agent 修正并补真实 PostgreSQL 回归；本文不改
代码：

1. runtime trigger 禁止 `child_epoch` 下降（`migrations/00080_managed_ollama_runtime.sql:235-250`），但有 child 的
   stop 路径在 TERM/Wait 后提交 `stopped, child_epoch=0`
   （`internal/localmodelruntime/reconciler.go:358-386`）。只要 child 曾经运行，数据库 transition 就会被 trigger 拒绝，
   预期表现是进程已经退出但权威 runtime 卡在 `stopping`，步骤 4 无法通过。
2. `Manager.nextGen` 是进程内从 0 开始的计数，新进程首次 child 固定 generation 1
   （`internal/localmodelruntime/manager.go:75-81,164-177`）。在同一 manager 完成一次 stop/reuse 后，数据库
   `child_epoch` 会达到 2；重启 manager 后新 child 又是 1，而 checking transition 写入这个 1
   （`internal/localmodelruntime/reconciler.go:470-493`），同样会被单调 trigger 拒绝。步骤 6 特意放在 stop/reuse 后，
   不能用“第一次重启时两边碰巧都是 1”掩盖该问题。

现有 reconciler 单元测试使用无 SQL trigger 的 fake store；migration integration 只逐条测 CAS，没有把真实 Reconciler 的
stop/restart 流程接到真实 PostgreSQL，因此尚未捕获这个跨层冲突
（`internal/localmodelruntime/reconciler_test.go:36-52,850-876`，
`internal/platform/migration/managed_ollama_integration_test.go:760-794`）。即使手工观测到 `serve=0`，也不得把 DB
`stopping` 当成成功 stopped；修复后仍按下表做完整真实 Compose 验收。

| 步骤 | API 目标 / Compose 命令 | 必须观测到的通过条件 |
|---|---|---|
| 0. 建立并记录双线上基线 | Fresh DB 先 PUT 受控双线上 fixture 并 POST activation；已预置 DB 只有在逐字段确认后才能直接 GET | baseline revision 已 desired=active=API/Worker applied，rollout idle，两个 target 均为 HTTPS `openai-compatible`；local runtime fresh+stopped；`serve=0 runner=0`；保存脱敏 baseline、app/worker/container identity、volume identity、EMPTY/已有 manifest、DB operation 快照；完成 60 秒 idle 内存采样。缺任一线上配置或恢复密钥立即停止。 |
| 1. Chat 本地 | PUT Chat Ollama + baseline Embedding（Embedding secret 可 keep，因为其 identity 未变），POST activation | 收敛到新 R1；Chat provider=ollama、Embedding=基线线上；local runtime ready/hash 相等；恰好一个 serve；R1 activation succeeded，resolved 含 Chat model。首次空卷通常 attempt>=1。记录 serve PID、child_epoch 和 manifest。 |
| 2. Embedding 本地 | PUT baseline Chat（Chat secret 必须 replace）+ Embedding Ollama，POST activation | 收敛 R2；Chat 线上、Embedding Ollama；仍恰好一个 serve，且从 R1 到 R2 本地需求 union 未清空，serve PID 应保持不变；R2 resolved 含 Embedding model。首次空卷通常 attempt>=1。 |
| 3. 双本地 | PUT 两侧 Ollama/clear，POST activation | 收敛 R3；两侧均 Ollama；恰好一个 serve，不是两个；两个 target 的 production probe 成功；R3 operation succeeded、required/resolved 含两个模型、attempt_no=0；serve PID 继续不变。完成 60 秒 active 内存基线。 |
| 4. 切回双线上并退出 | PUT 基线双线上，两个密钥均 replace；POST activation | 先收敛 R4 的 desired=active=applied，再等待 live hold 数为 0；随后 local runtime fresh+stopped、hash 空、`serve=0 runner=0`。这同时证明停止栅栏在 active/applied/lease 之后；卷仍存在、manifest 不变。完成 60 秒 idle 内存门禁。当前代码预计被上述 stop epoch 冲突阻断，不能降级判定。 |
| 5. 停服后复用 | PUT 双本地，POST activation | 收敛 R5；恰好一个 serve，并记录 `(container StartedAt, child PID, starttime)`；R5 operation attempt_no=0；resolved digest/bytes 与 R3 相同；卷 identity/marker/manifest 全同。该步才是“退出后再次使用”的卷复用证据。 |
| 6. 管理容器重启恢复 | 记录 manager CID/StartedAt/host PID、DB owner_id/owner_epoch/child_epoch、child `(PID,/proc/PID/stat starttime)`、app/worker identity、全部 operation attempt；执行 `"${COMPOSE[@]}" --profile workspace-runtime restart --timeout 45 local-model-runtime` | manager CID 不变、StartedAt 更新、host PID 对应的新容器进程已启动；旧 owner_id 被新的 owner_id 接管且 owner_epoch 增加；90 秒内 DB manager lease/heartbeat fresh、local runtime ready/hash 相等；全容器仍仅一个 serve，并记录新的 `(container StartedAt, child PID, starttime)`。PID 可在新 namespace 内复用，但 owner_id 必须不同；修正后的 child_epoch 契约必须通过 DB trigger，不能下降或卡 phase。app/worker Id/StartedAt/RestartCount 不变；卷身份/manifest 不变；既有 attempt 不增加，任何 recovery op attempt=0。30 秒 lease 可能使恢复不是瞬时（`cmd/local-model-runtime/main.go:162-172`，`internal/localmodelruntime/reconciler.go:16-20,130-214`，`migrations/00080_managed_ollama_runtime.sql:316-337`）。 |
| 7. 最终恢复最新双线上 | PUT 与步骤 0 相同的双线上非 Secret 内容，两个密钥均 replace；POST activation | 收敛 R6，且 R6 是最大 revision；desired=active=API/Worker applied=R6、rollout idle、apply/restart required=false；active/desired 内容逐字段等于基线（密钥只比 configured=true）；live hold=0；local runtime fresh+stopped；`serve=0 runner=0`；卷仍在、manifest 不变；最后再做一次 60 秒 idle 内存采样。 |

每轮保存前都重新 GET revision，遇 409 立即停止并诊断，不能拿旧 revision 自动覆盖。每轮 activation 的 202 后只轮询
GET，不因客户端超时盲目再 POST；先以 rollout target 和 active/applied 判断权威状态。API/Worker 容器身份在整个矩阵中
必须保持不变，现有 helper 已能记录 Id、StartedAt、RestartCount（`deploy/model-runtime-hot-activation-smoke.sh:357-365`）。

退出的根因证据链应同时成立：R4 已完成、API/Worker 已 applied、无 fresh unreleased hold、manager DB phase stopped、
进程数为零。只看到 404、healthcheck healthy 或 Compose service 仍 running 都不足以证明重型 child 已退出。manager 的实现只有在
权威需求集合完全为空时才 Stop，随后 TERM、等待和必要的 process-group KILL/reap
（`internal/localmodelruntime/reconciler.go:352-399`，`internal/localmodelruntime/manager.go:182-220`）。

### 6. exact legacy Ollama 资源的只读判定

仓库内 canonical 的只读入口是：

```bash
./zhixu local-model status
```

它只对 exact 名称执行 Docker inspect/list 并 fail closed；不要执行 `./zhixu local-model migrate` 或
`--confirm MIGRATE`。精确身份为：

- container：`zhixu-eino-live-ollama`；
- volume：`zhixu-eino-live-models`；
- configured image：`ollama/ollama:0.9.6`；
- approved RepoDigest：`ollama/ollama@sha256:f478761c18fea69b1624e095bce0f8aab06825d09ccabcd0f88828db0df185ce`。

常量见 `zhixu:26-29`；状态入口见 `zhixu:1996-2018`。它并非只按名字放行，而会验证
（`zhixu:1493-1625`）：container 无 labels、root user、`Entrypoint=[/bin/ollama]`、`Cmd=[serve]`、bridge-only、
非 privileged、可写 rootfs、restart=no、仅 `127.0.0.1:11434` binding、仅一个 RW volume mount 到
`/root/.ollama`、受限 env allowlist、状态仅 running/exited、image ID 是 sha256；随后检查上述 exact RepoDigest。
volume 必须 local driver/scope、无 labels/options，且引用者只能是该 exact container（container 缺失时必须无引用）。任一
条件不符即视为“同名但未获批准”，不得触碰。

需要保留原始只读证据时，只运行以下 inspect/list，不进入 legacy container，也不挂载 legacy volume：

```bash
docker container inspect zhixu-eino-live-ollama > "$STATE/legacy-container-inspect.json" 2>/dev/null || true
docker volume inspect zhixu-eino-live-models > "$STATE/legacy-volume-inspect.json" 2>/dev/null || true
docker ps --all --filter 'volume=zhixu-eino-live-models' --format '{{.Names}}' \
  > "$STATE/legacy-volume-references.txt"
./zhixu local-model status > "$STATE/legacy-status.txt"
```

这些 legacy 命令只做识别，不属于随机 Compose matrix，也不能把 legacy 模型卷当测试缓存。launcher 的迁移入口从
`zhixu:2020` 开始即进入可变流程，本清单明确不调用。

### 7. 收尾与保留证据

只有步骤 7 已确认最终 R6 双线上后，才能执行随机项目的精确 cleanup。先归档（不得包含 Secret）：每轮 Snapshot 的脱敏
JSON、read-only DB 查询、manager/app/worker identity、进程计数、volume inspect、manifest 指纹、60 秒内存原始样本、
Compose `ps` 和 manager/app/worker 脱敏日志。cleanup 必须复用
`cleanup_compose_smoke_project_images "$PROJECT" "${PROJECT}-netns"`，由其正则和 project binding 限定删除范围
（`deploy/compose-smoke-cleanup.sh:42-113`）。

执行真实项目而不是 disposable project 时，**禁止**自动 cleanup；最后仍需完成步骤 7 的双线上恢复和只读状态确认。

## Files Found

- `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md` - 需求、AC、64 MiB/80% 内存门禁、已完成证据和剩余 Compose 门禁。
- `.trellis/tasks/08-11-managed-ollama-lifecycle/design.md` - 单 supervisor/单 child、持久卷、Relay 与 demand/hold/operation 设计。
- `.trellis/tasks/08-11-managed-ollama-lifecycle/implement.md` - Phase A/G 真实 Compose 验证与 disposable-only 安全边界。
- `.trellis/spec/backend/model-settings-runtime.md` - managed lifecycle、attempt、hold、activation 和验证契约。
- `.trellis/spec/frontend/model-settings.md` - desired/active/applied、Session-only、202 轮询和浏览器验收契约。
- `deploy/compose.yml` - managed manager、volume init、credential init、app/worker relay 和 volume labels。
- `deploy/compose-smoke-cleanup.sh` - 随机 project/netns 生成和精确 cleanup allowlist。
- `deploy/model-runtime-hot-activation-smoke.sh` - 可复用 Session/API/identity/随机 Compose scaffolding；当前 relay fixture 不能复用。
- `deploy/Dockerfile.local-model-runtime` - pinned Ollama 0.32.9 image 与 non-root manager image。
- `deploy/local-model-volume-init.sh` - managed volume marker、ownership 和初始化契约。
- `internal/localmodelruntime/manager.go` - at-most-one child 与 TERM/grace/KILL/reap 实现。
- `internal/localmodelruntime/reconciler.go` - empty-demand stop、existing-child reuse 和 pull attempt 记录点。
- `internal/localmodelruntime/runner_unix.go` - exact child executable、HOME、models path、loopback port 和 env allowlist。
- `internal/modelsettings/http/handler.go` - GET/PUT/Test/Activation 路由、请求/响应和 202 行为。
- `internal/modelsettings/domain/settings.go` - managed Relay 和线上 HTTPS 写入约束。
- `internal/modelsettings/domain/secret.go` - keep 不得跨 target 重绑的 Secret 契约。
- `internal/modelsettings/adapter/postgres/revision.go` - clear/replace/keep 对 revision secret envelope 的实际行为。
- `migrations/00064_model_settings.sql` - desired/active/runtime 状态表与不可任意写约束。
- `migrations/00080_managed_ollama_runtime.sql` - manager runtime、hold、operation、attempt/resolved model schema。
- `zhixu` - exact legacy identity、只读 status 和可变 migrate 边界。

## Code Patterns

- `deploy/compose.yml:128-185` - 一个常驻 manager 容器，同一容器内按需启动 child，模型卷和 credential 卷分离。
- `internal/localmodelruntime/manager.go:99-178` - 并发/repeated Ensure 收敛到同一 child generation。
- `internal/localmodelruntime/reconciler.go:436-475` - requirement 改变但旧 child 健康时保留同一 child，不在单本地组合间误停。
- `internal/localmodelruntime/reconciler.go:733-754` - 只有真正 pull 前才增加持久 attempt。
- `internal/modelsettings/http/handler.go:335-394` - Activation 按 exact desired revision 启动，HTTP 202 后需轮询权威 Snapshot。
- `internal/modelsettings/adapter/postgres/revision.go:280-300` - Secret keep 只能继承上一 desired revision 的 envelope，local->online 需要 replace。
- `zhixu:1493-1625` - legacy container/image/volume exact shape allowlist；名字相同不等于可迁移。

## External References

本轮未新增外部资料。模型大小、协议和 384 维证据复用了任务内已保存的真实 Ollama 0.32.9 probe：
`.trellis/tasks/08-11-managed-ollama-lifecycle/research/phase-a-image-probe.md`；生产验收仍应记录实际下载时返回的
resolved digest/bytes，不能把历史 probe digest 当本次运行事实。

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md:215-266,268-309` - managed lifecycle 的 signature、contract 和测试矩阵。
- `.trellis/spec/frontend/model-settings.md:50-77` - Apply 收敛、容器不重启和隔离 Compose smoke 约束。
- `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md:75-111,119-134` - R1-R5 与 AC1-AC14。

## Caveats / Not Found

- 仓库当前没有覆盖真实 managed Ollama 的一键 Compose smoke；现有
  `deploy/model-runtime-hot-activation-smoke.sh` 是远程 fixture 热激活 smoke，不能直接声称本清单已执行。
- 当前 `child_epoch` 的 stop/reset 与 manager restart 语义和数据库单调 trigger 冲突，是执行本矩阵前已识别的代码 blocker；
  不能通过放宽 stopped/ready 断言绕过。
- 受 trellis-research 角色隔离约束，本轮未读取 `implement.jsonl`/`check.jsonl`；上下文按 `task.json`、PRD、Design、
  Implement 和目标源码建立。
- 本轮严格只读，没有启动 Docker、修改数据库、下载模型、采样真实 PID/RSS，也没有触碰任何 legacy container/volume；
  因此本文是可执行计划，不是验收通过证据。
- 管理容器 `docker compose restart local-model-runtime` 只验证 manager/container restart recovery，**不等于** Docker daemon
  recovery。daemon restart 会影响宿主机其他项目，必须另获明确授权并安排独占维护窗口；当前最小清单不执行它，AC7 的
  daemon recovery 子项仍保持未验证。
- 本矩阵验证 Embedding adapter probe 和 lifecycle，但不自动创建业务文档并验证完整 reindex/retrieval 语义；AC3 的索引/检索
  部分仍需另一个业务 E2E 证据。
- `attempt_no=0` 证明没有发起 `/api/pull`，不单独证明磁盘内容正确；必须与相同 volume identity、marker、manifest 指纹和
  resolved digest/bytes 一起判断。
- runner PID 数可能为 0、1 或 2，受 keep-alive 和两种模型是否同时加载影响；“唯一”硬条件针对 `ollama serve`。
  全线上稳定态则 serve 与 runner 都必须为 0。
- Docker Desktop 与原生 Linux 的 namespace、cgroup 和内存口径需分别执行；一个平台的结果不能替代另一个平台。
