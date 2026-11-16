#!/usr/bin/env python3
"""人脸打码微服务：给画布后端提供「参考视频主体打码」能力。

画布后端跑在容器里（没有 python/opencv），本服务跑在宿主机上，
后端通过 http://172.23.0.1:18091 调用。容器内路径 /data/xxx
对应宿主机 /opt/open-ai-canvas/data/backend/xxx。
"""
import json
import os
import sys
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import redact  # noqa: E402

CONTAINER_PREFIX = '/data/'
HOST_PREFIX = '/opt/open-ai-canvas/data/backend/'
LOCK = threading.Lock()
VERSION = '1.0.0'


def to_host(path):
    if path.startswith(CONTAINER_PREFIX):
        return HOST_PREFIX + path[len(CONTAINER_PREFIX):]
    return path


def to_container(path):
    if path.startswith(HOST_PREFIX):
        return CONTAINER_PREFIX + path[len(HOST_PREFIX):]
    return path


class Handler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    server_version = 'faceguard/' + VERSION

    def log_message(self, fmt, *args):
        sys.stderr.write('%s %s\n' % (time.strftime('%F %T'), fmt % args))

    def _send(self, code, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(code)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.split('?')[0] in ('/health', '/healthz'):
            return self._send(200, {'ok': True, 'version': VERSION, 'model': os.path.basename(redact.MODEL)})
        return self._send(404, {'ok': False, 'error': 'not found'})

    def do_POST(self):
        if self.path.split('?')[0] != '/redact':
            return self._send(404, {'ok': False, 'error': 'not found'})
        try:
            length = int(self.headers.get('Content-Length') or 0)
            payload = json.loads(self.rfile.read(length) or b'{}')
        except Exception as exc:
            return self._send(400, {'ok': False, 'error': 'bad json: %s' % exc})

        source = to_host(str(payload.get('input') or ''))
        if not source or not os.path.exists(source):
            return self._send(400, {'ok': False, 'error': 'input not found: %s' % source})

        target = payload.get('output')
        if target:
            target = to_host(str(target))
        else:
            base = os.path.splitext(source)[0]
            target = base + '.redacted.mp4'

        options = {
            'mode': payload.get('mode') or 'subject',
            'max_faces': int(payload.get('maxFaces') or 1),
            'sample': int(payload.get('sample') or 2),
            'pad': float(payload.get('pad') or 0.22),
            'blocks': int(payload.get('blocks') or 9),
        }
        started = time.time()
        try:
            with LOCK:
                stats = redact.redact_video(source, target, **options)
        except Exception as exc:
            traceback.print_exc()
            return self._send(500, {'ok': False, 'error': str(exc)})
        elapsed = int((time.time() - started) * 1000)
        self._send(200, {
            'ok': True,
            'input': to_container(source),
            'output': to_container(target),
            'stats': stats,
            'ms': elapsed,
        })


def main():
    port = int(os.environ.get('FACEGUARD_PORT', '18091'))
    host = os.environ.get('FACEGUARD_BIND', '172.19.0.1')
    server = ThreadingHTTPServer((host, port), Handler)
    sys.stderr.write('faceguard listening on %s:%d\n' % (host, port))
    server.serve_forever()


if __name__ == '__main__':
    main()
