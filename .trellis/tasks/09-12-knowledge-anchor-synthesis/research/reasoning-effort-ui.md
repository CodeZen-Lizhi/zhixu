# 模型思考强度：前端实施

2026-09-16，bounded UI 实施完成。沿用现有模型设置页面和控件，未改 CSS、生成客户端、服务端、公共任务计划或规范。

## 改动

- `web/src/api/model-settings.ts`：Chat summary 必有 `reasoningEffort`；strict decoder 接受模型默认空值和五档枚举，拒绝缺失、未知值及 disabled/Ollama 非空组合。Input 可省略此字段兼容旧调用，省略编码为默认空值；保存和测试都通过同一 encoder 及真实生成客户端发送 `reasoning_effort`。显式 undefined/null 拒绝，不静默降低强度。
- `web/src/features/settings/ModelSettingsPanel.tsx`：增加有 label 与 aria-describedby 的原生 select，提供模型默认/低/中/高/很高/最高。表单读取 desired；active 摘要读取服务端实际 active 值。选择模型默认不表示关闭思考；提示较高档位的时间/费用以及模型支持差异。Ollama、历史隐式本地配置、disabled 禁用控件；切换 provider 和提交 owner 双重保证不提交旧在线强度。保存/应用/CAS/Secret 状态机继续复用。
- 仅扩展既有 `model-settings.test.ts` 和 `ModelSettingsPanel.test.tsx`，同时更新其中受影响的 Chat fixture；搜索未发现其他 handwritten 模型配置 fixture。

## 验证

- `cd web && npm run test -- src/api/model-settings.test.ts src/features/settings/ModelSettingsPanel.test.tsx`：最终 2 文件、53 项通过，6.48s，退出0。包括全部枚举、非法/不支持组合、生成 transport 的保存和测试同档位、旧调用默认值；UI 仅改强度进入 dirty、保存后重新挂载读取 desired、active 不提前变化、应用轮询完成显示真实档位、切换 Ollama/disabled 清空旧档位。
- `cd web && npm run typecheck`：退出0。初次检查指出 generated disabled/Ollama 分支要求字面空值；已收窄 encoder 类型并再次通过，未改 generated。
- `cd web && npx eslint src/api/model-settings.ts src/api/model-settings.test.ts src/features/settings/ModelSettingsPanel.tsx src/features/settings/ModelSettingsPanel.test.tsx`：退出0。
- 本次4文件 `git diff --check`：退出0。
- Vitest 运行输出 Node 关于未指定 localstorage-file 的 ExperimentalWarning；测试本身通过。未将模拟组件结果称作实际数据库/热应用或外部模型验收。

## 交主会话

真实浏览器由主会话负责。已有产品入口 `/settings?section=models`，展开按钮名称 `配置对话模型`，强度控件名称 `对话模型思考强度`，active 区名称 `Chat 当前生效配置`。后端必须含新 `reasoning_effort` 响应字段方能通过 strict decoder。建议复用隔离设置运行时验证：选择高→仅保存→刷新待应用仍高且 active 原值→应用→active 高；390px 检查新增控件和说明无横向溢出。

没有调用真实模型、写用户模型设置或提交代码。UI 实施没有剩余阻塞；真实浏览器和跨层独立审查待主会话整合。
