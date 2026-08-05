import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Check, ExternalLink, FileSearch, GitCompare, Pause, Play, Square, X } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";

import {
  BusinessApiError,
  controlWorkflow,
  getWorkflow,
  listWorkflows,
  submitWorkflowHumanDecision,
  type WorkflowHumanTask,
  type WorkflowMergeCategory,
  type WorkflowReviewEvidence,
  type WorkflowStatus,
} from "../../api/business";
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
import { formatElapsedDuration } from "../../shared/time";
import { SourceSpanViewer } from "../source-spans";
import { BusinessWorkspaceGate } from "./BusinessWorkspaceGate";
import { useScopedCursor } from "./pagination";
import { parseWorkflowUrlState, workflowStatusOptions, writeWorkflowUrlState, type WorkflowUrlState } from "./url-state";

interface WorkflowControlAvailability {
  canPause: boolean;
  canResume: boolean;
  canCancel: boolean;
}

const workflowControlPending = (status: WorkflowStatus, pauseRequested: boolean, cancelRequested: boolean): boolean =>
  (pauseRequested && status !== "paused") || (cancelRequested && status !== "cancelled");

export const getWorkflowControlAvailability = (
  status: WorkflowStatus,
): WorkflowControlAvailability => ({
  canPause: status === "pending" || status === "running" || status === "waiting_for_human" || status === "retry_wait",
  canResume: status === "paused",
  canCancel: status === "pending" || status === "running" || status === "waiting_for_human" || status === "retry_wait" || status === "paused",
});

const mergeCategoryLabels: Record<WorkflowMergeCategory, string> = {
  DUPLICATE: "重复",
  COMPLEMENTARY: "互补",
  CONFLICT: "冲突",
  UNIQUE: "独有",
};

const ReviewEvidenceList = ({ evidence, label, workspaceId }: { evidence: readonly WorkflowReviewEvidence[]; label: string; workspaceId: string }) => (
  <ol className="workflow-review-evidence" aria-label={label}>
    {evidence.map((item, index) => <li key={item.kind === "SOURCE_VERSION" ? `${item.kind}:${item.sourceVersionId}:${item.sourceSpanId}` : `${item.kind}:${item.documentId}:${item.articleRevisionId}`}>
      <span className="workflow-review-evidence__index">{String(index + 1).padStart(2, "0")}</span>
      {item.kind === "SOURCE_VERSION" ? <><dl>
        <div><dt>Source Version</dt><dd><code>{item.sourceVersionId}</code></dd></div>
        <div><dt>Source Span</dt><dd><code>{item.sourceSpanId}</code></dd></div>
        <div><dt>Content Hash</dt><dd><code>{item.contentHash}</code></dd></div>
        <div><dt>Excerpt Hash</dt><dd><code>{item.excerptHash}</code></dd></div>
      </dl>
      <SourceSpanViewer
        className="ui-button ui-button--secondary workflow-review-evidence__open"
        label="打开来源片段"
        reference={{ workspaceId, sourceVersionId: item.sourceVersionId, sourceSpanId: item.sourceSpanId }}
      /></> : <dl>
        <div><dt>Document</dt><dd><code>{item.documentId}</code></dd></div>
        <div><dt>Article Revision</dt><dd><code>{item.articleRevisionId}</code></dd></div>
        <div><dt>Revision</dt><dd>{item.revisionNo}</dd></div>
        <div><dt>Content Hash</dt><dd><code>{item.contentHash}</code></dd></div>
      </dl>}
    </li>)}
  </ol>
);

