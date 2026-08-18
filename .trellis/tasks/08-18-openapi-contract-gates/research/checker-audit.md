# Research: OpenAPI checker audit

- Query: Audit `api/openapi/check.mjs`, `api/openapi/openapi.json`, `internal/app/router_inventory_test.go`, `Makefile`, and related CI/docs; classify existing checks by Spectral coverage, oasdiff coverage, project-owned invariants, and brittle assertions; propose the minimum migration boundary, risks, and verification commands.
- Scope: mixed
- Date: 2026-08-18

## Findings

### Executive conclusion

`api/openapi/check.mjs` cannot be replaced wholesale. It is a 4,178-line monolithic project contract test with 413 `throw new Error` sites, not a generic OpenAPI validator. Spectral should take ownership of syntax/OAS validation and generic quality rules; oasdiff should take ownership of relative client compatibility against an approved immutable Git revision; the repository must retain a smaller project checker for Workspace/Auth/Capability/SSE/idempotency/error-matrix/strict-decoder invariants.

The minimum safe migration is additive first:

1. Fix the current document/tool compatibility blockers listed below.
2. Add pinned Spectral and oasdiff gates while keeping the existing checker unchanged.
3. Add mutation fixtures proving each replacement catches the same failure class.
4. Only then delete covered assertions, beginning with 101 indentation-sensitive key markers, the semantically irrelevant path-order check, and Go-source route regexes already owned by runtime tests.

Deleting broad sections merely because oasdiff has many checks would lose security and strict-decoder protection. Verified mutations show that oasdiff `breaking --fail-on WARN` does **not** fail for removal of a global security alternative, changes to `x-required-capability`, or addition of an optional response field.

### Current inventory and measured shape

| Fact | Measured result | Evidence |
|---|---:|---|
| OpenAPI file | 32,208 lines; OpenAPI 3.1.0 | `api/openapi/openapi.json:1-6` |
| Runtime operations | 189 | `internal/app/router_inventory_test.go:39-64`; independently counted from `paths` |
| Component schemas | 525 | parsed `components.schemas` |
| Checker size | 4,178 lines | `api/openapi/check.mjs` |
| Explicit failure sites | 413 | count of `throw new Error` |
| Literal raw-source markers | 101 entries in 8 loops | `api/openapi/check.mjs:12-159` |
| Generic required-operation tuples | 124 | `api/openapi/check.mjs:162-294` |
| Explicit schema-existence entries | 199 | `api/openapi/check.mjs:665-868` |
| Order-sensitive `required.join(...)` source lines | 96 | static count; overlaps other categories |
| Order-sensitive `enum.join(...)` source lines | 99 | static count; overlaps other categories |
| `Object.keys(...)` source lines | 52 | static count; some sort first, many do not |
| Description substring checks | 11 occurrences | e.g. `api/openapi/check.mjs:1542-1544`, `3702-3711`, `3913-3915` |

The checker is organized mostly by feature ranges. Failure-site counts are useful for sizing, not as independent acceptance criteria:

| Range | Concern | `throw` sites |
|---|---|---:|
| 1-161 | parser/version/raw key markers | 9 |
| 162-935 | common HTTP/Auth/Graph/Timeline | 56 |
| 936-1370 | Workspace Analysis/Proposal Revision/Human Review | 41 |
| 1371-1674 | Capture/Authoring/Document History | 32 |
| 1675-2093 | Git Sync/Proposal/Auth/System | 51 |
| 2094-2908 | Model Settings/Export | 73 |
| 2909-3609 | Review/Artifact/Timeline Impact | 72 |
| 3610-4178 | Conversation/SSE/Memory/Interview/Organizing/Health | 79 |

`make openapi-check` currently runs only `node api/openapi/check.mjs` and passed during this audit (`Makefile:199-200`). `make test` includes that target (`Makefile:6`), and CI invokes `make test` (`.github/workflows/ci.yml:54-55`). There are no dedicated checker mutation fixtures or direct tests for `check.mjs` outside the script itself.

### Classification 1: generic OpenAPI checks Spectral should own

Spectral's built-in OpenAPI ruleset should own these generic concerns:

- JSON/YAML parse errors and duplicate keys. This supersedes the selected literal-marker duplicate defense at `api/openapi/check.mjs:12-159` and covers the whole document rather than 101 handpicked keys.
- OAS 3.1 document/schema validity, local `$ref` resolution, valid Path/Operation/Parameter/Response/Media Type shapes, path-parameter agreement, unique parameters, and valid security-scheme references.
- `operationId` presence/uniqueness, enum duplication, examples, response descriptions, success-response quality, unused components, and unsafe Markdown rules according to the committed policy.
- Stable, structured diagnostic paths and CI-native output instead of TypeErrors or hand-built error text.

