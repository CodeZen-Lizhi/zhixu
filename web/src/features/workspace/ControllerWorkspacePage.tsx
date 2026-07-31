import { ArrowRight, GitBranch, RefreshCw, Trash2, X } from "lucide-react";
import { type SyntheticEvent, useState } from "react";
import { Link } from "react-router-dom";

import { ControllerApiError, type ControllerOperation, type ControllerWorkspace } from "../../api/controller";
import { useHostControl } from "../../app/host-control-context";

const phaseLabels: Readonly<Record<ControllerOperation["phase"], string>> = {
  validating: "正在校验宿主机目录",
  quiescing: "正在停止旧 Workspace 的写入",
  revoking: "正在撤销旧目录授权",
  applying_grant: "正在应用新目录授权",
  preparing: "正在准备业务运行时",
  verifying: "正在验证 API 与 Worker",
  committing: "正在提交切换状态",
  activating: "正在激活 Workspace",
  rolling_back: "目标启动失败，正在回滚",
  recovering: "正在恢复可用运行时",
};

const processLabels = {
  stopped: "已停止",
  starting: "启动中",
  prepared: "已准备",
  ready: "就绪",
  unavailable: "不可用",
} as const;

const availabilityLabels = {
  available: "可用",
  unavailable: "不可用",
  migration_required: "需要迁移",
} as const;

const operationErrorMessages: Readonly<Record<string, string>> = {
  WORKSPACE_RUNTIME_ROOT_PERMISSION_DENIED: "运行容器无权访问所选目录。请检查 Docker 文件共享设置和该目录的读写权限。",
  WORKSPACE_RUNTIME_ROOT_UNAVAILABLE: "运行容器无法访问所选宿主机目录。请确认目录仍然存在且已允许 Docker 访问。",
  WORKSPACE_RUNTIME_ROOT_PROBE_FAILED: "运行容器未能完成所选目录的读写验证。请检查目录权限后重试。",
  WORKSPACE_GIT_REQUIRED: "所选目录不是 Git 仓库。如需在该目录创建仓库，请勾选“初始化 Git 仓库”后重试。",
  WORKSPACE_GIT_INVALID: "所选目录的 Git 仓库无法安全验证。请检查仓库状态后重试。",
  WORKSPACE_GIT_METADATA_OUTSIDE_ROOT: "Git 元数据位于所选目录之外，无法只授权这个目录。请选择独立仓库目录。",
  WORKSPACE_GIT_INIT_FAILED: "无法在所选目录中初始化 Git 仓库。请检查目录写入权限。",
  WORKSPACE_PATH_IDENTITY_CHANGED: "目录在验证期间发生了变化。请确认路径后重新选择。",
  WORKSPACE_CANDIDATE_DATABASE_UNAVAILABLE: "候选运行时暂时无法连接数据库。请稍后重试。",
  WORKSPACE_CANDIDATE_REGISTRATION_FAILED: "候选运行时未能登记准备状态。请稍后重试。",
  WORKSPACE_QUIESCENCE_TIMEOUT: "旧 Workspace 未能在安全检查点暂停，切换已取消。请稍后重试。",
  WORKSPACE_RUNTIME_REVOKE_FAILED: "旧目录授权未能确认撤销，系统已停止继续切换。请重启服务后再检查。",
};

const formatLastOpened = (value?: string): string => {
  if (value === undefined) return "尚未打开";
  return new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
};

const ControllerErrorNotice = ({ error }: { error: Error }) => (
  <div className="workspace-alert workspace-alert--error" role="alert">
    <strong>控制请求失败</strong>
    <span>{error.message}</span>
    {error instanceof ControllerApiError ? <code>{error.errorCode}</code> : null}
  </div>
);

const OperationNotice = ({ operation }: { operation: ControllerOperation }) => {
  const result = operation.result;
  const title = result === undefined
    ? phaseLabels[operation.phase]
    : result === "succeeded"
      ? "Workspace 已切换"
      : result === "rolled_back"
        ? "切换未完成，原 Workspace 已恢复"
        : result === "rejected"
          ? "切换请求未通过校验"
          : result === "cancelled"
            ? "切换已取消"
            : "自动恢复失败";
  const tone = result === "failed" || result === "rejected" ? "error" : result === "rolled_back" ? "warning" : "progress";

  return (
    <section className={`control-operation control-operation--${tone}`} aria-live="polite">
      <p className="eyebrow">运行边界操作</p>
      <h2>{title}</h2>
      {result === undefined ? <p>控制器会在完成授权、API 与 Worker 验证后再开放业务页面。</p> : null}
      {result === "rolled_back" ? <p>目标目录未成功激活。当前页面只会在原运行时重新就绪后恢复业务访问。</p> : null}
      {result === "failed" ? <p>当前没有可安全使用的业务运行时。请检查目录与运行环境后重新发起恢复。</p> : null}
      {operation.errorCode === undefined ? null : (
        <>
          <p className="control-operation__error-detail">
            {operationErrorMessages[operation.errorCode] ?? "请根据错误代码检查目录和运行环境后重试。"}
          </p>
          <code>{operation.errorCode}</code>
        </>
      )}
    </section>
  );
};

