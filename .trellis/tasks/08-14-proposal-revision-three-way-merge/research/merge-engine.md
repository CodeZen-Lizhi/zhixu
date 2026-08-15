# Research: 服务端三方合并引擎与安全契约

- Query: 在现有项目约束下选择成熟、确定且可受控的服务端三方文本合并引擎；比较项目 Git CLI 模式、`git merge-file` 官方语义和可行 Go 库，并定义 diff3 冲突、退出码、大小/编码、临时文件、超时、版本依赖与测试向量。
- Scope: mixed
- Date: 2026-08-14

## Findings

### 结论

首期应复用运行镜像已经安装的 **Git CLI `git merge-file`**，通过新的窄领域端口和 `internal/platform/gitcli` 内的纯计算 Adapter 暴露三方内容合并；不应引入新的 Go 合并库，也不应把原始 Git 参数或分支合并能力暴露给调用方。

推荐的固定算法契约为：

```text
engine_contract = git-merge-file/diff3/myers/marker32/v1
arguments       = merge-file -p -q --diff3 --diff-algorithm=myers --marker-size=32
                  -L CURRENT -L BASE -L PROPOSED current base proposed
input order     = current, base, proposed
working dir     = fresh private temporary directory, not the Workspace/repository
```

项目现有 runner 仍可提供 executable 注入、固定 argv、环境清理、context 取消和有界输出等机制，但需要增加合并专用的受控入口，而不是开放通用命令。该入口还必须隔离 system/global Git config、把 `1..127` 退出码解释为“产生冲突的有效结果”、解析并校验 diff3 输出、管理私有临时文件，并施加合并专用的输入/输出/并发/超时限制。

这个选择符合 ADR-0019：Git CLI 已是 ADR-0009 批准并部署的项目能力，官方三方算法覆盖核心需求；剩余工作是安全边界和领域结果映射的薄 Adapter，而不是重新实现 diff/LCS/三方区间归并。它也避免增加一个成熟度不足、输出语义不同且不可取消的 Go 依赖。

### ADR-0019 需求与候选覆盖

先应用硬约束：服务端真实三方语义、可展示 base/current/proposed 的冲突、输出可版本化、UTF-8/大小/超时可控、部署和许可证兼容、可做契约测试。任一硬约束失败即淘汰，不能由总分抵消（`docs/architecture/adr/0019-mature-framework-first.md:21-32`）。

加权项：三方/diff3 语义 25、确定性与版本化 20、取消和资源边界 15、现有部署与运维 15、成熟度/文档/测试/许可证 15、集成维护成本 10。

| 候选 | 加权覆盖 | 硬约束结论 | 证据与差距 |
| --- | ---: | --- | --- |
| 现有 Git CLI + `git merge-file` | 92/100 | 通过，但必须加薄安全 Adapter | 官方维护、真实三方合并和 diff3；镜像已有 Git。显式参数和运行时合规探针可冻结行为；Adapter 补结构化解析、应用级限制和临时文件清理。 |
| `github.com/epiclabs-io/diff3` | 61/100 | 不通过 | 可返回 A/O/B 结构且有 LCS/Myers，但只有 2026 pseudo-version、采用量很低、API 近期有 breaking change；无 context/超时/输出上限。文本 helper 使用默认 `bufio.Scanner` token 限制并重建 LF，丢失长行和 EOF 换行语义。 |
| `github.com/devsisters/go-diff3` | 58/100 | 不通过 | 可返回 A/O/B 结构，但无稳定 tag、采用量低、无 context/资源边界；文本 helper 同样受 Scanner 默认单行限制并重建换行，marker 语义也不是 Git diff3。 |
| `github.com/nasdf/diff3` | 42/100 | 不通过 | v1.0.0 但采用和测试证据很弱；只返回 marker 字符串，无 base marker、冲突数或结构化 hunk，也无取消/限制。全局可变 `DiffMatchPatch` 增加并发风险；双方同改的实现路径需差分测试证明正确。 |
| `github.com/charlesvdv/go-three-way-merge` | 35/100 | 不通过 | 2018 pseudo-version、无采用证据；rune 级实现遇首个冲突即返回，缺少完整冲突列表/base 上下文、context、资源约束，源码仍有 TODO。 |
| `github.com/sergi/go-diff` | 38/100 | 不通过 | 成熟的二方 diff/match/patch，不是三方合并器。`DiffTimeout` 是算法内部期限/降级行为，不是可观察、可取消的服务端 deadline；在其上自建 diff3 会复制核心机制。 |
| `github.com/go-git/go-git/v5` | 33/100 | 不通过 | 成熟纯 Go Git 实现，但兼容矩阵没有可替代 `git merge-file` 的工作树文本三方/diff3 能力，merge 支持也不覆盖本需求；引入它仍需另写合并算法。 |