Spectral does **not** replace the exact `openapi === "3.1.0"` policy at `api/openapi/check.mjs:9-11`: its OAS 3.1 format accepts the 3.1 family. If exact 3.1.0 remains a repository policy, retain it as a small custom rule/assertion.

#### Verified Spectral 6.16.3 baseline

A temporary exact-version run of `@stoplight/spectral-cli@6.16.3` with `extends: spectral:oas` produced 290 findings:

| Rule | Count | Interpretation |
|---|---:|---|
| `operation-tags` | 189 | every operation currently has no tags |
| `operation-description` | 85 | 104/189 operations have descriptions |
| parser tabs | 6 | two lines contain repeated tab indentation at `openapi.json:16183-16184` |
| `oas3-unused-component` | 3 | BadGateway, GatewayTimeout, HealthScanScopeType |
| `oas3-schema` | 2 | real invalid Media Type objects at `openapi.json:1752-1754` and `1770-1772`; both omit the `schema` member around `$ref` |
| `array-items` | 1 | `items: false` at `openapi.json:15981`; valid JSON Schema 2020-12 semantics but incompatible with this Spectral rule and oasdiff's loader |
| `info-contact` | 1 | root `info.contact` absent |
| `oas3-api-servers` | 1 | root `servers` absent |
| `oas3-examples-value-or-externalValue` | 1 | appears to be a rule/path interaction with the schema reached through a property named `examples` |
| `operation-success-response` | 1 | reserved `createHealthRepairProposal` intentionally declares only 400/503 at `openapi.json:5484-5512` |

Severity was 3 errors (two `oas3-schema`, one `array-items`) plus 287 warnings. Therefore:

- Do not enable `--fail-severity warn` blindly; it would turn known style debt into 287 blocking findings.
- Start with `--fail-severity error`, explicitly promote only approved quality rules, and keep every demotion/off rule documented with a reason and removal condition.
- Fix the two real Media Type errors before declaring lint green.
- Treat the reserved repair operation as an explicit policy exception or remove it from the public contract if an always-unavailable operation is no longer intended. Do not invent a fake 2xx response.
- Do not snapshot Spectral findings. The committed ruleset is the policy; otherwise new warnings can hide inside an ever-growing baseline.

### Classification 2: compatibility checks oasdiff should own

Against an immutable approved base, oasdiff is the right owner for standard compatibility changes involving:

- removed paths/operations/responses/media types/headers;
- request parameter/body/property requiredness, type, enum, bounds, nullability, composition and serialization changes;
- response property/type/enum/bounds/nullability/composition changes with request/response direction-aware semantics;
- component and security changes when their check severity is explicitly reviewed;
- deprecation and sunset policy once project values are configured.

This overlaps the generic existence and exact-shape parts of the following checker areas:

- the 124 required-operation tuples and required success/405 responses (`api/openapi/check.mjs:162-294`);
- operation-specific status/schema/media-type matrices throughout the file, e.g. Auth at `446-481`, Graph at `510-570`, Git Sync at `1721-1834`, Export at `2477-2611`, and Interview at `3882-3911`;
- many exact required/property/type/enum/bound checks after a reviewed baseline exists.

It is not safe to delete those blocks until mutation tests show the selected oasdiff version and severity policy fail for the exact change. Some oasdiff changes are INFO by default, including all examined security changes and additive response properties.

#### Verified oasdiff 1.29.1 behavior

The latest release on the research date exposes 514 changelog checks (219 ERR, 29 WARN, 266 INFO) and 106 validate checks. With a temporary semantically equivalent copy that removed the redundant `items: false`:

| Mutation | `breaking --fail-on WARN` |
|---|---|
| remove `GET /livez` path | failed: `api-path-removed-without-deprecation` |
| remove required `ActiveWorkspace.root_path` response property | failed: `response-required-property-removed` |
| remove global `apiBearer` security alternative | passed; only INFO in `changelog` |
| change `x-required-capability` from READ_LOCAL to WRITE_PROPOSAL | passed; absent from `breaking`/`changelog`; visible only in full `diff` |
| add optional `ActiveWorkspace.future_field` | passed; only INFO in `changelog` |
| reorder `required` or enum entries | no diff, which is correct because those arrays are sets semantically |

