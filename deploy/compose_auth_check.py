#!/usr/bin/env python3
"""Validate resolved Compose authentication boundaries without exposing secrets."""

from __future__ import annotations

import json
import ipaddress
import subprocess
import sys
from typing import Any


def fail(message: str) -> None:
    print(f"compose auth configuration is invalid: {message}", file=sys.stderr)
    raise SystemExit(1)


def service(model: dict[str, Any], service_name: str) -> dict[str, Any]:
    services = model.get("services")
    if not isinstance(services, dict):
        fail("services are missing")
    value = services.get(service_name)
    if not isinstance(value, dict):
        fail(f"{service_name} service is missing")
    return value


def service_environment(model: dict[str, Any], service_name: str) -> dict[str, Any]:
    environment = service(model, service_name).get("environment")
    if not isinstance(environment, dict):
        fail(f"{service_name} environment is missing")
    return environment


def is_loopback_host(value: Any) -> bool:
    if not isinstance(value, str):
        return False
    host = value.strip().strip("[]")
    if host.lower() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def parses_as_boolean(value: Any, name: str) -> bool:
    if not isinstance(value, str):
        fail(f"{name} must resolve to a boolean string")
    normalized = value.strip().lower()
    if normalized in {"1", "t", "true"}:
        return True
    if normalized in {"0", "f", "false"}:
        return False
    fail(f"{name} must resolve to a boolean string")


def is_loopback_listener(value: Any) -> bool:
    if not isinstance(value, str):
        return False
    address = value.strip()
    if address.startswith("["):
        closing = address.find("]")
        if closing == -1 or closing + 1 >= len(address) or address[closing + 1] != ":":
            return False
        host, port = address[1:closing], address[closing + 2 :]
    else:
        host, separator, port = address.rpartition(":")
        if separator == "":
            return False
    return port != "" and is_loopback_host(host)


def has_loopback_firewall(firewall: dict[str, Any]) -> bool:
    entrypoint = firewall.get("entrypoint")
    capabilities = firewall.get("cap_add")
    return (
        entrypoint == ["/app/loopback-firewall.sh"]
        and firewall.get("user") == "0:0"
        and isinstance(capabilities, list)
        and "NET_ADMIN" in capabilities
    )


def has_dependency_with_condition(service_definition: dict[str, Any], service_name: str, condition: str) -> bool:
    dependencies = service_definition.get("depends_on")
    if not isinstance(dependencies, dict):
        return False
    dependency = dependencies.get(service_name)
    return isinstance(dependency, dict) and dependency.get("condition") == condition


def validate_local_auth_ingress(model: dict[str, Any], app_environment: dict[str, Any], mode: str) -> None:
    secure_cookie = parses_as_boolean(app_environment.get("ZHIXU_AUTH_SECURE_COOKIE"), "ZHIXU_AUTH_SECURE_COOKIE")
    if mode == "required" and secure_cookie:
        return
    if not is_loopback_listener(app_environment.get("ZHIXU_HTTP_ADDR")):
        fail("disabled or insecure-cookie auth requires the API process to listen on loopback")
    for service_name in ("app", "firewall", "proxy"):
        network_mode = service(model, service_name).get("network_mode")
        if network_mode == "host":
            fail("disabled or insecure-cookie auth cannot use host networking")
    proxy = service(model, "proxy")
    if proxy.get("network_mode") != "service:app":
        fail("disabled or insecure-cookie auth requires the loopback proxy to share the app network namespace")
    if proxy.get("user") != "10001:10001" or "cap_add" in proxy:
        fail("disabled or insecure-cookie auth requires an unprivileged loopback proxy")
    firewall = service(model, "firewall")
    if firewall.get("network_mode") != "service:app" or not has_loopback_firewall(firewall):
        fail("disabled or insecure-cookie auth requires the root NET_ADMIN loopback firewall sidecar")
    if not has_dependency_with_condition(firewall, "app", "service_started"):
        fail("disabled or insecure-cookie auth requires the firewall to wait for the app network namespace")
    if not has_dependency_with_condition(proxy, "firewall", "service_completed_successfully"):
        fail("disabled or insecure-cookie auth requires the proxy to wait for firewall completion")
    ports = service(model, "app").get("ports")
    if not isinstance(ports, list) or not ports:
        fail("disabled or insecure-cookie auth requires an app loopback port mapping")
    has_tcp_port = False
    for port in ports:
        if not isinstance(port, dict):
            fail("app port mapping must be structured")
        if port.get("protocol", "tcp") != "tcp":
            continue
        has_tcp_port = True
        if not is_loopback_host(port.get("host_ip")):
            fail("disabled or insecure-cookie auth requires every app TCP port to bind loopback")
    if not has_tcp_port:
        fail("disabled or insecure-cookie auth requires an app TCP port mapping")


def resolved_compose_model() -> dict[str, Any]:
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
    model = resolved_compose_model()

    app_environment = service_environment(model, "app")
    mode = app_environment.get("ZHIXU_AUTH_MODE")
    token = app_environment.get("ZHIXU_AUTH_BOOTSTRAP_TOKEN")
    question_ref_key = app_environment.get("ZHIXU_REVIEW_QUESTION_REF_KEY")
    if not isinstance(mode, str) or not isinstance(token, str) or not isinstance(question_ref_key, str):
        fail("app auth mode and API-only secrets must resolve to strings")

    if mode == "required":
        if len(token) < 32 or token != token.strip():
            fail("required mode needs a canonical 32+ character ZHIXU_AUTH_BOOTSTRAP_TOKEN")
    elif mode == "disabled":
        if token != "":
            fail("ZHIXU_AUTH_BOOTSTRAP_TOKEN must be empty when auth mode is disabled")
    else:
        fail("ZHIXU_AUTH_MODE must be required or disabled")

    try:
        question_ref_key_bytes = len(question_ref_key.encode("utf-8"))
    except UnicodeEncodeError:
        fail("ZHIXU_REVIEW_QUESTION_REF_KEY must be valid UTF-8")
    if question_ref_key_bytes < 32 or question_ref_key != question_ref_key.strip():
        fail("ZHIXU_REVIEW_QUESTION_REF_KEY must contain at least 32 canonical bytes")

    validate_local_auth_ingress(model, app_environment, mode)

    for service_name in ("migrate", "worker"):
        environment = service_environment(model, service_name)
        for secret_name in ("ZHIXU_AUTH_BOOTSTRAP_TOKEN", "ZHIXU_REVIEW_QUESTION_REF_KEY"):
            if secret_name in environment:
                fail(f"{service_name} must not receive {secret_name}")


if __name__ == "__main__":
    main()
