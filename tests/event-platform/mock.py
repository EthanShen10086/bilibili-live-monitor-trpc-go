"""Disposable HTTPS Bilibili/Feishu/alert receiver. Never used by production compose."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ssl
from threading import Lock
import time
from urllib.parse import parse_qs, urlparse

lock = Lock()
state = {'live': 0, 'start': int(time.time()), 'probes': 0, 'accepted': [], 'alerts': [], 'fail_group': 0}
class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass
    def send(self, value, status=200):
        payload = json.dumps(value).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)
    def do_GET(self):
        with lock:
            if self.path == '/test/state':
                return self.send(state)
            if self.path.startswith('/room/v1/Room/get_info'):
                room = int(parse_qs(urlparse(self.path).query)['room_id'][0])
                state['probes'] += 1
                return self.send({'code': 0, 'data': {'room_id': room, 'live_status': state['live'], 'title': 'Integration stream', 'live_time': state['start']}})
        self.send({'error': 'not found'}, 404)
    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))) or b'{}')
        with lock:
            if self.path == '/test/state':
                for key in ('live', 'start', 'fail_group'):
                    if key in data:
                        state[key] = data[key]
                return self.send({'updated': True})
            if self.path == '/test/alerts':
                state['alerts'].append(data)
                return self.send({'accepted': True})
            if self.path.startswith('/open-apis/auth/'):
                return self.send({'code': 0, 'tenant_access_token': 'disposable-mock-token', 'expire': 3600})
            if self.path.startswith('/open-apis/bot/') and state['fail_group']:
                state['fail_group'] -= 1
                return self.send({'code': 999})
            if self.path.startswith('/open-apis/'):
                state['accepted'].append({'path': self.path, 'message': data})
                return self.send({'code': 0})
        self.send({'error': 'not found'}, 404)

server = ThreadingHTTPServer(('0.0.0.0', 443), Handler)
context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain('/certs/tls.crt', '/certs/tls.key')
server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