Use `--fail-on WARN`; without an explicit fail threshold oasdiff can report changes and still exit zero. Do not use full `diff --fail-on-diff` as the breaking gate: it would block documentation and compatible additions as well as project extensions. Extensions that carry security or resource policy remain project-checker inputs.

### Classification 3: project-specific assertions that must remain

The following are ZHIXU contracts, not generic OpenAPI validity or default client compatibility:

- Exact public/private/auth scheme matrix, including the three public health/status operations, bootstrap/session/API-token distinctions, and no accidental `security: []` on business APIs (`api/openapi/check.mjs:373-421`). oasdiff classifies tested security removal as INFO.
- Capability ownership through `x-required-capability`, especially Workspace/Auth/Git/Proposal mutations. oasdiff full diff sees extension changes but `breaking` does not classify them.
- CSRF and Origin parameter requirements on cookie-authenticated mutations (`api/openapi/check.mjs:423-437`).
- Workspace identity placement and exact Workspace-scoped cursor/header/query semantics (`api/openapi/check.mjs:295-368`, `3620-3627`).
- Idempotency-Key, exact replay status/schema, `replayed` constants, expected-version/CAS, no-body 204 semantics, and retry/response-loss contracts.
- Exact Problem response and `x-error-codes` matrices. A removed status may be a standard break, but the required domain error code set and status-to-code meaning are project-owned.
- Project extensions such as `x-max-body-bytes`, `x-max-response-bytes`, `x-max-utf8-bytes`, and bounded cursor/page/collection rules.
- Strict response whitelists and forbidden sensitive fields. The frontend currently uses strict decoders, so an optional response property addition can be a project break even when oasdiff calls it INFO. ActiveWorkspace's six-field bootstrap projection is the clearest example (`api/openapi/check.mjs:321-345`).
- Domain discriminated unions, exact status/state sets, provenance/hash bindings, proposal/approval ownership, immutable evidence, and forbidden writeback fields.
- SSE content type, Event envelope, Last-Event-ID/header-vs-query precedence, EventSource message mode, no-store and bounded cursor semantics (`api/openapi/check.mjs:3697-3717`, `3755-3758`).
- Runtime Router/OpenAPI parity. The correct owner already exists in Go (`internal/app/router_inventory_test.go:56-75`, `116-169`).

These assertions can be made smaller and more maintainable, but should not be deleted. Prefer a compact project checker using reusable structured helpers (`sameSet`, `exactObjectShape`, `requiredRef`, `problemStatuses`) plus behavior tests at the owning Go boundary.

### Classification 4: brittle assertions to replace or remove

#### Raw OpenAPI text markers

Eight loops require 101 exact indentation-sensitive strings (`api/openapi/check.mjs:12-159`). They fail on harmless formatting and cover only selected duplicate keys. Replace them with Spectral parser duplicate-key errors plus structured property/path lookup. Keep clear missing-schema messages through helpers if useful.

#### Serialization order

`api/openapi/check.mjs:316-320` requires the static `/workspaces/active` JSON key to appear before the dynamic path. JSON/OpenAPI object order has no contract meaning; the Gin runtime inventory is the relevant behavior. Delete this check.

The checker also frequently compares set-like arrays/maps by insertion order, for example ActiveWorkspace at `api/openapi/check.mjs:326-338`. `required`, `enum`, schema property names, response status names, and most `oneOf` membership comparisons should use sorted/set equality unless order is itself a documented semantic. Keep order only for constructs such as `prefixItems`, where position is meaningful.

#### Go source regexes

The checker reads three Go implementation files (`api/openapi/check.mjs:5-8`) and regex-parses:

- Git Sync route registration (`api/openapi/check.mjs:1689-1705`);
- Git Sync capability middleware registration (`api/openapi/check.mjs:1706-1719`);
- Export route registration (`api/openapi/check.mjs:2460-2475`).

The two route regexes duplicate the complete runtime route-set test and couple the gate to whitespace/helper names. Remove them after the targeted Go route test is part of the canonical OpenAPI target. Replace the capability regex with a Go behavior/inventory test that exercises the production middleware registration; capability is security-sensitive and must not be dropped.

Exact handler function names are internal implementation details, not OpenAPI contracts.

#### Prose substring checks

The 11 description `.includes(...)` checks freeze English wording rather than wire semantics. Examples include authoring response-loss replay (`1542-1544`), model API-key identity (`2409-2421`), Artifact publication (`3346-3350`), SSE precedence/mode (`3702-3711`), Search input semantics (`3768-3775`), and Interview replay/cursor wording (`3913-3915`, `3936-3945`).

