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
LOCAL_MODEL_VOLUME = "zhixu-local-models"
LOCAL_RUNTIME_CREDENTIAL_VOLUME = "zhixu-local-model-runtime-credentials"
LOCAL_MODEL_TARGET = "/var/lib/zhixu/ollama/.ollama"
LOCAL_RUNTIME_CREDENTIAL_TARGET = "/run/zhixu-local-model-runtime"
LOCAL_MODEL_HOME = "/var/lib/zhixu/ollama"
LOCAL_RUNTIME_DOCKERFILE = "deploy/Dockerfile.local-model-runtime"
LOCAL_RUNTIME_ENTRYPOINT = ["/usr/local/bin/zhixu-local-model-runtime"]
LOCAL_VOLUME_INIT_ENTRYPOINT = ["/usr/local/bin/local-model-volume-init"]
LOCAL_RUNTIME_HEALTHCHECK = [
    "CMD",
    "/bin/bash",
    "-ec",
    (
        "exec 3<>/dev/tcp/127.0.0.1/11434; "
        "printf 'GET /healthz HTTP/1.1\\r\\nHost: 127.0.0.1\\r\\nConnection: close\\r\\n\\r\\n' >&3; "
        "IFS=' ' read -r _ status _ <&3; test \"$${status}\" = 204"
    ),
]
MANAGED_RELAY_UPSTREAM = "TCP:local-model-runtime:11434"
STATIC_RELAY_UPSTREAM = "TCP:host.docker.internal:11434"
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
RETIRED_AI_RUNTIME_SELECTOR_KEYS = {
    "ZHIXU_CHAT_IMPLEMENTATION",
    "ZHIXU_EMBEDDING_IMPLEMENTATION",
    "ZHIXU_STRUCTURED_SCHEDULER_RAG",
    "ZHIXU_STRUCTURED_SCHEDULER_RELATION",
    "ZHIXU_STRUCTURED_SCHEDULER_ARTIFACT",
    "ZHIXU_STRUCTURED_SCHEDULER_CAPTURE",
    "ZHIXU_STRUCTURED_SCHEDULER_ORGANIZING",
}


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


def validate_local_runtime_build(definition: dict[str, Any], service_name: str) -> None:
    build = definition.get("build")
    if not isinstance(build, dict) or build.get("dockerfile") != LOCAL_RUNTIME_DOCKERFILE:
        fail(f"{service_name} must use the dedicated local model runtime image")
    for forbidden in ("args", "additional_contexts", "secrets", "ssh", "target"):
        if forbidden in build:
            fail(f"{service_name} must not override the pinned local model runtime build")
    if "image" in definition:
        fail(f"{service_name} must not override the dedicated local model runtime image")


def validate_local_model_volume(model: dict[str, Any], *, require_credentials: bool = True) -> None:
    volumes = model.get("volumes")
    if not isinstance(volumes, dict):
        fail("Compose volumes are missing")
    definition = volumes.get(LOCAL_MODEL_VOLUME)
    if not isinstance(definition, dict) or definition.get("external") is True:
        fail("the managed local model volume must be project-owned")
    if definition.get("labels") != {
        "com.zhixu.owner": "local-model-runtime",
        "com.zhixu.schema": "local-model-store/v1",
    }:
        fail("the managed local model volume ownership labels are invalid")
    if "driver_opts" in definition:
        fail("the managed local model volume must not map host storage")
    credential = volumes.get(LOCAL_RUNTIME_CREDENTIAL_VOLUME)
    if require_credentials:
        if not isinstance(credential, dict) or credential.get("external") is True or "driver_opts" in credential:
            fail("the local runtime credential volume must be project-owned")
        if credential.get("labels") != {
            "com.zhixu.owner": "local-model-runtime",
            "com.zhixu.schema": "local-model-runtime-credentials/v1",
        }:
            fail("the local runtime credential volume ownership labels are invalid")
    elif credential is not None:
        fail("external-static Compose must not define the runtime credential volume")


def validate_exact_model_mount(
    definition: dict[str, Any], service_name: str, *, writable: bool
) -> None:
    mounts = volume_mounts(definition, service_name)
    model_mounts = [mount for mount in mounts if mount.get("source") == LOCAL_MODEL_VOLUME]
    if len(model_mounts) != 1:
        fail(f"{service_name} must receive exactly one managed model volume")
    mount = model_mounts[0]
    if (
        mount.get("type") != "volume"
        or mount.get("source") != LOCAL_MODEL_VOLUME
        or mount.get("target") != LOCAL_MODEL_TARGET
        or (mount.get("read_only") is True) == writable
    ):
        fail(f"{service_name} managed model volume mount is invalid")


