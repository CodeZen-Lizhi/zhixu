import { useQuery, useQueryClient } from "@tanstack/react-query";
import { FileSearch, RotateCcw, Search as SearchIcon } from "lucide-react";
import { useCallback, useEffect, useState, type SyntheticEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";

import {
  SearchApiError,
  search,
  type SearchEvidence,
  type SearchMode,
  type SearchResponse,
} from "../../api/search";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { useRegisterWorkspaceRecovery } from "../../events/event-store";
import { Badge, Button, Card, CardHeader, EmptyState } from "../../shared/ui";
import { BusinessWorkspaceGate } from "../business/BusinessWorkspaceGate";
import { SourceSpanViewer } from "../source-spans";
import { searchQueryKeys } from "./query-keys";
import {
  normalizeSearchQuery,
  normalizeSourceVersionId,
  parseSearchUrlState,
  writeSearchUrlState,
} from "./url-state";

const searchModeLabels: Record<SearchMode, string> = {
  keyword: "Keyword",
  semantic: "Semantic",
  hybrid: "Hybrid",
};

const isSearchMode = (value: string): value is SearchMode =>
  value === "keyword" || value === "semantic" || value === "hybrid";

const isCursorRecoveryError = (error: unknown): boolean =>
  error instanceof SearchApiError
  && (error.code === "RETRIEVAL_SEARCH_CURSOR_INVALID" || error.code === "RETRIEVAL_SEARCH_CURSOR_STALE");

const formatTimestamp = (value: string): string => new Date(value).toLocaleString("zh-CN");

const scoreSummary = (item: SearchEvidence): string[] => {
  const values = [`Fusion #${String(item.scores.fusion.rank)} · ${item.scores.fusion.score.toFixed(4)}`];
  if (item.scores.lexical !== null) {
    values.push(`Lexical #${String(item.scores.lexical.rank)} · ${item.scores.lexical.score.toFixed(4)}`);
  }
  if (item.scores.vector !== null) {
    values.push(`Vector #${String(item.scores.vector.rank)} · distance ${item.scores.vector.distance.toFixed(4)}`);
  }
  if (item.scores.rerank !== null) {
    values.push(`Rerank #${String(item.scores.rerank.rank)} · ${item.scores.rerank.score.toFixed(4)}`);
  }
  return values;
};

const SearchEvidenceCard = ({ item, workspaceId }: { item: SearchEvidence; workspaceId: string }) => {
  const primaryProvenance = item.provenances[0];
  const title = item.headingPath.length > 0
    ? item.headingPath.join(" / ")
    : primaryProvenance?.relativePath ?? "未命名 Evidence";

  return <article className="search-result">
    <header className="search-result__header">
      <div>
        <p className="eyebrow">Evidence #{String(item.sequence)}</p>
        <h3>{title}</h3>
      </div>
      <Badge tone={item.scores.rerank === null ? "info" : "success"}>
        Fusion #{String(item.scores.fusion.rank)}
      </Badge>
    </header>
    <p className="search-result__snippet">{item.snippet}</p>
    <ul className="search-score-list" aria-label="检索阶段分数">
      {scoreSummary(item).map((score) => <li key={score}>{score}</li>)}
    </ul>
    <div className="search-provenance-list">
      {item.provenances.map((provenance) => <section key={`${provenance.sourceVersionId}:${item.span.spanId}`}>
        <div>
          <strong>{provenance.relativePath}</strong>
          <small>捕获于 {formatTimestamp(provenance.capturedAt)}</small>
        </div>
        <div className="search-provenance-actions">
          <Link className="ui-button ui-button--ghost" to={`/documents/${provenance.sourceVersionId}`}>
            资料版本
          </Link>
          <SourceSpanViewer className="ui-button ui-button--secondary" label="打开证据片段" reference={{ workspaceId, sourceVersionId: provenance.sourceVersionId, sourceSpanId: item.span.spanId }} />
        </div>
      </section>)}
    </div>
    {item.provenanceTruncated ? <p className="search-result__notice">该 Evidence 还有更多来源，当前响应已按服务端上限截断。</p> : null}
  </article>;
};

const SearchResultSummary = ({ response, fetching }: { response: SearchResponse; fetching: boolean }) => <Card>
  <CardHeader
    eyebrow="检索事实"
    title={`${String(response.items.length)} 条当前页 Evidence`}
    description="Search 不返回虚构总数；结果来自当前 Active Index 的有界窗口。"
  />
  <div className="search-result-summary" role="status" aria-live="polite">
    <div><span>请求模式</span><Badge tone="neutral">{searchModeLabels[response.requestedMode]}</Badge></div>
    <div><span>实际模式</span><Badge tone={response.requestedMode === response.effectiveMode ? "success" : "warning"}>{searchModeLabels[response.effectiveMode]}</Badge></div>
    <div><span>索引版本</span><code>{response.indexVersionId}</code></div>
    <div><span>Embedding</span><code>{response.embeddingVersionId ?? "未启用"}</code></div>
    {fetching ? <div><span>刷新状态</span><Badge tone="info">正在回查</Badge></div> : null}
  </div>
  {response.indexDegradedCapabilities.length > 0 || response.degradations.length > 0 ? <div className="ui-state ui-state--warning" role="status">
    <strong>Search 已降级，但没有隐藏能力差异</strong>
    {response.indexDegradedCapabilities.length > 0 ? <p>索引缺失能力：{response.indexDegradedCapabilities.join("、")}</p> : null}
    {response.degradations.map((item) => <p key={item.capability}>{item.capability}：{item.errorCode}{item.retryable ? "（可重试）" : ""}</p>)}
  </div> : null}
</Card>;

export const SearchPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const queryClient = useQueryClient();
  const [parameters, setParameters] = useSearchParams();
  const urlState = parseSearchUrlState(parameters, workspaceId);
  const canonicalSearch = writeSearchUrlState(urlState, workspaceId).toString();
  const [queryDraft, setQueryDraft] = useState(urlState.query);
  const [sourceVersionDraft, setSourceVersionDraft] = useState(urlState.sourceVersionId);
  const [formError, setFormError] = useState("");
  const [recoveryQueryPaused, setRecoveryQueryPaused] = useState(false);

  useEffect(() => {
    if (parameters.toString() !== canonicalSearch) {
      setParameters(new URLSearchParams(canonicalSearch), { replace: true });
    }
  }, [canonicalSearch, parameters, setParameters]);

  useEffect(() => setQueryDraft(urlState.query), [urlState.query]);
  useEffect(() => setSourceVersionDraft(urlState.sourceVersionId), [urlState.sourceVersionId]);
  useEffect(() => setRecoveryQueryPaused(false), [workspaceId]);

  const searchQuery = useQuery<SearchResponse>({
    queryKey: searchQueryKeys.page({
      workspaceId,
      query: urlState.query,
      mode: urlState.mode,
      sourceVersionId: urlState.sourceVersionId,
      cursor: urlState.cursor,
    }),
    queryFn: ({ signal }) => search({
      workspaceId,
      query: urlState.query,
      retrievalMode: urlState.mode,
      ...(urlState.sourceVersionId === "" ? {} : { filters: { sourceVersionIds: [urlState.sourceVersionId] } }),
      ...(urlState.cursor === "" ? {} : { cursor: urlState.cursor }),
      limit: 20,
    }, signal),
    enabled: workspaceId !== "" && urlState.query !== "" && !recoveryQueryPaused,
    retry: false,
    // Search pages are immutable snapshots for one request identity. Index events explicitly mark them stale,
    // while URL changes or SSE recovery own the next fetch. Keeping a completed recovery fetch fresh also avoids
    // an immediate duplicate request when the no-cursor Query observer mounts.
    staleTime: Infinity,
  });

  const recoverWorkspaceSearch = useCallback(async (recoveringWorkspaceId: string): Promise<void> => {
    if (recoveringWorkspaceId !== workspaceId || workspaceId === "") return;
    setRecoveryQueryPaused(true);
    const firstPageState = { ...urlState, cursor: "" };
    setParameters(writeSearchUrlState(firstPageState, workspaceId), { replace: true });
    if (urlState.query === "") {
      setRecoveryQueryPaused(false);
      return;
    }
    try {
      await queryClient.fetchQuery({
        queryKey: searchQueryKeys.page({
          workspaceId,
          query: urlState.query,
          mode: urlState.mode,
          sourceVersionId: urlState.sourceVersionId,
          cursor: "",
        }),
        queryFn: ({ signal }) => search({
          workspaceId,
          query: urlState.query,
          retrievalMode: urlState.mode,
          ...(urlState.sourceVersionId === "" ? {} : { filters: { sourceVersionIds: [urlState.sourceVersionId] } }),
          limit: 20,
        }, signal),
        staleTime: Infinity,
      });
      setRecoveryQueryPaused(false);
    } catch (error: unknown) {
      // Event Store retains its SSE cursor and owns the retry. Keeping the observer paused prevents an
      // untracked second request from racing after this callback rejects.
      throw error;
    }
  }, [queryClient, setParameters, urlState.mode, urlState.query, urlState.sourceVersionId, workspaceId]);
  useRegisterWorkspaceRecovery(recoverWorkspaceSearch);

  const updateUrlState = (updates: Partial<typeof urlState>): void => {
    setRecoveryQueryPaused(false);
    setParameters(writeSearchUrlState({ ...urlState, ...updates }, workspaceId));
  };

  const refetchSearch = (): void => {
    void searchQuery.refetch().then((result) => {
      if (!result.isError) setRecoveryQueryPaused(false);
    });
  };

  const submitSearch = (event: SyntheticEvent<HTMLFormElement, SubmitEvent>): void => {
    event.preventDefault();
    const query = normalizeSearchQuery(queryDraft);
    if (query === undefined) {
      setFormError("请输入 1 至 8192 UTF-8 bytes 的检索内容，且不要包含 NUL 字符。");
      return;
    }
    const sourceVersionId = sourceVersionDraft.trim() === ""
      ? ""
      : normalizeSourceVersionId(sourceVersionDraft);
    if (sourceVersionId === undefined) {
      setFormError("Source Version 必须为空或使用规范 UUID。");
      return;
    }
    setFormError("");
    const nextParameters = writeSearchUrlState({
      query,
      mode: urlState.mode,
      sourceVersionId,
      cursor: "",
    }, workspaceId);
    if (nextParameters.toString() === parameters.toString()) {
      refetchSearch();
      return;
    }
    setRecoveryQueryPaused(false);
    setParameters(nextParameters);
  };

  const restartFromFirstPage = (): void => updateUrlState({ cursor: "" });

  if (!workspaceId) {
    return <BusinessWorkspaceGate description="Search 必须绑定已连接 Workspace；页面不会发送无作用域检索或使用本地假结果。" />;
  }

  return <div className="page-stack">
    <div className="page-intro">
      <h1>检索</h1>
      <p>从当前索引查找可打开的证据。</p>
    </div>

    <Card>
      <form className="search-form" onSubmit={submitSearch}>
        <label className="search-form__query" htmlFor="search-query">
          检索内容
          <input
            id="search-query"
            type="search"
            value={queryDraft}
            onChange={(event) => setQueryDraft(event.currentTarget.value)}
            placeholder="例如：Approval 后 Relation 如何正式写入？"
            autoComplete="off"
          />
        </label>
        <label htmlFor="search-mode">
          检索模式
          <select id="search-mode" value={urlState.mode} onChange={(event) => {
            const mode = event.currentTarget.value;
            if (isSearchMode(mode)) updateUrlState({ mode, cursor: "" });
          }}>
            <option value="hybrid">Hybrid</option>
            <option value="keyword">Keyword</option>
            <option value="semantic">Semantic</option>
          </select>
        </label>
        <label htmlFor="search-source-version">
          Source Version（可选）
          <input
            id="search-source-version"
            value={sourceVersionDraft}
            onChange={(event) => setSourceVersionDraft(event.currentTarget.value)}
            placeholder="UUID"
            autoComplete="off"
          />
        </label>
        <Button type="submit" disabled={searchQuery.isFetching}>
          <SearchIcon size={16} />{searchQuery.isFetching ? "检索中…" : "检索"}
        </Button>
      </form>
      {formError !== "" ? <div className="ui-state ui-state--error" role="alert"><strong>Search 输入无效</strong><p>{formError}</p></div> : null}
      {urlState.sourceVersionId !== "" ? <p className="search-filter-note"><FileSearch size={15} />当前只检索 Source Version <code>{urlState.sourceVersionId}</code></p> : null}
    </Card>

    {urlState.query === "" ? <Card><EmptyState title="输入检索内容开始" description="URL 可恢复 query、mode 和 Source Version；游标只在同一 Workspace 与同一规范请求中继续使用。" /></Card> : null}

    {urlState.query !== "" && searchQuery.isPending ? <Card><p className="skeleton-line" aria-label="正在加载 Search Evidence" /></Card> : null}

    {searchQuery.isError ? <Card>
      <div className="ui-state ui-state--error" role="alert">
        <strong>{isCursorRecoveryError(searchQuery.error) ? "Search 结果窗口已失效" : "Search 请求失败"}</strong>
        <p>{searchQuery.error.message}{searchQuery.error instanceof SearchApiError ? `（${searchQuery.error.code}）` : ""}</p>
        <div className="document-actions">
          {isCursorRecoveryError(searchQuery.error) ? <Button variant="secondary" onClick={restartFromFirstPage}><RotateCcw size={15} />从第一页重新检索</Button> : null}
          {searchQuery.error instanceof SearchApiError && searchQuery.error.retryable ? <Button variant="secondary" onClick={refetchSearch}>重试</Button> : null}
        </div>
      </div>
    </Card> : null}

    {searchQuery.data ? <>
      <SearchResultSummary response={searchQuery.data} fetching={searchQuery.isFetching} />
      {searchQuery.data.items.length === 0 ? <Card><EmptyState title="没有命中 Evidence" description="服务端完成了检索但当前条件没有结果；这不是依赖故障或假成功。" /></Card> : <div className="search-results" aria-label="Search Evidence 列表">
        {searchQuery.data.items.map((item) => <SearchEvidenceCard key={item.chunkId} item={item} workspaceId={searchQuery.data.workspaceId} />)}
      </div>}
      <div className="pagination-row">
        {urlState.cursor !== "" ? <Button variant="ghost" onClick={restartFromFirstPage}>返回首屏</Button> : <span />}
        {searchQuery.data.nextCursor ? <Button variant="secondary" onClick={() => updateUrlState({ cursor: searchQuery.data.nextCursor ?? "" })}>下一页</Button> : null}
      </div>
    </> : null}
  </div>;
};
