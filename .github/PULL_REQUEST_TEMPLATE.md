<!--
提交前请按改动范围执行 CONTRIBUTING.md 和 Makefile 中当前受支持的门禁，并在下方记录结果。
尚未接入 Makefile/CI 的候选检查不得勾选为已通过。
-->

## 变更目的（Why）
<!-- 解决什么问题？关联 issue / ADR。一句话讲清意图。 -->

## 关联
- Issue:
- ADR（如涉及框架/依赖/架构）:

## 变更范围（What）
<!-- 改了哪些模块、是否破坏契约、是否含迁移/依赖升级。 -->
- [ ] 含数据库迁移（atlas/migrations/*.sql，前向兼容与恢复路径已验证）
- [ ] 含第三方依赖变更（已完成适用的漏洞与许可证检查，或已注明不适用）
- [ ] 含公开 API 契约变更（已跑 openapi-check）
- [ ] 含安全敏感模块（auth/changecontrol/migration/api/gitsync → 需双审）

## 自测步骤（How to verify）
1.
2.

## 门禁状态（CI Gate）
- [ ] go vet / go test -race 通过
- [ ] 前端 lint / typecheck / test / build 通过
- [ ] openapi-check / architecture-quality-baseline 通过
- [ ] `git diff --check` 通过
- [ ] 依赖变更已完成适用的漏洞与许可证检查，或已注明不适用

## 作者自检清单（对照五维度）
- [ ] **规范**：分层合规（domain 不依赖 adapter/http）；SQL 参数化；错误语义一致
- [ ] **可读**：命名自解释、嵌套浅、关键约束有 why 注释，无无关重构
- [ ] **安全**：边界输入已校验；无 SQL/命令拼接；路由权限正确；无硬编码密钥；内容安全展示
- [ ] **性能**：无 N+1 或无界查询；River job 幂等且尊重重试预算；资源关闭；context 取消传播
- [ ] **测试**：新增行为及边界/错误路径已覆盖；高风险改动有真实集成或 race 证据

## 审查请求
<!-- 说明希望重点看哪里；如本 PR 有暂不修的 🟡 问题，请在此链接 review-followup issue。 -->
