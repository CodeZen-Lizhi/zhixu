import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowDownToLine, ArrowUpFromLine, Check, GitCompareArrows, KeyRound, LoaderCircle, RefreshCw, Save, TestTube2, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import {
  createGitSyncRun,
  getGitSyncStatus,
  gitSyncMaxChangedFiles,
  listGitSyncRuns,
  removeGitRemoteConfig,
  retryGitSyncRun,
  saveGitRemoteConfig,
  testGitRemoteConfig,
  type GitSecretAction,
  type GitFileChangeKind,
  type SaveGitRemoteInput,
  type TestGitRemoteInput,
  type GitSyncFailureClass,
  type GitSyncIndexStatus,
  type GitSyncRun,
  type GitSyncRunStatus,
} from "../../api/git-sync";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { Badge, Button, EmptyState, ErrorState } from "../../shared/ui";

import "./git-remote-settings.css";

type SecretActionKind = GitSecretAction["action"];
interface CommandScope { workspaceId: string; controller: AbortController; attemptKind: string }
type SaveCommand = CommandScope & { input: Omit<SaveGitRemoteInput, "token">; secretHandle: string };
type TestCommand = CommandScope & { input: Omit<TestGitRemoteInput, "token">; secretHandle: string };
type RemoveCommand = CommandScope & { expectedRevision: number; idempotencyKey: string };
type RunCommand = CommandScope & { idempotencyKey: string };
type RetryCommand = CommandScope & { run: GitSyncRun; idempotencyKey: string };
interface CommandAttempt { signature: string; key: string }

const activeStatuses = new Set<GitSyncRunStatus>(["PENDING", "FETCHING", "COMPARING", "FAST_FORWARDING", "PUSHING", "VERIFYING"]);
const statusLabel: Record<GitSyncRunStatus, string> = {
  PENDING: "等待执行", FETCHING: "正在获取远端", COMPARING: "正在比较", FAST_FORWARDING: "正在快进",
  PUSHING: "正在推送", VERIFYING: "正在核验", SUCCEEDED: "Git 已同步", CONFLICT: "需要处理冲突",
  FAILED: "同步失败", STALE: "配置已变化", MANUAL_RECOVERY_REQUIRED: "需要人工恢复",
};
const failureLabel: Record<GitSyncFailureClass, string> = {
  NONE: "无", DIRTY: "工作树有未提交修改", DETACHED: "当前处于 detached HEAD", DIVERGED: "本地与远端历史已分叉",
  AUTHENTICATION: "远端拒绝了访问令牌", OFFLINE: "无法连接远端", REF_DRIFT: "当前分支与配置不一致，或同步期间分支发生变化",
  NON_FAST_FORWARD: "远端拒绝非快进推送", STALE_CONFIG: "运行绑定的配置已过期", RESULT_UNKNOWN: "远端结果无法证明",
  DEPENDENCY: "同步依赖不可用", INTERNAL: "同步内部状态异常",
};
const indexLabel: Record<GitSyncIndexStatus, string> = {
  NOT_REQUIRED: "无需更新", PENDING: "等待重新索引", RUNNING: "正在重新索引", SUCCEEDED: "知识索引已更新", FAILED: "重新索引失败",
};
const fileChangeLabel: Record<GitFileChangeKind, string> = {
  ADDED: "新增",
  MODIFIED: "修改",
  DELETED: "删除",
  RENAMED: "重命名",
};

const statusTone = (status: GitSyncRunStatus): "success" | "warning" | "danger" | "info" => status === "SUCCEEDED" ? "success" : activeStatuses.has(status) ? "info" : status === "CONFLICT" || status === "STALE" ? "warning" : "danger";
const indexTone = (status: GitSyncIndexStatus): "success" | "warning" | "danger" | "neutral" => status === "SUCCEEDED" ? "success" : status === "FAILED" ? "danger" : status === "PENDING" || status === "RUNNING" ? "warning" : "neutral";
const shortOID = (value?: string): string => value ? `${value.slice(0, 12)}…` : "未记录";

