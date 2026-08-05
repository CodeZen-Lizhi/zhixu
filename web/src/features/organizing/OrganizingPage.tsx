import {
  AlertTriangle,
  ArrowLeft,
  BookOpenText,
  Check,
  ChevronRight,
  CircleDot,
  Copy,
  FileText,
  FolderSearch,
  Layers3,
  LoaderCircle,
  Plus,
  RotateCcw,
  Save,
  Search,
  Sparkles,
  Trash2,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";

import {
  OrganizingApiError,
  type OrganizingDraft,
  type OrganizingEvidence,
  type OrganizingMaterial,
  type OrganizingMaterialKind,
  type OrganizingMaterialReference,
  type OrganizingMaterialSearchReference,
  type OrganizingMaterialSearchResult,
  type OrganizingReasonCode,
  type OrganizingRun,
  type OrganizingSnapshot,
  type OrganizingTemplate,
  type OrganizingTemplateDeclaration,
  type OrganizingTemplateKind,
} from "../../api/organizing";
import { getActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, EmptyState, ErrorState, PageHeader } from "../../shared/ui";
import { WorkflowHumanTaskDecision } from "../business/WorkflowsPage";
import { SourceSpanViewer } from "../source-spans";
import {
  useAddOrganizingMaterial,
  useConfirmOrganizingDraft,
  useCloneOrganizingTemplate,
  useCreateOrganizingDraft,
  useOrganizingDraft,
  useOrganizingMaterialSearch,
  useOrganizingRun,
  useOrganizingSnapshot,
  useOrganizingTemplates,
  useOrganizingWorkflow,
  useRemoveOrganizingMaterial,
  useReviseOrganizingTemplate,
  useSetOrganizingMaterialSelection,
  useSubmitOrganizingHumanDecision,
  useSuggestOrganizingMaterials,
  useUpdateOrganizingDraft,
} from "./queries";
import "./organizing.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const commandKey = (kind: string): string => `${kind}-${crypto.randomUUID()}`;
const mandatoryGovernanceSectionKeys = new Set(["conflicts", "gaps", "sources"]);

const kindMeta: Record<OrganizingMaterialKind, { label: string; hint: string; icon: typeof FileText }> = {
  SOURCE_VERSION: { label: "资料版本", hint: "Source Version ID", icon: FileText },
  DOCUMENT_REVISION: { label: "文章版本", hint: "Article Revision ID", icon: BookOpenText },
  CLAIM: { label: "正式知识", hint: "Claim ID", icon: CircleDot },
  SMART_COLLECTION: { label: "智能集合", hint: "Collection ID", icon: FolderSearch },
};

const templateMeta: Record<OrganizingTemplateKind, { output: string; governance: string }> = {
  TOPIC_ARTICLE: { output: "文章草稿", governance: "先确认大纲，再分章生成" },
  MERGE_DOCUMENTS: { output: "合并提案", governance: "保留冲突，不覆盖原文" },
  KNOWLEDGE_REPORT: { output: "知识报告", governance: "默认保留为产物" },
  INTERVIEW_REVIEW: { output: "复习文档", governance: "默认保留为产物" },
};

const reasonLabels: Record<OrganizingReasonCode, string> = {
  HYBRID_MATCH: "混合检索命中",
  PROFILE_MATCH: "画像知识点",
  ALIAS_MATCH: "主题别名",
  FORMAL_KNOWLEDGE: "正式知识",
  COLLECTION_MEMBER: "集合成员",
  USER_ADDED: "手动补充",
};

const errorText = (error: unknown): string => error instanceof OrganizingApiError
  ? `${error.message}（${error.errorCode}）`
  : error instanceof Error ? error.message : "整理请求未完成。";

const statusTone = (status: OrganizingMaterial["availability"]): "success" | "warning" | "danger" =>
  status === "AVAILABLE" ? "success" : status === "STALE" ? "warning" : "danger";

const statusLabel: Record<OrganizingMaterial["availability"], string> = {
  AVAILABLE: "版本可用",
  STALE: "需要刷新",
  UNAVAILABLE: "当前不可用",
};

const referenceIdentity = (reference: OrganizingMaterialReference | OrganizingMaterialSearchReference): string => {
  switch (reference.kind) {
    case "SOURCE_VERSION": return reference.sourceVersionId;
    case "DOCUMENT_REVISION": return `${reference.documentId}:${reference.articleRevisionId}`;
    case "CLAIM": return reference.claimId;
    case "SMART_COLLECTION": return reference.collectionId;
  }
};

const originCollectionId = (reference: OrganizingMaterialReference): string | null => {
  switch (reference.kind) {
    case "SOURCE_VERSION": return reference.originCollectionId;
    case "DOCUMENT_REVISION": return reference.originCollectionId;
    case "CLAIM": return reference.originCollectionId;
    case "SMART_COLLECTION": return null;
  }
};

const EvidenceList = ({ evidence, workspaceId, summary }: { evidence: readonly OrganizingEvidence[]; workspaceId: string; summary: string }) => evidence.length === 0
  ? <p className="organizing-material__no-evidence">当前材料没有可打开的 Source Span。</p>
  : <details className="organizing-evidence">
    <summary>{summary} ({evidence.length})</summary>
    <ol>{evidence.map((item) => <li key={`${item.indexVersionId}:${item.chunkId}:${item.sourceSpanId}`}>
      <blockquote>片段校验 <code>{item.excerptHash.slice(0, 12)}</code> · 内容 <code>{item.contentHash.slice(0, 12)}</code></blockquote>
      <SourceSpanViewer className="organizing-evidence__open" label="打开来源片段" reference={{ workspaceId, sourceVersionId: item.sourceVersionId, sourceSpanId: item.sourceSpanId }} />
    </li>)}</ol>
  </details>;

const SnapshotMaterialRow = ({ material, workspaceId }: { material: OrganizingSnapshot["materials"][number]; workspaceId: string }) => {
  const Icon = kindMeta[material.reference.kind].icon;
  const collectionId = originCollectionId(material.reference);
  return <li className="organizing-snapshot-material">
    <header><span className="organizing-material__kind"><Icon size={16} />{kindMeta[material.reference.kind].label}</span><span>冻结材料 {material.position + 1}</span></header>
    <dl>
      <div><dt>引用</dt><dd><code>{referenceIdentity(material.reference)}</code></dd></div>
      {material.reference.kind === "SMART_COLLECTION" ? <>
        <div><dt>集合标记</dt><dd><code>{material.reference.collectionId}</code></dd></div>
        <div><dt>集合版本</dt><dd>{material.reference.collectionVersion}</dd></div>
        <div><dt>查询 Hash</dt><dd><code>{material.reference.queryHash}</code></dd></div>
        <div><dt>Read Model Revision</dt><dd><code>{material.reference.readModelRevision}</code></dd></div>
      </> : <>
        <div><dt>内容 Hash</dt><dd><code>{material.reference.contentHash}</code></dd></div>
        {material.reference.kind === "SOURCE_VERSION" ? <div><dt>Profile Revision</dt><dd>{material.reference.profileRevisionId === null ? "未绑定" : <code>{material.reference.profileRevisionId}</code>}</dd></div> : null}
        {material.reference.kind === "DOCUMENT_REVISION" ? <div><dt>Revision 序号</dt><dd>{material.reference.revisionNo}</dd></div> : null}
        {material.reference.kind === "CLAIM" ? <div><dt>Claim 版本</dt><dd>{material.reference.claimVersion}</dd></div> : null}
        {collectionId !== null ? <div><dt>来源集合</dt><dd><code>{collectionId}</code></dd></div> : null}
      </>}
    </dl>
    <EvidenceList evidence={material.evidence} workspaceId={workspaceId} summary="查看冻结证据" />
  </li>;
};

const ResultLink = ({ run }: { run: OrganizingRun }) => {
  if (run.status !== "SUCCEEDED" || run.resultKind === null || run.resultRef === null) return null;
  const href = run.resultKind === "ARTIFACT"
    ? `/artifacts/${run.resultRef}`
    : `/proposals/${run.resultRef}`;
  const label = run.resultKind === "ARTIFACT" ? "查看产物" : "查看合并提案";
  return <Button asChild><Link to={href}>{label}<ChevronRight size={16} /></Link></Button>;
};

const RunPanel = ({ snapshot, run, loading, error, onRetry }: { snapshot: OrganizingSnapshot; run: OrganizingRun | undefined; loading: boolean; error: unknown; onRetry: () => void }) => {
  const meta = run?.status === "QUEUED"
    ? { title: "输入已冻结", detail: "正在等待整理流程启动。" }
    : run?.status === "STARTED"
      ? { title: "正在整理材料", detail: "结果会从持久化流程状态恢复。" }
      : run?.status === "WAITING_FOR_HUMAN"
        ? { title: "等待你的确认", detail: "流程已停在人工确认节点。" }
        : run?.status === "SUCCEEDED"
          ? { title: "整理结果已生成", detail: "结果仍遵守对应的发布与审批边界。" }
          : run?.status === "FAILED"
            ? { title: "整理流程失败", detail: run.errorCode ?? "服务端没有返回失败码。" }
            : { title: "正在读取启动状态", detail: "Snapshot 已经保存，页面刷新不会改变它。" };

  return <section className="organizing-result" aria-labelledby="organizing-result-heading">
    <div className="organizing-result__mark"><Check size={20} /></div>
    <div>
      <p className="eyebrow">Workflow Input Snapshot</p>
      <h2 id="organizing-result-heading">{meta.title}</h2>
      <p>{meta.detail}</p>
      <dl>
        <div><dt>材料</dt><dd>{snapshot.materials.length} 项</dd></div>
        <div><dt>模板 Revision</dt><dd><code>{snapshot.templateRevisionId}</code></dd></div>
        <div><dt>模板 Hash</dt><dd><code>{snapshot.templateHash}</code></dd></div>
        <div><dt>Snapshot</dt><dd><code>{snapshot.canonicalHash}</code></dd></div>
      </dl>
      <section className="organizing-snapshot-detail" aria-labelledby="organizing-snapshot-detail-heading">
        <div><p className="eyebrow">Frozen Inputs</p><h3 id="organizing-snapshot-detail-heading">已冻结的材料与证据</h3></div>
        <ol>{snapshot.materials.map((material) => <SnapshotMaterialRow key={material.position} material={material} workspaceId={snapshot.workspaceId} />)}</ol>
      </section>
      {loading ? <p className="organizing-inline-state" role="status"><LoaderCircle className="organizing-spin" size={16} />正在读取流程状态…</p> : null}
      {error !== null ? <ErrorState title="流程状态暂不可读" description={errorText(error)} onRetry={onRetry} /> : null}
      <div className="organizing-result__actions">
        {run?.workflowRunId ? <Button variant="secondary" asChild><Link to={`/workflows/${run.workflowRunId}`}>查看流程<ChevronRight size={16} /></Link></Button> : null}
        {run ? <ResultLink run={run} /> : null}
      </div>
    </div>
  </section>;
};

const MaterialRow = ({ material, disabled, workspaceId, onSelectionChange, onRemove }: {
  material: OrganizingMaterial;
  disabled: boolean;
  workspaceId: string;
  onSelectionChange: (selected: boolean) => void;
  onRemove: () => void;
}) => {
  const Icon = kindMeta[material.kind].icon;
  return <article className="organizing-material">
    <header>
      <span className="organizing-material__kind"><Icon size={17} />{kindMeta[material.kind].label}</span>
      <div className="organizing-material__actions">
        <label className="organizing-material__selection">
          <input type="checkbox" checked={material.selected} disabled={disabled || material.availability !== "AVAILABLE"} aria-label={`选择 ${material.title}`} onChange={(event) => onSelectionChange(event.target.checked)} />
          <span>纳入本次整理</span>
        </label>
        <Badge tone={statusTone(material.availability)}>{statusLabel[material.availability]}</Badge>
        <button type="button" className="organizing-icon-button" title={`移除 ${material.title}`} aria-label={`移除 ${material.title}`} disabled={disabled} onClick={onRemove}><Trash2 size={16} /></button>
      </div>
    </header>
    <div className="organizing-material__body">
      <div>
        <h3>{material.title}</h3>
        <p>{material.origin === "SUGGESTED" ? "系统建议" : "手动补充"} · {material.selected ? "已纳入本次确认" : "未选择"}</p>
      </div>
      <code title={referenceIdentity(material.reference)}>{referenceIdentity(material.reference).slice(0, 12)}</code>
    </div>
    <div className="organizing-material__reasons" aria-label="入选理由">
      {material.reasons.map((reason) => <span key={reason}>{reasonLabels[reason]}</span>)}
      <span>相关度 {Math.round(material.score * 100)}%</span>
    </div>
    <EvidenceList evidence={material.evidence} workspaceId={workspaceId} summary="查看证据" />
  </article>;
};

const declarationCopy = (value: OrganizingTemplateDeclaration): OrganizingTemplateDeclaration => ({
  ...value,
  materials: { ...value.materials, allowedKinds: [...value.materials.allowedKinds] },
  sections: value.sections.map((section) => ({ ...section })),
  presentation: { ...value.presentation },
  output: { ...value.output },
});

const textBytes = (value: string): number => new TextEncoder().encode(value).byteLength;
const sectionKeyPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

const outputDefaultsError = (directoryValue: string, filenameValue: string): string | null => {
  const directory = directoryValue.trim();
  const filename = filenameValue.trim();
  if (directory === "") return "输出目录不能为空。";
  if (textBytes(directory) > 1024) return "输出目录不能超过 1024 字节。";
  const directoryParts = directory.split("/");
  if (directory.startsWith("/") || directory.includes("\\") || directoryParts.some((part) => part === "" || part === "." || part === "..")) {
    return "输出目录必须是 Workspace 内的规范相对目录。";
  }
  if (filename === "") return "输出文件名模式不能为空。";
  if (textBytes(filename) > 256) return "输出文件名模式不能超过 256 字节。";
  const expanded = filename.replaceAll("{slug}", "x").replaceAll("{date}", "x");
  if (filename.includes("/") || filename.includes("\\") || filename.includes("..") || !filename.toLowerCase().endsWith(".md") || expanded.includes("{") || expanded.includes("}")) {
    return "文件名模式只能使用 {slug}、{date} 占位符，并且必须生成 Markdown 文件。";
  }
  return null;
};

const templateDeclarationError = (value: OrganizingTemplateDeclaration): string | null => {
  const requiredText = (field: string, candidate: string, maxBytes: number): string | null => candidate.trim() === ""
    ? `${field}不能为空。`
    : textBytes(candidate) > maxBytes ? `${field}不能超过 ${String(maxBytes)} 字节。` : null;
  const optionalText = (field: string, candidate: string, maxBytes: number): string | null =>
    textBytes(candidate) > maxBytes ? `${field}不能超过 ${String(maxBytes)} 字节。` : null;
  const values = [
    requiredText("模板名称", value.name, 128),
    requiredText("模板说明", value.description, 2048),
    optionalText("目标读者", value.presentation.audience, 512),
    requiredText("输出语言", value.presentation.language, 64),
    requiredText("表达语气", value.presentation.tone, 128),
    optionalText("补充要求", value.additionalInstructions, 8192),
  ];
  const textError = values.find((item) => item !== null);
  if (textError !== undefined) return textError;
  const outputError = outputDefaultsError(value.output.directory, value.output.filenamePattern);
  if (outputError !== null) return outputError;
  if (!Number.isSafeInteger(value.materials.minMaterials) || value.materials.minMaterials < 1
    || !Number.isSafeInteger(value.materials.maxMaterials) || value.materials.maxMaterials < value.materials.minMaterials || value.materials.maxMaterials > 500) {
    return "材料数量必须是 1 到 500 之间的整数，且最大值不能小于最小值。";
  }
  if (value.materials.allowedKinds.length === 0 || value.materials.allowedKinds.length > 4
    || new Set(value.materials.allowedKinds).size !== value.materials.allowedKinds.length) return "至少选择一种且不重复的材料类型。";
  if (value.sections.length === 0 || value.sections.length > 24) return "章节数量必须在 1 到 24 之间。";
  for (const section of value.sections) {
    if (!sectionKeyPattern.test(section.key) || textBytes(section.key) > 64) return "章节标识只能使用小写字母、数字和连字符，且最长 64 字节。";
    const sectionError = requiredText("章节标题", section.title, 256);
    if (sectionError !== null) return sectionError;
  }
  if (new Set(value.sections.map((section) => section.key)).size !== value.sections.length) return "章节标识不能重复。";
  for (const key of mandatoryGovernanceSectionKeys) {
    if (!value.sections.some((section) => section.key === key && section.required)) return "冲突、知识缺口和来源章节必须保留并设为必填。";
  }
  return null;
};

const nextSectionKey = (sections: readonly { key: string }[]): string => {
  for (let index = 1; index <= 24; index += 1) {
    const key = `section-${String(index)}`;
    if (!sections.some((section) => section.key === key)) return key;
  }
  return "section-24";
};

const TemplateManagement = ({ templates, onTemplateRevised }: { templates: OrganizingTemplate[]; onTemplateRevised: (template: OrganizingTemplate) => void }) => {
  const workspaceId = useActiveWorkspaceId();
  const clone = useCloneOrganizingTemplate();
  const revise = useReviseOrganizingTemplate();
  const builtIns = templates.filter((template) => template.builtIn);
  const customTemplates = templates.filter((template) => !template.builtIn && template.workspaceId === workspaceId);
  const [cloneSourceId, setCloneSourceId] = useState("");
  const [cloneName, setCloneName] = useState("");
  const [editingTemplateId, setEditingTemplateId] = useState("");
  const [form, setForm] = useState<OrganizingTemplateDeclaration | null>(null);
  const [cloneError, setCloneError] = useState<unknown>();
  const [revisionError, setRevisionError] = useState<unknown>();
  const cloneAttemptRef = useRef<Attempt | undefined>(undefined);
  const revisionAttemptRef = useRef<Attempt | undefined>(undefined);
  const cloneSource = builtIns.find((template) => template.id === cloneSourceId) ?? builtIns[0];
  const editingTemplate = customTemplates.find((template) => template.id === editingTemplateId);

  useEffect(() => {
    if (cloneSource === undefined) {
      setCloneSourceId("");
      setCloneName("");
      return;
    }
    if (cloneSource.id !== cloneSourceId) {
      setCloneSourceId(cloneSource.id);
      setCloneName(`${cloneSource.name} 副本`);
    }
  }, [cloneSource, cloneSourceId]);

  useEffect(() => {
    if (editingTemplate !== undefined) return;
    setEditingTemplateId(customTemplates[0]?.id ?? "");
  }, [customTemplates, editingTemplate]);

  useEffect(() => {
    if (editingTemplate === undefined) {
      setForm(null);
      return;
    }
    setForm(declarationCopy(editingTemplate.currentRevision.declaration));
    setRevisionError(undefined);
  }, [editingTemplate?.id]);

  const cloneValidation = cloneSource === undefined
    ? "当前没有可复制的内置模板。"
    : cloneName.trim() === "" ? "自定义模板名称不能为空。"
      : textBytes(cloneName.trim()) > 128 ? "自定义模板名称不能超过 128 字节。" : null;
  const declarationValidation = useMemo(() => form === null ? "请选择一个自定义模板。" : templateDeclarationError(form), [form]);
  const busy = clone.isPending || revise.isPending;

  const createClone = async (): Promise<void> => {
    if (cloneSource === undefined || cloneValidation !== null) return;
    setCloneError(undefined);
    const name = cloneName.trim();
    const signature = JSON.stringify([workspaceId, cloneSource.id, name]);
    if (cloneAttemptRef.current?.signature !== signature) cloneAttemptRef.current = { signature, key: commandKey("organizing-template-clone") };
    try {
      const result = await clone.mutateAsync({ workspaceId, templateId: cloneSource.id, name, idempotencyKey: cloneAttemptRef.current.key });
      if (getActiveWorkspaceId() !== workspaceId) return;
      cloneAttemptRef.current = undefined;
      setEditingTemplateId(result.template.id);
      setForm(declarationCopy(result.template.currentRevision.declaration));
      setCloneError(undefined);
    } catch (error: unknown) {
      setCloneError(error);
    }
  };

  const saveRevision = async (): Promise<void> => {
    if (editingTemplate === undefined || form === null || declarationValidation !== null) return;
    setRevisionError(undefined);
    const declaration = declarationCopy(form);
    const signature = JSON.stringify([workspaceId, editingTemplate.id, editingTemplate.version, declaration]);
    if (revisionAttemptRef.current?.signature !== signature) revisionAttemptRef.current = { signature, key: commandKey("organizing-template-revise") };
    try {
      const result = await revise.mutateAsync({ workspaceId, templateId: editingTemplate.id, expectedVersion: editingTemplate.version, declaration, idempotencyKey: revisionAttemptRef.current.key });
      if (getActiveWorkspaceId() !== workspaceId) return;
      revisionAttemptRef.current = undefined;
      setForm(declarationCopy(result.template.currentRevision.declaration));
      onTemplateRevised(result.template);
    } catch (error: unknown) {
      setRevisionError(error);
    }
  };

  const updateForm = (update: (current: OrganizingTemplateDeclaration) => OrganizingTemplateDeclaration): void => {
    setForm((current) => current === null ? current : update(current));
  };

  return <section className="organizing-template-management" aria-labelledby="organizing-template-management-heading">
    <div className="organizing-section-heading">
      <div><p className="eyebrow">Template Library</p><h2 id="organizing-template-management-heading">管理自定义模板</h2></div>
      <span>{customTemplates.length} 个自定义模板</span>
    </div>
    <p className="organizing-template-management__intro">内置模板保持受控；复制后才可在当前 Workspace 中维护新的 Revision。修改只影响之后确认的整理任务。</p>

    <div className="organizing-template-clone">
      <label><span>复制来源</span><select value={cloneSource?.id ?? ""} disabled={busy || cloneSource === undefined} onChange={(event) => {
        const source = builtIns.find((template) => template.id === event.target.value);
        setCloneSourceId(event.target.value);
        setCloneName(source === undefined ? "" : `${source.name} 副本`);
        setCloneError(undefined);
      }}>{builtIns.map((template) => <option key={template.id} value={template.id}>{template.name}</option>)}</select></label>
      <label><span>自定义模板名称</span><input value={cloneName} maxLength={128} disabled={busy || cloneSource === undefined} aria-invalid={cloneValidation !== null} onChange={(event) => { setCloneName(event.target.value); setCloneError(undefined); }} /></label>
      <Button variant="secondary" onClick={() => { void createClone(); }} disabled={busy || cloneValidation !== null}><Copy size={16} />{clone.isPending ? "正在复制…" : "复制为自定义模板"}</Button>
    </div>
    {cloneValidation !== null && cloneSource !== undefined ? <p className="organizing-form-error" role="alert">{cloneValidation}</p> : null}
    {cloneError !== undefined ? <ErrorState title="模板没有复制成功" description={errorText(cloneError)} onRetry={() => { void createClone(); }} /> : null}

    {customTemplates.length === 0 ? <EmptyState title="还没有自定义模板" description="先复制一个内置模板，再按自己的整理习惯修改输出结构。" /> : <div className="organizing-template-editor">
      <label><span>正在编辑</span><select value={editingTemplate?.id ?? ""} disabled={busy} onChange={(event) => { setEditingTemplateId(event.target.value); setRevisionError(undefined); }}>{customTemplates.map((template) => <option key={template.id} value={template.id}>{template.name} · v{String(template.version)}</option>)}</select></label>
      {form !== null && editingTemplate !== undefined ? <div className="organizing-template-editor__fields">
        <div className="organizing-template-editor__grid">
          <label><span>模板名称</span><input value={form.name} maxLength={128} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, name: event.target.value }))} /></label>
          <label><span>模板说明</span><input value={form.description} maxLength={2048} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, description: event.target.value }))} /></label>
        </div>
        <fieldset className="organizing-template-editor__materials" disabled={busy}>
          <legend>允许的材料类型</legend>
          <div>{Object.entries(kindMeta).map(([kind, meta]) => {
            const typedKind = kind as OrganizingMaterialKind;
            const checked = form.materials.allowedKinds.includes(typedKind);
            return <label key={kind}><input type="checkbox" checked={checked} onChange={() => updateForm((current) => ({ ...current, materials: { ...current.materials, allowedKinds: checked ? current.materials.allowedKinds.filter((item) => item !== typedKind) : [...current.materials.allowedKinds, typedKind] } }))} />{meta.label}</label>;
          })}</div>
        </fieldset>
        <div className="organizing-template-editor__grid organizing-template-editor__numbers">
          <label><span>最少材料</span><input type="number" min="1" max="500" step="1" value={Number.isFinite(form.materials.minMaterials) ? String(form.materials.minMaterials) : ""} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, materials: { ...current.materials, minMaterials: event.target.value === "" ? Number.NaN : Number(event.target.value) } }))} /></label>
          <label><span>最多材料</span><input type="number" min="1" max="500" step="1" value={Number.isFinite(form.materials.maxMaterials) ? String(form.materials.maxMaterials) : ""} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, materials: { ...current.materials, maxMaterials: event.target.value === "" ? Number.NaN : Number(event.target.value) } }))} /></label>
        </div>
        <div className="organizing-template-editor__section-heading"><strong>文章章节</strong><button type="button" className="organizing-text-button" disabled={busy || form.sections.length >= 24} onClick={() => updateForm((current) => ({ ...current, sections: [...current.sections, { key: nextSectionKey(current.sections), title: "新章节", required: true }] }))}><Plus size={15} />添加章节</button></div>
        <div className="organizing-template-editor__sections">{form.sections.map((section, index) => {
          const governed = mandatoryGovernanceSectionKeys.has(section.key);
          return <div key={`${section.key}:${String(index)}`}>
          <label><span>章节标识</span><input value={section.key} maxLength={64} disabled={busy || governed} onChange={(event) => updateForm((current) => ({ ...current, sections: current.sections.map((item, itemIndex) => itemIndex === index ? { ...item, key: event.target.value } : item) }))} /></label>
          <label><span>章节标题</span><input value={section.title} maxLength={256} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, sections: current.sections.map((item, itemIndex) => itemIndex === index ? { ...item, title: event.target.value } : item) }))} /></label>
          <label className="organizing-template-editor__required"><input type="checkbox" checked={section.required} disabled={busy || governed} onChange={() => updateForm((current) => ({ ...current, sections: current.sections.map((item, itemIndex) => itemIndex === index ? { ...item, required: !item.required } : item) }))} />必填</label>
          <button type="button" className="organizing-icon-button" title={`移除章节 ${section.title}`} aria-label={`移除章节 ${section.title}`} disabled={busy || governed || form.sections.length === 1} onClick={() => updateForm((current) => ({ ...current, sections: current.sections.filter((_item, itemIndex) => itemIndex !== index) }))}><Trash2 size={16} /></button>
        </div>;
        })}</div>
        <div className="organizing-template-editor__grid">
          <label><span>目标读者</span><input value={form.presentation.audience} maxLength={512} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, presentation: { ...current.presentation, audience: event.target.value } }))} /></label>
          <label><span>输出语言</span><input value={form.presentation.language} maxLength={64} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, presentation: { ...current.presentation, language: event.target.value } }))} /></label>
          <label><span>表达语气</span><input value={form.presentation.tone} maxLength={128} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, presentation: { ...current.presentation, tone: event.target.value } }))} /></label>
          <label><span>篇幅</span><select value={form.presentation.length} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, presentation: { ...current.presentation, length: event.target.value as OrganizingTemplateDeclaration["presentation"]["length"] } }))}><option value="SHORT">简短</option><option value="MEDIUM">适中</option><option value="LONG">详细</option></select></label>
        </div>
        <fieldset className="organizing-template-editor__materials" disabled={busy}><legend>内容要求</legend><div>
          <label><input type="checkbox" checked={form.presentation.includeCode} onChange={() => updateForm((current) => ({ ...current, presentation: { ...current.presentation, includeCode: !current.presentation.includeCode } }))} />包含代码</label>
          <label><input type="checkbox" checked={form.presentation.includeExamples} onChange={() => updateForm((current) => ({ ...current, presentation: { ...current.presentation, includeExamples: !current.presentation.includeExamples } }))} />包含示例</label>
          <label><input type="checkbox" checked={form.presentation.includeFaq} onChange={() => updateForm((current) => ({ ...current, presentation: { ...current.presentation, includeFaq: !current.presentation.includeFaq } }))} />包含 FAQ</label>
        </div></fieldset>
        <div className="organizing-template-editor__grid">
          <label><span>输出目录</span><input value={form.output.directory} maxLength={1024} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, output: { ...current.output, directory: event.target.value } }))} /></label>
          <label><span>文件名模式</span><input value={form.output.filenamePattern} maxLength={256} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, output: { ...current.output, filenamePattern: event.target.value } }))} /></label>
        </div>
        <label className="organizing-template-editor__instructions"><span>补充要求</span><textarea value={form.additionalInstructions} rows={3} maxLength={8192} disabled={busy} onChange={(event) => updateForm((current) => ({ ...current, additionalInstructions: event.target.value }))} /></label>
        {declarationValidation !== null ? <p className="organizing-form-error" role="alert">{declarationValidation}</p> : null}
        {revisionError !== undefined ? <ErrorState title="模板 Revision 没有保存" description={errorText(revisionError)} onRetry={() => { void saveRevision(); }} /> : null}
        <div className="organizing-template-editor__actions"><Button variant="secondary" onClick={() => { setForm(declarationCopy(editingTemplate.currentRevision.declaration)); setRevisionError(undefined); }} disabled={busy}><RotateCcw size={16} />恢复已保存版本</Button><Button onClick={() => { void saveRevision(); }} disabled={busy || declarationValidation !== null}><Save size={16} />{revise.isPending ? "正在保存…" : `保存 Revision ${String(editingTemplate.currentRevision.revisionNo + 1)}`}</Button></div>
      </div> : null}
    </div>}
  </section>;
};

