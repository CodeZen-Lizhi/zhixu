# M3 Workspace 实施计划

1. [x] 建立 Foundation ID/Clock/Error 与单元测试。
2. [x] 实现安全路径解析、符号链接边界和文件扫描测试。
3. [ ] 新增 Workspace/Source/Source Version migration 与 Repository 集成测试。
4. [ ] 实现 Create/Open/Scan Application 用例和 Git 只读状态 Adapter。
5. [ ] 扩展 OpenAPI、HTTP Handler 和 Contract Test。
6. [ ] 实现 React Workspace 页面、API decoder 和组件测试。
7. [ ] 执行 Compose 业务烟测、路径安全负向测试、Review 和文档同步。

## Verification

```bash
make test
go test -race ./internal/foundation/... ./internal/workspace/...
docker compose -f deploy/compose.yml --env-file .env.example up -d --build --wait
```
