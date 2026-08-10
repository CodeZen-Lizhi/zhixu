export const canonicalUuidPattern =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

export const isCanonicalUuid = (value: string): boolean =>
  canonicalUuidPattern.test(value);

export const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

export const isAbortError = (value: unknown): boolean =>
  (value instanceof Error && value.name === "AbortError")
  || (typeof DOMException !== "undefined"
    && value instanceof DOMException
    && value.name === "AbortError")
  || (isRecord(value) && value.name === "AbortError");

export const hasOnlyKeys = (
  value: Record<string, unknown>,
  allowed: readonly string[],
): boolean => {
  const allowedKeys = new Set(allowed);
  return Object.keys(value).every((key) => allowedKeys.has(key));
};

export const hasExactKeys = (
  value: Record<string, unknown>,
  required: readonly string[],
  optional: readonly string[] = [],
): boolean =>
  required.every((key) => Object.hasOwn(value, key))
  && hasOnlyKeys(value, [...required, ...optional]);