const HumanTaskReview = ({ task }: { task: WorkflowHumanTask }) => {
  const review = task.review;
  if (!review) {
    return <div className="workflow-review-unavailable" role="alert" id="workflow-review-unavailable">
      <AlertTriangle size={18} aria-hidden="true" />
      <div>
        <strong>审阅内容不可用</strong>
        <p>服务端没有返回与当前任务绑定的审阅投影。为避免盲目决策，批准和拒绝均已停用；请刷新后重试。</p>
      </div>
    </div>;
  }
  if (review.kind === "TOPIC_OUTLINE") {
    return <section className="workflow-review" aria-labelledby="workflow-topic-review-heading">
      <header className="workflow-review__header">
        <FileSearch size={18} aria-hidden="true" />
        <div>
          <p className="eyebrow">专题文章 / 大纲审阅</p>
          <h4 id="workflow-topic-review-heading">逐节核对证据与缺口</h4>
        </div>
      </header>
      <dl className="workflow-review__binding">
        <div><dt>Snapshot</dt><dd><code>{review.snapshotId}</code></dd></div>
        <div><dt>Snapshot Hash</dt><dd><code>{review.snapshotHash}</code></dd></div>
        <div><dt>Template Revision</dt><dd><code>{review.templateRevisionId}</code></dd></div>
        <div><dt>Template Hash</dt><dd><code>{review.templateHash}</code></dd></div>
      </dl>
      <ol className="workflow-outline-review">
        {review.outline.map((section, index) => <li key={section.key}>
          <div className="workflow-outline-review__title">
            <span>{String(index + 1).padStart(2, "0")}</span>
            <div><code>{section.key}</code><h5>{section.title}</h5></div>
          </div>
          {section.gapCode === null
            ? <ReviewEvidenceList evidence={section.supports} label={`${section.title} 的证据`} workspaceId={review.workspaceId} />
            : <p className="workflow-review-gap"><strong>GAP</strong><code>{section.gapCode}</code><span>当前章节没有已验证证据。</span></p>}
        </li>)}
      </ol>
    </section>;
  }

  return <section className="workflow-review" aria-labelledby="workflow-merge-review-heading">
    <header className="workflow-review__header workflow-review__header--split">
      <div className="workflow-review__heading">
        <GitCompare size={18} aria-hidden="true" />
        <div>
          <p className="eyebrow">合并文档 / 比较审阅</p>
          <h4 id="workflow-merge-review-heading">确认分类、冲突与来源</h4>
        </div>
      </div>
      <Link className="workflow-review__artifact-link" to={`/artifacts/${review.artifactId}`}>
        打开 Artifact <ExternalLink size={14} aria-hidden="true" />
      </Link>
    </header>
    <dl className="workflow-review__binding">
      <div><dt>Snapshot</dt><dd><code>{review.snapshotId}</code></dd></div>
      <div><dt>Snapshot Hash</dt><dd><code>{review.snapshotHash}</code></dd></div>
      <div><dt>Revision Hash</dt><dd><code>{review.revisionHash}</code></dd></div>
      <div><dt>Diff Hash</dt><dd><code>{review.diffHash}</code></dd></div>
    </dl>
    <dl className="workflow-merge-summary" aria-label="合并比较统计">
      <div className="workflow-merge-summary__total"><dt>正式证据</dt><dd>{review.evidenceCount}</dd></div>
      <div><dt>来源文档</dt><dd>{review.documentCount}</dd></div>
      <div className="workflow-merge-summary__conflict"><dt>冲突</dt><dd>{review.conflictCount}</dd></div>
      {review.categories.map((item) => <div key={item.category}>
        <dt>{mergeCategoryLabels[item.category]}</dt><dd>{item.count}</dd>
      </div>)}
    </dl>
    <div className="workflow-review-diff">
      <div><strong>统一 Diff 预览</strong><code>{review.diffHash}</code></div>
      <pre aria-label="统一 Diff 预览">{review.diffPreview}</pre>
      {review.diffTruncated ? <p role="note">Diff 已达到 32 KiB 审阅上限，当前仅显示受控预览；完整修订请在 Artifact 中核对。</p> : null}
    </div>
    {review.comparison.length === 0
      ? <p className="workflow-review-empty">当前合并结果没有材料来源。</p>
      : <ol className="workflow-merge-comparison" aria-label="合并材料来源">
        {review.comparison.map((item, index) => <li key={item.kind === "SOURCE_VERSION" ? `${item.kind}:${item.sourceVersionId}:${item.sourceSpanId}` : `${item.kind}:${item.documentId}:${item.articleRevisionId}`}>
          <div className="workflow-merge-comparison__category">
            <span>{String(index + 1).padStart(2, "0")}</span>
            <strong>{mergeCategoryLabels[item.category]}</strong>
            <code>{item.category}</code>
          </div>
          <ReviewEvidenceList evidence={[item]} label={`合并材料 ${String(index + 1)}`} workspaceId={review.workspaceId} />
        </li>)}
      </ol>}
  </section>;
};