interface Attempt {
  signature: string;
  key: string;
}

const DraftWorkspace = ({ draft, templates, snapshotId, onSnapshot, onRefresh }: { draft: OrganizingDraft; templates: OrganizingTemplate[]; snapshotId: string; onSnapshot: (snapshot: OrganizingSnapshot) => void; onRefresh: () => void }) => {
  const workspaceId = useActiveWorkspaceId();
  const update = useUpdateOrganizingDraft();
  const suggest = useSuggestOrganizingMaterials();
  const add = useAddOrganizingMaterial();
  const remove = useRemoveOrganizingMaterial();
  const selection = useSetOrganizingMaterialSelection();
  const confirm = useConfirmOrganizingDraft();
  const [intent, setIntent] = useState(draft.intent);
  const [templateRevisionId, setTemplateRevisionId] = useState(draft.templateRevisionId ?? templates[0]?.currentRevision.id ?? "");
  const [materialSearchQuery, setMaterialSearchQuery] = useState("");
  const [submittedMaterialSearchQuery, setSubmittedMaterialSearchQuery] = useState("");
  const [materialSearchKind, setMaterialSearchKind] = useState<OrganizingMaterialKind>("SOURCE_VERSION");
  const [commandError, setCommandError] = useState<unknown>();
  const attemptsRef = useRef(new Map<string, Attempt>());
  const latestDraftRef = useRef(draft);
  const previousVersionRef = useRef(draft.version);
  latestDraftRef.current = draft;

  useEffect(() => {
    if (previousVersionRef.current === draft.version) return;
    previousVersionRef.current = draft.version;
    setIntent(draft.intent);
    if (draft.templateRevisionId !== null) setTemplateRevisionId(draft.templateRevisionId);
    setCommandError(undefined);
  }, [draft.intent, draft.templateRevisionId, draft.version]);

  useEffect(() => {
    if (templateRevisionId === "" && templates[0] !== undefined) setTemplateRevisionId(templates[0].currentRevision.id);
  }, [templateRevisionId, templates]);

  const keyFor = (kind: string, signature: string): string => {
    const existing = attemptsRef.current.get(kind);
    if (existing?.signature === signature) return existing.key;
    const key = commandKey(`organizing-${kind}`);
    attemptsRef.current.set(kind, { signature, key });
    return key;
  };

  const finishAttempt = (kind: string): void => {
    attemptsRef.current.delete(kind);
  };

  const busy = update.isPending || suggest.isPending || add.isPending || remove.isPending || selection.isPending || confirm.isPending;
  const editing = draft.status === "EDITING" && snapshotId === "";
  const materialSearch = useOrganizingMaterialSearch(submittedMaterialSearchQuery, materialSearchKind, editing);
  const materialSearchQueryBytes = new TextEncoder().encode(materialSearchQuery.trim()).byteLength;
  const selectedTemplate = templates.find((template) => template.currentRevision.id === templateRevisionId);
  const selectedCount = draft.materials.filter((material) => material.selected).length;
  const unavailableCount = draft.materials.filter((material) => material.selected && material.availability !== "AVAILABLE").length;
  const trimmedIntent = intent.trim();
  const existingMaterialReferences = useMemo(() => new Set(draft.materials.map((material) => `${material.reference.kind}:${referenceIdentity(material.reference)}`)), [draft.materials]);
  const confirmBlocked = trimmedIntent === ""
    ? "先写下本次整理要解决的问题。"
    : selectedTemplate === undefined ? "选择一个当前可用的整理模板。"
      : selectedCount < selectedTemplate.currentRevision.declaration.materials.minMaterials
        ? `至少选择 ${String(selectedTemplate.currentRevision.declaration.materials.minMaterials)} 项材料。`
        : selectedCount > selectedTemplate.currentRevision.declaration.materials.maxMaterials
          ? `最多选择 ${String(selectedTemplate.currentRevision.declaration.materials.maxMaterials)} 项材料。`
          : unavailableCount > 0 ? "先取消选择或移除需要处理的材料。" : null;

  const ensureConfiguration = useCallback(async (): Promise<OrganizingDraft | undefined> => {
    if (getActiveWorkspaceId() !== workspaceId) return undefined;
    const current = latestDraftRef.current;
    if (current.intent === trimmedIntent && current.templateRevisionId === templateRevisionId) return current;
    const signature = JSON.stringify([current.id, current.version, trimmedIntent, templateRevisionId]);
    const result = await update.mutateAsync({ workspaceId, draftId: current.id, expectedVersion: current.version, intent: trimmedIntent, templateRevisionId, idempotencyKey: keyFor("update", signature) });
    if (getActiveWorkspaceId() !== workspaceId) return undefined;
    finishAttempt("update");
    latestDraftRef.current = result.draft;
    return result.draft;
  }, [templateRevisionId, trimmedIntent, update, workspaceId]);

  const requestSuggestions = async (): Promise<void> => {
    setCommandError(undefined);
    try {
      const configured = await ensureConfiguration();
      if (configured === undefined) return;
      const signature = `${configured.id}:${String(configured.version)}:${configured.intent}`;
      const result = await suggest.mutateAsync({ workspaceId, draftId: configured.id, expectedVersion: configured.version, idempotencyKey: keyFor("suggest", signature) });
      if (getActiveWorkspaceId() !== workspaceId) return;
      finishAttempt("suggest");
      latestDraftRef.current = result.draft;
    } catch (error: unknown) {
      setCommandError(error);
    }
  };

  const addMaterial = async (candidate: OrganizingMaterialSearchResult): Promise<void> => {
    setCommandError(undefined);
    try {
      const configured = await ensureConfiguration();
      if (configured === undefined) return;
      const signature = JSON.stringify([configured.id, configured.version, candidate.reference]);
      const base = { workspaceId, draftId: configured.id, expectedVersion: configured.version, idempotencyKey: keyFor("add", signature) };
      const input = candidate.reference.kind === "SOURCE_VERSION"
        ? { ...base, kind: candidate.reference.kind, sourceVersionId: candidate.reference.sourceVersionId }
        : candidate.reference.kind === "DOCUMENT_REVISION"
          ? { ...base, kind: candidate.reference.kind, documentId: candidate.reference.documentId, articleRevisionId: candidate.reference.articleRevisionId }
          : candidate.reference.kind === "CLAIM"
            ? { ...base, kind: candidate.reference.kind, claimId: candidate.reference.claimId }
            : { ...base, kind: candidate.reference.kind, collectionId: candidate.reference.collectionId };
      const result = await add.mutateAsync(input);
      if (getActiveWorkspaceId() !== workspaceId) return;
      finishAttempt("add");
      latestDraftRef.current = result.draft;
    } catch (error: unknown) {
      setCommandError(error);
    }
  };

  const removeMaterial = async (materialId: string): Promise<void> => {
    setCommandError(undefined);
    try {
      const configured = await ensureConfiguration();
      if (configured === undefined) return;
      const signature = JSON.stringify([configured.id, configured.version, materialId]);
      const result = await remove.mutateAsync({ workspaceId, draftId: configured.id, materialId, expectedVersion: configured.version, idempotencyKey: keyFor("remove", signature) });
      if (getActiveWorkspaceId() !== workspaceId) return;
      finishAttempt("remove");
      latestDraftRef.current = result.draft;
    } catch (error: unknown) {
      setCommandError(error);
    }
  };

  const setMaterialSelection = async (materialId: string, selected: boolean): Promise<void> => {
    setCommandError(undefined);
    try {
      const configured = await ensureConfiguration();
      if (configured === undefined) return;
      const signature = JSON.stringify([configured.id, configured.version, materialId, selected]);
      const result = await selection.mutateAsync({ workspaceId, draftId: configured.id, materialId, selected, expectedVersion: configured.version, idempotencyKey: keyFor("selection", signature) });
      if (getActiveWorkspaceId() !== workspaceId) return;
      finishAttempt("selection");
      latestDraftRef.current = result.draft;
    } catch (error: unknown) {
      setCommandError(error);
    }
  };

  const confirmMaterials = async (): Promise<void> => {
    if (confirmBlocked !== null) return;
    setCommandError(undefined);
    try {
      const configured = await ensureConfiguration();
      if (configured === undefined) return;
      const signature = JSON.stringify([configured.id, configured.version, templateRevisionId]);
      const result = await confirm.mutateAsync({ workspaceId, draftId: configured.id, expectedVersion: configured.version, templateRevisionId, idempotencyKey: keyFor("confirm", signature) });
      if (getActiveWorkspaceId() !== workspaceId) return;
      finishAttempt("confirm");
      onSnapshot(result.snapshot);
    } catch (error: unknown) {
      setCommandError(error);
    }
  };

  return <>
    <section className="organizing-brief" aria-labelledby="organizing-brief-heading">
      <div className="organizing-section-heading">
        <div><p className="eyebrow">01 · Brief</p><h2 id="organizing-brief-heading">定义整理目标</h2></div>
        <Badge tone={editing ? "info" : "neutral"}>{editing ? `草稿 v${String(draft.version)}` : "输入已冻结"}</Badge>
      </div>
      <label className="organizing-intent">
        <span>这次想整理什么？</span>
        <textarea value={intent} disabled={!editing || busy} rows={3} maxLength={4096} placeholder="例如：把项目里关于事件恢复和幂等处理的资料整理成一篇面向工程师的专题文章" onChange={(event) => setIntent(event.target.value)} />
        <small>{intent.length}/4096</small>
      </label>

      <fieldset className="organizing-templates" disabled={!editing || busy}>
        <legend>选择结果模板</legend>
        <div>{templates.map((template) => {
          const checked = template.currentRevision.id === templateRevisionId;
          const meta = templateMeta[template.currentRevision.kind];
          return <label className="organizing-template" data-selected={checked ? "true" : "false"} key={template.id}>
            <input type="radio" name="organizing-template" value={template.currentRevision.id} checked={checked} onChange={() => setTemplateRevisionId(template.currentRevision.id)} />
            <span><strong>{template.name}</strong><small>{meta.output} · {meta.governance}</small></span>
            <span className="organizing-template__check" aria-hidden="true">{checked ? <Check size={15} /> : null}</span>
          </label>;
        })}</div>
      </fieldset>
      {selectedTemplate ? <p className="organizing-template-note"><Sparkles size={15} />{selectedTemplate.description}</p> : null}
      <TemplateManagement templates={templates} onTemplateRevised={(template) => {
        if (selectedTemplate?.id === template.id) setTemplateRevisionId(template.currentRevision.id);
      }} />
      {editing ? <div className="organizing-brief__actions"><Button onClick={() => { void requestSuggestions(); }} disabled={busy || trimmedIntent === "" || selectedTemplate === undefined}><Search size={16} />{suggest.isPending ? "正在重新建议…" : draft.materials.length === 0 ? "获取材料建议" : "重新建议"}</Button></div> : null}
    </section>

    {commandError !== undefined ? <ErrorState title="整理草稿没有更新" description={errorText(commandError)} onRetry={onRefresh} /> : null}

    <div className="organizing-material-layout">
      <section className="organizing-materials" aria-labelledby="organizing-material-heading">
        <div className="organizing-section-heading">
          <div><p className="eyebrow">02 · Material Set</p><h2 id="organizing-material-heading">确认材料</h2></div>
          <span>{draft.materials.length} 项</span>
        </div>
        {editing ? <div className="organizing-material-search">
          <div>
            <label><span>搜索补充材料</span><input value={materialSearchQuery} disabled={busy} maxLength={256} placeholder="输入关键词、主题或集合名称" onChange={(event) => setMaterialSearchQuery(event.target.value)} /></label>
            <label><span>材料类型</span><select value={materialSearchKind} disabled={busy} onChange={(event) => setMaterialSearchKind(event.target.value as OrganizingMaterialKind)}>{Object.entries(kindMeta).map(([kind, meta]) => <option key={kind} value={kind}>{meta.label}</option>)}</select></label>
          </div>
          <Button variant="secondary" onClick={() => setSubmittedMaterialSearchQuery(materialSearchQuery.trim())} disabled={busy || materialSearchQueryBytes < 2 || materialSearchQueryBytes > 256}><Search size={16} />搜索</Button>
          {submittedMaterialSearchQuery !== "" && materialSearch.isPending ? <p className="organizing-inline-state" role="status"><LoaderCircle className="organizing-spin" size={16} />正在搜索材料…</p> : null}
          {submittedMaterialSearchQuery !== "" && materialSearch.isError ? <ErrorState title="材料搜索不可用" description={errorText(materialSearch.error)} onRetry={() => { void materialSearch.refetch(); }} /> : null}
          {submittedMaterialSearchQuery !== "" && materialSearch.data?.length === 0 ? <p className="organizing-material-search__empty">没有找到当前类型的材料。</p> : null}
          {materialSearch.data && materialSearch.data.length > 0 ? <ol className="organizing-material-search__results" aria-label="材料搜索结果">{materialSearch.data.map((candidate) => {
            const alreadyAdded = existingMaterialReferences.has(`${candidate.reference.kind}:${referenceIdentity(candidate.reference)}`);
            return <li key={`${candidate.reference.kind}:${referenceIdentity(candidate.reference)}`}>
              <div><span className="organizing-material__kind">{kindMeta[candidate.kind].label}</span><strong>{candidate.title}</strong><code>{referenceIdentity(candidate.reference)}</code><Badge tone={statusTone(candidate.availability)}>{statusLabel[candidate.availability]}</Badge></div>
              <Button variant="secondary" onClick={() => { void addMaterial(candidate); }} disabled={busy || candidate.availability !== "AVAILABLE" || alreadyAdded || trimmedIntent === "" || selectedTemplate === undefined}>{alreadyAdded ? "已加入" : "加入材料"}</Button>
            </li>;
          })}</ol> : null}
        </div> : null}
        {draft.materials.length === 0 ? <EmptyState title="还没有候选材料" description="填写目标并请求建议，或搜索补充材料。系统不会在这里自动开始生成。" /> : <div className="organizing-material-list">{draft.materials.map((material) => <MaterialRow key={material.id} material={material} workspaceId={workspaceId} disabled={!editing || busy || trimmedIntent === "" || selectedTemplate === undefined} onSelectionChange={(selected) => { void setMaterialSelection(material.id, selected); }} onRemove={() => { void removeMaterial(material.id); }} />)}</div>}
      </section>

      <aside className="organizing-confirm" aria-labelledby="organizing-confirm-heading">
        <p className="eyebrow">03 · Confirm</p>
        <h2 id="organizing-confirm-heading">冻结本次输入</h2>
        <p>确认后会保存材料版本、证据和模板 Revision，再异步启动整理流程。</p>
        <dl>
          <div><dt>已选择</dt><dd>{selectedCount} / {draft.materials.length}</dd></div>
          <div><dt>需处理</dt><dd>{unavailableCount}</dd></div>
          <div><dt>模板</dt><dd>{selectedTemplate?.name ?? "未选择"}</dd></div>
        </dl>
        {confirmBlocked !== null ? <p className="organizing-confirm__blocked"><AlertTriangle size={15} />{confirmBlocked}</p> : <p className="organizing-confirm__ready"><Check size={15} />可以冻结为不可变 Snapshot</p>}
        <Button onClick={() => { void confirmMaterials(); }} disabled={!editing || busy || confirmBlocked !== null}>{confirm.isPending ? <LoaderCircle className="organizing-spin" size={16} /> : <Layers3 size={16} />}{confirm.isPending ? "正在确认…" : "确认材料并开始整理"}</Button>
        <small>这个操作不会直接覆盖资料或文章。</small>
      </aside>
    </div>
  </>;
};

