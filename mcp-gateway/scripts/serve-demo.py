#!/usr/bin/env python3
"""Repository-only disposable Gateway and bounded local MCP fixtures."""

import argparse
import http.client
import ipaddress
import json
import math
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

LIMIT = 1024 * 1024
PROTOCOL = "2026-07-28"
MODULE = Path(__file__).resolve().parent.parent
INTERRUPTED = 0
DOCUMENTS = {
    "welcome": "Welcome to the local Gateway demo. No external services are used.",
    "permissions": "Demo Reader can echo and look up documents, but must request arithmetic access.",
}


class DemoError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise DemoError(message)


def authority(value):
    try:
        host, port = value.split(":")
        address = ipaddress.IPv4Address(host)
        valid = address.is_loopback and str(address) == host and str(int(port)) == port and 1 <= int(port) <= 65535
    except (ValueError, TypeError):
        valid = False
    if not valid:
        raise argparse.ArgumentTypeError("use canonical numeric IPv4 loopback and port, e.g. 127.0.0.1:8211")
    return value


class Child:
    def __init__(self, label, argv, env):
        self.label = label
        self.overflow = False
        self.settled = False
        self.cleanup_error = None
        self.process = subprocess.Popen(argv, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, start_new_session=True)
        self.pid = self.process.pid
        require(os.getpgid(self.pid) == self.pid, "child process-group capture failed")
        self.readers = []
        self.output = [bytearray(), bytearray()]
        for index, stream in enumerate((self.process.stdout, self.process.stderr)):
            reader = threading.Thread(target=self.drain, args=(stream, index), daemon=True)
            reader.start()
            self.readers.append(reader)

    def drain(self, stream, index):
        count = 0
        with stream:
            while chunk := stream.read1(8192):
                self.output[index].extend(chunk[:max(0, LIMIT - count)])
                count += len(chunk)
                if count > LIMIT:
                    self.overflow = True

    def exited(self):
        # Keep the direct child unreaped until group cleanup. Its reserved PID
        # cannot be reused between identity validation and signalling the group.
        return os.waitid(os.P_PID, self.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)

    def check(self):
        require(not INTERRUPTED, "interrupted by signal " + str(INTERRUPTED))
        require(not self.overflow, self.label + " exceeded output bound")
        require(self.exited() is None, self.label + " exited unexpectedly")

    def signal(self, value):
        require(os.getpgid(self.pid) == self.pid, self.label + " process identity changed")
        os.killpg(self.pid, value)

    def finish(self, timeout, expected_success=False):
        if self.settled:
            require(self.cleanup_error is None, self.cleanup_error)
            return
        deadline = time.monotonic() + timeout
        while self.exited() is None and time.monotonic() < deadline and not self.overflow and not INTERRUPTED:
            time.sleep(0.05)
        exited = self.exited()
        if exited is None:
            self.signal(signal.SIGTERM)
            deadline = time.monotonic() + 5
            while self.exited() is None and time.monotonic() < deadline:
                time.sleep(0.05)
        # Also fence any descendants before releasing the leader's identity.
        self.signal(signal.SIGKILL)
        deadline = time.monotonic() + 5
        while self.exited() is None and time.monotonic() < deadline:
            time.sleep(0.05)
        require(self.exited() is not None, self.label + " could not be reaped")
        code = self.process.wait()
        self.settled = True
        for reader in self.readers:
            reader.join(2)
        if any(reader.is_alive() for reader in self.readers):
            self.cleanup_error = self.label + " output pipes survived"
        try:
            os.killpg(self.pid, 0)
        except ProcessLookupError:
            pass
        else:
            self.cleanup_error = self.label + " process group survived cleanup"
        require(self.cleanup_error is None, self.cleanup_error)
        require(not self.overflow, self.label + " exceeded output bound")
        if expected_success:
            require(exited is not None and code == 0, self.label + " failed or timed out (child output suppressed)")


def schema(properties, required):
    return {"type": "object", "properties": properties, "required": required, "additionalProperties": False}


