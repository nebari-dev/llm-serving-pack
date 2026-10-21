#!/usr/bin/env python3
"""Usage tracer: send one model request on the dev kind cluster and follow it.

    python3 dev/usage-tracer/server.py      then open http://127.0.0.1:18894

It signs in as alice or bob (the dev realm's people, see usage-e2e.sh),
sends one request through the gateway with an API key or the login token,
and then looks for that request in each place it passes through: the
gateway's access log, the usage collector's counters, Kafka, ClickHouse,
OpenMeter's events API and the usage console. Every lookup is a real kubectl or
HTTP call, shown next to its result.

Local dev only: it listens on 127.0.0.1, runs a fixed set of commands
(never a shell), and never returns a password or a key to the page.
Needs the cluster from `make usage-e2e`. Standard library only.
"""

import json
import os
import re
import socket
import subprocess
import threading
import time
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.request import Request, urlopen

HERE = Path(__file__).resolve().parent
LISTEN = ("127.0.0.1", int(os.environ.get("TRACER_PORT", "18894")))
CONTEXT = "kind-" + os.environ.get("CLUSTER_NAME", "llm-serving-test")
NS = "llm-operator-system"
GW_NS = "envoy-gateway-system"
RELEASE = os.environ.get("OM_RELEASE", "usage")
os.environ.setdefault("KUBECONFIG", str(Path.home() / ".cache/kind-llm.kubeconfig"))

KUBECTL = ["kubectl", "--context", CONTEXT]
REQUEST_ID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")

# kubectl port-forwards the tracer keeps open: name -> (namespace, target, local, remote).
# Ports of their own, so a port-forward started by hand doesn't get in the way.
FORWARDS = {
    "gateway-external": (GW_NS, "gateway:nebari-gateway", 28443, 443),
    "gateway-internal": (GW_NS, "gateway:nebari-internal-gateway", 28444, 443),
    "key-manager": (NS, "svc/llm-key-manager", 28440, 8080),
    # The key-owner lookup the collector makes (internal port, cluster-only).
    "key-owners": (NS, "svc/llm-key-manager-owners", 28446, 8081),
    "openmeter": (NS, f"svc/{RELEASE}-openmeter-api", 28441, 80),
    "collector": (NS, "deploy/llm-operator-usage-collector", 28442, 4195),
    "console": (NS, f"svc/{RELEASE}-openmeter-console", 28445, 80),
}


def run(args, timeout=30, stdin=None):
    """Run a fixed argv (no shell). Returns stdout; raises on failure."""
    p = subprocess.run(args, input=stdin, capture_output=True, text=True, timeout=timeout)
    if p.returncode != 0:
        raise RuntimeError(f"{' '.join(args[:6])}... failed: {p.stderr.strip()[-300:]}")
    return p.stdout


def shown(args):
    """The command as a reader would type it."""
    return " ".join(a if re.fullmatch(r"[\w@%+=:,./-]+", a) else f"'{a}'" for a in args)


# ---- port-forwards -------------------------------------------------------------

_forwards = {}
_forward_lock = threading.Lock()


def _port_open(port):
    with socket.socket() as s:
        s.settimeout(0.3)
        return s.connect_ex(("127.0.0.1", port)) == 0


