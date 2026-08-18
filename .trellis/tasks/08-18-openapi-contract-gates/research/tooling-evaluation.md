# Research: Spectral and oasdiff tooling evaluation

- Query: Evaluate current official Stoplight Spectral and oasdiff releases for TODO 7, including pinning, licenses, installation/runtime choices, OpenAPI 3.1 lint and breaking-change CLI semantics, approved-baseline handling, exits, ignores/deprecation, reproducibility/offline use, and ADR-0019 coverage.
- Scope: mixed
- Date: 2026-08-18

## Findings

### Executive recommendation

- **Verified fact:** The latest non-prerelease releases on the research date are Stoplight Spectral CLI `6.16.3` (published 2026-08-03) and oasdiff `1.29.1` (published 2026-08-16). Both are Apache-2.0. Sources: [Spectral v6.16.3 release](https://github.com/stoplightio/spectral/releases/tag/v6.16.3), [Spectral package manifest](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/cli/package.json), [oasdiff v1.29.1 release](https://github.com/oasdiff/oasdiff/releases/tag/v1.29.1), [oasdiff license](https://github.com/oasdiff/oasdiff/blob/v1.29.1/LICENSE).
- **Recommendation:** Pin exactly `@stoplight/spectral-cli@6.16.3` in a dedicated `api/openapi/package.json` plus npm lockfile, and run it with the repository's pinned npm/Node baseline. Do not use unpinned `npx`, global installation, `latest`, or a floating GitHub Action tag.
- **Recommendation:** Pin oasdiff `v1.29.1` as a checksummed release binary in a repository-local ignored tool cache. Do not add it to the project's `go.mod`: oasdiff `v1.29.1` declares Go 1.26 while this repository and CI declare Go 1.25.4. The official release publishes CGO-disabled binaries for the repository's macOS/arm64 developer path and Linux/amd64 CI path.
- **Recommendation:** Use the protected target branch (for PRs) or an immutable release tag/commit (for release gates) as the compatibility base. Do not commit a mutable copy of `openapi.json` that the same PR can overwrite, because that defeats the approval requirement.
- **Recommendation:** Run `oasdiff breaking` with `--fail-on WARN`; without `--fail-on`, reported breaking changes still exit 0. Keep `--allow-external-refs=false`. Use a local-only Spectral resolver or an equivalent network sandbox, because Spectral's default resolver follows `http`, `https`, and `file` references.
- **Decision required:** oasdiff's default stable and beta deprecation grace periods are both `0`. Under that default, a resource already marked `deprecated` may later be removed without a breaking result. TODO 7 must freeze a nonzero, product-approved grace period and `x-sunset` policy, or explicitly document that deprecated removal is an approved exception. The official documentation uses 180 days only as an example, not as a vendor default.

### Repository facts and impact boundary

- `api/openapi/openapi.json` is OpenAPI `3.1.0`, with 160 paths, 189 operations, 525 component schemas, 2,396 local `$ref`s, 41 `allOf`, 108 `oneOf`, and 19 `anyOf` occurrences. It has no external `$ref`, `$dynamicRef`, `$dynamicAnchor`, `components.pathItems`, callback, or webhook occurrence as of this research.
- All 189 operations have an `operationId`; none has tags, 104 have descriptions, and 85 do not. The root document has no `servers`, global `tags`, or `info.contact`. Extending the recommended Spectral OAS ruleset and immediately failing on warnings would therefore create known legacy findings (`operation-tags`, `operation-description`, `oas3-api-servers`, and `info-contact`) before any new regression is introduced.
- `api/openapi/check.mjs` is 4,178 lines. It JSON-parses the document and checks the exact OpenAPI version at lines 1-10, then combines route inventory, security/capability, response/error matrices, exact schema shapes, body/pagination limits, SSE, and domain-specific invariants.
- The authoritative runtime route parity test already belongs to Go: `.trellis/spec/backend/http-boundary.md:18-21` requires `internal/app/router_inventory_test.go` to compare the Gin runtime inventory against all OpenAPI operations. The narrow source-regex checks in `check.mjs` at lines 1689-1718 and 2460-2475 should not be generalized into Spectral rules.
- The current CI fixes Node `24.18.0`, npm via the web package at `11.7.0`, and Go `1.25.4` (`.github/workflows/ci.yml:31-55`). Only `web/package-lock.json` is cached today. A separate OpenAPI npm lockfile must be added to install/cache inputs if the recommended layout is adopted.
- `docs/roadmap.md:78-83` requires pinned tools/rules/baseline, explicit failure on breaking changes, approval for baseline changes, preservation of project-specific Workspace/Auth/SSE assertions, and identical local/CI results. `.trellis/spec/backend/quality-guidelines.md:30` already prohibits bypassing breaking checks by renaming.

### Version, license, and runtime matrix

| Tool | Verified stable pin | License | Runtime compatibility | Recommended delivery |
| --- | --- | --- | --- | --- |
| Spectral CLI | `6.16.3`, tag commit `8340cf73d62437b95edcf319899652340dd3cc65` | Apache-2.0 | Package supports Node `^16.20 || ^18.18 || >=20.17`; compatible with CI Node 24.18.0 | Exact devDependency plus npm lockfile under `api/openapi/` |
| oasdiff | `v1.29.1`, tag commit `2bb87bada404d350cb56e5504e8bd5d76f6159bf` | Apache-2.0 | Source module requires Go 1.26, so it is not compatible with the repository's fixed Go 1.25.4 source-tool path; release binaries are independent of the repository compiler | Checksummed native binary cached by version/OS/arch |

Additional verified pinning data:

- The npm registry metadata for `@stoplight/spectral-cli@6.16.3` publishes integrity `sha512-corAOQ/WhGoPJOQ3Tcipyn64TgKYDkEhsDplS6vNSGys3kzi9+zzxiVOlbzk9mWuafovzoW+TpGCoNwIZvFf5w==`. Its manifest includes ranged transitive dependencies, notably `@stoplight/spectral-rulesets: ">=1"`; therefore an exact top-level version without a lockfile does **not** freeze rule behavior. Sources: [npm package version](https://www.npmjs.com/package/@stoplight/spectral-cli/v/6.16.3), [tagged package manifest](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/cli/package.json#L36-L56).
- At the tag, `@stoplight/spectral-rulesets` is `1.22.7`. The generated npm lockfile, not a handwritten assertion, should be the transitive-version source of truth.
- The official oasdiff release checksum file contains:
  - macOS universal: `759cc5703d9335c441ad84a7074c705486b2c493f79bcfdf251c7a9c788b1171`
  - Linux amd64 tarball: `541f7c66c933495fceef24eaf5c48aa66c19069f366f7bd0a60a6a4820c5e533`
  - Linux arm64 tarball: `8bc247f0280f62ca73599265db0d984e853d7df6e714dad6ead85afc7cfc5883`
  Source: [v1.29.1 checksums](https://github.com/oasdiff/oasdiff/releases/download/v1.29.1/checksums.txt).
- The oasdiff release configuration sets `CGO_ENABLED=0` and publishes Darwin, Linux, and Windows amd64/arm64 artifacts. Source: [v1.29.1 GoReleaser config](https://github.com/oasdiff/oasdiff/blob/v1.29.1/.goreleaser.yml).
- A Docker alternative is available as `tufin/oasdiff:v1.29.1`; the multi-architecture manifest observed on 2026-08-18 is `sha256:bdba99e5e56558002952aa9a8aa2b91ab5f8e850f5981b5bb9bec732544ff721`. The GitHub repository has moved from `Tufin/oasdiff` to `oasdiff/oasdiff`, while the Docker image retains the historical `tufin` namespace. Source: [official Docker documentation](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/DOCKER.md), [Docker Hub tags](https://hub.docker.com/r/tufin/oasdiff/tags).
- Spectral also publishes npm, standalone, and Docker forms. The tagged standalone installer downloads a versioned binary but does not verify a checksum; the Docker image declares anonymous telemetry unless disabled. npm installation also includes Scarf install analytics, disabled with `SCARF_ANALYTICS=false`. Sources: [Spectral installation](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/getting-started/2-installation.md), [tagged install script](https://github.com/stoplightio/spectral/blob/v6.16.3/scripts/install.sh), [CLI analytics notice](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/cli/README.md#anonymized-analytics).

### Spectral OpenAPI 3.1 lint semantics

**Verified facts**

- Spectral is a generic JSON/YAML linter and requires a ruleset. `extends: [[spectral:oas, recommended]]` enables the recommended built-in OpenAPI rules and automatically detects OpenAPI 3.1. Source: [OpenAPI support](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/getting-started/4-openapi.md), [recommended/all semantics](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/4e-recommended.md).
- The tagged OAS ruleset explicitly registers `oas3_1`, validates the full OAS v3 document (`oas3-schema`), and includes operation ID uniqueness, path parameter correctness, schema/example, enum, security-scheme, response, and documentation/style rules. Sources: [tagged ruleset source](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/rulesets/src/oas/index.ts#L33-L137), [OAS3 schema rule](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/rulesets/src/oas/index.ts#L708-L728).
- `--fail-severity error|warn|info|hint` controls the failure threshold. Exit `1` means a lint result met the threshold; exit `2` means CLI/ruleset/runtime failure; otherwise exit `0`. `--display-only-failures` changes displayed findings, not the underlying lint result. Sources: [CLI guide](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/2-cli.md#error-results), [tagged CLI exit implementation](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/cli/src/commands/lint.ts#L224-L266).
- Rules can be promoted, demoted, or set to `off`; `overrides` can scope a rule exception to a file and JSON Pointer. Source: [rule severity overrides](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/4b-extends.md#change-rule-severity), [pointer overrides](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/4d-overrides.md).
- Spectral's default resolver supports `http`, `https`, and `file` references. The CLI's official `--resolver` extension point accepts a JS module exporting a custom resolver, so the project can omit the external protocol resolvers and fixture-test an internal-only policy. Sources: [resolver implementation](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/ref-resolver/src/index.ts#L14-L30), [custom resolver CLI docs](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/2-cli.md#custom-ref-resolving).

**Recommended exact invocation**

Create a dedicated exact dependency and lockfile, expose the command as an npm script, and use the same script locally and in CI:

```json
{
  "private": true,
  "packageManager": "npm@11.7.0",
  "engines": { "node": ">=24.18.0" },
  "scripts": {
    "lint": "spectral lint --ruleset .spectral.yaml --resolver ./local-only-resolver.cjs --fail-severity error --display-only-failures --format text openapi.json"
  },
  "devDependencies": {
    "@stoplight/spectral-cli": "6.16.3",
    "@stoplight/spectral-ref-resolver": "1.0.5"
  },
  "scarfSettings": { "enabled": false }
}
```

```bash
SCARF_ANALYTICS=false npm ci --prefix api/openapi
npm run lint --prefix api/openapi
```

- **Recommendation:** Start the gate at `--fail-severity error`, explicitly promote project-required quality rules to `error`, and record why legacy-inapplicable rules remain `warn` or `off`. After existing warnings are fixed or narrowly waived, ratchet to `--fail-severity warn` in a dedicated reviewed change.
- **Recommendation:** Do not create a snapshot of current Spectral findings. The committed ruleset is the policy and the OpenAPI document must pass it. Ruleset, resolver, manifest, and lockfile changes are approval surfaces.
- **Recommendation:** Add a local-only resolver through the official extension point and test it against `http:`, `https:`, `file:`, and valid internal `$ref` fixtures. This is a small security adapter, not duplicated lint logic.

### oasdiff breaking-change and baseline semantics

**Verified facts**

- Syntax is `oasdiff breaking base revision [flags]`; base is the old consumer contract and revision is the candidate. `breaking` reports `ERR` (definite) and `WARN` (potential) changes. Source: [breaking command documentation](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/BREAKING-CHANGES.md#breaking-changes-and-changelog), [command source](https://github.com/oasdiff/oasdiff/blob/v1.29.1/internal/breaking_changes.go).
- Merely finding breaking changes does not fail by default. `--fail-on ERR` fails only definite errors; `--fail-on WARN` fails both errors and warnings. Source: [preventing breaking changes](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/BREAKING-CHANGES.md#preventing-breaking-changes).
- A spec can be loaded directly from `<git-ref>:<path>`. The object must already exist locally unless opt-in `--fetch` is used; `--fetch` mutates the Git object store. CI with Git refs requires a full checkout (`fetch-depth: 0`). Source: [Git revision documentation](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/GIT-REVISION.md).
- OpenAPI 3.1 is generally supported from v1.15.0. Known 3.1 gaps are `$dynamicRef`/`$dynamicAnchor` resolution and `components.pathItems`; breaking checks also document no callback checks. None is present in the current repository spec. Source: [OpenAPI 3.1 support and caveats](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/OPENAPI-31.md), [breaking known limitations](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/BREAKING-CHANGES.md#known-limitations).
- `allOf` changes may be softened to `WARN` without `--flatten-allof`; a custom severity override disables that softening. v1.29.1 specifically fixes flattening at `$ref` use sites. Source: [severity behavior](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/BREAKING-CHANGES.md#when-oasdiff-reports-a-change-below-its-checks-level), [v1.29.1 release notes](https://github.com/oasdiff/oasdiff/releases/tag/v1.29.1).
- External references are followed by default. `--allow-external-refs=false` rejects them and uses exit 123, preventing SSRF on untrusted PR specs. Source: [oasdiff security guidance](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/SECURITY.md), [dedicated exit-code source](https://github.com/oasdiff/oasdiff/blob/v1.29.1/internal/errors.go#L112-L119).

**Recommended exact invocation**

```bash
oasdiff breaking \
  "origin/main:api/openapi/openapi.json" \
  api/openapi/openapi.json \
  --fail-on WARN \
  --format text \
  --color never \
  --allow-external-refs=false
```

Equivalent config-first form, preferred once committed:

```yaml
# api/openapi/.oasdiff.yaml
fail-on: WARN
format: text
color: never
lang: en
allow-external-refs: false
```

```bash
oasdiff breaking \
  --config api/openapi/.oasdiff.yaml \
  "origin/main:api/openapi/openapi.json" \
  api/openapi/openapi.json
```

- **Recommendation:** For a PR, substitute the protected target branch: `origin/${GITHUB_BASE_REF}:api/openapi/openapi.json`, and set checkout `fetch-depth: 0`. For a release gate, use an immutable approved release tag or commit SHA. For push CI, use the event's immutable `before` SHA where available.
- **Recommendation:** Do not use `--fetch` inside the gate. Populate refs during checkout so the comparison is read-only and can run offline once dependencies and Git objects are present.
- **Recommendation:** Do not use a checked-in `baseline/openapi.json` that a candidate PR can update alongside the API. If a snapshot is nevertheless required, add an independent gate that rejects snapshot changes unless an explicit protected approval workflow authorizes them; CODEOWNERS alone assigns reviewers but does not technically prevent a silent same-PR refresh.
- **Recommendation:** Leave `--flatten-allof` off for the first gate and fail on `WARN`, which remains conservative without transforming schemas. Enable flattening only after fixtures cover the repository's 41 `allOf` sites and the documented 3.1 flatten caveats.

### Exit codes

#### Spectral 6.16.3

| Code | Meaning |
| --- | --- |
| `0` | No finding met `--fail-severity` |
| `1` | At least one finding met `--fail-severity` |
| `2` | CLI, ruleset load/validation, parse, resolver, or runtime error |

#### oasdiff 1.29.1

| Code | Meaning |
| --- | --- |
| `0` | Command succeeded and no configured fail condition matched; this can include displayed changes if `--fail-on` is absent |
| `1` | `--fail-on` threshold matched (or `--fail-on-diff` for the `diff` command) |
| `100` | General command execution error without a more specific code |
| `101` | Invalid flags |
| `102` | Failed to load a spec |
| `103` | Spec glob matched no files / failed to load a composed glob |
| `104` | Diff calculation failed |
| `105` | Output rendering/printing failed |
| `106` | Custom severity-level file failed to load |
| `107` | Configuration file failed to load |
| `110` | Unsupported output format |
| `111` | Template used with an unsupported format |
| `114` | Invalid color mode |
| `121` | Ignore file could not be processed |
| `122` | `allOf` flatten failed |
| `123` | External `$ref` rejected by `--allow-external-refs=false` |

Sources: [oasdiff error-code definitions](https://github.com/oasdiff/oasdiff/blob/v1.29.1/internal/errors.go), [oasdiff run fallback](https://github.com/oasdiff/oasdiff/blob/v1.29.1/internal/run.go#L77-L86), [oasdiff validate exit table](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/VALIDATE.md#exit-codes).

### Ignore and deprecation handling

#### Spectral

- **Verified fact:** A rule can be changed to another severity or `off`; pointer-scoped overrides can suppress a rule for a precise part of the root document. Overrides do not apply to external dependency documents reached through `$ref`.
- **Recommendation:** Prefer rule-specific, pointer-scoped overrides with an inline reason and removal condition. Broad `off` is acceptable only for an intentional repository-wide policy such as the absence of `servers` for a same-origin API.
- **Recommendation:** Treat `.spectral.yaml` changes like public contract changes: owner review, no generated suppressions, and regression fixtures for every custom rule or resolver behavior.

#### oasdiff

- **Verified fact:** `--err-ignore` and `--warn-ignore` load text files. Each line matches a method/path (or `components`) plus localized change description; matching is lowercased and filtered before `--fail-on` decides the exit. Source: [ignore-file format](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/BREAKING-CHANGES.md#ignoring-specific-breaking-changes), [filter implementation](https://github.com/oasdiff/oasdiff/blob/v1.29.1/checker/ignore.go).
- **Verified fact:** `--severity-levels` can promote, demote, or disable (`none`) a check globally. Explicitly pinning a check's level also disables oasdiff's normal uncertainty-based softening for that check.
- **Recommendation:** Start with no ignore files and no `none` severity. If an intentional exception is unavoidable, pin `lang: en`, generate the exact single-line identity, record owner/reason/expiry beside it, and require approval for the ignore file. Never regenerate it from current output as a baseline.
- **Caveat:** Ignore matching is textual and version/localization sensitive; the open-source CLI does not provide an expiry or approval workflow for ignore entries. Hosted oasdiff review provides per-change approvals, but it introduces an external service/data path and is not needed to satisfy the local CLI requirement.
- **Verified fact:** Default beta/stable deprecation days are both 0. A previously deprecated operation, parameter, or property may then be removed without a breaking result. A nonzero grace period makes `x-sunset` mandatory and rejects early removal; supported deprecation resources are operations, parameters, and properties. Sources: [deprecation behavior](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/DEPRECATION.md), [default values](https://github.com/oasdiff/oasdiff/blob/v1.29.1/checker/config.go#L15-L18).
- **Repository fact:** The current spec has four deprecated schema properties and no deprecated operation. They do not include an `x-sunset` in the surrounding definitions. A new nonzero policy will not create a diff for unchanged declarations, but their future removal should be covered by an explicit fixture before relying on the gate.

### Reproducibility, offline use, and supply chain

- **Spectral recommendation:** Commit an npm lockfile generated with the repository's npm 11.7.0 baseline and install with `npm ci`, not `npm install`. Pin the CLI exactly. Set `SCARF_ANALYTICS=false`/`scarfSettings.enabled=false`. Include the new lockfile in `actions/setup-node` cache inputs.
- **Spectral offline caveat:** A lockfile freezes versions and integrity but does not contain package bytes. A truly offline environment needs a pre-populated npm cache, internal registry, or vendored package tarballs. Committing `node_modules` is not recommended.
- **Spectral Docker caveat:** `stoplight/spectral:6.16.3` was observed as Linux amd64 only, making it a poor default for the current macOS arm64 developer machine. If Docker is chosen, pin the manifest digest and disable telemetry; do not use `latest`, `6`, or `6.16`.
- **oasdiff recommendation:** Download the exact OS/architecture asset into a versioned ignored cache and verify its published SHA-256 before first execution. CI and local machines then run the same tagged implementation without changing the root Go toolchain.
- **oasdiff offline caveat:** Checksums do not supply bytes. Air-gapped use needs the two required release archives (Darwin universal and Linux amd64) mirrored or vendored. If Docker is preferred, pin the multi-architecture digest and pre-load/mirror the image.
- **Security recommendation:** Keep all OpenAPI and ruleset references local. Run Spectral with a local-only resolver; run oasdiff with external refs disabled. Add negative fixtures proving remote HTTP, metadata-address, parent-directory file, and unresolved external references fail before any network access.
- **Upgrade recommendation:** Tool upgrades must be dedicated reviewed changes that update the exact pin, npm lock/checksums, license inventory, and fixture expectations together. Re-run current-spec lint, no-change diff, additive diff, ERR breaking diff, WARN uncertainty diff, deprecated/sunset diff, external-ref rejection, and malformed-spec exit-code fixtures.

### ADR-0019 mature-framework assessment

Mandatory constraints are satisfied **only with the recommended adapters/configuration**, not with vendor defaults:

| Mandatory constraint | Assessment |
| --- | --- |
| OpenAPI 3.1 | Pass. Both tools explicitly support 3.1; current spec avoids their documented unsupported 3.1 constructs. |
| License | Pass. Both Apache-2.0. |
| Runtime/deployment | Pass with Spectral npm + lock and oasdiff release binary. Fail for oasdiff-as-source under the current Go 1.25.4 pin. |
| Security/privacy | Pass only with Spectral local-only resolver and oasdiff external refs disabled. Vendor defaults follow remote references. |
| Reproducibility | Pass with npm lock plus binary checksums/digests; fail with global installs, `npx@latest`, floating Actions, or mutable image tags. |
| Testability | Pass. Both expose deterministic CLI thresholds, structured output choices, and documented nonzero exits. |

Weighted coverage of the declared generic contract-gate requirements:

| Requirement | Weight | Covered | Evidence/remaining gap |
| --- | ---: | ---: | --- |
| OAS 3.1 structural/schema validation | 20 | 20 | Spectral `oas3-schema` and 3.1 format detection |
| Configurable lint/style policy and diagnostics | 10 | 10 | Spectral rules, severities, overrides, CLI formats |
| Backward-compatibility detection | 25 | 25 | oasdiff ERR/WARN catalog covers current spec feature set |
| Protected/immutable baseline inputs | 10 | 10 | oasdiff native Git-ref inputs; approval is supplied by protected Git process |
| Ignore/deprecation governance | 10 | 7 | Mechanisms exist; approval, expiry, and the grace-period value remain project policy |
| Reproducible local/CI and offline path | 15 | 12 | Pins/lock/checksums exist; air-gapped byte mirroring remains project infrastructure |
| Maintained releases, documentation, license, exit behavior | 10 | 10 | Recent stable releases, official docs/source/tests, Apache-2.0 |
| **Total** | **100** | **94** | Above ADR-0019's 80% threshold |

**ADR conclusion:** Adopt Spectral + oasdiff for generic lint/validation and compatibility comparison. Retain only thin project-owned policy/configuration and domain-specific assertions. Do not reimplement schema validation, operation ID/path consistency, or compatibility-diff algorithms in `check.mjs`.

The remaining project-owned boundary is legitimate and not framework duplication:

- Gin runtime inventory versus OpenAPI operation parity (`internal/app/router_inventory_test.go`).
- Exact public/authenticated operation classification, capability, CSRF/Origin, idempotency, error-code, body/response limit, cache, and SSE wire policies.
- Domain-specific Workspace, Evidence, Proposal, Workflow, Artifact, Review, Interview, Export, and other schema invariants that are stronger than generic OpenAPI validity or backward compatibility.
- Tool bootstrap, local-only resolver, protected Git baseline selection, and approval/upgrade policy.

### Suggested implementation/verification slices

1. Add exact Spectral manifest/lock, local-only resolver, reviewed `.spectral.yaml`, and deterministic lint fixtures; run the same npm script locally and in CI.
2. Add version/checksum metadata and repository-local oasdiff bootstrap; verify `oasdiff --version` reports `1.29.1` before comparison.
3. Add `openapi-breaking` using protected Git refs and full CI checkout; ensure no `--fetch` and no mutable same-PR snapshot.
4. Freeze deprecation grace, ignore, severity, and intentional-breaking approval policy before calling the compatibility gate complete.
5. Run characterization tests around `check.mjs`, then delete only assertions demonstrably covered by Spectral/oasdiff; retain runtime and domain assertions.
6. Keep `make openapi-check` as the stable aggregate entry point, with distinct `openapi-lint`, `openapi-breaking`, and `openapi-project-contract` subtargets for diagnosis.

Minimum fixtures should assert exact exits:

| Fixture | Expected |
| --- | --- |
| Current valid 3.1 spec | Spectral 0 |
| Duplicate operation ID / invalid path parameter | Spectral 1 |
| Broken ruleset or malformed spec | Spectral 2 |
| No-change and additive compatible revision | oasdiff 0 |
| Definite breaking change | oasdiff 1 with `--fail-on WARN` |
| Uncertain/allOf WARN change | oasdiff 1 with `--fail-on WARN` |
| Malformed base/revision | oasdiff 102 |
| External HTTP/file `$ref` | no network access; oasdiff 123 and Spectral nonzero through local-only resolver |
| Deprecated removal before approved sunset | oasdiff 1 after the nonzero grace policy is fixed |

## Files Found

- `docs/roadmap.md` - TODO 7 outcome, scope, approval, reproducibility, and CI acceptance requirements (`:78`).
- `docs/architecture/adr/0019-mature-framework-first.md` - mandatory constraints and 80% weighted mature-framework threshold (`:19`).
- `api/openapi/openapi.json` - current OpenAPI 3.1 wire source of truth.
- `api/openapi/check.mjs` - current 4,178-line combined project contract checker (`:1`, `:162`, `:373`, `:869`, `:1689`, `:2460`).
- `Makefile` - current `openapi-check` invokes only `node api/openapi/check.mjs` (`:199`).
- `.github/workflows/ci.yml` - current Node/Go pins, shallow checkout default, dependency install, and aggregate quality gate (`:31`).
- `.github/CODEOWNERS` - `/api/openapi/` ownership exists, but the file itself notes CODEOWNERS does not enforce multi-review (`:1`, `:38`).
- `.trellis/spec/backend/http-boundary.md` - OpenAPI/runtime inventory ownership and required `make openapi-check` gate (`:18`, `:66`).
- `.trellis/spec/backend/quality-guidelines.md` - public contract and breaking-check quality constraints (`:30`, `:73`, `:80`).
- `.trellis/spec/frontend/type-safety.md` - generated client and breaking-change expectations at the frontend/API boundary (`:13`).
- `web/package.json` / `web/package-lock.json` - existing npm 11.7.0/Node baseline; currently no Spectral dependency.
- `go.mod` - repository Go 1.25.4 baseline, incompatible with building oasdiff v1.29.1's Go 1.26 module as a project tool.

## Code Patterns

- `api/openapi/check.mjs:1-10` parses JSON and pins `openapi: 3.1.0`; structural OpenAPI validation is absent and should move to Spectral.
- `api/openapi/check.mjs:162-294` maintains a manual required-operation/success/405 list. oasdiff can own backward-removal/change detection, while Go inventory owns live implementation parity.
- `api/openapi/check.mjs:373-438` enforces exact public/auth schemes plus CSRF/Origin. These are project security contracts and must remain.
- `api/openapi/check.mjs:665-867` enumerates required domain schemas; baseline compatibility can catch deletion, but exact domain-presence/invariant requirements remain project policy.
- `api/openapi/check.mjs:869-935` locks success/error schemas and body limits; generic schema validity is tool-owned, while exact business matrices remain project-owned.
- `api/openapi/check.mjs:1689-1718` and `:2460-2475` regex-read selected Gin handler sources. The established reusable pattern is the complete runtime inventory test in `.trellis/spec/backend/http-boundary.md:18-21`, not further source regex expansion.
- `.github/workflows/ci.yml:31-45` currently uses shallow checkout defaults and caches only `web/package-lock.json`; Git-ref comparison requires `fetch-depth: 0` and the OpenAPI tooling lockfile in cache inputs.
- `Makefile:199-200` is the stable user-facing entry point to preserve while splitting internal lint/breaking/project checks.

## External References

### Spectral

- [v6.16.3 release](https://github.com/stoplightio/spectral/releases/tag/v6.16.3)
- [CLI package manifest and Node/license constraints](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/cli/package.json)
- [Installation options](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/getting-started/2-installation.md)
- [CLI flags and failure threshold](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/2-cli.md)
- [OpenAPI ruleset source](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/rulesets/src/oas/index.ts)
- [Ruleset recommended/all policy](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/4e-recommended.md)
- [Ruleset overrides](https://github.com/stoplightio/spectral/blob/v6.16.3/docs/guides/4d-overrides.md)
- [Default resolver protocols](https://github.com/stoplightio/spectral/blob/v6.16.3/packages/ref-resolver/src/index.ts)
- [Apache-2.0 license](https://github.com/stoplightio/spectral/blob/v6.16.3/LICENSE)

### oasdiff

- [v1.29.1 release](https://github.com/oasdiff/oasdiff/releases/tag/v1.29.1)
- [Getting started and installation options](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/README.md)
- [Breaking-change CLI, levels, fail-on, ignores, and customization](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/BREAKING-CHANGES.md)
- [Git revision baseline inputs](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/GIT-REVISION.md)
- [Configuration file precedence](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/CONFIG-FILES.md)
- [OpenAPI 3.1 support and caveats](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/OPENAPI-31.md)
- [External-reference security](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/SECURITY.md)
- [Deprecation and sunset handling](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/DEPRECATION.md)
- [Error/exit semantics](https://github.com/oasdiff/oasdiff/blob/v1.29.1/docs/ERRORS.md)
- [v1.29.1 release checksums](https://github.com/oasdiff/oasdiff/releases/download/v1.29.1/checksums.txt)
- [Apache-2.0 license](https://github.com/oasdiff/oasdiff/blob/v1.29.1/LICENSE)

## Related Specs

- `docs/architecture/adr/0019-mature-framework-first.md` - mature framework threshold, mandatory constraints, lock/license/review requirements.
- `docs/roadmap.md:78-83` - TODO 7 authoritative scope and acceptance.
- `.trellis/spec/backend/http-boundary.md:12-21` - OpenAPI as public method/path source and runtime inventory owner.
- `.trellis/spec/backend/quality-guidelines.md:26-42` - fail-closed public-contract and breaking-change expectations.
- `.trellis/spec/frontend/type-safety.md:11-24` - OpenAPI/generated client/breaking-change boundary that TODO 8 will consume.
- `.trellis/spec/guides/cross-layer-thinking-guide.md` - cross-layer contract ownership and verification guidance.

## Caveats / Not Found

- The current task PRD is still a template (`Goal`, `Requirements`, and `Acceptance Criteria` are `TBD`), so this research uses the authoritative TODO 7 roadmap text and project specs as scope. Planning must convert the decisions above into explicit acceptance criteria before implementation.
- No repository-wide API sunset/grace-period policy was found. This is the one material product-policy decision required before finalizing `.oasdiff.yaml`.
- No current protected-branch configuration is stored in the repository. CODEOWNERS assigns `/api/openapi/`, but actual required-review and admin-bypass controls must be verified in the Git hosting settings; repository files alone cannot prove approval enforcement.
- No Spectral or oasdiff execution was performed against the repository because the researcher role may write only inside this task's `research/` directory; installing either tool would write dependency/cache state elsewhere. The implementer must run the fixture matrix before removing any existing assertion.
- Spectral's built-in recommended rules will produce known legacy style findings on the current spec. The exact initial ruleset cannot be finalized solely from static inspection; run once, classify every finding, and record each deliberate severity/waiver without creating a findings snapshot.
- oasdiff's documented 3.1 gaps do not affect the current document, but adding `$dynamicRef`, `$dynamicAnchor`, `components.pathItems`, or callbacks requires reevaluation and a project-specific guard until upstream support is verified.
- oasdiff v1.29.1 was released two days before this research and the project has a rapid recent release cadence. Exact pinning plus upgrade fixtures is important; do not float to `stable`/`latest`.
