import { type KeyboardEvent, type SyntheticEvent, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";

import { useActiveWorkspaceId } from "../../app/active-workspace";
import type { Answer, AnswerCitation, FeedbackType, Turn } from "../../api/conversation";
import { ConversationApiError } from "../../api/conversation";
import { createCommandId, useCreateConversationCommand, useSubmitFeedbackCommand, useSubmitQuestionCommand } from "./commands";
import { useWorkspaceEventState } from "../../events/event-store";
import { SourceSpanViewer } from "../source-spans";
import { useAnswer, useConversation, useConversationList, useConversationTurns, useLatestTurn } from "./queries";

const stageLabels: Record<string, string> = {
  "plan.started": "正在理解问题", "plan.completed": "检索计划已冻结",
  "retrieval.started": "正在检索证据", "retrieval.completed": "证据检索完成",
  "validation.started": "正在校验引用", "validation.completed": "回答校验完成",
};

const formatTime = (value: string) => new Intl.DateTimeFormat("zh-CN", {
  month: "short", day: "numeric", hour: "2-digit", minute: "2-digit",
}).format(new Date(value));

const stageRank = (answer: Answer): number => answer.currentStage === null ? 0 : [
  "plan.started", "plan.completed", "retrieval.started", "retrieval.completed", "validation.started", "validation.completed",
].indexOf(answer.currentStage) + 1;

const fresherAnswer = (left: Answer, right: Answer): Answer => {
  const leftTerminal = left.publicationStatus !== "pending";
  const rightTerminal = right.publicationStatus !== "pending";
  if (leftTerminal !== rightTerminal) return rightTerminal ? right : left;
  if (left.version !== right.version) return right.version > left.version ? right : left;
  if (left.workflow.version !== right.workflow.version) return right.workflow.version > left.workflow.version ? right : left;
  if (!leftTerminal && stageRank(left) !== stageRank(right)) return stageRank(right) > stageRank(left) ? right : left;
  return Date.parse(right.updatedAt) > Date.parse(left.updatedAt) ? right : left;
};

export const mergeLatestTurn = (items: Turn[], latest: Turn | null | undefined): Turn[] => {
  if (latest === null || latest === undefined) return items;
  const replaced = items.map((item) => item.question.id === latest.question.id
    ? { question: item.question, answer: fresherAnswer(item.answer, latest.answer) }
    : item);
  if (!items.some((item) => item.question.id === latest.question.id)) replaced.push(latest);
  return replaced.sort((left, right) => left.question.ordinal - right.question.ordinal);
};

const ErrorNotice = ({ error }: { error: Error }) => (
  <div className="rag-notice rag-notice--error" role="alert">
    <strong>请求未完成</strong><span>{error.message}</span>
    {error instanceof ConversationApiError ? <code>{error.errorCode}</code> : null}
  </div>
);

const WorkflowStage = ({ answer }: { answer: Answer }) => {
  const stage = answer.currentStage === null ? "等待执行器接手" : stageLabels[answer.currentStage];
  return (
    <div className="rag-stage" aria-live="polite">
      <span className="rag-stage__pulse" aria-hidden="true" />
      <div><strong>{stage}</strong><small>Workflow · {answer.workflow.status}</small></div>
    </div>
  );
};

const CitationList = ({ citations, onSelect }: { citations: AnswerCitation[]; onSelect: (citation: AnswerCitation) => void }) => (
  <ol className="rag-citations">
    {citations.map((citation, index) => (
      <li key={citation.id}>
        <button type="button" className="rag-citation-link" onClick={() => onSelect(citation)}>
          <span>{String(index + 1).padStart(2, "0")}</span>打开段落证据
        </button>
      </li>
    ))}
  </ol>
);

const RetrievalSummary = ({ answer }: { answer: Exclude<Answer, { publicationStatus: "pending" }> }) => (
  <details className="rag-details">
    <summary>为什么这样回答</summary>
    <dl className="rag-metrics">
      <div><dt>模式</dt><dd>{answer.retrievalSummary.requestedMode} → {answer.retrievalSummary.effectiveMode}</dd></div>
      <div><dt>候选</dt><dd>{answer.retrievalSummary.candidateCount}</dd></div>
      <div><dt>采用</dt><dd>{answer.retrievalSummary.selectedCount}</dd></div>
      <div><dt>冲突</dt><dd>{answer.retrievalSummary.conflictCount}</dd></div>
    </dl>
    {answer.retrievalSummary.rewrites.length > 0 ? <ul>{answer.retrievalSummary.rewrites.map((item) => <li key={item}>{item}</li>)}</ul> : null}
    {answer.retrievalSummary.degradations.map((item) => <p className="rag-degradation" key={`${item.capability}:${item.code}`}>退化：{item.capability} · {item.code}</p>)}
  </details>
);

const FeedbackForm = ({ answer }: { answer: Exclude<Answer, { publicationStatus: "pending" }> }) => {
  const mutation = useSubmitFeedbackCommand();
  const [feedbackType, setFeedbackType] = useState<FeedbackType>("helpful");
  const [citationId, setCitationId] = useState("");
  const [comment, setComment] = useState("");
  const [commandId, setCommandId] = useState(() => createCommandId());
  const needsCitation = feedbackType === "irrelevant_citation" || feedbackType === "broken_citation";
  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    mutation.mutate({
      workspaceId: answer.workspaceId, answerId: answer.id, idempotencyKey: commandId, feedbackType,
      citationId: needsCitation ? citationId : null, comment: comment.trim() === "" ? null : comment.trim(),
    }, { onSuccess: () => setCommandId(createCommandId()) });
  };
  return (
    <form className="rag-feedback" onSubmit={submit}>
      <label><span>反馈</span><select value={feedbackType} onChange={(event) => setFeedbackType(event.target.value as FeedbackType)}>
        <option value="helpful">有帮助</option><option value="incorrect">内容不正确</option>
        <option value="irrelevant_citation">引用不相关</option><option value="broken_citation">引用无法打开</option>
        <option value="missing_source">缺少来源</option>
      </select></label>
      {needsCitation ? <label><span>引用</span><select required value={citationId} onChange={(event) => setCitationId(event.target.value)}>
        <option value="">选择引用</option>{answer.citations.map((citation, index) => <option key={citation.id} value={citation.id}>引用 {index + 1}</option>)}
      </select></label> : null}
      <label className="rag-feedback__comment"><span>补充说明（可选）</span><input maxLength={2048} value={comment} onChange={(event) => setComment(event.target.value)} /></label>
      <button type="submit" disabled={mutation.isPending}>{mutation.isPending ? "正在记录…" : "提交反馈"}</button>
      {mutation.isSuccess ? <span role="status">反馈已记录，不会直接修改正式知识。</span> : null}
      {mutation.isError ? <ErrorNotice error={mutation.error} /> : null}
    </form>
  );
};

