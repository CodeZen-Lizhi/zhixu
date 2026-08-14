#!/usr/bin/env python3
"""Verify attested Eino production stability evidence."""

from __future__ import annotations

import argparse
from datetime import date, datetime, time, timedelta, timezone
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import stat
import sys
from typing import Any
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError


START_SCHEMA = "eino-stable-observation-start/v2"
DAY_SCHEMA = "eino-stable-observation-day/v2"
ATTESTATION_SCHEMA = "eino-stable-observation-attestation/v2"
TRUST_SCHEMA = "eino-stable-observation-trust/v2"
BACKEND_TRUST_SCHEMA = "eino-stable-observation-backend-trust/v1"
EVIDENCE_SCHEMA = "eino-stable-observation-collector-evidence/v2"
VERDICT_SCHEMA = "eino-stable-observation-verdict/v2"
TRUST_POLICY_PATH = Path(__file__).with_name("eino_stable_observation_trust.json")
BACKEND_TRUST_POLICY_PATH = Path(__file__).with_name("eino_stable_observation_backend_trust.json")
MAX_DOCUMENT_BYTES = 64 * 1024
MAX_DAILY_MANIFESTS = 366
MAX_KEY_BYTES = 128
MIN_ATTESTATION_KEY_BYTES = 32
HEX_SHA256 = re.compile(r"^[0-9a-f]{64}$")
ROLE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}$")
INCIDENT_CODE = re.compile(r"^[A-Z][A-Z0-9_]{0,63}$")


class VerificationError(ValueError):
    """A stable, privacy-safe observation evidence failure."""

    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


def reject_duplicate_fields(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise VerificationError("OBSERVATION_DUPLICATE_FIELD")
        result[key] = value
    return result


def reject_non_finite(value: str) -> None:
    del value
    raise VerificationError("OBSERVATION_NON_FINITE_NUMBER")


def close_descriptor(descriptor: int) -> None:
    try:
        os.close(descriptor)
    except OSError:
        pass


def open_directory_no_symlinks(path: Path) -> int:
    flags = (
        os.O_RDONLY
        | getattr(os, "O_CLOEXEC", 0)
        | getattr(os, "O_DIRECTORY", 0)
        | getattr(os, "O_NOFOLLOW", 0)
    )
    descriptor: int | None = None
    try:
        descriptor = os.open("/" if path.is_absolute() else ".", flags)
        parts = path.parts[1:] if path.is_absolute() else path.parts
        for part in parts:
            if part in {"", ".", ".."}:
                raise VerificationError("OBSERVATION_PATH_UNSAFE")
            child = os.open(part, flags, dir_fd=descriptor)
            close_descriptor(descriptor)
            descriptor = child
        metadata = os.fstat(descriptor)
        if not stat.S_ISDIR(metadata.st_mode):
            raise VerificationError("OBSERVATION_PATH_UNSAFE")
        return descriptor
    except VerificationError:
        if descriptor is not None:
            close_descriptor(descriptor)
        raise
    except OSError as exc:
        if descriptor is not None:
            close_descriptor(descriptor)
        raise VerificationError("OBSERVATION_PATH_UNSAFE") from exc


def read_regular_file_at(directory_descriptor: int, name: str, maximum_bytes: int) -> tuple[bytes, int]:
    if not name or name in {".", ".."} or Path(name).name != name:
        raise VerificationError("OBSERVATION_PATH_UNSAFE")

    flags = os.O_RDONLY | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(name, flags, dir_fd=directory_descriptor)
    except OSError as exc:
        raise VerificationError("OBSERVATION_FILE_UNAVAILABLE") from exc
    try:
        try:
            opened = os.fstat(descriptor)
        except OSError as exc:
            raise VerificationError("OBSERVATION_FILE_UNAVAILABLE") from exc
        if not stat.S_ISREG(opened.st_mode):
            raise VerificationError("OBSERVATION_FILE_UNSAFE")
        if opened.st_size < 1 or opened.st_size > maximum_bytes:
            raise VerificationError("OBSERVATION_FILE_SIZE_INVALID")
        chunks: list[bytes] = []
        remaining = maximum_bytes + 1
        try:
            while remaining > 0:
                chunk = os.read(descriptor, min(remaining, 64 * 1024))
                if not chunk:
                    break
                chunks.append(chunk)
                remaining -= len(chunk)
        except OSError as exc:
            raise VerificationError("OBSERVATION_FILE_UNAVAILABLE") from exc
        raw = b"".join(chunks)
        if len(raw) != opened.st_size:
            raise VerificationError("OBSERVATION_FILE_CHANGED")
        if len(raw) > maximum_bytes:
            raise VerificationError("OBSERVATION_FILE_SIZE_INVALID")
        return raw, opened.st_mode
    finally:
        close_descriptor(descriptor)


def read_regular_file_with_mode(path: Path, maximum_bytes: int) -> tuple[bytes, int]:
    if not path.name:
        raise VerificationError("OBSERVATION_PATH_UNSAFE")
    parent = open_directory_no_symlinks(path.parent)
    try:
        return read_regular_file_at(parent, path.name, maximum_bytes)
    finally:
        close_descriptor(parent)


def read_regular_file(path: Path, maximum_bytes: int) -> bytes:
    raw, _ = read_regular_file_with_mode(path, maximum_bytes)
    return raw


def decode_document(raw: bytes) -> dict[str, Any]:
    try:
        text = raw.decode("utf-8", errors="strict")
        document = json.loads(
            text,
            object_pairs_hook=reject_duplicate_fields,
            parse_constant=reject_non_finite,
        )
    except VerificationError:
        raise
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError, RecursionError) as exc:
        raise VerificationError("OBSERVATION_JSON_INVALID") from exc
    if not isinstance(document, dict):
        raise VerificationError("OBSERVATION_JSON_OBJECT_REQUIRED")
    return document


