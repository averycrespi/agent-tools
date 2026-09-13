#!/usr/bin/env python3
"""Bounded, one-shot per-user LaunchAgent installation and restart."""

import argparse
import os
from pathlib import Path
import plistlib
import signal
import selectors
import stat
import subprocess
import sys
import time

LABEL = "dev.agent-tools.agent-gateway"
LEGACY = "dev.agent-tools.mcp-gateway"


def fail(message):
    raise RuntimeError(message)


def run(*args):
    # The unreaped direct child owns the process group during timeout cleanup.
    child = subprocess.Popen(
        args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True,
        env={**os.environ, "LC_ALL": "C"},
    )
    data = bytearray()
    deadline = time.monotonic() + 5
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(child.stdout, selectors.EVENT_READ)
            while True:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    fail("Command timed out; inspect its outcome before another operation.")
                if not selector.select(remaining):
                    continue
                block = os.read(child.stdout.fileno(), 65536)
                if not block:
                    break
                data.extend(block)
                if len(data) > 1 << 20:
                    fail("Command output exceeds inspection bound.")
        child.wait(timeout=max(0.001, deadline - time.monotonic()))
        return child.returncode, bytes(data)
    except BaseException:
        if child.returncode is None:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            child.wait(timeout=5)
        raise
    finally:
        child.stdout.close()


def checked(*args):
    status, data = run(*args)
    if status:
        fail("Command failed: " + args[0] + "; inspect before retrying.")
    return data


def absolute(value, label):
    if not value or not os.path.isabs(value):
        fail(label + " must be an absolute path")
    return Path(value)


def private(path, directory=False, exact=True):
    info = path.lstat()
    kind = stat.S_ISDIR if directory else stat.S_ISREG
    if not kind(info.st_mode) or info.st_uid != os.getuid():
        fail("Refusing symlink or foreign ownership: " + str(path))
    wanted = 0o700 if directory else 0o600
    if (exact and stat.S_IMODE(info.st_mode) != wanted) or info.st_mode & 0o022:
        fail("Unsafe permissions; inspect without changing existing state: " + str(path))
    return info


def exists(path):
    return os.path.lexists(path)


def account_home():
    # Never use HOME; fixture tools supply isolated account records in tests.
    if checked("uname", "-s").strip() != b"Darwin":
        fail("This script requires macOS.")
    if os.getuid() == 0:
        fail("Run as the intended logged-in user, without sudo.")
    account = checked("id", "-un").decode().strip()
    data = plistlib.loads(checked("dscl", "-plist", ".", "-read", "/Users/" + account, "NFSHomeDirectory"))
    home = absolute(data["dsAttrTypeStandard:NFSHomeDirectory"][0], "OS-account home")
    private(home, directory=True, exact=False)
    return home


def service(label):
    status, data = run("launchctl", "print", "gui/" + str(os.getuid()) + "/" + label)
    text = data.decode("utf-8", "strict")
    if status == 0:
        return text
    if 'Could not find service "' + label + '" in domain' in text:
        return None
    fail("Service ownership is unknown; inspect both Gateway launchd identities.")


def legacy_absent(home):
    if exists(home / "Library/LaunchAgents" / (LEGACY + ".plist")):
        fail("Legacy plist exists; follow docs/operators/installation-migration.md and preserve its selections.")
    if service(LEGACY) is not None:
        fail("Legacy service is loaded; refusing dual service ownership.")


