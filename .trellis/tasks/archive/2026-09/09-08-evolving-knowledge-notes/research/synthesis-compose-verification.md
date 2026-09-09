# TODO4：真实 Compose 与浏览器验证

入口为 `make compose-synthesis-smoke`。脚本复用既有隔离 Compose、Auth、Workspace grant 与清理生命周期；业务写入使用真实 HTTP，SQL 只读取计数。模型 Provider 是确定性测试夹具，不代表真实模型质量评测。

## 首轮：Capture 聚合状态误判

项目 `zhixu-rag-smoke-14f6b88f871d` 在首篇 Capture 状态检查退出，尚未进入浏览器。原 helper 把所有 `READY_DEGRADED` 当作失败，忽略关闭向量能力时的受支持状态。旧日志没有 Capture 阶段/错误码，不能仅凭一次 Profile ModelCall 成功证明当次完整 Profile 终态。

修复后的检查要求 Capture/Workspace/Source Version 精确一致，Ingestion、Keyword Index 和 Profile 三个阶段均为 `READY`，且仅接纳：

- 无错误的 `READY`；或
- `READY_DEGRADED + INDEX + CAPTURE_VECTOR_CAPABILITY_UNAVAILABLE + retryable=false`。

随后读取真实 knowledge-profile，核对 Profile/Revision/Source/Workspace/Capture 绑定。其他降级、解析/画像失败、STALE 和身份漂移均拒绝；失败诊断仅打印受限状态/错误码。

2 个成功输入与 13 个拒绝输入的定向断言、`bash -n`、空白检查通过；W6 owner 对实际 Capture/Profile wire 与判断逻辑的独立只读复核通过。未修改业务状态或模型夹具来绕过失败。

## 第二轮：业务闭环通过，浏览器定位失败

项目 `zhixu-rag-smoke-e04106d409a8` 已通过：

- 三篇真实 Capture，Profile 与自动合成分别完成；Provider 实际接收 3 次 Profile、3 次增量生成和 3 次独立语义验证。
- 首篇产生未发布 v1 与待审 Proposal；批准前没有正式文件/Commit，真实 HTTP 批准后完成文件/Git/Reindex 与发布指针。
- 第二篇形成包含 FACT、CONFLICT、GAP 的 v2，v1 保持已发布，v2 待审。
- 第三篇重复资料为 NO_CHANGE；数据库只有 1 篇笔记、2 个 Revision、2 个 Proposal、1 个 Proposal Commit。
- 原 Capture 幂等键重放返回同一 Capture，模型、版本、提案、提交计数不增长。

Playwright 在首个版本内容断言失败：同一句 FACT 正文也出现在 CONFLICT 的一方说明中，全区域 `getByText` 命中两个元素。API/UI owner 已按权威条目与分组修正定位；来源按钮也按完整来源 tuple 对应的卡片/顺序定位，不用全局 `first()`。类型检查、定向 lint 与加载检查通过。本轮本身不记为浏览器通过。

两轮项目的容器与卷均已精确检查为空。后续浏览器产物使用单独的私有目录，保留失败日志/trace，避免清理隔离实例后丢失诊断。

## 第三轮：最终通过

`ZHIXU_SYNTHESIS_SMOKE_ARTIFACT_DIR=/tmp/zhixu-synthesis-smoke-20260909-artifacts-r3 bash deploy/compose-synthesis-smoke.sh`：PASS，进程退出 0。项目 `zhixu-rag-smoke-3255dbb8b703` 的容器和卷均已精确检查为空。

第三轮重走全部后端写入/批准/重放断言，并通过真实 Chromium 页面：

- 桌面 1440 px 与窄屏 390 px 均能读取当前候选 v2、切换已发布 v1、打开该历史版本的原始来源；没有水平溢出。
- 从已发布 v1 创建后台 NOTE preparation，实际调用模型生成问题/追问；题目继续冻结 FACT/GAP 的 v1，不假造 pending v2 的冲突题。
- 真实提交一次回答并读取确定性评分；答后回看精确原始来源，刷新后保持该 Turn 和下一道追问；窄屏可恢复同一面试。
- 只发生一次 preparation POST 和一次答题 POST，没有浏览/刷新造成的重复命令或 Proposal 写入。
- `synthesis-browser-summary.json` 记录 5 次来源读取、1 个已答 Turn、两个视口，`runtime_issues=[]`；7 张截图已保存。主会话目检桌面笔记与窄屏面试，无遮挡、裁切或水平溢出。

完整 AC 证据同时包含 [W2 数据/发布](synthesis-core-verification.md)、[W3 恢复](synthesis-runtime-verification.md)、[W4 面试](synthesis-interview-verification.md)、[W6 生产组合](synthesis-worker-composition.md) 与 [公共工作台](synthesis-public-interface.md)。真实模型质量、全产品 M11 与目标环境发布不由此测试替代。