def canonical_bytes(document: dict[str, Any]) -> bytes:
    try:
        encoded = json.dumps(
            document,
            allow_nan=False,
            ensure_ascii=True,
            separators=(",", ":"),
            sort_keys=True,
        )
    except (TypeError, ValueError, RecursionError) as exc:
        raise VerificationError("OBSERVATION_JSON_INVALID") from exc
    return encoded.encode("ascii") + b"\n"


def load_canonical_document(path: Path) -> tuple[dict[str, Any], bytes, str]:
    raw = read_regular_file(path, MAX_DOCUMENT_BYTES)
    return decode_canonical_document(raw)


def decode_canonical_document(raw: bytes) -> tuple[dict[str, Any], bytes, str]:
    document = decode_document(raw)
    canonical = canonical_bytes(document)
    if not hmac.compare_digest(raw, canonical):
        raise VerificationError("OBSERVATION_JSON_NOT_CANONICAL")
    return document, raw, hashlib.sha256(raw).hexdigest()


def require_exact_fields(document: dict[str, Any], expected: set[str]) -> None:
    if set(document) != expected:
        raise VerificationError("OBSERVATION_FIELDS_INVALID")


def require_schema(document: dict[str, Any], expected: str) -> None:
    if document.get("schema_version") != expected:
        raise VerificationError("OBSERVATION_SCHEMA_INVALID")


def require_bool(document: dict[str, Any], field: str) -> bool:
    value = document[field]
    if type(value) is not bool:
        raise VerificationError("OBSERVATION_FIELD_TYPE_INVALID")
    return value


def require_integer(
    document: dict[str, Any],
    field: str,
    *,
    minimum: int = 0,
    maximum: int = 2**63 - 1,
) -> int:
    value = document[field]
    if type(value) is not int or value < minimum or value > maximum:
        raise VerificationError("OBSERVATION_FIELD_VALUE_INVALID")
    return value


def require_hash(document: dict[str, Any], field: str) -> str:
    value = document[field]
    if not isinstance(value, str) or HEX_SHA256.fullmatch(value) is None:
        raise VerificationError("OBSERVATION_HASH_INVALID")
    return value


def require_role(document: dict[str, Any], field: str) -> str:
    value = document[field]
    if not isinstance(value, str) or ROLE.fullmatch(value) is None:
        raise VerificationError("OBSERVATION_ROLE_INVALID")
    return value


def parse_timestamp(value: Any) -> datetime:
    if not isinstance(value, str) or len(value) > 40:
        raise VerificationError("OBSERVATION_TIMESTAMP_INVALID")
    normalized = value[:-1] + "+00:00" if value.endswith("Z") else value
    try:
        parsed = datetime.fromisoformat(normalized)
    except ValueError as exc:
        raise VerificationError("OBSERVATION_TIMESTAMP_INVALID") from exc
    # Keep timezone conversion inside datetime's representable range for any
    # valid UTC offset or IANA observation zone.
    if parsed.tzinfo is None or parsed.utcoffset() is None or not 2 <= parsed.year <= 9998:
        raise VerificationError("OBSERVATION_TIMESTAMP_INVALID")
    return parsed


