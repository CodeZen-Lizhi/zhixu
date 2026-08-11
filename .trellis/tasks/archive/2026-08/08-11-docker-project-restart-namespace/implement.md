# 执行计划

1. 先补失败合同与隔离 Docker fixture：锁定当前 owner-owned namespace 的项目 restart
   失败、external anchor 下主 consumers 并行 restart 成功、daemon-style randomized start
   只能保证 ready 或 degraded（不得假 ready），以及 container network mode + links 被
   Engine 拒绝。
2. 新增 `deploy/compose.netns.yml` 和 app anchor entrypoint。app anchor 固定执行
   firewall -> privilege drop -> `exec socat`，worker anchor 运行 non-root loopback sentinel；
   Dockerfile 只引入成熟降权工具。补静态合同和真实 `/proc/1/status` 权限断言。
3. 更新 `deploy/compose.yml`：主 default network 改为 external `zhixu-runtime`；app/worker
   与两个 relay 引用固定 external anchor；端口/host-gateway 移到 anchor；删除主项目
   proxy/firewall service；增加可检测 namespace 分叉及 relay 本地 `11434` listener 的
   app/worker/relay healthcheck。
4. 扩展 Compose auth/runtime/workspace checker 与 contracts，交叉校验两份 model：固定
   project/container/network/port、API/relay loopback、helper 零 Workspace/secret/socket、
   app anchor 仅启动期 `NET_ADMIN`/`SETUID`/`SETGID`、worker anchor 零 capability、
   app anchor 长期进程能力全零、只有 app/worker exact bind。
5. 扩展 `zhixu` 双项目生命周期：helper identity 校验、旧拓扑无损迁移、幂等复用、
   anchor-first start/restart、端口漂移受控重建、status 合并、host ready probe，以及
   consumers -> main -> helper 的 down/reset 顺序。fake Docker contract 覆盖外来同名资源、
   中途失败、主项目 partial down 和 argv/log 脱敏。
6. 最小调整 `internal/workspacecontrol.ComposeDriver`：ApplyGrant 只在 launcher 已验证 anchor
   后 force-recreate app/worker，再启动并等待两个健康 relay；RevokeGrant 删除所有
   Workspace consumers，不再管理 proxy/firewall。保持 Coordinator、exact grant、
   prepared candidate 和 rollback 状态机不变，并补固定顺序/失败清理测试。
7. 更新受影响 smoke/cleanup scripts，使它们先启动隔离 helper fixture，验收 host loopback、
   bridge peer rejection、四类 consumer 的 DNS/hosts 继承、relay 本地 listener、到
   disposable host listener 的实际连接、Workspace 零越权和精确清理；不得使用宽泛
   container/network 删除或 `down -v`，也不得占用用户真实 Ollama。
8. 新增 ADR 并同步 Workspace/Auth/Model runtime specs、operations、requirements、system
   design。明确 Docker UI 只支持主 `zhixu` Restart project，helper/主 Down/Delete 的
   degraded 与 launcher 恢复路径，以及 daemon restart 不保证跨项目自动排序。
9. 运行静态和局部门禁，再执行真实 Docker 验收：当前旧拓扑升级、主项目 restart、
   helper recreate 后 peer isolation、端口 A -> B -> A、partial teardown recovery、status/
   curl/inode/privilege 检查。默认不重启全局 Docker daemon。
10. 使用 `go-review` 审查 Go/Docker/并发/权限变更，并用独立 `trellis-check` 复核 spec、
    contracts、测试与 dirty worktree 合并；修复发现后重新验证，再提交、push、归档任务。

## 预计修改文件

- `deploy/compose.yml`
- `deploy/compose.netns.yml`（新增）
- `deploy/Dockerfile`
- `deploy/netns-ingress.sh`（新增）
- `deploy/loopback-firewall.sh`（仅在复用接口需要时）
- `deploy/compose_auth_check.py` 及对应 contract/tests
- `deploy/compose_runtime_check.py`
- `deploy/compose_runtime_contract.py`
- `deploy/compose_workspace_check.py` 及对应 contract/tests（如交叉 model 校验需要）
- `deploy/launcher-contract.sh`
- `deploy/testdata/launcher-fake-docker.sh`
- 受影响的 `deploy/compose-*-smoke.sh`、cleanup contract/script
- `zhixu`
- `internal/workspacecontrol/compose_driver.go`
- `internal/workspacecontrol/compose_driver_test.go`
- `.trellis/spec/backend/workspace-root-grant.md`
- `.trellis/spec/backend/auth-security.md`
- `.trellis/spec/backend/model-settings-runtime.md`（保留并合并当前用户 dirty 改动）
- `docs/architecture/adr/0021-*.md` 与 ADR 索引
- `docs/operations.md`
- `docs/requirements.md`
- `docs/architecture/system-design.md`

