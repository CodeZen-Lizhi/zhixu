# Live Provider Smoke Bug Analysis

## Bug Analysis: reasoning 模型被严格响应合同拒绝

### 1. Root Cause Category

- **Category**: B - Cross-Layer Contract，同时包含 D - Test Coverage Gap 与 E - Implicit Assumption。
- **Specific Cause**: Ollama 的 Qwen3 在 OpenAI-Compatible `message` 中返回已知 `reasoning` 扩展。Eino SDK 能映射该字段，但项目 transport 在 SDK 映射前执行 `DisallowUnknownFields`，因此 direct/Eino 都拒绝；同时 live fixture 沿用了普通模型的 256 token 上限，thinking 先耗尽预算时只返回空 content 与 `finish_reason=length`。离线 fixture 没有 reasoning 响应，未覆盖该跨层差异。

### 2. Why Fixes Failed

1. **只把输出上限提高到 512**：解决了 thinking 抢占预算导致的截断，但未解决严格 decoder 对 `message.reasoning` 的拒绝，属于只修到第一层症状。
2. **改用 qwen2.5:0.5b**：真实 Adapter 连续通过，证明基础 Provider/Eino 链路正常，但会绕过 reasoning 兼容缺口，不能作为最终根因修复。

### 3. Prevention Mechanisms

| Priority | Mechanism | Specific Action | Status |
|---|---|---|---|
| P0 | Architecture | 只把 `reasoning`/`reasoning_content` 定义为 string/null typed allowlist，严格解码后丢弃；其他未知字段继续拒绝 | DONE |
| P0 | Test Coverage | direct/Eino 等价测试覆盖两种 reasoning 别名的 string/null 且断言不泄漏；拒绝未知 message 字段和两种别名的 object/array/number/bool | DONE |
| P0 | Integration | 生产 Eino Adapter 对 Ollama `0.32.6` + `qwen3:0.6b` 连续 live smoke 两次 | DONE |
| P1 | Documentation | 更新 Eino Chat code-spec、ADR、部署/测试文档、任务门禁和面试口径 | DONE |
| P1 | Release | 真实 smoke 通过后仍保留 direct 默认与五个独立回滚 selector，先灰度再决定删除 | DONE |

### 4. Systematic Expansion

- **Similar Issues**: Provider 可能使用 `reasoning_content` 别名；两种字段都应按相同类型和丢弃规则处理。Streaming/Tool/Embedding 未来若引入新的 Provider 扩展，也必须逐字段 allowlist，不能整体关闭 unknown-field 校验。
- **Design Improvement**: 项目 wire decoder 继续是最终协议 owner；SDK 支持某字段不等于项目自动接受。只有不改变业务结果、审计和安全边界的元数据才能显式加入白名单。
- **Process Improvement**: 框架或 Provider/模型版本升级时，除离线 fixture 外必须执行生产 Adapter live smoke；至少区分普通模型路径和会产生 Provider 扩展的模型路径。

### 5. Knowledge Capture

- [x] 更新 `.trellis/spec/backend/eino-chat-adapter.md` 与 backend index。
- [x] 更新 ADR-0019、部署/测试/技术栈文档和任务收口证据。
- [x] 增加 direct/Eino 回归与真实 qwen3 live smoke。
- [x] 当前仓库不存在 `src/templates/markdown/spec/` 模板树，无可同步目标。
- [ ] 获得用户提交授权后提交本次代码、测试、规范与任务证据。