def unique_zoned_midnight(day: date, zone: ZoneInfo) -> datetime:
    naive_midnight = datetime.combine(day, time.min)
    candidates: list[datetime] = []
    try:
        for fold in (0, 1):
            candidate = naive_midnight.replace(tzinfo=zone, fold=fold)
            round_trip = candidate.astimezone(timezone.utc).astimezone(zone)
            if round_trip.replace(tzinfo=None) == naive_midnight and round_trip.fold == fold:
                candidates.append(candidate)
    except (OverflowError, OSError, ValueError) as exc:
        raise VerificationError("OBSERVATION_TIMEZONE_MIDNIGHT_INVALID") from exc
    if len(candidates) != 1:
        raise VerificationError("OBSERVATION_TIMEZONE_MIDNIGHT_INVALID")
    return candidates[0]


def validate_midnight_horizon(window_start: datetime, zone: ZoneInfo) -> None:
    try:
        for offset in range(MAX_DAILY_MANIFESTS + 1):
            unique_zoned_midnight(window_start.date() + timedelta(days=offset), zone)
    except VerificationError:
        raise
    except (OverflowError, OSError, ValueError) as exc:
        raise VerificationError("OBSERVATION_TIMEZONE_MIDNIGHT_INVALID") from exc


def parse_zoned_midnight(value: Any, zone: ZoneInfo) -> datetime:
    parsed = parse_timestamp(value)
    try:
        localized = parsed.astimezone(zone)
    except (OverflowError, OSError, ValueError) as exc:
        raise VerificationError("OBSERVATION_DAY_BOUNDARY_INVALID") from exc
    if localized.hour != 0 or localized.minute != 0 or localized.second != 0 or localized.microsecond != 0:
        raise VerificationError("OBSERVATION_DAY_BOUNDARY_INVALID")
    if localized.utcoffset() != parsed.utcoffset():
        raise VerificationError("OBSERVATION_TIMEZONE_OFFSET_INVALID")
    canonical_midnight = unique_zoned_midnight(localized.date(), zone)
    if canonical_midnight.astimezone(timezone.utc) != localized.astimezone(timezone.utc):
        raise VerificationError("OBSERVATION_TIMEZONE_MIDNIGHT_INVALID")
    return localized


START_FIELDS = {
    "schema_version",
    "timezone",
    "window_start",
    "image_digest_sha256",
    "config_sha256",
    "embedding_gate_sha256",
    "attestation_policy_sha256",
    "backend_policy_sha256",
    "collector_evidence_sha256",
    "telemetry_required_api",
    "telemetry_required_worker",
    "collector_metrics_api_visible",
    "collector_metrics_worker_visible",
    "collector_traces_api_visible",
    "collector_traces_worker_visible",
    "threshold_table_sha256",
    "fixed_query_sha256",
    "thresholds",
    "reviewer_role",
}

THRESHOLD_FIELDS = {
    "failure_rate_ppm_max",
    "draft_degradation_delta_max",
    "first_token_p95_ms_max",
    "completion_p95_ms_max",
}

DAY_FIELDS = {
    "schema_version",
    "window_start",
    "window_end",
    "image_digest_sha256",
    "config_sha256",
    "telemetry_required_api",
    "telemetry_required_worker",
    "collector_metrics_api_visible",
    "collector_metrics_worker_visible",
    "collector_traces_api_visible",
    "collector_traces_worker_visible",
    "rag_v2_non_replay_terminal_delta",
    "failure_rate_ppm",
    "draft_degradation_delta",
    "first_token_p95_ms",
    "completion_p95_ms",
    "threshold_table_sha256",
    "fixed_query_sha256",
    "backend_policy_sha256",
    "collector_evidence_sha256",
    "incident_flags",
    "previous_manifest_sha256",
    "reviewer_role",
}

ATTESTATION_FIELDS = {
    "schema_version",
    "archive_sha256",
    "attestation_policy_sha256",
    "issued_at",
    "issuer_role",
    "hmac_sha256",
}

TRUST_FIELDS = {
    "schema_version",
    "status",
    "attestation_key_sha256",
    "issuer_role",
}
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
BACKEND_TRUST_HASH_FIELDS = (
    "prometheus_token_file_sha256",
    "prometheus_url_file_sha256",
    "prometheus_url_sha256",
    "tempo_token_file_sha256",
    "tempo_url_file_sha256",
    "tempo_url_sha256",
)
EVIDENCE_FIELDS = {
    "backend_policy_sha256",
    "collected_at",
    "collection_window_end",
    "collection_window_start",
    "fixed_query_sha256",
    "hmac_sha256",
    "incident_evidence_sha256",
    "manifest",
    "response_sha256",
    "schema_version",
}
START_PROMETHEUS_EVIDENCE_FIELDS = {
    "metrics_api_visible",
    "metrics_worker_visible",
    "telemetry_api_required",
    "telemetry_worker_required",
}
DAY_PROMETHEUS_EVIDENCE_FIELDS = START_PROMETHEUS_EVIDENCE_FIELDS | {
    "answer_failure_count",
    "answer_total_count",
    "completion_p95_ms",
    "draft_degradation_delta",
    "first_token_p95_ms",
    "rag_v2_terminal_delta",
}
TEMPO_EVIDENCE_FIELDS = {"traces_api_visible", "traces_worker_visible"}


