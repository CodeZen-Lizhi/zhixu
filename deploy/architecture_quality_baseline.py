#!/usr/bin/env python3
"""Generate a deterministic, read-only architecture quality baseline."""

from __future__ import annotations

import argparse
import ast
import gzip
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys
import tempfile
from typing import Any, Iterable


SCHEMA_VERSION = "architecture-quality-baseline/v1"
GO_ROOTS = ("cmd/", "internal/", "eval/", "atlas/", "poc/eino/")
GO_HELPERS = ("decodeJSON", "parseID", "writeError")
HOTSPOT_THRESHOLDS = (500, 1000)
HOTSPOT_LIMIT = 20
UUID_HELPER_PATTERN = re.compile(
    r"^(?:decode|nullable|optional|read|require|validate)[A-Za-z0-9_]*(?:Uuid|UUID)[A-Za-z0-9_]*$"
)
GO_IMPORT_BLOCK_PATTERN = re.compile(
    r"(?ms)^[ \t]*import[ \t]*\((?P<body>.*?)^[ \t]*\)"
)
GO_IMPORT_SINGLE_PATTERN = re.compile(
    r"(?m)^[ \t]*import[ \t]+"
    r"(?:(?:[._A-Za-z][A-Za-z0-9_]*)[ \t]+)?"
    r"(?P<path>\"(?:\\.|[^\"\\\n])*\"|`[^`]*`)"
)
GO_STRING_PATTERN = re.compile(r"\"(?:\\.|[^\"\\\n])*\"|`[^`]*`")


def read_utf8(
    path: Path,
    display_path: str | None = None,
    repository: Path | None = None,
) -> str:
    label = display_path if display_path is not None else path.name
    if repository is not None:
        try:
            relative_path = path.relative_to(repository)
        except ValueError as exc:
            raise ValueError(f"tracked file is outside repository: {label}") from exc
        current = repository
        for index, part in enumerate(relative_path.parts):
            current = current / part
            try:
                metadata = current.lstat()
            except OSError as exc:
                raise ValueError(f"tracked file is unavailable: {label}") from exc
            if stat.S_ISLNK(metadata.st_mode):
                raise ValueError(f"tracked path contains a symlink: {label}")
            is_final = index == len(relative_path.parts) - 1
            expected_type = stat.S_ISREG if is_final else stat.S_ISDIR
            if not expected_type(metadata.st_mode):
                raise ValueError(f"tracked input is not a regular file: {label}")
    else:
        try:
            metadata = path.lstat()
        except OSError as exc:
            raise ValueError(f"tracked file is unavailable: {label}") from exc
        if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
            raise ValueError(f"tracked input is not a regular file: {label}")
    try:
        return path.read_text(encoding="utf-8")
    except UnicodeDecodeError as exc:
        raise ValueError(f"supported source is not UTF-8: {label}") from exc


def physical_lines(text: str) -> int:
    """Match the existing audit's wc -l convention."""
    return text.count("\n")


