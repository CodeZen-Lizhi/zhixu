# M10-04 最小停写备份交付记录（2026-09-08）

## 结果与范围

交付 [`deploy/backup.py`](../../../../deploy/backup.py) 的 `create` / `verify` 和
[`deploy/backup_test.py`](../../../../deploy/backup_test.py)。Python 只用标准库，数据库备份交给明确指定的
PostgreSQL 容器内 `pg_dump`；没有恢复、停启服务、修改用户数据库或覆盖既有目录的命令。

初次 8 条参数/文件保护测试及一次隔离 PG18 基本备份恢复通过；最终独立检查修复循环链接异常与 Git lazy fetch/remote helper 边界，并将保护回归增至 **10/10 PASS**，见 [最终代码检查](final-code-check.md)。正常备份/还原数据路径未变，复用本记录的 PG18 证据。该次数据库使用最小 marker/数据 fixture，
未执行全应用迁移、正式数据灾备或跨文件/Git/数据库/索引的业务一致性演练，不据此宣称这些能力通过。

本次补充了 Git 内容过滤器保护：沿用现有 `internal/platform/gitcli` 的 `ls-files` → `check-attr`
边界，固定 Git cwd 为 Workspace Root，在 `git status` 可能执行 clean/process filter 前拒绝有 tracked filter 的仓库；关闭全局/系统 Git
配置、fsmonitor、hooks 与可选锁。`--app-version` 限定为完整 Git SHA 或 `sha256:` 镜像 ID，
仍明确标记为操作者提供的版本，工具不会推断或证明正在运行的应用版本。

最终检查另固定 `GIT_NO_LAZY_FETCH=1` 与空的 `GIT_ALLOW_PROTOCOL`，禁止 Git 读对象时请求远程对象或执行 remote helper，即使 partial clone 尚无 `.promisor` pack 也不能绕过。循环符号链接的路径解析异常统一为 `BACKUP_PATH_INVALID`，不输出原路径或 traceback；两项均有真实本地 canary 回归。

## 受支持的输入和产物

- `--workspace`：存在的绝对路径，解析到数据库记录的 canonical Root；必须有独立 `.git` 目录和已提交 HEAD。
- `--workspace-id`：数据库内该 Root 的 canonical UUID；Root 与 Git repository path 都必须精确匹配。
- `--postgres-container`：已经运行的 PostgreSQL 容器 ID/名称；工具固定连接容器内
  `/var/run/postgresql:5432`，不读取 `.env`、密码或完整 DSN，不修改认证配置。
- `--database` / `--username`：简单名称，默认均为 `zhixu`；不接受 DSN。目标须允许当前操作者通过本地 socket 连接。
- `--app-version`：40/64 位小写 Git commit SHA，或 `sha256:` 加 64 位小写摘要；拒绝 `latest` 等移动标签。
- `--output`：Workspace 外、受保护父目录下尚不存在的绝对目录。已经存在的目录、文件、符号链接均不覆盖。
- `--confirm-stopped`：操作者已停止全部数据库和 Workspace 写者的声明，不是脚本检测出的停写状态。
- 不支持 Workspace 内符号链接、特殊文件、跨文件系统目录、linked worktree/submodule `.git` 文件、
  共享 Git object store、未物化 partial clone 或 tracked content filter。失败不会改写源目录。

成功目录为 `0700`，包含四个 `0600` 文件：

| 文件 | 内容 |
|---|---|
| `workspace.tar.gz` | 指定 Root 的全部普通文件/目录，包括 `.git`、未提交文件和 `.knowledge` 原始字节；归档顶层固定为 `workspace/` |
| `database.dump` | `pg_dump --format=custom` 的整个指定数据库，不是单 Workspace SQL 导出 |
| `manifest.json` | 时间、操作者版本、Root/Workspace ID、实际 HEAD/dirty/status hash、DB 记录的 HEAD、Atlas version/未完成数、Active Index、登记 Workspace 数、PG 版本/镜像 ID、产物大小和验证边界 |
| `SHA256SUMS` | 三个产物的 SHA-256；最后写入，作为完成标记 |