def validate_start(document: dict[str, Any]) -> tuple[ZoneInfo, datetime, dict[str, int]]:
    require_exact_fields(document, START_FIELDS)
    require_schema(document, START_SCHEMA)
    timezone_name = document["timezone"]
    if not isinstance(timezone_name, str) or len(timezone_name) > 64:
        raise VerificationError("OBSERVATION_TIMEZONE_INVALID")
    try:
        zone = ZoneInfo(timezone_name)
    except (ZoneInfoNotFoundError, ValueError) as exc:
        raise VerificationError("OBSERVATION_TIMEZONE_INVALID") from exc
    window_start = parse_zoned_midnight(document["window_start"], zone)
    validate_midnight_horizon(window_start, zone)
    for field in (
        "image_digest_sha256",
        "config_sha256",
        "embedding_gate_sha256",
        "attestation_policy_sha256",
        "backend_policy_sha256",
        "collector_evidence_sha256",
        "threshold_table_sha256",
        "fixed_query_sha256",
    ):
        require_hash(document, field)
    for field in (
        "telemetry_required_api",
        "telemetry_required_worker",
        "collector_metrics_api_visible",
        "collector_metrics_worker_visible",
        "collector_traces_api_visible",
        "collector_traces_worker_visible",
    ):
        require_bool(document, field)
    require_role(document, "reviewer_role")
    thresholds = document["thresholds"]
    if not isinstance(thresholds, dict):
        raise VerificationError("OBSERVATION_FIELD_TYPE_INVALID")
    require_exact_fields(thresholds, THRESHOLD_FIELDS)
    validated_thresholds = {
        "failure_rate_ppm_max": require_integer(thresholds, "failure_rate_ppm_max", maximum=1_000_000),
        "draft_degradation_delta_max": require_integer(thresholds, "draft_degradation_delta_max"),
        "first_token_p95_ms_max": require_integer(thresholds, "first_token_p95_ms_max", minimum=1),
        "completion_p95_ms_max": require_integer(thresholds, "completion_p95_ms_max", minimum=1),
    }
    return zone, window_start, validated_thresholds


def validate_day(document: dict[str, Any], zone: ZoneInfo) -> tuple[datetime, datetime]:
    require_exact_fields(document, DAY_FIELDS)
    require_schema(document, DAY_SCHEMA)
    window_start = parse_zoned_midnight(document["window_start"], zone)
    window_end = parse_zoned_midnight(document["window_end"], zone)
    if window_end.date() != window_start.date() + timedelta(days=1):
        raise VerificationError("OBSERVATION_DAY_BOUNDARY_INVALID")
    for field in (
        "image_digest_sha256",
        "config_sha256",
        "backend_policy_sha256",
        "threshold_table_sha256",
        "fixed_query_sha256",
        "collector_evidence_sha256",
        "previous_manifest_sha256",
    ):
        require_hash(document, field)
    for field in (
        "telemetry_required_api",
        "telemetry_required_worker",
        "collector_metrics_api_visible",
        "collector_metrics_worker_visible",
        "collector_traces_api_visible",
        "collector_traces_worker_visible",
    ):
        require_bool(document, field)
    require_integer(document, "rag_v2_non_replay_terminal_delta")
    require_integer(document, "failure_rate_ppm", maximum=1_000_000)
    require_integer(document, "draft_degradation_delta")
    require_integer(document, "first_token_p95_ms")
    require_integer(document, "completion_p95_ms")
    incidents = document["incident_flags"]
    if (
        not isinstance(incidents, list)
        or len(incidents) > 32
        or incidents != sorted(set(incidents))
    ):
        raise VerificationError("OBSERVATION_INCIDENT_FLAGS_INVALID")
    if any(not isinstance(value, str) or INCIDENT_CODE.fullmatch(value) is None for value in incidents):
        raise VerificationError("OBSERVATION_INCIDENT_FLAGS_INVALID")
    require_role(document, "reviewer_role")
    return window_start, window_end


def archive_digest(manifest_hashes: list[str]) -> str:
    payload = "eino-stable-observation-archive/v2\n" + "".join(f"{value}\n" for value in manifest_hashes)
    return hashlib.sha256(payload.encode("ascii")).hexdigest()


