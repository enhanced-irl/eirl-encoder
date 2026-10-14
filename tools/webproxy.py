#!/usr/bin/env python3
# Test proxy: the encoder's web app at http://127.0.0.1:8099 with the session already open, to test it with
# a browser driven by tools (e.g. Chrome DevTools automation) without typing the password. The password
# comes in EIRL_PW and is never printed nor stored. On a development computer and for tests only.
#   EIRL_PW="$(<your password manager's command>)" python3 tools/webproxy.py
#   (BOARD=<host or IP> for another board; default jetson.local)
import http.client, http.server, json, os, socketserver, sys
BOARD = os.environ.get('BOARD', 'jetson.local')

def login():
    c = http.client.HTTPConnection(BOARD, 80, timeout=10)
    c.request('POST', '/api/v1/login', json.dumps({'password': os.environ['EIRL_PW']}), {'Content-Type': 'application/json'})
    r = c.getresponse(); r.read()
    for k, v in r.getheaders():
        if k.lower() == 'set-cookie' and v.startswith('eirl_sess='):
            return v.split(';')[0]
    sys.exit('login %d' % r.status)

COOKIE = login()

class H(http.server.BaseHTTPRequestHandler):
    def do(self):
        if self.path.startswith('/api/v1/logout'):
            self.send_response(204); self.end_headers(); return
        n = int(self.headers.get('Content-Length') or 0)
        body = self.rfile.read(n) if n else None
        hdr = {'Host': BOARD, 'Cookie': COOKIE}
        for k in ('Content-Type', 'Accept'):
            if self.headers.get(k): hdr[k] = self.headers[k]
        c = http.client.HTTPConnection(BOARD, 80, timeout=3600)
        c.request(self.command, self.path, body, hdr)
        r = c.getresponse()
        self.send_response(r.status)
        for k, v in r.getheaders():
            if k.lower() in ('content-type', 'cache-control', 'content-security-policy'):
                self.send_header(k, v)
        self.end_headers()
        while True:
            chunk = r.read1(65536) if hasattr(r, 'read1') else r.read(65536)
            if not chunk: break
            try:
                self.wfile.write(chunk); self.wfile.flush()
            except OSError:
                break
    do_GET = do_POST = do_PUT = do_DELETE = do
    def log_message(self, *a): pass

class S(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True

S(('127.0.0.1', 8099), H).serve_forever()