Git CLI 的 92% 覆盖不是“直接执行命令即可”。未覆盖部分是项目特有的输入约束、稳定错误、结构化冲突和生命周期管理，适合由标准库与窄 Adapter 完成，符合 ADR-0019 对薄封装的要求（`docs/architecture/adr/0019-mature-framework-first.md:25-26,48-49`）。

### `git merge-file` 官方行为与固定契约

官方语义是 `git merge-file <current> <base> <other>`：把 `base -> other` 的变化应用到 `current`。因此本功能的顺序必须固定为 `current, base, proposed`，不能按 API 字段字母顺序传递。

- `-p/--stdout` 把结果写到 stdout，避免覆盖 `current` 临时文件；这也是证明“合并不写 Workspace”的必要参数。
- `-q` 抑制预期冲突告警，stderr 仅保留真正诊断。
- `--diff3` 在冲突块中加入 `||||||| BASE` 段，提供 current/base/proposed 三方上下文。
- 三个 `-L` 固定为服务端常量 `CURRENT`、`BASE`、`PROPOSED`；不得带目标路径、标题或用户文本。
- `--diff-algorithm=myers` 明确算法，避免依赖配置或默认值；`--marker-size=32` 使用固定长 marker，降低普通 Markdown 碰撞概率并为解析器建立版本化语法。
- 不使用 `--object-id`：输入未必是 Git object，而且创建 object 会引入 repository/object-store 副作用。

官方退出语义与普通 CLI 不同：错误返回负值；否则返回冲突数，最大截断为 127。进程层通常把 C 的负退出值观察为 255，而 Go 对未启动/被信号终止等情况也可能得到 `-1`。因此 Adapter 的状态机必须是：

| 观察结果 | 解释 |
| --- | --- |
| exit 0 | 有效的无冲突候选；还需通过输出编码、大小和 marker 不变量校验。 |
| exit 1..126 | 有效的冲突结果；解析出的冲突数必须与退出码相等。 |
| exit 127 | 有效的冲突结果；解析出的冲突数必须至少为 127，因为官方已饱和截断。 |
| exit 128..255、exit -1、启动失败、signal | 引擎失败，不得解释为冲突。 |
| context 取消/超时或 stdout/stderr 超限 | 资源边界失败优先于退出码，不得使用部分输出。 |

现有 `runCommand` 对任意非零退出都构造 `commandError`，但同时保留有界 stdout 和退出码（`internal/platform/gitcli/runner.go:73-96,128-164`）。合并入口必须特殊处理预期的 `1..127`，不能先经过通用 `classifyGitReadError`，否则真实冲突会被错误映射为依赖失败；也不能把 `commandError.Stdout/Stderr` 上抛，因为里面包含正文。

#### 确定性和 marker 解析

推荐固定 marker 行为：

```text
<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<<< CURRENT
|||||||||||||||||||||||||||||||| BASE
================================
>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>> PROPOSED
```

解析器应按原始字节和完整行边界工作，保留“末尾有/无换行”事实；不要使用默认 `bufio.Scanner`，也不要用会丢失尾空项的字符串切分。输出应转成领域结构，例如交替的普通文本 segment 和 `{current, base, proposed}` conflict segment；浏览器不负责解析 marker。

为了让 marker 语法无歧义，规范化后的任一输入若含上述任一 **完整保留 marker 行**，应以稳定的输入错误拒绝。普通 7 字符 Markdown 示例不应被误拒。解析完成后必须验证：

1. marker 状态机完整闭合、顺序固定、无嵌套或孤立 marker；
2. 解析冲突数与退出码一致（127 按饱和规则）；
3. 结构化结果重新序列化后与原始 stdout 字节一致；
4. stdout 是合法 UTF-8、不含 NUL、未超结果上限；
5. 无冲突结果不得残留任何保留 marker；提交新 Revision 时也拒绝保留 marker。

