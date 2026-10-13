"""Disposable wire peers, never imported by or mounted over Gateway product code.

Only synthetic credentials exist here. No access log, request dump or automatic retry.
The same script is a derived-image stdio executable and separate-container HTTP peer.
"""
import base64
import hashlib
import http.client
import http.server
import json
import os
from pathlib import Path
import signal
import socket
import ssl
import struct
import subprocess
import sys
import threading
import time
import urllib.parse

ROOT = Path('/fixture-state')
CONFIG = Path('/fixture-config')
LOCK = threading.Lock()
COUNTS = {}
CODES = {}
ISSUER = 'https://external.test:8443'


def count(key):
    with LOCK:
        COUNTS[key] = COUNTS.get(key, 0) + 1


def rpc(request):
    method = request.get('method')
    count(method)
    if method == 'server/discover':
        result = {'ttlMs': 0, 'cacheScope': 'public', 'supportedVersions': ['2026-07-28'], 'capabilities': {}}
    elif method == 'tools/list':
        result = {'tools': [{'name': 'echo', 'inputSchema': {'type': 'object'}, 'annotations': {'readOnlyHint': True}}]}
    elif method == 'tools/call':
        if request.get('params', {}).get('arguments', {}).get('drain') is True:
            count('drain-started')
            deadline = time.monotonic() + 45
            while not (ROOT / 'drain-release').exists():
                if time.monotonic() >= deadline:
                    raise RuntimeError('drain fixture release deadline')
                time.sleep(0.01)
            count('drain-completed')
        result = {'content': [{'type': 'text', 'text': 'container fixture success'}]}
    else:
        return {'jsonrpc': '2.0', 'id': request.get('id'), 'error': {'code': -32601, 'message': 'Unsupported'}}
    return {'jsonrpc': '2.0', 'id': request['id'], 'result': result}


def stdio():
    marker = Path('/var/lib/agent-gateway/stdio-stopped')
    def stop(_sig, _frame):
        marker.write_text('terminated\n')
        raise SystemExit(0)
    signal.signal(signal.SIGTERM, stop)
    Path('/var/lib/agent-gateway/stdio-pid').write_text(str(os.getpid()))
    for line in sys.stdin:
        if len(line) > 65536:
            raise SystemExit(2)
        request = json.loads(line)
        print(json.dumps(rpc(request)), flush=True)
    stop(None, None)


