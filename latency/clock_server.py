#!/usr/bin/env python3
"""Sirve clock.html y /now (hora de la Jetson en ms, epoch) para medir la latencia.
Uso en la Jetson: python3 clock_server.py [puerto]   (por defecto 8080)"""
import http.server, os, socketserver, sys, time

ROOT = os.path.dirname(os.path.abspath(__file__))

class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith('/now'):
            body, ctype = ('%.3f' % (time.time() * 1000)).encode(), 'text/plain'
        else:
            body, ctype = open(os.path.join(ROOT, 'clock.html'), 'rb').read(), 'text/html; charset=utf-8'
        self.send_response(200)
        self.send_header('Content-Type', ctype)
        self.send_header('Cache-Control', 'no-store')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass

class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):   # Python 3.6: sin ThreadingHTTPServer
    allow_reuse_address = daemon_threads = True

Server(('', int(sys.argv[1]) if len(sys.argv) > 1 else 8080), H).serve_forever()
