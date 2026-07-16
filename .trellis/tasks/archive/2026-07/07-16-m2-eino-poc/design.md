# M2 Eino PoC 设计

## 隔离边界

```text
project contracts -> poc/eino adapter -> Eino compose/model/tool
        ^                    |
        |                    v
  deterministic fakes   optional live provider
```

- `poc/eino` 拥有独立 `go.mod`，主模块与正式 Domain/Workflow 不导入 Eino。
- Contract 使用项目语义的 Message、Embedding、Retrieval、Tool Authorization、Trace Context、Node Input/Output 和 Error Kind。
- Eino 仅在 Adapter 内转换类型；测试从 Contract 边界观察行为。

## 版本基线

- `github.com/cloudwego/eino v0.9.12`：当前稳定版。
- `github.com/cloudwego/eino-ext/components/model/openai v0.1.13`：当前稳定版 OpenAI-Compatible Adapter。
- 不采用 `v0.10.0-alpha.*`。

## 验证分组

1. Chat/Graph：`compose.Graph + Invoke`，Fake Model 验证成功、失败、取消。
2. Stream：消费、取消、Close 和 goroutine 回收。
3. Structured Output：Schema 校验与最多一次修复，不允许无限重试。
4. Tool：`WithTools` + 项目 Permission Gate，执行前授权，覆盖拒绝和未知工具。
5. Callback/Trace：项目 Trace ID 映射到回调，敏感字段脱敏。
6. Embedding/Retrieval/Rerank：通过项目 Interface 和 Fake Adapter 验证输入输出边界。
7. Node Executor：把一次短流程包装成可取消、可重试分类的 Node 执行，不保存 Eino Graph 为 Workflow 事实源。
8. Live Smoke：显式环境变量启用 OpenAI-Compatible endpoint；不配置则 Skip。

## 采用门禁

- 所有关键组必须 PASS，race/vet 必须通过，且无 Eino 类型泄漏到主模块。
- 如果只部分通过，报告必须判定“不采用”或限定采用范围；不能用文档承诺替代失败测试。

## 回滚

删除 `poc/eino` 与本任务报告即可；主应用、数据库和 API 契约不受影响。
