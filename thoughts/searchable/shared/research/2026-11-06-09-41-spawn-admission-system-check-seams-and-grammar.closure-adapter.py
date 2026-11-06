"""Closure adapter (STAGED PROPOSAL — not wired into the repo).
Derived from the ClosureMap for: robot spawn admission decision.  Pin: 905820535176783780bcf57035f4e9687c43ce24.
Promote into /home/maceo/Dev/ntm and complete each TODO(promote) before use.
Speaks the 7-op contract apps/closure-oracle already talks to (mock_adapter.py).
"""
import http.server, json, sys
ASYNC_EDGES = []                                   # chain A is fully synchronous; no drivers
CONNECTOR = {e: True for e in ASYNC_EDGES}
SINK = []                                          # Phase-0 /seed_sink target

def handle(op, p):
    if op == "/reset":        SINK.clear(); CONNECTOR.update({e: True for e in ASYNC_EDGES}); return {"ok": True}
    if op == "/set_connector": CONNECTOR[p["edge"]] = p["enabled"]; return {"ok": True}
    if op == "/seed_sink":     SINK.append(p["value"]); return {"ok": True}
    if op == "/seed":
        # TODO(promote): write p["data"] (list of pressure.MemorySample) via
        #   pressure.NewFileMemorySampleStore().Append  (internal/pressure/memory_store.go:73)
        #   with XDG_DATA_HOME pointed at a temp dir   (MemorySampleStorePath, memory_store.go:43)
        return {"ok": True}
    if op == "/trigger":
        # TODO(promote): run `ntm --robot-spawn=<s> --spawn-cc=N --dry-run` -> robot.PrintSpawn
        #   (internal/robot/spawn.go:714 -> GetSpawn :303, admission at :443)
        return {"ok": True}
    if op == "/drive":
        if not CONNECTOR.get(p["edge"], True): return {"ok": True}  # oracle disabled = red-at-seam
        # no async edges in this chain
        return {"ok": True}
    if op == "/observe":
        # TODO(promote): return the "admission" object from robot.PrintSpawn stdout JSON
        #   (SpawnOutput.Admission, internal/robot/spawn.go:75; shape pressure.SpawnAdmission pressure.go:162)
        return {"ok": True, "value": json.dumps(SINK)}
    return {"ok": False, "error": "unknown op"}

class Hn(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        out = json.dumps(handle(self.path, json.loads(self.rfile.read(n) or "{}"))).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(out))); self.end_headers(); self.wfile.write(out)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Hn).serve_forever()