同一 `engine_contract`、同一规范化输入必须得到逐字节相同结果。合并预览/创建 Revision 的请求绑定至少应包含该 contract id、source Revision/Change Hash、raw current Hash 和三份输入摘要；运行时 Git 版本只进入内部 metric/readiness，不进入公开错误或日志正文。升级 Git 或改变算法/marker/规范化规则，只能通过新的 contract version 和 golden 审核发布，不能静默改变同一幂等请求的输出。

### 编码、换行与大小限制

Git 自身不是项目安全边界：官方源码允许接近 1 GiB 的 xdiff 输入，所谓 binary 检测只看前 8000 字节是否出现 NUL；它不验证 UTF-8，也不会检查 8000 字节后的 NUL。因此进入临时文件前，三份输入都必须：

- 通过 `utf8.Valid`；
- 全文拒绝 NUL；
- 分别应用明确的字节上限，不能只限制 HTTP body 或最终输出；
- 使用同一 Markdown canonicalization 规则后再合并。

项目已有 `CanonicalizeMarkdown` 会把 CRLF 和裸 CR 都归一为 LF（`internal/authoring/domain/model.go:420-424`），Authoring/Safe Writeback 正文上限是 10 MiB（`internal/authoring/domain/model.go:23-24,570-573`、`internal/changecontrol/domain/workspace_store.go:15-16`），但现有 Proposal current-content 读取上限只有 1 MiB（`internal/changecontrol/application/service.go:35-36,656`），读取器已做 `LimitReader(max+1)` 和 UTF-8 校验（`internal/changecontrol/adapter/localfs/reader.go:103-138`）。

**首期冻结为每份输入/最终正文 1 MiB、stdout 4 MiB、stderr 64 KiB、最多 1024 conflicts、5 秒 deadline、每进程并发 2**，复用已经公开并验证的 current-content 边界；marker-free candidate 仍不得超过 1 MiB。Phase 0 必须在目标 API 镜像以双并发病理 fixture 验证这些常量，失败则停止并重新审阅合同。该边界会使合法的 1--10 MiB Proposal 无法进入三方合并，必须返回稳定“合并输入过大”并引导重新生成，而不是降级到另一算法。

若产品要求所有当前合法 Proposal 都能合并，则必须显式选择每份 10 MiB，并同步提升 current-content/OpenAPI 边界、将 stdout 上限提高到经基准验证的值（初始候选 64 MiB）、增加更严格并发闸门和病理输入基准。这不是实现者可暗中决定的常量调整。

三份输入建议在内存中规范化为 LF 后交给 Git，以避免 Git 按某一侧 CRLF 生成不同 marker 行；同时 **new Revision 的 BaseHash 必须绑定服务端读取的 raw current bytes**，否则后续 Safe Writeback 漂移检查会失真。当前 Authoring 会归一 CRLF 和裸 CR，而 `ComputeChangeHash` 只归一 CRLF（`internal/changecontrol/domain/model.go:231-236`）；实现前应收敛一个共享的 Revision 内容规范化事实源，并用测试证明 hash、preview、最终 revision 一致。

另一个现存契约冲突是 `file_patch` Revision 当前拒绝空白最终正文（`internal/changecontrol/domain/typed_proposal.go:697-708`）。AC7 的“空文件”可以覆盖空 base/current/proposed 的引擎向量，但若需求意图允许最终删除为 0 字节，必须先明确修改领域契约，不能只让合并引擎返回空字符串。

### 临时文件和隐私契约

1. 用 `os.MkdirTemp` 创建单次请求专用目录，目录权限保持 `0700`；目录必须位于服务端控制的临时根，不接受客户端路径。生产非 root UID 10001 必须在 readiness/Docker smoke 中证明该根可写。
2. 目录内只使用固定、不敏感的文件名 `current`、`base`、`proposed`。用 `O_CREATE|O_EXCL|O_WRONLY` 和 `0600` 创建，写入已验证的精确规范化字节，并检查 Write/Close 错误。
3. Git 的 cwd 固定为该临时目录，使用 `-p`；绝不把 Workspace 路径、Repository 路径或用户文件名交给 `merge-file`，因此命令不能修改 Workspace/Git/index/object database。
4. 所有返回路径（clean、conflict、解析失败、超时、输出超限、panic recovery）都清理精确创建的目录。包含正文的临时目录清理失败不能返回成功；映射为稳定的资源清理错误并产生不含路径/正文的运维告警。
5. 日志、Problem、Audit、trace attribute 和 metric label 均不得包含 argv 中的临时绝对路径、三方正文、marker 原文或 stderr。只记录 operation、contract id、字节数区间、耗时、冲突数量、稳定错误码和内部版本维度。

