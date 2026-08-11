# Research: Chi -> Gin 严格输入、流式与文件边界兼容性

- Query: 核对严格 JSON/validator、SSE、multipart upload、download、panic-after-write 在 Chi -> Gin v1.12.0 迁移中的兼容风险；划分项目必须保留与 Gin/validator 可接管的能力，并给出测试矩阵和回滚建议。
- Scope: mixed（仓库代码/测试/规格 + Gin v1.12.0 标签源码与官方文档）
- Date: 2026-08-11

## Findings

### 1. 结论摘要

这五类边界都不能通过把 Chi API 机械替换为 Gin 便捷 API完成：

1. **Gin 可以接管** `Engine`/route group、middleware chain、`c.Param`、`c.Request.Context()`、`c.FullPath()` 和响应 writer 的承载。
2. **项目必须继续拥有** 严格 JSON 文档检查、每个 endpoint 的资源上限和 Content-Type、Problem 映射、严格 query/header 多值语义、SSE wire/replay/heartbeat、multipart shape/MIME/总大小、经审计的下载流、以及脱敏 panic recovery。
3. validator 适合在项目严格解码完成后接管 `required/min/max/oneof` 等局部字段约束；它不能代替重复键、非法 Unicode、文档数量、字节/深度限制、跨字段安全规则、领域不变量或稳定错误分类。
4. 禁止使用 `gin.Default()`；也不应直接使用 `gin.Recovery()`/`gin.CustomRecovery()`、`Bind*`/`ShouldBindJSON`、multipart `ShouldBind`、`c.SSEvent`/`c.Stream`、`c.FileAttachment` 或裸 `c.DataFromReader` 来替代现有边界。
5. 最稳妥的迁移形态是：Gin 只负责调度，现有 project-owned codec/stream/file/recovery 逻辑改为接收 `*gin.Context` 或 `c.Writer/c.Request`，先通过生产 Gin Engine 的行为对等测试，再删除 Chi 入口。

### 2. Files Found

| File | Description |
| --- | --- |
| `docs/roadmap.md:63` | TODO 5 的目标、不可破坏边界和分阶段迁移要求。 |
| `.trellis/tasks/08-11-gin-http-migration/prd.md` | 本任务 R3/R4/R5 与 AC-04/AC-05 的直接验收来源。 |
| `docs/architecture/application-contracts.md:12` | 严格 JSON 必须拒绝未知字段、类型漂移、重复键和弱转换。 |
| `docs/architecture/application-contracts.md:61` | SSE 是失效通知而非事实源；定义 reconnect、Last-Event-ID 和 message/legacy wire。 |
| `.trellis/spec/backend/error-handling.md:13` | 稳定 Problem 字段、状态映射和安全错误暴露边界。 |
| `.trellis/spec/backend/capture-profile-contract.md:80` | Capture multipart 的 shape/MIME/大小/无副作用失败矩阵。 |
| `.trellis/spec/backend/export-contract.md:75` | 下载前同一 FD 校验、Audit、durable length、`io.CopyN` 与 close 契约。 |
| `internal/foundation/strictjson/strictjson.go:40` | 项目完整的有界严格 JSON 对象解码器。 |
| `internal/foundation/strictjson/strictjson_test.go:35` | duplicate/unknown/trailing/Unicode/类型/资源上限基线。 |
| `internal/httpapi/response.go:44` | 公共 JSON/Problem writer 与较弱的 1 MiB request decoder。 |
| `internal/events/http/handler.go:75` | SSE prepare-before-write、headers、manual frame、poll/heartbeat/cancel 实现。 |
| `internal/events/http/handler_test.go:25` | cursor、initial error、wire、heartbeat、cancel 的当前基线。 |
| `internal/capture/http/handler.go:235` | multipart 总大小、temp cleanup、shape、file size 与 server-side MIME 边界。 |
| `internal/capture/http/handler_test.go:56` | 当前 upload happy path、server sniff 和 unknown field 测试。 |
| `internal/export/http/handler.go:281` | Collection export 的经审计 reader、固定 headers 和 `CopyN`。 |
| `internal/export/http/attachment.go:229` | Attachment ZIP 下载的同类严格边界。 |
| `internal/export/http/handler_test.go:125` | Collection/Attachment 下载 actor、headers 与 bytes 基线。 |
| `internal/app/recovery.go:17` | 当前脱敏、bounded stack、started response 与 ErrAbortHandler 语义。 |
| `internal/app/recovery_test.go:18` | panic 前写/后写、日志泄漏、exact/wrapped abort 基线。 |
| `internal/app/router.go:497` | 当前 `statusWriter` 的 header/status/flush tracking 与 route-template logging。 |
| Gin `binding/json.go:16` | v1.12.0 JSON binder 的单次 Decode、可选 unknown-field flag 和 validator hook。 |
| Gin `binding/default_validator.go:43` | `binding` tag 的默认 validator 实现。 |
| Gin `context.go:748` | MustBind/ShouldBind 的响应与 binder 选择语义。 |
| Gin `context.go:1269` | DataFromReader/FileAttachment/SSEvent/Stream 的实现入口。 |
| Gin `response_writer.go:61` | pending status、Written/Flush 和 writer unwrap 语义。 |
| Gin `recovery.go:52` | 内置 recovery 的日志、broken-pipe/ErrAbortHandler 和默认 500 行为。 |

