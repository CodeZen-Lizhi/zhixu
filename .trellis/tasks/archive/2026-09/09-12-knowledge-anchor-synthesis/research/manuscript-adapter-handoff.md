# 完整稿与 Goldmark 映射器接线说明

2026-09-15 最终裁决。domain v2 具体接口及限定验证见 [manual-manuscript-contract.md](manual-manuscript-contract.md)。本切片没有完成持久化、合并应用、发布和 UI 接线，不能宣称人工稿已可发布。

## 当前映射行为

`internal/organizing/adapter/manuscript/mapper.go` 的零值 `manuscript.Mapper{}` 实现 domain port，每次独立创建 Goldmark parser。字符串仅定位标记；映射要求已知 marker 唯一、顶层完整 comment/list/comment 结构、正确顺序和不重叠。

AST 与 exact 骨架不证明人工文字只影响当前条目。接受跨项语境 P2：最终全文只要不同于机器全文，就设置 `ContextReviewRequired=true` 且全部退出可信映射，原全文逐字保存。即使只是项内批注或空白变化也如此。普通条目为 `CONTEXT_REVIEW`，历史排除条目为 `PREVIOUSLY_UNTRUSTED`。不添加关键词黑名单或新模型审查；尚无已授权且实现的显式局部编辑语义契约，不承诺保留人工稿中未改项的可信状态。

exact 机器全文且 AST 证明通过时，所有非历史排除项保留映射。`IneligibleItemIDs` 必须由服务端 owner 持续继承，不能因恢复旧字节、客户端请求或人工审批清空。

## 接线边界

1. `SynthesisManuscriptMachine` 为内部受信输入。owner 绑定已核验的机器结果、来源、scope 和身份，不接受客户端 mappings/排除清单作为证明。
2. 使用 manual-content-review.md 最后补充节的 L/P/F 双阶段合并。冻结裁决全文后调用 `NewSynthesisManuscript(machine, finalContent, manuscript.Mapper{})`。全文 hash 不代替 merge receipt、文件捕获证明或 CAS。
3. domain v2 revision/snapshot 已支持可选 envelope、独立 hash 版本和 `Content()`，读取完整原文；顶层 Items 仅对应已核验映射子集，允许为空。MachineItems 只供机器更新基线/审计，不得作为 Interview/body inclusion 的零项 fallback。
4. `ValidateIntegrity()` 是无 parser 依赖的结构/字节/hash 自校验，不证明来源和映射可信。app/store 创建/重读仍须 `Validate(expected, mapper)` 并绑定来源、scope、receipt 与 owner。
5. apply/publication 重读 L/P/F/scope，沿既有 AGENT 链，拒绝未知 USER/SYSTEM drift。需独立完成持久化 envelope、DB closure、Article/Proposal 同文绑定及 P 发布身份/F 文件 hash 双基线。
6. 后续 UI 应将人工全文旧来源标为历史参考并明确待复核，不能冒充当前来源证据；本切片未实施或验证 UI。

## 持久回归

真实回归文件为 `internal/organizing/adapter/manuscript/mapper_test.go`，覆盖 exact 全文映射/UTF-8 字节偏移/block hash、普通局部改写、跨项否定正文和嵌套列表、外围上下文、伪标记/结构、历史排除、客户端自洽重哈希映射拒绝。原任务目录探针保留，但不取代 Go 回归。

最近限定 `go test ./internal/organizing/adapter/manuscript ./internal/organizing/domain -run 'TestMapper|TestSynthesis(Manuscript|RevisionHashSnapshotAndSafeMarkdown)' -count=1` 与对应两包 `go vet` 通过；v1 黄金测试未修改。真实合并→落库→发布及 UI 尚未验证。

## 跨版本排除 tombstone 修正

`IneligibleItemIDs` 是跨版本墓碑集合，不限于当前 MachineItems；domain 仅要求 ID 有效且唯一，暂时缺席的 ID 仍写入 envelope/hash 并保留在 snapshot。Mapper 在原 ID 再出现时继续排除。Owner 必须持久继承 Next exclusions、Latest.Machine.IneligibleItemIDs、Latest.ReviewItems 的并集，不能与 next.MachineItems 取交集；确定性排序由 owner 完成。

持久回归 `TestMapperTombstoneSurvivesRemovalAndReintroduction` 验证三代 envelope（排除→移除→原 ID/字节重现）、JSON 往返、完整性校验、恢复后仍排除，以及非法/重复 ID 拒绝，限定两包测试与 vet 通过。该测试显式由调用方携带 tombstone，不能替代 application 三次 Preview 的继承回归；已通知 root 修复所持有的 helper 并补该序列。本切片未修改 application。

最新接线状态：application preview 已在本批修改为完整历史并集、确定性排序；历史集上限 128，超限以 SYNTHESIS_MANUSCRIPT_HISTORY_REVIEW_REQUIRED 拒绝，不截断。真实 Git 三次 Preview 移除/重现回归、两阶段 Resolve 冲突裁决回归及三包限定测试/vet 均通过，详情见 manual-manuscript-contract.md 最后一节；先前“应用继承待 root 修复”的状态已过期。仍未实施存储/授权/发布。
