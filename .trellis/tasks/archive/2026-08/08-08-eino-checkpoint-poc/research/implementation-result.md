# Checkpoint / Interrupt PoC 结果

## 决策

- Eino Interrupt/Resume 机制：**Go（能力验证通过）**。
- 当前生产 Worker/Workflow 接入：**No-Go（保持 PoC-only）**。

No-Go 的原因不是 Eino 缺少能力，而是项目当前 Worker 重启、lease reclaim 和 Human Task 会结束旧 Attempt。没有带 fence 的一次性 checkpoint handoff 时，旧 checkpoint 不能成为新 Attempt 的恢复事实。

## 已验证

- 独立 PoC module 已从 Eino `v0.9.12` 升级到 `v0.9.13`。
- 固定 Graph 第一次真实执行 `StatefulInterrupt`，通过 `ExtractInterruptInfo` 取得 resume ID；相同 scope 的 `ResumeWithData` 恢复原 state 并输出唯一终态。
- Checkpoint ID 对 Workflow Run、Node Run、Attempt 和 Fence 做版本化 HMAC；ID 不含原始身份，任一字段变化均无法读取旧 checkpoint。
- Store 使用 AES-256-GCM、每次新 nonce、checkpoint ID AAD、8 MiB 上限和最长 15 分钟 TTL；密文不含敏感输入，篡改和跨 ID 搬运均失败。
- 同一 scope 二次或并发 Start 不覆盖旧状态；同一 interrupt 并发 Resume 至多一个成功终态；Store 级生命周期
  闸门覆盖共享 Store 的多个 Runner，且等待可被 Context 取消。
- TTL 到期和成功 Resume 都删除 checkpoint；取消不会创建新 checkpoint。
- 64 路并发访问及 20 轮 `-race` 通过。

## 验证命令

```text
cd poc/eino && go test -race ./...        PASS
cd poc/eino && go test -race -count=20 ./checkpoint PASS
cd poc/eino && go vet ./...               PASS
cd poc/eino && go mod tidy -diff          CLEAN
git diff --check                          PASS
```

## 生产重开条件

生产采用前需要独立 ADR 和持久设计，至少包括：稳定 owner 或 fenced one-time handoff、PostgreSQL Store Schema、密钥轮换、TTL/cleanup worker、敏感状态分类、拓扑/serializer 版本迁移、跨 Attempt 重放与副作用幂等。River/PostgreSQL 仍必须是业务状态唯一事实源。
