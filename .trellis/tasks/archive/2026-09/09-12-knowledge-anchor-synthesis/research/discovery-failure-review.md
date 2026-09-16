# 120 discovery-failure 链路只读审查

2026-09-15，impact-check。接 main 的 119 修复复核及后续 120 审查任务；仅写审查记录，不修改实现。119 原 P2 已关闭，见 `manual-storage-review.md`。

## 开放发现

**P2：分页列表没有跨页 binding 校验，rebind 窗口可混显两个物理根的记录。**

- 位置：`web/src/features/workspace/WorkspacePage.tsx:64` 与 `:66`；关联 `web/src/api/workspace.ts:123`、`internal/workspace/adapter/postgres/gorm_discovery_failure.go:85`。
- 触发：页面已取得 workspace W / binding 1 第一页。W 完成目录 rebind 和重新激活为 binding 2；在 `WorkspaceCacheBoundary` 下一次 5 秒 active-workspace 轮询更新 version 之前，用户请求“更多记录”。
- 证据：后端每次按当前 binding 查表，而 cursor 只含相对路径；decoder 仅检查每条记录与它所在页的 binding 相同，不比较上一页；页面无条件 `pages.flatMap(page => page.items)`。于是 binding 1 第一页保留、binding 2 第二页追加。新根中路径 <= 旧 cursor 的失败也不会进入这一页。
- 可验证影响：同一“文件扫描记录”区域混显旧根与新根记录，没有 binding 提示。后端各页仍只读取各自当时获授权的当前 binding，不能据此称为跨 root 数据越权。外层 workspace.version 更新会重新挂载并修复，但存在轮询窗口；未复现实际 rebind 浏览器时间线，结论来自确定的数据拼接路径。
- 建议：下一页携带/核对第一页 binding；不一致时丢弃整个旧分页，从空 cursor 刷新。即使暂不改 HTTP cursor 格式，也需在消费端拒绝不同 binding 页。补第一页 binding1、第二页 binding2 的分页回归，并断言新 binding 从第一页重新取、旧记录不混显。
- 状态：开放，已发 main（seq17208），由 main 转交 impact-ui。无代码修改。

## 其余检查结论

- `source_discovery.go` 对 WalkDir 的二次 ReadDir 错误回调先处理，再判断页槽已满；从 readDirectories 删除失败目录，页尾失败不会同时产生恢复。真实非 root 权限测试覆盖 limit=1 的不可读目录及恢复。
- application 仅将实际读目录成功记为 WALK 成功，REGISTER 成功在 capture/登记成功之后；未扫描、被排除路径不代表恢复。数据库 WALK 成功只恢复旧 WALK，不能恢复 OBSERVE/REGISTER。
- Record/List 在 workspace 行锁内要求 active、校验授权；写入还比较冻结 root path/fingerprint/binding，结束前再次授权。失败记录按 workspace/binding/path 隔离，旧 binding 不由新根成功扫描恢复。
- discovery session 保留 os.Root，观察与 capture 使用同一个物理目录句柄；登记前 Revalidate 比较当前授权和物理 identity。真实目录替换测试确认只读/写旧授权目录，并拒绝后续登记许可。
- GET 路由具备 READ_LOCAL 配置、严格 query 参数和响应 workspace 校验。只存安全相对路径和白名单错误码；不保存原始错误/正文/绝对路径。
- 保存失败上抛；worker 只在整个 DiscoverWorkspaceSources 成功后推进 cursor。已登记而状态落库失败时下次重新扫描，可通过既有来源版本幂等恢复。
- 手动扫描完成或失败均 invalidate 状态查询；正常单 binding 的失败→恢复、重复扫描路径有测试，未发现该路径新增问题。

## 验证

本次执行：

- `go test ./internal/platform/filesystem -run 'Test(SourceDiscovery|DiscoverySession)' -count=1`：PASS 3.067s。
- `go test ./internal/workspace/http -run Discovery -count=1`：PASS 0.159s。
- `go vet -tags=integration ./internal/platform/filesystem ./internal/workspace/application ./internal/workspace/adapter/postgres ./internal/workspace/http`：exit 0。
- Web `workspace.test.ts` 与 `WorkspacePage.test.tsx`：2 文件、10 测试 PASS。
- 上述四个 Web 文件 ESLint、`npm run typecheck`：exit 0。
- 限定 discovery 文件 `git diff --check`：exit 0。

尚未证明：`discovery_failure_integration_test.go:35` 当前仍构造排除00119的内存迁移目录，不能作为正式119+120主目录最终验收；已通知 main 由 impact-ui 更新并复验。本轮阅读该实库用例的失败持久化、HTTP重读、重试恢复、旧binding/撤销授权/记录库故障断言，没有自行重复运行此旧fixture。浏览器检查由实施方承担，本轮未新增浏览器会话。上述 P2 的跨 binding 分页情形不在现有10项前端测试中。

120 本次审查发现 1 个 P2，未修复；没有新增 P1 发现。119 已关闭，不扩大两批业务契约。

## 修复复核关闭（main，2026-09-15）

主会话复核 WorkspacePage 的实际修复：跨页 workspace/binding 不同即清空可见记录、提示刷新并移除更多入口；刷新以 exact resetQueries 从空 cursor 重读。新增组件回归实际覆盖 A页1/B页2、旧新记录都不可见、B首页刷新成功及旧告警消失。实施方最终2文件11测试通过，原日志 discovery-failure-web-final.log；PG正式完整目录含121通过7.705s，浏览器失败→重扫恢复→刷新保持已有真实HTTP/PG证据，具体限制见 discovery-failure-implementation.md。复用定向证据，不重跑已通过检查。

本次P2已关闭，开放0。真实目录rebind竞态由确定的组件响应序列验证，未冒称真实rebind浏览器时间线。
