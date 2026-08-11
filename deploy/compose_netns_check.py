#!/usr/bin/env python3
"""Validate the isolated Compose project which owns Zhixu network namespaces."""

from __future__ import annotations

import json
import subprocess
import sys
from typing import Any


HOST_GATEWAY = "host.docker.internal=host-gateway"


def fail(message: str) -> None:
    print(f"netns Compose configuration is invalid: {message}", file=sys.stderr)
    raise SystemExit(1)


def service(model: dict[str, Any], name: str) -> dict[str, Any]:
    services = model.get("services")
    if not isinstance(services, dict):
        fail("services are missing")
    definition = services.get(name)
    if not isinstance(definition, dict):
        fail(f"{name} service is missing")
    return definition


def healthcheck(definition: dict[str, Any], name: str, expected: list[str]) -> None:
    configured = definition.get("healthcheck")
    if not isinstance(configured, dict) or configured.get("test") != expected:
        fail(f"{name} healthcheck is invalid")


def no_mounts(definition: dict[str, Any], name: str) -> None:
    for field in ("volumes", "secrets", "configs"):
        if definition.get(field):
            fail(f"{name} must not receive {field}")
    if definition.get("environment"):
        fail(f"{name} must not receive business environment")


def build_from_runtime_image(definition: dict[str, Any], name: str) -> None:
    build = definition.get("build")
    if not isinstance(build, dict) or build.get("dockerfile") != "deploy/Dockerfile":
        fail(f"{name} must build from the runtime Dockerfile")


def require_base_identity(definition: dict[str, Any], name: str, container_name: str) -> None:
    if definition.get("container_name") != container_name:
        fail(f"{name} container name must be fixed")
    if definition.get("restart") != "unless-stopped":
        fail(f"{name} must restart after Docker daemon recovery")
    if definition.get("read_only") is not True:
        fail(f"{name} root filesystem must be read-only")
    if definition.get("privileged") is True:
        fail(f"{name} must not be privileged")
    if definition.get("extra_hosts") != [HOST_GATEWAY]:
        fail(f"{name} must provide the fixed host-gateway mapping")
    no_mounts(definition, name)
    build_from_runtime_image(definition, name)


def validate_network(model: dict[str, Any]) -> None:
    if model.get("name") != "zhixu-netns":
        fail("project name must be zhixu-netns")
    networks = model.get("networks")
    if not isinstance(networks, dict):
        fail("network definition is missing")
    default = networks.get("default")
    if not isinstance(default, dict) or default.get("name") != "zhixu-runtime":
        fail("helper must own the fixed zhixu-runtime network")
    if default.get("external") is True:
        fail("helper must create the zhixu-runtime network")


def validate_app_anchor(model: dict[str, Any]) -> None:
    app = service(model, "app-netns")
    require_base_identity(app, "app-netns", "zhixu-app-netns")
    if app.get("entrypoint") != ["/app/netns-ingress.sh"] or app.get("user") != "0:0":
        fail("app-netns must run only the firewall-before-listen ingress entrypoint as root")
    if app.get("cap_drop") != ["ALL"] or set(app.get("cap_add", [])) != {"NET_ADMIN", "SETUID", "SETGID"}:
        fail("app-netns must receive only firewall and bootstrap privilege capabilities")
    if app.get("security_opt") != ["no-new-privileges:true"]:
        fail("app-netns must prohibit privilege escalation")
    if app.get("tmpfs") != ["/run:rw,noexec,nosuid,size=65536"]:
        fail("app-netns needs only its transient firewall lock tmpfs")
    ports = app.get("ports")
    if not isinstance(ports, list) or len(ports) != 1:
        fail("app-netns must publish exactly one loopback ingress port")
    port = ports[0]
    if not isinstance(port, dict) or port.get("host_ip") != "127.0.0.1" or port.get("target") != 8080 or port.get("protocol") != "tcp":
        fail("app-netns ingress must map host loopback to port 8080")
    try:
        published = int(port.get("published"))
    except (TypeError, ValueError):
        fail("app-netns host ingress port must be fixed")
    if not 1 <= published <= 65535:
        fail("app-netns host ingress port must be fixed")
    healthcheck(
        app,
        "app-netns",
        ["CMD-SHELL", "ss -H -ltn 'sport = :8080' | grep -q '0.0.0.0:8080'"],
    )


def validate_worker_anchor(model: dict[str, Any]) -> None:
    worker = service(model, "worker-netns")
    require_base_identity(worker, "worker-netns", "zhixu-worker-netns")
    expected_entrypoint = [
        "/app/netns-ingress.sh",
        "--worker-sentinel",
    ]
    if worker.get("entrypoint") != expected_entrypoint or worker.get("user") != "10001:10001":
        fail("worker-netns must run its loopback sentinel as the unprivileged runtime user")
    if worker.get("cap_drop") != ["ALL"] or "cap_add" in worker:
        fail("worker-netns must have zero Linux capabilities")
    if worker.get("security_opt") != ["no-new-privileges:true"]:
        fail("worker-netns must prohibit privilege escalation")
    if "ports" in worker or "tmpfs" in worker:
        fail("worker-netns must not publish ports or receive writable mounts")
    healthcheck(
        worker,
        "worker-netns",
        ["CMD", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:18082"],
    )


def resolved_model() -> dict[str, Any]:
    arguments = sys.argv[1:]
    try:
        if arguments:
            if arguments[0] != "--" or len(arguments) == 1:
                fail("Compose command arguments are invalid")
            completed = subprocess.run(
                arguments[1:],
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
            )
            if completed.returncode != 0:
                fail("resolved Compose model command failed")
            model = json.loads(completed.stdout)
        else:
            model = json.load(sys.stdin)
    except (json.JSONDecodeError, OSError, TypeError, ValueError):
        fail("resolved Compose model could not be read")
    if not isinstance(model, dict):
        fail("resolved Compose model must be an object")
    return model


def main() -> None:
    model = resolved_model()
    validate_network(model)
    validate_app_anchor(model)
    validate_worker_anchor(model)


if __name__ == "__main__":
    main()