Move machine-relevant facts into existing structured fields or narrowly named `x-zhixu-*` extensions and assert those values. Let Spectral require non-empty human descriptions. Rewording explanatory prose should not break the contract gate when the structured contract is unchanged.

### Existing runtime route test

`internal/app/router_inventory_test.go` is already the strongest route parity mechanism:

- it parses all OpenAPI methods into a set (`116-137`);
- it canonicalizes Gin paths and rejects duplicate runtime routes (`139-149`);
- it reports missing and extra routes (`152-169`);
- it separately permits only injected `/metrics` as the optional runtime route (`67-75`).

Keep this test. It covers all 189 operations, while `requiredOperations` in `check.mjs` lists only 124. The hardcoded count is an explicit review tripwire but duplicates the set comparison; it may remain if the team wants every operation addition to update a human-reviewed number.

### Current blockers before freezing the base

1. **Invalid Media Type objects:** 405 and 503 for `POST /api/v1/source-versions/{source_version_id}/ingestion-attempts` put `$ref` directly under `application/json` instead of under `schema` (`openapi.json:1749-1755`, `1767-1773`). Existing `check.mjs` passes this invalid OAS shape.
2. **Boolean Schema parser incompatibility:** `WorkflowMergeComparisonReview.categories` has `minItems: 4`, `maxItems: 4`, four `prefixItems`, and `items: false` (`openapi.json:15920-15981`). The final keyword is semantically redundant because `maxItems: 4` already forbids a fifth item. oasdiff v1.29.1 cannot load the document until it is removed; Spectral's `array-items` rule also rejects it.
3. **`--flatten-allof` is unusable:** on the normalized copy, oasdiff exits 122 with a const conflict. The spec intentionally uses const-discriminated `allOf`. Run without flattening and fail on WARN; WARN catches oasdiff's uncertainty downgrades.
4. **Do not use `oasdiff validate` as the OAS validator:** after the redundant boolean Schema was removed, `oasdiff validate` reported four regex errors because it compiles ECMAScript/OpenAPI patterns with Go RE2 and rejects lookahead/`\u` escapes. Spectral should own specification validity; oasdiff should own diffing.
5. **Legacy lint volume:** immediate fail-on-warning creates 287 existing warnings. Policy must be calibrated before CI becomes blocking.

Freeze the approved compatibility base only after the two real Media Type defects and the redundant `items: false` incompatibility are resolved. Otherwise the baseline blesses known invalid/tool-incompatible input.

### Minimum migration boundary

#### Phase A: land tools in parallel, delete nothing

- Pin Spectral 6.16.3 with a lockfile; do not use floating `latest`, a global install, or an unlocked `npx` path.
- Pin oasdiff v1.29.1 by release binary checksum or immutable Docker digest. Its source module requires Go 1.26 while this repository/CI uses Go 1.25.4, so do not add `go run ...@v1.29.1` to Makefile.
- Use one repository wrapper/Make target locally and in CI; no duplicated CI-only flags.
- Run Spectral with explicit ruleset, local-only `$ref` policy, and `--fail-severity error` initially.
- Run `oasdiff breaking <approved-base> api/openapi/openapi.json --fail-on WARN --allow-external-refs=false`, without `--flatten-allof`, `--open`, or auto-generated ignores.
- Keep `node api/openapi/check.mjs` and the Go route inventory test green.

Prefer an immutable protected-branch/release commit as `<approved-base>` rather than a mutable duplicate snapshot in the same PR. A same-PR snapshot can be overwritten to make a break disappear. Local reproduction should accept an explicit immutable SHA/ref.

#### Phase B: replace brittle ownership

- Remove the 101 raw key markers after Spectral duplicate-key coverage has a fixture.
- Delete the path-order check.
- Convert set-like `.join()`/`Object.keys()` comparisons to order-insensitive helpers.
- Remove the two Go route source regexes after `openapi-route-check` runs the existing production-router tests.
- Replace the Git Sync capability source regex with a Go behavior test.
- Replace description substrings with structured extensions/fields and keep descriptions as non-empty documentation.

#### Phase C: prune standard compatibility duplication

Delete an old assertion only when a mutation test proves one of the new gates fails for that mutation at the configured severity. Record the replacement rule/check ID beside the test. Start with path/operation/response removal and ordinary request/response schema compatibility; keep security, extensions, strict additive response shape, error-code matrices, and domain semantics in the project checker.