const RegistryRow = ({
  workspace,
  active,
  disabled,
  removalCandidate,
  onSwitch,
  onCheck,
  onRemoveIntent,
  onRemove,
  onCancelRemove,
}: {
  workspace: ControllerWorkspace;
  active: boolean;
  disabled: boolean;
  removalCandidate: boolean;
  onSwitch: () => void;
  onCheck: () => void;
  onRemoveIntent: () => void;
  onRemove: () => void;
  onCancelRemove: () => void;
}) => (
  <li className="control-registry-row">
    <div className="control-registry-row__identity">
      <div className="control-registry-row__title">
        <h3>{workspace.name}</h3>
        <span className={`control-status control-status--${workspace.availability}`}>{active ? "活动" : availabilityLabels[workspace.availability]}</span>
      </div>
      <code className="workspace-path control-host-path" title={workspace.rootPath}>{workspace.rootPath}</code>
      <small>{formatLastOpened(workspace.lastOpenedAt)}</small>
      {workspace.availabilityReason === undefined ? null : <p className="control-registry-row__reason">{workspace.availabilityReason}</p>}
    </div>
    {active ? <span className="control-registry-row__active">当前运行目录</span> : null}
    {!active && workspace.availability === "available" ? (
      <button type="button" className="secondary-button control-row-action" disabled={disabled} onClick={onSwitch}>
        <ArrowRight aria-hidden="true" size={17} />切换
      </button>
    ) : null}
    {!active && workspace.availability !== "available" && !removalCandidate ? (
      <div className="control-registry-row__actions">
        <button type="button" className="secondary-button control-row-action" disabled={disabled} onClick={onCheck}>
          <RefreshCw aria-hidden="true" size={17} />重新检查
        </button>
        <button type="button" className="secondary-button control-row-action control-row-action--danger" disabled={disabled} onClick={onRemoveIntent}>
          <Trash2 aria-hidden="true" size={17} />移除记录
        </button>
      </div>
    ) : null}
    {!active && removalCandidate ? (
      <div className="control-remove-confirmation" role="group" aria-label={`确认移除 ${workspace.name}`}>
        <p>只移除最近列表中的登记记录，不会删除宿主机文件。</p>
        <div>
          <button type="button" className="control-row-action control-row-action--danger" disabled={disabled} onClick={onRemove}>
            <Trash2 aria-hidden="true" size={17} />确认移除
          </button>
          <button type="button" className="secondary-button control-row-action" disabled={disabled} onClick={onCancelRemove}>
            <X aria-hidden="true" size={17} />取消
          </button>
        </div>
      </div>
    ) : null}
  </li>
);

