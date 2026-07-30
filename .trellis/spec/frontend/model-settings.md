# 模型设置前端契约

> 锁定 Settings 模型配置的 strict wire、状态所有权、Secret 生命周期和桌面/移动交互。

## Scenario: Model Settings Panel

### 1. Scope / Trigger

- 修改 `web/src/api/model-settings.ts`、`web/src/features/settings/ModelSettingsPanel*`、Settings 页面组合、
  模型设置 OpenAPI 或认证上下文时，必须应用本规范。
- UI 编辑 desired；active 与 API/Worker applied 只读展示，不能把保存渲染成已经生效。

### 2. Signatures

- 唯一 wire owner 是 `web/src/api/model-settings.ts`：所有 success/Problem JSON 从 `unknown` 严格解码后才能进入组件。
- 端点固定为 GET/PUT `/api/v1/settings/models` 与 POST `/api/v1/settings/models/test`。
- `expected_revision` 绑定保存；Secret 使用 `keep|replace|clear` tagged union，只有 `replace` 携带 write-only value。
- Settings 面板只由 Cookie Session 管理；API Token scope 不能让页面推导或声称具备此能力。

### 3. Contracts

- 页面分别显示 desired、active、API applied、Worker applied revision 与 fresh/phase；`restart_required`、rollout、
  unavailable 和 disabled 是独立状态。
- 表单从 `desired_settings` 初始化；active 摘要只读且 revision 0 使用 canonical disabled，不以 `null` 猜测。
- password 输入默认空表示 keep；clear 是显式控制。Secret、Endpoint Credential 不进入 URL、Browser Storage、
  Query key、日志、toast、错误详情或缓存持久化。
- 测试连接只发送所选 Chat 或 Embedding draft；pending 期间防重复，成功只说明该 draft 测试通过，不代表已保存或生效。
- 保存成功后立即清空本地 Secret value，并以服务端 Snapshot 更新 desired；active/applied 仍以响应事实展示。
- loading、test pending/result、save pending、conflict、rollout in progress、restart required、disabled、configured、
  unavailable 与 network/decode failure 必须有不同用户语义，禁止 Fake Success。
- 桌面与 390x844 移动布局不得横向溢出、遮挡或裁切长 Provider/Model/Error 文本；控件具备 label、键盘与可见焦点。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 未知/缺失字段、非法 revision/provider/phase | `ModelSettingsApiError(INVALID_RESPONSE)`，不渲染部分事实 |
| GET unavailable | 保留页面其他 Settings，模型区显示可恢复错误和重试 |
| PUT 409 | 保留用户非 Secret draft、清空 Secret value、显示当前 revision 并要求重新核对 |
| rollout 进行中 | 保存禁用或显示服务端冲突；不得 optimistic success |
| test 失败/超时 | 目标区显示脱敏错误；不修改 desired/active/applied |
| 保存后 desired != active | 明确 restart required，并保持 active/applied 旧 revision |
| API/Worker stale 或 mismatch | 显式 degraded/unavailable，不显示“已应用” |
| 非 Session 主体 | 403 语义可见；不能用 Token scope 绕过或隐藏成 Empty |

### 5. Good / Base / Bad Cases

- Good：用户编辑 desired、分别测试连接、保存后 Key 输入清空，页面展示新 desired 与旧 active，并在 restart 完成后
  由重新获取的 Snapshot 显示两个 applied 已一致。
- Base：revision 0 的两个 Provider disabled，仍能查看状态、选择 Provider、填写完整配置并保存。
- Bad：用星号掩码代替 keep、把 Key 放 React Query cache/localStorage、保存后立即显示 applied、组件 cast 原始 JSON、
  或用同一个模糊徽标合并 stale/failed/restart-required。

### 6. Tests Required

- API：严格 request encoder、success/Problem decoder、未知/重复字段、revision、Provider union、runtime/rollout 状态、
  Abort/network failure 和 Secret 不出现在错误序列化。
- Component/Page：loading/disabled/configured、test pending/success/failure、save pending/success/conflict、rollout、
  restart required、unavailable、Secret clear/replace/keep、Session-only 文案和键盘焦点。
- Canonical 门禁：`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run test --prefix web`、
  `npm run build --prefix web`。
- 浏览器必须打开真实开发栈 Settings，分别以 1440x900 与 390x844 验证布局、关键交互、Network、
  zero console warning/error、zero horizontal overflow，并扫描浏览器可观察面没有 Secret。

### 7. Wrong vs Correct

```text
Wrong: 保存返回 200 后显示“模型已生效”，并保留 password 输入便于下次提交。
Correct: 显示 desired 已保存和 restart required；立即销毁输入，active/applied 只跟随服务端事实。

Wrong: 用 API Token scope 决定能否展示管理操作，或把 403 当空配置。
Correct: 后端 Session-only 是授权事实；前端显式展示拒绝，不自行扩大权限。

Wrong: 移动端把桌面双列缩窄到溢出，长 Model/Error 被按钮遮挡。
Correct: 390x844 下重排为单列，文本可换行，命令控件保持稳定尺寸和可见焦点。
```
