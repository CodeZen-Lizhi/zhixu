import { BrowserRouter } from "react-router-dom";

import { AppRoutes } from "../routes/AppRoutes";

export const App = () => (
  <BrowserRouter>
    <main className="app-shell">
      <AppRoutes />
    </main>
  </BrowserRouter>
);
