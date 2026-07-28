import { describe, expect, it } from "vitest";

import {
  emptyTimelineUrlState,
  parseTimelineUrlState,
  serializeTimelineUrlState,
  timelineFilterFromUrlState,
  type TimelineUrlState,
} from "./url-state";

const aggregateId = "74000000-0000-4000-8000-000000000001";

describe("Timeline URL state", () => {
  it("round-trips canonical recoverable filters and never persists cursor", () => {
    const state: TimelineUrlState = {
      eventTypes: ["VERSION_SUPERSEDED", "CONFLICT_OPENED", "CONFLICT_OPENED"],
      aggregateType: "CONFLICT",
      aggregateId,
      sourceEventRef: "conflict.opened:7400",
      occurredAfter: "2026-07-28T00:00:00Z",
      occurredBefore: "2026-07-29T00:00:00Z",
    };

    const serialized = serializeTimelineUrlState(state);
    expect(serialized.getAll("event_type")).toEqual(["CONFLICT_OPENED", "VERSION_SUPERSEDED"]);
    expect(serialized.has("cursor")).toBe(false);
    expect(parseTimelineUrlState(new URLSearchParams(`${serialized.toString()}&cursor=opaque-server-state`))).toEqual({
      ...state,
      eventTypes: ["CONFLICT_OPENED", "VERSION_SUPERSEDED"],
    });
  });

  it("normalizes duplicate, unknown and malformed URL values without forwarding them", () => {
    const parameters = new URLSearchParams();
    parameters.append("aggregate_type", "CONFLICT");
    parameters.append("aggregate_type", "ARTIFACT");
    parameters.append("event_type", "UNKNOWN_EVENT");
    parameters.set("aggregate_id", "not-a-uuid");
    parameters.set("source_event_ref", " contains-space-padding ");

    expect(parseTimelineUrlState(parameters)).toEqual(emptyTimelineUrlState);
    expect(timelineFilterFromUrlState(parseTimelineUrlState(parameters))).toEqual({});
  });

  it.each([
    "0000-02-29T00:00:00Z",
    "1900-02-29T00:00:00Z",
    "2000-02-30T00:00:00Z",
    "2026-13-01T00:00:00Z",
  ])("rejects invalid Gregorian timestamp %s", (value) => {
    expect(parseTimelineUrlState(new URLSearchParams({ occurred_after: value })).occurredAfter).toBe("");
  });

  it("keeps valid leap days and clears an inverted range as one binding", () => {
    expect(parseTimelineUrlState(new URLSearchParams({ occurred_after: "2000-02-29T23:59:59Z" })).occurredAfter).toBe("2000-02-29T23:59:59Z");
    const inverted = parseTimelineUrlState(new URLSearchParams({
      occurred_after: "2026-07-30T00:00:00Z",
      occurred_before: "2026-07-29T00:00:00Z",
    }));
    expect(inverted).toMatchObject({ occurredAfter: "", occurredBefore: "" });
  });

  it("clears a range inverted below millisecond precision", () => {
    const inverted = parseTimelineUrlState(new URLSearchParams({
      occurred_after: "2026-07-29T00:00:00.000000002Z",
      occurred_before: "2026-07-29T00:00:00.000000001Z",
    }));

    expect(inverted).toMatchObject({ occurredAfter: "", occurredBefore: "" });
  });

  it("does not serialize an inverted range but preserves unrelated valid filters", () => {
    const serialized = serializeTimelineUrlState({
      ...emptyTimelineUrlState,
      aggregateType: "ARTIFACT",
      occurredAfter: "2026-07-30T00:00:00Z",
      occurredBefore: "2026-07-29T00:00:00Z",
    });

    expect(serialized.get("aggregate_type")).toBe("ARTIFACT");
    expect(serialized.has("occurred_after")).toBe(false);
    expect(serialized.has("occurred_before")).toBe(false);
  });
});
