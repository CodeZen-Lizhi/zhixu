import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createGitSyncRun, decodeGitRemoteConfig, decodeGitSyncRun, decodeGitSyncStatus, getGitSyncStatus, saveGitRemoteConfig } from "./git-sync";

const workspaceId = "10000000-0000-4000-8000-000000000001";
const runId = "20000000-0000-4000-8000-000000000001";

const configWire = () => ({
  workspace_id: workspaceId, configured: true, remote_url: "https://git.example.com/team/repo.git", branch: "main",
  auto_sync: false, token_configured: true, revision: 2, created_at: "2026-08-03T01:00:00Z", updated_at: "2026-08-03T01:00:00Z",
});

const runWire = () => ({
  id: runId, workspace_id: workspaceId, config_revision: 2, remote_url: "https://git.example.com/team/repo.git", branch: "main",
  trigger: "MANUAL", retry_of_run_id: null, status: "SUCCEEDED", direction: "PULL", failure_class: "NONE", error_code: "", retryable: false,
  expected_head_oid: "a".repeat(40), expected_remote_oid: "b".repeat(40), verified_head_oid: "b".repeat(40), verified_remote_oid: "b".repeat(40),
  changed_files: [{ path: "notes/new.md", old_path: null, kind: "ADDED" }], index_status: "FAILED", index_error_code: "INDEX_FAILED",
  index_retryable: true, index_version_id: null, attempt_count: 1, version: 7, created_at: "2026-08-03T01:00:00Z",
  updated_at: "2026-08-03T01:01:00Z", completed_at: "2026-08-03T01:01:00Z",
});

const pendingRunWire = () => ({
  ...runWire(),
  status: "PENDING", direction: "UNKNOWN", expected_head_oid: null, expected_remote_oid: null,
  verified_head_oid: null, verified_remote_oid: null, changed_files: [], index_status: "NOT_REQUIRED",
  index_error_code: "", index_retryable: false, attempt_count: 0, completed_at: null,
});

