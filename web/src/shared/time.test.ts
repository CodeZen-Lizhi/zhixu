import { describe, expect, it } from "vitest";

import { canonicalLocalDate, formatElapsedDuration, localDateStartRfc3339 } from "./time";

describe("formatElapsedDuration", () => {
  it.each([
    ["2026-07-22T00:00:00Z", "2026-07-22T00:00:30Z", "不足 1 分钟"],
    ["2026-07-22T00:00:00Z", "2026-07-22T00:42:00Z", "42 分钟"],
    ["2026-07-22T00:00:00Z", "2026-07-22T02:15:00Z", "2 小时 15 分钟"],
    ["2026-07-22T00:00:00Z", "2026-07-24T03:00:00Z", "2 天 3 小时"],
  ])("formats %s to %s", (start, end, expected) => {
    expect(formatElapsedDuration(start, end)).toBe(expected);
  });

  it("fails closed for invalid or reversed timestamps", () => {
    expect(formatElapsedDuration("invalid", "2026-07-22T00:00:00Z")).toBe("时长未知");
    expect(formatElapsedDuration("2026-07-23T00:00:00Z", "2026-07-22T00:00:00Z")).toBe("时长未知");
  });
});

describe("local date boundary", () => {
  it("保留日期输入并仅在请求边界转换时区", () => {
    const selected = canonicalLocalDate("2026-07-22");

    expect(selected).toBe("2026-07-22");
    const boundary = new Date(localDateStartRfc3339(selected ?? ""));
    expect([boundary.getFullYear(), boundary.getMonth() + 1, boundary.getDate()]).toEqual([2026, 7, 22]);
  });

  it.each(["", "2026-02-30", "2026-7-22", "invalid"])("拒绝非法本地日期 %s", (value) => {
    expect(canonicalLocalDate(value)).toBeUndefined();
  });
});
