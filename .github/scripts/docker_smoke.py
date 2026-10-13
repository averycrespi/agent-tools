"""Basic CI-only Docker packaging owner; no provider/proxy qualification or publication."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid


ROOT = Path(__file__).resolve().parents[2]
COMPOSE = ROOT / "agent-gateway/examples/docker/compose.yaml"


def command(args, *, env, timeout=30, check=True):
    # Keep child output off CI stdout: init/CLI must never become secret handoff sinks.
    with tempfile.TemporaryFile() as output:
        result = subprocess.run(args, env=env, stdout=output, stderr=subprocess.STDOUT,
                                timeout=timeout, check=False)
        output.seek(0, os.SEEK_END)
        size = output.tell()
        output.seek(max(0, size - 16384))
        text = output.read().decode("utf-8", errors="replace")
    if check and result.returncode:
        # Build has only allowlisted public source, never installation/credential state.
        # Keep runtime/CLI output private even on failure.
        if args[:2] == ["docker", "build"]:
            print(text, flush=True)
        raise RuntimeError(f"{args[0]} {args[1]} failed with exit {result.returncode}; output withheld")
    return result.returncode, text


class Smoke:
    def __init__(self, run=command):
        self.run = run
        self.name = "gateway-smoke-" + uuid.uuid4().hex
        self.label = "agent-tools.smoke=" + self.name
        self.image = self.name + ":test"
        self.volume = self.name + "-data"
        self.network = self.name + "-proxy"
        self.env = {**os.environ, "GATEWAY_IMAGE": self.image,
                    "GATEWAY_VOLUME": self.volume, "GATEWAY_PROXY_NETWORK": self.network,
                    "GATEWAY_EXTERNAL_ORIGIN": "https://gateway.example",
                    "GATEWAY_PORT": "18210", "GATEWAY_PROXY_PORT": "18212",
                    "GATEWAY_PROXY_HOST_PORT": "0", "GATEWAY_PROXY_BIND": "127.0.0.1"}
        self.compose = ["docker", "compose", "--project-name", self.name, "--file", str(COMPOSE)]

    def docker(self, *args, timeout=30, check=True):
        return self.run(["docker", *args], env=self.env, timeout=timeout, check=check)

    def dc(self, *args, timeout=30, check=True):
        return self.run([*self.compose, *args], env=self.env, timeout=timeout, check=check)

    def shell(self, script):
        return self.dc("run", "--rm", "--no-deps", "--entrypoint", "/bin/sh",
                       "gateway", "-ec", script, timeout=60)

    def refuses_uninitialized_or_unsafe(self):
        code, _ = self.dc("run", "--rm", "--no-deps", "gateway", "serve",
                          "--clear-http-proxy-listen", timeout=60, check=False)
        if code == 0:
            raise RuntimeError("serve unexpectedly accepted missing/unsafe installation")

    def prepare(self):
        """Optional qualification setup owned by the same disposable run."""

    def qualify(self, container):
        """Optional protocol cases before the common clean-stop assertion."""

    def exercise(self):
        print("Build exact checkout image (linux/amd64); no registry push", flush=True)
        self.docker("build", "--platform", "linux/amd64", "--label", self.label,
                    "--tag", self.image, str(ROOT / "agent-gateway"), timeout=600)
        self.docker("volume", "create", "--label", self.label, self.volume)
        self.docker("network", "create", "--label", self.label, self.network)
        self.prepare()
        self.dc("config", "--quiet")
        print("Verify empty mount refuses serve without creating authority", flush=True)
        self.refuses_uninitialized_or_unsafe()
        self.shell('test "$(id -u)" = 10001; test "$(stat -c %a .)" = 700; '
                   'test ! -e master-key; test ! -e admin-bearer')
        print("Explicit one-time init as runtime UID", flush=True)
        self.dc("run", "--rm", "--no-deps", "gateway", "init", "--confirm", timeout=120)
        self.shell('test "$(id -u)" = 10001; '
                   'for file in master-key admin-bearer gateway.db http-ca.pem; do '
                   'test "$(stat -c %u "$file")" = 10001; '
                   'test "$(stat -c %a "$file")" = 600; done; '
                   'test -s /etc/ssl/certs/ca-certificates.crt; chmod 755 .')
        print("Verify unsafe mount refuses without permission repair", flush=True)
        self.refuses_uninitialized_or_unsafe()
        self.shell('test "$(stat -c %a .)" = 755; chmod 700 .')
        print("Start foreground service and require readiness", flush=True)
        self.dc("up", "--detach", "--no-build", "--wait", "--wait-timeout", "90", timeout=120)
        _, container = self.dc("ps", "--quiet", "gateway")
        container = container.strip()
        if not container or "\n" in container:
            raise RuntimeError("expected exactly one Gateway container")
        self.dc("exec", "-T", "gateway", "agent-gateway", "doctor", "--online",
                "--address", "http://127.0.0.1:18210", timeout=30)
        container = self.qualify(container) or container
        print("Stop with SIGTERM and 30-second grace; require clean exit", flush=True)
        started = time.monotonic()
        self.dc("stop", "--timeout", "30", "gateway", timeout=40)
        _, raw = self.docker("inspect", "--format", "{{json .State}}", container)
        state = json.loads(raw)
        if state["Running"] or state["ExitCode"] != 0 or state["OOMKilled"]:
            raise RuntimeError("Gateway did not stop cleanly")
        if time.monotonic() - started > 35:
            raise RuntimeError("Gateway exceeded stop grace")
        self.shell('test ! -e run.unclean; test "$(stat -c %a .)" = 700')

    def cleanup_extra(self):
        """Dispose extension peers/images after the shared Gateway stops."""

    def cleanup(self):
        # Down removes only this random Compose project's containers. External resources
        # are deleted only after label reconciliation, including uncertain create results.
        failures = []
        try:
            self.dc("down", "--timeout", "30", "--remove-orphans", timeout=60)
        except (RuntimeError, subprocess.TimeoutExpired) as error:
            failures.append(str(error))
        try:
            self.cleanup_extra()
        except (RuntimeError, subprocess.TimeoutExpired) as error:
            failures.append(str(error))
        for kind, name in (("volume", self.volume), ("network", self.network), ("image", self.image)):
            try:
                code, raw = self.docker(kind, "ls", "--filter", "label=" + self.label,
                                        "--format", "{{.Name}}" if kind != "image" else "{{.Repository}}:{{.Tag}}")
                if code != 0:
                    raise RuntimeError(f"cannot reconcile {kind}")
                if name in raw.splitlines():
                    self.docker(kind, "rm", name)
            except (RuntimeError, subprocess.TimeoutExpired) as error:
                failures.append(str(error))
        if failures:
            raise RuntimeError("owned Docker cleanup unconfirmed: " + "; ".join(failures))
        print("Owned containers, volume, network and image cleaned", flush=True)


def main():
    import sys
    if sys.argv[1:] == ["--qualify"]:
        from docker_qualification import Qualification
        smoke = Qualification()
    elif not sys.argv[1:]:
        smoke = Smoke()
    else:
        raise SystemExit("usage: docker_smoke.py [--qualify]")
    import signal
    def interrupted(_signal, _frame):
        raise SystemExit("container owner interrupted; cleanup required")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGALRM, interrupted)
    signal.alarm(25 * 60)
    try:
        smoke.exercise()
    finally:
        signal.alarm(0)
        smoke.cleanup()


if __name__ == "__main__":
    main()
