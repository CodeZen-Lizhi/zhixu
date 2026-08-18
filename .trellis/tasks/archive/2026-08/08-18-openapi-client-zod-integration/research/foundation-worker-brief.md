Active task: .trellis/tasks/08-18-openapi-client-zod-integration

你已经是 trellis-implement worker，直接实现阶段 0，不要再派生 trellis-implement 或 trellis-check。

本轮仅负责生成基础设施与契约分组，范围：

1. 为全部 189 个 OpenAPI operation 增加稳定、单一领域 tag，并添加顶层 tag catalog；重新启用
   Spectral operation-tags。不得改变路由、认证、Workspace 或响应语义。
2. 在 api/openapi 中锁定 @openapitools/openapi-generator-cli@2.40.1 和 generator 7.24.0，增加
   可审计的 OpenAPI 3.1 generator-input normalizer、配置、runtime.mustache 最小补丁和升级说明。
3. 生成 typescript-fetch 到 web/src/api/generated，配置必须符合 research/generator-compatibility.md；
   generated 目录只读且不得承载项目业务逻辑。
4. 增加 Makefile/CI 的 openapi-generate 与 openapi-generate-check；重复生成必须无 diff，未知
   normalizer 变换 fail closed。
5. 可修改 api/openapi、api/openapi/openapi.json、.spectral.yaml、Makefile、CI、web/src/api/generated
   以及必要的 npm manifest/lock。不要实现共享 Transport，也不要迁移任何 web/src/api/*.ts 生产模块。

先读取 implement.jsonl、prd.md、design.md、implement.md 和 research/ 下两份研究。保留工作树现有
无关改动，不提交、不 push。结束前运行定向 OpenAPI gate、生成漂移检查、generated 严格 typecheck；
若全量门禁受既有问题阻塞，报告精确命令和证据。