const without = (value: Record<string, unknown>, field: string): Record<string, unknown> => {
  return Object.fromEntries(Object.entries(value).filter(([key]) => key !== field));
};

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn<typeof fetch>());
  vi.stubGlobal("crypto", { randomUUID: () => "30000000-0000-4000-8000-000000000001" });
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Git sync API boundary", () => {
  it("strictly decodes config and independent Git/index status", () => {
    expect(decodeGitRemoteConfig(configWire())).toMatchObject({ configured: true, tokenConfigured: true, revision: 2 });
    expect(decodeGitSyncRun(runWire())).toMatchObject({ status: "SUCCEEDED", indexStatus: "FAILED", changedFiles: [{ kind: "ADDED", path: "notes/new.md" }] });
    expect(() => decodeGitRemoteConfig({ ...configWire(), token: "must-not-exist" })).toThrow(/响应字段无效/);
    expect(() => decodeGitSyncRun({ ...runWire(), status: "MERGING" })).toThrow(/响应字段无效/);
    expect(() => decodeGitSyncRun({ ...runWire(), changed_files: [{ path: "a.md", kind: "ADDED" }] })).toThrow(/响应字段无效/);
    expect(() => decodeGitSyncRun({ ...runWire(), changed_files: [{ path: "a.md", old_path: null, kind: "RENAMED" }] })).toThrow(/响应字段无效/);
    expect(() => decodeGitSyncRun({ ...runWire(), changed_files: [{ path: "a.md", old_path: "a.md", kind: "RENAMED" }] })).toThrow(/响应字段无效/);
    expect(() => decodeGitSyncRun({ ...runWire(), id: "not-a-uuid" })).toThrow(/响应字段无效/);
    expect(() => decodeGitSyncRun({ ...runWire(), expected_head_oid: "A".repeat(40) })).toThrow(/响应字段无效/);
    expect(() => decodeGitRemoteConfig({ ...configWire(), created_at: "2026-02-31T01:00:00Z" })).toThrow(/响应字段无效/);
    for (const branch of ["-topic", "feature//topic", "feature/.hidden", "feature/topic.lock", "feature/foo.lock/bar"]) {
      expect(() => decodeGitRemoteConfig({ ...configWire(), branch })).toThrow(/响应字段无效/);
    }
  });

  it("rejects contradictory config projection shapes", () => {
    const tombstone = {
      ...configWire(), configured: false, remote_url: null, branch: null, auto_sync: false, token_configured: false,
    };
    expect(decodeGitRemoteConfig(tombstone)).toMatchObject({ configured: false, revision: 2 });
    expect(() => decodeGitRemoteConfig({ ...tombstone, auto_sync: true })).toThrow(/响应字段无效/);
    expect(() => decodeGitRemoteConfig({ ...tombstone, remote_url: configWire().remote_url })).toThrow(/响应字段无效/);
    expect(() => decodeGitRemoteConfig({ ...configWire(), revision: 0, created_at: null, updated_at: null })).toThrow(/响应字段无效/);
    expect(() => decodeGitRemoteConfig({ ...configWire(), created_at: null })).toThrow(/响应字段无效/);
    expect(() => decodeGitRemoteConfig({ ...configWire(), updated_at: "2026-08-02T01:00:00Z" })).toThrow(/响应字段无效/);
    for (const field of ["remote_url", "branch", "created_at", "updated_at"]) {
      expect(() => decodeGitRemoteConfig(without(tombstone, field))).toThrow(/响应字段无效/);
    }
  });

  it("rejects contradictory run, ref, and index state combinations", () => {
    const invalidRuns = [
      { ...pendingRunWire(), completed_at: runWire().completed_at },
      { ...pendingRunWire(), failure_class: "OFFLINE", error_code: "GIT_SYNC_OFFLINE", retryable: true },
      { ...pendingRunWire(), direction: "PULL", expected_head_oid: "a".repeat(40), expected_remote_oid: "b".repeat(40) },
      { ...pendingRunWire(), expected_head_oid: "a".repeat(40) },
      { ...pendingRunWire(), trigger: "RETRY" },
      { ...pendingRunWire(), retry_of_run_id: runId },
      { ...pendingRunWire(), attempt_count: 1001 },
      { ...runWire(), failure_class: "DIRTY", error_code: "GIT_SYNC_CONFLICT" },
      { ...runWire(), verified_remote_oid: "c".repeat(40) },
      { ...runWire(), expected_remote_oid: "a".repeat(40) },
      { ...runWire(), index_status: "NOT_REQUIRED", index_error_code: "", index_retryable: false },
      { ...runWire(), index_error_code: "" },
      { ...runWire(), completed_at: "2026-08-02T01:02:00Z" },
    ];
    for (const run of invalidRuns) expect(() => decodeGitSyncRun(run)).toThrow(/响应字段无效/);
    for (const field of ["retry_of_run_id", "expected_head_oid", "verified_head_oid", "index_version_id", "completed_at"]) {
      expect(() => decodeGitSyncRun(without(pendingRunWire(), field))).toThrow(/响应字段无效/);
    }
  });

  it("accepts the zero-attempt pending run returned when a manual sync is queued", () => {
    expect(decodeGitSyncRun(pendingRunWire())).toMatchObject({ status: "PENDING", attemptCount: 0 });
  });

  it("decodes status without accepting a secret field", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({ config: configWire(), current_run: runWire() }), { status: 200, headers: { "Content-Type": "application/json" } }));
    const result = await getGitSyncStatus(workspaceId);
    expect(result.currentRun?.status).toBe("SUCCEEDED");
    expect(result.currentRun?.indexStatus).toBe("FAILED");
  });

  it("accepts an unconfigured status without a current run", async () => {
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify({
      config: {
        workspace_id: workspaceId, configured: false, remote_url: null, branch: null,
        auto_sync: false, token_configured: false, revision: 0, created_at: null, updated_at: null,
      },
      current_run: null,
    }), { status: 200, headers: { "Content-Type": "application/json" } }));

    await expect(getGitSyncStatus(workspaceId)).resolves.toEqual({
      config: {
        workspaceId, configured: false, autoSync: false, tokenConfigured: false,
        revision: 0, replayed: false,
      },
    });
  });

  it("rejects a status response that omits the explicit current_run field", () => {
    const config = {
      workspace_id: workspaceId, configured: false, remote_url: null, branch: null,
      auto_sync: false, token_configured: false, revision: 0, created_at: null, updated_at: null,
    };
    expect(() => decodeGitSyncStatus({ config })).toThrow(/响应字段无效/);
    expect(() => decodeGitSyncStatus({ config, current_run: undefined })).toThrow(/响应字段无效/);
  });

  it("sends replacement token only in the mutation body and never expects it back", async () => {
    const responsePayload = configWire();
    vi.mocked(fetch).mockResolvedValue(new Response(JSON.stringify(responsePayload), { status: 201, headers: { "Content-Type": "application/json" } }));
    const token = "git-token-that-must-not-persist";
    await saveGitRemoteConfig(workspaceId, { expectedRevision: 2, remoteUrl: configWire().remote_url, branch: "main", autoSync: false, token: { action: "replace", value: token }, idempotencyKey: "git-remote-save-test-1" });

    const [path, init] = vi.mocked(fetch).mock.calls[0] ?? [];
    expect(path).toBe(`/api/v1/workspaces/${workspaceId}/git-remote`);
    expect(init?.method).toBe("PUT");
    expect(typeof init?.body).toBe("string");
    expect(init?.body).toContain(token);
    expect(init?.headers).toBeInstanceOf(Headers);
    expect(new Headers(init?.headers).get("Idempotency-Key")).toBe("git-remote-save-test-1");
    expect(JSON.stringify(responsePayload)).not.toContain(token);
  });

  it("uses the caller-owned key for a response-loss retry instead of generating a second command", async () => {
    vi.mocked(fetch).mockRejectedValueOnce(new TypeError("response lost"));
    vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify(runWire()), { status: 202, headers: { "Content-Type": "application/json" } }));
    const input = { idempotencyKey: "git-sync-create-retry-1" };
    await expect(createGitSyncRun(workspaceId, input)).rejects.toMatchObject({ code: "NETWORK_ERROR" });
    await expect(createGitSyncRun(workspaceId, input)).resolves.toMatchObject({ id: runId });
    expect(new Headers(vi.mocked(fetch).mock.calls[0]?.[1]?.headers).get("Idempotency-Key")).toBe(input.idempotencyKey);
    expect(new Headers(vi.mocked(fetch).mock.calls[1]?.[1]?.headers).get("Idempotency-Key")).toBe(input.idempotencyKey);
  });
});
