# 材料确认与整理模板：实施计划

## Prerequisite Gate

启动前验证 `08-02-quick-capture-profile` 已归档，Profile v1、Capture/Source 状态和 Evidence batch API 已写入稳定规范；不通过则保持本任务 planning。

## Order

1. 定义 Material union、Draft/Snapshot/Template/Run 状态与 canonical hash 测试。
2. 增加 additive migration、append-only/CAS 约束、Repository 与 concurrency tests。
3. 实现 Profile/Retrieval/Knowledge/Collection 批量 suggestion ports 和 reason 聚合。
4. 实现 Draft create、材料增删、refresh recovery 和 confirm transaction。
5. 实现 Built-in Template Catalog 与四个固定声明。
6. 实现 Custom Template clone/create/revision、strict validator/compiler 和越权拒绝测试。
7. 注册四个 Workflow Definition，接入 Human Task、Artifact 和 Change Control bridge。
8. 更新 OpenAPI、Capability、Composition Root、Worker、事件和 Timeline 必要投影。
9. 实现严格前端 API、整理页、材料确认、大纲审批、结果与发布路径。
10. 完成跨层、模型故障、response-loss、并发、浏览器和安全检查。

## Validation

```bash
go test -race -count=1 -timeout 60s ./internal/organizing/... ./internal/artifact/... ./internal/workflow/... ./internal/changecontrol/...
go vet ./internal/organizing/... ./internal/artifact/... ./internal/workflow/... ./internal/changecontrol/...
go mod tidy -diff
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- organizing artifacts workflows proposals
npm run build --prefix web
git diff --check
```

增加 disposable PostgreSQL migration/concurrency/fault tests、四模板 contract/eval fixtures、Workflow restart/Human Task/response-loss smoke，以及桌面/移动真实浏览器主链路。

## PRD Backfill Gate

实现、验证和 Review 通过后、归档前，将稳定交付行为回填到 `docs/product/PRD.md` 的 `10.4`、`10.6`、`10.11`、`11.1`、`11.4`、`13.7-13.10`、`14.4`、`14.8`、`14.10`、`14.12`、`21.12`、`21.14` 和 `22`；记录实际更新章节或无需更新的理由。不得提前写入未交付行为，不创建 `v2.0` PRD。

## Review

- 使用 `go-review`、`code-review-and-quality`、`sql-code-review`。
- 重点检查 Snapshot 原子性、N+1、模板越权、证据/GAP、Workspace 隔离、幂等和 Artifact/Proposal 唯一 owner。

## Rollback

按 capability 关闭新建 Draft/Run；保留 Template Revision、Snapshot、Artifact 和 Proposal 历史。不得删除已经产生的结果或把它们改写为旧模板版本。

## Delivery Record

- 已完成 `00074`、Organizing Domain/Application/PostgreSQL/owner adapter、四个固定 Workflow、受约束 Template Revision、HTTP/OpenAPI/Auth/Composition/Worker 和严格前端工作台。
- 确认链路使用单一 Serializable transaction fence，覆盖并发 receipt recovery、Source/Profile/Document/Claim/Collection/Evidence drift 和 active Index 复核。
- Human Task GET/POST 共享严格 review projector；专题大纲和合并比较绑定 Evidence、Document Source、Artifact、Diff、Snapshot 与 receipt，`review=null` 禁止提交。
- 同页材料补充已从 UUID 输入改为四类 Workspace owner 搜索；Snapshot UI 展示完整 Template/Snapshot hash、冻结材料和 Evidence。
- Document Revision 生成已补齐 exact batch content read：正文不持久化，只在模型调用期有界使用；Artifact 保存 DocumentSource identity，Markdown 导出保留 Article Revision、Revision No 与 SHA-256 来源投影。
- Generation 绑定新增 `Kind -> Prompt/Schema/ReducedSchema -> ResultType` 严格映射，首次绑定、幂等回放和成功/失败收口均拒绝跨类型 Model Run；最终输出对应的 Model Call 还必须继承同一 Model/Profile/Prompt，并按 `INITIAL/REPAIR` 与 `REDUCED` 阶段使用正确 Schema。
- `00074` disposable PostgreSQL 集成测试覆盖 DocumentSources v2 严格字段、同节去重、跨节复用、删除文档、64/65 边界、revision 投影去重和 deferred retrieval schema guard。
- PRD 已回填 `10.4`、`10.6`、`10.11`、`11.1`、`11.4`、`13.7-13.10`、`14.4`、`14.8`、`14.10`、`14.12`、`21.12`、`21.14` 和 `22/AC-39`。
- 已通过受影响 Go unit/race/vet、OpenAPI、Web lint/typecheck/全量 102 个测试文件（1061 tests）/build、`go mod tidy -diff` 与 `git diff --check`。Authoring 与 Organizing PostgreSQL integration 已使用本机 disposable 数据库实跑通过；桌面/移动真实浏览器门禁由父任务统一完成。
- Go/SQL 复核无剩余 Required finding；SQL Review 建议的 64/65 与 schema-kind 负向边界均已补为可执行测试，Generation 首次/重复绑定、Complete、Fail 与调用级 runtime 约束均有真实 PostgreSQL 仓储测试。
