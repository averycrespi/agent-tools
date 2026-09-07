import json
import os
from pathlib import Path
import re
import select
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest

from ci import inventory


ROOT = Path(__file__).resolve().parents[2]
TOOLS = inventory(ROOT)["tools"]
OTHERS = [tool for tool in TOOLS if tool != "mcp-gateway"]
WORKER = '''import json, os, socket, sys, time
from pathlib import Path
root = Path(__file__).parent
name, goal = sys.argv[1:]
record = {"tool": name, "goal": goal, "started": time.monotonic(), "flags": os.environ.get("MAKEFLAGS", "")}
path = root / "events" / (name + "-" + goal + ".json")
with path.open("x") as output:
    json.dump(record, output)
with socket.create_connection(("127.0.0.1", int(os.environ["ROOT_TEST_CONTROL"])), timeout=10) as connection:
    connection.sendall(json.dumps(record).encode() + b"\\n")
    result = connection.recv(1)
record["ended"] = time.monotonic()
path.write_text(json.dumps(record))
sys.exit(0 if result == b"0" else 1)
'''


class RootMakeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="root-make-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "Makefile").write_bytes((ROOT / "Makefile").read_bytes())
        (self.root / "events").mkdir()
        (self.root / "worker.py").write_text(WORKER)
        for tool in TOOLS:
            directory = self.root / tool
            directory.mkdir()
            command = f"{shlex.quote(sys.executable)} {shlex.quote(str(self.root / 'worker.py'))} {tool} $@"
            (directory / "Makefile").write_text(f".PHONY: test lint\ntest lint:\n\t@{command}\n")

    def accept(self, listener):
        connection, _ = listener.accept()
        connection.settimeout(10)
        self.addCleanup(connection.close)
        with connection.makefile("rb") as stream:
            record = json.loads(stream.readline(4096))
        return connection, record

    def exercise(self, target, jobs, fail=False):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen(16)
            listener.settimeout(10)
            arguments = ["make", "-s", "-C", str(self.root), target, f"LOCAL_TEST_JOBS={jobs}"]
            if fail:
                arguments.insert(1, "-k")
            process = subprocess.Popen(arguments, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                       env={**os.environ, "LC_ALL": "C", "ROOT_TEST_CONTROL": str(listener.getsockname()[1])},
                                       start_new_session=True)
            try:
                initial = [("mcp-gateway", "test")] if target == "test" else [(tool, "lint") for tool in OTHERS]
                for tool, goal in initial:
                    connection, record = self.accept(listener)
                    self.assertEqual((record["tool"], record["goal"]), (tool, goal))
                    connection.sendall(b"0")
                    connection.close()
                remaining = set(OTHERS)
                while remaining:
                    batch = [self.accept(listener) for _ in range(min(jobs, len(remaining)))]
                    names = {record["tool"] for _, record in batch}
                    self.assertEqual(len(names), len(batch))
                    self.assertTrue(names <= remaining, (names, remaining))
                    self.assertTrue(all(record["goal"] == "test" for _, record in batch))
                    remaining -= names
                    if fail:
                        failing = next(connection for connection, record in batch if record["tool"] == OTHERS[0])
                        failing.sendall(b"1")
                        failing.close()
                        output = b""
                        deadline = time.monotonic() + 10
                        while not re.search(rb"\[[^\n]*__test-mcp-broker[^\n]*\][^\n]*Error", output):
                            ready, _, _ = select.select([process.stdout], [], [], max(0, deadline-time.monotonic()))
                            self.assertTrue(ready, output.decode(errors="replace"))
                            chunk = os.read(process.stdout.fileno(), 8192)
                            self.assertTrue(chunk, output.decode(errors="replace"))
                            output += chunk
                        self.assertIsNone(process.poll(), "running tool must drain after peer failure")
                        for connection, _ in batch:
                            if connection is not failing:
                                connection.sendall(b"0")
                                connection.close()
                        break
                    for connection, _ in batch:
                        connection.sendall(b"0")
                        connection.close()
                output, _ = process.communicate(timeout=15)
                self.assertEqual(process.returncode, 2 if fail else 0, output.decode(errors="replace"))
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        process.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait(timeout=5)
                process.stdout.close()
        records = [json.loads(path.read_text()) for path in (self.root / "events").glob("*.json")]
        tests = [record for record in records if record["goal"] == "test"]
        expected = set(OTHERS[:jobs]) if fail else set(TOOLS if target == "test" else OTHERS)
        self.assertEqual({record["tool"] for record in tests}, expected)
        self.assertEqual(len(tests), len(expected))
        for record in records:
            self.assertIn("ended", record)
        for record in tests:
            concurrent = [other for other in tests if other["started"] <= record["started"] < other["ended"]]
            self.assertLessEqual(len(concurrent), jobs)
            if record["tool"] == "mcp-gateway":
                self.assertEqual(len(concurrent), 1)
            else:
                self.assertFalse(any(other["tool"] == "mcp-gateway" for other in concurrent))
        for record in records:
            if record["goal"] == "lint":
                self.assertFalse(any(other is not record and other["started"] <= record["started"] < other["ended"] for other in records))
        return records

    def test_two_tool_phase_preserves_gateway_isolation_and_exact_coverage(self):
        self.exercise("test", 2)

    def test_serial_comparison_uses_same_owners(self):
        self.exercise("test", 1)

    def test_other_tools_keep_lint_serial_and_exclude_gateway(self):
        self.exercise("check-other-tools", 2)

    def test_failure_stops_new_work_and_drains_active_tool_even_with_keep_going(self):
        self.exercise("check-other-tools", 2, fail=True)

    def test_invalid_bounds_fail_before_work(self):
        for value in ("0", "3", "-1", "1 2", "unbounded"):
            with self.subTest(value=value):
                result = subprocess.run(["make", "-s", "-C", str(self.root), "test", f"LOCAL_TEST_JOBS={value}"],
                                        capture_output=True, text=True, timeout=10)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("LOCAL_TEST_JOBS", result.stderr)
                self.assertEqual(list((self.root / "events").iterdir()), [])


if __name__ == "__main__":
    unittest.main()