现有文件写回的 temp/backup 是目标同目录的原子写安全模型（`docs/architecture/quality.md:45-48`）；本合并是无持久副作用的计算，应使用独立私有临时目录，而不是复用 Workspace 写回 locator/backup，也不需要 Workspace Git operation lock。

### 命令环境、超时和资源控制

现有 runner 已有可复用基础：默认 4 MiB 输出限制、固定全局参数、`exec.CommandContext`、清理继承的 `GIT_*`/locale、只读 optional locks 和越界时取消（`internal/platform/gitcli/runner.go:15,37-125,166-207`）；测试覆盖固定 argv/环境、输出上限、退出码、timeout 和 executable unavailable（`internal/platform/gitcli/runner_test.go:14-73,104-172`）。但合并契约还需补齐：

- 明确设置 `GIT_CONFIG_NOSYSTEM=1` 与 `GIT_CONFIG_GLOBAL=<os.DevNull>`。当前 runner 虽清理继承 `GIT_*`，仍可能读取 system/global config（`internal/platform/gitcli/runner.go:99-125`）。显式 CLI flag 优先，但完全隔离更能抵御 alias/config/未来 Git 行为变化。
- 合并调用不接受 nil context；由 Adapter 创建固定 5 秒总 deadline，caller 更短 deadline 优先。Phase 0 必须用上限文件和重复行病理 fixture 在目标容器验证该冻结值，失败时回到规划审阅。
- stdout 与 stderr 应使用独立上限；现有 runner 对两者共用同一个值（`internal/platform/gitcli/runner.go:45-53`），不应为了允许较大候选而同时允许同等大小的错误诊断。
- 加容量为 2 的进程内并发 semaphore，等待 semaphore 时计入固定 deadline 并尊重更短 caller context；Phase 0 目标容器门禁失败时不得现场放宽。
- `merge-file` 不会启动网络 remote helper，`exec.CommandContext` kill 加 `WaitDelay` 足以作为初始模型；可以复用 Unix remote runner 的进程组终止模式（`internal/platform/gitcli/remote_process_unix.go:13-27`）作为纵深防御，但不要把 remote credential/askpass 环境带入合并。
- timeout、cancel、output-too-large、temporary-storage-unavailable、engine-unavailable、unsupported-engine、invalid-engine-output 分别使用稳定错误码；不得 silent fallback 到纯 Go 或不同 diff 算法，因为那会破坏输出和幂等性。

### Git 版本与部署约束

- 运行镜像当前基于 Alpine 3.22 并安装 `git`，服务以无 home 的 UID 10001 运行（`deploy/Dockerfile:31-34,47-49`）。Alpine v3.22 main x86_64 在调研日发布的包是 Git `2.49.1-r0`。
- `--diff-algorithm` 由 Git commit `4f7fd79e57f483282bc21bf5b0669f9c8229d32a` 引入，tag 检查显示需要 Git 2.44 或更高；当前生产基线满足。`--diff3`、`-L`、`--marker-size` 是更早已有能力。
- Dockerfile 的 `apk add git` 没有锁定精确 package revision，`alpine:3.22` tag 也可能解析到更新镜像。仅写“Git >= 2.44”不足以保证 byte-for-byte 稳定；同一滚动发布期间不同 replica 也可能生成不同结果。
- 推荐启动时运行不含业务数据的 feature/conformance probe，验证固定 flags、预期退出语义与一组小 golden；不满足时 readiness 失败/能力不可用，绝不改用默认参数。发布物还应固定基础镜像 digest 和可复现 package 来源，或至少把经验证的 Git version/fingerprint 纳入部署门禁。
- Git 版本升级必须跑跨版本 differential corpus；任何候选字节或冲突 hunk 边界漂移，都需要人工审核并升级 `engine_contract`。旧 preview/token 仍由旧 contract 识别并拒绝跨 contract 提交。
- Git 是现有 GPL-2.0 外部可执行程序；本选择没有新增 Go 链接依赖或 module license。候选 Go 库多为 MIT/Apache，但许可证可接受不能弥补能力/成熟度硬缺口。

