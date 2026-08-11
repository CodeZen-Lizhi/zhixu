#!/usr/bin/env python3
"""Validate resolved Compose model-settings and local-model boundaries."""

from __future__ import annotations

import json
import subprocess
import sys
from typing import Any


SECRET_VOLUME = "zhixu-model-secrets"
SECRET_TARGET = "/run/zhixu-model-secrets"
KEY_FILE = f"{SECRET_TARGET}/model-settings.key"
STATIC_MANAGED_VALUES = {
    "ZHIXU_MODEL_SETTINGS_KEY_FILE": "",
    "ZHIXU_MODEL_SETTINGS_ROLLOUT_ID": "",
    "ZHIXU_MODEL_SETTINGS_PREPARED": "false",
}
LEGACY_MODEL_DEFAULTS = {
    "ZHIXU_CHAT_PROVIDER": "disabled",
    "ZHIXU_CHAT_API_STYLE": "chat_completions",
    "ZHIXU_CHAT_BASE_URL": "",
    "ZHIXU_CHAT_API_KEY": "",
    "ZHIXU_CHAT_MODEL": "",
    "ZHIXU_CHAT_MODEL_VERSION": "",
    "ZHIXU_CHAT_ADAPTER_VERSION": "v1",
    "ZHIXU_CHAT_TIMEOUT": "30s",
    "ZHIXU_CHAT_MAX_REQUEST_BYTES": "4194304",
    "ZHIXU_CHAT_MAX_RESPONSE_BYTES": "4194304",
    "ZHIXU_EMBEDDING_PROVIDER": "disabled",
    "ZHIXU_EMBEDDING_BASE_URL": "",
    "ZHIXU_EMBEDDING_API_KEY": "",
    "ZHIXU_EMBEDDING_MODEL": "",
    "ZHIXU_EMBEDDING_DIMENSIONS": "0",
    "ZHIXU_EMBEDDING_NORMALIZATION": "l2",
    "ZHIXU_EMBEDDING_DISTANCE_METRIC": "cosine",
    "ZHIXU_EMBEDDING_MAX_BATCH_SIZE": "128",
    "ZHIXU_EMBEDDING_MAX_INPUT_BYTES": "65536",
    "ZHIXU_EMBEDDING_MAX_BATCH_INPUT_BYTES": "8388608",
    "ZHIXU_EMBEDDING_TIMEOUT": "30s",
    "ZHIXU_EMBEDDING_MAX_RESPONSE_BYTES": "67108864",
}
STATIC_MODEL_KEYS = set(LEGACY_MODEL_DEFAULTS)


def fail(message: str) -> None:
    print(f"compose runtime configuration is invalid: {message}", file=sys.stderr)
    raise SystemExit(1)


def service(model: dict[str, Any], service_name: str) -> dict[str, Any]:
    services = model.get("services")
    if not isinstance(services, dict):
        fail("services are missing")
    value = services.get(service_name)
    if not isinstance(value, dict):
        fail(f"{service_name} service is missing")
    return value


def environment(definition: dict[str, Any], service_name: str) -> dict[str, Any]:
    value = definition.get("environment")
    if not isinstance(value, dict):
        fail(f"{service_name} environment is missing")
    return value


def volume_mounts(definition: dict[str, Any], service_name: str) -> list[dict[str, Any]]:
    value = definition.get("volumes", [])
    if not isinstance(value, list):
        fail(f"{service_name} volumes must be a list")
    mounts: list[dict[str, Any]] = []
    for mount in value:
        if not isinstance(mount, dict):
            fail(f"{service_name} volume mount must be structured")
        mounts.append(mount)
    return mounts


def has_dependency(definition: dict[str, Any], dependency_name: str, condition: str) -> bool:
    dependencies = definition.get("depends_on")
    if not isinstance(dependencies, dict):
        return False
    dependency = dependencies.get(dependency_name)
    return isinstance(dependency, dict) and dependency.get("condition") == condition


def validate_runtime_dependencies(model: dict[str, Any]) -> None:
    for service_name in ("app", "worker"):
        if not has_dependency(service(model, service_name), "postgres", "service_healthy"):
            fail(f"{service_name} must wait for PostgreSQL health")


def validate_runtime_entrypoints(model: dict[str, Any]) -> None:
    expected = {
        "app": ["/app/zhixu-runtime-wait", "--profile", "api", "--", "/app/zhixu-api"],
        "worker": ["/app/zhixu-runtime-wait", "--profile", "worker", "--", "/app/zhixu-worker"],
    }
    for service_name, entrypoint in expected.items():
        if service(model, service_name).get("entrypoint") != entrypoint:
            fail(f"{service_name} must wait for PostgreSQL before starting its runtime")


