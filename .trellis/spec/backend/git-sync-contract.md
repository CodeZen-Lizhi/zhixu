# Git 远端同步契约

本规范锁定一个 Workspace 与一个标准 HTTPS Git Remote 之间的受控同步边界。它不扩宽 Safe Writeback 的 Git 接口，也不把 Git 同步宣称为数据库或完整 Workspace 备份。

## 模块边界

- `internal/gitsync/domain` 拥有 RemoteConfig、Credential Context、SyncRun、Attempt、方向、失败分类和状态转换。
- `internal/gitsync/application` 拥有配置命令、运行创建、自动调度、执行编排、Outbox Worker 和捕获 follow-up。
- `internal/gitsync/adapter/postgres` 是配置 Revision、密文、Run、Attempt、Outbox、幂等回执和租约事实源。
- `internal/gitsync/adapter/remoteurl` 拥有 HTTPS URL 规范化、DNS 和 SSRF 策略。
- `internal/gitsync/adapter/security` 加载 Git 主密钥并拥有 Git 凭据 envelope、AAD 和稳定错误映射；不拥有业务配置。
- `internal/platform/secretstore` 是 Model Settings 与 Git Sync 共用的 AES-256-GCM key/nonce/envelope 原语；
  它不拥有任一业务的 AAD、持久化 shape 或错误语义。
- `internal/platform/gitcli` 只暴露固定领域动作。调用方不能提交原始 Git 参数、任意 refspec 或环境变量。
- `internal/platform/gitoperation` 是 Git Remote Sync、Safe Writeback 和文件历史共享的 Workspace 级 Git 操作互斥边界。
- Fast-forward 后的资料更新通过 `internal/gitsync/adapter/sourcecapture` 调用既有 external-change capture 契约；Git 成功与索引结果必须分列。

## 配置与凭据

- 每个 Workspace 首版最多一个活动 RemoteConfig；Remote URL 只接受标准 HTTPS，禁止 userinfo、query、fragment、重定向和不安全解析地址。
- 配置保存使用 `ExpectedRevision` CAS 和 `Idempotency-Key`。响应丢失后的 exact replay 必须在 DNS 或当前状态读取前返回原回执。
- Secret action 只有 `keep|replace|clear`。`keep` 只能沿用同一个 normalized URL 与 branch 的凭据；Remote 或 branch 改变时必须 `replace`。
- Token 不可回读。公开投影只有 `token_configured`，不得返回密文、掩码原文或密钥路径。
- Git Token 通过共享 `secretstore.Sealer` 使用 AES-256-GCM，但 Git wrapper 必须独立构造 AAD：purpose 固定为
  `git-remote-token`、schema 固定为 `git-remote-token/v1`，并绑定 Workspace、config revision、normalized URL 和 branch。
  任一绑定变化都必须导致解密失败；共用加密原语不允许复用 Model Settings 的 AAD 或把其他业务密文当作 Git Token 打开。
- 主密钥文件必须是 canonical absolute path、非 symlink、私有普通文件，权限只允许 `0400` 或 `0600`，内容为单行 Base64 编码的 32 字节密钥。
- 明文 Token 只允许存在于请求边界、短生命周期可清零 buffer 和受控 AskPass 子进程环境；不得进入 URL、argv、Git Config、Workspace、日志、审计或错误链。

## 运行创建与配置栅栏

- 所有手动、自动和重试同步先持久化 `SyncRun` 与 `EXECUTE_RUN` Outbox，再执行 Git。
- 同一 Workspace 最多一个活动 Run；幂等键与 request hash 精确绑定 trigger、retry binding 和自动运行的 expected config revision。
- 手动运行与重试读取创建时的当前配置；调用命令不得伪造配置版本。
- 自动候选由完成的 writeback Commit 与扫描时配置 Revision 组成。调度器必须把该 Revision 同时写入幂等键和 `ExpectedConfigRevision`。
- 自动创建在应用层拒绝已变化的配置，并在 PostgreSQL 事务内再次比较 current revision、URL、branch、token 和 auto-sync；竞态返回 `GIT_SYNC_CONFIG_STALE`，不得创建绑定新配置但沿用旧幂等键的 Run。
- 每个 Run 永久绑定创建时的 config revision、URL 和 branch。执行时只按该 Revision 打开凭据，旧 Run 不能使用新 Token。

## Git 状态机

```text
PENDING -> FETCHING -> COMPARING
  -> SUCCEEDED                         # same
  -> FAST_FORWARDING -> VERIFYING -> SUCCEEDED
  -> PUSHING          -> VERIFYING -> SUCCEEDED
  -> CONFLICT | FAILED | STALE | MANUAL_RECOVERY_REQUIRED
```

