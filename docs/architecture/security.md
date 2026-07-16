# 安全架构与威胁模型

## 1. 资产

- Markdown/PDF/附件。
- Git 历史。
- 模型 API Key。
- PostgreSQL 领域与运行数据。
- Approval 与审计。
- 用户回答和 Memory。

## 2. 威胁参与者

- 恶意 Source。
- 恶意网页。
- 不可靠模型输出。
- 本地低权限进程。
- 误操作用户。
- 暴露的自托管入口。

## 3. 信任边界

```mermaid
flowchart TB
    User["User"] --> Web["Web UI"]
    Web --> API["API Trust Boundary"]
    API --> DB["DB"]
    API --> Worker["Worker"]
    External["Source/Web/Model"] --> Worker
    Worker --> ToolBoundary["Tool Permission Boundary"]
    ToolBoundary --> Workspace["Workspace/Git"]
```

## 4. STRIDE 摘要

| 类别 | 例子 | 控制 |
|---|---|---|
| Spoofing | 自托管未授权访问 | 单用户认证、HTTPS |
| Tampering | 审批后文件变化 | Version Token、Change Hash |
| Repudiation | 否认写回 | Approval、Audit、Git Commit |
| Information Disclosure | Prompt 泄露密钥 | Secret 隔离、日志脱敏 |
| Denial of Service | 大 PDF/图谱查询 | 大小、超时、分页、配额 |
| Elevation | Source 请求写工具 | 服务端权限、Workflow Allowlist |

## 5. 身份

本地模式：

- 默认绑定 localhost。
- 回环地址只缩小暴露面，不等同于用户身份。
- Web UI 使用 Cookie Session，仍校验 Origin/CSRF。

自托管：

- 必须使用 HTTPS 和单用户认证。
- Web UI 使用 Secure、HttpOnly、SameSite Cookie Session。

自动化客户端：

- 使用可撤销、限 Scope、可过期的 API Token，不复用浏览器 Session Cookie。
- Token 明文只在创建时返回一次，服务端只保存不可逆摘要或等价安全表示。
- Token 禁止通过 URL Query 传递，必须支持轮换、撤销、最后使用时间和安全审计。

Session 要求：

- 登录后和敏感设置变化后轮换 Session ID。
- 登出、凭据变更和主动撤销立即使 Session 失效。
- Cookie Session 请求执行 CSRF Token 与 Origin 校验；SameSite 不能替代 CSRF 校验。

完整决策见 [ADR-0014](adr/0014-single-user-authentication.md)。

## 6. 授权

即使单用户，也按 Capability：

- Read Workspace。
- Read External。
- Create Proposal。
- Apply Knowledge。
- Git Write。
- Index Maintenance。

写权限短时、单任务、单 Proposal。

必须区分两层授权：

1. **身份与 Capability Authorization**：Session 或 API Token 证明调用者身份和允许发起的能力范围。
2. **Approval Write Authorization**：用户批准 Proposal 后，由服务端签发的一次性、短时、绑定 Workflow Run、Proposal Revision、Approval、Change Hash 和 Target Version 的写授权。

登录成功或持有高 Scope API Token 都不能直接获得 Apply Knowledge/Git Write；模型、Eino 和 Tool Request 也不能生成或扩大 Write Authorization。

## 7. Secret

- 环境变量或 Secret File。
- UI 只显示掩码。
- 不进入 DB 导出。
- 不发送给模型。
- 轮换后旧 Client 释放。

## 8. 文件

- Stable ID 解析路径。
- Canonical Root Check。
- Symlink Check。
- 临时文件权限。
- 文件大小限制。
- 禁止执行附件。

## 9. Prompt Injection

- System Policy 与 Source 分角色。
- Source 标记不可信。
- 权限不由模型控制。
- Tool Request 重新校验。
- 敏感操作需要 Approval。

## 10. SSRF

- 地址解析与重定向逐跳检查。
- 阻止私网/回环/元数据。
- DNS Rebinding 防护。
- 出站超时和大小。

## 11. Web

- CSRF。
- XSS/HTML 清理。
- CSP。
- Origin Check。
- 上传 Content-Type。
- 下载 Content-Disposition。

## 12. 数据库

- 独立应用用户。
- 最小权限。
- 参数化 SQL。
- 不将 DB 端口暴露公网。
- Backup Encryption。

## 13. Git

- 固定命令 Allowlist。
- 不执行 Hook（或使用受控环境禁用）。
- Commit Message 不包含 Secret。
- 不允许任意 remote push。

## 14. 模型数据

- 发送最小证据窗口。
- Provider 数据保留配置可见。
- 本地敏感 Topic 可禁止云模型。
- Full Prompt Debug 默认关闭。

## 15. 审计

必须审计：

- Login/Auth。
- Session/API Token 创建、轮换、撤销和拒绝。
- Approval。
- Tool Authorization。
- File/Git。
- Settings/Secret 更新。
- Security Block。

## 16. 安全失败模式

- 无法确认权限 → 拒绝。
- Git/DB 不一致 → 只读。
- 引用失效 → 不发布回答。
- Secret Store 不可用 → 模型功能不可用。
- 安全检查异常 → Source 隔离。

## 17. 安全测试

- Path Traversal。
- Symlink。
- SSRF。
- Prompt Injection Corpus。
- XSS Markdown。
- CSRF。
- Secret Leak。
- SQL Injection。
- Unauthorized Write。
- Session Fixation/Revocation。
- API Token Scope/Expiry。
- 已登录但无 Approval Write Authorization 的写入拒绝。

## 18. 依赖与镜像

- SBOM。
- CVE Scan。
- 非 root Container。
- 固定 Base Image。
- 依赖升级评测和回归。
