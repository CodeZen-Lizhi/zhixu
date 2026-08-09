import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Capture, DocumentKnowledgeProfile } from "../../api/captures";

const queryMocks = vi.hoisted(() => ({
  useCaptures: vi.fn(),
  useCapture: vi.fn(),
  useKnowledgeProfile: vi.fn(),
  useRetryCapture: vi.fn(),
  useRetryKnowledgeProfile: vi.fn(),
}));

vi.mock("./queries", () => queryMocks);
vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => workspaceId }));
vi.mock("../source-spans", () => ({
  SourceSpanViewer: ({ label, reference }: { label: string; reference: { sourceSpanId: string; sourceVersionId: string } }) => (
    <button type="button" data-source-span-id={reference.sourceSpanId} data-source-version-id={reference.sourceVersionId}>{label}</button>
  ),
}));

import { CaptureDetailPage } from "./CaptureDetailPage";
import { CaptureInboxSection } from "./CaptureInboxSection";

const workspaceId = "95000000-0000-4000-8000-000000000001";
const captureId = "95000000-0000-4000-8000-000000000003";
const sourceId = "95000000-0000-4000-8000-000000000004";
const sourceVersionId = "95000000-0000-4000-8000-000000000005";
const profileId = "95000000-0000-4000-8000-000000000006";
const profileRevisionId = "95000000-0000-4000-8000-000000000007";
const sourceSpanId = "95000000-0000-4000-8000-00000000000b";

const capture = (changes: Partial<Capture> = {}): Capture => ({
  id: captureId,
  workspaceId,
  kind: "TEXT",
  displayName: "Java AI 知识点",
  sourceId,
  latestSourceVersionId: sourceVersionId,
  status: "READY",
  fetchStatus: "NOT_APPLICABLE",
  ingestionStatus: "READY",
  indexStatus: "READY",
  profileStatus: "READY",
  retryable: false,
  version: 3,
  capturedAt: "2026-08-02T10:00:00Z",
  updatedAt: "2026-08-02T10:01:00Z",
  detailHref: `/api/v1/workspaces/${workspaceId}/captures/${captureId}`,
  profileHref: `/api/v1/workspaces/${workspaceId}/source-versions/${sourceVersionId}/knowledge-profile`,
  ...changes,
});

const profile: DocumentKnowledgeProfile = {
  id: profileId,
  workspaceId,
  captureId,
  sourceVersionId,
  currentRevisionId: profileRevisionId,
  status: "READY",
  retryable: false,
  version: 2,
  createdAt: "2026-08-02T10:00:00Z",
  updatedAt: "2026-08-02T10:01:00Z",
  revision: {
    id: profileRevisionId,
    profileId,
    workspaceId,
    sourceVersionId,
    parseProjectionId: "95000000-0000-4000-8000-000000000008",
    indexVersionId: "95000000-0000-4000-8000-000000000009",
    modelRunId: "95000000-0000-4000-8000-00000000000a",
    modelSettingsRevision: null,
    promptVersion: "profile-v1",
    schemaVersion: "document-knowledge-profile/v1",
    summary: "这份资料说明了 Java AI 的证据约束。",
    topics: [{ label: "Java AI", aliases: ["Spring AI"], sourceSpanIds: [sourceSpanId] }],
    terms: [],
    knowledgePoints: [{ text: "模型输出必须绑定来源片段。", sourceSpanIds: [sourceSpanId] }],
    examples: [],
    contentDigest: "a".repeat(64),
    createdAt: "2026-08-02T10:01:00Z",
  },
};

const idleMutation = () => ({
  mutate: vi.fn(),
  isPending: false,
  isError: false,
  error: undefined,
  variables: undefined,
});

const renderDetail = () => render(
  <MemoryRouter initialEntries={[`/captures/${captureId}`]}>
    <Routes><Route path="/captures/:captureId" element={<CaptureDetailPage />} /></Routes>
  </MemoryRouter>,
);

beforeEach(() => {
  queryMocks.useCaptures.mockReset();
  queryMocks.useCapture.mockReset();
  queryMocks.useKnowledgeProfile.mockReset();
  queryMocks.useRetryCapture.mockReset();
  queryMocks.useRetryKnowledgeProfile.mockReset();
  queryMocks.useRetryCapture.mockReturnValue(idleMutation());
  queryMocks.useRetryKnowledgeProfile.mockReturnValue(idleMutation());
});

