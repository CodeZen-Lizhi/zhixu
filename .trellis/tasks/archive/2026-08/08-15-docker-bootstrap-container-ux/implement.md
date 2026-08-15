# 实施计划

1. 创建 bootstrap Compose 文件并将全部一次性服务从主 Compose 移出；移除长期服务对一次性服务的 Compose 依赖，保留稳态 PostgreSQL/health、网络、卷、权限和 healthcheck 约束。
2. 重构 `zhixu` 的 Compose 调用为稳态与 bootstrap 两类固定 wrapper；启动、迁移、模型恢复和配置校验使用正确的模型组合，并确保 bootstrap 不接收 Workspace grant。
3. 调整 static-model overlay、Makefile 和所有直接运行 bootstrap 服务的 Compose smoke，使长期启动仍只调用稳态 Compose。
4. 调整 Compose runtime/auth/workspace validators 及其 Python/Go/Bash 契约测试，分别验证稳态零 bootstrap 可见性与合并模型的安全形状、执行顺序和失败阻断。
5. 更新 README、operations、system design/相关 ADR，说明 Docker Desktop 的观察与 Restart 边界以及 launcher 的完整启动职责。
6. 运行聚焦验证：Compose 渲染/校验、launcher contract、Compose runtime/workspace contract、相关 Go Compose contract、`git diff --check`；Docker 可用时补一条真实 `./zhixu up` 后 `ps --all` 的服务集合断言。

## Risky Files

- `deploy/compose.yml` 与新增 bootstrap 文件：服务拓扑、卷和权限边界。
- `zhixu`：启动顺序、锁、grant 与失败收敛。
- `deploy/compose_runtime_check.py` 及契约：安全校验不能因文件拆分放松。
- 各 Compose smoke：必须保持自动清理和隔离项目边界。

## Rollback

回退本任务涉及的 Compose、launcher、校验和文档变更即可恢复现有行为；不执行数据库或卷回滚。
