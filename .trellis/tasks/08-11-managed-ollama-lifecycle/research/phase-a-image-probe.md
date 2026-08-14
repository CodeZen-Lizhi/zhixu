# Research: Phase A Ollama image and protocol probe

- Query: Validate a pinned official Ollama image and the managed-runtime protocol surface in disposable resources only, including version/tags/show/pull, OpenAI-compatible Chat, native Embedding, non-root HOME, `OLLAMA_NOPRUNE=true`, and shutdown behavior.
- Scope: mixed (project contracts, official upstream sources, Docker registry metadata, local disposable runtime probe)
- Date: 2026-08-12

## Findings

### Decision and gate status

**Image/API sub-gate: conditional pass. Full Phase A: still blocked.**

The concrete candidate for continued disposable prototyping is:

```text
ollama/ollama:0.32.9@sha256:1685741456770df6e3cceb2a945a5f75e020f658d1701509668d6f4688f1dd3f
```

The digest is the immutable multi-platform OCI index. It resolved on 2026-08-12 to:

| Platform | Platform manifest digest |
|---|---|
| `linux/amd64` | `sha256:196a773990514116e5ecb5096b5a99ebb84cc0887e8658a51320ee12f091f33a` |
| `linux/arm64` | `sha256:22c95327f9fe0fcc1351ef3b73d679b21185b0d44548b45bf1a812915eef8e5d` |

Use the tag and index digest together in the dedicated runtime Dockerfile so the human-readable version and immutable content are both visible. Do not use `latest`, a tag without a digest, or an architecture-specific digest as the only cross-platform `FROM` reference.

The candidate passed the local arm64 image/API/security probe below. It is not yet a production approval because the following Phase A requirements remain unverified:

1. A disposable, read-only copy of a pre-existing `0.9.6` model store has not been tested against this candidate. This remains a migration blocker under `implement.md:11-18` and `design.md:275-287`.
2. The amd64 manifest exists, but the image has not been pulled or executed on amd64.
3. The actual manager/supervisor process has not been built or tested for direct loopback child binding, process groups, exactly-once `Wait`, stale generation fencing, crash recovery, and TERM-to-PGID-KILL escalation.
4. Docker Desktop and native Linux anchor-namespace-to-Compose-DNS reachability have not been tested; the local runtime was OrbStack on arm64.
5. The all-online manager-only 60-second RSS/cgroup gate cannot be measured before the manager binary exists. Direct `ollama serve` samples below are diagnostic only.

Therefore this result permits using the pin for the next isolated prototype, but it does **not** satisfy `implement.md:7-18` as a completed phase and must not be represented as permission to perform legacy migration or release the managed runtime.

### Host and image evidence

Local environment:

```text
host:        Darwin arm64
Docker:      client 29.4.0 darwin/arm64
Docker host: server 29.4.0, OrbStack Linux aarch64, 10 CPUs, 12,600,156,160 bytes RAM
```

An older official `ollama/ollama:0.9.6` image was already present locally at repo digest `sha256:f478761c18fea69b1624e095bce0f8aab06825d09ccabcd0f88828db0df185ce`, arm64 expanded size 3,616,575,805 bytes. Only image metadata and its version command were read; no pre-existing container or volume was used as an experiment target, mounted, stopped, or modified.

Official current-version evidence for the candidate:

- GitHub's official release page marked `v0.32.9` as Latest on 2026-08-12.
- Docker Hub's official tag record reported multi-platform digest `sha256:168574...dd3f`, updated `2026-08-11T21:38:29Z`.
- `docker buildx imagetools inspect ollama/ollama:0.32.9` returned the same index digest and both platform manifests.
- Pulling by tag plus index digest produced `/api/version = 0.32.9` on the local arm64 daemon.

Image shape observed after the digest-pinned pull:

| Property | Result |
|---|---|
| Local platform | `linux/arm64` |
| Local image ID | `sha256:769496c03de25e44a1029ada4c62ddf6435db3248820fd10dd57d8f8ef933b8c` |
| Registry compressed size | arm64 2,776,813,980 bytes; amd64 2,520,965,850 bytes |
| Local expanded size | 4,177,081,676 bytes |
| Base | Ubuntu 24.04.4 |
| Default user | unset, therefore root |
| Entrypoint / command | `[/bin/ollama] [serve]` |
| Default bind | `OLLAMA_HOST=0.0.0.0:11434` |
| Runner distribution | `/usr/lib/ollama`, including CPU and platform GPU runner libraries |