export const WorkflowHumanTaskDecision = ({ task, pending, error, onDecide }: {
  task: WorkflowHumanTask;
  pending: boolean;
  error: Error | null;
  onDecide: (approved: boolean, targetPath: string) => void;
}) => {
  const defaultTargetPath = task.review?.kind === "MERGE_COMPARISON" ? task.review.defaultTargetPath : "";
  const [targetPath, setTargetPath] = useState(defaultTargetPath);
  useEffect(() => setTargetPath(defaultTargetPath), [defaultTargetPath, task.id]);
  const targetRequired = task.decisionKind === "approval_with_target_path";
  const reviewUnavailable = task.review === null;
  const approveDisabled = pending || reviewUnavailable || (targetRequired && targetPath.trim() === "");
  return <section className="workflow-human-task" aria-labelledby="workflow-human-task-heading">
    <div>
      <p className="eyebrow">等待人工确认</p>
      <h3 id="workflow-human-task-heading">检查当前整理阶段</h3>
      <p>{targetRequired ? "确认合并内容及新文件位置后，流程才会创建可审阅的 Proposal。" : "批准当前大纲后，流程才会继续生成 Artifact。"}</p>
    </div>
    <HumanTaskReview task={task} />
    {targetRequired ? <label>目标 Markdown 路径<input value={targetPath} maxLength={4096} placeholder="notes/merged-topic.md" onChange={(event) => setTargetPath(event.target.value)} disabled={pending || reviewUnavailable} /></label> : null}
    <div className="button-row">
      <Button onClick={() => onDecide(true, targetPath)} disabled={approveDisabled}><Check size={15} />批准并继续</Button>
      <Button variant="secondary" onClick={() => onDecide(false, targetPath)} disabled={pending || reviewUnavailable}><X size={15} />拒绝</Button>
    </div>
    {error ? <p className="form-error" role="alert">{error.message}</p> : null}
  </section>;
};

