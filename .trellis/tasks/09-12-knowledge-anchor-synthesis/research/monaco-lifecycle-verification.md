# 只读差异视图卸载复核

2026-09-15；本次只修改 shared/MonacoDiffViewer.tsx 的 `renderGutterMenu: false`，不改模型释放时序或屏蔽错误。

真实浏览器加载当前共享 Diff/Text 组件，连续 12 轮“打开差异 → 切换稿件 → 编辑正文 → 关闭”。修复前发生 24 次 `AbstractContextKeyService has been disposed`，堆栈落在 Monaco gutterFeature 的延迟菜单更新；修复后相同流程零 error/warning，关闭后 registry 模型数为 0。

- 修复前 console：`.playwright-cli/console-2026-09-15T12-23-27-929Z.log`。
- 修复后冷启动 console：`.playwright-cli/console-2026-09-15T12-24-59-363Z.log`，仅 React DevTools INFO。
- 390×844 下真实键盘修改“人工批注：保留 Redis 的适用条件。”，关闭重开后 DOM 仍呈现原文；文档宽度为 390，最后模型数为 0。
- 临时 QA 页面仅组合实际共享组件和本地状态，未替代生产审批/持久化验收；页面、Vite 服务及浏览器会话已清理。

复用依据：本地 monaco-editor 0.56.0 的 `gutterFeature.js`、`diffEditorWidget.js` 与 @monaco-editor/react 4.7.0 wrapper；官方同类问题 https://github.com/microsoft/monaco-editor/issues/4581。只读对照不需要该编辑操作菜单。

先前完整人工流程还记录过一次 `no diff result available`，本次冷启动和反复切换均未复现。不能凭这一结果声称已单独定位该错误；下一次实际重新合并页面流程继续观察，不推测性修改清理时序。
