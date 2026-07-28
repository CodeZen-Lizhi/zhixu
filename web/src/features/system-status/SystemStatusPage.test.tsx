import { fireEvent, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithAppProviders } from "../../test/render";
import { SystemStatusPage } from "./SystemStatusPage";

const learningCapabilities = {
  review: { status: "ready" },
  memory: { status: "ready" },
  interview: { status: "ready" },
};

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

describe("SystemStatusPage", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn());
  });

  it("先展示 loading，再展示真实 ready 状态", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        status: "ready",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: { status: "ready" },
        rag: { status: "disabled" },
        collections: { status: "ready" }, knowledge_health: { status: "ready" }, knowledge_timeline: { status: "ready" }, ...learningCapabilities,
        auth: { status: "disabled" },
        request_id: "request-ready",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(screen.getByText("读取系统真实状态")).toBeInTheDocument();
    expect(await screen.findByText("所有基础依赖可用")).toBeInTheDocument();
    expect(screen.getByText("0.1.0")).toBeInTheDocument();
    expect(screen.getByText("request-ready")).toBeInTheDocument();
  });

  it("数据库不可用时显示 degraded 和重试入口", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        status: "degraded",
        version: "0.1.0",
        database: { status: "unavailable", message: "数据库连接失败" },
        graph: { status: "unavailable", reason: "graph_dependencies_unavailable" },
        semantic_links: { status: "unavailable", reason: "semantic_link_dependencies_unavailable" },
        rag: { status: "unavailable", reason: "rag_dependencies_unavailable" },
        collections: { status: "unavailable" }, knowledge_health: { status: "unavailable" }, knowledge_timeline: { status: "unavailable" }, ...learningCapabilities,
        auth: { status: "ready" },
        request_id: "request-degraded",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("API 可用，但数据库不可用")).toBeInTheDocument();
    expect(screen.getByText("数据库连接失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新检查" })).toBeEnabled();
  });

  it("Graph 依赖不可用时显示独立降级状态", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        status: "degraded",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "unavailable", reason: "graph_dependencies_unavailable" },
        semantic_links: { status: "ready" },
        rag: { status: "disabled" },
        collections: { status: "ready" }, knowledge_health: { status: "ready" }, knowledge_timeline: { status: "ready" }, ...learningCapabilities,
        auth: { status: "ready" },
        request_id: "request-graph",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("Graph 查询暂不可用")).toBeInTheDocument();
    expect(screen.getByText("Graph", { selector: "dt" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新检查" })).toBeEnabled();
  });

  it("语义候选依赖不可用时保持 Graph 可用并显示独立降级状态", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        status: "degraded",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: { status: "unavailable", reason: "semantic_link_dependencies_unavailable" },
        rag: { status: "disabled" },
        collections: { status: "ready" }, knowledge_health: { status: "ready" }, knowledge_timeline: { status: "ready" }, ...learningCapabilities,
        auth: { status: "ready" },
        request_id: "request-semantic-links",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("语义候选能力暂不可用")).toBeInTheDocument();
    expect(screen.getByText("正式 Graph 查询仍可用，但候选扫描与审阅暂不可用，请检查语义候选依赖。")).toBeInTheDocument();
    expect(screen.getByText("语义候选", { selector: "dt" })).toBeInTheDocument();
  });

  it("知识时间线依赖不可用时显示独立降级状态", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        status: "degraded",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: { status: "ready" },
        rag: { status: "disabled" },
        collections: { status: "ready" },
        knowledge_health: { status: "ready" },
        knowledge_timeline: { status: "unavailable" },
        ...learningCapabilities,
        auth: { status: "ready" },
        request_id: "request-timeline-degraded",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("知识时间线能力暂不可用")).toBeInTheDocument();
    expect(screen.getByText("Knowledge Timeline 与 Impact Analysis 暂不可用，请检查其投影依赖。")).toBeInTheDocument();
    expect(screen.getByText("知识时间线", { selector: "dt" })).toBeInTheDocument();
  });

  it("认证依赖不可用时显示 fail-closed 状态", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        status: "degraded",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: { status: "ready" },
        rag: { status: "disabled" },
        collections: { status: "ready" },
        knowledge_health: { status: "ready" },
        knowledge_timeline: { status: "ready" },
        ...learningCapabilities,
        auth: { status: "unavailable", reason: "auth_dependencies_unavailable" },
        request_id: "request-auth-degraded",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("认证依赖暂不可用")).toBeInTheDocument();
    expect(screen.getByText("认证边界无法初始化，业务 API 已 fail closed；请检查认证数据库与运行配置。")).toBeInTheDocument();
    expect(screen.getByText("不可用", { selector: "dd" })).toBeInTheDocument();
  });

  it("学习能力不可用时展示对应的状态明细", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        status: "degraded",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: { status: "ready" },
        rag: { status: "disabled" },
        collections: { status: "ready" },
        knowledge_health: { status: "ready" },
        knowledge_timeline: { status: "ready" },
        review: { status: "unavailable" },
        memory: { status: "ready" },
        interview: { status: "ready" },
        auth: { status: "ready" },
        request_id: "request-review-unavailable",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("Review 能力暂不可用")).toBeInTheDocument();
    expect(screen.getByText("Review", { selector: "dt" })).toBeInTheDocument();
    expect(screen.getByText("Memory", { selector: "dt" })).toBeInTheDocument();
    expect(screen.getByText("Interview", { selector: "dt" })).toBeInTheDocument();
  });

  it("请求失败后允许用户重试并恢复", async () => {
    vi.mocked(fetch)
      .mockRejectedValueOnce(new TypeError("network down"))
      .mockResolvedValueOnce(
        jsonResponse({
          status: "ready",
          version: "0.1.1",
          database: { status: "ready" },
          graph: { status: "ready" },
          semantic_links: { status: "ready" },
          rag: { status: "ready" },
          collections: { status: "ready" }, knowledge_health: { status: "ready" }, knowledge_timeline: { status: "ready" }, ...learningCapabilities,
          auth: { status: "ready" },
          request_id: "request-retry",
        }),
      );

    renderWithAppProviders(<SystemStatusPage />);

    const retryButton = await screen.findByRole("button", { name: "重新检查" });
    expect(screen.getByText("NETWORK_ERROR", { exact: false })).toBeInTheDocument();

    fireEvent.click(retryButton);

    expect(await screen.findByText("所有基础依赖可用")).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});
