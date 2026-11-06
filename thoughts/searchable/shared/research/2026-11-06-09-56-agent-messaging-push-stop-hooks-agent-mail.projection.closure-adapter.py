"""Closure adapter (STAGED PROPOSAL — not wired into the repo).
Derived from the ClosureMap for: Agent Mail message -> projection refresh -> state.db -> --robot-attention.
Pin: ntm 905820535176783780bcf57035f4e9687c43ce24 (+ mcp_agent_mail 1d5c82a7).
Promote into /home/maceo/Dev/ntm and complete each TODO(promote) before use.
Speaks the 7-op contract apps/closure-oracle already talks to (mock_adapter.py).
"""
import http.server, json, sys
ASYNC_EDGES = ["projection_refresh"]              # driver: robot.RefreshNormalizedProjection
CONNECTOR = {e: True for e in ASYNC_EDGES}
SINK = []                                          # Phase-0 /seed_sink target

def handle(op, p):
    if op == "/reset":        SINK.clear(); CONNECTOR.update({e: True for e in ASYNC_EDGES}); return {"ok": True}
    if op == "/set_connector": CONNECTOR[p["edge"]] = p["enabled"]; return {"ok": True}
    if op == "/seed_sink":     SINK.append(p["value"]); return {"ok": True}
    if op == "/seed":
        # TODO(promote): ensure project + receiving agent exist via agentmail.Client
        #   (EnsureProject / CreateAgentIdentity, internal/agentmail/tools.go:54, :109)
        return {"ok": True}
    if op == "/trigger":
        # TODO(promote): agentmail.Client.SendOverseerMessage(...) with importance high
        #   (internal/agentmail/tools.go:872; CLI entry runMailSendOverseer internal/cli/mail.go:770)
        return {"ok": True}
    if op == "/drive":
        if not CONNECTOR.get(p["edge"], True): return {"ok": True}  # oracle disabled = red-at-seam
        # TODO(promote): robot.RefreshNormalizedProjection(ctx, store, projectDir, "")
        #   (internal/robot/robot.go:7028) against a temp state.Open(path) store
        return {"ok": True}
    if op == "/observe":
        # TODO(promote): open a second store-backed feed on the same state.db and return
        #   Replay(cursor, limit) filtered to mail.* (internal/robot/attention_feed.go:474, :944)
        return {"ok": True, "value": json.dumps(SINK)}
    return {"ok": False, "error": "unknown op"}

class Hn(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        out = json.dumps(handle(self.path, json.loads(self.rfile.read(n) or "{}"))).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(out))); self.end_headers(); self.wfile.write(out)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Hn).serve_forever()
