"""A stand-in byz-file-service for scripts/e2e-sync.sh (GRS-20).

POST /api/v1/files (multipart, part "file") -> 201 {"id": ...}
GET  /api/v1/files/<id>/content             -> the bytes
Bearer must equal the token given. While the fail-flag file exists every
upload answers 502, so the e2e can prove a failed upload is retried.

Usage: python -I fake-byz-files.py <port> <token> <fail-flag-path> <count-path>
count-path receives the number of successful uploads after each one.
"""
from email.parser import BytesParser
from email.policy import HTTP
import json
import os
import sys
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT, TOKEN, FAIL_FLAG, COUNT = int(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
FILES = {}
UPLOADS = [0]


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def reply(self, code, body=b"", ctype="application/json"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def authed(self):
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            self.reply(401, b'{"error":"unauthorized"}')
            return False
        return True

    def do_GET(self):
        if self.path == "/healthz":
            return self.reply(200, b"ok", "text/plain")
        if not self.authed():
            return
        parts = self.path.strip("/").split("/")
        if len(parts) == 5 and parts[:3] == ["api", "v1", "files"] and parts[4] == "content" and parts[3] in FILES:
            return self.reply(200, FILES[parts[3]], "text/markdown")
        self.reply(404, b'{"error":"File not found"}')

    def do_POST(self):
        if not self.authed():
            return
        if self.path != "/api/v1/files":
            return self.reply(404, b"{}")
        n = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(n)
        if os.path.exists(FAIL_FLAG):
            return self.reply(502, b'{"error":"storage unavailable"}')
        msg = BytesParser(policy=HTTP).parsebytes(b"Content-Type: " + self.headers.get("Content-Type", "").encode() + b"\r\n\r\n" + raw)
        body = None
        for part in msg.iter_parts() if msg.is_multipart() else []:
            if part.get_param("name", header="content-disposition") == "file":
                body = part.get_payload(decode=True)
        if body is None:
            return self.reply(400, b'{"error":"file is required"}')
        fid = str(uuid.uuid4())
        FILES[fid] = body
        UPLOADS[0] += 1
        with open(COUNT, "w") as f:
            f.write(str(UPLOADS[0]))
        self.reply(201, json.dumps({"id": fid}).encode())


ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