No task should attempt to rewrite all 413 failure sites in one pass. The rollback is simply to keep/re-enable the old project assertion while the new lint/breaking gates continue running.

### Required regression fixtures

At minimum, add deterministic fixtures/tests for:

| Mutation | Expected owner/result |
|---|---|
| duplicate JSON key | Spectral fails |
| invalid Media Type `$ref` placement | Spectral fails |
| unresolved local `$ref` or mismatched path parameter | Spectral fails |
| remove operation/success response/required response property | oasdiff fails |
| add required request property or narrow request enum/bounds | oasdiff fails |
| remove/change auth alternative | project check fails unless oasdiff security severity is explicitly promoted |
| change `x-required-capability`, `x-error-codes`, or byte-limit extension | project check fails |
| add optional field to an exact strict response | project check fails |
| remove CSRF/Origin/Idempotency-Key/Problem status | project check fails |
| alter SSE cursor precedence/no-store/message-mode data | project check fails |
| reorder `required`, enum, or object properties only | passes; prevents serialization-order regressions |
| runtime route missing/extra/duplicate | Go inventory test fails |
| base ref missing/unreadable or tool version wrong | gate fails closed |

### Makefile and CI shape

Preserve `openapi-check` as the canonical no-surprise entry point because README, operations docs, CONTRIBUTING, PR template and multiple smoke targets already call it.

Recommended target graph:

```text
openapi-lint          -> pinned Spectral + committed ruleset
openapi-project-check -> node api/openapi/check.mjs (later reduced)
openapi-route-check   -> targeted internal/app route inventory tests
openapi-check         -> lint + project-check + route-check
openapi-breaking      -> pinned oasdiff + explicit immutable base ref
openapi-contract-test -> negative/mutation fixtures for all four owners
```

`make test` can continue to include `openapi-check`. CI should additionally call `openapi-breaking` with the event's immutable base SHA/ref because ordinary local `make test` does not inherently know a comparison base. Fetch/resolve failure must be a hard error, not a skip.

### Verification commands

Existing baseline commands:

```bash
make openapi-check
GIN_MODE=test go test -count=1 -timeout 60s ./internal/app -run 'TestRouter(RoutesExactlyMatchOpenAPI|MetricsIsTheOnlyOptionalRuntimeRoute)'
```

Proposed canonical verification after implementation:

```bash
make openapi-lint
make openapi-project-check
make openapi-route-check
make openapi-breaking OPENAPI_BASE_REVISION=<approved-immutable-sha-or-ref>
make openapi-contract-test
make openapi-check
git diff --check
```

Before task completion, also run the dependency/license checks selected by the tooling research and the directly affected CI path. Do not describe `make openapi-check` as proving runtime route parity unless it actually invokes the Go inventory target.

### Files found

| Path | Description |
|---|---|
| `api/openapi/check.mjs` | Monolithic fail-closed project checker combining generic, compatibility, domain and source-regex checks. |
| `api/openapi/openapi.json` | OpenAPI 3.1.0 wire source of truth; 189 operations and 525 schemas. |
| `internal/app/router_inventory_test.go` | Complete production Gin/OpenAPI route-set equality test plus optional `/metrics` case. |
| `Makefile` | Canonical `openapi-check` target and `make test` aggregation. |
| `.github/workflows/ci.yml` | CI installs Node/Web dependencies and invokes `make test`; no explicit breaking base today. |
| `.github/CODEOWNERS` | `/api/` and `/api/openapi/` owner assignment; does not itself prove required review. |
| `.github/PULL_REQUEST_TEMPLATE.md` | API change/openapi-check checklist, but no approved-breaking-baseline workflow. |
| `CONTRIBUTING.md` | OpenAPI is a high-risk public-contract boundary and `openapi-check` is canonical. |
| `docs/roadmap.md` | TODO 7 source requirement and TODO 8 dependency ordering. |
| `docs/architecture/application-contracts.md` | Declares OpenAPI as the unique precise HTTP/SSE wire source. |
| `.trellis/spec/backend/http-boundary.md` | Requires runtime/OpenAPI exact route parity and lists route boundary gates. |
| `.trellis/spec/backend/quality-guidelines.md` | Prohibits untested public Schema changes and breaking-check bypasses. |
| `docs/architecture/adr/0019-mature-framework-first.md` | Requires mature tools for generic infrastructure and only a thin project-owned adapter. |

### Code patterns

