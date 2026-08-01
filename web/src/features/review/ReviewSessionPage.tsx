import {
  ArrowLeft,
  BookOpenCheck,
  CheckCircle2,
  ChevronRight,
  CirclePause,
  CirclePlay,
  FileText,
  Flag,
  RefreshCw,
  RotateCcw,
  ShieldCheck,
  StopCircle,
  TriangleAlert,
} from "lucide-react";
import { useRef, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";

import {
  ReviewApiError,
  type CreateReviewLearningPathInput,
  type ReviewAnswerResult,
  type ReviewDueItem,
  type ReviewLearningPathResult,
  type ReviewLearningPathStatus,
  type ReviewLearningPathStepStatus,
  type ReviewLearningPathStepTargetStatus,
  type ReviewRating,
  type UpdateReviewLearningPathStatusInput,
  type UpdateReviewLearningPathStepInput,
} from "../../api/review";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import {
  Badge,
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  UnavailableState,
} from "../../shared/ui";
import { SourceSpanViewer } from "../source-spans";
import {
  useCompleteReviewSession,
  useCreateReviewLearningPath,
  useReviewDue,
  useReviewLearningPath,
  useSubmitReviewAnswer,
  useUpdateReviewLearningPathStatus,
  useUpdateReviewLearningPathStep,
} from "./queries";

interface AttemptStore {
  current: Map<string, string>;
}

const commandKey = (
  prefix: string,
  signature: string,
  attempts: AttemptStore,
): string => {
  const existing = attempts.current.get(signature);
  if (existing !== undefined) return existing;
  const key = `${prefix}-${crypto.randomUUID()}`;
  attempts.current.set(signature, key);
  return key;
};
const errorText = (error: unknown): string =>
  error instanceof Error ? error.message : "请求未完成，请重试。";
const isConflict = (error: unknown): boolean =>
  error instanceof ReviewApiError && error.status === 409;
const isNotFound = (error: unknown): boolean =>
  error instanceof ReviewApiError && error.status === 404;
const isRetryableReviewError = (error: unknown): error is ReviewApiError =>
  error instanceof ReviewApiError && error.retryable;
const bytes = (value: string): number => new TextEncoder().encode(value).length;
const ratingOptions: { value: ReviewRating; label: string }[] = [
  { value: 1, label: "再次复习" },
  { value: 2, label: "困难" },
  { value: 3, label: "良好" },
  { value: 4, label: "轻松" },
];
const pathStatusLabel: Record<ReviewLearningPathStatus, string> = {
  ACTIVE: "进行中",
  PAUSED: "已暂停",
  COMPLETED: "已完成",
};
const stepStatusLabel: Record<ReviewLearningPathStepStatus, string> = {
  PENDING: "待开始",
  IN_PROGRESS: "进行中",
  COMPLETED: "已完成",
  SKIPPED: "已跳过",
};
const actionableGap = (result: ReviewAnswerResult): boolean => {
  const { score } = result.answer;
  return (
    score.errors.length > 0 ||
    score.omissions.length > 0 ||
    (score.correctness.value + score.coverage.value + score.boundaries.value) /
      3 <
      0.75
  );
};

const ScoreResult = ({ result }: { result: ReviewAnswerResult }) => {
  const dimensions: { label: string; value: number; rationale: string }[] = [
    { label: "正确性", ...result.answer.score.correctness },
    { label: "覆盖度", ...result.answer.score.coverage },
    { label: "边界", ...result.answer.score.boundaries },
    { label: "清晰度", ...result.answer.score.clarity },
    { label: "自评置信度", ...result.answer.score.confidence },
  ];
  return (
    <section className="artifact-section" aria-label="本题评分结果">
      <header>
        <div>
          <p className="eyebrow">Server score</p>
          <h3>评分已写入下一次调度</h3>
        </div>
        <Badge tone={result.replayed ? "warning" : "success"}>
          {result.replayed ? "已恢复原请求" : "已提交"}
        </Badge>
      </header>
      <dl className="detail-grid">
        {dimensions.map((dimension) => (
          <div key={dimension.label}>
            <dt>{dimension.label}</dt>
            <dd>{String(Math.round(dimension.value * 100))}%</dd>
            <dd className="sidebar-note">{dimension.rationale}</dd>
          </div>
        ))}
      </dl>
      {result.answer.score.errors.length > 0 ? (
        <div className="artifact-coverage artifact-coverage--gap">
          <strong>错误</strong>
          <ul>
            {result.answer.score.errors.map((item) => (
              <li key={item}>
                <span>{item}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {result.answer.score.omissions.length > 0 ? (
        <div className="artifact-coverage artifact-coverage--partial">
          <strong>遗漏</strong>
          <ul>
            {result.answer.score.omissions.map((item) => (
              <li key={item}>
                <span>{item}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <div className="artifact-coverage artifact-coverage--covered">
        <strong>已验证证据</strong>
        <ul className="artifact-citations">
          {result.answer.score.evidence.map((evidence) => (
            <li key={`${evidence.sourceSpanId}:${evidence.evidenceHash}`}>
              <span>
                来源版本 {evidence.sourceVersionId.slice(0, 8)}… · 片段{" "}
                {evidence.sourceSpanId.slice(0, 8)}…
              </span>
              <span className="button-row">
                <Link
                  className="table-link"
                  to={`/documents/${evidence.sourceVersionId}`}
                >
                  <FileText size={14} />
                  来源版本
                </Link>
                <SourceSpanViewer
                  className="table-link"
                  label="打开片段"
                  reference={{
                    workspaceId: result.answer.workspaceId,
                    sourceVersionId: evidence.sourceVersionId,
                    sourceSpanId: evidence.sourceSpanId,
                  }}
                />
              </span>
            </li>
          ))}
        </ul>
      </div>
      <p className="artifact-note">
        下次复习：
        {new Date(result.schedule.dueAt).toLocaleString("zh-CN", {
          month: "short",
          day: "numeric",
          hour: "2-digit",
          minute: "2-digit",
        })}{" "}
        · 间隔 {result.schedule.intervalDays.toFixed(1)} 天
      </p>
    </section>
  );
};

const ReviewLearningPathPanel = ({
  result,
  pending,
  onPathStatus,
  onStepStatus,
}: {
  result: ReviewLearningPathResult;
  pending: boolean;
  onPathStatus: (status: ReviewLearningPathStatus) => void;
  onStepStatus: (stepId: string, status: ReviewLearningPathStepTargetStatus) => void;
}) => {
  const { path, steps } = result;
  const allStepsTerminal = steps.every(
    (step) => step.status === "COMPLETED" || step.status === "SKIPPED",
  );
  return (
    <section className="artifact-section" aria-label="复习学习路径">
      <header>
        <div>
          <p className="eyebrow">Learning path</p>
          <h3>评分缺口学习路径</h3>
        </div>
        <Badge
          tone={
            path.status === "COMPLETED"
              ? "success"
              : path.status === "PAUSED"
                ? "warning"
                : "info"
          }
        >
          {pathStatusLabel[path.status]}
        </Badge>
      </header>
      <p className="artifact-note">
        来源策略 {path.sourcePolicyVersion} · Artifact v
        {String(path.artifact.artifactVersion)} · 路径版本{" "}
        {String(path.version)}
      </p>
      <div className="button-row">
        <Button asChild size="sm" variant="ghost">
          <Link to={`/artifacts/${path.artifact.artifactId}`}>
            <BookOpenCheck size={14} />
            打开路径 Artifact
          </Link>
        </Button>
        {path.status === "ACTIVE" ? (
          <Button
            size="sm"
            variant="secondary"
            disabled={pending}
            onClick={() => onPathStatus("PAUSED")}
          >
            <CirclePause size={14} />
            暂停
          </Button>
        ) : null}
        {path.status === "PAUSED" ? (
          <Button
            size="sm"
            disabled={pending}
            onClick={() => onPathStatus("ACTIVE")}
          >
            <CirclePlay size={14} />
            恢复
          </Button>
        ) : null}
        {path.status === "ACTIVE" && allStepsTerminal ? (
          <Button
            size="sm"
            disabled={pending}
            onClick={() => onPathStatus("COMPLETED")}
          >
            <Flag size={14} />
            完成路径
          </Button>
        ) : null}
      </div>
      {path.status === "PAUSED" ? (
        <UnavailableState
          title="学习路径已暂停"
          description="恢复后可继续更新每个步骤。"
        />
      ) : null}
      {steps.length === 0 ? (
        <EmptyState
          title="没有学习步骤"
          description="服务端未从本题证据生成需要补强的步骤。"
        />
      ) : (
        <ol className="learning-path-list">
          {steps.map((step) => {
            const terminal =
              step.status === "COMPLETED" || step.status === "SKIPPED";
            const disabled = pending || path.status !== "ACTIVE";
            return (
              <li key={step.id}>
                <div className="learning-path-step__index" aria-hidden="true">
                  {String(step.stepNo).padStart(2, "0")}
                </div>
                <div className="learning-path-step__body">
                  <div className="learning-path-step__header">
                    <h3>{step.title}</h3>
                    <Badge
                      tone={
                        step.status === "COMPLETED"
                          ? "success"
                          : step.status === "SKIPPED"
                            ? "neutral"
                            : "info"
                      }
                    >
                      {stepStatusLabel[step.status]}
                    </Badge>
                  </div>
                  <p>{step.rationale}</p>
                  <p className="learning-path-step__binding">
                    Claim {step.claimId} · Evidence{" "}
                    {step.evidenceHash.slice(0, 16)}…
                  </p>
                  <div className="button-row">
                    <SourceSpanViewer
                      className="ui-button ui-button--secondary ui-button--sm"
                      label="打开步骤证据"
                      reference={{
                        workspaceId: step.workspaceId,
                        sourceVersionId: step.sourceVersionId,
                        sourceSpanId: step.sourceSpanId,
                      }}
                    />
                    {step.status === "PENDING" ? (
                      <Button
                        size="sm"
                        disabled={disabled}
                        onClick={() => onStepStatus(step.id, "IN_PROGRESS")}
                      >
                        <CirclePlay size={14} />
                        开始
                      </Button>
                    ) : null}
                    {step.status === "PENDING" ||
                    step.status === "IN_PROGRESS" ? (
                      <Button
                        size="sm"
                        disabled={disabled}
                        onClick={() => onStepStatus(step.id, "COMPLETED")}
                      >
                        <CheckCircle2 size={14} />
                        完成
                      </Button>
                    ) : null}
                    {!terminal ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={disabled}
                        onClick={() => onStepStatus(step.id, "SKIPPED")}
                      >
                        跳过
                      </Button>
                    ) : null}
                  </div>
                </div>
              </li>
            );
          })}
        </ol>
      )}
      {result.replayed ? (
        <p className="sidebar-note" role="status">
          已恢复同一创建请求的学习路径。
        </p>
      ) : null}
    </section>
  );
};

const ReviewQuestion = ({
  item,
  answer,
  rating,
  pending,
  answerTooLarge,
  onAnswerChange,
  onRatingChange,
  onSubmit,
}: {
  item: ReviewDueItem;
  answer: string;
  rating: ReviewRating;
  pending: boolean;
  answerTooLarge: boolean;
  onAnswerChange: (value: string) => void;
  onRatingChange: (value: ReviewRating) => void;
  onSubmit: () => void;
}) => (
  <>
    <section className="artifact-section" aria-label="当前复习题">
      <header>
        <div>
          <p className="eyebrow">Card / {item.card.cardType}</p>
          <h3>{item.card.question}</h3>
        </div>
        <Badge tone="info">
          难度 {String(Math.round(item.card.difficulty * 100))}%
        </Badge>
      </header>
      <p className="artifact-note">
        调度版本 {item.schedule.schedulerVersion} · 当前到期
      </p>
    </section>
    <form
      className="artifact-form"
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit();
      }}
    >
      <label className="artifact-form__wide">
        你的回答
        <textarea
          value={answer}
          maxLength={65_536}
          onChange={(event) => onAnswerChange(event.target.value)}
          placeholder="写下你的回忆…"
        />
      </label>
      <label>
        自评
        <select
          value={String(rating)}
          onChange={(event) =>
            onRatingChange(Number(event.target.value) as ReviewRating)
          }
        >
          {ratingOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </label>
      <div className="button-row">
        <Button type="submit" disabled={pending || answerTooLarge}>
          <ShieldCheck size={15} />
          {pending ? "正在提交可信评分…" : "提交回答"}
        </Button>
        <span className="sidebar-note">
          {answerTooLarge
            ? "回答超过服务端允许的 64 KiB。"
            : "自评用于调度；正确性由服务端评分。"}
        </span>
      </div>
    </form>
  </>
);

export const ReviewSessionPage = () => {
  const location = useLocation();
  const navigate = useNavigate();
  const workspaceId = useActiveWorkspaceId();
  const query = new URLSearchParams(location.search);
  const deckId = query.get("deck") ?? "";
  const sessionId = query.get("session") ?? "";
  const answerId = query.get("answer") ?? "";
  const due = useReviewDue(
    sessionId,
    deckId,
    workspaceId !== "" && deckId !== "" && sessionId !== "",
  );
  const submit = useSubmitReviewAnswer();
  const complete = useCompleteReviewSession();
  const learningPath = useReviewLearningPath(answerId, answerId !== "");
  const createLearningPath = useCreateReviewLearningPath();
  const pathStatus = useUpdateReviewLearningPathStatus();
  const stepStatus = useUpdateReviewLearningPathStep();
  const answerAttempts = useRef<AttemptStore>({ current: new Map() });
  const completeAttempts = useRef<AttemptStore>({ current: new Map() });
  const learningPathAttempts = useRef<AttemptStore>({ current: new Map() });
  const [answer, setAnswer] = useState("");
  const [rating, setRating] = useState<ReviewRating>(3);
  const [result, setResult] = useState<ReviewAnswerResult>();
  const [completedCards, setCompletedCards] = useState<ReadonlySet<string>>(
    () => new Set(),
  );
  const available =
    due.data?.items.filter((item) => !completedCards.has(item.card.id)) ?? [];
  const activeItem = available[0];
  const answerTooLarge = bytes(answer) > 64 * 1024;
  const pathPending =
    createLearningPath.isPending ||
    pathStatus.isPending ||
    stepStatus.isPending;

  const setAnswerInUrl = (nextAnswerId: string | undefined): void => {
    const next = new URLSearchParams(location.search);
    if (nextAnswerId === undefined) next.delete("answer");
    else next.set("answer", nextAnswerId);
    const search = next.toString();
    void navigate(
      {
        pathname: location.pathname,
        ...(search === "" ? {} : { search: `?${search}` }),
      },
      { replace: true },
    );
  };

  const submitCurrent = (): void => {
    if (
      workspaceId === "" ||
      activeItem === undefined ||
      sessionId === "" ||
      submit.isPending ||
      answerTooLarge
    )
      return;
    const signature = JSON.stringify({
      workspaceId,
      sessionId,
      cardId: activeItem.card.id,
      questionRef: activeItem.questionRef,
      answer,
      rating,
    });
    submit.mutate(
      {
        workspaceId,
        sessionId,
        cardId: activeItem.card.id,
        questionRef: activeItem.questionRef,
        userAnswer: answer,
        rating,
        idempotencyKey: commandKey(
          "review-answer",
          signature,
          answerAttempts.current,
        ),
      },
      {
        onSuccess: (accepted) => {
          answerAttempts.current.current.delete(signature);
          setResult(accepted);
          setAnswerInUrl(accepted.answer.id);
          setCompletedCards(
            (current) => new Set([...current, activeItem.card.id]),
          );
        },
      },
    );
  };
  const nextQuestion = (): void => {
    setResult(undefined);
    setAnswerInUrl(undefined);
    setAnswer("");
    setRating(3);
    submit.reset();
  };
  const finishSession = (cancelled: boolean): void => {
    if (workspaceId === "" || sessionId === "" || complete.isPending) return;
    const signature = `${workspaceId}:${sessionId}:${String(cancelled)}`;
    complete.mutate(
      {
        workspaceId,
        sessionId,
        cancelled,
        idempotencyKey: commandKey(
          cancelled ? "review-session-cancel" : "review-session-complete",
          signature,
          completeAttempts.current,
        ),
      },
      {
        onSuccess: () => {
          completeAttempts.current.current.delete(signature);
          void navigate("/review", { replace: true });
        },
      },
    );
  };
  const refreshDue = (): void => {
    setResult(undefined);
    submit.reset();
    void due.refetch();
  };
  const runCreateLearningPath = (
    input: CreateReviewLearningPathInput,
  ): void => {
    createLearningPath.mutate(input, {
      onSuccess: () =>
        learningPathAttempts.current.current.delete(
          JSON.stringify({
            workspaceId: input.workspaceId,
            answerId: input.answerId,
          }),
        ),
    });
  };
  const createPath = (): void => {
    const currentAnswerId = result?.answer.id ?? answerId;
    if (workspaceId === "" || currentAnswerId === "" || pathPending) return;
    const command = { workspaceId, answerId: currentAnswerId };
    runCreateLearningPath({
      ...command,
      idempotencyKey: commandKey(
        "review-learning-path",
        JSON.stringify(command),
        learningPathAttempts.current,
      ),
    });
  };
  const runPathStatus = (input: UpdateReviewLearningPathStatusInput): void => {
    pathStatus.mutate(input, {
      onSuccess: () =>
        learningPathAttempts.current.current.delete(
          JSON.stringify({
            workspaceId: input.workspaceId,
            answerId: input.answerId,
            expectedVersion: input.expectedVersion,
            status: input.status,
          }),
        ),
    });
  };
  const updatePathStatus = (status: ReviewLearningPathStatus): void => {
    const path = learningPath.data?.path;
    if (workspaceId === "" || path === undefined || pathPending) return;
    const command = {
      workspaceId,
      answerId: path.reviewAnswerId,
      expectedVersion: path.version,
      status,
    };
    runPathStatus({
      ...command,
      idempotencyKey: commandKey(
        "review-learning-path-status",
        JSON.stringify(command),
        learningPathAttempts.current,
      ),
    });
  };
  const runStepStatus = (input: UpdateReviewLearningPathStepInput): void => {
    stepStatus.mutate(input, {
      onSuccess: () =>
        learningPathAttempts.current.current.delete(
          JSON.stringify({
            workspaceId: input.workspaceId,
            answerId: input.answerId,
            stepId: input.stepId,
            expectedVersion: input.expectedVersion,
            status: input.status,
          }),
        ),
    });
  };
  const updateStepStatus = (
    stepId: string,
    status: ReviewLearningPathStepTargetStatus,
  ): void => {
    const path = learningPath.data?.path;
    if (workspaceId === "" || path === undefined || pathPending) return;
    const command = {
      workspaceId,
      answerId: path.reviewAnswerId,
      stepId,
      expectedVersion: path.version,
      status,
    };
    runStepStatus({
      ...command,
      idempotencyKey: commandKey(
        "review-learning-path-step",
        JSON.stringify(command),
        learningPathAttempts.current,
      ),
    });
  };

  if (workspaceId === "")
    return (
      <div className="page-stack">
        <UnavailableState
          title="请选择 Workspace"
          description="Review Session 只能在当前 Workspace 中继续。"
        />
      </div>
    );
  if (deckId === "" || sessionId === "")
    return (
      <div className="page-stack">
        <UnavailableState
          title="缺少复习会话"
          description="请从 Review Deck 进入新的复习会话。"
        />
        <Button asChild variant="secondary">
          <Link to="/review">
            <ArrowLeft size={15} />
            返回 Review
          </Link>
        </Button>
      </div>
    );

  return (
    <div className="page-stack">
      <div className="page-intro page-intro--split">
        <div>
          <Link className="back-link" to="/review">
            <ArrowLeft size={15} />
            返回 Deck
          </Link>
          <h1>今日复习</h1>
          <p>会话 {sessionId.slice(0, 8)}…</p>
        </div>
        <div className="folio-mark">
          <BookOpenCheck size={20} />
          <strong>{String(available.length)}</strong>
          <span>题待复习</span>
        </div>
      </div>

      <Card>
        <CardHeader
          eyebrow="Active session"
          title={
            due.isPending
              ? "正在读取待复习题"
              : result !== undefined
                ? "本题结果"
                : activeItem === undefined
                  ? completedCards.size > 0
                    ? "本轮已完成"
                    : "今天没有待复习题"
                  : "回忆问题"
          }
          {...(result === undefined
            ? {}
            : { description: "服务端评分与调度已作为同一答题结果返回。" })}
          action={
            <Button
              variant="ghost"
              onClick={refreshDue}
              disabled={due.isFetching || submit.isPending}
              aria-label="刷新待复习题"
            >
              <RefreshCw size={16} />
            </Button>
          }
        />
        {due.isError ? (
          <ErrorState
            title={
              isConflict(due.error) ? "待复习队列已变化" : "待复习题不可用"
            }
            description={errorText(due.error)}
            onRetry={refreshDue}
          />
        ) : null}
        {due.isPending ? (
          <div className="ui-state" role="status">
            <strong>正在读取今日队列</strong>
            <p>正在从服务器计算已审批且到期的 Card。</p>
          </div>
        ) : null}
        {!due.isPending && !due.isError && result !== undefined ? (
          <>
            <ScoreResult result={result} />
            {learningPath.isPending ? (
              <p className="artifact-note" role="status">
                正在恢复本题学习路径…
              </p>
            ) : null}
            {learningPath.data !== undefined ? (
              <ReviewLearningPathPanel
                result={learningPath.data}
                pending={pathPending}
                onPathStatus={updatePathStatus}
                onStepStatus={updateStepStatus}
              />
            ) : null}
            {learningPath.data === undefined && actionableGap(result) ? (
              <section className="artifact-section" aria-label="创建学习路径">
                <h3>本题存在可行动的学习缺口</h3>
                <p>
                  错误、遗漏或核心评分不足会由服务端基于已持久化的评分和证据生成学习步骤。
                </p>
                <Button onClick={createPath} disabled={pathPending}>
                  <BookOpenCheck size={15} />
                  {createLearningPath.isPending
                    ? "正在创建学习路径…"
                    : "创建学习路径"}
                </Button>
              </section>
            ) : null}
            {learningPath.data === undefined &&
            !learningPath.isPending &&
            !actionableGap(result) ? (
              <p className="artifact-note">
                本题没有达到创建学习路径的条件，无需额外创建路径。
              </p>
            ) : null}
            {learningPath.isError && !isNotFound(learningPath.error) ? (
              <ErrorState
                title="学习路径不可用"
                description={errorText(learningPath.error)}
                onRetry={() => void learningPath.refetch()}
              />
            ) : null}
            {createLearningPath.isError ? (
              <div className="ui-state ui-state--error" role="alert">
                <strong>学习路径未创建</strong>
                <p>{errorText(createLearningPath.error)}</p>
                {isRetryableReviewError(createLearningPath.error) ? (
                  <Button
                    variant="secondary"
                    onClick={() =>
                      createLearningPath.mutate(createLearningPath.variables)
                    }
                  >
                    <RotateCcw size={15} />
                    重试原请求
                  </Button>
                ) : null}
              </div>
            ) : null}
            {pathStatus.isError ? (
              <div className="ui-state ui-state--error" role="alert">
                <strong>路径状态未更新</strong>
                <p>{errorText(pathStatus.error)}</p>
                {isRetryableReviewError(pathStatus.error) ? (
                  <Button
                    variant="secondary"
                    onClick={() => pathStatus.mutate(pathStatus.variables)}
                  >
                    <RotateCcw size={15} />
                    重试原请求
                  </Button>
                ) : null}
              </div>
            ) : null}
            {stepStatus.isError ? (
              <div className="ui-state ui-state--error" role="alert">
                <strong>步骤状态未更新</strong>
                <p>{errorText(stepStatus.error)}</p>
                {isRetryableReviewError(stepStatus.error) ? (
                  <Button
                    variant="secondary"
                    onClick={() => stepStatus.mutate(stepStatus.variables)}
                  >
                    <RotateCcw size={15} />
                    重试原请求
                  </Button>
                ) : null}
              </div>
            ) : null}
            <div className="button-row">
              <Button onClick={nextQuestion}>
                <ChevronRight size={15} />
                下一题
              </Button>
              <Button
                variant="secondary"
                onClick={() => finishSession(false)}
                disabled={complete.isPending || pathPending}
              >
                <CheckCircle2 size={15} />
                结束本轮
              </Button>
            </div>
          </>
        ) : null}
        {!due.isPending &&
        !due.isError &&
        result === undefined &&
        learningPath.data !== undefined ? (
          <ReviewLearningPathPanel
            result={learningPath.data}
            pending={pathPending}
            onPathStatus={updatePathStatus}
            onStepStatus={updateStepStatus}
          />
        ) : null}
        {!due.isPending &&
        !due.isError &&
        result === undefined &&
        answerId !== "" &&
        learningPath.isPending ? (
          <p className="artifact-note" role="status">
            正在恢复本题学习路径…
          </p>
        ) : null}
        {!due.isPending &&
        !due.isError &&
        result === undefined &&
        answerId !== "" &&
        learningPath.isError &&
        !isNotFound(learningPath.error) ? (
          <ErrorState
            title="学习路径不可用"
            description={errorText(learningPath.error)}
            onRetry={() => void learningPath.refetch()}
          />
        ) : null}
        {!due.isPending &&
        !due.isError &&
        result === undefined &&
        answerId !== "" &&
        learningPath.isError &&
        isNotFound(learningPath.error) ? (
          <p className="artifact-note">
            该答案没有可恢复的学习路径，可能无需补强。
          </p>
        ) : null}
        {!due.isPending &&
        !due.isError &&
        result === undefined &&
        activeItem !== undefined ? (
          <ReviewQuestion
            item={activeItem}
            answer={answer}
            rating={rating}
            pending={submit.isPending}
            answerTooLarge={answerTooLarge}
            onAnswerChange={setAnswer}
            onRatingChange={setRating}
            onSubmit={submitCurrent}
          />
        ) : null}
        {!due.isPending &&
        !due.isError &&
        result === undefined &&
        activeItem === undefined ? (
          <EmptyState
            title={
              completedCards.size > 0 ? "本轮复习已完成" : "今天没有待复习题"
            }
            description={
              completedCards.size > 0
                ? "所有已加载的到期 Card 都已得到服务端评分。"
                : "当已审批 Card 到期后，会在这里出现。"
            }
            action={
              completedCards.size > 0 ? (
                <Button
                  onClick={() => finishSession(false)}
                  disabled={complete.isPending}
                >
                  <CheckCircle2 size={15} />
                  结束本轮
                </Button>
              ) : undefined
            }
          />
        ) : null}
        {submit.isError ? (
          <div className="ui-state ui-state--error" role="alert">
            <strong>
              {isConflict(submit.error) ? "此题状态已变化" : "回答未提交"}
            </strong>
            <p>{errorText(submit.error)}</p>
            <div className="button-row">
              <Button variant="secondary" onClick={refreshDue}>
                <RefreshCw size={15} />
                刷新队列
              </Button>
              {isRetryableReviewError(submit.error) ? (
                <Button
                  variant="secondary"
                  onClick={() => submit.mutate(submit.variables)}
                >
                  <RotateCcw size={15} />
                  重试原请求
                </Button>
              ) : null}
            </div>
          </div>
        ) : null}
        {complete.isError ? (
          <div className="ui-state ui-state--error" role="alert">
            <strong>会话未结束</strong>
            <p>{errorText(complete.error)}</p>
            {isRetryableReviewError(complete.error) ? (
              <Button
                variant="secondary"
                onClick={() => complete.mutate(complete.variables)}
              >
                <RotateCcw size={15} />
                重试原请求
              </Button>
            ) : null}
          </div>
        ) : null}
        {complete.isPending ? (
          <p className="artifact-note" role="status">
            <TriangleAlert size={15} />
            正在结束会话…
          </p>
        ) : null}
        <div className="button-row">
          <Button
            variant="ghost"
            onClick={() => finishSession(true)}
            disabled={complete.isPending}
          >
            <StopCircle size={15} />
            取消会话
          </Button>
        </div>
      </Card>
    </div>
  );
};
