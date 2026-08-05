import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { AuthoringApiError, type UpdateWorkingDraftInput, type WorkingDraft } from "../../api/authoring";

const workspaceId = "d1000000-0000-4000-8000-000000000001";
const draftId = "d1000000-0000-4000-8000-000000000002";
const documentId = "d1000000-0000-4000-8000-000000000003";
const revisionId = "d1000000-0000-4000-8000-000000000004";
const proposalId = "d1000000-0000-4000-8000-000000000005";
const proposalRevisionId = "d1000000-0000-4000-8000-000000000006";

interface MutationMock {
  mutate: ReturnType<typeof vi.fn>;
  mutateAsync: ReturnType<typeof vi.fn>;
  isPending: boolean;
  isError: boolean;
  error: unknown;
  data: unknown;
}

interface HookMocks {
  workspaceId: string;
  draft: WorkingDraft | undefined;
  draftPending: boolean;
  draftError: unknown;
  documentData: Record<string, unknown> | undefined;
  documentPending: boolean;
  documentError: unknown;
  refetchDraft: ReturnType<typeof vi.fn>;
  refetchDocument: ReturnType<typeof vi.fn>;
  create: MutationMock;
  update: MutationMock;
  freeze: MutationMock;
  publish: MutationMock;
}

const hooks: HookMocks = vi.hoisted(() => {
  const mutation = () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, isError: false, error: undefined, data: undefined });
  return {
    workspaceId: "d1000000-0000-4000-8000-000000000001",
    draft: undefined,
    draftPending: false,
    draftError: undefined,
    documentData: undefined,
    documentPending: false,
    documentError: undefined,
    refetchDraft: vi.fn(),
    refetchDocument: vi.fn(),
    create: mutation(),
    update: mutation(),
    freeze: mutation(),
    publish: mutation(),
  };
});

vi.mock("../../app/active-workspace", () => ({
  getActiveWorkspaceId: () => hooks.workspaceId,
  useActiveWorkspaceId: () => hooks.workspaceId,
}));
vi.mock("./queries", () => ({
  useCreateWorkingDraft: () => hooks.create,
  useWorkingDraft: () => ({
    data: hooks.draft,
    isPending: hooks.draftPending,
    isError: hooks.draftError !== undefined,
    error: hooks.draftError,
    refetch: hooks.refetchDraft,
  }),
  useDocumentDraft: () => ({
    data: hooks.documentData,
    isPending: hooks.documentPending,
    isError: hooks.documentError !== undefined,
    error: hooks.documentError,
    refetch: hooks.refetchDocument,
  }),
  useUpdateWorkingDraft: () => hooks.update,
  useFreezeWorkingDraft: () => hooks.freeze,
  usePublishArticleRevision: () => hooks.publish,
}));
vi.mock("./MonacoMarkdownEditor", () => ({
  MonacoMarkdownEditor: ({ value, onChange, disabled }: { value: string; onChange: (value: string) => void; disabled: boolean }) => <textarea aria-label="Markdown 正文" value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)} />,
}));
vi.mock("../../shared/monaco-runtime", () => ({
  markdownModelPath: (workspace: string, draft: string) => `inmemory://zhixu/workspaces/${workspace}/working-drafts/${draft}.md`,
}));

import { NewDocumentPage } from "./NewDocumentPage";

const workingDraft = (version: number, changes: Partial<WorkingDraft> = {}): WorkingDraft => ({
  id: draftId,
  workspaceId,
  documentId: null,
  title: "Java AI",
  targetPath: "notes/java-ai.md",
  body: "# Java AI\n",
  status: "EDITING",
  version,
  createdAt: "2026-08-03T08:00:00Z",
  updatedAt: `2026-08-03T08:0${String(version)}:00Z`,
  ...changes,
});

const frozenResult = {
  workingDraft: workingDraft(2, { documentId }),
  document: {
    id: documentId,
    workspaceId,
    title: "Java AI",
    canonicalPath: "notes/java-ai.md",
    lifecycleStatus: "DRAFT" as const,
    currentPublishedRevisionId: null,
    version: 1,
    createdAt: "2026-08-03T08:02:00Z",
    updatedAt: "2026-08-03T08:02:00Z",
  },
  articleRevision: {
    id: revisionId,
    workspaceId,
    documentId,
    sourceVersionId: null,
    parentRevisionId: null,
    revisionNo: 1,
    content: "# Java AI\n",
    contentHash: "a".repeat(64),
    status: "DRAFT" as const,
    optimizationMode: "NONE",
    gitCommit: null,
    createdByType: "USER",
    createdAt: "2026-08-03T08:02:00Z",
  },
  replayed: false,
};

