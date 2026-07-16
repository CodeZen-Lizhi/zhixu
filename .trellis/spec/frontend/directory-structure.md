# 前端目录结构

> 定义 M1 创建前端骨架前必须遵守的目录和依赖边界。

## 适用范围

适用于 React + TypeScript Web 应用。仓库当前没有前端源码、包清单或生产组件，以下路径是依据 `docs/architecture/frontend-architecture.md` 提炼的设计约束，不代表已有实现，必须在 M1 创建应用后验证。

## 已确认事实

- `docs/architecture/frontend-architecture.md` 定义依赖流向：Routes/Pages → Feature Modules → Domain UI Models 与 Query/Command Clients，并单独设置 SSE Event Store。
- Feature 包括 dashboard、inbox、documents、optimization、search、rag、proposals、graph、collections、health、artifacts、review、workflows、settings。
- Feature 禁止导入其他 Feature 的内部状态；共享领域显示模型放在 Domain UI 层。
- `docs/architecture/api-and-events.md` 要求在前端边界使用生成或强类型 API Client，领域模块不得依赖生成 Client 类型。

## 规划目录

M1 应创建职责等价的结构。只有在依赖边界仍清晰时才可调整具体文件名。

```text
src/
├── app/             # 应用启动、Router、Provider、Error Boundary
├── routes/          # 路由定义和页面级组合
├── features/        # 按产品功能划分模块
├── domain-ui/       # 共享且与 API 无关的显示模型和投影
├── api/             # 生成/强类型 Client 边界和 API 归一化
├── events/          # SSE 连接、解码、游标恢复和失效映射
├── shared/          # 可复用展示组件和非领域工具
├── assets/          # 前端构建拥有的静态资源
└── test/            # 共享测试配置、Fixture 和浏览器辅助能力
```

Feature 可按需包含 `components`、`hooks`、`queries`、`commands`、`routes` 和测试；不得为了匹配目录图创建空目录。

## 依赖规则

- `app` 和 `routes` 只组合 Feature，不承载 Feature 业务规则。
- Feature 可通过公开入口依赖 `domain-ui`、`api`、`events` 和 `shared`。
- Feature 禁止深层导入其他 Feature 的文件或可变状态。
- `domain-ui` 禁止依赖 React 组件、生成 API 类型、Router 对象或组件库类型。
- `api` 统一负责传输 DTO 解码和归一化，组件不得强转原始响应。
- `events` 统一负责 SSE Envelope 解码、Last-Event-ID 和事件到 Query 失效映射；Feature 不得重复解析 SSE。
- `shared` 保持领域无关；产品概念属于 Feature 或 `domain-ui`。

## 命名约定

- Feature 目录使用 `docs/architecture/CONTEXT.md` 和 `docs/product/PRD.md` 中的稳定产品术语。
- React 组件及文件使用 PascalCase；Hook 使用 `use` + PascalCase；其他模块按 M1 确定的 Formatter/Linter 使用描述性小写名称。
- 测试默认与被测行为共置，除非 M1 明确建立集成测试边界。
- Feature 只通过显式公开入口暴露能力，禁止跨边界深层导入。

## 禁止模式

- 用单一全局 `components` 或 `utils` 目录混放无关领域行为。
- 在路由文件中解码传输数据、修改缓存或推进 Workflow 状态。
- Feature 间深层导入。
- 组件直接使用数据库形状或未校验的 Wire Payload。
- 在生成/强类型 Client 之外维护第二份手写 API 契约。
- 仅因框架模板存在而创建没有当前职责的目录。

## 验证

M1 前执行：

```bash
rg -n 'Feature Modules|Domain UI|Generated Client|SSE' docs/architecture/frontend-architecture.md docs/architecture/api-and-events.md
git diff --check
```

M1 后应增加依赖边界检查，并运行任务记录的前端 Lint、Type Check、Unit Test 和 Build 命令。

## M1 待代码验证

M1 必须确认实际前端根目录、测试共置规则、公开入口机制、路径别名、生成 Client 输出目录和 Asset 策略。只有真实文件存在后，才能用实际模块链接替换规划目录。
