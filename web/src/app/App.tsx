import { BrowserRouter } from "react-router-dom";

import { AppRoutes } from "../routes/AppRoutes";

export const App = () => (
  <BrowserRouter>
    <main className="app-shell">
      <header className="hero">
        <p className="brand-mark">知序 · ZHIXU</p>
        <h1>让知识的运行状态，一眼可知。</h1>
        <p className="hero-copy">
          本页直接读取后端状态，不使用静态数据或假成功结果。
        </p>
      </header>
      <AppRoutes />
    </main>
  </BrowserRouter>
);
