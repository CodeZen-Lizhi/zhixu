# 人工全文 HTTP 与阅读页面接线

2026-09-15，manuscript-reading。此切片已实施并完成下述定向验证。未修改 domain、Mapper、merge、store、SQL、worker、Authoring 或冲突 HumanTask 链；没有提交、push、部署。开始前已向 main 发送公开读取契约与 OpenSource 文件协调消息（channel seq19147）。

## 公开读取契约

- v1 revision 响应原字段保持，省略新增 `display`；Items 仍至少一项。
- v2 revision 增加可选受控 `display`：`renderer_version=synthesis-markdown/v2`、`full_content`、`manual_changes`、`review_required`、`historical_sources`。HTTP 在 Validate 后调用 Content()；全文逐字使用不可变 Manuscript，不重建机器文本，ContentHash 与全文一致。
- 不输出 MachineItems、Prepared、模型日志、权限、映射内部字段。顶层 Items 仍只有可信子集。历史引用单独投影，排除已作为当前可信来源输出的精确引用，不混入 Items/catalog/include/Interview。
- 来源 HTTP 先读取并校验本 workspace/note/revision，再从保存的可信或审计引用查找 source_span_id。OpenSource 再验证 revision 完整性及请求身份、精确 SourceRef 成员关系；任意客户端 ref 不会到达来源 reader。仅审计历史成员的 HTTP 响应增加 `role=HISTORICAL_REVIEW`，原有 v1 来源响应省略此字段。
- 当前 v2 审计来源没有冻结知识目录绑定，页面直接说明未记录，不调用当前 Profile、不把审计条目交给知识目录/生成接口。

## 文件与页面

- `internal/organizing/http/synthesis_wire.go`：受控全文投影和独立历史 SourceRef 投影。
- `internal/organizing/http/synthesis_handler.go`：现有来源只读入口接历史引用及角色；保留 v1 精确原始来源。
- `internal/organizing/application/synthesis_service.go`：仅 OpenSource 的身份、完整性和精确历史成员验证。本文件其他 runtime 修改由并行代理拥有。
- `internal/organizing/http/synthesis_handler_test.go`、`internal/organizing/application/synthesis_source_reading_test.go`：实际 handler 和应用授权回归。
- `web/src/api/synthesis.ts`、对应测试：严格可选 v2 display 解码；只有明确 v2 允许空 Items；1 MiB UTF-8 字节界限、NUL、孤立 surrogate、未知字段/版本/角色、跨工作区拒绝，保留合法 BOM；人工变化不能携带可信项。历史来源独立，不与可信来源重叠。
- `web/src/features/synthesis/NoteContent.tsx`：复用原有 `authoring/MarkdownPreview`。人工正文为唯一正文渲染输入，可信项仅保留追溯入口；显示“含人工编辑，来源需复核”。历史来源用原生 details 按需展开，弹窗说明“历史参考，当前正文待复核”和知识目录未记录。
- `web/src/features/synthesis/source-link.ts`：精确历史链接可选择独立历史来源，仍验证全部来源身份；不合并到 revision.items。
- `web/src/features/synthesis/SynthesisPages.test.tsx`：人工原文、历史版本、刷新、历史来源和安全渲染。
- `api/openapi/openapi.json` 与 `web/src/api/generated`：同步可选 display 与历史角色、无 display 时 Items 非空条件；已重新生成并核验一致。

## 验证结果

最终定向命令均通过：

- `go test ./internal/organizing/http -run 'TestSynthesisHTTP' -count=1`（1.288s）。新回归使用真实 Mapper 创建完整 v2 envelope，再经过实际 HTTP handler；v1 shape、v2 未编辑/人工编辑零 Items、全文字节/hash、私有字段不泄漏、历史角色、未知来源、非法 envelope/版本/hash/NUL/UTF-8/超限及跨 workspace 拒绝。
- `go test ./internal/organizing/application -run '^TestOpenSourceAuthorizesOnlyExactManuscriptHistory$' -count=1`（0.979s）。真实应用 OpenSource，固定 Store/Source owner；伪造标题、片段、excerpt hash、workspace/note/revision、完整性损坏均拒绝，失败不会调用来源 reader。
- `go vet ./internal/organizing/http ./internal/organizing/application`。
- Web API/页面两个文件 46 项测试（2.02s）。保留已有 v1、body reference 精确定位/漂移拒绝等回归。
- `npm run typecheck --prefix web`、`npm run typecheck:generated --prefix web`。
- 相关五个 Web 文件 ESLint、定向 `git diff --check`。
- `make openapi-check`：Spectral、项目 checker、实际路由 inventory、238 operation tags；`npm run generate --prefix api/openapi` 及 `npm run generate:check --prefix api/openapi` 通过。

