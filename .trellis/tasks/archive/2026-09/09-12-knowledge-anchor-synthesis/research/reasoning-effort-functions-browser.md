# 按功能强度的真实页面保存验收

2026-09-16，真实 React ModelSettingsPanel + 生成客户端 + ModelSettings HTTP Handler/Service + GORM repository + 隔离 PostgreSQL（全部迁移含130）。沿用之前全局强度的临时 overlay方式，测试页/配置/后端fixture位于 `/tmp/zhixu-reasoning-functions-browser-0916`，未修改生产入口或新增测试接口。

浏览器实际保存并完整reload：

1. 初始 revision1 全局模型默认、覆盖空对象。
2. revision2 全局high；file_profile=low、main_note_synthesis=medium、manuscript_source_review显式空字符串、knowledge_qna继承。PUT200后reload，逐个读取真实select值一致。
3. revision3 全局max；file_profile改回继承；融合medium和来源复核显式模型默认保留。PUT200后reload，继承项显示“继承全局（最高）”，其他独立值不随全局改变。

后端在浏览器完成后独立SQL读取三条revision，严格断言全球强度及覆盖对象历史分别为 `{}`、`{file_profile:low,main_note_synthesis:medium,manuscript_source_review:""}`、`{main_note_synthesis:medium,manuscript_source_review:""}`。旧行不变，desired3/active0。`TestReasoningFunctionsSettingsBrowser` PASS **240.54s**，包241.352s，退出0；日志 `/tmp/zhixu-reasoning-functions-browser-0916/test.log`。

390×844移动端 DOM 无横向溢出，已查看截图；桌面1440×1000双列功能控件、右侧active摘要布局已查看。截图在 `output/playwright/reasoning-functions-mobile.png` 和 `reasoning-functions-desktop.png`。过程中仅临时入口favicon404，React开发工具及autocomplete提示，没有业务JS异常。

未覆盖完整登录AppShell、真实双进程激活及模型语义质量；保存后仍显示旧active0是预期行为。实际业务请求强度由后端独立证据验证。本轮生产用户配置只涉及单独记录的BGE desired保存，不把隔离fixture当作实际生效配置。

隔离PG已自动停止并删除，Vite5219已停止；浏览器关闭。
