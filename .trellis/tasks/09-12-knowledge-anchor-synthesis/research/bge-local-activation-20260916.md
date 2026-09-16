# 本地服务更新与 BGE 激活

2026-09-16。用户明确要求“更新本地服务，启用 BGE”。通过项目正式 launcher、备份流程和模型设置 API 完成；未修改业务代码、提交或推送。

## 已完成

1. 停写前核对无运行中的 Workflow / Attempt / River Job；停止 API、Worker 和模型运行进程，保留数据库。当前 Workspace ID 为 `a68d94c7-17b4-4fa5-8ef6-5747db51af28`，Root 为 `/Users/zhenglizhi/Documents/files/zhixu`。
2. 通过 `deploy/backup.py create` / `verify` 创建并校验当前 Root 与整库备份，私有路径为 `.zhixu/backups/bge-upgrade-20260916/`。已有运行主密钥卷保留；未执行恢复演练。该文件备份覆盖当前 Root，数据库包含其余登记工作区，不声称另外工作区的文件也被本轮归档。
3. `./zhixu restart` 构建和启动最新程序退出 0；实际数据库从 `00099` 升至 `00130`。`./zhixu status` 为 ready、Workspace selected、grant active；8 个项目运行容器 healthy，`/readyz` HTTP 200。
4. 部署后当前 Root 归档内的 19 个文件与磁盘逐字节相同，没有缺失或变化。
5. 修复 OrbStack 的指定 BGE 域名出口：精确 `network.proxy.exclude=api.siliconflow.cn`，保留全局 auto。详情和回滚资料见 `bge-runtime-network-20260916.md`；不修改应用 TLS/公网限制，不重启其他服务。
6. 通过受认证/CSRF 保护的 `POST /api/v1/settings/models/test` 执行真实 BGE Probe，HTTP 200 / `status=ok`，耗时 418 ms。请求走更新后的生产 Eino Embedding Adapter；固定宽度 1024 及既有响应合同通过。
7. 保存 BGE 单独启用的 desired revision **8**，旧 desired revision **7** 的 Qwen Chat 草稿仍在不可变历史中。新的 Chat 保持原 active 的 disabled；没有把未连通的 Chat 草稿一起激活。
8. 经既有 activation API 进入 preparing → arming → idle，最终 active/API applied/Worker applied 均为 **8**，API/Worker 均 fresh。生效 Embedding 为 `BAAI/bge-m3`、1024、l2、cosine；Chat disabled。临时 Session 正常撤销。

## 实际运行镜像

| 角色 | Image ID |
| --- | --- |
| API / Web | `sha256:c2a70806f044f6cad5465958083e6fb66cdb36b45454ed2444fcd321102822f8` |
| Worker | `sha256:f6c575d6bcb57cb3b5f403d382d88345c136ed45d7f369310f0c3157fe0cedf7` |

部署日志 `/tmp/zhixu-bge-upgrade-20260916.log`；无密钥的最终配置快照 `/tmp/zhixu-bge-activation-result-20260916.json`。没有在报告或源码写入 API Key。

## 失败定位与验收边界

- 首次临时管理脚本漏传连接测试必需的 Idempotency-Key，HTTP 400；补齐后才进行 Provider Probe。该脚本错误未保存 revision 或启动 activation。
- 初次容器 Probe 在 TLS 阶段 EOF，并非 BGE 模型参数或密钥错误；网络修复后同一生产入口通过。
- BGE 启用与 Chat / Responses 选择独立。本次不改 Chat 协议、不增加自动识别接口。
- 用户指定的 Responses 本地网关最小调用返回 HTTP 502 / `Upstream access forbidden`；此前 Chat 路径 503 不再当作唯一诊断依据。真实主笔记融合与补源语义质量仍未通过，保持整个任务 in_progress。
- 本轮证明真实 Embedding Probe 与双角色配置生效；没有对个人笔记触发专项重建索引或宣称完整知识问答质量已验收。