## 自动验证命令

```bash
go test -race ./internal/workspacecontrol ./cmd/workspacectl ./cmd/runtimewait
go vet ./internal/workspacecontrol ./cmd/workspacectl ./cmd/runtimewait
python3 deploy/compose_runtime_contract.py
bash deploy/launcher-contract.sh
bash deploy/compose-smoke-cleanup-contract.sh
bash -n zhixu deploy/*.sh deploy/testdata/launcher-fake-docker.sh
python3 -m py_compile deploy/compose_auth_check.py deploy/compose_runtime_check.py \
  deploy/compose_runtime_contract.py deploy/compose_workspace_check.py
git diff --check
```

若 shell glob 包含不适合 `bash -n` 的脚本，则按 `rg --files deploy -g '*.sh'` 得到的明确
文件列表分批校验，不把命令失败静默忽略。

## 真实 Docker 验收

所有诊断 fixture 使用唯一固定前缀、零 Workspace mount/零 named volume，完成后只删除
精确创建的 containers/networks。用户当前栈不执行 `down -v`、`reset` 或全局 daemon
restart。

```bash
# 当前旧拓扑由新版 launcher 原地迁移，保留 volume/selection/grant/Workspace
./zhixu restart

# 唯一受支持的 Docker UI 等价操作
docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml -f .zhixu/workspace-grant.yml --env-file .env restart

./zhixu status
curl -fsS "http://127.0.0.1:${ZHIXU_HTTP_PORT:-8080}/readyz"
```

随后断言：

1. app/relay 与 `zhixu-app-netns` 的 `/proc/1/ns/net` inode 一致；worker/relay 与
   `zhixu-worker-netns` 一致；两组互异。
2. app anchor PID 1 UID/GID、NoNewPrivs 与 capability 符合 AC-06；anchor 无 mount、
   secret、grant/database env 或 Docker socket。
3. 同 `zhixu-runtime` 的无权限 peer 无法直连 ingress；host loopback 仍可达。
4. 单独 recreate app anchor 后入口先 fail closed、status degraded；`./zhixu restart`
   恢复后再次执行 peer rejection。
5. 在 disposable env/fixture 中完成端口 A -> B -> A，验证旧端口释放、新端口唯一发布、
   status/curl 跟随目标端口。
6. 在 disposable project 中模拟主项目已 down、helper/network 残留，`./zhixu down`
   幂等收敛且不删除主 named volumes 或宿主机文件。
7. 在 app、worker、两个 relay 内分别执行受控解析/连接探针；relay health 同时证明
   `127.0.0.1:11434` LISTEN 与 anchor/owner 对齐，overlay 把 relay 上游指向唯一高端口
   disposable host listener 并完成一次 TCP round trip。

## 风险与回滚点

- 双项目顺序错误会留下 external network endpoint 或固定端口；合同必须锁定
  consumers -> main -> helper，且 partial teardown 可恢复。
- 误重建 anchor 而未重建 consumers 会产生 namespace 分叉；cross-namespace health、
  host ready 和 status 必须共同阻止假 ready。
- firewall 必须在 `socat` bind 前完成，降权必须以运行时 `/proc/1/status` 验证；只检查
  Compose YAML 不足以证明长期进程最小权限。
- 同名外来 container/network 不能自动删除；只有完整 label/config/endpoint identity
  匹配的 launcher-owned resource 才可管理。
- 端口检测不能继续读取 app service，也不能把待替换的旧 app anchor 当外部占用者。
- 当前 `.trellis/spec/backend/model-settings-runtime.md` 有用户未提交改动；实施必须逐段
  合并，不覆盖或回滚。
- 回滚前必须用新版 launcher down helper；任何回滚均不得删除 volume、selection、grant
  或宿主机 Workspace 文件。

## task.py start 前复核

- 用户明确批准新增可见的 `zhixu-netns` helper project，以及“Docker UI 只支持主项目
  Restart；daemon/helper restart 可能 degraded、由 launcher 恢复”的操作边界。
- `prd.md`、`design.md`、`implement.md`、research 与 implement/check manifests 通过
  task validation，并完成一次独立规划复核。
- 当前无关 dirty 文件已记录；实施不覆盖 model connection task 的改动。
