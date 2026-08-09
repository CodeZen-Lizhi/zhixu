import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { WorkflowDetail, WorkflowHumanTask } from "../../api/business";
import type { OrganizingDraft, OrganizingMaterialSearchResult, OrganizingRun, OrganizingSnapshot, OrganizingTemplate, OrganizingTemplateKind } from "../../api/organizing";

const hooks = vi.hoisted(() => ({
  add: { mutateAsync: vi.fn(), isPending: false },
  clone: { mutateAsync: vi.fn(), isPending: false },
  confirm: { mutateAsync: vi.fn(), isPending: false },
  create: { mutateAsync: vi.fn(), isPending: false, isError: false, error: null },
  decision: { mutate: vi.fn(), isPending: false, error: null },
  draft: { data: undefined as OrganizingDraft | undefined, isPending: false, isError: false, error: null, refetch: vi.fn() },
  remove: { mutateAsync: vi.fn(), isPending: false },
  selection: { mutateAsync: vi.fn(), isPending: false },
  revise: { mutateAsync: vi.fn(), isPending: false },
  run: { data: undefined as OrganizingRun | undefined, isPending: false, error: null, refetch: vi.fn() },
  search: { data: undefined as OrganizingMaterialSearchResult[] | undefined, isPending: false, isError: false, error: null, refetch: vi.fn() },
  snapshot: { data: undefined as OrganizingSnapshot | undefined, isPending: false, isError: false, error: null, refetch: vi.fn() },
  suggest: { mutateAsync: vi.fn(), isPending: false },
  templates: { data: [] as OrganizingTemplate[], isPending: false, isError: false, error: null, refetch: vi.fn() },
  update: { mutateAsync: vi.fn(), isPending: false },
  workflow: { data: undefined as WorkflowDetail | undefined, isPending: false, isError: false, isFetching: false, error: null as Error | null, refetch: vi.fn() },
}));
const activeWorkspace = vi.hoisted(() => ({ id: "c1000000-0000-4000-8000-000000000001" }));

vi.mock("../../app/active-workspace", () => ({
  getActiveWorkspaceId: () => activeWorkspace.id,
  useActiveWorkspaceId: () => activeWorkspace.id,
}));
vi.mock("./queries", () => ({
  useAddOrganizingMaterial: () => hooks.add,
  useCloneOrganizingTemplate: () => hooks.clone,
  useConfirmOrganizingDraft: () => hooks.confirm,
  useCreateOrganizingDraft: () => hooks.create,
  useOrganizingDraft: () => hooks.draft,
  useOrganizingMaterialSearch: () => hooks.search,
  useOrganizingRun: () => hooks.run,
  useOrganizingSnapshot: () => hooks.snapshot,
  useOrganizingTemplates: () => hooks.templates,
  useOrganizingWorkflow: () => hooks.workflow,
  useRemoveOrganizingMaterial: () => hooks.remove,
  useSetOrganizingMaterialSelection: () => hooks.selection,
  useReviseOrganizingTemplate: () => hooks.revise,
  useSuggestOrganizingMaterials: () => hooks.suggest,
  useSubmitOrganizingHumanDecision: () => hooks.decision,
  useUpdateOrganizingDraft: () => hooks.update,
}));
vi.mock("../source-spans", () => ({ SourceSpanViewer: ({ label }: { label: string }) => <button type="button">{label}</button> }));

import { OrganizingPage } from "./OrganizingPage";

const workspaceId = activeWorkspace.id;
const draftId = "c1000000-0000-4000-8000-000000000002";
const snapshotId = "c1000000-0000-4000-8000-000000000003";
const templateKinds: OrganizingTemplateKind[] = ["TOPIC_ARTICLE", "MERGE_DOCUMENTS", "KNOWLEDGE_REPORT", "INTERVIEW_REVIEW"];
const templateNames = ["专题知识文章", "多文档合并整理", "知识总结报告", "面试复习文档"];

