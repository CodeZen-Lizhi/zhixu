#!/usr/bin/env python3
"""Create and verify an offline Workspace + PostgreSQL backup; never restore in place."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import stat
import subprocess
import sys
import tarfile
import uuid
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from typing import BinaryIO


FORMAT = "zhixu-offline-backup/v1"
ARTIFACTS = ("workspace.tar.gz", "database.dump", "manifest.json")
# Goose was retired at 91; keep this boundary aligned with migration/runner.go.
LEGACY_GOOSE_MAX_VERSION = 91
HISTORY_TABLES = {
    "atlas": "atlas_schema_revisions.atlas_schema_revisions",
    "goose": "public.goose_db_version",
}
HISTORY_PRESENCE_SQL = """
SELECT json_build_object(
    'atlas', to_regclass('atlas_schema_revisions.atlas_schema_revisions') IS NOT NULL,
    'goose', to_regclass('public.goose_db_version') IS NOT NULL
)
"""
HISTORY_ROWS_SQL = {
    "atlas": """SELECT json_agg(json_build_array(version, applied, total,
        coalesce(error, '') <> '') ORDER BY version)
        FROM atlas_schema_revisions.atlas_schema_revisions""",
    "goose": """SELECT json_agg(json_build_array(id, version_id, is_applied) ORDER BY id)
        FROM public.goose_db_version""",
}
MARKER_SQL = """
BEGIN READ ONLY;
SELECT json_build_object(
    'workspace_id', w.id::text,
    'root_path', w.root_path,
    'git_repository_path', w.git_repository_path,
    'recorded_git_head', w.git_head,
    'registered_workspace_count', (SELECT count(*) FROM core.workspace),
    'server_version_num', current_setting('server_version_num')::integer,
    'migration_history_presence', ({history_presence}),
    'migration_rows', ({history_rows}),
    'active_index_version', (SELECT id::text FROM retrieval.index_version
        WHERE workspace_id = w.id AND status = 'active')
) FROM core.workspace AS w WHERE w.id = :'workspace_id'::uuid;
COMMIT;
"""


class BackupError(Exception):
    """A safe operator-facing failure, without raw command output or credentials."""


class SafeParser(argparse.ArgumentParser):
    def error(self, message: str) -> None:
        self.exit(2, "BACKUP_ARGUMENTS_INVALID: use --help; argument values are not echoed\n")


def run(
    arguments: list[str],
    *,
    operation: str,
    input_bytes: bytes | None = None,
    stdin: BinaryIO | None = None,
    stdout: BinaryIO | int = subprocess.PIPE,
    timeout: int = 30,
    environment: dict[str, str] | None = None,
    missing_ok: bool = False,
) -> bytes:
    try:
        result = subprocess.run(
            arguments, input=input_bytes, stdin=stdin, stdout=stdout,
            stderr=subprocess.DEVNULL, timeout=timeout, env=environment, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        raise BackupError(f"{operation}: command unavailable or timed out") from None
    # Only quiet Git ref probes opt in to exit 1 with no output. Their caller
    # must still distinguish an absent ref from corruption before accepting it.
    if result.returncode != 0 and not (missing_ok and result.returncode == 1 and not result.stdout):
        raise BackupError(f"{operation}: command failed; raw output suppressed")
    return result.stdout or b""


def absolute_directory(value: str) -> Path:
    path = Path(value)
    if not path.is_absolute() or any(ord(char) < 32 for char in value):
        raise BackupError("BACKUP_PATH_INVALID: an absolute directory is required")
    try:
        resolved = path.resolve(strict=True)
    except (OSError, RuntimeError):
        # Python 3.10 reports symlink loops as RuntimeError, not OSError.
        raise BackupError("BACKUP_PATH_INVALID: directory cannot be resolved") from None
    if not resolved.is_dir() or resolved == Path(resolved.anchor):
        raise BackupError("BACKUP_PATH_INVALID: filesystem root is not supported")
    return resolved


def new_destination(value: str, workspace: Path) -> Path:
    path = Path(value)
    if not path.is_absolute() or any(ord(char) < 32 for char in value):
        raise BackupError("BACKUP_OUTPUT_INVALID: an absolute new directory is required")
    if os.path.lexists(path):
        raise BackupError("BACKUP_OUTPUT_EXISTS: choose a new directory; nothing is overwritten")
    parent = absolute_directory(str(path.parent))
    parent_mode = parent.stat().st_mode
    if parent_mode & 0o022 and not parent_mode & stat.S_ISVTX:
        raise BackupError("BACKUP_OUTPUT_PARENT_UNSAFE: use a protected parent directory")
    destination = parent / path.name
    if destination == workspace or workspace in destination.parents:
        raise BackupError("BACKUP_OUTPUT_INSIDE_WORKSPACE: choose storage outside the source tree")
    return destination


def tree_snapshot(workspace: Path) -> dict[str, tuple[int, ...]]:
    """Detect ordinary edits during the operator's stopped-write window."""
    snapshot = {}
    device = workspace.stat().st_dev

    def visit(path: Path) -> None:
        info = path.lstat()
        if not (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)):
            raise BackupError("BACKUP_FILE_TYPE_UNSUPPORTED: symlinks and special files are not supported")
        if info.st_dev != device:
            raise BackupError("BACKUP_NESTED_MOUNT_UNSUPPORTED: Workspace must be on one filesystem")
        if path.name == ".git" and not stat.S_ISDIR(info.st_mode):
            raise BackupError("BACKUP_GIT_EXTERNAL: linked worktrees and submodules are not supported")
        relative = str(path.relative_to(workspace))
        snapshot[relative] = (
            info.st_dev, info.st_ino, info.st_mode, info.st_size,
            info.st_mtime_ns, info.st_ctime_ns,
        )
        if stat.S_ISDIR(info.st_mode):
            for child in sorted(path.iterdir()):
                visit(child)

    visit(workspace)
    return snapshot


