import { fireEvent, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithAppProviders } from "../../test/render";
import { SystemStatusPage } from "./SystemStatusPage";

const readyCapabilities = {
  review: { status: "ready" },
  memory: { status: "ready" },
  interview: { status: "ready" },
  authoring: { status: "ready" },
  capture: { status: "ready" },
  organizing: { status: "ready" },
};

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const issueByName = (name: string): HTMLElement => {
  const label = screen.getByText(name, { selector: ".status-impact-item__identity strong" });
  const issue = label.closest("article");
  if (!(issue instanceof HTMLElement)) throw new Error(`status issue is missing: ${name}`);
  return issue;
};

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
        collections: { status: "ready" }, knowledge_health: { status: "ready" }, knowledge_timeline: { status: "ready" }, ...readyCapabilities,
        auth: { status: "disabled" },
        request_id: "request-ready",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(screen.getByText("读取系统真实状态")).toBeInTheDocument();
    expect(await screen.findByText("运行正常")).toBeInTheDocument();
    expect(screen.getByText(/最近检查/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重新检查系统状态" })).toBeEnabled();
    const technicalDetails = screen.getByText("技术详情").closest("details");
    expect(technicalDetails).not.toHaveAttribute("open");
    expect(screen.getByText("15 项运行事实")).toBeInTheDocument();
    expect(screen.getByText("需要关注")).toBeInTheDocument();
    const authIssue = issueByName("认证");
    expect(within(authIssue).getByText("开发模式")).toBeInTheDocument();
    expect(within(authIssue).getByText("当前实例没有登录保护，仅适合受控开发环境。")).toBeInTheDocument();
    expect(within(authIssue).getByText("认证由运行配置明确关闭。")).toBeInTheDocument();
    expect(within(authIssue).getByRole("link", { name: "检查访问权限" })).toHaveAttribute("href", "/settings?section=access");
    const ragIssue = issueByName("RAG");
    expect(within(ragIssue).getByText("已关闭（可选）")).toBeInTheDocument();
    expect(within(ragIssue).getByText("问答增强入口不会运行，其他知识能力不受影响。")).toBeInTheDocument();
    expect(within(ragIssue).getByRole("link", { name: "配置模型与检索" })).toHaveAttribute("href", "/settings?section=models");
    expect(screen.getByText("其余服务正常")).toBeInTheDocument();
    expect(screen.getByText("0.1.0")).toBeInTheDocument();
    expect(screen.getByText("request-ready")).toBeInTheDocument();
  });

  it("紧凑模式只展示关键运行事实并明确 RAG 是可选能力", async () => {
    vi.mocked(fetch).mockResolvedValueOnce(
      jsonResponse({
        status: "ready",
        version: "0.1.0",
        database: { status: "ready" },
        graph: { status: "ready" },
        semantic_links: { status: "ready" },
        rag: { status: "disabled" },
        collections: { status: "ready" },
        knowledge_health: { status: "ready" },
        knowledge_timeline: { status: "ready" },
        ...readyCapabilities,
        auth: { status: "disabled" },
        request_id: "request-compact",
      }),
    );

    renderWithAppProviders(<SystemStatusPage display="compact" />);

    expect(screen.getByText("正在读取系统状态…")).toBeInTheDocument();
    expect(await screen.findByText("运行正常")).toBeInTheDocument();
    expect(screen.queryByText("运行摘要")).not.toBeInTheDocument();
    expect(screen.queryByText(/ZHIXU 已连接数据库/)).not.toBeInTheDocument();
    expect(screen.getByText("可选能力已关闭")).toBeInTheDocument();
    expect(screen.getByText("RAG", { selector: "dt" })).toBeInTheDocument();
    expect(screen.getByText("知识图谱", { selector: "dt" })).toBeInTheDocument();
    expect(screen.queryByText("request-compact")).not.toBeInTheDocument();
    expect(screen.queryByText("认证", { selector: "dt" })).not.toBeInTheDocument();
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
        collections: { status: "unavailable" }, knowledge_health: { status: "unavailable" }, knowledge_timeline: { status: "unavailable" }, ...readyCapabilities,
        auth: { status: "ready" },
        request_id: "request-degraded",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("当前无法继续")).toBeInTheDocument();
    const databaseIssue = issueByName("数据库");
    expect(within(databaseIssue).getByText("依赖持久化数据的工作流暂不可用。")).toBeInTheDocument();
    expect(within(databaseIssue).getByText("数据库连接失败")).toBeInTheDocument();
    expect(within(databaseIssue).getByRole("button", { name: "重新检查数据库" })).toBeEnabled();
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
        collections: { status: "ready" }, knowledge_health: { status: "ready" }, knowledge_timeline: { status: "ready" }, ...readyCapabilities,
        auth: { status: "ready" },
        request_id: "request-graph",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("部分功能受影响")).toBeInTheDocument();
    const graphIssue = issueByName("知识图谱");
    expect(within(graphIssue).getByText("知识图谱查询与依赖它的探索入口暂不可用。")).toBeInTheDocument();
    expect(within(graphIssue).getByText("知识图谱查询依赖未就绪。")).toBeInTheDocument();
    expect(within(graphIssue).getByRole("button", { name: "重新检查知识图谱" })).toBeEnabled();
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
        collections: { status: "ready" }, knowledge_health: { status: "ready" }, knowledge_timeline: { status: "ready" }, ...readyCapabilities,
        auth: { status: "ready" },
        request_id: "request-semantic-links",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("部分功能受影响")).toBeInTheDocument();
    const semanticIssue = issueByName("语义候选");
    expect(within(semanticIssue).getByText("正式知识图谱仍可用，但候选扫描与审阅暂不可用。")).toBeInTheDocument();
    expect(within(semanticIssue).getByText("语义候选依赖未就绪。")).toBeInTheDocument();
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
        ...readyCapabilities,
        auth: { status: "ready" },
        request_id: "request-timeline-degraded",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("部分功能受影响")).toBeInTheDocument();
    const timelineIssue = issueByName("知识时间线");
    expect(within(timelineIssue).getByText("知识时间线与影响分析暂不可用。")).toBeInTheDocument();
    expect(within(timelineIssue).getByText("时间线投影依赖未就绪。")).toBeInTheDocument();
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
        ...readyCapabilities,
        auth: { status: "unavailable", reason: "auth_dependencies_unavailable" },
        request_id: "request-auth-degraded",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("当前无法继续")).toBeInTheDocument();
    const authIssue = issueByName("认证");
    expect(within(authIssue).getByText("受保护的业务 API 将拒绝访问。")).toBeInTheDocument();
    expect(within(authIssue).getByText("认证依赖未能初始化。")).toBeInTheDocument();
    expect(within(authIssue).getByText("不可用")).toBeInTheDocument();
    expect(within(authIssue).getByRole("button", { name: "重新检查认证" })).toBeEnabled();
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
        authoring: { status: "ready" },
        capture: { status: "ready" },
        organizing: { status: "ready" },
        auth: { status: "ready" },
        request_id: "request-review-unavailable",
      }),
    );

    renderWithAppProviders(<SystemStatusPage />);

    expect(await screen.findByText("部分功能受影响")).toBeInTheDocument();
    const reviewIssue = issueByName("复习");
    expect(within(reviewIssue).getByText("主动回忆与复习调度暂不可用。")).toBeInTheDocument();
    expect(within(reviewIssue).getByText("复习服务依赖未就绪。")).toBeInTheDocument();
    expect(screen.getByText("记忆", { selector: "dt" })).toBeInTheDocument();
    expect(screen.getByText("访谈", { selector: "dt" })).toBeInTheDocument();
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
          collections: { status: "ready" }, knowledge_health: { status: "ready" }, knowledge_timeline: { status: "ready" }, ...readyCapabilities,
          auth: { status: "ready" },
          request_id: "request-retry",
        }),
      );

    renderWithAppProviders(<SystemStatusPage />);

    const retryButton = await screen.findByRole("button", { name: "重新检查" });
    expect(screen.getByText("NETWORK_ERROR", { exact: false })).toBeInTheDocument();

    fireEvent.click(retryButton);

    expect(await screen.findByText("运行正常")).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});