`docker history` attributed about 3.72 GB of the expanded image to `/usr/lib/ollama`. The official `v0.32.9` Dockerfile likewise assembles the per-platform runner tree at lines 260-293 and copies it into the Ubuntu final image at lines 295-319. This confirms `design.md:108-115`: use the official image as the final stage and copy the static manager into it; copying only `/bin/ollama` into the current Alpine runtime would omit required libraries and the compatible libc/runtime surface.

This is a disk-cost decision, not an idle-memory optimization. Image disk, model-volume disk, manager idle memory, `ollama serve` memory, and loaded-runner memory must remain separate metrics.

### Disposable runtime shape

All runtime resources created for the experiment used these exact names only:

```text
container: zhixu-probe-ollama-0329-20260812
init:      zhixu-probe-ollama-volume-init-0329-20260812
volume:    zhixu-probe-ollama-models-0329-20260812
```

The successful serve shape was:

```text
--init
--user 10001:10001
--read-only
--cap-drop ALL
--security-opt no-new-privileges
--tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m
HOME=/var/lib/zhixu/ollama
OLLAMA_MODELS=/var/lib/zhixu/ollama/.ollama/models
OLLAMA_NOPRUNE=true
OLLAMA_NO_CLOUD=true
OLLAMA_VULKAN=0
```

For probe access only, the container's `11434` was published to a Docker-selected port on host `127.0.0.1`. No LAN bind was used. This does not validate the final topology: production must publish no host port and must bind the child to a fixed container-loopback port behind the manager proxy (`design.md:91-98`).

Results:

- PID 1 was Docker init, with one non-root `/bin/ollama serve` child.
- `id` returned `uid=10001 gid=10001`.
- `$HOME/.ollama`, `models/`, generated identity files, manifests, and blobs were all written to the disposable model volume as UID/GID 10001.
- A write to `/` failed with `Read-only file system`.
- The runtime worked with zero Linux capabilities and `no-new-privileges`.
- No Docker socket, workspace, device, privileged mode, host bind, application secret, or database credential was present.

The official image defaults are not safe defaults for the managed service: it is root, uses `/root` as HOME, binds all interfaces, and carries NVIDIA-related environment entries. The dedicated image/Compose contract must override user, HOME, model path, host, and child environment explicitly.

### Child clean-environment result

The clean child environment was simulated with `/usr/bin/env -i ... /bin/ollama serve`. Reading the actual serve process's `/proc/<pid>/environ`, rather than a new `docker exec` process, showed only:

```text
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
HOME=/var/lib/zhixu/ollama
LD_LIBRARY_PATH=/usr/local/nvidia/lib:/usr/local/nvidia/lib64
OLLAMA_HOST=0.0.0.0:11434                 # probe-only bind
OLLAMA_MODELS=/var/lib/zhixu/ollama/.ollama/models
OLLAMA_NOPRUNE=true
OLLAMA_NO_CLOUD=true
OLLAMA_VULKAN=0
```

No host proxy variable, Docker-injected hostname, database credential, or model-settings secret reached the serve process. This validates the `exec.Cmd.Env = fixedAllowlist` design; using `append(os.Environ(), ...)` would violate the tested boundary (`design.md:110-115`).

Two refinements emerged:

1. Add fixed `OLLAMA_NO_CLOUD=true`. With its default `false`, the candidate logged that cloud was enabled and scheduled a model-recommendation cache task. With `true`, the log confirmed cloud disabled, while ordinary registry `/api/pull` still succeeded.
2. Add fixed `OLLAMA_VULKAN=0` for the CPU-only MVP with no device mounts. This removes ambient Vulkan discovery ambiguity. GPU support remains out of scope.

The probe retained upstream `LD_LIBRARY_PATH`; whether CPU-only execution needs it should be tested and then either frozen or removed. Do not broaden the environment without an observed requirement.

### Volume-init capability result

A new Docker volume mounted at `/var/lib/zhixu/ollama/.ollama` initially appeared as root-owned. A non-root serve cannot safely initialize that mount root by itself.

Capability experiments found:

| Init shape | Result |
|---|---|
| root + `cap_drop=ALL` | `chown` failed with `Operation not permitted` |
| root + `CHOWN` only | could not idempotently traverse/recover a partial prior init where the directory was mode `0700` and owned by UID 10001 |
| root + `CHOWN,DAC_OVERRIDE` | succeeded from the partial state and converged mount root/models/marker to UID/GID 10001 |