### 代码模式与建议落点

- `docs/architecture/adr/0009-git-cli-adapter.md:7-12` 已决定用 Git CLI、固定白名单和参数数组，运行时安装 Git，禁止 shell 与任意参数。
- `docs/architecture/quality.md:49-50` 要求固定 executable/argv/cwd/output/timeout 并清理环境，但当前文字笼统禁止 `merge`。`merge-file` 是不接触 ref/index/worktree 的纯计算 plumbing，不是 branch merge；设计/spec 必须显式增加这一受控例外，不能默认为已获授权。
- `.trellis/spec/backend/git-sync-contract.md:14-16` 要求 `gitcli` 只暴露固定领域动作且调用方不能传 raw args/env；新的 API 应是 `MergeText(ctx, input)` 一类窄方法，而不是把 `runCommand` 或 `[]string` 提升为公共接口。
- `.trellis/spec/backend/quality-guidelines.md:37-42` 要求先定义领域接口/不变量/稳定错误，并覆盖 timeout、版本冲突和资源清理；`.trellis/spec/backend/quality-guidelines.md:47-51` 将 ADR-0019 作为选型门禁。
- `internal/platform/gitcli/status.go:18-30` 的 executable 默认值与注入模式可用于 fake executable 契约测试；生产 Composition 已统一构造 `gitcli.New("")`，不应再引入第二套 executable 配置。
- `internal/platform/gitcli/writeback_inspect.go:23-27` 已有 16 MiB/64 MiB 的受控 Git 输出先例；`internal/platform/gitcli/writeback_inspect.go:844-862` 有 timeout/cancel/output/unavailable 的稳定分类模式，但合并层必须避免把承载正文的原始 error 链暴露到外部。
- `internal/changecontrol/adapter/localfs/reader.go:103-138` 已有同一快照、`LimitReader(max+1)`、UTF-8 和 hash 的读取模式；三方 current 必须复用安全目标解析与同一 raw byte snapshot，不能让浏览器回传 current 充当事实。

建议模块边界：Change Control domain/application 只依赖 `TextMergeEngine` 端口及领域 DTO；Git 参数、临时文件、marker parser、Git 版本探针和稳定 adapter errors 留在 `internal/platform/gitcli`（或其更窄子包）。领域层负责把 engine contract/current hash/source revision 绑定到预览与创建命令，并在创建新 Revision 时重新校验全部冲突已解决；它不依赖 Git 类型或 marker 字符串。

### 必测向量

#### 纯引擎与解析器

- base/current/proposed 全相等；仅 current 改；仅 proposed 改；双方做相同改动；双方非重叠改动，断言 clean 候选逐字节准确。
- 同一区域不同修改产生一个 diff3 hunk；多个独立冲突全部返回；构造 126、127、128 个冲突验证退出码饱和与解析计数。
- 相邻行修改、同位置插入、删除对修改、双方删除、删除对新增、文件头/尾冲突、base/current/proposed 分别为空、无 final LF。
- LF、CRLF、混合 CRLF/LF 和裸 CR，断言规范化结果、raw current hash 和新 Revision BaseHash 各自正确。
- 合法多字节 UTF-8/组合字符；非法 UTF-8；NUL 位于前 8000 字节及 8000 字节之后；输入含完整保留 marker；普通 7 字符 marker 文本不误判。
- 每份输入恰好 max、max+1；stdout 恰好 max、max+1；stderr 越界；长单行；高度重复 Markdown 导致最坏 diff；deadline 和 semaphore 等待取消。
- executable 缺失、不支持 flag、exit 255、signal、部分 stdout 后失败；任何异常都不返回部分候选或正文诊断。
- fake executable 断言完整 argv 顺序、固定 labels/cwd/env、config 隔离、文件名与 `0600` mode，不含用户路径/正文。
- clean、conflict、parser error、timeout、stdout overflow、write/close error 全部清理 temp dir；注入 cleanup failure 时返回稳定错误且无敏感路径。
- 同一 contract/input 重复执行得到完全相同 bytes/segments；结构化结果重序列化与 raw diff3 output 相等；最终正文无保留 marker。

#### 官方 fixture 和集成

