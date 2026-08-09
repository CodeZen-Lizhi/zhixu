import { AlertTriangle, ArrowLeft, Check, Eye, FileCheck2, PencilLine, RefreshCw, Send, ServerCrash } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";

import {
  AuthoringApiError,
  isPublishableDraft,
  type FreezeWorkingDraftInput,
  type FreezeWorkingDraftResult,
  type PublishRevisionInput,
  type UpdateWorkingDraftInput,
  type WorkingDraft,
} from "../../api/authoring";
import { getActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { markdownModelPath } from "../../shared/monaco-runtime";
import { Badge, Button, EmptyState, ErrorState, PageHeader } from "../../shared/ui";
import { MarkdownPreview } from "./MarkdownPreview";
import { MonacoMarkdownEditor } from "./MonacoMarkdownEditor";
import {
  useCreateWorkingDraft,
  useDocumentDraft,
  useFreezeWorkingDraft,
  usePublishArticleRevision,
  useUpdateWorkingDraft,
  useWorkingDraft,
} from "./queries";
import "./authoring.css";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const autosaveDelay = 700;

interface DraftBuffer {
  title: string;
  targetPath: string;
  body: string;
}

type SaveStatus = "saved" | "dirty" | "saving" | "retry" | "conflict" | "invalid";
type MobileMode = "edit" | "preview";

const draftBuffer = (draft: WorkingDraft): DraftBuffer => ({ title: draft.title, targetPath: draft.targetPath, body: draft.body });
const sameBuffer = (left: DraftBuffer, right: DraftBuffer): boolean => left.title === right.title && left.targetPath === right.targetPath && left.body === right.body;
const sameDraftSnapshot = (left: WorkingDraft, right: WorkingDraft): boolean => left.version === right.version
  && left.status === right.status
  && left.documentId === right.documentId
  && sameBuffer(draftBuffer(left), draftBuffer(right));
const canonicalMarkdown = (value: string): string => value.replaceAll("\r\n", "\n").replaceAll("\r", "\n");
const bufferSignature = (value: DraftBuffer): string => JSON.stringify([value.title, value.targetPath, value.body]);
const commandKey = (kind: string): string => `${kind}-${crypto.randomUUID()}`;
const errorText = (error: unknown): string => error instanceof AuthoringApiError ? `${error.message}（${error.errorCode}）` : error instanceof Error ? error.message : "请求未完成。";
const isConflict = (error: unknown): boolean => error instanceof AuthoringApiError && error.status === 409;
const deliveryIsUnknown = (error: unknown): boolean => error instanceof AuthoringApiError
  ? error.code === "NETWORK_ERROR" || error.retryable || (error.status !== null && error.status >= 500)
  : true;

const saveStatusText: Record<SaveStatus, string> = {
  saved: "已保存",
  dirty: "等待保存",
  saving: "正在保存",
  retry: "保存结果未确认",
  conflict: "发现版本冲突",
  invalid: "保存被拒绝",
};

export const NewDocumentPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const draftParams = searchParams.getAll("draft");
  const hasUnknownSearchParam = [...searchParams.keys()].some((key) => key !== "draft");
  const requestedDraftId = draftParams.length === 1 ? draftParams[0] ?? "" : "";
  const invalidDraftId = hasUnknownSearchParam
    || draftParams.length > 1
    || (searchParams.has("draft") && (requestedDraftId === "" || !uuidPattern.test(requestedDraftId)));
  const draftId = invalidDraftId ? "" : requestedDraftId;
  const editorBinding = `${workspaceId}:${draftId}`;
  const activeBindingRef = useRef(editorBinding);
  activeBindingRef.current = editorBinding;
  const createKeyRef = useRef(commandKey("working-draft-create"));
  const createStartedRef = useRef(false);
  const createRouteRef = useRef({ workspaceId, requestedDraftId });
  const create = useCreateWorkingDraft();
  const draftQuery = useWorkingDraft(draftId);
  const update = useUpdateWorkingDraft();
  const freeze = useFreezeWorkingDraft();
  const publish = usePublishArticleRevision();
  const [buffer, setBuffer] = useState<DraftBuffer>();
  const bufferRef = useRef<DraftBuffer | undefined>(undefined);
  const [confirmedDraft, setConfirmedDraft] = useState<WorkingDraft>();
  const [conflictDraft, setConflictDraft] = useState<WorkingDraft>();
  const [saveStatus, setSaveStatus] = useState<SaveStatus>("saved");
  const [saveError, setSaveError] = useState<unknown>();
  const [freezeError, setFreezeError] = useState<unknown>();
  const [publishError, setPublishError] = useState<unknown>();
  const [latestFreeze, setLatestFreeze] = useState<FreezeWorkingDraftResult>();
  const [mobileMode, setMobileMode] = useState<MobileMode>("edit");
  const [editorError, setEditorError] = useState<Error>();
  const initializedBindingRef = useRef("");
  const retrySaveRef = useRef<UpdateWorkingDraftInput | undefined>(undefined);
  const failedSaveSignatureRef = useRef("");
  const retryFreezeRef = useRef<FreezeWorkingDraftInput | undefined>(undefined);
  const retryPublishRef = useRef<PublishRevisionInput | undefined>(undefined);
  const commandControllersRef = useRef(new Set<AbortController>());
  const commandScopeRef = useRef<{ binding: string } | undefined>(undefined);
  const titleRef = useRef<HTMLInputElement>(null);
  const conflictRef = useRef<HTMLElement>(null);
  const currentDraft = confirmedDraft?.workspaceId === workspaceId && confirmedDraft.id === draftId ? confirmedDraft : undefined;
  const currentQueryDraft = draftQuery.data?.workspaceId === workspaceId && draftQuery.data.id === draftId ? draftQuery.data : undefined;
  const documentId = currentDraft?.documentId ?? currentQueryDraft?.documentId ?? "";
  const documentQuery = useDocumentDraft(documentId);

  useEffect(() => {
    bufferRef.current = buffer;
  }, [buffer]);

  useEffect(() => {
    const previous = createRouteRef.current;
    if (previous.workspaceId !== workspaceId || (requestedDraftId === "" && previous.requestedDraftId !== "")) {
      createKeyRef.current = commandKey("working-draft-create");
      createStartedRef.current = false;
    }
    createRouteRef.current = { workspaceId, requestedDraftId };
  }, [requestedDraftId, workspaceId]);

  useEffect(() => {
    const previousScope = commandScopeRef.current;
    if (previousScope !== undefined && previousScope.binding !== editorBinding) {
      for (const controller of commandControllersRef.current) controller.abort();
      commandControllersRef.current.clear();
    }
    const scope = { binding: editorBinding };
    commandScopeRef.current = scope;
    return () => queueMicrotask(() => {
      if (commandScopeRef.current !== scope) return;
      for (const controller of commandControllersRef.current) controller.abort();
      commandControllersRef.current.clear();
    });
  }, [editorBinding]);

  useEffect(() => {
    if (initializedBindingRef.current === "" || initializedBindingRef.current === editorBinding) return;
    initializedBindingRef.current = "";
    bufferRef.current = undefined;
    setBuffer(undefined);
    setConfirmedDraft(undefined);
    setConflictDraft(undefined);
    setSaveError(undefined);
    setFreezeError(undefined);
    setPublishError(undefined);
    setLatestFreeze(undefined);
    setSaveStatus("saved");
    retrySaveRef.current = undefined;
    retryFreezeRef.current = undefined;
    retryPublishRef.current = undefined;
    failedSaveSignatureRef.current = "";
  }, [editorBinding]);

  const beginCommand = useCallback((): AbortController => {
    const controller = new AbortController();
    commandControllersRef.current.add(controller);
    return controller;
  }, []);

  const finishCommand = useCallback((controller: AbortController): void => {
    commandControllersRef.current.delete(controller);
  }, []);

  const startCreate = useCallback((): void => {
    if (workspaceId === "") return;
    const commandBinding = activeBindingRef.current;
    const controller = beginCommand();
    void create.mutateAsync({ workspaceId, idempotencyKey: createKeyRef.current, signal: controller.signal })
      .then(({ workingDraft }) => {
        if (controller.signal.aborted || getActiveWorkspaceId() !== workspaceId || activeBindingRef.current !== commandBinding) return;
        void navigate(`/authoring/new?draft=${workingDraft.id}`, { replace: true });
      })
      .catch(() => undefined)
      .finally(() => finishCommand(controller));
  }, [beginCommand, create, finishCommand, navigate, workspaceId]);

  useEffect(() => {
    if (workspaceId === "" || invalidDraftId || requestedDraftId !== "" || createStartedRef.current) return;
    createStartedRef.current = true;
    startCreate();
  }, [invalidDraftId, requestedDraftId, startCreate, workspaceId]);

  useEffect(() => {
    const binding = `${workspaceId}:${draftId}`;
    const draft = draftQuery.data;
    if (draftId === "" || initializedBindingRef.current === binding
      || draft?.workspaceId !== workspaceId || draft.id !== draftId) return;
    initializedBindingRef.current = binding;
    const next = draftBuffer(draft);
    setBuffer(next);
    setConfirmedDraft(draft);
    setConflictDraft(undefined);
    setSaveError(undefined);
    setSaveStatus("saved");
    window.setTimeout(() => titleRef.current?.focus(), 0);
  }, [draftId, draftQuery.data, workspaceId]);

  useEffect(() => {
    if (saveStatus === "conflict") conflictRef.current?.focus();
  }, [saveStatus]);

  const readConflictDraft = useCallback(async (): Promise<void> => {
    const requestedBinding = activeBindingRef.current;
    const previousDraft = currentDraft;
    const result = await draftQuery.refetch();
    if (activeBindingRef.current !== requestedBinding || result.data === undefined
      || `${result.data.workspaceId}:${result.data.id}` !== requestedBinding) return;
    if (previousDraft !== undefined && (result.data.version < previousDraft.version || sameDraftSnapshot(result.data, previousDraft))) return;
    setConflictDraft(result.data);
  }, [currentDraft, draftQuery]);

  const enterConflict = useCallback((error: unknown): void => {
    retrySaveRef.current = undefined;
    retryFreezeRef.current = undefined;
    failedSaveSignatureRef.current = "";
    setSaveError(error);
    setSaveStatus("conflict");
    void readConflictDraft();
  }, [readConflictDraft]);

  const sendUpdate = useCallback((input: UpdateWorkingDraftInput): void => {
    retrySaveRef.current = input;
    failedSaveSignatureRef.current = "";
    setSaveError(undefined);
    setSaveStatus("saving");
    const commandBinding = activeBindingRef.current;
    const controller = beginCommand();
    update.mutate({ ...input, signal: controller.signal }, {
      onSuccess: ({ workingDraft }) => {
        finishCommand(controller);
        if (controller.signal.aborted || getActiveWorkspaceId() !== input.workspaceId || activeBindingRef.current !== commandBinding) return;
        retrySaveRef.current = undefined;
        setConfirmedDraft(workingDraft);
        const currentBuffer = bufferRef.current;
        setSaveStatus(currentBuffer !== undefined && sameBuffer(currentBuffer, draftBuffer(workingDraft)) ? "saved" : "dirty");
      },
      onError: (error) => {
        finishCommand(controller);
        if (controller.signal.aborted || getActiveWorkspaceId() !== input.workspaceId || activeBindingRef.current !== commandBinding) return;
        if (isConflict(error)) {
          enterConflict(error);
          return;
        }
        setSaveError(error);
        if (deliveryIsUnknown(error)) {
          setSaveStatus("retry");
          return;
        }
        retrySaveRef.current = undefined;
        failedSaveSignatureRef.current = bufferSignature({ title: input.title, targetPath: input.targetPath, body: input.body });
        setSaveStatus("invalid");
      },
    });
  }, [beginCommand, enterConflict, finishCommand, update]);

  useEffect(() => {
    if (buffer === undefined || confirmedDraft?.status !== "EDITING" || confirmedDraft.workspaceId !== workspaceId
      || confirmedDraft.id !== draftId || saveStatus === "conflict" || saveStatus === "saving" || conflictDraft !== undefined
      || retrySaveRef.current !== undefined || update.isPending || freeze.isPending) return;
    const confirmedBuffer = draftBuffer(confirmedDraft);
    if (sameBuffer(buffer, confirmedBuffer)) return;
    const signature = bufferSignature(buffer);
    if (failedSaveSignatureRef.current === signature) return;
    const timer = window.setTimeout(() => {
      sendUpdate({
        workspaceId,
        draftId: confirmedDraft.id,
        expectedVersion: confirmedDraft.version,
        title: buffer.title,
        targetPath: buffer.targetPath,
        body: buffer.body,
        idempotencyKey: commandKey("working-draft-update"),
      });
    }, autosaveDelay);
    return () => window.clearTimeout(timer);
  }, [buffer, confirmedDraft, conflictDraft, freeze.isPending, saveStatus, sendUpdate, update.isPending, workspaceId]);

  const changeBuffer = (change: Partial<DraftBuffer>): void => {
    setBuffer((current) => current === undefined ? current : { ...current, ...change });
    if (saveStatus !== "conflict" && saveStatus !== "retry") setSaveStatus("dirty");
  };

  const loadServerVersion = (): void => {
    if (conflictDraft === undefined) return;
    setBuffer(draftBuffer(conflictDraft));
    setConfirmedDraft(conflictDraft);
    setConflictDraft(undefined);
    setSaveError(undefined);
    setSaveStatus("saved");
    window.setTimeout(() => titleRef.current?.focus(), 0);
  };

  const continueWithLocalVersion = (): void => {
    if (conflictDraft === undefined) return;
    setConfirmedDraft(conflictDraft);
    setConflictDraft(undefined);
    setSaveError(undefined);
    setSaveStatus("dirty");
  };

  const sendFreeze = (input: FreezeWorkingDraftInput): void => {
    retryFreezeRef.current = input;
    setFreezeError(undefined);
    const commandBinding = activeBindingRef.current;
    const controller = beginCommand();
    freeze.mutate({ ...input, signal: controller.signal }, {
      onSuccess: (result) => {
        finishCommand(controller);
        if (controller.signal.aborted || getActiveWorkspaceId() !== input.workspaceId || activeBindingRef.current !== commandBinding) return;
        retryFreezeRef.current = undefined;
        setLatestFreeze(result);
        setConfirmedDraft(result.workingDraft);
        const currentBuffer = bufferRef.current;
        if (currentBuffer !== undefined && sameBuffer(currentBuffer, draftBuffer(result.workingDraft))) {
          setBuffer(draftBuffer(result.workingDraft));
          setSaveStatus("saved");
        }
      },
      onError: (error) => {
        finishCommand(controller);
        if (controller.signal.aborted || getActiveWorkspaceId() !== input.workspaceId || activeBindingRef.current !== commandBinding) return;
        setFreezeError(error);
        if (isConflict(error)) {
          enterConflict(error);
          return;
        }
        if (!deliveryIsUnknown(error)) retryFreezeRef.current = undefined;
      },
    });
  };

  const sendPublish = (input: PublishRevisionInput): void => {
    retryPublishRef.current = input;
    setPublishError(undefined);
    const commandBinding = activeBindingRef.current;
    const controller = beginCommand();
    publish.mutate({ ...input, signal: controller.signal }, {
      onSuccess: ({ publication }) => {
        finishCommand(controller);
        if (controller.signal.aborted || getActiveWorkspaceId() !== input.workspaceId || activeBindingRef.current !== commandBinding) return;
        retryPublishRef.current = undefined;
        void navigate(publication.proposalHref);
      },
      onError: (error) => {
        finishCommand(controller);
        if (controller.signal.aborted || getActiveWorkspaceId() !== input.workspaceId || activeBindingRef.current !== commandBinding) return;
        setPublishError(error);
        if (!deliveryIsUnknown(error)) retryPublishRef.current = undefined;
      },
    });
  };

  const frozenDocument = latestFreeze?.document ?? documentQuery.data?.document;
  const frozenRevision = latestFreeze?.articleRevision ?? documentQuery.data?.currentRevision ?? undefined;
  const publicationCandidate = publish.data?.publication ?? documentQuery.data?.publication ?? undefined;
  const publication = publicationCandidate?.articleRevisionId === frozenRevision?.id ? publicationCandidate : undefined;
  const documentSnapshotMissing = documentId !== "" && latestFreeze === undefined && documentQuery.data === undefined;
  const synchronized = buffer !== undefined && currentDraft !== undefined && sameBuffer(buffer, draftBuffer(currentDraft)) && saveStatus === "saved";
  const publishable = buffer !== undefined && isPublishableDraft(buffer);
  const frozenMatchesBuffer = buffer !== undefined && frozenDocument !== undefined && frozenRevision !== undefined
    && frozenDocument.title === buffer.title.trim()
    && frozenDocument.canonicalPath === buffer.targetPath
    && frozenRevision.content === canonicalMarkdown(buffer.body);
  const canFreeze = synchronized && publishable && !documentSnapshotMissing && !freeze.isPending && !publish.isPending && !frozenMatchesBuffer && currentDraft.status === "EDITING";
  const canPublish = synchronized && !documentSnapshotMissing && frozenMatchesBuffer && frozenRevision.status === "DRAFT" && publication === undefined && !freeze.isPending && !publish.isPending;
  const modelPath = useMemo(() => markdownModelPath(workspaceId, draftId), [draftId, workspaceId]);

  if (workspaceId === "") return <div className="page-stack authoring-editor-page"><PageHeader title="新建文章" /><EmptyState title="先连接 Workspace" description="连接工作区后才能保存文章。" action={<Button asChild><Link to="/workspace">连接 Workspace</Link></Button>} /></div>;
  if (invalidDraftId) return <div className="page-stack authoring-editor-page"><PageHeader title="新建文章" /><ErrorState title="草稿地址无效" description="URL 中的 Draft ID 不是规范 UUID。" /></div>;
  if (draftId === "") return <div className="page-stack authoring-editor-page"><PageHeader title="新建文章" /><div className="authoring-create-state" role={create.isError ? "alert" : "status"}>{create.isError ? <ServerCrash size={20} /> : <RefreshCw className="authoring-spin" size={20} />}<div><strong>{create.isError ? "空白草稿尚未创建" : "正在创建空白草稿"}</strong>{create.isError ? <p>{errorText(create.error)}</p> : null}</div>{create.isError ? <Button variant="secondary" onClick={startCreate} disabled={create.isPending}><RefreshCw size={15} />重试原请求</Button> : null}</div></div>;
  if (draftQuery.isError) return <div className="page-stack authoring-editor-page"><PageHeader title="新建文章" /><ErrorState title="草稿无法读取" description={errorText(draftQuery.error)} onRetry={() => { void draftQuery.refetch(); }} /></div>;
  if (draftQuery.isPending || buffer === undefined || currentDraft === undefined) return <div className="page-stack authoring-editor-page"><PageHeader title="新建文章" /><div className="authoring-create-state" role="status"><RefreshCw className="authoring-spin" size={20} /><strong>正在恢复草稿…</strong></div></div>;

  const archived = currentDraft.status === "ARCHIVED";

  return <div className="authoring-editor-page">
    <header className="authoring-editor-header">
      <Link className="authoring-back" to="/authoring"><ArrowLeft size={16} />创作台</Link>
      <div className="authoring-editor-header__actions">
        <span className={`authoring-save-state authoring-save-state--${saveStatus}`} role="status" aria-live="polite">{saveStatus === "saved" ? <Check size={14} /> : saveStatus === "conflict" || saveStatus === "invalid" ? <AlertTriangle size={14} /> : <RefreshCw className={saveStatus === "saving" ? "authoring-spin" : ""} size={14} />}{saveStatusText[saveStatus]} · v{String(currentDraft.version)}</span>
        <Button variant="secondary" onClick={() => sendFreeze({ workspaceId, draftId, expectedVersion: currentDraft.version, idempotencyKey: commandKey("working-draft-freeze") })} disabled={!canFreeze}><FileCheck2 size={16} />{freeze.isPending ? "正在保存版本…" : "保存版本"}</Button>
        <Button onClick={() => frozenDocument !== undefined && frozenRevision !== undefined && sendPublish({ workspaceId, documentId: frozenDocument.id, revisionId: frozenRevision.id, idempotencyKey: commandKey("document-publish") })} disabled={!canPublish}><Send size={16} />{publish.isPending ? "正在创建提案…" : "提交发布"}</Button>
      </div>
    </header>

    {archived ? <div className="authoring-warning"><AlertTriangle size={17} /><span>此草稿已归档，只能查看。</span></div> : null}
    {documentSnapshotMissing && documentQuery.isPending ? <div className="authoring-overview-state" role="status"><RefreshCw className="authoring-spin" size={16} />正在恢复已保存版本…</div> : null}
    {documentSnapshotMissing && documentQuery.isError ? <section className="authoring-command-error" role="alert"><div><strong>已保存版本暂时无法读取</strong><p>{errorText(documentQuery.error)}</p></div><Button variant="secondary" onClick={() => { void documentQuery.refetch(); }}><RefreshCw size={15} />重试读取版本</Button></section> : null}
    {saveStatus === "conflict" ? <section ref={conflictRef} className="authoring-conflict" role="alert" tabIndex={-1}>
      <div><strong>服务端草稿已经变化</strong><p>本地内容仍在当前编辑器中，没有被覆盖。</p></div>
      <div className="authoring-conflict__actions">{conflictDraft === undefined ? <Button variant="secondary" onClick={() => { void readConflictDraft(); }}><RefreshCw size={15} />重新读取服务端版本</Button> : <><Button variant="secondary" onClick={loadServerVersion}>载入服务端版本</Button><Button onClick={continueWithLocalVersion}>用本地内容继续</Button></>}</div>
    </section> : null}
    {saveStatus === "retry" && retrySaveRef.current !== undefined ? <section className="authoring-command-error" role="alert"><div><strong>自动保存结果未确认</strong><p>{errorText(saveError)}</p></div><Button variant="secondary" onClick={() => retrySaveRef.current !== undefined && sendUpdate(retrySaveRef.current)} disabled={update.isPending}><RefreshCw size={15} />重试原请求</Button></section> : null}
    {saveStatus === "invalid" ? <section className="authoring-command-error" role="alert"><div><strong>当前内容未保存</strong><p>{errorText(saveError)}</p></div></section> : null}
    {freezeError !== undefined && !isConflict(freezeError) ? <section className="authoring-command-error" role="alert"><div><strong>版本保存未完成</strong><p>{errorText(freezeError)}</p></div>{retryFreezeRef.current === undefined ? null : <Button variant="secondary" onClick={() => retryFreezeRef.current !== undefined && sendFreeze(retryFreezeRef.current)} disabled={freeze.isPending}><RefreshCw size={15} />重试原请求</Button>}</section> : null}
    {publishError !== undefined ? <section className="authoring-command-error" role="alert"><div><strong>发布提案未创建</strong><p>{errorText(publishError)}</p></div>{retryPublishRef.current === undefined ? null : <Button variant="secondary" onClick={() => retryPublishRef.current !== undefined && sendPublish(retryPublishRef.current)} disabled={publish.isPending}><RefreshCw size={15} />重试原请求</Button>}</section> : null}
    {publication !== undefined ? <section className="authoring-publication-state"><div><Badge tone={publication.status === "RECOVERY_REQUIRED" || publication.status === "CLOSED" ? "danger" : publication.status === "PUBLISHED" ? "success" : "info"}>{publication.status === "RECOVERY_REQUIRED" ? "待恢复" : publication.status === "CLOSED" ? "已关闭" : publication.status === "PUBLISHED" ? "已发布" : "等待确认"}</Badge><strong>{publication.targetPath}</strong></div><Button asChild variant="secondary"><Link to={publication.proposalHref}>查看提案</Link></Button></section> : frozenRevision !== undefined ? <div className="authoring-frozen-state"><Badge tone={frozenMatchesBuffer ? "success" : "warning"}>修订版本 {String(frozenRevision.revisionNo)}</Badge><span>{frozenMatchesBuffer ? "当前内容已有不可变版本" : "草稿已变化，需要保存新版本"}</span></div> : null}

    <section className="authoring-fields" aria-label="文章信息">
      <label><span>标题</span><input ref={titleRef} value={buffer.title} maxLength={512} disabled={archived} onChange={(event) => changeBuffer({ title: event.target.value })} /></label>
      <label><span>目标路径</span><input value={buffer.targetPath} maxLength={4096} disabled={archived} placeholder="notes/java-ai.md" spellCheck={false} onChange={(event) => changeBuffer({ targetPath: event.target.value })} /></label>
    </section>

    <div className="authoring-mobile-modes" role="group" aria-label="编辑视图">
      <button type="button" aria-pressed={mobileMode === "edit"} onClick={() => setMobileMode("edit")}><PencilLine size={16} />编辑</button>
      <button type="button" aria-pressed={mobileMode === "preview"} onClick={() => setMobileMode("preview")}><Eye size={16} />预览</button>
    </div>

    <section className="authoring-editor-layout" data-mobile-mode={mobileMode}>
      <div className="authoring-editor-pane">
        <header><span>Markdown</span>{editorError ? <Badge tone="danger">加载失败</Badge> : null}</header>
        {editorError ? <div className="authoring-editor-failure" role="alert"><AlertTriangle size={18} /><p>{editorError.message}</p></div> : <MonacoMarkdownEditor value={buffer.body} modelPath={modelPath} disabled={archived} onChange={(body) => changeBuffer({ body })} onError={setEditorError} />}
      </div>
      <div className="authoring-preview-pane">
        <header><span>预览</span></header>
        <MarkdownPreview markdown={buffer.body} />
      </div>
    </section>
  </div>;
};