The ordering matters. The init process must temporarily establish ownership/access on the exact mount root, create/chmod the exact schema paths and marker, and transfer ownership to 10001 only as the final step. A partial run followed by a retry was successfully recovered with `cap_drop=ALL`, `cap_add=[CHOWN,DAC_OVERRIDE]`, `network=none`, one exact volume mount, and `no-new-privileges`.

The prototype used recursive `chown` only because the disposable tree was tiny. Production must not recursively scan a populated multi-gigabyte model store on every boot. Prefer a schema/ownership marker plus exact root/new-path operations; an unexpected populated tree with mismatched ownership should fail closed or enter the explicit migration flow.

This capability belongs only to the root one-shot init container. The long-lived manager/serve container passed with `cap_drop=ALL` and no additions. A read-only root filesystem for the init one-shot was not tested and remains part of the Compose hardening probe.

### API and protocol matrix

#### Empty model store

| Request | Observed result |
|---|---|
| `GET /api/version` | `200 application/json`, `{"version":"0.32.9"}` |
| `GET /api/tags` | `200 application/json`, `{"models":[]}` |
| `POST /api/show` missing model | `404`, native `{"error":...}` envelope; omitted tag normalized to `:latest` |
| `POST /v1/chat/completions` missing model | `404 application/json`, OpenAI-shaped nested `error` object |
| `POST /api/embed` missing model | `404`, native `{"error":...}` envelope |
| `GET /api/ps` | `200`, `{"models":[]}` |
| `DELETE /api/delete` missing model | route exists and returned model-not-found; reinforces that the manager proxy must deny this native path |

#### Streaming pull

For a nonexistent model, default streaming pull returned:

```text
HTTP 200
Content-Type: application/x-ndjson
Transfer-Encoding: chunked

{"status":"pulling manifest"}
{"error":"pull model manifest: file does not exist"}
```

The equivalent `stream=false` request returned HTTP 500 with a single JSON error. Therefore a streaming pull must not be marked successful from HTTP 200, the first status, or EOF alone. It is successful only after a parsed terminal `{"status":"success"}`; any parsed `error`, malformed/oversized line, premature EOF, cancellation, or response-read error is failure.

Real streaming pulls used two deliberately small official test models, selected only after reading registry manifest sizes:

| Model | Registry layer bytes | Local `/api/tags` size | Resolved manifest digest | Capability |
|---|---:|---:|---|---|
| `smollm2:135m` | 270,898,111 | 270,898,672 | `9077fe9d2ae1a4a41a868836b56b8163731a8fe16621397028c2c76f838c6907` | `completion` |
| `all-minilm:latest` | 45,960,589 | 45,960,996 | `1b226e2802dbb772b5fc32a58f103ca1804ef7501331012de126ab22f67475ef` | `embedding` |

For both models, the local `/api/tags` digest exactly equaled the SHA-256 of the official registry manifest bytes. `/api/show` returned the expected family, parameter count, and capability. The total disposable model volume was about 316.9 MB; no production-sized model was downloaded.

Progress frames were duplicated and some frames omitted `completed`. The manager parser must:

- enforce an NDJSON line and total-response bound;
- require stable, valid digest/total fields when present;
- treat progress as `max(previous, completed)` rather than require every frame to advance;
- reject a decreasing or over-total normalized result;
- wait for terminal success, then re-read `/api/tags` and `/api/show` serially.

An intentional early concurrent `tags/show` read observed the pre-pull empty state even though the pull later completed. This confirms the design order in `design.md:186-193`: pull terminal success first, then exact local verification. HTTP 200 on pull is not a readiness barrier.

#### Chat

`POST /v1/chat/completions` with `smollm2:135m`, an OpenAI-compatible `messages` array, `stream=false`, and no Authorization header returned:

```json
{
  "object": "chat.completion",
  "model": "smollm2:135m",
  "choices": [{"message": {"role": "assistant"}, "finish_reason": "length"}],
  "usage": {"prompt_tokens": 32, "completion_tokens": 4, "total_tokens": 36}
}
```

The first cold call completed with HTTP 200 after loading `/usr/lib/ollama/llama-server`; a warm bounded call returned the response above. This matches the project's fixed Chat path and JSON response expectations in `internal/platform/models/chat_http.go:53-56` and `internal/platform/models/chat_http.go:269-305`.