class Peer(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *_args):
        pass

    def handle_one_request(self):
        self.connection.settimeout(15)
        super().handle_one_request()

    def reply(self, status, value=b'', headers=None):
        if not isinstance(value, bytes):
            value = json.dumps(value).encode()
        self.send_response(status)
        for key, val in (headers or {}).items():
            self.send_header(key, val)
        self.send_header('Content-Length', str(len(value)))
        if 'Content-Type' not in (headers or {}):
            self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(value)

    def body(self):
        if self.headers.get('Transfer-Encoding', '').lower() == 'chunked':
            chunks = bytearray()
            while True:
                line = self.rfile.readline(128)
                length = int(line.split(b';', 1)[0], 16)
                if length == 0:
                    if self.rfile.readline(128) != b'\r\n':
                        raise ValueError('unexpected trailers')
                    return bytes(chunks)
                if len(chunks) + length > 2 * 1024 * 1024:
                    raise ValueError('body limit')
                chunks.extend(self.rfile.read(length))
                if self.rfile.read(2) != b'\r\n':
                    raise ValueError('chunk framing')
        length = int(self.headers.get('Content-Length', '0'))
        if length > 2 * 1024 * 1024:
            raise ValueError('body limit')
        return self.rfile.read(length)

    def do_GET(self):
        path = urllib.parse.urlsplit(self.path)
        if path.path == '/stats':
            with LOCK:
                snapshot = dict(COUNTS)
            return self.reply(200, snapshot)
        if path.path.startswith('/repo.git/'):
            return self.git(path)
        if path.path == '/.well-known/oauth-protected-resource/oauth':
            return self.reply(200, {'resource': ISSUER + '/oauth', 'authorization_servers': [ISSUER]})
        if path.path == '/.well-known/oauth-authorization-server':
            return self.reply(200, {'issuer': ISSUER, 'authorization_endpoint': ISSUER + '/authorize', 'token_endpoint': ISSUER + '/token', 'registration_endpoint': ISSUER + '/register', 'response_types_supported': ['code'], 'grant_types_supported': ['authorization_code', 'refresh_token'], 'code_challenge_methods_supported': ['S256'], 'token_endpoint_auth_methods_supported': ['none'], 'authorization_response_iss_parameter_supported': True})
        if path.path == '/authorize':
            query = urllib.parse.parse_qs(path.query)
            get = lambda name: query.get(name, [''])[0]
            callback = (CONFIG / 'origin').read_text().strip() + '/oauth/callback'
            if (get('redirect_uri') != callback or get('resource') != ISSUER + '/oauth' or get('client_id') != 'docker-fixture' or get('response_type') != 'code' or get('code_challenge_method') != 'S256' or not get('state') or not get('code_challenge')):
                return self.reply(400)
            code = 'synthetic-code-' + get('state')
            with LOCK:
                CODES[code] = get('code_challenge')
            location = callback + '?' + urllib.parse.urlencode({'state': get('state'), 'code': code, 'iss': ISSUER})
            return self.reply(302, headers={'Location': location})
        if path.path == '/plain':
            if any(name in self.headers for name in ('X-Fixture-Key', 'Authorization', 'Proxy-Authorization')):
                return self.reply(403)
            return self.reply(200, b'plain')
        if path.path == '/injected':
            count('http-injected')
            if self.headers.get('X-Fixture-Key') != 'synthetic-http-secret' or self.headers.get('Proxy-Authorization'):
                return self.reply(403)
            return self.reply(200, b'injected')
        return self.reply(404)

    def do_POST(self):
        path = urllib.parse.urlsplit(self.path)
        if path.path.startswith('/repo.git/'):
            return self.git(path)
        body = self.body()
        if path.path == '/uncertain':
            count('uncertain')
            self.connection.shutdown(socket.SHUT_RDWR)
            self.close_connection = True
            return
        if path.path == '/register':
            value = json.loads(body)
            callback = (CONFIG / 'origin').read_text().strip() + '/oauth/callback'
            if value.get('redirect_uris') != [callback] or value.get('application_type') != 'web':
                return self.reply(400)
            return self.reply(201, {'client_id': 'docker-fixture', 'redirect_uris': [callback], 'response_types': ['code'], 'grant_types': ['authorization_code', 'refresh_token'], 'token_endpoint_auth_method': 'none'})
        if path.path == '/token':
            value = urllib.parse.parse_qs(body.decode())
            get = lambda name: value.get(name, [''])[0]
            challenge = base64.urlsafe_b64encode(hashlib.sha256(get('code_verifier').encode()).digest()).decode().rstrip('=')
            with LOCK:
                expected = CODES.pop(get('code'), None)
            callback = (CONFIG / 'origin').read_text().strip() + '/oauth/callback'
            if expected is None or challenge != expected or get('redirect_uri') != callback or get('resource') != ISSUER + '/oauth' or get('client_id') != 'docker-fixture' or get('grant_type') != 'authorization_code':
                return self.reply(400)
            count('token-exchange')
            return self.reply(200, {'access_token': 'synthetic-oauth-access', 'refresh_token': 'synthetic-oauth-refresh', 'token_type': 'Bearer', 'expires_in': 3600})
        if path.path in ('/mcp', '/oauth', '/stream'):
            if path.path == '/oauth' and self.headers.get('Authorization') != 'Bearer synthetic-oauth-access':
                return self.reply(401)
            request = json.loads(body)
            result = rpc(request)
            if path.path == '/stream' and request.get('method') == 'tools/call':
                # A real downstream SSE response, flushed before completion.
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.send_header('Connection', 'close')
                self.end_headers()
                self.wfile.write(b': fixture stream\n')
                self.wfile.flush()
                time.sleep(0.2)
                self.wfile.write(b'event: message\ndata: ' + json.dumps(result).encode() + b'\n\n')
                self.wfile.flush()
                self.close_connection = True
                return
            return self.reply(200, result)
        return self.reply(404)

    def git(self, path):
        if self.headers.get('Authorization') != 'Bearer synthetic-git-secret' or self.headers.get('Proxy-Authorization'):
            return self.reply(403)
        count('git-' + path.path.rsplit('/', 1)[-1])
        body = self.body()
        env = {**os.environ, 'GIT_PROJECT_ROOT': str(ROOT), 'GIT_HTTP_EXPORT_ALL': '1', 'REQUEST_METHOD': self.command, 'PATH_INFO': path.path, 'QUERY_STRING': path.query, 'CONTENT_TYPE': self.headers.get('Content-Type', ''), 'CONTENT_LENGTH': str(len(body)), 'REMOTE_USER': 'fixture', 'HTTP_GIT_PROTOCOL': self.headers.get('Git-Protocol', '')}
        result = subprocess.run(['git', 'http-backend'], input=body, capture_output=True, env=env, timeout=10, check=True)
        head, body = result.stdout.split(b'\r\n\r\n', 1)
        headers = {}
        status = 200
        for line in head.decode().split('\r\n'):
            key, value = line.split(':', 1)
            if key.lower() == 'status':
                status = int(value.strip().split()[0])
            else:
                headers[key] = value.strip()
        return self.reply(status, body, headers)


