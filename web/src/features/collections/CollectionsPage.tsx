import { ArrowRight, Check, Columns3, Filter, HeartPulse, LayoutGrid, List, RefreshCw, Save, Search, Table2 } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type ReactElement, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";

import { collectionEnumValueRegistry, collectionFieldOperators, collectionGroupFieldRegistry, collectionQueryFieldRegistry, collectionSortableFieldRegistry, collectionViewColumnRegistry, CollectionApiError, type Collection, type CollectionClause, type CollectionDefinitionInput, type CollectionItem, type CollectionOperator, type CollectionQuery, type CollectionViewConfig, type CollectionViewType } from "../../api/collections";
import { useActiveWorkspaceId } from "../../app/active-workspace";
import { createSemanticLinkIdempotencyKey, useStartSemanticLinkScan } from "../graph/semantic-link-queries";
import { useStartHealthScan } from "../health/queries";
import { Badge, Button, Card, CardHeader, EmptyState, ErrorState, UnavailableState } from "../../shared/ui";
import { CollectionExportPanel } from "./CollectionExportPanel";
import { useCollection, useCollectionPreview, useCollectionResults, useCollections, useCreateCollection } from "./queries";
import { defaultCollectionUrlState, parseCollectionUrlState, writeCollectionUrlState, type CollectionPredicateDraft, type CollectionUrlState, type CollectionUrlStateDefaults } from "./url-state";