TOOLS = {
    "workshop": [
        {"name": "echo", "description": "Echo at most 256 characters", "inputSchema": schema({"text": {"type": "string", "maxLength": 256}}, ["text"])},
        {"name": "add", "description": "Add two bounded finite numbers", "inputSchema": schema({name: {"type": "number", "minimum": -1000000, "maximum": 1000000} for name in ("a", "b")}, ["a", "b"])},
        {"name": "controlled_error", "description": "Return a deliberate harmless tool error", "inputSchema": schema({}, [])},
    ],
    "library": [
        {"name": "lookup", "description": "Look up a fixed bundled sample document", "inputSchema": schema({"document": {"type": "string", "enum": list(DOCUMENTS)}}, ["document"])},
    ],
}


def tool_result(kind, name, arguments):
    require(type(arguments) is dict, "invalid arguments")
    require(name in {tool["name"] for tool in TOOLS[kind]}, "unknown tool")
    if name == "echo":
        require(set(arguments) == {"text"} and type(arguments["text"]) is str and len(arguments["text"]) <= 256, "invalid echo arguments")
        text = arguments["text"]
    elif name == "add":
        require(set(arguments) == {"a", "b"}, "invalid arithmetic arguments")
        require(all(type(value) in (int, float) and abs(value) <= 1000000 and math.isfinite(value) for value in arguments.values()), "invalid arithmetic operands")
        text = str(arguments["a"] + arguments["b"])
    elif name == "lookup":
        require(set(arguments) == {"document"} and type(arguments["document"]) is str and arguments["document"] in DOCUMENTS, "unknown document")
        text = DOCUMENTS[arguments["document"]]
    else:
        require(not arguments, "invalid controlled-error arguments")
        return {"content": [{"type": "text", "text": "Deliberate demo tool error"}], "isError": True}
    return {"content": [{"type": "text", "text": text}]}


def fixture(kind, endpoint):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def setup(self):
            super().setup()
            self.connection.settimeout(3)

        def do_POST(self):
            try:
                length = int(self.headers.get("Content-Length", "0"))
                require(self.path == "/mcp" and 0 < length <= 8192 and not self.headers.get("Transfer-Encoding"), "invalid fixture request")
                request = json.loads(self.rfile.read(length))
                method = request["method"]
                if method == "server/discover":
                    result = {"ttlMs": 0, "cacheScope": "public", "supportedVersions": [PROTOCOL], "capabilities": {}}
                elif method == "tools/list":
                    result = {"tools": TOOLS[kind], "nextCursor": None}
                elif method == "tools/call":
                    params = request["params"]
                    result = tool_result(kind, params["name"], params["arguments"])
                else:
                    raise DemoError("unsupported fixture method")
                body = json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            except (DemoError, ValueError, KeyError, TypeError, OSError):
                self.send_error(400, "invalid demo request")

    with HTTPServer(("127.0.0.1", 0), Handler) as server:
        with open(endpoint, "x", encoding="utf-8") as output:
            output.write(str(server.server_port))
        server.serve_forever(poll_interval=0.1)


class Client:
    def __init__(self, listen, bearer, deadline):
        self.host, port = listen.split(":")
        self.port = int(port)
        self.bearer = bearer
        self.deadline = deadline
        self.sequence = 0

    def request(self, method, path, body=None, headers=None, status=200, bearer=None):
        remaining = self.deadline - time.monotonic()
        require(remaining > 0, "seed deadline exceeded")
        connection = http.client.HTTPConnection(self.host, self.port, timeout=min(3, remaining))
        fields = {"Authorization": "Bearer " + (self.bearer if bearer is None else bearer)}
        if body is not None:
            fields["Content-Type"] = "application/json"
        fields.update(headers or {})
        try:
            connection.request(method, path, None if body is None else json.dumps(body), fields)
            response = connection.getresponse()
            content = response.read(LIMIT + 1)
            require(len(content) <= LIMIT, "public response exceeded bound")
            require(response.status == status, method + " " + path.split("?")[0] + " returned HTTP " + str(response.status))
            return (json.loads(content) if content else None), dict(response.getheaders())
        finally:
            connection.close()

    def get(self, path):
        return self.request("GET", "/api/v1/" + path)[0]

    def call(self, bearer, name, arguments):
        return self.rpc(bearer, "tools/call", {"name": name, "arguments": arguments})

    def rpc(self, bearer, method, params):
        self.sequence += 1
        params = dict(params, _meta={"io.modelcontextprotocol/protocolVersion": PROTOCOL, "io.modelcontextprotocol/clientInfo": {"name": "serve-demo", "version": "1"}, "io.modelcontextprotocol/clientCapabilities": {}})
        body = {"jsonrpc": "2.0", "id": self.sequence, "method": method, "params": params}
        return self.request("POST", "/mcp", body, {"Accept": "application/json, text/event-stream", "Mcp-Protocol-Version": PROTOCOL}, bearer=bearer)[0]


