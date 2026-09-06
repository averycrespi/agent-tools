import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

from ci import SUITE_JOBS, cache_identity, classify, changed_paths, check_gate, inventory


ROOT = Path(__file__).resolve().parents[2]
TOOLS = [
    "mcp-broker", "mcp-gateway", "sandbox-manager", "local-git-mcp",
    "local-gomod-proxy", "telegram-mcp", "http-broker",
]


class SelectionTests(unittest.TestCase):
    def setUp(self):
        self.inventory = inventory(ROOT)

    def select(self, paths, event="pull_request"):
        return classify(paths, event, self.inventory)

    def test_inventory_matches_repository(self):
        self.assertEqual(self.inventory["tools"], TOOLS)
        for suite in ("integration", "e2e"):
            self.assertTrue(set(self.inventory[suite]) <= set(TOOLS))

    def test_each_tool_includes_code_docs_fixtures_and_configuration(self):
        for tool in TOOLS:
            for suffix in ("internal/main.go", "README.md", "test/fixture.json", "examples/config.yaml"):
                with self.subTest(tool=tool, suffix=suffix):
                    result = self.select([f"{tool}/{suffix}"])
                    self.assertEqual(result["tools"], [tool])
                    for suite in ("integration", "e2e"):
                        self.assertEqual(result[suite], [tool] if tool in self.inventory[suite] else [])
                    self.assertEqual(result["gateway"], tool == "mcp-gateway")
                    self.assertEqual(result["sandbox"], tool == "sandbox-manager")

    def test_shared_and_unknown_paths_select_everything(self):
        for path in ("go.work", "go.work.sum", "Makefile", "package.json", "package-lock.json",
                     ".github/workflows/ci.yml", ".github/scripts/ci.py", ".github/actions/go-cache/action.yml", ".prettierignore",
                     "README.md", "assets/tool-relationships.svg", "new-tool/main.go",
                     "mcp-gateway-lookalike/main.go"):
            with self.subTest(path=path):
                self.assertEqual(self.select([path])["tools"], TOOLS)

    def test_tool_build_metadata_also_invalidates_gateway_contracts(self):
        for suffix in ("Makefile", "go.mod", "go.sum", ".golangci.yml"):
            self.assertEqual(self.select([f"http-broker/{suffix}"])["tools"], ["mcp-gateway", "http-broker"])

    def test_multiple_tools_are_unique_and_stably_ordered(self):
        self.assertEqual(self.select(["http-broker/a.go", "mcp-broker/b.go", "http-broker/c.go"])["tools"],
                         ["mcp-broker", "http-broker"])

    def test_empty_pr_can_skip_but_other_events_are_full(self):
        self.assertEqual(self.select([])["tools"], [])
        for event in ("push", "schedule", "workflow_dispatch", "unknown"):
            self.assertEqual(self.select([], event)["tools"], TOOLS)


