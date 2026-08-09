import {
  BookOpen,
  Brain,
  CheckCircle2,
  ChevronRight,
  CirclePause,
  CirclePlay,
  FileText,
  Flag,
  History,
  Play,
  RefreshCw,
  RotateCcw,
  Square,
  Target,
} from "lucide-react";
import { useEffect, useRef, useState, type RefObject } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import {
  InterviewApiError,
  type CompleteInterviewInput,
  type InterviewConfig,
  type InterviewDifficulty,
  type InterviewEvidence,
  type InterviewFinding,
  type InterviewScore,
  type InterviewSnapshot,
  type LearningPathStatus,
  type LearningPathStepStatus,
  type LearningPathStepTargetStatus,
  type SuggestInterviewMemoryCandidateInput,
  type StartInterviewInput,
  type SubmitInterviewTurnInput,
  type SubmitInterviewTurnResult,
  type UpdateLearningPathStatusInput,
  type UpdateLearningPathStepInput,
} from "../../api/interview";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, Card, CardHeader, Dialog, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import { SourceSpanViewer } from "../source-spans";
import {
  useCompleteInterview,
  useInterview,
  useInterviewSessions,
  useStartInterview,
  useSubmitInterviewTurn,
  useSuggestInterviewMemoryCandidate,
  useUpdateLearningPathStatus,
  useUpdateLearningPathStep,
} from "./queries";

