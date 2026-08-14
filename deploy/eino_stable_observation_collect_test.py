#!/usr/bin/env python3

from __future__ import annotations

from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timedelta, timezone
from decimal import Decimal
from email.message import Message
import hashlib
import importlib.util
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock
import urllib.error
import urllib.request
from zoneinfo import ZoneInfo


DEPLOY_DIRECTORY = Path(__file__).resolve().parent
VERIFY_SCRIPT = DEPLOY_DIRECTORY / "eino_stable_observation_verify.py"
VERIFY_SPEC = importlib.util.spec_from_file_location("eino_stable_observation_verify", VERIFY_SCRIPT)
assert VERIFY_SPEC is not None and VERIFY_SPEC.loader is not None
gate = importlib.util.module_from_spec(VERIFY_SPEC)
sys.modules[VERIFY_SPEC.name] = gate
VERIFY_SPEC.loader.exec_module(gate)

COLLECT_SCRIPT = DEPLOY_DIRECTORY / "eino_stable_observation_collect.py"
COLLECT_SPEC = importlib.util.spec_from_file_location("eino_stable_observation_collect", COLLECT_SCRIPT)
assert COLLECT_SPEC is not None and COLLECT_SPEC.loader is not None
collector = importlib.util.module_from_spec(COLLECT_SPEC)
sys.modules[COLLECT_SPEC.name] = collector
COLLECT_SPEC.loader.exec_module(collector)


IMAGE_DIGEST = "a" * 64
CONFIG_DIGEST = "b" * 64
EMBEDDING_GATE_DIGEST = "c" * 64
ATTESTATION_KEY = b"k" * 32
APPROVED_AT = datetime(2026, 1, 1, tzinfo=timezone.utc)


def write_canonical(path: Path, document: dict[str, object]) -> str:
    raw = gate.canonical_bytes(document)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(raw)
    return hashlib.sha256(raw).hexdigest()


def assert_collection_error(
    test: unittest.TestCase,
    code: str,
    operation,
) -> collector.CollectionError:
    with test.assertRaises(collector.CollectionError) as raised:
        operation()
    test.assertEqual(raised.exception.code, code)
    return raised.exception


class FakeBackendClient:
    DEFAULT_VALUES = {
        "answer_failure_count": Decimal("1"),
        "answer_total_count": Decimal("20"),
        "completion_p95_ms": Decimal("2500.1"),
        "draft_degradation_delta": Decimal("0"),
        "first_token_p95_ms": Decimal("750.1"),
        "metrics_api_visible": Decimal("1"),
        "metrics_worker_visible": Decimal("1"),
        "rag_v2_terminal_delta": Decimal("15"),
        "telemetry_api_required": Decimal("1"),
        "telemetry_worker_required": Decimal("1"),
    }

    def __init__(self, values: dict[str, Decimal] | None = None) -> None:
        self.values = dict(self.DEFAULT_VALUES)
        if values is not None:
            self.values.update(values)
        self.backend_policy_digest = ""
        self.prometheus_calls: list[tuple[str, str, datetime]] = []
        self.tempo_calls: list[tuple[str, datetime, datetime]] = []

    def prometheus_value(self, query: str, evaluation_time: datetime) -> tuple[Decimal, str]:
        field = next(
            (candidate for candidate in collector.PROMETHEUS_QUERY_FIELDS if f"metric_{candidate}" in query),
            None,
        )
        if field is None:
            raise AssertionError("unexpected Prometheus query")
        self.prometheus_calls.append((field, query, evaluation_time))
        digest = hashlib.sha256(
            f"prometheus\n{field}\n{query}\n{evaluation_time.isoformat()}\n".encode("ascii")
        ).hexdigest()
        return self.values[field], digest

    def tempo_visible(
        self,
        query: str,
        window_start: datetime,
        window_end: datetime,
    ) -> tuple[bool, str]:
        self.tempo_calls.append((query, window_start, window_end))
        digest = hashlib.sha256(
            f"tempo\n{query}\n{window_start.isoformat()}\n{window_end.isoformat()}\n".encode("ascii")
        ).hexdigest()
        return True, digest


