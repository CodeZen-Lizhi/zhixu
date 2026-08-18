# Research: OpenAPI 3.1 spec readiness for Spectral and oasdiff

- Query: Assess `api/openapi/openapi.json` readiness for standard Spectral linting and oasdiff compatibility checks, including structure, operations, schemas, security, SSE/download exceptions, naming, descriptions, examples, nullability/unions/discriminators, and the assertions that must remain project-owned.
- Scope: mixed
- Date: 2026-08-18

## Findings

### Executive assessment

The document is close enough to adopt Spectral and oasdiff without redesigning the API, but it is not ready to become the approved baseline unchanged.

1. A stock `spectral:oas` run reports **290 findings: 3 errors and 287 warnings**. One warning is a confirmed false positive caused by `$ref` resolution; with that rule corrected to run unresolved, the actionable baseline is **289 findings: 3 errors and 286 warnings**.
2. oasdiff v1.29.1 cannot load the current document because the valid OpenAPI 3.1 / JSON Schema boolean schema `"items": false` is not accepted by its Go model. Replacing it with an equivalent object schema makes `oasdiff breaking` load the document and a self-comparison returns `[]`.
3. The highest-risk gaps are not stylistic: 62 protected operations omit `401`, 61 omit `403`, and 22 omit `405`, even though authentication and global 405 behavior are runtime contracts. Standard Spectral and a newly snapshotted oasdiff baseline will not discover that the initial baseline is incomplete.
4. Standard tools cannot replace most of the 4,178-line project checker. `check.mjs` contains 413 explicit failure sites, primarily for capability, idempotency, Problem/error matrices, bounded payloads, exact tagged unions, SSE, download headers, and cross-source route parity. Spectral should replace generic syntax/style checks; oasdiff should detect later compatibility regressions; project-specific invariants must remain.

Recommended readiness order:

1. Fix the three structural/interoperability errors and tabs.
2. Close the missing auth/405 response documentation and add explicit mappings to the two incomplete discriminators.
3. Add stable domain tags, the 85 missing descriptions, a relative same-origin server, and remove or wire the three unused components.
4. Commit a reviewed Spectral ruleset with narrow documented exceptions, then create the immutable approved oasdiff baseline.
5. Keep the project checker for the semantic contracts listed below; delete only checks demonstrably covered by Spectral, oasdiff, or the runtime route inventory test.

### Files found

| File | Role |
| --- | --- |
| `api/openapi/openapi.json` | OpenAPI 3.1 wire-contract source of truth. |
| `api/openapi/check.mjs` | Current bespoke contract checker; also reads Export, Git Sync, and Auth Go handler source. |
| `internal/app/router_inventory_test.go` | Exact runtime Gin route versus OpenAPI operation inventory gate. |
| `internal/auth/http/handler.go` | Runtime authentication and capability policy used to assess OpenAPI security completeness. |
| `Makefile` | Currently exposes only `openapi-check`, which runs `node api/openapi/check.mjs` (`Makefile:199`). |
| `.github/workflows/ci.yml` | CI uses Node 24.18.0 and Go 1.25.4, then reaches OpenAPI through `make test` (`.github/workflows/ci.yml:33`, `.github/workflows/ci.yml:41`, `.github/workflows/ci.yml:54`). |
| `docs/roadmap.md` | TODO 7 scope: pinned tools/rules/baseline, breaking-change failure, local/CI parity, and preservation of project-specific constraints (`docs/roadmap.md:78`). |
| `.trellis/spec/backend/http-boundary.md` | Runtime/OpenAPI route, Problem, SSE, upload/download, and 404/405 boundary contract. |
| `.trellis/spec/backend/auth-security.md` | Exact Cookie/Bearer/Bootstrap, CSRF/Origin, capability, and error semantics. |
| `.trellis/spec/backend/export-contract.md` | Exact binary download media types and security headers. |
| `.trellis/spec/backend/eino-runtime-adoption-gates.md` | Transient Answer draft SSE cursor/event/publication contract. |

### Document inventory and healthy patterns

Measured from the parsed document:

