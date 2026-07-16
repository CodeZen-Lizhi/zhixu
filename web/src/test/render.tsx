import { QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router-dom";

import { createQueryClient } from "../app/query-client";

export const renderWithAppProviders = (element: ReactElement) =>
  render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter>{element}</MemoryRouter>
    </QueryClientProvider>,
  );