### 3. 严格 JSON 与 Validator

#### 3.1 当前必须保持的行为

完整严格边界由 `strictjson.DecodeObject` 两阶段完成：先检查单一顶层对象、重复键、UTF-8/surrogate、深度、字符串、数组和对象字段数，再使用 `DisallowUnknownFields` 解码具体 DTO，并再次确认 EOF（`internal/foundation/strictjson/strictjson.go:51`, `:63`, `:72`, `:173`, `:185`）。这些条件已有直接测试（`internal/foundation/strictjson/strictjson_test.go:35`, `:59`, `:76`）。

现代 HTTP handler 还在进入 codec 前按 endpoint 设置不同字节上限。例如：

- Artifact 128 KiB（`internal/artifact/http/handler.go:31`, `:843`）。
- Collection 64 KiB（`internal/collection/http/handler.go:32`, `:591`）。
- Authoring 根据命令分别设置 small/update body 和 string limit（`internal/authoring/http/handler.go:600`）。
- Capture create 2 MiB、retry 1 KiB（`internal/capture/http/handler.go:33`, `:536`, `:555`）。

因此不能用一个 Gin 全局 body limit 或全局 binder 设置代替这些 endpoint-specific 约束。Content-Type 也由项目使用 `mime.ParseMediaType` 精确检查；显式 `ShouldBindJSON` 本身不会验证请求 Content-Type。

仓库还有 9 个 `httpapi.DecodeJSON` 调用点。该 helper 保持 1 MiB、invalid Unicode、unknown field 和 trailing document 拒绝（`internal/httpapi/response.go:15`, `:56`），但**没有 duplicate-key/depth/array/object-field 检查**。这是迁移前已存在的基线差异：不得把完整 strictjson endpoint 降级到此 helper；若本任务按架构契约统一补齐重复键，必须先为这 9 个调用点添加测试并作为显式行为收紧记录，不能把它误记为 Gin 自动提供的能力。

#### 3.2 Gin binder 为什么不足

Gin v1.12.0 的 JSON binder：

- 默认 `EnableDecoderDisallowUnknownFields=false`（Gin `binding/json.go:21`）。
- 只调用一次 `Decode`，随后立即校验，不检查第二个 JSON document/EOF（Gin `binding/json.go:44`）。
- 没有 duplicate-key、非法 surrogate、document/depth/string/array/object-field limit（同文件 `:44-55`）。
- `ShouldBindBodyWithJSON` 使用无上限 `io.ReadAll` 并缓存完整 body（Gin `context.go:923`），不适合任何受控 request boundary。
- `Bind*`/`MustBindWith` 会先提交 400、abort，并产生 Gin 自有 bind error 响应语义（Gin `context.go:748`, `:807`）；这会覆盖项目 Problem。
- `ShouldBind` 根据 method/Content-Type 自动选择 form/XML/YAML 等 binder（Gin `binding/binding.go:93`），错误媒体类型可能落入 form binder，而不是项目 `415 Problem`。
- `gin.EnableJsonDecoderDisallowUnknownFields()` 和 `EnableJsonDecoderUseNumber()` 修改 package global（Gin `mode.go:85`）；即使开启也只覆盖其中两项，不能表达项目的 per-route limits。