export const WorkflowsPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [params, setParams] = useSearchParams();
  const urlState = parseWorkflowUrlState(params);
  const canonicalSearch = writeWorkflowUrlState(urlState).toString();
  useEffect(() => {
    if (params.toString() !== canonicalSearch) setParams(new URLSearchParams(canonicalSearch), { replace: true });
  }, [canonicalSearch, params, setParams]);
  const { status } = urlState;
  const [cursor, setCursor] = useScopedCursor([workspaceId, status]);
  const query = useQuery({
    queryKey: ["business", workspaceId, "workflows", status, cursor],
    queryFn: ({ signal }) =>
      listWorkflows(workspaceId, {
        limit: 30,
        ...(status ? { status } : {}),
        ...(cursor ? { cursor } : {}),
      }, signal),
    enabled: Boolean(workspaceId),
    retry: false,
  });
  const updateStatus = (value: WorkflowUrlState["status"]) => {
    setCursor("");
    setParams(writeWorkflowUrlState({ ...urlState, status: value }));
  };

  if (!workspaceId) return <BusinessWorkspaceGate description="Workflow 中心必须绑定已连接 Workspace；未连接时不会查询持久 Run。" />;

  return (
    <div className="page-stack">
      <div className="page-intro">
        <h1>流程</h1>
        <p>查看任务运行状态与执行记录。</p>
      </div>
      <Card>
        <div className="filter-bar" aria-label="Workflow 筛选">
          <label>
            运行状态
            <select
              value={status}
              onChange={(event) => updateStatus(event.target.value as WorkflowUrlState["status"])}
            >
              <option value="">全部</option>
              {workflowStatusOptions.map(([value, label]) => <option value={value} key={value}>{label}</option>)}
            </select>
          </label>
        </div>
        <CardHeader
          eyebrow="运行记录"
          title={query.isPending ? "读取中…" : `${String(query.data?.items.length ?? 0)} 个 Run`}
        />
        {query.isError ? (
          <UnavailableState title="Workflow 列表不可用" description={query.error.message} />
        ) : query.data?.items.length === 0 ? (
          <EmptyState
            title="暂无 Workflow Run"
            description="摄取、写回和检索任务的持久状态会出现在这里。"
          />
        ) : (
          <div className="workflow-list">
            {query.data?.items.map((item) => (
              <Link className="workflow-row" to={`/workflows/${item.id}`} key={item.id}>
                <div>
                  <strong>{item.definitionKey}</strong>
                  <small>
                    {item.id.slice(0, 8)} · v{item.definitionVersion}
                  </small>
                </div>
                <Badge
                  tone={
                    item.status === "failed"
                      ? "danger"
                      : item.waitingForHuman
                        ? "warning"
                        : item.status === "succeeded"
                          ? "success"
                          : "info"
                  }
                >
                  {item.status}
                </Badge>
                <div className="workflow-row__timing"><small>创建 {new Date(item.createdAt).toLocaleString("zh-CN")}</small><time>更新 {new Date(item.updatedAt).toLocaleString("zh-CN")}</time><small>持续 {formatElapsedDuration(item.createdAt, item.completedAt)}</small>{item.waitingForHuman ? <small className="workflow-waiting">等待用户输入</small> : null}{workflowControlPending(item.status, item.pauseRequested, item.cancelRequested) ? <small className="workflow-waiting">等待安全检查点</small> : null}</div>
              </Link>
            ))}
          </div>
        )}
        <div className="pagination-row">
          {cursor ? (
            <Button
              variant="ghost"
              onClick={() => setCursor("")}
            >
              返回首屏
            </Button>
          ) : (
            <span />
          )}
          {query.data?.nextCursor ? (
            <Button
              variant="secondary"
              onClick={() => setCursor(query.data.nextCursor ?? "")}
            >
              下一页
            </Button>
          ) : null}
        </div>
      </Card>
    </div>
  );
};

