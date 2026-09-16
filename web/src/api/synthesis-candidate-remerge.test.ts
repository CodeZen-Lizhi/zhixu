import { afterEach, expect, it, vi } from "vitest";
import { resumeCandidateRemerge, applyCandidateRemerge, beginCandidateRemerge, decodeCandidateRemergeReview, decodeCandidateRemergeTarget, getCandidateRemergeTarget, readCandidateRemerge } from "./synthesis-candidate-remerge";
import { synthesisId as id } from "../test/synthesis-fixtures";
const target = { workspace_id: id(1), note_id: id(2), expected_revision_id: id(4), expected_note_version: 2, expected_document_id: id(5), expected_document_version: 3, expected_publication_id: id(6), expected_proposal_id: id(7), expected_proposal_revision_id: id(8), expected_proposal_version: 4 };
const review = { workspace_id: id(1), note_id: id(2), attempt_id: id(3), state: "CONFLICTS", preview_fingerprint: "a".repeat(64), replayed: false, review: { stage: "WORKSPACE_MANUAL_CONTENT", base: "base", current: "latest", proposed: "full human candidate", candidate: "conflicts", conflicts: [{ ordinal: 1, base: "base", current: "latest", proposed: "candidate" }] } };
const applied = { ...review, state: "APPLIED", review: undefined, result: { revision_id: id(9), article_revision_id: id(10) } };
const json = (v: unknown) => new Response(JSON.stringify(v), { headers: { "Content-Type": "application/json" } });
afterEach(() => vi.unstubAllGlobals());
it("strict target preserves exact independent owner versions and rejects private or scoped identities", () => {
  expect(decodeCandidateRemergeTarget(target, id(1), id(2))).toEqual(target);
  for (const bad of [{ ...target, workspace_id: id(99) }, { ...target, note_id: id(99) }, { ...target, expected_document_version: 0 }, { ...target, expected_proposal_version: 1.5 }, { ...target, content_hash: "a".repeat(64) }, { ...target, expected_publication_id: null }]) expect(() => decodeCandidateRemergeTarget(bad, id(1), id(2))).toThrow();
});
it("strict review binds scope and attempt and rejects unsafe wire, unknown states, stages and ordinal gaps", () => {
  expect(decodeCandidateRemergeReview(review, id(1), id(2), id(3)).review?.proposed).toBe("full human candidate");
  for (const bad of [{ ...review, attempt_id: id(99) }, { ...review, workspace_id: id(99) }, { ...review, state: "PUBLISHED" }, { ...review, capture: {} }, { ...review, review: { ...review.review, stage: "CANDIDATE_MANUAL_CONTENT" } }, { ...review, review: { ...review.review, base: "\ud800" } }, { ...review, review: { ...review.review, candidate: "\0" } }, { ...review, review: { ...review.review, conflicts: [{ Ordinal: 1, Base: "YmFzZQ==" }] } }, { ...review, review: { ...review.review, conflicts: [{ ordinal: 2, base: "a", current: "b", proposed: "c" }] } }]) expect(() => decodeCandidateRemergeReview(bad, id(1), id(2), id(3))).toThrow();
});
it("incomplete proposal result remains a candidate without invented approval identity", () => {
  const value: unknown = JSON.parse(JSON.stringify(applied));
  expect(decodeCandidateRemergeReview(value, id(1), id(2)).result?.proposalId).toBeUndefined();
  expect(() => decodeCandidateRemergeReview({ ...applied, review: undefined, result: { ...applied.result, proposal_id: id(11) } }, id(1), id(2))).toThrow();
});
it("generated transport keeps frozen Begin and Apply keys and reads only the Begin query key", async () => {
  const calls: { url: string; body: unknown }[] = [];
  vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockImplementation((input, init) => { const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url; calls.push({ url, body: typeof init?.body === "string" ? JSON.parse(init.body) : null }); return Promise.resolve(json(url.endsWith("/target") ? target : url.endsWith("/apply") ? applied : review)); }));
  const begin = { ...target, idempotency_key: "begin-original" };
  expect(await getCandidateRemergeTarget(id(1), id(2))).toEqual(target);
  await beginCandidateRemerge(begin); await readCandidateRemerge(id(1), id(2), begin.idempotency_key);
  const apply = { workspace_id: id(1), note_id: id(2), attempt_id: id(3), idempotency_key: "apply-original", resolution: { stage: "WORKSPACE_MANUAL_CONTENT" as const, preview_fingerprint: review.preview_fingerprint, acknowledged_ordinals: [1], final_content: "人工最终全文" } };
  await applyCandidateRemerge(apply); await applyCandidateRemerge(apply);
  expect(calls[0]?.url).toContain("/candidate-remerge/target"); expect(calls[0]?.body).toBeNull(); expect(calls[1]?.body).toEqual(begin); expect(calls[2]?.url).toContain("?key=begin-original"); expect(calls[2]?.body).toBeNull(); expect(calls[3]?.body).toEqual(apply); expect(calls[4]?.body).toEqual(apply);
});

it("READY requires actual merged text, preserving empty UTF-8 and refusing missing or malformed preview", () => {
  const ready = { workspace_id: id(1), note_id: id(2), attempt_id: id(3), state: "READY", preview_fingerprint: "a".repeat(64), replayed: false, candidate: "actual newly merged human text" };
  expect(decodeCandidateRemergeReview(ready, id(1), id(2)).candidate).toBe(ready.candidate);
  expect(decodeCandidateRemergeReview({ ...ready, candidate: "" }, id(1), id(2)).candidate).toBe("");
  for (const bad of [{ ...ready, candidate: undefined }, { ...ready, candidate: null }, { ...ready, candidate: "\0" }, { ...ready, review: review.review }, { ...ready, candidate: "x".repeat(1048577) }]) expect(() => decodeCandidateRemergeReview(bad, id(1), id(2))).toThrow();
});

it("Resume posts only an empty object to the exact attempt and validates its response", async () => {
  const calls: { url: string; init: RequestInit | undefined }[] = [];
  const fetcher = vi.fn<typeof fetch>().mockImplementation((input, init) => { calls.push({ url: typeof input === "string" ? input : input instanceof URL ? input.href : input.url, init }); return Promise.resolve(json(applied)); });
  vi.stubGlobal("fetch", fetcher);
  await resumeCandidateRemerge(id(1), id(2), id(3)); await resumeCandidateRemerge(id(1), id(2), id(3));
  for (const call of calls) { expect(call.url).toContain(`/candidate-remerge/${id(3)}/resume`); expect(call.init?.method).toBe("POST"); expect(call.init?.body).toBe("{}"); expect(new Headers(call.init?.headers).has("Idempotency-Key")).toBe(false); }
  fetcher.mockResolvedValueOnce(json({ ...applied, attempt_id: id(99) }));
  await expect(resumeCandidateRemerge(id(1), id(2), id(3))).rejects.toThrow();
});