describe("Capture Inbox and detail views", () => {
  it("keeps a failed URL Capture visible before a Source Version exists", () => {
    const failedURL = capture({
      kind: "URL",
      displayName: "Java AI article",
      originalUrl: "https://example.com/java-ai/agents",
      status: "FETCH_FAILED",
      fetchStatus: "FAILED",
      ingestionStatus: "PENDING",
      indexStatus: "PENDING",
      profileStatus: "PENDING",
      failureStage: "FETCH",
      errorCode: "CAPTURE_FETCH_TIMEOUT",
      retryable: true,
    });
    delete failedURL.latestSourceVersionId;
    delete failedURL.profileHref;
    queryMocks.useCaptures.mockReturnValue({
      data: { workspaceId, items: [failedURL] },
      isPending: false,
      isError: false,
      isFetching: false,
      refetch: vi.fn(),
    });

    render(<MemoryRouter><CaptureInboxSection workspaceId={workspaceId} /></MemoryRouter>);

    const captureLink = screen.getByRole("link", { name: "Java AI article" });
    expect(captureLink).toHaveAttribute("href", `/captures/${captureId}`);
    expect(screen.getByText("example.com/java-ai/agents")).toBeInTheDocument();
    const captureRow = captureLink.closest("article");
    if (captureRow === null) throw new Error("capture row is missing");
    expect(within(captureRow).getByText("抓取失败")).toBeInTheDocument();
    expect(within(captureRow).getByText("CAPTURE_FETCH_TIMEOUT")).toBeInTheDocument();
  });

  it("shows image capability degradation without fabricated OCR or Profile content", () => {
    queryMocks.useCapture.mockReturnValue({
      data: capture({
        kind: "IMAGE",
        status: "READY_DEGRADED",
        ingestionStatus: "CAPABILITY_UNAVAILABLE",
        indexStatus: "CAPABILITY_UNAVAILABLE",
        profileStatus: "CAPABILITY_UNAVAILABLE",
      }),
      isPending: false,
      isError: false,
    });
    queryMocks.useKnowledgeProfile.mockReturnValue({ data: undefined, isError: false, refetch: vi.fn() });

    renderDetail();

    expect(screen.getByText("仅保存原图，没有伪造 OCR 正文")).toBeInTheDocument();
    expect(screen.getByText("当前仅保留基础资料")).toBeInTheDocument();
    expect(screen.queryByText("这份资料说明了 Java AI 的证据约束。")).not.toBeInTheDocument();
  });

  it("labels Profile output as a derived candidate and preserves Source Span bindings", () => {
    queryMocks.useCapture.mockReturnValue({ data: capture(), isPending: false, isError: false });
    queryMocks.useKnowledgeProfile.mockReturnValue({ data: profile, isError: false, refetch: vi.fn() });

    renderDetail();

    expect(screen.getByText(/不是已确认的主题、主张或关系/)).toBeInTheDocument();
    const candidate = screen.getByText("Java AI").closest("article");
    if (candidate === null) throw new Error("candidate article is missing");
    fireEvent.click(within(candidate).getByText("1 个来源片段"));
    const evidence = within(candidate).getByRole("button", { name: "证据 1" });
    expect(evidence).toHaveAttribute("data-source-span-id", sourceSpanId);
    expect(evidence).toHaveAttribute("data-source-version-id", sourceVersionId);
  });

  it("offers only the profile-specific retry when the derived projection fails", () => {
    queryMocks.useCapture.mockReturnValue({
      data: capture({
        status: "READY_DEGRADED",
        profileStatus: "FAILED",
        failureStage: "PROFILE",
        errorCode: "MODEL_TEMPORARILY_UNAVAILABLE",
        retryable: true,
      }),
      isPending: false,
      isError: false,
    });
    queryMocks.useKnowledgeProfile.mockReturnValue({
      data: {
        ...profile,
        status: "FAILED",
        errorCode: "MODEL_TEMPORARILY_UNAVAILABLE",
        retryable: true,
      },
      isPending: false,
      isError: false,
      refetch: vi.fn(),
    });

    renderDetail();

    expect(screen.getByRole("button", { name: "重试画像" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重试处理" })).not.toBeInTheDocument();
  });

  it("allows regeneration after the Profile model capability becomes available", () => {
    const retryMutation = idleMutation();
    queryMocks.useRetryKnowledgeProfile.mockReturnValue(retryMutation);
    queryMocks.useCapture.mockReturnValue({
      data: capture({ status: "READY_DEGRADED", profileStatus: "CAPABILITY_UNAVAILABLE" }),
      isPending: false,
      isError: false,
    });
    const unavailableProfile: DocumentKnowledgeProfile = {
      id: profile.id,
      workspaceId: profile.workspaceId,
      captureId: profile.captureId,
      sourceVersionId: profile.sourceVersionId,
      status: "CAPABILITY_UNAVAILABLE",
      errorCode: "CAPTURE_PROFILE_CAPABILITY_UNAVAILABLE",
      retryable: false,
      version: profile.version,
      createdAt: profile.createdAt,
      updatedAt: profile.updatedAt,
    };
    queryMocks.useKnowledgeProfile.mockReturnValue({
      data: unavailableProfile,
      isPending: false,
      isError: false,
      refetch: vi.fn(),
    });

    renderDetail();

    expect(screen.getByText("模型能力不可用")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重新生成画像" }));
    expect(retryMutation.mutate).toHaveBeenCalledWith(expect.objectContaining({
      workspaceId,
      captureId,
      sourceVersionId,
      expectedVersion: profile.version,
    }));
  });

  it("marks a stale Profile stage and reuses the Profile retry mutation to regenerate it", () => {
    const retryMutation = idleMutation();
    queryMocks.useRetryKnowledgeProfile.mockReturnValue(retryMutation);
    queryMocks.useCapture.mockReturnValue({
      data: capture({ profileStatus: "STALE" }),
      isPending: false,
      isError: false,
    });
    queryMocks.useKnowledgeProfile.mockReturnValue({
      data: { ...profile, status: "STALE", retryable: false },
      isPending: false,
      isError: false,
      refetch: vi.fn(),
    });

    renderDetail();

    expect(screen.getByText("已过期")).toBeInTheDocument();
    expect(screen.getByText("当前画像已过期")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "重新生成画像" }));
    expect(retryMutation.mutate).toHaveBeenCalledWith(expect.objectContaining({
      workspaceId,
      captureId,
      sourceVersionId,
      expectedVersion: profile.version,
    }));
  });
});
