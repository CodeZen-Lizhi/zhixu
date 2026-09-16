# 多轮正文传播验收

状态：本项目标测试通过；不代表完整 PRD 或生产 v2 runtime 已验收。

新增 `synthesis_body_roundtrip_integration_test.go` 与同前缀 runtime helper；未改共同 owner、其他 runtime 测试或迁移。helper 仅组合现有生产构造器，单一 Runtime/River 跨三次执行复用，没有复制处理状态机或伪造 processing/model 输出。已有一次性 helper 不适合重入，保持不动。

已证明：
- A 的 gap 被 B 包含，B 的 gap 被 C 包含；A 另一个条目反向包含 B，形成真实 A/B 笔记引用环。
- A 的解决草稿不传播；真实 Approval/Git 发布后只产生 B 候选，B/C 正式文件、正式指针和正式重读保持原样，C 不因 B 草稿而变化。
- B 候选经过真实审批/Git 发布后，C 与 A 的对应条目通过真实 dispatcher/River 生成局部候选；无关 item 完整结构及 Markdown 条目块逐字一致。
- 每个新候选持久 body_reference 精确绑定 upstream note/revision/publication/item/projection hash；旧 revision 重读结构不变。每次发布核对实际文件、Git commit、proposal_commit、正式指针及正式 snapshot。
- 有限轮发布后重复三次完整扫描/dispatch：固定3个请求、3个processing、6个READY模型步骤及6次SUCCEEDED持久ModelCall；A/B/C版本数4/2/2，每笔记恰1个刷新请求，当前候选/正式指针和文件不再变化。

验证命令与证据（正式完整迁移目录至123，无 overlay）：
- `go test -tags=integration ./internal/organizing/adapter/postgres -run '^$'`：编译通过，0.877s；`/tmp/body-roundtrip-compile.log`。
- `GOCACHE=/tmp/zhixu-go-cache go test -tags=integration ./internal/organizing/adapter/postgres -run '^TestSynthesisBodyRoundtripPublicationPropagation$' -count=1 -timeout=180s -v`：最终PASS，测试38.19s、包41.812s；`/tmp/body-roundtrip-final.log`。独占PG容器6729db8faea9已由testdb清理。
- `GOCACHE=/tmp/zhixu-go-cache go vet -tags=integration ./internal/organizing/adapter/postgres`：退出0；`/tmp/body-roundtrip-vet.log`。
- 两个新Go文件 gofmt、定向 diff whitespace检查通过；未跑全仓测试。

边界：初始来源、初始候选及关联审核沿用 synthesisGitFixture / anchor owner fixture。发布使用既有真实 Approval / Git / proposal_commit owner（SafeWriteback 的 running node 仍是既有 fixture seam）。自动传播阶段使用实际 dispatcher、River、RecordingChatModel journal、独立 semantic ModelRun 和当前 fence。使用固定 deterministic Provider，不证明真实外部模型质量；只注册 v1 graph、无人工全文，不代替另一个代理的 v2 runtime 验收。该环以未变化的上游条目终止，不声称证明任意模型对任意循环的收敛。

中间失败均为本测试夹具问题：topic key大写违反canonical约束、三份相同seed内容违反artifact workspace/hash唯一键，已修正；之后第一次实库PASS22.66s，再补无关条目Markdown字节断言后得上述最终PASS。没有生产bug或共同owner修复。最终默认Go缓存被沙箱拒绝，改用已有可写/tmp缓存完成验证。

协调限制：`trellis channel send ... --to main` 被当前沙箱拒绝（用户级 `~/.trellis/channels/...lock` 不在 writable roots），无法发主动消息；可读取channel。已读到main seq21634正式122/123 checksum可实库通知。此前另代理看到的瞬时 unused import 已修复，最终编译/vet通过。未提交、push或部署。
