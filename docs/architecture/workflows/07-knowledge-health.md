# 知识健康维护流程

## 1. 目标

持续发现、去重、解释和修复知识质量问题。

## 2. 触发

- 手动。
- 每日/每周/Cron。
- Revision Published。
- Relation/Conflict 变化。
- Index Validation。

## 3. 检测

- Orphan。
- Duplicate。
- Conflict。
- Stale。
- Missing Source。
- Low Confidence。
- Broken Reference。
- Index Error。
- Superseded Usage。
- Review Invalidated。

## 4. 流程

```mermaid
flowchart TD
    A["Determine Scope"] --> B["Run Detectors"]
    B --> C["Build Fingerprint"]
    C --> D{"Existing + Evidence Same?"}
    D -->|"Yes"| E["Update Last Verified"]
    D -->|"No"| F["Create/Reopen Issue"]
    F --> G["Severity + Evidence"]
    G --> H{"User Action"}
    H -->|"Ignore"| I["Store Reason/Fingerprint"]
    H -->|"Repair"| J["Repair Proposal"]
    H -->|"Rescan"| B
```

M7-03 的 Scan 是 PostgreSQL 持久事实并绑定 versioned Workflow/River node。每个 detector 分页保存 coverage、
typed checkpoint 和计数；单 detector 失败形成 PARTIAL/FAILED，不允许执行 missing-set resolve。Workspace、Topic、
Smart Collection scope 均在启动时冻结 binding；Collection ID/version/query hash/read-model revision/exact count 任一
漂移都 fail closed，不能退化为 Workspace scan。

## 5. 定时

- 相同扫描不并发。
- 错过只补跑一次。
- 低优先级。
- 只创建 Issue/Proposal，不自动批准。

## 6. Fingerprint

- Type。
- Target。
- Evidence。
- Object Version。
- Detector Version。

`identity_hash = schema + workspace + issue_type + canonical_target + detector_id`；`fingerprint` 再加入排序后的
Evidence hash、target object version 与 detector version。相同 identity+fingerprint 只更新 `last_verified_at`；
fingerprint 变化复用原 Issue ID、追加 observation/evidence 并进入 REOPENED，保留历史 decision。

## 7. Severity

- Critical：错误/不一致。
- High：RAG/引用。
- Medium：组织。
- Low：建议。

## 8. Repair

- Orphan → Topic/Relation。
- Duplicate → Merge。
- Conflict → Conflict Workflow。
- Missing Source → Provenance。
- Broken Ref → Link Update。
- Stale → Verify/New Revision。

## 9. 失败

- Detector 失败独立记录。
- 扫描部分成功不标记完整成功。
- 大范围分页恢复。
- Retry exhaustion、取消、Worker 重启和 completion response-loss 从持久 checkpoint/receipt 恢复。
- Knowledge/Relation/Conflict/Index 已提交变化先写 typed affected-change outbox；投递失败不回滚源事务。

## 10. 验收

- Issue 不重复刷屏。
- 忽略直到证据变化。
- 每个 Issue 有证据。
- 修复经 Proposal。
- Schedule 默认关闭，DAILY/WEEKLY/Cron missed run 只补一次，同 scope 非终态 scan 不并发。
- REVIEW_INVALIDATED、Directory 以及无真实 apply seam 的 repair option 显式 unavailable，不写成零问题或假 Proposal。
