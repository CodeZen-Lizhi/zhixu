# Tool Calling 与工具安全

## 1. 目标

确保 Agent 只能通过注册、验证、授权和审计后的工具访问外部能力。

## 2. 架构

```mermaid
flowchart LR
    Agent["Agent Tool Request"] --> Registry["Tool Registry"]
    Registry --> Schema["Schema Validation"]
    Schema --> Auth["Permission Decision"]
    Auth --> Executor["Tool Executor"]
    Executor --> Adapter["Concrete Adapter"]
    Adapter --> Audit["Tool Call Audit"]
```

## 3. Tool Definition

- name。
- description。
- input_schema。
- output_schema。
- permissions。
- side_effect_level。
- timeout。
- retry_policy。
- idempotency_policy。
- sensitive_fields。
- allowed_workflows。

## 4. 权限

| 权限 | 能力 |
|---|---|
| READ_LOCAL | 读取 Workspace 内资料 |
| READ_EXTERNAL | 访问用户允许网页 |
| WRITE_PROPOSAL | 创建候选/Proposal |
| WRITE_KNOWLEDGE | 执行已批准文件写入 |
| GIT_WRITE | 创建批准 Commit |
| INDEX_MAINTENANCE | 索引构建/切换 |
| EVALUATION_RUN | 运行评测 |

## 5. 授权上下文

工具执行前先验证调用者身份和普通 Capability：

- Web 请求来自有效 Cookie Session，并通过 CSRF/Origin 校验。
- 自动化请求来自未过期、未撤销且 Scope 匹配的 API Token。
- Workflow 内部调用来自服务端持久化的 Run/Node Context，不信任模型自报身份。

身份认证与普通 Capability 只允许调用者请求工具，不能代替一次性 Approval Write Authorization。

写权限要求：

- Workflow Run。
- Node ID。
- Proposal ID。
- Proposal Revision。
- Approval ID。
- Approved Change Hash。
- Target Version。
- Expiry。

授权令牌只在服务端存在，不发送给模型。

Write Authorization 必须一次性或幂等消费、短时有效，并严格绑定上述字段。Session、API Token、Eino Context、模型 Tool Call 或管理员式 Scope 都不能自行构造、延长或扩大该授权。授权消费只确认数据库中的审批和版本快照；文件/Git 写回必须在 M5-04 的实际原子替换点重新执行 Target Version CAS。

当前实现落在 `internal/changecontrol`：服务端只保存 `token_hash`，授权记录位于 `change_control.tool_authorization`，签发前复核 Proposal/Approval/Change Hash/目标哈希和已持久化 Workflow Run/Node；消费先完成完整绑定校验，再在数据库行锁事务内校验审批快照、过期/撤销状态并将 `issued` 原子转为 `consumed`。当前目标文件哈希仅作快速失败检查，不与数据库形成跨存储原子事务。本切片不执行文件、Git 或索引副作用，后续 M5-04 必须把消费结果接入 Safe Writeback Saga 并在写入点做最终 CAS。

明文 Credential 只在首次签发时返回，服务端不保存可恢复副本；首次响应丢失后的幂等重放不会再次返回 Credential。调用方只能使用新幂等键重新签发或等待短 TTL 过期，不能通过查询接口恢复写凭据。

## 6. 核心工具

### SearchKnowledge

- 只读。
- 返回 Evidence Items。
- 不返回密钥和任意文件。

### ReadSource/ReadDocument

- 路径由稳定 ID 解析。
- 不允许模型传入任意绝对路径。

### FetchWebPage

- READ_EXTERNAL。
- SSRF 防护。
- 内容大小和类型限制。

### CalculateDiff

- 纯函数。
- 输入为已授权内容。

### ApplyApprovedPatch

- WRITE_KNOWLEDGE。
- 必须匹配 Change Hash。
- 幂等。

### CreateGitCommit

- GIT_WRITE。
- Commit 关联 Proposal。
- 不允许任意 Git 参数。

### RebuildIndex

- INDEX_MAINTENANCE。
- 需要范围和 Index Version。

## 7. Prompt Injection

防御：

- Source 标记 untrusted。
- Tool Result 标记 data。
- System Policy 不与 Source 拼接为同等角色。
- Tool Permission 由服务端 Workflow 决定。
- Eino 或其他 Agent Framework 只能转交 Tool Request，不参与身份、Capability 或 Write Authorization 决策。
- 模型不能请求未注册工具。

检测：

- 要求忽略系统规则。
- 要求读取 Secret。
- 要求访问 Workspace 外路径。
- 要求自动批准。

检测到可疑内容：

- 添加风险标记。
- 高风险 Source 可隔离。
- 记录安全审计。

## 8. 文件路径

- 只接受 Stable Object ID。
- Adapter 解析 canonical path。
- filepath.Clean/Abs 后检查根目录。
- 检查符号链接最终目标。
- 禁止 NUL、设备文件和特殊路径。

## 9. 命令执行

- 使用固定可执行文件。
- 参数数组，不拼 shell。
- 禁止用户输入成为命令名。
- 超时和输出大小限制。
- 工作目录固定。

Git 允许命令白名单：

- status。
- diff。
- add 指定文件。
- commit 固定参数。
- show。
- rev-parse。

## 10. SSRF

- 只允许 http/https。
- DNS 解析后检查地址。
- 阻止 loopback、private、link-local、multicast、metadata。
- 每次重定向重新检查。
- 限制重定向、时间、大小。
- 可选域名 Allowlist。

## 11. Tool Output

- 输出通过 Schema。
- 截断超大响应。
- Secret 脱敏。
- HTML 清理。
- 不直接作为下一条 System Message。

## 12. 幂等

有副作用工具必须：

- 要求 idempotency_key。
- 执行前查询已有结果。
- 成功后原子保存结果。
- 重复调用返回既有结果。

## 13. 超时与重试

- Read/Search 可安全重试。
- Web Fetch 可安全重试但受频率限制。
- File/Git 根据幂等记录重试。
- 非幂等未知结果进入人工恢复。

## 14. 审计

记录：

- 调用者。
- Workflow/Node。
- Tool。
- 权限。
- 参数摘要。
- 目标。
- Idempotency Key。
- 耗时。
- 结果/错误。

不记录：

- 完整 API Key。
- 未脱敏 Authorization。
- 不必要的全文。

## 15. 威胁场景

| 场景 | 防护 |
|---|---|
| PDF 指令要求删除文件 | Source 不影响权限 |
| 模型请求 /etc/passwd | Stable ID + Workspace Root |
| URL 指向 169.254.169.254 | SSRF 地址阻止 |
| 重复 Git Tool Call | Idempotency + Proposal Commit Mapping |
| Approval 后内容被修改 | Change Hash/Version 校验 |
| Tool 返回恶意 Prompt | Output 当作 untrusted data |
| 已登录用户直接调用写工具 | 仍要求一次性 Approval Write Authorization |
| 高 Scope API Token 申请任意写入 | Scope 只允许发起流程，不绕过 Proposal/Approval |
| Eino Graph 构造写权限 | 服务端忽略框架权限字段并重新判定 |

## 16. 测试

- 权限矩阵。
- Schema Fuzz。
- 路径穿越。
- Symlink Escape。
- SSRF Redirect。
- Command Injection。
- Duplicate Side Effect。
- Audit Redaction。
- Session/API Token 与 Tool Capability 映射。
- 登录成功但缺少 Approval Write Authorization 的拒绝路径。
- Eino/模型伪造授权上下文。