export const AnswerPublication = ({ answer, onCitation, onPrompt = () => undefined }: { answer: Answer; onCitation: (citation: AnswerCitation) => void; onPrompt?: (prompt: string) => void }) => {
  if (answer.publicationStatus === "pending") return <WorkflowStage answer={answer} />;
  if (answer.publicationStatus === "refused") return <article className="rag-answer rag-answer--refused">
    <p className="eyebrow">明确拒答</p><h3>现有证据不足以安全回答</h3><p>{answer.result.payload.summary}</p>
    {answer.result.payload.missingRequirements.length > 0 ? <ul>{answer.result.payload.missingRequirements.map((item) => <li key={item}>{item}</li>)}</ul> : null}
    <RetrievalSummary answer={answer} /><FeedbackForm answer={answer} />
  </article>;
  if (answer.publicationStatus === "clarification_required") return <article className="rag-answer rag-answer--clarification">
    <p className="eyebrow">需要澄清</p><h3>{answer.result.payload.question}</h3><p>{answer.result.payload.reason}</p>
    {answer.result.payload.suggestedScopes.length > 0 ? <ul>{answer.result.payload.suggestedScopes.map((item) => <li key={item}><button type="button" className="rag-prompt-link" onClick={() => onPrompt(item)}>{item}</button></li>)}</ul> : null}
    <RetrievalSummary answer={answer} />
  </article>;
  return <article className="rag-answer">
    <p className="eyebrow">已校验回答</p><div className="rag-answer__copy">{answer.assistantText}</div>
    {answer.result.payload.assertions.some((item) => item.kind === "MODEL_INFERENCE") ? <p className="rag-inference">◇ 回答包含已明确标记的模型推断。</p> : null}
    {answer.result.payload.conflictPositions.length > 0 ? <details className="rag-details rag-conflicts" open><summary>来源存在 {answer.result.payload.conflictPositions.length} 个不同观点</summary>
      {answer.result.payload.conflictPositions.map((position) => <article key={`${position.claimId}:${position.position}`}><strong>{position.position}</strong><p>更新时间：{formatTime(position.updatedAt)}</p><code>{JSON.stringify(position.applicability)}</code></article>)}</details> : null}
    <CitationList citations={answer.citations} onSelect={onCitation} />
    <RetrievalSummary answer={answer} />
    {answer.result.payload.relatedTopics.length > 0 ? <div className="rag-topics"><strong>相关主题</strong>{answer.result.payload.relatedTopics.map((topic) => <span key={topic.topicId}>{topic.name}</span>)}</div> : null}
    {answer.result.payload.followUpQuestions.length > 0 ? <div className="rag-followups"><strong>继续追问</strong><ul>{answer.result.payload.followUpQuestions.map((item) => <li key={item}><button type="button" className="rag-prompt-link" onClick={() => onPrompt(item)}>{item}</button></li>)}</ul></div> : null}
    <FeedbackForm answer={answer} />
  </article>;
};

