"""A stand-in byz-orchestrator for scripts/e2e-sync.sh (GRS-56).

Usage: python -I fake-orchestrator.py <port> <token>

Serves POST /api/v1/runs, GET /api/v1/runs/<id> and
POST /api/v1/runs/<id>/steps/<step>/(complete|skip) and
POST /api/v1/runs/<id>/cancel (GRS-69) on an in-memory model.
Requires Authorization: Bearer <token> and the three X-Byz-* headers, as
the real service does for a trusted caller. A registered runbook (ref) has
steps backup, migrate, verify. GET /healthz answers ok.
"""
import json
import sys
import uuid
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT, TOKEN = int(sys.argv[1]), sys.argv[2]
RUNS = {}


OFFICIAL = [
    {"ref": "single-ticket-development", "version": 1, "title": "Single ticket development", "internal": False, "frozen": True,
     "immutable": True, "steps": [{"id": "take", "kind": "card.move", "params": {"state": "In Progress"}}, {"id": "push", "kind": "git.push"}]},
    {"ref": "starter", "version": 1, "title": "Starter runbook", "internal": False, "frozen": False,
     "immutable": True, "steps": [{"id": "plan", "kind": "ask", "params": {"question": "goal?"}}, {"id": "log", "kind": "note"}]},
]


ABILITIES = {"items": [
    {"kind": "card.move", "title": "Move a card", "tool": "card_move", "via": "mcp", "evidenceKinds": ["card"], "requiresEvidence": True,
     "params": [{"name": "state", "type": "string", "required": True}]},
    {"kind": "ask", "title": "Ask a person", "tool": "desk_send", "via": "mcp", "evidenceKinds": ["desk"], "requiresEvidence": True,
     "params": [{"name": "question", "type": "string", "required": True}]},
    {"kind": "note", "title": "Log a note", "tool": "session_new", "via": "mcp", "evidenceKinds": ["session"], "requiresEvidence": False, "params": []},
], "evidenceKinds": ["session", "artifact", "commit", "desk", "card"]}


def now():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send(self, status, body):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json" if status < 300 else "application/problem+json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def problem(self, status, code, detail):
        self.send(status, {"status": status, "code": code, "detail": detail})

    def authed(self):
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            self.problem(401, "auth.invalid", "bad token")
            return False
        for h in ("X-Byz-Org-Id", "X-Byz-Tenant-Id", "X-Byz-Actor"):
            if not self.headers.get(h):
                self.problem(400, "scope.invalid_headers", "missing " + h)
                return False
        return True

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(n) or b"{}")

    def do_GET(self):
        if self.path == "/healthz":
            return self.send(200, {"ok": True})
        if not self.authed():
            return
        parts = self.path.split("/")
        if self.path.split("?")[0] == "/api/v1/abilities":  # GRS-70: the step-kind catalog
            return self.send(200, ABILITIES)
        if self.path.split("?")[0] == "/api/v1/runbooks":  # GRS-59: the official (public) catalog
            return self.send(200, {"items": OFFICIAL})
        if len(parts) == 5 and parts[1:4] == ["api", "v1", "runs"] and parts[4] in RUNS:
            return self.send(200, RUNS[parts[4]])
        self.problem(404, "run.not_found", "run not found")

    def do_POST(self):
        if not self.authed():
            return
        b = self.body()
        parts = self.path.split("/")
        if self.path == "/api/v1/runs":
            rb = b.get("runbook", {})
            steps = rb.get("steps") or next((o["steps"] for o in OFFICIAL if o["ref"] == rb.get("ref")), None)                 or [{"id": "backup"}, {"id": "migrate"}, {"id": "verify"}]
            t = now()
            run = {"runId": "run-" + str(uuid.uuid4()), "runbook": rb.get("ref") or rb.get("name"), "runbookRef": rb.get("ref", ""),
                   "tenant": {"orgId": self.headers["X-Byz-Org-Id"], "tenantId": self.headers["X-Byz-Tenant-Id"]},
                   "context": b.get("context", {}), "startedBy": self.headers["X-Byz-Actor"], "startedVia": "gears-api",
                   "status": "running", "currentStep": steps[0]["id"], "version": 1, "startedAt": t, "updatedAt": t, "steps": []}
            for i, s in enumerate(steps):
                run["steps"].append({"id": s["id"], "kind": s.get("kind") or "freeform", "title": s.get("title") or s["id"],
                                     "status": "current" if i == 0 else "pending", **({"startedAt": t} if i == 0 else {})})
            RUNS[run["runId"]] = run
            return self.send(201, {"created": True, "run": run})
        if len(parts) == 6 and parts[5] == "cancel" and parts[4] in RUNS:  # GRS-69 (GRS-68 there)
            run, t = RUNS[parts[4]], now()
            if not (b.get("reason") or "").strip():
                return self.problem(422, "runbook.invalid", "reason required")
            if run["status"] != "running":
                return self.problem(409, "run.finished", "run is " + run["status"])
            for s in run["steps"]:
                if s["status"] == "current":
                    s.update({"status": "canceled", "finishedAt": t, "doneBy": self.headers["X-Byz-Actor"],
                              "doneLabel": b.get("agentLabel", ""), "doneVia": "gears-api"})
            run.update({"status": "canceled", "currentStep": "", "finishedAt": t, "updatedAt": t, "version": run["version"] + 1,
                        "cancelReason": b["reason"], "canceledBy": self.headers["X-Byz-Actor"], "canceledLabel": b.get("agentLabel", ""),
                        "canceledVia": "gears-api", "canceledAt": t})
            return self.send(200, run)
        if len(parts) == 8 and parts[5] == "steps" and parts[4] in RUNS:
            run, step, verb = RUNS[parts[4]], parts[6], parts[7]
            if run["status"] != "running":
                return self.problem(409, "run.finished", "run is " + run["status"])
            if step != run["currentStep"]:
                return self.problem(409, "step.not_current", "the current step is " + run["currentStep"])
            idx = [s["id"] for s in run["steps"]].index(step)
            s, t = run["steps"][idx], now()
            s.update({"finishedAt": t, "doneBy": self.headers["X-Byz-Actor"], "doneLabel": b.get("agentLabel", ""), "doneVia": "gears-api"})
            if verb == "complete":
                s.update({"status": "completed", "evidence": b.get("evidence", [])})
            else:
                s.update({"status": "skipped", "skipReason": b.get("reason", "")})
            run["version"] += 1
            run["updatedAt"] = t
            if idx + 1 < len(run["steps"]):
                run["steps"][idx + 1].update({"status": "current", "startedAt": t})
                run["currentStep"] = run["steps"][idx + 1]["id"]
            else:
                run.update({"currentStep": "", "status": "completed", "finishedAt": t})
            return self.send(200, {"runId": run["runId"], "stepId": step, "stepStatus": s["status"], "runStatus": run["status"],
                                   "currentStep": run["currentStep"], "version": run["version"]})
        self.problem(404, "run.not_found", "run not found")


ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()