interface AttemptStore {
  current: Map<string, string>;
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const difficultyValues: readonly InterviewDifficulty[] = ["FOUNDATION", "INTERMEDIATE", "ADVANCED"];
const difficultyLabels: Record<InterviewDifficulty, string> = { FOUNDATION: "基础", INTERMEDIATE: "进阶", ADVANCED: "高级" };
const textBytes = (value: string): number => new TextEncoder().encode(value).length;
const errorText = (value: unknown): string => value instanceof Error ? value.message : "请求未完成，请重试。";
const isRetryable = (value: unknown): value is InterviewApiError => value instanceof InterviewApiError && value.retryable;
const isDifficulty = (value: string): value is InterviewDifficulty => difficultyValues.some((item) => item === value);
const commandKey = (prefix: string, signature: string, attempts: AttemptStore): string => {
  const previous = attempts.current.get(signature);
  if (previous !== undefined) return previous;
  const value = `${prefix}-${crypto.randomUUID()}`;
  attempts.current.set(signature, value);
  return value;
};
const orderedQuestions = (snapshot: InterviewSnapshot) => [...snapshot.questions].sort((left, right) =>
  left.questionNo - right.questionNo || left.followUpNo - right.followUpNo);
const pathStatusLabel: Record<LearningPathStatus, string> = { ACTIVE: "进行中", PAUSED: "已暂停", COMPLETED: "已完成" };
const stepStatusLabel: Record<LearningPathStepStatus, string> = {
  PENDING: "待开始",
  IN_PROGRESS: "学习中",
  COMPLETED: "已完成",
  SKIPPED: "已跳过",
};
const interviewSessionStatusLabel = { ACTIVE: "进行中", COMPLETED: "已完成", CANCELLED: "已取消" } as const;
const deadlineTimestamp = (startedAt: string, durationMinutes: number): number => Date.parse(startedAt) + durationMinutes * 60_000;
const remainingTimeLabel = (milliseconds: number): string => {
  const totalSeconds = Math.ceil(Math.max(0, milliseconds) / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor(totalSeconds % 3600 / 60);
  const seconds = totalSeconds % 60;
  const padded = (value: number): string => String(value).padStart(2, "0");
  return hours > 0 ? `${String(hours)}:${padded(minutes)}:${padded(seconds)}` : `${String(minutes)}:${padded(seconds)}`;
};

const parseScopeIds = (source: string, label: string): string[] => {
  const values = source.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean);
  if (values.some((item) => !uuidPattern.test(item))) throw new Error(`${label} 包含无效 UUID。`);
  if (new Set(values).size !== values.length) throw new Error(`${label} 包含重复 ID。`);
  return values;
};

const CommandError = ({ title, error, canRetry, onRetry }: { title: string; error: unknown; canRetry: boolean; onRetry: () => void }) => {
  if (error === null || error === undefined) return null;
  return <div className="ui-state ui-state--error" role="alert">
    <strong>{title}</strong>
    <p>{errorText(error)}</p>
    {canRetry && isRetryable(error) ? <Button variant="secondary" onClick={onRetry}><RotateCcw size={15} />重试原请求</Button> : null}
  </div>;
};

const EvidenceList = ({ values, workspaceId }: { values: InterviewEvidence[]; workspaceId: string }) => {
  if (values.length === 0) return <p className="artifact-note">报告没有附加证据。</p>;
  return <ul className="interview-evidence-list">
    {values.map((item) => <li key={item.evidenceHash}>
      <div>
        <strong>知识点 {item.claimId.slice(0, 8)}…</strong>
        <code>{item.evidenceHash.slice(0, 16)}…</code>
      </div>
      <div className="button-row">
        <Button asChild size="sm" variant="ghost"><Link to={`/documents/${item.sourceVersionId}`}><FileText size={14} />来源版本</Link></Button>
        <SourceSpanViewer
          className="ui-button ui-button--secondary ui-button--sm"
          label="打开证据片段"
          reference={{ workspaceId, sourceVersionId: item.sourceVersionId, sourceSpanId: item.sourceSpanId }}
        />
      </div>
    </li>)}
  </ul>;
};

const ScorePanel = ({ result, workspaceId }: { result: SubmitInterviewTurnResult; workspaceId: string }) => {
  const value = result.turn.score;
  const dimensions: readonly [string, InterviewScore["correctness"]][] = [
    ["正确性", value.correctness],
    ["覆盖度", value.coverage],
    ["边界", value.boundaries],
    ["清晰度", value.clarity],
  ];
  return <section className="interview-score" aria-label="本题服务端评分" aria-live="polite">
    <div className="interview-score__header">
      <div><p className="eyebrow">服务端评分</p><h3>本题评分</h3></div>
      <div className="button-row">
        {result.turn.decision.followUpCreated ? <Badge tone="warning">已安排追问</Badge> : null}
        {result.replayed ? <Badge tone="neutral">精确重放</Badge> : null}
      </div>
    </div>
    <dl className="interview-score-grid">
      {dimensions.map(([label, dimension]) => <div key={label}>
        <dt>{label}</dt>
        <dd>{Math.round(dimension.value * 100)}%</dd>
        <dd className="sidebar-note">{dimension.rationale}</dd>
      </div>)}
    </dl>
    {value.errors.length > 0 ? <div className="artifact-coverage artifact-coverage--gap"><strong>错误</strong><ul>{value.errors.map((item) => <li key={item}>{item}</li>)}</ul></div> : null}
    {value.omissions.length > 0 ? <div className="artifact-coverage artifact-coverage--partial"><strong>遗漏</strong><ul>{value.omissions.map((item) => <li key={item}>{item}</li>)}</ul></div> : null}
    <EvidenceList values={value.evidence} workspaceId={workspaceId} />
  </section>;
};

const FindingGroup = ({ title, findings, workspaceId }: { title: string; findings: InterviewFinding[]; workspaceId: string }) => <section className="artifact-section">
  <h3>{title}</h3>
  {findings.length === 0 ? <p className="artifact-note">没有此类发现。</p> : <ul className="interview-finding-list">
    {findings.map((item) => <li key={`${item.claimId}:${item.detail}`}>
      <p>{item.detail}</p>
      <EvidenceList values={item.evidence} workspaceId={workspaceId} />
    </li>)}
  </ul>}
</section>;

interface ReportProps {
  snapshot: InterviewSnapshot;
  pending: boolean;
  candidatePendingStepId: string | undefined;
  candidateNotice: { stepId: string; replayed: boolean } | undefined;
  onStep: (stepId: string, status: LearningPathStepTargetStatus) => void;
  onPath: (status: LearningPathStatus) => void;
  onMemoryCandidate: (stepId: string) => void;
}

const ReportAndLearningPath = ({ snapshot, pending, candidatePendingStepId, candidateNotice, onStep, onPath, onMemoryCandidate }: ReportProps) => {
  if (snapshot.report === undefined || snapshot.path === undefined) return null;
  const { report, path, steps } = snapshot;
  const scores: readonly [string, number][] = [
    ["正确性", report.summary.correctness],
    ["覆盖度", report.summary.coverage],
    ["边界", report.summary.boundaries],
    ["清晰度", report.summary.clarity],
  ];
  const allStepsTerminal = steps.every((step) => step.status === "COMPLETED" || step.status === "SKIPPED");
  const pathActions = <div className="button-row">
    {path.status === "ACTIVE" ? <Button size="sm" variant="secondary" onClick={() => onPath("PAUSED")} disabled={pending}><CirclePause size={14} />暂停</Button> : null}
    {path.status === "PAUSED" ? <Button size="sm" onClick={() => onPath("ACTIVE")} disabled={pending}><CirclePlay size={14} />恢复</Button> : null}
    {path.status === "ACTIVE" && allStepsTerminal ? <Button size="sm" onClick={() => onPath("COMPLETED")} disabled={pending}><Flag size={14} />完成路径</Button> : null}
  </div>;

  return <div className="interview-report-stack">
    <Card>
      <CardHeader
        eyebrow="访谈报告"
        title="面试报告"
        description={`已回答 ${String(report.summary.answeredTotal)}，跳过 ${String(report.summary.skippedTotal)}，共 ${String(report.summary.questionsTotal)} 题`}
        action={<Button asChild size="sm" variant="secondary"><Link to={`/artifacts/${report.artifact.artifactId}`}><BookOpen size={14} />查看报告产物</Link></Button>}
      />
      <dl className="interview-score-grid interview-score-grid--summary">
        {scores.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{Math.round(value * 100)}%</dd></div>)}
      </dl>
      <FindingGroup title="强项" findings={report.strengths} workspaceId={report.workspaceId} />
      <FindingGroup title="学习缺口" findings={report.gaps} workspaceId={report.workspaceId} />
      <FindingGroup title="表达问题" findings={report.expression} workspaceId={report.workspaceId} />
      <section className="artifact-section"><h3>报告来源</h3><EvidenceList values={report.evidence} workspaceId={report.workspaceId} /></section>
    </Card>

