# M5-04B 实施清单

1. [x] 定义 WorkspaceStore/TargetLock、Prepare/Prepared/Applied/Restore 类型、hash/change/path/locator 不变量与领域测试。
2. [x] 提取可复用的 Root 安全父链、file identity、hash/copy/sync 和 Git exclude helper，保持现有 Capture/Scanner 行为不变。
3. [x] 实现 Workspace ID → canonical Root、目标 Markdown/owner/device/symlink/特殊文件校验与 device/inode advisory lock。
4. [x] 实现 Parser-backed Markdown Validator、同目录 temp、大小限制、Result/Change Hash 校验、mode 保留和 temp fsync。
5. [x] 实现 CommitCAS：最终 identity/base hash、独立 backup copy+fsync、rename、parent fsync、结果验证与 unknown-result 分类。
6. [x] 实现 RestoreCAS/Cleanup/Close 幂等和受控 locator 校验。
7. [x] 补 unit/contract/fault tests：路径、特殊文件、权限、parser、CAS、restore、cleanup、故障注入。
8. [x] 补 helper subprocess 跨进程 flock、取消/退出释放和 20 轮 race 并发测试。
9. [x] 同步 Filesystem/错误 code-spec 与父任务 checklist，运行全仓门禁、go-review、Trellis check。

## Validation

```bash
go test -race ./internal/changecontrol/domain ./internal/changecontrol/adapter/localfs ./internal/platform/filesystem
go test -race -count=20 -run 'Test.*Lock' ./internal/changecontrol/adapter/localfs
go vet ./...
make test
git diff --check -- . ':!vendor/github.com/yuin/goldmark/README.md'
```

## Risky Files And Rollback

- `internal/platform/filesystem/content_store.go` 已承载 Artifact Capture；helper 提取必须跑原有全量测试，禁止改变只读/不可变 Store 行为。
- lock/temp/backup 文件名与 Git exclude 是恢复契约；一旦持久化 locator 后不得无迁移改格式。
- rename 后错误不得通过普通 error 丢失 AppliedWrite 摘要；调用方必须能区分 old/new/unknown。
- 回滚时停止新写回，保留已 Applied backup；不得批量删除 `.zhixu-writeback-*` 未知文件。
