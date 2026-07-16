---
status: accepted
---

# 使用 Git CLI Adapter

系统需要与用户已有 Git 仓库保持一致，并使用 status、diff、commit、show 和反向 Commit 等标准行为。Git CLI 的兼容性和可诊断性优于在 v1 中引入不完整的纯 Go Git 实现，因此通过固定命令白名单和参数数组封装。

## Consequences

- 运行环境必须安装 Git。
- 禁止 shell 拼接、任意参数、reset --hard 和自动 push。