因此迁移实现应保持“读取有限 bytes -> project strict decoder -> validator -> application”顺序；不得调用 Gin bind family 作为公共 JSON 入口。

#### 3.3 Validator 可以接管的范围

Gin 默认 validator 使用 `go-playground/validator` 且 tag 名为 `binding`（Gin `binding/default_validator.go:75`, `:90`）。Gin v1.12.0 声明 validator `v10.30.1`，项目直接依赖 `v10.30.3`，Go MVS 会保留较高版本；项目 direct dependency 仍需保留，因为 config 代码直接 import 它。

截至研究时，`internal/` 有 91 个 HTTP request struct，`binding:"..."` 命中为 0。当前唯一直接 validator 实例用于配置，并启用 `WithRequiredStructEnabled` 与自定义 `notblank`（`internal/platform/config/validation.go:14`）；它使用 `validate` tag，而非 Gin 的 `binding` tag。

建议边界：

| 能力 | Owner | 说明 |
| --- | --- | --- |
| `required/min/max/len/oneof` 等 DTO 局部字段规则 | validator，可逐 DTO 接管 | 必须在 project strict decode 后调用，并映射回现有稳定 Problem；先有等价测试再删手写判断。 |
| JSON document/UTF-8/duplicate/unknown/trailing/资源上限/Content-Type | project codec | validator 看不到原始文档形状，不能接管。 |
| optional/null/zero-value 区分 | project wire type + tests | 不应为使用 `required` 而改变 pointer/value 与 explicit null 语义。 |
| 跨字段互斥、Workspace/Capability、安全策略、idempotency binding | project HTTP/Application/Domain | 不放入 Gin 全局 validator。 |
| 错误码、状态、中文 message、retryable/details | project Problem mapper | 不返回 raw `validator.ValidationErrors` 或字段内部名。 |
| config validator | existing project instance | 与 HTTP `binding` tag namespace、required semantics 分开。 |

推荐显式创建/封装 HTTP validator，或在严格 decode 后显式调用受控的 validator；不建议把 config validator 塞进 `gin/binding.Validator` 全局变量。

### 4. SSE

#### 4.1 当前项目 wire 与生命周期

当前 handler 在写任何 SSE header 前完成 request/cursor 校验、watermark/retention 检查、首批查询和整页编码；初始依赖失败仍返回 JSON Problem（`internal/events/http/handler.go:75-107`, `:131-162`; `internal/events/http/handler_test.go:83`）。连接开始后固定：

- `Content-Type: text/event-stream`
- `Cache-Control: no-store`
- `X-Content-Type-Options: nosniff`
- `X-Accel-Buffering: no`
- 先写/flush initial page，再按 ticker poll，空闲时写 `: heartbeat\n\n`（`internal/events/http/handler.go:103`, `:191`）。

wire 默认是带空格的 `id: <seq>\nevent: <type>\ndata: <one-line-json>\n\n`；`event_format=message` 去掉 event 行（`internal/events/http/handler.go:266`）。Last-Event-ID header 优先于 query seed，且 raw header/query 多值、unknown query、canonical cursor 均严格拒绝（`internal/events/http/handler.go:303`）。断连通过 `request.Context().Done()` 释放正在阻塞的 store 调用（`internal/events/http/handler.go:211`; `internal/events/http/handler_test.go:271`）。

#### 4.2 不可直接使用 Gin SSE helpers

`c.SSEvent` 使用 `gin-contrib/sse`：其默认 Content-Type 是 `text/event-stream;charset=utf-8`、默认 Cache-Control 是 `no-cache`，字段编码为 `id:`/`event:`/`data:`（无项目当前空格），且不包含项目 envelope/replay/page invariant（`gin-contrib/sse@v1.1.0/sse-encoder.go:21`, `:43`, `:99`）。这会使 byte-level wire 和 headers 漂移。

`c.Stream` 依赖 deprecated `CloseNotify`，每轮无条件 Flush，并把阻塞/节拍责任交给 callback（Gin `context.go:1320`）；项目应继续用 request context、store-aware poll/drain 和独立 heartbeat，避免 busy loop、测试 writer panic或取消传播漂移。