def git_state(workspace: Path) -> dict[str, str | bool | None]:
    git_dir = workspace / ".git"
    if not git_dir.is_dir() or git_dir.is_symlink():
        raise BackupError("BACKUP_GIT_EXTERNAL: a standalone Workspace .git directory is required")
    if any(os.path.lexists(git_dir / name) for name in ("commondir", "objects/info/alternates")):
        raise BackupError("BACKUP_GIT_EXTERNAL: shared Git object stores are not supported")
    if any((git_dir / "objects/pack").glob("*.promisor")):
        raise BackupError("BACKUP_GIT_EXTERNAL: partial clones must be materialized first")
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    environment.update({
        "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_OPTIONAL_LOCKS": "0", "GIT_NO_REPLACE_OBJECTS": "1", "GIT_TERMINAL_PROMPT": "0",
        "GIT_ATTR_NOSYSTEM": "1",
        # Even object reads can invoke a partial clone's configured remote helper.
        "GIT_NO_LAZY_FETCH": "1", "GIT_ALLOW_PROTOCOL": "",
    })
    prefix = [
        "git", "--no-pager", "-C", str(workspace), f"--git-dir={git_dir}", f"--work-tree={workspace}",
        "-c", "core.fsmonitor=false", "-c", f"core.hooksPath={os.devnull}",
        "-c", "core.untrackedCache=false", "-c", "gc.auto=0", "-c", f"core.attributesFile={os.devnull}",
    ]
    # A partial clone can have no promisor pack yet, including on an unborn
    # branch. Inspect only config key names and promisor booleans, never URLs
    # or credentials. Includes use the same effective config as object reads.
    config_names = run(prefix + ["config", "--includes", "--null", "--name-only", "--list"],
                       operation="BACKUP_GIT_CONFIG_FAILED", environment=environment).split(b"\0")
    for name in sorted(set(config_names)):
        if name.lower() == b"extensions.partialclone":
            raise BackupError("BACKUP_GIT_EXTERNAL: partial clones must be materialized first")
        if re.fullmatch(rb"remote\..+\.promisor", name.lower()):
            flags = run(prefix + ["config", "--includes", "--bool", "--get-all", name.decode()],
                        operation="BACKUP_GIT_CONFIG_FAILED", environment=environment).splitlines()
            if not flags or any(flag != b"false" for flag in flags):
                raise BackupError("BACKUP_GIT_EXTERNAL: promisor remotes are not supported")
    head = run(prefix + ["rev-parse", "--verify", "--quiet", "HEAD^{commit}"],
               operation="BACKUP_GIT_HEAD_FAILED", environment=environment, missing_ok=True).decode().strip()
    branch_ref = run(prefix + ["symbolic-ref", "--quiet", "HEAD"],
                     operation="BACKUP_GIT_HEAD_FAILED", environment=environment, missing_ok=True).decode().strip()
    if (head and re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", head) is None
            or not head and not branch_ref
            or branch_ref and not branch_ref.startswith("refs/heads/")):
        raise BackupError("BACKUP_GIT_HEAD_INVALID: valid committed HEAD or unborn branch required")
    if branch_ref:
        run(prefix + ["check-ref-format", branch_ref],
            operation="BACKUP_GIT_HEAD_INVALID", environment=environment)
    if not head:
        # rev-parse exit 1 also covers broken refs and missing objects. Git's
        # own integrity checker accepts a genuine unborn branch and rejects
        # those cases; do not infer an empty repository from failure alone.
        run(prefix + ["fsck", "--full", "--no-dangling"],
            operation="BACKUP_GIT_HEAD_FAILED", environment=environment)
    # Like gitcli.Status, reject content filters before status can execute them.
    tracked = run(prefix + ["ls-files", "--cached", "-z"],
                  operation="BACKUP_GIT_FILTER_CHECK_FAILED", environment=environment)
    if tracked:
        attributes = run(prefix + ["check-attr", "-z", "--stdin", "filter"], input_bytes=tracked,
                         operation="BACKUP_GIT_FILTER_CHECK_FAILED", environment=environment).split(b"\0")
        if attributes.pop() != b"" or len(attributes) % 3:
            raise BackupError("BACKUP_GIT_FILTER_CHECK_FAILED: invalid attribute output")
        for index in range(0, len(attributes), 3):
            if attributes[index + 1] != b"filter" or attributes[index + 2] not in (b"unspecified", b"unset"):
                raise BackupError("BACKUP_GIT_FILTER_UNSUPPORTED: tracked content filters are not supported")
    status = run(prefix + ["status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all"],
                 operation="BACKUP_GIT_STATUS_FAILED", environment=environment)
    return {"head": head or None, "branch": branch_ref.removeprefix("refs/heads/") or None,
            "branch_state": "unborn" if not head else "attached" if branch_ref else "detached",
            "dirty": bool(status), "status_sha256": hashlib.sha256(status).hexdigest()}


