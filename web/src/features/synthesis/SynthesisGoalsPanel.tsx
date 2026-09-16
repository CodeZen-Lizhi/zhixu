import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { RefreshCw, Sparkles } from "lucide-react";
import { createSynthesisGoal, listSynthesisGoalSelections, listSynthesisGoals, retrySynthesisGoalSelection, type CreateSynthesisGoalInput, type RetrySynthesisGoalSelectionInput, type SynthesisGoalSelection, type SynthesisGoalView } from "../../api/synthesis-goals";
import { getActiveWorkspaceId, useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, EmptyState, ErrorState } from "../../shared/ui";
import { ProcessingRecord } from "./ProcessingRecord";
import { synthesisQueryKeys } from "./queries";

const goalQueryKey = (workspaceId: string) => ["synthesis", workspaceId, "goals"] as const;
const stage = (view: SynthesisGoalView): { label: string; tone: "neutral" | "info" | "success" | "warning" | "danger"; description: string } => {
  const { request, progress, processing, candidate } = view;
  if (candidate !== null) return { label: "候选已生成", tone: "success", description: "已完成生成和独立审查。可打开该版本审阅内容，并查看当前发布状态。" };
  if (processing?.status === "RECOVERY_REQUIRED" || progress.recoveryRequired > 0) return { label: "需要人工恢复", tone: "danger", description: "有一次异步处理的结果尚不能确认，请先检查处理记录。" };
  if (processing?.status === "FAILED") return { label: "生成失败", tone: "danger", description: processing.failure?.retryable ? "生成未完成，可以显式重试这次生成。" : "生成未完成，请根据错误信息调整后重新发起目标。" };
  if (progress.failed > 0) return { label: "筛选失败", tone: "danger", description: "资料筛选未全部完成。失败记录可逐条显式重试，系统不会自动重做。" };
  if (request.status === "DISCOVERING") {
    if (request.errorCode === "SYNTHESIS_GOAL_SOURCE_PENDING") return { label: "等待资料分析", tone: "info", description: "有资料尚未完成解析或知识分析，完成后会自动继续整理。" };
    if (["SYNTHESIS_GOAL_PROFILE_FAILED", "SYNTHESIS_GOAL_PROFILE_CAPABILITY_UNAVAILABLE", "SYNTHESIS_GOAL_SOURCE_PROCESSING_FAILED"].includes(request.errorCode ?? "")) return { label: "资料分析需要处理", tone: "danger", description: "部分资料分析未完成，请到资料收件箱查看原因并恢复处理。" };
    if (request.errorCode !== null) return { label: "资料发现暂未完成", tone: "warning", description: "正在等待资料目录恢复，请查看提示或稍后刷新。" };
    return { label: "正在发现资料", tone: "info", description: "正在检查资料的知识目录，寻找与目标相关的内容。" };
  }
  if (progress.preparationFailures > 0) return { label: "资料目录准备受阻", tone: "warning", description: `有 ${String(progress.preparationFailures)} 批资料尚未完成准备，恢复后会自动继续筛选。` };
  if (!progress.ready) return { label: "正在筛选知识", tone: "info", description: `已准备 ${String(progress.preparedBatches)}/${String(progress.catalogBatches)} 批资料，AI 正在判断与目标相关的知识点。` };
  if (progress.selectedPoints === 0) return { label: "没有匹配知识", tone: "neutral", description: "已完成筛选，但当前资料中没有可用于这个目标的知识点。" };
  if (processing === null || processing.status === "PENDING") return { label: "等待生成", tone: "neutral", description: "已选出相关知识，正在等待生成任务开始。" };
  if (processing.status === "RUNNING") return { label: "正在生成候选", tone: "info", description: "正在融合已选知识并进行独立审查，完成后会生成待发布候选。" };
  return { label: "等待更新", tone: "neutral", description: "正在从服务端恢复目标的最新处理状态。" };
};

const isGoalActive = (view: SynthesisGoalView): boolean => {
  if (view.candidate !== null) return false;
  if (view.processing !== null) return view.processing.status === "PENDING" || view.processing.status === "RUNNING";
  if (view.request.status === "DISCOVERING") return true;
  if (view.progress.pending > 0 || view.progress.running > 0) return true;
  if (view.progress.failed > 0 || view.progress.recoveryRequired > 0) return false;
  return !view.progress.ready || view.progress.selectedPoints > 0;
};

interface GoalAttempt { signature: string; key: string; goal: string; workspaceId: string; controller: AbortController }
interface SelectionAttempt { version: number; key: string; controller: AbortController }

