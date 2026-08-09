import { type SyntheticEvent, useEffect, useState } from "react";

import type { RelationType } from "../../api/graph";
import {
  graphFilterClaimStatuses,
  graphNodeTypes,
  graphRelationStatuses,
  graphRelationTypes,
} from "./options";
import type { GraphUrlFilter } from "./url-state";
import { graphClaimStatusLabel, nodeTypeLabel, relationStatusLabel, relationTypeLabel } from "./view-model";

const toggle = <T extends string>(values: T[], value: T): T[] => {
  const next = values.includes(value) ? values.filter((item) => item !== value) : [...values, value];
  return next.length === 0 ? values : next.sort();
};

const parseTopicIds = (value: string): string[] => [...new Set(value.split(/[\s,]+/).map((item) => item.trim()).filter((item) => item !== ""))].sort();

const withoutOptionalFilter = (
  filter: GraphUrlFilter,
  key: "claimMinConfidence" | "relationMinConfidence" | "updatedAfter",
): GraphUrlFilter => {
  const { claimMinConfidence, relationMinConfidence, updatedAfter, ...required } = filter;
  return {
    ...required,
    ...(key === "claimMinConfidence" || claimMinConfidence === undefined ? {} : { claimMinConfidence }),
    ...(key === "relationMinConfidence" || relationMinConfidence === undefined ? {} : { relationMinConfidence }),
    ...(key === "updatedAfter" || updatedAfter === undefined ? {} : { updatedAfter }),
  };
};

const toLocalInput = (value: string | undefined): string => {
  if (value === undefined) return "";
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return "";
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
};

const RelationTypeOptions = ({ filter, onChange }: {
  filter: GraphUrlFilter;
  onChange: (filter: GraphUrlFilter) => void;
}) => (
  <div className="graph-relation-filter__grid">
    {graphRelationTypes.map((type) => <label key={type}><input type="checkbox" checked={filter.relationTypes.includes(type)} onChange={() => onChange({ ...filter, relationTypes: toggle<RelationType>(filter.relationTypes, type) })} /><span>{relationTypeLabel(type)}</span></label>)}
  </div>
);

export const GraphFilterPanel = ({ filter, onChange, pathOnly = false }: {
  filter: GraphUrlFilter;
  onChange: (filter: GraphUrlFilter) => void;
  pathOnly?: boolean;
}) => {
  const [topicDraft, setTopicDraft] = useState(filter.topicIds.join(", "));
  useEffect(() => setTopicDraft(filter.topicIds.join(", ")), [filter.topicIds]);
  const applyTopicScope = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    onChange({ ...filter, topicIds: parseTopicIds(topicDraft) });
  };

  if (pathOnly) {
    return (
      <details className="graph-filters">
        <summary>路径关系过滤 · {filter.relationTypes.length}</summary>
        <div className="graph-filters__body">
          <RelationTypeOptions filter={filter} onChange={onChange} />
        </div>
      </details>
    );
  }

  return (
    <details className="graph-filters">
      <summary>过滤器</summary>
      <div className="graph-filters__body">
        <fieldset>
          <legend>节点类型</legend>
          <div className="graph-toggle-grid">
            {graphNodeTypes.map((type) => <label key={type}><input type="checkbox" checked={filter.nodeTypes.includes(type)} onChange={() => onChange({ ...filter, nodeTypes: toggle(filter.nodeTypes, type) })} /><span>{nodeTypeLabel(type)}</span></label>)}
          </div>
        </fieldset>
        <fieldset>
          <legend>关系状态</legend>
          <div className="graph-toggle-grid">
            {graphRelationStatuses.map((status) => <label key={status}><input type="checkbox" checked={filter.relationStatuses.includes(status)} onChange={() => onChange({ ...filter, relationStatuses: toggle(filter.relationStatuses, status) })} /><span>{relationStatusLabel(status)}</span></label>)}
          </div>
        </fieldset>
        <fieldset>
          <legend>主张状态</legend>
          <div className="graph-toggle-grid">
            {graphFilterClaimStatuses.map((status) => <label key={status}><input type="checkbox" checked={filter.claimStatuses.includes(status)} onChange={() => onChange({ ...filter, claimStatuses: toggle(filter.claimStatuses, status) })} /><span>{graphClaimStatusLabel(status)}</span></label>)}
          </div>
        </fieldset>
        <details className="graph-relation-filter">
          <summary>关系类型 · {filter.relationTypes.length}</summary>
          <RelationTypeOptions filter={filter} onChange={onChange} />
        </details>
        <form className="graph-topic-filter" onSubmit={applyTopicScope}>
          <label><span>主题 ID</span><input value={topicDraft} onChange={(event) => setTopicDraft(event.target.value)} placeholder="UUID, UUID" /></label>
          <button type="submit" className="graph-text-button">应用主题范围</button>
        </form>
        <div className="graph-range-filter">
          <label className="graph-range-filter__switch"><input type="checkbox" checked={filter.claimMinConfidence !== undefined} onChange={(event) => onChange(event.target.checked ? { ...filter, claimMinConfidence: 0.5 } : withoutOptionalFilter(filter, "claimMinConfidence"))} /><span>主张最低置信度</span></label>
          <label><input type="range" min="0" max="1" step="0.05" disabled={filter.claimMinConfidence === undefined} value={filter.claimMinConfidence ?? 0.5} onChange={(event) => onChange({ ...filter, claimMinConfidence: Number(event.target.value) })} /><output>{filter.claimMinConfidence?.toFixed(2) ?? "关闭"}</output></label>
        </div>
        <div className="graph-range-filter">
          <label className="graph-range-filter__switch"><input type="checkbox" checked={filter.relationMinConfidence !== undefined} onChange={(event) => onChange(event.target.checked ? { ...filter, relationMinConfidence: 0.5 } : withoutOptionalFilter(filter, "relationMinConfidence"))} /><span>关系最低置信度</span></label>
          <label><input type="range" min="0" max="1" step="0.05" disabled={filter.relationMinConfidence === undefined} value={filter.relationMinConfidence ?? 0.5} onChange={(event) => onChange({ ...filter, relationMinConfidence: Number(event.target.value) })} /><output>{filter.relationMinConfidence?.toFixed(2) ?? "关闭"}</output></label>
        </div>
        <label className="graph-date-filter"><span>更新时间晚于</span><input type="datetime-local" value={toLocalInput(filter.updatedAfter)} onChange={(event) => onChange(event.target.value === "" ? withoutOptionalFilter(filter, "updatedAfter") : { ...filter, updatedAfter: new Date(event.target.value).toISOString() })} /></label>
      </div>
    </details>
  );
};
