/** 将两个 RFC3339 时刻之间的持续时间格式化为紧凑中文。 */
export const formatElapsedDuration = (
  startedAt: string,
  endedAt = new Date().toISOString(),
): string => {
  const start = Date.parse(startedAt);
  const end = Date.parse(endedAt);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) return "时长未知";
  const minutes = Math.floor((end - start) / 60_000);
  if (minutes < 1) return "不足 1 分钟";
  if (minutes < 60) return `${String(minutes)} 分钟`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  if (hours < 24) return remainingMinutes === 0 ? `${String(hours)} 小时` : `${String(hours)} 小时 ${String(remainingMinutes)} 分钟`;
  const days = Math.floor(hours / 24);
  const remainingHours = hours % 24;
  return remainingHours === 0 ? `${String(days)} 天` : `${String(days)} 天 ${String(remainingHours)} 小时`;
};

const localDatePattern = /^(\d{4})-(\d{2})-(\d{2})$/;

/** 校验并保留浏览器本地日期输入的 YYYY-MM-DD 语义。 */
export const canonicalLocalDate = (value: string | null | undefined): string | undefined => {
  if (value === null || value === undefined) return undefined;
  const match = localDatePattern.exec(value);
  if (match === null) return undefined;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const local = new Date(year, month - 1, day);
  if (local.getFullYear() !== year || local.getMonth() !== month - 1 || local.getDate() !== day) return undefined;
  return value;
};

/** 将本地日期零点只在 API 请求边界转换为 RFC3339。 */
export const localDateStartRfc3339 = (value: string): string => {
  const canonical = canonicalLocalDate(value);
  if (canonical === undefined) throw new RangeError("本地日期无效");
  return new Date(`${canonical}T00:00:00`).toISOString();
};
