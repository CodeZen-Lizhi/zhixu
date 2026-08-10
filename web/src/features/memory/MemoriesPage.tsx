import { CheckCircle2, CirclePause, CirclePlay, Pencil, Plus, RefreshCw, RotateCcw, Trash2 } from "lucide-react";
import { useMemo, useRef, useState } from "react";

import { MemoryApiError, parseMemoryJsonObject, type CreateMemoryCandidateInput, type EditMemoryInput, type MemoryRecord, type MemorySourceType, type MemoryStatus, type MemoryType, type TransitionMemoryInput } from "../../api/memory";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { isCanonicalUuid } from "../../shared/codec";
import { Badge, Button, Card, CardHeader, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import { useConfirmMemory, useCreateMemoryCandidate, useDeleteMemory, useEditMemory, useMemories, usePauseMemory, useResumeMemory } from "./queries";

const memoryTypes: MemoryType[] = ["PREFERENCE", "EPISODIC", "GOAL", "FEEDBACK"];
const memoryStatuses: MemoryStatus[] = ["CANDIDATE", "ACTIVE", "PAUSED", "EXPIRED", "DELETED"];
const memoryTypeLabels: Record<MemoryType, string> = { PREFERENCE: "偏好", EPISODIC: "经历", GOAL: "目标", FEEDBACK: "反馈" };
const memoryStatusLabels: Record<MemoryStatus, string> = { CANDIDATE: "候选", ACTIVE: "已启用", PAUSED: "已暂停", EXPIRED: "已过期", DELETED: "已删除" };
const memorySourceLabels: Record<MemorySourceType, string> = { USER: "用户", AGENT: "助手", INTERVIEW: "访谈" };
const statusTone = (status: MemoryStatus): "success" | "warning" | "danger" | "neutral" | "info" => status === "ACTIVE" ? "success" : status === "CANDIDATE" ? "info" : status === "PAUSED" ? "warning" : status === "DELETED" ? "danger" : "neutral";
const errorText = (error: unknown): string => error instanceof Error ? error.message : "请求未完成，请重试。";
const retryable = (error: unknown): error is MemoryApiError => error instanceof MemoryApiError && error.retryable;
const contentText = (content: MemoryRecord["content"]): string => JSON.stringify(content, null, 2);
const datetimeValue = (value: string | undefined): string => value === undefined ? "" : value.slice(0, 16);
const isoTime = (value: string): string | undefined => {
  if (value === "") return undefined;
  const date = new Date(value);
  return Number.isFinite(date.getTime()) ? date.toISOString() : undefined;
};

interface AttemptStore { current: Map<string, string>; }
const commandKey = (prefix: string, signature: string, attempts: AttemptStore): string => {
  const existing = attempts.current.get(signature);
  if (existing !== undefined) return existing;
  const key = `${prefix}-${crypto.randomUUID()}`;
  attempts.current.set(signature, key);
  return key;
};

const MemoryForm = ({ item, onSave, pending }: { item?: MemoryRecord; onSave: (input: Omit<CreateMemoryCandidateInput, "workspaceId" | "idempotencyKey">) => void; pending: boolean }) => {
  const [type, setType] = useState<MemoryType>(item?.type ?? "PREFERENCE");
  const [content, setContent] = useState(item === undefined ? "{\n  \"text\": \"\"\n}" : contentText(item.content));
  const [taskScopeId, setTaskScopeId] = useState(item?.taskScopeId ?? "");
  const [expiresAt, setExpiresAt] = useState(datetimeValue(item?.expiresAt));
  const parsed = parseMemoryJsonObject(content);
  const expiry = isoTime(expiresAt);
  const invalid = parsed === undefined || (taskScopeId !== "" && !isCanonicalUuid(taskScopeId)) || (type === "EPISODIC" && expiry === undefined) || (expiresAt !== "" && expiry === undefined);
  return <form className="artifact-form" onSubmit={(event) => {
    event.preventDefault();
    if (invalid) return;
    onSave({ type, content: parsed, ...(taskScopeId === "" ? {} : { taskScopeId }), ...(expiry === undefined ? {} : { expiresAt: expiry }) });
  }}>
    <label>类型<select value={type} disabled={item !== undefined} onChange={(event) => setType(event.target.value as MemoryType)}>{memoryTypes.map((value) => <option key={value} value={value}>{memoryTypeLabels[value]}</option>)}</select></label>
    <label>任务范围 ID（可选）<input value={taskScopeId} onChange={(event) => setTaskScopeId(event.target.value)} placeholder="仅对该任务生效" /></label>
    <label>到期时间（可选）<input type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} /></label>
    <label className="artifact-form__wide">记忆内容（JSON 对象）<textarea value={content} onChange={(event) => setContent(event.target.value)} rows={7} /></label>
    <div className="artifact-form__wide button-row"><Button type="submit" disabled={invalid || pending}>{item === undefined ? <Plus size={15} /> : <Pencil size={15} />}{pending ? "正在保存…" : item === undefined ? "创建候选" : "保存修改"}</Button><span className="sidebar-note">候选记忆在确认前不会进入 Agent 上下文；身份由当前认证会话决定。</span></div>
  </form>;
};