| Item | Count / result |
| --- | --- |
| OpenAPI version | `3.1.0` (`api/openapi/openapi.json:2`) |
| Paths / operations | 160 / 189 |
| Component schemas | 525 |
| Component responses / parameters / headers | 17 / 20 / 1 |
| Operation IDs | 189 present, 189 unique, all lower camelCase |
| Schema names | All PascalCase |
| JSON property names | 3,059 inspected, all lower snake_case |
| Local references | 562 unique local `$ref`s, none unresolved and none external |
| Strict object schemas | 482 occurrences of `additionalProperties: false` |

The missing `jsonSchemaDialect` is valid: OpenAPI 3.1 uses the OAS dialect by default. Do not add a dialect merely to silence a tool, and do not rewrite the document to OpenAPI 3.0 `nullable` syntax.

### Spectral first-run results

Probe versions were `@stoplight/spectral-cli` 6.16.3 and `@stoplight/spectral-rulesets` 1.22.7. The unmodified recommended OAS ruleset produced:

| Rule | Severity | Count | Disposition |
| --- | --- | ---: | --- |
| `array-items` | error | 1 | Fix for tool interoperability. |
| `oas3-schema` | error | 2 | Fix; these are invalid Media Type Objects. |
| `operation-tags` | warning | 189 | Fix before TODO 8 so generated APIs are grouped by stable domains. |
| `operation-description` | warning | 85 | Fix with meaningful English descriptions, following the existing document language. |
| `parser` | warning | 6 | Fix two lines containing three tab characters each. |
| `oas3-unused-component` | warning | 3 | Remove or actually reference the components; do not blanket-disable. |
| `oas3-api-servers` | warning | 1 | Add `servers: [{"url":"/"}]` for the same-origin/self-hosted contract. |
| `info-contact` | warning | 1 | Configure off unless a real maintained contact can be supplied; do not invent metadata. |
| `operation-success-response` | warning | 1 | Intentional reserved endpoint; replace the stock rule with a project-aware assertion. |
| `oas3-examples-value-or-externalValue` | warning | 1 | Confirmed false positive; retain the rule with `resolved: false`. |

Specific fixes:

- `POST /api/v1/source-versions/{source_version_id}/ingestion-attempts` places `$ref` directly under the `application/json` Media Type Object for `405` and `503` (`api/openapi/openapi.json:1752`, `api/openapi/openapi.json:1770`). Wrap each reference under `schema`.
- `WorkflowMergeComparisonReview.categories` uses `prefixItems` plus `minItems: 4`, `maxItems: 4`, and `items: false` (`api/openapi/openapi.json:15919`). The boolean schema is valid 3.1 JSON Schema, but Spectral's rule and oasdiff's loader reject it. Because the exact length is already fixed at four, `items: {}` is validation-equivalent and interoperable. Update the corresponding exact assertion in `check.mjs:1362` without weakening the four-element/order checks.
- Lines `api/openapi/openapi.json:16183` and `api/openapi/openapi.json:16184` contain tabs; normalize whitespace only.
- `BadGateway` and `GatewayTimeout` are unused (`api/openapi/openapi.json:12166`, `api/openapi/openapi.json:12176`). Model Settings uses `ModelSettingsTestProblemNoStore`, while `check.mjs:2127` merely preserves the dead components. Remove the dead components and that obsolete assertion unless an operation is deliberately changed to reference them.
- `HealthScanScopeType` is also unreferenced (`api/openapi/openapi.json:24935`) and has no repository references outside the OpenAPI file. Remove it or wire it into the intended schema.

The Example warning is not a wire defect. `DocumentKnowledgeProfileContent` has a legitimate property named `examples` (`api/openapi/openapi.json:14467`). With resolved references, the stock rule recursively interprets that Schema property as an OpenAPI Examples Object. Re-declaring the same rule with its stock `given`/`then` clauses and `resolved: false` removes only this false positive and preserves validation of actual Example Objects.

The one operation without a success response is deliberately unavailable: `createHealthRepairProposal` documents only `400` and `503` because no executable repair Proposal owner exists (`api/openapi/openapi.json:5484`). Do not add a fake 2xx response. Disable the stock rule and replace it with a project rule/check that requires a success response for every operation except this explicitly marked reserved operation, whose exact `400/503` matrix must be locked.

### Descriptions, summaries, tags, and examples