def read_attestation_key(path: Path) -> bytes:
    raw, mode = read_regular_file_with_mode(path, MAX_KEY_BYTES)
    if stat.S_IMODE(mode) not in {0o400, 0o600}:
        raise VerificationError("OBSERVATION_ATTESTATION_KEY_PERMISSIONS_INVALID")
    if len(raw) < MIN_ATTESTATION_KEY_BYTES:
        raise VerificationError("OBSERVATION_ATTESTATION_KEY_INVALID")
    return raw


def load_trust_policy(path: Path) -> tuple[dict[str, Any], str]:
    document, _, digest = load_canonical_document(path)
    require_exact_fields(document, TRUST_FIELDS)
    require_schema(document, TRUST_SCHEMA)
    if document["status"] not in {"unconfigured", "configured"}:
        raise VerificationError("OBSERVATION_TRUST_POLICY_INVALID")
    require_role(document, "issuer_role")
    key_sha256 = document["attestation_key_sha256"]
    if document["status"] == "unconfigured":
        if key_sha256 is not None:
            raise VerificationError("OBSERVATION_TRUST_POLICY_INVALID")
    elif not isinstance(key_sha256, str) or HEX_SHA256.fullmatch(key_sha256) is None:
        raise VerificationError("OBSERVATION_TRUST_POLICY_INVALID")
    return document, digest


def load_backend_trust_policy(path: Path) -> tuple[dict[str, Any], str]:
    document, _, digest = load_canonical_document(path)
    require_exact_fields(document, BACKEND_TRUST_FIELDS)
    require_schema(document, BACKEND_TRUST_SCHEMA)
    if document["backend"] != "prometheus-tempo" or document["status"] not in {
        "unconfigured",
        "configured",
    }:
        raise VerificationError("OBSERVATION_BACKEND_POLICY_INVALID")
    for field in BACKEND_TRUST_HASH_FIELDS:
        value = document[field]
        if document["status"] == "unconfigured":
            if value is not None:
                raise VerificationError("OBSERVATION_BACKEND_POLICY_INVALID")
        elif not isinstance(value, str) or HEX_SHA256.fullmatch(value) is None:
            raise VerificationError("OBSERVATION_BACKEND_POLICY_INVALID")
    return document, digest


