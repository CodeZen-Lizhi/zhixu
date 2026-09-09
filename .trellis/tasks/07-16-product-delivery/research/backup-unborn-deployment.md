# 部署前备份的 unborn Git 支持（2026-09-09）

## 结果与范围

已修改 [`deploy/backup.py`](../../../../deploy/backup.py) 和
[`deploy/backup_test.py`](../../../../deploy/backup_test.py)，允许合法、独立且尚无 HEAD Commit 的
Workspace 进入原有停写备份流程。新增保护测试与原有测试合计 **19/19 PASS**。

已对本轮选定的知识 Root 执行一次只读预检，得到 `head=null`、`branch_state=unborn`、存在合法分支、
`dirty=true`；读取前后的 `tree_snapshot` 完全相同。没有对原 Root 执行 config、init、commit、文件写入或删除。

本子任务未操作 Docker、容器、数据库、运行配置或 Secret，没有创建正式备份，没有提交或部署。
实际停写、保护安装身份/密钥、确认 PostgreSQL 原数据卷、创建并验证备份及部署仍由主任务完成。

## 已验证的原因与实现选择

[`internal/platform/gitcli/status.go`](../../../../internal/platform/gitcli/status.go) 的 `Status` 接受
尚未解析出首次 HEAD 的已初始化仓库，并返回分支、空 Head 和 dirty；`Initialize` 初始化后也复用该状态。
旧备份脚本直接要求 `HEAD^{commit}` 成功，增加了应用没有要求的“先创建一次用户 Commit”前提。

继续复用标准库、Git CLI、现有命令执行器和文件归档，不新增 Git 库、数据库驱动或恢复入口：

- `rev-parse --verify --quiet HEAD^{commit}` 和 `symbolic-ref --quiet HEAD` 两个只读探测可以接受
  exit 1 且 stdout 为空；其他退出码、意外 stdout、命令缺失和超时仍失败，不打印底层输出。
- 接受 unborn 还必须有合法的 `refs/heads/` 分支，并通过 Git 自身的
  `fsck --full --no-dangling`。这样可以区分合法空分支与损坏 loose/packed ref、悬空 symbolic ref、
  缺失 Commit 或已暂存 Blob。没有通过完整检查的 HEAD 失败不能变成 `null` 成功。
- 已提交和 detached HEAD 继续验证可解析为 Commit 的 HEAD 与原有 status；本次没有扩展为对所有已提交
  历史运行全量 fsck 的新门禁。新增 fsck 只用于证明未解析出 HEAD 的分支是否合法，沿用 30 秒命令超时，超时安全失败。
- 在对象读取前通过 Git 的 `config --includes --null --name-only --list` 读取配置键名，拒绝
  `extensions.partialClone`；只对命中的 `remote.*.promisor` 读取布尔值，拒绝 true 或非法值。
  `false` 仍可使用。包含文件中的配置同样检查，不读取 Remote URL 或 Credential 值。
- 原有 `.promisor` pack、共享对象库、linked worktree、symlink/特殊文件、tracked content filter、
  hooks/fsmonitor、全局配置、可选锁及 lazy fetch/远程协议保护保留。即使没有 promisor pack、尚无首次提交，
  partial clone 也不能借助空 HEAD 绕过预检。filter 与 remote helper 均有本地 canary 证明未执行。
- Git 状态仍参与备份前后比较；备份期间产生首次 Commit 时不发布 `SHA256SUMS`，保留新的未完成私有目录。

官方文档使用 `find-docs` / Context7 查询，并在本机 Git `2.54.0 (Apple Git-157)` 的临时仓库核对：

