<!-- TRELLIS:START -->
# Trellis Instructions

These instructions are for AI assistants working in this project.

This project is managed by Trellis. The working knowledge you need lives under `.trellis/`:

- `.trellis/workflow.md` — development phases, when to create tasks, skill routing
- `.trellis/spec/` — package- and layer-scoped coding guidelines (read before writing code in a given layer)
- `.trellis/workspace/` — per-developer journals and session traces
- `.trellis/tasks/` — active and archived tasks (PRDs, research, jsonl context)

If a Trellis command is available on your platform (e.g. `/trellis:finish-work`, `/trellis:continue`), prefer it over manual steps. Not every platform exposes every command.

If you're using Codex or another agent-capable tool, additional project-scoped helpers may live in:
- `.agents/skills/` — reusable Trellis skills
- `.codex/agents/` — optional custom subagents

Managed by Trellis. Edits outside this block are preserved; edits inside may be overwritten by a future `trellis update`.

<!-- TRELLIS:END -->

## 项目依赖选型规则

- 新增通用基础设施、协议处理、框架能力或第三方集成前，必须先检索项目已有实现、标准库和主流成熟框架。
- 在全部强制安全、数据一致性、部署和兼容约束均满足的前提下，成熟方案能够覆盖至少 80% 加权需求时，默认复用该方案，不得重复自研。
- 用户已经指定或项目已经批准的技术选型属于硬约束；更换、绕开或并行引入替代方案前，必须先取得用户明确确认。
- 确需自研时，必须用 ADR 记录候选方案、需求覆盖、未采用原因、自研边界、维护成本、测试方式和退出路径。
- 具体判定与例外要求见 [`docs/architecture/adr/0019-mature-framework-first.md`](docs/architecture/adr/0019-mature-framework-first.md)。
