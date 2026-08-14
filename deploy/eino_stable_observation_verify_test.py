#!/usr/bin/env python3

from __future__ import annotations

from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timedelta, timezone
import hashlib
import hmac
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
from zoneinfo import ZoneInfo


SCRIPT = Path(__file__).with_name("eino_stable_observation_verify.py")
SPEC = importlib.util.spec_from_file_location("eino_stable_observation_verify", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


HASH_A = "a" * 64
HASH_B = "b" * 64
HASH_C = "c" * 64
HASH_D = "d" * 64
HASH_E = "e" * 64
HASH_F = "f" * 64
START_AT = datetime(2026, 8, 1, tzinfo=timezone.utc)
NOW = datetime(2026, 8, 9, 12, tzinfo=timezone.utc)
TRUSTED_KEY = b"k" * 32


def canonical(document: dict[str, object]) -> bytes:
    return gate.canonical_bytes(document)


def write_canonical(path: Path, document: dict[str, object]) -> str:
    raw = canonical(document)
    path.write_bytes(raw)
    return hashlib.sha256(raw).hexdigest()


def start_manifest(
    *,
    start_at: datetime = START_AT,
    timezone_name: str = "UTC",
    policy_hash: str = HASH_E,
    backend_policy_hash: str = HASH_D,
) -> dict[str, object]:
    return {
        "schema_version": gate.START_SCHEMA,
        "timezone": timezone_name,
        "window_start": start_at.isoformat(),
        "image_digest_sha256": HASH_A,
        "config_sha256": HASH_B,
        "embedding_gate_sha256": HASH_C,
        "attestation_policy_sha256": policy_hash,
        "backend_policy_sha256": backend_policy_hash,
        "collector_evidence_sha256": HASH_E,
        "telemetry_required_api": True,
        "telemetry_required_worker": True,
        "collector_metrics_api_visible": True,
        "collector_metrics_worker_visible": True,
        "collector_traces_api_visible": True,
        "collector_traces_worker_visible": True,
        "threshold_table_sha256": HASH_F,
        "fixed_query_sha256": "1" * 64,
        "thresholds": {
            "failure_rate_ppm_max": 10_000,
            "draft_degradation_delta_max": 1,
            "first_token_p95_ms_max": 2_000,
            "completion_p95_ms_max": 10_000,
        },
        "reviewer_role": "SRE_REVIEWER",
    }


def day_manifest(
    index: int,
    previous_hash: str,
    *,
    outcomes: int = 15,
    start_at: datetime = START_AT,
    backend_policy_hash: str = HASH_D,
) -> dict[str, object]:
    start = start_at + timedelta(days=index)
    return {
        "schema_version": gate.DAY_SCHEMA,
        "window_start": start.isoformat(),
        "window_end": (start + timedelta(days=1)).isoformat(),
        "image_digest_sha256": HASH_A,
        "config_sha256": HASH_B,
        "telemetry_required_api": True,
        "telemetry_required_worker": True,
        "collector_metrics_api_visible": True,
        "collector_metrics_worker_visible": True,
        "collector_traces_api_visible": True,
        "collector_traces_worker_visible": True,
        "rag_v2_non_replay_terminal_delta": outcomes,
        "failure_rate_ppm": 1_000,
        "draft_degradation_delta": 0,
        "first_token_p95_ms": 500,
        "completion_p95_ms": 3_000,
        "threshold_table_sha256": HASH_F,
        "fixed_query_sha256": "1" * 64,
        "backend_policy_sha256": backend_policy_hash,
        "collector_evidence_sha256": f"{index + 2:x}".rjust(64, "0"),
        "incident_flags": [],
        "previous_manifest_sha256": previous_hash,
        "reviewer_role": "SRE_REVIEWER",
    }


class Fixture:
    def __init__(
        self,
        root: Path,
        days: int = 7,
        outcomes: int = 15,
        *,
        start_at: datetime = START_AT,
        timezone_name: str = "UTC",
        trust_policy_path: Path | None = None,
        trusted_key: bytes = TRUSTED_KEY,
    ) -> None:
        self.root = root.resolve()
        self.daily = self.root / "daily"
        self.daily.mkdir()
        self.evidence = self.root / "evidence"
        self.evidence.mkdir()
        self.start = self.root / "start.json"
        self.start_at = start_at
        self.timezone_name = timezone_name
        self.trusted_key = trusted_key
        if trust_policy_path is None:
            self.trust = self.root / "trust.json"
            trust_document: dict[str, object] = {
                "schema_version": gate.TRUST_SCHEMA,
                "status": "configured",
                "attestation_key_sha256": hashlib.sha256(trusted_key).hexdigest(),
                "issuer_role": "PROTECTED_CI",
            }
            self.trust_hash = write_canonical(self.trust, trust_document)
        else:
            self.trust = trust_policy_path.resolve()
            _, _, self.trust_hash = gate.load_canonical_document(self.trust)
        self.backend_trust = self.root / "backend-trust.json"
        backend_trust_document: dict[str, object] = {
            "backend": "prometheus-tempo",
            "prometheus_token_file_sha256": "2" * 64,
            "prometheus_url_file_sha256": "3" * 64,
            "prometheus_url_sha256": "4" * 64,
            "schema_version": gate.BACKEND_TRUST_SCHEMA,
            "status": "configured",
            "tempo_token_file_sha256": "5" * 64,
            "tempo_url_file_sha256": "6" * 64,
            "tempo_url_sha256": "7" * 64,
        }
        self.backend_trust_hash = write_canonical(self.backend_trust, backend_trust_document)
        self.start_document = start_manifest(
            start_at=start_at,
            timezone_name=timezone_name,
            policy_hash=self.trust_hash,
            backend_policy_hash=self.backend_trust_hash,
        )
        self.documents = [
            day_manifest(
                index,
                "0" * 64,
                outcomes=outcomes,
                start_at=start_at,
                backend_policy_hash=self.backend_trust_hash,
            )
            for index in range(days)
        ]
        self.hashes: list[str] = []
        self.rebuild_chain()

    def write_evidence(
        self,
        manifest: dict[str, object],
        *,
        name: str,
        start_probe: bool,
        key: bytes,
    ) -> str:
        binding = dict(manifest)
        binding.pop("collector_evidence_sha256")
        if start_probe:
            collection_end = datetime.fromisoformat(str(binding["window_start"])) - timedelta(minutes=1)
            collection_start = collection_end - timedelta(minutes=5)
            prometheus_fields = gate.START_PROMETHEUS_EVIDENCE_FIELDS
            incident_digest = None
        else:
            collection_start = datetime.fromisoformat(str(binding["window_start"]))
            collection_end = datetime.fromisoformat(str(binding["window_end"]))
            prometheus_fields = gate.DAY_PROMETHEUS_EVIDENCE_FIELDS
            incident_digest = HASH_A
        response_fields = {"prometheus." + field for field in prometheus_fields} | {
            "tempo." + field for field in gate.TEMPO_EVIDENCE_FIELDS
        }
        unsigned: dict[str, object] = {
            "backend_policy_sha256": binding["backend_policy_sha256"],
            "collected_at": (
                collection_end if start_probe else collection_end + timedelta(hours=1)
            ).isoformat(),
            "collection_window_end": collection_end.isoformat(),
            "collection_window_start": collection_start.isoformat(),
            "fixed_query_sha256": binding["fixed_query_sha256"],
            "incident_evidence_sha256": incident_digest,
            "manifest": binding,
            "response_sha256": {field: HASH_B for field in sorted(response_fields)},
            "schema_version": gate.EVIDENCE_SCHEMA,
        }
        document = dict(unsigned)
        document["hmac_sha256"] = hmac.new(key, canonical(unsigned), hashlib.sha256).hexdigest()
        return write_canonical(self.evidence / name, document)

    def rebuild_chain(self, key: bytes | None = None) -> None:
        signing_key = self.trusted_key if key is None else key
        self.start_document["collector_evidence_sha256"] = self.write_evidence(
            self.start_document,
            name="start.json",
            start_probe=True,
            key=signing_key,
        )
        previous = write_canonical(self.start, self.start_document)
        self.hashes = [previous]
        for index, document in enumerate(self.documents):
            document["previous_manifest_sha256"] = previous
            evidence_name = datetime.fromisoformat(str(document["window_start"])).date().isoformat() + ".json"
            document["collector_evidence_sha256"] = self.write_evidence(
                document,
                name=evidence_name,
                start_probe=False,
                key=signing_key,
            )
            previous = write_canonical(self.daily / f"day-{index + 1:02d}.json", document)
            self.hashes.append(previous)

    def rewrite_start(self, mutate) -> None:
        mutate(self.start_document)
        self.rebuild_chain()

    def rewrite_day(self, index: int, mutate) -> None:
        mutate(self.documents[index])
        self.rebuild_chain()

    def attest(self, key: bytes | None = None) -> tuple[Path, Path]:
        signing_key = self.trusted_key if key is None else key
        self.rebuild_chain(signing_key)
        archive = gate.archive_digest(self.hashes)
        last_window_end = self.start_at + timedelta(days=len(self.documents))
        document: dict[str, object] = {
            "schema_version": gate.ATTESTATION_SCHEMA,
            "archive_sha256": archive,
            "attestation_policy_sha256": self.start_document["attestation_policy_sha256"],
            "issued_at": (last_window_end + timedelta(hours=1)).astimezone(timezone.utc).isoformat(),
            "issuer_role": "PROTECTED_CI",
        }
        document["hmac_sha256"] = hmac.new(signing_key, canonical(document), hashlib.sha256).hexdigest()
        attestation = self.root / "attestation.json"
        write_canonical(attestation, document)
        key_file = self.root / "attestation.key"
        key_file.write_bytes(signing_key)
        key_file.chmod(0o600)
        return attestation, key_file


def invoke_main(
    arguments: list[str],
    *,
    trust_policy_path: Path = gate.TRUST_POLICY_PATH,
    backend_trust_policy_path: Path = gate.BACKEND_TRUST_POLICY_PATH,
    now: datetime = NOW,
) -> tuple[int, str, str]:
    stdout = io.StringIO()
    stderr = io.StringIO()
    with redirect_stdout(stdout), redirect_stderr(stderr):
        exit_code = gate.main(
            arguments,
            trust_policy_path=trust_policy_path,
            backend_trust_policy_path=backend_trust_policy_path,
            now=now,
        )
    return exit_code, stdout.getvalue(), stderr.getvalue()


class EinoStableObservationVerifyTest(unittest.TestCase):
    def test_signed_daily_evidence_rejects_timestamp_overflow(self) -> None:
        for collected_at in (
            "0001-01-01T00:00:00+14:00",
            "9999-12-31T23:59:59-12:00",
        ):
            with self.subTest(collected_at=collected_at), tempfile.TemporaryDirectory() as temporary:
                fixture = Fixture(Path(temporary))
                manifest = fixture.documents[0]
                evidence_name = (
                    datetime.fromisoformat(str(manifest["window_start"])).date().isoformat()
                    + ".json"
                )
                evidence_path = fixture.evidence / evidence_name
                evidence, _, _ = gate.load_canonical_document(evidence_path)
                evidence["collected_at"] = collected_at
                unsigned = dict(evidence)
                unsigned.pop("hmac_sha256")
                evidence["hmac_sha256"] = hmac.new(
                    fixture.trusted_key,
                    canonical(unsigned),
                    hashlib.sha256,
                ).hexdigest()
                manifest["collector_evidence_sha256"] = write_canonical(evidence_path, evidence)

                with self.assertRaisesRegex(
                    gate.VerificationError,
                    "OBSERVATION_COLLECTOR_EVIDENCE_INVALID",
                ):
                    gate.validate_evidence_archive(
                        fixture.evidence,
                        fixture.start_document,
                        fixture.documents,
                        fixture.trusted_key,
                    )

    def test_signed_daily_evidence_rejects_collection_after_daily_grace(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            manifest = fixture.documents[0]
            evidence_name = datetime.fromisoformat(str(manifest["window_start"])).date().isoformat() + ".json"
            evidence_path = fixture.evidence / evidence_name
            evidence, _, _ = gate.load_canonical_document(evidence_path)
            evidence["collected_at"] = (
                datetime.fromisoformat(str(manifest["window_end"])) + timedelta(days=1)
            ).isoformat()
            unsigned = dict(evidence)
            unsigned.pop("hmac_sha256")
            evidence["hmac_sha256"] = hmac.new(
                fixture.trusted_key,
                canonical(unsigned),
                hashlib.sha256,
            ).hexdigest()
            manifest["collector_evidence_sha256"] = write_canonical(evidence_path, evidence)

            with self.assertRaisesRegex(
                gate.VerificationError,
                "OBSERVATION_COLLECTOR_EVIDENCE_INVALID",
            ):
                gate.validate_evidence_archive(
                    fixture.evidence,
                    fixture.start_document,
                    fixture.documents,
                    fixture.trusted_key,
                )

    def test_complete_attested_window_passes(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            attestation, key = fixture.attest()
            result = gate.verify_archive(
                fixture.start,
                fixture.daily,
                attestation_path=attestation,
                attestation_key_path=key,
                evidence_directory=fixture.evidence,
                trust_policy_path=fixture.trust,
                backend_trust_policy_path=fixture.backend_trust,
                now=NOW,
            )
            self.assertEqual(result["status"], "passed")
            self.assertEqual(result["complete_days"], 7)
            self.assertEqual(result["qualified_rag_v2_terminal_outcomes"], 105)

    def test_structurally_complete_window_without_attestation_is_incomplete(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            result = gate.verify_archive(fixture.start, fixture.daily, now=NOW)
            self.assertEqual(result["status"], "incomplete")
            self.assertIn("OBSERVATION_TRUSTED_ATTESTATION_REQUIRED", result["reasons"])

    def test_time_and_sample_minimums_are_both_required(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary), days=6, outcomes=16)
            result = gate.verify_archive(fixture.start, fixture.daily, now=NOW)
            self.assertIn("OBSERVATION_SEVEN_COMPLETE_DAYS_REQUIRED", result["reasons"])
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary), days=7, outcomes=14)
            result = gate.verify_archive(fixture.start, fixture.daily, now=NOW)
            self.assertIn("OBSERVATION_ONE_HUNDRED_OUTCOMES_REQUIRED", result["reasons"])

    def test_threshold_and_incident_fail(self) -> None:
        cases = (
            (lambda day: day.update(first_token_p95_ms=2_001), "OBSERVATION_FIRST_TOKEN_P95_EXCEEDED"),
            (lambda day: day.update(incident_flags=["LEASE_FENCE_INCIDENT"]), "OBSERVATION_INCIDENT_RECORDED"),
        )
        for mutate, reason in cases:
            with self.subTest(reason=reason), tempfile.TemporaryDirectory() as temporary:
                fixture = Fixture(Path(temporary))
                fixture.rewrite_day(2, mutate)
                result = gate.verify_archive(fixture.start, fixture.daily, now=NOW)
                self.assertEqual(result["status"], "failed")
                self.assertIn(reason, result["reasons"])

    def test_release_and_policy_drift_fail(self) -> None:
        cases = (
            (lambda day: day.update(config_sha256="9" * 64), "OBSERVATION_RELEASE_DRIFT"),
            (lambda day: day.update(fixed_query_sha256="8" * 64), "OBSERVATION_POLICY_DRIFT"),
            (lambda day: day.update(backend_policy_sha256="7" * 64), "OBSERVATION_POLICY_DRIFT"),
            (lambda day: day.update(reviewer_role="OTHER_REVIEWER"), "OBSERVATION_POLICY_DRIFT"),
            (lambda day: day.update(collector_traces_worker_visible=False), "OBSERVATION_TELEMETRY_UNAVAILABLE"),
        )
        for mutate, reason in cases:
            with self.subTest(reason=reason), tempfile.TemporaryDirectory() as temporary:
                fixture = Fixture(Path(temporary))
                fixture.rewrite_day(1, mutate)
                result = gate.verify_archive(fixture.start, fixture.daily, now=NOW)
                self.assertEqual(result["status"], "failed")
                self.assertIn(reason, result["reasons"])

    def test_configured_backend_policy_must_match_start_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            backend, _, _ = gate.load_canonical_document(fixture.backend_trust)
            backend["prometheus_url_sha256"] = "8" * 64
            write_canonical(fixture.backend_trust, backend)
            result = gate.verify_archive(
                fixture.start,
                fixture.daily,
                backend_trust_policy_path=fixture.backend_trust,
                now=NOW,
            )
            self.assertEqual(result["status"], "failed")
            self.assertIn("OBSERVATION_POLICY_DRIFT", result["reasons"])

    def test_gap_and_hash_chain_tampering_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            (fixture.daily / "day-03.json").unlink()
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_DAILY_WINDOW_NOT_CONTIGUOUS"):
                gate.verify_archive(fixture.start, fixture.daily, now=NOW)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            fixture.documents[2]["previous_manifest_sha256"] = "9" * 64
            write_canonical(fixture.daily / "day-03.json", fixture.documents[2])
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_HASH_CHAIN_INVALID"):
                gate.verify_archive(fixture.start, fixture.daily, now=NOW)

    def test_reused_collector_evidence_and_oversized_directory_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            reused = fixture.documents[0]["collector_evidence_sha256"]
            fixture.documents[1]["collector_evidence_sha256"] = reused
            previous = hashlib.sha256(fixture.start.read_bytes()).hexdigest()
            for index, document in enumerate(fixture.documents):
                document["previous_manifest_sha256"] = previous
                previous = write_canonical(fixture.daily / f"day-{index + 1:02d}.json", document)
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_COLLECTOR_EVIDENCE_REUSED"):
                gate.verify_archive(fixture.start, fixture.daily, now=NOW)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary), days=0)
            for index in range(gate.MAX_DAILY_MANIFESTS + 1):
                (fixture.daily / f"day-{index:03d}.json").write_text("{}\n", encoding="utf-8")
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_DAILY_COUNT_INVALID"):
                gate.verify_archive(fixture.start, fixture.daily, now=NOW)

    def test_noncanonical_unknown_duplicate_and_future_documents_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            fixture.start.write_text(json.dumps(start_manifest(), indent=2), encoding="utf-8")
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_JSON_NOT_CANONICAL"):
                gate.verify_archive(fixture.start, fixture.daily, now=NOW)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            start = dict(fixture.start_document)
            start["unknown"] = True
            write_canonical(fixture.start, start)
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_FIELDS_INVALID"):
                gate.verify_archive(fixture.start, fixture.daily, now=NOW)
        duplicate = b'{"schema_version":"x","schema_version":"x"}\n'
        with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_DUPLICATE_FIELD"):
            gate.decode_document(duplicate)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_DAILY_WINDOW_INCOMPLETE"):
                gate.verify_archive(fixture.start, fixture.daily, now=datetime(2026, 8, 7, 12, tzinfo=timezone.utc))

    def test_attestation_key_identity_hmac_permissions_and_arguments_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            attestation, key = fixture.attest()
            key.write_bytes(b"z" * 32)
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_ATTESTATION_KEY_NOT_TRUSTED"):
                gate.verify_archive(
                    fixture.start,
                    fixture.daily,
                    attestation_path=attestation,
                    attestation_key_path=key,
                    evidence_directory=fixture.evidence,
                    trust_policy_path=fixture.trust,
                    backend_trust_policy_path=fixture.backend_trust,
                    now=NOW,
                )
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_ATTESTATION_ARGUMENTS_INCOMPLETE"):
                gate.verify_archive(fixture.start, fixture.daily, attestation_path=attestation, now=NOW)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            attestation, key = fixture.attest()
            document, _, _ = gate.load_canonical_document(attestation)
            document["hmac_sha256"] = "0" * 64
            write_canonical(attestation, document)
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_ATTESTATION_INVALID"):
                gate.verify_archive(
                    fixture.start,
                    fixture.daily,
                    attestation_path=attestation,
                    attestation_key_path=key,
                    evidence_directory=fixture.evidence,
                    trust_policy_path=fixture.trust,
                    backend_trust_policy_path=fixture.backend_trust,
                    now=NOW,
                )
        for permissions in (0o644, 0o700):
            with self.subTest(permissions=oct(permissions)), tempfile.TemporaryDirectory() as temporary:
                fixture = Fixture(Path(temporary))
                attestation, key = fixture.attest()
                key.chmod(permissions)
                with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_ATTESTATION_KEY_PERMISSIONS_INVALID"):
                    gate.verify_archive(
                        fixture.start,
                        fixture.daily,
                        attestation_path=attestation,
                        attestation_key_path=key,
                        evidence_directory=fixture.evidence,
                        trust_policy_path=fixture.trust,
                        backend_trust_policy_path=fixture.backend_trust,
                        now=NOW,
                    )

    def test_attested_archive_rechecks_backend_trust_and_evidence_archive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            attestation, key = fixture.attest()
            result = gate.verify_archive(
                fixture.start,
                fixture.daily,
                attestation_path=attestation,
                attestation_key_path=key,
                evidence_directory=fixture.evidence,
                trust_policy_path=fixture.trust,
                now=NOW,
            )
            self.assertEqual(result["status"], "incomplete")
            self.assertIn("OBSERVATION_BACKEND_TRUST_UNCONFIGURED", result["reasons"])

        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            attestation, key = fixture.attest()
            (fixture.evidence / "2026-08-02.json").unlink()
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_EVIDENCE_DIRECTORY_INVALID"):
                gate.verify_archive(
                    fixture.start,
                    fixture.daily,
                    attestation_path=attestation,
                    attestation_key_path=key,
                    evidence_directory=fixture.evidence,
                    trust_policy_path=fixture.trust,
                    backend_trust_policy_path=fixture.backend_trust,
                    now=NOW,
                )

    def test_attested_archive_reuses_the_single_trusted_key_read(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            attestation, key = fixture.attest()
            original_read = gate.read_attestation_key
            reads = 0

            def read_once(path: Path) -> bytes:
                nonlocal reads
                reads += 1
                if reads > 1:
                    raise AssertionError("attestation key was read more than once")
                return original_read(path)

            with mock.patch.object(gate, "read_attestation_key", side_effect=read_once):
                result = gate.verify_archive(
                    fixture.start,
                    fixture.daily,
                    attestation_path=attestation,
                    attestation_key_path=key,
                    evidence_directory=fixture.evidence,
                    trust_policy_path=fixture.trust,
                    backend_trust_policy_path=fixture.backend_trust,
                    now=NOW,
                )
            self.assertEqual(result["status"], "passed")
            self.assertEqual(reads, 1)

    def test_formal_unconfigured_trust_root_rejects_arbitrary_local_key(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary), trust_policy_path=gate.TRUST_POLICY_PATH)
            attestation, key = fixture.attest(b"local-operator-key-material-0001")
            arguments = [
                "--start",
                str(fixture.start),
                "--daily-dir",
                str(fixture.daily),
                "--attestation",
                str(attestation),
                "--attestation-key-file",
                str(key),
                "--evidence-dir",
                str(fixture.evidence),
            ]
            exit_code, stdout, stderr = invoke_main(
                arguments,
                backend_trust_policy_path=fixture.backend_trust,
            )
            self.assertEqual(exit_code, 2)
            self.assertEqual(stderr, "")
            self.assertIn('"status":"incomplete"', stdout)
            self.assertIn("OBSERVATION_TRUST_ROOT_UNCONFIGURED", stdout)

            missing, stdout, stderr = invoke_main(
                [
                    "--start",
                    str(fixture.start),
                    "--daily-dir",
                    str(fixture.daily),
                    "--attestation",
                    str(fixture.root / "missing-attestation.json"),
                    "--attestation-key-file",
                    str(fixture.root / "missing-attestation.key"),
                    "--evidence-dir",
                    str(fixture.evidence),
                ],
                backend_trust_policy_path=fixture.backend_trust,
            )
            self.assertEqual((missing, stdout), (1, ""))
            self.assertIn("OBSERVATION_FILE_UNAVAILABLE", stderr)

            attestation.write_text("{}\n", encoding="utf-8")
            malformed, stdout, stderr = invoke_main(
                arguments,
                backend_trust_policy_path=fixture.backend_trust,
            )
            self.assertEqual((malformed, stdout), (1, ""))
            self.assertIn("OBSERVATION_FIELDS_INVALID", stderr)

    def test_main_exit_codes_for_passed_incomplete_and_failed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            attestation, key = fixture.attest()
            passed, stdout, stderr = invoke_main(
                [
                    "--start",
                    str(fixture.start),
                    "--daily-dir",
                    str(fixture.daily),
                    "--attestation",
                    str(attestation),
                    "--attestation-key-file",
                    str(key),
                    "--evidence-dir",
                    str(fixture.evidence),
                ],
                trust_policy_path=fixture.trust,
                backend_trust_policy_path=fixture.backend_trust,
            )
            self.assertEqual((passed, stderr), (0, ""))
            self.assertIn('"status":"passed"', stdout)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            incomplete, stdout, stderr = invoke_main(
                ["--start", str(fixture.start), "--daily-dir", str(fixture.daily)],
                trust_policy_path=fixture.trust,
            )
            self.assertEqual((incomplete, stderr), (2, ""))
            self.assertIn('"status":"incomplete"', stdout)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            fixture.rewrite_day(0, lambda day: day.update(incident_flags=["LEASE_FENCE_INCIDENT"]))
            failed, stdout, stderr = invoke_main(
                ["--start", str(fixture.start), "--daily-dir", str(fixture.daily)],
                trust_policy_path=fixture.trust,
            )
            self.assertEqual((failed, stderr), (1, ""))
            self.assertIn('"status":"failed"', stdout)

    def test_dst_calendar_days_and_invalid_boundaries(self) -> None:
        cases = (
            (datetime(2026, 3, 5, tzinfo=ZoneInfo("America/New_York")), 23),
            (datetime(2026, 10, 29, tzinfo=ZoneInfo("America/New_York")), 25),
        )
        for start_at, transition_hours in cases:
            with self.subTest(start_at=start_at), tempfile.TemporaryDirectory() as temporary:
                fixture = Fixture(
                    Path(temporary),
                    start_at=start_at,
                    timezone_name="America/New_York",
                )
                now = (start_at + timedelta(days=8, hours=12)).astimezone(timezone.utc)
                result = gate.verify_archive(fixture.start, fixture.daily, now=now)
                self.assertEqual(result["complete_days"], 7)
                durations = []
                for document in fixture.documents:
                    day_start = datetime.fromisoformat(str(document["window_start"])).astimezone(timezone.utc)
                    day_end = datetime.fromisoformat(str(document["window_end"])).astimezone(timezone.utc)
                    durations.append(int((day_end - day_start).total_seconds() // 3600))
                self.assertIn(transition_hours, durations)
        with tempfile.TemporaryDirectory() as temporary:
            start_at = datetime(2026, 3, 5, tzinfo=ZoneInfo("America/New_York"))
            fixture = Fixture(Path(temporary), start_at=start_at, timezone_name="America/New_York")
            fixture.start_document["window_start"] = "2026-03-05T00:00:00-04:00"
            write_canonical(fixture.start, fixture.start_document)
            with self.assertRaisesRegex(
                gate.VerificationError,
                "OBSERVATION_(DAY_BOUNDARY|TIMEZONE_OFFSET)_INVALID",
            ):
                gate.verify_archive(fixture.start, fixture.daily, now=NOW)
        with tempfile.TemporaryDirectory() as temporary:
            start_at = datetime(2026, 3, 5, tzinfo=ZoneInfo("America/New_York"))
            fixture = Fixture(Path(temporary), start_at=start_at, timezone_name="America/New_York")
            fixture.rewrite_day(0, lambda day: day.update(window_start="2026-03-05T00:01:00-05:00"))
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_DAY_BOUNDARY_INVALID"):
                gate.verify_archive(fixture.start, fixture.daily, now=NOW)

    def test_start_rejects_observation_horizon_with_non_unique_midnight(self) -> None:
        cases = (
            (
                "nonexistent-midnight",
                "Africa/Cairo",
                datetime(2026, 4, 21, tzinfo=ZoneInfo("Africa/Cairo")),
            ),
            (
                "ambiguous-midnight",
                "America/Havana",
                datetime(2026, 10, 26, tzinfo=ZoneInfo("America/Havana")),
            ),
        )
        for label, timezone_name, start_at in cases:
            with self.subTest(label=label):
                document = start_manifest(start_at=start_at, timezone_name=timezone_name)
                with self.assertRaisesRegex(
                    gate.VerificationError,
                    "OBSERVATION_TIMEZONE_MIDNIGHT_INVALID",
                ):
                    gate.validate_start(document)

    def test_symlinks_are_rejected_and_directory_replacement_cannot_redirect_reads(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            daily_link = fixture.root / "daily-link"
            daily_link.symlink_to(fixture.daily, target_is_directory=True)
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_DAILY_DIRECTORY_UNSAFE"):
                gate.verify_archive(fixture.start, daily_link, now=NOW)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            external = fixture.root / "external.json"
            external.write_bytes((fixture.daily / "day-01.json").read_bytes())
            (fixture.daily / "day-link.json").symlink_to(external)
            with self.assertRaisesRegex(gate.VerificationError, "OBSERVATION_DAILY_ENTRY_INVALID"):
                gate.load_daily_manifests(fixture.daily)
        with tempfile.TemporaryDirectory() as temporary:
            fixture = Fixture(Path(temporary))
            original_open = gate.open_directory_no_symlinks
            held_directory = fixture.root / "daily-held"
            replacement = fixture.root / "replacement"
            replacement.mkdir()

            def replace_after_open(path: Path) -> int:
                descriptor = original_open(path)
                path.rename(held_directory)
                path.symlink_to(replacement, target_is_directory=True)
                return descriptor

            with mock.patch.object(gate, "open_directory_no_symlinks", side_effect=replace_after_open):
                manifests = gate.load_daily_manifests(fixture.daily)
            self.assertEqual(len(manifests), 7)


if __name__ == "__main__":
    unittest.main()