- `description`: 104/189 operations have one; 85 are missing.
- `summary`: 0/189 operations have one. The recommended Spectral OAS ruleset does not flag this. Do not make summaries an initial gate unless the project intentionally adopts a separate documentation standard; operation IDs plus descriptions are sufficient for the first gate.
- `tags`: 0/189 operations have tags and there is no top-level tag catalog. This should be fixed, not suppressed, because TODO 8's generated client grouping will otherwise collapse into a default API surface. Use stable domain tags such as System, Auth, Model Settings, Workspaces, Capture, Authoring, Organizing, Git Sync, Workflows, Proposals, Search, Graph, Conversations, Events, Timeline, Exports, Artifacts, Review, Memory, and Health, with top-level descriptions.
- Examples: there are no OpenAPI Example Objects and no request/response examples; the only ordinary Schema `example` is `SubmitQuestionRequest.mode`. This is a documentation gap, not a structural or compatibility blocker. Add focused examples later for high-risk JSON commands; do not try to model raw SSE framing or binary downloads as ordinary JSON examples.

### Security readiness and response-matrix gaps

The declared authentication structure is coherent:

- Root security is `sessionCookie OR apiBearer` (`api/openapi/openapi.json:8`).
- Exactly three operations explicitly use `security: []`: liveness, readiness, and system status (`api/openapi/openapi.json:17`, `api/openapi/openapi.json:45`, `api/openapi/openapi.json:83`).
- Ten operations are Session-cookie-only and one Bootstrap-Bearer-only; 175 operations inherit root business authentication.
- The three security schemes correctly describe Cookie, API Token Bearer, and Bootstrap Bearer (`api/openapi/openapi.json:11854`).
- `check.mjs:373` through `check.mjs:437` correctly owns exact public/root/session/bootstrap and CSRF/Origin assertions. These must remain.

However, the response contract is incomplete relative to runtime Auth and HTTP behavior:

- 62 protected operations omit `401`.
- 61 protected operations omit `403`.
- 22 operations omit `405`: three Workflow control operations, eight Collection operations, and eleven Health operations.

The root security declaration does not automatically document these responses. Add the reusable Problem responses where runtime middleware can produce them, then enforce the policy generically. This should happen before taking the approved baseline, otherwise oasdiff will faithfully preserve an incomplete initial contract.

`x-required-capability` appears on 101 operations, while 74 inherited business operations lack it. Some omissions are explainable by default GET=`READ_LOCAL` behavior and five multi-capability mutation routes in `internal/auth/http/handler.go:170` and `internal/auth/http/handler.go:315`; the singular extension also cannot express an AND-set. Do not fill the current singular extension ad hoc. Either:

1. keep capability truth in the runtime Auth policy and project checker/tests, documenting the extension as partial; or
2. design an array-valued `x-required-capabilities` contract with explicit AND semantics, migrate every protected business operation, and add parity checks against Auth policy.

The second option is stronger but is a deliberate project contract change, not a standard Spectral rule.

### Nullability, unions, and discriminators

The document consistently uses OpenAPI 3.1 / JSON Schema forms and contains no legacy `nullable` keyword:

- 110 `oneOf`, 19 `anyOf`, and 41 `allOf` occurrences.
- 103 explicit `{ "type": "null" }` schemas.
- 64 type arrays that include `"null"`.
- 12 discriminators.
- 39 conditionals (`if`/`then`/`else` groups), three `unevaluatedProperties`, three `contains`, and one `prefixItems` use.

oasdiff explicitly recognizes the three common nullability forms, including a two-branch `$ref` plus `type: null` wrapper. Keep the current 3.1 forms.

Ten discriminators have explicit mappings. Two do not:

- `OrganizingAddMaterialRequest` (`api/openapi/openapi.json:31279`)
- `OrganizingMaterialSearchReference` (`api/openapi/openapi.json:31473`)

Their discriminator values (`SOURCE_VERSION`, `DOCUMENT_REVISION`, `CLAIM`, `SMART_COLLECTION`) do not equal the referenced Schema names, so implicit mapping is not sufficient for generator interoperability. Add explicit mappings before TODO 8 and lock them in the project checker. Do not add discriminators to every `oneOf`: null wrappers and validation-only branches do not need one.

### oasdiff compatibility and policy

