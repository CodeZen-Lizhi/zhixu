# Model Settings 基线

## Public surface

Legacy `Repository` 实现 Revision、Rollout、Runtime、RuntimeAvailability、Activation、Participant 六个 Application Store，共 25 个业务方法，并额外实现 pgx `CheckEnqueue`。Repository 持有 pgx DB、SecretSealer、legacy Settings Audit appender 与 Local Runtime `TxLifecycle`。

## Production construction

`internal/modelsettings/runtime.Bootstrap` 直接从 legacy pgx DB 构造 Audit Store、Local Runtime PostgresStore 和 Model Settings Repository；API、Worker、modelctl 是三个生产调用方。API/Worker 又把 legacy Repository 作为 River `EnqueueFence`。本 child 不改这些构造或 `cmd/**`。

## Stage boundary

新增 sibling GORM Repository、scoped Audit/Local Runtime/Workflow fence 和独立 GORM Bootstrap。legacy API 是临时 allowlist，退出条件是真实 PostgreSQL TODO 9 通过并由 Final 同时切换生产 Composition。