const GoalSelectionFailures = ({ goal }: { goal: SynthesisGoalView }) => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const attempts = useRef(new Map<string, SelectionAttempt>());
  const selections = useInfiniteQuery({
    queryKey: ["synthesis", workspaceId, "goal-selections", goal.request.id],
    queryFn: ({ signal, pageParam }) => listSynthesisGoalSelections(workspaceId, goal.request.id, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.nextAfterId,
    enabled: workspaceId === goal.request.workspaceId && (goal.progress.failed > 0 || goal.progress.recoveryRequired > 0), retry: false,
  });
  const retry = useMutation({
    mutationFn: (input: RetrySynthesisGoalSelectionInput) => retrySynthesisGoalSelection(input), retry: false,
    onSuccess: async (_selection, input) => {
      if (getActiveWorkspaceId() !== input.workspaceId) return;
      attempts.current.delete(`${input.selectionId}:${String(input.expectedVersion)}`);
      await queryClient.invalidateQueries({ queryKey: ["synthesis", input.workspaceId, "goal-selections", input.goalId] });
      await queryClient.invalidateQueries({ queryKey: goalQueryKey(input.workspaceId) });
    },
  });
  useEffect(() => () => { attempts.current.forEach((entry) => entry.controller.abort()); attempts.current.clear(); }, [workspaceId, goal.request.id]);
  const retrySelection = (selection: SynthesisGoalSelection) => {
    if (retry.isPending || !selection.retryable || selection.status !== "FAILED") return;
    const key = `${selection.id}:${String(selection.version)}`;
    let active = attempts.current.get(key);
    if (active === undefined) {
      active = { version: selection.version, key: `synthesis-goal-selection-${crypto.randomUUID()}`, controller: new AbortController() };
      attempts.current.set(key, active);
    }
    retry.mutate({ workspaceId, goalId: goal.request.id, selectionId: selection.id, expectedVersion: active.version, idempotencyKey: active.key, signal: active.controller.signal });
  };
  const items = selections.data?.pages.flatMap((page) => page.items) ?? [];
  return <section className="synthesis-goal-selection-failures" aria-label="知识筛选处理记录">
    <h4>知识筛选处理记录</h4>
    {selections.isPending ? <p role="status">正在读取筛选处理记录…</p> : null}
    {selections.isError ? <ErrorState title="筛选处理记录不可用" description={selections.error.message} onRetry={() => { void selections.refetch(); }} /> : null}
    {items.map((selection) => <div className="synthesis-goal-selection" key={selection.id}>
      <div><strong>{({ RECOVERY_REQUIRED: "需要人工恢复", FAILED: "筛选失败", SUCCEEDED: "筛选完成", PENDING: "等待筛选", RUNNING: "正在筛选" })[selection.status]}</strong><span>记录 {selection.id.slice(0, 8)}</span></div>
      {selection.errorCode !== null ? <p className="synthesis-failure">知识筛选未完成<span>错误编号：{selection.errorCode}</span></p> : null}
      {selection.status === "RECOVERY_REQUIRED" ? <p>这次筛选的执行结果尚不能确认，不能自动重做。</p> : null}
      {selection.status === "FAILED" && selection.retryable ? <Button size="sm" variant="secondary" disabled={retry.isPending} onClick={() => retrySelection(selection)}>{retry.isPending ? "正在提交重试…" : "重试知识筛选"}</Button> : null}
    </div>)}
    {selections.hasNextPage ? <Button size="sm" variant="secondary" disabled={selections.isFetchingNextPage} onClick={() => { void selections.fetchNextPage(); }}>{selections.isFetchingNextPage ? "正在读取…" : "加载更多筛选记录"}</Button> : null}
    {retry.isError ? <ErrorState title="筛选重试未完成" description={retry.error.message} /> : null}
  </section>;
};

