# 2026-09-09 开发与 Docker 部署预检

本记录只描述已执行的只读预检和定向测试，不代表完成部署。用户已授权本轮开发完成后重新部署本机 Docker；不得操作其他项目容器。

## 已执行结果

- `go test -mod=vendor ./internal/agent/adapter/eino -run 'TestAgent|TestEinoAgent' -count=1 -timeout=60s`：PASS，包执行 0.831s。覆盖既有 Eino ReAct/Tool bridge、ReturnDirectly、精确工具版本与 Provider Call 关联等名称匹配用例；这只是复用前基线，不是新 v2 验收。
- `./zhixu status`：`Runtime: degraded`，URL 为 `http://127.0.0.1:8080/`；已选 Root 为 `/Users/zhenglizhi/Documents/files/zhixu`，Grant active。
- `zhixu-app-1`、`zhixu-postgres-1` 已停止；`zhixu-worker-1` 和两个 relay 仍在运行但 unhealthy。两个 namespace anchor、local-model-runtime healthy。
- 当前应用镜像 `sha256:3a5d9c742956932e209fc9869ede3d0c885a356afc47d5bd9448cc5d63484e88`；Worker 镜像 `sha256:2b2296b49127007ca5ca1a8fdc03e4e62872f21123a752ec6cf1ce2752bb26c4`；PG 镜像 `sha256:ac7cb07620a70d091bd1acf8bf50d62978458c95a32f80c848ad621cbcac5da6`。
- PostgreSQL 原容器同时保留 `zhixu_zhixu-postgres` 命名卷和一个镜像声明的匿名卷。已核实容器 `PGDATA=/var/lib/postgresql/data`，该精确路径映射到原命名卷；匿名卷挂在其父路径 `/var/lib/postgresql`。备份时继续从原容器核对实际数据库，不能删除任一旧卷或用空库冒充恢复成功。
- `.env` 存在，HTTP port 为 8080；Workspace Analysis API/Worker flags 与 config revision 未显式设置，当前使用 false/false/1 默认值。未读取或输出 Secret 值。
- selection/control identity 文件存在且权限为 0600。
- 保存的知识 Root 当前 Git 没有可解析 HEAD，`git status --short` 只显示未跟踪 `.DS_Store`。部署前需核对受支持的 unborn Workspace 路径；不得自行删除、覆盖或创建用户 Git 历史来掩盖初始化问题。

## 部署执行顺序（尚未执行）

1. TODO2、TODO4 与既有收尾代码集成，所有必要构建/定向检查完成，迁移编号和 Atlas hash 协调一致。
2. 确认主项目写者安全停止，保留原 PG/Workspace/配置/密钥/selection；按实际数据形态选择受支持的备份方式并验证成功。现有 `backup.py` 要求已提交 HEAD，不能忽略上述 unborn 状态假报备份成功。
3. 使用受支持 launcher 构建并执行 bootstrap/前向升级，保留原 Root 与数据库身份。更新新功能 flags 时复核 API/Worker 精确 Definition/catalog/policy readiness。
4. 验证 PG/migration、API/Worker、relay、anchors 与 Web ready；固定页面真实可用，受影响功能与历史读取通过最小部署检查。
5. 记录最终镜像、Schema、ready/HTTP/浏览器证据及任务关闭状态。M11 最终验收保持暂缓，未执行项不写 PASS。

## 外部任务协作

- TODO4 原任务 `01a08116-e0b5-7f02-a569-d5344567b606` 与旧收尾任务 `01a08079-3181-74a2-85dc-58737199713c` 后续均被 `wait_threads` 证实为 failed/systemError；TODO4 一次续接也失败，不再重复发送。
- 当前主会话通过本地 Trellis 实现代理接续 TODO4 的 W2–W5，并复用已落盘成果与可信验证。外部任务不再被当作仍在工作的依赖。
