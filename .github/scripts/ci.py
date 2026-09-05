import json
import os
from pathlib import Path
import re
import subprocess
import sys


SUITE_JOBS = {
    "unit-tests": "tools",
    "integration-tests": "integration",
    "e2e-tests": "e2e",
    "vulnerability-scan": "tools",
    "gateway-temporary": "gateway",
    "sandbox-manager-macos": "sandbox",
}


def inventory(root):
    makefile = (root / "Makefile").read_text()
    suites = {}
    for key, variable in (("tools", "TOOLS"), ("integration", "INTEGRATION_TOOLS"), ("e2e", "E2E_TOOLS")):
        match = re.search(rf"^{variable} := ([a-z0-9 -]+)$", makefile, re.MULTILINE)
        if not match:
            raise ValueError(f"Missing literal {variable} inventory")
        suites[key] = match[1].split()
    return suites


def classify(paths, event, suites):
    selected = set(suites["tools"]) if event != "pull_request" else set()
    for path in paths:
        tool, separator, relative = path.partition("/")
        if not separator or tool not in suites["tools"]:
            selected.update(suites["tools"])
            break
        selected.add(tool)
        # Gateway acceptance definitions also inspect other tools' build metadata.
        if relative in {"Makefile", "go.mod", "go.sum", ".golangci.yml"}:
            selected.add("mcp-gateway")
    result = {key: [tool for tool in tools if tool in selected] for key, tools in suites.items()}
    result["gateway"] = "mcp-gateway" in selected
    result["sandbox"] = "sandbox-manager" in selected
    return result


def changed_paths(root, base, head):
    if not all(re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", sha) for sha in (base, head)):
        raise ValueError("PR base and head must be full commit SHAs")
    # No rename detection: both sides must invalidate their respective tools.
    output = subprocess.check_output(
        ["git", "-C", str(root), "diff", "--no-renames", "--name-only", "-z", f"{base}...{head}", "--"],
        timeout=30,
    )
    return [os.fsdecode(path) for path in output.split(b"\0") if path]


def check_gate(needs):
    if set(needs) != {"changes", "quality", *SUITE_JOBS}:
        raise ValueError("Required-gate dependencies do not match the known CI jobs")
    for job in ("changes", "quality"):
        if needs.get(job, {}).get("result") != "success":
            raise ValueError(f"{job} did not succeed")
    outputs = needs["changes"].get("outputs", {})
    for job, key in SUITE_JOBS.items():
        if key not in outputs:
            raise ValueError(f"Missing selection output: {key}")
        selection = json.loads(outputs[key])
        if key in {"gateway", "sandbox"}:
            valid = isinstance(selection, bool)
        else:
            valid = isinstance(selection, list) and all(isinstance(tool, str) for tool in selection)
        if not valid:
            raise ValueError(f"Invalid selection output: {key}")
        expected = "success" if selection else "skipped"
        actual = needs.get(job, {}).get("result")
        if actual != expected:
            raise ValueError(f"{job}: expected {expected}, got {actual}")


def main():
    if sys.argv[1:] == ["gate"]:
        check_gate(json.loads(os.environ["NEEDS_JSON"]))
        print("All selected checks passed; other suites were intentionally skipped.")
    elif sys.argv[1:] == ["select"]:
        root = Path(__file__).resolve().parents[2]
        event = os.environ["GITHUB_EVENT_NAME"]
        paths = []
        if event == "pull_request":
            payload = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
            pr = payload["pull_request"]
            paths = changed_paths(root, pr["base"]["sha"], pr["head"]["sha"])
        selection = classify(paths, event, inventory(root))
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            for key, value in selection.items():
                output.write(f"{key}={json.dumps(value, separators=(',', ':'))}\n")
        print(json.dumps(selection, indent=2))
    else:
        raise ValueError("Usage: ci.py select|gate")


if __name__ == "__main__":
    main()
