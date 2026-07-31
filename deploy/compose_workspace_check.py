#!/usr/bin/env python3
"""Validate zero-base and exact Workspace grant Compose contracts."""

from __future__ import annotations

import json
import posixpath
import re
import subprocess
import sys
from typing import Any


GRANT_KEYS = (
    "ZHIXU_WORKSPACE_GRANTED_ID",
    "ZHIXU_WORKSPACE_GRANTED_ROOT",
    "ZHIXU_WORKSPACE_GRANT_GENERATION",
)
RESERVED_NAMESPACES = (
    "/app", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc",
    "/root", "/run", "/sbin", "/sys", "/usr", "/var/lib", "/var/run", "/workspace",
)
EXACT_RESERVED = {"/tmp", "/private/tmp"}
GRANT_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")


def fail(message: str) -> None:
    print(f"compose Workspace configuration is invalid: {message}", file=sys.stderr)
    raise SystemExit(1)


def service(model: dict[str, Any], name: str) -> dict[str, Any]:
    services = model.get("services")
    value = services.get(name) if isinstance(services, dict) else None
    if not isinstance(value, dict):
        fail(f"{name} service is missing")
    return value


def mounts(definition: dict[str, Any], name: str) -> list[dict[str, Any]]:
    value = definition.get("volumes", [])
    if not isinstance(value, list) or any(not isinstance(item, dict) for item in value):
        fail(f"{name} mounts must use structured syntax")
    return value


def environment(definition: dict[str, Any], name: str) -> dict[str, Any]:
    value = definition.get("environment", {})
    if not isinstance(value, dict):
        fail(f"{name} environment must be an object")
    return value


def overlaps(left: str, right: str) -> bool:
    return left == right or left.startswith(right + "/") or right.startswith(left + "/")


def valid_target(root: Any) -> bool:
    if (
        not isinstance(root, str)
        or len(root) > 4096
        or not root.startswith("/")
        or root.startswith("//")
        or any(ord(character) < 32 or ord(character) == 127 for character in root)
        or posixpath.normpath(root) != root
        or root == "/"
    ):
        return False
    if root in EXACT_RESERVED:
        return False
    return not any(overlaps(root, reserved) for reserved in RESERVED_NAMESPACES)


def validate_common(model: dict[str, Any]) -> None:
    services = model.get("services")
    if not isinstance(services, dict):
        fail("services are missing")
    for name, raw_definition in services.items():
        if not isinstance(raw_definition, dict):
            fail(f"{name} service definition must be an object")
        for mount in mounts(raw_definition, name):
            source = str(mount.get("source", ""))
            target = str(mount.get("target", ""))
            if source == "docker.sock" or target == "docker.sock" or "/docker.sock" in source or "/docker.sock" in target:
                fail("Docker socket mounts are forbidden")
        if "ZHIXU_WORKSPACE_ROOT" in environment(raw_definition, name):
            fail("legacy ZHIXU_WORKSPACE_ROOT is forbidden")


def validate_base(model: dict[str, Any]) -> None:
    validate_common(model)
    for name, raw_definition in model["services"].items():
        definition = raw_definition
        if any(mount.get("type") == "bind" for mount in mounts(definition, name)):
            fail(f"base Compose must grant zero bind mounts ({name})")
        if any(key in environment(definition, name) for key in GRANT_KEYS):
            fail(f"base Compose must grant zero Workspace environment ({name})")


def validate_grant(model: dict[str, Any]) -> None:
    validate_common(model)
    app_environment = environment(service(model, "app"), "app")
    workspace_id = app_environment.get(GRANT_KEYS[0])
    root = app_environment.get(GRANT_KEYS[1])
    generation = app_environment.get(GRANT_KEYS[2])
    if not isinstance(workspace_id, str) or GRANT_ID_PATTERN.fullmatch(workspace_id) is None or not valid_target(root):
        fail("grant identity or target is invalid")
    if not isinstance(generation, str) or not generation.isdigit() or int(generation) < 1:
        fail("grant generation is invalid")

    for name in ("app", "worker"):
        definition = service(model, name)
        if definition.get("user") != "10001:10001":
            fail(f"{name} must run as the fixed non-root user")
        role_environment = environment(definition, name)
        if tuple(role_environment.get(key) for key in GRANT_KEYS) != (workspace_id, root, generation):
            fail("API and Worker grant environment must match")
        bindings = [mount for mount in mounts(definition, name) if mount.get("type") == "bind"]
        if len(bindings) != 1:
            fail(f"{name} must receive exactly one Workspace bind")
        binding = bindings[0]
        bind_options = binding.get("bind")
        if (
            binding.get("source") != root
            or binding.get("target") != root
            or binding.get("source") != binding.get("target")
            or binding.get("read_only") is True
            or not isinstance(bind_options, dict)
            or bind_options.get("create_host_path") is not False
        ):
            fail(f"{name} Workspace bind must preserve the exact writable identity")

    for name, raw_definition in model["services"].items():
        if name not in {"app", "worker"}:
            if any(mount.get("type") == "bind" for mount in mounts(raw_definition, name)):
                fail(f"{name} must receive zero bind mounts")
            if any(key in environment(raw_definition, name) for key in GRANT_KEYS):
                fail(f"{name} must receive zero Workspace environment")


def resolved_model(arguments: list[str]) -> dict[str, Any]:
    try:
        if arguments:
            if arguments[0] != "--" or len(arguments) == 1:
                fail("Compose command arguments are invalid")
            completed = subprocess.run(arguments[1:], check=False, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            if completed.returncode != 0:
                fail("resolved Compose model command failed")
            value = json.loads(completed.stdout)
        else:
            value = json.load(sys.stdin)
    except (OSError, TypeError, ValueError, json.JSONDecodeError):
        fail("resolved Compose model could not be read")
    if not isinstance(value, dict):
        fail("resolved Compose model must be an object")
    return value


def main() -> None:
    arguments = sys.argv[1:]
    mode = "base"
    if arguments[:1] in (["--base"], ["--grant"]):
        mode = arguments[0][2:]
        arguments = arguments[1:]
    model = resolved_model(arguments)
    if mode == "grant":
        validate_grant(model)
    else:
        validate_base(model)


if __name__ == "__main__":
    main()
