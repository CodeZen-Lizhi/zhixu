import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ArtifactApiError, type Artifact, type ArtifactSectionGeneration, type ArtifactSectionGenerationAcceptance, type ArtifactSectionGenerationPage, type GenerateArtifactSectionInput } from "../../api/artifacts";
import type { WorkflowDetail, WorkflowStatus } from "../../api/business";
import { ArtifactDetailPage, ArtifactsPage } from "./ArtifactsPage";

const artifact: Artifact = { id: "12000000-0000-4000-8000-000000000002", workspaceId: "12000000-0000-4000-8000-000000000001", type: "knowledge-note", title: "Gap artifact", status: "PUBLISH_PROPOSED", scopeDefinition: "scope", sourceCoverage: [{ sectionKey: "gap", status: "GAP", gaps: [{ code: "KNOWLEDGE_GAP", description: "missing" }] }], currentRevisionId: "12000000-0000-4000-8000-000000000003", version: 4, createdAt: "2026-07-26T00:00:00Z", updatedAt: "2026-07-26T00:00:00Z", revision: { id: "12000000-0000-4000-8000-000000000003", artifactId: "12000000-0000-4000-8000-000000000002", revisionNo: 4, outline: [{ key: "gap", title: "Gap" }], sections: [{ key: "gap", title: "Gap", content: "", citations: [], documentSources: [], coverage: { sectionKey: "gap", status: "GAP", gaps: [{ code: "KNOWLEDGE_GAP", description: "missing" }] } }], createdBy: "HUMAN", contentHash: "a".repeat(64), createdAt: "2026-07-26T00:00:00Z" } };
const evidenceArtifact: Artifact = { ...artifact, status: "DRAFT", sourceCoverage: [{ sectionKey: "covered", status: "COVERED", gaps: [] }, { sectionKey: "partial", status: "PARTIAL", gaps: [{ code: "MISSING_DETAIL", description: "citation is bounded" }] }], revision: { ...artifact.revision, outline: [{ key: "covered", title: "Covered" }, { key: "partial", title: "Partial" }], sections: [
  { key: "covered", title: "Covered", content: "covered body", citations: [{ sourceVersionId: "12000000-0000-4000-8000-000000000004", sourceSpanId: "12000000-0000-4000-8000-000000000005", verifiedContentHash: "a".repeat(64), excerpt: "covered citation", verified: true }], documentSources: [], coverage: { sectionKey: "covered", status: "COVERED", gaps: [] } },
  { key: "partial", title: "Partial", content: "partial body", citations: [], documentSources: [{ documentId: "12000000-0000-4000-8000-000000000006", articleRevisionId: "12000000-0000-4000-8000-000000000007", revisionNo: 2, verifiedContentHash: "a".repeat(64), verified: true }], coverage: { sectionKey: "partial", status: "PARTIAL", gaps: [{ code: "MISSING_DETAIL", description: "citation is bounded" }] } },
] } };
const generatingArtifact: Artifact = { ...artifact, status: "GENERATING", sourceCoverage: [], revision: { ...artifact.revision, sections: [] } };
const revisingArtifact: Artifact = { ...evidenceArtifact, status: "GENERATING", version: 5 };
const generationInput: GenerateArtifactSectionInput = { workspaceId: artifact.workspaceId, artifactId: artifact.id, expectedVersion: artifact.version, sectionKey: "gap", idempotencyKey: "artifact-generate-original" };
const generationRecord: ArtifactSectionGeneration = { generationId: "12000000-0000-4000-8000-000000000007", workspaceId: artifact.workspaceId, artifactId: artifact.id, sourceRevisionId: artifact.revision.id, sourceRevisionNo: artifact.revision.revisionNo, sourceArtifactVersion: artifact.version, sectionKey: "gap", workflowRunId: "12000000-0000-4000-8000-000000000008", nodeRunId: "12000000-0000-4000-8000-000000000009", status: "PENDING", version: 1, createdAt: artifact.createdAt, updatedAt: artifact.updatedAt, statusUrl: "/api/v1/workflows/12000000-0000-4000-8000-000000000008" };
const generationAcceptance: ArtifactSectionGenerationAcceptance = { ...generationRecord, replayed: false };
const generationPage = (generation?: ArtifactSectionGeneration): ArtifactSectionGenerationPage => ({ workspaceId: artifact.workspaceId, artifactId: artifact.id, items: generation === undefined ? [] : [generation] });
const workflow = (status: WorkflowStatus): WorkflowDetail => ({ id: generationRecord.workflowRunId, workspaceId: artifact.workspaceId, definitionId: "12000000-0000-4000-8000-00000000000a", status, input: {}, version: 2, createdAt: artifact.createdAt, updatedAt: artifact.updatedAt, ...(status === "succeeded" || status === "failed" || status === "cancelled" ? { completedAt: artifact.updatedAt } : {}), pauseRequested: false, cancelRequested: false });
const mutation = { mutate: vi.fn(), reset: vi.fn(), isPending: false, error: null as Error | null, data: undefined };
const generationMutation = { mutate: vi.fn(), isPending: false, isError: false, error: null as Error | null, data: undefined as ArtifactSectionGenerationAcceptance | undefined, variables: undefined as GenerateArtifactSectionInput | undefined };
const detailRefetch = vi.fn();
const generationsRefetch = vi.fn();
const workflowRefetch = vi.fn();
const mocks = vi.hoisted<{ workspace: string; list: unknown; detail: unknown; generations: unknown; workflow: unknown }>(() => ({ workspace: "12000000-0000-4000-8000-000000000001", list: undefined, detail: undefined, generations: undefined, workflow: undefined }));