const template = (index: number): OrganizingTemplate => {
  const kind = templateKinds[index] ?? "TOPIC_ARTICLE";
  const templateId = `c2000000-0000-4000-8000-${String(index + 1).padStart(12, "0")}`;
  const revisionId = `c3000000-0000-4000-8000-${String(index + 1).padStart(12, "0")}`;
  return {
    id: templateId,
    workspaceId: null,
    key: kind.toLowerCase().replaceAll("_", "-"),
    name: templateNames[index] ?? kind,
    description: `${templateNames[index] ?? kind} 的受控模板说明。`,
    builtIn: true,
    kind,
    currentRevisionId: revisionId,
    version: 1,
    currentRevision: {
      id: revisionId,
      templateId,
      workspaceId: null,
      revisionNo: 1,
      kind,
      schemaVersion: "organizing-template/v1",
      canonicalHash: String(index + 1).repeat(64),
      declaration: {
        schemaVersion: "organizing-template/v1",
        kind,
        name: templateNames[index] ?? kind,
        description: "固定治理边界",
        materials: { allowedKinds: ["SOURCE_VERSION", "DOCUMENT_REVISION", "CLAIM", "SMART_COLLECTION"], minMaterials: 1, maxMaterials: 500 },
        sections: [
          { key: "overview", title: "概览", required: true },
          { key: "conflicts", title: "冲突", required: true },
          { key: "gaps", title: "知识缺口", required: true },
          { key: "sources", title: "来源", required: true },
        ],
        presentation: { audience: "工程师", language: "zh-CN", tone: "严谨", length: "MEDIUM", includeCode: false, includeExamples: true, includeFaq: false },
        output: { directory: "articles", filenamePattern: "{slug}.md" },
        additionalInstructions: "",
      },
      createdAt: "2026-08-03T08:00:00Z",
    },
    createdAt: "2026-08-03T08:00:00Z",
    updatedAt: "2026-08-03T08:00:00Z",
  };
};

const templates = templateKinds.map((_kind, index) => template(index));
const primaryTemplate = templates[0];
if (primaryTemplate === undefined) throw new Error("primary organizing template fixture is missing");

const customTemplate: OrganizingTemplate = {
  ...template(0),
  id: "c4000000-0000-4000-8000-000000000001",
  workspaceId,
  key: "event-recovery-custom",
  name: "事件恢复自定义模板",
  builtIn: false,
  currentRevisionId: "c5000000-0000-4000-8000-000000000001",
  version: 3,
  currentRevision: {
    ...template(0).currentRevision,
    id: "c5000000-0000-4000-8000-000000000001",
    templateId: "c4000000-0000-4000-8000-000000000001",
    workspaceId,
    revisionNo: 3,
  },
};

const draft: OrganizingDraft = {
  id: draftId,
  workspaceId,
  intent: "整理事件恢复与幂等",
  status: "EDITING",
  templateRevisionId: primaryTemplate.currentRevision.id,
  confirmedSnapshotId: null,
  version: 4,
  materials: [{
    id: "c1000000-0000-4000-8000-000000000010",
    draftId,
    workspaceId,
    kind: "SOURCE_VERSION",
    title: "事件恢复设计",
    reasons: ["HYBRID_MATCH", "PROFILE_MATCH"],
    origin: "SUGGESTED",
    availability: "AVAILABLE",
    score: 0.91,
    selected: true,
    position: 0,
    reference: { kind: "SOURCE_VERSION", sourceVersionId: "c1000000-0000-4000-8000-000000000012", version: 0, contentHash: "a".repeat(64), profileRevisionId: null, originCollectionId: null },
    evidence: [{ indexVersionId: "c1000000-0000-4000-8000-000000000013", chunkId: "c1000000-0000-4000-8000-000000000014", sourceVersionId: "c1000000-0000-4000-8000-000000000012", sourceSpanId: "c1000000-0000-4000-8000-000000000015", contentHash: "a".repeat(64), excerptHash: "b".repeat(64) }],
    createdAt: "2026-08-03T08:00:00Z",
  }],
  createdAt: "2026-08-03T08:00:00Z",
  updatedAt: "2026-08-03T08:03:00Z",
};
const primaryMaterial = draft.materials[0];
if (primaryMaterial === undefined) throw new Error("primary organizing material fixture is missing");
if (primaryMaterial.reference.kind !== "SOURCE_VERSION") throw new Error("primary organizing material fixture must be a Source Version");
const primarySourceReference = primaryMaterial.reference;

