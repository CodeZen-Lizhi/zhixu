---
status: accepted
---

# 通过 Host Controller 应用精确 Workspace Root Grant

Docker 部署中的网页只接受宿主机真实绝对路径，API 与 Worker 在任一时刻只获得该精确 Workspace Root 的 bind mount。`./zhixu up` 启动仅监听本机的受限 Host Controller，由它验证路径、先撤销旧 Grant、再替换唯一挂载并受控重建 API 与 Worker；不挂载父目录，也不把 Docker Socket 交给业务容器。

首次执行 `./zhixu up` 时不挂载项目自带 `workspace/` 或任何用户目录。网页与 Host Controller 先进入无 Active Workspace 的等待状态；只有用户在网页提交真实宿主机绝对路径后，Host Controller 才授予该精确 Root 并启动依赖 Workspace 的业务运行时。

## Considered Options

- 挂载父目录后让业务 API 选择子目录：切换无停机，但权限范围超过用户明确选择的目录。
- 由用户运行启动器命令修改路径：权限精确，但违背网页作为唯一配置入口的产品体验。
- 把 Docker Socket 挂入业务容器：可以自行重建，但等价于授予远超 Workspace Root 的宿主机控制能力。

## Consequences

- Root 切换会短暂重建 API 与 Worker，网页必须显示重连并在就绪后恢复。
- 首次启动需要在网页选择 Root 后再启动 API 与 Worker，但选择前没有任何用户目录权限。
- Host Controller 成为新的本机信任边界，必须使用 loopback、严格请求认证和固定操作面，且不得读取 Workspace 内容。
- 启动器为每次 Host Controller 生命周期生成一次性控制链接，网页自动交换为 HttpOnly、SameSite 控制会话并清除地址栏凭证；后续修改操作同时校验 CSRF 与严格 Origin，控制凭证不进入业务容器。
- 旧 Root 的授权必须在新 Root 激活前撤销，失败时不得让两个目录同时保持可访问；新运行时未就绪时自动恢复旧 Grant，恢复失败则保持无 Active Workspace。
- 切换前必须达到 Workspace Quiescence；超时取消切换，不能靠强杀正在写文件的运行时换取快速切换。