def validate_restart_policy(model: dict[str, Any], prepared_candidate: bool) -> None:
    expected_app_policy = "no" if prepared_candidate else "on-failure"
    expected_worker_policy = "no" if prepared_candidate else "on-failure"
    if service(model, "app").get("restart") != expected_app_policy:
        fail(f"app restart policy must be {expected_app_policy}")
    if service(model, "worker").get("restart") != expected_worker_policy:
        fail(f"worker restart policy must be {expected_worker_policy}")


def validate_secret_boundary(model: dict[str, Any], prepared_candidate: bool = False) -> None:
    volumes = model.get("volumes")
    if not isinstance(volumes, dict) or SECRET_VOLUME not in volumes:
        fail("the dedicated model settings secret volume is missing")
    secret_volume = volumes[SECRET_VOLUME]
    if not isinstance(secret_volume, dict) or secret_volume.get("external") is True:
        fail("the model settings secret volume must be project-owned")

    key_init = service(model, "model-settings-key-init")
    if key_init.get("entrypoint") != ["/app/model-secrets-init.sh"] or key_init.get("user") != "0:0":
        fail("model-settings-key-init must run the trusted initializer as root")
    if key_init.get("privileged") is True or "cap_add" in key_init or "ports" in key_init:
        fail("model-settings-key-init must not receive runtime privileges or ingress")
    key_init_environment = environment(key_init, "model-settings-key-init")
    if key_init_environment != {"ZHIXU_MODEL_SETTINGS_KEY_DIR": "/var/lib/zhixu/model-secrets"}:
        fail("model-settings-key-init must receive only its fixed key directory")

    key_init_mounts = volume_mounts(key_init, "model-settings-key-init")
    writable_mounts = [
        mount
        for mount in key_init_mounts
        if mount.get("source") == SECRET_VOLUME
        and mount.get("target") == "/var/lib/zhixu/model-secrets"
        and mount.get("read_only") is not True
    ]
    if len(key_init_mounts) != 1 or len(writable_mounts) != 1:
        fail("model-settings-key-init needs exactly one writable dedicated secret volume")

    readers = {"app", "worker", "modelctl"}
    services = model.get("services")
    assert isinstance(services, dict)
    for service_name, raw_definition in services.items():
        if not isinstance(raw_definition, dict):
            fail(f"{service_name} service definition must be an object")
        definition = raw_definition
        mounts = volume_mounts(definition, service_name)
        secret_mounts = [mount for mount in mounts if mount.get("source") == SECRET_VOLUME]
        if service_name in readers:
            if len(secret_mounts) != 1:
                fail(f"{service_name} must mount the model secret volume exactly once")
            mount = secret_mounts[0]
            if mount.get("target") != SECRET_TARGET or mount.get("read_only") is not True:
                fail(f"{service_name} model secret mount must be read-only at the fixed target")
            service_environment = environment(definition, service_name)
            if service_environment.get("ZHIXU_MODEL_SETTINGS_MODE") != "managed":
                fail(f"{service_name} must use managed model settings")
            if service_environment.get("ZHIXU_MODEL_SETTINGS_KEY_FILE") != KEY_FILE:
                fail(f"{service_name} must use the fixed model settings key file")
            if not has_dependency(definition, "model-settings-key-init", "service_completed_successfully"):
                fail(f"{service_name} must wait for model-settings-key-init")
        elif service_name == "model-settings-key-init":
            pass
        elif secret_mounts:
            fail(f"{service_name} must not mount the model settings secret volume")

        for mount in mounts:
            source = str(mount.get("source", ""))
            target = str(mount.get("target", ""))
            if "docker.sock" in source or "docker.sock" in target:
                fail(f"{service_name} must not mount a Docker socket")

    rollout_ids: list[str] = []
    for service_name in ("app", "worker"):
        definition = service(model, service_name)
        service_environment = environment(definition, service_name)
        rollout_id = service_environment.get("ZHIXU_MODEL_SETTINGS_ROLLOUT_ID")
        prepared = service_environment.get("ZHIXU_MODEL_SETTINGS_PREPARED")
        if prepared_candidate:
            if not isinstance(rollout_id, str) or not rollout_id:
                fail(f"prepared {service_name} candidate must have a rollout id")
            if prepared != "true":
                fail(f"prepared {service_name} candidate must select prepared mode")
            rollout_ids.append(rollout_id)
        else:
            if rollout_id != "":
                fail(f"{service_name} default rollout id must be empty")
            if prepared != "false":
                fail(f"{service_name} must not start prepared by default")
        static_keys = sorted(STATIC_MODEL_KEYS.intersection(service_environment))
        if static_keys:
            fail(f"{service_name} managed environment contains static model fields")
    if prepared_candidate and len(set(rollout_ids)) != 1:
        fail("prepared API and Worker candidates must use the same rollout id")
    validate_restart_policy(model, prepared_candidate)
    modelctl = service(model, "modelctl")
    if modelctl.get("entrypoint") != ["/app/zhixu-modelctl"]:
        fail("modelctl entrypoint is invalid")
    if modelctl.get("profiles") != ["modelctl"] or "ports" in modelctl:
        fail("modelctl must be profile-scoped and have no published ports")


