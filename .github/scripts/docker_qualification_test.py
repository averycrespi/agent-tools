"""Adapter and real local peer-wire tests, explicitly NOT container qualification."""
import base64
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import socket
import ssl
import struct
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch
import urllib.parse

from docker_qualification import CADDY_IMAGE, CADDY_START, CASES, PUBLIC_IP, PUBLIC_SUBNET, LoopbackRelay, Qualification, read_sse_frame, request, startup_failure
from docker_smoke import ROOT, Smoke, command
from docker_smoke_test import FakeDocker

spec = importlib.util.spec_from_file_location('docker_fixture', ROOT / '.github/docker/fixture.py')
peer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(peer)


class AdapterTests(unittest.TestCase):
    def test_extension_keeps_one_init_and_uses_replacement_identity(self):
        class Extended(Smoke):
            def qualify(self, container):
                self.asserted = container
                return 'replacement-container'
        fake = FakeDocker()
        owner = Extended(fake)
        try:
            owner.exercise()
        finally:
            owner.cleanup()
        self.assertEqual(owner.asserted, 'fixture-container')
        inspect = [args for args, _ in fake.calls if args[1] == 'inspect']
        self.assertEqual(inspect[-1][-1], 'replacement-container')
        self.assertEqual(sum('init' in args for args, _ in fake.calls), 1)

    def test_case_failure_keeps_remaining_unrun_and_report_sanitized(self):
        owner = Qualification(FakeDocker())
        owner.admin = 'private-canary-admin'
        owner.token = 'private-canary-agent'
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'report.json'
            with self.assertRaisesRegex(RuntimeError, 'fixture failure'):
                owner.case(CASES[0], lambda: (_ for _ in ()).throw(RuntimeError('fixture failure')))
            with patch.dict(os.environ, {'GATEWAY_DOCKER_REPORT': str(target)}), patch.object(Smoke, 'cleanup'):
                owner.cleanup()
            report = json.loads(target.read_text())
            self.assertEqual(report['cases'][CASES[0]], 'failed')
            self.assertTrue(all(value == 'unrun' for name, value in report['cases'].items() if name != CASES[0]))
            self.assertEqual(report['cleanup'], 'passed')
            self.assertNotIn('private-canary', target.read_text())

    def test_cleanup_failure_is_retained_not_turned_into_a_pass(self):
        owner = Qualification(FakeDocker())
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'report.json'
            with patch.dict(os.environ, {'GATEWAY_DOCKER_REPORT': str(target)}), patch.object(Smoke, 'cleanup', side_effect=RuntimeError('unconfirmed')):
                with self.assertRaises(RuntimeError):
                    owner.cleanup()
            self.assertEqual(json.loads(target.read_text())['cleanup'], 'failed')

    def test_route_collision_refuses_before_docker_network_creation(self):
        calls = []
        def run(args, **_kwargs):
            calls.append(args)
            return 0, json.dumps([{'dst': PUBLIC_SUBNET}])
        owner = Qualification(run)
        try:
            with self.assertRaisesRegex(RuntimeError, 'overlaps host route'):
                owner.prepare_network()
            self.assertEqual(len(calls), 1)
        finally:
            owner.temp.cleanup()

    def test_prepare_uses_isolated_networks_public_cert_only_and_same_binary(self):
        calls = []
        def run(args, **kwargs):
            calls.append(args)
            if args[0] == 'openssl':
                return command(args, **kwargs)
            if args[0] == 'ip':
                return 0, '[{"dst":"default"},{"dst":"172.20.0.0/16"}]'
            if args[1:3] == ['network', 'inspect']:
                return 0, 'true\n'
            if args[1:4] == ['inspect', '--format', '{{json .}}']:
                return 0, json.dumps({'State': {'Running': True}, 'NetworkSettings': {
                    'Ports': {}, 'Networks': {owner.network: {'IPAddress': '172.20.0.2'}}}})
            return 0, 'sha256:fixture-image\n'
        owner = Qualification(run)
        try:
            owner.prepare()
            config = json.loads((owner.files / 'override.json').read_text())
            gateway = config['services']['gateway']
            self.assertEqual(gateway['dns'], [PUBLIC_IP])
            self.assertEqual(gateway['environment'], {'SSL_CERT_FILE': '/fixture-ca.pem'})
            self.assertEqual(len(gateway['volumes']), 1)
            self.assertTrue(gateway['volumes'][0].endswith('/cert.pem:/fixture-ca.pem:ro'))
            self.assertNotIn('key.pem', str(gateway))
            dockerfile = (owner.files / 'build/Dockerfile').read_text()
            self.assertIn('FROM ' + owner.image, dockerfile)
            self.assertNotIn('go build', dockerfile)
            self.assertIn('git-daemon', dockerfile)
            self.assertTrue(any(args[1:4] == ['network', 'create', '--internal'] for args in calls))
            self.assertFalse(any('push' in args or '--privileged' in args or '--network=host' in args for args in calls))
            self.assertEqual(owner.report['cases']['image-network-isolation'], 'passed')
            for text in ((owner.files / 'config/Caddyfile').read_text(),
                         (ROOT / 'agent-gateway/docs/operators/docker.md').read_text()):
                self.assertIn('header_up Host {http.request.hostport}', text)
                self.assertNotIn('header_up Host {http.request.host}', text)
                self.assertIn('header_up -X-Forwarded-*', text)
            caddy = next(args for args in calls if CADDY_IMAGE in args)
            self.assertEqual(caddy[-2:], ['-ec', CADDY_START])
            self.assertIn('0000000000000000', CADDY_START)
            self.assertIn('/tmp:exec,mode=0700,size=128m', caddy)
            self.assertNotIn('--cap-add', caddy)
            self.assertIn('--cap-drop', caddy)
            self.assertIn('no-new-privileges:true', caddy)
            self.assertFalse(any('-p' in args for args in calls), 'internal-only peers cannot publish ports')
            self.assertEqual(owner.port(owner.private_peer, '9443/tcp'), owner.proxy_port)
            self.assertEqual(len(owner.relays), 2)
            self.assertTrue(all(relay.server.server_address[0] == '127.0.0.1' for relay in owner.relays.values()))
        finally:
            for relay in owner.relays.values():
                relay.close()
            owner.temp.cleanup()

    @unittest.skipUnless(sys.platform.startswith('linux'), 'Linux fixture inode metadata')
    def test_caddy_launch_copies_bytes_without_extended_attributes(self):
        with tempfile.TemporaryDirectory() as directory:
            source, target = Path(directory) / 'source', Path(directory) / 'target'
            source.write_text('#!/bin/sh\n[ "$1" = run ] && [ "$2" = --config ]\n')
            source.chmod(0o755)
            os.setxattr(source, 'user.fixture-canary', b'not-to-be-inherited')
            # Exercise the byte-copy/exec seam without constraining the test host's caps.
            program = CADDY_START.partition('; ')[2].replace('/usr/bin/caddy', str(source)).replace('/tmp/caddy', str(target))
            result = subprocess.run(['/bin/sh', '-ec', program], capture_output=True, timeout=5, check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(source.read_bytes(), target.read_bytes())
            self.assertIn('user.fixture-canary', os.listxattr(source))
            self.assertEqual(os.listxattr(target), [])
            self.assertEqual(target.stat().st_mode & 0o777, 0o700)

    def test_ingress_refuses_exited_peer_before_creating_listener(self):
        owner = Qualification(lambda *_args, **_kwargs: (0, '{"State":{"Running":false}}'))
        try:
            with self.assertRaisesRegex(RuntimeError, 'exited before relay'):
                owner.port(owner.private_peer, '9443/tcp')
            self.assertFalse(owner.relays)
        finally:
            owner.temp.cleanup()

    def test_startup_failure_never_exposes_runtime_output(self):
        for message, expected in (('unknown command', 'cli-command'), ('operation not permitted', 'capability'),
                                  ('permission denied', 'permissions'), ('read-only file system', 'read-only-filesystem'),
                                  ('no such file or directory', 'missing-file'), ('opaque error', 'unclassified')):
            with self.subTest(message=message):
                failure = startup_failure({'ExitCode': 1, 'Error': ''},
                                          'private-canary-token /fixture-config/key.pem ' + message)
                self.assertEqual(failure, {'exit_code': 1, 'reason': expected, 'path': 'private-key'})
                self.assertNotIn('private-canary', json.dumps(failure))
        failure = startup_failure({'ExitCode': 126, 'Error': 'private-canary: operation not permitted'}, '')
        self.assertEqual(failure['reason'], 'capability')
        self.assertNotIn('private-canary', json.dumps(failure))

    def test_relay_cleanup_closes_listener_and_active_stream(self):
        accepted, closed = threading.Event(), threading.Event()
        server = socket.socket()
        server.bind(('127.0.0.1', 0))
        server.listen()
        server.settimeout(5)
        def upstream():
            with server.accept()[0] as conn:
                conn.settimeout(5)
                conn.sendall(b'frame\n\n')
                accepted.set()
                if conn.recv(1) == b'':
                    closed.set()
        thread = threading.Thread(target=upstream)
        thread.start()
        relay = LoopbackRelay(server.getsockname())
        try:
            with socket.create_connection(('127.0.0.1', relay.port), timeout=5) as client:
                self.assertTrue(accepted.wait(5))
                self.assertEqual(client.recv(7), b'frame\n\n')
                relay.close()
                self.assertEqual(client.recv(1), b'')
                self.assertTrue(closed.wait(5))
            with self.assertRaises(OSError):
                socket.create_connection(('127.0.0.1', relay.port), timeout=1)
        finally:
            relay.close()
            server.close()
            thread.join(timeout=5)
            self.assertFalse(thread.is_alive())

    def test_stopped_owner_fault_helper_has_only_required_effective_capabilities(self):
        calls = []
        def run(args, **_kwargs):
            calls.append(args)
            return 0, 'false\n'
        owner = Qualification(run)
        owner.container = 'owned-gateway'
        try:
            owner.fault_owner('chown 0:0 master-key')
            self.assertEqual(calls[0][-2:], ['{{.State.Running}}', 'owned-gateway'])
            helper = calls[1]
            self.assertEqual([helper[i + 1] for i, value in enumerate(helper) if value == '--cap-add'], ['CHOWN', 'DAC_OVERRIDE'])
            self.assertIn('0000000000000003', helper[-1])
            self.assertNotIn('--privileged', helper)
            compose = (ROOT / 'agent-gateway/examples/docker/compose.yaml').read_text()
            self.assertIn('cap_drop:\n      - ALL', compose)
            self.assertNotIn('cap_add:', compose)
            with patch.object(owner, 'docker', return_value=(0, 'true\n')):
                with self.assertRaisesRegex(RuntimeError, 'requires stopped'):
                    owner.fault_owner('chown 0:0 master-key')
            self.assertEqual(len(calls), 2, 'running refusal must not dispatch helper')
        finally:
            owner.temp.cleanup()

    def test_ingress_stream_requires_body_frame_not_just_headers(self):
        release = threading.Event()
        class Stream(peer.Peer):
            def do_GET(self):
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.end_headers()
                if self.path == '/flushed':
                    self.wfile.write(b': ok\n\n')
                    self.wfile.flush()
                release.wait(5)
                self.close_connection = True
        server = peer.http.server.ThreadingHTTPServer(('127.0.0.1', 0), Stream)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            for path, succeeds in (('/flushed', True), ('/headers-only', False)):
                conn = http.client.HTTPConnection(*server.server_address, timeout=0.15)
                try:
                    conn.request('GET', path)
                    response = conn.getresponse()
                    self.assertEqual(response.status, 200)
                    if succeeds:
                        self.assertEqual(read_sse_frame(response), b': ok\n\n')
                    else:
                        with self.assertRaises(TimeoutError):
                            read_sse_frame(response)
                finally:
                    conn.close()
        finally:
            release.set()
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
            self.assertFalse(thread.is_alive())

    def test_dns_answer_contains_both_classes_and_never_recurses(self):
        def query(name, kind=1):
            labels = b''.join(bytes([len(part)]) + part.encode() for part in name.split('.')) + b'\x00'
            return b'\x12\x34' + struct.pack('!HHHHH', 0x0100, 1, 0, 0, 0) + labels + struct.pack('!HH', kind, 1)
        response = peer.dns_packet(query('mixed.test'))
        self.assertEqual(struct.unpack('!H', response[6:8])[0], 2)
        self.assertIn(socket.inet_aton(PUBLIC_IP), response)
        self.assertIn(socket.inet_aton('127.0.0.1'), response)
        self.assertEqual(struct.unpack('!H', peer.dns_packet(query('outside.example'))[6:8])[0], 0)
        self.assertEqual(struct.unpack('!H', peer.dns_packet(query('mixed.test', 28))[6:8])[0], 0)
        self.assertIsNone(peer.dns_packet(b'bad'))

    def test_private_runner_has_deadline_and_does_not_echo_secrets(self):
        owner = Qualification(FakeDocker())
        try:
            with self.assertRaises(subprocess.TimeoutExpired):
                owner.input_command([sys.executable, '-c', 'import time; time.sleep(10)'], b'', timeout=0.05)
            with self.assertRaisesRegex(RuntimeError, 'output withheld') as result:
                owner.input_command([sys.executable, '-c', 'import sys; print(sys.stdin.read()); sys.exit(3)'], b'private-test-value')
            self.assertNotIn('private-test-value', str(result.exception))
        finally:
            owner.temp.cleanup()

    def test_runtime_gate_uses_one_owner_and_keeps_docker_out_of_local_aggregate(self):
        makefile = (ROOT / 'Makefile').read_text()
        self.assertIn('test-docker:\n\tpython3 -B .github/scripts/docker_smoke.py --qualify', makefile)
        self.assertNotIn('test-docker', makefile.split('check:')[1].split('\n')[0])
        workflow = (ROOT / '.github/workflows/ci.yml').read_text()
        job = workflow.split('  gateway-docker:')[1].split('  gateway-lint:')[0]
        self.assertEqual(job.count('make test-docker'), 1)
        self.assertIn('if: always()', job)
        self.assertNotIn('continue-on-error', job)
        self.assertIn('if-no-files-found: error', job)


class WirePeerTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix='docker-peer-unit-')
        self.root = Path(self.directory.name)
        self.old_root, self.old_config = peer.ROOT, peer.CONFIG
        peer.ROOT = self.root
        peer.CONFIG = self.root
        (self.root / 'origin').write_text('https://gateway.example:9443')
        with peer.LOCK:
            peer.COUNTS.clear()
            peer.CODES.clear()
        self.server = peer.http.server.ThreadingHTTPServer(('127.0.0.1', 0), peer.Peer)
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        self.assertFalse(self.thread.is_alive())
        peer.ROOT, peer.CONFIG = self.old_root, self.old_config
        self.directory.cleanup()

    def exchange(self, method, path, data=None, headers=None):
        conn = http.client.HTTPConnection(*self.server.server_address, timeout=5)
        try:
            conn.request(method, path, data, headers or {})
            response = conn.getresponse()
            return response.status, dict(response.getheaders()), response.read(1024 * 1024)
        finally:
            conn.close()

    def test_https_client_checks_exact_fixture_trust(self):
        cert, key = self.root / 'cert.pem', self.root / 'key.pem'
        command(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                 '-keyout', str(key), '-out', str(cert), '-subj', '/CN=localhost',
                 '-addext', 'subjectAltName=DNS:localhost'], env=os.environ, timeout=10)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(cert, key)
        server = peer.http.server.ThreadingHTTPServer(('127.0.0.1', 0), peer.Peer)
        server.socket = tls.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        relay = LoopbackRelay(server.server_address)
        try:
            for port in (server.server_port, relay.port):
                origin = 'https://localhost:' + str(port)
                self.assertEqual(request(origin, 'GET', '/stats', context=ssl.create_default_context(cafile=str(cert)))[0], 200)
                with self.assertRaises(ssl.SSLCertVerificationError):
                    request(origin, 'GET', '/stats', context=ssl.create_default_context())
        finally:
            relay.close()
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)
            self.assertFalse(thread.is_alive())

    def test_mcp_wire_and_sse_are_observable_not_configured_success(self):
        for method in ('server/discover', 'tools/list', 'tools/call'):
            status, _, body = self.exchange('POST', '/mcp', json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': method}))
            self.assertEqual(status, 200)
            self.assertIn('result', json.loads(body))
        status, headers, body = self.exchange('POST', '/stream', json.dumps({'jsonrpc': '2.0', 'id': 2, 'method': 'tools/call'}))
        self.assertEqual(status, 200)
        self.assertEqual(headers['Content-Type'], 'text/event-stream')
        # Gateway consumes the first SSE event: no empty comment event precedes data.
        first = body.split(b'\n\n', 1)[0]
        data = next(line[6:] for line in first.splitlines() if line.startswith(b'data: '))
        self.assertEqual(json.loads(data)['id'], 2)
        self.assertEqual(peer.COUNTS['tools/call'], 2)

    def test_oauth_checks_callback_resource_pkce_and_single_exchange(self):
        callback = 'https://gateway.example:9443/oauth/callback'
        status, _, _ = self.exchange('POST', '/register', json.dumps({'redirect_uris': [callback], 'application_type': 'web'}))
        self.assertEqual(status, 201)
        self.assertEqual(self.exchange('POST', '/register', json.dumps({'redirect_uris': ['https://wrong.example'], 'application_type': 'web'}))[0], 400)
        verifier = 'synthetic-verifier-for-wire-test'
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip('=')
        query = urllib.parse.urlencode({'redirect_uri': callback, 'resource': peer.ISSUER + '/oauth', 'client_id': 'docker-fixture', 'response_type': 'code', 'code_challenge_method': 'S256', 'state': 'synthetic-state', 'code_challenge': challenge})
        status, headers, _ = self.exchange('GET', '/authorize?' + query)
        self.assertEqual(status, 302)
        redirect = urllib.parse.urlsplit(headers['Location'])
        code = urllib.parse.parse_qs(redirect.query)['code'][0]
        form = urllib.parse.urlencode({'code': code, 'code_verifier': verifier, 'redirect_uri': callback, 'resource': peer.ISSUER + '/oauth', 'client_id': 'docker-fixture', 'grant_type': 'authorization_code'})
        status, _, body = self.exchange('POST', '/token', form)
        self.assertEqual(status, 200)
        token = json.loads(body)['access_token']
        self.assertEqual(self.exchange('POST', '/token', form)[0], 400)
        self.assertEqual(peer.COUNTS['token-exchange'], 1)
        rpc = json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call'})
        self.assertEqual(self.exchange('POST', '/oauth', rpc)[0], 401)
        self.assertEqual(self.exchange('POST', '/oauth', rpc, {'Authorization': 'Bearer ' + token})[0], 200)

    def test_drain_peer_waits_for_explicit_release(self):
        outcomes = []
        def call():
            try:
                outcomes.append(self.exchange('POST', '/mcp', json.dumps({'jsonrpc': '2.0', 'id': 1,
                    'method': 'tools/call', 'params': {'arguments': {'drain': True}}})))
            except Exception as error:
                outcomes.append(error)
        worker = threading.Thread(target=call)
        worker.start()
        try:
            deadline = time.monotonic() + 3
            while json.loads(self.exchange('GET', '/stats')[2]).get('drain-started', 0) != 1:
                self.assertLess(time.monotonic(), deadline, 'peer did not enter barrier')
                time.sleep(0.01)
            self.assertTrue(worker.is_alive())
            self.assertEqual(json.loads(self.exchange('GET', '/stats')[2]).get('drain-completed', 0), 0)
        finally:
            (self.root / 'drain-release').touch()
            worker.join(timeout=6)
        self.assertFalse(worker.is_alive())
        self.assertEqual(len(outcomes), 1)
        self.assertIsInstance(outcomes[0], tuple)
        self.assertEqual(outcomes[0][0], 200)
        self.assertIn(b'container fixture success', outcomes[0][2])
        self.assertEqual(json.loads(self.exchange('GET', '/stats')[2]),
                         {'tools/call': 1, 'drain-started': 1, 'drain-completed': 1})

    def test_cleartext_peer_refuses_credentials(self):
        status, _, body = self.exchange('GET', '/plain')
        self.assertEqual((status, body), (200, b'plain'))
        for name in ('X-Fixture-Key', 'Authorization', 'Proxy-Authorization'):
            for value in ('synthetic-fixture-value', ''):
                with self.subTest(header=name, value=value):
                    self.assertEqual(self.exchange('GET', '/plain', headers={name: value})[0], 403)

    def test_injection_role_boundary_and_uncertain_wire_closes_once(self):
        self.assertEqual(self.exchange('GET', '/injected')[0], 403)
        self.assertEqual(self.exchange('GET', '/injected', headers={'X-Fixture-Key': 'synthetic-http-secret'})[0], 200)
        self.assertEqual(self.exchange('GET', '/injected', headers={'X-Fixture-Key': 'synthetic-http-secret', 'Proxy-Authorization': 'must-not-forward'})[0], 403)
        with self.assertRaises(http.client.RemoteDisconnected):
            self.exchange('POST', '/uncertain')
        self.assertEqual(peer.COUNTS['uncertain'], 1)

    def test_real_native_git_peer_supports_push_clone_and_ref_observation(self):
        env = {'PATH': os.environ['PATH'], 'HOME': str(self.root), 'GIT_CONFIG_NOSYSTEM': '1',
               'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_TERMINAL_PROMPT': '0'}
        def git(*args):
            result = subprocess.run(['git', '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                                     '-c', 'http.extraHeader=Authorization: Bearer synthetic-git-secret', *args],
                                    env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
            self.assertEqual(result.returncode, 0, 'native peer fixture command failed')
            return result.stdout.decode()
        git('init', '--bare', str(self.root / 'repo.git'))
        git('--git-dir=' + str(self.root / 'repo.git'), 'config', 'http.receivepack', 'true')
        git('init', str(self.root / 'seed'))
        git('-C', str(self.root / 'seed'), 'commit', '--allow-empty', '-m', 'fixture')
        url = 'http://127.0.0.1:' + str(self.server.server_port) + '/repo.git'
        git('-C', str(self.root / 'seed'), 'push', url, 'HEAD:refs/heads/allowed')
        self.assertIn('refs/heads/allowed', git('ls-remote', url))
        git('clone', '--branch', 'allowed', url, str(self.root / 'clone'))
        self.assertEqual(git('-C', str(self.root / 'seed'), 'rev-parse', 'HEAD'), git('-C', str(self.root / 'clone'), 'rev-parse', 'HEAD'))


if __name__ == '__main__':
    unittest.main()