- Fetch 使用独立受控 tracking ref；Remote URL、branch、refspec 和 Git 环境由 Adapter 构造。
- Compare 必须重新读取 attached branch、HEAD、工作树/index、remote OID 和 ancestry，不能复用配置测试或旧页面状态。
- Compare 的 `changed_files` 是按 Git 输出顺序保留的前 `500` 条预览，不是完整变更清单。Adapter 必须继续解析并校验上限后的全部记录；尾部存在非法状态或路径时整次 Compare 以 `GIT_SYNC_STATE_CORRUPT` 失败，不能因截断而隐藏坏数据。Fast-forward 后的 Source capture 仍扫描实际 Git tree，不得把预览当作捕获输入。
- 两端相同只记录成功，不制造文件或 Git 变化。
- Remote 单向领先时，只有 strict clean 且 local 是 remote ancestor 才允许 fast-forward；更新后复读 HEAD 与工作树证明结果。
- Local 单向领先时，只允许显式 non-force push；push 后重新读取 remote OID，只有与 local OID 相同才成功。
- dirty、detached、diverged、ref drift、non-fast-forward 或无法证明外部结果时停止。禁止自动 merge、rebase、force push、reset、checkout 或冲突解决。
- 网络/进程响应丢失后不得盲目重复有副作用的操作。Attempt checkpoint、expected OID 和 post-check 决定可重试、冲突或 `MANUAL_RECOVERY_REQUIRED`。
- `MANUAL_RECOVERY_REQUIRED / GIT_SYNC_RESULT_UNKNOWN` 只用于 `FAST_FORWARDING|PUSHING|VERIFYING` 等可能已发生外部突变的阶段。Fetch、Compare 或只读元数据解析失败必须落为普通失败、依赖不可用或状态损坏，不能伪装成未知突变。
- Unix 运行时取消 Remote Git 命令时必须终止其独立进程组并设置有界 `WaitDelay`，确保 `git-remote-https` 等 helper 不会在父进程退出后继续持有 pipe 或 `config.lock`。

## 一致性、恢复与索引

- Git Remote Sync 与 Safe Writeback 必须持有同一个 Workspace Git operation lock；等待或取消不能绕过锁执行。
- Run、Attempt、lease、checkpoint 和 Outbox 是持久事实。Worker restart 通过 claim/lease 和 exact replay 恢复，不把内存进度当事实源。
- Fast-forward 成功后创建独立 capture/index follow-up。Git Run 保持 `SUCCEEDED`，索引使用 `PENDING|RUNNING|SUCCEEDED|FAILED` 独立投影。
- 索引失败不能回滚 Git；只重试 follow-up。自动同步失败不能回滚已完成 Proposal、Commit 或 Active Index。
- 配置删除或变更不会删除历史 Run/Attempt/Audit；旧 pending Run 在执行前因配置栅栏进入 stale。
- Capability 可独立关闭。关闭 Git sync 后，本地 Workspace、Authoring、Safe Writeback、History 和 Search 仍继续工作。

## HTTP 与公开投影

- API 只提供配置 GET/PUT/DELETE/Test、状态、Run 创建/列表/详情和显式重试。
- Handler 使用 strict JSON、Workspace-scoped route、受控超时、稳定 Problem code 和 `Idempotency-Key`；未知字段、重复字段和非空无 body 请求必须拒绝。
- 配置响应不含 Secret；Run 错误只返回稳定分类码和有界业务元数据，不返回原始 stderr、解析 IP、AskPass 路径或 Token。
- `current_run` 与历史列表必须保持 Workspace 绑定；不存在当前 Run 使用 `null`，不能伪造空 Run。

## 场景：有界差异投影与可取消 Remote 命令

### 1. Scope / Trigger

- 修改 Git Compare 输出、Run 的 `changed_files` 投影、Remote 子进程执行或取消语义时，必须遵守本场景。
- 该边界防止大仓库因投影上限进入错误的人工恢复，也防止取消后遗留 helper 与 Git 配置锁。

### 2. Signatures

- Git Adapter：`RemoteClient.Compare(context.Context, application.GitAccess) (domain.Comparison, error)`。
- 解析器：`parseRemoteChanges([]byte) ([]domain.FileChange, error)`。
- 进程配置：`configureRemoteCommandCancellation(*exec.Cmd)`。
- HTTP/DB：`SyncRun.changed_files` / `changed_files` JSON 数组长度为 `0..500`；本场景不新增环境变量。

### 3. Contracts