工具在结束前复查目录 metadata、Git 状态、DB marker 和容器 ID/启动时间；它们不能证明所有业务行都没有变化，
也不能替代操作者停写。中途失败保留本次新建的私有目录，不发布成功；重试需选另一新目录。

`verify` 不连接数据库，只检查目录/文件权限、完成标记、hash、custom dump 头及完整 Workspace archive
可读性/路径/类型/Git HEAD 文件。`create` 另外执行 `pg_restore --list`；清单可读不等于已经恢复。
manifest 的 `database_restore_checked` 和 `application_consistency_checked` 保持 `false`。
校验和用于发现损坏，不认证外来备份来源；只还原操作者信任的备份。

## 操作者备份命令

以下是原安装默认 `zhixu` Compose 拓扑的人工命令，未对用户当前运行环境执行。若项目名、配置文件或停机预算已定制，
使用该安装的精确值。默认 Worker 的 hard stop 为 60s、Compose grace 为 70s；示例给予 app/worker 90s，
自定义 timeout 更长时必须相应调整。先等待在途副作用到安全检查点，停止外部编辑器、同步、导入器及其他 DB 写者。

```bash
docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml --env-file .env stop --timeout 90 app worker &&

docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml --env-file .env stop \
  local-model-runtime app-model-relay worker-model-relay &&

docker compose --project-name zhixu --profile workspace-runtime \
  -f deploy/compose.yml --env-file .env ps --all
```

六个稳态服务名称与当前 `deploy/compose.yml` 一致。PostgreSQL 和 namespace anchors 留在运行状态；
`./zhixu down` 会移除 PostgreSQL 容器，不能用作本工具的停写前置。强杀、超时或副作用未知不能当成安全 checkpoint，
须先按运行手册保留/处理恢复现场。完成备份前不要重新启动任何写者。

从原安装已知信息填入下面变量；备份父目录必须已经存在且位于受保护的加密存储中：

```bash
BACKUP_WORKSPACE='/original/canonical/workspace'
BACKUP_WORKSPACE_ID='canonical-workspace-uuid'
BACKUP_POSTGRES_CONTAINER='exact-running-postgres-container-id'
BACKUP_APP_REF='full-git-commit-sha-or-sha256-image-id'
BACKUP_DIRECTORY='/private/encrypted/backups/new-backup-directory'

python3 deploy/backup.py create \
  --workspace "$BACKUP_WORKSPACE" \
  --workspace-id "$BACKUP_WORKSPACE_ID" \
  --postgres-container "$BACKUP_POSTGRES_CONTAINER" \
  --database zhixu --username zhixu \
  --app-version "$BACKUP_APP_REF" \
  --output "$BACKUP_DIRECTORY" --confirm-stopped

python3 deploy/backup.py verify --backup "$BACKUP_DIRECTORY"
```

这是“单 Root 文件 + 整库”备份。若 `registered_workspace_count > 1`，其他 Root 的文件必须在同一停写窗口单独备份；
不能把任意一个包描述为所有登记 Workspace 的完整文件备份。工具不自动扫描其他 Root，不合并多个包。

备份包含正文、运行历史、数据库中的加密配置及 Git 元数据，应按敏感数据加密保管。部署 `.env`、模型/Git 主密钥、
认证材料、数据库全局角色/密码/tablespace 定义及受保护 `.zhixu/workspace-selection`、
`.zhixu/control-instance-id` 均未收集，必须按原安装 Secret/配置策略分别保护。

## 向新目录、新空数据库还原验证

这些命令只做隔离验证，不启动应用。先用受信、兼容备份 PG major/extension 的镜像准备独立 PostgreSQL 容器，
不连接用户的生产数据库；本轮实际使用与源相同的 PG18.4/pgvector 0.8.5 镜像、`--network none`、无宿主端口、tmpfs 数据。
填入该独立容器的 ID 和新数据库名：

