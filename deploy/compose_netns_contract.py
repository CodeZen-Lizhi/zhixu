#!/usr/bin/env python3
"""Exercise positive and fail-closed contracts for the netns helper project."""

from __future__ import annotations

import copy
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Any, Callable


ROOT = Path(__file__).resolve().parent.parent
CHECKER = ROOT / "deploy" / "compose_netns_check.py"
COMPOSE = ROOT / "deploy" / "compose.netns.yml"
ENV_FILE = ROOT / ".env.example"


def fail(message: str) -> None:
    print(f"[compose-netns-contract] failed: {message}", file=sys.stderr)
    raise SystemExit(1)


def render(environment: dict[str, str] | None = None) -> dict[str, Any]:
    command = [
        "docker",
        "compose",
        "--project-name",
        "zhixu-netns",
        "-f",
        str(COMPOSE),
        "--env-file",
        str(ENV_FILE),
        "config",
        "--format",
        "json",
    ]
    process_environment = os.environ.copy()
    if environment is not None:
        process_environment.update(environment)
    completed = subprocess.run(command, check=False, capture_output=True, text=True, env=process_environment)
    if completed.returncode != 0:
        fail("Docker Compose could not render the helper fixture")
    try:
        model = json.loads(completed.stdout)
    except json.JSONDecodeError:
        fail("Docker Compose returned invalid JSON")
    if not isinstance(model, dict):
        fail("Docker Compose returned a non-object model")
    return model


def check(model: dict[str, Any]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(CHECKER)],
        input=json.dumps(model),
        check=False,
        capture_output=True,
        text=True,
    )


def expect_valid(model: dict[str, Any]) -> None:
    result = check(model)
    if result.returncode != 0:
        fail(f"valid helper model was rejected: {result.stderr.strip()}")


def expect_invalid(name: str, model: dict[str, Any], mutate: Callable[[dict[str, Any]], None]) -> None:
    candidate = copy.deepcopy(model)
    mutate(candidate)
    if check(candidate).returncode == 0:
        fail(f"invalid helper Compose model was accepted: {name}")


def main() -> None:
    default = render()
    custom_port = render({"ZHIXU_HTTP_PORT": "18080"})
    expect_valid(default)
    expect_valid(custom_port)
    if int(default["services"]["app-netns"]["ports"][0]["published"]) != 8080:
        fail("default helper ingress port is not 8080")
    if int(custom_port["services"]["app-netns"]["ports"][0]["published"]) != 18080:
        fail("ZHIXU_HTTP_PORT did not select the helper ingress port")

    expect_invalid("main project identity", default, lambda model: model.update(name="zhixu"))
    expect_invalid("external helper network", default, lambda model: model["networks"]["default"].update(external=True))
    expect_invalid("wrong helper network", default, lambda model: model["networks"]["default"].update(name="foreign"))
    expect_invalid("unstable app name", default, lambda model: model["services"]["app-netns"].update(container_name="app"))
    expect_invalid("public ingress", default, lambda model: model["services"]["app-netns"]["ports"][0].update(host_ip="0.0.0.0"))
    expect_invalid("app ingress cap bypass", default, lambda model: model["services"]["app-netns"].update(cap_add=["NET_ADMIN", "SETUID", "SETGID", "SYS_ADMIN"]))
    expect_invalid("app writable root", default, lambda model: model["services"]["app-netns"].update(read_only=False))
    expect_invalid("app direct socat", default, lambda model: model["services"]["app-netns"].update(entrypoint=["socat"]))
    expect_invalid("app secret", default, lambda model: model["services"]["app-netns"].update(volumes=[{"type": "volume", "source": "secret", "target": "/run/secret"}]))
    expect_invalid("worker root", default, lambda model: model["services"]["worker-netns"].update(user="0:0"))
    expect_invalid("worker cap", default, lambda model: model["services"]["worker-netns"].update(cap_add=["NET_ADMIN"]))
    expect_invalid("worker host port", default, lambda model: model["services"]["worker-netns"].update(ports=[]))
    expect_invalid("worker broad sentinel", default, lambda model: model["services"]["worker-netns"].update(entrypoint=["/app/netns-ingress.sh"]))

    print("[compose-netns-contract] passed")


if __name__ == "__main__":
    main()