Observed current release resolution was oasdiff v1.29.1. Important behavior verified against this document:

- Current file load fails on boolean `items: false`.
- After the three in-memory structural/interoperability fixes, `oasdiff breaking --fail-on WARN` can load the document and a same-file comparison returns no changes.
- All 562 references are local, so the gate should pass `--allow-external-refs=false`.
- Use `--fail-on WARN`, not only `ERR`; warnings are potential breaking changes and the project requires explicit review rather than silent acceptance.
- Do not enable `--flatten-allof` globally. This document has 42 `allOf` branches containing 3.1 conditionals/`contains`/related keywords, and oasdiff documents that flattening can drop such subschema keywords. Default comparison plus fail-on-WARN is the safer conservative policy here.
- Consider `--include-path-params` because path parameter names are part of the project's Gin/OpenAPI snake_case contract.

Do **not** add `oasdiff validate` to the gate for this document. After the load fix it reports four error groups because its validator compiles Schema patterns with Go RE2 and rejects valid ECMA-262 lookaheads and `\u` escapes, including `WorkflowMergeComparisonReview.default_target_path`, `DocumentHistoryPath`, and Unicode-control exclusions. Rewriting those patterns merely for RE2 would alter or weaken the OpenAPI contract. Spectral should own structural validation; oasdiff should own compatibility comparison.

oasdiff `breaking` also has important semantic blind spots confirmed with in-memory comparisons:

- Removing root security produced no breaking result because security checks are INFO by default.
- Removing `x-required-capability` or `x-error-codes` produced no breaking result.
- Replacing the semantic SSE response description produced no breaking result.
- Removing an optional download header was detected, but changing the exact `Cache-Control` const or `Content-Disposition` pattern was not.

Therefore the approved baseline is necessary but insufficient. Security check severities can be promoted in an oasdiff severity file, but exact Auth/capability/SSE/download behavior must remain project-owned.

Tool pinning caveat: `@stoplight/spectral-cli@6.16.3` declares `@stoplight/spectral-rulesets >=1`, so pinning only the CLI through `npx` is not deterministic. Lock both CLI 6.16.3 and rulesets 1.22.7 in a committed lockfile. oasdiff v1.29.1 declares Go 1.26, while CI currently installs Go 1.25.4; `go run` auto-downloaded Go 1.26.6 during the probe. The implementation must use a pinned/checksummed prebuilt artifact, a pinned container digest, or an explicitly aligned toolchain rather than an unpinned `go run ...@latest`.

### Project-specific assertions that must remain

Standard tools should not be used as justification to delete these contracts:

1. **Runtime route parity.** `internal/app/router_inventory_test.go:56` compares the full Gin runtime route set with all 189 OpenAPI operations and treats `/metrics` as the only optional route. This is stronger than Spectral/oasdiff.
2. **Exact Auth boundary.** Preserve the exact root OR security, three public operations, Session-only and Bootstrap-only operations, Origin/CSRF requirements, and fail-closed capability policy (`api/openapi/check.mjs:373`). oasdiff classifies security changes as INFO by default.
3. **Capability and project extensions.** Preserve `x-required-capability`, multi-capability runtime policy, `x-max-body-bytes`, UTF-8 byte bounds, `x-error-codes`, response-byte limits, and invariant extensions. Standard tools do not understand their semantics.
4. **Problem/error/status matrices.** Preserve domain-specific success/replay status, exact Problem schema, 405, idempotency, and no-body/204 rules. Spectral only checks that some success response exists.
5. **SSE.** Preserve both streams: transient Answer draft SSE (`api/openapi/openapi.json:3294`) and durable Server Events (`api/openapi/openapi.json:3360`). The current checker locks the durable stream's envelope, cursor, event-format precedence, cache behavior, and descriptive wire semantics (`api/openapi/check.mjs:3697`), but it lacks a corresponding exact assertion for Answer draft `generation:sequence`, chunk/reset/end order, heartbeat/reset/refetch semantics. Add that missing project assertion based on `.trellis/spec/backend/eino-runtime-adoption-gates.md:63`.
6. **Downloads.** Preserve exact media types, binary format, safe filename patterns, bounded Content-Length, private/no-store, nosniff, and 410 behavior (`api/openapi/check.mjs:2599`, `api/openapi/check.mjs:2779`). oasdiff did not flag header const/pattern changes in the probe.
7. **Tagged unions and state machines.** Preserve exact discriminators, branch constants, nullable lifecycle projections, response replay flags, and cross-field business invariants. Compatibility checking does not prove that the current document still matches server/domain truth.
8. **Reserved repair endpoint.** Preserve the intentional no-success `400/503` contract rather than weakening lint for every operation.

