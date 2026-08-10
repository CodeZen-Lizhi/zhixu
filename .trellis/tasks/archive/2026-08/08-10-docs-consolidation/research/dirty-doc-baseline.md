# 相关脏文档迁移基线

记录时间：2026-08-10。以下文件在本任务开始前已有用户未提交修改，迁移必须在当前工作树内容上进行。

| 文件 | 迁移前工作树 SHA-256 | 相对 Git 基线 |
|---|---|---|
| `docs/architecture/adr/README.md` | `cddfc00cd6101190d8857f69300e8e7719ffdad2d019d478753c16d1ddac9992` | `+1/-0`，新增 ADR-0019 索引 |
| `docs/architecture/technology-stack.md` | `3d3f49a19acfefda5c280218c4abefe41c4899ed665eeed826c8eca6cb9c30eb` | `+6/-1`，新增成熟框架 80% 门禁与 ADR-0019 引用 |
| `docs/product/2026-08-01-requirement-optimization-list.md` | `8240ececddfac6c4f8aacc87fb1f338e83a52f1f35029110fc490e6cab85f576` | `+327/-0`，新增/扩充 TODO 1-12 |

## 必须保留的独有内容

- ADR 索引继续包含 `0019-mature-framework-first.md`，状态为 accepted。
- `system-design.md` 或等价当前架构章节保留成熟方案优先、强制约束满足、至少 80% 加权覆盖、偏离既定选型需用户确认和自研例外材料。
- `roadmap.md` 保留需求优化清单 TODO 1-12 的标题、优先级、依赖和目标；详细实施边界与验收必须保存在后续 Trellis 任务或本任务 research，不得丢失。
- `requirements.md` 保留 TODO 2 与 TODO 4 中独有的用户需求和产品验收边界；TODO 3、5-12 的技术实施要求不得被误写成当前架构。

## 最终核对

- [x] ADR-0019 索引和正文仍存在。
- [x] 成熟框架 80% 门禁可从系统设计/ADR 定位。
- [x] TODO 1-12 均可从路线图定位。
- [x] TODO 2/4 的产品需求可从 requirements 定位。
- [x] TODO 3、5-12 的详细技术约束已在 [`source-requirement-optimization-list.md`](source-requirement-optimization-list.md) 原文留档，并由路线图指明后续任务化边界。

留档文件 SHA-256 为 `8240ececddfac6c4f8aacc87fb1f338e83a52f1f35029110fc490e6cab85f576`，与迁移前工作树完全一致。
