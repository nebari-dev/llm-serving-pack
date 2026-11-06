"""Mock OpenAI-compatible server for the local dev stack.

Answers the health check, model list, and chat endpoints so the dev cluster can
exercise the full request path (gateway auth/routing -> backend -> 200) without
a real model. Responses carry a `usage` block, and `stream: true` is answered
as server-sent events, so token accounting through the gateway can be tested.

Set TLS_CERT_FILE and TLS_KEY_FILE to serve HTTPS: a PassthroughModel always
connects to its provider over TLS, so the mock has to speak it to stand in for
an external provider.
"""
import json
import os
import ssl
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

DEFAULT_MODEL = "test/tiny-model"
REPLY_WORDS = ["Hello", " from", " the", " mock", " model", "."]
# Pause between streamed chunks, long enough for a client to disconnect
# mid-stream when testing what the gateway records for a cancelled response.
STREAM_CHUNK_DELAY_SECONDS = float(os.environ.get("STREAM_CHUNK_DELAY_SECONDS", "0.3"))


def usage_for(request):
    """Deterministic token counts derived from the request, so two different
    prompts produce two different, predictable usage records."""
    text = "".join(
        m.get("content", "") for m in request.get("messages", []) if isinstance(m.get("content"), str)
    )
    prompt_tokens = len(text) // 4 + 1
    completion_tokens = len(REPLY_WORDS)
    return {
        "prompt_tokens": prompt_tokens,
        "completion_tokens": completion_tokens,
        "total_tokens": prompt_tokens + completion_tokens,
        # Report a slice of the prompt as served from the provider's cache so
        # the cached-input path is exercised too.
        "prompt_tokens_details": {"cached_tokens": prompt_tokens // 2},
    }


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_json(200, {"status": "ok"})
        elif self.path == "/v1/models":
            self.send_json(200, {"object": "list", "data": [{"id": DEFAULT_MODEL, "object": "model"}]})
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        if self.path not in ("/v1/chat/completions", "/v1/completions"):
            self.send_response(404)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", 0) or 0)
        try:
            request = json.loads(self.rfile.read(length)) if length else {}
        except ValueError:
            request = {}
        model = request.get("model") or DEFAULT_MODEL
        if request.get("stream"):
            self.send_stream(request, model)
            return
        self.send_json(200, {
            "id": "chatcmpl-mock",
            "object": "chat.completion",
            "model": model,
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": "".join(REPLY_WORDS)},
                "finish_reason": "stop",
            }],
            "usage": usage_for(request),
        })

    def send_json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def send_stream(self, request, model):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

        def event(payload):
            self.wfile.write(b"data: " + json.dumps(payload).encode() + b"\n\n")
            self.wfile.flush()

        base = {"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": model}
        try:
            for word in REPLY_WORDS:
                event({**base, "choices": [{"index": 0, "delta": {"content": word}, "finish_reason": None}]})
                time.sleep(STREAM_CHUNK_DELAY_SECONDS)
            event({**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]})
            # Like OpenAI and vLLM, only report usage on a stream when asked to.
            if (request.get("stream_options") or {}).get("include_usage"):
                event({**base, "choices": [], "usage": usage_for(request)})
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            pass  # client went away mid-stream

    def log_message(self, format, *args):
        pass  # suppress logs


if __name__ == "__main__":
    port = int(os.environ.get("PORT", "8000"))
    server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    cert, key = os.environ.get("TLS_CERT_FILE"), os.environ.get("TLS_KEY_FILE")
    if cert and key:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert, key)
        server.socket = context.wrap_socket(server.socket, server_side=True)
    print(f"Mock vLLM server listening on :{port} ({'https' if cert and key else 'http'})")
    server.serve_forever()