def wait_until(check, children, deadline, label):
    while time.monotonic() < deadline:
        for child in children:
            child.check()
        if check():
            return
        time.sleep(0.1)
    raise DemoError(label + " deadline exceeded")


def seed(client, root, endpoints, children):
    servers = {}
    for kind, port in endpoints.items():
        body = {"namespace": "demo_" + kind, "display_name": "Demo " + kind.title(), "enabled": True,
                "transport": {"kind": "streamable_http", "url": "http://127.0.0.1:" + port + "/mcp", "protocol_mode": "modern", "authentication": {"mode": "none"}}}
        created, _ = client.request("POST", "/api/v1/servers", body, {"Idempotency-Key": "demo-" + kind}, 201)
        server_id = created["server"]["id"]
        servers[kind] = server_id
        def active():
            server = client.get("servers/" + server_id)
            return server["runtime"]["state"] == "active" and server["catalog"]["active_state"] == "current" and server["catalog"]["active_tool_count"] == len(TOOLS[kind])
        wait_until(active, children, client.deadline, "fixture catalog")

    agents = {}
    for label, visibility in (("Explorer", "allowed-only"), ("Reader", "requestable"), ("Disabled", "allowed-only")):
        created, _ = client.request("POST", "/api/v1/principals", {"display_name": "Demo " + label, "visibility": visibility}, status=201)
        principal_id = created["principal"]["id"]
        # Credential publication uses the existing CLI's prepared one-time sink.
        agents[label] = principal_id
    return servers, agents


