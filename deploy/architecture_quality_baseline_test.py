from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


MODULE_PATH = Path(__file__).with_name("architecture_quality_baseline.py")
SPEC = importlib.util.spec_from_file_location("architecture_quality_baseline", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
baseline = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(baseline)


class ArchitectureQualityBaselineTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.repository = Path(self.temporary_directory.name)
        subprocess.run(["git", "init", "-q"], cwd=self.repository, check=True)
        self.write("go.mod", "module example.com/project\n")
        self.write("Makefile", ".PHONY: test graph-integration graph-smoke\n\ntest:\n\t@true\n\ngraph-integration:\n\t@true\n\ngraph-smoke:\n\t@true\n")

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def write(self, relative_path: str, content: str, *, tracked: bool = True) -> None:
        path = self.repository / relative_path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        if tracked:
            subprocess.run(["git", "add", "--", relative_path], cwd=self.repository, check=True)

    def write_bytes(self, relative_path: str, content: bytes, *, tracked: bool = True) -> None:
        path = self.repository / relative_path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        if tracked:
            subprocess.run(["git", "add", "--", relative_path], cwd=self.repository, check=True)

    def collect(self, web_dist: Path | None = None) -> dict:
        return baseline.collect(self.repository, web_dist)

    def test_collects_tracked_sources_helpers_dependencies_and_tests(self) -> None:
        self.write("cmd/api/main.go", "package main\n\nfunc main() {}\n")
        self.write(
            "internal/alpha/domain/model.go",
            "package domain\n\nimport (\n\t\"example.com/project/internal/beta/domain\"\n\t\"example.com/project/internal/gamma/http\"\n)\n\nfunc parseID(v string) string { return v }\n",
        )
        self.write(
            "internal/alpha/domain/other.go",
            "package domain\nimport \"example.com/project/internal/beta/domain/value\"\n",
        )
        self.write(
            "internal/alpha/http/handler_test.go",
            "package http\n// t.Skip(\"comment\")\nfunc decodeJSON[T any]() {}\nfunc test(t interface{}) { t.Skip(\"db\") }\nfunc chained() { object.t.Skip(\"other receiver\") }\n",
        )
        self.write(
            "internal/alpha/http/handler_integration_test.go",
            "//go:build integration\n\npackage http\nfunc writeError() {}\n",
        )
        self.write(
            "web/src/api/example.ts",
            "function isRecord(value: unknown) { return true }\nconst readUuid = () => 'x'\n",
        )
        self.write("web/src/api/example.test.ts", "function isRecord() { return false }\n")
        self.write("web/e2e/example.spec.ts", "test('x', () => {})\n")
        self.write("migrations/00001_init.sql", "SELECT 1;\n")
        self.write("internal/untracked/domain/no.go", "package domain\n", tracked=False)

        report = self.collect()

        self.assertEqual(report["languages"]["go"]["production"]["file_count"], 3)
        self.assertEqual(report["languages"]["go"]["test"]["file_count"], 2)
        self.assertEqual(report["languages"]["web"]["production"]["file_count"], 1)
        self.assertEqual(report["languages"]["web"]["unit_test"]["file_count"], 1)
        self.assertEqual(report["languages"]["sql_migrations"]["file_count"], 1)
        self.assertEqual(report["boundary_helpers"]["go"]["parseID"]["production_count"], 1)
        self.assertEqual(report["boundary_helpers"]["go"]["decodeJSON"]["test_count"], 1)
        self.assertEqual(report["boundary_helpers"]["web"]["isRecord"]["count"], 1)
        self.assertEqual(report["boundary_helpers"]["web"]["uuid_reader_or_validator"]["count"], 1)
        self.assertEqual(report["domain_dependencies"]["cross_domain_edges"], ["alpha -> beta"])
        self.assertEqual(report["domain_dependencies"]["cross_domain_import_count"], 2)
        self.assertEqual(report["domain_dependencies"]["domain_to_adapter_or_http_count"], 1)
        self.assertEqual(report["test_assets"]["go_integration_named"]["count"], 1)
        self.assertEqual(report["test_assets"]["go_integration_token_named"]["count"], 1)
        self.assertEqual(report["test_assets"]["go_integration_build_tag"]["count"], 1)
        self.assertEqual(report["test_assets"]["go_t_skip_calls"]["count"], 1)
        self.assertEqual(report["test_assets"]["go_t_skip_calls"]["raw_token_hits"], 3)
        self.assertEqual(report["test_assets"]["playwright_specs"]["count"], 1)
        self.assertNotIn("internal/untracked/domain/no.go", json.dumps(report))

    def test_excludes_tracked_files_deleted_from_the_worktree(self) -> None:
        self.write("internal/alpha/domain/deleted.go", "package domain\n")
        (self.repository / "internal/alpha/domain/deleted.go").unlink()

        report = self.collect(self.repository / "missing-dist")

        self.assertEqual(0, report["languages"]["go"]["production"]["file_count"])

    def test_hotspots_include_exact_threshold_and_sort_stably(self) -> None:
        self.write("internal/alpha/large.go", "package alpha\n" + "// line\n" * 999)
        self.write("web/src/z.ts", "// line\n" * 500)
        self.write("web/src/a.ts", "// line\n" * 500)

        report = self.collect()

        self.assertEqual(report["complexity"]["thresholds"]["1000"]["file_count"], 1)
        self.assertEqual(
            [item["path"] for item in report["complexity"]["hotspots"]],
            ["internal/alpha/large.go", "web/src/a.ts", "web/src/z.ts"],
        )

    def test_bundle_missing_and_present_are_explicit_and_deterministic(self) -> None:
        missing = self.collect(self.repository / "missing-dist")
        self.assertEqual(missing["web_bundle"], {"available": False})

        dist = self.repository / "generated-dist"
        (dist / "assets").mkdir(parents=True)
        (dist / "assets/app.js").write_bytes(b"const value = 1;\n")
        first = baseline.render_json(self.collect(dist))
        second = baseline.render_json(self.collect(dist))

        self.assertEqual(first, second)
        self.assertNotIn(str(self.repository), first)
        parsed = json.loads(first)
        self.assertTrue(parsed["web_bundle"]["available"])
        self.assertEqual(parsed["web_bundle"]["assets"][0]["path"], "assets/app.js")

        os.symlink(dist / "assets", dist / "assets/linked")
        with self.assertRaisesRegex(ValueError, "symlinked directory"):
            self.collect(dist)

    def test_atomic_output_replaces_existing_file(self) -> None:
        output = self.repository / "reports/baseline.json"
        baseline.write_atomic(output, "first\n")
        baseline.write_atomic(output, "second\n")
        self.assertEqual(output.read_text(encoding="utf-8"), "second\n")
        self.assertEqual(output.stat().st_mode & 0o777, 0o600)

    def test_masks_frontend_comments_and_literals_before_helper_matching(self) -> None:
        self.write(
            "web/src/api/commented.ts",
            "/*\n"
            "const isRecord = () => true;\n"
            "const readUuid = () => 'fake';\n"
            "*/\n"
            "const template = `const isRecord = () => true;`;\n"
            "const quotedPattern = /^\\\"value\\\"$/;\n"
            "const isRecord = (value: unknown) => value !== null;\n"
            "const readUuid: (value: unknown) => string = (value) => String(value);\n"
            "const lowercaseuuid = (value: unknown) => String(value);\n",
        )
        report = self.collect(self.repository / "missing-dist")
        helpers = report["boundary_helpers"]["web"]
        self.assertEqual(1, helpers["isRecord"]["count"])
        self.assertEqual(1, helpers["uuid_reader_or_validator"]["count"])

    def test_frontend_helper_scan_supports_default_export_functions(self) -> None:
        self.write(
            "web/src/api/default-record.ts",
            "export default function isRecord(value: unknown) { return value !== null; }\n",
        )
        self.write(
            "web/src/api/default-uuid.ts",
            "export default async function readUUID(value: unknown) { return String(value); }\n",
        )

        helpers = self.collect(self.repository / "missing-dist")["boundary_helpers"]["web"]

        self.assertEqual(1, helpers["isRecord"]["count"])
        self.assertEqual(1, helpers["uuid_reader_or_validator"]["count"])

    def test_make_target_scan_ignores_variable_assignments(self) -> None:
        result = baseline.make_targets(
            "benchmark_mode := local\n"
            "export browser_mode ?= chromium\n"
            "graph-benchmark:\n\t@true\n"
        )

        self.assertEqual(["graph-benchmark"], result["quality_related"])

    def test_domain_import_scan_does_not_treat_string_literals_as_imports(self) -> None:
        self.write(
            "internal/alpha/domain/strings.go",
            "package domain\n\n"
            "import _ \"example.com/project/internal/beta/sub/adapter/postgres\"\n\n"
            "var example = \"example.com/project/internal/fake/domain\"\n"
            "var another = []string{\"example.com/project/internal/fake/domain\"}\n"
            "var sourceExample = `\n"
            "import (\n"
            "\t\"example.com/project/internal/fake/domain\"\n"
            ")\n"
            "`\n",
        )
        report = self.collect(self.repository / "missing-dist")
        dependencies = report["domain_dependencies"]
        self.assertEqual(0, dependencies["cross_domain_import_count"])
        self.assertNotIn("alpha -> fake", dependencies["cross_domain_edges"])
        self.assertEqual(1, dependencies["domain_to_adapter_or_http_count"])
        self.assertEqual("adapter", dependencies["domain_to_adapter_or_http"][0]["layer"])

    def test_tracked_symlink_and_invalid_utf8_fail_closed(self) -> None:
        outside = self.repository / "outside.go"
        outside.write_text("package outside\n", encoding="utf-8")
        symlink = self.repository / "internal/alpha/domain/linked.go"
        symlink.parent.mkdir(parents=True, exist_ok=True)
        os.symlink(outside, symlink)
        subprocess.run(["git", "add", "--", "internal/alpha/domain/linked.go"], cwd=self.repository, check=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.collect(self.repository / "missing-dist")

        subprocess.run(
            ["git", "rm", "--cached", "--", "internal/alpha/domain/linked.go"],
            cwd=self.repository,
            check=True,
            stdout=subprocess.DEVNULL,
        )
        symlink.unlink()
        self.write_bytes("internal/alpha/domain/invalid.go", b"\xff\n")
        with self.assertRaisesRegex(ValueError, "not UTF-8"):
            self.collect(self.repository / "missing-dist")

    def test_tracked_parent_directory_symlink_fails_closed(self) -> None:
        self.write("internal/alpha/domain/model.go", "package domain\n")
        with tempfile.TemporaryDirectory() as outside_directory:
            outside_root = Path(outside_directory)
            (self.repository / "internal").rename(outside_root / "internal")
            os.symlink(outside_root / "internal", self.repository / "internal")

            with self.assertRaisesRegex(ValueError, "path contains a symlink"):
                self.collect(self.repository / "missing-dist")


if __name__ == "__main__":
    unittest.main()