const defaultViewConfig: CollectionViewConfig = { columns: ["object_type", "title", "summary", "status", "updated_at"], fixedColumns: ["title"], sort: [{ field: "updated_at", direction: "DESC" }], groupBy: null, density: "COMFORTABLE" };
const fieldMetadata: Record<(typeof collectionQueryFieldRegistry)[number], { label: string; operators: CollectionOperator[]; kind: "text" | "number" | "date" | "enum"; options?: readonly string[] }> = {
  object_type: { label: "对象类型", operators: [...collectionFieldOperators.object_type], kind: "enum", options: collectionEnumValueRegistry.object_type }, topic_id: { label: "Topic ID", operators: [...collectionFieldOperators.topic_id], kind: "text" }, status: { label: "状态", operators: [...collectionFieldOperators.status], kind: "enum", options: collectionEnumValueRegistry.status },
  created_at: { label: "创建时间", operators: [...collectionFieldOperators.created_at], kind: "date" }, updated_at: { label: "更新时间", operators: [...collectionFieldOperators.updated_at], kind: "date" }, confidence: { label: "置信度", operators: [...collectionFieldOperators.confidence], kind: "number" },
  relation_type: { label: "关系类型", operators: [...collectionFieldOperators.relation_type], kind: "enum", options: collectionEnumValueRegistry.relation_type }, health_issue_type: { label: "Health 类型", operators: [...collectionFieldOperators.health_issue_type], kind: "enum", options: collectionEnumValueRegistry.health_issue_type }, source_type: { label: "来源类型", operators: [...collectionFieldOperators.source_type], kind: "enum", options: collectionEnumValueRegistry.source_type },
  file_path: { label: "文件路径", operators: [...collectionFieldOperators.file_path], kind: "text" }, text: { label: "文本", operators: [...collectionFieldOperators.text], kind: "text" },
};
const columnOptions = collectionViewColumnRegistry;
const newKey = (prefix: string): string => `${prefix}-${crypto.randomUUID()}`;
interface IdempotentAttempt { signature: string; key: string }
const attemptKey = (attempt: { current: IdempotentAttempt | null }, prefix: string, signature: string): string => {
  if (attempt.current?.signature !== signature) attempt.current = { signature, key: newKey(prefix) };
  return attempt.current.key;
};
const formatDate = (value: string): string => new Date(value).toLocaleString("zh-CN", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
const textOrDash = (value: string): string => value === "" ? "—" : value;
const StateMessage = ({ title, description, action }: { title: string; description: string; action?: ReactNode }) => <div className="ui-state" role="status"><strong>{title}</strong><p>{description}</p>{action}</div>;
const groupFields = collectionGroupFieldRegistry;
const viewDefaultsFromCollection = (collection: Collection): CollectionUrlStateDefaults => {
  const group = collection.viewConfig.groupBy?.field;
  return {
    ...defaultCollectionUrlState,
    view: collection.viewType,
    columns: collection.viewConfig.columns.length === 0 ? defaultCollectionUrlState.columns : collection.viewConfig.columns,
    fixedColumns: collection.viewConfig.fixedColumns,
    group: groupFields.includes(group as (typeof groupFields)[number]) ? group as CollectionUrlStateDefaults["group"] : "",
    density: collection.viewConfig.density,
  };
};

const predicateValue = (clause: CollectionPredicateDraft): CollectionClause => {
  const base = { kind: "predicate" as const, field: clause.field, operator: clause.operator };
  if (clause.operator === "IS_NULL") return base;
  const parse = (value: string): string | number => {
    const trimmed = value.trim();
    if (clause.field === "confidence") return trimmed === "" ? Number.NaN : Number(trimmed);
    if (clause.field !== "created_at" && clause.field !== "updated_at") return trimmed;
    const parsed = new Date(trimmed);
    return Number.isNaN(parsed.getTime()) ? trimmed : parsed.toISOString();
  };
  if (clause.operator === "IN") return { ...base, values: clause.value.split(",").map(parse).filter((value) => value !== "") };
  if (clause.operator === "BETWEEN") {
    const [lower = "", upper = ""] = clause.value.split("..");
    return { ...base, lower: parse(lower), upper: parse(upper) };
  }
  return { ...base, value: parse(clause.value) };
};

export const buildCollectionQuery = (state: CollectionUrlState): CollectionQuery => ({
  schema_version: "collection-query/v1",
  root: { kind: "group", operator: state.logic, clauses: state.clauses.slice(0, 63).map(predicateValue) },
  sort: [{ field: state.sortField, direction: state.sortDirection }],
});
export const collectionItemRefs = (items: CollectionItem[]): string[] => items.map((item) => `${item.objectType}:${item.id}`);

const itemTone = (item: CollectionItem) => item.healthSummary?.count ? "danger" as const : item.status === "ACTIVE" || item.status === "CONFIRMED" ? "success" as const : "warning" as const;
const ItemMeta = ({ item }: { item: CollectionItem }) => <div className="collection-item__meta"><Badge tone={item.objectType === "TOPIC" ? "info" : "neutral"}>{item.objectType}</Badge><Badge tone={itemTone(item)}>{item.status}</Badge>{item.healthSummary ? <Badge tone="danger">{item.healthSummary.maxSeverity} · {item.healthSummary.count}</Badge> : null}<time>{formatDate(item.updatedAt)}</time></div>;
const SelectItem = ({ selected, onSelect }: { selected: boolean; onSelect: () => void }) => <Button size="sm" variant={selected ? "primary" : "ghost"} aria-pressed={selected} onClick={onSelect}>{selected ? <Check size={14} /> : null}{selected ? "已选择" : "查看"}</Button>;

const ListView = ({ items, selected, onSelect }: { items: CollectionItem[]; selected: string; onSelect: (ref: string) => void }) => <div className="collection-items" aria-label="Collection 列表视图">{items.map((item) => { const ref = `${item.objectType}:${item.id}`; return <article className="collection-item" key={ref}><div><h3>{textOrDash(item.title)}</h3><p>{textOrDash(item.summary)}</p><ItemMeta item={item} /></div><SelectItem selected={selected === ref} onSelect={() => onSelect(selected === ref ? "" : ref)} /></article>; })}</div>;
const columnValue = (item: CollectionItem, column: string): ReactNode => ({ object_type: item.objectType, title: textOrDash(item.title), summary: textOrDash(item.summary), status: item.status, topic: item.topicId?.slice(0, 8) ?? "—", source: textOrDash(item.sourceSummaries.map((source) => `${source.sourceType}:${source.filePath}`).join("；")), relation_summary: textOrDash(item.relationTypes.join("、")), health_summary: item.healthSummary === null ? "—" : `${item.healthSummary.maxSeverity} · ${String(item.healthSummary.count)} · ${textOrDash(item.healthSummary.summary)}`, confidence: item.confidence?.toFixed(2) ?? "—", created_at: formatDate(item.createdAt), updated_at: formatDate(item.updatedAt) } as Record<string, ReactNode>)[column] ?? "—";
const groupValue = (item: CollectionItem, group: CollectionUrlState["group"]): string => group === "" ? "" : textOrDash(({ object_type: item.objectType, status: item.status, topic_id: item.topicId?.slice(0, 8) ?? "", relation_type: item.relationType ?? item.relationTypes.join(", "), health_issue_type: item.healthIssueType ?? item.healthSummary?.issueTypes.join(", ") ?? "", source_type: item.sourceType ?? item.sourceSummaries[0]?.sourceType ?? "" } as Record<Exclude<CollectionUrlState["group"], "">, string>)[group]);
const TableView = ({ items, columns, group, selected, onSelect }: { items: CollectionItem[]; columns: string[]; group: CollectionUrlState["group"]; selected: string; onSelect: (ref: string) => void }) => {
  let previousGroup = "";
  return <div className="table-scroll"><table className="data-table collection-table" aria-label="Collection 表格视图"><thead><tr>{columns.map((column) => <th key={column}>{column}</th>)}<th>操作</th></tr></thead><tbody>{items.flatMap((item) => { const ref = `${item.objectType}:${item.id}`; const currentGroup = groupValue(item, group); const groupRow = group !== "" && currentGroup !== previousGroup ? <tr className="collection-group-row" key={`group:${ref}`}><td colSpan={columns.length + 1}>分组：{currentGroup}</td></tr> : null; previousGroup = currentGroup; return [groupRow, <tr key={ref}>{columns.map((column) => <td key={column}>{columnValue(item, column)}</td>)}<td><SelectItem selected={selected === ref} onSelect={() => onSelect(selected === ref ? "" : ref)} /></td></tr>].filter((row): row is ReactElement => row !== null); })}</tbody></table></div>;
};
const CardView = ({ items, selected, onSelect }: { items: CollectionItem[]; selected: string; onSelect: (ref: string) => void }) => <div className="collection-card-grid" aria-label="Collection 卡片视图">{items.map((item) => { const ref = `${item.objectType}:${item.id}`; return <article className="collection-card" key={ref}><div className="collection-card__top"><Badge tone="info">{item.objectType}</Badge><span className="mono">{item.id.slice(0, 8)}…</span></div><h3>{textOrDash(item.title)}</h3><p>{textOrDash(item.summary)}</p><ItemMeta item={item} /><SelectItem selected={selected === ref} onSelect={() => onSelect(selected === ref ? "" : ref)} /></article>; })}</div>;

const CollectionViews = ({ items, state, onStateChange }: { items: CollectionItem[]; state: CollectionUrlState; onStateChange: (state: CollectionUrlState) => void }) => <div className={`collection-view-density collection-view-density--${state.density.toLowerCase()}`}>{state.view === "TABLE" ? <TableView items={items} columns={state.columns} group={state.group} selected={state.selected} onSelect={(selected) => onStateChange({ ...state, selected })} /> : state.view === "COMPACT_CARD" ? <CardView items={items} selected={state.selected} onSelect={(selected) => onStateChange({ ...state, selected })} /> : <ListView items={items} selected={state.selected} onSelect={(selected) => onStateChange({ ...state, selected })} />}</div>;

const QueryBuilder = ({ state, onChange, onRefresh, refreshing }: { state: CollectionUrlState; onChange: (state: CollectionUrlState) => void; onRefresh: () => void; refreshing: boolean }) => {
  const updateClause = (index: number, patch: Partial<CollectionPredicateDraft>) => {
    const clauses = state.clauses.map((clause, clauseIndex) => {
      if (clauseIndex !== index) return clause;
      const next = { ...clause, ...patch };
      const metadata = fieldMetadata[next.field];
      if (patch.field !== undefined && !metadata.operators.includes(next.operator)) {
        next.operator = metadata.operators[0] ?? "EQ";
        next.value = next.field === "object_type" ? "TOPIC" : "";
      }
      return next;
    });
    onChange({ ...state, clauses, field: clauses[0]?.field ?? state.field, operator: clauses[0]?.operator ?? state.operator, value: clauses[0]?.value ?? state.value });
  };
  const addClause = () => {
    if (state.clauses.length >= 63) return;
    onChange({ ...state, clauses: [...state.clauses, { field: "object_type", operator: "EQ", value: "TOPIC" }] });
  };
  const removeClause = (index: number) => {
    const clauses = state.clauses.filter((_, clauseIndex) => clauseIndex !== index);
    if (clauses.length === 0) return;
    const first = clauses[0];
    if (first === undefined) return;
    onChange({ ...state, clauses, field: first.field, operator: first.operator, value: first.value });
  };
  const valueControl = (clause: CollectionPredicateDraft, index: number, metadata: (typeof fieldMetadata)[(typeof collectionQueryFieldRegistry)[number]]) => {
    if (clause.operator === "IS_NULL") return null;
    const updateValue = (value: string) => updateClause(index, { value });
    if (clause.operator === "BETWEEN") {
      const [lower = "", upper = ""] = clause.value.split("..", 2);
      const inputType = metadata.kind === "number" ? "number" : metadata.kind === "date" ? "datetime-local" : "text";
      return <div className="collection-between-values"><label>下界<input aria-label={`条件 ${String(index + 1)} 下界`} type={inputType} value={lower} onChange={(event) => updateValue(`${event.target.value}..${upper}`)} /></label><label>上界<input aria-label={`条件 ${String(index + 1)} 上界`} type={inputType} value={upper} onChange={(event) => updateValue(`${lower}..${event.target.value}`)} /></label></div>;
    }
    if (metadata.kind === "enum" && metadata.options !== undefined) {
      if (clause.operator === "IN") return <select aria-label={`条件 ${String(index + 1)} 值`} multiple value={clause.value.split(",").filter(Boolean)} onChange={(event) => updateValue(Array.from(event.currentTarget.selectedOptions, (option) => option.value).join(","))}>{metadata.options.map((option) => <option key={option} value={option}>{option}</option>)}</select>;
      return <select aria-label={`条件 ${String(index + 1)} 值`} value={clause.value} onChange={(event) => updateValue(event.target.value)}>{metadata.options.map((option) => <option key={option} value={option}>{option}</option>)}</select>;
    }
    return <input aria-label={`条件 ${String(index + 1)} 值`} type={metadata.kind === "number" ? "number" : metadata.kind === "date" ? "datetime-local" : "text"} value={clause.value} onChange={(event) => updateValue(event.target.value)} placeholder={clause.operator === "IN" ? "逗号分隔，最多 100 项" : "受控查询值"} />;
  };
  return <Card className="collection-builder"><CardHeader eyebrow="查询" title="条件构建器" description="选择字段、条件和值，实时查看匹配数量。" action={<Filter size={18} />} />
    <div className="filter-bar" aria-label="Collection 查询条件"><label>组合<select value={state.logic} onChange={(event) => onChange({ ...state, logic: event.target.value === "OR" ? "OR" : "AND" })}><option>AND</option><option>OR</option></select></label><label>排序字段<select value={state.sortField} onChange={(event) => onChange({ ...state, sortField: event.target.value as CollectionUrlState["sortField"] })}>{collectionSortableFieldRegistry.map((field) => <option key={field}>{field}</option>)}</select></label><label>方向<select value={state.sortDirection} onChange={(event) => onChange({ ...state, sortDirection: event.target.value === "ASC" ? "ASC" : "DESC" })}><option>DESC</option><option>ASC</option></select></label></div>
    <div className="collection-clause-list">{state.clauses.map((clause, index) => {
      const metadata = fieldMetadata[clause.field];
      return <div className="filter-bar collection-clause-row" key={`${String(index)}:${clause.field}:${clause.operator}`} aria-label={`Collection 条件 ${String(index + 1)}`}><label>字段<select value={clause.field} onChange={(event) => updateClause(index, { field: event.target.value as CollectionPredicateDraft["field"] })}>{collectionQueryFieldRegistry.map((field) => <option key={field} value={field}>{fieldMetadata[field].label}</option>)}</select></label><label>Operator<select value={clause.operator} onChange={(event) => updateClause(index, { operator: event.target.value as CollectionOperator })}>{metadata.operators.map((operator) => <option key={operator}>{operator}</option>)}</select></label>{clause.operator === "IS_NULL" ? null : <label className="filter-field--wide">值{valueControl(clause, index, metadata)}</label>}<Button variant="ghost" disabled={state.clauses.length <= 1} onClick={() => removeClause(index)}>删除</Button></div>;
    })}</div>
    <div className="button-row"><Button variant="ghost" onClick={addClause} disabled={state.clauses.length >= 63}>新增条件</Button><span className="sidebar-note">{String(state.clauses.length)} / 63 条件（连同根组共 64 节点），最终仍由服务端验证。</span></div>
    <div className="button-row"><Button variant="secondary" onClick={onRefresh} disabled={refreshing}><Search size={15} />{refreshing ? "正在计算…" : "重新计算"}</Button><span className="sidebar-note">字段不可用、超时和依赖不可用会分别显示。</span></div>
  </Card>;
};

const ViewControls = ({ state, fixedColumns, onChange }: { state: CollectionUrlState; fixedColumns: string[]; onChange: (state: CollectionUrlState) => void }) => <Card><CardHeader eyebrow="视图" title="视图、列与分组" description="列表、表格和卡片保持相同对象顺序。" /><div className="collection-view-controls"><div className="view-switcher" aria-label="结果视图">{(["LIST", "TABLE", "COMPACT_CARD"] as CollectionViewType[]).map((view) => <Button key={view} size="sm" variant={state.view === view ? "primary" : "ghost"} onClick={() => onChange({ ...state, view })}>{view === "LIST" ? <List size={15} /> : view === "TABLE" ? <Table2 size={15} /> : <LayoutGrid size={15} />}{view}</Button>)}</div><label>分组<select value={state.group} onChange={(event) => onChange({ ...state, group: event.target.value as CollectionUrlState["group"] })}><option value="">不分组</option>{collectionGroupFieldRegistry.map((field) => <option key={field} value={field}>{field}</option>)}</select></label><label>密度<select value={state.density} onChange={(event) => onChange({ ...state, density: event.target.value === "COMPACT" ? "COMPACT" : "COMFORTABLE" })}><option>COMFORTABLE</option><option>COMPACT</option></select></label><fieldset><legend>表格列</legend>{columnOptions.map((column) => { const fixed = fixedColumns.includes(column); return <label key={column} title={fixed ? "固定列不可取消" : undefined}><input type="checkbox" checked={state.columns.includes(column)} disabled={fixed} onChange={() => { if (fixed) return; const columns = state.columns.includes(column) ? state.columns.filter((item) => item !== column) : [...state.columns, column]; if (columns.length > 0) onChange({ ...state, columns }); }} />{column}</label>; })}</fieldset></div></Card>;

const SelectedProjection = ({ item }: { item: CollectionItem | undefined }) => {
  if (item === undefined) return null;
  const healthText = item.healthSummary === null ? "无活动 Issue 摘要" : textOrDash(item.healthSummary.summary) === "—" ? textOrDash(item.healthSummary.issueTypes.join("、")) : item.healthSummary.summary;
  return <Card className="collection-selected"><CardHeader eyebrow="当前页对象" title={textOrDash(item.title) === "—" ? `${item.objectType} ${item.id.slice(0, 8)}` : item.title} description="展开对象摘要、来源、关系与健康状态。" /><dl className="stacked-meta"><div><dt>Aliases</dt><dd>{textOrDash(item.aliases.join("、"))}</dd></div><div><dt>Applicability</dt><dd>{item.applicabilitySchemaVersion ?? "—"} {item.applicabilityHash?.slice(0, 12) ?? ""}</dd></div><div><dt>Sources</dt><dd>{textOrDash(item.sourceSummaries.map((source) => `${source.sourceType}:${textOrDash(source.filePath)}`).join("；"))}</dd></div><div><dt>Relations</dt><dd>{textOrDash(item.relationTypes.join("、"))}</dd></div><div><dt>Health</dt><dd>{healthText}</dd></div></dl></Card>;
};

export const CollectionsPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const [params, setParams] = useSearchParams();
  const state = parseCollectionUrlState(params);
  const listScope = workspaceId;
  const [listPage, setListPage] = useState<{ scope: string; cursor?: string }>({ scope: listScope });
  const listCursor = listPage.scope === listScope ? listPage.cursor : undefined;
  useEffect(() => setListPage({ scope: listScope }), [listScope]);
  const collections = useCollections({ ...(listCursor === undefined ? {} : { cursor: listCursor }), limit: 50 });
  const create = useCreateCollection();
  const [name, setName] = useState("");
  const createAttempt = useRef<IdempotentAttempt | null>(null);
  const query = useMemo(() => buildCollectionQuery(state), [state]);
  const previewInput = useMemo<CollectionDefinitionInput | null>(() => workspaceId === "" ? null : ({ workspaceId, name: "Preview", description: "", query, viewType: state.view, viewConfig: { ...defaultViewConfig, columns: state.columns, sort: query.sort ?? [], groupBy: state.group ? { field: state.group } : null, density: state.density } }), [query, state.columns, state.density, state.group, state.view, workspaceId]);
  const preview = useCollectionPreview(previewInput);
  const updateState = (next: CollectionUrlState) => setParams(writeCollectionUrlState(next, params), { replace: true });
  if (!workspaceId) return <div className="page-stack"><UnavailableState title="先连接 Workspace" description="Collection 查询必须绑定真实 Workspace；这里不会用本地样例填充。" /><Link className="ui-button ui-button--primary" to="/settings">前往 Settings</Link></div>;
  const save = () => { if (previewInput === null || name.trim() === "") return; const input = { ...previewInput, name: name.trim() }; const signature = JSON.stringify(input); create.mutate({ input, idempotencyKey: attemptKey(createAttempt, "collection-create", signature) }, { onSuccess: () => { setName(""); createAttempt.current = null; } }); };
  return <div className="page-stack"><div className="page-intro page-intro--split"><div><p className="eyebrow">Learning / Smart Collections</p><h2>把知识组织成可复用的查询。</h2><p>保存筛选条件与展示方式，不复制 Topic/Claim；每次执行都读取当前知识事实。</p></div><div className="folio-mark"><Columns3 size={20} /><strong>{collections.data?.items.length ?? "—"}</strong><span>个集合</span></div></div><QueryBuilder state={state} onChange={updateState} onRefresh={() => void preview.refetch()} refreshing={preview.isFetching} />
    <Card><CardHeader eyebrow="即时预览" title={preview.isPending ? "正在计算精确数量" : `${String(preview.data?.exactCount ?? 0)} 个匹配对象`} description="实时数量和结果页会按同一套筛选规则计算。" />{preview.isError ? <ErrorState title={preview.error instanceof CollectionApiError && preview.error.errorCode === "COLLECTION_QUERY_TIMEOUT" ? "查询超时" : "预览不可用"} description={preview.error instanceof Error ? preview.error.message : "查询预览失败。"} onRetry={() => void preview.refetch()} /> : preview.isPending ? <StateMessage title="正在验证条件" description="字段、条件、层级和能力都会严格校验。" /> : preview.data.items.length === 0 ? <EmptyState title="预览为空" description="当前条件没有匹配对象；这不是依赖故障。" /> : <CollectionViews items={preview.data.items} state={state} onStateChange={updateState} />}<div className="collection-save"><label>保存为<input value={name} maxLength={256} onChange={(event) => setName(event.target.value)} placeholder="集合名称" /></label><Button onClick={save} disabled={name.trim() === "" || create.isPending}><Save size={15} />{create.isPending ? "正在保存…" : "保存 Collection"}</Button>{create.isError ? <span role="alert">{create.error.message}</span> : null}</div></Card>
    <Card><CardHeader eyebrow="Saved definitions" title="已保存集合" action={<Button variant="ghost" onClick={() => void collections.refetch()} aria-label="刷新集合列表"><RefreshCw size={16} /></Button>} />{collections.isError ? <ErrorState title="集合列表不可用" description={collections.error instanceof Error ? collections.error.message : "Collection 能力暂不可用。"} onRetry={() => void collections.refetch()} /> : collections.isPending ? <StateMessage title="正在读取集合" description="读取 Workspace 内保存的查询定义。" /> : collections.data.items.length === 0 ? <EmptyState title="还没有保存集合" description="使用上方条件构建器验证并保存第一个集合。" /> : <div className="collection-definition-list">{collections.data.items.map((collection) => <Link className="collection-definition-row" to={`/collections/${collection.id}`} key={collection.id}><div><strong>{collection.name}</strong><small>{collection.description || "没有描述"} · 查询 v{collection.queryVersion} · 定义 v{collection.version}</small></div><div><Badge tone={collection.status === "ACTIVE" ? "success" : "neutral"}>{collection.status}</Badge><ArrowRight size={15} /></div></Link>)}</div>}<div className="pagination-row"><span className="sidebar-note">当前页 {String(collections.data?.items.length ?? 0)} 个集合</span><div className="button-row">{listCursor !== undefined ? <Button variant="ghost" onClick={() => setListPage({ scope: listScope })}>回到首屏</Button> : null}{collections.data?.nextCursor ? <Button variant="secondary" onClick={() => { const nextCursor = collections.data.nextCursor; if (nextCursor) setListPage({ scope: listScope, cursor: nextCursor }); }}>下一页<ArrowRight size={15} /></Button> : null}</div></div></Card></div>;
};

