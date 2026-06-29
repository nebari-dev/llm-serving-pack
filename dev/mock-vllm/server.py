"""Mock vLLM server that responds to health check, model list, and chat endpoints."""
import json
from http.server import HTTPServer, BaseHTTPRequestHandler

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"status":"ok"}')
        elif self.path == "/v1/models":
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            body = json.dumps({"object": "list", "data": [{"id": "test/tiny-model", "object": "model"}]})
            self.wfile.write(body.encode())
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        # Minimal OpenAI-compatible chat-completion response so the dev stack can
        # exercise the full request path (gateway auth/routing -> backend -> 200)
        # without a real model.
        if self.path in ("/v1/chat/completions", "/v1/completions"):
            length = int(self.headers.get("Content-Length", 0) or 0)
            if length:
                self.rfile.read(length)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            body = json.dumps({
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "model": "test/tiny-model",
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": "ok"},
                    "finish_reason": "stop",
                }],
            })
            self.wfile.write(body.encode())
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        pass  # suppress logs

if __name__ == "__main__":
    server = HTTPServer(("0.0.0.0", 8000), Handler)
    print("Mock vLLM server listening on :8000")
    server.serve_forever()