const actionPayload = (kind: SecretActionKind, token: string): GitSecretAction => kind === "replace" ? { action: "replace", value: token } : { action: kind };

const RunSummary = ({ run, onRetry, retrying }: { run: GitSyncRun; onRetry: () => void; retrying: boolean }) => {
  const retryIndex = run.status === "SUCCEEDED" && run.indexStatus === "FAILED";
  const showRetry = retryIndex || (run.status !== "SUCCEEDED" && (run.retryable || run.status === "CONFLICT" || run.status === "STALE"));
  return <article className="git-sync-run" aria-label={`同步运行 ${run.id}`}>
    <header><div><Badge tone={statusTone(run.status)}>{statusLabel[run.status]}</Badge><span>{new Date(run.createdAt).toLocaleString("zh-CN")}</span></div><span className="mono">{run.id.slice(0, 8)}</span></header>
    <div className="git-sync-run__facts">
      <div><span>方向</span><strong>{run.direction === "PULL" ? <><ArrowDownToLine size={14} />拉取</> : run.direction === "PUSH" ? <><ArrowUpFromLine size={14} />推送</> : run.direction === "NONE" ? "无变更" : "尚未判断"}</strong></div>
      <div><span>本地 / 远端</span><strong className="mono">{shortOID(run.verifiedHeadOid ?? run.expectedHeadOid)} / {shortOID(run.verifiedRemoteOid ?? run.expectedRemoteOid)}</strong></div>
      <div><span>知识索引</span><strong><Badge tone={indexTone(run.indexStatus)}>{indexLabel[run.indexStatus]}</Badge></strong></div>
    </div>
    {run.failureClass === "NONE" ? null : <p className="git-sync-run__notice" role="alert"><AlertTriangle size={15} />{failureLabel[run.failureClass]}{run.errorCode ? `（${run.errorCode}）` : ""}</p>}
    {run.changedFiles.length === 0 ? null : <details className="git-sync-run__changes"><summary>{run.changedFiles.length === gitSyncMaxChangedFiles ? `显示前 ${String(gitSyncMaxChangedFiles)} 个文件变化，可能还有更多` : `${String(run.changedFiles.length)} 个文件发生变化`}</summary><ul>{run.changedFiles.map((change) => <li key={`${change.kind}:${change.oldPath ?? ""}:${change.path}`}><span>{fileChangeLabel[change.kind]}</span><code>{change.oldPath ? `${change.oldPath} → ${change.path}` : change.path}</code></li>)}</ul></details>}
    {showRetry ? <Button size="sm" variant="secondary" disabled={retrying} onClick={onRetry}><RefreshCw className={retrying ? "is-spinning" : undefined} size={14} />{retrying ? "正在重试…" : retryIndex ? "重试索引" : "重新同步"}</Button> : null}
  </article>;
};

