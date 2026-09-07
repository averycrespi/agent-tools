from pathlib import Path
import re
import shlex
import subprocess
import tempfile
import unittest

from ci import inventory


ROOT = Path(__file__).resolve().parents[2]
GO_LITERALS = re.compile(r'''(?P<line>//[^\n]*)|/\*.*?\*/|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`[^`]*`''', re.S)


def integration_entries(source):
    tokens = list(GO_LITERALS.finditer(source))
    code = GO_LITERALS.sub(lambda match: "".join("\n" if character == "\n" else " " for character in match[0]), source)
    package = re.search(r"\bpackage\s+\w+", code)
    if package is None:
        raise ValueError("missing Go package")
    constraints = []
    for token in tokens:
        if token["line"] and token.start() < package.start():
            constraint = re.match(r"//go:build[ \t]+(.*)$", token["line"])
            if constraint:
                constraints.append(constraint[1].strip())
    if any(re.search(r"\bintegration\b", constraint) and constraint != "integration" for constraint in constraints):
        raise ValueError("integration ownership requires the single integration build tag")
    tagged = "integration" in constraints
    legacy = [token["line"] for token in tokens if token["line"] and token.start() < package.start()
              and re.match(r"//\s*\+build\s", token["line"])]
    if not tagged and any(re.search(r"\bintegration\b", constraint) for constraint in legacy):
        raise ValueError("integration ownership requires the modern single integration build tag")
    names = []
    for match in re.finditer(r"\bfunc\s+((?:Test|Fuzz|Example)\w*)\s*\(", code):
        name = match[1]
        prefix = next(prefix for prefix in ("Test", "Fuzz", "Example") if name.startswith(prefix))
        suffix = name[len(prefix):]
        if suffix and suffix[0].islower():
            continue
        if tagged and not name.startswith("TestIntegration"):
            raise ValueError(f"unselected integration entry point {name}; use the checked TestIntegration namespace")
        if not tagged and name.startswith("TestIntegration"):
            raise ValueError(f"integration namespace lacks its build tag: {name}")
        if tagged:
            names.append(name)
    return names


def integration_packages(module):
    packages = set()
    for path in module.rglob("*_test.go"):
        relative = path.relative_to(module)
        if any(part.startswith((".", "_")) or part in ("vendor", "testdata", "node_modules") for part in relative.parts):
            continue
        try:
            names = integration_entries(path.read_text())
        except ValueError as error:
            raise ValueError(f"{relative}: {error}") from error
        if names:
            packages.add("./" + relative.parent.as_posix())
    if not packages:
        raise ValueError("empty integration owner")
    return packages


class IntegrationOwnershipTests(unittest.TestCase):
    def test_selected_other_modules_match_checked_namespace_and_packages(self):
        for tool in inventory(ROOT)["integration"]:
            if tool == "mcp-gateway":
                continue
            with self.subTest(tool=tool):
                module = ROOT / tool
                packages = integration_packages(module)
                command = shlex.split(subprocess.check_output(
                    ["make", "--no-print-directory", "-s", "-n", "-C", str(module), "test-integration"], text=True, timeout=10))
                self.assertEqual(command[:2], ["go", "test"])
                self.assertIn("-race", command)
                self.assertIn("-tags=integration", command)
                self.assertIn("-run", command)
                self.assertEqual(command[command.index("-run")+1], "^TestIntegration")
                self.assertEqual({argument for argument in command if argument.startswith("./")}, packages)
                makefile = (module / "Makefile").read_text()
                phony = " ".join(re.findall(r"^\.PHONY:\s*(.*)$", makefile, re.M))
                self.assertIn("test-integration", phony.split())

    def test_untagged_reserved_name_fails(self):
        with self.assertRaisesRegex(ValueError, "lacks its build tag"):
            integration_entries("package p\nfunc TestIntegrationMissing(t *testing.T) {}")

    def test_unselected_tagged_entries_fail(self):
        for name in ("TestMissed", "FuzzIntegrationSeed", "Example"):
            with self.subTest(name=name), self.assertRaisesRegex(ValueError, "unselected integration entry point"):
                integration_entries(f"//go:build integration\npackage p\nfunc {name}() {{}}")

    def test_ambiguous_integration_tag_fails(self):
        for constraint in ("!integration", "integration || e2e", "integration && linux"):
            with self.subTest(constraint=constraint), self.assertRaisesRegex(ValueError, "single integration build tag"):
                integration_entries(f"//go:build {constraint}\npackage p\nfunc TestIntegrationOne() {{}}")

    def test_modern_tag_whitespace_preserves_ownership(self):
        self.assertEqual(integration_entries("//go:build\tintegration\npackage p\nfunc TestIntegrationOne() {}"), ["TestIntegrationOne"])
        with self.assertRaisesRegex(ValueError, "unselected integration entry point"):
            integration_entries("//go:build\tintegration\npackage p\nfunc TestMissed() {}")

    def test_legacy_only_integration_tag_cannot_hide_an_entry_point(self):
        with self.assertRaisesRegex(ValueError, "single integration build tag"):
            integration_entries("// +build integration\npackage p\nfunc TestMissed() {}")

    def test_empty_owner_fails(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, "empty integration owner"):
                integration_packages(Path(directory))

    def test_comments_literals_and_fixture_helpers_are_not_executable_names(self):
        source = '''//go:build integration
package p
// func TestMissing() {}
/* func Example() {} */
var fixture = `func TestMissingInRawString() {}`
var quoted = "func TestMissingInQuotedString() {}"
func helper() {}
func TestlowercaseHelper() {}
func (receiver *fixture) TestMethod() {}
func /* comment */ TestIntegrationReal(t *testing.T) {}
'''
        self.assertEqual(integration_entries(source), ["TestIntegrationReal"])


if __name__ == "__main__":
    unittest.main()
