import { afterEach, expect, it, vi } from "vitest";
import { synthesisId as id } from "../test/synthesis-fixtures";
import { applyHistoricalRepublish, beginHistoricalRepublish, decodeHistoricalRepublishReview, getHistoricalRepublishTarget, readHistoricalRepublish, resumeHistoricalRepublish } from "./synthesis-historical-republish";
const target = { workspace_id: id(1), note_id: id(2), selected_revision_id: id(4), selected_projection_hash: "a".repeat(64), expected_revision_id: id(5), expected_note_version: 2, expected_document_id: id(6), expected_document_version: 3, requires_retirement: false };
const review = { current_scope: null, workspace_id: id(1), note_id: id(2), attempt_id: id(3), state: "READY", target, candidate: "selected\r\n", current_content: "manual\n", published_content: "published\n", preview_fingerprint: "b".repeat(64), warnings: [], replayed: false };
const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { "Content-Type": "application/json" } });
afterEach(() => vi.unstubAllGlobals());
it("uses all generated operations with exact identity and retains applied snapshot without a proposal", async () => {
 const fetcher = vi.fn<typeof fetch>(); vi.stubGlobal("fetch", fetcher);
 fetcher.mockResolvedValueOnce(json(target)); await getHistoricalRepublishTarget(id(1), id(2), id(4)); expect(fetcher.mock.calls[0]?.[0]).toContain(`/historical-republish/target?selected_revision_id=${id(4)}`);
 fetcher.mockResolvedValueOnce(json(review)); await beginHistoricalRepublish({ ...target, idempotency_key: "original" }); expect(fetcher.mock.calls[1]?.[1]?.body).toBe(JSON.stringify({ ...target, idempotency_key: "original" }));
 fetcher.mockResolvedValueOnce(json(review)); await readHistoricalRepublish(id(1), id(2), id(4), "original"); expect(fetcher.mock.calls[2]?.[1]?.method).toBe("GET");
 const applied = { ...review, state: "APPLIED", result: { revision_id: id(7), article_revision_id: id(8) } }; fetcher.mockResolvedValueOnce(json(applied)); const result = await applyHistoricalRepublish({ workspace_id: id(1), note_id: id(2), attempt_id: id(3), idempotency_key: "apply", preview_fingerprint: review.preview_fingerprint, confirm_exact_restore: true, retire_current_candidate: false }, id(4)); expect(result.candidate).toBe("selected\r\n"); expect(result.result?.proposalId).toBeUndefined();
 fetcher.mockResolvedValueOnce(json(applied)); await resumeHistoricalRepublish(id(1), id(2), id(4), id(3)); expect(fetcher.mock.calls[4]?.[1]?.body).toBe("{}");
});
it("rejects internal fields, wrong selected identity and malformed applied proof", () => {
 for (const value of [{ ...review, authority: {} }, { ...review, target: { ...target, root: "secret" } }, { ...review, target: { ...target, selected_revision_id: id(99) } }, { ...review, state: "APPLIED" }, { ...review, candidate: "\ud800" }, { ...review, state: "APPLIED", result: { revision_id: id(7), article_revision_id: id(8), proposal_id: id(9) } }]) expect(() => decodeHistoricalRepublishReview(value, id(1), id(2), id(4))).toThrow();
});