最初 Go 编译遇到并行 runtime 尚未定义 applyGeneration；已通知 main，待其接线完成后以上最终测试通过。最初测试代码的非空断言被 ESLint 拒绝，已替换并复验。自检发现 TextDecoder 默认去 BOM 会误拒合法全文，已设置 ignoreBOM 并覆盖原字节保持。

## 实际浏览器验收与限制

使用 playwright CLI 启动独立 Chromium 会话 `manuscript-reading`。临时 Go 测试通过实际 HTTP handler 生成 revision/source JSON；后端 Store/Source 为固定 owner，不是 PostgreSQL 或真实模型。临时页面加载严格 decoder 和正式 NoteContent/MarkdownPreview，来源请求返回同一个 HTTP handler 的测试响应。

实际观察：人工批注显示；展开历史来源并打开弹窗，原文 `immutable source excerpt`、历史待复核标签、未记录知识目录说明同时出现。整页 reload 后人工批注和 review 提示仍在。390 CSS 像素下 viewport/scrollWidth 均为390；危险 javascript 链接数为0，script 内容没有执行或显示，控制台仅 React DevTools 信息。临时 HTML/TSX/JSON/Go fixture 已删除，专用浏览器已关闭，Vite 5197 已停止并核验无监听。

浏览器没有经过完整 App 认证、PostgreSQL 当前/历史读取、River 或外部模型；本切片不能替代运行时/存储/双基线发布代理的真实链证据。没有重跑完整生产模型链，也未实现人工冲突编辑/裁决 HTTP。当前保守整篇 review 契约不变，未来若扩大可信映射语义须同步 owner 与公开解码契约。

## 主会话收尾复核修正（2026-09-15）

主会话指出非零可信项的索引卡片缺少可辨认内容，以及冲突多观点共享来源时重复按钮/key。两项均已修正：

- v2 全文后明确显示“全文片段索引”和“以下摘录用于辨认上方全文中的知识点及其来源，不是新增正文。”事实展示原文本；冲突展示主题、尚未裁决标签和各观点文本；缺口展示问题与已补充结论/待补充状态。保留原 article item ID、focus/scroll 与 body reference 精确链接；不重新拼装全文。
- sourceButtons 使用既有 synthesisSourceIdentity 对全部来源身份（workspace/source/version/artifact/projection/content hash/span/excerpt hash）去重和作为 React key。title 是展示元数据，不作为来源身份；不按 span 单字段吞掉不同来源。API 边界原有“同 span 不同身份”拒绝仍保留，新增冲突两观点共享精确引用成功、同 span 换 SourceID 拒绝断言。
- 历史角色不是仅由 revision.display 推断：独立来源 HTTP 响应本身带 `role=HISTORICAL_REVIEW`，原始来源可用性 AVAILABLE 仅表示片段可读，不表示当前正文可信。现有 handler 回归补充明确断言：不包含 display 的独立来源响应仍携带历史角色。严格 Web decoder 保留该受控枚举并拒绝未知角色。弹窗也直接识别返回 role；响应未到达时通过当前 revision 的精确历史引用身份预先标记，避免等待期间显示可信误导。

验证：两个 Web 文件48项测试通过（2.02s）；typecheck、相关ESLint与diff检查通过；HTTP TestSynthesisHTTPManuscriptReading通过（0.676s）。没有改变OpenAPI结构或生成契约，因此沿用上一轮生成一致性证据。

另用独立 Chromium 会话 manuscript-index 实际运行严格 decoder 与正式 NoteContent，固定 v2 非零 Items UI 夹具。确认焦点为 synthesis-item-ca000000-0000-4000-8000-000000000012；聚焦卡片包含冲突主题及30秒/60秒两个观点；共享来源按钮数恰为1；全文索引与非新增正文说明可见；控制台仅React DevTools信息，无重复key错误。此项是组件夹具验收，不是后端/模型链。临时HTML/TSX、专用浏览器和Vite服务均已清理。
