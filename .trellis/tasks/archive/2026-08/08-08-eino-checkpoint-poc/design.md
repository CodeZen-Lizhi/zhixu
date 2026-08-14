# Eino Checkpoint / Interrupt PoC 设计

## 1. 范围

实现位于独立 `poc/eino/checkpoint` module package。生产主模块、数据库、River Worker、Workflow DTO 和 Composition Root 均不修改。

```text
AttemptScope -> opaque checkpoint ID
                   |
                   v
Eino Graph -> StatefulInterrupt -> encrypted TTL Store
                   |
             ResumeWithData
                   |
                   v
              terminal output -> explicit Delete
```

## 2. Scope 与 Fence

`AttemptScope` 包含 Workflow Run、Node Run、Attempt 和 Fence。使用独立 HMAC key 对版本化 canonical tuple 求摘要，生成不含原始 ID 的 checkpoint ID。任何字段变化都会得到不同 key；wrapper 在调用 Eino resume 前先确认该 key 仍存在。

## 3. Store

- AES-256-GCM；checkpoint ID 作为 AAD，避免跨 key 搬运密文。
- 每次 `Set` 使用新随机 nonce；内存只保存 `nonce + ciphertext + expires_at`。
- `Get` 返回副本；过期时原子删除并返回不存在。
- `Delete` 幂等；成功 resume 后由 wrapper 显式调用。
- Mutex 保护并发；所有方法先检查 context。

该 Store 只用于验证 Eino 生命周期合同，不是生产持久化实现。

## 4. Graph

单个 approval node 首次读取不到 interrupt state 时调用 `StatefulInterrupt` 保存原输入；恢复时读取持久 state 和 `ResumeWithData` 的批准数据，只允许批准后输出终态。Graph 拓扑和 compile options 在同一 Runner 实例中冻结。
Store 拥有可响应 Context 的进程内生命周期闸门，串行化所有共享该 Store 的 Runner Start/Resume；同一 scope
的并发 Start 只能创建一个 checkpoint，同一 interrupt 的并发 Resume 只能发布一个终态。该闸门只证明 PoC
的单进程唯一执行，不是生产
分布式 fence 或持久幂等实现。

## 5. 生产边界

当前 Worker 启动会生成新 owner，lease reclaim 会关闭旧 Attempt 并创建新 Attempt；Human Task 也会结束当前 Attempt。PoC 不设计旧 checkpoint 的 fenced handoff，所以不能接入这些路径。生产采用需要新的 ADR、数据库 Schema、加密密钥轮换和跨 Attempt 一次性转移协议。
