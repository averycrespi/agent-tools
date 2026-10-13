import json
from pathlib import Path
import re
import subprocess
import sys
import unittest

from docker_smoke import ROOT, Smoke, command


class FakeDocker:
    """Exercise owner decisions without a Docker client or daemon."""

    def __init__(self, failure=None, bad_exit=False, cleanup_failure=False):
        self.calls = []
        self.failure = failure
        self.bad_exit = bad_exit
        self.cleanup_failure = cleanup_failure

    def __call__(self, args, *, env, timeout, check=True):
        self.calls.append((args, timeout))
        joined = " ".join(args)
        if self.failure and self.failure in joined:
            raise RuntimeError("injected failure")
        if self.cleanup_failure and " down " in joined:
            raise RuntimeError("injected cleanup failure")
        if args[1] == "inspect":
            return 0, json.dumps({"Running": False, "ExitCode": 137 if self.bad_exit else 0,
                                  "OOMKilled": False})
        if " ps " in joined:
            return 0, "fixture-container\n"
        if " ls " in joined:
            return 0, {"volume": env["GATEWAY_VOLUME"], "network": env["GATEWAY_PROXY_NETWORK"],
                       "image": env["GATEWAY_IMAGE"]}[args[1]] + "\n"
        if "serve --clear-http-proxy-listen" in joined:
            return 7, "expected refusal"
        return 0, ""


class SmokeTests(unittest.TestCase):
    def test_happy_path_requires_init_readiness_clean_stop_and_owned_cleanup(self):
        fake = FakeDocker()
        smoke = Smoke(fake)
        try:
            smoke.exercise()
        finally:
            smoke.cleanup()
        commands = [" ".join(args) for args, _ in fake.calls]
        init = next(i for i, text in enumerate(commands) if "init --confirm" in text)
        start = next(i for i, text in enumerate(commands) if "up --detach" in text)
        stop = next(i for i, text in enumerate(commands) if "stop --timeout 30" in text)
        self.assertLess(init, start)
        self.assertLess(start, stop)
        self.assertIn("--wait --wait-timeout 90", commands[start])
        self.assertTrue(all(0 < timeout <= 600 for _, timeout in fake.calls))
        self.assertTrue(all("push" not in args and "prune" not in args for args, _ in fake.calls))
        for kind, name in (("volume", smoke.volume), ("network", smoke.network), ("image", smoke.image)):
            self.assertIn(f"docker {kind} rm {name}", commands)
        self.assertNotEqual(Smoke(fake).name, smoke.name)

    def test_failures_never_pass_and_cleanup_still_runs(self):
        for failure in ("docker build", "init --confirm", "up --detach", "doctor --online", "stop --timeout"):
            with self.subTest(failure=failure):
                fake = FakeDocker(failure=failure)
                smoke = Smoke(fake)
                with self.assertRaises(RuntimeError):
                    try:
                        smoke.exercise()
                    finally:
                        smoke.cleanup()
                self.assertTrue(any("down" in args for args, _ in fake.calls))

    def test_forced_exit_and_unconfirmed_cleanup_fail(self):
        with self.assertRaisesRegex(RuntimeError, "did not stop cleanly"):
            Smoke(FakeDocker(bad_exit=True)).exercise()
        fake = FakeDocker(cleanup_failure=True)
        with self.assertRaisesRegex(RuntimeError, "cleanup unconfirmed"):
            Smoke(fake).cleanup()
        self.assertTrue(any(args[1:3] == ["image", "rm"] for args, _ in fake.calls))

    def test_command_deadline_and_failure_are_enforced_without_echoing_output(self):
        with self.assertRaises(subprocess.TimeoutExpired):
            command([sys.executable, "-c", "import time; time.sleep(10)"], env={}, timeout=0.05)
        with self.assertRaisesRegex(RuntimeError, "output withheld") as result:
            command([sys.executable, "-c", "print('private-fixture-output'); exit(2)"], env={})
        self.assertNotIn("private-fixture-output", str(result.exception))


class PackagingTests(unittest.TestCase):
    def test_runtime_and_context_are_narrow_and_match_module_toolchain(self):
        dockerfile = (ROOT / "agent-gateway/Dockerfile").read_text()
        version = re.search(r"^go (\S+)$", (ROOT / "agent-gateway/go.mod").read_text(), re.M)[1]
        self.assertRegex(dockerfile, rf"FROM golang:{re.escape(version)}-bookworm@sha256:[0-9a-f]{{64}} AS build")
        self.assertRegex(dockerfile, r"FROM alpine:[0-9.]+@sha256:[0-9a-f]{64}\n")
        self.assertIn("CGO_ENABLED=0", dockerfile)
        self.assertIn("-mod=readonly -trimpath -buildvcs=false", dockerfile)
        runtime = dockerfile.split("FROM alpine:")[1]
        for fragment in ("USER 10001:10001", "chmod 0700", "STOPSIGNAL SIGTERM",
                         'ENTRYPOINT ["/usr/local/bin/agent-gateway"]', 'CMD ["serve"]',
                         "/etc/ssl/certs/ca-certificates.crt"):
            self.assertIn(fragment, runtime)
        for forbidden in ("npm", "node", "go build", "VOLUME", "init --confirm"):
            self.assertNotIn(forbidden, runtime)
        ignore = (ROOT / "agent-gateway/.dockerignore").read_text()
        rules = [line for line in ignore.splitlines() if line and not line.startswith("#")]
        self.assertEqual(rules[0], "**")
        self.assertEqual(set(line for line in rules if line.startswith("!")), {
            "!go.mod", "!go.sum", "!cmd/", "!cmd/**/", "!cmd/**/*.go", "!internal/",
            "!internal/**/", "!internal/**/*.go", "!internal/storage/migrations/*.sql",
            "!internal/api/static/app.js", "!internal/api/static/app.css",
            "!internal/api/static/index.html", "!internal/api/static/favicon.svg"})

    def test_compose_and_guide_preserve_installation_and_protocol_boundaries(self):
        compose = (ROOT / "agent-gateway/examples/docker/compose.yaml").read_text()
        self.assertIn('user: "10001:10001"', compose)
        self.assertIn("stop_grace_period: 30s", compose)
        self.assertEqual(compose.count("external: true"), 2)
        self.assertIn("installation:/var/lib/agent-gateway", compose)
        ports = compose.split("    ports:\n")[1].split("    read_only:")[0]
        self.assertNotIn("GATEWAY_PORT", ports)
        self.assertIn("GATEWAY_PROXY_HOST_PORT", ports)
        self.assertNotIn("docker.sock", compose)
        self.assertNotIn("caddy:", compose)
        guide = ROOT / "agent-gateway/docs/operators/docker.md"
        text = guide.read_text()
        for topic in ("init --confirm", "0700", "0600", "docker exec", "master-key",
                      "30-second", "linux/amd64", "allow_private_http", "No Docker access",
                      "not fresh-install import", "registry", "Caddy", "outer"):
            self.assertIn(topic, text)
        for link in re.findall(r"\[[^]]+\]\(([^)]+)\)", text):
            if "://" not in link and not link.startswith("#"):
                self.assertTrue((guide.parent / link.split("#")[0]).resolve().exists(), link)


if __name__ == "__main__":
    unittest.main()