    <Card>
      <CardHeader
        eyebrow="学习路径"
        title="学习路径"
        description={`产物 v${String(path.artifact.artifactVersion)} · ${pathStatusLabel[path.status]} · 路径版本 ${String(path.version)}`}
        action={path.status === "COMPLETED" ? <Badge tone="success">已完成</Badge> : pathActions}
      />
      <div className="button-row interview-artifact-link">
        <Button asChild size="sm" variant="ghost"><Link to={`/artifacts/${path.artifact.artifactId}`}><BookOpen size={14} />打开学习路径产物</Link></Button>
      </div>
      {path.status === "PAUSED" ? <UnavailableState title="学习路径已暂停" description="恢复后可继续更新步骤。" /> : null}
      {steps.length === 0 ? <EmptyState title="没有学习步骤" description="当前报告没有生成需要补强的知识步骤。" /> : <ol className="learning-path-list">
        {steps.map((step) => {
          const terminal = step.status === "COMPLETED" || step.status === "SKIPPED";
          const disabled = pending || path.status !== "ACTIVE";
          return <li key={step.id}>
            <div className="learning-path-step__index" aria-hidden="true">{String(step.stepNo).padStart(2, "0")}</div>
            <div className="learning-path-step__body">
              <div className="learning-path-step__header"><h3>{step.title}</h3><Badge tone={step.status === "COMPLETED" ? "success" : step.status === "SKIPPED" ? "neutral" : "info"}>{stepStatusLabel[step.status]}</Badge></div>
              <p>{step.rationale}</p>
              <p className="learning-path-step__binding">知识点 {step.claimId} · 证据 {step.evidenceHash.slice(0, 16)}…</p>
              <div className="button-row">
                <SourceSpanViewer
                  className="ui-button ui-button--secondary ui-button--sm"
                  label="打开步骤证据"
                  reference={{ workspaceId: step.workspaceId, sourceVersionId: step.sourceVersionId, sourceSpanId: step.sourceSpanId }}
                />
                <Button size="sm" variant="secondary" onClick={() => onMemoryCandidate(step.id)} disabled={pending}>
                  <Brain size={14} />{candidatePendingStepId === step.id ? "正在创建…" : "创建记忆候选"}
                </Button>
                {step.status === "PENDING" ? <Button size="sm" onClick={() => onStep(step.id, "IN_PROGRESS")} disabled={disabled}><CirclePlay size={14} />开始</Button> : null}
                {step.status === "PENDING" || step.status === "IN_PROGRESS" ? <Button size="sm" onClick={() => onStep(step.id, "COMPLETED")} disabled={disabled}><CheckCircle2 size={14} />完成</Button> : null}
                {!terminal ? <Button size="sm" variant="ghost" onClick={() => onStep(step.id, "SKIPPED")} disabled={disabled}>跳过</Button> : null}
              </div>
              {candidateNotice?.stepId === step.id ? <p className="sidebar-note" role="status">
                {candidateNotice.replayed ? "该步骤的待确认候选已存在。" : "待确认候选已创建。"} <Link to="/memories">前往记忆确认</Link>
              </p> : null}
            </div>
          </li>;
        })}
      </ol>}
    </Card>
  </div>;
};

