# 模型设置前端契约

> 锁定 Settings 模型配置的 strict wire、无重启热应用、状态所有权、Secret 生命周期和桌面/移动交互。

## Scenario: Model Settings Panel

### 1. Scope / Trigger

- 修改 `web/src/api/model-settings.ts`、`web/src/features/settings/ModelSettingsPanel*`、Settings 页面组合、
  模型设置 OpenAPI 或认证上下文时，必须应用本规范。
- UI 编辑 desired；active 与 API/Worker applied 只读展示，不能把保存渲染成已经生效。

### 2. Signatures

- 唯一 wire owner 是 `web/src/api/model-settings.ts`：所有 success/Problem JSON 从 `unknown` 严格解码后才能进入组件。
- 端点固定为 GET/PUT `/api/v1/settings/models`、POST `/api/v1/settings/models/test` 与
  POST `/api/v1/settings/models/activations`；激活请求只允许 `{expected_revision}`。
- `expected_revision` 绑定保存；Secret 使用 `keep|replace|clear` tagged union，只有 `replace` 携带 write-only value。
- Snapshot 严格包含 desired/active、API/Worker runtime、rollout、API/Worker participants、`apply_required`、
  `restart_required=false` 与 capabilities；任何额外、缺失或不可能的状态组合都 fail closed。
- Settings 面板只由 Cookie Session 管理；API Token scope 不能让页面推导或声称具备此能力。

### 3. Contracts

- 页面分别显示 desired、active、API applied、Worker applied revision 与 fresh/phase；rollout/participant progress、
  failed、unavailable、disabled 与 apply-required 是独立状态。`restart_required=true` 是非法响应，不是 UI 状态。
- 表单从 `desired_settings` 初始化；active 摘要只读且 revision 0 使用 canonical disabled，不以 `null` 猜测。
- password 输入默认空表示 keep；clear 是显式控制。Secret、Endpoint Credential 不进入 URL、Browser Storage、
  Query key、日志、toast、错误详情或缓存持久化。
- 测试连接只发送所选 Chat 或 Embedding draft；pending 期间防重复，成功只说明该 draft 测试通过，不代表已保存或生效。
- “仅保存”只推进 desired；“保存并应用”必须先保存并使用响应中的 exact desired revision 发起 activation。
  两种路径都在保存完成后立即销毁本地 Secret value，active/applied 只跟随服务端 Snapshot。
- activation `202` 只表示持久操作已接受；页面必须持续轮询权威 Snapshot，直到 idle/failed。请求取消或响应丢失后
  通过 target/rollout/active/applied 回查恢复，不能重复制造不同 target 或显示 Fake Success。
- loading、test pending/result、save pending、activation preparing/arming/activating/failed、conflict、apply-required、
  disabled、configured、unavailable 与 network/decode failure 必须有不同用户语义。
- failed rollout 在当前 wire 中必须携带稳定错误码且 `retryable=true`；不可重试失败组合必须由 strict decoder 拒绝，
  组件不维护不可达的按钮分支。
- 桌面与 390x844 移动布局不得横向溢出、遮挡或裁切长 Provider/Model/Error 文本；控件具备 label、键盘与可见焦点。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 未知/缺失字段、非法 revision/provider/phase | `ModelSettingsApiError(INVALID_RESPONSE)`，不渲染部分事实 |
| GET unavailable | 保留页面其他 Settings，模型区显示可恢复错误和重试 |
| PUT 409 | 保留用户非 Secret draft、清空 Secret value、显示当前 revision 并要求重新核对 |
| rollout 进行中 | 展示 API/Worker participant 进度，相关写控件禁用；不得 optimistic success |
| test 失败/超时 | 目标区显示脱敏错误；不修改 desired/active/applied |
| 保存后 desired != active | 明确“已保存，尚未应用”，保持 active/applied 旧 revision并提供应用操作 |
| activation 返回 202 或响应丢失 | 轮询/回查权威 Snapshot；仅在 active/applied 收敛后显示已生效 |
| activation 409 | 显示版本冲突，刷新当前 revision；不得用旧 target 静默重试 |
| target probe 失败 | 显示稳定错误与重试；active/applied 继续展示 previous revision |
| API/Worker stale 或 mismatch | 显式 degraded/unavailable，不显示“已应用” |
| `restart_required=true`、failed 且 retryable=false | `ModelSettingsApiError(INVALID_RESPONSE)`，不渲染部分事实 |
| 非 Session 主体 | 403 语义可见；不能用 Token scope 绕过或隐藏成 Empty |

### 5. Good / Base / Bad Cases

- Good：用户编辑并测试配置，点击“保存并应用”；Key 输入立即清空，页面展示双进程进度，随后由权威 Snapshot
  显示 desired/active/API/Worker applied 一致，整个过程不提示或要求重启容器。
- Base：revision 0 的两个 Provider disabled，仍能查看状态、选择 Provider、填写完整配置并保存。
- Bad：用星号掩码代替 keep、把 Key 放 React Query cache/localStorage、保存后立即显示 applied、组件 cast 原始 JSON、
  仅凭 activation 202 显示成功、提供“重启以生效”主路径，或用同一个模糊徽标合并 stale/failed/apply-required。

### 6. Tests Required

- API：activation exact request encoder、success/Problem decoder、未知/重复字段、revision、Provider union、runtime/rollout/
  participant 状态、`restart_required=false`、Abort/network failure 和 Secret 不出现在错误序列化。
- Component/Page：loading/disabled/configured、test pending/success/failure、仅保存、保存并应用、202 polling、响应丢失恢复、
  conflict、preparing/arming/activating/failed、unavailable、Secret clear/replace/keep、Session-only 文案和键盘焦点。
- Canonical 门禁：`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run test --prefix web`、
  `npm run build --prefix web`。
- 浏览器必须打开真实开发栈 Settings，分别以 1440x900 与 390x844 验证布局、关键交互、Network、
  zero console warning/error、zero horizontal overflow，并扫描请求/响应、DOM、Storage 与日志没有 Secret。
- 隔离 Compose smoke 必须覆盖成功 Apply、错误凭据 precommit 失败并保留旧 active、修正后重试，以及前后 API/Worker
  container id、StartedAt 相同且 RestartCount 为零；cleanup 只能删除本次随机项目的精确资源。

### 7. Wrong vs Correct

```text
Wrong: 保存返回 200 后显示“模型已生效”或“请重启容器”，并保留 password 输入便于下次提交。
Correct: 显示 desired 已保存；立即销毁输入，显式发起 exact target activation，active/applied 只跟随服务端事实。

Wrong: activation 返回 202 就结束进度，或网络中断后直接再造一次应用请求。
Correct: 202 后轮询权威 Snapshot；响应丢失先按 rollout/target/active/applied 恢复，再决定是否允许重试。

Wrong: 用 API Token scope 决定能否展示管理操作，或把 403 当空配置。
Correct: 后端 Session-only 是授权事实；前端显式展示拒绝，不自行扩大权限。

Wrong: 移动端把桌面双列缩窄到溢出，长 Model/Error 被按钮遮挡。
Correct: 390x844 下重排为单列，文本可换行，命令控件保持稳定尺寸和可见焦点。
```
