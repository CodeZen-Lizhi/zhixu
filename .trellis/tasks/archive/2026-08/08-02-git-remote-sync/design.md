# Git 远端同步：技术设计

## Module And Least Privilege

新增 `internal/gitsync`，拥有 RemoteConfig、Credential、SyncRun 和同步编排。Change Control `GitRepository` 保持原接口和“不提供 push/fetch/remote”约束。

Remote Git CLI 通过独立 `RemoteRepository` 端口暴露领域动作：InspectRemote、Fetch、Compare、FastForward、Push、VerifyRemote。调用方不能提交原始 Git args、refspec 或环境。

## Data Model

- `git_remote_config`：Workspace、normalized HTTPS URL、branch policy、auto-sync、desired/current revision、status。
- `git_remote_credential`：config revision、ciphertext、nonce/key version、AAD digest、configured flag；不保存明文。
- `git_sync_run`：trigger、config revision、本地/远端 expected OID、direction、status、failure class、index follow-up、version/time。
- `git_sync_attempt`：lease/attempt/checkpoints 和脱敏结果摘要。
- Outbox：manual/auto dispatch、external-change capture follow-up。

同 Workspace 同一时间最多一个 active SyncRun。精确 request replay 返回既有 Run；配置 revision 变化使旧 pending run stale。

## Secret Store

将 AES-GCM 原语抽为通用 `internal/platform/secretstore` 或等价深模块。Model Settings 继续使用原业务 wrapper/AAD；Git Token 使用独立 purpose、schema、Workspace 和 config revision AAD。

Token 只在配置请求、短生命周期解密 buffer 和 Git child process credential session 中为明文。配置 API 只返回 `configured` 与 masked metadata，不回读 Token。

Git 使用项目控制的 AskPass helper：helper path/协议固定，Token 通过仅该子进程继承的受控环境传递；argv、URL、Git config、stdout/stderr 和 telemetry 不含 Token。Runner 的环境过滤新增显式 credential session，而不是允许任意 `GIT_*` 注入。

## Sync State Machine

```text
PENDING -> FETCHING -> COMPARING
  -> SYNCED
  -> FAST_FORWARDING -> VERIFYING -> SUCCEEDED
  -> PUSHING -> VERIFYING -> SUCCEEDED
  -> CONFLICT | FAILED | MANUAL_RECOVERY_REQUIRED
```

每阶段持久化 expected local/remote refs。Fetch 使用受控 tracking ref；Compare 只接受 attached current branch 和 canonical remote branch。

Fast-forward 必须先证明 clean、local 是 remote ancestor 且 refs 未漂移。Adapter 使用受控 fast-forward 操作并在结果未知时重新检查 HEAD/worktree/remote ref；不能盲目重试。

Push 使用显式非 force branch refspec和 expected precondition；之后再次 Fetch/remote inspect，只有远端 OID 等于本地 OID 才成功。

## Inbound Change Flow

Fast-forward 成功后写 follow-up Outbox，调用 Quick Capture/Profile 子任务交付的 external-change capture port。该流程创建/刷新 Source/Revision 并触发 Ingestion/Index/Profile。SyncRun 保存 follow-up identity，但 Git `SUCCEEDED` 与 Index `PENDING|SUCCEEDED|FAILED` 分列。

## Automatic Sync

自动触发只监听已完成本地写回的稳定事件/Outbox，不参与 Proposal completion 事务。相同 Commit + config revision 只有一个自动 SyncRun。自动失败不反向修改 Proposal/Writeback。

## API And UI

- Remote config GET/PUT/DELETE/Test。
- Sync status/current run/list/detail/manual create/retry。
- Conflict detail 只返回 Commit/file metadata 和有界 Diff，不返回 Credential 或任意 stderr。

Settings 工作区分类拥有 Remote 配置与同步状态。前端唯一 `web/src/api/git-sync.ts` strict decoder；Query/mutation 绑定 Workspace/config revision/run。Secret 只在受控 input 中存在，提交后立即清空且不进入 Browser Storage。

## Compatibility And Rollback

Remote capability 独立装配；默认 auto-sync off。禁用后本地 Workspace、Safe Writeback、History、Search 均继续工作。Migration additive；禁用/回滚保留 encrypted config 和 SyncRun audit，生产使用 forward fix。

## Risks

- Credential 泄露：AskPass、环境 allowlist、递归 redaction、argv/config/log scan。
- Git 结果未知：durable checkpoint + ref/worktree post-check，无法证明则 manual recovery。
- 与 Safe Writeback 并发：Workspace-scoped Git operation lease/lock；Sync 与 Approval/writeback preflight 互斥。
- 拉取后索引失败：状态分离，不回滚 Git。
