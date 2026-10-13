"""CI-only full qualification extending the packaging owner's exact image/lifecycle.

Runtime output is private. The retained report contains fixed case names, IDs and
results only, never request/response bodies, credentials, callback URLs or logs.
"""
import base64
import http.client
import ipaddress
import json
import os
from pathlib import Path
import shutil
import signal
import select
import socket
import socketserver
import ssl
import subprocess
import tempfile
import threading
import time
import urllib.parse

from docker_smoke import ROOT, Smoke, command

PUBLIC_SUBNET = '11.254.139.0/29'
PUBLIC_IP = '11.254.139.2'
ISSUER = 'https://external.test:8443'
CADDY_IMAGE = 'caddy:2.10.2-alpine@sha256:d8c17a862962def15cde69863a3a463f25a2664942eafd7bdbf050e9c3116b83'
# The official Caddy binary has a file capability incompatible with cap_drop ALL.
# Copy bytes, not inode metadata, and keep the fixture's empty capability set.
CADDY_START = ('test "$(awk \'/^CapEff:/ {print $2}\' /proc/self/status)" = 0000000000000000; '
               'cat /usr/bin/caddy > /tmp/caddy; chmod 0700 /tmp/caddy; '
               'cmp /usr/bin/caddy /tmp/caddy; exec /tmp/caddy run --config /fixture-config/Caddyfile')
CASES = ('image-network-isolation', 'shipped-image-then-derived', 'browser-generic-proxy', 'boundary-and-caddy',
         'mcp-stdio-http-https-stream', 'mcp-address-denials', 'oauth-callbacks',
         'forward-http-https-trust', 'native-git-permissions', 'one-shot-uncertain',
         'replacement-persistence', 'stopped-backup-maintenance', 'forced-stop-diagnostics')


# Read-only fingerprints stay private; no file bytes or paths enter the report.
STOPPED_SNAPSHOT = r'''
import hashlib,itertools,json,stat,sys
from pathlib import Path
root=Path(sys.argv[1]); entries=[]; total=0; pending=False; marker=False
for path in itertools.chain([root],root.rglob('*')):
    if len(entries)>=4096: raise RuntimeError('snapshot entry bound')
    info=path.lstat(); name=str(path.relative_to(root)); digest=None
    if stat.S_ISREG(info.st_mode):
        total+=info.st_size
        if info.st_size>64*1024*1024 or total>256*1024*1024: raise RuntimeError('snapshot byte bound')
        with path.open('rb') as stream: digest=hashlib.file_digest(stream,'sha256').hexdigest()
        pending |= name in ('gateway.db-wal','gateway.db-journal') and info.st_size>0
        marker |= name=='run.unclean' and info.st_size>0
    elif not stat.S_ISDIR(info.st_mode): raise RuntimeError('snapshot unsafe file type')
    entries.append([name,info.st_mode,info.st_uid,info.st_gid,info.st_ino,info.st_nlink,info.st_size,info.st_mtime_ns,info.st_ctime_ns,digest])
entries.sort(key=lambda entry:entry[0])
print(json.dumps({'digest':hashlib.sha256(json.dumps(entries).encode()).hexdigest(),'control_pending':pending,'marker':marker}))
'''


def validate_stopped_plan(code, text, pending, installation):
    try:
        result = json.loads(text)
    except (ValueError, TypeError):
        raise RuntimeError('stopped dry-run returned invalid JSON; output withheld') from None
    require(isinstance(result, dict), 'stopped dry-run did not return an object')
    if pending:
        require(code == 7 and result.get('code') == 'inspection_unavailable'
                and result.get('exit_code') == 7 and result.get('uncertain') is False,
                'nonempty control sidecar did not yield exact immutable-inspection refusal')
        return 'refused-control-sidecar'
    require(code == 0 and result.get('dry_run') is True
            and result.get('operation') == 'verify-and-recover-storage'
            and result.get('installation_id') == installation,
            'closed control storage did not yield the expected dry-run plan')
    return 'closed-plan'


def require(value, message):
    if not value:
        raise RuntimeError(message)


def request(origin, method, path, body=None, headers=None, context=None, connect_port=None):
    url = urllib.parse.urlsplit(origin)
    conn = http.client.HTTPSConnection(url.hostname, connect_port or url.port, context=context, timeout=15)
    try:
        data = None if body is None else json.dumps(body).encode()
        fields = {'Host': url.netloc, **(headers or {})}
        if data is not None:
            fields['Content-Type'] = 'application/json'
        conn.request(method, path, data, fields)
        response = conn.getresponse()
        raw = response.read(1024 * 1024 + 1)
        require(len(raw) <= 1024 * 1024, 'response limit')
        return response.status, response.headers, raw
    finally:
        conn.close()


def read_sse_frame(response):
    """Require actual flushed body bytes, not just streaming response headers."""
    deadline = time.monotonic() + 5
    frame = bytearray()
    while not frame.endswith(b'\n\n'):
        require(time.monotonic() < deadline and len(frame) < 4096, 'SSE frame deadline/size exceeded')
        chunk = response.read(1)
        require(chunk, 'SSE ended before a complete frame')
        frame.extend(chunk)
    require(frame.startswith((b':', b'data:', b'event:')), 'invalid SSE frame')
    require(not response.isclosed(), 'SSE closed instead of remaining streaming')
    return bytes(frame)


def settlement_log_summary(text, resources):
    """Project only exact-operation diagnostic causes from a bounded private window."""
    raw = text.encode('utf-8')
    lines = raw[-16384:].decode('utf-8', errors='replace').splitlines()
    if len(raw) > 16384:
        lines = lines[1:]  # Never interpret a possibly partial first record.
    causes = []
    for line in lines[-100:]:
        try:
            value = json.loads(line)
        except ValueError:
            continue
        if (isinstance(value, dict) and value.get('event') == 'reconciliation_settlement_failure'
                and isinstance(value.get('resource'), str) and value['resource'] in resources):
            cause = value.get('cause')
            causes.append(cause if cause in ('capacity', 'unavailable', 'stopped') else 'other')
    return {'window_only': True, 'causes': causes[:8],
            'truncated': len(causes) > 8 or len(lines) > 100 or len(raw) > 16384}


def catalog_log_summary(text, server_id):
    """Keep only closed catalog audit facts for the exact private server resource."""
    raw = text.encode('utf-8')
    lines = raw[-16384:].decode('utf-8', errors='replace').splitlines()
    if len(raw) > 16384:
        lines = lines[1:]  # Never interpret a possibly partial first record.
    events = []
    operations = {'prepare refresh audit': 'prepare-audit', 'append refresh audit': 'append-audit'}
    for line in lines[-100:]:
        try:
            value = json.loads(line)
        except ValueError:
            continue
        if (isinstance(value, dict) and value.get('event') == 'upstream_unhealthy'
                and value.get('phase') == 'tool_discovery' and value.get('component') == 'catalog'
                and value.get('resource') == server_id):
            events.append({'operation': operations.get(value.get('operation'), 'other')
                           if isinstance(value.get('operation'), str) else 'other',
                           'cause': 'capacity' if value.get('explanation') == 'storage mutation capacity is exhausted' else 'other'})
    return {'window_only': True, 'events': events[:8],
            'truncated': len(events) > 8 or len(lines) > 100 or len(raw) > 16384}


