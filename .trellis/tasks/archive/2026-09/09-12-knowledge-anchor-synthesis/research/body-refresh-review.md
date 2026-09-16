# 118 正文引用刷新审查

2026-09-15 独立 impact-check 审查结论（channel seq 7911），root 汇总；该 P2 已修复并定向复核关闭（seq 9484）。

## P2：实际片段许可被 provenance 种子来源错误收窄

`workflow/synthesis_body_refresh_prepare.go:22` 使用上游 revision 的 SourceEvent.Source 调用 ReadSynthesisAnchorAdmission，而 `adapter/postgres/anchor_sources.go:67` 只返回该精确来源版本的证据。上游发布片段可能使用另一已获下游许可的来源。此时真实许可被漏读，准备过程误报 SYNTHESIS_BODY_REFRESH_SCOPE_REQUIRED。SourceEvent 只能保留真实溯源身份，不能代替本次完整证据许可目录。

处理：按实际受影响片段原始证据集合读取、冻结并在 apply 逐来源、逐 span 重验当前 scope/许可；旧引用不再隐式扩大许可。真实 PostgreSQL + River 三场景通过（113.180s，/tmp/body-refresh-admission-final.log）：event=A、实际更新证据=B 且 B 已准入时生成候选；apply 时缺少 B 许可或 scope 变化均拒绝、候选和正式指针不变。负向场景是事务故障注入下真实 owner fence 的拒绝与回滚，不冒充新增撤销 API 或真实并发撤销流程。未修改 Schema。

## 已复核与限制

独立审查已核对完整请求读取的 UTC 规范化和冻结哈希路径，TestBodyRefreshFrozenHashTimezoneIndependent 定向通过。root 完成118隔离迁移、Schema空库恢复、Atlas validate与原真实River回归。原River场景的证据与event种子一致，因此不覆盖上述P2。未声称不存在其他问题，也未将固定Provider语义结果当作外部模型质量证明。