Safe candidates to remove or replace after the new gates pass:

- Raw source-string duplicate-key/marker checks can be replaced by Spectral parser duplicate-key validation; retain only ordering rules that have a proven runtime reason.
- Generic OpenAPI structural/ref/operation-ID checks can move to Spectral.
- The obsolete unused `BadGateway`/`GatewayTimeout` assertion at `check.mjs:2127` should leave with those dead components.
- Generic “has some success response” checking can move to Spectral plus the one project-aware reserved-operation exception.
- Do not delete exact response matrices, security/capability checks, custom-extension checks, or domain schema invariants merely because oasdiff has a baseline.

### Related specs

- TODO 7 explicitly requires pinned tools/rules/baseline, breaking-change failure, and continued Workspace/Auth/SSE checks (`docs/roadmap.md:78`).
- HTTP boundary makes OpenAPI the public method/path source of truth and requires exact runtime parity, Problem responses, and SSE/download behavior (`.trellis/spec/backend/http-boundary.md:18`, `.trellis/spec/backend/http-boundary.md:30`).
- Auth requires exact Cookie/Bearer precedence, Session-only management, CSRF/Origin, and stable 401/403 errors (`.trellis/spec/backend/auth-security.md:18`, `.trellis/spec/backend/auth-security.md:28`).
- Export requires exact content type, filename, length, private/no-store, and nosniff (`.trellis/spec/backend/export-contract.md:101`).
- Answer draft SSE requires `Last-Event-ID=generation:sequence`, chunk/reset/end, heartbeat, and generation isolation (`.trellis/spec/backend/eino-runtime-adoption-gates.md:63`).

One spec drift should be corrected during the later spec-update phase: `.trellis/spec/backend/http-boundary.md:18` says there are 183 operations, while the verified document and `internal/app/router_inventory_test.go:39` both say 189. Prefer removing the brittle prose count or updating it from the executable source.

### External references

- Spectral CLI 6.16.3 / OAS rulesets 1.22.7: [Spectral repository](https://github.com/stoplightio/spectral), [npm CLI package](https://www.npmjs.com/package/@stoplight/spectral-cli), [npm rulesets package](https://www.npmjs.com/package/@stoplight/spectral-rulesets).
- OpenAPI 3.1 Schema Object and default dialect: [OpenAPI Specification 3.1.0](https://spec.openapis.org/oas/v3.1.0.html#schema-object).
- oasdiff breaking checks, `--fail-on`, ignores, and severity customization: [Breaking Changes](https://github.com/oasdiff/oasdiff/blob/main/docs/BREAKING-CHANGES.md).
- oasdiff nullability equivalence: [Nullability Changes](https://github.com/oasdiff/oasdiff/blob/main/docs/NULLABILITY.md).
- oasdiff `allOf` flattening limitations for OpenAPI 3.1 keywords: [Merging AllOf Schemas](https://github.com/oasdiff/oasdiff/blob/main/docs/ALLOF.md).
- oasdiff extension tracking: [Diff](https://github.com/oasdiff/oasdiff/blob/main/docs/DIFF.md#openapi-extensions).

## Caveats / Not Found

- No approved oasdiff baseline, Spectral configuration, root API-tool lockfile, or installed Spectral/oasdiff binary exists under `api/openapi`; only `openapi.json` and `check.mjs` are present.
- The probe did not modify product code or dependencies. Structural fixes were applied only in memory for validation.
- The GitHub Releases API returned 403 in this environment, so no release timestamp was recorded. Version v1.29.1 was independently resolved by the Go module proxy/tool and observed in repository tags.
- No generator was run; generator-specific handling of the complex 3.1 conditionals and unions belongs to TODO 8. The two missing discriminator mappings are called out now because they are unambiguously incomplete under OpenAPI discriminator semantics.