#### Embedding

`POST /api/embed` used the project's production request shape: a two-element `input` array and `truncate=false`. It returned HTTP 200 with:

```text
model:            all-minilm:latest
embedding_count:  2
dimensions:       [384, 384]
nonzero_counts:   [384, 384]
error:            null
```

This matches `internal/platform/models/embedding_ollama.go:34-41` and `internal/platform/models/embedding_ollama.go:61-69`, including model echo and result cardinality.

### Runner and shutdown evidence

After Chat and Embedding probes, one `ollama serve` parent owned two runner children:

```text
/usr/lib/ollama/llama-server ... smollm2 blob ... --offline ...
/usr/lib/ollama/llama-server ... all-minilm blob ... --offline --embedding ...
```

`/api/ps` listed both model digests. Docker stats showed about 457.4 MiB total and cgroup v2 reported:

```text
memory.current = 527,294,464
anon           = 468,725,760
file           = 57,913,344
```

`docker stop --timeout 15` sent SIGTERM through Docker init while both runners were loaded. The container exited in about 0.5 seconds with exit code 0, `OOMKilled=false`, and `Dead=false`. `docker top` then had no process, and the host process list had no remaining probe `ollama serve` or `llama-server` process.

This validates candidate Ollama's direct SIGTERM shutdown with loaded CPU runners. It does not validate the future manager's TERM/Wait/PGID-KILL implementation or crash paths; those remain a full Phase A blocker.

### `OLLAMA_NOPRUNE` and restart evidence

The same disposable container was restarted against the same volume with `OLLAMA_NOPRUNE=true` and clean environment. On restart:

- `/api/version` remained `0.32.9`.
- `/api/tags` immediately returned both models with the same sizes and resolved digests.
- `/api/ps` returned `models=[]`; no runner was loaded merely because a model existed.
- the process tree contained only Docker init and `ollama serve`.
- startup logs contained `model list cache hydration complete models=2` and no pull/prune/delete event.

This is evidence that a verified local `latest` copy remains bound to its local resolved digest across serve restart and is not background-repulled. The lifecycle layer should persist requested ref plus resolved digest/size and only pull when the exact local model is missing, as required by `design.md:128-135`.

The restart's single diagnostic sample was about 40.13 MiB Docker total, `memory.current=50,745,344`, and `anon=12,505,088`, with no runner. This is not the required manager-only 60-second memory gate and does not justify keeping `ollama serve` running when demand is empty.

### Cleanup evidence

After the probe:

- both exact probe containers were stopped and removed;
- the exact probe volume, including both downloaded test models, was removed;
- the newly pulled `0.32.9` image reference and its unshared layers were removed;
- two exact `/tmp/zhixu-probe-pull-chat.*` capture files were unlinked;
- `docker ps -a --filter name='^/zhixu-probe-'` returned no container;
- `docker volume ls --filter name='^zhixu-probe-'` returned no volume;
- exact `docker inspect` calls returned `No such container`, `No such volume`, and `No such image` for the candidate probe resources.

No prune, broad deletion, Compose down, daemon restart, or pre-existing container/volume operation was used.

### Reproducible command skeleton

The following is the reduced command shape; use a fresh unique `zhixu-probe-*` suffix and exact cleanup targets. It intentionally omits the production-sized models and all legacy resources.

