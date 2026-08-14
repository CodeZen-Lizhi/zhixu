# Eino Checkpoint Interrupt PoC

## Goal

验证同一进程、同一活跃 Attempt 内的 Eino Interrupt/Resume 与 checkpoint 生命周期边界。

## Requirements

- PoC 使用 Eino core `v0.9.13` 的 `StatefulInterrupt`、`ExtractInterruptInfo`、`ResumeWithData` 和 `CheckPointStore`。
- 范围只允许同一进程、同一活跃 Attempt/Fence 的短期暂停与恢复；不得接入生产 River Worker 或宣称支持进程崩溃/HITL 长等待。
- Checkpoint ID 必须绑定 Workflow Run、Node Run、Attempt 和 Fence，且不暴露原始身份。
- Store 必须实现有界 TTL、显式 Delete、并发安全和静态加密；原始 checkpoint 不得以明文保存。
- 成功恢复后由项目 wrapper 显式删除 checkpoint；过期或 fence/attempt 变化必须 fail closed。
- PoC 结论必须说明 Eino 不提供业务事实源、默认 Store、自动 TTL 或跨 Attempt checkpoint 转移。

## Acceptance Criteria

- [x] 第一次运行真实触发 Eino interrupt，第二次用 interrupt ID 和同一 checkpoint 恢复到唯一终态。
- [x] 不同 Attempt/Fence 无法读取或恢复旧 checkpoint。
- [x] 原始 store 内容不含敏感输入，篡改或错误 AAD 无法解密。
- [x] TTL 到期与成功恢复都会删除 checkpoint，且测试覆盖并发访问和 context cancel。
- [x] `go test -race ./...` 与 `go vet ./...` 在 `poc/eino` 独立 module 通过。
- [x] 形成生产 No-Go/PoC-only 结论和重开条件。

## Notes

- Eino 的 `CheckPointStore` 只有 `Get/Set`；`CheckPointDeleter` 是可选接口，框架不会替项目自动管理全部生命周期。
