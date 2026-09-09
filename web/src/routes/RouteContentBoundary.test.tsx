import { act, fireEvent, render, screen } from "@testing-library/react";
import { Component, lazy, Suspense, type ComponentType, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { reportCaughtRouteError, RouteContentBoundary } from "./RouteContentBoundary";

describe("RouteContentBoundary", () => {
  it("隔离渲染失败，只报告安全 code，并允许重试恢复", () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const secret = "bootstrap-token=secret-canary，正文=private-content";
    let shouldFail = true;
    const Page = () => {
      if (shouldFail) throw new Error(secret);
      return <p>页面已恢复</p>;
    };

    render(<RouteContentBoundary resetKey="page"><Page /></RouteContentBoundary>, {
      onCaughtError: reportCaughtRouteError,
    });

    expect(screen.getByRole("alert", { name: "页面暂时无法显示" })).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent(secret);
    expect(log.mock.calls).toEqual([["ROUTE_CONTENT_ERROR"]]);
    expect(screen.getByRole("button", { name: "重试页面" })).toHaveFocus();

    shouldFail = false;
    fireEvent.click(screen.getByRole("button", { name: "重试页面" }));

    expect(screen.getByText("页面已恢复")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("lazy 拒绝后保持稳定恢复入口，只有用户点击才刷新", async () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const onReload = vi.fn();
    let rejectPage: ((error: Error) => void) | undefined;
    const loadPage = vi.fn(() => new Promise<{ default: ComponentType }>((_resolve, reject) => {
      rejectPage = reject;
    }));
    const Page = lazy(loadPage);

    render(<RouteContentBoundary resetKey="page" onReload={onReload}>
      <Suspense fallback={<p>正在加载…</p>}><Page /></Suspense>
    </RouteContentBoundary>, { onCaughtError: reportCaughtRouteError });

    expect(screen.getByText("正在加载…")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    act(() => {
      rejectPage?.(new Error("Failed to fetch dynamically imported module: /assets/page.js?secret=canary"));
    });

    expect(await screen.findByRole("alert", { name: "页面暂时无法显示" })).toBeInTheDocument();
    expect(screen.queryByText("正在加载…")).not.toBeInTheDocument();
    expect(log.mock.calls).toEqual([["ROUTE_CONTENT_ERROR"]]);
    expect(document.body).not.toHaveTextContent("secret=canary");
    expect(onReload).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "重试页面" }));

    expect(screen.getByRole("alert", { name: "页面暂时无法显示" })).toBeInTheDocument();
    expect(loadPage).toHaveBeenCalledTimes(1);
    expect(onReload).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "刷新页面" }));
    expect(onReload).toHaveBeenCalledTimes(1);
  });

  it("其他错误边界仍能报告自身异常", () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const error = new Error("Other boundary failure");
    class OtherBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
      override state = { failed: false };
      static getDerivedStateFromError() { return { failed: true }; }
      override render() { return this.state.failed ? <p>局部错误</p> : this.props.children; }
    }
    const Page = (): ReactNode => { throw error; };

    render(<OtherBoundary><Page /></OtherBoundary>, { onCaughtError: reportCaughtRouteError });

    expect(screen.getByText("局部错误")).toBeInTheDocument();
    expect(log).toHaveBeenCalledExactlyOnceWith(error);
  });
});