const TurnCard = ({ turn, answer, onCitation, onPrompt }: { turn: Turn; answer: Answer; onCitation: (citation: AnswerCitation) => void; onPrompt: (prompt: string) => void }) => (
  <section className="rag-turn" aria-labelledby={`question-${turn.question.id}`}>
    <div className="rag-question"><span>Q{turn.question.ordinal}</span><div><h2 id={`question-${turn.question.id}`}>{turn.question.question}</h2><small>{formatTime(turn.question.createdAt)}</small></div></div>
    <AnswerPublication answer={answer} onCitation={onCitation} onPrompt={onPrompt} />
  </section>
);

export const CitationInspector = ({ citation, onClose }: { citation: AnswerCitation | undefined; onClose: () => void }) => (
  citation === undefined ? <><h2>引用会在这里打开</h2><p>选择回答中的编号引用，查看服务端提供的真实段落链接。</p></> : <><h2>已选择引用</h2><dl><div><dt>Source Version</dt><dd>{citation.sourceVersionId}</dd></div><div><dt>Chunk</dt><dd>{citation.chunkId}</dd></div></dl><SourceSpanViewer className="secondary-button" label="打开段落证据" reference={{ workspaceId: citation.workspaceId, sourceVersionId: citation.sourceVersionId, sourceSpanId: citation.sourceSpanId }} /><button className="secondary-button" type="button" onClick={onClose}>关闭引用</button></>
);

