import type { SearchMode } from "../../api/search";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const textEncoder = new TextEncoder();
const maxQueryBytes = 8 * 1024;
const maxCursorBytes = 2048;

interface SingleParameter {
  value?: string;
  valid: boolean;
}

const single = (parameters: URLSearchParams, name: string): SingleParameter => {
  const values = parameters.getAll(name);
  if (values.length === 0) return { valid: true };
  if (values.length !== 1) return { valid: false };
  const value = values[0];
  return value === undefined ? { valid: false } : { value, valid: true };
};

export const normalizeSearchQuery = (value: string): string | undefined => {
  const normalized = value.trim();
  if (normalized === "" || normalized.includes("\0") || textEncoder.encode(normalized).length > maxQueryBytes) {
    return undefined;
  }
  return normalized;
};

export const normalizeSourceVersionId = (value: string): string | undefined => {
  const normalized = value.trim();
  return uuidPattern.test(normalized) ? normalized : undefined;
};

const normalizeCursor = (value: string): string | undefined =>
  value !== "" && value.trim() === value && textEncoder.encode(value).length <= maxCursorBytes
    ? value
    : undefined;

const readMode = (field: SingleParameter): { mode: SearchMode; valid: boolean } => {
  if (!field.valid) return { mode: "hybrid", valid: false };
  if (field.value === undefined) return { mode: "hybrid", valid: true };
  if (field.value === "keyword" || field.value === "semantic" || field.value === "hybrid") {
    return { mode: field.value, valid: true };
  }
  return { mode: "hybrid", valid: false };
};

export interface SearchUrlState {
  query: string;
  mode: SearchMode;
  sourceVersionId: string;
  cursor: string;
}

export const parseSearchUrlState = (
  parameters: URLSearchParams,
  workspaceId: string,
): SearchUrlState => {
  const queryField = single(parameters, "query");
  const modeField = readMode(single(parameters, "mode"));
  const sourceVersionField = single(parameters, "source_version_id");
  const cursorField = single(parameters, "cursor");
  const scopeWorkspaceField = single(parameters, "scope_workspace");

  // Legacy unscoped deep links are adopted by the current Workspace once and
  // immediately canonicalized with scope_workspace. An explicit mismatched or
  // duplicated scope fails closed so switching Workspace never replays an old
  // query or Source Version filter against the new Workspace.
  const scopeBindingValid = scopeWorkspaceField.valid
    && workspaceId !== ""
    && (scopeWorkspaceField.value === undefined || scopeWorkspaceField.value === workspaceId);
  const query = !scopeBindingValid || queryField.value === undefined ? "" : normalizeSearchQuery(queryField.value) ?? "";
  const sourceVersionId = !scopeBindingValid || sourceVersionField.value === undefined
    ? ""
    : normalizeSourceVersionId(sourceVersionField.value) ?? "";
  const cursor = cursorField.value === undefined ? undefined : normalizeCursor(cursorField.value);
  const sourceVersionValid = sourceVersionField.valid
    && (sourceVersionField.value === undefined || sourceVersionId !== "");
  const queryValid = queryField.valid && (queryField.value === undefined || query !== "");
  const cursorBindingValid = scopeBindingValid
    && queryValid
    && modeField.valid
    && sourceVersionValid
    && cursorField.valid
    && query !== "";

  return {
    query,
    mode: modeField.mode,
    sourceVersionId,
    cursor: cursorBindingValid ? cursor ?? "" : "",
  };
};

export const writeSearchUrlState = (
  state: SearchUrlState,
  workspaceId: string,
): URLSearchParams => {
  const parameters = new URLSearchParams();
  const query = state.query === "" ? "" : normalizeSearchQuery(state.query) ?? "";
  const sourceVersionId = state.sourceVersionId === ""
    ? ""
    : normalizeSourceVersionId(state.sourceVersionId) ?? "";
  const cursor = normalizeCursor(state.cursor);

  if (query !== "") parameters.set("query", query);
  if (state.mode !== "hybrid") parameters.set("mode", state.mode);
  if (sourceVersionId !== "") parameters.set("source_version_id", sourceVersionId);
  if (workspaceId !== "" && (query !== "" || sourceVersionId !== "" || cursor !== undefined)) {
    parameters.set("scope_workspace", workspaceId);
  }
  if (query !== "" && cursor !== undefined && workspaceId !== "") {
    parameters.set("cursor", cursor);
  }
  return parameters;
};