- 移植 Git 官方 `t/t6403-merge-file.sh` 的无改动、无冲突、缺失 final LF、冲突、labels、文件尾删除冲突、binary、diff3、marker-size、CRLF marker 和 diff algorithm 用例。
- 真实 Git adapter contract 在开发环境和生产 Docker 镜像中运行固定 golden；验证 feature probe、非 root 临时目录权限、无 repository 也可运行、无 Workspace/Git/index/object/db 变化。
- 在每个受支持 Git 版本上跑 differential corpus；升级前比较候选 bytes、冲突数量和每个 A/O/B hunk，差异必须触发 contract version 审核。
- 应用集成验证 engine timeout/invalid output 映射、current 二次漂移、preview contract/hash 绑定、冲突未清零禁止创建 Revision，以及正式写入仍只能走 Proposal Revision -> Approval -> Safe Writeback。

Git 官方测试还揭示一个重要选择：Myers 在某些代码排列上可能产生额外冲突，而 histogram 可避免；这不是“更优算法”的普遍证明。首期应固定 Myers 以获得 Git 默认算法的长期兼容面，并加入真实 Markdown corpus 比较。若后续证据支持 histogram，需作为新的 engine contract 发布，不能在运行时自动切换。

## Files Found

- `.trellis/tasks/08-14-proposal-revision-three-way-merge/prd.md:21-36` — 确认服务端确定性三方合并、三方上下文、非法/超限拒绝和无直接写入边界。
- `.trellis/tasks/08-14-proposal-revision-three-way-merge/prd.md:54-65` — 隐私要求和空文件/EOF/换行/相邻修改/同区删除新增/大文件测试验收。
- `docs/architecture/adr/0019-mature-framework-first.md:19-49` — 候选硬约束、80% 加权门槛、Adapter 和自研例外证据要求。
- `docs/architecture/adr/0009-git-cli-adapter.md:5-12` — 已批准 Git CLI Adapter、固定白名单/argv 与运行时依赖。
- `docs/architecture/quality.md:43-50` — 文件/Git 安全边界及当前 `merge` 禁止措辞。
- `.trellis/spec/backend/quality-guidelines.md:10-14,25-42,47-61` — Adapter、稳定错误、资源清理、成熟方案和测试层级要求。
- `.trellis/spec/backend/git-sync-contract.md:14-16,49-58` — 固定领域动作、禁止 raw args/自动 branch merge、进程取消契约。
- `internal/platform/gitcli/runner.go:15-207` — 现有有界、可取消 Git CLI runner 及环境/错误行为。
- `internal/platform/gitcli/runner_test.go:14-172` — 固定 argv/env、输出上限、退出码、超时和 executable 测试模式。
- `internal/platform/gitcli/remote_process_unix.go:13-27` — Unix 进程组取消与 2 秒 WaitDelay 模式。
- `internal/platform/gitcli/writeback_inspect.go:23-27,844-862` — 现有 Git 大输出限制和稳定错误分类模式。
- `internal/changecontrol/application/service.go:35-36,625-663` — current-content 的 1 MiB 上限与权威读取链路。
- `internal/changecontrol/adapter/localfs/reader.go:103-138` — 安全文件快照、大小、UTF-8 与 hash 校验。
- `internal/authoring/domain/model.go:23-24,420-424,570-573` — 10 MiB 正文边界、Markdown 换行规范化、UTF-8/NUL 校验。
- `internal/changecontrol/domain/workspace_store.go:15-16` — Safe Writeback 的 10 MiB 上限。
- `internal/changecontrol/domain/model.go:231-236` — 当前 Change Hash 的换行规范化细节。
- `internal/changecontrol/domain/typed_proposal.go:697-708` — `file_patch` Revision 当前禁止空白正文。
- `deploy/Dockerfile:31-49` — Alpine 3.22、安装 Git、生产非 root UID。
- `go.mod:1-31` — Go 1.25.4 基线，当前没有三方合并库依赖。

## External References