def validate_no_runtime_privilege(definition: dict[str, Any], service_name: str) -> None:
    if definition.get("privileged") is True:
        fail(f"{service_name} must not be privileged")
    for forbidden in ("devices", "device_cgroup_rules", "gpus", "ports", "extra_hosts"):
        if definition.get(forbidden):
            fail(f"{service_name} must not receive {forbidden}")


def validate_local_model_runtime(model: dict[str, Any], *, external_static: bool) -> None:
    validate_local_model_volume(model, require_credentials=not external_static)

    volume_init = service(model, "local-model-volume-init")
    validate_local_runtime_build(volume_init, "local-model-volume-init")
    if (
        volume_init.get("profiles") != ["workspace-runtime"]
        or volume_init.get("entrypoint") != LOCAL_VOLUME_INIT_ENTRYPOINT
        or volume_init.get("user") != "0:0"
        or volume_init.get("restart") != "no"
        or volume_init.get("network_mode") != "none"
        or volume_init.get("read_only") is not True
        or volume_init.get("cap_drop") != ["ALL"]
        or set(volume_init.get("cap_add", [])) != {"CHOWN", "DAC_OVERRIDE"}
        or volume_init.get("security_opt") != ["no-new-privileges:true"]
    ):
        fail("local-model-volume-init security or lifecycle shape is invalid")
    validate_no_runtime_privilege(volume_init, "local-model-volume-init")
    if volume_init.get("environment"):
        fail("local-model-volume-init must not receive credentials or runtime configuration")
    validate_exact_model_mount(volume_init, "local-model-volume-init", writable=True)

    credential_init = service(model, "local-model-runtime-credential-init")
    validate_local_runtime_build(credential_init, "local-model-runtime-credential-init")
    credential_environment = environment(credential_init, "local-model-runtime-credential-init")
    if (
        credential_init.get("profiles") != ["workspace-runtime"]
        or credential_init.get("entrypoint") != (["/bin/true"] if external_static else ["/usr/local/bin/local-model-runtime-credential-init"])
        or credential_init.get("user") != "0:0"
        or credential_init.get("restart") != "no"
        or credential_init.get("read_only") is not True
        or credential_init.get("cap_drop") != ["ALL"]
        or set(credential_init.get("cap_add", [])) != (set() if external_static else {"CHOWN", "DAC_OVERRIDE"})
        or credential_init.get("security_opt") != ["no-new-privileges:true"]
        or (not external_static and not has_dependency(credential_init, "postgres", "service_healthy"))
        or (not external_static and not has_dependency(credential_init, "migrate", "service_completed_successfully"))
    ):
        fail("local-model-runtime credential initializer shape is invalid")
    credential_mounts = volume_mounts(credential_init, "local-model-runtime-credential-init")
    if external_static:
        if credential_mounts:
            fail("external-static credential initializer must not mount its credential volume")
        if credential_init.get("depends_on"):
            fail("external-static credential initializer must not depend on the database")
    elif (
        len(credential_mounts) != 1
        or credential_mounts[0].get("source") != LOCAL_RUNTIME_CREDENTIAL_VOLUME
        or credential_mounts[0].get("target") != LOCAL_RUNTIME_CREDENTIAL_TARGET
    ):
        fail("managed credential initializer must mount its credential volume exactly once")
    validate_no_runtime_privilege(credential_init, "local-model-runtime-credential-init")
    if external_static:
        if any(credential_environment.get(key) for key in ("ZHIXU_DATABASE_HOST", "ZHIXU_DATABASE_PORT", "ZHIXU_DATABASE_NAME", "ZHIXU_DATABASE_USER", "ZHIXU_DATABASE_PASSWORD")):
            fail("external-static credential initializer must not receive database credentials")
    else:
        for key in ("ZHIXU_DATABASE_HOST", "ZHIXU_DATABASE_PORT", "ZHIXU_DATABASE_NAME", "ZHIXU_DATABASE_USER", "ZHIXU_DATABASE_PASSWORD"):
            if not credential_environment.get(key):
                fail(f"credential initializer must receive {key}")

    runtime = service(model, "local-model-runtime")
    validate_local_runtime_build(runtime, "local-model-runtime")
    expected_mode = "external-static" if external_static else "managed"
    runtime_environment = environment(runtime, "local-model-runtime")
    required_environment = {
        "HOME": LOCAL_MODEL_HOME,
        "ZHIXU_LOCAL_MODEL_RUNTIME_MODE": expected_mode,
    }
    if any(runtime_environment.get(key) != value for key, value in required_environment.items()):
        fail(f"local-model-runtime must use the fixed {expected_mode} environment")
    if not external_static:
        for key in (
            "ZHIXU_DATABASE_HOST",
            "ZHIXU_DATABASE_PORT",
            "ZHIXU_DATABASE_NAME",
        ):
            if not runtime_environment.get(key):
                fail(f"managed local-model-runtime must receive {key}")
        if runtime_environment.get("ZHIXU_DATABASE_USER") or runtime_environment.get("ZHIXU_DATABASE_PASSWORD"):
            fail("managed local-model-runtime must not receive application database credentials")
        if runtime_environment.get("ZHIXU_DATABASE_USER_FILE") != f"{LOCAL_RUNTIME_CREDENTIAL_TARGET}/database-user" or runtime_environment.get("ZHIXU_DATABASE_PASSWORD_FILE") != f"{LOCAL_RUNTIME_CREDENTIAL_TARGET}/database-password":
            fail("managed local-model-runtime must use the dedicated credential files")
        if runtime_environment.get("ZHIXU_DATABASE_MAX_CONNS") != "2" or runtime_environment.get("ZHIXU_DATABASE_MIN_CONNS") != "1":
            fail("managed local-model-runtime must use the bounded database pool")
    elif any(key.startswith("ZHIXU_DATABASE_") and value for key, value in runtime_environment.items()):
        fail("external-static local-model-runtime must not receive database credentials")
    if (
        runtime.get("profiles") != ["workspace-runtime"]
        or runtime.get("entrypoint") != LOCAL_RUNTIME_ENTRYPOINT
        or runtime.get("user") != "10001:10001"
        or runtime.get("init") is not True
        or runtime.get("restart") != "unless-stopped"
        or runtime.get("stop_signal") != "SIGTERM"
        or runtime.get("stop_grace_period") != "45s"
        or runtime.get("read_only") is not True
        or runtime.get("cap_drop") != ["ALL"]
        or "cap_add" in runtime
        or runtime.get("security_opt") != ["no-new-privileges:true"]
        or runtime.get("working_dir") != LOCAL_MODEL_HOME
    ):
        fail("local-model-runtime security or lifecycle shape is invalid")
    validate_no_runtime_privilege(runtime, "local-model-runtime")
    if external_static and runtime_environment.get("ZHIXU_LOCAL_MODEL_RUNTIME_MODE") != "external-static":
        fail("external-static local-model-runtime must remain explicitly static")
    validate_exact_model_mount(runtime, "local-model-runtime", writable=True)
    runtime_mounts = volume_mounts(runtime, "local-model-runtime")
    credential_mounts = [mount for mount in runtime_mounts if mount.get("source") == LOCAL_RUNTIME_CREDENTIAL_VOLUME]
    if external_static:
        if credential_mounts:
            fail("external-static local-model-runtime must not mount its credential volume")
    elif len(credential_mounts) != 1 or credential_mounts[0].get("target") != LOCAL_RUNTIME_CREDENTIAL_TARGET or credential_mounts[0].get("read_only") is not True:
        fail("managed local-model-runtime credential volume mount is invalid")
    if set(runtime.get("tmpfs", [])) != {
        "/tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777",
        "/run:rw,noexec,nosuid,nodev,size=16m,mode=0755",
    }:
        fail("local-model-runtime writable scratch space is invalid")
    if not has_dependency(runtime, "local-model-volume-init", "service_completed_successfully"):
        fail("local-model-runtime must wait for its volume initializer")
    if external_static:
        if any(
            has_dependency(runtime, dependency, condition)
            for dependency, condition in (
                ("postgres", "service_healthy"),
                ("migrate", "service_completed_successfully"),
                ("local-model-runtime-credential-init", "service_completed_successfully"),
            )
        ):
            fail("external-static local-model-runtime must not depend on the database")
    else:
        if not has_dependency(runtime, "postgres", "service_healthy"):
            fail("managed local-model-runtime must wait for PostgreSQL health")
        if not has_dependency(runtime, "migrate", "service_completed_successfully"):
            fail("managed local-model-runtime must wait for migration")
        if not has_dependency(runtime, "local-model-runtime-credential-init", "service_completed_successfully"):
            fail("managed local-model-runtime must wait for credential initialization")
    healthcheck = runtime.get("healthcheck")
    if (
        not isinstance(healthcheck, dict)
        or healthcheck.get("test") != LOCAL_RUNTIME_HEALTHCHECK
        or healthcheck.get("interval") != "5s"
        or healthcheck.get("timeout") != "3s"
        or healthcheck.get("retries") != 20
    ):
        fail("local-model-runtime healthcheck must check manager liveness only")

    services = model.get("services")
    assert isinstance(services, dict)
    for service_name, raw_definition in services.items():
        if service_name in {"local-model-runtime", "local-model-volume-init", "local-model-runtime-credential-init"}:
            continue
        if not isinstance(raw_definition, dict):
            fail(f"{service_name} service definition must be an object")
        for mount in volume_mounts(raw_definition, service_name):
            if mount.get("source") == LOCAL_MODEL_VOLUME:
                fail(f"{service_name} must not mount the managed local model volume")


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