const CollectionResults = ({ result, cursor, setCursor, state, fixedColumns, onStateChange }: { result: ReturnType<typeof useCollectionResults>; cursor: string | undefined; setCursor: (cursor: string | undefined) => void; state: CollectionUrlState; fixedColumns: string[]; onStateChange: (state: CollectionUrlState) => void }) => {
  const items = result.data?.items ?? [];
  const selected = items.find((item) => `${item.objectType}:${item.id}` === state.selected);
  const stale = result.error instanceof CollectionApiError && (result.error.errorCode === "COLLECTION_CURSOR_STALE" || result.error.status === 409);
  return <><ViewControls state={state} fixedColumns={fixedColumns} onChange={onStateChange} /><Card className="collection-results"><CardHeader eyebrow="结果" title={result.isPending ? "读取结果…" : `${String(result.data?.exactCount ?? 0)} 个结果`} description="列表、表格和卡片保持相同对象顺序。" />{result.isError ? <ErrorState title={stale ? "结果分页已过期" : "结果读取失败"} description={stale ? "集合或事实已变化。刷新第一页可读取新结果。" : result.error.message} onRetry={() => { setCursor(undefined); void result.refetch(); }} /> : result.isPending ? <StateMessage title="正在读取结果" description="读取当前集合中的 Topic 和 Claim。" /> : items.length === 0 ? <EmptyState title="没有匹配对象" description="当前查询没有返回结果。" /> : <CollectionViews items={items} state={state} onStateChange={onStateChange} />}<div className="pagination-row"><span className="sidebar-note">事实版本 {result.data?.revisionHash.slice(0, 12) ?? "—"}</span><div className="button-row">{cursor !== undefined ? <Button variant="ghost" onClick={() => setCursor(undefined)}>回到首屏</Button> : null}{result.data?.nextCursor ? <Button variant="secondary" onClick={() => setCursor(result.data.nextCursor ?? undefined)}>下一页<ArrowRight size={15} /></Button> : null}</div></div></Card><SelectedProjection item={selected} /></>;
};

