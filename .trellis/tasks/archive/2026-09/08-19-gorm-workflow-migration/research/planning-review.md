# Workflow GORM 规划审查记录

## Go/API Review

初审发现并已修复：

- legacy `55000` 被错误规划为 consistency violation；现已固定为 legacy `dependency_unavailable`、retryable；
- Execution Fence snapshot 未冻结 nullable owner/lease；现已写出完整 Go 字段与 `Set` 标志；
- Runtime 可接收异池 producer；现改为公开 factory 只接平台 Pool/River Options/scoped fence，内部构造 producer；
- Fence 对 `55P03` 类别存在歧义；现固定为 legacy dependency unavailable、retryable。

复核后无剩余 P0/P1；最后一项 P2 已按建议修复。

## SQL/事务 Review

初审发现并已修复：

- Claim 错把 Model Settings 锁提前到首次 DB time；现冻结 exact-delivery Attempt、首次 DB time、replay/stale 早返回，以及仅新 Claim 才进入 Model Settings 锁和 freshness DB time 的两阶段顺序；
- Claim 的 Attempt 描述可能扩大成 history 锁；现固定 exact delivery 谓词、排序与 Limit；
- Human Wait/Submit 被错误合并为一条锁序；现分别冻结 Wait 的 Attempt-first 与 Submit 的 Task-first/replay/expiry 顺序。

其余 River 原子性、Hook scope 与 TODO 9 门禁无 P0/P1/P2。

## Trellis Review

初审发现并已修复：

- Pool + 外部 Client 不能证明同池；现由公开 factory 从 `pool.DB()` 内建 insert-only Client，再从同一 Pool 建 scoped producer；
- 70KB quality spec 会被上下文注入截断；现改为完整的任务内 `research/quality-gates.md`，主 agent 已完整读取原 spec。

`task.py validate` 当前 10 条 implement context、8 条 check context 全部通过且无截断 warning；`git diff --check` 通过。

## 审批与状态

用户连续确认继续模块化实施。Workflow child 可从 `planning` 进入 `in_progress`；TODO 9、生产 wiring、PRD AC、legacy 删除和归档仍未获准提前执行。
