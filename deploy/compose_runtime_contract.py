#!/usr/bin/env python3
"""Exercise positive and fail-closed Compose runtime configuration contracts."""

from __future__ import annotations

import copy
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any, Callable


ROOT = Path(__file__).resolve().parent.parent
CHECKER = ROOT / "deploy" / "compose_runtime_check.py"
COMPOSE = ROOT / "deploy" / "compose.yml"
STATIC_MODELS = ROOT / "deploy" / "compose.static-models.yml"
ENV_FILE = ROOT / ".env.example"


def fail(message: str) -> None:
    print(f"[compose-runtime-contract] failed: {message}", file=sys.stderr)
    raise SystemExit(1)


def render(
    *files: Path,
    profiles: tuple[str, ...] = (),
    environment: dict[str, str] | None = None,
) -> dict[str, Any]:
    command = ["docker", "compose"]
    for profile in profiles:
        command.extend(["--profile", profile])
    command.extend(["--project-name", "zhixu"])
    for path in files:
        command.extend(["-f", str(path)])
    command.extend(["--env-file", str(ENV_FILE), "config", "--format", "json"])
    process_environment = os.environ.copy()
    if environment is not None:
        process_environment.update(environment)
    completed = subprocess.run(
        command,
        check=False,
        capture_output=True,
        text=True,
        env=process_environment,
    )
    if completed.returncode != 0:
        fail("Docker Compose could not render the contract fixture")
    try:
        model = json.loads(completed.stdout)
    except json.JSONDecodeError:
        fail("Docker Compose returned invalid JSON")
    if not isinstance(model, dict):
        fail("Docker Compose returned a non-object model")
    return model


def check(model: dict[str, Any], *, mode: str = "managed") -> subprocess.CompletedProcess[str]:
    command = [sys.executable, str(CHECKER)]
    if mode == "static":
        command.append("--static-models")
    elif mode == "legacy":
        command.append("--legacy-model-env")
    elif mode == "prepared":
        command.append("--prepared-candidate")
    return subprocess.run(
        command,
        input=json.dumps(model),
        check=False,
        capture_output=True,
        text=True,
    )


def expect_valid(model: dict[str, Any], *, mode: str = "managed") -> None:
    completed = check(model, mode=mode)
    if completed.returncode != 0:
        fail(f"valid {mode} model was rejected: {completed.stderr.strip()}")


def expect_invalid(
    name: str,
    model: dict[str, Any],
    mutate: Callable[[dict[str, Any]], None],
    *,
    mode: str = "managed",
) -> None:
    candidate = copy.deepcopy(model)
    mutate(candidate)
    if check(candidate, mode=mode).returncode == 0:
        fail(f"invalid Compose model was accepted: {name}")


def secret_mount(model: dict[str, Any], service_name: str) -> dict[str, Any]:
    mounts = model["services"][service_name]["volumes"]
    return next(mount for mount in mounts if mount.get("source") == "zhixu-model-secrets")


