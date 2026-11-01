"""A local HTTPS stand-in for the Gateway's /agent/* entry, shared by the install script tests.
Run directly it serves a directory for the PowerShell tests: python3 platform_stub.py CERT KEY DIR GOOS ARCH LOGFILE [VERSION [REDEEM_STATUS]]
供安装脚本测试共用的 Gateway /agent/* 入口本地 HTTPS 替身。直接运行时为 PowerShell 测试提供一个目录。
"""
import hashlib
import http.server
import json
from pathlib import Path
import ssl
import sys
import threading

VERSION = 'v0.99.1'
CODE = 'ABCD-EFGH-JKMN-PQRS-TVWX'
TOKEN = 'secret-fixture-token'
CA_PEM = '-----BEGIN CERTIFICATE-----\nZml4dHVyZQ==\n-----END CERTIFICATE-----\n'
CA_SHA = hashlib.sha256(CA_PEM.encode()).hexdigest()


class Platform:
    """Platform is a local HTTPS stand-in for the Gateway's /agent/* entry.

    Platform 是 Gateway /agent/* 入口的本地 HTTPS 替身。
    """

    def __init__(self, certfile, keyfile, files, goos, arch):
        self.files = files            # name -> bytes served under /agent/download/<version>/ and /custom/
        self.version = VERSION
        self.redeem_status = 200
        self.redeem_bodies = []
        self.fail_download = False
        self.goos, self.arch = goos, arch
        stub = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, status, body=b'', ctype='application/json'):
                self.send_response(status)
                self.send_header('Content-Type', ctype)
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                if self.path == '/agent/v1/manifest':
                    asset = f'aiserveweave-agent_{stub.version}_{stub.goos}_{stub.arch}' + ('.exe' if stub.goos == 'windows' else '')
                    self.reply(200, json.dumps({'version': stub.version, 'platform_url': stub.url,
                                                'assets': [{'os': stub.goos, 'arch': stub.arch, 'name': asset, 'sha256': '0' * 64}]},
                                               separators=(',', ':')).encode())
                elif self.path == '/agent/v1/ca':
                    self.reply(200, json.dumps({'ca_pem': CA_PEM, 'ca_sha256': CA_SHA}, separators=(',', ':')).encode())
                else:
                    for prefix in (f'/agent/download/{stub.version}/', '/custom/'):
                        if self.path.startswith(prefix) and self.path[len(prefix):] in stub.files and not stub.fail_download:
                            self.reply(200, stub.files[self.path[len(prefix):]], 'application/octet-stream')
                            return
                    self.reply(404)

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
                if self.path != '/agent/v1/redeem':
                    self.reply(404)
                    return
                stub.redeem_bodies.append(body)
                if stub.redeem_status != 200:
                    self.reply(stub.redeem_status, b'{"error":{"code":"invalid_code"}}')
                    return
                self.reply(200, json.dumps({'node_id': 'node-redeemed', 'registry_addr': 'registry.test:9443',
                                            'gateway_addr': 'gateway.test:8443', 'ca_pem': CA_PEM, 'ca_sha256': CA_SHA,
                                            'bootstrap_token': TOKEN, 'token_expires_at': '2030-01-01T00:00:00Z'},
                                           separators=(',', ':')).encode())

        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(certfile, keyfile)
        self.server.socket = context.wrap_socket(self.server.socket, server_side=True)
        self.url = f'https://127.0.0.1:{self.server.server_address[1]}'
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()


if __name__ == '__main__':
    cert, key, directory, goos, arch, log = sys.argv[1:7]
    served = {p.name: p.read_bytes() for p in Path(directory).iterdir() if p.is_file()}
    stub = Platform(cert, key, served, goos, arch)
    if len(sys.argv) > 7 and sys.argv[7]:
        stub.version = sys.argv[7]
    if len(sys.argv) > 8:
        stub.redeem_status = int(sys.argv[8])
    print(stub.url, flush=True)
    try:
        sys.stdin.read()          # runs until the test closes our stdin / 运行到测试关闭标准输入
    finally:
        stub.close()
        Path(log).write_text('\n'.join(b.decode() for b in stub.redeem_bodies))
