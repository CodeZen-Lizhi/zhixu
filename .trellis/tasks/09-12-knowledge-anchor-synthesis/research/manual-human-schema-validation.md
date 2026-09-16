# 通用 Human decision schema 验证修复

2026-09-15。完成范围：`internal/workflow/adapter/postgres/runtime_validation.go`、相邻 `runtime_validation_test.go`、独立 `runtime_human_schema_integration_test.go`。未修改 Human 提交生命周期、owner 权限、organizing/HTTP/UI、SQL/迁移/schema/hash；未提交、推送、部署或派生代理。

## 实现与复用判断

读取 backend 索引、验证约定、GORM/runtime 规范、任务 PRD/design/implement 与 `manual-human-core-review.md`；使用 trellis-before-dev 方法加载规范，并按 go-review 核对调用方和事务拒绝路径。

检索现有 `validateDecisionSchema`、`jsonEqual`、`matchesJSONType` 和 vendor 的 eino-contrib/jsonschema v1.0.3。该依赖 README 明确用于从 Go 类型反射生成 schema；公开实现提供 Schema/Reflector，`ID.Validate` 只验证 schema identifier，不提供 JSON 实例验证入口。已有 `jsonEqual` 使用 float64，不适合精确数值 enum。未新增依赖；在原有限验证器内使用 encoding/json、regexp、reflect、math/big 标准库。

- 属性级 `enum` 执行 JSON 结构等值：对象键顺序无关、数组顺序有意义，JSON 类型不混同。字符串/字符串数组绑定 processing、run、完整 note 集及顺序。
- 属性级 `pattern` 使用 Go regexp 编译，对字符串执行 MatchString；是否全字符串匹配由 schema 的锚点决定。不是 ECMA 正则兼容层，非字符串仍由已有 type 规则判断。
- 在检查提交值之前验证全部属性声明，包括提交中缺席的可选属性。非法 property/type、非数组或空 enum、非字符串 pattern、无效正则返回 `HUMAN_TASK_SCHEMA_UNSUPPORTED`。值不匹配返回原 `HUMAN_DECISION_SCHEMA_INVALID`。
- 合法 schema 的 required/type/additionalProperties 行为保留。支持的 type 仍为原七类；非法/联合 type 不再静默忽略。
- enum 单独 UseNumber 解码，并用标准库 Rat 规范数值后结构比较，避免相邻大整数因 float64 舍入被误判相等；1 与 1.0 相等。旧 number/integer type 检查及 `jsonEqual` 重放判断仍使用 float64，本次没有通用数字行为迁移。初始 Unmarshal 的 float64 范围限制也仍存在，不声称支持任意精度 JSON 数值输入/重放。当前 Synthesis enum 仅使用字符串及字符串数组。

这是现有验证器的明确有限子集扩展，不是完整 JSON Schema 实现：没有新增递归属性 schema、items、组合关键字、引用解析等验证支持。

## 关键证据

单元：Synthesis 同形 schema 的合法提交；错误 version/processing/run/note 集、缺 note、顺序；字段及数组元素类型；短/大写/非十六进制 hash；额外字段；非法 enum/pattern/type；缺席可选字段不能隐藏非法 schema；旧布尔 approval true/false、required/type/additionalProperties 拒绝；对象键序无关；9007199254740992 与 9007199254740993 不相等；1 与 1.0 相等；非锚定 pattern 的搜索语义。

实际 PG：复用 workflow 的 `newGORMRuntimeTestDatabase` → `testdb.Require` 和 `newGORMRuntimeTestRepository`、runtimeStateStartFixture；testdb 默认 AtlasMigration 从正式 MigrationDir 执行迁移。真实 pgvector:pg16 隔离容器，无 overlay、无禁用 trigger、无 SQL 状态修补。建立 hash→after 两节点 workflow，经真实 Start/Claim/WaitForHuman/SubmitHuman 调用：错误 processing enum 与错误 hash pattern 均拒绝；每次重新读取 task=pending、node=waiting_for_human 且后继不存在；合法决定 task=submitted、node=succeeded、后继=pending；精确重放保持 task identity、decision、node version 与原后继；不同但格式合法的 hash 重放返回 HUMAN_DECISION_CONFLICT。容器完成清理。

## 实际命令与结果

```sh
GOCACHE=/tmp/zhixu-human-schema-gocache go test -mod=vendor ./internal/workflow/adapter/postgres -run '^TestDecisionSchema' -count=1 -v
GOCACHE=/tmp/zhixu-human-schema-gocache go vet -mod=vendor ./internal/workflow/adapter/postgres
```

两项退出 0；单元包 0.656s。日志 `/tmp/manual-human-schema-unit.log`、`/tmp/manual-human-schema-vet.log`。

包级 integration 首次因默认 Go cache 读取权限失败；改为 /tmp cache 后发现现有 `organizing_terminal_integration_test.go` 导入 organizing postgres，而 organizing 生产代码导回 workflow postgres，形成 test import cycle。本次未修改该文件，以显式源文件列表排除它，保留其他原生产文件与测试/夹具；实际最终执行如下：

```python
import glob, os, subprocess
files = sorted(p for p in glob.glob('internal/workflow/adapter/postgres/*.go')
               if not p.endswith('organizing_terminal_integration_test.go'))
env = dict(os.environ, GOCACHE='/tmp/zhixu-human-schema-gocache')
for name, cmd in [
    ('pg', ['go', 'test', '-mod=vendor', '-tags=integration', *files,
            '-run', '^TestRuntimeHumanSchemaRejectsWithoutTransitionAndReplays$', '-count=1', '-v']),
    ('integration-vet', ['go', 'vet', '-mod=vendor', '-tags=integration', *files]),
]:
    with open('/tmp/manual-human-schema-' + name + '.log', 'w') as log:
        result = subprocess.run(cmd, env=env, stdout=log, stderr=subprocess.STDOUT)
    if result.returncode:
        raise SystemExit(result.returncode)
```

最终两项退出 0，PG 用例 9.46s、command-line-arguments 包 10.503s。日志 `/tmp/manual-human-schema-pg.log`、`/tmp/manual-human-schema-integration-vet.log`。不是全工作流包集成通过声明；没有重跑整个集成矩阵。

已 gofmt；定向 `git diff --check -- internal/workflow/adapter/postgres/runtime_validation.go internal/workflow/adapter/postgres/runtime_validation_test.go internal/workflow/adapter/postgres/runtime_human_schema_integration_test.go .trellis/tasks/09-12-knowledge-anchor-synthesis/research/manual-human-schema-validation.md` 通过。新文件另用 no-index diff 检查空白。go-review 自检未发现范围内新增阻断问题：验证在 submitted/后继写入之前执行，原事务/权限/owner 路径未变。

## 限制

enum/pattern 只是 schema 约束，不能证明真实 owner receipt。所有字段和格式符合但没有 owner receipt 的通用 Submit 仍可能通过 schema；后继 owner apply 必须拒绝。该 owner 防御测试由 runtime agent 保留，本次未重复运行、不以此报告替代其证据。当前 PG 测试使用通用两节点 schema，不执行 organizing apply，也不验证 HTTP/浏览器或真实模型质量。现有 integration import cycle 留给 main 处理。