export const ControllerWorkspacePage = () => {
  const {
    state,
    actionPending,
    actionError,
    createAndSwitchWorkspace,
    switchToRegisteredWorkspace,
    checkWorkspaceAvailability,
    removeWorkspace,
  } = useHostControl();
  const [name, setName] = useState("");
  const [rootPath, setRootPath] = useState("");
  const [initializeGit, setInitializeGit] = useState(false);
  const [removalCandidate, setRemovalCandidate] = useState<string>();

  if (state.status !== "ready") return null;
  const controlState = state.controlState;
  const active = controlState.activeWorkspace;
  const operation = controlState.operation;
  const operationRunning = operation !== null && operation.result === undefined;
  const controlsDisabled = actionPending || operationRunning || controlState.runtime.status === "switching";

  const submit = (event: SyntheticEvent<HTMLFormElement>): void => {
    event.preventDefault();
    void createAndSwitchWorkspace({ name: name.trim(), rootPath: rootPath.trim(), initializeGit });
  };

  return (
    <div className="workspace-page controller-workspace-page">
      <header className="workspace-intro controller-workspace-intro">
        <p className="brand-mark">知序 · HOST CONTROL</p>
        <h2>{active === null ? "选择真实工作目录。" : "管理本机 Workspace。"}</h2>
        <p>目录授权由本机控制器持有；只有活动目录、API 与 Worker 同时就绪时，业务页面才会开放。</p>
      </header>

      <section className="control-runtime-band" aria-label="本机运行状态">
        <div>
          <span>控制状态</span>
          <strong>{controlState.runtime.status === "ready" ? "业务运行时就绪" : controlState.runtime.status === "waiting_for_workspace" ? "等待 Workspace" : controlState.runtime.status === "switching" ? "正在切换" : "需要手动恢复"}</strong>
        </div>
        <div><span>API</span><strong>{processLabels[controlState.runtime.api.status]}</strong></div>
        <div><span>Worker</span><strong>{processLabels[controlState.runtime.worker.status]}</strong></div>
      </section>

      {operation === null ? null : <OperationNotice operation={operation} />}
      {controlState.runtime.status === "recovery_failed" && operation?.result !== "failed" ? (
        <section className="control-operation control-operation--error" role="alert">
          <p className="eyebrow">需要手动恢复</p>
          <h2>当前没有安全可用的业务运行时</h2>
          <p>检查宿主机目录与运行环境后，重新选择一个可用 Workspace。</p>
        </section>
      ) : null}
      {actionError === undefined ? null : <ControllerErrorNotice error={actionError} />}

      {active === null ? null : (
        <section className="control-active-workspace" aria-labelledby="control-active-title">
          <div>
            <p className="eyebrow">活动 Workspace</p>
            <h2 id="control-active-title">{active.name}</h2>
          </div>
          <div className="control-active-workspace__access">
            <code className="workspace-path control-host-path" title={active.rootPath}>{active.rootPath}</code>
            <Link className="workspace-action-link" to="/dashboard"><ArrowRight aria-hidden="true" size={17} />进入工作台</Link>
          </div>
        </section>
      )}

      <section className="workspace-connection control-workspace-setup" aria-labelledby="control-workspace-setup-title">
        <div className="section-heading workspace-connection__heading">
          <div>
            <p className="eyebrow">宿主机目录授权</p>
            <h2 id="control-workspace-setup-title">打开新的 Workspace</h2>
          </div>
          <p>填写已经存在的绝对路径。控制器只会授权你填写的这个目录，其他目录不会被授权。</p>
        </div>
        <form className="control-workspace-form" onSubmit={submit}>
          <label>
            <span>Workspace 名称</span>
            <input value={name} onChange={(event) => setName(event.target.value)} disabled={controlsDisabled} required />
          </label>
          <label>
            <span>宿主机目录</span>
            <input value={rootPath} onChange={(event) => setRootPath(event.target.value)} disabled={controlsDisabled} placeholder="/Users/me/knowledge" required />
          </label>
          <label className="checkbox-field control-git-option">
            <input type="checkbox" checked={initializeGit} onChange={(event) => setInitializeGit(event.target.checked)} disabled={controlsDisabled} />
            <span className="control-git-option__label">
              <GitBranch aria-hidden="true" size={18} />
              <span className="control-git-option__copy">如果目录尚未使用 Git，明确初始化 Git 仓库</span>
            </span>
          </label>
          <button type="submit" disabled={controlsDisabled || name.trim() === "" || rootPath.trim() === ""}>
            <ArrowRight aria-hidden="true" size={18} />{controlsDisabled ? "正在处理…" : "授权并打开"}
          </button>
        </form>
      </section>

      <section className="control-registry" aria-labelledby="control-registry-title">
        <div className="section-heading">
          <div>
            <p className="eyebrow">本机登记记录</p>
            <h2 id="control-registry-title">最近 Workspace</h2>
          </div>
          <p>可用性来自控制器的宿主机检查；浏览器不会探测、创建或改写这些目录。</p>
        </div>
        {controlState.recentWorkspaces.length === 0 ? (
          <p className="control-registry__empty">尚无登记记录。</p>
        ) : (
          <ul className="control-registry-list">
            {controlState.recentWorkspaces.map((workspace) => (
              <RegistryRow
                key={workspace.workspaceId}
                workspace={workspace}
                active={workspace.workspaceId === active?.workspaceId}
                disabled={controlsDisabled}
                removalCandidate={removalCandidate === workspace.workspaceId}
                onSwitch={() => void switchToRegisteredWorkspace(workspace.workspaceId)}
                onCheck={() => void checkWorkspaceAvailability(workspace.workspaceId)}
                onRemoveIntent={() => setRemovalCandidate(workspace.workspaceId)}
                onRemove={() => { setRemovalCandidate(undefined); void removeWorkspace(workspace.workspaceId); }}
                onCancelRemove={() => setRemovalCandidate(undefined)}
              />
            ))}
          </ul>
        )}
      </section>
    </div>
  );
};