Gin `ResponseWriter` 自身总是实现 `http.Flusher`，但其 `Flush` 在 underlying writer 不支持时只是跳过（Gin `response_writer.go:128`）。所以在 Gin handler 内对 `c.Writer.(http.Flusher)` 的断言永远成功，无法维持 `SSE_STREAMING_UNSUPPORTED` 检查。实现时应在替换/包装 `c.Writer` 前，通过其 `Unwrap()` 检查最底层 writer 是否真的实现 Flusher，将 capability 存入 request-local 状态；实际 flush 仍调用 `c.Writer.Flush()`，以便 Gin 和项目 writer 都记录 response started。

Gin 可接管的只有路由、`c.Request` 和 writer 承载。帧编码、prepare-before-write、headers、cursor raw multi-value 检查、replay/retention、heartbeat 与 cancellation 均保留 project owner。

### 5. Multipart Upload

当前 Capture upload 的边界顺序是：

1. canonical path/idempotency 和精确 `multipart/form-data` 检查（`internal/capture/http/handler.go:235-249`）。
2. 先用 `http.MaxBytesReader` 限制整个 multipart 为 `10 MiB source + 1 MiB overhead`，再用 1 MiB memory threshold 解析（`:251-253`; source 上限来自 `internal/workspace/domain/files.go:9`）。
3. 始终 `MultipartForm.RemoveAll()` 清理 spill temp file（`:256-258`）。
4. 只允许唯一 `kind`、可选唯一 `display_name`、唯一 `file`；拒绝 unknown/duplicate/missing field/file（`:574-591`）。
5. 从 file stream 最多读取 `10 MiB + 1`，拒绝 empty/oversize（`:268-282`）。
6. MIME 以服务器 sniff 的 bytes 为准；`.md` 只是把 sniffed `text/plain` 收敛为 `text/markdown`，FILE/IMAGE 各有 allowlist（`:602-616`）。filename 只作为 display metadata 进入 command，不决定持久路径（`:294-297`; capture spec `:60`）。

Gin `c.FormFile`/`c.MultipartForm` 使用 `engine.MaxMultipartMemory`（Gin `context.go:697`），但这个参数只是 `ParseMultipartForm` 的 memory threshold，不是总 request/file size；默认 32 MiB（Gin `gin.go:25`, `:165`, `:221`）。更危险的是 multipart `ShouldBind` 走 `binding.FormMultipart`，内部固定使用另一个 32 MiB `defaultMemory`，不读取 engine 配置（Gin `binding/form.go:12`, `:51`）。两条路径都会忽略 unknown form field，且不能表达项目的 exact shape、server MIME、总 body 和 per-file limit。

迁移应继续直接操作 `c.Request`，并在任何 `PostForm`/`FormFile`/`ShouldBind` 调用前安装 `MaxBytesReader`。不得使用 `c.SaveUploadedFile`：原字节应进入现有 Application 的受控 stage/transaction/publish 协议，而不是由 HTTP framework 直接写目标路径。

### 6. Export / Attachment Download

下载不是普通静态文件服务。Application 在返回 reader 前先按 Workspace/scope 查询 Job、在同一 FD 验证 path/hash/size、写下载统计和 `export.download` Audit；任何 Audit/投影失败都关闭 stream（`internal/export/application/service.go:303-347`; export spec `:75-80`）。HTTP 再校验 succeeded projection，设置受控 ASCII filename、durable Content-Length、no-store/nosniff，并 `io.CopyN`（`internal/export/http/handler.go:281-324`; `internal/export/http/attachment.go:229-268`）。

不能使用：

- `c.FileAttachment`：它从 path 调 `http.ServeFile`（Gin `context.go:1301`），会绕过 verified reader/Audit owner，并引入 range/conditional/MIME/path 等静态文件语义。
- 裸 `c.DataFromReader`：Gin `render.Reader` 设置 Content-Length 后使用 `io.Copy` 到 EOF（Gin `render/reader.go:21`），不是 durable size 的 `CopyN`；render error 只进入 `c.Errors` 并 abort（Gin `context.go:1151`），response started 后不能再生成项目 Problem。

迁移应保留 `defer content.Close()`、固定 headers、显式 status 和 `io.CopyN(c.Writer, content, job.FileSize)`。写入开始后的 short read/client disconnect 只能结束响应并记录安全诊断，不能追加 JSON Problem 或回滚已经持久化的 `prepared_for_return` Audit。