def install(args, home):
    agents = home / "Library/LaunchAgents"
    plist = agents / (LABEL + ".plist")
    if exists(plist):
        fail("Plist already exists; use the stopped plist-change procedure.")
    binary = args.binary
    if not binary:
        gopath = checked("go", "env", "GOPATH").decode().strip()
        if ":" in gopath:
            fail("Multiple GOPATH entries; select --binary explicitly.")
        binary = str(Path(gopath) / "bin/agent-gateway")
    binary = absolute(binary, "Binary")
    if not binary.is_file() or not os.access(binary, os.X_OK):
        fail("Gateway executable not found; run make install or use --binary.")
    data = args.data_dir
    if not data:
        base = absolute(os.environ["XDG_DATA_HOME"], "XDG_DATA_HOME") if os.environ.get("XDG_DATA_HOME") else home / ".local/share"
        if exists(base / "mcp-gateway"):
            fail("Legacy root exists; migrate explicitly or select the existing --data-dir. Do not initialize another root.")
        data = str(base / "agent-gateway")
    data = absolute(data, "Data directory")
    if exists(data):
        private(data, directory=True)
    legacy_absent(home)
    if service(LABEL) is not None:
        fail("Canonical service is already loaded; refusing unknown ownership.")
    logs = home / "Library/Logs/agent-gateway"
    template = Path(__file__).resolve().parent.parent / "examples/launchd/agent-gateway.plist"
    with template.open("rb") as source:
        definition = plistlib.load(source)
    if definition.get("Label") != LABEL:
        fail("Template has an unexpected label.")
    if args.from_plist:
        archived = absolute(args.from_plist, "Archived plist")
        if archived.parent == agents or args.listen is not None or args.allowed_host:
            fail("Use an archived plist outside LaunchAgents without argument overrides.")
        if not args.binary or not args.data_dir:
            fail("Archived handover requires explicit --binary and --data-dir selections.")
        argv = read_definition(archived, labels=(LABEL, LEGACY))
        argv[0], argv[3] = str(binary), str(data)
        definition["ProgramArguments"] = argv
    else:
        definition["ProgramArguments"] = [str(binary), "serve", "--data-dir", str(data), "--listen", args.listen or "127.0.0.1:8210"]
        for host in args.allowed_host:
            definition["ProgramArguments"].extend(["--allowed-host", host])
    definition["StandardOutPath"] = str(logs / "stdout.log")
    definition["StandardErrorPath"] = str(logs / "stderr.log")
    content = plistlib.dumps(definition)
    for directory in [home / "Library", agents, home / "Library/Logs", logs]:
        if not exists(directory):
            directory.mkdir(mode=0o700)
        private(directory, directory=True, exact=directory == logs)
    for log in [logs / "stdout.log", logs / "stderr.log"]:
        if exists(log):
            private(log)
        else:
            fd = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            os.close(fd)
    fd = os.open(plist, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "wb") as target:
        target.write(content)
        target.flush()
        os.fsync(target.fileno())
    print("Installed:", plist)
    print("Executable:", binary)
    print("Data directory:", data)
    print("Logs:", logs)
    print("No state initialized and no service started. Verify selections before an explicit bootstrap.")


def read_definition(plist, labels=(LABEL,)):
    private(plist)
    with plist.open("rb") as source:
        definition = plistlib.load(source)
    if definition.get("Label") not in labels:
        fail("Plist label differs from the canonical service; use the manual procedure.")
    argv = definition.get("ProgramArguments")
    if not isinstance(argv, list) or len(argv) < 6 or not all(isinstance(arg, str) and arg and arg.strip() == arg and "\n" not in arg for arg in argv):
        fail("Unknown service arguments; reconcile the plist explicitly.")
    if argv[1:3] != ["serve", "--data-dir"] or argv[4] != "--listen":
        fail("Unknown service selections; reconcile the plist explicitly.")
    absolute(argv[0], "Executable")
    absolute(argv[3], "Data directory")
    return argv


def loaded_identity(text, plist, argv):
    lines = [line.strip() for line in text.splitlines()]
    if lines.count("path = " + str(plist)) != 1 or lines.count("program = " + argv[0]) != 1:
        fail("Loaded service identity differs from the plist; use the manual procedure.")
    if lines.count("arguments = {") != 1:
        fail("Loaded service arguments are unknown.")
    start = lines.index("arguments = {") + 1
    try:
        end = lines.index("}", start)
    except ValueError:
        fail("Loaded service arguments are incomplete.")
    if lines[start:end] != argv:
        fail("Loaded arguments differ from the installed plist; reconcile explicitly.")
    pids = [line[6:] for line in lines if line.startswith("pid = ")]
    if len(pids) > 1 or (pids and (not pids[0].isdigit() or int(pids[0]) <= 0)):
        fail("Cannot identify service process.")
    if not pids:
        fail("Loaded service has no provable process identity; use the manual stopped procedure.")
    return pids[0]


