#!/usr/bin/env python3
"""Small CLI/file-safety checks. No Docker or user database is contacted."""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import io
import json
import os
import shlex
import subprocess
import sys
import tarfile
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("backup.py").resolve()
SPEC = importlib.util.spec_from_file_location("backup", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
backup = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(backup)


class BackupSafetyTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory(prefix="zhixu-backup-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.workspace = self.root / "workspace"
        self.workspace.mkdir()
        (self.workspace / "note.md").write_text("Keep the original bytes.\n")

    def create(self, output: Path, *extra: str, confirm: bool = True,
               cwd: Path | None = None) -> subprocess.CompletedProcess:
        arguments = [
            sys.executable, str(SCRIPT), "create", "--workspace", str(self.workspace),
            "--workspace-id", "00000000-0000-4000-8000-000000000001",
            "--postgres-container", "must-not-be-contacted", "--app-version", "0" * 40,
            "--output", str(output),
        ]
        if confirm:
            arguments.append("--confirm-stopped")
        return subprocess.run(arguments + list(extra), capture_output=True, text=True, timeout=10, cwd=cwd)

    def test_requires_stopped_writers_and_rejects_credentials_in_arguments(self) -> None:
        destination = self.root / "new-backup"
        result = self.create(destination, confirm=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_WRITERS_NOT_CONFIRMED", result.stderr)
        secret = "canary-do-not-print"
        result = self.create(destination, "--database", f"postgres://operator:{secret}@localhost/data")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_DATABASE_ARGUMENT_INVALID", result.stderr)
        self.assertNotIn(secret, result.stdout + result.stderr)
        result = self.create(destination, "--app-version", "latest")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_VERSION_INVALID", result.stderr)
        self.assertFalse(destination.exists())

    def test_existing_output_and_symlink_are_never_overwritten(self) -> None:
        directory = self.root / "existing"
        directory.mkdir()
        sentinel = directory / "sentinel"
        sentinel.write_text("Preserve this file.\n")
        alias = self.root / "alias"
        alias.symlink_to(directory, target_is_directory=True)
        for output in (directory, alias):
            with self.subTest(kind=output.name):
                result = self.create(output)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("BACKUP_OUTPUT_EXISTS", result.stderr)
        self.assertEqual(sentinel.read_text(), "Preserve this file.\n")
        self.assertEqual(list(directory.iterdir()), [sentinel])
        self.assertTrue(alias.is_symlink())

    def test_output_cannot_enter_workspace_through_a_parent_alias(self) -> None:
        alias = self.root / "workspace-alias"
        alias.symlink_to(self.workspace, target_is_directory=True)
        for output in (self.workspace / "backup", alias / "backup"):
            result = self.create(output)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("BACKUP_OUTPUT_INSIDE_WORKSPACE", result.stderr)
        self.assertEqual([path.name for path in self.workspace.iterdir()], ["note.md"])

    def test_workspace_symlink_is_rejected_without_following_it(self) -> None:
        outside = self.root / "outside-secret"
        outside.write_text("canary-outside-workspace")
        (self.workspace / "link").symlink_to(outside)
        destination = self.root / "new-backup"
        result = self.create(destination)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_FILE_TYPE_UNSUPPORTED", result.stderr)
        self.assertNotIn("canary-outside-workspace", result.stdout + result.stderr)
        self.assertFalse(destination.exists())

    def test_path_symlink_loop_is_rejected_without_echoing_the_path(self) -> None:
        loop = self.root / "sensitive-path-canary"
        loop.symlink_to(loop.name)
        destination = self.root / "new-backup"
        results = (
            self.create(destination, "--workspace", str(loop)),
            self.create(loop / "new-backup"),
            self.verify(loop),
        )
        for result in results:
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("BACKUP_PATH_INVALID", result.stderr)
            self.assertNotIn("Traceback", result.stderr)
            self.assertNotIn(str(loop), result.stdout + result.stderr)
        self.assertFalse(destination.exists())
        self.assertTrue(loop.is_symlink())

    def git(self, *arguments: str) -> bytes:
        environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        environment.update({"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull})
        result = subprocess.run([
            "git", "-C", str(self.workspace), "-c", "user.name=Backup Test",
            "-c", "user.email=backup@example.invalid", "-c", "commit.gpgsign=false",
            "-c", f"core.hooksPath={os.devnull}", *arguments,
        ], env=environment, check=True, capture_output=True, timeout=10)
        return result.stdout

    def test_unborn_git_state_preserves_files_refs_and_staged_index(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        for staged in (False, True):
            with self.subTest(staged=staged):
                if staged:
                    self.git("add", "note.md")
                before = backup.tree_snapshot(self.workspace)
                state = backup.git_state(self.workspace)
                self.assertIsNone(state["head"])
                self.assertEqual(state["branch"], "main")
                self.assertEqual(state["branch_state"], "unborn")
                self.assertTrue(state["dirty"])
                porcelain = self.git("--no-optional-locks", "status", "--porcelain=v1", "-z",
                                     "--untracked-files=all", "--ignore-submodules=all")
                self.assertEqual(state["status_sha256"], hashlib.sha256(porcelain).hexdigest())
                self.assertEqual(backup.tree_snapshot(self.workspace), before)
                self.assertFalse((self.workspace / ".git/refs/heads/main").exists())
                self.assertEqual((self.workspace / "note.md").read_text(), "Keep the original bytes.\n")

    def test_committed_git_state_keeps_attached_and_detached_heads(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        self.git("add", "note.md")
        self.git("commit", "-m", "isolated backup fixture")
        head = self.git("rev-parse", "HEAD").decode().strip()
        for detached in (False, True):
            with self.subTest(detached=detached):
                if detached:
                    self.git("switch", "--detach", "HEAD")
                before = backup.tree_snapshot(self.workspace)
                state = backup.git_state(self.workspace)
                self.assertEqual(state["head"], head)
                self.assertEqual(state["branch"], None if detached else "main")
                self.assertEqual(state["branch_state"], "detached" if detached else "attached")
                self.assertFalse(state["dirty"])
                self.assertEqual(backup.tree_snapshot(self.workspace), before)

    def test_broken_refs_are_not_accepted_as_unborn(self) -> None:
        cases = (
            ("malformed-ref", "refs/heads/main", b"invalid-ref-canary\n"),
            ("missing-object", "refs/heads/main", b"a" * 40 + b"\n"),
            ("null-object", "refs/heads/main", b"0" * 40 + b"\n"),
            ("dangling-symbolic-ref", "refs/heads/main", b"ref: refs/heads/missing\n"),
            ("detached-missing-object", "HEAD", b"a" * 40 + b"\n"),
            ("non-branch-unborn", "HEAD", b"ref: refs/tags/missing\n"),
            ("broken-packed-ref", "packed-refs", b"a" * 40 + b" refs/heads/main\n"),
        )
        for label, relative, content in cases:
            with self.subTest(case=label):
                self.workspace = self.root / label
                self.workspace.mkdir()
                self.git("init", "--template=", "--initial-branch=main")
                (self.workspace / ".git" / relative).write_bytes(content)
                before = backup.tree_snapshot(self.workspace)
                destination = self.root / f"{label}-backup"
                result = self.create(destination)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("BACKUP_GIT_HEAD_", result.stderr)
                self.assertNotIn("invalid-ref-canary", result.stdout + result.stderr)
                self.assertNotIn("BACKUP_CONTAINER", result.stderr)
                self.assertFalse(destination.exists())
                self.assertEqual(backup.tree_snapshot(self.workspace), before)

    def test_unborn_missing_index_object_is_rejected(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        self.git("add", "note.md")
        blob = self.git("rev-parse", ":note.md").decode().strip()
        (self.workspace / ".git/objects" / blob[:2] / blob[2:]).unlink()
        before = backup.tree_snapshot(self.workspace)
        destination = self.root / "new-backup"
        result = self.create(destination)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_GIT_HEAD_FAILED", result.stderr)
        self.assertFalse(destination.exists())
        self.assertEqual(backup.tree_snapshot(self.workspace), before)

    def test_git_probe_unknown_errors_remain_failures(self) -> None:
        for returncode, stdout in ((1, b"unexpected-canary"), (2, b""), (128, b""), (-15, b"")):
            with self.subTest(returncode=returncode):
                process = subprocess.CompletedProcess(["git"], returncode, stdout, b"private-error-canary")
                with mock.patch.object(backup.subprocess, "run", return_value=process):
                    with self.assertRaises(backup.BackupError) as caught:
                        backup.run(["git"], operation="BACKUP_GIT_HEAD_FAILED", missing_ok=True)
                self.assertNotIn("canary", str(caught.exception))

    def test_git_content_filter_is_rejected_without_execution(self) -> None:
        for committed in (False, True):
            with self.subTest(committed=committed):
                self.workspace = self.root / f"filter-{committed}"
                self.workspace.mkdir()
                (self.workspace / "note.md").write_text("Unfiltered fixture bytes.\n")
                self.git("init", "--template=", "--initial-branch=main")
                self.git("add", "note.md")
                if committed:
                    self.git("commit", "-m", "isolated backup fixture")
                marker = self.root / "filter-ran"
                content_filter = self.root / "filter.sh"
                content_filter.write_text(f"#!/bin/sh\nprintf ran > {shlex.quote(str(marker))}\ncat\n")
                content_filter.chmod(0o700)
                self.git("config", "filter.canary.clean", str(content_filter))
                (self.workspace / ".git/info").mkdir(exist_ok=True)
                (self.workspace / ".git/info/attributes").write_text("*.md filter=canary\n")
                (self.workspace / "note.md").write_text("Changed bytes would trigger a clean filter.\n")
                destination = self.root / "new-backup"
                nested = self.workspace / "nested"
                nested.mkdir()
                result = self.create(destination, cwd=nested)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("BACKUP_GIT_FILTER_UNSUPPORTED", result.stderr)
                self.assertFalse(marker.exists())
                self.assertFalse(destination.exists())

    def test_git_partial_clone_cannot_execute_remote_helper(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        marker = self.root / "remote-helper-ran"
        helper = self.root / "remote-helper.sh"
        helper.write_text(f"#!/bin/sh\nprintf ran > {shlex.quote(str(marker))}\nexit 1\n")
        helper.chmod(0o700)
        self.git("config", "core.repositoryformatversion", "1")
        self.git("config", "extensions.partialClone", "origin")
        self.git("config", "remote.origin.promisor", "true")
        self.git("config", "remote.origin.url", f"ext::{helper}")
        self.git("config", "protocol.ext.allow", "always")
        # No promisor pack exists, but resolving this missing object would fetch.
        (self.workspace / ".git/refs/heads/main").write_text("a" * 40 + "\n")
        destination = self.root / "new-backup"
        result = self.create(destination)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_GIT_EXTERNAL", result.stderr)
        self.assertFalse(marker.exists())
        self.assertFalse(destination.exists())

    def test_unborn_partial_clone_config_is_rejected_without_remote_helpers(self) -> None:
        for partial_kind in ("extension", "promisor-remote", "included-config", "promisor-pack"):
            with self.subTest(kind=partial_kind):
                self.workspace = self.root / partial_kind
                self.workspace.mkdir()
                self.git("init", "--template=", "--initial-branch=main")
                marker = self.root / "remote-helper-ran"
                helper = self.root / "remote-helper.sh"
                helper.write_text(f"#!/bin/sh\nprintf ran > {shlex.quote(str(marker))}\nexit 1\n")
                helper.chmod(0o700)
                self.git("config", "remote.origin.url", f"ext::{helper}")
                self.git("config", "protocol.ext.allow", "always")
                if partial_kind == "extension":
                    self.git("config", "core.repositoryformatversion", "1")
                    self.git("config", "extensions.partialClone", "origin")
                elif partial_kind == "promisor-remote":
                    self.git("config", "remote.origin.promisor", "true")
                elif partial_kind == "included-config":
                    (self.workspace / ".git/partial.conf").write_text('[remote "origin"]\n\tpromisor = true\n')
                    self.git("config", "include.path", "partial.conf")
                else:
                    (self.workspace / ".git/objects/pack/empty.promisor").touch()
                before = backup.tree_snapshot(self.workspace)
                destination = self.root / "new-backup"
                result = self.create(destination)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("BACKUP_GIT_EXTERNAL", result.stderr)
                self.assertFalse(marker.exists())
                self.assertFalse(destination.exists())
                self.assertEqual(backup.tree_snapshot(self.workspace), before)

    def test_false_promisor_is_supported_but_invalid_boolean_is_rejected(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        self.git("config", "remote.origin.promisor", "false")
        self.assertEqual(backup.git_state(self.workspace)["branch_state"], "unborn")
        self.git("config", "remote.origin.promisor", "invalid-promisor-canary")
        result = self.create(self.root / "new-backup")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_GIT_CONFIG_FAILED", result.stderr)
        self.assertNotIn("invalid-promisor-canary", result.stdout + result.stderr)

    def marker_response(self, engine: str, rows: list) -> dict:
        return {
            "workspace_id": "00000000-0000-4000-8000-000000000001",
            "root_path": str(self.workspace), "git_repository_path": str(self.workspace),
            "recorded_git_head": None, "registered_workspace_count": 1,
            "server_version_num": 180004, "active_index_version": None,
            "migration_history_presence": {"atlas": engine == "atlas", "goose": engine == "goose"},
            "migration_rows": rows,
        }

    def read_database_marker(self) -> dict:
        args = argparse.Namespace(database="zhixu", username="zhixu",
                                  workspace_id="00000000-0000-4000-8000-000000000001")
        return backup.database_marker(args, "fixture-not-a-container", self.workspace)

    def test_database_marker_selects_only_the_existing_history_table(self) -> None:
        cases = (
            ("atlas", [["00001", 2, 2, False], ["00099", 4, 4, False]], "00099",
             "atlas_schema_revisions.atlas_schema_revisions", "public.goose_db_version"),
            ("goose", [[version + 1, version, True] for version in range(82)], "81",
             "public.goose_db_version", "atlas_schema_revisions.atlas_schema_revisions"),
        )
        for engine, rows, version, relation, absent_relation in cases:
            with self.subTest(engine=engine):
                response = self.marker_response(engine, rows)
                responses = [response["migration_history_presence"], response]
                with mock.patch.object(backup, "run", side_effect=[json.dumps(item).encode() for item in responses]) as run:
                    marker = self.read_database_marker()
                self.assertEqual(marker["migration_engine"], engine)
                self.assertEqual(marker["migration_history_table"], relation)
                self.assertEqual(marker["schema_version"], version)
                self.assertEqual(marker["incomplete_migrations"], 0)
                self.assertRegex(marker["migration_history_sha256"], r"^[0-9a-f]{64}$")
                self.assertNotIn("migration_rows", marker)
                self.assertEqual(run.call_count, 2)
                probe, read = [call.kwargs["input_bytes"].decode() for call in run.call_args_list]
                self.assertNotIn("FROM " + relation, probe)
                self.assertIn("FROM " + relation, read)
                self.assertNotIn("FROM " + absent_relation, read)
                for call in run.call_args_list:
                    self.assertTrue(call.kwargs["input_bytes"].lstrip().startswith(b"BEGIN READ ONLY;"))
                    self.assertIn("--set=ON_ERROR_STOP=1", call.args[0])
                    self.assertIn("psql", call.args[0])

    def test_database_marker_rejects_unknown_or_mixed_history_before_reading_relations(self) -> None:
        for presence in ({"atlas": True, "goose": True}, {"atlas": False, "goose": False},
                         {"atlas": None, "goose": True}, {"atlas": 1, "goose": False},
                         {"flyway": True}, [], None):
            with self.subTest(presence=presence):
                with mock.patch.object(backup, "run", return_value=json.dumps(presence).encode()) as run:
                    with self.assertRaisesRegex(backup.BackupError, "BACKUP_SCHEMA_INVALID"):
                        self.read_database_marker()
                self.assertEqual(run.call_count, 1)

    def test_goose_marker_uses_latest_events_and_tracks_history_changes(self) -> None:
        rows = [[version + 1, version, True] for version in range(4)]
        initial = backup.migration_marker("goose", rows)
        rolled_back = backup.migration_marker("goose", rows + [[5, 3, False]])
        reapplied = backup.migration_marker("goose", rows + [[5, 3, False], [6, 3, True]])
        self.assertEqual(initial["schema_version"], "3")
        self.assertEqual(rolled_back["schema_version"], "2")
        self.assertEqual(reapplied["schema_version"], "3")
        self.assertNotEqual(initial["migration_history_sha256"], reapplied["migration_history_sha256"])

    def test_goose_marker_rejects_missing_unknown_and_incomplete_history(self) -> None:
        base = [[1, 0, True], [2, 1, True]]
        cases = (
            [], [[1, 0, True]], [[2, 1, True]], [[1, 0, False], [2, 1, True]],
            base + [[3, 3, True]], base + [[3, 1, False], [4, 2, True]],
            base + [[3, 92, True]], base + [[3, 92, False]], base + [[3, -1, True]],
            base + [[3, 2, None]], base + [[3, "2", True]], base + [[3, True, True]],
            base + [[2, 2, True]], base + [[None, 2, True]], list(reversed(base)),
            base + [[3, 2]], base + [None],
        )
        for rows in cases:
            with self.subTest(rows=rows):
                with self.assertRaisesRegex(backup.BackupError, "BACKUP_SCHEMA_INVALID"):
                    backup.migration_marker("goose", rows)

    def test_atlas_marker_rejects_incomplete_or_unrecognized_revisions(self) -> None:
        for rows in ([], None, [["00099", 1, 2, False]], [["00099", 2, 2, True]],
                     [["00099", None, None, False]], [["00099", -1, -1, False]],
                     [["unrecognized", 2, 2, False]], [[99, 2, 2, False]],
                     [["00099", 2, 2, False]] * 2):
            with self.subTest(rows=rows):
                with self.assertRaisesRegex(backup.BackupError, "BACKUP_SCHEMA_INVALID"):
                    backup.migration_marker("atlas", rows)

    def test_database_marker_rechecks_history_presence_and_workspace(self) -> None:
        for field, value, error in (
            ("migration_history_presence", {"atlas": True, "goose": True}, "BACKUP_SOURCE_CHANGED"),
            ("migration_history_presence", {"atlas": True, "goose": False}, "BACKUP_SOURCE_CHANGED"),
            ("workspace_id", "other-workspace", "BACKUP_WORKSPACE_MISMATCH"),
            ("root_path", "/other-root", "BACKUP_WORKSPACE_MISMATCH"),
            ("git_repository_path", "/other-git-root", "BACKUP_WORKSPACE_MISMATCH"),
        ):
            with self.subTest(field=field, value=value):
                response = self.marker_response("goose", [[1, 0, True], [2, 1, True]])
                presence = response["migration_history_presence"]
                response[field] = value
                with mock.patch.object(backup, "run", side_effect=[json.dumps(item).encode() for item in (presence, response)]):
                    with self.assertRaisesRegex(backup.BackupError, error):
                        self.read_database_marker()

    def create_with_database_fixture(self, destination: Path, *, change_during_dump: bool = False,
                                     database_engine: str = "atlas", history_change: bool = False) -> tuple:
        """Real Git/archive/CLI flow; synthetic DB bytes never contact Docker."""
        original_run = backup.run
        rows = [["00001", 2, 2, False]] if database_engine == "atlas" else [[1, 0, True], [2, 1, True]]
        before = self.marker_response(database_engine, rows)
        if history_change:
            rows = [["00001", 3, 3, False]] if database_engine == "atlas" else rows + [[3, 1, False], [4, 1, True]]
        after = self.marker_response(database_engine, rows)
        database_responses = iter([before["migration_history_presence"], before,
                                   after["migration_history_presence"], after])

        def fixture_run(arguments: list[str], **options) -> bytes:
            if arguments[0] == "git":
                return original_run(arguments, **options)
            if arguments[0] != "docker":
                raise AssertionError("unexpected backup dependency")
            if "psql" in arguments:
                return json.dumps(next(database_responses)).encode()
            if "pg_dump" in arguments:
                if "--version" in arguments:
                    return b"pg_dump (file-safety fixture, not PostgreSQL)\n"
                options["stdout"].write(b"PGDMP-file-safety-fixture-not-a-database")
                if change_during_dump:
                    self.git("add", "note.md")
                    self.git("commit", "-m", "isolated first-commit race")
                return b""
            if "pg_restore" in arguments and "--list" in arguments:
                return b""
            raise AssertionError("unexpected Docker call")

        arguments = [
            str(SCRIPT), "create", "--workspace", str(self.workspace),
            "--workspace-id", "00000000-0000-4000-8000-000000000001",
            "--postgres-container", "fixture-not-a-container", "--app-version", "0" * 40,
            "--output", str(destination), "--confirm-stopped",
        ]
        stdout, stderr = io.StringIO(), io.StringIO()
        with (mock.patch.object(backup, "run", side_effect=fixture_run),
              mock.patch.object(backup, "container_identity", return_value=["a" * 64, "sha256:" + "b" * 64,
                                                                          "true", "fixture-start-time"]),
              mock.patch.object(sys, "argv", arguments), redirect_stdout(stdout), redirect_stderr(stderr)):
            result = backup.main()
        return result, stdout.getvalue(), stderr.getvalue()

    def test_unborn_backup_records_null_head_and_preserves_archive(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        before = backup.tree_snapshot(self.workspace)
        destination = self.root / "unborn-backup"
        result, _, stderr = self.create_with_database_fixture(destination)
        self.assertEqual(result, 0, stderr)
        manifest = json.loads((destination / "manifest.json").read_text())
        self.assertIsNone(manifest["workspace"]["git"]["head"])
        self.assertEqual(manifest["workspace"]["git"]["branch"], "main")
        self.assertEqual(manifest["workspace"]["git"]["branch_state"], "unborn")
        self.assertFalse(manifest["verification"]["database_restore_checked"])
        self.assertEqual(backup.tree_snapshot(self.workspace), before)
        result = self.verify(destination)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(destination / "workspace.tar.gz", "r:gz") as archive:
            for relative in (".git/HEAD", "note.md"):
                member = archive.extractfile(f"workspace/{relative}")
                self.assertIsNotNone(member)
                with member:
                    self.assertEqual(member.read(), (self.workspace / relative).read_bytes())

    def test_first_commit_during_backup_does_not_publish_success_marker(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        destination = self.root / "changed-backup"
        result, stdout, stderr = self.create_with_database_fixture(destination, change_during_dump=True)
        self.assertNotEqual(result, 0)
        self.assertIn("BACKUP_SOURCE_CHANGED", stderr)
        self.assertNotIn("BACKUP_CREATED", stdout)
        self.assertTrue(destination.is_dir())
        self.assertFalse((destination / "SHA256SUMS").exists())
        self.assertEqual((self.workspace / "note.md").read_text(), "Keep the original bytes.\n")

    def test_backup_manifest_records_actual_migration_engine(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        for engine, table in (("atlas", "atlas_schema_revisions.atlas_schema_revisions"),
                              ("goose", "public.goose_db_version")):
            with self.subTest(engine=engine):
                destination = self.root / f"{engine}-backup"
                result, _, stderr = self.create_with_database_fixture(destination, database_engine=engine)
                self.assertEqual(result, 0, stderr)
                marker = json.loads((destination / "manifest.json").read_text())["database"]["marker"]
                self.assertEqual(marker["migration_engine"], engine)
                self.assertEqual(marker["migration_history_table"], table)
                self.assertRegex(marker["migration_history_sha256"], r"^[0-9a-f]{64}$")
                self.assertEqual(self.verify(destination).returncode, 0)

    def test_history_drift_with_same_schema_version_does_not_publish_success_marker(self) -> None:
        self.git("init", "--template=", "--initial-branch=main")
        before = backup.tree_snapshot(self.workspace)
        for engine in ("atlas", "goose"):
            with self.subTest(engine=engine):
                destination = self.root / f"{engine}-changed-backup"
                result, stdout, stderr = self.create_with_database_fixture(
                    destination, database_engine=engine, history_change=True)
                self.assertNotEqual(result, 0)
                self.assertIn("BACKUP_SOURCE_CHANGED", stderr)
                self.assertNotIn("BACKUP_CREATED", stdout)
                self.assertTrue(destination.is_dir())
                self.assertFalse((destination / "SHA256SUMS").exists())
                self.assertEqual(backup.tree_snapshot(self.workspace), before)

    def bundle(self, member_name: str = "workspace/note.md") -> Path:
        """File-integrity fixture only; PGDMP bytes are not a restorable database."""
        bundle = self.root / "bundle"
        bundle.mkdir(mode=0o700)
        with tarfile.open(bundle / "workspace.tar.gz", "w:gz") as archive:
            for name, body in (("workspace/.git/HEAD", b"ref: refs/heads/main\n"),
                               (member_name, b"fixture bytes\n")):
                member = tarfile.TarInfo(name)
                member.mode = 0o600
                member.size = len(body)
                archive.addfile(member, io.BytesIO(body))
        (bundle / "database.dump").write_bytes(b"PGDMP-file-integrity-fixture")
        (bundle / "manifest.json").write_text(json.dumps({"format": "zhixu-offline-backup/v1"}))
        names = ("workspace.tar.gz", "database.dump", "manifest.json")
        (bundle / "SHA256SUMS").write_text("".join(
            f"{hashlib.sha256((bundle / name).read_bytes()).hexdigest()}  {name}\n" for name in names
        ))
        for path in bundle.iterdir():
            path.chmod(0o600)
        return bundle

    def verify(self, bundle: Path) -> subprocess.CompletedProcess:
        return subprocess.run([sys.executable, str(SCRIPT), "verify", "--backup", str(bundle)],
                              capture_output=True, text=True, timeout=10)

    def test_verify_detects_tampering_without_touching_source(self) -> None:
        bundle = self.bundle()
        result = self.verify(bundle)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("database restore/application consistency not checked", result.stdout)
        with (bundle / "database.dump").open("ab") as output:
            output.write(b"tampered")
        result = self.verify(bundle)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_CHECKSUM_MISMATCH", result.stderr)
        self.assertEqual((self.workspace / "note.md").read_text(), "Keep the original bytes.\n")

    def test_verify_requires_complete_private_regular_artifacts(self) -> None:
        bundle = self.bundle()
        sums = (bundle / "SHA256SUMS").read_bytes()
        (bundle / "SHA256SUMS").unlink()
        self.assertNotEqual(self.verify(bundle).returncode, 0)
        (bundle / "SHA256SUMS").write_bytes(sums)
        (bundle / "SHA256SUMS").chmod(0o600)
        dump = bundle / "database.dump"
        dump.chmod(0o644)
        result = self.verify(bundle)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_FILE_INVALID", result.stderr)
        dump.chmod(0o600)
        outside = self.root / "outside"
        (bundle / "manifest.json").rename(outside)
        (bundle / "manifest.json").symlink_to(outside)
        result = self.verify(bundle)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_FILE_INVALID", result.stderr)

    def test_verify_rejects_archive_traversal_even_with_matching_checksums(self) -> None:
        bundle = self.bundle("../outside-canary")
        result = self.verify(bundle)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_ARCHIVE_INVALID", result.stderr)
        self.assertNotIn("outside-canary", result.stdout + result.stderr)
        self.assertFalse((self.root / "outside-canary").exists())


if __name__ == "__main__":
    unittest.main()
