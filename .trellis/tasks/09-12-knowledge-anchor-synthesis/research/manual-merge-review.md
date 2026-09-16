# 纯合并与裁决定向审查

2026-09-15，impact-check；限定 application/synthesis_manuscript_merge.go、synthesis_manuscript_resolution.go 及对应测试、domain/synthesis_manuscript.go 与 manuscript Mapper。不涉及 119 存储、生产接线或发布授权。

原两个 P2 已修复：Mapper 对任何人工字节变化整篇待复核；历史排除继承 next、Latest 历史 tombstone 和 ReviewItems 的完整 union，保留缺席 ID、稳定排序，不再因删除后原 ID/字节恢复而复活。

未发现新增信任边界缺陷。两阶段保留 L 人工全文后再按 P/F 合并；每阶段冲突独立 fingerprint，逐一核对完整有序 ack，未解决外层冲突不返回 Manuscript。Fingerprint 绑定完整输入与阶段预览，F 漂移使旧裁决失效；裁决结果复制 ack，且不产生文件/授权/发布副作用。NUL 在 envelope 创建与完整性校验均拒绝；v1 固定 projection/content golden 与省略扩展字段测试仍保留。

历史排除上限已确认沿用 MaxSynthesisItems=128，与 root 的 seq13350 实施要求一致；第 129 个历史排除明确要求人工恢复，不丢弃历史。审查指令中的 1024 为笔误，该数值属于既有 Git 冲突块上限。本项无待统一事项，无需修改实现或测试。

验证：复用 impact-ui 最终消息所报告的三包限定测试及 go vet PASS；本轮审阅了三次真实 Git tombstone 预览、超限、两阶段裁决、输入漂移、NUL 与 v1 golden 断言，未重复运行已通过检查。此前本审查者独立执行的四项 Git 合并与 Mapper 测试 PASS 不替代新 resolution 证据。文件/发布真实性、来源授权、历史与 receipt 持久继承仍由后续 owner 核验，不据此宣称已接存储或已发布。
