#!/usr/bin/env python3
"""Exercise positive and fail-closed Workspace grant Compose contracts."""

from __future__ import annotations

import copy
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any, Callable


ROOT = Path(__file__).resolve().parent.parent
CHECKER = ROOT / "deploy" / "compose_workspace_check.py"
COMPOSE = ROOT / "deploy" / "compose.yml"
ENV_FILE = ROOT / ".env.example"
PATH_FIXTURE = ROOT / "deploy" / "workspace_path_contract.json"
GRANT_KEYS = (
    "ZHIXU_WORKSPACE_GRANTED_ID",
    "ZHIXU_WORKSPACE_GRANTED_ROOT",
    "ZHIXU_WORKSPACE_GRANT_GENERATION",
)


def fail(message: str) -> None:
    print(f"[compose-workspace-contract] failed: {message}", file=sys.stderr)
    raise SystemExit(1)


def render(*files: Path) -> dict[str, Any]:
    command = [
        "docker", "compose", "--profile", "workspace-runtime", "--profile", "modelctl",
        "--project-name", "zhixu",
    ]
    for path in files:
        command.extend(["-f", str(path)])
    command.extend(["--env-file", str(ENV_FILE), "config", "--format", "json"])
    completed = subprocess.run(command, check=False, capture_output=True, text=True, env=os.environ.copy())
    if completed.returncode != 0:
        fail("Docker Compose could not render the Workspace contract fixture")
    try:
        model = json.loads(completed.stdout)
    except json.JSONDecodeError:
        fail("Docker Compose returned invalid JSON")
    if not isinstance(model, dict):
        fail("Docker Compose returned a non-object model")
    return model


def check(model: dict[str, Any], mode: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(CHECKER), f"--{mode}"],
        input=json.dumps(model), check=False, capture_output=True, text=True,
    )


def expect_valid(model: dict[str, Any], mode: str) -> None:
    completed = check(model, mode)
    if completed.returncode != 0:
        fail(f"valid {mode} model was rejected: {completed.stderr.strip()}")


def expect_invalid(name: str, model: dict[str, Any], mutate: Callable[[dict[str, Any]], None]) -> None:
    candidate = copy.deepcopy(model)
    mutate(candidate)
    if check(candidate, "grant").returncode == 0:
        fail(f"invalid grant model was accepted: {name}")


def set_grant_root(model: dict[str, Any], root: str) -> None:
    for name in ("app", "worker"):
        definition = model["services"][name]
        definition["environment"][GRANT_KEYS[1]] = root
        binding = next(mount for mount in definition["volumes"] if mount.get("type") == "bind")
        binding["source"] = root
        binding["target"] = root


def main() -> None:
    base = render(COMPOSE)
    expect_valid(base, "base")

    with tempfile.TemporaryDirectory(prefix="zhixu-compose-workspace-") as temporary:
        root = "/tmp/zhixu-workspace-contract"
        override = Path(temporary) / "grant.json"
        service = {
            "environment": {
                GRANT_KEYS[0]: "workspace-contract-1",
                GRANT_KEYS[1]: root,
                GRANT_KEYS[2]: "7",
            },
            "volumes": [{
                "type": "bind", "source": root, "target": root,
                "bind": {"create_host_path": False},
            }],
        }
        override.write_text(json.dumps({"services": {"app": service, "worker": service}}), encoding="utf-8")
        granted = render(COMPOSE, override)

    expect_valid(granted, "grant")
    expect_invalid(
        "Worker target mismatch", granted,
        lambda model: next(
            mount for mount in model["services"]["worker"]["volumes"] if mount.get("type") == "bind"
        ).update(target="/tmp/different"),
    )
    expect_invalid(
        "second API bind", granted,
        lambda model: model["services"]["app"]["volumes"].append(
            {"type": "bind", "source": "/tmp/extra", "target": "/tmp/extra", "bind": {"create_host_path": False}}
        ),
    )
    expect_invalid(
        "sidecar bind", granted,
        lambda model: model["services"]["proxy"].update(
            volumes=[{"type": "bind", "source": "/tmp/extra", "target": "/tmp/extra"}]
        ),
    )
    expect_invalid(
        "Docker socket", granted,
        lambda model: model["services"]["modelctl"]["volumes"].append(
            {"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"}
        ),
    )
    expect_invalid(
        "Docker socket descendant", granted,
        lambda model: model["services"]["modelctl"]["volumes"].append(
            {"type": "volume", "source": "/var/run/docker.sock/child", "target": "/run/socket"}
        ),
    )
    expect_invalid(
        "sidecar grant environment", granted,
        lambda model: model["services"]["proxy"].setdefault("environment", {}).update(
            ZHIXU_WORKSPACE_GRANTED_ID="workspace-contract-1"
        ),
    )
    expect_invalid(
        "invalid Workspace identity", granted,
        lambda model: [
            model["services"][name]["environment"].update(
                ZHIXU_WORKSPACE_GRANTED_ID="workspace with spaces"
            ) for name in ("app", "worker")
        ],
    )
    expect_invalid(
        "Worker generation mismatch", granted,
        lambda model: model["services"]["worker"]["environment"].update(
            ZHIXU_WORKSPACE_GRANT_GENERATION="8"
        ),
    )
    expect_invalid(
        "implicit host path creation", granted,
        lambda model: next(
            mount for mount in model["services"]["app"]["volumes"] if mount.get("type") == "bind"
        )["bind"].update(create_host_path=True),
    )
    expect_invalid(
        "root runtime user", granted,
        lambda model: model["services"]["worker"].update(user="0:0"),
    )

    path_cases = json.loads(PATH_FIXTURE.read_text(encoding="utf-8"))
    for case in path_cases:
        candidate = copy.deepcopy(granted)
        set_grant_root(candidate, case["path"])
        accepted = check(candidate, "grant").returncode == 0
        if accepted != case["valid"]:
            fail("Go/Compose shared path fixture parity failed")

    print("[compose-workspace-contract] passed")


if __name__ == "__main__":
    main()
