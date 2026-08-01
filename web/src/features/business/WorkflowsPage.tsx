import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Pause, Play, Square } from "lucide-react";
import { useEffect } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";

import {
  BusinessApiError,
  controlWorkflow,
  getWorkflow,
  listWorkflows,
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
      controlWorkflow(workflowId, action, query.data?.version ?? 0),
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
  const { canPause, canResume, canCancel } = getWorkflowControlAvailability(run.status);
  const pendingControl = workflowControlPending(run.status, run.pauseRequested, run.cancelRequested);
  const controlsDisabled = mutation.isPending || query.isFetching || pendingControl;

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
