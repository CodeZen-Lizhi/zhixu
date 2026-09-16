# 00117 接线检查重点

根会话确认当前 pending view 只匹配 synthesis_note.current_revision_id。默认阅读页却显示 Authoring 的 current_published_revision_id 对应的 SynthesisRevision。这两个身份在有待审草稿时不同。

必须验证：下游已发布版本 P 引用上游 X；下游后来产生未发布草稿 C；上游发布 X2 后首次运行影响扫描。此时用户默认仍阅读 P，P 的引用必须有更新提醒，不能因为已有 C 而只记录 C、让 P 的 body-impacts 返回空。

建议当前可见基线集合同时覆盖当前候选与当前已发布版本（后者通过 core.document 的当前发布指针和 synthesis_proven_publication 精确核验）。记录仍按 base_revision/item/publication 去重；后续生成 dispatcher 选择最新可编辑基线并重新验证，提醒记录本身没有写正文权限。只读历史查询仍返回曾记录的历史观察。

如果以其他方式完成同样行为，必须用上述真实发布 + 待审草稿场景证明。不要以测试只有一个未发布下游草稿为由忽略默认阅读的发布版本。另需检验新加入无关条目不触发、同文 body_reference 变化不触发、草稿上游不触发、重复/并发处理不重复落记录。
