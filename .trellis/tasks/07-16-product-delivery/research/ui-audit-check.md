# UI / Audit 定向检查（2026-09-08）

检查范围以 `lean-closeout-2026-09-08.md` 为准；使用 Trellis check、前端 code-review-and-quality 与 go-review 的适用部分，没有派生其他代理。

## Findings (fixed)

- File: `deploy/Dockerfile`
- Issue: 新 `cmd/audit` 没有进入运行镜像，已运行 Compose 用户缺少容器内调用入口。
- Fix: 按现有 binaries 的构建方式添加一条 `CGO_ENABLED=0 go build` 与一条 runtime `COPY`，输出 `/app/zhixu-audit`。等价 Linux 静态构建已通过；不部署或重建用户运行镜像。

## Findings (not fixed)

无本范围尚待修复的问题。路由边界、重试/导航/Workspace 生命周期、安全 root 报告，CLI 作用域/复合游标/固定输出/只读连接/资源关闭均已核对。

## Verification

- Lint: pass；检查代理执行 Web lint 与 gofmt 检查，复用此前同范围 Go vet。
- TypeCheck: pass；检查代理的 Web build 包含 `tsc --noEmit`。
- Tests: pass；复用前端 61 项与 Go Audit 定向单测；93/checksum 同步后只补跑一条 Audit Testcontainers 集成测试，通过（9.044s）。先前迁移准备失败没有进入 CLI 断言。
- Build: pass；Web production build、Audit Linux 静态 binary；既有 Web chunk 提示未新增为精简收尾门禁。
- Diff whitespace: pass。

详细行为和证据分别见 [WP2 收尾记录](../../archive/2026-09/08-05-architecture-quality-optimization/research/route-recovery-closeout.md) 与 [Audit 收尾记录](audit-closeout.md)。主会话统一同步长期 docs/spec 与任务状态；本次未执行 commit、push、部署、完整 M11 或全仓测试矩阵。
