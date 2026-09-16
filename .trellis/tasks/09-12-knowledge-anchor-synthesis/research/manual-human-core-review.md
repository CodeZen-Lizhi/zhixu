# HumanWait/core 独立审查

## P2：通用提交未执行 task schema 的 enum/pattern 绑定

[runtime_validation.go](../../../../../internal/workflow/adapter/postgres/runtime_validation.go:81) 的 `validateDecisionSchema` 只检查 `required`、字段 `type` 和 `additionalProperties`，未检查 schema 中的 `enum` 或 `pattern`。但 [synthesis_manuscript_review_service.go](../../../../../internal/organizing/application/synthesis_manuscript_review_service.go:39) 为人工裁决 task 写入了 `processing_id`、`workflow_run_id`、完整 `note_ids` 的 `enum`，以及 `result_hash` 的格式约束。

触发条件：具有该 workflow 的 `READ_LOCAL` 与 `WRITE_PROPOSAL` 权限的调用方绕过 owner service，直接调用通用 `SubmitHuman`，提交仅满足字段名和 JSON 类型、但使用伪造 processing/run/note 集或 result hash 的对象。任务会在 [gorm_runtime_human.go](../../../../../internal/workflow/adapter/postgres/gorm_runtime_human.go:218) 被标记为 submitted 并激活 apply。随后 [synthesis_manuscript_runtime.go](../../../../../internal/organizing/adapter/postgres/synthesis_manuscript_runtime.go:169) 会因 submitted decision 与真实 owner receipts 不同（或 receipt 缺失）拒绝 apply；不会产生候选，但 HumanTask 已不可恢复为 pending，处理只能进入失败/显式重试路径。

建议：让通用 schema validator 实施当前 schema 已声明的 `enum` 和 `pattern`（数组 enum 必须做精确结构比较），或在 task transition 前增加 owner 结果的专用校验。这样伪造的通用提交不会终结等待节点。

其余审查结论：未发现其他有代码证据支撑的 P1/P2。生产 `HumanAuthority` 先验证 caller capabilities，再按 run→node→task 锁顺序核对真实 task、注册图和冻结 schema；模型历史证明与当前 root/owner 漂移复核分开执行。apply 会重建所有 receipt 的 owner identity，并与 submitted task decision 精确比较，因此通用决定本身不能代替 receipt。

验证与限制：复用 `/tmp/manuscript-human-unanchored.log` 的 33.247 秒真实 PostgreSQL 路径 PASS；本轮 `go test -mod=vendor ./internal/workflow/application ./internal/organizing/application -run 'Test.*(Human|SynthesisManuscript)' -count=1` 通过，且定向 `git diff --check` 通过。未重跑实库矩阵；HTTP、cmd 与 Web 接线及完整产品验收不在本次范围内。

## 2026-09-15 root复核与关闭

原P2 enum/pattern未执行已修复。root读了最终有限验证器、实际Submit约束和Resume复用路径：字符串/有序note数组enum与锚定hash pattern执行；非法约束返回unsupported；数值enum避免float64误合并，但原通用数字type/replay行为不在本次迁移范围。定向单元与真实PG错误决定不转移、合法决定及精确重放证据见manual-human-schema-validation.md。符合schema不等于拥有receipt，真实owner apply仍逐receipt和完整hash复核，原无receipt伪提交防御保留。未发现本次Resume/validator增量的其他P1/P2。

验证环境新增生产依赖导致旧同包跨域integration测试的import cycle，main已派独立修复；不能将显式源文件排除执行当作工作流包最终验证。浏览器/最终Git另验，不在此审查完成声明内。
