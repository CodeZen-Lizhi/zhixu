import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { useScopedCursor } from "./pagination";

interface CursorHarnessProps {
  workspaceId: string;
  status: string;
}

const CursorHarness = ({ workspaceId, status }: CursorHarnessProps) => {
  const [cursor, setCursor] = useScopedCursor([workspaceId, status]);
  return <>
    <output aria-label="当前游标">{cursor || "first-page"}</output>
    <button type="button" onClick={() => setCursor("page-2")}>下一页</button>
  </>;
};

afterEach(cleanup);

describe("useScopedCursor", () => {
  it("Workspace A→B→A 后仍保持第一页，不复活 A 的旧游标", () => {
    const rendered = render(<CursorHarness workspaceId="workspace-a" status="running" />);
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    expect(screen.getByLabelText("当前游标")).toHaveTextContent("page-2");

    rendered.rerender(<CursorHarness workspaceId="workspace-b" status="running" />);
    expect(screen.getByLabelText("当前游标")).toHaveTextContent("first-page");

    rendered.rerender(<CursorHarness workspaceId="workspace-a" status="running" />);
    expect(screen.getByLabelText("当前游标")).toHaveTextContent("first-page");
  });

  it("有效筛选变化时立即丢弃旧游标", () => {
    const rendered = render(<CursorHarness workspaceId="workspace-a" status="running" />);
    fireEvent.click(screen.getByRole("button", { name: "下一页" }));

    rendered.rerender(<CursorHarness workspaceId="workspace-a" status="paused" />);

    expect(screen.getByLabelText("当前游标")).toHaveTextContent("first-page");
  });
});
