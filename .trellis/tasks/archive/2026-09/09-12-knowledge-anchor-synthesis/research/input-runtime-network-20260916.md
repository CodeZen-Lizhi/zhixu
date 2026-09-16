# ai.input.im 容器出口修复（2026-09-16）

用户已授权使用此服务并激活聊天模型。本子任务仅处理指定 endpoint 的本地网络配置，不读取密钥、不执行模型调用或 activation、不改应用代码。

## 根因证据

- 主会话验证：宿主机直连 `https://ai.input.im/v1/models` 在约 0.24 秒返回 HTTP 401，TLS 校验通过；managed 生产连接测试和 API 容器探测超时。
- 本子任务复现：`docker exec zhixu-app-1 wget -S --spider -T 8 https://ai.input.im/v1/models` 连接 `23.136.252.4:443` 后超时。
- 同时读取 Mihomo UNIX controller 的 `/connections`，捕获 `host=""`、`destinationIP=23.136.252.4`、`destinationPort=443`、`process="OrbStack Helper"`、`type="Socks5"`、`dnsMode="normal"`、`rule="RuleSet"`、`rulePayload="szkane-developer"`、`DIRECT=false`。
- 这是与 BGE 相同的 OrbStack 自动代理出口差异。[官方文档](https://docs.orbstack.dev/docker/network#proxies)说明容器透明跟随系统代理，并支持用 `network.proxy.exclude` 配置精确域名例外；应用没有代理环境变量或使用 `Proxy=nil` 不能排除此运行时代理路径。

## 精确变动

| 配置 | 修改前 | 修改后 |
| --- | --- | --- |
| `network_proxy` | `auto` | `auto` |
| `network.proxy.exclude` | `api.siliconflow.cn` | `api.siliconflow.cn,ai.input.im` |

原值和恢复命令保存到私有 `.zhixu/backups/input-runtime-network-20260916.json`（0600）。修改前再次比较原值，避免覆盖并发更新。仅追加 `ai.input.im`，保留 BGE，不新增 IP 范围、其他域名或 Clash 规则，不重启运行时或服务。配置读取确认与预期新值完全一致。

需要撤销本次变动且配置没有后续更新时，可恢复为 `orb config set network.proxy.exclude api.siliconflow.cn`。若之后又新增排除项，应仅移除本次 `ai.input.im`，不要覆盖后续修改。

## 验证与边界

- API 与 Worker 的同一无凭据 HTTP 探测均在约 0.26 秒返回 `HTTP/1.1 401 Unauthorized`，不再超时。`wget` 因 401 退出 1 是预期的 HTTP 结果，证明 TLS/HTTP 已恢复，不能用来判定用户密钥是否有效。
- API/Worker 的容器 ID、StartedAt、RestartCount 与变更前一致，两者均 healthy。
- 仅修改本机运行配置并新增本文；没有业务代码变更，因此没有运行应用测试或全仓构建。
- 这一例外对 OrbStack 中访问同一精确域名的容器都生效，其他目标的代理配置不变。主会话继续真实模型调用和激活验收。