def complete_seed(client, root, servers, principals, run_command):
    agents = {}
    for label, principal_id in principals.items():
        sink = root / (label.lower() + "-bearer")
        run_command("issue " + label, ["principal", "credential", "issue", principal_id, "--secret-output", str(sink), "--yes", "--address", "http://" + client.host + ":" + str(client.port), "--admin-bearer-file", str(root / "admin-bearer")])
        agents[label] = sink.read_text().strip()
    current, headers = client.request("GET", "/api/v1/principals/" + principals["Disabled"])
    client.request("PATCH", "/api/v1/principals/" + current["id"], {"state": "disabled"}, {"If-Match": headers["Etag"]})
    for label, kind, name in (("Explorer", "workshop", None), ("Explorer", "library", None), ("Reader", "workshop", "echo"), ("Reader", "library", "lookup"), ("Reader", "workshop", "controlled_error")):
        client.request("POST", "/api/v1/grants", {"description": "Demo " + label + " " + kind + " access", "principal_id": principals[label], "effect": "deny" if name == "controlled_error" else "allow", "server_id": servers[kind], "upstream_name": name, "constraint": None, "expires_at": None}, status=201)
    for name, arguments, text in (("demo_workshop.echo", {"text": "Hello from the demo"}, "Hello from the demo"), ("demo_workshop.add", {"a": 19, "b": 23}, "42"), ("demo_library.lookup", {"document": "welcome"}, DOCUMENTS["welcome"])):
        response = client.call(agents["Explorer"], name, arguments)
        require(response.get("result", {}).get("content") == [{"type": "text", "text": text}], "seed invocation result mismatch")
    response = client.call(agents["Explorer"], "demo_workshop.controlled_error", {})
    require(response.get("error", {}).get("data", {}).get("code") == "downstream_failure", "controlled error missing")
    response = client.call(agents["Reader"], "demo_workshop.echo", {"text": "Reader access works"})
    require(response.get("result", {}).get("content") == [{"type": "text", "text": "Reader access works"}], "restricted allow failed")
    response = client.call(agents["Reader"], "demo_workshop.add", {"a": 1, "b": 2})
    require(response.get("error", {}).get("data", {}).get("code") == "call_rejected", "restricted call was not denied")
    client.request("POST", "/mcp", {}, {"Accept": "application/json, text/event-stream", "Mcp-Protocol-Version": PROTOCOL}, status=401, bearer=agents["Disabled"])
    response = client.call(agents["Reader"], "mcp_gateway.create_grant_request", {"policy": {"scope": "tool", "target": "demo_workshop.add", "constraint": None, "duration_seconds": None, "future_tools_acknowledged": False}})
    require("error" not in response and not response.get("result", {}).get("isError"), "grant request failed")
    require(len(client.get("servers")["items"]) == 2, "server verification failed")
    require(len(client.get("principals")["items"]) == 3, "principal verification failed")
    require(len(client.get("grants")["items"]) == 8, "grant verification failed")
    requests = client.get("grant-requests")["items"]
    require(len(requests) == 1 and requests[0]["state"] == "pending", "pending request missing")
    history = client.get("invocations")["items"]
    require(any(row["requested_name"] == "demo_workshop.add" and row["outcome"]["class"] == "succeeded" for row in history), "successful history missing")
    require(any(row["requested_name"] == "demo_workshop.controlled_error" and row["outcome"]["class"] == "downstream_failure" for row in history), "tool-error history missing")
    names = {tool["name"] for tool in client.rpc(agents["Explorer"], "tools/list", {})["result"]["tools"]}
    require({"demo_workshop.echo", "demo_workshop.add", "demo_workshop.controlled_error", "demo_library.lookup"} <= names, "demo discovery incomplete")
    reader_names = {tool["name"] for tool in client.rpc(agents["Reader"], "tools/list", {})["result"]["tools"]}
    require("demo_workshop.controlled_error" not in reader_names and {"demo_workshop.echo", "demo_workshop.add", "demo_library.lookup"} <= reader_names, "restricted discovery mismatch")
    return agents