export const InterviewsPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const navigate = useNavigate();
  const start = useStartInterview();
  const sessions = useInterviewSessions();
  const attempts = useRef(new Map<string, string>());
  const [role, setRole] = useState("");
  const [claimIds, setClaimIds] = useState("");
  const [topicIds, setTopicIds] = useState("");
  const [difficulty, setDifficulty] = useState<InterviewDifficulty>("INTERMEDIATE");
  const [duration, setDuration] = useState("30");
  const [questionCount, setQuestionCount] = useState("5");
  const [maxFollowUps, setMaxFollowUps] = useState("2");
  const [formError, setFormError] = useState<string>();
  const [recoverySessionId, setRecoverySessionId] = useState("");
  const [recoveryError, setRecoveryError] = useState<string>();
  const sessionItems = sessions.data?.pages.flatMap((page) => page.items) ?? [];
  const initialSessionError = sessions.isError && sessionItems.length === 0;

  const runStart = (input: StartInterviewInput): void => {
    start.mutate(input, {
      onSuccess: (result) => {
        attempts.current.delete(JSON.stringify({ workspaceId: input.workspaceId, config: input.config }));
        void navigate(`/interviews/${result.session.id}`);
      },
    });
  };

  const submitConfiguration = (): void => {
    setFormError(undefined);
    try {
      const normalizedRole = role.trim();
      const claims = parseScopeIds(claimIds, "知识点 ID");
      const topics = parseScopeIds(topicIds, "主题 ID");
      const durationMinutes = Number(duration);
      const questions = Number(questionCount);
      const followUps = Number(maxFollowUps);
      if (normalizedRole === "" || textBytes(normalizedRole) > 256) throw new Error("岗位名称必须为 1 到 256 字节。 ");
      if (claims.length === 0 && topics.length === 0) throw new Error("至少需要一个知识点或主题 ID。");
      if (!Number.isInteger(durationMinutes) || durationMinutes < 1 || durationMinutes > 240) throw new Error("面试时长必须为 1 到 240 分钟。");
      if (!Number.isInteger(questions) || questions < 1 || questions > 20) throw new Error("题目数必须为 1 到 20。");
      if (!Number.isInteger(followUps) || followUps < 0 || followUps > 20) throw new Error("追问数必须为 0 到 20。");
      const config: InterviewConfig = {
        schemaVersion: "interview/v1",
        role: normalizedRole,
        scope: { claimIds: claims, topicIds: topics },
        difficulty,
        durationMinutes,
        questionCount: questions,
        maxFollowUps: followUps,
      };
      const signature = JSON.stringify({ workspaceId, config });
      runStart({ workspaceId, config, idempotencyKey: commandKey("interview-start", signature, attempts) });
    } catch (error: unknown) {
      setFormError(errorText(error).trim());
    }
  };

  const recoverSession = (): void => {
    const value = recoverySessionId.trim();
    if (!uuidPattern.test(value)) {
      setRecoveryError("请输入有效的访谈会话 UUID。");
      return;
    }
    setRecoveryError(undefined);
    void navigate(`/interviews/${value}`);
  };

  if (workspaceId === "") return <div className="page-stack"><UnavailableState title="请选择 Workspace" description="访谈只能从当前 Workspace 的正式知识开始。" /></div>;

  return <div className="page-stack">
    <div className="page-intro page-intro--split">
      <div><h1>访谈</h1><p>基于知识内容进行模拟问答。</p></div>
      <div className="folio-mark"><Target size={20} /><strong>访谈</strong><span>证据绑定</span></div>
    </div>
    <div className="interview-start-grid">
      <Card>
        <CardHeader eyebrow="开始访谈" title="配置面试" description="范围可由知识点、主题或两者共同组成。" />
        <form className="artifact-form" onSubmit={(event) => { event.preventDefault(); submitConfiguration(); }}>
          <label className="artifact-form__wide">岗位<input required value={role} onChange={(event) => setRole(event.target.value)} maxLength={256} autoComplete="off" /></label>
          <label className="artifact-form__wide">知识点 ID<textarea value={claimIds} onChange={(event) => setClaimIds(event.target.value)} spellCheck={false} /></label>
          <label className="artifact-form__wide">主题 ID<textarea value={topicIds} onChange={(event) => setTopicIds(event.target.value)} spellCheck={false} /></label>
          <label>难度<select value={difficulty} onChange={(event) => { if (isDifficulty(event.target.value)) setDifficulty(event.target.value); }}><option value="FOUNDATION">基础</option><option value="INTERMEDIATE">进阶</option><option value="ADVANCED">高级</option></select></label>
          <label>时长（分钟）<input type="number" min="1" max="240" step="1" value={duration} onChange={(event) => setDuration(event.target.value)} /></label>
          <label>题目数<input type="number" min="1" max="20" step="1" value={questionCount} onChange={(event) => setQuestionCount(event.target.value)} /></label>
          <label>最多追问<input type="number" min="0" max="20" step="1" value={maxFollowUps} onChange={(event) => setMaxFollowUps(event.target.value)} /></label>
          <div className="artifact-form__wide button-row"><Button type="submit" disabled={start.isPending}><Play size={15} />{start.isPending ? "正在创建…" : "开始面试"}</Button></div>
        </form>
        {formError !== undefined ? <p className="form-error" role="alert">{formError}</p> : null}
        <CommandError title="无法开始面试" error={start.error} canRetry={start.variables !== undefined} onRetry={() => { if (start.variables !== undefined) runStart(start.variables); }} />
      </Card>

      <Card>
        <CardHeader eyebrow="恢复会话" title="恢复面试会话" description="会话、已答题目和报告从服务端投影恢复。" />
        <form className="interview-resume-form" onSubmit={(event) => { event.preventDefault(); recoverSession(); }}>
          <label>会话 ID<input value={recoverySessionId} onChange={(event) => setRecoverySessionId(event.target.value)} spellCheck={false} autoComplete="off" /></label>
          <Button type="submit" variant="secondary"><RefreshCw size={15} />恢复会话</Button>
        </form>
        {recoveryError !== undefined ? <p className="form-error" role="alert">{recoveryError}</p> : null}
      </Card>
    </div>
    <Card className="interview-session-history">
      <CardHeader
        eyebrow="会话历史"
        title="最近会话"
        description="按开始时间倒序"
        action={<span className="interview-session-history__count"><History size={15} />{String(sessionItems.length)} 场</span>}
      />
      {sessions.isPending ? <div className="ui-state" role="status">正在恢复会话列表…</div> : null}
      {initialSessionError ? <ErrorState title="会话列表不可用" description={errorText(sessions.error)} onRetry={() => void sessions.refetch()} /> : null}
      {!sessions.isPending && !initialSessionError && sessionItems.length === 0 ? <EmptyState title="还没有面试会话" description="完成上方配置后，新会话会出现在这里。" /> : null}
      {sessionItems.length > 0 ? <ul className="interview-session-list">
        {sessionItems.map((session) => <li key={session.id}>
          <div className="interview-session-list__body">
            <div className="interview-session-list__title">
              <strong>{session.config.role}</strong>
              <Badge tone={session.status === "COMPLETED" ? "success" : session.status === "CANCELLED" ? "danger" : "info"}>{interviewSessionStatusLabel[session.status]}</Badge>
            </div>
            <span>{difficultyLabels[session.config.difficulty]} · {String(session.config.questionCount)} 题 · {String(session.config.durationMinutes)} 分钟</span>
            <time dateTime={session.startedAt}>{new Date(session.startedAt).toLocaleString("zh-CN")}</time>
          </div>
          <Button asChild size="sm" variant={session.status === "ACTIVE" ? "primary" : "secondary"}>
            <Link to={`/interviews/${session.id}`}>{session.status === "ACTIVE" ? "继续面试" : "查看结果"}</Link>
          </Button>
        </li>)}
      </ul> : null}
      {sessions.isFetchNextPageError ? <div className="ui-state ui-state--error" role="alert">
        <strong>更多会话未加载</strong>
        <p>{errorText(sessions.error)}</p>
        <Button variant="secondary" size="sm" onClick={() => void sessions.fetchNextPage()} disabled={sessions.isFetchingNextPage}>重试</Button>
      </div> : null}
      {sessions.hasNextPage && !sessions.isFetchNextPageError ? <div className="pagination-row"><span /><Button variant="secondary" onClick={() => void sessions.fetchNextPage()} disabled={sessions.isFetchingNextPage}>{sessions.isFetchingNextPage ? "加载中…" : "加载更多会话"}</Button></div> : null}
    </Card>
  </div>;
};

