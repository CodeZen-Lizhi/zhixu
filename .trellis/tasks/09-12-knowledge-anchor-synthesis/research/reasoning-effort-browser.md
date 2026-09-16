# 思考强度真实页面核验

2026-09-16，临时 Go overlay `/tmp/zhixu-reasoning-browser-0916/overlay.json` 运行 `TestReasoningSettingsBrowser`，退出0，包耗时383.428s，日志 `/tmp/zhixu-reasoning-browser-0916/test.log`。

- 隔离 Testcontainers PostgreSQL 正式迁移到129，真实 GORMRepository、Audit、Service、Validator、HTTP，实际 React ModelSettingsPanel + generated client。临时 Vite 位于5218，无用户库或用户模型配置写入。
- 浏览器从保存的模型默认配置开始，选择高→仅保存→完整刷新仍高，再选择最高→保存→完整刷新仍最高，再选择模型默认→保存→完整刷新仍默认。每次检查实际PUT200、desired值与revision，以及active没有提前变化。
- Go最终独立SQL检查不可变revision1–4分别为 `""`, `high`, `max`, `""`，desired=4、active=0；证明保存和后续读取一致，未把仅保存标为生效。
- 390×844宽度检查无横向溢出，1440×900查看桌面；已目视检查手机强度控件及说明。截图 `output/playwright/reasoning-effort-mobile.png`、`output/playwright/reasoning-effort-desktop.png`。
- 关键快照在临时目录：snapshot-high-verified.txt、snapshot-max-mobile.txt、snapshot-default-verified.txt。首次临时页面有 favicon 404、React DevTools提示和已有password autocomplete建议；最后刷新后的 console error 查询为0 errors/0 warnings，没有业务接口或React运行异常。

边界：挂载真实设置面板，但未覆盖全AppShell登录；使用handler既有开发模式，不构成Session/CSRF验收。仅保存流程实测，不构成真实API/Worker双进程热激活或外部模型质量结论。实际参数发送和managed runtime绑定由后端定向检查分别证明。最初在代码热更新期间的试填因同时打开的空向量草稿被前端校验拒绝，未产生保存；完整刷新后只编辑Chat完成上述4版本流程。