def run(listen, dataset):
    require(hasattr(os, "waitid") and hasattr(os, "WNOWAIT"), "serve-demo requires POSIX waitid/WNOWAIT process ownership")
    with socket.socket() as probe:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        host, port = listen.split(":")
        try:
            probe.bind((host, int(port)))
            probe.listen(1)
        except OSError as error:
            raise DemoError("demo listener unavailable; choose another --listen authority") from error
    os.umask(0o077)
    root = Path(tempfile.mkdtemp(prefix="mcp-gateway-demo-"))
    children = []
    error = None
    try:
        home = root / "home"
        home.mkdir()
        binary = root / "mcp-gateway"
        print("Building demo Gateway from " + str(MODULE), flush=True)
        build = Child("Gateway build", ["go", "-C", str(MODULE), "build", "-mod=readonly", "-tags=e2e", "-o", str(binary), "./cmd/mcp-gateway"], os.environ.copy())
        children.append(build)
        build.finish(300, expected_success=True)
        children.remove(build)
        env = {"PATH": os.defpath, "HOME": str(home), "TMPDIR": str(root), "XDG_CONFIG_HOME": str(home / "config"), "XDG_DATA_HOME": str(home / "data"), "MCP_GATEWAY_E2E_ACCOUNT_HOME": str(home)}
        def command(label, args):
            require(not INTERRUPTED, "interrupted before " + label)
            child = Child(label, [str(binary), *args, "--data-dir", str(root / "data")], env)
            children.append(child)
            child.finish(15, expected_success=True)
            children.remove(child)
        command("initialize", ["initialize", "--secret-output", str(root / "admin-bearer")])
        gateway = Child("Gateway", [str(binary), "serve", "--data-dir", str(root / "data"), "--listen", listen], env)
        children.append(gateway)
        deadline = time.monotonic() + 60
        client = Client(listen, (root / "admin-bearer").read_text().strip(), deadline)
        def ready():
            try:
                return client.request("GET", "/readyz")[0] is not None
            except (OSError, http.client.HTTPException, DemoError):
                return False
        wait_until(ready, children, deadline, "Gateway readiness")
        endpoints = {}
        if dataset == "curated":
            for kind in TOOLS:
                endpoint = root / (kind + "-port")
                child = Child(kind + " fixture", [sys.executable, str(Path(__file__).resolve()), "--fixture", kind, str(endpoint)], env)
                children.append(child)
                wait_until(endpoint.exists, children, deadline, "fixture startup")
                endpoints[kind] = endpoint.read_text()
            servers, principals = seed(client, root, endpoints, children)
            complete_seed(client, root, servers, principals, command)
        else:
            for collection in ("servers", "principals", "grants", "grant-requests", "invocations"):
                require(not client.get(collection)["items"], "empty dataset contains records")
        for child in children:
            child.check()
        manifest = {"dataset": dataset, "listen": listen, "processes": {child.label: child.pid for child in children}, "fixtures": endpoints}
        with open(root / "ready.json", "x", encoding="utf-8") as output:
            json.dump(manifest, output)
        print("\nDemo Gateway ready (" + dataset + ")", flush=True)
        print("  URL:          http://" + listen + "/")
        print("  Run root:     " + str(root))
        print("  Data:         " + str(root / "data"))
        print("  Admin bearer: " + str(root / "admin-bearer"))
        if dataset == "curated":
            print("  Explorer:     " + str(root / "explorer-bearer") + " (all demo tools)")
            print("  Reader:       " + str(root / "reader-bearer") + " (echo/lookup; pending arithmetic request)")
            print("  Disabled:     " + str(root / "disabled-bearer") + " (authentication denied)")
            print("Use an MCP client with an Authorization bearer read from its protected file at /mcp.")
            print("Tools: demo_workshop.echo/add/controlled_error and demo_library.lookup.")
        print("Separate Vite: MCP_GATEWAY_UI_GATEWAY=http://" + listen + " npm run ui:dev")
        print("Stop with Ctrl-C; all temporary state will be removed. Relaunch for fresh data; DEMO_DATASET=empty for empty state.", flush=True)
        while True:
            for child in children:
                child.check()
            time.sleep(0.1)
    except BaseException as caught:
        error = caught
    finally:
        for value in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            signal.signal(value, signal.SIG_IGN)
        failures = []
        for child in reversed(children):
            try:
                child.finish(0)
            except (DemoError, OSError) as caught:
                failures.append(str(caught))
        if failures:
            raise DemoError("cleanup unconfirmed; retained " + str(root) + ": " + "; ".join(failures))
        try:
            shutil.rmtree(root)
        except OSError:
            raise DemoError("cleanup unconfirmed; retained " + str(root)) from None
    if error:
        raise error


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "--fixture":
        require(sys.argv[2] in TOOLS, "unknown fixture")
        os.umask(0o077)
        fixture(sys.argv[2], sys.argv[3])
        return
    parser = argparse.ArgumentParser(description="Build and serve an isolated curated Gateway demo; Ctrl-C removes all runtime state.")
    parser.add_argument("--listen", type=authority, default="127.0.0.1:8211")
    parser.add_argument("--dataset", choices=("curated", "empty"), default="curated")
    args = parser.parse_args()
    def interrupted(value, frame):
        global INTERRUPTED
        INTERRUPTED = value
    for value in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(value, interrupted)
    run(args.listen, args.dataset)


if __name__ == "__main__":
    try:
        main()
    except DemoError as error:
        print("serve-demo: " + str(error), file=sys.stderr)
        sys.exit(1)
    except (OSError, ValueError, KeyError, http.client.HTTPException):
        # Never render exception payloads: transport/parser failures can contain
        # credential-bearing response bytes. Diagnostics identify only the stage.
        print("serve-demo failed; environment was not delivered (see cleanup status if retained).", file=sys.stderr)
        sys.exit(1)
