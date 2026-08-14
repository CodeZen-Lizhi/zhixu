# Eino Checkpoint / Interrupt PoC 实施

1. 将独立 PoC module 的 Eino core 升级并锁定到 `v0.9.13`。
2. 实现 AttemptScope opaque key 与加密 TTL `CheckPointStore`/`CheckPointDeleter`。
3. 实现只允许同一 scope 恢复的 Interrupt/Resume Runner，并在成功后显式 Delete。
4. 覆盖 interrupt/resume、fence/attempt 隔离、密文/AAD/篡改、TTL/Delete、取消和并发 race。
5. 更新 `poc/eino` 报告，明确 PoC-only 与生产重开条件。

验证：

```bash
cd poc/eino
go mod tidy -diff
go test -race ./...
go vet ./...
```