def validate_zero_base_grant(model: dict[str, Any]) -> None:
    for service_name, raw_definition in model.get("services", {}).items():
        if not isinstance(raw_definition, dict):
            fail(f"{service_name} service definition must be an object")
        definition = raw_definition
        for mount in volume_mounts(definition, service_name):
            if mount.get("type") == "bind":
                fail(f"base Compose must not grant a bind mount to {service_name}")
        service_environment = definition.get("environment", {})
        if not isinstance(service_environment, dict):
            fail(f"{service_name} environment must be an object")
        for key in (
            "ZHIXU_WORKSPACE_ROOT",
            "ZHIXU_WORKSPACE_GRANTED_ID",
            "ZHIXU_WORKSPACE_GRANTED_ROOT",
            "ZHIXU_WORKSPACE_GRANT_GENERATION",
        ):
            if key in service_environment:
                fail(f"base Compose must not contain {key}")


def validate_network_mode(definition: dict[str, Any], service_name: str, anchor_name: str) -> None:
    if definition.get("network_mode") != f"container:{anchor_name}":
        fail(f"{service_name} must share the fixed {anchor_name} network namespace")
    if definition.get("extra_hosts"):
        fail(f"{service_name} must inherit host mapping from {anchor_name}")


def validate_healthcheck(definition: dict[str, Any], service_name: str, expected: list[str]) -> None:
    healthcheck = definition.get("healthcheck")
    if not isinstance(healthcheck, dict) or healthcheck.get("test") != expected:
        fail(f"{service_name} healthcheck must detect namespace divergence")


def validate_relay(model: dict[str, Any], relay_name: str, anchor_name: str, healthcheck: list[str]) -> None:
    relay = service(model, relay_name)
    expected_entrypoint = [
        "socat",
        "TCP-LISTEN:11434,bind=127.0.0.1,fork,reuseaddr",
        "TCP:host.docker.internal:11434",
    ]
    if relay.get("entrypoint") != expected_entrypoint:
        fail(f"{relay_name} must use the fixed loopback-to-host Ollama relay")
    validate_network_mode(relay, relay_name, anchor_name)
    if relay.get("restart") != "on-failure":
        fail(f"{relay_name} restart policy must be on-failure")
    if relay.get("user") != "10001:10001" or relay.get("privileged") is True or "cap_add" in relay or "ports" in relay:
        fail(f"{relay_name} must be unprivileged and publish no ports")
    if volume_mounts(relay, relay_name):
        fail(f"{relay_name} must not receive host or secret mounts")
    owner_name = "app" if relay_name == "app-model-relay" else "worker"
    if not has_dependency(relay, owner_name, "service_started"):
        fail(f"{relay_name} must wait for {owner_name} startup")
    validate_healthcheck(relay, relay_name, healthcheck)


def validate_relays(model: dict[str, Any]) -> None:
    validate_relay(
        model,
        "app-model-relay",
        "zhixu-app-netns",
        [
            "CMD-SHELL",
            "ss -H -ltn 'sport = :11434' | grep -q '127.0.0.1:11434' && wget -q -O /dev/null http://127.0.0.1:8080/readyz",
        ],
    )
    validate_relay(
        model,
        "worker-model-relay",
        "zhixu-worker-netns",
        [
            "CMD-SHELL",
            "ss -H -ltn 'sport = :11434' | grep -q '127.0.0.1:11434' && wget -q -O /dev/null http://127.0.0.1:8081/readyz && wget -q -O /dev/null http://127.0.0.1:18082",
        ],
    )


