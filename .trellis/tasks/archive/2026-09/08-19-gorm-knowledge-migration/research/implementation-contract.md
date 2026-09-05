# Knowledge GORM Implementation Contract

## Hard Boundaries

1. 一个platform Pool是唯一连接生命周期；GORM root、UoW、Events与Audit scoped collaborator必须同源。
2. legacy pgx Repository/Port与生产wiring保留；不做selector、双写、silent fallback或caller-owned pgx到GORM的伪桥接。
3. GORM新接口只暴露`foundation.TransactionScope`；禁止新增`any`、pgx、sql或GORM transaction类型。
4. Relation Apply不调用自开事务的Change Control GORM methods；跨Schema协议继续由单一Knowledge-owned UoW承载。
5. Impact GORM只能由`NewScopedImpactServiceWithAudit`在编译期绑定`ScopedImpactRepository`与`ScopedImpactAuditPort`；legacy Tx Port仅供legacy Repository。
6. Schema/trigger/migration是约束唯一事实；禁AutoMigrate、hook、association和implicit timestamps。

## Semantic Invariants

- receipt-first exact replay、Workspace isolation、canonical Relation endpoint、provenance binding和version CAS不变。
- deferred Claim/Relation/Conflict/lifecycle constraints在commit时生效，不能拆事务或提前吞错。
- Candidate-before-Proposal、Workspace-before-receipt、sorted endpoint/member锁序不变。
- Timeline projection每次只领取一条source，project/poison CAS与source event同一短事务。
- Impact Report+Audit、Relation Apply+optional Event均是all-or-nothing；stale needs_revision是唯一明确的例外提交。
- callback成功后的UoW错误不得猜测成功；只允许后续durable exact replay恢复。

## Completion Gate

- Static implementation只能证明compile、API shape、参数化和局部回归；不能证明真实driver、trigger、锁、SQLSTATE或commit语义。
- TODO 9不可用时task保持`in_progress`、PRD AC未勾、生产legacy不变。
- Final前不得删除legacy DB/constructors、Organizing pgx transaction path或cmd wiring。