当前 HTTP happy-path 测试覆盖两类 headers/body 和 actor mapping（`internal/export/http/handler_test.go:125`, `:217`），但未覆盖 short stream、handler validation failure 时 close、成功 close、copy error 后无 Problem append；这些应在迁移中补齐。

### 7. Panic After Write / Recovery

当前 recovery 契约比 Gin 内置 recovery 更严格：

- 不记录 panic value、真实 URL path 或完整本机路径；只记 `HTTP_PANIC_RECOVERED`、request ID、matched route template 和 basename/bounded stack（`internal/app/recovery.go:12-40`）。
- response 已开始时不追加/替换 Problem（`:39`）。
- exact `http.ErrAbortHandler` 重新 panic 给 `net/http`，wrapped abort 当普通 panic（`:27`; `internal/app/recovery_test.go:107`, `:122`）。

Gin 内置 recovery 会记录 panic value和含完整文件路径的 stack；debug 模式还 dump request，且只遮盖 Authorization，其他 headers 仍会进入 dump（Gin `recovery.go:52-106`, `:113`）。它把 `errors.Is(..., http.ErrAbortHandler)` 与 EPIPE/ECONNRESET 都视为 broken pipe并直接 Abort，不调用 custom recovery（`:63-87`），因此 `gin.CustomRecovery` 也无法恢复项目 exact-vs-wrapped 语义。

实现必须基于 `gin.New()` 自写 `defer/recover` middleware，不安装任何 Gin Recovery。Middleware 顺序继续让 request log 在 recovery 外层，确保 recovery 收口后 completion log 仍产生。

Gin writer 还有一个容易遗漏的差异：`WriteHeader` 只设置 pending status，`Written()` 仍为 false；只有 `WriteHeaderNow`、Write/WriteString/Flush 才真正开始响应（Gin `response_writer.go:67-107`; Gin 自测 `response_writer_test.go:56`）。当前 net/http `WriteHeader(204)` 立即开始响应，而生产 Auth 确有两个 204 endpoint（`internal/auth/http/handler.go:450`, `:590`）。如果 recovery 只看 `c.Writer.Written()`，`WriteHeader(204)` 后 panic 会错误改写成 500。

建议让 request-log middleware 把 `c.Writer` 替换为嵌入 `gin.ResponseWriter` 的 project tracking writer：覆写 `WriteHeader` 记录 `headerRequested/status`，保留 Gin writer 的 Flusher/Hijacker/Pusher/Size/Written；recovery 的 started 判定为 `headerRequested || c.Writer.Written()`。这样既保留 Gin pending header 机制，又保持项目“调用 WriteHeader 即承诺响应”的旧语义。处理 panic 值时先做 `error` 类型断言，再与 `http.ErrAbortHandler` 做精确比较，避免对不可比较的 panic 值直接接口比较触发二次 panic。`c.FullPath()` 用于低基数 route template；不要从 `Request.URL.Path` 回退记录动态值。

### 8. Capability Ownership Matrix

| Area | Gin/validator may own | Project must own |
| --- | --- | --- |
| Routing | Engine, groups, method/path match, `c.Param`, `c.FullPath` | 404/405/trailing-slash config、外部 route inventory。 |
| JSON | writer transport only | bounded read、media type、strictjson、per-route limits、Problem。 |
| Validation | selected local field tags after decode | null/optional semantics、cross-field/security/domain rules、error mapping。 |
| Query/Header | `c.Request` access | unknown/duplicate/empty/canonical rules；不能用单值 getter丢信息。 |
| SSE | response writer + request context | prepare、replay、wire、headers、heartbeat、flush capability、cancel。 |
| Multipart | underlying `net/http.Request` | total/file limit、shape、cleanup、MIME、managed persistence。 |
| Download | response writer | verified/Audited reader、headers、CopyN、close、post-start failure。 |
| Recovery | middleware chain and route template | recover、redacted log、bounded stack、started tracking、ErrAbortHandler。 |

### 9. Required Test Matrix

所有专项测试至少要有一组通过最终 `internal/app.NewRouter` 的 Gin production engine，而不只测试裸 handler；否则无法发现 writer wrapping、middleware order、404/405 和 route-template 差异。