const materialSearchResult: OrganizingMaterialSearchResult = {
  workspaceId,
  kind: "SOURCE_VERSION",
  title: "恢复策略补充资料",
  availability: "AVAILABLE",
  reference: { kind: "SOURCE_VERSION", sourceVersionId: "c1000000-0000-4000-8000-000000000099" },
};

const snapshot: OrganizingSnapshot = {
  id: snapshotId,
  workspaceId,
  draftId,
  draftVersion: 4,
  templateId: primaryTemplate.id,
  templateRevisionId: primaryTemplate.currentRevision.id,
  templateHash: primaryTemplate.currentRevision.canonicalHash,
  intent: draft.intent,
  canonicalHash: "f".repeat(64),
  materials: [{ position: 0, reference: primaryMaterial.reference, evidence: primaryMaterial.evidence }],
  createdAt: "2026-08-03T08:04:00Z",
};
const workflowRunId = "c1000000-0000-4000-8000-000000000082";
const outlineHumanTask: WorkflowHumanTask = {
  id: "c1000000-0000-4000-8000-000000000083",
  runId: workflowRunId,
  nodeRunId: "c1000000-0000-4000-8000-000000000084",
  status: "pending",
  targetVersion: 2,
  decisionKind: "approval",
  createdAt: "2026-08-03T08:06:00Z",
  review: {
    kind: "TOPIC_OUTLINE",
    schemaVersion: 1,
    workspaceId,
    runId: workflowRunId,
    taskId: "c1000000-0000-4000-8000-000000000083",
    nodeRunId: "c1000000-0000-4000-8000-000000000084",
    snapshotId,
    snapshotHash: snapshot.canonicalHash,
    templateRevisionId: primaryTemplate.currentRevision.id,
    templateHash: primaryTemplate.currentRevision.canonicalHash,
    outline: [{ key: "overview", title: "概览", supports: [], gapCode: "EVIDENCE_UNAVAILABLE" }],
  },
};

const LocationProbe = () => {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}{location.search}</output>;
};

const renderPage = (path = `/authoring/organize?draft=${draftId}`) => render(<MemoryRouter initialEntries={[path]}><OrganizingPage /><LocationProbe /></MemoryRouter>);

afterEach(() => {
  activeWorkspace.id = workspaceId;
  hooks.draft.data = undefined;
  hooks.draft.isPending = false;
  hooks.draft.isError = false;
  hooks.templates.data = [];
  hooks.templates.isPending = false;
  hooks.templates.isError = false;
  hooks.snapshot.data = undefined;
  hooks.snapshot.isPending = false;
  hooks.snapshot.isError = false;
  hooks.run.data = undefined;
  hooks.run.isPending = false;
  hooks.run.error = null;
  hooks.search.data = undefined;
  hooks.search.isPending = false;
  hooks.search.isError = false;
  hooks.search.error = null;
  hooks.create.isPending = false;
  hooks.create.isError = false;
  hooks.workflow.data = undefined;
  hooks.workflow.isPending = false;
  hooks.workflow.isError = false;
  hooks.workflow.isFetching = false;
  hooks.workflow.error = null;
  hooks.decision.isPending = false;
  hooks.decision.error = null;
  hooks.add.mutateAsync.mockReset();
  hooks.clone.mutateAsync.mockReset();
  hooks.confirm.mutateAsync.mockReset();
  hooks.create.mutateAsync.mockReset();
  hooks.decision.mutate.mockReset();
  hooks.draft.refetch.mockReset();
  hooks.remove.mutateAsync.mockReset();
  hooks.selection.mutateAsync.mockReset();
  hooks.revise.mutateAsync.mockReset();
  hooks.run.refetch.mockReset();
  hooks.search.refetch.mockReset();
  hooks.snapshot.refetch.mockReset();
  hooks.suggest.mutateAsync.mockReset();
  hooks.templates.refetch.mockReset();
  hooks.update.mutateAsync.mockReset();
  hooks.workflow.refetch.mockReset();
});

