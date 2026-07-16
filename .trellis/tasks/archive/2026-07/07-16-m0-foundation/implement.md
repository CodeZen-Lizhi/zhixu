# M0 基线执行计划

1. [x] 扫描当前文档和仓库事实，分别提取后端、前端、数据库、错误、日志、测试和安全约束。
2. [x] 用 `apply_patch` 填充 backend/frontend Spec；每份加入事实引用和验证门禁。
3. [x] 将版本策略、License 待决项和 canonical 命令写入本任务设计（不修改业务文档）。
4. [x] 创建 implement/check 上下文清单并运行 Trellis 校验。
5. [x] 审查 diff，确认未生成业务代码、未引入未经验证的版本号或依赖。

## 验证命令

```bash
rg -n 'TBD|To be filled' .trellis/spec/backend .trellis/spec/frontend
python3 ./.trellis/scripts/task.py validate 07-16-m0-foundation
git diff --check
git status --short
```

## 回滚

本任务只涉及 Spec 和任务文档；若发现错误，按文件回退对应补丁，不使用破坏性 Git 命令，不触碰用户已有文件。