| Area | Required cases | Expected evidence |
| --- | --- | --- |
| Strict JSON document | valid exact limit；+1 byte；empty；top-level non-object；unknown root/nested；duplicate root/nested；trailing/multiple document；wrong type；invalid UTF-8；unpaired/paired surrogate；depth/string/array/object limit | Application call count remains 0 on invalid input；exact existing status/code/Content-Type/Problem body。 |
| Content-Type / bind guard | missing/wrong/duplicate Content-Type；JSON with charset；ensure no `Bind*` auto response | Existing `415/400` distinction and project Problem；no `text/plain` Gin bind response。 |
| Validator | one simple required/range/oneof success/failure per migrated DTO；optional zero/null cases；cross-field remains project-owned | Existing error code/message/retryable retained；no raw validator namespace leaked。 |
| SSE pre-start | missing store、initial DB failure、retention race、future/expired cursor、unknown/duplicate query/header、underlying writer without Flusher | JSON Problem before stream starts；`SSE_STREAMING_UNSUPPORTED` remains reachable。 |
| SSE active | exact four headers；legacy and message frame bytes；header cursor overrides query；multi-page monotonic drain；heartbeat exact bytes；client cancel releases blocked store；write error exits | No persistence fields；no extra charset/no-cache；no goroutine/ticker leak；production Gin writer flushes first page. |
| Multipart shape | happy FILE/IMAGE；unknown/duplicate/missing value/file；two files；empty file；wrong kind；unsupported MIME；malformed boundary | Stable 400/415；service call 0；spill temp files removed. |
| Multipart limits | total body exact/+1 around 11 MiB；file exact/+1 around 10 MiB；large overhead with small file | No unbounded read；correct stable code；`MaxMultipartMemory` is not mistaken for total limit. |
| Download | collection MD/JSON and attachment ZIP headers/body；actor/scope；service error before write；nil/inconsistent reader；reader close on every branch；short read/cancel | Exact Content-Disposition/Length/Type/no-store/nosniff；no body buffering；no Problem appended after start. |
| Recovery before write | arbitrary string/error/uncomparable panic where supported；stable 500 Problem；route template/request ID | Two structured log entries in expected order；no panic value, actual path, absolute path, Cookie/Auth/Idempotency/body leakage. |
| Recovery after write | header-only 204 then panic；JSON body then panic；SSE flush then panic；download copy panic | Original status/body prefix retained；zero appended Problem；completion status stays original. |
| Abort semantics | exact `http.ErrAbortHandler` and wrapped error | Exact value re-panics; wrapped value follows ordinary safe recovery, matching current tests. |
| Static guard | search for Chi and forbidden Gin helpers | Final production/tests have no Chi；HTTP product code has no `gin.Default`, Gin Recovery, JSON/multipart bind, SSEvent/Stream, FileAttachment shortcut unless an explicit reviewed exception exists. |

### 10. Rollback Strategy

该迁移没有数据库/schema/data migration，回滚应保持为代码/二进制级操作，不引入 runtime 双 Router 或 feature flag：

1. **Checkpoint A - shared boundaries**：先落 Gin tracking writer、request log/recovery、strict decoder + validator adapter 的专项测试；尚未切生产 router。失败时只回滚该独立 commit。
2. **Checkpoint B - stream/file handlers**：分别迁移 Events、Capture、Export/Attachment 的 handler tests 到 Gin，并保持生产仍在唯一旧入口，直到各自 production-engine contract test 通过。每个目录独立 commit，任何失败回滚该目录 commit。
3. **Checkpoint C - single engine switch**：一次性把 production Composition Root 切到唯一 Gin Engine，同时删除 production Chi mount；运行 route inventory、Auth、安全、SSE/upload/download/recovery 门禁。失败时部署/恢复到上一 Chi binary/commit，因为 wire、DB、files 和 Audit schema 均未变化。
4. **Checkpoint D - dependency cleanup**：只有全量行为对等后才删 Chi imports/module/vendor。若 cleanup gate 失败，恢复 cleanup commit，不恢复双运行时。

