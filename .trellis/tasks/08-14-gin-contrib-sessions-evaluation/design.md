# 技术设计

## 1. 决策框架

本任务先执行 ADR-0019 的强制约束门禁，再计算加权覆盖率。候选必须同时满足唯一 PostgreSQL 事实源、共享连接/事务边界、即时撤销、数据库时钟、原子轮换、凭据不落库明文、独立 CSRF/Origin/Capability 以及现有 Go/Gin/部署基线。任一强制项失败即不接入。

## 2. 当前数据流

```text
Cookie 随机 Token
  -> HTTP 精确提取 / Origin + CSRF 校验
  -> Application SHA-256
  -> auth Repository 原子 PostgreSQL 认证 + last_seen
  -> Principal + Capability
  -> 业务 Handler
```

签发和轮换由 Application 生成随机 Token/CSRF，数据库只保存摘要并使用数据库时钟；Cookie 写入只投影已提交的数据库结果。撤销先提交 PostgreSQL，再清除 Cookie。

## 3. 候选路径

### A. 内置 PostgreSQL Store

`gin-contrib/sessions/postgres` 使用 `database/sql` 和 `pgstore` 自有表/序列化生命周期。它会建立第二连接/Store 事实源，且不能复用当前 `auth.session` 原子查询，不满足强制约束。

### B. 内置 GORM Store

`gin-contrib/sessions/gorm` 使用 `gormstore` 的 Session Values 模型。TODO 10 尚未完成；提前接入会形成 pgx/GORM 双轨和第二 Session 生命周期，不满足依赖及唯一事实源约束。

### C. Cookie Store

Cookie Store 把 Session Values 编码到客户端。项目要求客户端只持有随机凭证，撤销、Scope 和用户绑定由 PostgreSQL权威裁决；自包含 Values 会引入客户端可重放的第二状态，不满足强制约束。

### D. 自定义 Store

可以实现 Gorilla `Store.Get/New/Save`，但要保持现有语义，Store 仍需调用全部现有 Application/Repository 逻辑，还需处理 Gin middleware 延迟加载、错误日志、Save-before-response 和 Gorilla Registry。它不能删除认证、轮换、撤销、CSRF、Capability 或 Cookie 投影的核心代码，只增加框架类型和错误面，因此不构成有效覆盖。

## 4. 推荐决策

不把 `gin-contrib/sessions` 加入生产依赖。保留当前标准库 Cookie 投影与项目 Session Application/Repository；用 ADR 和静态合同检查固化原因。由于原始清单把 TODO 10 列为 TODO 11 前置，只有在用户明确批准后，才把本次评估作为 TODO 11 的最终关闭结论；否则它只是预研证据，TODO 11 保持未完成。框架未来只有在同时提供以下能力或项目边界变化后重新评估：

- 可直接复用项目统一 GORM transaction boundary 且不创建第二表/连接池；
- 支持 opaque token digest lookup、数据库时钟、即时撤销和原子 rotate CAS；
- 能删除现有实质代码，而非仅把其包装为 custom Store；
- 依赖图可按使用后端裁剪，且安全回归证明收益大于供应链扩张。

当前 Roadmap 所称 Session “版本”不映射为单行可变 version。既有实现用不可变 Session UUID、旧行撤销和新行创建表示轮换代际；本任务在文档中明确该语义，不引入无消费者的字段或第二状态机。

## 5. 契约补强

- 新增真实 PostgreSQL 并发登录测试：并发签发的两个 Session 都可独立认证，撤销其中一个不影响另一个。
- 新增静态采用状态合同，只验证 ADR 结构化标记与 module/vendor 没有未采用依赖。Router 内部组装不是稳定接口，不用源码文本断言锁定；Bearer 优先、CSRF/Origin、Capability 与 fail-closed 继续由 HTTP/Composition 行为测试保障。
- 记录既有 Auth `409`/OpenAPI 漂移，但不在本任务中只修正 Auth 管理路由：损坏 Session/API Token 可在全部受保护路由的认证 Middleware 阶段返回 `409`，局部 OpenAPI 加法仍会保留全局不一致。

## 6. 兼容与回滚

- 不改 API、OpenAPI、Cookie、数据库或运行时装配，因此没有数据迁移和发布兼容风险。
- 新增的合同检查只读 `go.mod`、源码/规范和决策矩阵；若误判，可单独回滚检查和 ADR，不影响运行时。
- 未来采用必须新建任务并通过本 ADR 的重新评估门禁；不得直接删除当前 Repository。
