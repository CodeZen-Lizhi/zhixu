# 119 人工全文保存层只读审查

2026-09-15，impact-check，续 seq15325 / seq15611。只审本批保存层；未重新审纯 merge，未读取 120 实现，未改生产代码、迁移、主 schema 或 atlas.sum。

## 修复复核结论（119 已关闭）

2026-09-15 收到 main 转交 native `manuscript_storage_finish` 修复后复核：`Prepare` 的保存事务直接使用 `s.uow.Within`，保留原始 DB error，在 23505 精确恢复分支之后才调用 `synthesisDBError`。原 P2 的错误链丢失根因已消除；其他调用继续沿用既有分类包装。

新增 `synthesis_manuscript_store_test.go:151` 并发实库回归用 Baseline barrier 确保两请求都越过首查后再放行，断言两方成功且完整 attempt 相同、capture/attempt 各一行、同键异参拒绝。直接读取 `/tmp/manuscript-store-119-review-fixed.log` 确认 PASS 15.211s；main 确认该运行使用含119+120的正式完整迁移目录，另有 BodyRefresh PASS48.762s、vet通过。复用这些证据，不重复运行实库。

原 P2 已关闭。本轮仍是只读复核，未改实现；生产 Baseline/Proof、双基线发布和跨节点许可等原切片限制保持。

## 原发现与修复依据

**P2：并发同键 Prepare 的恢复分支无法识别唯一键冲突。**

- 位置：`internal/organizing/adapter/postgres/synthesis_manuscript_store.go:187`，关联同文件 `:443`、`internal/organizing/adapter/postgres/synthesis_store.go:594`、`internal/organizing/adapter/postgres/synthesis_models.go:198`。
- 触发：两个相同 command 的 Prepare 都在首查时未找到 attempt，随后分别捕获并落库；胜出方提交，另一方 INSERT attempt 触发 `(workspace_id,idempotency_key)` 唯一约束。
- 可验证调用链：`within` 在返回前调用 `synthesisDBError`；23505 分支返回 `synthesisConflict`，后者用新的 `errors.New(message)` 替换底层 cause。`platformpostgres.SQLState` 只从错误链提取 `*pgconn.PgError`，所以外层 `SQLState(err)=="23505"` 为 false，无法执行 `recoverAttempt`。
- 影响：并发精确重放的失败方收到版本冲突，不能按代码声明恢复胜出方完整 attempt。失败事务回滚，后续独立重试可以恢复，不是重复数据或正文覆盖问题。
- 建议：在错误分类前处理唯一冲突恢复，或保留底层错误链；仍须用完整 command 比较拒绝同键异参。补两请求同时越过首次查询的有界并发回归，断言都返回同一 attempt/capture，且仅保存一套记录。
- 状态：原只读发现已通过 channel seq16319 通知；现由 native implement 修复并补并发实库回归，复核关闭，详见上节。

## 已检查范围

1. `internal/organizing/application/synthesis_manuscript_store.go`：具名 command、server baseline、scope proof、authority/capture/receipt 契约。
2. `internal/organizing/adapter/postgres/synthesis_manuscript_store.go`：捕获前授权、事务内重验与 F CAS、恢复、seal、receipt/revision 精确绑定。
3. `internal/organizing/adapter/postgres/synthesis_models.go`：仅本批 v2 序列化/重读 diff。
4. `internal/organizing/adapter/postgres/synthesis_manuscript_store_test.go`：真实 PG/文件/Git fixture、故障恢复、跨 workspace、非法捕获、篡改与 v2 原子闭包。
5. `atlas/migrations/00119_synthesis_manuscript_storage.sql`：capture/attempt/receipt 不可变与复合 FK，v2 receipt、model/apply/generated closure，v1 原约束兼容。

除上述 P2，未发现本范围内有证据支持的新增 P1/P2。Prepare 在文件读取前使用事务 scoped Proof，并独立比较完整 authority；落库和首次 seal 再验 authority/F。GetAttempt 重验 canonical payload/hash、冻结 merge/parser 结果；已提交 clean receipt 的恢复早于当前 authority/F 检查。SQL 保留旧 revision/source/body-reference/Authoring closure；v1 仍由 counts 约束配合 item_count>=1 禁止空 Items，v2 才允许零可信项且须确切 receipt。

## 验证与限制

- 本轮执行 `go vet -tags=integration ./internal/organizing/application ./internal/organizing/adapter/postgres`：通过（exit 0），含相关包编译检查。
- 本轮限定五文件 `git diff --check`：通过。
- 复用 body-refresh 实库证据：直接读取 `/tmp/manuscript-store-119-final.log`，当前内容为 postgres PASS **10.832s**（更新前通知为10.926s）；测试含真实文件/Git、seal 响应丢失恢复、冲突/absence/空文件/NUL/超限/跨 workspace、篡改、v1/v2 receipt 完整事务。该日志不包含并发同键 Prepare 回归。
- 该实库运行采用 1..119 独立 overlay；root 已统一主 atlas.sum 含119+120，但本审查没有把此前 overlay 结果写成正式主目录最终迁移验证，主目录/导出验证由 root 收口。
- BaselineReader/Proof 尚无生产实现。测试明确仅包含真实 source fence，部分模型/semantic/root grant 身份为 fixture。精确 L/P/current owner、generation/semantic、scope/source、root grant 的生产授权证明仍需后续 composition，不能用本次 PASS 宣称完成。
- 未来跨 merge_review/HumanWait/apply 必须分离冻结模型 Node 身份与当前执行许可；Authoring F/P 双基线、冲突持久裁决、HTTP/工作流/发布尚未接线。均为本批已声明边界，不作为新增 bug。

共检查 5 个目标文件，发现 1 个 P2；实施方修复 1，复核关闭 1，开放 0。未提交、推送或发布。
