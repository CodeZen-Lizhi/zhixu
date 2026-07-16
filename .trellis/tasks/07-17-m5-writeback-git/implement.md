# M5-04C 实施清单

1. [x] 定义 GitRepository、Snapshot/Diff/Commit/Lookup/Reverse 类型、Trailer 常量、hash/path/ID/binding 不变量与领域测试。
2. [x] 重构 Git CLI runner：固定环境/config、bounded stdout/stderr、context/error 分类、测试 fault seam，保持 Status/Initialize 兼容。
3. [x] 实现 Workspace ID → canonical Git top-level、object format、attached branch、HEAD、author identity、in-progress operation 和 clean status Inspect。
4. [x] 实现 porcelain `-z` 单目标解析、tracked regular blob/index flags、pathspec 安全、target regular/hash、危险属性拒绝、raw blob 与 full-index binary Diff Hash。
5. [x] 实现 raw staging、immutable tree、commit-tree/update-ref CAS、固定 trailers 和 staged/tree/post-publish parent/path/blob/result/diff/message 验证。
6. [x] 实现 bounded FindWritebackCommit、完全重放、publish-after-timeout recovery 与受控 index 恢复；多个/冲突/无法证明进入一致性或人工恢复。
7. [x] 实现严格 Reverse preflight、固定 revert、tree/ref CAS、revert state 清理、ancestor replay 与 unknown-result recovery；禁止 destructive abort/reset。
8. [x] 补真实 Git repo contract/security/fault tests：dirty/staged/untracked/conflict/detached/HEAD drift/root mismatch、特殊路径、hook/filter/replace/graft/signature、SHA-1/SHA-256、unknown/replay/reverse。
9. [x] 同步 Git/error/tool-security/runbook code-spec 与父任务 checklist，运行 full gate、go-review、Trellis check。

## Validation

```bash
go test -race ./internal/changecontrol/domain ./internal/platform/gitcli
go test -race -count=20 ./internal/platform/gitcli
go vet ./...
make test
git diff --check -- . ':!vendor/github.com/yuin/goldmark/README.md'
```

## Risky Files And Rollback

- `internal/platform/gitcli/status.go` 已服务 Workspace 创建/打开；runner 重构必须保留 repo missing、containing root、cancel 和 Initialize 行为。
- raw stage 到 `update-ref` 是真实 index/ref 副作用窗口；immutable tree 与 expected-old ref CAS 防止夹带或错误 parent，任何未知发布结果必须先 exact lookup，不能盲目重复 Commit。
- Trailer key、subject、Diff Hash 算法和 lookup window 是持久恢复契约；一旦 M5-04D 写入生产 Commit，不得无版本迁移更改。
- Revert 冲突可能留下 Git conflict state；不得用 reset/checkout 自动掩盖，必须转 Manual Recovery 并保留诊断。