def validate_eino_primary_runtime(model: dict[str, Any]) -> None:
    service_names = ["app", "worker"]
    services = model.get("services")
    if isinstance(services, dict) and "modelctl" in services:
        service_names.append("modelctl")
    for service_name in service_names:
        service_environment = environment(service(model, service_name), service_name)
        retired = sorted(RETIRED_AI_RUNTIME_SELECTOR_KEYS.intersection(service_environment))
        if retired:
            fail(f"{service_name} contains retired AI runtime selectors")
        if service_name == "modelctl":
            continue
        if service_environment.get("ZHIXU_TOOL_RUNTIME_MODE") not in ("disabled", "enabled"):
            fail(f"{service_name} tool runtime mode must be disabled or enabled")

    worker_environment = environment(service(model, "worker"), "worker")
    if worker_environment.get("ZHIXU_TOOL_RUNTIME_MODE") != "enabled":
        fail("Eino chat requires the Worker Tool runtime")


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


def validate_relay(
    model: dict[str, Any],
    relay_name: str,
    anchor_name: str,
    healthcheck: list[str],
    *,
    external_static: bool,
) -> None:
    relay = service(model, relay_name)
    expected_entrypoint = [
        "socat",
        "TCP-LISTEN:11434,bind=127.0.0.1,fork,reuseaddr",
        STATIC_RELAY_UPSTREAM if external_static else MANAGED_RELAY_UPSTREAM,
    ]
    if relay.get("entrypoint") != expected_entrypoint:
        destination = "external host" if external_static else "managed local model runtime"
        fail(f"{relay_name} must use the fixed loopback-to-{destination} relay")
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
    if not has_dependency(relay, "local-model-runtime", "service_healthy"):
        fail(f"{relay_name} must wait for local-model-runtime health")
    validate_healthcheck(relay, relay_name, healthcheck)


