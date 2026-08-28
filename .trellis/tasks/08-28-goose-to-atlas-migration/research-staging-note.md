# TODO 3 Atlas 在途文件暂存

2026-08-28 由 Codex 主会话暂存。

## 2026-08-28 解除暂存（TODO3 会话）

TODO3 已创建并经用户批准进入实现（任务目录
`.trellis/tasks/08-28-goose-to-atlas-migration`，状态 in_progress）。四个
Atlas 文件已移回 `internal/platform/migration/`。依赖决策与暂存 README 的
建议不同：Atlas 锁定 **v1.2.2** 而非 latest——v1.3.0 的 go 指令要求
go 1.26.4，会破坏 ADR-0015 锁定的 Go 1.25.4 工具链（deploy/Dockerfile
`golang:1.25.4-bookworm`）。go.mod 已回正为 `go 1.25.4` +
`ariga.io/atlas v1.2.2`（Apache-2.0）。rivermigrate.go 的共享
migrateRiver/validationMessage 已按注释约定合并，Atlas runner 内的重复
定义已删除。请 TODO 10 会话不要再将这批文件移出构建路径；构建当前为绿。

## 原因

`atlasdir.go`、`atlasrrw.go`、`atlasrunner.go` 是 TODO 3（Goose 迁移到 Atlas）的在途实现，import `ariga.io/atlas`，但 go.mod/go.sum 尚未引入该模块，导致 `go build ./...` 整体失败。TODO 3 按路线图仍是未启动的独立任务，Atlas 不得提前成为当前技术基线，因此本轮 TODO 10 开发先将这三个文件移出构建路径，不删除、不修改内容。

同目录的 `convert.go` / `convert_test.go` 不依赖 ariga.io/atlas，可独立编译，仍保留在 `internal/platform/migration/`。

## 恢复方式

TODO 3 启动时，将三个文件移回 `internal/platform/migration/`，然后执行：

```bash
go get ariga.io/atlas@latest
go mod tidy && go mod vendor
```

注意 `atlasrunner.go` 同时依赖 `rivermigrate` 与 `riverpgxv5`，恢复后需重新核对与 Foundation GORM pool 的兼容矩阵。
