import { ArrowRight, Download, ExternalLink, FileText, LoaderCircle, Plus, RefreshCw, RotateCcw, Send, ShieldCheck, Sparkles, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";

import { useActiveWorkspaceId } from "../../app/active-workspace";
import { ArtifactApiError, type Artifact, type ArtifactCoverage, type ArtifactOutlineSection, type ArtifactSection, type ArtifactSectionGeneration, type ArtifactSectionGenerationPage, type ArtifactSectionGenerationStatus, type ArtifactStatus, type GenerateArtifactSectionInput } from "../../api/artifacts";
import { Badge, Button, Card, CardHeader, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import { artifactGenerationPollMilliseconds, artifactGenerationReceiptPollLimit, isArtifactGenerationWorkflowTerminal, useApproveArtifactDraft, useApproveArtifactOutline, useArtifact, useArtifactGenerationWorkflow, useArtifacts, useCreateArtifactPublishProposal, useExportArtifactMarkdown, useGenerateArtifactSection, usePlanArtifact, useRecordArtifactGapSection, useSectionGenerations, useStartArtifactRevision, useSubmitArtifactOutline } from "./queries";

const statusTone = (status: ArtifactStatus): "neutral" | "success" | "warning" | "info" => status === "APPROVED" || status === "EXPORTED" || status === "PUBLISHED" ? "success" : status === "DRAFT" || status === "GENERATING" ? "info" : status === "OUTLINE_REVIEW" || status === "PUBLISH_PROPOSED" ? "warning" : "neutral";
const coverageTone = (coverage: ArtifactCoverage): "success" | "warning" | "danger" => coverage.status === "COVERED" ? "success" : coverage.status === "PARTIAL" ? "warning" : "danger";
interface AttemptStore { current: Map<string, string>; }
const commandKey = (prefix: string, signature: string, attempts: AttemptStore): string => { const previous = attempts.current.get(signature); if (previous !== undefined) return previous; const value = `${prefix}-${crypto.randomUUID()}`; attempts.current.set(signature, value); return value; };
const errorText = (error: unknown): string => error instanceof Error ? error.message : "请求未完成，请重试。";
const generationErrorText = (error: unknown): string => error instanceof ArtifactApiError ? `${error.errorCode}：${error.message}` : errorText(error);
const isArtifactVersionConflict = (error: unknown): error is ArtifactApiError => error instanceof ArtifactApiError && error.status === 409 && error.errorCode === "ARTIFACT_VERSION_CONFLICT";
const createGenerationKey = (): string => `artifact-generate-${crypto.randomUUID()}`;
const generationTone = (status: ArtifactSectionGenerationStatus): "success" | "warning" | "info" | "danger" => status === "COMPLETED" ? "success" : status === "FAILED" || status === "RECOVERY_REQUIRED" ? "danger" : status === "CANCELLED" ? "warning" : "info";

const Coverage = ({ coverage }: { coverage: ArtifactCoverage }) => <div className={`artifact-coverage artifact-coverage--${coverage.status.toLowerCase()}`}><Badge tone={coverageTone(coverage)}>{coverage.status}</Badge>{coverage.gaps.length > 0 ? <ul>{coverage.gaps.map((gap) => <li key={`${gap.code}:${gap.description}`}><strong>{gap.code}</strong><span>{gap.description}</span></li>)}</ul> : <span>服务端已验证章节覆盖。</span>}</div>;

type RefetchSectionGenerations = () => Promise<{ data: ArtifactSectionGenerationPage | undefined }>;
type RefetchArtifact = () => Promise<{ data?: Artifact | undefined }>;

const ArtifactSectionView = ({ artifact, outlineSection, section, serverGeneration, refetchGenerations, refetchArtifact }: { artifact: Artifact; outlineSection: ArtifactOutlineSection; section: ArtifactSection | undefined; serverGeneration: ArtifactSectionGeneration | undefined; refetchGenerations: RefetchSectionGenerations; refetchArtifact: RefetchArtifact }) => {
  const generation = useGenerateArtifactSection();
  const accepted = generation.data?.artifactId === artifact.id && generation.data.sectionKey === outlineSection.key ? generation.data : undefined;
  const currentGeneration = serverGeneration ?? accepted;
  const workflow = useArtifactGenerationWorkflow(currentGeneration?.workflowRunId ?? "", currentGeneration?.status === "PENDING");
  const [receiptPollExhaustedGenerationId, setReceiptPollExhaustedGenerationId] = useState("");
  const workflowTerminal = isArtifactGenerationWorkflowTerminal(workflow.data?.status);

  useEffect(() => {
    if (currentGeneration?.status !== "PENDING" || !workflowTerminal) return;
    const generationId = currentGeneration.generationId;
    const lifecycle = { cancelled: false };
    const isCancelled = (): boolean => lifecycle.cancelled;
    let timer: number | undefined;
    const reconcile = async (attempt: number): Promise<void> => {
      const generationResult = await refetchGenerations();
      const persisted = generationResult.data?.items.find((item) => item.generationId === generationId);
      const stillPending = generationResult.data === undefined || persisted?.status === "PENDING";
      if (!isCancelled() || !stillPending) await refetchArtifact();
      if (isCancelled()) return;
      if (!stillPending) return;
      if (attempt + 1 >= artifactGenerationReceiptPollLimit) {
        setReceiptPollExhaustedGenerationId(generationId);
        return;
      }
      timer = window.setTimeout(() => void reconcile(attempt + 1), artifactGenerationPollMilliseconds);
    };
    void reconcile(0);
    return () => {
      lifecycle.cancelled = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [currentGeneration?.generationId, currentGeneration?.status, refetchArtifact, refetchGenerations, workflowTerminal]);

  const startGeneration = (reuseRequest: boolean): void => {
    const previous = generation.variables;
    const input: GenerateArtifactSectionInput = reuseRequest && previous?.workspaceId === artifact.workspaceId && previous.artifactId === artifact.id && previous.sectionKey === outlineSection.key
      ? previous
      : {
        workspaceId: artifact.workspaceId,
        artifactId: artifact.id,
        expectedVersion: artifact.version,
        sectionKey: outlineSection.key,
        idempotencyKey: createGenerationKey(),
      };
    generation.mutate(input);
  };

  const retryableRequestError = generation.isError && (!(generation.error instanceof ArtifactApiError) || generation.error.retryable);
  const status = currentGeneration?.status;
  const workflowStatus = workflow.data?.status;
  const receiptPollExhausted = receiptPollExhaustedGenerationId !== "" && receiptPollExhaustedGenerationId === currentGeneration?.generationId;
  const replayed = accepted !== undefined && accepted.generationId === currentGeneration?.generationId && accepted.replayed;
  const refreshReceipt = (): void => { void refetchGenerations().then(() => refetchArtifact()); };
  const generationAction = section === undefined ? "生成章节" : "重新生成章节";
  const generationPanel = artifact.status === "GENERATING" ? <div className={`artifact-generation${status === "RECOVERY_REQUIRED" ? " artifact-generation--recovery" : ""}`}>
    {status === undefined && !generation.isPending ? <Button onClick={() => startGeneration(false)} aria-label={`${generationAction} ${outlineSection.title}`}><Sparkles size={15} />{generationAction}</Button> : null}
    {generation.isPending ? <p className="artifact-generation__status" role="status"><LoaderCircle className="is-spinning" size={15} />{status === "PENDING" && workflowTerminal ? "正在同步 Generation 终态…" : "正在提交生成请求…"}</p> : null}
    {generation.isError ? <div className="artifact-generation__error" role="alert"><strong>{generationErrorText(generation.error)}</strong>{retryableRequestError ? <Button variant="secondary" onClick={() => startGeneration(true)}><RotateCcw size={15} />重试生成请求</Button> : null}</div> : null}
    {currentGeneration !== undefined ? <>
      <div className="artifact-generation__headline"><Badge tone={generationTone(currentGeneration.status)}>{currentGeneration.status}</Badge>{replayed ? <span>已从原请求恢复</span> : <span>Generation {currentGeneration.generationId.slice(0, 8)}…</span>}</div>
      {currentGeneration.status === "PENDING" ? <p className="artifact-generation__status">{workflow.isError ? "Workflow 状态读取失败。" : workflowTerminal ? receiptPollExhausted ? "Generation 终态仍未可见，请手动刷新。" : "Workflow 已结束，正在核对 Generation 终态。" : workflowStatus === undefined ? "正在读取 Workflow 状态…" : `Workflow · ${workflowStatus}`}</p> : null}
      {currentGeneration.status === "COMPLETED" ? <p className="artifact-generation__status">章节生成完成，正在刷新 Artifact Revision。</p> : null}
      {currentGeneration.status === "FAILED" ? <p className="artifact-generation__status">生成已安全失败，可使用新请求重新生成。</p> : null}
      {currentGeneration.status === "CANCELLED" ? <p className="artifact-generation__status">生成已安全取消，可使用新请求重新生成。</p> : null}
      {currentGeneration.status === "RECOVERY_REQUIRED" ? <p className="artifact-generation__status"><TriangleAlert size={15} />需要人工恢复，已禁止自动重试。</p> : null}
      <div className="artifact-generation__actions">
        <Button asChild variant="ghost"><Link to={`/workflows/${currentGeneration.workflowRunId}`}><ExternalLink size={15} />打开 Workflow</Link></Button>
        {workflow.isError ? <Button variant="secondary" onClick={() => void workflow.refetch()}><RefreshCw size={15} />重新查询 Workflow</Button> : null}
        {workflowTerminal && currentGeneration.status === "PENDING" ? <Button variant="secondary" onClick={refreshReceipt}><RefreshCw size={15} />刷新 Generation</Button> : null}
        {currentGeneration.status === "FAILED" || currentGeneration.status === "CANCELLED" ? <Button variant="secondary" onClick={() => startGeneration(false)} disabled={generation.isPending} aria-label={`重新生成 ${outlineSection.title}`}><RotateCcw size={15} />重新生成</Button> : null}
      </div>
    </> : null}
  </div> : null;

  return <article className="artifact-section">
    <header><div><small>{outlineSection.key}</small><h3>{outlineSection.title}</h3></div>{section === undefined ? <Badge tone={status === undefined ? "neutral" : generationTone(status)}>{generation.isPending && status === undefined ? "PENDING" : status ?? "未开始"}</Badge> : <Badge tone={coverageTone(section.coverage)}>{section.coverage.status}</Badge>}</header>
    {section === undefined ? <>
      <p className="muted">此章节尚未记录。</p>
      {generationPanel}
    </> : <><p className="artifact-section__content">{section.content || "此章节没有正文，缺口已显式记录。"}</p><Coverage coverage={section.coverage} />{section.citations.length > 0 ? <ul className="artifact-citations">{section.citations.map((citation) => <li key={citation.sourceSpanId}><code>{citation.sourceVersionId.slice(0, 8)}…/{citation.sourceSpanId.slice(0, 8)}…</code><span>{citation.excerpt}</span></li>)}</ul> : null}{generationPanel}</>}
  </article>;
};

export const ArtifactsPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  useEffect(() => setCursor(undefined), [workspaceId]);
  const artifacts = useArtifacts(cursor);
  const plan = usePlanArtifact();
  const attempts = useRef(new Map<string, string>());
  const [type, setType] = useState("knowledge-note");
  const [title, setTitle] = useState("");
  const [scope, setScope] = useState("");
  const submit = () => {
    if (workspaceId === "" || title.trim() === "" || scope.trim() === "") return;
    const input = { workspaceId, type: type.trim(), title: title.trim(), scopeDefinition: scope.trim() };
    const signature = JSON.stringify(input);
    plan.mutate({ ...input, idempotencyKey: commandKey("artifact-plan", signature, attempts) }, { onSuccess: () => { setTitle(""); setScope(""); attempts.current.clear(); } });
  };
  if (workspaceId === "") return <div className="page-stack"><UnavailableState title="先连接 Workspace" description="Artifact 只能读取和写入当前 Workspace；不会使用本地样例替代。" /><Link className="ui-button ui-button--primary" to="/workspace">连接 Workspace</Link></div>;
  return <div className="page-stack"><div className="page-intro page-intro--split"><div><p className="eyebrow">Learning / Artifact</p><h2>先冻结产物，再决定是否入库。</h2><p>Artifact Revision、来源覆盖和知识缺口都由服务端保存；创建 Publish Proposal 不会直接写入正式知识。</p></div><div className="folio-mark"><FileText size={20} /><strong>{artifacts.data?.items.length ?? "—"}</strong><span>份产物</span></div></div>
    <Card><CardHeader eyebrow="新建隔离产物" title="目标与范围" description="创建后先提交并审批大纲，章节及其来源覆盖保留在不可变 Revision 中。" /><form className="artifact-form" onSubmit={(event) => { event.preventDefault(); submit(); }}><label>类型<input value={type} maxLength={128} onChange={(event) => setType(event.target.value)} /></label><label>标题<input value={title} maxLength={512} onChange={(event) => setTitle(event.target.value)} placeholder="例如：检索链路复盘" required /></label><label className="artifact-form__wide">范围<textarea value={scope} maxLength={16_384} onChange={(event) => setScope(event.target.value)} placeholder="目标、边界和预期读者" required /></label><div className="button-row"><Button type="submit" disabled={plan.isPending}><Plus size={15} />{plan.isPending ? "正在创建…" : "创建 Artifact"}</Button>{plan.isError ? <span className="form-error" role="alert">{errorText(plan.error)}</span> : null}</div></form></Card>
    <Card><CardHeader eyebrow="Workspace artifacts" title="已创建产物" action={<Button variant="ghost" onClick={() => void artifacts.refetch()} aria-label="刷新 Artifact 列表"><RefreshCw size={16} /></Button>} />{artifacts.isError ? <ErrorState title="Artifact 列表不可用" description={errorText(artifacts.error)} onRetry={() => void artifacts.refetch()} /> : artifacts.isPending ? <div className="ui-state"><strong>正在读取 Artifact</strong><p>当前 Workspace 内的隔离 Revision 正在加载。</p></div> : artifacts.data.items.length === 0 ? <EmptyState title="还没有 Artifact" description="先创建目标和范围，再提交可审阅的大纲。" /> : <div className="artifact-list">{artifacts.data.items.map((artifact) => <Link className="artifact-row" key={artifact.id} to={`/artifacts/${artifact.id}`}><div><strong>{artifact.title}</strong><small>{artifact.type} · Revision {String(artifact.revision.revisionNo)} · v{String(artifact.version)}</small></div><div><Badge tone={statusTone(artifact.status)}>{artifact.status}</Badge><ArrowRight size={16} /></div></Link>)}</div>}<div className="pagination-row"><span className="sidebar-note">当前页 {String(artifacts.data?.items.length ?? 0)} 个 Artifact</span><div className="button-row">{cursor !== undefined ? <Button variant="ghost" onClick={() => setCursor(undefined)}>回到首屏</Button> : null}{artifacts.data?.nextCursor ? <Button variant="secondary" onClick={() => setCursor(artifacts.data.nextCursor)}>下一页<ArrowRight size={15} /></Button> : null}</div></div></Card>
  </div>;
};

export const ArtifactDetailPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const { artifactId = "" } = useParams();
  const artifact = useArtifact(artifactId);
  const generations = useSectionGenerations(artifactId);
  if (workspaceId === "") return <div className="page-stack"><UnavailableState title="未连接 Workspace" description="无法读取未绑定 Workspace 的 Artifact。" /></div>;
  if (artifact.isPending) return <div className="page-stack"><div className="ui-state"><strong>正在读取 Artifact</strong><p>正在核对当前 Revision、状态和来源覆盖。</p></div></div>;
  if (artifact.isError) return <div className="page-stack"><ErrorState title="Artifact 不可用" description={errorText(artifact.error)} onRetry={() => void artifact.refetch()} /></div>;
  if (generations.isPending) return <div className="page-stack"><div className="ui-state"><strong>正在恢复章节生成状态</strong><p>正在从服务端核对每章的权威 Generation。</p></div></div>;
  if (generations.isError) return <div className="page-stack"><ErrorState title="章节生成状态不可用" description={errorText(generations.error)} onRetry={() => void generations.refetch()} /></div>;
  return <ArtifactWorkspace artifact={artifact.data} generations={generations.data} refetchGenerations={generations.refetch} refetchArtifact={artifact.refetch} />;
};

const ArtifactWorkspace = ({ artifact, generations, refetchGenerations, refetchArtifact }: { artifact: Artifact; generations: ArtifactSectionGenerationPage; refetchGenerations: RefetchSectionGenerations; refetchArtifact: RefetchArtifact }) => {
  const submitOutline = useSubmitArtifactOutline(); const approveOutline = useApproveArtifactOutline(); const startRevision = useStartArtifactRevision(); const recordGap = useRecordArtifactGapSection(); const approveDraft = useApproveArtifactDraft(); const exportMarkdown = useExportArtifactMarkdown(); const publish = useCreateArtifactPublishProposal();
  const attempts = useRef(new Map<string, string>());
  const [outlineText, setOutlineText] = useState(artifact.revision.outline.map((section) => `${section.key}: ${section.title}`).join("\n"));
  const [sectionKey, setSectionKey] = useState(artifact.revision.outline.find((section) => !artifact.revision.sections.some((saved) => saved.key === section.key))?.key ?? "");
  const [gapCode, setGapCode] = useState("KNOWLEDGE_GAP"); const [gapDescription, setGapDescription] = useState("");
  const [refreshingRevision, setRefreshingRevision] = useState(false);
  const revisionCommand = (operation: string, mutate: (input: { workspaceId: string; artifactId: string; expectedVersion: number; idempotencyKey: string }) => void) => { const input = { workspaceId: artifact.workspaceId, artifactId: artifact.id, expectedVersion: artifact.version }; const signature = `${operation}:${JSON.stringify(input)}`; mutate({ ...input, idempotencyKey: commandKey(operation, signature, attempts) }); };
  const parseOutline = (): { key: string; title: string }[] | undefined => {
    const lines = outlineText.split("\n").map((line) => line.trim()).filter(Boolean); const parsed = lines.map((line) => { const split = line.indexOf(":"); return split < 1 ? undefined : { key: line.slice(0, split).trim(), title: line.slice(split + 1).trim() }; });
    if (parsed.some((item) => item === undefined)) return undefined;
    const complete = parsed as { key: string; title: string }[];
    return complete.some((item) => item.key === "" || item.title === "") || new Set(complete.map((item) => item.key)).size !== complete.length ? undefined : complete;
  };
  const outline = () => { const value = parseOutline(); if (value === undefined) return; const input = { workspaceId: artifact.workspaceId, artifactId: artifact.id, expectedVersion: artifact.version, outline: value }; submitOutline.mutate({ ...input, idempotencyKey: commandKey("artifact-outline", JSON.stringify(input), attempts) }); };
  const selectedOutline = artifact.revision.outline.find((section) => section.key === sectionKey);
  const record = () => { if (selectedOutline === undefined || gapDescription.trim() === "") return; const input = { workspaceId: artifact.workspaceId, artifactId: artifact.id, expectedVersion: artifact.version, sectionKey, title: selectedOutline.title, gapCode: gapCode.trim(), gapDescription: gapDescription.trim() }; recordGap.mutate({ ...input, idempotencyKey: commandKey("artifact-gap", JSON.stringify(input), attempts) }, { onSuccess: () => { setGapDescription(""); attempts.current.clear(); } }); };
  const selectedSectionExists = selectedOutline !== undefined && artifact.revision.sections.some((section) => section.key === selectedOutline.key);
  const busy = submitOutline.isPending || approveOutline.isPending || startRevision.isPending || recordGap.isPending || approveDraft.isPending || exportMarkdown.isPending || publish.isPending;
  const mutationError = [submitOutline.error, approveOutline.error, startRevision.error, recordGap.error, approveDraft.error, exportMarkdown.error, publish.error].find((error) => error !== null);
  const versionConflict = isArtifactVersionConflict(mutationError);
  const refreshCurrentRevision = (): void => {
    setRefreshingRevision(true);
    void refetchArtifact()
      .then(({ data: refreshedArtifact }) => {
        if (refreshedArtifact?.workspaceId !== artifact.workspaceId) return;
        if (refreshedArtifact.id !== artifact.id || refreshedArtifact.version <= artifact.version) return;
        attempts.current.clear();
        submitOutline.reset();
        approveOutline.reset();
        startRevision.reset();
        recordGap.reset();
        approveDraft.reset();
        exportMarkdown.reset();
        publish.reset();
        return refetchGenerations();
      })
      .catch(() => undefined)
      .finally(() => setRefreshingRevision(false));
  };
  return <div className="page-stack"><div className="page-intro page-intro--split"><div><Link className="back-link" to="/artifacts">← 返回 Artifact</Link><p className="eyebrow">{artifact.type} / Revision {String(artifact.revision.revisionNo)}</p><h2>{artifact.title}</h2><p>{artifact.scopeDefinition}</p></div><div className="hash-card"><span>当前内容哈希</span><code>{artifact.revision.contentHash.slice(0, 16)}…</code><Badge tone={statusTone(artifact.status)}>{artifact.status}</Badge></div></div>
    <Card><CardHeader eyebrow="受控操作" title="Artifact 生命周期" description="每次操作带当前版本和稳定 Idempotency-Key；409 表示状态或版本已变化，刷新后再继续。" /><div className="button-row">{artifact.status === "OUTLINE_REVIEW" ? <Button onClick={() => revisionCommand("artifact-approve-outline", (input) => approveOutline.mutate(input))} disabled={busy || versionConflict || refreshingRevision}><ShieldCheck size={15} />审批大纲</Button> : null}{artifact.status === "DRAFT" ? <Button onClick={() => revisionCommand("artifact-approve-draft", (input) => approveDraft.mutate(input))} disabled={busy || versionConflict || refreshingRevision}><ShieldCheck size={15} />审批草稿</Button> : null}{artifact.status === "DRAFT" || artifact.status === "APPROVED" || artifact.status === "EXPORTED" ? <Button variant="secondary" onClick={() => revisionCommand("artifact-start-revision", (input) => startRevision.mutate(input))} disabled={busy || versionConflict || refreshingRevision}><RotateCcw size={15} />开始修订</Button> : null}{artifact.status === "APPROVED" ? <Button onClick={() => revisionCommand("artifact-export", (input) => exportMarkdown.mutate(input))} disabled={busy || versionConflict || refreshingRevision}><Download size={15} />导出 Markdown</Button> : null}{artifact.status === "APPROVED" || artifact.status === "EXPORTED" ? <Button variant="secondary" onClick={() => revisionCommand("artifact-publish", (input) => publish.mutate(input))} disabled={busy || versionConflict || refreshingRevision}><Send size={15} />创建 Publish Proposal</Button> : null}{artifact.status === "PUBLISH_PROPOSED" ? <span className="artifact-note"><TriangleAlert size={15} />已创建 Proposal；正式知识写入仍须在 Proposals 中审批与执行。</span> : null}</div>{versionConflict ? <section className="artifact-generation__error" role="alert"><strong>当前 Revision 已更新</strong><p>其他操作已改变 Artifact，不能继续使用旧版本提交。</p><Button variant="secondary" onClick={refreshCurrentRevision} disabled={refreshingRevision}><RefreshCw size={15} />{refreshingRevision ? "正在刷新当前 Revision…" : "刷新当前 Revision"}</Button></section> : mutationError !== undefined ? <p className="form-error" role="alert">{errorText(mutationError)}</p> : null}{exportMarkdown.data?.export ? <p className="artifact-success" role="status">Markdown 已导出并绑定 Revision {String(exportMarkdown.data.export.revisionNo)}，输出哈希 {exportMarkdown.data.export.outputHash.slice(0, 16)}…</p> : null}{publish.data?.publication ? <p className="artifact-success" role="status">Publish Proposal 已创建：<Link to={`/proposals/${publish.data.publication.proposalId}`}>{publish.data.publication.proposalId}</Link>。Artifact 尚未成为正式知识。</p> : null}</Card>
    {artifact.status === "PLANNING" ? <Card><CardHeader eyebrow="第一步" title="提交大纲" description="每行使用 key: 标题。提交会冻结新的不可变 Revision，随后进入人工审批。" /><label className="artifact-outline-editor">大纲<textarea value={outlineText} onChange={(event) => setOutlineText(event.target.value)} placeholder="intro: 背景\nevidence: 已批准知识" /></label><div className="button-row"><Button onClick={outline} disabled={submitOutline.isPending || parseOutline() === undefined}>提交大纲</Button>{parseOutline() === undefined ? <span className="form-error" role="alert">每行必须为唯一的 key: 标题。</span> : null}</div></Card> : null}
    {artifact.status === "GENERATING" ? <Card><CardHeader eyebrow="章节记录" title="显式记录知识缺口" description="此入口只记录没有正文和 Citation 的 GAP 章节，也可替换当前 Revision 中的已有章节。带引用的 COVERED/PARTIAL 章节必须由服务端验证来源后写入。" /><div className="artifact-form"><label>修订章节<select value={sectionKey} onChange={(event) => setSectionKey(event.target.value)}><option value="">选择章节</option>{artifact.revision.outline.map((section) => <option key={section.key} value={section.key}>{section.title}</option>)}</select></label><label>缺口代码<input value={gapCode} maxLength={128} onChange={(event) => setGapCode(event.target.value)} /></label><label className="artifact-form__wide">缺口说明<textarea value={gapDescription} maxLength={4096} onChange={(event) => setGapDescription(event.target.value)} required /></label><Button onClick={record} disabled={recordGap.isPending || selectedOutline === undefined || gapDescription.trim() === ""}>{selectedSectionExists ? "替换为 GAP 章节" : "记录 GAP 章节"}</Button></div></Card> : null}
    <Card><CardHeader eyebrow="Revision evidence" title="章节、Citation 与 Coverage" description="Citation 的 verified、excerpt 和 hash 都来自服务端验证结果，页面不会自行判定可信性。" />{artifact.revision.outline.length === 0 ? <EmptyState title="尚未提交大纲" description="Artifact 仍在规划阶段。" /> : <div className="artifact-sections">{artifact.revision.outline.map((outlineSection) => <ArtifactSectionView key={outlineSection.key} artifact={artifact} outlineSection={outlineSection} section={artifact.revision.sections.find((item) => item.key === outlineSection.key)} serverGeneration={generations.items.find((item) => item.sectionKey === outlineSection.key)} refetchGenerations={refetchGenerations} refetchArtifact={refetchArtifact} />)}</div>}</Card>
    <Card><CardHeader eyebrow="Immutable binding" title="当前快照" /><dl className="detail-grid"><div><dt>Artifact version</dt><dd>{String(artifact.version)}</dd></div><div><dt>Revision</dt><dd>{String(artifact.revision.revisionNo)}</dd></div><div><dt>Revision ID</dt><dd className="mono">{artifact.revision.id}</dd></div><div><dt>Content hash</dt><dd className="mono">{artifact.revision.contentHash}</dd></div></dl></Card>
  </div>;
};