export const RagPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const { conversationId = "" } = useParams();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const listQuery = useConversationList(workspaceId);
  const conversationQuery = useConversation(workspaceId, conversationId);
  const turnsQuery = useConversationTurns(workspaceId, conversationId);
  const latestTurnQuery = useLatestTurn(workspaceId, conversationId);
  const connectionState = useWorkspaceEventState();
  const createMutation = useCreateConversationCommand();
  const questionMutation = useSubmitQuestionCommand();
  const [title, setTitle] = useState("");
  const [createCommandKey, setCreateCommandKey] = useState(() => createCommandId());
  const [question, setQuestion] = useState("");
  const [retrievalMode, setRetrievalMode] = useState<"keyword" | "semantic" | "hybrid">("hybrid");
  const [sourceIds, setSourceIds] = useState("");
  const [sourceVersionIds, setSourceVersionIds] = useState("");
  const [pathPrefixes, setPathPrefixes] = useState("");
  const [capturedAtFrom, setCapturedAtFrom] = useState("");
  const [capturedAtBefore, setCapturedAtBefore] = useState("");
  const [allowOriginalSources, setAllowOriginalSources] = useState(false);
  const [allowWeb, setAllowWeb] = useState(false);
  const [answerDepth, setAnswerDepth] = useState<"concise" | "standard" | "detailed">("standard");
  const [outputFormat, setOutputFormat] = useState<"markdown" | "outline">("markdown");
  const [questionCommandId, setQuestionCommandId] = useState(() => createCommandId());
  const conversations = useMemo(() => listQuery.data?.pages.flatMap((page) => page.items) ?? [], [listQuery.data]);
  const turns = useMemo(() => {
    const items = turnsQuery.data?.pages.flatMap((page) => page.items) ?? [];
    return mergeLatestTurn(items, latestTurnQuery.data);
  }, [latestTurnQuery.data, turnsQuery.data]);
  const lastPendingId = [...turns].reverse().find((turn) => turn.answer.publicationStatus === "pending")?.answer.id ?? "";
  const recoveredAnswer = useAnswer(workspaceId, lastPendingId, connectionState !== "open");
  const selectedCitationId = searchParams.get("citation");
  const selectedCitation = useMemo(() => turns.flatMap((turn) => turn.answer.citations).find((item) => item.id === selectedCitationId), [selectedCitationId, turns]);
  const evidenceRef = useRef<HTMLElement>(null);
  const citationTriggerRef = useRef<HTMLElement | null>(null);
  useEffect(() => { if (selectedCitation !== undefined) evidenceRef.current?.focus(); }, [selectedCitation]);
  const create = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    createMutation.mutate({ workspaceId, idempotencyKey: createCommandKey, title: title.trim() === "" ? null : title.trim() }, {
      onSuccess: ({ resource }) => { setCreateCommandKey(createCommandId()); if (resource !== null) void navigate(`/chat/${resource.id}`); },
    });
  };
  const submitQuestionForm = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (question.trim() === "" || conversationId === "") return;
    const split = (value: string) => value.split(/[\n,]/).map((item) => item.trim()).filter((item) => item !== "");
    questionMutation.mutate({ workspaceId, conversationId, idempotencyKey: questionCommandId, question: question.trim(), answerDepth, outputFormat,
      scope: { retrievalMode, sourceIds: split(sourceIds), sourceVersionIds: split(sourceVersionIds), pathPrefixes: split(pathPrefixes), capturedAtFrom: capturedAtFrom === "" ? null : new Date(capturedAtFrom).toISOString(), capturedAtBefore: capturedAtBefore === "" ? null : new Date(capturedAtBefore).toISOString(), allowOriginalSources, allowWeb },
    }, {
      onSuccess: () => { setQuestion(""); setQuestionCommandId(createCommandId()); },
    });
  };
  const keyboardSubmit = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); }
  };
  const selectCitation = (citation: AnswerCitation) => {
    citationTriggerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setSearchParams({ citation: citation.id });
  };
  const closeCitation = () => { setSearchParams({}); citationTriggerRef.current?.focus(); };

  if (workspaceId === "") return <section className="rag-gate"><p className="brand-mark">知序 · RAG</p><h1>先连接一个 Workspace。</h1><p>会话必须绑定真实 Workspace，不能用临时示例数据代替。</p><Link className="rag-text-link" to="/">返回 Workspace 配置</Link></section>;
  return <div className="rag-page">
    <header className="rag-header"><div><Link to="/" className="brand-mark">知序 · ZHIXU</Link><h1>证据研究台</h1></div><div className={`rag-connection rag-connection--${connectionState}`} role="status"><span aria-hidden="true" />{connectionState === "open" ? "事件已连接" : connectionState === "reconnecting" ? "正在恢复连接" : "事件连接已关闭"}</div></header>
    <div className="rag-layout">
      <aside className="rag-rail" aria-label="会话列表"><form onSubmit={create}><label><span>新会话标题</span><input maxLength={512} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="例如：发布流程核查" /></label><button disabled={createMutation.isPending}>{createMutation.isPending ? "创建中…" : "新建会话"}</button></form>
        {createMutation.isError ? <ErrorNotice error={createMutation.error} /> : null}
        <nav>{listQuery.isPending ? <p>正在加载会话…</p> : null}{listQuery.isError ? <ErrorNotice error={listQuery.error} /> : null}
          {conversations.length === 0 ? <p className="rag-muted">还没有会话。</p> : null}
          {conversations.map((item) => <Link className={item.id === conversationId ? "is-active" : ""} key={item.id} to={`/chat/${item.id}`}><strong>{item.title ?? `会话 ${item.id.slice(0, 8)}`}</strong><small>{formatTime(item.lastActivityAt)}</small></Link>)}
          {listQuery.hasNextPage ? <button type="button" className="secondary-button" disabled={listQuery.isFetchingNextPage} onClick={() => { void listQuery.fetchNextPage(); }}>{listQuery.isFetchingNextPage ? "加载中…" : "加载更多会话"}</button> : null}</nav>
      </aside>
      <main className="rag-workspace">
        {conversationId === "" ? <section className="rag-empty"><p className="eyebrow">Conversation</p><h2>选择一个会话，开始基于证据提问。</h2><p>回答只有在引用和忠实度校验通过后才会发布。</p></section> : null}
        {conversationQuery.isError ? <ErrorNotice error={conversationQuery.error} /> : null}
        {turnsQuery.isPending && conversationId !== "" ? <p>正在恢复会话事实…</p> : null}
        {turnsQuery.isError ? <ErrorNotice error={turnsQuery.error} /> : null}
        {conversationId !== "" && turns.length === 0 && !turnsQuery.isPending ? <section className="rag-empty"><p className="eyebrow">空会话</p><h2>提出第一个问题。</h2></section> : null}
        <div className="rag-timeline">{turns.map((turn) => <TurnCard key={turn.question.id} turn={turn} answer={recoveredAnswer.data?.id === turn.answer.id ? recoveredAnswer.data : turn.answer} onCitation={selectCitation} onPrompt={setQuestion} />)}</div>
        {turnsQuery.hasNextPage ? <button type="button" className="secondary-button" disabled={turnsQuery.isFetchingNextPage} onClick={() => { void turnsQuery.fetchNextPage(); }}>{turnsQuery.isFetchingNextPage ? "正在恢复…" : "加载更多 Turn"}</button> : null}
        {conversationId !== "" ? <form className="rag-composer" onSubmit={submitQuestionForm}><label><span>问题</span><textarea maxLength={8192} rows={4} value={question} onKeyDown={keyboardSubmit} onChange={(event) => setQuestion(event.target.value)} placeholder="输入问题。Enter 提交，Shift + Enter 换行。" /></label>
          <details className="rag-scope"><summary>检索范围与证据边界</summary><div className="rag-scope__grid">
            <label><span>检索模式</span><select value={retrievalMode} onChange={(event) => setRetrievalMode(event.target.value as typeof retrievalMode)}><option value="hybrid">混合</option><option value="keyword">关键词</option><option value="semantic">语义</option></select></label>
            <label><span>Source IDs（逗号分隔）</span><input value={sourceIds} onChange={(event) => setSourceIds(event.target.value)} /></label>
            <label><span>Source Version IDs</span><input value={sourceVersionIds} onChange={(event) => setSourceVersionIds(event.target.value)} /></label>
            <label><span>相对路径前缀</span><input value={pathPrefixes} onChange={(event) => setPathPrefixes(event.target.value)} placeholder="docs/architecture" /></label>
            <label><span>捕获时间从</span><input type="datetime-local" value={capturedAtFrom} onChange={(event) => setCapturedAtFrom(event.target.value)} /></label>
            <label><span>捕获时间前</span><input type="datetime-local" value={capturedAtBefore} onChange={(event) => setCapturedAtBefore(event.target.value)} /></label>
            <label className="rag-check"><input type="checkbox" checked={allowOriginalSources} onChange={(event) => setAllowOriginalSources(event.target.checked)} /><span>允许原始来源（能力不可用时明确拒绝）</span></label>
            <label className="rag-check"><input type="checkbox" checked={allowWeb} onChange={(event) => setAllowWeb(event.target.checked)} /><span>允许 Web（能力不可用时明确拒绝）</span></label>
          </div></details>
          <div className="rag-composer__options"><label><span>回答深度</span><select value={answerDepth} onChange={(event) => setAnswerDepth(event.target.value as typeof answerDepth)}><option value="concise">简洁</option><option value="standard">标准</option><option value="detailed">详细</option></select></label><label><span>输出格式</span><select value={outputFormat} onChange={(event) => setOutputFormat(event.target.value as typeof outputFormat)}><option value="markdown">Markdown</option><option value="outline">大纲</option></select></label><button disabled={questionMutation.isPending || question.trim() === "" || conversationQuery.data?.status === "archived"}>{questionMutation.isPending ? "已接收，正在启动…" : "提交问题"}</button></div>{conversationQuery.data?.status === "archived" ? <p className="rag-muted">此会话已归档，不能继续提问。</p> : null}{questionMutation.isError ? <ErrorNotice error={questionMutation.error} /> : null}</form> : null}
      </main>
      <aside ref={evidenceRef} tabIndex={selectedCitation === undefined ? undefined : -1} className={`rag-evidence${selectedCitation === undefined ? "" : " is-open"}`} aria-label="引用证据"><p className="eyebrow">Evidence</p><CitationInspector citation={selectedCitation} onClose={closeCitation} /></aside>
    </div>
  </div>;
};
