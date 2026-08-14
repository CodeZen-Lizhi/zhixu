# Research: Eino planning document consistency review

- Query: Review `prd.md`, `design.md`, and `implement.md` for category clarity, Structured Output Graph versus full RAG boundaries, and cumulative effort omissions.
- Scope: internal
- Date: 2026-08-06

## Resolution after review

主规划已根据本报告完成修订：`design.md` 现在使用互斥四分类主矩阵并逐项列出五类正式消费者；Retriever/Embedding/Rerank 移到后续独立任务；`implement.md` 补齐五类消费者门禁、8-12 人日口径和包含基线/收口的累计工期。以下 Findings 保留为审查时点记录，不再代表当前未解决问题。

## Findings

1. **High - The four requested categories are not represented as a mutually exclusive migration matrix.** The PRD requires `replace / adapt through project Port / retain project implementation / defer` (`prd.md:23-25`), while the design has only three headings (`design.md:37,47,56`). Structured Output appears under all three (`design.md:43,51,58-60`); Tool Calling is labeled as replacement even though the described implementation is a bridge into the project executor (`design.md:44,90-97`); Embedding and Rerank are deferred but placed under Adapter (`design.md:53-54`). Add an explicit `defer` section and assign every row one primary category, with retained sub-contracts described in a separate ownership column.

2. **High - The checked claim that the matrix covers every production AI call chain is not supported by the design matrix.** The PRD marks complete coverage as done (`prd.md:31-39`), but relation evaluation, Artifact, Capture, and Organizing only appear later as rollout targets (`implement.md:35-43`), not as individual matrix entries with category and retention reason. Either add those consumers to the matrix or change the acceptance item back to incomplete.

3. **Medium - The short Structured Output Graph and full RAG boundary is internally consistent, but optional retrieval phase 5 has no approved consumer.** The design correctly limits the Graph to `INITIAL/REPAIR/REDUCED` and leaves Query Plan, retrieval, eligibility, citation, and proposal in `RAGExecutor` (`design.md:82-88`; `implement.md:35-43`). However, Retriever is only useful inside a Graph (`design.md:52`), while the only approved Graph contains no retrieval node; phase 5 therefore creates an orphan bridge unless a larger RAG Graph is separately approved (`implement.md:54-61`). Classify phase 5 as `defer / separate task`, or define the concrete approved consumer without broadening the current RAG scope.

4. **High - Every cumulative effort figure omits mandatory baseline and closeout work.** The published totals (`implement.md:93-100`) are exactly the sums of phases 1 onward and exclude phase 0 (`2-3` days) plus phase 6 (`2-4` days). If both remain mandatory, corrected cumulative totals are: Chat + Callback `13-21`, plus Structured Graph `21-33`, plus read-only Tool Calling `27-43`, and all optional retrieval work `32-51` person-days.

5. **Medium - Phase 3 expands to five consumers without consumer-specific gates or effort visibility.** The plan rolls out from RAG to relation evaluation, Artifact, Capture, and Organizing (`implement.md:39`), but the phase gate names only StructuredRunner equivalence and RAG-specific Citation/Faithfulness/refusal evaluation (`implement.md:40-43`). Add each consumer's domain acceptance gate and state whether its rollout is included in the `8-12` day phase estimate; otherwise the estimate and completion condition are ambiguous.

## Files found

- `.trellis/tasks/08-06-eino-layered-migration/prd.md`: requirements, accepted scope, and checked acceptance criteria.
- `.trellis/tasks/08-06-eino-layered-migration/design.md`: migration taxonomy, target boundaries, and adapter design.
- `.trellis/tasks/08-06-eino-layered-migration/implement.md`: phased execution, gates, rollback points, and effort totals.

## Code patterns

- This review concerns planning contracts only; no product-code pattern was evaluated.
- The relevant document pattern is a one-to-one classification matrix followed by phased gates and cumulative estimates; the current documents do not preserve that one-to-one mapping.

## External references

- None. This was an internal consistency review of the three requested task documents.

## Related specs

- `.trellis/workflow.md`: task planning and implementation phase separation.
- `.trellis/spec/backend/index.md`: project-owned workflow, authorization, and Eino dependency boundaries.

## Caveats / Not Found

- No direct contradiction was found between the explicitly stated Structured Output short-Graph boundary and the decision to retain full RAG orchestration.
- Estimates were checked arithmetically; no independent delivery benchmark was used to judge whether each phase range is realistic.
- The reviewed files were not modified.