- 解析器完整消费 `git diff --name-status -z -M`，只把前 `domain.MaxChangedFiles` 条写入 Comparison/Run。
- 每个 status/path 字段都必须以 NUL 终止；普通状态只接受 `A|M|T|D`，Rename 只接受 `R0..R100`
  并精确消费 old/new path。缺少末尾 NUL、非法 score 或截断 Rename 必须整体失败，不能返回部分预览。
- 前 `500` 条保持 Git 顺序；达到上限时 UI 明示“显示前 500 个，可能还有更多”，不宣称总数恰为 500。
- `A|M|T|D|R0..R100` 之外的状态、截断 rename、非法相对路径在预览尾部同样必须拒绝。
- Unix Remote 命令拥有独立进程组；Context 取消向整个组发送终止信号，并在两秒 `WaitDelay` 内收敛 pipe/helper。

### 4. Validation & Error Matrix

| 条件 | 结果 |
|---|---|
| 合法变更超过 500 条 | Compare 成功，持久化前 500 条预览 |
| 第 501 条以后格式或路径非法 | `FAILED / INTERNAL / GIT_SYNC_STATE_CORRUPT` |
| Fetch/Compare 只读命令异常 | 普通可分类失败，不进入 `RESULT_UNKNOWN` |
| Mutation 阶段响应丢失且 post-check 无法证明 | `MANUAL_RECOVERY_REQUIRED / GIT_SYNC_RESULT_UNKNOWN` |
| Context 取消 Remote 命令 | 父 Git 与 helper 一并退出，不遗留 `config.lock` |

### 5. Good / Base / Bad Cases

- Good：`952` 条合法差异返回前 `500` 条，页面显示有界预览，后续 capture 读取真实 tree。
- Base：少于 `500` 条时完整展示；两端相同返回空数组。
- Bad：先截断再停止解析，或把只读解析失败映射为未知外部突变。

### 6. Tests Required

- `TestParseRemoteChangesKeepsBoundedPreviewAndValidatesTail`：断言长度、顺序及非法尾部拒绝。
- `TestParseRemoteChangesRejectsMalformedStatusAndTermination`：断言 NUL 终止、双路径结构以及 Rename score 严格校验；score 后缀只能是 ASCII 数字 `0-100`，不得接受 `+`、`-` 或其他 `Atoi` 可解析形式。
- `TestRunExecutorDoesNotReportReadOnlyComparisonAsUnknownMutation`：断言 Compare 失败不会进入人工恢复。
- `TestRemoteClientCancellationKillsProcessGroupAndReleasesConfigLock`：断言取消有界返回、helper 退出且锁文件消失。
- 前端组件测试断言 `500` 条时出现有界预览提示；真实浏览器检查长列表无横向溢出。

### 7. Wrong vs Correct

```go
// Wrong: 到达投影上限后停止解析，尾部坏记录被隐藏。
if len(changes) == domain.MaxChangedFiles { break }

// Correct: 继续验证所有记录，只限制持久化预览。
if len(changes) < domain.MaxChangedFiles { changes = append(changes, change) }
```

## 验证门禁

- Domain/Application：状态转换、幂等重放、配置漂移、自动候选 revision、取消和 response loss。
- PostgreSQL：真实事务、唯一活动 Run、CAS、keep binding、Outbox、lease/checkpoint、migration Up/Down guard 和并发。
- Git：真实临时仓库覆盖 same、pull、push、dirty、detached、diverged、ref drift、post-check 和共享锁。
- Git 取消：Unix 下覆盖父 Git、remote helper、pipe 与 common-dir `config.lock` 的共同收敛。
- Security：SSRF、DNS re-resolution/pinning、redirect/proxy、argv/config/log/error redaction、key file 模式、共享
  `secretstore` 原语、Git 独立 purpose/schema、AAD 篡改和 buffer 清零。
- Composition：API/Worker capability、空密钥禁用、有效密钥启用、Worker restart 和安全关闭。
- 公共契约：OpenAPI drift、Go race/vet/tidy、相关集成测试和 `git diff --check`。
- 浏览器：真实 API/Worker 下覆盖配置、连接测试、持久 Run 恢复、diverged 冲突、双方 OID、`500` 条预览提示和重试入口；桌面与 `390x844` 均检查 Console、Secret 回显和横向溢出。

## 禁止模式

- 不在 Change Control `GitRepository` 上增加 fetch、push、remote 或任意命令能力。
- 不把 Remote URL 或 branch 当作凭据 AAD 之外的可变显示字段后继续 `keep`。
- 不用当前配置替换自动候选的 expected revision。
- 不把 Git 命令退出码直接映射为成功，不吞掉 post-check 或 result unknown。
- 不将索引失败映射为 Git 失败，也不为恢复索引撤销 Git。
- 不记录原始 adapter error、URL userinfo、凭据、密钥路径或完整外部 stderr。