def container_identity(container: str) -> list[str]:
    if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", container) is None:
        raise BackupError("BACKUP_CONTAINER_INVALID: specify one PostgreSQL container name or ID")
    fields = run([
        "docker", "inspect", "--type", "container", "--format",
        "{{.Id}} {{.Image}} {{.State.Running}} {{.State.StartedAt}}", container,
    ], operation="BACKUP_CONTAINER_UNAVAILABLE").decode().split()
    if (len(fields) != 4 or re.fullmatch(r"[0-9a-f]{64}", fields[0]) is None
            or re.fullmatch(r"sha256:[0-9a-f]{64}", fields[1]) is None or fields[2] != "true"):
        raise BackupError("BACKUP_CONTAINER_UNAVAILABLE: PostgreSQL must already be running")
    return fields


def pg_command(container: str, tool: str, database: str, username: str) -> list[str]:
    # Use the chosen container's local socket. No DSN/password enters argv or output.
    return [
        "docker", "exec", "-i", "--env", "PGCONNECT_TIMEOUT=10", container, tool,
        "--no-password", "--host=/var/run/postgresql", "--port=5432",
        f"--username={username}", f"--dbname={database}",
    ]


def migration_marker(engine: str, rows: object) -> dict:
    invalid = "BACKUP_SCHEMA_INVALID: complete Atlas or supported legacy Goose history is required"
    if not isinstance(rows, list) or not rows:
        raise BackupError(invalid)
    if engine == "atlas":
        versions = []
        for row in rows:
            if (not isinstance(row, list) or len(row) != 4
                    or not isinstance(row[0], str) or re.fullmatch(r"[0-9]+", row[0]) is None
                    or type(row[1]) is not int or type(row[2]) is not int
                    or row[1] < 0 or row[1] != row[2] or row[3] is not False):
                raise BackupError(invalid)
            versions.append(row[0])
        if len(set(versions)) != len(versions):
            raise BackupError(invalid)
        version = max(versions)
    elif engine == "goose":
        latest = {}
        previous_id = 0
        for row in rows:
            if (not isinstance(row, list) or len(row) != 3
                    or type(row[0]) is not int or row[0] <= previous_id
                    or type(row[1]) is not int or not 0 <= row[1] <= LEGACY_GOOSE_MAX_VERSION
                    or type(row[2]) is not bool):
                raise BackupError(invalid)
            previous_id = row[0]
            latest[row[1]] = row[2]
        # Goose records both Up and Down. The last event per version, not the
        # largest historically applied version, determines the current state.
        applied = sorted(version for version, active in latest.items() if version > 0 and active)
        if (latest.get(0) is not True or not applied
                or applied != list(range(1, applied[-1] + 1))):
            raise BackupError(invalid)
        version = str(applied[-1])
    else:
        raise BackupError(invalid)
    return {
        "migration_engine": engine, "migration_history_table": HISTORY_TABLES[engine],
        "migration_history_sha256": hashlib.sha256(
            json.dumps(rows, separators=(",", ":")).encode()
        ).hexdigest(),
        "schema_version": version, "incomplete_migrations": 0,
    }