const publication = {
  id: "d1000000-0000-4000-8000-000000000007",
  workspaceId,
  documentId,
  articleRevisionId: revisionId,
  proposalId,
  proposalRevisionId,
  targetPath: "notes/java-ai.md",
  contentHash: "a".repeat(64),
  status: "PENDING" as const,
  gitCommit: null,
  errorCode: null,
  version: 1,
  createdAt: "2026-08-03T08:03:00Z",
  updatedAt: "2026-08-03T08:03:00Z",
  publishedAt: null,
  proposalHref: `/proposals/${proposalId}`,
};

const LocationProbe = () => {
  const location = useLocation();
  const navigate = useNavigate();
  return <><output data-testid="location">{location.pathname}{location.search}</output><button type="button" onClick={() => { void navigate("/authoring/new"); }}>测试新建空白文章</button></>;
};

const renderPage = (entry = `/authoring/new?draft=${draftId}`) => render(
  <MemoryRouter initialEntries={[entry]}>
    <LocationProbe />
    <Routes>
      <Route path="/authoring/new" element={<NewDocumentPage />} />
      <Route path="/proposals/:proposalId" element={<p>Proposal detail</p>} />
    </Routes>
  </MemoryRouter>,
);

const runInitialFocus = (): void => {
  void act(() => vi.advanceTimersByTime(0));
};

beforeEach(() => {
  vi.useFakeTimers();
  hooks.workspaceId = workspaceId;
  hooks.draft = workingDraft(1);
  hooks.draftPending = false;
  hooks.draftError = undefined;
  hooks.documentData = undefined;
  hooks.documentPending = false;
  hooks.documentError = undefined;
  hooks.refetchDraft.mockReset();
  hooks.refetchDraft.mockResolvedValue({ data: hooks.draft });
  hooks.refetchDocument.mockReset();
  hooks.refetchDocument.mockResolvedValue({ data: hooks.documentData });
  for (const mutation of [hooks.create, hooks.update, hooks.freeze, hooks.publish]) {
    mutation.mutate.mockReset();
    mutation.mutateAsync.mockReset();
    mutation.mutateAsync.mockReturnValue(new Promise(() => undefined));
    mutation.isPending = false;
    mutation.isError = false;
    mutation.error = undefined;
    mutation.data = undefined;
  }
});

afterEach(() => {
  vi.useRealTimers();
});