def forward(name):
    """Make sure the port-forward is up; restart it if kubectl exited."""
    ns, target, local, remote = FORWARDS[name]
    with _forward_lock:
        proc = _forwards.get(name)
        if proc and proc.poll() is None and _port_open(local):
            return local
        if proc and proc.poll() is None:
            proc.kill()
        if target.startswith("gateway:"):
            svc = run(KUBECTL + ["-n", ns, "get", "svc", "-l", f"gateway.envoyproxy.io/owning-gateway-name={target[8:]}", "-o", "name"]).split()[0]
        else:
            svc = target
        _forwards[name] = subprocess.Popen(KUBECTL + ["-n", ns, "port-forward", svc, f"{local}:{remote}"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for _ in range(50):
            if _port_open(local):
                return local
            time.sleep(0.1)
        raise RuntimeError(f"port-forward {name} did not come up")


def http_json(name, path, method="GET", body=None, headers=None):
    port = forward(name)
    req = Request(f"http://127.0.0.1:{port}{path}", method=method, data=body, headers=headers or {})
    with urlopen(req, timeout=15) as r:
        return json.loads(r.read() or b"null")


# ---- people: login tokens and API keys --------------------------------------------

_keys = {}  # person -> {"clientId", "apiKey"}; minted once per tracer run


def login_token(person):
    """A Keycloak access token for alice or bob, minted inside the cluster so
    its issuer matches what the gateway checks. The password never leaves
    this process."""
    pw = run(KUBECTL + ["-n", "keycloak", "get", "secret", "demo-users", "-o", "jsonpath={.data." + person + "}"])
    import base64

    password = base64.b64decode(pw).decode()
    script = (
        "import json,sys,urllib.request,urllib.parse\n"
        "u,p=sys.stdin.read().split('\\n')[:2]\n"
        "d=urllib.parse.urlencode({'client_id':'nebari-frontend-spa','username':u,'password':p,'grant_type':'password'}).encode()\n"
        "print(json.load(urllib.request.urlopen('http://keycloak.keycloak.svc.cluster.local:8080/realms/nebari/protocol/openid-connect/token',d))['access_token'])"
    )
    return run(KUBECTL + ["-n", NS, "exec", "-i", "deploy/mock-provider", "--", "python", "-c", script], stdin=f"{person}\n{password}\n").strip()


def api_key(person, token):
    if person not in _keys:
        body = json.dumps({"modelName": "mock-provider", "description": "usage tracer"}).encode()
        _keys[person] = http_json("key-manager", "/api/keys", "POST", body, {"Authorization": f"Bearer {token}", "Content-Type": "application/json"})
    return _keys[person]


# ---- how each part runs --------------------------------------------------------

# name -> (namespace, label selector, what it is). The pods are read live.
COMPONENTS = {
    "gateway": (GW_NS, "gateway.envoyproxy.io/owning-gateway-name in (nebari-gateway,nebari-internal-gateway)",
                "Envoy (C++), in pods Envoy Gateway creates for each Gateway: one for API keys (nebari-gateway), one for logins (nebari-internal-gateway). Envoy AI Gateway's ext_proc (Go) runs as a sidecar container in the same pod."),
    "model": (NS, "app=mock-provider", "A small Python server (dev/mock-vllm) that answers like an OpenAI-compatible model."),
    "collector": (NS, "app=llm-operator-usage-collector",
                  "OpenMeter's Benthos collector (Go). Its pipeline is the pack's files/usage-collector.yaml, mounted at /etc/usage-collector/."),
    "key-manager": (NS, "app=llm-key-manager",
                    "The pack's key manager (Go). The collector calls its owner lookup on port 8081, through the llm-key-manager-owners Service."),
    "kafka": (NS, f"app.kubernetes.io/instance={RELEASE},app.kubernetes.io/component=kafka", "Confluent's Kafka image (Java), one broker."),
    "sink-worker": (NS, f"app.kubernetes.io/instance={RELEASE},app.kubernetes.io/component=sink-worker",
                    "OpenMeter's sink worker (Go): the same image as the API, running the openmeter-sink-worker binary."),
    "redis": (NS, f"app.kubernetes.io/instance={RELEASE},app.kubernetes.io/component=redis", "Redis (C). The sink worker keeps the event ids it has stored here, to drop repeats."),
    "clickhouse": (NS, f"app.kubernetes.io/instance={RELEASE},app.kubernetes.io/component=clickhouse", "ClickHouse server (C++)."),
    "openmeter-api": (NS, f"app.kubernetes.io/instance={RELEASE},app.kubernetes.io/component=api", "OpenMeter (Go): the openmeter binary, serving its HTTP API."),
    "postgres": (NS, f"cnpg.io/cluster={RELEASE}-openmeter-db",
                 "PostgreSQL, run by CloudNativePG. OpenMeter keeps its own records here (customers, features, prices); usage events are not stored here."),
    "console": (NS, f"app.kubernetes.io/instance={RELEASE},app.kubernetes.io/component=console",
                "The usage console: a Go server that reads OpenMeter's API and serves the React app, in one container."),
}
_components = {"at": 0, "data": None}


def owner_of(pod):
    ref = (pod["metadata"].get("ownerReferences") or [{}])[0]
    kind, name = ref.get("kind", ""), ref.get("name", "")
    if kind == "ReplicaSet":  # <deployment>-<template hash>
        return "Deployment", name.rsplit("-", 1)[0]
    if kind == "Cluster":
        return "CloudNativePG Cluster", name
    return kind, name


def components():
    """Every part's pods, containers and images, read from the cluster (cached 30 s)."""
    if _components["data"] and time.time() - _components["at"] < 30:
        return _components["data"]
    out = {}
    for name, (ns, selector, about) in COMPONENTS.items():
        pods = json.loads(run(KUBECTL + ["-n", ns, "get", "pods", "-l", selector, "-o", "json"]))["items"]
        rows = []
        for pod in pods:
            if pod["status"].get("phase") in ("Succeeded", "Failed"):
                continue
            statuses = {c["name"]: c for c in pod["status"].get("containerStatuses", [])}
            kind, workload = owner_of(pod)
            rows.append({
                "pod": pod["metadata"]["name"], "kind": kind, "workload": workload, "node": pod["spec"].get("nodeName"),
                "volumes": [v["persistentVolumeClaim"]["claimName"] for v in pod["spec"].get("volumes", []) if "persistentVolumeClaim" in v],
                "containers": [{
                    "name": c["name"], "image": c["image"],
                    "run": " ".join(c.get("command", []) + c.get("args", [])),
                    "ports": [pt["containerPort"] for pt in c.get("ports", [])],
                    "ready": statuses.get(c["name"], {}).get("ready", False),
                    "restarts": statuses.get(c["name"], {}).get("restartCount", 0),
                } for c in pod["spec"]["containers"]],
            })
        out[name] = {"about": about, "namespace": ns, "pods": rows, "command": f"kubectl -n {ns} get pods -l '{selector}' -o wide"}
    _components.update(at=time.time(), data=out)
    return out


# ---- one traced request ---------------------------------------------------------------

_traces = {}
_trace_lock = threading.Lock()


def collector_metrics():
    port = forward("collector")
    with urlopen(f"http://127.0.0.1:{port}/metrics", timeout=5) as r:
        text = r.read().decode()
    out = {}
    for line in text.splitlines():
        m = re.match(r"^(input_received|output_sent|output_error)\{[^}]*\} (\d+)", line)
        if m:
            out[m[1]] = out.get(m[1], 0) + int(m[2])
    return out


def send(person, path, model, cancel):
    token = login_token(person)
    if path == "key":
        key = api_key(person, token)
        bearer, host, fwd, match = key["apiKey"], "llm.local", "gateway-external", {"client_id": key["clientId"]}
    else:
        bearer, host, fwd, match = token, "llm-internal.local", "gateway-internal", {"auth_user": person}
    body = {"model": model, "messages": [{"role": "user", "content": "Trace this request, please."}]}
    if cancel:
        body["stream"] = True
    attempts = []
    for _ in range(60):
        port = forward(fwd)
        # Counted per attempt, so a refused earlier attempt isn't taken for this one.
        before = collector_metrics()
        sent = time.time()
        args = ["curl", "-sk", "-o", "-", "-w", "\n%{http_code}", "--max-time", "1" if cancel else "20",
                "--resolve", f"{host}:{port}:127.0.0.1", f"https://{host}:{port}/v1/chat/completions",
                "-H", "Content-Type: application/json", "-H", "@-", "-d", json.dumps(body)]
        p = subprocess.run(args, input=f"Authorization: Bearer {bearer}\n", capture_output=True, text=True, timeout=30)
        took = time.time() - sent
        out, _, code = p.stdout.rpartition("\n")
        attempts.append(code or "000")
        # A new key is refused (403) until the gateway has loaded it, and a
        # dropped port-forward answers 000; both are retried. A cancelled
        # stream can end before any status, so its 000 is kept.
        if code != "403" and (code != "000" or cancel):
            break
        time.sleep(2)
    usage = None
    try:
        usage = json.loads(out).get("usage")
    except ValueError:
        pass
    with _trace_lock:
        tid = str(len(_traces) + 1)
        _traces[tid] = {
            "person": person, "path": path, "model": model, "cancel": cancel, "sentAt": sent,
            "gateway": f"nebari-{'internal-' if path == 'login' else ''}gateway", "match": match,
            "collectorBefore": before, "status": code, "attempts": attempts, "usage": usage, "tookMs": int(took * 1000), "stages": {}, "polling": threading.Lock(),
        }
    return tid


def parse_time(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()


def stage_gateway(t):
    args = KUBECTL + ["-n", GW_NS, "logs", "-l", f"gateway.envoyproxy.io/owning-gateway-name={t['gateway']}", "-c", "envoy", "--since=10m", "--tail=2000"]
    best = None
    for line in run(args).splitlines():
        line = line[line.find("{"):] if "{" in line else ""
        try:
            rec = json.loads(line)
        except ValueError:
            continue
        if not isinstance(rec, dict) or "request_id" not in rec or not rec.get("start_time"):
            continue
        if parse_time(rec["start_time"]) < t["sentAt"] - 1:
            continue
        if any(rec.get(k) != v for k, v in t["match"].items()) or not REQUEST_ID.match(str(rec["request_id"])):
            continue
        if best is None or rec["start_time"] >= best["start_time"]:
            best = rec
    if best:
        t["requestId"] = best["request_id"]
    return best, shown(args) + " | grep '\"input_tokens\"'"


def stage_collector(t):
    """The collector logs nothing per request and keeps nothing: its counters
    show it handled the record, and for an API key the tracer repeats its
    key-owner lookup. The events it produced are shown from Kafka."""
    now = collector_metrics()
    delta = {k: now.get(k, 0) - t["collectorBefore"].get(k, 0) for k in ("input_received", "output_sent", "output_error")}
    if delta["input_received"] <= 0:
        return None, None
    owner = None
    if "client_id" in t["match"]:
        owner = http_json("key-owners", f"/internal/v1/key-owners/{t['match']['client_id']}")
    cmd = f"kubectl -n {NS} port-forward svc/llm-key-manager-owners 18081:8081 & curl -s localhost:18081/internal/v1/key-owners/{t['match']['client_id']}" if owner else None
    return {"counters": delta, "owner": owner}, cmd


def stage_openmeter(t):
    rid = t["requestId"]
    events = http_json("openmeter", f"/api/v1/events?id={rid}&limit=20")
    rows = [
        {"type": e["event"]["type"], "id": e["event"]["id"], "subject": e["event"].get("subject"), **{k: e["event"]["data"].get(k) for k in ("user", "team", "api_key", "model", "token_type", "tokens", "usage_reported") if k in e["event"]["data"]}}
        for e in events
    ]
    rows.sort(key=lambda r: (r["type"] != "llm.request", r["id"]))
    cmd = f"kubectl -n {NS} port-forward svc/{RELEASE}-openmeter-api 18893:80 & curl -s 'localhost:18893/api/v1/events?id={rid}' | jq"
    return (rows or None), cmd


def stage_kafka(t):
    rid = t["requestId"]
    args = KUBECTL + ["-n", NS, "exec", f"{RELEASE}-openmeter-kafka-0", "--", "kafka-console-consumer", "--bootstrap-server", "localhost:9092",
                      "--topic", "om_default_events", "--from-beginning", "--timeout-ms", "8000", "--property", "print.partition=true", "--property", "print.offset=true"]
    p = subprocess.run(args, capture_output=True, text=True, timeout=40)
    msgs = []
    for line in p.stdout.splitlines():
        if rid not in line:
            continue
        m = re.match(r"^Partition:(\d+)\s+Offset:(\d+)\s+(.*)$", line)
        payload = m[3] if m else line
        try:
            ev = json.loads(payload)
        except ValueError:
            ev = {"raw": payload[:200]}
        msgs.append({"partition": m[1] if m else None, "offset": m[2] if m else None, "id": ev.get("id"), "type": ev.get("type"), "event": ev})
    return (msgs or None), shown(args) + f" | grep {rid}"


def stage_clickhouse(t):
    rid = t["requestId"]
    query = "SELECT type, id, subject, toString(time) AS time, data FROM openmeter.om_events WHERE startsWith(id, {rid:String}) ORDER BY id FORMAT JSONEachRow"
    # The container has its password in CLICKHOUSE_PASSWORD, so the tracer never handles it.
    script = 'exec clickhouse-client --password "$CLICKHOUSE_PASSWORD" --param_rid="$1" -q "$2"'
    args = KUBECTL + ["-n", NS, "exec", f"{RELEASE}-openmeter-clickhouse-0", "--", "sh", "-c", script, "sh", rid, query]
    rows = []
    for line in run(args).splitlines():
        r = json.loads(line)
        try:
            r["data"] = json.loads(r["data"])
        except (ValueError, TypeError):
            pass
        rows.append(r)
    cmd = f"kubectl -n {NS} exec {RELEASE}-openmeter-clickhouse-0 -- sh -c 'clickhouse-client --password \"$CLICKHOUSE_PASSWORD\" --param_rid={rid} -q \"{query}\"'"
    return (rows or None), cmd


def stage_console(t):
    rid = t["requestId"]
    receipts = http_json("console", "/api/v1/demo/receipts?limit=200")
    found = next((r for r in receipts if r["id"] == rid), None)
    if found:
        found = {k: found.get(k) for k in ("actor", "team", "keyId", "resolvedModel", "inputTokens", "cachedInputTokens", "outputTokens", "costUsd", "verdict", "status", "errorCode")}
    return found, f"curl -s 'localhost:{FORWARDS['console'][2]}/api/v1/demo/receipts?limit=200' | jq '.[] | select(.id==\"{rid}\")'"


STAGES = [
    ("gateway", stage_gateway, False),
    ("collector", stage_collector, False),
    ("kafka", stage_kafka, True),
    ("clickhouse", stage_clickhouse, True),
    # OpenMeter's events API reads ClickHouse, so it can only answer after it.
    ("openmeter", stage_openmeter, True),
    ("console", stage_console, True),
]


def check(t, name, fn):
    try:
        data, cmd = fn(t)
    except Exception as e:  # report and keep polling
        t["stages"][name] = {"found": False, "error": str(e)[:300]}
        return
    entry = {"found": data is not None, "command": cmd}
    if data is not None:
        entry["data"] = data
        entry["afterMs"] = int((time.time() - t["sentAt"]) * 1000)
    t["stages"][name] = entry


def poll(tid):
    """Check every stage that hasn't found the request yet, all at once. The
    page polls often; while one check is running, others get its last state."""
    t = _traces[tid]
    if t["polling"].acquire(blocking=False):
        try:
            todo = []
            for name, fn, needs_id in STAGES:
                if t["stages"].get(name, {}).get("found"):
                    continue
                if needs_id and not t.get("requestId"):
                    t["stages"][name] = {"found": False, "waiting": "the gateway's record"}
                elif name == "console" and not t["stages"].get("openmeter", {}).get("found"):
                    t["stages"][name] = {"found": False, "waiting": "OpenMeter"}  # the console scan is costly
                else:
                    todo.append((name, fn))
            threads = [threading.Thread(target=check, args=(t, name, fn)) for name, fn in todo]
            for th in threads:
                th.start()
            for th in threads:
                th.join()
        finally:
            t["polling"].release()
    out = {k: v for k, v in t.items() if k not in ("collectorBefore", "match", "polling")}
    if t.get("requestId"):
        out["consoleUrl"] = f"http://127.0.0.1:{FORWARDS['console'][2]}/traffic?receipt={t['requestId']}"
    return out


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def reply(self, code, obj, ctype="application/json"):
        body = obj if isinstance(obj, bytes) else json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path in ("/", "/index.html"):
            return self.reply(200, (HERE / "index.html").read_bytes(), "text/html; charset=utf-8")
        if self.path == "/api/components":
            try:
                return self.reply(200, components())
            except Exception as e:
                return self.reply(500, {"error": str(e)[:300]})
        m = re.fullmatch(r"/api/trace/(\d+)", self.path)
        if m and m[1] in _traces:
            return self.reply(200, poll(m[1]))
        self.reply(404, {"error": "not found"})

    def do_POST(self):
        if self.path != "/api/send":
            return self.reply(404, {"error": "not found"})
        try:
            req = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
            person = req.get("person")
            path = req.get("path")
            model = req.get("model")
            if person not in ("alice", "bob") or path not in ("key", "login") or model not in ("mock/small", "mock/large"):
                return self.reply(400, {"error": "person, path or model not allowed"})
            self.reply(200, {"trace": send(person, path, model, bool(req.get("cancel")))})
        except Exception as e:
            self.reply(500, {"error": str(e)[:300]})


if __name__ == "__main__":
    # The dev Keycloak forgets alice and bob when its pod restarts.
    subprocess.run([str(HERE.parent / "demo-users.sh")], check=True, stdout=subprocess.DEVNULL)
    print(f"usage tracer on http://{LISTEN[0]}:{LISTEN[1]} (cluster context {CONTEXT})")
    try:
        ThreadingHTTPServer(LISTEN, Handler).serve_forever()
    finally:
        for p in _forwards.values():
            p.kill()
