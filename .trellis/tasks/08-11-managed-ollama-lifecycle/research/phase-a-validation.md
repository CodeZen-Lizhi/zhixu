# Phase A Validation Evidence (historical, superseded)

> This note records the earlier `0.14.0` disposable probe only. It is not
> release evidence and must not override the later `phase-a-image-probe.md`
> decision, which pins `0.32.9`. No production or legacy resource was touched.

Date: 2026-08-12. All commands used disposable resources prefixed with
`zhixu-phasea-`; no `zhixu-eino-live-*` container or volume was mounted,
stopped, modified, or removed.

## Historical Image Candidate

- Candidate used by this historical probe: `ollama/ollama:0.14.0`.
- Multi-architecture index digest:
  `sha256:4d1c3ff8badca57327c1eb35e1842f7a28f3578ef40c8488a58854e083f53a1b`.
- linux/amd64 manifest:
  `sha256:bad6b6e1799279dcc8d13e50ff6a6c18d7268de52865554c8d65e497b00a94b4`.
- linux/arm64 manifest:
  `sha256:05122b03f4f08cb098aab106515935bc5fff32f250970eb5120c74ebaa47b026`.
- The arm64 image was pulled and reported Ollama `0.14.0`. Its official
  image retains `/usr/bin/ollama`, `/usr/lib/ollama`, and the runner assets;
  the dedicated runtime image must therefore use this image as the final
  stage rather than copying only the Ollama executable.

The release was selected from registry-verifiable immutable metadata at the
time. It was later superseded by the pinned `0.32.9` candidate documented in
`phase-a-image-probe.md`; the old digest is retained only for provenance.

## Non-root Volume And Child Process

- A disposable named volume was initialized as root, then its managed home
  and model directory were chowned to UID/GID `10001:10001`.
- The official image successfully ran `/bin/ollama --version` as that UID.
- The same UID could write
  `/var/lib/zhixu/ollama/.ollama/models` after the one-shot initialization.
- `ollama serve` successfully bound only `127.0.0.1:11534` with:
  `HOME=/var/lib/zhixu/ollama`, the managed model path, and
  `OLLAMA_NOPRUNE=true`.
- Docker `--init` plus SIGTERM stopped the model-free server within the
  15-second grace and the container exited with code 0 without OOM.

The server generated an Ollama identity file in the disposable home on first
start. That is expected model-home data and remains inside the managed volume;
it is not a product credential exposed to app/worker.

## Remaining Historical Gates

- The exact four-model legacy `0.9.6` store must still be copied read-only into
  a disposable destination and opened by the target image before migration is
  enabled. The implementation must fail closed until that evidence exists.
- Chat, Embedding, tags/show/pull streaming, runner termination under an
  actually loaded model, relay DNS on Docker Desktop/Linux, and the 60-second
  idle RSS budget remain full Compose gates. Unit and model-free process probes
  cannot substitute for them.
- The newer `0.32.9` candidate still requires the read-only legacy-copy,
  amd64, supervisor, relay, and idle-RSS gates listed in the authoritative
  image-probe note. It must not test upgrades against the user's source volume.