def validate_relays(model: dict[str, Any], *, external_static: bool) -> None:
    validate_relay(
        model,
        "app-model-relay",
        "zhixu-app-netns",
        [
            "CMD-SHELL",
            "ss -H -ltn 'sport = :11434' | grep -q '127.0.0.1:11434' && wget -q -O /dev/null http://127.0.0.1:8080/readyz",
        ],
        external_static=external_static,
    )
    validate_relay(
        model,
        "worker-model-relay",
        "zhixu-worker-netns",
        [
            "CMD-SHELL",
            "ss -H -ltn 'sport = :11434' | grep -q '127.0.0.1:11434' && wget -q -O /dev/null http://127.0.0.1:8081/readyz && wget -q -O /dev/null http://127.0.0.1:18082",
        ],
        external_static=external_static,
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
    external_static = mode in {"static", "legacy"}
    validate_runtime_dependencies(model)
    validate_runtime_entrypoints(model)
    validate_local_model_runtime(model, external_static=external_static)
    if mode == "static":
        validate_static_models(model)
    elif mode == "legacy":
        validate_legacy_model_environment(model)
    elif mode == "prepared":
        validate_secret_boundary(model, prepared_candidate=True)
    else:
        validate_secret_boundary(model)
    validate_eino_primary_runtime(model)
    validate_relays(model, external_static=external_static)
    validate_ingress(model)
    validate_external_runtime_network(model)
    validate_zero_base_grant(model)


if __name__ == "__main__":
    main()
