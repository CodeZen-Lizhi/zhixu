import { BrowserRouter } from "react-router-dom";

import { AppRoutes } from "../routes/AppRoutes";

export const App = () => (
  <BrowserRouter>
    <main className="app-shell">
      <header className="hero">
        <p className="brand-mark">知序 · ZHIXU</p>
        <h1>把本地知识，接入一条可信链路。</h1>
        <p className="hero-copy">
          创建 Workspace、检查 Git 基线，并安全扫描真实文件；所有状态均来自后端与本地事实源。
        </p>
      </header>
      <AppRoutes />
    </main>
  </BrowserRouter>
);
