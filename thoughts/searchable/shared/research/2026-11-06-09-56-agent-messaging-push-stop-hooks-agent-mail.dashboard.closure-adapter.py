"""Closure adapter (STAGED PROPOSAL — not wired into the repo).
Derived from the ClosureMap for: Agent Mail message -> ntm dashboard poll -> attention feed mail event.
Pin: ntm 905820535176783780bcf57035f4e9687c43ce24 (+ mcp_agent_mail 1d5c82a7).
Promote into /home/maceo/Dev/ntm and complete each TODO(promote) before use.
Speaks the 7-op contract apps/closure-oracle already talks to (mock_adapter.py).
"""
import http.server, json, sys
ASYNC_EDGES = ["dashboard_inbox_poll"]            # driver: fetchAgentMailInboxes
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
        # TODO(promote): agentmail.Client.SendOverseerMessage(...) with any importance
        #   (internal/agentmail/tools.go:872; CLI entry runMailSendOverseer internal/cli/mail.go:770)
        return {"ok": True}
    if op == "/drive":
        if not CONNECTOR.get(p["edge"], True): return {"ok": True}  # oracle disabled = red-at-seam
        # TODO(promote): run one dashboard inbox poll: fetchAgentMailInboxes
        #   (internal/tui/dashboard/dashboard.go:799), then apply its result (publish at :2938-2942)
        return {"ok": True}
    if op == "/observe":
        # TODO(promote): return robot.GetAttentionFeed().Replay(cursor, limit) filtered to
        #   mail.received / mail.ack_required (internal/robot/attention_feed.go:2956, :944)
        return {"ok": True, "value": json.dumps(SINK)}
    return {"ok": False, "error": "unknown op"}

class Hn(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        out = json.dumps(handle(self.path, json.loads(self.rfile.read(n) or "{}"))).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(out))); self.end_headers(); self.wfile.write(out)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Hn).serve_forever()