- Canonical gate aggregation: `Makefile:4-6`, `199-200`.
- OAS parse/version plus brittle raw-marker pattern: `api/openapi/check.mjs:1-18`.
- Operation existence/status matrix: `api/openapi/check.mjs:162-294`.
- Exact Auth/CSRF contract: `api/openapi/check.mjs:373-437`.
- Runtime route set derived from the OpenAPI document rather than a duplicate list: `internal/app/router_inventory_test.go:116-169`.
- Fragile Go source regex inventory: `api/openapi/check.mjs:1689-1719`, `2460-2475`.
- Structured project extension checks: e.g. `x-required-capability` and body bounds at `api/openapi/check.mjs:3892-3929`.
- SSE project contract: `api/openapi/check.mjs:3697-3717`, `3755-3758`.

### External references

- Spectral v6.16.3 release: https://github.com/stoplightio/spectral/releases/tag/v6.16.3
- Spectral tagged OpenAPI ruleset: https://github.com/stoplightio/spectral/blob/v6.16.3/packages/rulesets/src/oas/index.ts
- Spectral OpenAPI rule reference: https://github.com/stoplightio/spectral/blob/v6.16.3/docs/reference/openapi-rules.md
- Spectral ruleset/parser configuration: https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/4-custom-rulesets.md
- Spectral CLI/failure severity: https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/2-cli.md
- Spectral Apache-2.0 license: https://github.com/stoplightio/spectral/blob/v6.16.3/LICENSE
- oasdiff v1.29.1 release: https://github.com/oasdiff/oasdiff/releases/tag/v1.29.1
- oasdiff breaking checks/failure thresholds/severity customization: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/BREAKING-CHANGES.md
- oasdiff full diff and extension tracking: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/DIFF.md
- oasdiff committed config behavior: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/CONFIG-FILES.md
- oasdiff Docker usage: https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/DOCKER.md
- oasdiff upstream Go 1.26 requirement: https://github.com/oasdiff/oasdiff/blob/v1.29.1/go.mod
- oasdiff v1.29.1 release checksums: https://github.com/oasdiff/oasdiff/releases/download/v1.29.1/checksums.txt
- oasdiff Apache-2.0 license: https://github.com/oasdiff/oasdiff/blob/v1.29.1/LICENSE

### Related specs

- `docs/roadmap.md:78-83` - pin versions/rules/base, fail on breaking changes, retain Workspace/Auth/SSE constraints, and expose local/CI commands.
- `docs/architecture/application-contracts.md:1-20` - OpenAPI is the unique precise wire source; strict input, Problem and bounded cursor semantics.
- `.trellis/spec/backend/http-boundary.md:12-21`, `41-54`, `66-83` - complete runtime/OpenAPI equality and required validation commands.
- `.trellis/spec/backend/quality-guidelines.md:25-50`, `53-76` - no untested public Schema changes, mature-tool-first boundary, and API contract gates.
- `.trellis/spec/guides/cross-layer-thinking-guide.md:19-52`, `62-101` - validation ownership and single payload-contract owner.
- `docs/architecture/adr/0019-mature-framework-first.md:19-49` - Spectral/oasdiff own mature generic behavior; custom code remains only for differentiated project invariants.

## Caveats / Not Found

- `.trellis/spec/backend/http-boundary.md:18-20` says the current operation count is 183, while `openapi.json` and `internal/app/router_inventory_test.go:39-64` now prove 189. This is existing spec drift and should be corrected through the task's later spec-update phase; this research role did not edit specs.
- Repository files do not prove GitHub branch protection, required status checks, required CODEOWNERS review, or administrator bypass auditing. An immutable Git base prevents same-PR snapshot overwrite, but approval remains unenforced if the external repository settings do not require the check/review.
- oasdiff's default security severities are INFO and its `breaking`/`changelog` commands do not classify project extension changes. A generic tool green result is not evidence that Auth/Capability contracts are unchanged.
- oasdiff v1.29.1 cannot parse the current `items: false`; `--flatten-allof` also fails on current const-discriminated schemas. These were verified with a temporary copy only; no production file was changed.
- Spectral's 290-finding baseline was measured with the official built-in ruleset and exact CLI version, but the final committed ruleset has not been designed yet. Counts will change when intentional policy overrides are applied.
- `check.mjs` has no direct mutation-test harness. Until such fixtures exist, retain structured assertions even when they appear to overlap oasdiff.
- This audit did not run the Go route tests because the researcher role is write-isolated and Go test/cache writes are outside its permitted output directory. The source and command were inspected; `make openapi-check` itself was executed and passed.
