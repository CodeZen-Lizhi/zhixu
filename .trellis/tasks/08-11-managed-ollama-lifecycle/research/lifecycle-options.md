# Local Ollama Lifecycle Options

## Problem Restatement

The product outcome is not merely “hide a Docker row.” The current idle `ollama serve` process consumes about 707 MiB even with no loaded models. The required behavior is therefore to terminate the heavy Ollama process when no active or retiring model generation needs it, while preserving model data and keeping Settings-driven activation safe.

The existing architecture imposes three hard constraints:

1. Normal model Save/Apply is becoming an in-process hot activation and does not invoke `./zhixu` or restart containers.
2. API/Worker containers cannot receive Docker Socket or arbitrary host-command authority.
3. ADR-0020 deliberately removed the always-on HTTP Host Controller and its PID/log/session lifecycle from the normal page availability path.

## Option A: Tiny Always-On Supervisor In The Main Compose Project

Run one small, unprivileged `local-model-runtime` supervisor as part of the main `zhixu` project. The supervisor owns the model volume and launches/stops `ollama serve` as a child process. When all active/retiring generations are online or disabled, the supervisor stays idle but the Ollama child is absent.

The model activation service talks to a narrow internal contract owned by the supervisor; it cannot run Docker commands. The supervisor participates in preparation before a local target is probed and keeps Ollama alive until all old local generation leases retire.

Advantages:

- Settings-page Test and Save/Apply can prepare local models immediately.
- No Docker Socket, host command execution, or reintroduced Host Controller.
- The measured 707 MiB idle Ollama process is terminated; expected remaining supervisor memory is small and can be bounded/tested.
- The helper appears inside the existing `zhixu` Compose project rather than as a third standalone top-level application.
- Fits the hot-activation state machine and can expose durable preparation/status facts.

Costs:

- One small container/process remains running even in all-online mode.
- Requires a purpose-built narrow supervisor and internal lifecycle contract.
- The current host-loopback relay topology likely needs to target a managed internal endpoint rather than an unrelated host `11434` owner.

## Option B: Reintroduce A Narrow Host Lifecycle Agent

Start a native background agent from `./zhixu up`. It watches durable model-runtime intent in PostgreSQL, manages an optional profiled Compose Ollama service, and stops the entire container when unused.

Advantages:

- The Ollama container can truly be stopped/absent in all-online mode.
- Docker ownership remains on the host side rather than inside a container.

Costs:

- Reintroduces persistent host PID/log/recovery/liveness management that ADR-0020 intentionally removed.
- Requires a secure bridge between in-app Settings activation and the host agent, plus durable lease/recovery semantics.
- If the agent is unavailable, local Test/Apply cannot complete; cross-platform daemon installation and sleep/resume behavior expand scope substantially.
- Page availability can remain independent, but local-model availability gains a new host-side single point.

## Option C: Launcher-Only Compose Profile

Define Ollama behind a Compose profile and let `./zhixu up|restart` read current settings and explicitly start or stop it.

Advantages:

- Smallest change and reuses official Compose profile semantics.
- Entire Ollama container is absent/stopped when the launcher reconciles an all-online configuration.

Costs:

- Does not meet immediate Settings-page automation. Online -> local Test/Apply cannot prepare Ollama until the user runs a host command.
- Conflicts with the accepted hot-activation UX, which removes normal `./zhixu restart` from model setting application.

This is viable only if the product accepts “automatic on launcher commands,” not “automatic on Settings changes.”

## Option D: Docker-Socket Controller Container

Run a dedicated controller container with `/var/run/docker.sock` and expose a narrow API to the application.

This option is rejected for the current threat model. Possession of the Docker Socket is effectively host-level control; HTTP method filtering does not provide object-level ownership security. It also contradicts the existing Compose validation and exact-control boundary.

## Recommendation

Choose Option A if the user accepts a very small always-on supervisor container while requiring the heavy Ollama process and model memory to be absent in all-online mode. It is the only option that simultaneously supports immediate Settings-driven behavior, preserves the no-Docker-Socket rule, and avoids reintroducing a host daemon.

Choose Option B only if “no running helper container whatsoever” is a hard observable requirement worth the additional host-agent architecture and operational cost.

Do not implement Option C as a silent substitute for the requested automatic Settings behavior, and reject Option D.
