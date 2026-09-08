# Retrieval GORM Planning Review

- Date: 2026-08-21
- Scope: PRD、Design、Implementation Plan、Go/API边界、SQL/事务/native allowlist
- Reviewers: independent Go/architecture review；independent SQL/transaction review

## Findings And Resolutions

### 1. Dispatcher And Completion Owner Capabilities

- Finding: Workflow outbox claim/publish和Change Control Reindex binding/completion的concrete scoped能力当前不存在；
  Retrieval若直接实现完整GORM路径，只能重新写跨schema mutation或留下不可装配空壳。
- Resolution: Retrieval本child只定义consumer-owned scoped interfaces并实现不依赖owner concrete的普通路径。
  Dispatcher/Completion完整GORM实现明确标为依赖阻塞；concrete实现回各owner task，不在本child修改。
- Gate: owner capability交付前不得勾选AC3、TODO9 parity或任务完成。

### 2. Scoped Pool Affinity

- Finding: Foundation scope不携带可比较Pool identity，不能拒绝另一active Pool的scope。
- Resolution: scoped read只拒绝nil、非平台GORM类型和失活scope；同Pool由完整Pool constructor、
  Composition与TODO9 fixture保证，不做反射或虚假foreign-scope断言。

### 3. Dispatcher Construction

- Finding: 只构造GORMDispatcherStore不足以证明River client、options、queue/schema和fence来自同Pool。
- Resolution: 设计冻结完整NewGORMDispatcher：接收完整Pool、IDs、River options、scoped fence和typed owner
  collaborators；内部创建insert-only Workflow River client与scoped typed inserter；拒绝legacy
  options.EnqueueFence。

### 4. Regression Transaction Mode

- Finding: legacy Regression在结构失败时于同一RepeatableRead transaction把IndexVersion CAS为failed；
  read-only会触发SQLSTATE 25006并破坏fail-closed语义。
- Resolution: 改为RepeatableRead可写UoW；观察与失败更新保持同一transaction。TODO9覆盖失败更新、CAS丢失
  和commit response-loss。

### 5. pgx Allowlist

- Finding: Final allowlist文字遗漏既有riverpgxv5 worker/listener/migrator runtime。
- Resolution: 明确区分Retrieval Repository三项native capability与平台/runtime River例外；River例外不得
  放宽普通Repository。

## Review Outcome

普通Repository/Search/Evidence/Delivery/Processor Context/Regression、三项native capability、single Pool、
生产不接线和TODO9门禁的规划可执行。未发现剩余P0/P1/P2规划缺陷。

当前可进入实现的范围：Core boundary、Search/Evidence、Core Index/Vector/Regression、native allowlist、
Delivery/Processor Context、scoped接口和River mapping。Dispatcher/Completion完整落地继续等待owner capability。
