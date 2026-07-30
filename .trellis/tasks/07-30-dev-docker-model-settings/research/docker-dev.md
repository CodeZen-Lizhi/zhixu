# Docker 开发环境与垃圾镜像研究

## 已确认事实

- `Makefile:157-161` 的 `compose-up` 固定读取 `deploy/compose.yml + .env.example`，执行 `up -d --build --wait`；没有创建本地 `.env`、打印访问地址或区分普通停止与销毁数据。
- `deploy/compose.yml:68-145` 的 app、worker、migrate、proxy、firewall 分别声明同一 Dockerfile 的 `build`，随机 Compose project 会为每个服务生成独立镜像名。
- 四个 smoke 都生成随机项目名并显式 build：
  - `deploy/compose-auth-smoke.sh:107-143`
  - `deploy/compose-search-smoke.sh:207-241`
  - `deploy/compose-tool-smoke.sh:69-97`
  - `deploy/compose-rag-smoke.sh:185-223`
- 四个 cleanup 只执行 `down --volumes --remove-orphans`，没有删除本次本地镜像：
  - `deploy/compose-auth-smoke.sh:56-65`
  - `deploy/compose-search-smoke.sh:36-46`
  - `deploy/compose-tool-smoke.sh:50-60`
  - `deploy/compose-rag-smoke.sh:57-67`

## 本机盘点（2026-07-30）

- 知序 smoke 镜像 282 个，282 个唯一 Image ID，均没有容器引用：
  - auth 165
  - rag 75
  - search 24
  - tool 18
- Docker images 总计 33.06GB，可回收 12.39GB；BuildKit cache 总计 35.63GB，可回收 31.98GB。
- 当前 `deploy` Compose 栈的 app、worker、proxy、postgres 正在运行；migrate/firewall 正常一次性退出。
- 同机还有 Aether、AI learning 等其他项目，以及无法仅凭名字证明归属的知序 PostgreSQL 测试容器。

## 设计结论

- 根目录提供 `./zhixu up|restart|status|logs|down|reset`；默认继续使用稳定 Compose project `deploy`，兼容当前 `deploy_zhixu-postgres` 数据卷。
- `up` 首次从 `.env.example` 初始化 `.env`、创建 `workspace`，执行安全预检和 `up -d --build --wait`，再查询 API/Worker readiness 并打印 URL。
- `down` 不带 `-v`；只有需要二次确认的 `reset` 删除知序开发栈数据。
- smoke cleanup 增加 `--rmi local`，并用测试证明成功/失败退出均无该随机项目镜像。
- 一次性清理只匹配 `^zhixu-(auth|rag|search|tool)-smoke-[0-9a-f]+-`，删除前再次检查容器引用；禁止全局 prune。

## 风险

- 当前 8080 已由 `deploy` 栈占用；实施完成后应升级同一 project，而不是启动第二个 project 竞争端口。
- 全局 BuildKit cache 无可靠项目归属标签，本任务不删除。
- `make compose-down` 当前带 `-v`，不能继续作为普通停止入口。