vi.mock("../../app/active-workspace", () => ({ useActiveWorkspaceId: () => mocks.workspace }));
vi.mock("./queries", () => ({
  artifactGenerationPollMilliseconds: 2_000,
  artifactGenerationReceiptPollLimit: 12,
  useArtifacts: () => mocks.list,
  useArtifact: () => mocks.detail,
  useSectionGenerations: () => mocks.generations,
  usePlanArtifact: () => mutation,
  useSubmitArtifactOutline: () => mutation,
  useApproveArtifactOutline: () => mutation,
  useStartArtifactRevision: () => mutation,
  useRecordArtifactGapSection: () => mutation,
  useApproveArtifactDraft: () => mutation,
  useExportArtifactMarkdown: () => mutation,
  useCreateArtifactPublishProposal: () => mutation,
  useGenerateArtifactSection: () => generationMutation,
  useArtifactGenerationWorkflow: () => mocks.workflow,
  isArtifactGenerationWorkflowTerminal: (status: WorkflowStatus | undefined) => status === "succeeded" || status === "failed" || status === "cancelled",
}));

const renderDetail = () => render(<MemoryRouter initialEntries={[`/artifacts/${artifact.id}`]}><Routes><Route path="/artifacts/:artifactId" element={<ArtifactDetailPage />} /></Routes></MemoryRouter>);

beforeEach(() => {
  mutation.mutate.mockReset();
  mutation.reset.mockReset().mockImplementation(() => { mutation.error = null; });
  mutation.error = null;
  generationMutation.mutate.mockReset();
  generationMutation.isPending = false;
  generationMutation.isError = false;
  generationMutation.error = null;
  generationMutation.data = undefined;
  generationMutation.variables = undefined;
  detailRefetch.mockReset().mockResolvedValue({ data: generatingArtifact });
  generationsRefetch.mockReset().mockResolvedValue({ data: generationPage() });
  workflowRefetch.mockReset().mockResolvedValue({ data: undefined });
  mocks.list = { isPending: false, isError: false, data: { items: [artifact] }, refetch: vi.fn() };
  mocks.detail = { isPending: false, isError: false, data: artifact, refetch: detailRefetch };
  mocks.generations = { isPending: false, isError: false, data: generationPage(), refetch: generationsRefetch };
  mocks.workflow = { isPending: false, isError: false, data: undefined, refetch: workflowRefetch };
});

afterEach(() => vi.useRealTimers());