export const MemoriesPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [types, setTypes] = useState<MemoryType[]>([]);
  const [statuses, setStatuses] = useState<MemoryStatus[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [editing, setEditing] = useState<MemoryRecord>();
  const create = useCreateMemoryCandidate();
  const edit = useEditMemory();
  const confirm = useConfirmMemory();
  const pause = usePauseMemory();
  const resume = useResumeMemory();
  const remove = useDeleteMemory();
  const memories = useMemories({ types, statuses, ...(cursor === undefined ? {} : { cursor }), limit: 50 });
  const attempts = useRef<AttemptStore>({ current: new Map() });
  const busy = create.isPending || edit.isPending || confirm.isPending || pause.isPending || resume.isPending || remove.isPending;
  const commandError = create.error ?? edit.error ?? confirm.error ?? pause.error ?? resume.error ?? remove.error;
  const commandVariables = create.variables ?? edit.variables ?? confirm.variables ?? pause.variables ?? resume.variables ?? remove.variables;

  const toggle = <T extends string,>(value: T, values: T[], setValues: (next: T[]) => void): void => setValues(values.includes(value) ? values.filter((item) => item !== value) : [...values, value]);
  const createCandidate = (input: Omit<CreateMemoryCandidateInput, "workspaceId" | "idempotencyKey">): void => {
    if (workspaceId === "") return;
    const signature = JSON.stringify({ workspaceId, ...input });
    create.mutate({ ...input, workspaceId, idempotencyKey: commandKey("memory-create", signature, attempts.current) }, { onSuccess: () => attempts.current.current.delete(signature) });
  };
  const saveEdit = (item: MemoryRecord, input: Omit<CreateMemoryCandidateInput, "workspaceId" | "idempotencyKey">): void => {
    if (workspaceId === "") return;
    const mutable = { content: input.content, ...(input.taskScopeId === undefined ? {} : { taskScopeId: input.taskScopeId }), ...(input.expiresAt === undefined ? {} : { expiresAt: input.expiresAt }) };
    const signature = JSON.stringify({ workspaceId, memoryId: item.id, version: item.version, ...mutable });
    const command: EditMemoryInput = { ...mutable, workspaceId, memoryId: item.id, expectedVersion: item.version, idempotencyKey: commandKey("memory-edit", signature, attempts.current) };
    edit.mutate(command, { onSuccess: () => { attempts.current.current.delete(signature); setEditing(undefined); } });
  };
  const transition = (item: MemoryRecord, action: "confirm" | "pause" | "resume" | "delete"): void => {
    if (workspaceId === "") return;
    const signature = `${workspaceId}:${item.id}:${String(item.version)}:${action}`;
    const command: TransitionMemoryInput = { workspaceId, memoryId: item.id, expectedVersion: item.version, idempotencyKey: commandKey(`memory-${action}`, signature, attempts.current) };
    const mutation = action === "confirm" ? confirm : action === "pause" ? pause : action === "resume" ? resume : remove;
    mutation.mutate(command, { onSuccess: () => attempts.current.current.delete(signature) });
  };
  const retry = (): void => {
    if (create.variables !== undefined) create.mutate(create.variables);
    else if (edit.variables !== undefined) edit.mutate(edit.variables);
    else if (confirm.variables !== undefined) confirm.mutate(confirm.variables);
    else if (pause.variables !== undefined) pause.mutate(pause.variables);
    else if (resume.variables !== undefined) resume.mutate(resume.variables);
    else if (remove.variables !== undefined) remove.mutate(remove.variables);
  };
  const title = useMemo(() => editing === undefined ? "创建候选记忆" : `编辑 ${editing.id.slice(0, 8)}…`, [editing]);

  if (workspaceId === "") return <div className="page-stack"><UnavailableState title="请选择 Workspace" description="记忆只属于当前 Workspace，浏览器不能指定或伪造归属者。" /></div>;
  return <div className="page-stack">
    <div className="page-intro page-intro--split"><div><h1>记忆</h1><p>管理需要明确确认的长期上下文。</p></div><div className="folio-mark"><strong>{String(memories.data?.items.length ?? 0)}</strong><span>条记忆</span></div></div>
    <Card><CardHeader eyebrow="候选记忆" title={title} description="只有明确确认后才会进入已启用状态；编辑、暂停、删除均保留服务端审计历史。" {...(editing === undefined ? {} : { action: <Button variant="ghost" onClick={() => setEditing(undefined)}>取消编辑</Button> })} /><MemoryForm key={editing?.id ?? "new"} {...(editing === undefined ? {} : { item: editing })} pending={create.isPending || edit.isPending} onSave={(input) => editing === undefined ? createCandidate(input) : saveEdit(editing, input)} />{commandError !== null ? <div className="ui-state ui-state--error" role="alert"><strong>记忆操作未完成</strong><p>{errorText(commandError)}</p>{retryable(commandError) && commandVariables !== undefined ? <Button variant="secondary" onClick={retry}><RotateCcw size={15} />重试原请求</Button> : null}</div> : null}</Card>
    <Card><CardHeader eyebrow="当前用户范围列表" title="记忆生命周期" description="筛选只影响当前 Workspace 的服务端读取；暂停、到期和删除记录不会进入上下文。" action={<Button variant="ghost" aria-label="刷新记忆列表" onClick={() => void memories.refetch()} disabled={memories.isFetching}><RefreshCw size={16} /></Button>} />
      <div className="filter-bar"><fieldset><legend>类型</legend>{memoryTypes.map((value) => <label key={value}><input type="checkbox" checked={types.includes(value)} onChange={() => { setCursor(undefined); toggle(value, types, setTypes); }} />{memoryTypeLabels[value]}</label>)}</fieldset><fieldset><legend>状态</legend>{memoryStatuses.map((value) => <label key={value}><input type="checkbox" checked={statuses.includes(value)} onChange={() => { setCursor(undefined); toggle(value, statuses, setStatuses); }} />{memoryStatusLabels[value]}</label>)}</fieldset></div>
      {memories.isPending ? <div className="ui-state" role="status"><strong>正在读取记忆</strong><p>正在读取当前认证用户在此 Workspace 中的生命周期记录。</p></div> : null}
      {memories.isError ? <ErrorState title="记忆列表不可用" description={errorText(memories.error)} onRetry={() => void memories.refetch()} /> : null}
      {!memories.isPending && !memories.isError && memories.data.items.length === 0 ? <EmptyState title="没有匹配的记忆" description="创建候选记忆后，确认前不会用于任何 Agent 上下文。" /> : null}
      {!memories.isPending && !memories.isError && memories.data.items.length > 0 ? <div className="collection-definition-list">{memories.data.items.map((item) => <article className="collection-definition-row" key={item.id}><div><strong>{memoryTypeLabels[item.type]}</strong><small>来源 {memorySourceLabels[item.source.type]} · {item.source.ref} · v{item.version}{item.expiresAt === undefined ? "" : ` · 到期 ${new Date(item.expiresAt).toLocaleString("zh-CN")}`}</small><pre className="json-preview">{contentText(item.content)}</pre></div><div><Badge tone={statusTone(item.status)}>{memoryStatusLabels[item.status]}</Badge><div className="button-row">{item.status === "CANDIDATE" ? <Button size="sm" onClick={() => transition(item, "confirm")} disabled={busy}><CheckCircle2 size={15} />确认</Button> : null}{item.status === "ACTIVE" ? <Button size="sm" variant="secondary" onClick={() => transition(item, "pause")} disabled={busy}><CirclePause size={15} />暂停</Button> : null}{item.status === "PAUSED" ? <Button size="sm" variant="secondary" onClick={() => transition(item, "resume")} disabled={busy}><CirclePlay size={15} />恢复</Button> : null}{(item.status === "CANDIDATE" || item.status === "ACTIVE" || item.status === "PAUSED") ? <Button size="sm" variant="ghost" onClick={() => setEditing(item)} disabled={busy}><Pencil size={15} />编辑</Button> : null}{item.status !== "DELETED" && item.status !== "EXPIRED" ? <Button size="sm" variant="ghost" onClick={() => transition(item, "delete")} disabled={busy}><Trash2 size={15} />删除</Button> : null}</div></div></article>)}</div> : null}
      <div className="pagination-row"><span className="sidebar-note">当前页 {String(memories.data?.items.length ?? 0)} 条</span><div className="button-row">{cursor !== undefined ? <Button variant="ghost" onClick={() => setCursor(undefined)}>回到首屏</Button> : null}{memories.data?.nextCursor ? <Button variant="secondary" onClick={() => setCursor(memories.data.nextCursor)}>下一页</Button> : null}</div></div>
    </Card>
  </div>;
};