export const WorkflowDetailPage = () => {
  const { workflowId = "" } = useParams();
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["business", workspaceId, "workflow", workflowId],
    queryFn: ({ signal }) => getWorkflow(workspaceId, workflowId, signal),
    enabled: Boolean(workspaceId && workflowId),
    retry: false,
  });
  const mutation = useMutation({
    mutationFn: (action: "pause" | "resume" | "cancel") =>
      controlWorkflow(workspaceId, workflowId, action, query.data?.version ?? 0),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["business", workspaceId, "workflows"] });
      await query.refetch();
    },
    onError: async (error) => {
      if (error instanceof BusinessApiError && error.status === 409) await query.refetch();
    },
  });
  const humanMutation = useMutation({
    mutationFn: ({ task, approved, targetPath }: { task: WorkflowHumanTask; approved: boolean; targetPath: string }) =>
      submitWorkflowHumanDecision(workspaceId, workflowId, task, { approved, targetPath }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["business", workspaceId, "workflows"] });
      await query.refetch();
    },
    onError: async (error) => {
      if (error instanceof BusinessApiError && error.status === 409) await query.refetch();
    },
  });

  if (!workspaceId) return <BusinessWorkspaceGate description="Workflow 详情必须绑定已连接 Workspace；未连接时不会读取或控制 Run。" />;
  if (query.isError) {
    return (
      <div className="page-stack">
        <ErrorState
          title="Workflow 读取失败"
          description={query.error.message}
          onRetry={() => void query.refetch()}
        />
      </div>
    );
  }
  if (query.isPending) {
    return (
      <div className="page-stack">
        <Card>
          <p>正在读取 Workflow 详情…</p>
        </Card>
      </div>
    );
  }

  const run = query.data;
  const humanTask = run.humanTask;
  const { canPause, canResume, canCancel } = getWorkflowControlAvailability(run.status);
  const pendingControl = workflowControlPending(run.status, run.pauseRequested, run.cancelRequested);
  const controlsDisabled = mutation.isPending || humanMutation.isPending || query.isFetching || pendingControl;
  const humanDecisionPending = humanMutation.isPending || mutation.isPending || query.isFetching || pendingControl;

  return (
    <div className="page-stack">
      <div className="page-intro page-intro--split">
        <div>
          <p className="eyebrow">Workflow Run / durable state</p>
          <h1>{run.definitionId}</h1>
          <p>
            <Badge
              tone={
                run.status === "failed"
                  ? "danger"
                  : run.status === "succeeded"
                    ? "success"
                    : "info"
              }
            >
              {run.status}
            </Badge>{" "}
            <span className="muted">version {run.version}</span>
          </p>
        </div>
        <div className="hash-card">
          <span>Run ID</span>
          <code>{run.id}</code>
        </div>
      </div>
      <Card>
        <CardHeader eyebrow="控制" title="公开命令" />
        {humanTask ? <WorkflowHumanTaskDecision
          task={humanTask}
          pending={humanDecisionPending}
          error={humanMutation.error}
          onDecide={(approved, targetPath) => humanMutation.mutate({ task: humanTask, approved, targetPath })}
        /> : null}
        {canPause || canResume || canCancel ? <div className="button-row">
          {canPause ? <Button
            variant="secondary"
            disabled={controlsDisabled}
            onClick={() => mutation.mutate("pause")}
          >
            <Pause size={15} />暂停
          </Button> : null}
          {canResume ? <Button
            variant="secondary"
            disabled={controlsDisabled}
            onClick={() => mutation.mutate("resume")}
          >
            <Play size={15} />恢复
          </Button> : null}
          {canCancel ? <Button
            variant="danger"
            disabled={controlsDisabled}
            onClick={() => mutation.mutate("cancel")}
          >
            <Square size={15} />取消
          </Button> : null}
        </div> : <UnavailableState title="当前 Run 不允许控制" description="该 Workflow 已进入终态，服务端不接受 pause、resume 或 cancel。" />}
        {pendingControl ? <UnavailableState title="正在等待安全检查点" description="服务端已受理控制请求；Run 到达安全检查点前，所有控制按钮保持禁用。" /> : null}
        {mutation.isError ? <p className="form-error" role="alert">{mutation.error.message}</p> : null}
        <p className="sidebar-note">
          控制命令携带 expected_version；版本冲突时请重新查询后再操作。
        </p>
      </Card>
      <Card>
        <CardHeader eyebrow="持久事实" title="Run 时间线" />
        <dl className="detail-grid">
          <div>
            <dt>创建</dt>
            <dd>{new Date(run.createdAt).toLocaleString("zh-CN")}</dd>
          </div>
          <div>
            <dt>更新</dt>
            <dd>{new Date(run.updatedAt).toLocaleString("zh-CN")}</dd>
          </div>
          <div>
            <dt>完成</dt>
            <dd>
              {run.completedAt
                ? new Date(run.completedAt).toLocaleString("zh-CN")
                : "尚未完成"}
            </dd>
          </div>
          <div>
            <dt>持续时间</dt>
            <dd>{formatElapsedDuration(run.createdAt, run.completedAt)}</dd>
          </div>
          <div>
            <dt>输入</dt>
            <dd>
              <pre className="json-preview">{JSON.stringify(run.input, null, 2)}</pre>
            </dd>
          </div>
        </dl>
        <UnavailableState
          title="节点级时间线尚未交付"
          description="当前公共契约只提供 Run 摘要，Tool Call、Token 和节点详情不会被前端猜测。"
        />
      </Card>
    </div>
  );
};
