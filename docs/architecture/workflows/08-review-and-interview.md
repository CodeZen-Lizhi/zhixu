# 智能复习与面试模拟流程

## 1. 目标

将批准 Claim 转化为可评估的主动回忆与间隔重复。

## 2. Deck 创建

输入：

- Topic/Collection/Artifact/Role。
- Difficulty。
- Card Types。
- Daily Limit。

## 3. Card 生成

```mermaid
flowchart TD
    A["Approved Claims"] --> B["Generate Questions/Answer Points"]
    B --> C["Deduplicate"]
    C --> D["Citation Review"]
    D --> E{"User Approval"}
    E -->|"Edit/Reject"| B
    E -->|"Approve"| F["Active Card + Schedule"]
```

## 4. Review

```mermaid
flowchart TD
    A["Due Card"] --> B["Show Question"]
    B --> C["User Answer"]
    C --> D["Evidence-based Scoring"]
    D --> E["Review Scoring"]
    E --> F["Show Errors/Omissions/Citations"]
    F --> G["FSRS Schedule"]
    F --> H{"Gap?"}
    H -->|"Yes"| I["Source/Follow-up/Learning Path"]
```

## 5. Scoring

- Correctness。
- Coverage。
- Boundaries。
- Clarity。
- Confidence。
- Errors/Omissions。

## 6. 调度

- Answer + Schedule 同事务。
- Idempotency Key。
- Scheduler Version。
- Pause/Reset。

## 7. 面试模拟

```mermaid
sequenceDiagram
    participant U as User
    participant I as Interview Agent
    participant R as Retrieval
    I->>R: Select evidence-backed question
    I-->>U: Question
    U->>I: Answer
    I->>R: Claims/Evidence
    I-->>U: Follow-up
    I-->>U: Final Coverage Report
```

## 8. 失效

Claim Superseded/Disputed/Source Broken：

- Card Invalidated。
- 从 Due 移除。
- 创建 Health Issue。

## 9. 知识缺口

- 推荐 Source。
- 追问题。
- 学习路径。
- Proposal Candidate。

不自动改知识。

## 10. 验收

- 卡片绑定证据。
- 评分解释。
- 重复提交幂等。
- Claim 失效卡片停止。
- 面试报告有行动建议。

