const rfc3339Pattern = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|([+-])(\d{2}):(\d{2}))$/;

const daysInMonth = (year: number, month: number): number => {
  if (month === 2) {
    const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    return leapYear ? 29 : 28;
  }
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
};

export const canonicalGraphTimestamp = (value: string): string | undefined => {
  const match = rfc3339Pattern.exec(value);
  if (match === null) return undefined;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  const fraction = match[7] ?? "";
  const zone = match[8];
  const sign = match[9];
  const offsetHour = Number(match[10] ?? 0);
  const offsetMinute = Number(match[11] ?? 0);
  if (year < 1 || month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month)
    || hour > 23 || minute > 59 || second > 59 || offsetHour > 23 || offsetMinute > 59
    || !Number.isFinite(Date.parse(value))) {
    return undefined;
  }

  const local = new Date(0);
  local.setUTCFullYear(year, month - 1, day);
  local.setUTCHours(hour, minute, second, 0);
  const offsetSign = zone === "Z" || sign === "+" ? 1 : -1;
  const offsetMilliseconds = (zone === "Z" ? 0 : offsetSign * (offsetHour * 60 + offsetMinute)) * 60_000;
  const utc = new Date(local.getTime() - offsetMilliseconds);
  if (utc.getUTCFullYear() < 1 || utc.getUTCFullYear() > 9999) return value;
  const canonicalFraction = fraction.replace(/0+$/, "");
  return `${utc.toISOString().slice(0, 19)}${canonicalFraction === "" ? "" : `.${canonicalFraction}`}Z`;
};
