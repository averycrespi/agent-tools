#!/usr/bin/env python3
import importlib.util
import json
import os
from pathlib import Path
import signal
import socket
import sys
import tempfile
import time
import unittest

SCRIPT = Path(__file__).resolve().parent.parent / "scripts" / "serve-demo.py"
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("demo", SCRIPT)
demo = importlib.util.module_from_spec(spec)
spec.loader.exec_module(demo)


def available_authority():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return "127.0.0.1:" + str(listener.getsockname()[1])


class DemoTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="gateway-demo-test-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = dict(os.environ, TMPDIR=str(self.root))
        self.children = []
        self.addCleanup(self.cleanup_children)

    def cleanup_children(self):
        for child in reversed(self.children):
            child.finish(0)

    def start(self, dataset="curated", prefix="", listen=None):
        listen = listen or available_authority()
        if prefix:
            code = "import sys,importlib.util,time; s=importlib.util.spec_from_file_location('demo',sys.argv[1]); d=importlib.util.module_from_spec(s); s.loader.exec_module(d); sys.argv=sys.argv[1:]; " + prefix + "; d.main()"
            argv = [sys.executable, "-B", "-c", code, str(SCRIPT), "--listen", listen, "--dataset", dataset]
        else:
            argv = [sys.executable, str(SCRIPT), "--listen", listen, "--dataset", dataset]
        child = demo.Child("demo test subject", argv, self.env)
        self.children.append(child)
        return child, listen

    def ready(self, child):
        deadline = time.monotonic() + 75
        while time.monotonic() < deadline:
            child.check()
            for root in self.root.glob("mcp-gateway-demo-*"):
                if (root / "ready.json").exists() and b"Demo Gateway ready" in child.output[0]:
                    return root, json.loads((root / "ready.json").read_text())
            time.sleep(0.05)
        self.fail("demo never became ready")

    def stopped(self, child, root=None):
        child.finish(15)
        self.assertFalse(list(self.root.glob("mcp-gateway-demo-*")))
        if root is not None:
            self.assertFalse(root.exists())
        self.assertFalse(child.overflow)

    def client(self, listen, root):
        return demo.Client(listen, (root / "admin-bearer").read_text().strip(), time.monotonic() + 15)

    def test_curated_public_results_privacy_and_fixture_exit(self):
        sentinel = self.root / "default-installation-sentinel"
        sentinel.write_text("unchanged")
        self.env["HOME"] = str(self.root)
        child, listen = self.start()
        root, manifest = self.ready(child)
        self.assertEqual(manifest["dataset"], "curated")
        self.assertEqual(len(manifest["fixtures"]), 2)
        client = self.client(listen, root)
        principals = client.get("principals")["items"]
        self.assertEqual({item["display_name"] for item in principals}, {"Demo Explorer", "Demo Reader", "Demo Disabled"})
        self.assertEqual(len(client.get("servers")["items"]), 2)
        self.assertEqual(len(client.get("grants")["items"]), 8)
        requests = client.get("grant-requests")["items"]
        self.assertEqual(requests[0]["state"], "pending")
        explorer = (root / "explorer-bearer").read_text().strip()
        reader = (root / "reader-bearer").read_text().strip()
        reader_tools = {tool["name"] for tool in client.rpc(reader, "tools/list", {})["result"]["tools"]}
        self.assertNotIn("demo_workshop.controlled_error", reader_tools)
        self.assertIn("demo_workshop.add", reader_tools)
        before = client.get("invocations")["items"]
        time.sleep(0.2)
        self.assertEqual(before, client.get("invocations")["items"], "background activity after readiness")
        response = client.call(explorer, "demo_workshop.add", {"a": 40, "b": 2})
        self.assertEqual(response["result"]["content"], [{"type": "text", "text": "42"}])
        self.assertEqual(len(client.get("invocations")["items"]), len(before) + 1)
        self.assertEqual(client.call(reader, "demo_workshop.add", {"a": 1, "b": 2})["error"]["data"]["code"], "call_rejected")
        self.assertEqual(client.call(explorer, "demo_workshop.controlled_error", {})["error"]["data"]["code"], "downstream_failure")
        self.assertEqual(client.call(reader, "demo_library.lookup", {"document": "welcome"})["result"]["content"][0]["text"], demo.DOCUMENTS["welcome"])
        self.assertEqual(root.stat().st_mode & 0o777, 0o700)
        sinks = list(root.glob("*-bearer"))
        self.assertEqual(len(sinks), 4)
        for sink in sinks:
            self.assertEqual(sink.stat().st_mode & 0o777, 0o600)
            secret = sink.read_bytes().strip()
            self.assertNotIn(secret, bytes(child.output[0]) + bytes(child.output[1]))
            for artifact in (root / "data").rglob("*"):
                if artifact.is_file():
                    self.assertNotIn(secret, artifact.read_bytes(), "credential in durable state")
        fixture_pid = manifest["processes"]["workshop fixture"]
        self.assertEqual(os.getpgid(fixture_pid), fixture_pid)
        os.kill(fixture_pid, signal.SIGTERM)
        self.stopped(child, root)
        self.assertIn(b"fixture exited unexpectedly", child.output[1])
        self.assertNotEqual(child.process.returncode, 0)
        self.assertEqual(sentinel.read_text(), "unchanged")
        for port in manifest["fixtures"].values():
            with socket.socket() as probe:
                self.assertNotEqual(probe.connect_ex(("127.0.0.1", int(port))), 0)

    def test_empty_fresh_runs_and_signals(self):
        bearers = []
        roots = []
        for value in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            child, listen = self.start("empty")
            root, manifest = self.ready(child)
            roots.append(str(root))
            bearers.append((root / "admin-bearer").read_text())
            self.assertEqual(manifest["fixtures"], {})
            self.assertEqual(set(manifest["processes"]), {"Gateway"})
            client = self.client(listen, root)
            for collection in ("servers", "principals", "grants", "grant-requests", "invocations"):
                self.assertEqual(client.get(collection)["items"], [])
            child.signal(value)
            if value == signal.SIGHUP:
                child.signal(signal.SIGHUP)
            self.stopped(child, root)
            self.assertNotEqual(child.process.returncode, 0)
        self.assertEqual(len(set(roots)), 3)
        self.assertEqual(len(set(bearers)), 3)

    def test_invalid_selectors_and_occupied_listener(self):
        for args in (("--dataset", "unknown"), ("--listen", "localhost:8211"), ("--listen", "0.0.0.0:8211"), ("--listen", "127.0.0.1:0"), ("--listen", "127.0.0.1:08211"), ("--listen", "127.0.0.1:8211;exit"), ("--unknown",)):
            child = demo.Child("invalid selector", [sys.executable, str(SCRIPT), *args], self.env)
            self.children.append(child)
            self.stopped(child)
            self.assertEqual(child.process.returncode, 2)
            self.assertNotIn(b"Building", child.output[0])
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            child, _ = self.start(listen="127.0.0.1:" + str(listener.getsockname()[1]))
            self.stopped(child)
            self.assertIn(b"listener unavailable", child.output[1])
            self.assertNotEqual(child.process.returncode, 0)

    def test_delayed_readiness_and_selected_failures(self):
        # In-process substitutions are test-only; the runner exposes no fault selectors.
        prefix = "original=d.Client.request; until=time.monotonic()+2; d.Client.request=lambda self,method,path,*a,**kw: (_ for _ in ()).throw(d.DemoError('delayed')) if path=='/readyz' and time.monotonic()<until else original(self,method,path,*a,**kw)"
        began = time.monotonic()
        child, _ = self.start("empty", prefix)
        root, _ = self.ready(child)
        self.assertGreaterEqual(time.monotonic() - began, 2)
        child.signal(signal.SIGTERM)
        self.stopped(child, root)
        for prefix in (
            "original=d.Client.request; d.Client.request=lambda self,method,path,body=None,*a,**kw: original(self,method,path,dict(body,unexpected=True) if method=='POST' and path=='/api/v1/principals' else body,*a,**kw)",
            "d.TOOLS={'unknown': []}",
            "original=d.Child.__init__; d.Child.__init__=lambda self,label,argv,env: original(self,label,[sys.executable,'-c','raise SystemExit(9)'] if label=='Gateway' else argv,env)",
            "original=d.wait_until; d.wait_until=lambda check,children,deadline,label: original(lambda:False,children,time.monotonic()+0.2,label)",
        ):
            child, _ = self.start(prefix=prefix)
            self.stopped(child)
            self.assertNotEqual(child.process.returncode, 0)
            self.assertNotIn(b"Demo Gateway ready", child.output[0])

    def test_listener_reuse_after_connection_close(self):
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            port = listener.getsockname()[1]
            with socket.create_connection(("127.0.0.1", port), timeout=2) as client:
                connection, _ = listener.accept()
                with connection:
                    connection.shutdown(socket.SHUT_WR)
                    self.assertEqual(client.recv(1), b"")
        child, _ = self.start("empty", listen="127.0.0.1:" + str(port))
        root, _ = self.ready(child)
        child.signal(signal.SIGTERM)
        self.stopped(child, root)

    def test_filesystem_cleanup_failure_reports_retained_root(self):
        prefix = "import runpy; d.shutil.rmtree=lambda root: (_ for _ in ()).throw(OSError((d.Path(root)/'admin-bearer').read_text())); d.main=lambda:runpy.run_path(sys.argv[0],run_name='__main__')"
        child, listen = self.start("empty", prefix)
        root, manifest = self.ready(child)
        secret = (root / "admin-bearer").read_bytes().strip()
        child.signal(signal.SIGTERM)
        child.finish(15)
        self.assertNotEqual(child.process.returncode, 0)
        self.assertTrue(root.exists())
        self.assertTrue((root / "admin-bearer").exists())
        output = bytes(child.output[0]) + bytes(child.output[1])
        self.assertIn(("cleanup unconfirmed; retained " + str(root)).encode(), child.output[1])
        self.assertFalse(secret in output, "cleanup diagnostic leaked a credential")
        with socket.socket() as probe:
            host, port = listen.split(":")
            self.assertNotEqual(probe.connect_ex((host, int(port))), 0)
        for pid in manifest["processes"].values():
            with self.assertRaises(ProcessLookupError):
                os.killpg(pid, 0)

    def test_fixture_tools_are_bounded(self):
        self.assertEqual(demo.tool_result("workshop", "add", {"a": -5, "b": 2})["content"][0]["text"], "-3")
        for kind, name, args in (("workshop", "echo", {"text": "x" * 257}), ("workshop", "add", {"a": float("inf"), "b": 1}), ("workshop", "add", {"a": True, "b": 1}), ("workshop", "add", {"a": "__import__('os')", "b": 1}), ("library", "lookup", {"document": "/etc/passwd"}), ("library", "echo", {"text": "wrong server"})):
            with self.assertRaises(demo.DemoError):
                demo.tool_result(kind, name, args)


if __name__ == "__main__":
    def interrupt(value, frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupt)
    unittest.main(verbosity=2)