- [git-symbolic-ref](https://git-scm.com/docs/git-symbolic-ref)：quiet 查询与 detached 状态。
- [git-rev-parse](https://git-scm.com/docs/git-rev-parse)：quiet verify 与 Commit 类型验证。
- [Git User Manual / corruption recovery](https://git-scm.com/docs/user-manual)：`fsck --full --no-dangling`
  检查对象损坏和缺失，可能耗时。

## Manifest 兼容

`format` 继续为 `zhixu-offline-backup/v1`；旧 `verify` 及本工具的现有校验不要求 HEAD 必为字符串。
`workspace.git` 保留原 porcelain v1 的 `dirty` / `status_sha256`，增加 `branch` / `branch_state`：

| 状态 | `head` | `branch` | `branch_state` |
|---|---|---|---|
| 合法 unborn | `null` | 经验证的现有短分支名 | `unborn` |
| 已提交、attached | 原 Commit SHA | 经验证的短分支名 | `attached` |
| 已提交、detached | 原 Commit SHA | `null` | `detached` |

HEAD 为 `null` 不代表没有文件：未跟踪、已暂存文件、`.git` 与原始内容仍按原路径和字节归档。
DB 中 recorded Git HEAD 仍独立保存在 marker，不由脚本改写，也不将 Git/DB 差异解释为已通过应用一致性。

## 实际检查

1. 旧实现基线：`python3 deploy/backup_test.py -v`，**10/10 PASS（0.705 秒）**。
2. 最终代码：`PYTHONDONTWRITEBYTECODE=1 python3 deploy/backup_test.py -v`，
   **19/19 PASS（3.115 秒）**。覆盖未跟踪/已暂存 unborn、attached/detached、7 类坏引用、缺失 index Blob、
   未知命令错误、无 pack 的 partial clone/remote/include 配置、非法 promisor Boolean、filter/helper 不执行、
   manifest `null` 与归档字节、首次 Commit 竞态，以及原有路径/权限/篡改保护。
3. Python **3.10.14**：两文件 `ast.parse(feature_version=(3, 10))` 和 `py_compile` 通过；
   编译产物只写入临时目录。
4. 定向代码 diff 的 whitespace 检查无报告；本轮修改与接管前副本对比，未替换旧脚本的其他逻辑。
5. 真实选定 Root 只读预检通过，前后 metadata 快照一致；只输出 `head`、分支状态、是否有分支、dirty 和一致性布尔值。

新增完整 CLI/归档测试使用真实临时 Git 和文件，但数据库调用被明确替换为文件保护 fixture。
测试中的 `PGDMP` 字节不是数据库，不能据此报告数据库备份/恢复通过。
数据库 dump/restore 路径未改，既有隔离 PG18 基本还原证据见 [原备份交付记录](backup-closeout.md)；
本次没有重跑 PG 或完整灾备验证，也没有验证正式运行数据库。

## 主任务需要同步的操作手册

本子任务不修改 `docs/operations.md`，建议主任务统一处理以下现有位置：

1. 第 7.1 节“含独立 `.git` 和已提交 HEAD”改为“含独立 `.git`，HEAD 为有效 Commit 或通过检查的合法 unborn 分支”；
   说明 unborn 在 manifest 中记录 `head=null`，无需为备份创建首次 Commit。
2. manifest 产物说明加入 `branch` / `branch_state`；partial clone 前提说明包括仅有配置、尚无 promisor pack 的情况。
3. 第 7.2 节无条件 `git ... rev-parse HEAD` 会使合法 unborn 还原核验失败，应改为与 manifest 的 Git 状态比较。
   可直接复用 `deploy/backup.py` 的 `git_state`，避免在手册另写一套“失败等于 unborn”的判断。
   保留既有受控的 fsck/数据库还原步骤；比较新字段时允许历史 manifest 缺少它们。
4. “10 条保护测试”是前一版的历史结果；当前入口可链接本文记录的 19 项结果，原记录不应被改成当时已支持 unborn。

第 7.2 节可用下面的只读比较替代无条件 `rev-parse HEAD`，从项目目录执行：

```bash
python3 - "$RESTORE_PARENT/workspace" "$BACKUP_DIRECTORY/manifest.json" <<'PY'
import json
import runpy
import sys
from pathlib import Path

actual = runpy.run_path("deploy/backup.py")["git_state"](Path(sys.argv[1]).resolve(strict=True))
expected = json.loads(Path(sys.argv[2]).read_text())["workspace"]["git"]
for key in ("head", "dirty", "status_sha256", "branch", "branch_state"):
    if key in expected and actual[key] != expected[key]:
        raise SystemExit("RESTORE_GIT_STATE_MISMATCH")
print("RESTORE_GIT_STATE_MATCHED")
PY
```

该比较只证明 Git 状态与备份 marker 一致，不启动应用、不改变原 Workspace 身份、不替代真实数据库还原与应用一致性核验。