def startup_failure(state, logs):
    """Retain only fixed categories from private startup output, never its bytes."""
    text = (str(state.get('Error', '')) + '\n' + logs).lower()
    categories = (('unknown command', 'cli-command'), ('operation not permitted', 'capability'),
                  ('permission denied', 'permissions'), ('read-only file system', 'read-only-filesystem'),
                  ('no such file or directory', 'missing-file'), ('unrecognized directive', 'config-directive'),
                  ('address already in use', 'socket-collision'), ('error decoding config', 'config-json'),
                  ('adapting config', 'config-adapter'), ('loading initial config', 'config-provision'),
                  ('panic:', 'panic'))
    reason = next((category for marker, category in categories if marker in text), 'unclassified')
    path = next((label for marker, label in (('/fixture-config/cert.pem', 'certificate'),
                ('/fixture-config/key.pem', 'private-key'), ('/data/', 'data-store'),
                ('/config/', 'config-store')) if marker in text), 'unspecified')
    return {'exit_code': int(state.get('ExitCode', -1)), 'reason': reason, 'path': path}


class LoopbackRelay:
    """Raw, bounded TCP ingress to an internal bridge; never terminate TLS."""
    def __init__(self, target):
        self.stopping = threading.Event()
        stopping = self.stopping

        class Handler(socketserver.BaseRequestHandler):
            def handle(self):
                try:
                    with socket.create_connection(target, timeout=3) as upstream:
                        self.request.settimeout(1)
                        upstream.settimeout(1)
                        sockets = (self.request, upstream)
                        deadline = time.monotonic() + 180
                        while not stopping.is_set() and time.monotonic() < deadline:
                            readable, _, _ = select.select(sockets, [], [], 0.2)
                            for source in readable:
                                data = source.recv(16384)
                                if not data:
                                    return
                                sockets[1 if source is sockets[0] else 0].sendall(data)
                except OSError:
                    # Connection failures are observed by the qualifying client.
                    pass

        self.server = socketserver.ThreadingTCPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval': 0.1})
        self.thread.start()
        self.port = self.server.server_address[1]

    def close(self):
        self.stopping.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        require(not self.thread.is_alive(), 'loopback relay cleanup unconfirmed')


