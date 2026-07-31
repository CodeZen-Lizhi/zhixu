import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { consumeControllerBootstrapTokenFromFragment } from "./api/controller";
import { App } from "./app/App";
import { createQueryClient } from "./app/query-client";
import { runtimeMode } from "./app/runtime-mode";
import "./styles.css";
import "./app/controller.css";

const rootElement = document.getElementById("root");
if (rootElement === null) {
  throw new Error("缺少应用挂载节点 #root");
}

createRoot(rootElement).render(
  <StrictMode>
    <QueryClientProvider client={createQueryClient()}>
      <App initialControllerToken={runtimeMode === "controller" ? consumeControllerBootstrapTokenFromFragment() : undefined} />
    </QueryClientProvider>
  </StrictMode>,
);