def validate_ingress(model: dict[str, Any]) -> None:
    app = service(model, "app")
    app_environment = environment(app, "app")
    if app_environment.get("ZHIXU_HTTP_ADDR") != "127.0.0.1:8081":
        fail("app must keep the API listener on internal loopback port 8081")
    validate_network_mode(app, "app", "zhixu-app-netns")
    if "ports" in app:
        fail("app must not publish ingress; the netns anchor owns host port 8080")
    validate_healthcheck(
        app,
        "app",
        [
            "CMD-SHELL",
            "wget -q -O /dev/null http://127.0.0.1:8081/readyz && wget -q -O /dev/null http://127.0.0.1:8080/readyz",
        ],
    )

    worker = service(model, "worker")
    validate_network_mode(worker, "worker", "zhixu-worker-netns")
    validate_healthcheck(
        worker,
        "worker",
        [
            "CMD-SHELL",
            "wget -q -O /dev/null http://127.0.0.1:8081/readyz && wget -q -O /dev/null http://127.0.0.1:18082",
        ],
    )

    postgres_ports = service(model, "postgres").get("ports")
    if not isinstance(postgres_ports, list) or len(postgres_ports) != 1:
        fail("PostgreSQL must publish exactly one Workspace-control loopback port")
    postgres_port = postgres_ports[0]
    if (
        not isinstance(postgres_port, dict)
        or postgres_port.get("host_ip") != "127.0.0.1"
        or postgres_port.get("target") != 5432
        or postgres_port.get("protocol") != "tcp"
        or postgres_port.get("published") not in (None, 0, "0")
    ):
        fail("PostgreSQL host port must be allocated automatically on loopback")

    services = model.get("services")
    assert isinstance(services, dict)
    for removed_service in ("proxy", "firewall"):
        if removed_service in services:
            fail(f"{removed_service} must be owned by the zhixu-netns helper instead of the main project")


def validate_external_runtime_network(model: dict[str, Any]) -> None:
    networks = model.get("networks")
    if not isinstance(networks, dict):
        fail("main Compose networks are missing")
    default_network = networks.get("default")
    if not isinstance(default_network, dict):
        fail("main Compose default network is missing")
    if default_network.get("external") is not True or default_network.get("name") != "zhixu-runtime":
        fail("main Compose default network must be the external zhixu-runtime helper network")


def validate_static_models(model: dict[str, Any]) -> None:
    for service_name in ("app", "worker"):
        service_environment = environment(service(model, service_name), service_name)
        if service_environment.get("ZHIXU_MODEL_SETTINGS_MODE") != "static":
            fail(f"{service_name} static overlay must select static model settings")
        for key, expected in STATIC_MANAGED_VALUES.items():
            if service_environment.get(key) != expected:
                fail(f"{service_name} static overlay must clear {key}")
        missing = sorted(STATIC_MODEL_KEYS.difference(service_environment))
        if missing:
            fail(f"{service_name} static overlay is missing static model fields")
    validate_restart_policy(model, prepared_candidate=False)


def validate_legacy_model_environment(model: dict[str, Any]) -> None:
    validate_static_models(model)
    for service_name in ("app", "worker"):
        service_environment = environment(service(model, service_name), service_name)
        for key, expected in LEGACY_MODEL_DEFAULTS.items():
            if service_environment.get(key) != expected:
                fail(
                    f"legacy {key} override is not accepted by managed Compose; "
                    "restore .env model fields to .env.example and configure models in Settings"
                )


def resolved_compose_model() -> tuple[dict[str, Any], str]:
    arguments = sys.argv[1:]
    mode = "managed"
    modes = {
        "--static-models": "static",
        "--legacy-model-env": "legacy",
        "--prepared-candidate": "prepared",
    }
    if arguments[:1] and arguments[0] in modes:
        mode = modes[arguments[0]]
        arguments = arguments[1:]
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
    return model, mode


def main() -> None:
    model, mode = resolved_compose_model()
    validate_runtime_dependencies(model)
    validate_runtime_entrypoints(model)
    if mode == "static":
        validate_static_models(model)
    elif mode == "legacy":
        validate_legacy_model_environment(model)
    elif mode == "prepared":
        validate_secret_boundary(model, prepared_candidate=True)
    else:
        validate_secret_boundary(model)
    validate_relays(model)
    validate_ingress(model)
    validate_external_runtime_network(model)
    validate_zero_base_grant(model)


if __name__ == "__main__":
    main()
