import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useRef, useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  CaptureApiError,
  type Capture,
  type CaptureCommandResult,
  type CaptureCreateInput,
} from "../../api/captures";

const captureApi = vi.hoisted(() => ({
  createCapture: vi.fn<(input: CaptureCreateInput) => Promise<CaptureCommandResult>>(),
}));

vi.mock("../../api/captures", async (importOriginal) => {
  // eslint-disable-next-line @typescript-eslint/consistent-type-imports
  const actual = await importOriginal<typeof import("../../api/captures")>();
  return { ...actual, createCapture: captureApi.createCapture };
});

import { QuickCaptureDialog } from "./QuickCaptureDialog";

const workspaceId = "95000000-0000-4000-8000-000000000001";
const captureId = "95000000-0000-4000-8000-000000000003";
const capture: Capture = {
  id: captureId,
  workspaceId,
  kind: "TEXT",
  displayName: "Java AI",
  sourceId: "95000000-0000-4000-8000-000000000004",
  latestSourceVersionId: "95000000-0000-4000-8000-000000000005",
  status: "SOURCE_SAVED",
  fetchStatus: "NOT_APPLICABLE",
  ingestionStatus: "PENDING",
  indexStatus: "PENDING",
  profileStatus: "PENDING",
  retryable: false,
  version: 1,
  capturedAt: "2026-08-02T10:00:00Z",
  updatedAt: "2026-08-02T10:00:00Z",
  detailHref: `/api/v1/workspaces/${workspaceId}/captures/${captureId}`,
  profileHref: `/api/v1/workspaces/${workspaceId}/source-versions/95000000-0000-4000-8000-000000000005/knowledge-profile`,
};

const renderDialog = () => {
  const onCaptured = vi.fn();
  const onOpenChange = vi.fn();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const Host = () => {
    const [open, setOpen] = useState(true);
    const restoreFocusRef = useRef<HTMLButtonElement>(null);
    return <><button ref={restoreFocusRef} type="button">快速记录入口</button><QuickCaptureDialog
      open={open}
      onOpenChange={(nextOpen) => {
        onOpenChange(nextOpen);
        setOpen(nextOpen);
      }}
      onCaptured={onCaptured}
      restoreFocusRef={restoreFocusRef}
      workspaceId={workspaceId}
    /></>;
  };
  render(<QueryClientProvider client={queryClient}><Host /></QueryClientProvider>);
  return { onCaptured, onOpenChange, queryClient };
};