class DiffTests(unittest.TestCase):
    def test_merge_base_diff_includes_deletions_both_rename_paths_and_odd_names(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", "-C", directory, *args], timeout=10).decode().strip()
            git("init", "-q")
            git("config", "user.email", "ci-test@example.invalid")
            git("config", "user.name", "CI test")
            (root / "mcp-broker").mkdir()
            (root / "http-broker").mkdir()
            (root / "mcp-broker/old.go").write_text("rename fixture\n")
            (root / "mcp-broker/deleted.go").write_text("delete fixture\n")
            git("add", "mcp-broker/old.go", "mcp-broker/deleted.go")
            git("commit", "-qm", "base")
            ancestor = git("rev-parse", "HEAD")
            (root / "README.md").write_text("base branch only\n")
            git("add", "README.md")
            git("commit", "-qm", "base advances")
            base = git("rev-parse", "HEAD")
            git("switch", "--detach", ancestor)
            (root / "mcp-broker/old.go").rename(root / "http-broker/new.go")
            (root / "mcp-broker/deleted.go").unlink()
            odd = "http-broker/space and\nnewline.go"
            (root / odd).write_text("odd filename\n")
            git("add", "mcp-broker/old.go", "mcp-broker/deleted.go", "http-broker/new.go", odd)
            git("commit", "-qm", "head changes")
            head = git("rev-parse", "HEAD")
            self.assertEqual(set(changed_paths(root, base, head)), {
                "mcp-broker/old.go", "mcp-broker/deleted.go", "http-broker/new.go", odd,
            })

    def test_invalid_or_missing_revisions_fail_closed(self):
        for base in ("", "--help", "main"):
            with self.assertRaises(ValueError):
                changed_paths(ROOT, base, "0" * 40)
        with self.assertRaises(subprocess.CalledProcessError):
            changed_paths(ROOT, "0" * 40, "1" * 40)


class CLITests(unittest.TestCase):
    def run_cli(self, command, environment):
        return subprocess.run(
            [sys.executable, "-B", str(ROOT / ".github/scripts/ci.py"), command],
            env={**os.environ, **environment}, capture_output=True, text=True, timeout=30,
        )

    def test_select_emits_machine_readable_full_run_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            result = self.run_cli("select", {"GITHUB_EVENT_NAME": "push", "GITHUB_OUTPUT": str(output)})
            self.assertEqual(result.returncode, 0, result.stderr)
            values = dict(line.split("=", 1) for line in output.read_text().splitlines())
            self.assertEqual(json.loads(values["tools"]), TOOLS)
            self.assertEqual(values["gateway"], "true")
            self.assertEqual(values["sandbox"], "true")

    def test_gate_exit_code_blocks_failed_and_missing_results(self):
        needs = GateTests().needs(["README.md"])
        self.assertEqual(self.run_cli("gate", {"NEEDS_JSON": json.dumps(needs)}).returncode, 0)
        needs["unit-tests"]["result"] = "failure"
        self.assertNotEqual(self.run_cli("gate", {"NEEDS_JSON": json.dumps(needs)}).returncode, 0)
        self.assertNotEqual(self.run_cli("gate", {"NEEDS_JSON": "{}"}).returncode, 0)


class GateTests(unittest.TestCase):
    def needs(self, paths):
        selection = classify(paths, "pull_request", inventory(ROOT))
        needs = {
            "changes": {"result": "success", "outputs": {key: json.dumps(value) for key, value in selection.items()}},
            "quality": {"result": "success"},
        }
        for job, key in {"unit-tests": "tools", "integration-tests": "integration", "e2e-tests": "e2e",
                         "vulnerability-scan": "tools", "gateway-temporary": "gateway", "gateway-lint": "gateway",
                         "sandbox-manager-macos": "sandbox"}.items():
            needs[job] = {"result": "success" if selection[key] else "skipped"}
        return needs

    def test_success_and_only_intentional_skips_pass(self):
        for paths in ([], ["telegram-mcp/main.go"], ["README.md"]):
            check_gate(self.needs(paths))

    def test_failures_cancellation_and_unexpected_skips_block(self):
        for job in self.needs(["README.md"]):
            for result in ("failure", "cancelled", "skipped"):
                with self.subTest(job=job, result=result):
                    needs = self.needs(["README.md"])
                    needs[job]["result"] = result
                    with self.assertRaises(ValueError):
                        check_gate(needs)

    def test_unmapped_dependencies_fail_closed(self):
        needs = self.needs([])
        needs["new-check"] = {"result": "failure"}
        with self.assertRaises(ValueError):
            check_gate(needs)

    def test_missing_jobs_and_invalid_outputs_block(self):
        needs = self.needs([])
        del needs["unit-tests"]
        with self.assertRaises(ValueError):
            check_gate(needs)
        needs = self.needs([])
        for value in ("not json", "null", "false", "{}", "[1]"):
            needs["changes"]["outputs"]["tools"] = value
            with self.assertRaises(ValueError):
                check_gate(needs)
        needs = self.needs([])
        needs["changes"]["outputs"]["gateway"] = '"false"'
        with self.assertRaises(ValueError):
            check_gate(needs)


class CacheTests(unittest.TestCase):
    def identity(self, root=ROOT, **overrides):
        values = dict(role="unit", tool="mcp-gateway", toolchain="go version go1.25.13 linux/arm64",
                      platform="Linux/ARM64", run="123", attempt="1")
        values.update(overrides)
        return cache_identity(root, **values)

    def test_partial_restores_can_save_new_material(self):
        first = self.identity()
        for later in (self.identity(run="124"), self.identity(attempt="2")):
            self.assertEqual(first["prefix"], later["prefix"])
            self.assertNotEqual(first["key"], later["key"])
            self.assertTrue(first["key"].startswith(later["prefix"]))
            self.assertTrue(later["key"].startswith(later["prefix"]))

    def test_roles_tools_toolchains_and_platforms_are_isolated(self):
        first = self.identity()["prefix"]
        for override in ({"role": "lint"}, {"role": "integration"}, {"role": "e2e"},
                         {"tool": "mcp-broker"}, {"toolchain": "go version go1.26 linux/arm64"},
                         {"platform": "Linux/X64"}, {"platform": "macOS/ARM64"}):
            self.assertNotEqual(first, self.identity(**override)["prefix"])
        for override in ({"role": "unknown"}, {"tool": "unknown"}, {"run": ""}, {"attempt": "one"}):
            with self.assertRaises(ValueError):
                self.identity(**override)

    def test_workspace_dependencies_and_linter_configuration_invalidate(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Makefile").write_text((ROOT / "Makefile").read_text())
            paths = [root / "go.work", root / "mcp-gateway/go.mod", root / "mcp-gateway/go.sum",
                     root / "mcp-broker/go.mod", root / "mcp-broker/go.sum", root / "mcp-gateway/.golangci.yml"]
            for path in paths:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("original\n")
            before = self.identity(root)["prefix"]
            for path in paths:
                path.write_text("changed\n")
                self.assertNotEqual(before, self.identity(root)["prefix"], str(path))
                path.write_text("original\n")
            (root / "mcp-gateway/main.go").write_text("changed source\n")
            self.assertEqual(before, self.identity(root)["prefix"], "build material is not correctness evidence")

    def test_workflow_wires_cache_and_mandatory_independent_gateway_lint(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        jobs = dict(re.findall(r"^  ([a-z0-9-]+):\n(.*?)(?=^  [a-z0-9-]+:|\Z)", workflow, re.M | re.S))
        required = jobs["required"].split("    runs-on:")[0]
        dependencies = re.findall(r"^      - ([a-z0-9-]+)$", required, re.M)
        self.assertEqual(set(dependencies), {"changes", "quality", *SUITE_JOBS})
        roles = {"quality": "quality", "unit-tests": "unit", "gateway-lint": "lint",
                 "integration-tests": "integration", "e2e-tests": "e2e", "gateway-temporary": "temporary",
                 "vulnerability-scan": "vulnerability", "sandbox-manager-macos": "macos"}
        for job, role in roles.items():
            self.assertEqual(jobs[job].count("uses: ./.github/actions/go-cache"), 1)
            self.assertIn(f"role: {role}\n", jobs[job])
        self.assertNotIn("actions/setup-go", workflow)
        self.assertIn("if: matrix.tool != 'mcp-gateway'\n        run: make -C \"$TOOL\" lint", jobs["unit-tests"])
        self.assertIn("if: needs.changes.outputs.gateway == 'true'", jobs["gateway-lint"])
        self.assertIn("run: make -C mcp-gateway lint", jobs["gateway-lint"])
        self.assertIn("needs: changes\n", jobs["gateway-lint"])
        action = (ROOT / ".github/actions/go-cache/action.yml").read_text()
        for fragment in ("cache: false", "ci.py cache", "uses: actions/cache@v4", "go env GOMODCACHE",
                         "go env GOCACHE", "GOLANGCI_LINT_CACHE=", "key: ${{ steps.identity.outputs.key }}",
                         "restore-keys: ${{ steps.identity.outputs.prefix }}"):
            self.assertIn(fragment, action)


if __name__ == "__main__":
    unittest.main()
