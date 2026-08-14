# Runtime And Official Documentation Evidence

## Current Runtime Snapshot

Captured read-only on 2026-08-11. No container or setting was mutated.

- `ops.model_settings_state`: desired revision `6`, active revision `2`.
- Desired Chat and Embedding providers are both `openai-compatible`; neither desired Base URL is the fixed Ollama Relay.
- Active Chat and Embedding providers are both `disabled`; neither active Base URL is the fixed Ollama Relay.
- `zhixu-eino-live-ollama` is nevertheless running and reports approximately `711 MiB` memory usage. `/api/ps` returns no loaded models, while `docker top` attributes about `707 MiB` RSS to the idle `/bin/ollama serve` process; unloading model weights alone therefore does not solve the reported idle-memory cost.
- The container uses `ollama/ollama:0.9.6`, restart policy `no`, host bind `127.0.0.1:11434`, and volume `zhixu-eino-live-models` mounted at `/root/.ollama`.
- `/api/tags` reports four installed models: `qwen2.5:0.5b`, `qwen2.5:1.5b`, `qwen2.5:3b`, and `all-minilm:latest`.
- The container has no Compose project/service labels, so the current launcher cannot safely claim ownership from Docker metadata.

This confirms the reported resource leak shape: current settings do not need local Ollama, while an independently-created container remains running.

## Docker Compose Documentation

Source: Docker official documentation, `content/manuals/compose/how-tos/profiles.md`, retrieved through Context7 `/docker/docs`.

- A service assigned to a profile is ignored by ordinary `docker compose up` unless the profile is enabled.
- Explicitly targeting a profiled service starts that service without globally enabling the profile; only the target and declared dependencies are started.
- This supports a launcher-owned optional Ollama service without making the normal all-online stack start it.
- Compose profile selection alone does not observe PostgreSQL settings or react to an in-app hot activation. A trusted host-side reconciler is still required for settings-driven immediate lifecycle changes.

## Ollama Documentation

Source: Ollama official `docs/api.md` and `docs/faq.mdx`, retrieved through Context7 `/ollama/ollama`.

- `GET /api/tags` lists models installed on the local instance without loading them.
- `POST /api/show` returns model details and capabilities for a named model.
- `POST /api/pull` downloads a model and supports progress reporting.
- `GET /api/ps` lists models currently loaded in memory.
- `keep_alive: 0` unloads a model from memory, but the current idle daemon evidence shows that process termination is still required to reclaim the reported baseline memory. A design may keep a tiny supervisor alive, but it must stop the `ollama serve` child when no local target or lease needs it.

## Planning Implications

- Use an explicit profiled Compose service for the managed Ollama resource, but do not treat profiles as the state reconciler.
- Verify local model presence through `/api/tags` or `/api/show`; model download policy remains a user-owned product decision because pulls can consume substantial bandwidth and disk.
- Keep the model volume across normal stop/start transitions.
- Treat the current unlabeled legacy container and volume as migration input, not automatically trusted ownership.
