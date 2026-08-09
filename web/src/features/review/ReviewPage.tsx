import { BookOpenCheck, CirclePause, CirclePlay, Plus, RefreshCw, RotateCcw } from "lucide-react";
import { useRef, useState } from "react";
import { useNavigate } from "react-router-dom";

import { ReviewApiError, type CreateReviewDeckInput, type ReviewDeck, type ReviewDeckScheduleInput } from "../../api/review";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import { useCreateReviewDeck, usePauseReviewDeck, useResetReviewDeck, useResumeReviewDeck, useReviewDecks, useStartReviewSession } from "./queries";
import { ReviewCardsPanel } from "./ReviewCardsPanel";

interface AttemptStore { current: Map<string, string>; }

const commandKey = (prefix: string, signature: string, attempts: AttemptStore): string => {
  const existing = attempts.current.get(signature);
  if (existing !== undefined) return existing;
  const key = `${prefix}-${crypto.randomUUID()}`;
  attempts.current.set(signature, key);
  return key;
};
const errorText = (error: unknown): string => error instanceof Error ? error.message : "请求未完成，请重试。";
const deckTone = (status: ReviewDeck["status"]): "success" | "warning" | "neutral" => status === "ACTIVE" ? "success" : status === "PAUSED" ? "warning" : "neutral";
const deckStatusLabels: Record<ReviewDeck["status"], string> = { ACTIVE: "进行中", PAUSED: "已暂停", ARCHIVED: "已归档" };
const isVersionConflict = (error: unknown): boolean => error instanceof ReviewApiError && error.status === 409;
const isRetryableReviewError = (error: unknown): error is ReviewApiError => error instanceof ReviewApiError && error.retryable;

const DeckActions = ({
  deck,
  onStart,
  onSchedule,
  pending,
}: {
  deck: ReviewDeck;
  onStart: (deck: ReviewDeck) => void;
  onSchedule: (deck: ReviewDeck, action: "pause" | "resume" | "reset") => void;
  pending: boolean;
}) => <div className="button-row">
  <Button size="sm" onClick={() => onStart(deck)} disabled={deck.status !== "ACTIVE" || pending} title={deck.status === "ACTIVE" ? undefined : "只有启用的复习卡组可以开始复习"}>
    <BookOpenCheck size={15} />开始复习
  </Button>
  {deck.status === "ACTIVE" ? <Button size="sm" variant="secondary" onClick={() => onSchedule(deck, "pause")} disabled={pending}><CirclePause size={15} />暂停</Button> : null}
  {deck.status === "PAUSED" ? <Button size="sm" variant="secondary" onClick={() => onSchedule(deck, "resume")} disabled={pending}><CirclePlay size={15} />恢复</Button> : null}
  <Button size="sm" variant="ghost" onClick={() => onSchedule(deck, "reset")} disabled={deck.status === "ARCHIVED" || pending} title={deck.status === "ARCHIVED" ? "已归档卡组不可重置调度" : undefined}><RotateCcw size={15} />重置调度</Button>
</div>;

