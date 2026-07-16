# M2 Eino PoC 实施计划

1. [ ] 创建独立 module、锁定稳定依赖并建立 Project Contract 类型。
2. [ ] 实现确定性 Chat Model Fake 与最小 Graph Runner，覆盖成功/失败/取消。
3. [ ] 验证 Streaming 取消、Close 和资源回收。
4. [ ] 验证 Structured Output 校验与有限修复。
5. [ ] 验证 Tool Calling 权限隔离、未知工具和参数失败。
6. [ ] 验证 Callback/Trace、错误分类、限流和敏感信息脱敏。
7. [ ] 验证 Embedding、Retriever/Rerank 与 Node Executor Contract。
8. [ ] 增加显式可选 Live Smoke，不配置凭据时明确 Skip。
9. [ ] 运行 race/vet/测试、Go Review，生成 `report.md` 采用结论。

## Canonical Verification

```bash
cd poc/eino
go test -race ./...
go vet ./...
```

## 并行边界

- Contract/Runner：项目自有类型、Graph Runner、错误映射。
- Stream/Structured：流关闭、取消、JSON Schema 和有限修复。
- Tool/Trace：权限门禁、Tool Calling、Callback 和 Trace。
- 主 Agent 负责版本、依赖锁定、跨组集成、Live Smoke、Review 和最终报告。
