import {
  compareTimelineTimestamps,
  timelineAggregateTypes,
  timelineEventTypes,
  type TimelineAggregateType,
  type TimelineEventType,
  type TimelineFilter,
} from "../../api/timeline";

export interface TimelineUrlState {
  eventTypes: TimelineEventType[];
  aggregateType: TimelineAggregateType | "";
  aggregateId: string;
  sourceEventRef: string;
  occurredAfter: string;
  occurredBefore: string;
}

export const emptyTimelineUrlState: TimelineUrlState = {
  eventTypes: [],
  aggregateType: "",
  aggregateId: "",
  sourceEventRef: "",
  occurredAfter: "",
  occurredBefore: "",
};

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/;
const textEncoder = new TextEncoder();

const one = (parameters: URLSearchParams, key: string): string | undefined => {
  const values = parameters.getAll(key);
  return values.length === 1 ? values[0] : undefined;
};

const validTimestamp = (value: string | undefined): value is string => {
  if (value === undefined || !timestampPattern.test(value) || !Number.isFinite(Date.parse(value))) return false;
  const matched = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})/.exec(value);
  if (matched === null) return false;
  const year = Number(matched[1]);
  const month = Number(matched[2]);
  const day = Number(matched[3]);
  const hour = Number(matched[4]);
  const minute = Number(matched[5]);
  const second = Number(matched[6]);
  const days = month === 2
    ? year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28
    : month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
  return year >= 1 && month >= 1 && month <= 12 && day >= 1 && day <= days && hour <= 23 && minute <= 59 && second <= 59;
};

const validSourceRef = (value: string | undefined): value is string =>
  value !== undefined && value !== "" && value.trim() === value && textEncoder.encode(value).byteLength <= 512 && !/[\u0000-\u001f\u007f]/.test(value);

export const parseTimelineUrlState = (parameters: URLSearchParams): TimelineUrlState => {
  const rawEventTypes = parameters.getAll("event_type").flatMap((value) => value.split(","));
  const eventTypes = rawEventTypes.length <= 32 && rawEventTypes.every((value) => timelineEventTypes.some((candidate) => candidate === value))
    ? [...new Set(rawEventTypes)].sort().flatMap((value) => {
        const resolved = timelineEventTypes.find((candidate) => candidate === value);
        return resolved === undefined ? [] : [resolved];
      })
    : [];
  const aggregateTypeValue = one(parameters, "aggregate_type");
  const aggregateType = timelineAggregateTypes.find((candidate) => candidate === aggregateTypeValue) ?? "";
  const aggregateIdValue = one(parameters, "aggregate_id");
  const sourceEventRefValue = one(parameters, "source_event_ref");
  let occurredAfter = one(parameters, "occurred_after");
  let occurredBefore = one(parameters, "occurred_before");
  occurredAfter = validTimestamp(occurredAfter) ? occurredAfter : "";
  occurredBefore = validTimestamp(occurredBefore) ? occurredBefore : "";
  if (occurredAfter !== "" && occurredBefore !== "" && compareTimelineTimestamps(occurredAfter, occurredBefore) > 0) {
    occurredAfter = "";
    occurredBefore = "";
  }
  return {
    eventTypes,
    aggregateType,
    aggregateId: aggregateIdValue !== undefined && uuidPattern.test(aggregateIdValue) ? aggregateIdValue : "",
    sourceEventRef: validSourceRef(sourceEventRefValue) ? sourceEventRefValue : "",
    occurredAfter,
    occurredBefore,
  };
};

export const serializeTimelineUrlState = (state: TimelineUrlState): URLSearchParams => {
  const parameters = new URLSearchParams();
  for (const eventType of [...new Set(state.eventTypes)].sort()) parameters.append("event_type", eventType);
  if (state.aggregateType !== "") parameters.set("aggregate_type", state.aggregateType);
  if (uuidPattern.test(state.aggregateId)) parameters.set("aggregate_id", state.aggregateId);
  if (validSourceRef(state.sourceEventRef)) parameters.set("source_event_ref", state.sourceEventRef);
  const occurredAfter = validTimestamp(state.occurredAfter) ? state.occurredAfter : undefined;
  const occurredBefore = validTimestamp(state.occurredBefore) ? state.occurredBefore : undefined;
  if (occurredAfter !== undefined && occurredBefore !== undefined && compareTimelineTimestamps(occurredAfter, occurredBefore) > 0) return parameters;
  if (occurredAfter !== undefined) parameters.set("occurred_after", occurredAfter);
  if (occurredBefore !== undefined) parameters.set("occurred_before", occurredBefore);
  return parameters;
};

export const timelineFilterFromUrlState = (state: TimelineUrlState): TimelineFilter => ({
  ...(state.eventTypes.length === 0 ? {} : { eventTypes: state.eventTypes }),
  ...(state.aggregateType === "" ? {} : { aggregateType: state.aggregateType }),
  ...(state.aggregateId === "" ? {} : { aggregateId: state.aggregateId }),
  ...(state.sourceEventRef === "" ? {} : { sourceEventRef: state.sourceEventRef }),
  ...(state.occurredAfter === "" ? {} : { occurredAfter: state.occurredAfter }),
  ...(state.occurredBefore === "" ? {} : { occurredBefore: state.occurredBefore }),
});