export const CollectionDetailPage = () => {
  const workspaceId = useActiveWorkspaceId();
  const { collectionId = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const collection = useCollection(collectionId);
  const binding = collection.data === undefined ? undefined : { version: collection.data.version, queryHash: collection.data.queryHash };
  const resultScope = `${workspaceId}:${collectionId}:${String(binding?.version ?? 0)}:${binding?.queryHash ?? ""}`;
  const [resultPage, setResultPage] = useState<{ scope: string; cursor?: string }>({ scope: resultScope });
  const cursor = resultPage.scope === resultScope ? resultPage.cursor : undefined;
  useEffect(() => setResultPage({ scope: resultScope }), [resultScope]);
  const setCursor = (nextCursor: string | undefined) => setResultPage({ scope: resultScope, ...(nextCursor === undefined ? {} : { cursor: nextCursor }) });
  const request = cursor === undefined ? { limit: 25 } : { cursor, limit: 25 };
  const result = useCollectionResults(collectionId, binding, request);
  const startScan = useStartHealthScan();
  const startSemanticScan = useStartSemanticLinkScan();
  const healthScanAttempt = useRef<IdempotentAttempt | null>(null);
  if (!workspaceId) return <div className="page-stack"><UnavailableState title="未连接 Workspace" description="请先在 Settings 选择 Workspace，再打开 Collection。" /></div>;
  if (collection.isPending) return <div className="page-stack"><StateMessage title="正在读取 Collection" description="读取保存的查询条件与视图配置。" /></div>;
  if (collection.isError) return <div className="page-stack"><ErrorState title="Collection 不可用" description={collection.error instanceof Error ? collection.error.message : "找不到该 Collection。"} onRetry={() => void collection.refetch()} /></div>;
  const item = collection.data;
  const defaults = viewDefaultsFromCollection(item);
  const state = parseCollectionUrlState(params, defaults);
  const updateState = (next: CollectionUrlState) => setParams(writeCollectionUrlState(next, params, defaults), { replace: true });
  const resultBinding = result.data;
  const resultMatchesCollection = resultBinding?.collectionId === item.id && resultBinding.queryHash === item.queryHash;
  const exceedsHealthScanCapacity = resultBinding !== undefined && resultBinding.exactCount > 5000;
  const scanDisabled = item.status !== "ACTIVE" || startScan.isPending || result.isPending || result.isFetching || result.isError || resultBinding === undefined || !resultMatchesCollection || exceedsHealthScanCapacity;
  const scanStatus = result.isError ? "结果读取失败，修复后才能启动 Health Scan。" : result.isPending || result.isFetching ? "结果加载完成后才能启动 Health Scan。" : !resultMatchesCollection ? "结果与当前集合版本不一致，刷新后再扫描。" : exceedsHealthScanCapacity ? `当前集合有 ${String(resultBinding.exactCount)} 个对象，单次 Health Scan 最多处理 5000 个；请收窄 Collection 条件后重试。` : `将扫描 ${String(resultBinding.exactCount)} 个对象。`;
  const scan = () => {
    if (resultBinding === undefined || !resultMatchesCollection || exceedsHealthScanCapacity) return;
    const scope = { type: "SMART_COLLECTION" as const, ref: item.id, version: item.version, schemaVersion: "health-scope/smart-collection/v1", hash: item.queryHash, readModelRevision: resultBinding.scanRevisionHash, exactCount: resultBinding.exactCount };
    const signature = JSON.stringify({ scope, maxItems: 5000, preventScopeConcurrency: true });
    startScan.mutate({ scope, maxItems: 5000, preventScopeConcurrency: true, idempotencyKey: attemptKey(healthScanAttempt, "health-scan", signature) }, { onSuccess: (accepted) => { healthScanAttempt.current = null; void navigate(`/health?scan=${accepted.healthScanId}`); } });
  };
  const startRelationScan = () => startSemanticScan.mutate({ workspaceId, scope: { kind: "SMART_COLLECTION", collectionId: item.id }, idempotencyKey: createSemanticLinkIdempotencyKey("candidate-scan") }, { onSuccess: (accepted) => void navigate(`/graph?candidate_scan_id=${accepted.scanId}`) });
  return <div className="page-stack"><div className="page-intro page-intro--split"><div><Link className="back-link" to="/collections">← 返回集合</Link><p className="eyebrow">Saved Query / v{item.queryVersion}</p><h2>{item.name}</h2><p>{item.description || "没有描述"}</p></div><div className="hash-card"><span>范围指纹</span><code>{item.queryHash.slice(0, 16)}…</code><Badge tone={item.status === "ACTIVE" ? "success" : "neutral"}>{item.status}</Badge></div></div><Card><CardHeader eyebrow="Bounded actions" title="下游入口" description="健康扫描绑定当前集合版本与结果事实；关系分析由服务端冻结 Collection 范围。" /><div className="button-row"><Button onClick={scan} disabled={scanDisabled}><HeartPulse size={15} />{startScan.isPending ? "正在接受扫描…" : "启动 Health Scan"}</Button><Button variant="secondary" onClick={startRelationScan} disabled={item.status !== "ACTIVE" || startSemanticScan.isPending}>{startSemanticScan.isPending ? "正在启动…" : "启动关系分析"}</Button><Button variant="ghost" disabled title="Review owner 尚未落地">创建 Review Deck（不可用）</Button></div><p className="sidebar-note">{scanStatus}</p>{startScan.isError ? <p className="form-error" role="alert">{startScan.error.message}</p> : null}{startSemanticScan.isError ? <p className="form-error" role="alert">{startSemanticScan.error.message}</p> : null}</Card><CollectionExportPanel collection={item} /><CollectionResults result={result} cursor={cursor} setCursor={setCursor} state={state} fixedColumns={item.viewConfig.fixedColumns} onStateChange={updateState} /><Card><CardHeader eyebrow="查询定义" title="保存的条件" /><pre className="json-preview">{JSON.stringify(item.query, null, 2)}</pre><p className="sidebar-note">归档不会删除 Knowledge；事实变化会让旧分页明确失效。</p></Card></div>;
};