describe("Artifact pages", () => {
  it("shows a real loading and error state instead of a placeholder success", () => {
    mocks.list = { isPending: true, isError: false, data: undefined, refetch: vi.fn() };
    const { rerender } = render(<MemoryRouter><ArtifactsPage /></MemoryRouter>);
    expect(screen.getByText("正在读取 Artifact")).toBeInTheDocument();
    mocks.list = { isPending: false, isError: true, data: undefined, refetch: vi.fn() };
    rerender(<MemoryRouter><ArtifactsPage /></MemoryRouter>);
    expect(screen.getByRole("alert")).toHaveTextContent("Artifact 列表不可用");
  });

  it("shows GAP coverage and keeps PUBLISH_PROPOSED separate from formal knowledge", () => {
    renderDetail();
    expect(screen.getAllByText("GAP").length).toBeGreaterThan(0);
    expect(screen.getByText("已创建 Proposal；正式知识写入仍须在 Proposals 中审批与执行。")).toBeInTheDocument();
    expect(screen.getByText("此章节没有正文，缺口已显式记录。")).toBeInTheDocument();
  });

  it("renders formal Citations and document sources separately with their distinct Coverage", () => {
    mocks.detail = { isPending: false, isError: false, data: evidenceArtifact, refetch: detailRefetch };
    renderDetail();

    expect(screen.getAllByText("COVERED").length).toBeGreaterThan(0);
    expect(screen.getAllByText("PARTIAL").length).toBeGreaterThan(0);
    expect(screen.getByText("covered citation")).toBeInTheDocument();
    expect(screen.getByText("来源文档")).toBeInTheDocument();
    expect(screen.getByText(/Revision 2/)).toBeInTheDocument();
    expect(screen.getByText("citation is bounded")).toBeInTheDocument();
  });

  it("recovers an Artifact version conflict by refreshing and retrying with the new Revision version", async () => {
    const staleArtifact: Artifact = { ...artifact, status: "OUTLINE_REVIEW", version: 4 };
    const refreshedArtifact: Artifact = { ...staleArtifact, version: 5 };
    mocks.detail = { isPending: false, isError: false, data: staleArtifact, refetch: detailRefetch };
    detailRefetch.mockResolvedValueOnce({ data: staleArtifact }).mockResolvedValueOnce({ data: refreshedArtifact });
    const view = renderDetail();

    fireEvent.click(screen.getByRole("button", { name: "审批大纲" }));
    const firstAttempt = mutation.mutate.mock.calls[0]?.[0] as { expectedVersion: number; idempotencyKey: string };
    mutation.error = new ArtifactApiError("HTTP_ERROR", "ARTIFACT_VERSION_CONFLICT", "stale", false, 409);
    view.rerender(<MemoryRouter initialEntries={[`/artifacts/${artifact.id}`]}><Routes><Route path="/artifacts/:artifactId" element={<ArtifactDetailPage />} /></Routes></MemoryRouter>);

    expect(screen.getByRole("alert")).toHaveTextContent("当前 Revision 已更新");
    expect(screen.getByRole("button", { name: "审批大纲" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "刷新当前 Revision" }));
    await waitFor(() => expect(detailRefetch).toHaveBeenCalledTimes(1));
    expect(mutation.reset).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("当前 Revision 已更新");

    fireEvent.click(screen.getByRole("button", { name: "刷新当前 Revision" }));
    await waitFor(() => expect(detailRefetch).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mutation.reset).toHaveBeenCalledTimes(7));
    await waitFor(() => expect(generationsRefetch).toHaveBeenCalledTimes(1));

    mocks.detail = { isPending: false, isError: false, data: refreshedArtifact, refetch: detailRefetch };
    view.rerender(<MemoryRouter initialEntries={[`/artifacts/${artifact.id}`]}><Routes><Route path="/artifacts/:artifactId" element={<ArtifactDetailPage />} /></Routes></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "审批大纲" }));
    const refreshedAttempt = mutation.mutate.mock.calls[1]?.[0] as { expectedVersion: number; idempotencyKey: string };

    expect(firstAttempt.expectedVersion).toBe(4);
    expect(refreshedAttempt.expectedVersion).toBe(5);
    expect(refreshedAttempt.idempotencyKey).not.toBe(firstAttempt.idempotencyKey);
  });

  it("offers generation only for incomplete sections while the Artifact is GENERATING", () => {
    mocks.detail = { isPending: false, isError: false, data: generatingArtifact, refetch: detailRefetch };
    const view = renderDetail();

    fireEvent.click(screen.getByRole("button", { name: "生成章节 Gap" }));
    const submitted = generationMutation.mutate.mock.calls[0]?.[0] as GenerateArtifactSectionInput;
    expect(submitted).toMatchObject({ workspaceId: artifact.workspaceId, artifactId: artifact.id, expectedVersion: artifact.version, sectionKey: "gap" });
    expect(submitted.idempotencyKey).toMatch(/^artifact-generate-/);

    mocks.detail = { isPending: false, isError: false, data: artifact, refetch: detailRefetch };
    view.rerender(<MemoryRouter initialEntries={[`/artifacts/${artifact.id}`]}><Routes><Route path="/artifacts/:artifactId" element={<ArtifactDetailPage />} /></Routes></MemoryRouter>);
    expect(screen.queryByRole("button", { name: /生成章节/ })).not.toBeInTheDocument();
  });

  it("starts a revision and lets existing sections be regenerated or replaced by a GAP", () => {
    mocks.detail = { isPending: false, isError: false, data: evidenceArtifact, refetch: detailRefetch };
    const view = renderDetail();

    fireEvent.click(screen.getByRole("button", { name: "开始修订" }));
    expect(mutation.mutate.mock.calls.at(-1)?.[0]).toMatchObject({
      workspaceId: artifact.workspaceId,
      artifactId: artifact.id,
      expectedVersion: evidenceArtifact.version,
    });

    mocks.detail = { isPending: false, isError: false, data: revisingArtifact, refetch: detailRefetch };
    view.rerender(<MemoryRouter initialEntries={[`/artifacts/${artifact.id}`]}><Routes><Route path="/artifacts/:artifactId" element={<ArtifactDetailPage />} /></Routes></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "重新生成章节 Covered" }));
    expect(generationMutation.mutate.mock.calls.at(-1)?.[0]).toMatchObject({ sectionKey: "covered", expectedVersion: revisingArtifact.version });

    fireEvent.change(screen.getByLabelText("修订章节"), { target: { value: "covered" } });
    fireEvent.change(screen.getByLabelText("缺口说明"), { target: { value: "verified source no longer covers this section" } });
    fireEvent.click(screen.getByRole("button", { name: "替换为 GAP 章节" }));
    expect(mutation.mutate.mock.calls.at(-1)?.[0]).toMatchObject({
      sectionKey: "covered",
      expectedVersion: revisingArtifact.version,
      gapDescription: "verified source no longer covers this section",
    });
  });

  it("reuses the original Idempotency-Key after a lost generation response", () => {
    generationMutation.isError = true;
    generationMutation.error = new ArtifactApiError("NETWORK_ERROR", "NETWORK_ERROR", "lost response", true);
    generationMutation.variables = generationInput;
    mocks.detail = { isPending: false, isError: false, data: generatingArtifact, refetch: detailRefetch };
    renderDetail();

    fireEvent.click(screen.getByRole("button", { name: "重试生成请求" }));
    expect(generationMutation.mutate).toHaveBeenCalledWith(generationInput);
  });

  it("restores a pending Generation from GET after refresh and resumes Workflow polling", () => {
    mocks.detail = { isPending: false, isError: false, data: generatingArtifact, refetch: detailRefetch };
    mocks.generations = { isPending: false, isError: false, data: generationPage(generationRecord), refetch: generationsRefetch };
    mocks.workflow = { isPending: false, isError: false, data: workflow("running"), refetch: workflowRefetch };
    renderDetail();

    expect(screen.getByText("Workflow · running")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "打开 Workflow" })).toHaveAttribute("href", `/workflows/${generationRecord.workflowRunId}`);
    expect(generationMutation.mutate).not.toHaveBeenCalled();
  });

  it("polls Generation GET through terminal receipt lag without replaying POST", async () => {
    vi.useFakeTimers();
    mocks.detail = { isPending: false, isError: false, data: generatingArtifact, refetch: detailRefetch };
    mocks.generations = { isPending: false, isError: false, data: generationPage(generationRecord), refetch: generationsRefetch };
    generationsRefetch.mockResolvedValue({ data: generationPage(generationRecord) });
    mocks.workflow = { isPending: false, isError: false, data: workflow("succeeded"), refetch: workflowRefetch };
    const view = renderDetail();

    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    expect(generationsRefetch).toHaveBeenCalledTimes(1);
    expect(detailRefetch).toHaveBeenCalledTimes(1);
    expect(generationMutation.mutate).not.toHaveBeenCalled();

    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    expect(generationsRefetch).toHaveBeenCalledTimes(2);
    expect(detailRefetch).toHaveBeenCalledTimes(2);
    expect(generationMutation.mutate).not.toHaveBeenCalled();
    view.unmount();
  });

  it("shows response replay metadata while server GET remains the displayed Generation", () => {
    generationMutation.data = { ...generationAcceptance, replayed: true };
    generationMutation.variables = generationInput;
    mocks.detail = { isPending: false, isError: false, data: generatingArtifact, refetch: detailRefetch };
    mocks.generations = { isPending: false, isError: false, data: generationPage(generationRecord), refetch: generationsRefetch };
    mocks.workflow = { isPending: false, isError: false, data: workflow("running"), refetch: workflowRefetch };
    renderDetail();

    expect(screen.getByText("已从原请求恢复")).toBeInTheDocument();
  });

  it("uses new Idempotency-Keys for refreshed FAILED and CANCELLED Generations", () => {
    mocks.detail = { isPending: false, isError: false, data: generatingArtifact, refetch: detailRefetch };
    mocks.generations = { isPending: false, isError: false, data: generationPage({ ...generationRecord, status: "FAILED" }), refetch: generationsRefetch };
    const view = renderDetail();

    fireEvent.click(screen.getByRole("button", { name: "重新生成 Gap" }));
    const failedRetry = generationMutation.mutate.mock.calls[0]?.[0] as GenerateArtifactSectionInput;
    expect(failedRetry.idempotencyKey).not.toBe(generationInput.idempotencyKey);

    generationMutation.mutate.mockReset();
    mocks.generations = { isPending: false, isError: false, data: generationPage({ ...generationRecord, status: "CANCELLED" }), refetch: generationsRefetch };
    view.rerender(<MemoryRouter initialEntries={[`/artifacts/${artifact.id}`]}><Routes><Route path="/artifacts/:artifactId" element={<ArtifactDetailPage />} /></Routes></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "重新生成 Gap" }));
    const cancelledRetry = generationMutation.mutate.mock.calls[0]?.[0] as GenerateArtifactSectionInput;
    expect(cancelledRetry.idempotencyKey).not.toBe(generationInput.idempotencyKey);
    expect(cancelledRetry.idempotencyKey).not.toBe(failedRetry.idempotencyKey);
  });

  it("blocks automatic and manual retry for refreshed RECOVERY_REQUIRED", () => {
    mocks.detail = { isPending: false, isError: false, data: generatingArtifact, refetch: detailRefetch };
    mocks.generations = { isPending: false, isError: false, data: generationPage({ ...generationRecord, status: "RECOVERY_REQUIRED" }), refetch: generationsRefetch };
    renderDetail();

    expect(screen.getByText("需要人工恢复，已禁止自动重试。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /重新生成/ })).not.toBeInTheDocument();
    expect(generationMutation.mutate).not.toHaveBeenCalled();
  });

  it("refetches Artifact detail when a completed receipt makes the Generation item disappear", async () => {
    mocks.detail = { isPending: false, isError: false, data: generatingArtifact, refetch: detailRefetch };
    mocks.generations = { isPending: false, isError: false, data: generationPage(generationRecord), refetch: generationsRefetch };
    generationsRefetch.mockResolvedValue({ data: generationPage() });
    detailRefetch.mockResolvedValue({ data: artifact });
    mocks.workflow = { isPending: false, isError: false, data: workflow("succeeded"), refetch: workflowRefetch };
    const view = renderDetail();

    await waitFor(() => expect(detailRefetch).toHaveBeenCalledTimes(1));
    expect(generationMutation.mutate).not.toHaveBeenCalled();

    mocks.detail = { isPending: false, isError: false, data: artifact, refetch: detailRefetch };
    mocks.generations = { isPending: false, isError: false, data: generationPage(), refetch: generationsRefetch };
    view.rerender(<MemoryRouter initialEntries={[`/artifacts/${artifact.id}`]}><Routes><Route path="/artifacts/:artifactId" element={<ArtifactDetailPage />} /></Routes></MemoryRouter>);
    expect(screen.getByText("此章节没有正文，缺口已显式记录。")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /生成章节|重新生成/ })).not.toBeInTheDocument();
  });
});