发布回滚注意：下载 Audit 只表示 `prepared_for_return`，SSE 不是事实源，Capture/Export 持久状态都由既有 Application/DB owner 管理；因此回滚到上一 Chi binary 不需要数据补偿。不得在回滚时删除 upload stage、export result 或 Audit 事实。

### 11. External References

精确结论以本机 module cache 中的 Gin `v1.12.0` 标签源码为主，并可由以下官方 tag 链接复核：

- Gin v1.12.0 JSON binder: https://github.com/gin-gonic/gin/blob/v1.12.0/binding/json.go
- Gin v1.12.0 validator: https://github.com/gin-gonic/gin/blob/v1.12.0/binding/default_validator.go
- Gin v1.12.0 Context helpers: https://github.com/gin-gonic/gin/blob/v1.12.0/context.go
- Gin v1.12.0 ResponseWriter: https://github.com/gin-gonic/gin/blob/v1.12.0/response_writer.go
- Gin v1.12.0 Recovery: https://github.com/gin-gonic/gin/blob/v1.12.0/recovery.go
- Gin v1.12.0 reader renderer: https://github.com/gin-gonic/gin/blob/v1.12.0/render/reader.go
- gin-contrib/sse v1.1.0 encoder: https://github.com/gin-contrib/sse/blob/v1.1.0/sse-encoder.go
- Official model binding docs: https://gin-gonic.com/en/docs/examples/binding-and-validation/
- Official multipart docs: https://gin-gonic.com/en/docs/examples/upload-file/multiple-file/

Context7 `/gin-gonic/gin`（High reputation）在 2026-08-11 复核了 MustBind/ShouldBind 分工、`binding` tags、MaxMultipartMemory、DataFromReader 和 custom recovery 的公开用法；其结果链接到 `master` 文档，因此只作 API 意图佐证，不替代上述 v1.12.0 tag source。

### 12. Related Specs

- `docs/roadmap.md:63-68`：TODO 5 目标与不可破坏边界。
- `.trellis/tasks/08-11-gin-http-migration/prd.md`：R3/R4/R5、AC-04/AC-05。
- `docs/architecture/application-contracts.md:3-17`：OpenAPI wire、strict JSON、Problem。
- `docs/architecture/application-contracts.md:61-77`：SSE owner、recovery、legacy/message 和 Last-Event-ID。
- `.trellis/spec/backend/error-handling.md:27-60`：错误传播、Problem 与 SSE。
- `.trellis/spec/backend/capture-profile-contract.md:39-65`, `:80-89`, `:109-118`：Capture 持久/上传/测试边界。
- `.trellis/spec/backend/export-contract.md:52-80`, `:101-123`, `:147-148`：download validation/Audit/stream/headers/测试。
- `.trellis/spec/backend/logging-guidelines.md:63-68`：日志脱敏和 SSE 摘要。

## Caveats / Not Found

1. 本研究是只读源码/测试审计，没有执行或修改产品代码；现有定向测试“已通过”的事实来自任务 PRD 基线，本文件不重复宣称动态验证。
2. `httpapi.DecodeJSON` 当前不拒绝 duplicate keys，而架构文档要求拒绝；这是已有差异，不能用 Gin binder 掩盖。统一收紧前需评估 9 个调用点并补测试。
3. `SSE_STREAMING_UNSUPPORTED` 当前没有直接测试；现有 app `statusWriter` 自身实现 Flush，也会掩盖 underlying writer 不支持 Flusher。Gin 迁移应同时补 capability test，不能把该常量存在当成已验证行为。
4. Capture 当前 HTTP test 只覆盖 server sniff happy path和一个 unknown multipart field；duplicate/missing/two-file/size/temp-cleanup 仍缺直接覆盖。
5. Export HTTP tests 未覆盖 `io.CopyN` short read/write error 和所有 handler-level close 分支；Application 已覆盖 Audit 失败 close，但不能替代 HTTP streaming fault test。
6. 当前 recovery 的 `responseStarted` 只识别具体 `*statusWriter`；该实现不能原样套在 Gin writer 上。必须由新的 Gin-aware tracking writer/recovery test 证明 header-only 204 语义。
7. Gin `ResponseWriter.CloseNotify()` 与 `Hijack()` 对不支持相应接口的 underlying writer使用 unchecked assertion；项目 SSE 不应通过 `c.Stream` 触发它，测试 double 也要明确提供所需能力。
