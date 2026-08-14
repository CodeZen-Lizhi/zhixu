#!/usr/bin/env python3
"""Collect attested Eino stability evidence from Prometheus and Tempo."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from decimal import Decimal, InvalidOperation, ROUND_CEILING
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import stat
import sys
from typing import Any, Callable
import urllib.error
import urllib.parse
import urllib.request
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

import eino_stable_observation_verify as gate


QUERY_SCHEMA = "eino-stable-observation-queries/v1"
THRESHOLD_SCHEMA = "eino-stable-observation-thresholds/v1"
BACKEND_TRUST_SCHEMA = "eino-stable-observation-backend-trust/v1"
INCIDENT_SCHEMA = "eino-stable-observation-incidents/v1"
EVIDENCE_SCHEMA = gate.EVIDENCE_SCHEMA
QUERY_PATH = Path(__file__).with_name("eino_stable_observation_queries.json")
THRESHOLD_PATH = Path(__file__).with_name("eino_stable_observation_thresholds.json")
BACKEND_TRUST_PATH = Path(__file__).with_name("eino_stable_observation_backend_trust.json")
TRUST_PATH = gate.TRUST_POLICY_PATH
MAX_BACKEND_RESPONSE_BYTES = 64 * 1024
MAX_SECRET_BYTES = 4096
MAX_QUERY_BYTES = 4096
REQUEST_TIMEOUT_SECONDS = 20
HEX_SHA256 = re.compile(r"^[0-9a-f]{64}$")
SERVICE_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$")

PROMETHEUS_QUERY_FIELDS = set(gate.DAY_PROMETHEUS_EVIDENCE_FIELDS)
TEMPO_QUERY_FIELDS = set(gate.TEMPO_EVIDENCE_FIELDS)
QUERY_FIELDS = {
    "backend",
    "prometheus",
    "schema_version",
    "service_names",
    "status",
    "tempo",
    "visibility_probe_seconds",
}
THRESHOLD_FIELDS = {
    "approved_at",
    "approver_role",
    "completion_p95_ms_max",
    "draft_degradation_delta_max",
    "failure_rate_denominator",
    "failure_rate_ppm_max",
    "first_token_p95_ms_max",
    "quantile",
    "reviewer_role",
    "schema_version",
    "statistical_window",
    "status",
}
INCIDENT_FIELDS = {"incident_flags", "schema_version", "window_end", "window_start"}
BACKEND_TRUST_FIELDS = {
    "backend",
    "prometheus_token_file_sha256",
    "prometheus_url_file_sha256",
    "prometheus_url_sha256",
    "schema_version",
    "status",
    "tempo_token_file_sha256",
    "tempo_url_file_sha256",
    "tempo_url_sha256",
}


class CollectionError(ValueError):
    """A stable, privacy-safe collection failure."""

    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


@dataclass(frozen=True)
class BackendFiles:
    prometheus_url: Path
    prometheus_token: Path
    tempo_url: Path
    tempo_token: Path


@dataclass(frozen=True)
class QueryPolicy:
    prometheus: dict[str, str]
    tempo: dict[str, str]
    visibility_probe_seconds: int
    digest: str


@dataclass(frozen=True)
class ThresholdPolicy:
    thresholds: dict[str, int]
    reviewer_role: str
    approved_at: datetime
    digest: str


@dataclass(frozen=True)
class CollectorResult:
    values: dict[str, Decimal | bool]
    response_hashes: dict[str, str]


@dataclass(frozen=True)
class StartReadiness:
    current_time: datetime
    zone: ZoneInfo
    policy: QueryPolicy
    thresholds: ThresholdPolicy
    backend_policy_digest: str
    trust_digest: str
    key: bytes


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req: Any, fp: Any, code: int, msg: str, headers: Any, newurl: str) -> None:
        del req, fp, code, msg, headers, newurl
        return None


def require_exact_fields(document: dict[str, Any], expected: set[str]) -> None:
    try:
        gate.require_exact_fields(document, expected)
    except gate.VerificationError as exc:
        raise CollectionError("OBSERVATION_POLICY_FIELDS_INVALID") from exc


def require_hash(value: str) -> str:
    if not isinstance(value, str) or HEX_SHA256.fullmatch(value) is None:
        raise CollectionError("OBSERVATION_RELEASE_HASH_INVALID")
    return value


def require_role(value: Any) -> str:
    if not isinstance(value, str) or gate.ROLE.fullmatch(value) is None:
        raise CollectionError("OBSERVATION_ROLE_INVALID")
    return value


def load_canonical(path: Path) -> tuple[dict[str, Any], str]:
    try:
        document, _, digest = gate.load_canonical_document(path)
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    return document, digest


def validate_query_text(value: Any, *, windowed: bool) -> str:
    if not isinstance(value, str) or not value or len(value.encode("utf-8")) > MAX_QUERY_BYTES:
        raise CollectionError("OBSERVATION_QUERY_INVALID")
    if any(ord(character) < 0x20 for character in value):
        raise CollectionError("OBSERVATION_QUERY_INVALID")
    placeholders = re.findall(r"\$\{[^}]+\}", value)
    if windowed:
        valid_placeholders = bool(placeholders) and all(value == "${WINDOW_SECONDS}" for value in placeholders)
    else:
        valid_placeholders = not placeholders
    if not valid_placeholders:
        raise CollectionError("OBSERVATION_QUERY_INVALID")
    return value


def load_query_policy(path: Path = QUERY_PATH) -> QueryPolicy:
    document, digest = load_canonical(path)
    require_exact_fields(document, QUERY_FIELDS)
    if document.get("schema_version") != QUERY_SCHEMA or document.get("status") != "configured" or document.get("backend") != "prometheus-tempo":
        raise CollectionError("OBSERVATION_QUERY_POLICY_UNCONFIGURED")
    service_names = document.get("service_names")
    if not isinstance(service_names, dict) or set(service_names) != {"api", "worker"}:
        raise CollectionError("OBSERVATION_QUERY_INVALID")
    for value in service_names.values():
        if not isinstance(value, str) or SERVICE_NAME.fullmatch(value) is None:
            raise CollectionError("OBSERVATION_QUERY_INVALID")
    prometheus = document.get("prometheus")
    tempo = document.get("tempo")
    if not isinstance(prometheus, dict) or set(prometheus) != PROMETHEUS_QUERY_FIELDS:
        raise CollectionError("OBSERVATION_QUERY_INVALID")
    if not isinstance(tempo, dict) or set(tempo) != TEMPO_QUERY_FIELDS:
        raise CollectionError("OBSERVATION_QUERY_INVALID")
    validated_prometheus = {key: validate_query_text(value, windowed=True) for key, value in prometheus.items()}
    validated_tempo = {key: validate_query_text(value, windowed=False) for key, value in tempo.items()}
    for role, service_name in service_names.items():
        if not all(service_name in validated_prometheus[key] for key in (f"metrics_{role}_visible", f"telemetry_{role}_required")):
            raise CollectionError("OBSERVATION_QUERY_INVALID")
        if service_name not in validated_tempo[f"traces_{role}_visible"]:
            raise CollectionError("OBSERVATION_QUERY_INVALID")
    probe = document.get("visibility_probe_seconds")
    if type(probe) is not int or probe < 300 or probe > 3600 or probe % 300 != 0:
        raise CollectionError("OBSERVATION_QUERY_INVALID")
    return QueryPolicy(validated_prometheus, validated_tempo, probe, digest)


def load_threshold_policy(path: Path = THRESHOLD_PATH, *, now: datetime | None = None) -> ThresholdPolicy:
    document, digest = load_canonical(path)
    require_exact_fields(document, THRESHOLD_FIELDS)
    if document.get("schema_version") != THRESHOLD_SCHEMA or document.get("status") != "configured":
        raise CollectionError("OBSERVATION_THRESHOLD_POLICY_UNCONFIGURED")
    if document.get("statistical_window") != "natural_day" or document.get("failure_rate_denominator") != "agent.answer.result_total" or document.get("quantile") != "0.95":
        raise CollectionError("OBSERVATION_THRESHOLD_POLICY_INVALID")
    require_role(document.get("approver_role"))
    reviewer_role = require_role(document.get("reviewer_role"))
    try:
        approved_at = gate.parse_timestamp(document.get("approved_at"))
    except gate.VerificationError as exc:
        raise CollectionError("OBSERVATION_THRESHOLD_POLICY_INVALID") from exc
    current_time = now or datetime.now(timezone.utc)
    if approved_at > current_time + timedelta(minutes=5):
        raise CollectionError("OBSERVATION_THRESHOLD_POLICY_INVALID")
    thresholds: dict[str, int] = {}
    for field, maximum in (
        ("failure_rate_ppm_max", 1_000_000),
        ("draft_degradation_delta_max", 2**63 - 1),
        ("first_token_p95_ms_max", 2**63 - 1),
        ("completion_p95_ms_max", 2**63 - 1),
    ):
        value = document.get(field)
        minimum = 0 if field in {"failure_rate_ppm_max", "draft_degradation_delta_max"} else 1
        if type(value) is not int or value < minimum or value > maximum:
            raise CollectionError("OBSERVATION_THRESHOLD_POLICY_INVALID")
        thresholds[field] = value
    return ThresholdPolicy(thresholds, reviewer_role, approved_at, digest)


def load_trust_policy(path: Path = TRUST_PATH) -> tuple[dict[str, Any], str]:
    try:
        document, digest = gate.load_trust_policy(path)
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    if document["status"] != "configured":
        raise CollectionError("OBSERVATION_TRUST_POLICY_UNCONFIGURED")
    return document, digest


def load_backend_trust_policy(path: Path = BACKEND_TRUST_PATH) -> tuple[dict[str, Any], str]:
    document, digest = load_canonical(path)
    require_exact_fields(document, BACKEND_TRUST_FIELDS)
    if (
        document.get("schema_version") != BACKEND_TRUST_SCHEMA
        or document.get("status") != "configured"
        or document.get("backend") != "prometheus-tempo"
    ):
        raise CollectionError("OBSERVATION_BACKEND_POLICY_UNCONFIGURED")
    for field in (
        "prometheus_token_file_sha256",
        "prometheus_url_file_sha256",
        "prometheus_url_sha256",
        "tempo_token_file_sha256",
        "tempo_url_file_sha256",
        "tempo_url_sha256",
    ):
        if not isinstance(document.get(field), str) or HEX_SHA256.fullmatch(document[field]) is None:
            raise CollectionError("OBSERVATION_BACKEND_POLICY_INVALID")
    return document, digest


def trusted_attestation_key(path: Path, trust: dict[str, Any]) -> bytes:
    validate_secure_file_location(path, "OBSERVATION_ATTESTATION_KEY_INVALID")
    try:
        key = gate.read_attestation_key(path)
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    if hashlib.sha256(key).hexdigest() != trust["attestation_key_sha256"]:
        raise CollectionError("OBSERVATION_ATTESTATION_KEY_NOT_TRUSTED")
    return key


def protected_path_digest(path: Path) -> str:
    if not path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts[1:]):
        raise CollectionError("OBSERVATION_PATH_UNSAFE")
    return hashlib.sha256(str(path).encode("utf-8")).hexdigest()


def validate_secure_file_location(path: Path, error_code: str) -> None:
    if not path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts[1:]):
        raise CollectionError("OBSERVATION_PATH_UNSAFE")
    try:
        file_info = os.lstat(path)
        parent_info = os.lstat(path.parent)
    except OSError as exc:
        raise CollectionError(error_code) from exc
    if not stat.S_ISREG(file_info.st_mode) or not stat.S_ISDIR(parent_info.st_mode):
        raise CollectionError(error_code)
    allowed_uids = {0, os.geteuid()}
    if file_info.st_uid not in allowed_uids or parent_info.st_uid not in allowed_uids:
        raise CollectionError(error_code)
    if stat.S_IMODE(file_info.st_mode) not in {0o400, 0o600} or stat.S_IMODE(parent_info.st_mode) & 0o022:
        raise CollectionError(error_code)
    for ancestor in path.parent.parents:
        try:
            info = os.lstat(ancestor)
        except OSError as exc:
            raise CollectionError(error_code) from exc
        if not stat.S_ISDIR(info.st_mode) or info.st_uid not in allowed_uids or stat.S_IMODE(info.st_mode) & 0o022:
            raise CollectionError(error_code)


def read_protected_text(path: Path, *, minimum_bytes: int = 1, maximum_bytes: int = MAX_SECRET_BYTES) -> str:
    validate_secure_file_location(path, "OBSERVATION_BACKEND_CREDENTIAL_INVALID")
    try:
        raw, mode = gate.read_regular_file_with_mode(path, maximum_bytes)
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    if stat.S_IMODE(mode) not in {0o400, 0o600} or len(raw) < minimum_bytes:
        raise CollectionError("OBSERVATION_BACKEND_CREDENTIAL_INVALID")
    try:
        value = raw.decode("utf-8", errors="strict")
    except UnicodeDecodeError as exc:
        raise CollectionError("OBSERVATION_BACKEND_CREDENTIAL_INVALID") from exc
    if value.endswith("\n"):
        value = value[:-1]
    if not value or value != value.strip() or any(character.isspace() or ord(character) < 0x20 for character in value):
        raise CollectionError("OBSERVATION_BACKEND_CREDENTIAL_INVALID")
    return value


def read_backend_url(path: Path) -> str:
    value = read_protected_text(path)
    try:
        parsed = urllib.parse.urlsplit(value)
        hostname = parsed.hostname
        port = parsed.port
    except ValueError as exc:
        raise CollectionError("OBSERVATION_BACKEND_URL_INVALID") from exc
    if (
        not value.startswith("https://")
        or parsed.scheme != "https"
        or not hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or port == 0
    ):
        raise CollectionError("OBSERVATION_BACKEND_URL_INVALID")
    if parsed.path not in {"", "/"} and (not parsed.path.startswith("/") or parsed.path.endswith("/")):
        raise CollectionError("OBSERVATION_BACKEND_URL_INVALID")
    return value.rstrip("/")


def read_bearer_token(path: Path) -> str:
    value = read_protected_text(path, minimum_bytes=16)
    if len(value.encode("utf-8")) > MAX_SECRET_BYTES:
        raise CollectionError("OBSERVATION_BACKEND_CREDENTIAL_INVALID")
    return value


def append_url_path(base_url: str, suffix: str) -> str:
    parsed = urllib.parse.urlsplit(base_url)
    path = parsed.path.rstrip("/") + suffix
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, path, "", ""))


class BackendClient:
    def __init__(self, files: BackendFiles, *, backend_trust_path: Path = BACKEND_TRUST_PATH) -> None:
        trust, self.backend_policy_digest = load_backend_trust_policy(backend_trust_path)
        if (
            protected_path_digest(files.prometheus_url) != trust["prometheus_url_file_sha256"]
            or protected_path_digest(files.prometheus_token) != trust["prometheus_token_file_sha256"]
            or protected_path_digest(files.tempo_url) != trust["tempo_url_file_sha256"]
            or protected_path_digest(files.tempo_token) != trust["tempo_token_file_sha256"]
        ):
            raise CollectionError("OBSERVATION_BACKEND_FILE_NOT_TRUSTED")
        self.prometheus_url = read_backend_url(files.prometheus_url)
        self.tempo_url = read_backend_url(files.tempo_url)
        if (
            hashlib.sha256(self.prometheus_url.encode("utf-8")).hexdigest() != trust["prometheus_url_sha256"]
            or hashlib.sha256(self.tempo_url.encode("utf-8")).hexdigest() != trust["tempo_url_sha256"]
        ):
            raise CollectionError("OBSERVATION_BACKEND_URL_NOT_TRUSTED")
        self.prometheus_token = read_bearer_token(files.prometheus_token)
        self.tempo_token = read_bearer_token(files.tempo_token)
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirectHandler())

    def request_json(self, request: urllib.request.Request) -> tuple[dict[str, Any], str]:
        try:
            response = self.opener.open(request, timeout=REQUEST_TIMEOUT_SECONDS)
        except (urllib.error.URLError, urllib.error.HTTPError, TimeoutError, OSError) as exc:
            del exc
            raise CollectionError("OBSERVATION_BACKEND_UNAVAILABLE")
        try:
            if response.status != 200:
                raise CollectionError("OBSERVATION_BACKEND_UNAVAILABLE")
            content_type = response.headers.get_content_type()
            if content_type != "application/json":
                raise CollectionError("OBSERVATION_BACKEND_RESPONSE_INVALID")
            declared = response.headers.get("Content-Length")
            if declared is not None:
                try:
                    declared_bytes = int(declared)
                except ValueError as exc:
                    raise CollectionError("OBSERVATION_BACKEND_RESPONSE_INVALID") from exc
                if declared_bytes < 0:
                    raise CollectionError("OBSERVATION_BACKEND_RESPONSE_INVALID")
                if declared_bytes > MAX_BACKEND_RESPONSE_BYTES:
                    raise CollectionError("OBSERVATION_BACKEND_RESPONSE_TOO_LARGE")
            raw = response.read(MAX_BACKEND_RESPONSE_BYTES + 1)
            if len(raw) > MAX_BACKEND_RESPONSE_BYTES:
                raise CollectionError("OBSERVATION_BACKEND_RESPONSE_TOO_LARGE")
        finally:
            response.close()
        try:
            document = gate.decode_document(raw)
        except gate.VerificationError as exc:
            raise CollectionError("OBSERVATION_BACKEND_RESPONSE_INVALID") from exc
        return document, hashlib.sha256(raw).hexdigest()

    def prometheus_value(self, query: str, evaluation_time: datetime) -> tuple[Decimal, str]:
        body = urllib.parse.urlencode({"query": query, "time": evaluation_time.astimezone(timezone.utc).isoformat()}).encode("ascii")
        request = urllib.request.Request(
            append_url_path(self.prometheus_url, "/api/v1/query"),
            data=body,
            method="POST",
            headers={
                "Accept": "application/json",
                "Authorization": "Bearer " + self.prometheus_token,
                "Content-Type": "application/x-www-form-urlencoded",
            },
        )
        document, digest = self.request_json(request)
        if document.get("status") != "success" or document.get("warnings") or document.get("infos"):
            raise CollectionError("OBSERVATION_PROMETHEUS_QUERY_FAILED")
        data = document.get("data")
        if not isinstance(data, dict):
            raise CollectionError("OBSERVATION_PROMETHEUS_RESPONSE_INVALID")
        result_type = data.get("resultType")
        result = data.get("result")
        sample: Any
        if result_type == "scalar":
            sample = result
        elif result_type == "vector" and isinstance(result, list) and len(result) == 1 and isinstance(result[0], dict):
            sample = result[0].get("value")
        else:
            raise CollectionError("OBSERVATION_PROMETHEUS_RESPONSE_INVALID")
        if not isinstance(sample, list) or len(sample) != 2 or not isinstance(sample[1], str):
            raise CollectionError("OBSERVATION_PROMETHEUS_RESPONSE_INVALID")
        try:
            value = Decimal(sample[1])
        except (InvalidOperation, ValueError) as exc:
            raise CollectionError("OBSERVATION_PROMETHEUS_RESPONSE_INVALID") from exc
        if not value.is_finite() or value < 0:
            raise CollectionError("OBSERVATION_PROMETHEUS_RESPONSE_INVALID")
        return value, digest

    def tempo_visible(self, query: str, window_start: datetime, window_end: datetime) -> tuple[bool, str]:
        parameters = urllib.parse.urlencode(
            {
                "q": query,
                "start": str(int(window_start.astimezone(timezone.utc).timestamp())),
                "end": str(int(window_end.astimezone(timezone.utc).timestamp())),
                "limit": "1",
            }
        )
        request = urllib.request.Request(
            append_url_path(self.tempo_url, "/api/search") + "?" + parameters,
            method="GET",
            headers={"Accept": "application/json", "Authorization": "Bearer " + self.tempo_token},
        )
        document, digest = self.request_json(request)
        traces = document.get("traces")
        if not isinstance(traces, list):
            raise CollectionError("OBSERVATION_TEMPO_RESPONSE_INVALID")
        if any(not isinstance(trace, dict) for trace in traces):
            raise CollectionError("OBSERVATION_TEMPO_RESPONSE_INVALID")
        return len(traces) > 0, digest


def render_query(template: str, window_seconds: int) -> str:
    rendered = template.replace("${WINDOW_SECONDS}", str(window_seconds))
    if "${" in rendered:
        raise CollectionError("OBSERVATION_QUERY_INVALID")
    return rendered


def collect_queries(
    client: BackendClient,
    policy: QueryPolicy,
    window_start: datetime,
    window_end: datetime,
    *,
    start_probe: bool,
) -> CollectorResult:
    window_seconds = int((window_end.astimezone(timezone.utc) - window_start.astimezone(timezone.utc)).total_seconds())
    if window_seconds < 300 or window_seconds > 27 * 60 * 60:
        raise CollectionError("OBSERVATION_QUERY_WINDOW_INVALID")
    fields = set(gate.START_PROMETHEUS_EVIDENCE_FIELDS)
    if not start_probe:
        fields = PROMETHEUS_QUERY_FIELDS
    values: dict[str, Decimal | bool] = {}
    response_hashes: dict[str, str] = {}
    for field in sorted(fields):
        value, digest = client.prometheus_value(render_query(policy.prometheus[field], window_seconds), window_end)
        values[field] = value
        response_hashes["prometheus." + field] = digest
    for field in sorted(TEMPO_QUERY_FIELDS):
        value, digest = client.tempo_visible(policy.tempo[field], window_start, window_end)
        values[field] = value
        response_hashes["tempo." + field] = digest
    return CollectorResult(values, response_hashes)


def decimal_integer(value: Decimal, field: str) -> int:
    if value != value.to_integral_value() or value < 0 or value > 2**63 - 1:
        raise CollectionError("OBSERVATION_AGGREGATE_INVALID")
    del field
    return int(value)


def decimal_ceiling(value: Decimal) -> int:
    if value < 0 or value > 2**63 - 1:
        raise CollectionError("OBSERVATION_AGGREGATE_INVALID")
    return int(value.to_integral_value(rounding=ROUND_CEILING))


def query_truth(result: CollectorResult, field: str) -> bool:
    value = result.values.get(field)
    return isinstance(value, Decimal) and value >= 1


def aggregate_day(result: CollectorResult) -> dict[str, int | bool]:
    total = decimal_integer(result.values["answer_total_count"], "answer_total_count")  # type: ignore[arg-type]
    failures = decimal_integer(result.values["answer_failure_count"], "answer_failure_count")  # type: ignore[arg-type]
    if failures > total or (total == 0 and failures != 0):
        raise CollectionError("OBSERVATION_FAILURE_RATE_UNAVAILABLE")
    failure_rate_ppm = 0
    if total > 0:
        failure_rate_ppm = (failures * 1_000_000 + total - 1) // total
    return {
        "collector_metrics_api_visible": query_truth(result, "metrics_api_visible"),
        "collector_metrics_worker_visible": query_truth(result, "metrics_worker_visible"),
        "collector_traces_api_visible": result.values.get("traces_api_visible") is True,
        "collector_traces_worker_visible": result.values.get("traces_worker_visible") is True,
        "completion_p95_ms": decimal_ceiling(result.values["completion_p95_ms"]),  # type: ignore[arg-type]
        "draft_degradation_delta": decimal_integer(result.values["draft_degradation_delta"], "draft_degradation_delta"),  # type: ignore[arg-type]
        "failure_rate_ppm": failure_rate_ppm,
        "first_token_p95_ms": decimal_ceiling(result.values["first_token_p95_ms"]),  # type: ignore[arg-type]
        "rag_v2_non_replay_terminal_delta": decimal_integer(result.values["rag_v2_terminal_delta"], "rag_v2_terminal_delta"),  # type: ignore[arg-type]
        "telemetry_required_api": query_truth(result, "telemetry_api_required"),
        "telemetry_required_worker": query_truth(result, "telemetry_worker_required"),
    }


def safe_directory_descriptor(path: Path) -> int:
    if not path.is_absolute():
        raise CollectionError("OBSERVATION_PATH_UNSAFE")
    try:
        descriptor = gate.open_directory_no_symlinks(path)
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    mode = stat.S_IMODE(os.fstat(descriptor).st_mode)
    if mode & 0o022:
        gate.close_descriptor(descriptor)
        raise CollectionError("OBSERVATION_DIRECTORY_PERMISSIONS_INVALID")
    return descriptor


def write_new_canonical(path: Path, document: dict[str, Any]) -> str:
    if not path.name or path.name in {".", ".."} or Path(path.name).name != path.name:
        raise CollectionError("OBSERVATION_PATH_UNSAFE")
    raw = gate.canonical_bytes(document)
    if len(raw) > gate.MAX_DOCUMENT_BYTES:
        raise CollectionError("OBSERVATION_FILE_SIZE_INVALID")
    directory = safe_directory_descriptor(path.parent)
    descriptor: int | None = None
    created = False
    try:
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
        try:
            descriptor = os.open(path.name, flags, 0o600, dir_fd=directory)
            created = True
        except FileExistsError as exc:
            raise CollectionError("OBSERVATION_FILE_ALREADY_EXISTS") from exc
        except OSError as exc:
            raise CollectionError("OBSERVATION_FILE_UNAVAILABLE") from exc
        view = memoryview(raw)
        while view:
            written = os.write(descriptor, view)
            if written < 1:
                raise CollectionError("OBSERVATION_FILE_UNAVAILABLE")
            view = view[written:]
        os.fsync(descriptor)
        os.fsync(directory)
    except Exception:
        if descriptor is not None:
            gate.close_descriptor(descriptor)
            descriptor = None
        if created:
            try:
                os.unlink(path.name, dir_fd=directory)
            except OSError:
                pass
        raise
    finally:
        if descriptor is not None:
            gate.close_descriptor(descriptor)
        gate.close_descriptor(directory)
    return hashlib.sha256(raw).hexdigest()


def validate_archive_directories(archive: Path) -> tuple[Path, Path]:
    if not archive.is_absolute():
        raise CollectionError("OBSERVATION_PATH_UNSAFE")
    daily = archive / "daily"
    evidence = archive / "evidence"
    for path in (archive, daily, evidence):
        descriptor = safe_directory_descriptor(path)
        gate.close_descriptor(descriptor)
    return daily, evidence


def directory_names(path: Path) -> set[str]:
    descriptor = safe_directory_descriptor(path)
    try:
        with os.scandir(descriptor) as iterator:
            return {entry.name for entry in iterator}
    except OSError as exc:
        raise CollectionError("OBSERVATION_DIRECTORY_UNAVAILABLE") from exc
    finally:
        gate.close_descriptor(descriptor)


def collector_evidence(
    result: CollectorResult,
    policy: QueryPolicy,
    backend_policy_sha256: str,
    collection_window_start: datetime,
    collection_window_end: datetime,
    collected_at: datetime,
    manifest: dict[str, Any],
    incident_evidence_sha256: str | None,
) -> dict[str, Any]:
    return {
        "backend_policy_sha256": backend_policy_sha256,
        "collected_at": collected_at.astimezone(timezone.utc).isoformat(),
        "collection_window_end": collection_window_end.isoformat(),
        "collection_window_start": collection_window_start.isoformat(),
        "fixed_query_sha256": policy.digest,
        "incident_evidence_sha256": incident_evidence_sha256,
        "manifest": manifest,
        "response_sha256": dict(sorted(result.response_hashes.items())),
        "schema_version": EVIDENCE_SCHEMA,
    }


def sign_evidence(document: dict[str, Any], key: bytes) -> dict[str, Any]:
    signed = dict(document)
    signed["hmac_sha256"] = hmac.new(key, gate.canonical_bytes(document), hashlib.sha256).hexdigest()
    return signed


def load_incidents(path: Path, window_start: datetime, window_end: datetime) -> tuple[list[str], str]:
    document, digest = load_canonical(path)
    require_exact_fields(document, INCIDENT_FIELDS)
    if document.get("schema_version") != INCIDENT_SCHEMA:
        raise CollectionError("OBSERVATION_INCIDENT_EVIDENCE_INVALID")
    try:
        incident_start = gate.parse_timestamp(document.get("window_start"))
        incident_end = gate.parse_timestamp(document.get("window_end"))
    except gate.VerificationError as exc:
        raise CollectionError("OBSERVATION_INCIDENT_EVIDENCE_INVALID") from exc
    if incident_start != window_start or incident_end != window_end:
        raise CollectionError("OBSERVATION_INCIDENT_EVIDENCE_INVALID")
    flags = document.get("incident_flags")
    if not isinstance(flags, list) or len(flags) > 32 or flags != sorted(set(flags)):
        raise CollectionError("OBSERVATION_INCIDENT_EVIDENCE_INVALID")
    if any(not isinstance(value, str) or gate.INCIDENT_CODE.fullmatch(value) is None for value in flags):
        raise CollectionError("OBSERVATION_INCIDENT_EVIDENCE_INVALID")
    return flags, digest


def next_midnight(now: datetime, zone: ZoneInfo) -> datetime:
    localized = now.astimezone(zone)
    try:
        return gate.unique_zoned_midnight(localized.date() + timedelta(days=1), zone)
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    except (OverflowError, OSError, ValueError) as exc:
        raise CollectionError("OBSERVATION_TIMEZONE_MIDNIGHT_INVALID") from exc


def validate_start_readiness(
    archive: Path,
    client: BackendClient,
    key_path: Path,
    *,
    timezone_name: str,
    image_digest_sha256: str,
    config_sha256: str,
    embedding_gate_sha256: str,
    query_path: Path = QUERY_PATH,
    threshold_path: Path = THRESHOLD_PATH,
    backend_trust_path: Path = BACKEND_TRUST_PATH,
    trust_path: Path = TRUST_PATH,
    now: datetime | None = None,
) -> StartReadiness:
    current_time = now or datetime.now(timezone.utc)
    if current_time.tzinfo is None or current_time.utcoffset() is None:
        raise CollectionError("OBSERVATION_NOW_INVALID")
    try:
        zone = ZoneInfo(timezone_name)
    except (ZoneInfoNotFoundError, ValueError) as exc:
        raise CollectionError("OBSERVATION_TIMEZONE_INVALID") from exc
    first_window_start = next_midnight(current_time, zone)
    try:
        gate.validate_midnight_horizon(first_window_start, zone)
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    daily, evidence_directory = validate_archive_directories(archive)
    if (
        directory_names(archive) != {"daily", "evidence"}
        or directory_names(daily)
        or directory_names(evidence_directory)
    ):
        raise CollectionError("OBSERVATION_ARCHIVE_NOT_EMPTY")
    policy = load_query_policy(query_path)
    thresholds = load_threshold_policy(threshold_path, now=current_time)
    _, backend_policy_digest = load_backend_trust_policy(backend_trust_path)
    if getattr(client, "backend_policy_digest", None) != backend_policy_digest:
        raise CollectionError("OBSERVATION_BACKEND_POLICY_MISMATCH")
    trust, trust_digest = load_trust_policy(trust_path)
    key = trusted_attestation_key(key_path, trust)
    require_hash(image_digest_sha256)
    require_hash(config_sha256)
    require_hash(embedding_gate_sha256)
    return StartReadiness(
        current_time,
        zone,
        policy,
        thresholds,
        backend_policy_digest,
        trust_digest,
        key,
    )


def create_start(
    archive: Path,
    client: BackendClient,
    key_path: Path,
    *,
    timezone_name: str,
    image_digest_sha256: str,
    config_sha256: str,
    embedding_gate_sha256: str,
    query_path: Path = QUERY_PATH,
    threshold_path: Path = THRESHOLD_PATH,
    backend_trust_path: Path = BACKEND_TRUST_PATH,
    trust_path: Path = TRUST_PATH,
    now: datetime | None = None,
) -> dict[str, Any]:
    readiness = validate_start_readiness(
        archive,
        client,
        key_path,
        timezone_name=timezone_name,
        image_digest_sha256=image_digest_sha256,
        config_sha256=config_sha256,
        embedding_gate_sha256=embedding_gate_sha256,
        query_path=query_path,
        threshold_path=threshold_path,
        backend_trust_path=backend_trust_path,
        trust_path=trust_path,
        now=now,
    )
    current_time = readiness.current_time
    zone = readiness.zone
    policy = readiness.policy
    thresholds = readiness.thresholds
    backend_policy_digest = readiness.backend_policy_digest
    trust_digest = readiness.trust_digest
    key = readiness.key
    evidence_directory = archive / "evidence"
    probe_end = current_time.astimezone(zone)
    probe_start = probe_end - timedelta(seconds=policy.visibility_probe_seconds)
    collected = collect_queries(client, policy, probe_start, probe_end, start_probe=True)
    collected_at = current_time if now is not None else datetime.now(timezone.utc)
    if collected_at.astimezone(zone) >= next_midnight(current_time, zone):
        raise CollectionError("OBSERVATION_START_COLLECTION_LATE")
    aggregates: dict[str, int | bool] = {
        "collector_metrics_api_visible": query_truth(collected, "metrics_api_visible"),
        "collector_metrics_worker_visible": query_truth(collected, "metrics_worker_visible"),
        "collector_traces_api_visible": collected.values.get("traces_api_visible") is True,
        "collector_traces_worker_visible": collected.values.get("traces_worker_visible") is True,
        "telemetry_required_api": query_truth(collected, "telemetry_api_required"),
        "telemetry_required_worker": query_truth(collected, "telemetry_worker_required"),
    }
    if not all(bool(value) for value in aggregates.values()):
        raise CollectionError("OBSERVATION_START_GATE_NOT_SATISFIED")
    image_digest_sha256 = require_hash(image_digest_sha256)
    config_sha256 = require_hash(config_sha256)
    embedding_gate_sha256 = require_hash(embedding_gate_sha256)
    manifest_binding = {
        "attestation_policy_sha256": trust_digest,
        "backend_policy_sha256": backend_policy_digest,
        "collector_metrics_api_visible": aggregates["collector_metrics_api_visible"],
        "collector_metrics_worker_visible": aggregates["collector_metrics_worker_visible"],
        "collector_traces_api_visible": aggregates["collector_traces_api_visible"],
        "collector_traces_worker_visible": aggregates["collector_traces_worker_visible"],
        "config_sha256": config_sha256,
        "embedding_gate_sha256": embedding_gate_sha256,
        "fixed_query_sha256": policy.digest,
        "image_digest_sha256": image_digest_sha256,
        "reviewer_role": thresholds.reviewer_role,
        "schema_version": gate.START_SCHEMA,
        "telemetry_required_api": aggregates["telemetry_required_api"],
        "telemetry_required_worker": aggregates["telemetry_required_worker"],
        "threshold_table_sha256": thresholds.digest,
        "thresholds": thresholds.thresholds,
        "timezone": timezone_name,
        "window_start": next_midnight(current_time, zone).isoformat(),
    }
    evidence = sign_evidence(
        collector_evidence(
            collected,
            policy,
            backend_policy_digest,
            probe_start,
            probe_end,
            collected_at,
            manifest_binding,
            None,
        ),
        key,
    )
    evidence_digest = hashlib.sha256(gate.canonical_bytes(evidence)).hexdigest()
    document = {**manifest_binding, "collector_evidence_sha256": evidence_digest}
    gate.validate_start(document)
    write_new_canonical(evidence_directory / "start.json", evidence)
    write_new_canonical(archive / "start.json", document)
    return document


def load_existing_chain(archive: Path) -> tuple[dict[str, Any], str, ZoneInfo, datetime, list[tuple[dict[str, Any], str]]]:
    try:
        start, _, start_digest = gate.load_canonical_document(archive / "start.json")
        zone, expected_start, _ = gate.validate_start(start)
        days = gate.load_daily_manifests(archive / "daily")
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    previous = start_digest
    for index, (document, digest) in enumerate(days):
        window_start, _ = gate.validate_day(document, zone)
        expected = expected_start + timedelta(days=index)
        if window_start != expected or document["previous_manifest_sha256"] != previous:
            raise CollectionError("OBSERVATION_HASH_CHAIN_INVALID")
        previous = digest
    return start, previous, zone, expected_start, days


def create_day(
    archive: Path,
    client: BackendClient,
    key_path: Path,
    *,
    image_digest_sha256: str,
    config_sha256: str,
    incident_path: Path,
    query_path: Path = QUERY_PATH,
    threshold_path: Path = THRESHOLD_PATH,
    backend_trust_path: Path = BACKEND_TRUST_PATH,
    trust_path: Path = TRUST_PATH,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = now or datetime.now(timezone.utc)
    if current_time.tzinfo is None or current_time.utcoffset() is None:
        raise CollectionError("OBSERVATION_NOW_INVALID")
    daily_directory, evidence_directory = validate_archive_directories(archive)
    start, previous_digest, zone, first_start, days = load_existing_chain(archive)
    if len(days) >= gate.MAX_DAILY_MANIFESTS:
        raise CollectionError("OBSERVATION_DAILY_COUNT_INVALID")
    window_start = first_start + timedelta(days=len(days))
    window_end = window_start + timedelta(days=1)
    if current_time.astimezone(window_end.tzinfo) < window_end:
        raise CollectionError("OBSERVATION_DAILY_WINDOW_INCOMPLETE")
    if current_time.astimezone(zone).date() != window_end.astimezone(zone).date():
        raise CollectionError("OBSERVATION_DAILY_COLLECTION_LATE")
    if require_hash(image_digest_sha256) != start["image_digest_sha256"] or require_hash(config_sha256) != start["config_sha256"]:
        raise CollectionError("OBSERVATION_RELEASE_DRIFT")
    policy = load_query_policy(query_path)
    thresholds = load_threshold_policy(threshold_path, now=current_time)
    _, backend_policy_digest = load_backend_trust_policy(backend_trust_path)
    if getattr(client, "backend_policy_digest", None) != backend_policy_digest:
        raise CollectionError("OBSERVATION_BACKEND_POLICY_MISMATCH")
    trust, trust_digest = load_trust_policy(trust_path)
    key = trusted_attestation_key(key_path, trust)
    if (
        policy.digest != start["fixed_query_sha256"]
        or thresholds.digest != start["threshold_table_sha256"]
        or backend_policy_digest != start["backend_policy_sha256"]
        or trust_digest != start["attestation_policy_sha256"]
    ):
        raise CollectionError("OBSERVATION_POLICY_DRIFT")
    try:
        gate.validate_evidence_archive(
            evidence_directory,
            start,
            [document for document, _ in days],
            key,
        )
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    flags, incident_digest = load_incidents(incident_path, window_start, window_end)
    collected = collect_queries(client, policy, window_start, window_end, start_probe=False)
    collected_at = current_time if now is not None else datetime.now(timezone.utc)
    if collected_at.astimezone(zone).date() != window_end.astimezone(zone).date():
        raise CollectionError("OBSERVATION_DAILY_COLLECTION_LATE")
    aggregates = aggregate_day(collected)
    date_name = window_start.date().isoformat() + ".json"
    manifest_binding = {
        **aggregates,
        "backend_policy_sha256": backend_policy_digest,
        "config_sha256": start["config_sha256"],
        "fixed_query_sha256": policy.digest,
        "image_digest_sha256": start["image_digest_sha256"],
        "incident_flags": flags,
        "previous_manifest_sha256": previous_digest,
        "reviewer_role": thresholds.reviewer_role,
        "schema_version": gate.DAY_SCHEMA,
        "threshold_table_sha256": thresholds.digest,
        "window_end": window_end.isoformat(),
        "window_start": window_start.isoformat(),
    }
    evidence = sign_evidence(
        collector_evidence(
            collected,
            policy,
            backend_policy_digest,
            window_start,
            window_end,
            collected_at,
            manifest_binding,
            incident_digest,
        ),
        key,
    )
    evidence_digest = hashlib.sha256(gate.canonical_bytes(evidence)).hexdigest()
    document = {**manifest_binding, "collector_evidence_sha256": evidence_digest}
    gate.validate_day(document, zone)
    write_new_canonical(evidence_directory / date_name, evidence)
    write_new_canonical(daily_directory / date_name, document)
    return document


def create_attestation(
    archive: Path,
    key_path: Path,
    *,
    query_path: Path = QUERY_PATH,
    threshold_path: Path = THRESHOLD_PATH,
    backend_trust_path: Path = BACKEND_TRUST_PATH,
    trust_path: Path = TRUST_PATH,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = now or datetime.now(timezone.utc)
    if current_time.tzinfo is None or current_time.utcoffset() is None:
        raise CollectionError("OBSERVATION_NOW_INVALID")
    validate_archive_directories(archive)
    start, _, _, _, days = load_existing_chain(archive)
    policy = load_query_policy(query_path)
    thresholds = load_threshold_policy(threshold_path, now=current_time)
    _, backend_policy_digest = load_backend_trust_policy(backend_trust_path)
    trust, trust_digest = load_trust_policy(trust_path)
    if (
        start["attestation_policy_sha256"] != trust_digest
        or start["fixed_query_sha256"] != policy.digest
        or start["threshold_table_sha256"] != thresholds.digest
        or start["backend_policy_sha256"] != backend_policy_digest
    ):
        raise CollectionError("OBSERVATION_POLICY_DRIFT")
    key = trusted_attestation_key(key_path, trust)
    try:
        gate.validate_evidence_archive(
            archive / "evidence",
            start,
            [document for document, _ in days],
            key,
        )
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    try:
        result = gate.verify_archive(
            archive / "start.json",
            archive / "daily",
            trust_policy_path=trust_path,
            backend_trust_policy_path=backend_trust_path,
            now=current_time,
        )
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    if result["status"] != "incomplete" or result["reasons"] != ["OBSERVATION_TRUSTED_ATTESTATION_REQUIRED"]:
        raise CollectionError("OBSERVATION_ARCHIVE_NOT_ATTESTABLE")
    signed = {
        "archive_sha256": result["archive_sha256"],
        "attestation_policy_sha256": trust_digest,
        "issued_at": current_time.astimezone(timezone.utc).isoformat(),
        "issuer_role": trust["issuer_role"],
        "schema_version": gate.ATTESTATION_SCHEMA,
    }
    document = dict(signed)
    document["hmac_sha256"] = hmac.new(key, gate.canonical_bytes(signed), hashlib.sha256).hexdigest()
    write_new_canonical(archive / "attestation.json", document)
    try:
        verified = gate.verify_archive(
            archive / "start.json",
            archive / "daily",
            attestation_path=archive / "attestation.json",
            attestation_key_path=key_path,
            evidence_directory=archive / "evidence",
            trust_policy_path=trust_path,
            backend_trust_policy_path=backend_trust_path,
            now=current_time,
        )
    except gate.VerificationError as exc:
        raise CollectionError(exc.code) from exc
    if verified["status"] != "passed":
        raise CollectionError("OBSERVATION_ATTESTATION_INVALID")
    return document


def backend_files_from_options(options: argparse.Namespace) -> BackendFiles:
    return BackendFiles(
        prometheus_url=options.prometheus_url_file,
        prometheus_token=options.prometheus_bearer_token_file,
        tempo_url=options.tempo_url_file,
        tempo_token=options.tempo_bearer_token_file,
    )


def add_backend_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--prometheus-url-file", required=True, type=Path)
    parser.add_argument("--prometheus-bearer-token-file", required=True, type=Path)
    parser.add_argument("--tempo-url-file", required=True, type=Path)
    parser.add_argument("--tempo-bearer-token-file", required=True, type=Path)


def add_start_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--archive-dir", required=True, type=Path)
    parser.add_argument("--timezone", required=True)
    parser.add_argument("--image-digest-sha256", required=True)
    parser.add_argument("--config-sha256", required=True)
    parser.add_argument("--embedding-gate-sha256", required=True)
    parser.add_argument("--attestation-key-file", required=True, type=Path)
    add_backend_arguments(parser)


def parse_arguments(arguments: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="operation", required=True)
    preflight = subparsers.add_parser("preflight")
    add_start_arguments(preflight)
    start = subparsers.add_parser("start")
    add_start_arguments(start)
    day = subparsers.add_parser("day")
    day.add_argument("--archive-dir", required=True, type=Path)
    day.add_argument("--image-digest-sha256", required=True)
    day.add_argument("--config-sha256", required=True)
    day.add_argument("--incident-evidence", required=True, type=Path)
    day.add_argument("--attestation-key-file", required=True, type=Path)
    add_backend_arguments(day)
    attest = subparsers.add_parser("attest")
    attest.add_argument("--archive-dir", required=True, type=Path)
    attest.add_argument("--attestation-key-file", required=True, type=Path)
    return parser.parse_args(arguments)


def main(
    arguments: list[str] | None = None,
    *,
    query_path: Path = QUERY_PATH,
    threshold_path: Path = THRESHOLD_PATH,
    backend_trust_path: Path = BACKEND_TRUST_PATH,
    trust_path: Path = TRUST_PATH,
    now: datetime | None = None,
) -> int:
    options = parse_arguments(sys.argv[1:] if arguments is None else arguments)
    try:
        if options.operation in {"preflight", "start"}:
            start_client = BackendClient(
                backend_files_from_options(options),
                backend_trust_path=backend_trust_path,
            )
        if options.operation == "preflight":
            validate_start_readiness(
                options.archive_dir,
                start_client,
                options.attestation_key_file,
                timezone_name=options.timezone,
                image_digest_sha256=options.image_digest_sha256,
                config_sha256=options.config_sha256,
                embedding_gate_sha256=options.embedding_gate_sha256,
                query_path=query_path,
                threshold_path=threshold_path,
                backend_trust_path=backend_trust_path,
                trust_path=trust_path,
                now=now,
            )
        elif options.operation == "start":
            create_start(
                options.archive_dir,
                start_client,
                options.attestation_key_file,
                timezone_name=options.timezone,
                image_digest_sha256=options.image_digest_sha256,
                config_sha256=options.config_sha256,
                embedding_gate_sha256=options.embedding_gate_sha256,
                query_path=query_path,
                threshold_path=threshold_path,
                backend_trust_path=backend_trust_path,
                trust_path=trust_path,
                now=now,
            )
        elif options.operation == "day":
            create_day(
                options.archive_dir,
                BackendClient(
                    backend_files_from_options(options),
                    backend_trust_path=backend_trust_path,
                ),
                options.attestation_key_file,
                image_digest_sha256=options.image_digest_sha256,
                config_sha256=options.config_sha256,
                incident_path=options.incident_evidence,
                query_path=query_path,
                threshold_path=threshold_path,
                backend_trust_path=backend_trust_path,
                trust_path=trust_path,
                now=now,
            )
        elif options.operation == "attest":
            create_attestation(
                options.archive_dir,
                options.attestation_key_file,
                query_path=query_path,
                threshold_path=threshold_path,
                backend_trust_path=backend_trust_path,
                trust_path=trust_path,
                now=now,
            )
        else:
            raise CollectionError("OBSERVATION_OPERATION_INVALID")
    except (CollectionError, gate.VerificationError) as exc:
        code = exc.code if hasattr(exc, "code") else "OBSERVATION_COLLECTION_FAILED"
        print(f"eino-stable-observation-collect: {code}", file=sys.stderr)
        return 1
    except Exception:
        print("eino-stable-observation-collect: OBSERVATION_COLLECTION_FAILED", file=sys.stderr)
        return 1
    print(gate.canonical_bytes({"operation": options.operation, "status": "completed"}).decode("ascii"), end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