def tracked_files(repository: Path) -> list[str]:
    completed = subprocess.run(
        ["git", "ls-files", "-z", "--cached"],
        cwd=repository,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    deleted = subprocess.run(
        ["git", "ls-files", "-z", "--deleted"],
        cwd=repository,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    deleted_paths = {
        value for value in deleted.stdout.decode("utf-8").split("\0") if value
    }
    paths: list[str] = []
    for value in completed.stdout.decode("utf-8").split("\0"):
        if not value or value in deleted_paths:
            continue
        path = PurePosixPath(value)
        if path.is_absolute() or any(part in {"", ".", ".."} for part in path.parts):
            raise ValueError(f"git returned an invalid tracked path: {value!r}")
        paths.append(path.as_posix())
    return sorted(set(paths))


def is_go_source(path: str) -> bool:
    return path.endswith(".go") and path.startswith(GO_ROOTS)


def is_web_source(path: str) -> bool:
    return path.startswith("web/src/") and path.endswith((".ts", ".tsx"))


def is_web_test(path: str) -> bool:
    return bool(re.search(r"\.(?:test|spec)\.tsx?$", path))


def inventory(paths: Iterable[str], contents: dict[str, str]) -> dict[str, int]:
    selected = list(paths)
    return {
        "file_count": len(selected),
        "physical_lines": sum(physical_lines(contents[path]) for path in selected),
    }


def line_number(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def mask_go_comments_and_literals(text: str, *, keep_strings: bool = False) -> str:
    """Mask Go comments and, optionally, literals while preserving newlines."""
    output = list(text)
    index = 0
    length = len(text)

    def mask(start: int, end: int) -> None:
        for position in range(start, end):
            if output[position] not in ("\n", "\r"):
                output[position] = " "

    while index < length:
        if text.startswith("//", index):
            end = text.find("\n", index)
            if end == -1:
                end = length
            mask(index, end)
            index = end
            continue
        if text.startswith("/*", index):
            end_marker = text.find("*/", index + 2)
            end = length if end_marker == -1 else end_marker + 2
            mask(index, end)
            index = end
            continue
        quote = text[index]
        if quote in ('"', "'", "`"):
            start = index
            index += 1
            if quote == "`":
                end_marker = text.find("`", index)
                index = length if end_marker == -1 else end_marker + 1
            else:
                while index < length:
                    if text[index] == "\\":
                        index += 2
                        continue
                    if text[index] == quote:
                        index += 1
                        break
                    index += 1
            if keep_strings:
                closed = index > start + 1 and index <= length and text[index - 1] == quote
                content_end = index - 1 if closed else index
                mask(start + 1, min(content_end, length))
            else:
                mask(start, min(index, length))
            continue
        index += 1
    return "".join(output)


def mask_typescript_comments_and_literals(text: str) -> str:
    """Mask TypeScript comments, strings, templates, and regex literals."""
    output = list(text)
    index = 0
    state = "code"
    regex_character_class = False
    previous_significant = ""
    regex_prefixes = set("=(:,![{;?&|+-*%^~<>")

    def mask(position: int) -> None:
        if output[position] not in ("\n", "\r"):
            output[position] = " "

    while index < len(text):
        char = text[index]
        next_char = text[index + 1] if index + 1 < len(text) else ""

        if state == "code":
            if char == "/" and next_char == "/":
                mask(index)
                mask(index + 1)
                state = "line_comment"
                index += 2
                continue
            if char == "/" and next_char == "*":
                mask(index)
                mask(index + 1)
                state = "block_comment"
                index += 2
                continue
            if char in ('"', "'", "`"):
                mask(index)
                state = {"\"": "double", "'": "single", "`": "template"}[char]
                index += 1
                continue
            if char == "/" and (previous_significant == "" or previous_significant in regex_prefixes):
                mask(index)
                state = "regex"
                regex_character_class = False
                index += 1
                continue
            if not char.isspace():
                previous_significant = char
            index += 1
            continue

        if state == "line_comment":
            if char == "\n":
                state = "code"
            else:
                mask(index)
            index += 1
            continue

        if state == "block_comment":
            if char == "*" and next_char == "/":
                mask(index)
                mask(index + 1)
                state = "code"
                index += 2
                continue
            mask(index)
            index += 1
            continue

        if state in {"double", "single", "template"}:
            mask(index)
            if char == "\\" and index + 1 < len(text):
                mask(index + 1)
                index += 2
                continue
            delimiter = {"double": '"', "single": "'", "template": "`"}[state]
            if char == delimiter:
                state = "code"
                previous_significant = delimiter
            index += 1
            continue

        if state == "regex":
            mask(index)
            if char == "\n":
                state = "code"
                previous_significant = ""
                index += 1
                continue
            if char == "\\" and index + 1 < len(text):
                mask(index + 1)
                index += 2
                continue
            if char == "[":
                regex_character_class = True
            elif char == "]":
                regex_character_class = False
            elif char == "/" and not regex_character_class:
                state = "code"
                previous_significant = "/"
            index += 1
            continue

    return "".join(output)


def go_helper_signals(
    go_paths: Iterable[str], contents: dict[str, str]
) -> dict[str, dict[str, Any]]:
    matches: dict[str, list[dict[str, Any]]] = {name: [] for name in GO_HELPERS}
    pattern = re.compile(
        r"(?m)^func[ \t]+(" + "|".join(GO_HELPERS) + r")(?=[ \t]*(?:\[|\())"
    )
    for path in go_paths:
        scrubbed = mask_go_comments_and_literals(contents[path])
        for match in pattern.finditer(scrubbed):
            matches[match.group(1)].append(
                {
                    "line": line_number(scrubbed, match.start()),
                    "path": path,
                    "scope": "test" if path.endswith("_test.go") else "production",
                }
            )

    result: dict[str, dict[str, Any]] = {}
    for name in GO_HELPERS:
        locations = sorted(matches[name], key=lambda item: (item["path"], item["line"]))
        result[name] = {
            "count": len(locations),
            "metric": f"exact top-level Go function named {name}",
            "production_count": sum(item["scope"] == "production" for item in locations),
            "test_count": sum(item["scope"] == "test" for item in locations),
            "locations": locations,
        }
    return result


def web_function_names(text: str) -> list[tuple[str, int]]:
    declaration_pattern = re.compile(
        r"(?m)^(?:export[ \t]+(?:default[ \t]+)?)?"
        r"(?:async[ \t]+)?(?:function|const|let|var)"
        r"[ \t]+([A-Za-z_$][\w$]*)\b"
    )
    scrubbed = mask_typescript_comments_and_literals(text)
    found = [
        (match.group(1), line_number(scrubbed, match.start()))
        for match in declaration_pattern.finditer(scrubbed)
    ]
    return sorted(found, key=lambda item: (item[1], item[0]))


def web_helper_signals(
    web_paths: Iterable[str], contents: dict[str, str]
) -> dict[str, dict[str, Any]]:
    is_record: list[dict[str, Any]] = []
    uuid_helpers: list[dict[str, Any]] = []
    for path in web_paths:
        if not path.startswith("web/src/api/") or is_web_test(path):
            continue
        for name, line in web_function_names(contents[path]):
            location = {"identifier": name, "line": line, "path": path}
            if name == "isRecord":
                is_record.append(location)
            if UUID_HELPER_PATTERN.fullmatch(name):
                uuid_helpers.append(location)
    is_record.sort(key=lambda item: (item["path"], item["line"], item["identifier"]))
    uuid_helpers.sort(key=lambda item: (item["path"], item["line"], item["identifier"]))
    return {
        "isRecord": {
            "count": len(is_record),
            "metric": "exact top-level web/src/api identifier named isRecord",
            "locations": is_record,
        },
        "uuid_reader_or_validator": {
            "count": len(uuid_helpers),
            "identifier_rule": (
                "prefix=(decode|nullable|optional|read|require|validate), "
                "contains=(Uuid|UUID)"
            ),
            "locations": uuid_helpers,
        },
    }


def module_path(repository: Path) -> str:
    for line in read_utf8(repository / "go.mod", "go.mod", repository).splitlines():
        if line.startswith("module "):
            return line.removeprefix("module ").strip()
    raise ValueError("go.mod does not declare a module")


def decode_go_import(literal: str) -> str:
    if literal.startswith("`"):
        return literal[1:-1]
    try:
        value = ast.literal_eval(literal)
    except (SyntaxError, ValueError) as exc:
        raise ValueError(f"unsupported Go import string: {literal!r}") from exc
    if not isinstance(value, str):
        raise ValueError(f"unsupported Go import string: {literal!r}")
    return value


def go_imports(text: str) -> list[tuple[str, int]]:
    comment_free = mask_go_comments_and_literals(text, keep_strings=True)
    imports: list[tuple[str, int]] = []
    for block in GO_IMPORT_BLOCK_PATTERN.finditer(comment_free):
        body_start = block.start("body")
        for literal in GO_STRING_PATTERN.finditer(block.group("body")):
            offset = body_start + literal.start()
            original = text[offset : body_start + literal.end()]
            imports.append((decode_go_import(original), line_number(text, offset)))
    for single in GO_IMPORT_SINGLE_PATTERN.finditer(comment_free):
        start = single.start("path")
        end = single.end("path")
        imports.append(
            (
                decode_go_import(text[start:end]),
                line_number(text, start),
            )
        )
    return sorted(imports, key=lambda item: (item[1], item[0]))


def domain_dependencies(
    repository: Path, go_paths: Iterable[str], contents: dict[str, str]
) -> dict[str, Any]:
    module = module_path(repository)
    project_prefix = f"{module}/internal/"
    domain_pattern = re.compile(r"^internal/([^/]+)/domain(?:/|$)")
    import_locations: list[dict[str, Any]] = []
    reverse: list[dict[str, Any]] = []
    edges: set[str] = set()

    for path in go_paths:
        if path.endswith("_test.go"):
            continue
        consumer_match = domain_pattern.match(path)
        if consumer_match is None:
            continue
        consumer = consumer_match.group(1)
        for imported, line in go_imports(contents[path]):
            if not imported.startswith(project_prefix):
                continue
            import_parts = imported.removeprefix(project_prefix).split("/")
            if not import_parts:
                continue
            owner = import_parts[0]
            location = {"line": line, "path": path}
            if len(import_parts) >= 2 and import_parts[1] == "domain" and owner != consumer:
                edge = f"{consumer} -> {owner}"
                edges.add(edge)
                import_locations.append({**location, "edge": edge, "import": imported})
            forbidden_layers = sorted(
                {part for part in import_parts[1:] if part in {"adapter", "http"}}
            )
            for layer in forbidden_layers:
                reverse.append({**location, "import": imported, "layer": layer})

    return {
        "cross_domain_edge_count": len(edges),
        "cross_domain_edges": sorted(edges),
        "cross_domain_import_count": len(import_locations),
        "cross_domain_imports": sorted(import_locations, key=lambda item: (item["path"], item["line"], item["import"])),
        "domain_to_adapter_or_http_count": len(reverse),
        "domain_to_adapter_or_http": sorted(reverse, key=lambda item: (item["path"], item["line"], item["import"])),
    }


def test_assets(go_paths: Iterable[str], e2e_paths: Iterable[str], contents: dict[str, str]) -> dict[str, Any]:
    integration_named = sorted(path for path in go_paths if path.endswith("integration_test.go"))
    integration_token_named = sorted(
        path
        for path in go_paths
        if path.endswith("_test.go") and "integration" in PurePosixPath(path).name
    )
    integration_tagged = sorted(
        path
        for path in go_paths
        if path.endswith("_test.go")
        and re.search(r"(?m)^//go:build[ \t]+integration[ \t]*$", contents[path])
    )
    skip_calls: list[dict[str, Any]] = []
    raw_skip_hits = 0
    skip_pattern = re.compile(r"(?<![A-Za-z0-9_.])t\s*\.\s*(Skip(?:f|Now)?)\s*\(")
    for path in go_paths:
        if not path.endswith("_test.go"):
            continue
        raw_skip_hits += contents[path].count("t.Skip")
        scrubbed = mask_go_comments_and_literals(contents[path])
        for match in skip_pattern.finditer(scrubbed):
            skip_calls.append(
                {"call": f"t.{match.group(1)}", "line": line_number(scrubbed, match.start()), "path": path}
            )
    skip_calls.sort(key=lambda item: (item["path"], item["line"], item["call"]))
    return {
        "go_integration_build_tag": {"count": len(integration_tagged), "paths": integration_tagged},
        "go_integration_named": {"count": len(integration_named), "paths": integration_named},
        "go_integration_token_named": {
            "count": len(integration_token_named),
            "metric": "Go test basename contains integration",
            "paths": integration_token_named,
        },
        "go_t_skip_calls": {
            "count": len(skip_calls),
            "file_count": len({item["path"] for item in skip_calls}),
            "locations": skip_calls,
            "raw_token_hits": raw_skip_hits,
        },
        "playwright_specs": {"count": len(list(e2e_paths)), "paths": sorted(e2e_paths)},
    }


def make_targets(makefile: str) -> dict[str, Any]:
    names: set[str] = set()
    for line in makefile.splitlines():
        if line.startswith(("\t", " ")) or ":" not in line or line.startswith("."):
            continue
        if re.match(
            r"^(?:(?:export|override|private|unexport)[ \t]+)?"
            r"[A-Za-z_][A-Za-z0-9_.-]*[ \t]*(?::=|\+=|\?=|!=|=)",
            line,
        ):
            continue
        target_part = line.split(":", 1)[0]
        for name in target_part.split():
            if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", name):
                names.add(name)
    relevant = sorted(
        name for name in names if any(token in name for token in ("benchmark", "browser", "integration", "smoke"))
    )
    return {
        "metric": "target name contains integration|smoke|benchmark|browser",
        "quality_related": relevant,
        "quality_related_count": len(relevant),
    }


def web_bundle(web_dist: Path) -> dict[str, Any]:
    if web_dist.is_symlink():
        raise ValueError("web dist input must be a regular directory")
    if not web_dist.exists():
        return {"available": False}
    if web_dist.is_symlink() or not web_dist.is_dir():
        raise ValueError("web dist input must be a regular directory")
    assets_root = web_dist / "assets"
    if assets_root.is_symlink() or (assets_root.exists() and not assets_root.is_dir()):
        raise ValueError("web dist assets input must be a regular directory")
    if not assets_root.exists():
        return {"available": True, "assets": [], "asset_count": 0, "raw_bytes": 0, "gzip_bytes": 0}
    assets: list[dict[str, Any]] = []
    for directory, directory_names, file_names in os.walk(assets_root, followlinks=False):
        directory_path = Path(directory)
        for name in directory_names:
            if (directory_path / name).is_symlink():
                raise ValueError("web bundle contains a symlinked directory")
        directory_names.sort()
        for name in sorted(file_names):
            path = directory_path / name
            metadata = path.lstat()
            if stat.S_ISLNK(metadata.st_mode):
                raise ValueError("web bundle contains a symlinked file")
            if not stat.S_ISREG(metadata.st_mode):
                continue
            data = path.read_bytes()
            assets.append(
                {
                    "gzip_bytes": len(gzip.compress(data, compresslevel=9, mtime=0)),
                    "path": path.relative_to(web_dist).as_posix(),
                    "raw_bytes": len(data),
                }
            )
    assets.sort(key=lambda item: (-item["raw_bytes"], item["path"]))
    return {
        "available": True,
        "asset_count": len(assets),
        "assets": assets,
        "gzip_bytes": sum(item["gzip_bytes"] for item in assets),
        "metric": "regular files under assets; gzip level 9 with mtime=0",
        "raw_bytes": sum(item["raw_bytes"] for item in assets),
    }


def collect(repository: Path, web_dist: Path | None = None) -> dict[str, Any]:
    repository = repository.resolve()
    paths = tracked_files(repository)
    tracked = set(paths)
    for required_path in ("go.mod", "Makefile"):
        if required_path not in tracked:
            raise ValueError(f"tracked {required_path} is required")
    go_paths = [path for path in paths if is_go_source(path)]
    web_paths = [path for path in paths if is_web_source(path)]
    sql_paths = [
        path
        for path in paths
        if path.startswith("atlas/migrations/")
        and "/" not in path.removeprefix("atlas/migrations/")
        and path.endswith(".sql")
    ]
    e2e_paths = [path for path in paths if path.startswith("web/e2e/") and path.endswith(".spec.ts")]
    supported_paths = sorted(set(go_paths + web_paths + sql_paths + ["Makefile"]))
    contents = {
        path: read_utf8(repository / path, path, repository)
        for path in supported_paths
    }

    go_production = [path for path in go_paths if not path.endswith("_test.go")]
    go_tests = [path for path in go_paths if path.endswith("_test.go")]
    web_tests = [path for path in web_paths if is_web_test(path)]
    web_production = [path for path in web_paths if not is_web_test(path)]
    production_paths = go_production + web_production
    hotspot_rows = [
        {"lines": physical_lines(contents[path]), "path": path}
        for path in production_paths
        if physical_lines(contents[path]) >= min(HOTSPOT_THRESHOLDS)
    ]
    hotspot_rows.sort(key=lambda item: (-item["lines"], item["path"]))

    bundle_path = web_dist if web_dist is not None else repository / "web/dist"
    if not bundle_path.is_absolute():
        bundle_path = repository / bundle_path

    return {
        "boundary_helpers": {
            "interpretation": (
                "duplicate signals only; matching names do not prove equivalent behavior"
            ),
            "go": go_helper_signals(go_paths, contents),
            "web": web_helper_signals(web_paths, contents),
        },
        "complexity": {
            "hotspots": hotspot_rows[:HOTSPOT_LIMIT],
            "hotspots_limit": HOTSPOT_LIMIT,
            "metric": "physical lines in Go/Web production files; SQL and tests excluded",
            "thresholds": {
                str(threshold): {
                    "file_count": sum(item["lines"] >= threshold for item in hotspot_rows),
                    "physical_lines": sum(item["lines"] for item in hotspot_rows if item["lines"] >= threshold),
                }
                for threshold in HOTSPOT_THRESHOLDS
            },
        },
        "domain_dependencies": domain_dependencies(repository, go_paths, contents),
        "languages": {
            "go": {"production": inventory(go_production, contents), "test": inventory(go_tests, contents)},
            "line_metric": "physical LF line count (wc -l compatible)",
            "sql_migrations": inventory(sql_paths, contents),
            "web": {"production": inventory(web_production, contents), "unit_test": inventory(web_tests, contents)},
        },
        "make_targets": make_targets(contents["Makefile"]),
        "schema_version": SCHEMA_VERSION,
        "source": {"tracked_file_count": len(paths)},
        "test_assets": test_assets(go_paths, e2e_paths, contents),
        "web_bundle": web_bundle(bundle_path),
    }


def render_json(report: dict[str, Any]) -> str:
    return json.dumps(report, ensure_ascii=True, indent=2, sort_keys=True) + "\n"


def render_text(report: dict[str, Any]) -> str:
    languages = report["languages"]
    complexity = report["complexity"]["thresholds"]
    helpers = report["boundary_helpers"]
    dependencies = report["domain_dependencies"]
    tests = report["test_assets"]
    bundle = report["web_bundle"]
    lines = [
        f"Architecture quality baseline ({report['schema_version']})",
        f"Tracked files: {report['source']['tracked_file_count']}",
        (
            f"Go production: {languages['go']['production']['file_count']} files / "
            f"{languages['go']['production']['physical_lines']} lines; tests: "
            f"{languages['go']['test']['file_count']} files / "
            f"{languages['go']['test']['physical_lines']} lines"
        ),
        (
            f"Web production: {languages['web']['production']['file_count']} files / "
            f"{languages['web']['production']['physical_lines']} lines; unit tests: "
            f"{languages['web']['unit_test']['file_count']} files / "
            f"{languages['web']['unit_test']['physical_lines']} lines"
        ),
        (
            f"SQL migrations: {languages['sql_migrations']['file_count']} files / "
            f"{languages['sql_migrations']['physical_lines']} lines"
        ),
        f"Production files >=500 lines: {complexity['500']['file_count']}; >=1000 lines: {complexity['1000']['file_count']}",
        "Go helpers: " + ", ".join(f"{name}={helpers['go'][name]['count']}" for name in GO_HELPERS),
        f"Web helpers: isRecord={helpers['web']['isRecord']['count']}, UUID={helpers['web']['uuid_reader_or_validator']['count']}",
        f"Domain edges: {dependencies['cross_domain_edge_count']}; reverse adapter/http imports: {dependencies['domain_to_adapter_or_http_count']}",
        (
            "Integration files strict/broad, tags, t.Skip calls/raw tokens: "
            f"{tests['go_integration_named']['count']}/"
            f"{tests['go_integration_token_named']['count']}/"
            f"{tests['go_integration_build_tag']['count']}/"
            f"{tests['go_t_skip_calls']['count']}/"
            f"{tests['go_t_skip_calls']['raw_token_hits']}"
        ),
        f"Playwright specs: {tests['playwright_specs']['count']}",
        f"Make integration/smoke/benchmark/browser targets: {report['make_targets']['quality_related_count']}",
        "Largest production files:",
    ]
    lines.extend(
        f"  {item['lines']:>6}  {item['path']}"
        for item in report["complexity"]["hotspots"][:5]
    )
    if bundle["available"]:
        lines.append(
            f"Web bundle: {bundle['asset_count']} assets / {bundle['raw_bytes']} raw bytes / "
            f"{bundle['gzip_bytes']} gzip bytes"
        )
    else:
        lines.append("Web bundle: unavailable")
    return "\n".join(lines) + "\n"


def write_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_name: str | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", delete=False
        ) as handle:
            temporary_name = handle.name
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary_name, path)
    finally:
        if temporary_name is not None and os.path.exists(temporary_name):
            os.unlink(temporary_name)


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--format", choices=("json", "text"), default="text")
    parser.add_argument("--web-dist", type=Path, default=Path("web/dist"))
    parser.add_argument("--output", type=Path)
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    repository = Path(__file__).resolve().parent.parent
    try:
        report = collect(repository, args.web_dist)
        rendered = render_json(report) if args.format == "json" else render_text(report)
        if args.output is None:
            sys.stdout.write(rendered)
        else:
            write_atomic(args.output, rendered)
    except (OSError, subprocess.CalledProcessError, ValueError) as exc:
        print(f"architecture quality baseline failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
