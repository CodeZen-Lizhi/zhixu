# 技术设计

## Application Contract

新增公共常量与类型：

```text
DefaultIssueHistoryLimit = 25
MaxIssueHistoryLimit = 100
IssueHistoryRequest { WorkspaceID, IssueID, Limit, Cursor }
IssueObservationPage / IssueDecisionPage
IssueHistoryPosition { At, ID }
```

- `IssueReadService.ListIssueObservations` 与 `ListIssueDecisions` 负责 request、cursor binding、repository 上界和 next cursor。
- `GetIssueDetail` 使用同一内部第一页逻辑组装两类默认页，不能调用无界 repository 方法。
- `IssueReadPort.GetIssue` 返回 `IssueSnapshot`，其中当前 observation 为非空值，并与当前 Issue fingerprint 精确绑定。
- `IssueDetail` 增加两类 `NextCursor`/`HasMore`。

## Cursor

- 保留现有 `health-issue-cursor/v1` 文档和 `Encode/Decode` 行为。
- 在同一 codec 增加 history 专用方法，schema 为 `health-issue-history-cursor/v1`。
- binding：`workspace_id`、`issue_id`、`history_kind`、`limit`。
- position：`at`、`id`。
- MAC 计算仍使用 JSON 去掉 MAC 后的 canonical struct marshal 与 HMAC-SHA256。

## Repository

Observation page：

```sql
WHERE observation.workspace_id=$1 AND observation.issue_id=$2
  AND observation.observed_at <= $after_at
  AND (observation.observed_at < $after_at
       OR (observation.observed_at = $after_at AND observation.id > $after_id))
ORDER BY observation.observed_at DESC, observation.id ASC
LIMIT limit + 1
```

Decision page 同构使用 `created_at`。

- 所有 `ORDER BY` 使用表别名限定原生 UUID/时间列；`SELECT id::text` 的输出别名不得劫持排序，否则现有顺序索引无法复用。
- 冗余但等价的 `timestamp <= cursor` 让深页游标进入 `Index Cond`，精确 OR 条件继续负责同时间戳 tie-break。
- 先读取 `limit+1` 判断 has-more，再裁掉 sentinel，最后仅为返回页 observation IDs 批量读取 evidence。
- evidence 使用 `observation_id=ANY($2::text[]::uuid[])`，类型转换只发生在参数侧，禁止 cast 索引列。
- 空页仍需区分“存在但无历史”与“不存在/跨 Workspace”；使用当前 Issue lookup 保持 404 语义。
- 详情固定最多 4 条 SQL 路径：Issue、observation page、evidence batch、decision page；空 observation 时跳过 evidence query。
- 独立 observation page 最多 3 条，decision page 最多 2 条，均与历史总量无关。

## HTTP/Wire

- 在同一路径为 `/decisions` 增加 GET，不改变既有 POST。
- `latest_observation` 为 required、非 nullable；`observations` 的详情首屏至少包含一条当前 Issue 历史。
- Page response 固定包含 `workspace_id`、`issue_id`、`items`、`next_cursor`、`has_more`。
- Detail response 增加：
  - `observations_next_cursor`
  - `observations_has_more`
  - `decisions_next_cursor`
  - `decisions_has_more`
- `next_cursor` 无下一页时按现有 wire 约定省略，OpenAPI nullable/required 语义与 decoder 保持一致。

## Web

- `healthQueryKeys` 增加 observation/decision history key。
- 两个 `useInfiniteQuery` hook 的 `initialPageParam` 为空 cursor，`getNextPageParam` 使用 `nextCursor`。
- Evidence Dialog 使用共享 Tabs 展示“当前证据 / 观测历史 / 决策历史”；历史 tab 激活后才启用相应 query。
- 详情请求成功时把两类首屏及其 cursor 写入对应 infinite-query cache；首次打开 Tab 不重复请求首屏，只有显式加载更多才消费 continuation cursor。
- 每页按 ID 去重只作为 UI 防御；decoder 仍拒绝单页重复，服务端测试保证跨页无重复。
- 加载更多错误单独呈现，不替换已加载历史。

## Compatibility And Rollback

- 新端点和字段是加法，但旧数组语义从完整改为第一页；已得到用户确认并通过 `has_more` 显式表达。
- 回滚 Web 历史 UI 不要求回滚新 API；回滚 API 时必须保留服务端硬上限，禁止恢复无界查询。
- 无 schema migration，应用二进制可独立回滚。