const EarlyEndDialog = ({ open, onOpenChange, onConfirm, pending, expired, restoreFocusRef }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  pending: boolean;
  expired: boolean;
  restoreFocusRef: RefObject<HTMLButtonElement | null>;
}) => <Dialog
  open={open}
  onOpenChange={onOpenChange}
  title={expired ? "面试时间已到" : "提前结束面试"}
  description="未回答的题目会记为跳过，服务端随后固定报告与学习路径。"
  restoreFocusRef={restoreFocusRef}
>
  <div className="dialog-actions">
    <Button variant="danger" onClick={onConfirm} disabled={pending}><Square size={15} />{pending ? "正在结束…" : expired ? "结束并生成报告" : "确认结束"}</Button>
    <Button variant="secondary" onClick={() => onOpenChange(false)} disabled={pending}>{expired ? "暂不结束" : "继续作答"}</Button>
  </div>
</Dialog>;

export const InterviewSessionPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const { sessionId = "" } = useParams();
  const snapshot = useInterview(sessionId);
  const submit = useSubmitInterviewTurn();
  const complete = useCompleteInterview();
  const memoryCandidate = useSuggestInterviewMemoryCandidate();
  const pathStatus = useUpdateLearningPathStatus();
  const stepStatus = useUpdateLearningPathStep();
  const attempts = useRef(new Map<string, string>());
  const earlyEndButtonRef = useRef<HTMLButtonElement>(null);
  const [draft, setDraft] = useState({ questionId: "", value: "" });
  const [lastResult, setLastResult] = useState<SubmitInterviewTurnResult>();
  const [earlyEndOpen, setEarlyEndOpen] = useState(false);
  const [candidateNotice, setCandidateNotice] = useState<{ stepId: string; replayed: boolean }>();
  const pending = submit.isPending || complete.isPending || pathStatus.isPending || stepStatus.isPending || memoryCandidate.isPending;
  const data = snapshot.data;
  const activeQuestion = data === undefined ? undefined : orderedQuestions(data).find((item) => item.status === "PENDING");
  const answer = draft.questionId === activeQuestion?.id ? draft.value : "";
  const answerBytes = textBytes(answer);
  const [now, setNow] = useState(() => Date.now());
  const activeDeadline = data?.session.status === "ACTIVE"
    ? deadlineTimestamp(data.session.startedAt, data.session.config.durationMinutes)
    : undefined;
  const remainingMilliseconds = activeDeadline === undefined ? 0 : Math.max(0, activeDeadline - now);
  const deadlineExpired = activeDeadline !== undefined && remainingMilliseconds === 0;

  useEffect(() => {
    if (activeDeadline === undefined) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [activeDeadline]);

  const runSubmit = (input: SubmitInterviewTurnInput): void => {
    submit.mutate(input, {
      onSuccess: (result) => {
        attempts.current.delete(JSON.stringify({ workspaceId: input.workspaceId, sessionId: input.sessionId, questionId: input.questionId, answer: input.userAnswer }));
        setDraft({ questionId: "", value: "" });
        setLastResult(result);
      },
    });
  };

  const submitAnswer = (): void => {
    if (workspaceId === "" || activeQuestion === undefined || data?.session.status !== "ACTIVE" || deadlineExpired || pending || answerBytes > 64 * 1024) return;
    const signature = JSON.stringify({ workspaceId, sessionId, questionId: activeQuestion.id, answer });
    runSubmit({
      workspaceId,
      sessionId,
      questionId: activeQuestion.id,
      userAnswer: answer,
      idempotencyKey: commandKey("interview-turn", signature, attempts),
    });
  };

  const runComplete = (input: CompleteInterviewInput): void => {
    complete.mutate(input, {
      onSuccess: () => {
        attempts.current.delete(JSON.stringify({ workspaceId: input.workspaceId, sessionId: input.sessionId, manualEnd: input.manualEnd }));
        setEarlyEndOpen(false);
        setLastResult(undefined);
      },
    });
  };

  const finish = (manualEnd: boolean): void => {
    if (workspaceId === "" || pending) return;
    const signature = JSON.stringify({ workspaceId, sessionId, manualEnd });
    runComplete({ workspaceId, sessionId, manualEnd, idempotencyKey: commandKey("interview-complete", signature, attempts) });
  };

  const runPathStatus = (input: UpdateLearningPathStatusInput): void => {
    pathStatus.mutate(input, {
      onSuccess: () => attempts.current.delete(JSON.stringify({
        workspaceId: input.workspaceId,
        pathId: input.pathId,
        expectedVersion: input.expectedVersion,
        status: input.status,
      })),
    });
  };

  const updatePath = (status: LearningPathStatus): void => {
    const path = data?.path;
    if (workspaceId === "" || path === undefined || pending) return;
    const command = { workspaceId, pathId: path.id, expectedVersion: path.version, status };
    const signature = JSON.stringify(command);
    runPathStatus({ ...command, idempotencyKey: commandKey("learning-path", signature, attempts) });
  };

  const runStepStatus = (input: UpdateLearningPathStepInput): void => {
    stepStatus.mutate(input, {
      onSuccess: () => attempts.current.delete(JSON.stringify({
        workspaceId: input.workspaceId,
        pathId: input.pathId,
        stepId: input.stepId,
        expectedVersion: input.expectedVersion,
        status: input.status,
      })),
    });
  };

  const updateStep = (stepId: string, status: LearningPathStepTargetStatus): void => {
    const path = data?.path;
    if (workspaceId === "" || path === undefined || pending) return;
    const command = { workspaceId, pathId: path.id, stepId, expectedVersion: path.version, status };
    const signature = JSON.stringify(command);
    runStepStatus({ ...command, idempotencyKey: commandKey("learning-step", signature, attempts) });
  };

  const runMemoryCandidate = (input: SuggestInterviewMemoryCandidateInput): void => {
    memoryCandidate.mutate(input, {
      onSuccess: (result) => {
        attempts.current.delete(JSON.stringify({
          workspaceId: input.workspaceId,
          sessionId: input.sessionId,
          pathId: input.pathId,
          stepId: input.stepId,
        }));
        setCandidateNotice({ stepId: input.stepId, replayed: result.replayed });
      },
    });
  };

  const suggestMemoryCandidate = (stepId: string): void => {
    const path = data?.path;
    if (workspaceId === "" || path === undefined || data?.session.status !== "COMPLETED" || pending) return;
    const command = { workspaceId, sessionId, pathId: path.id, stepId };
    const signature = JSON.stringify(command);
    setCandidateNotice((current) => current?.stepId === stepId ? undefined : current);
    runMemoryCandidate({ ...command, idempotencyKey: commandKey("interview-memory-candidate", signature, attempts) });
  };

  if (workspaceId === "") return <div className="page-stack"><UnavailableState title="请选择 Workspace" description="面试会话需要当前 Workspace。" /></div>;
  if (sessionId === "") return <div className="page-stack"><UnavailableState title="缺少面试会话" description="请先开始或恢复一次面试。" /></div>;

  return <div className="page-stack">
    <div className="page-intro page-intro--split">
      <div><Link className="back-link" to="/interviews">返回访谈</Link><h1>模拟面试</h1></div>
      <Button variant="ghost" aria-label="刷新面试会话" onClick={() => void snapshot.refetch()} disabled={snapshot.isFetching}><RefreshCw size={16} /></Button>
    </div>
    {snapshot.isPending ? <div className="ui-state" role="status">正在恢复面试会话…</div> : null}
    {snapshot.isError ? <ErrorState title="面试会话不可用" description={errorText(snapshot.error)} onRetry={() => void snapshot.refetch()} /> : null}
    {data === undefined ? null : <>
      <Card className="interview-session-summary">
        <CardHeader
          eyebrow="面试会话"
          title={data.session.config.role}
          description={`${difficultyLabels[data.session.config.difficulty]} · ${String(data.session.config.durationMinutes)} 分钟 · 最多 ${String(data.session.config.maxFollowUps)} 次追问`}
          action={<div className="button-row">
            {data.session.status === "ACTIVE" ? <Badge tone={deadlineExpired ? "warning" : "info"}>{deadlineExpired ? "时间已到" : `剩余 ${remainingTimeLabel(remainingMilliseconds)}`}</Badge> : null}
            <Badge tone={data.session.status === "COMPLETED" ? "success" : data.session.status === "CANCELLED" ? "danger" : "info"}>{interviewSessionStatusLabel[data.session.status]}</Badge>
          </div>}
        />
        <div className="interview-progress-row">
          <label htmlFor="interview-progress">答题进度</label>
          <progress id="interview-progress" max={data.questions.length} value={data.questions.filter((item) => item.status !== "PENDING").length} />
          <span>{String(data.questions.filter((item) => item.status !== "PENDING").length)} / {String(data.questions.length)}</span>
        </div>
      </Card>

      {data.session.status === "CANCELLED" ? <UnavailableState title="面试已取消" description="该会话保留为只读恢复事实，不会生成报告。" /> : null}
      {data.session.status === "ACTIVE" ? <Card className="interview-question-card">
        <CardHeader
          eyebrow={lastResult === undefined && activeQuestion !== undefined ? `第 ${String(activeQuestion.questionNo)} 题${activeQuestion.followUpNo > 0 ? ` · 第 ${String(activeQuestion.followUpNo)} 次追问` : ""}` : "作答结果"}
          title={lastResult === undefined ? activeQuestion?.prompt ?? "题目已全部处理" : "评分已记录"}
          description={lastResult === undefined ? "评分依据将在提交后显示。" : "下一题决策来自已持久化的服务端 Turn。"}
        />
        {lastResult !== undefined ? <>
          <ScorePanel result={lastResult} workspaceId={workspaceId} />
          <div className="button-row">
            <Button onClick={() => setLastResult(undefined)}><ChevronRight size={15} />{lastResult.nextQuestion === undefined ? "查看完成操作" : "继续下一题"}</Button>
            {deadlineExpired && lastResult.nextQuestion !== undefined ? <Button ref={earlyEndButtonRef} variant="secondary" onClick={() => setEarlyEndOpen(true)} disabled={pending}><Square size={15} />结束并生成报告</Button> : null}
          </div>
        </> : null}
        {lastResult === undefined && activeQuestion !== undefined ? <form className="interview-answer-form" onSubmit={(event) => { event.preventDefault(); submitAnswer(); }}>
          <label htmlFor="interview-answer">你的回答</label>
          <textarea
            id="interview-answer"
            value={answer}
            onChange={(event) => setDraft({ questionId: activeQuestion.id, value: event.target.value })}
            aria-describedby="interview-answer-limit"
            disabled={deadlineExpired || pending}
          />
          {deadlineExpired ? <p className="form-error" role="status">面试时间已到，请结束面试以固定报告。</p> : null}
          <div className="interview-answer-form__footer">
            <span id="interview-answer-limit" className={answerBytes > 64 * 1024 ? "form-error" : "sidebar-note"}>{String(answerBytes)} / 65536 字节</span>
            <div className="button-row">
              <Button type="submit" disabled={deadlineExpired || pending || answerBytes > 64 * 1024}><Target size={15} />{submit.isPending ? "正在评分…" : "提交回答"}</Button>
              <Button ref={earlyEndButtonRef} type="button" variant="secondary" onClick={() => setEarlyEndOpen(true)} disabled={pending}><Square size={15} />{deadlineExpired ? "结束并生成报告" : "提前结束"}</Button>
            </div>
          </div>
        </form> : null}
        {lastResult === undefined && activeQuestion === undefined ? <EmptyState title="全部题目已处理" description="现在可以固定报告与学习路径。" action={<Button onClick={() => finish(false)} disabled={pending}><Flag size={15} />{complete.isPending ? "正在生成…" : "完成并生成报告"}</Button>} /> : null}
        <CommandError title="回答未提交" error={submit.error} canRetry={submit.variables !== undefined} onRetry={() => { if (submit.variables !== undefined) runSubmit(submit.variables); }} />
        <CommandError title="面试未结束" error={complete.error} canRetry={complete.variables !== undefined} onRetry={() => { if (complete.variables !== undefined) runComplete(complete.variables); }} />
      </Card> : null}

      <ReportAndLearningPath
        snapshot={data}
        pending={pending}
        candidatePendingStepId={memoryCandidate.isPending ? memoryCandidate.variables.stepId : undefined}
        candidateNotice={candidateNotice}
        onStep={updateStep}
        onPath={updatePath}
        onMemoryCandidate={suggestMemoryCandidate}
      />
      <CommandError title="学习路径状态未更新" error={pathStatus.error} canRetry={pathStatus.variables !== undefined} onRetry={() => { if (pathStatus.variables !== undefined) runPathStatus(pathStatus.variables); }} />
      <CommandError title="学习步骤未更新" error={stepStatus.error} canRetry={stepStatus.variables !== undefined} onRetry={() => { if (stepStatus.variables !== undefined) runStepStatus(stepStatus.variables); }} />
      <CommandError title="记忆候选未创建" error={memoryCandidate.error} canRetry={memoryCandidate.variables !== undefined} onRetry={() => { if (memoryCandidate.variables !== undefined) runMemoryCandidate(memoryCandidate.variables); }} />
    </>}
    <EarlyEndDialog open={earlyEndOpen} onOpenChange={setEarlyEndOpen} onConfirm={() => finish(true)} pending={complete.isPending} expired={deadlineExpired} restoreFocusRef={earlyEndButtonRef} />
  </div>;
};