def database_marker(args: argparse.Namespace, container: str, workspace: Path) -> dict:
    command = pg_command(container, "psql", args.database, args.username) + [
        "-X", "--quiet", "--tuples-only", "--no-align", "--set=ON_ERROR_STOP=1",
        f"--set=workspace_id={args.workspace_id}",
    ]
    presence = json.loads(run(command, operation="BACKUP_DATABASE_MARKER_FAILED",
                              input_bytes=("BEGIN READ ONLY;\n" + HISTORY_PRESENCE_SQL + ";\nCOMMIT;\n").encode()))
    if not isinstance(presence, dict) or set(presence) != set(HISTORY_TABLES):
        raise BackupError("BACKUP_SCHEMA_INVALID: migration history could not be identified")
    if presence["atlas"] is True and presence["goose"] is False:
        engine = "atlas"
    elif presence["goose"] is True and presence["atlas"] is False:
        engine = "goose"
    else:
        raise BackupError("BACKUP_SCHEMA_INVALID: missing or mixed migration histories are not supported")
    # Select only a known, existing relation. A CASE containing both relations
    # would still fail PostgreSQL parsing when the other table does not exist.
    sql = MARKER_SQL.format(history_presence=HISTORY_PRESENCE_SQL, history_rows=HISTORY_ROWS_SQL[engine])
    value = run(command, operation="BACKUP_DATABASE_MARKER_FAILED", input_bytes=sql.encode())
    marker = json.loads(value)
    if (not isinstance(marker, dict) or marker.get("workspace_id") != args.workspace_id
            or marker.get("root_path") != str(workspace)
            or marker.get("git_repository_path") != str(workspace)):
        raise BackupError("BACKUP_WORKSPACE_MISMATCH: database ID and exact Root must match")
    # Discovery and the marker use separate read-only transactions. Recheck
    # relation presence in the marker's snapshot so adoption cannot go unseen.
    if marker.pop("migration_history_presence", None) != presence:
        raise BackupError("BACKUP_SOURCE_CHANGED: migration history changed during inspection")
    marker.update(migration_marker(engine, marker.pop("migration_rows", None)))
    return marker


