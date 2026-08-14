# Checkpoint 与 Runtime Attempt 边界

## 官方能力

Eino `v0.9.13` 提供动态 `StatefulInterrupt`、interrupt context、`ResumeWithData` 和调用方实现的 `CheckPointStore`。核心 Store 接口只有 `Get/Set`；可选 `CheckPointDeleter` 只提供删除能力，未实现时不会自动清理 stale checkpoint。

## 当前项目冲突

- Worker 每次进程启动生成新的 worker identity。
- River delivery 的 lease owner 绑定该 identity；Runtime Claim 只有 owner 相同才可能复用当前 Attempt。
- lease reclaim 会把旧 Attempt 终结为 `lease_lost` 并创建新 Attempt。
- Human Task 暂停也会结束当前 Attempt，不满足“同一活跃 Attempt”限制。

因此“进程崩溃后恢复同一 Eino checkpoint”与当前 Runtime 所有权不兼容。安全选择是只做同一进程/Attempt PoC；跨 Attempt 需要稳定 owner 或带 fence 的一次性 checkpoint handoff，并重新设计 TTL、删除、加密、密钥轮换和敏感消息生命周期。