```bash
RESTORE_POSTGRES_CONTAINER='exact-isolated-postgres-container-id'
RESTORE_DATABASE='backup_restore_new'
RESTORE_USERNAME='restore_operator'
RESTORE_PARENT='/private/restore-check-new'

(
set -eu
python3 deploy/backup.py verify --backup "$BACKUP_DIRECTORY"
umask 077
mkdir -m 0700 "$RESTORE_PARENT"
tar --no-same-owner -xzf "$BACKUP_DIRECTORY/workspace.tar.gz" -C "$RESTORE_PARENT"
git -C "$RESTORE_PARENT/workspace" fsck --full --strict
git -C "$RESTORE_PARENT/workspace" rev-parse HEAD

docker exec "$RESTORE_POSTGRES_CONTAINER" createdb \
  --no-password --host=/var/run/postgresql --port=5432 \
  --username="$RESTORE_USERNAME" --template=template0 "$RESTORE_DATABASE"

docker exec -i "$RESTORE_POSTGRES_CONTAINER" pg_restore \
  --no-password --host=/var/run/postgresql --port=5432 \
  --username="$RESTORE_USERNAME" --dbname="$RESTORE_DATABASE" \
  --exit-on-error --single-transaction --no-owner --no-privileges --no-tablespaces \
  < "$BACKUP_DIRECTORY/database.dump"
)
```

每一步非零都停止继续操作；不要跳过失败继续解包或导入。`mkdir` / `createdb` 在目标存在时失败，禁止改用覆盖、
`--clean`、删除旧库或原地解包。解包路径固定是 `$RESTORE_PARENT/workspace`，不是 manifest 原 Root。

在新库检查 marker，与 manifest 比较；原 Root 用变量比较为布尔结果，避免把它输出到终端记录：

```bash
docker exec -i "$RESTORE_POSTGRES_CONTAINER" psql \
  -X --no-password --host=/var/run/postgresql --port=5432 \
  --username="$RESTORE_USERNAME" --dbname="$RESTORE_DATABASE" \
  --set=ON_ERROR_STOP=1 --set=workspace_id="$BACKUP_WORKSPACE_ID" \
  --set=original_root="$BACKUP_WORKSPACE" <<'SQL'
BEGIN READ ONLY;
SELECT id, git_head, root_path = :'original_root' AS root_matches,
       git_repository_path = :'original_root' AS git_root_matches
FROM core.workspace WHERE id = :'workspace_id'::uuid;
SELECT count(*) AS registered_workspaces FROM core.workspace;
SELECT max(version) AS atlas_version,
       count(*) FILTER (WHERE applied <> total OR coalesce(error, '') <> '') AS incomplete
FROM atlas_schema_revisions.atlas_schema_revisions;
SELECT id AS active_index_version FROM retrieval.index_version
WHERE workspace_id = :'workspace_id'::uuid AND status = 'active';
COMMIT;
SQL
```

`--no-owner --no-privileges --no-tablespaces` 使隔离验证不依赖原全局角色和存储布局；这也意味着不能把该验证库直接作为原安装
权限模型的恢复结果。原安装的角色、ACL、Secret 与兼容版本必须另行保留/恢复。`pg_dump` 不含 cluster 全局角色，
不要为了方便而导出密码或把应用换成宽权限临时账号。

临时目录只验证文件/Git字节，数据库内 `root_path` 仍是原 canonical Root。不得直接修改 immutable `root_path`，
也不能对临时目录执行 `workspace switch` 后声称复用了原 Workspace ID。原安装灾难恢复需要：保留受保护的配置、
主密钥、selection/control identity；把可信文件副本恢复到原 canonical path；恢复兼容数据库；确认一致性和运行权限。
物理 fingerprint 因恢复改变时，再由操作者执行已有的 `./zhixu workspace rebind --confirm REBIND`。
该命令使用原 selection，不接受任意新 Root。没有这些原安装材料、换了 canonical path 或全局角色/权限不明时，
本工具不提供“任意新机自动恢复”，不能绕过既有 Root/Grant fence。

## 实际验证证据

