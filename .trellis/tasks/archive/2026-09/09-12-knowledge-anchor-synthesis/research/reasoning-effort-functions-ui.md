# 按功能思考强度：前端实施

2026-09-16，UI 实施及定向检查完成；真实浏览器交主会话整合。

## 行为与文件

- `web/src/api/model-settings.ts`：固定8个功能键的 `ChatReasoningFunction` 和 `reasoningEffortByFunction` projection。GET 必须返回对象；未知功能、未知值、null、数组及 disabled/Ollama 的非空覆盖均拒绝。写入兼容省略整个 map（编码为 `{}`），缺 key 表示继承，空字符串表示显式模型默认。保存/测试复用严格 encoder 和生成 transport，不把 UI 的 `inherit` 放入 wire。
- `web/src/features/settings/ModelSettingsPanel.tsx`：原统一设置改称“全局默认思考强度”，下方8个功能控件按研究固定顺序展示。继承选项实时显示全局实际档位，独立“模型默认”保留空字符串；切换到继承删除该 key。draft 初始化和命令构造均复制 map，编辑不改 Query 保存快照。Ollama/disabled 清空所有覆盖、禁用控件。
- active 摘要增加“各功能生效强度”原生 details，每项明确显示“继承全局 · 档位”或“档位（单独设置）”，只消费服务端 active；保存后仍显示旧 active。全局默认和单功能独立值分开表达。
- `web/src/features/settings/model-settings.css`：复用既有字号/边框/间距，功能选项桌面两列、520px以下单列；select继承44px高度，details summary至少44px。无新依赖、预设或自动选档。
- 扩展两个既有测试文件 `web/src/api/model-settings.test.ts`、`web/src/features/settings/ModelSettingsPanel.test.tsx`，更新其中模型设置 fixture，无其他 handwritten fixture 受影响。

## 检查

- `cd web && npm run test -- src/api/model-settings.test.ts src/features/settings/ModelSettingsPanel.test.tsx`：最终2文件、56项通过，7.09s，退出0。新增关键行为为两个不同功能档位、第三功能显式模型默认、第四功能继承，保存/测试真实生成请求及后续GET保留差异；组件保存重开、exact revision应用后active显示、从单独设置改回继承与旧快照不被污染。
- `cd web && npm run typecheck`：退出0，使用 backend 本轮生成客户端。
- `cd web && npx eslint src/api/model-settings.ts src/api/model-settings.test.ts src/features/settings/ModelSettingsPanel.tsx src/features/settings/ModelSettingsPanel.test.tsx`：最终退出0。首轮动态delete被项目lint拒绝，已改为固定功能键构造新map，未添加lint例外。
- 修改的5个Web文件 `git diff --check`：通过。
- Vitest输出Node未指定localstorage-file的ExperimentalWarning；非业务失败。组件模拟结果不代表真实热运行时或外部模型语义质量已验收。

## 浏览器定位

入口 `/settings?section=models` → `配置对话模型`。全局selector仍为 `对话模型思考强度`；功能容器role group名称 `按功能设置`；功能selector名称是中文功能名加`思考强度`，例如 `文件简介与标签思考强度`、`主笔记融合思考强度`。UI select继承值为 `inherit`，模型默认值为空字符串。active 区 `Chat 当前生效配置`，展开 `各功能生效强度` 可查看所有实际档位。

未改用户运行配置、后端、生成文件或共享任务计划；未提交。浏览器布局及真实服务端激活后读回待主会话完成。
