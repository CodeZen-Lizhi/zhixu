# Model Settings 质量门禁

- Go review：scoped port 生命周期、typed nil、context/cause、UoW commit/rollback、Rows、no-row、secret 与错误链。
- SQL review：参数化/casts、State/Runtime/Participant 锁序、DB time、CAS、trigger、Snapshot、权限与执行计划。
- Trellis review：legacy production wiring 未切换、GORM Bootstrap 同池、无 AutoMigrate/第二池/no-op fence，清单与证据一致。
- 当前只能执行局部 test/race/vet/compile/diff；真实 PostgreSQL 未配置，不得把 integration compile-only 当作 AC 证据。