初次执行 `python3 deploy/backup_test.py -v`：**8/8 PASS**；最终检查补两条并复跑 **10/10 PASS（0.775s）**。原范围为停写声明、拒绝凭据/移动版本标签、
已有输出/符号链接不覆盖、父路径别名不能把输出写入 Workspace、拒绝 Workspace 内 symlink、Git filter 不执行、
从 Workspace 子目录调用时 filter 仍不会执行，以及 verify 篡改/未完成/权限/非普通文件/路径穿越保护。文件完整性单测的 `PGDMP` 字节是明确的非数据库 fixture，
数据库恢复结论仅来自下面实际容器验证。

Python 3.10.14 语法解析通过；两个新 Python 文件的 `git diff --no-index --check /dev/null <file>` 无空白错误诊断
（no-index 检出新文件内容差异的退出码为 1，不按普通构建错误解释）。未运行全仓 Go/Web 检查。

一次隔离验证通过，使用 Docker 29.4.0、Git 2.54.0（Apple Git-157）：

| 证据 | 实际结果 |
|---|---|
| PostgreSQL | `pg_dump (PostgreSQL) 18.4 (Debian 18.4-1.pgdg12+1)`，`server_version_num=180004` |
| 镜像 | `pgvector/pgvector:0.8.5-pg18-bookworm` |
| 镜像 ID | `sha256:ac7cb07620a70d091bd1acf8bf50d62978458c95a32f80c848ad621cbcac5da6` |
| 网络/存储 | 单一随机 `zhixu-backup-closeout-*` 容器、`--network none`、无端口/宿主挂载、512 MiB tmpfs；仅该容器 Unix socket 使用测试 trust |
| Fixture | 两个 Workspace 行、一个 Active Index、最小 Atlas marker 表及两组 bytea/pgvector 数据；`00093` 是合成 marker，不代表执行过该迁移 |
| 创建/完整性 | `BACKUP_CREATED`、`BACKUP_VERIFIED`，目录 0700、四文件 0600 |
| 产物大小 | `database.dump=8024` bytes，`workspace.tar.gz=1850` bytes |
| 文件/Git | 12 个文件逐字节 hash、HEAD、dirty status 相同，恢复副本 `git fsck --full --strict` 通过；其他 Root 文件未打包 |
| 数据库 | 新 `template0` 空库恢复通过；两个 Workspace、Root、HEAD、Atlas/Index marker 及 bytea/向量值与源一致 |
| 源保护 | 源文件全部 hash 和源数据库查询结果在完成后保持不变 |
| 清理 | 唯一测试容器（含其匿名卷）已删除，专属临时目录已删除；未操作用户运行环境 |

测试的操作者应用引用为当前仓库 HEAD `68cea9ef486d98f693ff984e9980aa0f297b21ff`；它只验证版本字段传递，
不声明该提交已部署。验证命令使用源镜像内的 `pg_dump`/`psql`，同容器的新数据库通过
`pg_restore --exit-on-error --single-transaction --no-owner --no-privileges --no-tablespaces` 恢复。

按用户要求移出本轮门禁且未执行：全应用 Schema/升级恢复、正式数据恢复、跨版本/跨机器、全域业务一致性、
全部历史 Root、角色/Secret/selection 恢复、停机/RPO/RTO/大容量演练、应用启动/索引重建和 M11。
未 commit、push 或部署；隔离 Git fixture 的初始 commit 只用于测试，随临时目录清理。

## 官方参数依据

`find-docs` 找到 PostgreSQL 18 官方索引后正文查询返回 `fetch failed`，改为直接读取官方手册并核对测试容器
`pg_restore --help`：

- [PostgreSQL 18 pg_dump](https://www.postgresql.org/docs/18/app-pgdump.html)：custom 格式使用 pg_restore；单库备份不包含角色/tablespace 等 cluster 全局对象。
- [PostgreSQL 18 pg_restore](https://www.postgresql.org/docs/18/app-pgrestore.html)：`--single-transaction` 包含失败即停语义；`--no-owner`、`--no-privileges`、`--no-tablespaces` 分别省略 owner、ACL 与 tablespace 还原。

本次审查使用 `code-review-and-quality`，按范围检查参数/命令注入、路径/输出保护、Git 外部程序、数据库只读边界、
失败保留、身份材料和测试证据；没有增加基础设施依赖或另一条业务恢复写路径。