def validate_attestation(
    path: Path,
    key_path: Path,
    expected_archive_sha256: str,
    expected_policy_sha256: str,
    last_window_end: datetime,
    now: datetime,
    trust_policy_path: Path,
) -> tuple[bool, bytes]:
    trust_policy, trust_policy_sha256 = load_trust_policy(trust_policy_path)
    if trust_policy_sha256 != expected_policy_sha256:
        raise VerificationError("OBSERVATION_TRUST_POLICY_MISMATCH")
    document, _, _ = load_canonical_document(path)
    require_exact_fields(document, ATTESTATION_FIELDS)
    require_schema(document, ATTESTATION_SCHEMA)
    require_hash(document, "archive_sha256")
    require_hash(document, "attestation_policy_sha256")
    require_hash(document, "hmac_sha256")
    require_role(document, "issuer_role")
    if document["issuer_role"] != trust_policy["issuer_role"]:
        raise VerificationError("OBSERVATION_ATTESTATION_ISSUER_MISMATCH")
    issued_at = parse_timestamp(document["issued_at"])
    if document["archive_sha256"] != expected_archive_sha256:
        raise VerificationError("OBSERVATION_ATTESTATION_ARCHIVE_MISMATCH")
    if document["attestation_policy_sha256"] != expected_policy_sha256:
        raise VerificationError("OBSERVATION_ATTESTATION_POLICY_MISMATCH")
    if issued_at < last_window_end or issued_at > now + timedelta(minutes=5):
        raise VerificationError("OBSERVATION_ATTESTATION_TIME_INVALID")
    signed = dict(document)
    provided_hmac = signed.pop("hmac_sha256")
    key = read_attestation_key(key_path)
    if (
        trust_policy["status"] == "configured"
        and hashlib.sha256(key).hexdigest() != trust_policy["attestation_key_sha256"]
    ):
        raise VerificationError("OBSERVATION_ATTESTATION_KEY_NOT_TRUSTED")
    expected_hmac = hmac.new(key, canonical_bytes(signed), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(provided_hmac, expected_hmac):
        raise VerificationError("OBSERVATION_ATTESTATION_INVALID")
    return trust_policy["status"] == "configured", key


def load_daily_manifests(directory: Path) -> list[tuple[dict[str, Any], str]]:
    try:
        descriptor = open_directory_no_symlinks(directory)
    except VerificationError as exc:
        raise VerificationError("OBSERVATION_DAILY_DIRECTORY_UNSAFE") from exc
    try:
        names: list[str] = []
        with os.scandir(descriptor) as iterator:
            for entry in iterator:
                if len(names) >= MAX_DAILY_MANIFESTS:
                    raise VerificationError("OBSERVATION_DAILY_COUNT_INVALID")
                if entry.name.startswith(".") or Path(entry.name).suffix != ".json" or not entry.is_file(follow_symlinks=False):
                    raise VerificationError("OBSERVATION_DAILY_ENTRY_INVALID")
                names.append(entry.name)
        manifests: list[tuple[dict[str, Any], str]] = []
        for name in sorted(names):
            raw, _ = read_regular_file_at(descriptor, name, MAX_DOCUMENT_BYTES)
            document, _, digest = decode_canonical_document(raw)
            manifests.append((document, digest))
        return manifests
    except VerificationError:
        raise
    except OSError as exc:
        raise VerificationError("OBSERVATION_DAILY_DIRECTORY_UNAVAILABLE") from exc
    finally:
        close_descriptor(descriptor)


def manifest_binding(manifest: dict[str, Any]) -> dict[str, Any]:
    binding = dict(manifest)
    if not isinstance(binding.pop("collector_evidence_sha256", None), str):
        raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")
    return binding


def validate_collector_evidence(
    document: dict[str, Any],
    key: bytes,
    *,
    expected_manifest: dict[str, Any],
    start_probe: bool,
    zone: ZoneInfo,
) -> None:
    try:
        require_exact_fields(document, EVIDENCE_FIELDS)
        require_schema(document, EVIDENCE_SCHEMA)
        if (
            document["backend_policy_sha256"] != expected_manifest["backend_policy_sha256"]
            or document["fixed_query_sha256"] != expected_manifest["fixed_query_sha256"]
            or document["manifest"] != expected_manifest
        ):
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")
        collection_start = parse_timestamp(document["collection_window_start"])
        collection_end = parse_timestamp(document["collection_window_end"])
        collected_at = parse_timestamp(document["collected_at"])
        if collection_start >= collection_end or collected_at < collection_end:
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")
        if start_probe:
            manifest_start = parse_timestamp(expected_manifest["window_start"])
            if (
                collection_end > manifest_start
                or collected_at >= manifest_start
                or collection_end - collection_start > timedelta(hours=1)
            ):
                raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")
        elif (
            document["collection_window_start"] != expected_manifest["window_start"]
            or document["collection_window_end"] != expected_manifest["window_end"]
            or collected_at.astimezone(zone).date() != collection_end.astimezone(zone).date()
        ):
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")

        incident_digest = document["incident_evidence_sha256"]
        if (start_probe and incident_digest is not None) or (
            not start_probe
            and (not isinstance(incident_digest, str) or HEX_SHA256.fullmatch(incident_digest) is None)
        ):
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")

        prometheus_fields = (
            START_PROMETHEUS_EVIDENCE_FIELDS if start_probe else DAY_PROMETHEUS_EVIDENCE_FIELDS
        )
        expected_response_fields = {"prometheus." + field for field in prometheus_fields} | {
            "tempo." + field for field in TEMPO_EVIDENCE_FIELDS
        }
        response_hashes = document["response_sha256"]
        if not isinstance(response_hashes, dict) or set(response_hashes) != expected_response_fields:
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")
        if any(
            not isinstance(value, str) or HEX_SHA256.fullmatch(value) is None
            for value in response_hashes.values()
        ):
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")

        provided_hmac = document["hmac_sha256"]
        if not isinstance(provided_hmac, str) or HEX_SHA256.fullmatch(provided_hmac) is None:
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")
        unsigned = dict(document)
        unsigned.pop("hmac_sha256")
        expected_hmac = hmac.new(key, canonical_bytes(unsigned), hashlib.sha256).hexdigest()
        if not hmac.compare_digest(provided_hmac, expected_hmac):
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")
    except VerificationError as exc:
        if exc.code == "OBSERVATION_COLLECTOR_EVIDENCE_INVALID":
            raise
        raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID") from exc


def validate_evidence_archive(
    evidence_directory: Path,
    start: dict[str, Any],
    days: list[dict[str, Any]],
    key: bytes,
) -> None:
    zone, _, _ = validate_start(start)
    expected: dict[str, tuple[dict[str, Any], bool]] = {
        "start.json": (start, True),
    }
    for document in days:
        name = parse_timestamp(document["window_start"]).date().isoformat() + ".json"
        expected[name] = (document, False)

    try:
        descriptor = open_directory_no_symlinks(evidence_directory)
    except VerificationError as exc:
        raise VerificationError("OBSERVATION_EVIDENCE_DIRECTORY_INVALID") from exc
    try:
        names: set[str] = set()
        with os.scandir(descriptor) as iterator:
            for entry in iterator:
                if entry.name.startswith(".") or not entry.is_file(follow_symlinks=False):
                    raise VerificationError("OBSERVATION_EVIDENCE_DIRECTORY_INVALID")
                names.add(entry.name)
        if names != set(expected):
            raise VerificationError("OBSERVATION_EVIDENCE_DIRECTORY_INVALID")
        for name, (manifest, start_probe) in expected.items():
            raw, _ = read_regular_file_at(descriptor, name, MAX_DOCUMENT_BYTES)
            evidence, _, digest = decode_canonical_document(raw)
            if digest != manifest["collector_evidence_sha256"]:
                raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_INVALID")
            validate_collector_evidence(
                evidence,
                key,
                expected_manifest=manifest_binding(manifest),
                start_probe=start_probe,
                zone=zone,
            )
    except VerificationError:
        raise
    except OSError as exc:
        raise VerificationError("OBSERVATION_EVIDENCE_DIRECTORY_INVALID") from exc
    finally:
        close_descriptor(descriptor)


def gate_truths(document: dict[str, Any]) -> bool:
    return all(
        document[field]
        for field in (
            "telemetry_required_api",
            "telemetry_required_worker",
            "collector_metrics_api_visible",
            "collector_metrics_worker_visible",
            "collector_traces_api_visible",
            "collector_traces_worker_visible",
        )
    )


def verdict(status: str, reasons: list[str], days: int, outcomes: int, digest: str) -> dict[str, Any]:
    return {
        "archive_sha256": digest,
        "complete_days": days,
        "qualified_rag_v2_terminal_outcomes": outcomes,
        "reasons": sorted(set(reasons)),
        "schema_version": VERDICT_SCHEMA,
        "status": status,
    }


def verify_archive(
    start_path: Path,
    daily_directory: Path,
    *,
    attestation_path: Path | None = None,
    attestation_key_path: Path | None = None,
    evidence_directory: Path | None = None,
    trust_policy_path: Path = TRUST_POLICY_PATH,
    backend_trust_policy_path: Path = BACKEND_TRUST_POLICY_PATH,
    now: datetime | None = None,
) -> dict[str, Any]:
    current_time = now if now is not None else datetime.now(timezone.utc)
    if current_time.tzinfo is None or current_time.utcoffset() is None:
        raise VerificationError("OBSERVATION_NOW_INVALID")
    start, _, start_hash = load_canonical_document(start_path)
    zone, expected_start, thresholds = validate_start(start)
    backend_trust, backend_policy_sha256 = load_backend_trust_policy(backend_trust_policy_path)
    reasons: list[str] = []
    failed = False
    if backend_trust["status"] == "configured" and start["backend_policy_sha256"] != backend_policy_sha256:
        reasons.append("OBSERVATION_POLICY_DRIFT")
        failed = True
    if not gate_truths(start):
        reasons.append("START_GATE_NOT_SATISFIED")
        failed = True

    loaded_days: list[tuple[datetime, datetime, dict[str, Any], str]] = []
    for document, digest in load_daily_manifests(daily_directory):
        window_start, window_end = validate_day(document, zone)
        loaded_days.append((window_start, window_end, document, digest))
    loaded_days.sort(key=lambda value: value[0])

    manifest_hashes = [start_hash]
    previous_hash = start_hash
    qualified_outcomes = 0
    collector_evidence_hashes: set[str] = {start["collector_evidence_sha256"]}
    for index, (window_start, window_end, document, digest) in enumerate(loaded_days):
        expected_day_start = expected_start + timedelta(days=index)
        if window_start.date() != expected_day_start.date() or window_start.time() != expected_day_start.time():
            raise VerificationError("OBSERVATION_DAILY_WINDOW_NOT_CONTIGUOUS")
        if window_end > current_time.astimezone(zone):
            raise VerificationError("OBSERVATION_DAILY_WINDOW_INCOMPLETE")
        if document["previous_manifest_sha256"] != previous_hash:
            raise VerificationError("OBSERVATION_HASH_CHAIN_INVALID")
        previous_hash = digest
        manifest_hashes.append(digest)
        collector_evidence_hash = document["collector_evidence_sha256"]
        if collector_evidence_hash in collector_evidence_hashes:
            raise VerificationError("OBSERVATION_COLLECTOR_EVIDENCE_REUSED")
        collector_evidence_hashes.add(collector_evidence_hash)

        for field in ("image_digest_sha256", "config_sha256"):
            if document[field] != start[field]:
                reasons.append("OBSERVATION_RELEASE_DRIFT")
                failed = True
        for field in (
            "threshold_table_sha256",
            "fixed_query_sha256",
            "backend_policy_sha256",
            "reviewer_role",
        ):
            if document[field] != start[field]:
                reasons.append("OBSERVATION_POLICY_DRIFT")
                failed = True
        if not gate_truths(document):
            reasons.append("OBSERVATION_TELEMETRY_UNAVAILABLE")
            failed = True
        if document["incident_flags"]:
            reasons.append("OBSERVATION_INCIDENT_RECORDED")
            failed = True
        if document["failure_rate_ppm"] > thresholds["failure_rate_ppm_max"]:
            reasons.append("OBSERVATION_FAILURE_RATE_EXCEEDED")
            failed = True
        if document["draft_degradation_delta"] > thresholds["draft_degradation_delta_max"]:
            reasons.append("OBSERVATION_DRAFT_DEGRADATION_EXCEEDED")
            failed = True
        if document["first_token_p95_ms"] > thresholds["first_token_p95_ms_max"]:
            reasons.append("OBSERVATION_FIRST_TOKEN_P95_EXCEEDED")
            failed = True
        if document["completion_p95_ms"] > thresholds["completion_p95_ms_max"]:
            reasons.append("OBSERVATION_COMPLETION_P95_EXCEEDED")
            failed = True
        qualified_outcomes += document["rag_v2_non_replay_terminal_delta"]

    digest = archive_digest(manifest_hashes)
    if len(loaded_days) < 7:
        reasons.append("OBSERVATION_SEVEN_COMPLETE_DAYS_REQUIRED")
    if qualified_outcomes < 100:
        reasons.append("OBSERVATION_ONE_HUNDRED_OUTCOMES_REQUIRED")

    if (attestation_path is None) != (attestation_key_path is None):
        raise VerificationError("OBSERVATION_ATTESTATION_ARGUMENTS_INCOMPLETE")
    attested = attestation_path is not None and attestation_key_path is not None
    if attested:
        if evidence_directory is None:
            raise VerificationError("OBSERVATION_EVIDENCE_DIRECTORY_REQUIRED")
        last_window_end = loaded_days[-1][1] if loaded_days else expected_start
        trusted, trusted_key = validate_attestation(
            attestation_path,
            attestation_key_path,
            digest,
            start["attestation_policy_sha256"],
            last_window_end,
            current_time,
            trust_policy_path,
        )
        if not trusted:
            reasons.append("OBSERVATION_TRUST_ROOT_UNCONFIGURED")
        if backend_trust["status"] != "configured":
            reasons.append("OBSERVATION_BACKEND_TRUST_UNCONFIGURED")
        validate_evidence_archive(
            evidence_directory,
            start,
            [document for _, _, document, _ in loaded_days],
            trusted_key,
        )
    else:
        reasons.append("OBSERVATION_TRUSTED_ATTESTATION_REQUIRED")

    if failed:
        return verdict("failed", reasons, len(loaded_days), qualified_outcomes, digest)
    if reasons:
        return verdict("incomplete", reasons, len(loaded_days), qualified_outcomes, digest)
    return verdict("passed", [], len(loaded_days), qualified_outcomes, digest)


def parse_arguments(arguments: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--start", required=True, type=Path)
    parser.add_argument("--daily-dir", required=True, type=Path)
    parser.add_argument("--attestation", type=Path)
    parser.add_argument("--attestation-key-file", type=Path)
    parser.add_argument("--evidence-dir", type=Path)
    return parser.parse_args(arguments)


def main(
    arguments: list[str] | None = None,
    *,
    trust_policy_path: Path = TRUST_POLICY_PATH,
    backend_trust_policy_path: Path = BACKEND_TRUST_POLICY_PATH,
    now: datetime | None = None,
) -> int:
    options = parse_arguments(sys.argv[1:] if arguments is None else arguments)
    try:
        result = verify_archive(
            options.start,
            options.daily_dir,
            attestation_path=options.attestation,
            attestation_key_path=options.attestation_key_file,
            evidence_directory=options.evidence_dir,
            trust_policy_path=trust_policy_path,
            backend_trust_policy_path=backend_trust_policy_path,
            now=now,
        )
    except VerificationError as exc:
        print(f"eino-stable-observation: {exc.code}", file=sys.stderr)
        return 1
    print(canonical_bytes(result).decode("ascii"), end="")
    if result["status"] == "passed":
        return 0
    if result["status"] == "incomplete":
        return 2
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