class ObservationFixture:
    def __init__(self, root: Path, *, key: bytes = ATTESTATION_KEY) -> None:
        self.root = root.resolve()
        self.archive = self.root / "archive"
        self.archive.mkdir()
        (self.archive / "daily").mkdir()
        (self.archive / "evidence").mkdir()
        self.policy_directory = self.root / "policy"
        self.policy_directory.mkdir()
        self.incident_directory = self.root / "incidents"
        self.incident_directory.mkdir()

        self.query_path = self.policy_directory / "queries.json"
        self.threshold_path = self.policy_directory / "thresholds.json"
        self.backend_trust_path = self.policy_directory / "backend-trust.json"
        self.trust_path = self.policy_directory / "trust.json"
        self.key_path = self.root / "attestation.key"
        self.key_path.write_bytes(key)
        self.key_path.chmod(0o600)
        self.prometheus_url_path = self.root / "prometheus-url"
        self.prometheus_token_path = self.root / "prometheus-token"
        self.tempo_url_path = self.root / "tempo-url"
        self.tempo_token_path = self.root / "tempo-token"
        write_protected_text(self.prometheus_url_path, "https://prometheus.invalid")
        write_protected_text(self.prometheus_token_path, "p" * 32)
        write_protected_text(self.tempo_url_path, "https://tempo.invalid")
        write_protected_text(self.tempo_token_path, "t" * 32)

        prometheus: dict[str, str] = {}
        for field in collector.PROMETHEUS_QUERY_FIELDS:
            service_name = "zhixu-worker"
            if field in {"metrics_api_visible", "telemetry_api_required"}:
                service_name = "zhixu-api"
            prometheus[field] = (
                f'metric_{field}{{service_name="{service_name}"}}'
                "[${WINDOW_SECONDS}s]"
            )
        self.query_document: dict[str, object] = {
            "backend": "prometheus-tempo",
            "prometheus": prometheus,
            "schema_version": collector.QUERY_SCHEMA,
            "service_names": {"api": "zhixu-api", "worker": "zhixu-worker"},
            "status": "configured",
            "tempo": {
                "traces_api_visible": '{ resource.service.name = "zhixu-api" }',
                "traces_worker_visible": '{ resource.service.name = "zhixu-worker" }',
            },
            "visibility_probe_seconds": 900,
        }
        self.threshold_document: dict[str, object] = {
            "approved_at": APPROVED_AT.isoformat(),
            "approver_role": "RELEASE_APPROVER",
            "completion_p95_ms_max": 10_000,
            "draft_degradation_delta_max": 1,
            "failure_rate_denominator": "agent.answer.result_total",
            "failure_rate_ppm_max": 100_000,
            "first_token_p95_ms_max": 2_000,
            "quantile": "0.95",
            "reviewer_role": "SRE_REVIEWER",
            "schema_version": collector.THRESHOLD_SCHEMA,
            "statistical_window": "natural_day",
            "status": "configured",
        }
        self.trust_document: dict[str, object] = {
            "attestation_key_sha256": hashlib.sha256(key).hexdigest(),
            "issuer_role": "PROTECTED_CI",
            "schema_version": gate.TRUST_SCHEMA,
            "status": "configured",
        }
        self.backend_trust_document: dict[str, object] = {
            "backend": "prometheus-tempo",
            "prometheus_token_file_sha256": collector.protected_path_digest(self.prometheus_token_path),
            "prometheus_url_file_sha256": collector.protected_path_digest(self.prometheus_url_path),
            "prometheus_url_sha256": hashlib.sha256(
                b"https://prometheus.invalid"
            ).hexdigest(),
            "schema_version": collector.BACKEND_TRUST_SCHEMA,
            "status": "configured",
            "tempo_token_file_sha256": collector.protected_path_digest(self.tempo_token_path),
            "tempo_url_file_sha256": collector.protected_path_digest(self.tempo_url_path),
            "tempo_url_sha256": hashlib.sha256(b"https://tempo.invalid").hexdigest(),
        }
        self.write_policies()

    def write_policies(self) -> None:
        write_canonical(self.query_path, self.query_document)
        write_canonical(self.threshold_path, self.threshold_document)
        self.backend_policy_digest = write_canonical(
            self.backend_trust_path,
            self.backend_trust_document,
        )
        write_canonical(self.trust_path, self.trust_document)

    def bind_backend_policy(self, client: FakeBackendClient) -> None:
        client.backend_policy_digest = self.backend_policy_digest

    def create_start(
        self,
        client: FakeBackendClient,
        *,
        now: datetime,
        timezone_name: str = "UTC",
    ) -> dict[str, object]:
        self.bind_backend_policy(client)
        return collector.create_start(
            self.archive,
            client,
            self.key_path,
            timezone_name=timezone_name,
            image_digest_sha256=IMAGE_DIGEST,
            config_sha256=CONFIG_DIGEST,
            embedding_gate_sha256=EMBEDDING_GATE_DIGEST,
            query_path=self.query_path,
            threshold_path=self.threshold_path,
            backend_trust_path=self.backend_trust_path,
            trust_path=self.trust_path,
            now=now,
        )

    def next_window(self) -> tuple[datetime, datetime]:
        _, _, _, first_start, days = collector.load_existing_chain(self.archive)
        window_start = first_start + timedelta(days=len(days))
        return window_start, window_start + timedelta(days=1)

    def write_incident_evidence(
        self,
        window_start: datetime,
        window_end: datetime,
        *,
        flags: list[str] | None = None,
    ) -> Path:
        path = self.incident_directory / f"{window_start.date().isoformat()}.json"
        write_canonical(
            path,
            {
                "incident_flags": [] if flags is None else flags,
                "schema_version": collector.INCIDENT_SCHEMA,
                "window_end": window_end.isoformat(),
                "window_start": window_start.isoformat(),
            },
        )
        return path

    def create_day(
        self,
        client: FakeBackendClient,
        *,
        now: datetime | None = None,
        image_digest_sha256: str = IMAGE_DIGEST,
        config_sha256: str = CONFIG_DIGEST,
    ) -> dict[str, object]:
        self.bind_backend_policy(client)
        window_start, window_end = self.next_window()
        incidents = self.write_incident_evidence(window_start, window_end)
        return collector.create_day(
            self.archive,
            client,
            self.key_path,
            image_digest_sha256=image_digest_sha256,
            config_sha256=config_sha256,
            incident_path=incidents,
            query_path=self.query_path,
            threshold_path=self.threshold_path,
            backend_trust_path=self.backend_trust_path,
            trust_path=self.trust_path,
            now=now or window_end + timedelta(minutes=1),
        )