```bash
PIN='ollama/ollama:0.32.9@sha256:1685741456770df6e3cceb2a945a5f75e020f658d1701509668d6f4688f1dd3f'
docker buildx imagetools inspect ollama/ollama:0.32.9
docker pull "$PIN"

# Root one-shot: network none, exact empty/probe volume only.
docker run --name <probe-init> --network none \
  --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE \
  --security-opt no-new-privileges --user 0:0 \
  -v <probe-volume>:/var/lib/zhixu/ollama/.ollama \
  --entrypoint /bin/sh "$PIN" -c '<fixed init program>'

# Runtime: the final product must replace the probe host publication with a
# container-loopback child port behind the manager proxy.
docker run -d --name <probe-runtime> --init --read-only \
  --cap-drop ALL --security-opt no-new-privileges --user 10001:10001 \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
  --mount type=volume,source=<probe-volume>,target=/var/lib/zhixu/ollama/.ollama \
  -p 127.0.0.1::11434 --entrypoint /usr/bin/env "$PIN" -i \
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
  HOME=/var/lib/zhixu/ollama \
  OLLAMA_HOST=0.0.0.0:11434 \
  OLLAMA_MODELS=/var/lib/zhixu/ollama/.ollama/models \
  OLLAMA_NOPRUNE=true OLLAMA_NO_CLOUD=true OLLAMA_VULKAN=0 \
  /bin/ollama serve

curl http://127.0.0.1:<resolved-port>/api/version
curl http://127.0.0.1:<resolved-port>/api/tags
curl http://127.0.0.1:<resolved-port>/api/show \
  -H 'Content-Type: application/json' -d '{"model":"<model>"}'
curl --no-buffer http://127.0.0.1:<resolved-port>/api/pull \
  -H 'Content-Type: application/json' -d '{"model":"<small-model>","stream":true}'
curl http://127.0.0.1:<resolved-port>/v1/chat/completions \
  -H 'Content-Type: application/json' -d '<bounded chat payload>'
curl http://127.0.0.1:<resolved-port>/api/embed \
  -H 'Content-Type: application/json' -d '<bounded embedding payload>'
docker stop --timeout 15 <probe-runtime>
```

Do not copy the probe's `OLLAMA_HOST=0.0.0.0` into production. It was required only for Docker host-port probe access; the managed child contract remains loopback-only.

## Files Found

| Path | Description |
|---|---|
| `.trellis/tasks/08-11-managed-ollama-lifecycle/prd.md` | Product goal, three-layer runtime shape, no-Docker-socket boundary, memory/data-retention requirements. |
| `.trellis/tasks/08-11-managed-ollama-lifecycle/design.md` | Non-root HOME, official-image final stage, clean child env, pull/verify order, model digest binding, shutdown and migration gates. |
| `.trellis/tasks/08-11-managed-ollama-lifecycle/implement.md` | Phase A ordered probes and explicit blockers (`implement.md:7-18`). |
| `.trellis/spec/backend/model-settings-runtime.md` | Managed/static model settings, fixed relay, no-restart activation and no-socket contracts (`model-settings-runtime.md:27-72`). |
| `.trellis/spec/backend/workspace-root-grant.md` | Stable namespace, exact mount and launcher ownership constraints (`workspace-root-grant.md:39-82`). |
| `.trellis/spec/backend/quality-guidelines.md` | Fixed command, secret, idempotency and validation expectations. |
| `deploy/Dockerfile` | Current Alpine application runtime; not a compatible replacement for the official Ollama final distribution (`deploy/Dockerfile:31-50`). |
| `deploy/compose.yml` | Existing non-root app/worker and loopback relay topology (`deploy/compose.yml:81-138`, `deploy/compose.yml:140-215`). |
| `internal/platform/models/chat_http.go` | Production Chat Completions path, JSON media type and bounded response contract (`chat_http.go:53-56`, `chat_http.go:269-305`). |
| `internal/platform/models/embedding_ollama.go` | Native `/api/embed`, `truncate=false`, model/cardinality checks (`embedding_ollama.go:29-69`). |
| `internal/modelsettings/runtime/models.go` | Production Chat/Embedding connection probes (`models.go:233-307`). |

## Code Patterns

- `design.md:91-98`: only the manager proxy is reachable on the Compose network; the Ollama child must be loopback-only and un-published.
- `design.md:100-116`: non-root manager/child, official pinned final image, Docker init, fixed child environment, one-shot volume init, and separate manager liveness vs child readiness.
- `design.md:128-135`: canonical requested refs must be bound to locally resolved digest/size; a local `latest` must not background-drift.
- `design.md:178-193`: health before model inspection, serial pull, terminal verification, and no automatic prune.
- `design.md:203-218`: stop is fenced by exact demand/holds and should TERM the serve PID before a bounded process-group KILL fallback.
- `design.md:267-287`: managed volume ownership and legacy compatibility require an isolated copy; the original data is never the upgrade test target.
- `implement.md:7-18`: image/API, old-store compatibility, supervisor, multi-platform security, network, and memory are one Phase A gate; passing only the API subset is insufficient.
- `internal/platform/models/chat_http.go:287-305`: production Chat sends JSON and requires a bounded `application/json` success response.
- `internal/platform/models/embedding_ollama.go:61-69`: production Embedding sends batch input with `truncate=false` and validates model echo/cardinality.

## External References