- [Git merge-file manual](https://git-scm.com/docs/git-merge-file) — 参数、输入顺序、diff3 和退出码的官方契约。
- [Git `merge-file` implementation](https://github.com/git/git/blob/master/builtin/merge-file.c) — 读取限制、binary 检测、label、输出与冲突数截断实现。
- [Git xdiff size definition](https://github.com/git/git/blob/master/xdiff-interface.h) 与 [binary detection implementation](https://github.com/git/git/blob/master/xdiff-interface.c) — `MAX_XDIFF_SIZE` 和前 8000 字节 NUL 检测边界。
- [Git xmerge implementation](https://github.com/git/git/blob/master/xdiff/xmerge.c) — diff3 marker 和冲突计数实现。
- [Git official merge-file tests](https://github.com/git/git/blob/master/t/t6403-merge-file.sh) — EOF、CRLF、diff3、marker-size、binary 和算法差异 fixture。
- [Git commit adding `--diff-algorithm`](https://github.com/git/git/commit/4f7fd79e57f483282bc21bf5b0669f9c8229d32a) — 最低版本/feature probe 依据。
- [Alpine 3.22 x86_64 Git package](https://pkgs.alpinelinux.org/package/v3.22/main/x86_64/git) — 调研日运行镜像对应包版本。
- [go-git compatibility matrix](https://github.com/go-git/go-git/blob/master/COMPATIBILITY.md) — merge 能力范围证据。
- [sergi/go-diff](https://pkg.go.dev/github.com/sergi/go-diff/diffmatchpatch) — 二方 diff/match/patch API 与 timeout 语义。
- [nasdf/diff3](https://pkg.go.dev/github.com/nasdf/diff3)、[charlesvdv/go-three-way-merge](https://pkg.go.dev/github.com/charlesvdv/go-three-way-merge)、[devsisters/go-diff3](https://pkg.go.dev/github.com/devsisters/go-diff3)、[epiclabs-io/diff3](https://pkg.go.dev/github.com/epiclabs-io/diff3) — Go 候选的版本、API、许可证与采用信息。

## Related Specs

- `docs/architecture/adr/0019-mature-framework-first.md` — 本次选型门禁与薄 Adapter 原则。
- `docs/architecture/adr/0009-git-cli-adapter.md` — Git CLI 是既定基础设施选型。
- `docs/architecture/quality.md` — 文件/Git/隐私与受控命令总边界。
- `.trellis/spec/backend/quality-guidelines.md` — 领域端口、Adapter contract、稳定错误、资源清理和验证层级。
- `.trellis/spec/backend/git-sync-contract.md` — 固定 Git 领域动作和进程取消约束；本功能不得扩大成 branch merge。
- `.trellis/spec/backend/authoring-contract.md` — Revision 不可变、正文规范化与 Authoring 边界。

## Caveats / Not Found

- **输入上限尚未由需求选定。** 当前审阅链路是 1 MiB，而 Proposal/Writeback 可到 10 MiB。本文推荐首期 1 MiB；若 AC 意图覆盖所有合法 Proposal，必须采用 10 MiB 路径并同步 API、output、并发和基准设计。
- **现有架构文字禁止 `merge`。** 尽管 `merge-file` 是无 repository 副作用的文本计算而不是 branch merge，实施前仍应在设计/规范中显式批准固定白名单例外，不能靠名称解释绕过现有约束。
- **空最终正文语义未对齐。** 当前 `file_patch` Revision 拒绝空白内容；AC7 只明确要求空文件测试，没有明确允许创建空 Revision。
- **换行规范化存在两个事实源。** Authoring 归一 CRLF 和裸 CR，Change Hash 只归一 CRLF；合并实现前需明确唯一 canonicalization 和 raw BaseHash 绑定。
- **精确运行时版本尚未锁定。** Alpine 3.22 当前包满足所需 flags，但 Dockerfile 未 pin Git revision；必须由镜像 pin/conformance probe/differential fixtures 把升级变成显式事件。
- **超时、并发和输出数值必须通过目标容器门禁。** 冻结值为 5 秒、并发 2、1 MiB 输入/4 MiB stdout/64 KiB stderr、1024 conflicts；Phase 0 双并发 30 轮必须全部在 deadline 内且增量峰值 RSS 不超过 128 MiB，否则不得进入 Adapter 编码。
- 外部库成熟度/版本数据按 2026-08-14 的官方仓库与 pkg.go.dev 页面记录；没有发现同时满足稳定发布、完整 diff3、context 取消、长行/EOF 保真和资源上限的纯 Go 库。
- 官方站点的内置检索通道曾超时，本次外部事实改用官方 Git/GitHub/Alpine/pkg.go.dev 页面直接核对；没有以博客或二手文章作为行为依据。