export const OrganizingPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const draftValues = searchParams.getAll("draft");
  const snapshotValues = searchParams.getAll("snapshot");
  const unknownParams = [...searchParams.keys()].some((key) => key !== "draft" && key !== "snapshot");
  const draftId = draftValues.length === 1 ? draftValues[0] ?? "" : "";
  const snapshotId = snapshotValues.length === 1 ? snapshotValues[0] ?? "" : "";
  const invalidRoute = unknownParams || draftValues.length > 1 || snapshotValues.length > 1
    || draftId !== "" && !uuidPattern.test(draftId)
    || snapshotId !== "" && (!uuidPattern.test(snapshotId) || draftId === "");
  const create = useCreateOrganizingDraft();
  const draftQuery = useOrganizingDraft(invalidRoute ? "" : draftId);
  const templates = useOrganizingTemplates();
  const snapshot = useOrganizingSnapshot(invalidRoute ? "" : snapshotId);
  const run = useOrganizingRun(!invalidRoute && snapshot.data?.draftId === draftId ? snapshotId : "");
  const workflowRunId = run.data?.workflowRunId ?? "";
  const waitingForHuman = run.data?.status === "WAITING_FOR_HUMAN" && workflowRunId !== "";
  const workflow = useOrganizingWorkflow(workflowRunId, waitingForHuman);
  const humanDecision = useSubmitOrganizingHumanDecision(snapshotId, workflowRunId);
  const createAttemptRef = useRef<{ signature: string; key: string }>({ signature: "", key: commandKey("organizing-draft-create") });
  const [newIntent, setNewIntent] = useState("");

  useEffect(() => {
    const confirmedSnapshotId = draftQuery.data?.confirmedSnapshotId;
    if (invalidRoute || snapshotId !== "" || draftQuery.data?.status !== "CONFIRMED" || confirmedSnapshotId === null || confirmedSnapshotId === undefined) return;
    if (getActiveWorkspaceId() !== workspaceId) return;
    void navigate(`/authoring/organize?draft=${draftQuery.data.id}&snapshot=${confirmedSnapshotId}`, { replace: true });
  }, [draftQuery.data, invalidRoute, navigate, snapshotId, workspaceId]);

  const startCreate = useCallback((): void => {
    if (workspaceId === "" || invalidRoute || draftId !== "" || newIntent.trim() === "") return;
    const commandWorkspace = workspaceId;
    const intent = newIntent.trim();
    const signature = JSON.stringify([workspaceId, intent]);
    if (createAttemptRef.current.signature !== signature) createAttemptRef.current = { signature, key: commandKey("organizing-draft-create") };
    void create.mutateAsync({ workspaceId, intent, idempotencyKey: createAttemptRef.current.key })
      .then(({ draft }) => {
        if (getActiveWorkspaceId() !== commandWorkspace) return;
        void navigate(`/authoring/organize?draft=${draft.id}`, { replace: true });
      })
      .catch(() => undefined);
  }, [create, draftId, invalidRoute, navigate, newIntent, workspaceId]);

  const showSnapshot = (value: OrganizingSnapshot): void => {
    if (getActiveWorkspaceId() !== value.workspaceId) return;
    void navigate(`/authoring/organize?draft=${value.draftId}&snapshot=${value.id}`, { replace: true });
  };

  const snapshotBindingInvalid = snapshot.data !== undefined && snapshot.data.draftId !== draftId;
  const humanTask = waitingForHuman ? workflow.data?.humanTask : undefined;

  if (workspaceId === "") return <div className="page-stack organizing-page"><PageHeader title="整理成文" /><EmptyState title="先连接 Workspace" description="连接工作区后才能建议、确认和冻结材料。" action={<Button asChild><Link to="/workspace">连接 Workspace</Link></Button>} /></div>;
  if (invalidRoute) return <div className="page-stack organizing-page"><PageHeader title="整理成文" /><ErrorState title="整理链接无效" description="Draft 或 Snapshot 参数不是唯一的规范 UUID。" /><Button asChild variant="secondary"><Link to="/authoring/organize">重新开始</Link></Button></div>;

  return <div className="page-stack organizing-page">
    <div className="organizing-page__back"><Link to="/authoring"><ArrowLeft size={15} />返回创作</Link></div>
    <PageHeader title="整理成文" description="先核对材料和版本，再明确授权启动生成。" />

    {draftId === "" ? <section className="organizing-bootstrap" aria-labelledby="organizing-start-heading">
      <div className="organizing-section-heading"><div><p className="eyebrow">Start With An Intent</p><h2 id="organizing-start-heading">先写下你要解决的问题</h2></div></div>
      <label className="organizing-intent"><span>整理目标</span><textarea value={newIntent} rows={4} maxLength={4096} placeholder="例如：把项目里关于事件恢复和幂等处理的资料整理成一篇专题文章" onChange={(event) => setNewIntent(event.target.value)} /></label>
      <div className="organizing-brief__actions"><Button onClick={startCreate} disabled={create.isPending || newIntent.trim() === ""}><Plus size={16} />{create.isPending ? "正在建立草稿…" : "开始整理"}</Button></div>
      {create.isError ? <ErrorState title="无法建立整理草稿" description={errorText(create.error)} onRetry={startCreate} /> : null}
    </section> : null}

    {draftId !== "" && draftQuery.isPending ? <p className="organizing-inline-state" role="status"><LoaderCircle className="organizing-spin" size={18} />正在恢复整理草稿…</p> : null}
    {draftId !== "" && draftQuery.isError ? <ErrorState title="无法恢复整理草稿" description={errorText(draftQuery.error)} onRetry={() => { void draftQuery.refetch(); }} /> : null}
    {draftId !== "" && templates.isPending ? <p className="organizing-inline-state" role="status"><LoaderCircle className="organizing-spin" size={18} />正在读取整理模板…</p> : null}
    {templates.isError ? <ErrorState title="模板暂不可读" description={errorText(templates.error)} onRetry={() => { void templates.refetch(); }} /> : null}
    {draftQuery.data && !templates.isPending && !templates.isError && templates.data.length === 0 ? <EmptyState title="没有可用的整理模板" description="至少需要一个服务端模板 Revision 才能冻结整理输入。" /> : null}
    {draftQuery.data && templates.data && templates.data.length > 0 && !snapshotBindingInvalid ? <DraftWorkspace draft={draftQuery.data} templates={templates.data} snapshotId={snapshotId} onSnapshot={showSnapshot} onRefresh={() => { void draftQuery.refetch(); }} /> : null}

    {snapshotId !== "" && snapshot.isPending ? <p className="organizing-inline-state" role="status"><LoaderCircle className="organizing-spin" size={18} />正在恢复输入 Snapshot…</p> : null}
    {snapshotId !== "" && snapshot.isError ? <ErrorState title="无法恢复输入 Snapshot" description={errorText(snapshot.error)} onRetry={() => { void snapshot.refetch(); }} /> : null}
    {snapshotBindingInvalid ? <ErrorState title="Snapshot 与草稿不匹配" description="这个链接组合了不同整理任务的 Draft 与 Snapshot。" /> : null}
    {snapshot.data && !snapshotBindingInvalid ? <RunPanel snapshot={snapshot.data} run={run.data} loading={run.isPending} error={run.error} onRetry={() => { void run.refetch(); }} /> : null}
    {waitingForHuman && workflow.isPending ? <p className="organizing-inline-state" role="status"><LoaderCircle className="organizing-spin" size={18} />正在读取待确认内容…</p> : null}
    {waitingForHuman && workflow.isError ? <ErrorState title="待确认内容暂不可读" description={workflow.error.message} onRetry={() => { void workflow.refetch(); }} /> : null}
    {waitingForHuman && workflow.data && humanTask === undefined ? <ErrorState title="待确认任务尚未就绪" description="Workflow 已进入人工确认状态，但服务端尚未返回绑定的审阅任务。请刷新后重试。" onRetry={() => { void workflow.refetch(); }} /> : null}
    {humanTask ? <WorkflowHumanTaskDecision
      task={humanTask}
      pending={humanDecision.isPending || workflow.isFetching}
      error={humanDecision.error}
      onDecide={(approved, targetPath) => humanDecision.mutate({ task: humanTask, approved, targetPath })}
    /> : null}
  </div>;
};