- Official Ollama `v0.32.9` release (marked Latest during probe): <https://github.com/ollama/ollama/releases/tag/v0.32.9>
- Official Docker Hub tag metadata and multi-platform image records: <https://hub.docker.com/v2/repositories/ollama/ollama/tags/0.32.9>
- Official `v0.32.9` Dockerfile, including runner assembly and Ubuntu final image: <https://github.com/ollama/ollama/blob/v0.32.9/Dockerfile>
- Official Docker usage and model-volume documentation: <https://github.com/ollama/ollama/blob/v0.32.9/docs/docker.mdx>
- Official API reference for tags/show/pull/embed/version: <https://github.com/ollama/ollama/blob/v0.32.9/docs/api.md>
- Official OpenAI compatibility reference for `/v1/chat/completions`: <https://github.com/ollama/ollama/blob/v0.32.9/docs/api/openai-compatibility.mdx>
- Official environment source for HOME-derived models, `OLLAMA_HOST`, `OLLAMA_NOPRUNE`, and `OLLAMA_NO_CLOUD`: <https://github.com/ollama/ollama/blob/v0.32.9/envconfig/config.go>
- Official model registry manifests used only for the two small disposable probes: <https://registry.ollama.ai/v2/library/smollm2/manifests/135m>, <https://registry.ollama.ai/v2/library/all-minilm/manifests/latest>

## Related Specs

- `.trellis/spec/backend/model-settings-runtime.md:7-11`, `:27-72`: managed scope, exact local relay, no Docker socket, and no application-container restart.
- `.trellis/spec/backend/workspace-root-grant.md:45-49`, `:64-82`: non-root exact mounts, stable namespace ownership, fail-closed lifecycle and down/reset ordering.
- `.trellis/spec/backend/quality-guidelines.md`: fixed commands, bounded inputs/outputs, idempotent external effects, secret non-propagation and focused verification.
- `docs/architecture/adr/0019-mature-framework-first.md`: use the complete official image/runtime distribution rather than reimplementing or copying a single binary without its runner dependencies.

## Caveats / Not Found

1. **Blocking: old-store compatibility.** No pre-existing model container or volume was touched, by instruction. Consequently the required read-only disposable copy test from `0.9.6` to `0.32.9` is absent. Phase F migration and full Phase A remain blocked until that separately authorized experiment passes tags/digest/size plus production Chat/Embedding probes and rollback.
2. **Blocking: amd64 runtime.** The official amd64 manifest was resolved, but non-root startup, runner execution, read-only rootfs, and shutdown were only executed on arm64.
3. **Blocking: custom supervisor.** Docker init directly supervised Ollama for this probe. Manager process-group creation, singleflight Ensure, exact `Wait`, stale-generation fence, child crash, signal escalation, cancellation during pull, and zombie/orphan checks were not implemented or tested.
4. **Blocking: final network topology.** The candidate was host-probed through a loopback-only random published port. Candidate `OLLAMA_HOST=127.0.0.1:<child-port>`, manager proxy enforcement, Compose DNS alias, anchor namespace relay, no-host-port shape, Docker Desktop and native Linux were not runtime-tested.
5. **Blocking: manager idle budget.** There is no manager binary, so the required all-online 60-second manager-only process/cgroup anonymous RSS gate was not measurable. Direct-serve one-point values are not a substitute.
6. **Not tested:** read-only rootfs on the root volume-init one-shot; exact minimal `LD_LIBRARY_PATH`; cancellation/resume of a real partial pull; malformed/oversized NDJSON; concurrent same-model pull attachment; registry outage/backoff; full production Adapter binary against the candidate; GPU paths; large production models.
7. **Candidate freshness risk:** `0.32.9` was the newest stable release and had been published to Docker Hub less than one day before this probe. The immutable digest prevents drift, but it does not replace vulnerability review, amd64 validation, or a short soak. If those fail, choose and re-probe an older stable digest rather than silently moving the tag.
8. **Image cost:** the arm64 final image expanded to about 4.18 GB before any model data. This is expected from the complete runner distribution and must be disclosed as disk use even though idle/runtime memory is separately managed.
9. **Research-role isolation:** `prd.md`, `design.md`, `implement.md`, relevant specs, code, and prior task research were read. `implement.jsonl` was not loaded because the Trellis researcher role explicitly forbids reading implementation/check context JSONL; this is a context-audit caveat, not an image/API evidence gap.
