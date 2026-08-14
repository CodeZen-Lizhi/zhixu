import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { GraphApiError } from "../../api/graph";
import { GraphErrorNotice } from "./feedback";

describe("GraphErrorNotice", () => {
  it.each([
    ["GRAPH_CURSOR_STALE", "图谱结果已变化"],
    ["GRAPH_DEPENDENCY_UNAVAILABLE", "图谱服务暂不可用"],
    ["GRAPH_PROJECTION_INCONSISTENT", "图谱投影不一致"],
  ])("明确展示 %s", (errorCode, title) => {
    render(<GraphErrorNotice error={new GraphApiError({
      errorCode,
      message: "Graph query failed",
      retryable: false,
    })} />);

    expect(screen.getByRole("alert")).toHaveTextContent(title);
    expect(screen.getByRole("alert")).toHaveTextContent(errorCode);
  });
});