def main() -> None:
    managed = render(COMPOSE, profiles=("workspace-runtime", "modelctl"))
    custom_ports = render(
        COMPOSE,
        profiles=("workspace-runtime", "modelctl"),
        environment={"ZHIXU_HTTP_PORT": "18080"},
    )
    static = render(COMPOSE, STATIC_MODELS, profiles=("workspace-runtime",))
    prepared = render(
        COMPOSE,
        profiles=("workspace-runtime", "modelctl"),
        environment={
            "ZHIXU_MODEL_SETTINGS_ROLLOUT_ID": "compose-contract-rollout",
            "ZHIXU_MODEL_SETTINGS_PREPARED": "true",
            "ZHIXU_APP_RESTART_POLICY": "no",
            "ZHIXU_WORKER_RESTART_POLICY": "no",
        },
    )
    expect_valid(managed)
    expect_valid(custom_ports)
    expect_valid(static, mode="static")
    expect_valid(static, mode="legacy")
    expect_valid(prepared, mode="prepared")
    if int(managed["services"]["app"]["ports"][0]["published"]) != 8080:
        fail("default Web ingress is not fixed to 127.0.0.1:8080")
    if int(custom_ports["services"]["app"]["ports"][0]["published"]) != 18080:
        fail("ZHIXU_HTTP_PORT did not select the fixed Web ingress")
    if managed["services"]["postgres"]["ports"][0].get("published") not in (None, 0, "0"):
        fail("Workspace-control PostgreSQL ingress must use a random loopback port")

    for service_name in ("app", "worker"):
        expect_invalid(
            f"{service_name} without PostgreSQL health gate",
            managed,
            lambda model, name=service_name: model["services"][name]["depends_on"].pop("postgres"),
        )
        expect_invalid(
            f"{service_name} with weak PostgreSQL dependency",
            managed,
            lambda model, name=service_name: model["services"][name]["depends_on"]["postgres"].update(
                condition="service_started"
            ),
        )
        expect_invalid(
            f"{service_name} bypasses PostgreSQL runtime wait",
            managed,
            lambda model, name=service_name: model["services"][name].update(
                entrypoint=[f"/app/zhixu-{name}"]
            ),
        )

    expect_invalid(
        "static app bypasses PostgreSQL runtime wait",
        static,
        lambda model: model["services"]["app"].update(entrypoint=["/app/zhixu-api"]),
        mode="static",
    )
    expect_invalid(
        "prepared Worker bypasses PostgreSQL runtime wait",
        prepared,
        lambda model: model["services"]["worker"].update(entrypoint=["/app/zhixu-worker"]),
        mode="prepared",
    )

    for service_name in ("app", "worker"):
        expect_invalid(
            f"steady {service_name} without auto-restart",
            managed,
            lambda model, name=service_name: model["services"][name].update(restart="no"),
        )
    expect_invalid(
        "prepared Worker auto-restart",
        prepared,
        lambda model: model["services"]["worker"].update(restart="on-failure"),
        mode="prepared",
    )
    expect_invalid(
        "prepared runtime rollout mismatch",
        prepared,
        lambda model: model["services"]["worker"]["environment"].update(
            ZHIXU_MODEL_SETTINGS_ROLLOUT_ID="different-rollout"
        ),
        mode="prepared",
    )
    for relay_name in ("app-model-relay", "worker-model-relay"):
        expect_invalid(
            f"{relay_name} without auto-restart",
            managed,
            lambda model, name=relay_name: model["services"][name].update(restart="no"),
        )
    expect_invalid(
        "proxy without auto-restart",
        managed,
        lambda model: model["services"]["proxy"].update(restart="no"),
    )

    expect_invalid("external secret volume", managed, lambda model: model["volumes"]["zhixu-model-secrets"].update(external=True))
    expect_invalid("writable app key", managed, lambda model: secret_mount(model, "app").update(read_only=False))
    expect_invalid(
        "initializer extra mount",
        managed,
        lambda model: model["services"]["model-settings-key-init"]["volumes"].append(
            {"type": "bind", "source": "/tmp", "target": "/host"}
        ),
    )
    expect_invalid(
        "modelctl skips key initialization",
        managed,
        lambda model: model["services"]["modelctl"]["depends_on"].pop("model-settings-key-init"),
    )
    expect_invalid(
        "managed static secret",
        managed,
        lambda model: model["services"]["worker"]["environment"].update(ZHIXU_CHAT_API_KEY="canary"),
    )
    expect_invalid(
        "Docker socket",
        managed,
        lambda model: model["services"]["modelctl"]["volumes"].append(
            {"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"}
        ),
    )
    expect_invalid(
        "relay private bind",
        managed,
        lambda model: model["services"]["app-model-relay"].update(
            entrypoint=[
                "socat",
                "TCP-LISTEN:11434,bind=0.0.0.0,fork,reuseaddr",
                "TCP:host.docker.internal:11434",
            ]
        ),
    )
    expect_invalid(
        "relay mount",
        managed,
        lambda model: model["services"]["worker-model-relay"].update(
            volumes=[{"type": "bind", "source": "/tmp", "target": "/host"}]
        ),
    )
    expect_invalid(
        "relay incompatible host mapping",
        managed,
        lambda model: model["services"]["worker-model-relay"].update(
            extra_hosts=["host.docker.internal=host-gateway"]
        ),
    )
    expect_invalid(
        "missing host gateway",
        managed,
        lambda model: model["services"]["worker"].update(extra_hosts=[]),
    )
    expect_invalid(
        "public ingress",
        managed,
        lambda model: model["services"]["app"]["ports"][0].update(host_ip="0.0.0.0"),
    )
    expect_invalid(
        "automatic Web ingress",
        managed,
        lambda model: model["services"]["app"]["ports"][0].update(published="0"),
    )
    expect_invalid(
        "static mode missing identity field",
        static,
        lambda model: model["services"]["app"]["environment"].pop("ZHIXU_CHAT_PROVIDER"),
        mode="static",
    )
    expect_invalid(
        "static mode retains managed key path",
        static,
        lambda model: model["services"]["worker"]["environment"].update(
            ZHIXU_MODEL_SETTINGS_KEY_FILE="/run/zhixu-model-secrets/model-settings.key"
        ),
        mode="static",
    )
    expect_invalid(
        "legacy managed Compose provider override",
        static,
        lambda model: model["services"]["app"]["environment"].update(
            ZHIXU_CHAT_PROVIDER="openai-compatible"
        ),
        mode="legacy",
    )

    print("[compose-runtime-contract] passed")


if __name__ == "__main__":
    main()