def process(pid, binary):
    status, data = run("ps", "-ww", "-p", pid, "-o", "uid=,lstart=,comm=")
    if status == 1 and not data.strip():
        return None
    if status or not data.strip():
        fail("Cannot inspect process identity; refusing to continue.")
    fields = data.decode().strip().split(None, 6)
    if len(fields) != 7 or fields[0] != str(os.getuid()) or fields[6] != binary:
        fail("Process identity is unknown; refusing to continue.")
    return data


def no_gateway_process(binary):
    text = checked("ps", "-axwwo", "uid=,pid=,comm=").decode()
    if not text.strip():
        fail("Process inspection was empty; refusing to bootstrap.")
    for line in text.splitlines():
        fields = line.strip().split(None, 2)
        if len(fields) != 3 or not fields[0].isdigit() or not fields[1].isdigit():
            fail("Process inspection was incomplete; refusing to bootstrap.")
        if int(fields[0]) == os.getuid() and Path(fields[2]).name in {"agent-gateway", "mcp-gateway", Path(binary).name}:
            fail("A Gateway process remains; refusing to bootstrap another owner.")


def restart(home):
    plist = home / "Library/LaunchAgents" / (LABEL + ".plist")
    argv = read_definition(plist)
    legacy_absent(home)
    state = service(LABEL)
    if state is not None:
        pid = loaded_identity(state, plist, argv)
        identity = process(pid, argv[0]) if pid else None
        checked("launchctl", "bootout", "gui/" + str(os.getuid()) + "/" + LABEL)
        deadline = time.monotonic() + 30
        for attempt in range(31):
            loaded = service(LABEL)
            current = process(pid, argv[0]) if pid else None
            if current is not None and current != identity:
                fail("Process identity changed after stop; refusing to bootstrap.")
            if loaded is None and current is None:
                break
            if attempt == 30 or time.monotonic() >= deadline:
                fail("Stop not confirmed within 30 seconds; refusing to bootstrap.")
            checked("sleep", "1")
        # Recheck the other identity and immutable selections before one start.
    legacy_absent(home)
    if read_definition(plist) != argv:
        fail("Plist changed during handover; refusing to bootstrap.")
    no_gateway_process(argv[0])
    checked("launchctl", "bootstrap", "gui/" + str(os.getuid()), str(plist))
    print("Launch accepted. Readiness has not been checked; follow docs/operators/launchd.md#verify.")


def main():
    parser = argparse.ArgumentParser(description="Install without initializing or starting Gateway, or restart one canonical service.")
    parser.add_argument("operation", choices=["install", "restart"])
    parser.add_argument("--binary")
    parser.add_argument("--data-dir")
    parser.add_argument("--listen")
    parser.add_argument("--from-plist", help="preserve literal arguments from an archived stopped-service plist")
    parser.add_argument("--allowed-host", action="append", default=[])
    args = parser.parse_args()
    if args.operation == "restart" and (args.binary or args.data_dir or args.listen or args.allowed_host or args.from_plist):
        fail("Restart uses unchanged installed selections; no overrides accepted.")
    for value, label in [(args.binary, "--binary"), (args.data_dir, "--data-dir")]:
        if value is not None:
            absolute(value, label)
    os.umask(0o077)
    home = account_home()
    if args.operation == "install":
        install(args, home)
    else:
        restart(home)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, plistlib.InvalidFileException, subprocess.SubprocessError) as error:
        print("LaunchAgent operation refused:", error, file=sys.stderr)
        sys.exit(1)