export const GitRemoteSettingsPanel = () => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const activeWorkspaceRef = useRef(workspaceId);
  const controllersRef = useRef(new Map<AbortController, string>());
  const attemptsRef = useRef(new Map<string, CommandAttempt>());
  const secretActionsRef = useRef(new Map<string, GitSecretAction>());
  activeWorkspaceRef.current = workspaceId;
  const status = useQuery({
    queryKey: ["settings", "git-sync", workspaceId, "status"], queryFn: ({ signal }) => getGitSyncStatus(workspaceId, signal),
    enabled: workspaceId !== "", retry: false,
    refetchInterval: (query) => query.state.data?.currentRun && activeStatuses.has(query.state.data.currentRun.status) ? 1500 : false,
  });
  const runs = useQuery({ queryKey: ["settings", "git-sync", workspaceId, "runs"], queryFn: ({ signal }) => listGitSyncRuns(workspaceId, undefined, 8, signal), enabled: workspaceId !== "", retry: false });
  const config = status.data?.config;
  const [loadedConfigScope, setLoadedConfigScope] = useState("");
  const [loadedRevision, setLoadedRevision] = useState(-1);
  const [remoteUrl, setRemoteUrl] = useState("");
  const [branch, setBranch] = useState("main");
  const [autoSync, setAutoSync] = useState(false);
  const [secretAction, setSecretAction] = useState<SecretActionKind>("replace");
  const [token, setToken] = useState("");
  const [feedback, setFeedback] = useState("");
  const [commandError, setCommandError] = useState("");

  useEffect(() => {
    if (!config || (config.workspaceId === loadedConfigScope && config.revision === loadedRevision)) return;
    setLoadedConfigScope(config.workspaceId);
    setLoadedRevision(config.revision);
    setRemoteUrl(config.remoteUrl ?? "");
    setBranch(config.branch ?? "main");
    setAutoSync(config.autoSync);
    setSecretAction(config.tokenConfigured ? "keep" : "replace");
    setToken("");
  }, [config, loadedConfigScope, loadedRevision]);

  const refresh = useCallback(async (scopeWorkspaceId: string): Promise<void> => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["settings", "git-sync", scopeWorkspaceId, "status"] }),
      queryClient.invalidateQueries({ queryKey: ["settings", "git-sync", scopeWorkspaceId, "runs"] }),
    ]);
  }, [queryClient]);
  const activeScope = (scopeWorkspaceId: string): boolean => activeWorkspaceRef.current === scopeWorkspaceId;
  const startCommand = (scopeWorkspaceId: string): AbortController => {
    const controller = new AbortController();
    controllersRef.current.set(controller, scopeWorkspaceId);
    return controller;
  };
  const finishCommand = (controller: AbortController): void => { controllersRef.current.delete(controller); };
  const commandKey = (kind: string, signature: string): string => {
    const prior = attemptsRef.current.get(kind);
    if (prior?.signature === signature) return prior.key;
    const key = `${kind}-${crypto.randomUUID()}`;
    attemptsRef.current.set(kind, { signature, key });
    return key;
  };
  const clearAttempt = (kind: string): void => { attemptsRef.current.delete(kind); };
  const retainAttemptAfterError = (error: unknown): boolean => typeof error === "object" && error !== null && "retryable" in error && (error as { retryable?: unknown }).retryable === true;
  const reportError = (error: unknown, scopeWorkspaceId: string): void => {
    if (activeScope(scopeWorkspaceId)) setCommandError(error instanceof Error ? error.message : "Git 同步请求未完成。");
  };
  const save = useMutation({
    mutationFn: (command: SaveCommand) => {
      const token = secretActionsRef.current.get(command.secretHandle);
      secretActionsRef.current.delete(command.secretHandle);
      if (token === undefined) throw new Error("Git 访问令牌已失效，请重新输入。");
      return saveGitRemoteConfig(command.workspaceId, { ...command.input, token }, command.controller.signal);
    },
    onSuccess: async (saved, command) => {
      clearAttempt(command.attemptKind);
      if (!activeScope(command.workspaceId)) return;
      setToken(""); setSecretAction(saved.tokenConfigured ? "keep" : "replace"); setFeedback("远端配置已保存。"); await refresh(command.workspaceId);
    },
    onError: (error, command) => { clearAttempt(command.attemptKind); reportError(error, command.workspaceId); },
    onSettled: (_data, _error, command) => { finishCommand(command.controller); queueMicrotask(() => { if (save.variables === command) save.reset(); }); },
  });
  const test = useMutation({
    mutationFn: (command: TestCommand) => {
      const token = secretActionsRef.current.get(command.secretHandle);
      secretActionsRef.current.delete(command.secretHandle);
      if (token === undefined) throw new Error("Git 访问令牌已失效，请重新输入。");
      return testGitRemoteConfig(command.workspaceId, { ...command.input, token }, command.controller.signal);
    },
    onSuccess: (_result, command) => { clearAttempt(command.attemptKind); if (activeScope(command.workspaceId)) { setToken(""); setFeedback("当前填写的连接信息测试通过。"); } },
    onError: (error, command) => { clearAttempt(command.attemptKind); reportError(error, command.workspaceId); },
    onSettled: (_data, _error, command) => { finishCommand(command.controller); queueMicrotask(() => { if (test.variables === command) test.reset(); }); },
  });
  const remove = useMutation({
    mutationFn: (command: RemoveCommand) => removeGitRemoteConfig(command.workspaceId, { expectedRevision: command.expectedRevision, idempotencyKey: command.idempotencyKey }, command.controller.signal),
    onSuccess: async (_saved, command) => { clearAttempt(command.attemptKind); if (activeScope(command.workspaceId)) { setFeedback("Git 远端配置已移除。"); setToken(""); await refresh(command.workspaceId); } },
    onError: (error, command) => { if (!retainAttemptAfterError(error)) clearAttempt(command.attemptKind); reportError(error, command.workspaceId); },
    onSettled: (_data, _error, command) => { finishCommand(command.controller); queueMicrotask(() => { if (remove.variables === command) remove.reset(); }); },
  });
  const sync = useMutation({
    mutationFn: (command: RunCommand) => createGitSyncRun(command.workspaceId, { idempotencyKey: command.idempotencyKey }, command.controller.signal),
    onSuccess: async (_run, command) => { clearAttempt(command.attemptKind); if (activeScope(command.workspaceId)) { setFeedback("同步任务已进入队列。"); await refresh(command.workspaceId); } },
    onError: (error, command) => { if (!retainAttemptAfterError(error)) clearAttempt(command.attemptKind); reportError(error, command.workspaceId); },
    onSettled: (_data, _error, command) => { finishCommand(command.controller); queueMicrotask(() => { if (sync.variables === command) sync.reset(); }); },
  });
  const retry = useMutation({
    mutationFn: (command: RetryCommand) => retryGitSyncRun(command.workspaceId, command.run.id, { expectedVersion: command.run.version, idempotencyKey: command.idempotencyKey }, command.controller.signal),
    onSuccess: async (_run, command) => { clearAttempt(command.attemptKind); if (activeScope(command.workspaceId)) await refresh(command.workspaceId); },
    onError: (error, command) => { if (!retainAttemptAfterError(error)) clearAttempt(command.attemptKind); reportError(error, command.workspaceId); },
    onSettled: (_data, _error, command) => { finishCommand(command.controller); queueMicrotask(() => { if (retry.variables === command) retry.reset(); }); },
  });

  useEffect(() => {
    for (const [controller, commandWorkspaceId] of controllersRef.current) {
      if (commandWorkspaceId !== workspaceId) { controller.abort(); controllersRef.current.delete(controller); }
    }
    attemptsRef.current.clear();
    secretActionsRef.current.clear();
    setLoadedConfigScope(""); setLoadedRevision(-1); setRemoteUrl(""); setBranch("main"); setAutoSync(false);
    setSecretAction("replace"); setToken(""); setFeedback(""); setCommandError("");
    save.reset(); test.reset(); remove.reset(); sync.reset(); retry.reset();
  }, [workspaceId]);

  const canKeep = Boolean(config?.tokenConfigured && config.remoteUrl === remoteUrl.trim() && config.branch === branch.trim());
  const validation = useMemo(() => {
    if (!remoteUrl.trim().startsWith("https://")) return "Remote URL 必须使用 HTTPS。";
    if (branch.trim() === "") return "请填写远端分支。";
    if (secretAction === "keep" && !canKeep) return "Remote URL 或分支已改变，请替换访问令牌。";
    if (secretAction === "replace" && token.trim() === "") return "请输入访问令牌。";
    if (secretAction === "clear") return "同步需要访问令牌；清除后只能保存为未就绪配置。";
    return undefined;
  }, [branch, canKeep, remoteUrl, secretAction, token]);
  const saveBlocked = Boolean(validation && secretAction !== "clear");
  const pending = save.isPending || test.isPending || remove.isPending;
  const mutationError = commandError;

  const submitWithSecret = (target: "save" | "test"): void => {
    setFeedback(""); setCommandError("");
    if (validation && !(target === "save" && secretAction === "clear")) return;
    const action = actionPayload(secretAction, token);
    const scopeWorkspaceId = workspaceId;
    const expectedRevision = config?.revision ?? 0;
    const trimmedURL = remoteUrl.trim(); const trimmedBranch = branch.trim();
    const kind = target === "save" ? "git-remote-save" : "git-remote-test";
    const idempotencyKey = commandKey(kind, JSON.stringify({ scopeWorkspaceId, expectedRevision, trimmedURL, trimmedBranch, autoSync, action: action.action }));
    const controller = startCommand(scopeWorkspaceId);
    const secretHandle = crypto.randomUUID();
    secretActionsRef.current.set(secretHandle, action);
    setToken("");
    if (target === "save") save.mutate({ workspaceId: scopeWorkspaceId, controller, attemptKind: kind, secretHandle, input: { expectedRevision, remoteUrl: trimmedURL, branch: trimmedBranch, autoSync, idempotencyKey } });
    else test.mutate({ workspaceId: scopeWorkspaceId, controller, attemptKind: kind, secretHandle, input: { expectedRevision, remoteUrl: trimmedURL, branch: trimmedBranch, idempotencyKey } });
  };
  const removeConfig = (): void => {
    const scopeWorkspaceId = workspaceId; const expectedRevision = config?.revision ?? 0;
    setFeedback(""); setCommandError("");
    const attemptKind = "git-remote-remove";
    remove.mutate({ workspaceId: scopeWorkspaceId, expectedRevision, idempotencyKey: commandKey(attemptKind, JSON.stringify({ scopeWorkspaceId, expectedRevision })), controller: startCommand(scopeWorkspaceId), attemptKind });
  };
  const createRun = (): void => {
    const scopeWorkspaceId = workspaceId;
    setFeedback(""); setCommandError("");
    const attemptKind = "git-sync-create";
    sync.mutate({ workspaceId: scopeWorkspaceId, idempotencyKey: commandKey(attemptKind, scopeWorkspaceId), controller: startCommand(scopeWorkspaceId), attemptKind });
  };
  const retryRun = (run: GitSyncRun): void => {
    const scopeWorkspaceId = workspaceId;
    setFeedback(""); setCommandError("");
    const attemptKind = "git-sync-retry";
    retry.mutate({ workspaceId: scopeWorkspaceId, run, idempotencyKey: commandKey(attemptKind, JSON.stringify({ scopeWorkspaceId, runId: run.id, version: run.version })), controller: startCommand(scopeWorkspaceId), attemptKind });
  };

  if (workspaceId === "") return <EmptyState title="尚未连接工作区" description="连接工作区后才能配置它的 Git 远端。" />;
  if (status.isPending) return <p className="git-sync-loading" role="status"><LoaderCircle className="is-spinning" size={16} />正在读取 Git 同步配置…</p>;
  if (status.isError) return <ErrorState title="Git 同步配置不可用" description={status.error.message} onRetry={() => void status.refetch()} />;
  if (loadedConfigScope !== status.data.config.workspaceId || loadedRevision !== status.data.config.revision) return <p className="git-sync-loading" role="status"><LoaderCircle className="is-spinning" size={16} />正在切换 Git 同步配置…</p>;

  const loadedConfig = status.data.config;
  const currentRun = status.data.currentRun;

  return <section className="settings-section git-remote-settings" aria-labelledby="git-remote-settings-title">
    <header className="settings-section__header"><div><h2 id="git-remote-settings-title">Git 同步</h2><p>使用标准 HTTPS Remote 同步当前分支；不会自动合并、变基或强制推送。</p></div>{loadedConfig.configured ? <Badge tone={loadedConfig.tokenConfigured ? "success" : "warning"}>{loadedConfig.tokenConfigured ? "已配置" : "缺少令牌"}</Badge> : <Badge tone="neutral">未配置</Badge>}</header>
    <form className="git-remote-form" onSubmit={(event) => { event.preventDefault(); submitWithSecret("save"); }}>
      <div className="git-remote-fields">
        <label className="git-remote-field--wide">远端 URL<input type="url" inputMode="url" autoComplete="off" value={remoteUrl} maxLength={2048} placeholder="https://git.example.com/team/knowledge.git" disabled={pending} onChange={(event) => setRemoteUrl(event.target.value)} /></label>
        <label>分支<input value={branch} maxLength={255} autoComplete="off" disabled={pending} onChange={(event) => setBranch(event.target.value)} /></label>
        <label className="git-remote-toggle"><input type="checkbox" checked={autoSync} disabled={pending} onChange={(event) => setAutoSync(event.target.checked)} /><span><strong>批准写回后自动同步</strong><small>默认关闭；失败不会回滚本地提交。</small></span></label>
      </div>
      <fieldset className="git-secret-control"><legend><KeyRound size={15} />访问令牌</legend><div role="radiogroup" aria-label="访问令牌操作">
        <label><input type="radio" name="git-token-action" checked={secretAction === "keep"} disabled={!canKeep || pending} onChange={() => { setSecretAction("keep"); setToken(""); }} />保留</label>
        <label><input type="radio" name="git-token-action" checked={secretAction === "replace"} disabled={pending} onChange={() => setSecretAction("replace")} />替换</label>
        <label><input type="radio" name="git-token-action" checked={secretAction === "clear"} disabled={pending} onChange={() => { setSecretAction("clear"); setToken(""); }} />清除</label>
      </div><input aria-label="访问令牌" type="password" autoComplete="new-password" maxLength={16 * 1024} value={token} disabled={pending || secretAction !== "replace"} placeholder={secretAction === "keep" ? "保留已保存令牌" : secretAction === "clear" ? "保存后清除令牌" : "输入新的访问令牌"} onChange={(event) => setToken(event.target.value)} /></fieldset>
      {validation ? <p className="git-remote-validation"><AlertTriangle size={14} />{validation}</p> : null}
      {mutationError ? <p className="git-remote-error" role="alert">{mutationError}</p> : null}
      {feedback ? <p className="git-remote-feedback" role="status"><Check size={15} />{feedback}</p> : null}
      <div className="git-remote-actions"><Button type="submit" disabled={pending || saveBlocked}><Save size={15} />{save.isPending ? "正在保存…" : "保存配置"}</Button><Button type="button" variant="secondary" disabled={pending || Boolean(validation)} onClick={() => submitWithSecret("test")}><TestTube2 size={15} />{test.isPending ? "正在测试…" : "测试连接"}</Button>{loadedConfig.configured ? <Button type="button" variant="ghost" disabled={pending} onClick={() => { if (window.confirm("移除 Git 远端配置？历史同步记录会保留。")) removeConfig(); }}><Trash2 size={15} />移除配置</Button> : null}</div>
    </form>

    <div className="git-sync-divider" />
    <section className="git-sync-status" aria-labelledby="git-sync-status-title"><header><div><h3 id="git-sync-status-title">同步状态</h3><p>Git 与知识索引分别核验。</p></div><Button size="sm" disabled={!loadedConfig.configured || !loadedConfig.tokenConfigured || sync.isPending || Boolean(currentRun && activeStatuses.has(currentRun.status))} onClick={createRun}><GitCompareArrows size={15} />{sync.isPending ? "正在创建…" : "立即同步"}</Button></header>
      {currentRun ? <RunSummary run={currentRun} retrying={retry.isPending && retry.variables.run.id === currentRun.id} onRetry={() => retryRun(currentRun)} /> : <EmptyState title="还没有同步记录" description="保存可用配置后，可以手动发起第一次同步。" />}
    </section>
    <section className="git-sync-history" aria-labelledby="git-sync-history-title"><header><h3 id="git-sync-history-title">最近运行</h3><Button size="sm" variant="ghost" onClick={() => void runs.refetch()} disabled={runs.isFetching}><RefreshCw className={runs.isFetching ? "is-spinning" : undefined} size={14} />刷新</Button></header>{runs.isError ? <p className="git-remote-error" role="alert">{runs.error.message}</p> : runs.data?.items.length ? <div className="git-sync-history__list">{runs.data.items.filter((run) => run.id !== currentRun?.id).slice(0, 5).map((run) => <RunSummary key={run.id} run={run} retrying={retry.isPending && retry.variables.run.id === run.id} onRetry={() => retryRun(run)} />)}</div> : <p className="muted">暂无历史运行。</p>}</section>
  </section>;
};
