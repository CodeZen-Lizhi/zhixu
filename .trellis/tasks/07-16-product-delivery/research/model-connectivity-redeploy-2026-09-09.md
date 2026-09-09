# 模型连通性修复与兼容版本重新部署（2026-09-09）

用户要求激活本机模型并重新部署最新 OpenAPI 兼容修复。排查确认 Chat 的免费额度已耗尽后，用户明确选择保留“仅使用免费额度”限制，并将本轮模型验收调整为接口能够连通。本轮已完成这一验收与重新部署；没有关闭 Chat 的免费额度限制，也没有把连通状态记为配置激活成功。M11 继续暂缓。

## 模型连接

保存的 revision 6 使用 `qwen3.7-plus` 和 `qwen3.7-text-embedding`，两项均保留原有凭据与配置。

1. 容器 DNS 最初将 DashScope 解析到 `198.18.0.0/15` 的 Fake-IP，触发项目公网地址校验。Clash 全局扩展脚本增加 `+.dashscope.aliyuncs.com` 例外，覆盖服务域名及其 CNAME 子域名。
2. 实时出口记录确认，该服务命中通用开发工具规则并使用境外代理节点，容器 TLS 握手返回 EOF。在该通用规则之前增加 `DOMAIN-SUFFIX,dashscope.aliyuncs.com,DIRECT` 后恢复连接。
3. 持久全局脚本相对私有备份仅有上述两处修改；Node 语法检查、Mihomo 配置检查均退出 0，受支持的本机配置重载返回 204。服务自己的 TLS、公网地址约束、重定向与 production Probe 均未修改。
4. Embedding 在重启前、重启后均通过生产 Adapter 的连接测试。重启后的测试返回 HTTP 200，耗时 230 ms。
5. Chat 已到达阿里云，服务商返回 HTTP 403 / `AllocationQuota.FreeTierOnly`，明确表示免费额度耗尽。诊断仅输出脱敏的错误字段；临时诊断程序已清理。

用户选择保留免费额度限制，因此没有开始必然失败的 activation，也没有修改 active 数据库状态。部署后的事实仍为 `desired_revision=6`、`active_revision=2`；API/Worker 均 applied 2、active 且 fresh；active Chat/Embedding 均为 disabled。整套 Apply 要求两项真实 Probe 成功，Embedding 单项连接成功不代表整套配置已激活。

原 rollout 仍为 failed / target 6 / version 49，保存的历史错误仍为 `MODEL_CHAT_REQUEST_FAILED`。本轮 Chat 连接测试的错误为 `MODEL_CHAT_UNAUTHORIZED`，上游实际原因为免费额度限制；不得把历史 rollout 错误误写为本轮新激活的结果。

## 停写备份

- 停写前非终态 Workflow、running Attempt、running River Job 均为 0。API/Worker 与本地模型 manager 正常退出，relay 以 SIGTERM 停止；PostgreSQL 与 namespace anchors 保持运行。
- 当前知识 Root + 整库已通过 `deploy/backup.py create` / `verify`：文件包 9,595 bytes、数据库 dump 2,294,184 bytes；安装配置、主密钥和身份材料另行私有备份。
- 另一仍存在的 inactive Workspace 是开发仓库，在同一停写窗口于仓库外完成完整文件归档、SHA-256 与可读性校验，归档为 348,888,712 bytes。第三个登记 Root 在操作前已不存在，未创建替代目录；其数据库记录保留。
- 私有备份位置登记在 `.zhixu/model-activation-deploy-20260909/backup-location.json`；密钥、dump、原代理配置和 Session 不进入 Git。未执行恢复演练，完整性检查不等于灾备验收。

## 最新版本部署

`./zhixu restart` 退出 0，耗时 145.7 秒。源码基线为 `7c720dc02f84d2046ebbea823250673629b6af01`，包含 `789692e2` 的全部 OpenAPI 兼容修复；运行时 system status 返回相同完整版本。后续提交仅同步本文与状态记录。

| 角色 | 实际运行镜像 |
| --- | --- |
| API / Web | `sha256:9a75beb6106a9e2b700ed4418dd122b9ecc9d1154a49ad722fd708f8062169fc` |
| Worker | `sha256:11c7d50720df753d64f097ee8e25a06ed840b6f43ab7c22c01f0e7e1f1925cde` |

- `./zhixu status` 为 Runtime ready，8 个容器全部 healthy；`/livez`、`/readyz`、system status、active Workspace 与模型设置均为 HTTP 200，入口为 `http://127.0.0.1:8080/`。
- 十个 v2 operation 全部已注册且未认证请求返回 401；五个只读端点在认证后进入真实 owner，面试列表返回 200，四个不存在资源返回对应领域 404。首页与真实脚本资源返回 200。未创建测试业务数据。
- Atlas head 仍为 `00099`，没有未完成或失败的迁移。挂在 PGDATA 父目录的匿名卷随容器重建更换，实际 PGDATA 命名卷保持一致。
- Workspace 3、Proposal 1、Source/SourceVersion/Claim/Workflow Run 0 的原 ID 集合逐项一致。当前 Root 归档中的 19 个文件与重启后逐字节一致。
- `.env`、control identity、模型主密钥和本地运行凭据逐字节一致；selection 只刷新 `committed_at`，Workspace ID、Root 与 grant 保持原值。

具体证据保存在 `.zhixu/model-activation-deploy-20260909/` 的部署日志、API/v2/数据核验 JSON。此前固定基线 OpenAPI 0 error / 0 warning 与代码测试证据沿用已推送版本，本轮不改应用代码、不重跑 M11，也不宣称现场 AI 生成闭环已通过。