class Qualification(Smoke):
    def __init__(self, run=command):
        super().__init__(run)
        self.public_network = self.name + '-public'
        self.derived = self.name + ':derived'
        self.peer = self.name + '-peer'
        self.private_peer = self.name + '-private'
        self.caddy = self.name + '-caddy'
        self.temp = tempfile.TemporaryDirectory(prefix='gateway-docker-')
        self.files = Path(self.temp.name)
        self.report = {'revision': os.environ.get('GITHUB_SHA', 'unknown'),
                       'source_head': os.environ.get('GATEWAY_SOURCE_HEAD', os.environ.get('GITHUB_SHA', 'unknown')),
                       'target_base': os.environ.get('GATEWAY_TARGET_BASE', ''), 'image': None,
                       'commands': ['python3 -B .github/scripts/docker_smoke.py --qualify'],
                       'cases': {name: 'unrun' for name in CASES}, 'cleanup': 'unrun',
                       'limitations': ['not live Debian adoption', 'not live provider/proxy qualification',
                                       'linux/amd64 only', 'not physical power-loss durability']}
        self.current_case = None
        self.admin = ''
        self.token = ''
        self.operations = {}
        self.relays = {}

    def case(self, name, work):
        self.current_case = name
        self.report['cases'][name] = 'failed'
        work()
        self.report['cases'][name] = 'passed'
        print('PASS container case: ' + name, flush=True)
        self.current_case = None

    def prepare(self):
        self.case('image-network-isolation', self.prepare_network)

    def prepare_network(self):
        # Refuse any route collision instead of hijacking an existing host destination.
        _, routes = self.run(['ip', '-json', 'route', 'show', 'table', 'all'], env=self.env, timeout=10)
        subnet = ipaddress.ip_network(PUBLIC_SUBNET)
        for route in json.loads(routes):
            destination = route.get('dst', 'default')
            if destination != 'default':
                require(not subnet.overlaps(ipaddress.ip_network(destination, strict=False)), 'fixture subnet overlaps host route')
        self.docker('network', 'create', '--internal', '--subnet', PUBLIC_SUBNET,
                    '--label', self.label, self.public_network)
        _, image = self.docker('image', 'inspect', '--format', '{{.Id}}', self.image)
        self.report['image'] = image.strip()
        config = self.files / 'config'
        config.mkdir(mode=0o755)
        self.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
                  '-keyout', str(config / 'key.pem'), '-out', str(config / 'cert.pem'),
                  '-subj', '/CN=container-fixture', '-addext',
                  'subjectAltName=DNS:localhost,DNS:external.test,DNS:notes-mcp,DNS:mixed.test'],
                 env=self.env, timeout=30)
        # Parent directory is 0700; only the peer mounts this key, not Gateway.
        (config / 'key.pem').chmod(0o644)
        (config / 'cert.pem').chmod(0o644)
        self.tls = ssl.create_default_context(cafile=str(config / 'cert.pem'))
        build = self.files / 'build'
        build.mkdir()
        shutil.copyfile(ROOT / '.github/docker/fixture.py', build / 'fixture.py')
        (build / 'Dockerfile').write_text('FROM ' + self.image + '\nUSER root\nRUN apk add --no-cache python3 git git-daemon\nCOPY fixture.py /opt/fixture.py\nUSER 10001:10001\n')
        self.docker('build', '--platform', 'linux/amd64', '--label', self.label, '-t', self.derived, str(build), timeout=300)
        self.report['derived_image'] = self.docker('image', 'inspect', '--format', '{{.Id}}', self.derived)[1].strip()
        self.start_peer(self.peer, self.public_network, ['--ip', PUBLIC_IP, '--network-alias', 'external.test'])
        self.start_peer(self.private_peer, self.network, ['--network-alias', 'notes-mcp'])
        self.proxy_port = self.port(self.private_peer, '9443/tcp')
        self.origin = 'https://localhost:' + str(self.proxy_port)
        (config / 'origin').write_text(self.origin)
        (config / 'origin').chmod(0o644)
        self.env['GATEWAY_EXTERNAL_ORIGIN'] = self.origin
        override = {'services': {'gateway': {
            'environment': {'SSL_CERT_FILE': '/fixture-ca.pem'},
            'volumes': [str(config / 'cert.pem') + ':/fixture-ca.pem:ro'],
            'dns': [PUBLIC_IP],
            'networks': {'proxy': {}, 'public_fixture': {}},
            'command': ['serve', '--data-dir', '/var/lib/agent-gateway', '--listen', '0.0.0.0:18210',
                        '--external-origin', self.origin, '--http-proxy-listen', '0.0.0.0:18212',
                        '--allowed-host', 'gateway', '--allowed-host', 'internal.example']}},
            'networks': {'public_fixture': {'external': True, 'name': self.public_network}}}
        path = self.files / 'override.json'
        path.write_text(json.dumps(override))
        self.compose.extend(['--file', str(path)])
        caddyfile = config / 'Caddyfile'
        caddyfile.write_text('{\n admin off\n auto_https off\n}\n:9443 {\n tls /fixture-config/cert.pem /fixture-config/key.pem\n reverse_proxy gateway:18210 {\n header_up Host {http.request.hostport}\n header_up -Forwarded\n header_up -X-Forwarded-*\n flush_interval -1\n lb_try_duration 0\n }\n}\n')
        caddyfile.chmod(0o644)
        self.docker('run', '-d', '--platform', 'linux/amd64', '--name', self.caddy, '--label', self.label, '--network', self.network,
                    '--read-only', '--cap-drop', 'ALL',
                    '--security-opt', 'no-new-privileges:true', '--tmpfs', '/data', '--tmpfs', '/config',
                    '--tmpfs', '/tmp:exec,mode=0700,size=128m', '--entrypoint', '/bin/sh',
                    '-v', str(config) + ':/fixture-config:ro', CADDY_IMAGE, '-ec', CADDY_START, timeout=120)
        self.caddy_port = self.port(self.caddy, '9443/tcp')
        self.report['caddy_image'] = self.docker('inspect', '--format', '{{.Image}}', self.caddy)[1].strip()
        for network in (self.network, self.public_network):
            # The existing packaging network is also internal in qualification mode.
            raw = self.docker('network', 'inspect', '--format', '{{.Internal}}', network)[1]
            require(raw.strip() == 'true', 'runtime network must be internal')

    def docker(self, *args, timeout=30, check=True):
        if args[:2] == ('network', 'create') and '--internal' not in args:
            args = (*args[:2], '--internal', *args[2:])
        return super().docker(*args, timeout=timeout, check=check)

    def start_peer(self, name, network, extra):
        self.docker('run', '-d', '--name', name, '--label', self.label, '--network', network,
                    *extra, '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true',
                    '--tmpfs', '/tmp:mode=1777,size=32m', '--tmpfs', '/fixture-state:uid=10001,gid=10001,mode=0700,size=32m',
                    '-v', str(self.files / 'config') + ':/fixture-config:ro',
                    '--entrypoint', '/usr/bin/python3', self.derived, '/opt/fixture.py')

    def port(self, container, port):
        # Docker does not publish ports for internal-only networks. The Linux CI
        # host can reach their bridge addresses without giving peers external egress.
        key = (container, port)
        if key not in self.relays:
            raw = self.docker('inspect', '--format', '{{json .}}', container)[1]
            info = json.loads(raw)
            if not info['State']['Running']:
                _, logs = self.docker('logs', '--tail', '30', container, check=False)
                failure = startup_failure(info['State'], logs)
                failure['peer'] = 'caddy' if container == self.caddy else 'gateway' if container == getattr(self, 'container', None) else 'private-peer'
                self.report['startup_failure'] = failure
                raise RuntimeError('ingress fixture exited before relay setup: ' + json.dumps(failure))
            address = info['NetworkSettings']['Networks'][self.network]['IPAddress']
            ip = ipaddress.ip_address(address)
            require(ip.version == 4 and ip.is_private and not ip.is_loopback, 'ingress requires owned private bridge address')
            require(port in ('9443/tcp', '18212/tcp'), 'unexpected ingress fixture port')
            self.relays[key] = LoopbackRelay((address, int(port.split('/')[0])))
        return self.relays[key].port

    def api(self, method, path, body=None, status=200, headers=None):
        code, fields, raw = request(self.origin, method, path, body,
                                    {'Authorization': 'Bearer ' + self.admin, **(headers or {})}, self.tls)
        require(code == status, 'control API ' + method + ' ' + path.split('?')[0] + ': expected ' + str(status) + ', got ' + str(code))
        require(fields.get('Cache-Control') == 'no-store', 'API cache boundary')
        return (json.loads(raw) if raw else None), fields

    def read_file(self, name):
        return self.dc('exec', '-T', 'gateway', 'cat', '/var/lib/agent-gateway/' + name)[1].strip()

    def wait_server(self, sid, active=True):
        deadline = time.monotonic() + 30
        observed = {}
        while time.monotonic() < deadline:
            value, _ = self.api('GET', '/api/v2/mcp/servers/' + sid)
            settled = True
            if sid in self.operations:
                operation, _ = self.api('GET', '/api/v2/mcp/servers/' + sid + '/operations/' + self.operations[sid])
                settled = operation['state'] in ('succeeded', 'failed', 'cancelled', 'superseded', 'interrupted')
            observed = {'tracked_operation_terminal': settled,
                        'reconciliation_idle': value['runtime']['reconciliation']['in_use'] == 0,
                        'runtime_active': value['runtime']['state'] == 'active',
                        'runtime_id_present': bool(value['runtime'].get('runtime_id')),
                        'catalog_one_tool': value['catalog']['active_tool_count'] == 1}
            for key, field, allowed in (
                    ('runtime_state', value['runtime']['state'], ('inactive', 'activating', 'active', 'stopping', 'retry_wait', 'degraded', 'authentication_required', 'deleted')),
                    ('runtime_reason', value['runtime'].get('reason'), (None, 'connectivity', 'resource_limit', 'configuration_invalid', 'credential_absent', 'tls_failed', 'catalog_invalid', 'catalog_stale', 'stop_unconfirmed')),
                    ('catalog_state', value['catalog'].get('active_state'), ('absent', 'refreshing', 'current', 'stale', 'unavailable'))):
                observed[key] = field if field in allowed else 'other'
            if settled and value['runtime']['reconciliation']['in_use'] == 0:
                if active and value['runtime']['state'] == 'active' and value['catalog']['active_tool_count'] == 1:
                    return value
                if not active and value['runtime']['state'] != 'active':
                    return value
            time.sleep(0.1)
        self.report['server_settlement_failure'] = observed
        # An old terminal operation does not prove restart activation was created.
        operations, _ = self.api('GET', '/api/v2/mcp/servers/' + sid + '/operations')
        observed['operations_truncated'] = bool(operations.get('next_cursor')) or len(operations['items']) > 8
        observed['operations'] = [{'tracked': item['id'] == self.operations.get(sid),
                                  'activate': item['kind'] == 'activate',
                                  'state': item['state'] if item['state'] in ('scheduled', 'running', 'succeeded', 'failed', 'cancelled', 'superseded', 'interrupted') else 'other',
                                  'started': item.get('started_at') is not None,
                                  'finished': item.get('finished_at') is not None} for item in operations['items'][:8]]
        resources = {sid + '/' + item['id'] for item in operations['items'][:8]}
        try:
            code, text = self.docker('logs', '--tail', '100', self.container, timeout=10, check=False)
            observed['settlement_diagnostics'] = settlement_log_summary(text, resources) if code == 0 else {}
            observed['settlement_diagnostics']['read_ok'] = code == 0
            observed['catalog_diagnostics'] = catalog_log_summary(text, sid) if code == 0 else {}
            observed['catalog_diagnostics']['read_ok'] = code == 0
        except (RuntimeError, subprocess.TimeoutExpired):
            observed['settlement_diagnostics'] = {'read_ok': False}
            observed['catalog_diagnostics'] = {'read_ok': False}
        raise RuntimeError('server did not reach expected settled state: ' + json.dumps(observed, sort_keys=True))

    def server(self, namespace, transport, enabled=True, status=201):
        value, fields = self.api('POST', '/api/v2/mcp/servers', {'namespace': namespace, 'display_name': namespace,
                               'enabled': enabled, 'transport': transport}, status,
                               {'Idempotency-Key': namespace})
        if status != 201:
            return None, None
        sid = value['server']['id']
        if value.get('operation'):
            self.operations[sid] = value['operation']['id']
        return sid, fields['ETag']

    @staticmethod
    def transport(url, private=False, oauth=False):
        auth = {'mode': 'oauth', 'registration': {'mode': 'dynamic', 'issuer': None},
                'trusted_origins': [], 'request_offline_access': False} if oauth else {'mode': 'none'}
        return {'kind': 'streamable_http', 'url': url, 'protocol_mode': 'modern',
                'allow_private_http': private, 'authentication': auth}

    def rpc(self, method, params=None, token=None, port=None):
        data = {'_meta': {'io.modelcontextprotocol/protocolVersion': '2026-07-28',
                         'io.modelcontextprotocol/clientInfo': {'name': 'docker-fixture', 'version': '1'},
                         'io.modelcontextprotocol/clientCapabilities': {}}, **(params or {})}
        return request(self.origin, 'POST', '/mcp', {'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': data},
                       {'Authorization': 'Bearer ' + (self.token if token is None else token),
                        'MCP-Protocol-Version': '2026-07-28', 'MCP-Method': method,
                        'Accept': 'application/json, text/event-stream'}, self.tls, port)

    def call(self, name):
        status, _, raw = self.rpc('tools/call', {'name': name, 'arguments': {}})
        require(status == 200 and 'container fixture success' in raw.decode(), 'MCP call did not return actual fixture result')

    def grant(self, sid):
        self.api('POST', '/api/v2/mcp/grants', {'description': 'Docker fixture', 'principal_id': self.principal,
                 'effect': 'allow', 'server_id': sid, 'upstream_name': 'echo', 'constraint': None, 'expires_at': None}, 201)

    def qualify(self, container):
        self.container = container
        self.case('shipped-image-then-derived', self.switch_to_derived)
        self.admin = self.read_file('admin-bearer')
        resolved = json.loads(self.dc('exec', '-T', 'gateway', 'python3', '-c',
            "import socket,json; print(json.dumps({name:sorted({e[4][0] for e in socket.getaddrinfo(name,8443)}) for name in ['external.test','notes-mcp']}))")[1])
        require(set(resolved['external.test']) == {PUBLIC_IP}, 'external fixture DNS not pinned to owned peer')
        require(bool(resolved['notes-mcp']) and all(ipaddress.ip_address(ip).is_private for ip in resolved['notes-mcp']), 'private HTTP fixture did not resolve privately')
        self.ca = self.read_file('http-ca.pem') + '\n'
        self.case('browser-generic-proxy', self.browser)
        self.case('boundary-and-caddy', self.boundary)
        self.case('mcp-stdio-http-https-stream', self.mcp)
        self.case('mcp-address-denials', self.address_denials)
        self.case('oauth-callbacks', self.oauth)
        self.case('forward-http-https-trust', self.forwarding)
        self.case('native-git-permissions', self.git)
        self.case('one-shot-uncertain', self.uncertain)
        self.case('replacement-persistence', self.persistence)
        self.case('stopped-backup-maintenance', self.maintenance)
        self.case('forced-stop-diagnostics', self.forced_stop)
        return self.container

    def switch_to_derived(self):
        # The inherited owner has just initialized, readied and authenticated the
        # shipped image. Only now replace it with the documented stdio extension.
        self.stop()
        self.shell('test ! -e run.unclean')
        self.dc('rm', '--force', 'gateway')
        digests = []
        for suffix, image in (('basehash', self.image), ('derivedhash', self.derived)):
            raw = self.docker('run', '--rm', '--name', self.name + '-' + suffix, '--label', self.label,
                              '--network', 'none', '--read-only', '--cap-drop', 'ALL',
                              '--entrypoint', 'sha256sum', image, '/usr/local/bin/agent-gateway')[1]
            digests.append(raw.split()[0])
        require(len(digests[0]) == 64 and digests[0] == digests[1], 'derived fixture replaced product binary')
        self.report['gateway_binary_sha256'] = digests[0]
        self.env['GATEWAY_IMAGE'] = self.derived
        self.start()

    def browser(self):
        payload = json.dumps({'origin': self.origin, 'admin': self.admin}).encode()
        # No browser tracing, screenshots or HAR can capture one-time credentials.
        env = {**self.env, 'NODE_EXTRA_CA_CERTS': str(self.files / 'config/cert.pem')}
        self.report['browser'] = json.loads(self.input_command(
            ['node', str(ROOT / '.github/docker/browser.mjs')], payload, timeout=90, env=env))

    def boundary(self):
        for path in ('/livez', '/readyz'):
            require(request(self.origin, 'GET', path, context=self.tls)[0] == 200, 'proxied health probe')
        for headers in ({'Host': 'evil.example'}, {'Origin': 'https://evil.example'}):
            require(request(self.origin, 'POST', '/api/v2/admin-sessions', {},
                            {'Authorization': 'Bearer ' + self.admin, **headers}, self.tls)[0] >= 400, 'external Host/Origin accepted')
        # A compliant proxy strips spoofed fields, not derives authority from them.
        require(request(self.origin, 'GET', '/readyz', headers={'Forwarded': 'host=evil.example', 'X-Forwarded-Host': 'evil.example'}, context=self.tls)[0] == 200, 'generic proxy stripping')
        code, _, _ = request(self.origin, 'GET', '/api/v2/system-status', headers={'Authorization': 'Bearer ' + self.admin}, context=self.tls, connect_port=self.caddy_port)
        require(code == 200, 'optional Caddy example authenticated API: expected 200, got ' + str(code))
        # Direct backend aliases are MCP/API Host authority, never browser Origin authority.
        program = "import http.client,json; c=http.client.HTTPConnection('gateway',18210,timeout=5); c.request('GET','/readyz',headers=json.loads(__import__('sys').argv[1])); r=c.getresponse(); print(r.status); r.read(); c.close()"
        for headers, expected in (({'Host': 'internal.example'}, 200), ({'Host': 'evil.example'}, 421), ({'Host': 'internal.example', 'Forwarded': 'host=localhost'}, 400), ({'Host': 'internal.example', 'X-Forwarded-Host': 'localhost'}, 400)):
            raw = self.docker('exec', self.private_peer, 'python3', '-c', program, json.dumps(headers))[1]
            require(int(raw.strip()) == expected, 'direct Host/forwarding boundary')
        program = "import http.client,json,sys; token=sys.stdin.read(); out=[]\nfor extra in [{},{'Origin':'http://internal.example'}]:\n c=http.client.HTTPConnection('gateway',18210,timeout=5); c.request('GET','/api/v2/system-status',headers={'Host':'internal.example','Authorization':'Bearer '+token,**extra}); r=c.getresponse(); out.append(r.status); r.read(); c.close()\nprint(json.dumps(out))"
        statuses = json.loads(self.input_command(['docker', 'exec', '-i', self.private_peer, 'python3', '-c', program], self.admin.encode()))
        # Browser traffic on an internal alias fails Host authority before Origin validation.
        require(statuses == [200, 421], 'alias API/browser boundary: expected [200, 421], got ' + str(statuses))

    def mcp(self):
        principal, fields = self.api('POST', '/api/v2/principals', {'display_name': 'Docker agent', 'visibility': 'all'}, 201)
        self.principal = principal['principal']['id']
        issued, _ = self.api('POST', '/api/v2/principals/' + self.principal + '/credential', {}, 201, {'If-Match': fields['ETag']})
        self.token = issued['bearer']
        for invalid, expected, problem in (('invalid', 401, 'authentication_required'),
                                           (self.admin, 403, 'credential_domain_mismatch')):
            status, _, raw = self.rpc('server/discover', token=invalid)
            require(status == expected, 'MCP credential boundary: expected ' + str(expected) + ', got ' + str(status))
            require(json.loads(raw)['code'] == problem, 'MCP credential boundary returned wrong problem code')
        self.servers = []
        transports = [('stdio', {'kind': 'stdio', 'executable': '/usr/bin/python3', 'arguments': ['/opt/fixture.py', 'stdio'],
                                 'working_directory': '/tmp', 'environment': {}, 'secret_environment': {}}),
                      ('private', self.transport('http://notes-mcp:8080/mcp', True)),
                      ('https', self.transport(ISSUER + '/mcp')),
                      ('stream', self.transport(ISSUER + '/stream'))]
        for name, transport in transports:
            sid, _ = self.server(name, transport)
            self.wait_server(sid)
            self.grant(sid)
            self.call(name + '.echo')
            self.servers.append(sid)
        require(self.rpc('server/discover')[0] == 200, 'authenticated MCP discovery')
        status, _, body = self.rpc('tools/list')
        require(status == 200 and all((name + '.echo').encode() in body for name, _ in transports), 'MCP projected catalog')
        status, _, body = self.rpc('tools/call', {'name': 'https.echo', 'arguments': {}}, port=self.caddy_port)
        require(status == 200 and b'container fixture success' in body, 'Caddy MCP call')
        for port in (self.proxy_port, self.caddy_port):
            self.mcp_stream(port)

    def mcp_stream(self, port):
        headers = {'Authorization': 'Bearer ' + self.token, 'MCP-Protocol-Version': '2025-11-25',
                   'Accept': 'application/json, text/event-stream'}
        status, fields, _ = request(self.origin, 'POST', '/mcp', {'jsonrpc': '2.0', 'id': 1,
            'method': 'initialize', 'params': {'protocolVersion': '2025-11-25', 'capabilities': {},
            'clientInfo': {'name': 'container-stream', 'version': '1'}}}, headers, self.tls, port)
        session = next((v for k, v in fields.items() if k.lower() == 'mcp-session-id'), None)
        require(status == 200 and session, 'legacy stream session not established')
        headers['MCP-Session-Id'] = session
        require(request(self.origin, 'POST', '/mcp', {'jsonrpc': '2.0', 'method': 'notifications/initialized'}, headers, self.tls, port)[0] == 202, 'legacy stream initialization')
        conn = http.client.HTTPSConnection('localhost', port, context=self.tls, timeout=5)
        try:
            conn.request('GET', '/mcp', headers={'Host': urllib.parse.urlsplit(self.origin).netloc,
                                              **headers, 'Accept': 'text/event-stream'})
            response = conn.getresponse()
            require(response.status == 200 and response.getheader('Content-Type').startswith('text/event-stream'), 'MCP ingress stream headers absent')
            # The pinned production SDK emits a priming comment on its still-open
            # legacy stream. Reading the frame catches header-only proxy flushing.
            read_sse_frame(response)
        finally:
            conn.close()
        require(request(self.origin, 'DELETE', '/mcp', headers=headers, context=self.tls, connect_port=port)[0] == 204, 'legacy stream cleanup')
        require(request(self.origin, 'GET', '/mcp', headers=headers, context=self.tls, connect_port=port)[0] == 404, 'deleted stream session still admitted')

    def address_denials(self):
        resolved = json.loads(self.dc('exec', '-T', 'gateway', 'python3', '-c',
            "import socket,json; print(json.dumps(sorted({entry[4][0] for entry in socket.getaddrinfo('mixed.test',8443)})))")[1])
        require(set(resolved) == {PUBLIC_IP, '127.0.0.1'}, 'mixed DNS fixture did not supply both address classes')
        self.server('default-denied', self.transport('http://notes-mcp:8080/mcp'), status=400)
        for name, endpoint in (('private-https', 'https://notes-mcp:8443/mcp'), ('mixed', 'https://mixed.test:8443/mcp'), ('forbidden', 'https://169.254.169.254/mcp')):
            sid, _ = self.server(name, self.transport(endpoint))
            view = self.wait_server(sid, False)
            require(view['catalog']['active_tool_count'] == 0, 'denied address published tools')
        sid = self.servers[1]
        _, fields = self.api('GET', '/api/v2/mcp/servers/' + sid)
        self.api('PATCH', '/api/v2/mcp/servers/' + sid,
                 {'transport': self.transport('http://notes-mcp:8080/mcp')}, 400, {'If-Match': fields['ETag']})
        # Explicitly disabling withdraws the opted-in private runtime and stale access.
        updated, _ = self.api('PATCH', '/api/v2/mcp/servers/' + sid, {'enabled': False}, 200, {'If-Match': fields['ETag']})
        if updated.get('operation'):
            self.operations[sid] = updated['operation']['id']
        self.wait_server(sid, False)
        _, _, raw = self.rpc('tools/call', {'name': 'private.echo', 'arguments': {}})
        require(b'container fixture success' not in raw, 'disabled private server still dispatches')

    def fixture_get(self, path):
        program = "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:8080'+__import__('sys').argv[1],timeout=5).read().decode())"
        return json.loads(self.docker('exec', self.peer, 'python3', '-c', program, path)[1])

    def provider_authorize(self, authorization_url):
        # Browser-equivalent GET, connected through Docker DNS rather than host DNS.
        program = "import http.client,ssl,urllib.parse,sys; u=urllib.parse.urlsplit(sys.stdin.read()); c=http.client.HTTPSConnection(u.hostname,u.port,context=ssl.create_default_context(cafile='/fixture-config/cert.pem'),timeout=5); c.request('GET',u.path+'?'+u.query); r=c.getresponse(); assert r.status==302; print(r.getheader('Location')); r.read(); c.close()"
        result = self.input_command(['docker', 'exec', '-i', self.peer, 'python3', '-c', program], authorization_url.encode())
        return result.strip()

    def input_command(self, args, payload, timeout=30, success=True, env=None):
        with tempfile.TemporaryFile() as output, tempfile.TemporaryFile() as errors:
            process = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=output, stderr=errors,
                                       env=self.env if env is None else env, start_new_session=True)
            try:
                process.communicate(payload, timeout=timeout)
            except BaseException:
                # This unreaped child owns the freshly created process group.
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
                raise
            require(output.tell() <= 1024 * 1024 and errors.tell() <= 1024 * 1024, 'private command output limit')
            require((process.returncode == 0) == success, 'private fixture command unexpected exit; output withheld')
            output.seek(0)
            return output.read().decode()

    def oauth(self):
        self.oauth_id, _ = self.server('oauth', self.transport(ISSUER + '/oauth', oauth=True), enabled=False)
        path = '/api/v2/mcp/servers/' + self.oauth_id
        def start():
            self.wait_server(self.oauth_id, False)
            _, fields = self.api('GET', path)
            flow, _ = self.api('POST', path + '/oauth-flows', {}, 201, {'If-Match': fields['ETag']})
            return flow, self.provider_authorize(flow['authorization_url'])
        flow, callback = start()
        parsed = urllib.parse.urlsplit(callback)
        query = urllib.parse.parse_qs(parsed.query)
        query['state'] = ['unknown-state']
        bad = parsed.path + '?' + urllib.parse.urlencode(query, doseq=True)
        require(request(self.origin, 'GET', bad, context=self.tls)[0] == 400, 'unknown OAuth state')
        require(self.fixture_get('/stats').get('token-exchange', 0) == 0, 'invalid state exchanged code')
        callback_path = parsed.path + '?' + parsed.query
        require(request(self.origin, 'GET', '/oauth/%63allback?' + parsed.query, context=self.tls)[0] >= 400, 'encoded OAuth callback path accepted')
        require(request(self.origin, 'GET', callback_path, headers={'Host': 'wrong.example'}, context=self.tls)[0] == 421, 'wrong callback authority accepted')
        require(request(self.origin, 'GET', callback_path, context=self.tls)[0] == 200, 'OAuth callback')
        require(request(self.origin, 'GET', callback_path, context=self.tls)[0] == 400, 'OAuth replay')
        require(self.fixture_get('/stats')['token-exchange'] == 1, 'one OAuth exchange')
        completed, _ = self.api('GET', path + '/oauth-flows/' + flow['flow']['id'])
        require(completed['state'] == 'succeeded', 'OAuth completion state')
        _, fields = self.api('GET', path)
        self.api('PATCH', path, {'enabled': True}, 200, {'If-Match': fields['ETag']})
        self.wait_server(self.oauth_id)
        self.grant(self.oauth_id)
        self.call('oauth.echo')
        # Leave a real pending callback to prove replacement invalidation later.
        _, fields = self.api('GET', path)
        self.api('PATCH', path, {'enabled': False}, 200, {'If-Match': fields['ETag']})
        canceled, location = start()
        self.api('DELETE', path + '/oauth-flows/' + canceled['flow']['id'], {}, 204)
        parsed_cancel = urllib.parse.urlsplit(location)
        require(request(self.origin, 'GET', parsed_cancel.path + '?' + parsed_cancel.query, context=self.tls)[0] == 400, 'canceled OAuth state accepted')
        _, location = start()
        bad_issuer = urllib.parse.urlsplit(location)
        query = urllib.parse.parse_qs(bad_issuer.query)
        query['iss'] = ['https://wrong.example']
        require(request(self.origin, 'GET', bad_issuer.path + '?' + urllib.parse.urlencode(query, doseq=True), context=self.tls)[0] == 400, 'wrong OAuth issuer accepted')
        interrupted, self.pending_callback = start()
        self.pending_flow = interrupted['flow']['id']

    def http_grant(self, scheme, port, credential=None, status=201):
        policy = {'version': 1, 'type': 'allow_requests', 'request': {'origin': {'scheme': scheme, 'host': 'external.test', 'port': port}, 'methods': {'any': True}, 'path': {'kind': 'any'}}}
        if credential:
            policy['credential_id'] = credential
        return self.api('POST', '/api/v2/http/grants', {'principal_id': self.principal, 'description': None, 'expires_at': None, 'policy': policy}, status)

    def proxy_request(self, scheme, method='GET', path='/injected', trust='gateway', token=True):
        proxy_port = self.port(self.container, '18212/tcp')
        conn = http.client.HTTPConnection('127.0.0.1', proxy_port, timeout=10)
        auth = 'Basic ' + base64.b64encode(('agent:' + self.token).encode()).decode()
        headers = {'Proxy-Authorization': auth} if token else {}
        try:
            if scheme == 'https':
                conn.set_tunnel('external.test', 8443, headers)
                conn.connect()
                context = (ssl.create_default_context(cadata=self.ca) if trust == 'gateway' else
                           ssl.create_default_context(cafile=str(self.files / 'config/cert.pem')) if trust == 'wrong' else
                           ssl.create_default_context())
                conn.sock = context.wrap_socket(conn.sock, server_hostname='external.test')
                conn.request(method, path, headers={'Host': 'external.test:8443'})
            else:
                conn.request(method, 'http://external.test:8080' + path, headers=headers)
            response = conn.getresponse()
            return response.status, response.read(65536)
        finally:
            conn.close()

    def forwarding(self):
        require(self.proxy_request('http', token=False)[0] == 407, 'proxy missing agent authentication')
        require(self.proxy_request('http')[0] == 403, 'proxy default deny')
        for scheme, port in (('http', 8080), ('https', 8443)):
            material, _ = self.api('POST', '/api/v2/http/credentials', {'name': 'fixture-' + scheme,
                     'boundary': {'host': 'external.test', 'port': port, 'allow_wildcard': False},
                     'recipe': {'header': 'X-Fixture-Key', 'prefix': ''}, 'secret': 'synthetic-http-secret'}, 201)
            if scheme == 'http':
                self.http_grant(scheme, port, material['id'], status=400)
                require(self.proxy_request(scheme, path='/plain')[0] == 403, 'rejected HTTP credential grant allowed forwarding')
                self.http_grant(scheme, port)
                status, raw = self.proxy_request(scheme, path='/plain')
                require(status == 200 and raw == b'plain', 'proxy HTTP forwarding without credentials')
            else:
                self.http_grant(scheme, port, material['id'])
                status, raw = self.proxy_request(scheme)
                require(status == 200 and raw == b'injected', 'proxy actual HTTPS injection')
        for trust in ('wrong', 'missing'):
            try:
                self.proxy_request('https', trust=trust)
            except ssl.SSLCertVerificationError:
                pass
            else:
                raise RuntimeError(trust + ' interception trust succeeded')

    def git(self):
        credential, _ = self.api('POST', '/api/v2/git/credentials', {'name': 'fixture-git', 'origin': ISSUER,
                  'recipe': {'header': 'Authorization', 'prefix': 'Bearer '}, 'secret': 'synthetic-git-secret'}, 201)
        repository, _ = self.api('POST', '/api/v2/git/repositories', {'name': 'fixture', 'url': ISSUER + '/repo.git',
                                 'aliases': [], 'credential_id': credential['id']}, 201)
        _, fields = self.api('GET', '/api/v2/git/routing-profile')
        self.api('PATCH', '/api/v2/git/routing-profile', {'origins': [ISSUER]}, 200, {'If-Match': fields['ETag']})
        self.api('POST', '/api/v2/git/grants', {'principal_id': self.principal, 'repository_id': repository['id'],
                 'description': 'fixture read and one branch', 'expires_at': None,
                 'policy': {'version': 1, 'read': True, 'refs': [{'ref': {'kind': 'exact', 'value': 'refs/heads/allowed'}, 'actions': ['create', 'update']}]}}, 201)
        # Native clients have private HOME/config and no inherited credential helpers.
        directory = self.files / 'git'
        directory.mkdir(mode=0o700)
        ca = directory / 'ca.pem'
        ca.write_text(self.ca)
        config = directory / 'config'
        config.write_text('[http]\n sslCAInfo = ' + str(ca) + '\n version = HTTP/1.1\n[credential]\n helper =\n[user]\n name = Fixture\n email = fixture@example.invalid\n')
        config.chmod(0o600)
        env = {key: value for key, value in os.environ.items() if key in ('PATH', 'LANG')}
        env.update({'HOME': str(directory), 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': str(config),
                    'GIT_TERMINAL_PROMPT': '0', 'GIT_ASKPASS': '/bin/false',
                    'https_proxy': 'http://agent:' + self.token + '@127.0.0.1:' + str(self.port(self.container, '18212/tcp'))})
        self.git_env = env
        self.git_dir = directory
        def run(*args, good=True):
            code, output = command(['git', *args], env=env, timeout=30, check=False)
            require((code == 0) == good, 'native Git ' + args[0] + ' unexpected result')
            return output
        run('init', str(directory / 'seed'))
        run('-C', str(directory / 'seed'), 'commit', '--allow-empty', '-m', 'fixture')
        run('-C', str(directory / 'seed'), 'push', ISSUER + '/repo.git', 'HEAD:refs/heads/allowed')
        refs = run('ls-remote', ISSUER + '/repo.git')
        require('refs/heads/allowed' in refs, 'native Git remote ref absent')
        run('clone', '--branch', 'allowed', ISSUER + '/repo.git', str(directory / 'clone'))
        run('-C', str(directory / 'clone'), 'fetch', 'origin')
        run('-C', str(directory / 'seed'), 'push', ISSUER + '/repo.git', 'HEAD:refs/heads/denied', good=False)
        require('refs/heads/denied' not in run('ls-remote', ISSUER + '/repo.git'), 'denied Git ref exists')

    def uncertain(self):
        before = self.fixture_get('/stats').get('uncertain', 0)
        try:
            status, _ = self.proxy_request('http', 'POST', '/uncertain')
            require(status >= 400, 'uncertain mutation reported success')
        except (http.client.RemoteDisconnected, ConnectionResetError):
            pass
        require(self.fixture_get('/stats').get('uncertain', 0) == before + 1, 'uncertain mutation replayed')

    def stop(self):
        self.dc('stop', '--timeout', '30', 'gateway', timeout=40)
        state = json.loads(self.docker('inspect', '--format', '{{json .State}}', self.container)[1])
        require(not state['Running'] and state['ExitCode'] == 0 and not state['OOMKilled'], 'replacement stop unclean')

    def start(self):
        self.dc('up', '-d', '--no-build', '--wait', '--wait-timeout', '90', timeout=120)
        self.container = self.dc('ps', '--quiet', 'gateway')[1].strip()

    def persistence(self):
        before = self.dc('exec', '-T', 'gateway', 'sha256sum', 'admin-bearer', 'master-key', 'http-ca.pem')[1]
        backup, _ = self.api('POST', '/api/v2/backups', {}, 201, {'Idempotency-Key': 'before-replacement'})
        self.report['installation_identity'] = backup['installation_id']
        outcomes = []
        def admitted():
            try:
                outcomes.append(self.rpc('tools/call', {'name': 'https.echo', 'arguments': {'drain': True}}))
            except Exception:
                outcomes.append(None)
        worker = threading.Thread(target=admitted)
        worker.start()
        try:
            deadline = time.monotonic() + 5
            while self.fixture_get('/stats').get('drain-started', 0) != 1:
                require(time.monotonic() < deadline, 'drain call not admitted')
                time.sleep(0.05)
            self.stop()
        finally:
            # Local clean shutdown does not prove remote effects stopped. Release
            # the separate peer only after stop, then observe its own settlement.
            try:
                self.docker('exec', self.peer, 'touch', '/fixture-state/drain-release')
            finally:
                worker.join(timeout=20)
        require(not worker.is_alive() and len(outcomes) == 1 and outcomes[0] is not None, 'drain call did not settle')
        require(outcomes[0][0] == 200, 'drain call did not return an MCP response')
        result = json.loads(outcomes[0][2])
        error = result.get('error', {})
        data = error.get('data', {})
        require('result' not in result and error.get('code') == -32000 and data.get('code') == 'outcome_unknown'
                and data.get('outcomeUnknown') is True and bool(data.get('invocationId')), 'SIGTERM did not classify handed-off call as uncertain')
        deadline = time.monotonic() + 5
        while self.fixture_get('/stats').get('drain-completed', 0) != 1:
            require(time.monotonic() < deadline, 'drain peer did not settle after release')
            time.sleep(0.05)
        require(self.fixture_get('/stats').get('drain-started', 0) == 1, 'drain upstream work was replayed')
        self.shell('test -s stdio-stopped; test ! -e run.unclean')
        self.dc('rm', '--force', 'gateway')
        self.start()
        after = self.dc('exec', '-T', 'gateway', 'sha256sum', 'admin-bearer', 'master-key', 'http-ca.pem')[1]
        require(before == after, 'replacement changed persistent authority')
        # Readiness admits control requests before optional runtimes settle.
        # Do not add a backup writer to the reconstruction being qualified.
        self.wait_server(self.servers[2])
        current, _ = self.api('POST', '/api/v2/backups', {}, 201, {'Idempotency-Key': 'after-replacement'})
        require(current['installation_id'] == backup['installation_id'], 'replacement changed installation identity')
        require(self.fixture_get('/stats').get('drain-started', 0) == 1, 'replacement replayed uncertain call')
        self.call('https.echo')
        require(self.proxy_request('https')[0] == 200, 'replacement lost encrypted HTTP credential or CA')
        parsed = urllib.parse.urlsplit(self.pending_callback)
        require(request(self.origin, 'GET', parsed.path + '?' + parsed.query, context=self.tls)[0] == 400, 'restart preserved OAuth callback state')
        path = '/api/v2/mcp/servers/' + self.oauth_id
        flow, _ = self.api('GET', path + '/oauth-flows/' + self.pending_flow)
        require(flow['state'] == 'interrupted', 'restart OAuth flow not interrupted')
        _, fields = self.api('GET', path)
        self.api('PATCH', path, {'enabled': True}, 200, {'If-Match': fields['ETag']})
        self.wait_server(self.oauth_id)
        self.call('oauth.echo')
        require(self.fixture_get('/stats')['token-exchange'] == 1, 'restart repeated token exchange')

    def maintenance(self):
        self.dc('exec', '-T', 'gateway', 'agent-gateway', 'backup', 'create', '--address', 'http://127.0.0.1:18210', timeout=60)
        code, _ = self.dc('exec', '-T', 'gateway', 'agent-gateway', 'maintenance', 'verify-and-recover-storage', '--dry-run', check=False)
        require(code != 0, 'maintenance admitted while serving')
        self.stop()
        self.dc('run', '--rm', '--no-deps', 'gateway', 'maintenance', 'verify-and-recover-storage', '--dry-run', timeout=60)
        self.shell('mv master-key master-key.held')
        self.refuses_uninitialized_or_unsafe()
        self.shell('test ! -e master-key; mv master-key.held master-key')
        # Wrong ownership is injected only into this explicitly stopped disposable volume.
        self.fault_owner('chown 0:0 master-key')
        self.refuses_uninitialized_or_unsafe()
        self.fault_owner('test "$(stat -c %u master-key)" = 0; chown 10001:10001 master-key')
        self.start()

    def fault_owner(self, script):
        require(self.docker('inspect', '--format', '{{.State.Running}}', self.container)[1].strip() == 'false', 'ownership injection requires stopped Gateway')
        # Only this stopped disposable helper gains traversal and chown. Serving
        # keeps cap_drop ALL, UID10001 and no permission-repair entrypoint.
        capabilities = 'test "$(awk \'$1 == "CapEff:" {print $2}\' /proc/self/status)" = 0000000000000003; '
        self.dc('run', '--rm', '--no-deps', '--cap-add', 'CHOWN', '--cap-add', 'DAC_OVERRIDE',
                '--user', '0:0', '--entrypoint', '/bin/sh', 'gateway', '-ec', capabilities + script)

    def forced_stop(self):
        self.docker('kill', '--signal', 'KILL', self.container)
        state = json.loads(self.docker('inspect', '--format', '{{json .State}}', self.container)[1])
        require(not state['Running'] and state['ExitCode'] == 137, 'forced stop not observed')
        def snapshot():
            _, text = self.dc('run', '--rm', '--no-deps', '--entrypoint', 'python3', 'gateway',
                              '-c', STOPPED_SNAPSHOT, '/var/lib/agent-gateway', timeout=60)
            return json.loads(text)
        before = snapshot()
        require(before['marker'], 'forced-stop marker absent')
        # A crash marker alone is not corruption: normal startup verifies integrity.
        # Stopped diagnostic inspection must neither remove it nor repair/reinitialize.
        _, text = self.dc('run', '--rm', '--no-deps', 'gateway', 'doctor', '--verify-storage', '--json', check=False)
        diagnostics = json.loads(text)
        require(any(check['name'] == 'process ownership' and check['state'] == 'stopped' for check in diagnostics['checks']), 'forced-stop ownership diagnostic absent')
        expected = 'failed' if before['control_pending'] else 'ok'
        require(any(check['name'] == 'storage inspection' and check['state'] == expected
                    for check in diagnostics['checks']), 'forced-stop storage diagnostic did not match control sidecar state')
        require(snapshot() == before, 'stopped doctor changed installation evidence')
        code, text = self.dc('run', '--rm', '--no-deps', 'gateway', 'maintenance',
                             'verify-and-recover-storage', '--dry-run', '--json', timeout=60, check=False)
        require(snapshot() == before, 'stopped dry-run changed installation evidence')
        self.report['forced_stop_inspection'] = validate_stopped_plan(
            code, text, before['control_pending'], self.report['installation_identity'])
        self.start()
        self.api('GET', '/api/v2/system-status')
        self.wait_server(self.servers[2])
        self.call('https.echo')

    def cleanup_extra(self):
        failures = []
        for relay in self.relays.values():
            try:
                relay.close()
            except (OSError, RuntimeError) as error:
                failures.append(type(error).__name__)
        self.relays.clear()
        for name in (self.peer, self.private_peer, self.caddy, self.name + '-basehash', self.name + '-derivedhash'):
            try:
                raw = self.docker('ps', '-a', '--filter', 'label=' + self.label, '--format', '{{.Names}}')[1]
                if name in raw.splitlines():
                    self.docker('rm', '-f', name)
            except (RuntimeError, subprocess.TimeoutExpired) as error:
                failures.append(type(error).__name__)
        for kind, name in (('network', self.public_network), ('image', self.derived)):
            try:
                raw = self.docker(kind, 'ls', '--filter', 'label=' + self.label, '--format', '{{.Name}}' if kind == 'network' else '{{.Repository}}:{{.Tag}}')[1]
                if name in raw.splitlines():
                    self.docker(kind, 'rm', name)
            except (RuntimeError, subprocess.TimeoutExpired) as error:
                failures.append(type(error).__name__)
        require(not failures, 'qualification cleanup unconfirmed')

    def exercise(self):
        self.report['packaging'] = 'failed'
        super().exercise()
        self.report['packaging'] = 'passed'

    def cleanup(self):
        self.report['cleanup'] = 'failed'
        try:
            super().cleanup()
            self.report['cleanup'] = 'passed'
        finally:
            target = Path(os.environ.get('GATEWAY_DOCKER_REPORT', '/tmp/gateway-docker-report.json'))
            target.write_text(json.dumps(self.report, indent=2) + '\n')
            self.temp.cleanup()
