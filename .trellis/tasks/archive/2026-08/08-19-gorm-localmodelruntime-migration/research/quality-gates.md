# Local Model Runtime 质量门禁

- Go review：scope 生命周期、typed nil、context/cause、UoW rollback/commit error、Rows Close/Err、JSON/nullability、错误链和敏感信息。
- SQL review：固定 SECURITY DEFINER function 调用、参数化/casts、DB time、CAS/idempotency、Repeatable Read snapshot、权限、锁竞争和 EXPLAIN。
- Trellis review：legacy consumer/production wiring 未切换、无第二 pool/AutoMigrate/selector/fallback，研究证据与清单一致。
- 当前可执行门禁以局部 test/race/vet/compile/diff 为主；真实 PostgreSQL 未配置，不能把 integration compile-only 当作 AC 证据。
