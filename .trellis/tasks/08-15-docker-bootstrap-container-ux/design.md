# Docker Bootstrap 与稳态 Compose 分离设计

## Architecture

`deploy/compose.yml` 成为 Docker Desktop 可见的稳态定义，只包含 PostgreSQL、`local-model-runtime`、API、Worker 和两个 model relay，以及它们共享的卷和外部网络。它不得声明一次性服务，也不得以 `service_completed_successfully` 依赖它们。

新增 `deploy/compose.bootstrap.yml`，只定义下列 launcher 专用服务：

- `model-settings-key-init`
- `migrate`
- `local-model-volume-init`
- `local-model-runtime-credential-init`
- `modelctl`

该文件不单独作为 Docker Desktop 项目启动。launcher 和需要完整准备的 smoke 以 `-f deploy/compose.yml -f deploy/compose.bootstrap.yml` 临时解析它，并始终用 `docker compose run --rm --no-deps` 调用一次性服务。

```text
./zhixu up / restart
  ├─ render and validate steady + bootstrap models
  ├─ start PostgreSQL (steady model)
  ├─ run --rm key init -> migrate -> credential init -> volume init (bootstrap model)
  ├─ start local-model-runtime (steady model)
  ├─ run --rm modelctl recover --stale (bootstrap model)
  └─ use exact Workspace grant to start app/worker/relays (steady model)

Docker Desktop: zhixu
  └─ postgres, local-model-runtime, app, worker, app-model-relay, worker-model-relay
```

## Boundaries And Contracts

- Bootstrap commands use the fixed `zhixu` project and the same environment/volume/network resolution as the steady model, but do not receive the Workspace grant override. No initializer, migration or model control service gains host bind access.
- The launcher is the sole authority for bootstrap ordering. Any non-zero bootstrap exit prevents the next step and prevents `local-model-runtime` / API / Worker from starting.
- `local-model-runtime` continues to wait for PostgreSQL health in the steady model. API and Worker retain their PostgreSQL health gate and PID 1 runtime wait; these are resilience gates, not evidence that bootstrap ran.
- Docker Desktop Restart project operates only on persisted steady containers and does not re-run migration or initializers. This matches the accepted UI contract; upgrades and recovery must use the launcher.
- `compose.static-models.yml` remains a smoke-only runtime override. It must no longer refer to bootstrap services; its static manager must not acquire managed credential mounts or database dependencies.

## Validation Design

- Split Compose validation into steady-model and bootstrap-model checks. The steady check requires no bootstrap services or completed-service dependencies. The bootstrap check validates every initializer's image, UID, capability set, network isolation, secret/credential boundaries, volume mounts and `modelctl` runtime shape.
- Render and validate both models in launcher validation and Make targets. Runtime/Workspace validation remains against the steady model; bootstrap validation uses the merged model.
- Update Python/Go/Bash contract fixtures so `compose.bootstrap.yml` is copied/rendered where launcher or Compose behavior is simulated. Contract tests assert bootstrap commands include both files and preserve the current run order.
- Update all Compose smoke wrappers so their `run --rm` steps receive the bootstrap file while their long-running `up` steps target only steady services.

## Compatibility And Rollback

- Existing images, project name, volume names, network name, anchors and service names for all long-running containers remain unchanged. No schema or data migration is introduced.
- The next `./zhixu up` or `restart` removes older exited bootstrap containers through the steady
  `up --remove-orphans`; `down`/`reset` retains the legacy service-name allowlist as a fallback cleanup path.
- Rollback is a code rollback to the previous Compose/launcher pair. It reintroduces the one-shot service declarations; persisted data volumes stay compatible.

## Risks

- Missing `-f deploy/compose.bootstrap.yml` in a bootstrap caller would fail with an unknown service or skip preparation. Central launcher wrappers and contract coverage must catch this.
- Removing `depends_on` could allow a direct Docker Desktop Start project to reach an unprepared runtime. This is intentionally unsupported and must be explicit in documentation.
- Test-only static Compose overlays previously reset bootstrap services. Their model and validation must be revised together to avoid invalid merge assumptions.
