# Review Core Planning Review

## Result

独立只读规划审查未发现剩余 P0/P1。启动前已修复以下契约漂移：

1. `GetSession` 保持 legacy 的通用 Workspace-scoped Session shell lookup；只有 Start、Complete、SubmitAnswer 与 Answer replay 强制 REVIEW+Deck，避免把 Application 边界错误地下沉到通用查询。
2. 所有 GORM Raw Row/Rows/Exec helper 必须显式接收 caller context，并在 root/scoped DB 上先调用 `WithContext(ctx)`。
3. TODO 9 的 GORM commit-response-loss 不包装 opaque Pool/Begin；同包 integration 通过 UoW wrapper 让真实 `Within` 完成提交后返回受控错误，再验证 Repository 的 root exact lookup 恢复。

## Start Decision

- PRD、Design、Implement、baseline 与 manifests 已齐全；
- Foundation 前置已具备 staged GORM root/UoW；
- 生产 wiring、legacy pgx、Schema 与测试当前不改；
- TODO 9 真实 PostgreSQL 不可用，因此只批准 staged adapter 实施，任务保持 `in_progress` 且 AC 不勾选。
