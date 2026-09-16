# BGE 容器出口诊断（2026-09-16）

范围：仅排查本地运行配置；不改应用 Transport、TLS、公网地址限制、模型协议或业务代码，不执行模型 activation。

## 已验证事实

- 当前 Docker context 为 `orbstack`，连接 `~/.orbstack/run/docker.sock`，服务端标识 OrbStack / Docker 29.4.0。不能按 Docker Desktop 的代理设置处理。
- OrbStack 当前 `network_proxy=auto`，`network.proxy.exclude` 为空。macOS HTTP、HTTPS、SOCKS 均启用本机 7890；Clash 当前 rule 模式，TUN 未启用。
- [OrbStack 官方文档](https://docs.orbstack.dev/docker/network#proxies)说明容器会透明遵循 macOS 代理，优先使用 SOCKS；即使容器没有代理环境变量、Go Transport 使用 `Proxy=nil`，也不能据此判断没有经过运行时代理。
- 宿主机对 `https://api.siliconflow.cn/v1/embeddings` 的无凭据 GET，在直连、HTTP proxy、SOCKS5、SOCKS5h 下均返回 HTTP 404，TLS 校验结果 0。固定目标 `47.102.37.23` 的直连和 SOCKS5 也成功；父会话已验证固定 `47.103.87.49` 直连成功。
- 同时，`docker exec zhixu-app-1 wget -S --spider -T 10 https://api.siliconflow.cn/v1/embeddings` 稳定复现 TLS EOF / connection reset，或在握手阶段超时。两个当前实际公网解析地址均出现过，不能只按其中一个固定 IP 排查。
- 通过本机 Mihomo UNIX controller 的只读 `/connections`，在容器请求期间捕获到：`destinationIP=47.102.37.23`、`destinationPort=443`、`host=""`、`process="OrbStack Helper"`、`type="Socks5"`、`dnsMode="normal"`、`rule="RuleSet"`、`rulePayload="szkane-developer"`、`DIRECT=false`。
- 现有 DashScope 的 DOMAIN-SUFFIX / DIRECT 规则仍存在，但当前运行计数为 0 命中；父会话也复现 DashScope 容器 TLS EOF。这支持“当前运行时出口/规则匹配差异”的方向，不能解释为 BGE 模型或 API key 错误。

## 根因与最小修复

容器与宿主机的代理请求并非同一路径：OrbStack 发给 SOCKS 的目标是 IP，域名为空，且命中了开发工具规则。因此宿主机显式 HTTP proxy 请求成功，不能证明容器所走分流也能成功。

官方支持 `orb config set network.proxy.exclude "api.siliconflow.cn"` 这种 NO_PROXY 格式的精确域名排除。主会话确认，这一指定 endpoint 的可撤销本地修复属于用户已授权的 BGE 启用范围，因此执行了如下变更：

- 原 `network.proxy.exclude` 为空；新值仅为 `api.siliconflow.cn`。`network_proxy` 仍为 `auto`，不改变 macOS/Clash 的代理模式，不增加任何 IP 范围或其他域名。
- 变更前的原值、恢复命令及 API/Worker 运行身份已保存到私有 `.zhixu/backups/bge-runtime-network-20260916.json`（0600），未复制密钥或其他代理配置。恢复命令为 `orb config set network.proxy.exclude ''`。
- 官方 CLI 执行退出 0，立即生效，无须重启 OrbStack、API 或 Worker。
- 同一容器探测在 API 与 Worker 均从 TLS EOF/超时变为 `HTTP/1.1 404 Not Found`。探测使用无凭据 GET，404 是 Embeddings 路径不支持这一探测方法的正常 HTTP 层结果；`wget` 退出 1 不代表 TLS 仍失败。
- API/Worker 的容器 ID、StartedAt、RestartCount 与变更前完全一致，两者继续 healthy。

这一结果证明当前故障位于 OrbStack 自动代理的容器出口路径；通过精确域名排除恢复。Clash 具体上游为何对该路径失败没有进一步拆解，不能将其细化为未经验证的节点故障。该设置为 OrbStack 级，对其他容器访问相同 BGE 域名也生效，其余目标的代理选择不变。

## 当前状态

已恢复 API/Worker 到 SiliconFlow 的 TLS/HTTP 连接，并向主会话交付继续通过既有模型设置 API 做真实 Embeddings Probe 与 activation。无凭据 GET 不能证明模型请求、1024 维返回或配置激活成功；这些由主会话另行验证。本文仅记录脱敏网络元数据，没有读取订阅链接、节点凭据或模型密钥。没有应用代码变更，因此未运行应用测试或全仓构建。