export const ReviewPage = () => {
  const navigate = useNavigate();
  const workspaceId = useActiveWorkspaceId();
  const decks = useReviewDecks();
  const create = useCreateReviewDeck();
  const start = useStartReviewSession();
  const pause = usePauseReviewDeck();
  const resume = useResumeReviewDeck();
  const reset = useResetReviewDeck();
  const createAttempts = useRef<AttemptStore>({ current: new Map() });
  const scheduleAttempts = useRef<AttemptStore>({ current: new Map() });
  const sessionAttempts = useRef<AttemptStore>({ current: new Map() });
  const [name, setName] = useState("");
  const [dailyLimit, setDailyLimit] = useState("20");
  const [activeDeckId, setActiveDeckId] = useState("");

  const scheduleMutation = pause.isPending ? pause : resume.isPending ? resume : reset.isPending ? reset : undefined;
  const scheduleError = pause.error ?? resume.error ?? reset.error;
  const schedulePending = scheduleMutation !== undefined;
  const createInput = (): CreateReviewDeckInput | undefined => {
    const limit = Number(dailyLimit);
    if (workspaceId === "" || name.trim() === "" || !Number.isSafeInteger(limit) || limit < 1 || limit > 1000) return undefined;
    const signature = JSON.stringify({ workspaceId, name: name.trim(), dailyLimit: limit });
    return { workspaceId, name: name.trim(), scope: {}, dailyLimit: limit, idempotencyKey: commandKey("review-deck-create", signature, createAttempts.current) };
  };
  const submitCreate = (): void => {
    const input = createInput();
    if (input === undefined) return;
    create.mutate(input, { onSuccess: () => { createAttempts.current.current.clear(); setName(""); } });
  };
  const startSession = (deck: ReviewDeck): void => {
    if (workspaceId === "") return;
    setActiveDeckId(deck.id);
    const signature = `${workspaceId}:${deck.id}:REVIEW`;
    start.mutate({ workspaceId, deckId: deck.id, config: {}, idempotencyKey: commandKey("review-session-start", signature, sessionAttempts.current) }, {
      onSuccess: (session) => {
        sessionAttempts.current.current.delete(signature);
        setActiveDeckId("");
        const query = new URLSearchParams({ deck: deck.id, session: session.id });
        void navigate(`/review/session?${query}`);
      },
      onError: () => setActiveDeckId(""),
    });
  };
  const runSchedule = (deck: ReviewDeck, action: "pause" | "resume" | "reset"): void => {
    if (workspaceId === "") return;
    const signature = `${workspaceId}:${deck.id}:${String(deck.version)}:${action}`;
    const input: ReviewDeckScheduleInput = {
      workspaceId,
      deckId: deck.id,
      expectedVersion: deck.version,
      idempotencyKey: commandKey(`review-deck-${action}`, signature, scheduleAttempts.current),
    };
    const mutation = action === "pause" ? pause : action === "resume" ? resume : reset;
    mutation.mutate(input, { onSuccess: () => scheduleAttempts.current.current.delete(signature) });
  };
  const retryCreate = (): void => {
    if (create.variables !== undefined) create.mutate(create.variables);
  };
  const retrySchedule = (): void => {
    if (pause.variables !== undefined) pause.mutate(pause.variables);
    else if (resume.variables !== undefined) resume.mutate(resume.variables);
    else if (reset.variables !== undefined) reset.mutate(reset.variables);
  };
  const createInvalid = name.trim() === "" || !Number.isSafeInteger(Number(dailyLimit)) || Number(dailyLimit) < 1 || Number(dailyLimit) > 1000;

  if (workspaceId === "") return <div className="page-stack"><UnavailableState title="请选择 Workspace" description="复习卡组始终在单一 Workspace 内创建和复习。" /></div>;

  return <div className="page-stack">
    <div className="page-intro page-intro--split">
      <div>
        <h1>复习</h1>
        <p>管理复习卡组并开始主动回忆。</p>
      </div>
      <div className="folio-mark"><BookOpenCheck size={20} /><strong>{decks.data?.items.length ?? "—"}</strong><span>个卡组</span></div>
    </div>

    <Card>
      <CardHeader eyebrow="新建卡组" title="创建复习卡组" description="每日上限、暂停、恢复和重置均由服务端调度事实决定。" />
      <form className="artifact-form" onSubmit={(event) => { event.preventDefault(); submitCreate(); }}>
        <label>卡组名称<input value={name} maxLength={256} onChange={(event) => setName(event.target.value)} placeholder="例如：Go 并发基础" /></label>
        <label>每日上限<input type="number" min="1" max="1000" value={dailyLimit} onChange={(event) => setDailyLimit(event.target.value)} /></label>
        <div className="artifact-form__wide button-row"><Button type="submit" disabled={createInvalid || create.isPending}><Plus size={15} />{create.isPending ? "正在创建…" : "创建卡组"}</Button><span className="sidebar-note">范围固定为当前 Workspace；卡片仍须经过服务端证据校验与审批后才会进入复习队列。</span></div>
      </form>
      {create.isError ? <div className="ui-state ui-state--error" role="alert"><strong>卡组创建失败</strong><p>{errorText(create.error)}</p>{isRetryableReviewError(create.error) ? <Button variant="secondary" onClick={retryCreate}><RotateCcw size={15} />重试原请求</Button> : null}</div> : null}
    </Card>

    <Card>
      <CardHeader eyebrow="当前 Workspace" title="复习卡组" description="今天的待复习数由卡组上限、卡片状态和当前调度共同计算。" action={<Button variant="ghost" onClick={() => void decks.refetch()} disabled={decks.isFetching} aria-label="刷新卡组列表"><RefreshCw size={16} /></Button>} />
      {decks.isPending ? <div className="ui-state" role="status"><strong>正在读取卡组</strong><p>正在加载当前 Workspace 的复习配置。</p></div> : null}
      {decks.isError ? <ErrorState title={isVersionConflict(decks.error) ? "卡组版本已变化" : "卡组列表不可用"} description={errorText(decks.error)} onRetry={() => void decks.refetch()} /> : null}
      {!decks.isPending && !decks.isError && decks.data.items.length === 0 ? <EmptyState title="还没有复习卡组" description="先创建一个卡组，再由服务端验证并审批卡片。" /> : null}
      {!decks.isPending && !decks.isError && decks.data.items.length > 0 ? <div className="collection-definition-list">{decks.data.items.map((deck) => <article className="collection-definition-row" key={deck.id}>
        <div><strong>{deck.name}</strong><small>每日上限 {String(deck.dailyLimit)} · 调度器 {deck.schedulerVersion} · 配置版本 {String(deck.version)}</small></div>
        <div><Badge tone={deckTone(deck.status)}>{deckStatusLabels[deck.status]}</Badge><DeckActions deck={deck} onStart={startSession} onSchedule={runSchedule} pending={schedulePending || (start.isPending && activeDeckId === deck.id)} /></div>
      </article>)}</div> : null}
      {scheduleError !== null ? <div className="ui-state ui-state--error" role="alert"><strong>{isVersionConflict(scheduleError) ? "卡组已在其他位置更新" : "卡组调度操作失败"}</strong><p>{errorText(scheduleError)}</p><div className="button-row"><Button variant="secondary" onClick={() => { void decks.refetch(); }}><RefreshCw size={15} />刷新卡组</Button>{isRetryableReviewError(scheduleError) ? <Button variant="secondary" onClick={retrySchedule}><RotateCcw size={15} />重试原请求</Button> : null}</div></div> : null}
      {start.isError ? <div className="ui-state ui-state--error" role="alert"><strong>{isVersionConflict(start.error) ? "无法开始当前复习" : "创建复习会话失败"}</strong><p>{errorText(start.error)}</p>{isRetryableReviewError(start.error) ? <Button variant="secondary" onClick={() => start.mutate(start.variables)}><RotateCcw size={15} />重试原请求</Button> : null}</div> : null}
    </Card>

    <ReviewCardsPanel decks={decks.data?.items ?? []} />
  </div>;
};
