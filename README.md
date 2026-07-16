# ZHIXU / 知序

> A local-first, self-organizing knowledge workbench.  
> 本地优先的自组织知识工作台。

知序致力于将零散的 Markdown、PDF、网页和个人笔记整理为可追溯、可关联、可演进、可复习的知识体系。

## Core ideas

- Local-first：正式知识由用户本地持有。
- Evidence-first：回答、关系和修改都能追溯来源。
- Human-in-the-loop：AI 先生成变更建议，用户审批后才写入。
- Versioned knowledge：使用 Markdown 与 Git 保存正式知识及历史。
- Durable agents：Agent 工作流支持持久化、恢复、重试和审计。

## Planned capabilities

- 文章优化与版本管理
- 混合检索与带引用 RAG 问答
- 知识关系分析与可操作知识图谱
- 语义反向链接与知识健康检查
- Proposal、人工审批与 Git 安全写回
- 面试文档、学习路径、闪卡与智能复习
- Agent Workflow、Tool Calling、Memory 与 AI 评测

## Planned stack

- Go
- React + TypeScript
- PostgreSQL + pgvector
- Markdown + Git
- Docker Compose

## Status

The project is under active development. The current skeleton provides a Go API and worker, a React web application, and PostgreSQL with pgvector.

项目正在开发中，当前工程骨架包含 Go API 与 Worker、React Web，以及启用 pgvector 的 PostgreSQL。

## Local development

Prerequisites:

- Go 1.25.4
- Node.js 24.18.0 and npm 11.7.0
- Docker with Docker Compose

Install dependencies and run all local quality gates:

```bash
make web-install
make test
```

Start the complete local stack. `.env.example` contains development-only values and must not be used as production secrets.

Docker Compose mounts `ZHIXU_WORKSPACE_ROOT` into the containers at `/workspace`.
Create that host directory first, or override the value in a local `.env`; the
Workspace form should use `/workspace` when running through Compose.

```bash
make compose-up
```

Then open <http://127.0.0.1:8080>. Health and dependency status are available at:

- `GET /livez`: process liveness
- `GET /readyz`: readiness including PostgreSQL connectivity
- `GET /api/v1/system/status`: API and database status used by the web page
- `POST /api/v1/workspaces`: create the single active Workspace and record its Git baseline
- `GET /api/v1/workspaces/{id}`: reopen the persisted Workspace
- `POST /api/v1/workspaces/{id}/scan`: scan supported files and register immutable Source Version metadata

Stop the stack and remove its local database volume:

```bash
make compose-down
```

Useful standalone checks:

```bash
make go-test go-vet
make web-lint web-typecheck web-test web-build
make openapi-check compose-check docker-build
```

The isolated Eino adoption gate is available under `poc/eino` and is included in `make test`. The current decision is not to adopt Eino formally because several real integration gates and the provider smoke remain incomplete; see `poc/eino/report.md`.

## Documentation

- [Documentation index](docs/README.md)
- [Product requirements](docs/product/PRD.md)
- [Architecture documentation](docs/architecture/README.md)

## License

To be determined.
