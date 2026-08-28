# Change Control Planning Review

## Review Scope

独立审查覆盖 `prd.md`、`design.md`、`implement.md`、context manifests，以及 Change Control、Approval Dispatch、Foundation、Events、Workflow、Model Settings 的现有代码、迁移和 integration fixtures。

## Findings And Resolutions

1. TODO 9 的 Workflow GORM fixture 未写出强制 enqueue fence 的真实装配链，且可能误用 no-op fence或不同 Pool。
   - 已固定为同一 `platformpostgres.Pool` 下的 Audit GORM、固定测试密钥 Sealer、Model Settings GORM fence、Events GORM、Change Control scoped cancellation guard、Workflow scoped River runtime、Approval Dispatch 顺序。
2. Dispatch response-loss 若错误包装 Workflow runtime 的 UoW不会命中 caller-owned commit。
   - 已明确分别包装主 Repository 和 Dispatch Repository 自身的 UoW；Workflow `StartScoped` 不拥有 commit。
3. Authorization create/get/consume/revoke 被错误概括为一个锁序。
   - 已按四个 legacy 方法拆分 DB clock、row lock、trigger binding、expiry/replay与 CAS 顺序。
4. Checkpoint response-loss 被错误泛化为同命令 exact replay。
   - 已冻结按方法恢复协议：Checkpoint 通过 `GetWritebackExecution` + Application `Resume` 继续；只有具备既有 durable receipt/binding 的方法允许 exact replay。
5. Downstream proposal 的 `impact_report FOR UPDATE` 与 Artifact/Review owner `FOR SHARE` 未进入矩阵；migration `00015` 未列入 gate。
   - 已补锁序、并发漂移场景、reindex completion guard和 migration 清单。
6. Writeback Create 与 Checkpoint/Publish 的 Proposal/Execution 锁语义曾被合并。
   - 已拆成 Create trigger binding，以及 Checkpoint/Publish 的 immutable proposal lookup -> Proposal `FOR UPDATE` -> Execution `FOR UPDATE`。
7. `foreign scope` 描述超过 Foundation 现有能力。
   - 已收窄为拒绝 nil、非 platform 类型与 stale scope；active cross-Pool scope由同一 Pool Composition/TODO 9 门禁保证。
8. Proposal create conflict replay 与无 Event 的 Approval replay 被错误描述为 commit。
   - 已冻结为内部 rollback sentinel：Proposal conflict 在 UoW rollback 后从 root `GetProposal`；Approval 无 Event replay rollback/no commit，有 Event replay才 exact append并commit。

## Baseline Verification

- `task.py validate`：通过；仅有大型规范文件注入截断 warning。
- Change Control、Events、Workflow、Model Settings、Audit 相关包 compile-only：通过。
- `go vet -mod=vendor ./internal/changecontrol/...`：通过。
- Markdown/JSONL trailing whitespace scan：通过。
- `git diff --check`：通过。

## Gate Status

独立 post-fix re-review 确认无剩余 P0/P1，规划可激活。P2 文案已同步：fixture 使用固定测试密钥 Sealer，并在 PRD 中补充 Model Settings scoped enqueue fence 的 TODO 9 依赖。
