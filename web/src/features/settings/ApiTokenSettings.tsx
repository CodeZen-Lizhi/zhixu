import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { KeyRound } from "lucide-react";
import { useEffect, useState } from "react";

import { createApiToken, listApiTokens, revokeApiToken, type ApiTokenCredential, type AuthCapability } from "../../api/auth";
import { Button, EmptyState, UnavailableState } from "../../shared/ui";

const authCapabilities: readonly AuthCapability[] = ["READ_LOCAL", "READ_EXTERNAL", "WRITE_PROPOSAL", "WRITE_KNOWLEDGE", "GIT_WRITE", "INDEX_MAINTENANCE", "EVALUATION_RUN"];

const authCapabilityLabels: Record<AuthCapability, string> = {
  READ_LOCAL: "读取本地资料",
  READ_EXTERNAL: "读取外部资源",
  WRITE_PROPOSAL: "创建写入提案",
  WRITE_KNOWLEDGE: "应用知识变更",
  GIT_WRITE: "写入 Git",
  INDEX_MAINTENANCE: "维护索引",
  EVALUATION_RUN: "执行评测",
  MANAGE_SYSTEM_SETTINGS: "管理系统设置",
};

export const ApiTokenSettings = () => {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<AuthCapability[]>(["READ_LOCAL"]);
  const [expiresInSeconds, setExpiresInSeconds] = useState("2592000");
  const [created, setCreated] = useState<ApiTokenCredential>();
  const [createdConfirmed, setCreatedConfirmed] = useState(false);
  const [creating, setCreating] = useState(false);
  const [formError, setFormError] = useState<string>();
  const tokens = useInfiniteQuery({
    queryKey: ["auth", "api-tokens"],
    initialPageParam: "",
    queryFn: ({ pageParam, signal }) => listApiTokens(pageParam || undefined, 30, signal),
    getNextPageParam: (lastPage) => lastPage.nextCursor,
    retry: false,
  });
  const tokenItems = tokens.data?.pages.flatMap((page) => page.items) ?? [];
  const initialTokenListError = tokens.isError && tokenItems.length === 0;
  const handleCreate = async (): Promise<void> => {
    setFormError(undefined);
    if (creating || created !== undefined) {
      if (created !== undefined) setFormError("请先确认已复制上一个 API Token。");
      return;
    }
    if (name.trim() === "" || scopes.length === 0 || !Number.isSafeInteger(Number(expiresInSeconds)) || Number(expiresInSeconds) < 0) {
      setFormError("请填写名称、至少一个权限和有效期。");
      return;
    }

    setCreating(true);
    try {
      const credential = await createApiToken({ name: name.trim(), scopes, expiresInSeconds: Number(expiresInSeconds) });
      setCreated(credential);
      setCreatedConfirmed(false);
      setName("");
      void queryClient.invalidateQueries({ queryKey: ["auth", "api-tokens"] });
    } catch (error: unknown) {
      setFormError(error instanceof Error ? error.message : "创建 API Token 失败");
    } finally {
      setCreating(false);
    }
  };
  const revoke = useMutation({
    mutationFn: (tokenId: string) => revokeApiToken(tokenId),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["auth", "api-tokens"] }),
  });

  useEffect(() => {
    if (created === undefined || createdConfirmed) return undefined;
    const warnBeforeUnload = (event: BeforeUnloadEvent): void => event.preventDefault();
    const confirmInAppNavigation = (event: MouseEvent): void => {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const target = event.target instanceof Element ? event.target.closest("a[href]") : null;
      if (target === null || (target instanceof HTMLAnchorElement && (target.target === "_blank" || target.hasAttribute("download")))) return;
      if (window.confirm("该 API Token 尚未确认复制，离开后将无法再次查看。确定放弃它吗？")) return;
      event.preventDefault();
      event.stopPropagation();
    };
    window.addEventListener("beforeunload", warnBeforeUnload);
    document.addEventListener("click", confirmInAppNavigation, true);
    return () => {
      window.removeEventListener("beforeunload", warnBeforeUnload);
      document.removeEventListener("click", confirmInAppNavigation, true);
    };
  }, [created, createdConfirmed]);

  const toggleScope = (scope: AuthCapability) => setScopes((current) => current.includes(scope) ? current.filter((item) => item !== scope) : [...current, scope]);

  return <section className="settings-section" aria-labelledby="api-token-settings-title">
    <header className="settings-section__header"><div><h2 id="api-token-settings-title">API Token</h2><p>明文只显示一次，确认保存前离开页面会收到提醒。</p></div></header>
    {created ? <div className="token-reveal" role="alert" id="api-token-pending-copy"><strong>请立即复制 API Token</strong><code>{created.token}</code><p>服务端不会再次返回该值，也不会写入浏览器存储。</p><label className="token-scope"><input type="checkbox" checked={createdConfirmed} onChange={(event) => setCreatedConfirmed(event.target.checked)} />我已复制并安全保存该 Token</label><Button variant="secondary" size="sm" disabled={!createdConfirmed} onClick={() => { setCreated(undefined); setCreatedConfirmed(false); }}>关闭一次性 Token</Button></div> : null}
    <form className="token-form" aria-describedby={created === undefined ? undefined : "api-token-pending-copy"} onSubmit={(event) => {
      event.preventDefault();
      void handleCreate();
    }}>
      <div className="settings-form-grid"><label>名称<input value={name} maxLength={80} onChange={(event) => setName(event.target.value)} placeholder="例如：CI 只读" /></label><label>有效期（秒）<input value={expiresInSeconds} inputMode="numeric" onChange={(event) => setExpiresInSeconds(event.target.value)} /></label></div>
      <fieldset><legend>权限</legend>{authCapabilities.map((scope) => <label key={scope} className="token-scope"><input type="checkbox" checked={scopes.includes(scope)} onChange={() => toggleScope(scope)} />{authCapabilityLabels[scope]}</label>)}</fieldset>
      {formError ? <p className="form-error" role="alert">{formError}</p> : null}
      <Button type="submit" disabled={creating || created !== undefined}><KeyRound size={15} />{creating ? "正在创建…" : "创建 API Token"}</Button>
    </form>
    <div className="settings-section__body">
      {initialTokenListError ? <UnavailableState title="API Token 列表不可用" description={tokens.error.message} /> : tokens.isPending ? <p>正在读取 Token 元数据…</p> : tokenItems.length === 0 ? <EmptyState title="尚无 API Token" description="为脚本或自动化任务创建一个限权限 Token。" /> : <><div className="table-scroll"><table className="data-table"><thead><tr><th>名称</th><th>权限</th><th>有效期</th><th>最后使用</th><th /></tr></thead><tbody>{tokenItems.map((token) => <tr key={token.id}><td>{token.name}</td><td><span className="mono">{token.scopes.map((scope) => authCapabilityLabels[scope]).join(", ")}</span></td><td>{new Date(token.expiresAt).toLocaleString("zh-CN")}</td><td>{token.lastUsedAt ? new Date(token.lastUsedAt).toLocaleString("zh-CN") : "未使用"}</td><td><Button variant="danger" size="sm" onClick={() => revoke.mutate(token.id)} disabled={(revoke.isPending && revoke.variables === token.id) || token.revokedAt !== undefined}>{token.revokedAt ? "已撤销" : revoke.isPending && revoke.variables === token.id ? "撤销中…" : "撤销"}</Button></td></tr>)}</tbody></table></div>{revoke.isError ? <div className="ui-state ui-state--error" role="alert"><strong>撤销 API Token 失败</strong><p>{revoke.error.message}</p><Button variant="secondary" size="sm" onClick={() => revoke.mutate(revoke.variables)} disabled={revoke.isPending}>重试撤销 {tokenItems.find((token) => token.id === revoke.variables)?.name ?? "Token"}</Button></div> : null}{tokens.isFetchNextPageError ? <div className="ui-state ui-state--error" role="alert"><strong>加载更多 API Token 失败</strong><p>{tokens.error.message}</p><Button variant="secondary" size="sm" onClick={() => void tokens.fetchNextPage()} disabled={tokens.isFetchingNextPage}>重试加载更多 Token</Button></div> : null}{tokens.hasNextPage && !tokens.isFetchNextPageError ? <div className="pagination-row"><span /><Button variant="secondary" onClick={() => void tokens.fetchNextPage()} disabled={tokens.isFetchingNextPage}>{tokens.isFetchingNextPage ? "加载中…" : "加载更多 Token"}</Button></div> : null}</>}
    </div>
  </section>;
};