class StubResponse:
    def __init__(
        self,
        raw: bytes,
        *,
        content_type: str = "application/json",
        content_length: str | None = None,
        status: int = 200,
    ) -> None:
        self.raw = raw
        self.status = status
        self.closed = False
        self.headers = Message()
        self.headers["Content-Type"] = content_type
        if content_length is not None:
            self.headers["Content-Length"] = content_length

    def read(self, maximum: int) -> bytes:
        return self.raw[:maximum]

    def close(self) -> None:
        self.closed = True


class StubOpener:
    def __init__(self, response: StubResponse | None = None, *, error: Exception | None = None) -> None:
        self.response = response
        self.error = error

    def open(self, request: urllib.request.Request, *, timeout: int):
        del request, timeout
        if self.error is not None:
            raise self.error
        assert self.response is not None
        return self.response


def backend_client_with_opener(opener: StubOpener) -> collector.BackendClient:
    client = object.__new__(collector.BackendClient)
    client.opener = opener
    return client


def write_protected_text(path: Path, value: str) -> None:
    path.write_text(value, encoding="utf-8")
    path.chmod(0o600)


class EinoStableObservationCollectTest(unittest.TestCase):
    def test_preflight_validates_start_inputs_without_network_or_archive_writes(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            arguments = [
                "preflight",
                "--archive-dir",
                str(fixture.archive),
                "--timezone",
                "UTC",
                "--image-digest-sha256",
                IMAGE_DIGEST,
                "--config-sha256",
                CONFIG_DIGEST,
                "--embedding-gate-sha256",
                EMBEDDING_GATE_DIGEST,
                "--attestation-key-file",
                str(fixture.key_path),
                "--prometheus-url-file",
                str(fixture.prometheus_url_path),
                "--prometheus-bearer-token-file",
                str(fixture.prometheus_token_path),
                "--tempo-url-file",
                str(fixture.tempo_url_path),
                "--tempo-bearer-token-file",
                str(fixture.tempo_token_path),
            ]
            stdout = io.StringIO()
            stderr = io.StringIO()
            with mock.patch.object(
                urllib.request.OpenerDirector,
                "open",
                side_effect=AssertionError("preflight must not access a backend"),
            ), redirect_stdout(stdout), redirect_stderr(stderr):
                exit_code = collector.main(
                    arguments,
                    query_path=fixture.query_path,
                    threshold_path=fixture.threshold_path,
                    backend_trust_path=fixture.backend_trust_path,
                    trust_path=fixture.trust_path,
                    now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc),
                )

            self.assertEqual(exit_code, 0)
            self.assertEqual(stderr.getvalue(), "")
            self.assertEqual(
                stdout.getvalue(),
                '{"operation":"preflight","status":"completed"}\n',
            )
            self.assertEqual(set(fixture.archive.iterdir()), {fixture.archive / "daily", fixture.archive / "evidence"})
            self.assertEqual(list((fixture.archive / "daily").iterdir()), [])
            self.assertEqual(list((fixture.archive / "evidence").iterdir()), [])

    def test_preflight_fails_closed_before_network_when_policy_is_unconfigured(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            fixture.threshold_document.update(
                {
                    "approved_at": None,
                    "completion_p95_ms_max": None,
                    "draft_degradation_delta_max": None,
                    "failure_rate_ppm_max": None,
                    "first_token_p95_ms_max": None,
                    "status": "unconfigured",
                }
            )
            fixture.write_policies()
            client = collector.BackendClient(
                collector.BackendFiles(
                    fixture.prometheus_url_path,
                    fixture.prometheus_token_path,
                    fixture.tempo_url_path,
                    fixture.tempo_token_path,
                ),
                backend_trust_path=fixture.backend_trust_path,
            )
            with mock.patch.object(
                client.opener,
                "open",
                side_effect=AssertionError("preflight must not access a backend"),
            ):
                assert_collection_error(
                    self,
                    "OBSERVATION_THRESHOLD_POLICY_UNCONFIGURED",
                    lambda: collector.validate_start_readiness(
                        fixture.archive,
                        client,
                        fixture.key_path,
                        timezone_name="UTC",
                        image_digest_sha256=IMAGE_DIGEST,
                        config_sha256=CONFIG_DIGEST,
                        embedding_gate_sha256=EMBEDDING_GATE_DIGEST,
                        query_path=fixture.query_path,
                        threshold_path=fixture.threshold_path,
                        backend_trust_path=fixture.backend_trust_path,
                        trust_path=fixture.trust_path,
                        now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc),
                    ),
                )

    def test_preflight_rejects_non_unique_midnights_before_network(self) -> None:
        cases = (
            (
                "nonexistent-midnight",
                "Africa/Cairo",
                datetime(2026, 4, 20, 12, tzinfo=ZoneInfo("Africa/Cairo")),
            ),
            (
                "ambiguous-midnight",
                "America/Havana",
                datetime(2026, 10, 25, 12, tzinfo=ZoneInfo("America/Havana")),
            ),
        )
        for label, timezone_name, current_time in cases:
            with self.subTest(label=label), tempfile.TemporaryDirectory() as temporary:
                fixture = ObservationFixture(Path(temporary))
                client = FakeBackendClient()
                fixture.bind_backend_policy(client)

                assert_collection_error(
                    self,
                    "OBSERVATION_TIMEZONE_MIDNIGHT_INVALID",
                    lambda: collector.validate_start_readiness(
                        fixture.archive,
                        client,
                        fixture.key_path,
                        timezone_name=timezone_name,
                        image_digest_sha256=IMAGE_DIGEST,
                        config_sha256=CONFIG_DIGEST,
                        embedding_gate_sha256=EMBEDDING_GATE_DIGEST,
                        query_path=fixture.query_path,
                        threshold_path=fixture.threshold_path,
                        backend_trust_path=fixture.backend_trust_path,
                        trust_path=fixture.trust_path,
                        now=current_time,
                    ),
                )
                self.assertEqual(client.prometheus_calls, [])
                self.assertEqual(client.tempo_calls, [])
                self.assertEqual(
                    set(fixture.archive.iterdir()),
                    {fixture.archive / "daily", fixture.archive / "evidence"},
                )
                self.assertEqual(list((fixture.archive / "daily").iterdir()), [])
                self.assertEqual(list((fixture.archive / "evidence").iterdir()), [])

    def test_next_midnight_normalizes_datetime_overflow(self) -> None:
        assert_collection_error(
            self,
            "OBSERVATION_TIMEZONE_MIDNIGHT_INVALID",
            lambda: collector.next_midnight(
                datetime(9999, 12, 31, 12, tzinfo=timezone.utc),
                ZoneInfo("UTC"),
            ),
        )

    def test_backend_client_requires_frozen_url_identity_without_network(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            prometheus_url = "https://prometheus.invalid"
            tempo_url = "https://tempo.invalid"
            prometheus_url_path = root / "prometheus-url"
            prometheus_token_path = root / "prometheus-token"
            tempo_url_path = root / "tempo-url"
            tempo_token_path = root / "tempo-token"
            write_protected_text(prometheus_url_path, prometheus_url)
            write_protected_text(prometheus_token_path, "p" * 32)
            write_protected_text(tempo_url_path, tempo_url)
            write_protected_text(tempo_token_path, "t" * 32)
            backend_trust_path = root / "backend-trust.json"
            backend_policy_digest = write_canonical(
                backend_trust_path,
                {
                    "backend": "prometheus-tempo",
                    "prometheus_token_file_sha256": collector.protected_path_digest(prometheus_token_path),
                    "prometheus_url_file_sha256": collector.protected_path_digest(prometheus_url_path),
                    "prometheus_url_sha256": hashlib.sha256(
                        prometheus_url.encode("utf-8")
                    ).hexdigest(),
                    "schema_version": collector.BACKEND_TRUST_SCHEMA,
                    "status": "configured",
                    "tempo_token_file_sha256": collector.protected_path_digest(tempo_token_path),
                    "tempo_url_file_sha256": collector.protected_path_digest(tempo_url_path),
                    "tempo_url_sha256": hashlib.sha256(
                        tempo_url.encode("utf-8")
                    ).hexdigest(),
                },
            )
            files = collector.BackendFiles(
                prometheus_url_path,
                prometheus_token_path,
                tempo_url_path,
                tempo_token_path,
            )
            client = collector.BackendClient(files, backend_trust_path=backend_trust_path)
            self.assertEqual(client.backend_policy_digest, backend_policy_digest)

            alternate_token = root / "alternate-prometheus-token"
            write_protected_text(alternate_token, "p" * 32)
            substituted = collector.BackendFiles(
                prometheus_url_path,
                alternate_token,
                tempo_url_path,
                tempo_token_path,
            )
            assert_collection_error(
                self,
                "OBSERVATION_BACKEND_FILE_NOT_TRUSTED",
                lambda: collector.BackendClient(substituted, backend_trust_path=backend_trust_path),
            )

            write_protected_text(prometheus_url_path, "https://other-prometheus.invalid")
            error = assert_collection_error(
                self,
                "OBSERVATION_BACKEND_URL_NOT_TRUSTED",
                lambda: collector.BackendClient(files, backend_trust_path=backend_trust_path),
            )
            self.assertNotIn("other-prometheus", str(error))

    def test_start_seven_days_and_attestation_form_a_verified_chain(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            client = FakeBackendClient()
            started = fixture.create_start(
                client,
                now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc),
            )
            self.assertEqual(started["window_start"], "2026-08-02T00:00:00+00:00")
            self.assertEqual((fixture.archive / "start.json").stat().st_mode & 0o777, 0o600)
            self.assertTrue((fixture.archive / "evidence" / "start.json").is_file())

            previous_digest = hashlib.sha256((fixture.archive / "start.json").read_bytes()).hexdigest()
            days: list[dict[str, object]] = []
            for _ in range(7):
                day = fixture.create_day(client)
                days.append(day)
                self.assertEqual(day["previous_manifest_sha256"], previous_digest)
                date_name = datetime.fromisoformat(str(day["window_start"])).date().isoformat() + ".json"
                day_path = fixture.archive / "daily" / date_name
                evidence_path = fixture.archive / "evidence" / date_name
                self.assertEqual(
                    day["collector_evidence_sha256"],
                    hashlib.sha256(evidence_path.read_bytes()).hexdigest(),
                )
                previous_digest = hashlib.sha256(day_path.read_bytes()).hexdigest()

            last_window_end = datetime.fromisoformat(str(days[-1]["window_end"]))
            attestation = collector.create_attestation(
                fixture.archive,
                fixture.key_path,
                query_path=fixture.query_path,
                threshold_path=fixture.threshold_path,
                backend_trust_path=fixture.backend_trust_path,
                trust_path=fixture.trust_path,
                now=last_window_end + timedelta(hours=1),
            )
            self.assertEqual(attestation["issuer_role"], "PROTECTED_CI")
            verdict = gate.verify_archive(
                fixture.archive / "start.json",
                fixture.archive / "daily",
                attestation_path=fixture.archive / "attestation.json",
                attestation_key_path=fixture.key_path,
                evidence_directory=fixture.archive / "evidence",
                trust_policy_path=fixture.trust_path,
                backend_trust_policy_path=fixture.backend_trust_path,
                now=last_window_end + timedelta(hours=1),
            )
            self.assertEqual(verdict["status"], "passed")
            self.assertEqual(verdict["complete_days"], 7)
            self.assertEqual(verdict["qualified_rag_v2_terminal_outcomes"], 105)

    def test_attestation_rejects_manifest_numbers_not_bound_to_signed_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            client = FakeBackendClient()
            fixture.create_start(client, now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc))
            days = [fixture.create_day(client) for _ in range(7)]

            previous = hashlib.sha256((fixture.archive / "start.json").read_bytes()).hexdigest()
            for index, day in enumerate(days):
                if index == 0:
                    day["completion_p95_ms"] = 1
                day["previous_manifest_sha256"] = previous
                path = fixture.archive / "daily" / (
                    datetime.fromisoformat(str(day["window_start"])).date().isoformat() + ".json"
                )
                previous = write_canonical(path, day)

            last_window_end = datetime.fromisoformat(str(days[-1]["window_end"]))
            assert_collection_error(
                self,
                "OBSERVATION_COLLECTOR_EVIDENCE_INVALID",
                lambda: collector.create_attestation(
                    fixture.archive,
                    fixture.key_path,
                    query_path=fixture.query_path,
                    threshold_path=fixture.threshold_path,
                    backend_trust_path=fixture.backend_trust_path,
                    trust_path=fixture.trust_path,
                    now=last_window_end + timedelta(hours=1),
                ),
            )
            self.assertFalse((fixture.archive / "attestation.json").exists())

    def test_natural_days_render_23_and_25_hour_query_windows_across_dst(self) -> None:
        cases = (
            (
                "spring-forward",
                datetime(2026, 3, 7, 12, tzinfo=ZoneInfo("America/New_York")),
                23 * 60 * 60,
            ),
            (
                "fall-back",
                datetime(2026, 10, 31, 12, tzinfo=ZoneInfo("America/New_York")),
                25 * 60 * 60,
            ),
        )
        for label, start_now, expected_seconds in cases:
            with self.subTest(label=label), tempfile.TemporaryDirectory() as temporary:
                fixture = ObservationFixture(Path(temporary))
                client = FakeBackendClient()
                fixture.create_start(client, now=start_now, timezone_name="America/New_York")
                client.prometheus_calls.clear()
                day = fixture.create_day(client)
                window_start = datetime.fromisoformat(str(day["window_start"]))
                window_end = datetime.fromisoformat(str(day["window_end"]))
                actual_seconds = int(
                    (
                        window_end.astimezone(timezone.utc)
                        - window_start.astimezone(timezone.utc)
                    ).total_seconds()
                )
                self.assertEqual(actual_seconds, expected_seconds)
                self.assertEqual(len(client.prometheus_calls), len(collector.PROMETHEUS_QUERY_FIELDS))
                for _, query, _ in client.prometheus_calls:
                    self.assertIn(f"[{expected_seconds}s]", query)
                    self.assertNotIn("${WINDOW_SECONDS}", query)

    def test_zero_traffic_day_is_valid_and_uses_zero_aggregates(self) -> None:
        zero_values = {
            "answer_failure_count": Decimal("0"),
            "answer_total_count": Decimal("0"),
            "completion_p95_ms": Decimal("0"),
            "draft_degradation_delta": Decimal("0"),
            "first_token_p95_ms": Decimal("0"),
            "rag_v2_terminal_delta": Decimal("0"),
        }
        with tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            client = FakeBackendClient(zero_values)
            fixture.create_start(client, now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc))
            day = fixture.create_day(client)
            self.assertEqual(day["failure_rate_ppm"], 0)
            self.assertEqual(day["first_token_p95_ms"], 0)
            self.assertEqual(day["completion_p95_ms"], 0)
            self.assertEqual(day["rag_v2_non_replay_terminal_delta"], 0)
            verdict = gate.verify_archive(
                fixture.archive / "start.json",
                fixture.archive / "daily",
                trust_policy_path=fixture.trust_path,
                now=datetime.fromisoformat(str(day["window_end"])) + timedelta(minutes=1),
            )
            self.assertEqual(verdict["status"], "incomplete")
            self.assertNotIn("OBSERVATION_FAILURE_RATE_EXCEEDED", verdict["reasons"])

    def test_daily_collection_rejects_historical_backfill_after_grace_period(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            client = FakeBackendClient()
            fixture.create_start(client, now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc))
            calls_before = len(client.prometheus_calls)

            assert_collection_error(
                self,
                "OBSERVATION_DAILY_COLLECTION_LATE",
                lambda: fixture.create_day(
                    client,
                    now=datetime(2026, 8, 9, 12, tzinfo=timezone.utc),
                ),
            )
            self.assertEqual(len(client.prometheus_calls), calls_before)

    def test_daily_collection_rejects_archive_limit_before_backend_queries(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            client = FakeBackendClient()
            first_start = datetime(2026, 1, 1, tzinfo=timezone.utc)
            existing_days = [({}, "0" * 64)] * gate.MAX_DAILY_MANIFESTS

            with mock.patch.object(
                collector,
                "load_existing_chain",
                return_value=(
                    {},
                    "0" * 64,
                    ZoneInfo("UTC"),
                    first_start,
                    existing_days,
                ),
            ):
                assert_collection_error(
                    self,
                    "OBSERVATION_DAILY_COUNT_INVALID",
                    lambda: collector.create_day(
                        fixture.archive,
                        client,
                        fixture.key_path,
                        image_digest_sha256=IMAGE_DIGEST,
                        config_sha256=CONFIG_DIGEST,
                        incident_path=fixture.incident_directory / "not-read.json",
                        query_path=fixture.query_path,
                        threshold_path=fixture.threshold_path,
                        backend_trust_path=fixture.backend_trust_path,
                        trust_path=fixture.trust_path,
                        now=first_start + timedelta(days=gate.MAX_DAILY_MANIFESTS + 1),
                    ),
                )

            self.assertEqual(client.prometheus_calls, [])
            self.assertEqual(client.tempo_calls, [])

    def test_exclusive_create_never_removes_an_existing_file(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary).resolve() / "immutable.json"
            original = b"existing evidence must remain intact\n"
            target.write_bytes(original)
            assert_collection_error(
                self,
                "OBSERVATION_FILE_ALREADY_EXISTS",
                lambda: collector.write_new_canonical(target, {"replacement": True}),
            )
            self.assertTrue(target.exists())
            self.assertEqual(target.read_bytes(), original)

    def test_archive_rejects_symlink_permissions_and_preexisting_entries(self) -> None:
        with self.subTest(case="symlink"), tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            real_archive = root / "real"
            real_archive.mkdir()
            (real_archive / "daily").mkdir()
            (real_archive / "evidence").mkdir()
            alias = root / "alias"
            alias.symlink_to(real_archive, target_is_directory=True)
            assert_collection_error(
                self,
                "OBSERVATION_PATH_UNSAFE",
                lambda: collector.validate_archive_directories(alias),
            )

        with self.subTest(case="permissions"), tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            archive = root / "archive"
            archive.mkdir(mode=0o700)
            (archive / "daily").mkdir()
            (archive / "evidence").mkdir()
            archive.chmod(0o770)
            assert_collection_error(
                self,
                "OBSERVATION_DIRECTORY_PERMISSIONS_INVALID",
                lambda: collector.validate_archive_directories(archive),
            )

        with self.subTest(case="non-empty"), tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            sentinel = fixture.archive / "unexpected.txt"
            sentinel.write_text("keep", encoding="ascii")
            assert_collection_error(
                self,
                "OBSERVATION_ARCHIVE_NOT_EMPTY",
                lambda: fixture.create_start(
                    FakeBackendClient(),
                    now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc),
                ),
            )
            self.assertEqual(sentinel.read_text(encoding="ascii"), "keep")
            self.assertFalse((fixture.archive / "start.json").exists())

    def test_release_and_frozen_policy_drift_fail_before_daily_collection(self) -> None:
        with self.subTest(case="release"), tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            client = FakeBackendClient()
            fixture.create_start(client, now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc))
            calls_before = len(client.prometheus_calls)
            assert_collection_error(
                self,
                "OBSERVATION_RELEASE_DRIFT",
                lambda: fixture.create_day(client, config_sha256="9" * 64),
            )
            self.assertEqual(len(client.prometheus_calls), calls_before)

        mutations = (
            (
                "query",
                lambda fixture: fixture.query_document["prometheus"].__setitem__(  # type: ignore[union-attr]
                    "answer_total_count",
                    'metric_answer_total_count_changed{service_name="zhixu-worker"}[${WINDOW_SECONDS}s]',
                ),
            ),
            (
                "threshold",
                lambda fixture: fixture.threshold_document.__setitem__("completion_p95_ms_max", 10_001),
            ),
            (
                "trust",
                lambda fixture: fixture.trust_document.__setitem__("issuer_role", "OTHER_PROTECTED_CI"),
            ),
        )
        for label, mutate in mutations:
            with self.subTest(case=label), tempfile.TemporaryDirectory() as temporary:
                fixture = ObservationFixture(Path(temporary))
                client = FakeBackendClient()
                fixture.create_start(client, now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc))
                calls_before = len(client.prometheus_calls)
                mutate(fixture)
                fixture.write_policies()
                assert_collection_error(
                    self,
                    "OBSERVATION_POLICY_DRIFT",
                    lambda: fixture.create_day(client),
                )
                self.assertEqual(len(client.prometheus_calls), calls_before)

    def test_missing_historical_evidence_blocks_daily_collection(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = ObservationFixture(Path(temporary))
            client = FakeBackendClient()
            fixture.create_start(client, now=datetime(2026, 8, 1, 12, tzinfo=timezone.utc))
            (fixture.archive / "evidence" / "start.json").unlink()
            calls_before = len(client.prometheus_calls)
            daily_before = set((fixture.archive / "daily").iterdir())
            assert_collection_error(
                self,
                "OBSERVATION_EVIDENCE_DIRECTORY_INVALID",
                lambda: fixture.create_day(client),
            )
            self.assertEqual(len(client.prometheus_calls), calls_before)
            self.assertEqual(set((fixture.archive / "daily").iterdir()), daily_before)

    def test_backend_json_boundary_rejects_wrong_type_size_duplicates_and_nonfinite(self) -> None:
        cases = (
            (
                "content-type",
                StubResponse(b"{}\n", content_type="text/plain"),
                "OBSERVATION_BACKEND_RESPONSE_INVALID",
            ),
            (
                "declared-size",
                StubResponse(b"{}\n", content_length=str(collector.MAX_BACKEND_RESPONSE_BYTES + 1)),
                "OBSERVATION_BACKEND_RESPONSE_TOO_LARGE",
            ),
            (
                "actual-size",
                StubResponse(b"{" + b" " * collector.MAX_BACKEND_RESPONSE_BYTES),
                "OBSERVATION_BACKEND_RESPONSE_TOO_LARGE",
            ),
            (
                "duplicate-field",
                StubResponse(b'{"status":"success","status":"success"}\n'),
                "OBSERVATION_BACKEND_RESPONSE_INVALID",
            ),
            (
                "non-finite",
                StubResponse(b'{"value":NaN}\n'),
                "OBSERVATION_BACKEND_RESPONSE_INVALID",
            ),
        )
        for label, response, expected_code in cases:
            with self.subTest(case=label):
                client = backend_client_with_opener(StubOpener(response))
                assert_collection_error(
                    self,
                    expected_code,
                    lambda: client.request_json(urllib.request.Request("https://metrics.invalid/query")),
                )
                self.assertTrue(response.closed)

    def test_backend_and_cli_errors_do_not_disclose_exception_details(self) -> None:
        secret = "bearer-token-must-not-leak"
        backend_error = urllib.error.HTTPError(
            "https://metrics.invalid/api",
            302,
            secret,
            Message(),
            None,
        )
        client = backend_client_with_opener(StubOpener(error=backend_error))
        error = assert_collection_error(
            self,
            "OBSERVATION_BACKEND_UNAVAILABLE",
            lambda: client.request_json(urllib.request.Request("https://metrics.invalid/query")),
        )
        self.assertNotIn(secret, str(error))
        self.assertIsNone(
            collector.NoRedirectHandler().redirect_request(
                None,
                None,
                302,
                secret,
                Message(),
                "https://redirect.invalid/secret",
            )
        )

        stdout = io.StringIO()
        stderr = io.StringIO()
        arguments = [
            "start",
            "--archive-dir",
            "/protected/archive",
            "--timezone",
            "UTC",
            "--image-digest-sha256",
            IMAGE_DIGEST,
            "--config-sha256",
            CONFIG_DIGEST,
            "--embedding-gate-sha256",
            EMBEDDING_GATE_DIGEST,
            "--attestation-key-file",
            "/run/secrets/observation-key",
            "--prometheus-url-file",
            "/run/secrets/prometheus-url",
            "--prometheus-bearer-token-file",
            "/run/secrets/prometheus-token",
            "--tempo-url-file",
            "/run/secrets/tempo-url",
            "--tempo-bearer-token-file",
            "/run/secrets/tempo-token",
        ]
        with mock.patch.object(collector, "BackendClient", side_effect=RuntimeError(secret)):
            with redirect_stdout(stdout), redirect_stderr(stderr):
                exit_code = collector.main(arguments)
        self.assertEqual(exit_code, 1)
        self.assertEqual(stdout.getvalue(), "")
        self.assertEqual(
            stderr.getvalue(),
            "eino-stable-observation-collect: OBSERVATION_COLLECTION_FAILED\n",
        )
        self.assertNotIn(secret, stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
