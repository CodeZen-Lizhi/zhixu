import { describe, expect, it } from "vitest";
import {
  hasExactKeys,
  hasOnlyKeys,
  isAbortError,
  isCanonicalUuid,
  isRecord,
} from "./codec";

describe("shared codec primitives", () => {
  it("recognizes records without treating arrays or null as records", () => {
    expect(isRecord({ value: 1 })).toBe(true);
    expect(isRecord(new Error("failed"))).toBe(true);
    expect(isRecord([])).toBe(false);
    expect(isRecord(null)).toBe(false);
  });

  it("recognizes native and structural abort errors", () => {
    const error = new Error("cancelled");
    error.name = "AbortError";

    expect(isAbortError(error)).toBe(true);
    expect(isAbortError(new DOMException("cancelled", "AbortError"))).toBe(true);
    expect(isAbortError({ name: "AbortError" })).toBe(true);
    expect(isAbortError(new Error("failed"))).toBe(false);
  });

  it("accepts lowercase RFC UUIDs and rejects non-canonical values", () => {
    expect(isCanonicalUuid("95000000-0000-4000-8000-0000000000aa")).toBe(true);
    expect(isCanonicalUuid("018f0000-0000-7000-8000-000000000001")).toBe(true);
    expect(isCanonicalUuid("00000000-0000-0000-0000-000000000000")).toBe(false);
    expect(isCanonicalUuid("95000000-0000-4000-8000-0000000000AA")).toBe(false);
    expect(isCanonicalUuid("95000000-0000-9000-8000-0000000000aa")).toBe(false);
    expect(isCanonicalUuid("95000000-0000-4000-7000-0000000000aa")).toBe(false);
    expect(isCanonicalUuid(" 95000000-0000-4000-8000-0000000000aa ")).toBe(false);
    expect(isCanonicalUuid("not-a-uuid")).toBe(false);
  });

  it("checks allowed, required, and optional key sets", () => {
    expect(hasOnlyKeys({ id: "1" }, ["id", "name"])).toBe(true);
    expect(hasOnlyKeys({ id: "1", extra: true }, ["id", "name"])).toBe(false);
    expect(hasExactKeys({ id: "1" }, ["id"], ["name"])).toBe(true);
    expect(hasExactKeys({ name: "test" }, ["id"], ["name"])).toBe(false);
    expect(hasExactKeys({ id: "1", extra: true }, ["id"], ["name"])).toBe(false);
  });
});