def archive_workspace(workspace: Path, target: Path, snapshot: dict) -> None:
    def check_member(member: tarfile.TarInfo) -> tarfile.TarInfo:
        if not (member.isdir() or member.isfile() or member.islnk()):
            raise BackupError("BACKUP_WORKSPACE_CHANGED: unsupported file appeared during backup")
        return member

    with tarfile.open(target, "x:gz") as archive:
        for relative in snapshot:
            archive.add(workspace / relative, arcname=str(PurePosixPath("workspace") / relative),
                        recursive=False, filter=check_member)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def create(args: argparse.Namespace) -> None:
    if not args.confirm_stopped:
        raise BackupError("BACKUP_WRITERS_NOT_CONFIRMED: stop all writers, then pass --confirm-stopped")
    for value in (args.database, args.username):
        if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_-]{0,62}", value) is None:
            raise BackupError("BACKUP_DATABASE_ARGUMENT_INVALID: simple database/user names only; no DSN")
    if re.fullmatch(r"[0-9a-f]{40}|(?:sha256:)?[0-9a-f]{64}", args.app_version) is None:
        raise BackupError("BACKUP_VERSION_INVALID: supply a full Git commit SHA or sha256 image ID")
    if not 1 <= args.timeout_seconds <= 86400:
        raise BackupError("BACKUP_ARGUMENT_INVALID: timeout must be between 1 and 86400 seconds")
    try:
        if str(uuid.UUID(args.workspace_id)) != args.workspace_id:
            raise ValueError
    except ValueError:
        raise BackupError("BACKUP_WORKSPACE_ID_INVALID: canonical UUID required") from None
    workspace = absolute_directory(args.workspace)
    destination = new_destination(args.output, workspace)
    snapshot = tree_snapshot(workspace)
    git = git_state(workspace)
    identity = container_identity(args.postgres_container)
    container = identity[0]
    marker = database_marker(args, container, workspace)
    dump_version = run(["docker", "exec", container, "pg_dump", "--version"],
                       operation="BACKUP_PG_DUMP_UNAVAILABLE").decode().strip()
    started_at = utc_now()
    destination.mkdir(mode=0o700, exist_ok=False)
    # A failed run keeps its new private directory. SHA256SUMS is published last.
    archive_workspace(workspace, destination / "workspace.tar.gz", snapshot)
    with (destination / "database.dump").open("xb") as output:
        run(pg_command(container, "pg_dump", args.database, args.username)
            + ["--format=custom", "--lock-wait-timeout=10s"],
            operation="BACKUP_PG_DUMP_FAILED", stdout=output, timeout=args.timeout_seconds)
    with (destination / "database.dump").open("rb") as source:
        run(["docker", "exec", "-i", container, "pg_restore", "--list"], stdin=source,
            stdout=subprocess.DEVNULL, operation="BACKUP_DUMP_UNREADABLE", timeout=args.timeout_seconds)
    if (tree_snapshot(workspace) != snapshot or git_state(workspace) != git
            or database_marker(args, container, workspace) != marker
            or container_identity(container) != identity):
        raise BackupError("BACKUP_SOURCE_CHANGED: retain incomplete output and repeat after stopping writers")
    manifest = {
        "format": FORMAT, "started_at": started_at, "completed_at": utc_now(),
        "application_version_operator_supplied": args.app_version,
        "workspace": {"id": args.workspace_id, "canonical_root": str(workspace), "git": git},
        "database": {"scope": "whole_database", "marker": marker,
                     "image_id": identity[1], "pg_dump_version": dump_version},
        "artifact_bytes": {name: (destination / name).stat().st_size for name in ARTIFACTS[:2]},
        "verification": {"pg_restore_list": "passed", "writers_stopped_operator_confirmed": True,
                         "application_consistency_checked": False, "database_restore_checked": False},
        "secrets": "Sensitive backup; use encrypted storage. Deployment configuration and master key are not collected.",
    }
    with (destination / "manifest.json").open("x", encoding="utf-8") as output:
        json.dump(manifest, output, ensure_ascii=True, indent=2)
        output.write("\n")
    checksums = "".join(f"{sha256_file(destination / name)}  {name}\n" for name in ARTIFACTS)
    for name in ARTIFACTS:
        with (destination / name).open("rb") as source:
            os.fsync(source.fileno())
    with (destination / "SHA256SUMS").open("x", encoding="ascii") as output:
        output.write(checksums)
        output.flush()
        os.fsync(output.fileno())
    directory_fd = os.open(destination, os.O_RDONLY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
    print("BACKUP_CREATED: private bundle complete; run verify before copying or restoring")


def verify(args: argparse.Namespace) -> None:
    backup = absolute_directory(args.backup)
    if backup.stat().st_mode & 0o077:
        raise BackupError("BACKUP_PERMISSIONS_INVALID: backup directory must be private (0700)")
    for name in (*ARTIFACTS, "SHA256SUMS"):
        info = (backup / name).lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_mode & 0o077:
            raise BackupError("BACKUP_FILE_INVALID: artifacts must be private regular files (0600)")
    with (backup / "SHA256SUMS").open("r", encoding="ascii") as source:
        lines = source.read(1024).splitlines()
    if len(lines) != len(ARTIFACTS):
        raise BackupError("BACKUP_CHECKSUM_INVALID: incomplete checksum list")
    for name, line in zip(ARTIFACTS, lines):
        if (re.fullmatch(r"[0-9a-f]{64}  " + re.escape(name), line) is None
                or line[:64] != sha256_file(backup / name)):
            raise BackupError("BACKUP_CHECKSUM_MISMATCH: do not restore this bundle")
    with (backup / "manifest.json").open("rb") as source:
        manifest = json.loads(source.read(65536))
    if not isinstance(manifest, dict) or manifest.get("format") != FORMAT:
        raise BackupError("BACKUP_FORMAT_INVALID: unsupported backup format")
    with (backup / "database.dump").open("rb") as source:
        if source.read(5) != b"PGDMP":
            raise BackupError("BACKUP_DUMP_INVALID: PostgreSQL custom archive required")
    files = set()
    with tarfile.open(backup / "workspace.tar.gz", "r|gz") as archive:
        for member in archive:
            name = PurePosixPath(member.name)
            if (name.is_absolute() or ".." in name.parts or not name.parts
                    or name.parts[0] != "workspace"):
                raise BackupError("BACKUP_ARCHIVE_INVALID: member outside Workspace")
            if member.isfile():
                source = archive.extractfile(member)
                if source is None:
                    raise BackupError("BACKUP_ARCHIVE_INVALID: unreadable member")
                with source:
                    while source.read(1024 * 1024):
                        pass
                files.add(member.name)
            elif member.islnk():
                if member.linkname not in files:
                    raise BackupError("BACKUP_ARCHIVE_INVALID: invalid hardlink")
                files.add(member.name)
            elif not member.isdir():
                raise BackupError("BACKUP_ARCHIVE_INVALID: unsupported member type")
    if "workspace/.git/HEAD" not in files:
        raise BackupError("BACKUP_ARCHIVE_INVALID: Workspace Git history is missing")
    print("BACKUP_VERIFIED: checksums and Workspace archive readable; database restore/application consistency not checked")


def main() -> int:
    parser = SafeParser(description=__doc__, epilog=(
        "Stop API, Worker, model runtime, external editors/sync and all other DB/Workspace writers first. "
        "Keep encrypted storage, deployment secrets and the model/Git master key separately. "
        "One Workspace's files + the WHOLE database are copied; no service is stopped, started or restored."
    ))
    commands = parser.add_subparsers(dest="command", required=True)
    create_parser = commands.add_parser("create", help="create a new private backup directory", description=(
        "Requires stopped writers, an existing private/encrypted destination parent, a standalone Git "
        "Workspace and the running PostgreSQL container. Configuration/master keys are kept separately. "
        "The database dump includes every registered Workspace; the file archive includes only --workspace."
    ))
    for name in ("workspace", "workspace-id", "postgres-container", "output"):
        create_parser.add_argument(f"--{name}", required=True)
    create_parser.add_argument("--app-version", required=True,
                               help="operator-supplied full Git commit SHA or sha256 image ID; no moving tags")
    create_parser.add_argument("--database", default="zhixu")
    create_parser.add_argument("--username", default="zhixu")
    create_parser.add_argument("--confirm-stopped", action="store_true")
    create_parser.add_argument("--timeout-seconds", type=int, default=3600)
    verify_parser = commands.add_parser("verify", help="verify checksums/archive without a database connection")
    verify_parser.add_argument("--backup", required=True)
    args = parser.parse_args()
    previous_umask = os.umask(0o077)
    try:
        if args.command == "create":
            create(args)
        else:
            verify(args)
    except BackupError as error:
        print(str(error), file=sys.stderr)
        return 1
    except (OSError, ValueError, UnicodeError, tarfile.TarError):
        print("BACKUP_IO_FAILED: check files, permissions and metadata; incomplete output is retained", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("BACKUP_INTERRUPTED: incomplete output is retained; services are unchanged", file=sys.stderr)
        return 130
    finally:
        os.umask(previous_umask)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