describe("NewDocumentPage", () => {
  it("creates a server draft immediately and replaces the URL with its identity", async () => {
    hooks.create.mutateAsync.mockResolvedValueOnce({ workingDraft: workingDraft(1), replayed: false });
    renderPage("/authoring/new");

    expect(hooks.create.mutateAsync).toHaveBeenCalledOnce();
    const [input] = hooks.create.mutateAsync.mock.calls[0] as [Record<string, unknown>];
    expect(input).toMatchObject({ workspaceId });
    expect(input.idempotencyKey).toMatch(/^working-draft-create-/);
    await act(async () => Promise.resolve());

    expect(screen.getByTestId("location")).toHaveTextContent(`/authoring/new?draft=${draftId}`);
  });

  it("debounces autosave for 700ms and sends exact CAS state", () => {
    renderPage();
    runInitialFocus();
    expect(screen.getByRole("textbox", { name: "标题" })).toHaveFocus();

    fireEvent.change(screen.getByRole("textbox", { name: "Markdown 正文" }), { target: { value: "# Java AI\n\nCAS\n" } });
    void act(() => vi.advanceTimersByTime(699));
    expect(hooks.update.mutate).not.toHaveBeenCalled();
    void act(() => vi.advanceTimersByTime(1));

    expect(hooks.update.mutate).toHaveBeenCalledOnce();
    const [input] = hooks.update.mutate.mock.calls[0] as [Record<string, unknown>];
    expect(input).toMatchObject({
      workspaceId,
      draftId,
      expectedVersion: 1,
      title: "Java AI",
      targetPath: "notes/java-ai.md",
      body: "# Java AI\n\nCAS\n",
    });
    expect(input.idempotencyKey).toMatch(/^working-draft-update-/);
  });

  it("retries an unknown autosave with the exact same command identity", () => {
    const error = new AuthoringApiError("NETWORK_ERROR", "NETWORK_ERROR", "response lost", null, true);
    hooks.update.mutate.mockImplementationOnce((_input, options: { onError: (value: unknown) => void }) => options.onError(error));
    renderPage();
    runInitialFocus();

    fireEvent.change(screen.getByRole("textbox", { name: "Markdown 正文" }), { target: { value: "# Local buffer\n" } });
    void act(() => vi.advanceTimersByTime(700));
    const [firstInput] = hooks.update.mutate.mock.calls[0] as unknown as [UpdateWorkingDraftInput];
    expect(screen.getByRole("alert")).toHaveTextContent("自动保存结果未确认");

    fireEvent.click(screen.getByRole("button", { name: /重试原请求/ }));
    expect(hooks.update.mutate).toHaveBeenCalledTimes(2);
    const secondInput = hooks.update.mutate.mock.calls[1]?.[0] as UpdateWorkingDraftInput;
    expect(secondInput).toMatchObject({
      workspaceId: firstInput.workspaceId,
      draftId: firstInput.draftId,
      expectedVersion: firstInput.expectedVersion,
      title: firstInput.title,
      targetPath: firstInput.targetPath,
      body: firstInput.body,
      idempotencyKey: firstInput.idempotencyKey,
    });
    expect(secondInput.signal).not.toBe(firstInput.signal);
  });

  it("coalesces edits made during an in-flight save into one next-version CAS", () => {
    renderPage();
    runInitialFocus();

    const editor = screen.getByRole("textbox", { name: "Markdown 正文" });
    fireEvent.change(editor, { target: { value: "# First pending body\n" } });
    void act(() => vi.advanceTimersByTime(700));
    expect(hooks.update.mutate).toHaveBeenCalledOnce();
    const [, firstOptions] = hooks.update.mutate.mock.calls[0] as [
      UpdateWorkingDraftInput,
      { onSuccess: (result: { workingDraft: WorkingDraft; replayed: boolean }) => void },
    ];

    fireEvent.change(editor, { target: { value: "# Latest local body\n" } });
    void act(() => vi.advanceTimersByTime(1400));
    expect(hooks.update.mutate).toHaveBeenCalledOnce();

    act(() => firstOptions.onSuccess({
      workingDraft: workingDraft(2, { body: "# First pending body\n" }),
      replayed: false,
    }));
    void act(() => vi.advanceTimersByTime(700));

    expect(hooks.update.mutate).toHaveBeenCalledTimes(2);
    expect(hooks.update.mutate.mock.calls[1]?.[0]).toMatchObject({
      expectedVersion: 2,
      body: "# Latest local body\n",
    });
  });

  it("does not create a draft for duplicate, empty, unknown or malformed URL identity", () => {
    for (const entry of [
      `/authoring/new?draft=${draftId}&draft=${draftId}`,
      "/authoring/new?draft=",
      "/authoring/new?source=unexpected",
      "/authoring/new?draft=d1000000-0000-0000-0000-000000000002",
    ]) {
      const view = renderPage(entry);
      expect(screen.getByRole("alert")).toHaveTextContent("草稿地址无效");
      view.unmount();
    }
    expect(hooks.create.mutateAsync).not.toHaveBeenCalled();
  });

  it("uses one stable create intent and handles its late result under React StrictMode", async () => {
    let resolveCreate: ((result: { workingDraft: WorkingDraft; replayed: boolean }) => void) | undefined;
    hooks.create.mutateAsync.mockReturnValueOnce(new Promise((resolve) => {
      resolveCreate = resolve;
    }));
    render(
      <StrictMode>
        <MemoryRouter initialEntries={["/authoring/new"]}>
          <LocationProbe />
          <Routes><Route path="/authoring/new" element={<NewDocumentPage />} /></Routes>
        </MemoryRouter>
      </StrictMode>,
    );

    expect(hooks.create.mutateAsync).toHaveBeenCalledOnce();
    expect(hooks.create.mutateAsync.mock.calls[0]?.[0]).toMatchObject({ workspaceId });
    await act(async () => {
      resolveCreate?.({ workingDraft: workingDraft(1), replayed: false });
      await Promise.resolve();
    });
    expect(screen.getByTestId("location")).toHaveTextContent(`/authoring/new?draft=${draftId}`);
  });

  it("creates a fresh intent whenever the same route returns to a blank article", async () => {
    hooks.create.mutateAsync.mockResolvedValue({ workingDraft: workingDraft(1), replayed: false });
    renderPage();
    expect(hooks.create.mutateAsync).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "测试新建空白文章" }));
    expect(hooks.create.mutateAsync).toHaveBeenCalledOnce();
    const [firstInput] = hooks.create.mutateAsync.mock.calls[0] as [Record<string, unknown>];
    await act(async () => Promise.resolve());

    fireEvent.click(screen.getByRole("button", { name: "测试新建空白文章" }));
    expect(hooks.create.mutateAsync).toHaveBeenCalledTimes(2);
    const secondInput = hooks.create.mutateAsync.mock.calls[1]?.[0] as Record<string, unknown>;
    expect(secondInput).toMatchObject({ workspaceId });
    expect(secondInput.idempotencyKey).not.toBe(firstInput.idempotencyKey);
  });

  it("does not send an old dirty buffer after the active Workspace changes", () => {
    const view = renderPage();
    runInitialFocus();
    fireEvent.change(screen.getByRole("textbox", { name: "Markdown 正文" }), { target: { value: "# Workspace A local buffer\n" } });

    hooks.workspaceId = "d1000000-0000-4000-8000-000000000009";
    view.rerender(
      <MemoryRouter initialEntries={[`/authoring/new?draft=${draftId}`]}>
        <LocationProbe />
        <Routes>
          <Route path="/authoring/new" element={<NewDocumentPage />} />
          <Route path="/proposals/:proposalId" element={<p>Proposal detail</p>} />
        </Routes>
      </MemoryRouter>,
    );
    void act(() => vi.advanceTimersByTime(700));

    expect(hooks.update.mutate).not.toHaveBeenCalled();
    expect(screen.getByText("正在恢复草稿…")).toBeInTheDocument();
  });

  it("keeps the local buffer on 409 and can continue from the refreshed server version", async () => {
    const serverDraft = workingDraft(2, { body: "# Server version\n" });
    hooks.refetchDraft.mockResolvedValue({ data: serverDraft });
    hooks.update.mutate.mockImplementationOnce((_input, options: { onError: (value: unknown) => void }) => options.onError(
      new AuthoringApiError("HTTP_ERROR", "AUTHORING_VERSION_CONFLICT", "stale", 409, false),
    ));
    renderPage();
    runInitialFocus();

    const editor = screen.getByRole("textbox", { name: "Markdown 正文" });
    fireEvent.change(editor, { target: { value: "# My local version\n" } });
    void act(() => vi.advanceTimersByTime(700));
    await act(async () => Promise.resolve());

    const conflict = screen.getByRole("alert");
    expect(conflict).toHaveTextContent("服务端草稿已经变化");
    expect(conflict).toHaveFocus();
    expect(editor).toHaveValue("# My local version\n");
    expect(hooks.update.mutate).toHaveBeenCalledOnce();

    fireEvent.click(within(conflict).getByRole("button", { name: "用本地内容继续" }));
    void act(() => vi.advanceTimersByTime(700));
    expect(hooks.update.mutate).toHaveBeenCalledTimes(2);
    expect(hooks.update.mutate.mock.calls[1]?.[0]).toMatchObject({ expectedVersion: 2, body: "# My local version\n" });
  });

  it("does not offer conflict recovery from the unchanged pre-conflict cache", async () => {
    hooks.refetchDraft.mockResolvedValue({ data: workingDraft(1) });
    hooks.update.mutate.mockImplementationOnce((_input, options: { onError: (value: unknown) => void }) => options.onError(
      new AuthoringApiError("HTTP_ERROR", "AUTHORING_VERSION_CONFLICT", "stale", 409, false),
    ));
    renderPage();
    runInitialFocus();

    fireEvent.change(screen.getByRole("textbox", { name: "Markdown 正文" }), { target: { value: "# Local remains visible\n" } });
    void act(() => vi.advanceTimersByTime(700));
    await act(async () => Promise.resolve());

    const conflict = screen.getByRole("alert");
    expect(conflict).toHaveTextContent("本地内容仍在当前编辑器中");
    expect(within(conflict).getByRole("button", { name: "重新读取服务端版本" })).toBeInTheDocument();
    expect(within(conflict).queryByRole("button", { name: "用本地内容继续" })).not.toBeInTheDocument();
  });

  it("blocks duplicate Freeze while an existing Document snapshot is unavailable and recovers the original Revision", () => {
    hooks.draft = workingDraft(2, { documentId });
    hooks.documentError = new Error("document detail offline");
    const view = renderPage();
    runInitialFocus();

    const recovery = screen.getByRole("alert");
    expect(recovery).toHaveTextContent("已保存版本暂时无法读取");
    expect(screen.getByRole("button", { name: /保存版本/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: /提交发布/ })).toBeDisabled();
    fireEvent.click(within(recovery).getByRole("button", { name: "重试读取版本" }));
    expect(hooks.refetchDocument).toHaveBeenCalledOnce();
    expect(hooks.freeze.mutate).not.toHaveBeenCalled();

    hooks.documentError = undefined;
    hooks.documentData = {
      document: frozenResult.document,
      currentRevision: frozenResult.articleRevision,
      publication: null,
    };
    view.rerender(
      <MemoryRouter initialEntries={[`/authoring/new?draft=${draftId}`]}>
        <LocationProbe />
        <Routes>
          <Route path="/authoring/new" element={<NewDocumentPage />} />
          <Route path="/proposals/:proposalId" element={<p>Proposal detail</p>} />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.queryByText("已保存版本暂时无法读取")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /保存版本/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: /提交发布/ })).toBeEnabled();
  });

  it("renders a CLOSED publication as terminal and does not republish the same Revision", () => {
    hooks.draft = workingDraft(2, { documentId });
    hooks.documentData = {
      document: frozenResult.document,
      currentRevision: frozenResult.articleRevision,
      publication: { ...publication, status: "CLOSED", errorCode: "AUTHORING_PROPOSAL_CLOSED" },
    };
    renderPage();
    runInitialFocus();

    expect(screen.getByText("已关闭")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "查看 Proposal" })).toHaveAttribute("href", `/proposals/${proposalId}`);
    expect(screen.getByRole("button", { name: /保存版本/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: /提交发布/ })).toBeDisabled();
  });

  it("publishes a newly frozen Revision without waiting for a stale CLOSED snapshot to refetch", () => {
    const nextRevisionId = "d1000000-0000-4000-8000-000000000008";
    const nextBody = "# Java AI\n\nSecond revision\n";
    hooks.draft = workingDraft(3, { documentId, body: nextBody });
    hooks.documentData = {
      document: frozenResult.document,
      currentRevision: frozenResult.articleRevision,
      publication: { ...publication, status: "CLOSED", errorCode: "AUTHORING_PROPOSAL_CLOSED" },
    };
    renderPage();
    runInitialFocus();

    expect(screen.getByText("已关闭")).toBeInTheDocument();
    const freezeButton = screen.getByRole("button", { name: /保存版本/ });
    expect(freezeButton).toBeEnabled();
    fireEvent.click(freezeButton);
    const [, freezeOptions] = hooks.freeze.mutate.mock.calls[0] as [Record<string, unknown>, { onSuccess: (result: unknown) => void }];
    act(() => freezeOptions.onSuccess({
      ...frozenResult,
      workingDraft: workingDraft(4, { documentId, body: nextBody }),
      articleRevision: {
        ...frozenResult.articleRevision,
        id: nextRevisionId,
        parentRevisionId: revisionId,
        revisionNo: 2,
        content: nextBody,
      },
    }));

    expect(screen.queryByText("已关闭")).not.toBeInTheDocument();
    const publishButton = screen.getByRole("button", { name: /提交发布/ });
    expect(publishButton).toBeEnabled();
    fireEvent.click(publishButton);
    const [publishInput] = hooks.publish.mutate.mock.calls[0] as [Record<string, unknown>];
    expect(publishInput).toMatchObject({ workspaceId, documentId, revisionId: nextRevisionId });
  });

  it("freezes explicitly, then publishes only that Revision and follows the real Proposal href", () => {
    renderPage();
    runInitialFocus();

    const freezeButton = screen.getByRole("button", { name: /保存版本/ });
    expect(freezeButton).toBeEnabled();
    expect(screen.getByRole("button", { name: /提交发布/ })).toBeDisabled();
    fireEvent.click(freezeButton);
    const [freezeInput, freezeOptions] = hooks.freeze.mutate.mock.calls[0] as [Record<string, unknown>, { onSuccess: (result: unknown) => void }];
    expect(freezeInput).toMatchObject({ workspaceId, draftId, expectedVersion: 1 });
    act(() => freezeOptions.onSuccess(frozenResult));

    const publishButton = screen.getByRole("button", { name: /提交发布/ });
    expect(publishButton).toBeEnabled();
    fireEvent.click(publishButton);
    const [publishInput, publishOptions] = hooks.publish.mutate.mock.calls[0] as [Record<string, unknown>, { onSuccess: (result: unknown) => void }];
    expect(publishInput).toMatchObject({ workspaceId, documentId, revisionId });
    act(() => publishOptions.onSuccess({ publication, replayed: false }));

    expect(screen.getByText("Proposal detail")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(`/proposals/${proposalId}`);
  });
});