describe("OrganizingPage", () => {
  it("shows the Workspace gate without issuing a create command", () => {
    activeWorkspace.id = "";
    renderPage("/authoring/organize");

    expect(screen.getByText("先连接 Workspace")).toBeInTheDocument();
    expect(hooks.create.mutateAsync).not.toHaveBeenCalled();
  });

  it("creates a Draft only after the user supplies an intent", async () => {
    hooks.create.mutateAsync.mockResolvedValue({ draft: { ...draft, intent: "整理恢复" }, replayed: false });
    renderPage("/authoring/organize");

    expect(hooks.create.mutateAsync).not.toHaveBeenCalled();
    fireEvent.change(screen.getByRole("textbox", { name: "整理目标" }), { target: { value: "整理恢复" } });
    fireEvent.click(screen.getByRole("button", { name: "开始整理" }));
    await waitFor(() => expect(hooks.create.mutateAsync).toHaveBeenCalledWith(expect.objectContaining({ intent: "整理恢复", workspaceId })));
  });

  it("renders four governed templates and explainable material evidence", () => {
    hooks.draft.data = draft;
    hooks.templates.data = templates;
    renderPage();

    for (const name of templateNames) expect(screen.getAllByText(name).length).toBeGreaterThan(0);
    expect(screen.getByText("事件恢复设计")).toBeInTheDocument();
    expect(screen.getByText("混合检索命中")).toBeInTheDocument();
    expect(screen.getByText("画像知识点")).toBeInTheDocument();
    expect(screen.getByText("相关度 91%")).toBeInTheDocument();
    fireEvent.click(screen.getByText("查看证据 (1)"));
    expect(screen.getByText(/片段校验/)).toHaveTextContent("片段校验 bbbbbbbbbbbb · 内容 aaaaaaaaaaaa");
    expect(screen.getByRole("button", { name: "打开来源片段" })).toBeInTheDocument();
  });

  it("explains when the server has no organizing template", () => {
    hooks.draft.data = draft;
    hooks.templates.data = [];
    renderPage();

    expect(screen.getByText("没有可用的整理模板")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "确认材料并开始整理" })).not.toBeInTheDocument();
  });

  it("does not confirm until the explicit confirmation command", async () => {
    hooks.draft.data = draft;
    hooks.templates.data = templates;
    hooks.confirm.mutateAsync.mockResolvedValue({ draft: { ...draft, status: "CONFIRMED", confirmedSnapshotId: snapshot.id, version: 5 }, snapshot, dispatchStatus: "PENDING", replayed: false });
    renderPage();

    expect(hooks.confirm.mutateAsync).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确认材料并开始整理" }));

    await waitFor(() => expect(hooks.confirm.mutateAsync).toHaveBeenCalledWith(expect.objectContaining({
      workspaceId,
      draftId,
      expectedVersion: 4,
      templateRevisionId: templates[0]?.currentRevision.id,
    })));
  });

  it("blocks confirmation while a selected material is stale or unavailable", () => {
    hooks.draft.data = { ...draft, materials: [{ ...primaryMaterial, availability: "UNAVAILABLE" }] };
    hooks.templates.data = templates;
    renderPage();

    expect(screen.getByText("先取消选择或移除需要处理的材料。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "确认材料并开始整理" })).toBeDisabled();
  });

  it("requires explicit selection before confirming a suggested material", async () => {
    const unselected = { ...primaryMaterial, selected: false };
    hooks.draft.data = { ...draft, materials: [unselected] };
    hooks.templates.data = templates;
    hooks.selection.mutateAsync.mockResolvedValue({ draft: { ...draft, version: 5 }, replayed: false });
    renderPage();

    expect(screen.getByText("至少选择 1 项材料。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "确认材料并开始整理" })).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "选择 事件恢复设计" }));

    await waitFor(() => expect(hooks.selection.mutateAsync).toHaveBeenCalledWith(expect.objectContaining({
      materialId: unselected.id,
      expectedVersion: 4,
      selected: true,
    })));
  });

  it("removes and adds a same-page material search result through Draft CAS commands", async () => {
    hooks.draft.data = draft;
    hooks.templates.data = templates;
    hooks.remove.mutateAsync.mockResolvedValue({ draft: { ...draft, version: 5, materials: [] }, replayed: false });
    hooks.add.mutateAsync.mockResolvedValue({ draft: { ...draft, version: 5 }, replayed: false });
    hooks.search.data = [materialSearchResult];
    renderPage();

    fireEvent.click(screen.getByRole("button", { name: "移除 事件恢复设计" }));
    await waitFor(() => expect(hooks.remove.mutateAsync).toHaveBeenCalledWith(expect.objectContaining({ expectedVersion: 4, materialId: draft.materials[0]?.id })));

    fireEvent.change(screen.getByLabelText("搜索补充材料"), { target: { value: "恢复策略" } });
    fireEvent.click(screen.getByRole("button", { name: "搜索" }));
    expect(screen.getByText("恢复策略补充资料")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "加入材料" }));
    await waitFor(() => expect(hooks.add.mutateAsync).toHaveBeenCalledWith(expect.objectContaining({ kind: "SOURCE_VERSION", sourceVersionId: "c1000000-0000-4000-8000-000000000099" })));
  });

  it("persists edited intent/template before requesting new suggestions", async () => {
    hooks.draft.data = draft;
    hooks.templates.data = templates;
    const updated = { ...draft, intent: "整理 Snapshot 恢复", templateRevisionId: templates[2]?.currentRevision.id ?? null, version: 5 };
    hooks.update.mutateAsync.mockResolvedValue({ draft: updated, replayed: false });
    hooks.suggest.mutateAsync.mockResolvedValue({ draft: { ...updated, version: 6 }, replayed: false });
    renderPage();

    fireEvent.change(screen.getByRole("textbox", { name: /这次想整理什么/ }), { target: { value: "整理 Snapshot 恢复" } });
    fireEvent.click(screen.getByLabelText(/知识总结报告/));
    fireEvent.click(screen.getByRole("button", { name: "重新建议" }));

    await waitFor(() => expect(hooks.update.mutateAsync).toHaveBeenCalledWith(expect.objectContaining({ intent: "整理 Snapshot 恢复", expectedVersion: 4, templateRevisionId: templates[2]?.currentRevision.id })));
    await waitFor(() => expect(hooks.suggest.mutateAsync).toHaveBeenCalledWith(expect.objectContaining({ expectedVersion: 5 })));
  });

  it("clones a built-in template with a stable response-loss key", async () => {
    hooks.draft.data = draft;
    hooks.templates.data = templates;
    hooks.clone.mutateAsync.mockRejectedValue(new Error("response lost"));
    renderPage();

    fireEvent.change(screen.getByLabelText("自定义模板名称"), { target: { value: "事件恢复工作模板" } });
    fireEvent.click(screen.getByRole("button", { name: "复制为自定义模板" }));
    await waitFor(() => expect(hooks.clone.mutateAsync).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "复制为自定义模板" }));
    await waitFor(() => expect(hooks.clone.mutateAsync).toHaveBeenCalledTimes(2));

    const firstInput = hooks.clone.mutateAsync.mock.calls[0]?.[0] as { idempotencyKey: string; workspaceId: string; templateId: string; name: string };
    const secondInput = hooks.clone.mutateAsync.mock.calls[1]?.[0] as { idempotencyKey: string };
    expect(firstInput).toMatchObject({ workspaceId, templateId: primaryTemplate.id, name: "事件恢复工作模板" });
    expect(secondInput.idempotencyKey).toBe(firstInput.idempotencyKey);
  });

  it("edits only a Workspace custom template and blocks an invalid declaration locally", async () => {
    hooks.draft.data = draft;
    hooks.templates.data = [...templates, customTemplate];
    renderPage();

    await waitFor(() => expect(screen.getByLabelText("正在编辑")).toHaveValue(customTemplate.id));
    for (const materialKind of ["资料版本", "文章版本", "正式知识", "智能集合"]) fireEvent.click(screen.getByLabelText(materialKind));

    expect(screen.getByText("至少选择一种且不重复的材料类型。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /保存修订版本 4/ })).toBeDisabled();
    expect(hooks.revise.mutateAsync).not.toHaveBeenCalled();
  });

  it("allows an empty audience but rejects an unsafe output path before saving", async () => {
    hooks.draft.data = draft;
    hooks.templates.data = [...templates, {
      ...customTemplate,
      currentRevision: {
        ...customTemplate.currentRevision,
        declaration: {
          ...customTemplate.currentRevision.declaration,
          presentation: { ...customTemplate.currentRevision.declaration.presentation, audience: "" },
        },
      },
    }];
    renderPage();

    await waitFor(() => expect(screen.getByLabelText("正在编辑")).toHaveValue(customTemplate.id));
    expect(screen.getByRole("button", { name: /保存修订版本 4/ })).toBeEnabled();
    fireEvent.change(screen.getByLabelText("输出目录"), { target: { value: "../outside" } });

    expect(screen.getByText("输出目录必须是 Workspace 内的规范相对目录。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /保存修订版本 4/ })).toBeDisabled();
    expect(hooks.revise.mutateAsync).not.toHaveBeenCalled();
  });

  it("retries a custom-template revision with the same key and original version", async () => {
    hooks.draft.data = draft;
    hooks.templates.data = [...templates, customTemplate];
    hooks.revise.mutateAsync.mockRejectedValue(new Error("response lost"));
    renderPage();

    await waitFor(() => expect(screen.getByLabelText("正在编辑")).toHaveValue(customTemplate.id));
    fireEvent.click(screen.getByRole("button", { name: /保存修订版本 4/ }));
    await waitFor(() => expect(hooks.revise.mutateAsync).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: /保存修订版本 4/ }));
    await waitFor(() => expect(hooks.revise.mutateAsync).toHaveBeenCalledTimes(2));

    const firstInput = hooks.revise.mutateAsync.mock.calls[0]?.[0] as { idempotencyKey: string; workspaceId: string; templateId: string; expectedVersion: number };
    const secondInput = hooks.revise.mutateAsync.mock.calls[1]?.[0] as { idempotencyKey: string };
    expect(firstInput).toMatchObject({ workspaceId, templateId: customTemplate.id, expectedVersion: 3 });
    expect(secondInput.idempotencyKey).toBe(firstInput.idempotencyKey);
  });

  it("links a completed Artifact result to its exact owner page", () => {
    const resultArtifactId = "c1000000-0000-4000-8000-000000000081";
    hooks.draft.data = { ...draft, status: "CONFIRMED", confirmedSnapshotId: snapshotId };
    hooks.templates.data = templates;
    hooks.snapshot.data = snapshot;
    hooks.run.data = {
      snapshotId,
      dispatchStatus: "STARTED",
      workflowStatus: "succeeded",
      workflowRunId: "c1000000-0000-4000-8000-000000000082",
      status: "SUCCEEDED",
      resultKind: "ARTIFACT",
      resultRef: resultArtifactId,
      resultHash: "d".repeat(64),
      errorCode: null,
      retryable: false,
      attemptCount: 1,
      updatedAt: "2026-08-03T08:06:00Z",
    };
    renderPage(`/authoring/organize?draft=${draftId}&snapshot=${snapshotId}`);

    expect(screen.getByRole("link", { name: /查看产物/ })).toHaveAttribute("href", `/artifacts/${resultArtifactId}`);
  });

  it("renders exact frozen materials, evidence and template hash from the Snapshot", () => {
    hooks.draft.data = { ...draft, status: "CONFIRMED", confirmedSnapshotId: snapshotId };
    hooks.templates.data = templates;
    const profileRevisionId = "c1000000-0000-4000-8000-000000000089";
    hooks.snapshot.data = {
      ...snapshot,
      materials: [
        { position: 0, reference: { ...primarySourceReference, profileRevisionId }, evidence: primaryMaterial.evidence },
        {
          position: 1,
          reference: {
            kind: "DOCUMENT_REVISION",
            documentId: "c1000000-0000-4000-8000-000000000091",
            articleRevisionId: "c1000000-0000-4000-8000-000000000092",
            revisionNo: 7,
            contentHash: "3".repeat(64),
            originCollectionId: null,
          },
          evidence: [],
        },
        {
          position: 2,
          reference: {
            kind: "CLAIM",
            claimId: "c1000000-0000-4000-8000-000000000093",
            claimVersion: 4,
            contentHash: "4".repeat(64),
            originCollectionId: null,
          },
          evidence: [],
        },
        {
          position: 3,
          reference: { kind: "SMART_COLLECTION", collectionId: "c1000000-0000-4000-8000-000000000090", collectionVersion: 2, queryHash: "1".repeat(64), readModelRevision: "2".repeat(64) },
          evidence: [],
        },
      ],
    };
    renderPage(`/authoring/organize?draft=${draftId}&snapshot=${snapshotId}`);

    expect(screen.getByRole("heading", { name: "已冻结的材料与证据" })).toBeInTheDocument();
    expect(screen.getByText("集合标记")).toBeInTheDocument();
    expect(screen.getAllByText(snapshot.templateHash).length).toBeGreaterThan(0);
    expect(screen.getByText("查看冻结证据 (1)")).toBeInTheDocument();
    expect(screen.getByText(profileRevisionId)).toBeInTheDocument();
    const documentRow = screen.getByText("文章版本").closest("li");
    const claimRow = screen.getByText("正式知识").closest("li");
    if (documentRow === null || claimRow === null) throw new Error("frozen material row is missing");
    expect(within(documentRow).getByText("7")).toBeInTheDocument();
    expect(within(claimRow).getByText("4")).toBeInTheDocument();
  });

  it("reviews and approves the bound outline without leaving the organizing page", () => {
    hooks.draft.data = { ...draft, status: "CONFIRMED", confirmedSnapshotId: snapshotId };
    hooks.templates.data = templates;
    hooks.snapshot.data = snapshot;
    hooks.run.data = {
      snapshotId,
      dispatchStatus: "STARTED",
      workflowStatus: "waiting_for_human",
      workflowRunId,
      status: "WAITING_FOR_HUMAN",
      resultKind: null,
      resultRef: null,
      resultHash: null,
      errorCode: null,
      retryable: false,
      attemptCount: 1,
      updatedAt: "2026-08-03T08:06:00Z",
    };
    hooks.workflow.data = {
      id: workflowRunId,
      workspaceId,
      definitionId: "c1000000-0000-4000-8000-000000000085",
      status: "waiting_for_human",
      input: {},
      version: 2,
      createdAt: "2026-08-03T08:05:00Z",
      updatedAt: "2026-08-03T08:06:00Z",
      pauseRequested: false,
      cancelRequested: false,
      humanTask: outlineHumanTask,
    };
    renderPage(`/authoring/organize?draft=${draftId}&snapshot=${snapshotId}`);

    expect(screen.getByRole("heading", { name: "逐节核对证据与缺口" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "批准并继续" }));
    expect(hooks.decision.mutate).toHaveBeenCalledWith({ task: outlineHumanTask, approved: true, targetPath: "" });
  });

  it("restores the immutable Snapshot route from a confirmed Draft", async () => {
    hooks.draft.data = { ...draft, status: "CONFIRMED", confirmedSnapshotId: snapshotId };
    hooks.templates.data = templates;
    renderPage(`/authoring/organize?draft=${draftId}`);

    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent(`/authoring/organize?draft=${draftId}&snapshot=${snapshotId}`));
  });

  it("does not navigate when a confirmation finishes after Workspace changes", async () => {
    hooks.draft.data = draft;
    hooks.templates.data = templates;
    let resolveConfirm: ((value: { draft: OrganizingDraft; snapshot: OrganizingSnapshot; dispatchStatus: "PENDING"; replayed: boolean }) => void) | undefined;
    hooks.confirm.mutateAsync.mockReturnValue(new Promise((resolve) => { resolveConfirm = resolve; }));
    renderPage();

    fireEvent.click(screen.getByRole("button", { name: "确认材料并开始整理" }));
    await waitFor(() => expect(hooks.confirm.mutateAsync).toHaveBeenCalled());
    activeWorkspace.id = "c1000000-0000-4000-8000-000000000099";
    await act(async () => {
      resolveConfirm?.({ draft: { ...draft, status: "CONFIRMED", confirmedSnapshotId: snapshot.id, version: 5 }, snapshot, dispatchStatus: "PENDING", replayed: false });
      await Promise.resolve();
    });

    expect(screen.getByTestId("location")).toHaveTextContent(`/authoring/organize?draft=${draftId}`);
  });

  it("rejects a route that combines an unrelated Snapshot and Draft", () => {
    hooks.draft.data = draft;
    hooks.templates.data = templates;
    hooks.snapshot.data = { ...snapshot, draftId: "c1000000-0000-4000-8000-000000000091" };
    renderPage(`/authoring/organize?draft=${draftId}&snapshot=${snapshotId}`);

    expect(screen.getByText("快照与草稿不匹配")).toBeInTheDocument();
    expect(screen.queryByText("Workflow Input Snapshot")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "确认材料并开始整理" })).not.toBeInTheDocument();
  });
});
