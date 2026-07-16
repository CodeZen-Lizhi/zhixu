# M0 契约收敛实施计划

1. [x] 修订 PRD 的 pagination、SSE、表格和导出表述。
2. [x] 补齐数据库物理模型、版本字段、约束与审计授权记录。
3. [x] 新增 Eino PoC 门禁 ADR、认证 ADR，并更新 technology/API/security/interface 文档。
4. [x] 更新 ADR 索引与需求追踪矩阵。
5. [x] 执行冲突搜索、链接检查、Trellis 校验和 diff 审查。

## 验证命令

```bash
rg -n 'WebSocket 或 SSE|"page"|Excel|CSV' docs/product/PRD.md docs/architecture
python3 ./.trellis/scripts/task.py validate 07-16-m0-contracts
git diff --check
```

## 回滚

仅按文档文件回退错误段落；不改已有 accepted ADR 内容，不使用破坏性 Git 命令。
