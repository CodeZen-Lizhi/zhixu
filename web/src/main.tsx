import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./app/App";
import { createQueryClient } from "./app/query-client";
import { reportCaughtRouteError } from "./routes/RouteContentBoundary";
import "./styles.css";

const rootElement = document.getElementById("root");
if (rootElement === null) {
  throw new Error("缺少应用挂载节点 #root");
}

createRoot(rootElement, { onCaughtError: reportCaughtRouteError }).render(
  <StrictMode>
    <QueryClientProvider client={createQueryClient()}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
);
