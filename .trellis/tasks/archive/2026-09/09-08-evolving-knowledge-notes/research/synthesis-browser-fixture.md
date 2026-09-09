# W6：隔离 Compose / 浏览器模型夹具

## 入口与边界

`cmd/rag-model-fixture/synthesis_notes.go` 提供：

- `synthesisNotesFixtureStage(json.RawMessage) string`
- `synthesisNotesFixtureResponse(stage string, input map[string]any) (string, error)`

stage 为 `capture_profile`、`synthesis_generate`、`synthesis_validate`、
`synthesis_note_interview`。`main.go` 由 dynamic_runtime owner 在原有严格请求检查后接入，
仍复用当前 HTTP completion 编码、授权、绑定地址和显式 Compose opt-in。

Schema 从生产 Catalog / `NotePlanJSONSchema` 读取并做完整 JSON 结构匹配；响应再经生产
decoder 校验。不靠 Schema 的名称 hash、关键词或固定 UUID 识别。笔记、条目、来源和答案点
全部引用当前输入提供的短标签；不写数据库、Proposal、Git 或 HTTP 业务响应 mock。

普通 RAG / Workspace Analysis 入料如果没有 `Cache source alpha.` / `Cache source beta.`
标记，会返回 `notes=[]`，并实际走独立的 NO_CHANGE 语义请求。这样真实 Source-ready
调度仍完成，同时不向既有只读 smoke 添加无关笔记或 Proposal。

## 精确推荐三篇正文

第一篇（建议 display_name：`Cache expiration first`，独立幂等键）：

```text
Cache source alpha. Cache entries expire after five minutes in the default configuration. The material does not explain expiration during an outage.
```

第二篇（建议 display_name：`Cache expiration update`，新的幂等键）：

```text
Cache source beta. Cache entries expire after five minutes in the default configuration. In the high-traffic configuration, cache entries expire after ten minutes. Refreshing invalidates the cache key. The material does not explain expiration during an outage.
```

第三篇（建议 display_name：`Cache expiration repeated`，再次使用新的幂等键；字节与第二篇相同）：

```text
Cache source beta. Cache entries expire after five minutes in the default configuration. In the high-traffic configuration, cache entries expire after ten minutes. Refreshing invalidates the cache key. The material does not explain expiration during an outage.
```

笔记精确 `title=Cache expiration`，`topic_key=cache expiration`。

1. Alpha 自动生成 v1：FACT（默认五分钟）与 GAP（outage 未说明），不制造原资料没有的冲突。
2. 等待并批准 v1 后导入 Beta。v2 对原 FACT ADD_SUPPORT，保留原 GAP，增加 refresh FACT 和默认/高流量条件的 CONFLICT；v1 保持 Published，v2 是待审 Current。
3. 重复 Beta 返回 NO_CHANGE，独立语义校验成功，保留 v2 为当前版本，不新增第三个 Revision / Proposal。
4. 重放三次原幂等命令另用于验证已有 Capture 的 replay；它与第三次使用新幂等键的重复资料是不同场景。

## 笔记面试

对已发布 v1 请求 `question_count=3`、`max_follow_ups=1`。夹具读取真实 frozen items，
先选 FACT，再覆盖其他存在的 kind，额外题目轮换已有条目。v1 顺序为 FACT → GAP → FACT；
包含三类的冻结版本会覆盖 FACT → CONFLICT → GAP。第一题不假定 I001 就是 FACT。

所有基础题与追问题面互不重复。每题引用所属条目的全部 P-label，Conflict 不遗漏任一条件，
GAP 保留“证据不足”的答案边界。`max_follow_ups=0` 返回空数组，否则每题提供一个条件追问。
条件分别为 FACT=`LOW_COVERAGE`、CONFLICT=`LOW_BOUNDARIES`、GAP=`LOW_CORRECTNESS`。

v1 第一题为：`Explain the supported fact and its applicability for Cache expiration (question 1).`
完整答案可用 `Cache entries expire after five minutes. For the default configuration.`；
简短不完整回答可触发真实评分的 LOW_COVERAGE 追问。页面断言应读取真实 Interview API 的题面和
`next_question`，不把固定文案当作浏览器数据源。Source 回看绑定准备任务冻结的已发布 v1。

## 验证记录

- `go test -race -mod=vendor -run '^TestFixtureSynthesis' -count=1 -timeout=60s ./cmd/rag-model-fixture`：PASS，2.176s。
- 测试用生产 Eino HTTP Adapter 实际请求十次，覆盖两次增量、重复/普通资料 NO_CHANGE 及其独立语义请求、Cache/普通资料 Profile。
- NOTE 测试使用生产 `EncodeNotePlanInput` → HTTP Provider → `DecodeNotePlan`，覆盖实际 FACT 非 I001、FACT/GAP 三题、全部三类、禁用追问，以及未绑定答案点/不可能的 kind 覆盖。
- 定向 `go vet -mod=vendor ./cmd/rag-model-fixture`：PASS。
- 全包 `go test -race -mod=vendor -count=1 -timeout=60s ./cmd/rag-model-fixture`：PASS，2.284s。
- 四个新增 Go 文件 `gofmt -l`：PASS，无输出；四个 Go 文件与本文共 5 个 owned 文件 whitespace：PASS。

这是确定性模型的协议与浏览器环境准备证据；真实 Compose / REST / Git 批准 / 浏览器结果由
主会话实际执行并单独记录，不能从夹具单元测试推断通过，也不代表真实模型质量验收。