export const SynthesisGoalsPanel = () => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const [goal, setGoal] = useState("");
  const attempt = useRef<GoalAttempt | null>(null);
  const goals = useInfiniteQuery({
    queryKey: goalQueryKey(workspaceId),
    queryFn: ({ signal, pageParam }) => listSynthesisGoals(workspaceId, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: (page) => page.nextCursor,
    enabled: workspaceId !== "", retry: false,
    refetchInterval: (query) => query.state.status === "error" ? false : query.state.data?.pages.some((page) => page.items.some((item) => isGoalActive(item))) ? 3000 : false,
  });
  const create = useMutation({
    mutationFn: (input: CreateSynthesisGoalInput) => createSynthesisGoal(input), retry: false,
    onSuccess: async (_request, input) => {
      if (getActiveWorkspaceId() !== input.workspaceId) return;
      attempt.current = null;
      setGoal("");
      await queryClient.invalidateQueries({ queryKey: goalQueryKey(input.workspaceId) });
      await queryClient.invalidateQueries({ queryKey: synthesisQueryKeys.notes(input.workspaceId) });
    },
    onError: (_error, input) => { if (getActiveWorkspaceId() === input.workspaceId) void goals.refetch(); },
  });
  useEffect(() => () => { attempt.current?.controller.abort(); attempt.current = null; }, [workspaceId]);
  const submit = (event: React.SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (create.isPending || workspaceId === "") return;
    const value = goal.trim(), signature = JSON.stringify([workspaceId, value]);
    if (attempt.current?.signature !== signature) attempt.current = { signature, key: `synthesis-goal-${crypto.randomUUID()}`, goal: value, workspaceId, controller: new AbortController() };
    const active = attempt.current;
    create.mutate({ workspaceId: active.workspaceId, goal: active.goal, idempotencyKey: active.key, signal: active.controller.signal });
  };
  const retryOriginal = () => {
    const active = attempt.current;
    if (active === null || create.isPending || active.workspaceId !== workspaceId) return;
    create.mutate({ workspaceId: active.workspaceId, goal: active.goal, idempotencyKey: active.key, signal: active.controller.signal });
  };
  const refresh = () => { void goals.refetch(); };
  const views = goals.data?.pages.flatMap((page) => page.items) ?? [];
  return <section className="synthesis-list-section synthesis-goals" aria-labelledby="synthesis-goals-heading">
    <div className="synthesis-section-heading"><div><h2 id="synthesis-goals-heading">按目标生成主笔记</h2><p>描述你希望整理的内容，系统会从已分析资料中自动判定范围和关联知识。</p></div><Button variant="secondary" onClick={refresh} disabled={goals.isFetching} aria-label="刷新主笔记目标"><RefreshCw size={16} aria-hidden="true" />刷新</Button></div>
    <form className="synthesis-goal-form" onSubmit={submit}>
      <label htmlFor="synthesis-goal">希望整理的内容<input id="synthesis-goal" name="goal" value={goal} onChange={(event) => setGoal(event.target.value)} maxLength={2048} required disabled={workspaceId === "" || create.isPending} placeholder="例如：数据库专项知识，用于系统复习" /></label>
      <Button type="submit" disabled={workspaceId === "" || create.isPending}><Sparkles size={16} aria-hidden="true" />{create.isPending ? "正在保存目标…" : "开始整理"}</Button>
    </form>
    {create.isError ? <ErrorState title="尚未确认目标是否已保存" description={create.error.message} onRetry={retryOriginal} /> : null}
    <div className="synthesis-goal-list" aria-live="polite">
      {goals.isPending ? <p role="status">正在恢复主笔记目标…</p> : null}
      {goals.isError ? <ErrorState title="主笔记目标不可用" description={goals.error.message} onRetry={refresh} /> : null}
      {goals.isSuccess && views.length === 0 ? <EmptyState title="还没有整理目标" description="填写一个希望整理的方向后，系统会异步筛选相关知识并生成候选版本。" /> : null}
      {views.map((view) => {
        const current = stage(view);
        return <article className="synthesis-goal-record" key={view.request.id}>
          <div className="synthesis-item-heading"><h3>{view.request.goal}</h3><Badge tone={current.tone}>{current.label}</Badge></div>
          <p>{current.description}</p>
          <div className="synthesis-meta"><span>已选 {String(view.progress.selectedPoints)} 个知识点</span><time dateTime={view.request.updatedAt}>更新于 {new Date(view.request.updatedAt).toLocaleString("zh-CN", { hour12: false })}</time></div>
          {view.progress.preparationErrorCode !== null ? <p className="synthesis-failure">知识筛选尚未开始<span>错误编号：{view.progress.preparationErrorCode}</span></p> : null}
          {view.request.errorCode !== null ? <p className="synthesis-failure">资料处理提示<span>错误编号：{view.request.errorCode}</span><Link to="/inbox">查看资料收件箱</Link></p> : null}
          {view.candidate !== null ? <Link className="ui-button ui-button--secondary" to={`/authoring/notes/${view.candidate.noteId}?revision_id=${view.candidate.revisionId}`}>审阅候选版本</Link> : null}
          {view.progress.failed > 0 || view.progress.recoveryRequired > 0 ? <GoalSelectionFailures goal={view} /> : null}
          {view.processing !== null ? <div className="synthesis-goal-processing"><ProcessingRecord processing={view.processing} /></div> : null}
        </article>;
      })}
      {goals.hasNextPage ? <Button variant="secondary" disabled={goals.isFetchingNextPage} onClick={() => { void goals.fetchNextPage(); }}>{goals.isFetchingNextPage ? "正在读取…" : "加载更多目标"}</Button> : null}
    </div>
  </section>;
};
