# Mock OpenAI-compatible chat/completions: streams one text answer and reports usage. No model, no network.
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
ANSWER = json.dumps({"status": "ok", "reason": None, "files": [], "payload": {"summary": "probe", "files_changed": []}})
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): sys.stderr.write("mock: " + (a[0] % a[1:]) + "\n")
    def do_GET(self):
        body = json.dumps({"data": [{"id": "mock-1", "object": "model"}]}).encode()
        self.send_response(200); self.send_header("content-type", "application/json"); self.end_headers(); self.wfile.write(body)
    def do_POST(self):
        req = json.loads(self.rfile.read(int(self.headers.get("content-length", 0))) or b"{}")
        open("/tmp/mock-req.json", "a").write(json.dumps({"path": self.path, "stream": req.get("stream"), "keys": sorted(req)}) + "\n")
        usage = {"prompt_tokens": 1000, "completion_tokens": 200, "total_tokens": 1200}
        if req.get("stream"):
            self.send_response(200); self.send_header("content-type", "text/event-stream"); self.end_headers()
            def ev(o): self.wfile.write(b"data: " + json.dumps(o).encode() + b"\n\n"); self.wfile.flush()
            base = {"id": "c1", "object": "chat.completion.chunk", "created": 1, "model": "mock-1"}
            ev({**base, "choices": [{"index": 0, "delta": {"role": "assistant", "content": ANSWER}, "finish_reason": None}]})
            ev({**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]})
            ev({**base, "choices": [], "usage": usage})
            self.wfile.write(b"data: [DONE]\n\n")
        else:
            body = json.dumps({"id": "c1", "object": "chat.completion", "created": 1, "model": "mock-1",
                "choices": [{"index": 0, "message": {"role": "assistant", "content": ANSWER}, "finish_reason": "stop"}], "usage": usage}).encode()
            self.send_response(200); self.send_header("content-type", "application/json"); self.end_headers(); self.wfile.write(body)
HTTPServer(("127.0.0.1", 8000), H).serve_forever()
