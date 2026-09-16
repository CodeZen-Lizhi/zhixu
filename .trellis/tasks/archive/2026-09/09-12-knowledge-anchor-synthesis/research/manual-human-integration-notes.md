# 人工裁决集成中发现

2026-09-15。以下不代表已完成验收。

- 既有重试保留 processing ID 并切换 workflow run；旧 attempts 不可删除。ReviewStore manifest 和生产 HumanAuthority 查询必须同时按 workspace/processing/GenerationInput.WorkflowRunID 限定当前 run，再按该 run 的完整冻结生成重算应有目标集。原 processing-only 查询会被旧 run 阻断。已交两位实施者，channel seq26672。GenerationInput 序列化字段是 PascalCase WorkflowRunID，不能查询 workflow_run_id。
- ProcessingRecord 目前会链接普通 /workflows 页面。business API decoder 只接受 approval/target_path 的 HumanTask schema；新增主笔记 owner schema 须显式识别并呈现受控裁决链接，不能把它当普通 approval 提交，也不能让既有“查看处理过程”变成解码错误。
- 一次读取至多显示8个目标摘要；选中单个 note 后才读取 Base/Current/Proposed/Candidate 全文。禁止将整个 Prepared、模型日志、根身份或完整 manifest 暴露给 Web。
- 工作台复用既有 MonacoTextEditor/MonacoDiffViewer、逐冲突确认、未知响应同命令重放、stale 保留编辑模式。若提取公共组件，优先窄三方比较/编辑视图；不为复用伪造 Proposal ID、revision 或发布元数据。主笔记裁决成功还可能进入第二阶段或下一个 note，全部完成后才生成待审核候选。

## 最后裁决已存、工作流尚未继续的刷新恢复

root 在 service.Decide 的两次事务之间识别缺口：最后 receipt 已保存而 SubmitHuman 前进程退出，刷新后 summary 为 Ready=true/Submitted=false，detail 无未决 Review；原完整命令/幂等键不在浏览器中，现有“同命令重试”不足以继续。已要求新增仅接 Binding 的 owner Resume，从真实全 ready receipts 重新构造相同结果身份，补交同一 task；没有新正文或新模型调用。正常最后一次 Decide 继续自动提交，Resume 仅供中断恢复，不是额外正常审批。见 channel seq28157/28158。已关闭：Resume owner+HTTP已经落地，真实PG两阶段保存最终receipt后清空旧command、重建服务，仅binding恢复原task/apply；重复/未ready/伪binding/readonly/root拒绝均PASS23.487s，/tmp/manuscript-resume-integration.log。正常Decide仍自动继续，无额外审批。

## 候选生成后的文件再次变化（待定位完整恢复入口）

PRD明确“草稿生成后用户再次修改主笔记，旧草稿不得直接覆盖新内容，必须基于最新内容重新合并并审阅”。当前已证明的 cancel→retry 是待审 HumanTask 期间的 F 变化；它不能自动代替 processing 已 SUCCEEDED、候选已生成后 F 再变的验收。已有 RetryProcessing 仅处理 FAILED/retryable，ApplyRecovery 会恢复已提交结果；公开 Synthesis 路由目前未见独立重新合并命令。需先追踪普通 Proposal Revision/Authoring owner 是否已存在合法恢复路径，不能直接断言没有，也不能用绕过生成内容绑定的普通 append 冒充恢复。后续实际验收必须覆盖这一阶段，若需要新增 owner 接线，仍按原PRD范围实施并保护既有不可变证据。

## Web 实施中 root 检查（待复验）

- P2：unknown 原命令重试使用含 stage/fingerprint/ready 的 sameBinding 门禁。服务器已推进下一阶段、摘要刷新后会禁用原命令重放；应只允许同一不可变 task/attempt/capture 身份下重放完整原命令，不能要求阶段仍旧。已交UI补实际状态序列测试。
- P2：Monaco editor/diff 的 onError 仅写提示，未阻止新提交。编辑器未准备好或差异加载失败时必须禁止新裁决；已冻结的 unknown 命令恢复不依赖编辑器加载。已交UI沿原工作台ready/error规则修复。

## 浏览器与发布闭环（main，2026-09-15）

正式组件通过 loopback 仅转发真实认证 handler 接到活的 PG/River 两阶段夹具。Playwright 实际编辑完整正文、逐冲突确认两阶段，检查候选与正式版本分离，执行既有审批/Git 写回，刷新主笔记并核对当前/正式 revision 和正文。集成测试 `/tmp/zhixu-manuscript-live-verified/test.log` 以 379.53s PASS。首次浏览器尝试的末尾旧断言已按 opt-in 发布验收分支修正，不改变普通运行；临时页面已删除。测试期间 Monaco 阶段切换记录了生命周期 console 错误但未阻断流程，UI loading gate 代理已补就绪/错误门禁，仍需后续修复该 console 根因，不能把“流程通过”写成 console 零错误。
