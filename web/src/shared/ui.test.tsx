import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

import {
  DropdownMenu,
  DropdownMenuItem,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
  Tooltip,
} from "./ui";

beforeAll(() => {
  vi.stubGlobal("ResizeObserver", class {
    observe() { /* test shim */ }
    unobserve() { /* test shim */ }
    disconnect() { /* test shim */ }
  });
  Object.defineProperties(HTMLElement.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => undefined },
    releasePointerCapture: { configurable: true, value: () => undefined },
    scrollIntoView: { configurable: true, value: () => undefined },
  });
});

afterAll(() => vi.unstubAllGlobals());

describe("shadcn open-code UI primitives", () => {
  it("Tabs exposes an accessible tablist and switches panels", async () => {
    render(<Tabs defaultValue="workspace"><TabsList aria-label="设置分组"><TabsTrigger value="workspace">Workspace</TabsTrigger><TabsTrigger value="runtime">运行依赖</TabsTrigger></TabsList><TabsContent value="workspace">Workspace 内容</TabsContent><TabsContent value="runtime">运行内容</TabsContent></Tabs>);

    expect(screen.getByRole("tablist", { name: "设置分组" })).toBeInTheDocument();
    fireEvent.mouseDown(screen.getByRole("tab", { name: "运行依赖" }), { button: 0, ctrlKey: false });
    await waitFor(() => expect(screen.getByRole("tabpanel")).toHaveTextContent("运行内容"));
  });

  it("Tooltip is available from keyboard focus", async () => {
    render(<Tooltip content="连接状态说明"><button type="button">同步状态</button></Tooltip>);

    fireEvent.focus(screen.getByRole("button", { name: "同步状态" }));
    expect(await screen.findByRole("tooltip")).toHaveTextContent("连接状态说明");
  });

  it("Dropdown menu exposes menu items and restores trigger focus on Escape", async () => {
    render(<DropdownMenu label="快捷入口" trigger={<button type="button">打开菜单</button>}><DropdownMenuItem>Workspace</DropdownMenuItem><DropdownMenuItem>Settings</DropdownMenuItem></DropdownMenu>);
    const trigger = screen.getByRole("button", { name: "打开菜单" });
    fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false });

    expect(await screen.findByRole("menu")).toHaveAttribute("aria-label", "快捷入口");
    expect(screen.getAllByRole("menuitem")).toHaveLength(2);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    await waitFor(() => expect(trigger).toHaveFocus());
  });
});