beforeEach(() => {
  captureApi.createCapture.mockReset();
  vi.spyOn(crypto, "randomUUID").mockReturnValue("95000000-0000-4000-8000-0000000000aa");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("QuickCaptureDialog", () => {
  it("submits exact text bytes and closes only after the server receipt", async () => {
    captureApi.createCapture.mockResolvedValue({ capture, replayed: false });
    const { onCaptured, onOpenChange } = renderDialog();
    const dialog = await screen.findByRole("dialog", { name: "快速记录" });

    fireEvent.change(within(dialog).getByRole("textbox", { name: "临时名称" }), { target: { value: " Java AI " } });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "记录内容" }), { target: { value: "  保留输入两侧空格。  " } });
    fireEvent.click(within(dialog).getByRole("button", { name: "存入收件箱" }));

    await waitFor(() => expect(captureApi.createCapture).toHaveBeenCalledTimes(1));
    expect(captureApi.createCapture.mock.calls[0]?.[0]).toMatchObject({
      workspaceId,
      idempotencyKey: "capture-95000000-0000-4000-8000-0000000000aa",
      kind: "TEXT",
      displayName: "Java AI",
      text: "  保留输入两侧空格。  ",
    });
    await waitFor(() => expect(onCaptured).toHaveBeenCalledWith(capture));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("retries response loss with the original variables and Idempotency-Key", async () => {
    captureApi.createCapture
      .mockRejectedValueOnce(new CaptureApiError("NETWORK_ERROR", "NETWORK_ERROR", "响应丢失", { retryable: true }))
      .mockResolvedValueOnce({ capture, replayed: true });
    renderDialog();
    const dialog = await screen.findByRole("dialog", { name: "快速记录" });

    fireEvent.change(within(dialog).getByRole("textbox", { name: "记录内容" }), { target: { value: "Java AI retry" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "存入收件箱" }));
    const retryButton = await within(dialog).findByRole("button", { name: "重试原请求" });
    const originalInput = captureApi.createCapture.mock.calls[0]?.[0];

    fireEvent.click(retryButton);
    await waitFor(() => expect(captureApi.createCapture).toHaveBeenCalledTimes(2));
    expect(captureApi.createCapture.mock.calls[1]?.[0]).toBe(originalInput);
    expect(captureApi.createCapture.mock.calls[1]?.[0]).toMatchObject({
      idempotencyKey: "capture-95000000-0000-4000-8000-0000000000aa",
    });
  });

  it("requires confirmation before discarding a dirty memory-only draft", async () => {
    renderDialog();
    const dialog = await screen.findByRole("dialog", { name: "快速记录" });
    fireEvent.change(within(dialog).getByRole("textbox", { name: "记录内容" }), { target: { value: "未保存草稿" } });

    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(within(dialog).getByText("放弃这条草稿？")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "快速记录" })).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "放弃" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "快速记录" })).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "快速记录入口" })).toHaveFocus();
  });

  it("accepts one pasted image and switches to the image mode", async () => {
    renderDialog();
    const form = await screen.findByRole("form", { name: "快速记录表单" });
    const image = new File(["image"], "diagram.png", { type: "image/png" });

    fireEvent.paste(form, {
      clipboardData: {
        files: [image],
        items: [],
      },
    });

    expect(screen.getByRole("tab", { name: "图片" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("diagram.png")).toBeInTheDocument();
  });

  it("accepts one dropped document and rejects a multi-file drop", async () => {
    renderDialog();
    const dialog = await screen.findByRole("dialog", { name: "快速记录" });
    fireEvent.mouseDown(within(dialog).getByRole("tab", { name: "文件" }), { button: 0, ctrlKey: false });
    const dropzone = await within(dialog).findByRole("button", { name: /选择或拖入文件/ });
    const markdown = new File(["# Note"], "note.md", { type: "text/markdown" });

    fireEvent.drop(dropzone, { dataTransfer: { files: [markdown] } });
    expect(within(dialog).getByText("note.md")).toBeInTheDocument();

    const textFile = new File(["Note"], "note.txt", { type: "text/plain" });
    fireEvent.drop(dropzone, { dataTransfer: { files: [markdown, textFile] } });
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("每次只能记录一个文件");
  });

  it("supports keyboard submit, exposes pending state, and restores focus after success", async () => {
    let resolveRequest: ((value: CaptureCommandResult) => void) | undefined;
    captureApi.createCapture.mockReturnValue(new Promise((resolve) => { resolveRequest = resolve; }));
    renderDialog();
    const dialog = await screen.findByRole("dialog", { name: "快速记录" });
    const textarea = within(dialog).getByRole("textbox", { name: "记录内容" });
    await waitFor(() => expect(textarea).toHaveFocus());
    fireEvent.change(textarea, { target: { value: "keyboard capture" } });

    fireEvent.keyDown(within(dialog).getByRole("form", { name: "快速记录表单" }), {
      key: "Enter",
      ctrlKey: true,
    });

    expect(await within(dialog).findByRole("status")).toHaveTextContent("正在可靠保存");
    expect(textarea).toBeDisabled();
    resolveRequest?.({ capture, replayed: false });
    await act(async () => { await Promise.resolve(); });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "快速记录" })).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "快速记录入口" })).toHaveFocus();
  });
});