class Proxy(Peer):
    """Generic no-retry TLS reverse proxy, deliberately no vendor header authority."""
    def forward(self):
        headers = {key: value for key, value in self.headers.items() if key.lower() != 'forwarded' and not key.lower().startswith('x-forwarded-') and key.lower() not in ('connection', 'transfer-encoding')}
        conn = http.client.HTTPConnection('gateway', 18210, timeout=15)
        try:
            conn.request(self.command, self.path, body=self.body(), headers=headers)
            response = conn.getresponse()
            self.send_response(response.status)
            for key, value in response.getheaders():
                if key.lower() not in ('connection', 'transfer-encoding'):
                    self.send_header(key, value)
            self.send_header('Connection', 'close')
            self.end_headers()
            while chunk := response.read1(16384):
                self.wfile.write(chunk)
                self.wfile.flush()
        finally:
            conn.close()
            self.close_connection = True

    do_GET = forward
    do_POST = forward
    do_PATCH = forward
    do_DELETE = forward


def dns_packet(data):
    if len(data) < 17 or data[4:6] != b'\x00\x01':
        return None
    offset, labels = 12, []
    while offset < len(data) and data[offset]:
        length = data[offset]
        if length > 63 or offset + 1 + length >= len(data):
            return None
        labels.append(data[offset + 1:offset + 1 + length].decode('ascii', 'replace'))
        offset += length + 1
    end = offset + 5
    if end > len(data):
        return None
    name = '.'.join(labels)
    kind = struct.unpack('!H', data[offset + 1:offset + 3])[0]
    addresses = ['11.254.139.2', '127.0.0.1'] if name == 'mixed.test' and kind == 1 else []
    answer = b''.join(b'\xc0\x0c' + struct.pack('!HHIH', 1, 1, 0, 4) + socket.inet_aton(ip) for ip in addresses)
    return data[:2] + struct.pack('!HHHHH', 0x8180, 1, len(addresses), 0, 0) + data[12:end] + answer


def serve_dns():
    """Closed nonrecursive DNS fixture; no query is ever forwarded off-network."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(('0.0.0.0', 53))
    while True:
        data, address = sock.recvfrom(512)
        packet = dns_packet(data)
        if packet is not None:
            sock.sendto(packet, address)


def serve():
    ROOT.mkdir(exist_ok=True)
    for args in (['git', 'init', '--bare', str(ROOT / 'repo.git')], ['git', '--git-dir=' + str(ROOT / 'repo.git'), 'config', 'http.receivepack', 'true']):
        subprocess.run(args, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10, check=True)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain('/fixture-config/cert.pem', '/fixture-config/key.pem')
    servers = []
    for port, handler, encrypted in ((8080, Peer, False), (8443, Peer, True), (9443, Proxy, True)):
        server = http.server.ThreadingHTTPServer(('0.0.0.0', port), handler)
        if encrypted:
            server.socket = tls.wrap_socket(server.socket, server_side=True)
        servers.append(server)
        threading.Thread(target=server.serve_forever, daemon=True).start()
    threading.Thread(target=serve_dns, daemon=True).start()
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    signal.pause()


if __name__ == '__main__':
    if sys.argv[1:] == ['stdio']:
        stdio()
    else:
        serve()
