---
title: Troubleshooting
---
This page covers concrete failure modes seen during production installs. Each section describes the symptom, the likely cause, and the exact commands to diagnose and fix it. For the full validation checklist, see the [installation runbook](/installation/).

---

## vLLM pod stuck in `Pending` - no GPU available

**Symptom:** The model pod sits in `Pending` indefinitely. `kubectl describe pod` shows an event like:

```
0/3 nodes are available: 3 Insufficient nvidia.com/gpu.
```

**Likely cause:** Either no GPU node exists in the cluster, the NVIDIA GPU Operator is not running (so `nvidia.com/gpu` is never advertised), or the GPU node is already fully consumed by another model pod.

**Fix:**

Check whether any node exposes GPU capacity:

```bash
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.capacity.nvidia\.com/gpu}{"\n"}{end}'
```

Expected output is a non-empty count (e.g. `1`) next to at least one node. If every node shows an empty value:

```bash
# Confirm the GPU Operator pods are all Running/Completed
kubectl get pods -n nvidia-gpu-operator --no-headers \
  | awk '$3 != "Running" && $3 != "Completed" {print}'
```

If any GPU Operator pod is not `Running` or `Completed`, fix it before continuing - the device plugin daemonset is what exposes `nvidia.com/gpu` to the scheduler.

For single-GPU clusters, only one model pod can run at a time. A rolling update will deadlock if the new pod cannot schedule alongside the old one. To recover:

```bash
# Scale down first, then let the replacement schedule
kubectl patch llmmodel -n nebari-llm-serving-system <model-name> \
  --type=merge -p '{"spec":{"serving":{"replicas":0}}}'
# Wait for the old pod to terminate
kubectl get pods -n nebari-llm-serving-system -w
# Then scale back up
kubectl patch llmmodel -n nebari-llm-serving-system <model-name> \
  --type=merge -p '{"spec":{"serving":{"replicas":1}}}'
```

---

## Model init container slow or stuck - download phase

**Symptom:** The model pod stays in `Init:0/1` for more than 20 minutes, or the pod keeps restarting after the init container exits non-zero.

**Likely cause:** The `model-downloader` init container is downloading the model weights from HuggingFace. Large models (30 GB+) take 10-20 minutes on typical connections. If it exits with an error, the most common causes are a missing or wrong HuggingFace token (gated model), or transient network issues.

**Fix:**

Tail the init container logs to see download progress:

```bash
kubectl logs -n nebari-llm-serving-system <vllm-pod> -c model-downloader -f
```

If the logs show a 401 or "access denied" from HuggingFace, the model is gated and requires a token. Confirm the `spec.model.authSecretName` field on the LLMModel CR points to a Secret that contains a valid `HF_TOKEN`:

```bash
kubectl get llmmodel -n nebari-llm-serving-system <model-name> \
  -o jsonpath='{.spec.model.authSecretName}'

# Verify the Secret exists and has HF_TOKEN
kubectl get secret -n nebari-llm-serving-system <secret-name> \
  -o jsonpath='{.data.HF_TOKEN}' | base64 -d | head -c 20 && echo
```

If the download completed but the pod keeps crash-looping, check PVC binding - the init container writes to a volume that must be bound before it starts:

```bash
kubectl get pvc -n nebari-llm-serving-system
```

All PVCs should be `Bound`. If any is `Pending`, check your `storageClass` setting in the LLMModel CR against available storage classes (`kubectl get storageclass`). See [shared storage](/shared-storage/) for storage configuration details.

---

## Key-manager returns 403 - user not in the allowed Keycloak group

**Symptom:** A user opens `https://llm-keys.<baseDomain>/` and the SPA's keycloak-js PKCE login succeeds (Keycloak authentication works), but they see no models listed, or receive an HTTP 403 when trying to create an API key.

**Likely cause:** The user's Keycloak account is not a member of the group named in `LLMModel.spec.access.groups` (default: `llm`). The key manager validates the bearer token on every `/api` request and checks group membership from the `groups` claim.

**Fix:**

Confirm the user is in the group using the Keycloak admin API:

```bash
KC_HOST="https://keycloak.<baseDomain>"
KC_REALM=nebari
KC_ADMIN_USER=$(kubectl get secret -n keycloak keycloak-admin-credentials \
  -o jsonpath='{.data.admin-username}' | base64 -d)
KC_ADMIN_PASS=$(kubectl get secret -n keycloak keycloak-admin-credentials \
  -o jsonpath='{.data.admin-password}' | base64 -d)

TOKEN=$(curl -sS -X POST \
  "$KC_HOST/realms/master/protocol/openid-connect/token" \
  -d "grant_type=password&client_id=admin-cli&username=$KC_ADMIN_USER&password=$KC_ADMIN_PASS" \
  | python3 -c 'import sys, json; print(json.load(sys.stdin)["access_token"])')

curl -sS -H "Authorization: Bearer $TOKEN" \
  "$KC_HOST/admin/realms/$KC_REALM/groups?search=llm" | python3 -m json.tool
```

If the `llm` group does not exist, create it (see the installation runbook section 7.2). If the group exists but the user is not a member, add them through the Keycloak admin console or API.

Groups are compared as Keycloak paths by default (`auth.oidc.groupsClaimForm: path`), the form NIC's realm puts in tokens: `llm` and `/llm` in `access.groups` both match a token carrying `/llm`. A user in a nested group `/org/llm` does not match `llm`; list the nested group by its full path. If the groups claim is not a group path (for example `auth.oidc.groupsClaim: realm_access.roles`), set `auth.oidc.groupsClaimForm: name` so values are compared as written.

Also verify the `groups` claim is included in tokens - the Keycloak client must have a group-membership protocol mapper configured. Check the key-manager pod logs for the claim it is actually receiving:

```bash
kubectl logs -n nebari-llm-serving-system deploy/nebari-llm-serving-key-manager --tail=30
```

---

## Gateway HTTPS listener not `Programmed: True` - missing TLS cert

**Symptom:** External requests to `https://llm.<baseDomain>/` fail with a TLS handshake error or connection refused. Running the listener check shows `Programmed: False`:

```bash
kubectl get gateway -n envoy-gateway-system nebari-gateway \
  -o jsonpath='{range .status.listeners[*]}{.name}: {range .conditions[?(@.type=="Programmed")]}{.status} {.message}{end}{"\n"}{end}'
```

**Likely cause:** The `nebari-llm-shared-tls` Certificate in the pack namespace has not yet reached `Ready=True`, so the gateway listener has no TLS secret to reference. On fresh installs this is often a cert-manager ACME HTTP-01 challenge race when multiple certs are issued simultaneously.

**Fix:**

Check certificate status across all namespaces:

```bash
kubectl get certificate -A
```

All certs listed in the [installation validation checklist](/installation/) must show `Ready=True`. If `nebari-llm-shared-tls` is stuck:

```bash
kubectl describe certificate -n nebari-llm-serving-system nebari-llm-shared-tls
kubectl get certificaterequest,order,challenge -n nebari-llm-serving-system
```

For the `nebari-gateway-cert` in `envoy-gateway-system` stuck due to the HTTP-01 race (tracked as nebari-dev/nebari-infrastructure-core#267), delete it and let ArgoCD recreate it:

```bash
kubectl delete certificate -n envoy-gateway-system nebari-gateway-cert
kubectl get certificate -n envoy-gateway-system nebari-gateway-cert -w
```

NIC must ship with `cert-manager.maxConcurrentChallenges: 1` to prevent ACME solver races on overlapping SANs.

---

## AI Gateway webhook `caBundle` stale - envoy proxy pod fails to create

**Symptom:** Envoy proxy pods cannot be created. ReplicaSet events and the API server log show:

```
failed calling webhook "ai-gateway-controller.envoy-ai-gateway-system.svc.cluster.local":
x509: certificate signed by unknown authority (possibly because of "crypto/rsa: verification
error" while trying to verify candidate authority certificate "ai-gateway-controller-ca")
```

This affects **every** Envoy Gateway managed proxy in the cluster, not just LLM traffic. The
webhook's `objectSelector` matches `app.kubernetes.io/managed-by: envoy-gateway`, so a stale
`caBundle` blocks pod creation for the shared `nebari-gateway` that fronts the landing page,
Keycloak and ArgoCD too.

**Root cause:** In the chart's default (non cert-manager) mode, the *chart* generates the CA and
serving certificate at Helm render time, and the *controller* writes `caBundle` exactly once, at
process start. The chart only reuses an existing certificate when a Helm `lookup` finds the
Secret, and `lookup` is always empty under ArgoCD, which renders without cluster access. So every
render mints a new CA while `caBundle` keeps the old one. Nothing reconciles the difference. The
controller does not generate certificates at all, and no pod rescheduling is required to create
the divergence.

**Why it can look healthy when it is not:** two delays sit between the divergence and any visible
failure.

1. The pod keeps serving the *old* certificate until kubelet refreshes the mounted Secret and
   `certwatcher` picks it up on its 10s poll, measured at 24s on AI Gateway v0.5.0. Until then the old certificate
   still matches the old `caBundle` and everything works.
2. Certificates are validated at TLS handshake, not per request, and the API server reuses its
   webhook connection. A diverged cluster can serve normally for as long as that connection
   lives. Measured on AI Gateway v0.5.0: over three minutes with zero errors.

The failure then appears all at once, when something forces a fresh handshake: the controller pod
restarting or being rescheduled, the API server restarting, or the connection idling out. This is
why the problem is often noticed after a node replacement or a cluster upgrade, and why it can lie
dormant for days.

**Do not diagnose this by comparing checksums.** The Secret's `ca.crt` and the webhook's
`caBundle` can differ while the webhook is perfectly healthy, and can look fine immediately after
a re-render because no handshake has happened yet. The reverse is also true and was measured on a
live cluster: during certificate rotation, the Secret and the webhook can both already hold the
new CA and therefore agree, while the pod is still serving its old certificate, so calls fail
anyway. Agreement is not evidence of health. Use the probe below, which is the only check that
exercises the real trust path.

**Check the trust chain:**

```bash
kubectl create -n envoy-gateway-system --dry-run=server -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  generateName: aigw-webhook-probe-
  labels:
    app.kubernetes.io/managed-by: envoy-gateway
    gateway.envoyproxy.io/owning-gateway-name: nebari-gateway
    gateway.envoyproxy.io/owning-gateway-namespace: envoy-gateway-system
spec:
  containers:
    - name: probe
      image: registry.k8s.io/pause:3.9
EOF
```

A healthy webhook returns the pod definition with an `ai-gateway-extproc` container injected. A
broken one returns the x509 error above. Nothing is created either way.

**Fix: migrate to cert-manager mode.** This is a supported branch of the same chart. cert-manager
issues the certificate and `cainjector` keeps `caBundle` in sync continuously, so re-rendering
becomes a no-op. Nebari Infrastructure Core installs cert-manager and cainjector by default. Add
to the `envoy-ai-gateway` ArgoCD Application:

```yaml
  source:
    helm:
      values: |
        controller:
          mutatingWebhook:
            certManager:
              enable: true
```

Expect on sync:

- The Secret is briefly deleted and recreated, measured at about 1 second. This is unavoidable:
  Secret `type` is immutable, the chart renders it as `Opaque`, and cert-manager requires
  `kubernetes.io/tls`. The controller does not restart and there is no observable impact.
- If you are migrating from an already-broken state, allow about **80 seconds** before the probe
  passes, even though the `Certificate` reports `Ready` within a few seconds. The remainder is
  the pod picking up the new certificate and the API server making a fresh handshake. This is
  normal. Do not intervene.

There is no need to strip ArgoCD tracking metadata from the old Secret. cert-manager recreates the
object, and the replacement carries no ArgoCD tracking.

**Known residual:** at certificate renewal, `cainjector` updates `caBundle` immediately while the
pod keeps serving the previous certificate until its mount refreshes, so pod CREATEs fail for a
short window. Measured at **43 and 56 seconds** on AI Gateway v1.1.0, self-recovering with no intervention. cert-manager
defaults give a 90 day lifetime with renewal at 60 days, so expect this roughly every two months.
The exact duration is bounded by kubelet's mount refresh and will vary. Upstream:
[cert-manager/cert-manager#6350](https://github.com/cert-manager/cert-manager/issues/6350).

### Emergency stopgap for installs still in self-signed mode

Neither of these is a fix. Both are undone by the next render. Use them to restore ingress, then
migrate to cert-manager mode.

Copy the live CA into the webhook configuration:

```bash
CA=$(kubectl get secret -n envoy-ai-gateway-system \
  self-signed-cert-for-mutating-webhook -o jsonpath='{.data.ca\.crt}')
kubectl patch mutatingwebhookconfiguration \
  envoy-ai-gateway-gateway-pod-mutator.envoy-ai-gateway-system \
  --type='json' \
  -p="[{\"op\":\"replace\",\"path\":\"/webhooks/0/clientConfig/caBundle\",\"value\":\"$CA\"}]"
```

Or restart the controller, which re-runs its one-shot startup patch against whatever certificate
is currently mounted:

```bash
kubectl rollout restart deploy -n envoy-ai-gateway-system ai-gateway-controller
kubectl rollout status deploy -n envoy-ai-gateway-system ai-gateway-controller
```

Re-run the probe above to confirm recovery. Both forms restore service, and live-cluster
validation showed the restart form working reliably across three consecutive restarts. Prefer the
direct patch regardless: it is the narrower action, and it does not take the webhook offline while
the controller pod is replaced.

---

## LLMModel not reconciling - operator logs show errors

**Symptom:** `kubectl get llmmodel -n nebari-llm-serving-system` shows the model stuck in `Pending` or `Degraded` phase after more than a few minutes, and no child resources (HTTPRoute, InferencePool, SecurityPolicy) appear.

**Likely cause:** The operator cannot reconcile the LLMModel CR due to a missing dependency (Keycloak unreachable, missing Secret, invalid field value) or because the LLMModel was applied to the wrong namespace (the validating webhook rejects CRs created outside the operator namespace).

**Fix:**

Check operator logs for the reconciliation error:

```bash
kubectl logs -n nebari-llm-serving-system \
  deploy/nebari-llm-serving-operator --tail=50
```

(The pack's own operator - `nebari-llm-serving-operator` in `nebari-llm-serving-system` - reconciles LLMModels. This is distinct from the upstream Nebari Operator / NIC in `nebari-operator-system`, which only provisions NebariApps.)

Common operator log errors and their fixes:

- `keycloak: connection refused` or `keycloak: 401` - the operator cannot reach Keycloak. Verify the `KEYCLOAK_URL` environment variable and that the `keycloak` namespace pods are healthy:

  ```bash
  kubectl get deploy -n nebari-llm-serving-system nebari-llm-serving-operator \
    -o jsonpath='{range .spec.template.spec.containers[0].env[*]}{.name}={.value}{"\n"}{end}' \
    | grep KEYCLOAK
  ```

  Expected: `KEYCLOAK_ENABLED=true`, `KEYCLOAK_URL=http://keycloak-keycloakx-http.keycloak.svc.cluster.local:8080`, `KEYCLOAK_REALM=nebari`.

- `LLMModel namespace rejected` - the CR was applied outside the operator namespace. The validating webhook only accepts LLMModels in the pack namespace. Reapply to `nebari-llm-serving-system` (or whichever namespace your pack is installed in).

- `secret not found` - `spec.model.authSecretName` references a Secret that does not exist in the operator namespace. Create it before reapplying the LLMModel.

Check the LLMModel status conditions for a machine-readable message:

```bash
kubectl get llmmodel -n nebari-llm-serving-system <model-name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
```

See the [configuration reference](/configuration/) for valid LLMModel field values.

---

## `direct_response: 500` on `/v1/chat/completions`

**Symptom:** POST requests to the external or internal endpoint return a 500 response body of `{"error": "direct_response: 500"}` immediately - the request never reaches vLLM.

**Likely cause:** The Envoy proxy is returning a synthetic 500 before the AI Gateway extension processor handles the request. This usually means `extensionApis.enableEnvoyPatchPolicy` or `extensionApis.enableBackend` is not set in the Envoy Gateway configuration, so the AI Gateway route cannot attach its filter chain.

**Fix:**

Verify the Envoy Gateway ConfigMap has both extension API flags:

```bash
kubectl get configmap -n envoy-gateway-system envoy-gateway-config \
  -o jsonpath='{.data.envoy-gateway\.yaml}' | grep -A5 extensionApis
```

Expected:

```yaml
extensionApis:
  enableEnvoyPatchPolicy: true
  enableBackend: true
```

If either flag is missing, update the ConfigMap (or the ArgoCD Application values) and restart the Envoy Gateway controller:

```bash
kubectl rollout restart deploy -n envoy-gateway-system envoy-gateway
```

---

## Endpoint stuck in `RoutePending` or `RouteNotAccepted`

An endpoint condition is `True` only once the Gateway accepted the route. `RoutePending` (`Unknown`, rechecked every 5 seconds, phase unchanged) is normal for a few seconds while Envoy Gateway catches up. After 2 minutes without acceptance the condition becomes `RouteNotAccepted` (`False`, retried every minute), and its message says what is missing, with Envoy Gateway's reason when it gave one. The phase becomes `Error` on a PassthroughModel and `Degraded` on an LLMModel that was `Ready`. While an LLMModel's `SecurityPoliciesReady` is pending, the recheck runs every 15 seconds. Read the condition, then the route's status, and list the Gateways:

```bash
kubectl -n nebari-llm-serving-system get llmmodel <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
# for a PassthroughModel use: get passthroughmodel <name>
kubectl -n nebari-llm-serving-system get httproute <name>-<endpoint> -o jsonpath='{.status.parents}'
kubectl get gateway -A
```

`<endpoint>` is `external` or `internal`. Match the `RouteNotAccepted` message:

| Message starts with | Meaning | Check |
|---|---|---|
| `AI Gateway has not generated HTTPRoute <ns>/<name>` (then `; AI Gateway: <msg>` when the AIGatewayRoute reports one) | The AIGatewayRoute exists but no HTTPRoute was created from it. | The AI Gateway controller's logs and the AIGatewayRoute's status, which the appended message quotes. |
| `Gateway <ns>/<name> listener <section> reported no status for the route` | The HTTPRoute exists but the Gateway wrote no status for it. | That the Gateway exists and Envoy Gateway is running. |
| `Gateway <ns>/<name> listener <section> did not accept the route: <Reason>: <Message>` | The Gateway rejected the route, for example `NoMatchingParent`. | Fix what the reason names, such as the listener. |
| `generated HTTPRoute <ns>/<name> does not reflect the applied route or was not re-evaluated by Gateway <ns>/<name> listener <section>` | The HTTPRoute is out of date with the AIGatewayRoute, or the Gateway has not re-evaluated it after a change. | The AI Gateway and Envoy Gateway controllers' logs. |

An empty `status.parents` means no Gateway reported on the route, usually because the Gateway named in `platform.gateway` does not exist. `NoMatchingParent` means the Gateway exists but has no listener with the expected name. Fix the Gateway or the chart's `platform.gateway` values and the next retry clears the condition.

`RouteStatusUnreadable` (`Unknown`, retried every 15 seconds, phase unchanged on both kinds) means the operator could not read the HTTPRoute. A persistent read error other than a missing CRD, for example RBAC forbidding `get httproutes`, keeps the endpoint at `Unknown/RouteStatusUnreadable` and the phase at `Ready`. If the endpoint was already `RouteNotAccepted`, it stays `False` with `; last status read failed: <err>` appended to the message. Check the operator's RBAC for `get` on `httproutes.gateway.networking.k8s.io`.

---

## External provider (PassthroughModel) not working

**Upstream 401 / auth errors from an OpenAI-compatible provider.** The gateway injects the key from `spec.provider.credentialSecretName`. Inspect the credential condition first:

```bash
kubectl -n nebari-llm-serving-system get passthroughmodel <name> \
  -o jsonpath='{range .status.conditions[?(@.type=="UpstreamCredentialResolved")]}{.status} {.reason}: {.message}{"\n"}{end}'
```

`SecretNotFound` means the referenced Secret does not exist in the PassthroughModel's namespace. `APIKeyMissing` means the Secret exists but its `apiKey` entry is absent or empty. `LookupFailed` indicates a Kubernetes API or cache read error; the operator retries after one minute. Inspect the condition message and operator health if it persists. Create or update the Secret with that exact key name; the operator watches referenced credential Secrets and refreshes the condition automatically. `Resolved` confirms only that the entry is non-empty. If the provider still returns 401, replace an invalid, expired, or revoked key.

**Upstream auth errors from Bedrock (SigV4 signing failures, `AccessDeniedException`, `UnrecognizedClientException`).** A Bedrock provider authenticates with workload identity; there is no upstream credential Secret to check (the webhook rejects `credentialSecretName` on workload identity), so `UpstreamCredentialResolved` is omitted. The AWS identity belongs to the Envoy data-plane pod running the AI Gateway `extproc` container, not the operator. Check the EKS Pod Identity association or IRSA annotation on the data-plane ServiceAccount, that the role grants `bedrock:InvokeModel` / `bedrock:InvokeModelWithResponseStream` for the declared models, and that `spec.provider.backend.bedrock.region` matches where the models are enabled:

```bash
aws eks list-pod-identity-associations --cluster-name <cluster>
kubectl -n envoy-gateway-system get sa \
  -o custom-columns='NAME:.metadata.name,ROLE:.metadata.annotations.eks\.amazonaws\.com/role-arn'
```

The `<name>-api-keys` Secret is the key-manager's user API-key store, not an upstream credential; a 401 issued by the gateway itself points at the user's minted key, not the AWS identity.

**Upstream connection fails (HTTP 503, `upstream connect error or disconnect/reset before headers`).** A 503 alone does not establish a certificate problem. Envoy hides the detailed transport failure from the response body by default. Check the Envoy proxy access logs for `CERTIFICATE_VERIFY_FAILED` in the response-code details to confirm a TLS certificate verification failure:

```bash
kubectl -n envoy-gateway-system logs \
  -l gateway.envoyproxy.io/owning-gateway-name=<gateway> \
  -c envoy --since=10m --tail=-1 \
  | grep CERTIFICATE_VERIFY_FAILED
```

`<gateway>` is the Gateway the request came in through, named in the chart's `platform.gateway` values: by default `nebari-gateway` for both `llm.<baseDomain>` and `llm-internal.<baseDomain>`. Keep `--tail=-1`: with a label selector, `kubectl logs` otherwise prints only the last 10 lines of each pod.

By default the gateway accepts only the public CA roots in the Envoy proxy image, so a certificate from a private or corporate CA needs `spec.provider.tls.caConfigMapName` (see [Self-hosted servers](/self-hosted/)). When that field is set, check the policy status:

```bash
kubectl -n nebari-llm-serving-system get backendtlspolicy <name>-backend-tls \
  -o jsonpath='{range .status.ancestors[*].conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
```

`NoValidCACertificate` / `InvalidCACertificateRef` means the ConfigMap is missing from the PassthroughModel's namespace, or has neither a `ca.crt` key nor exactly one key. If the policy is accepted, inspect the certificate the server presents:

```bash
openssl s_client -connect <hostname>:<port> -servername <hostname> </dev/null 2>/dev/null \
  | openssl x509 -noout -text | grep -E 'Issuer:|Subject:|DNS:|Signature Algorithm'
```

The certificate must list `spec.provider.hostname` in its subject alternative names and chain to a CA in the ConfigMap.

**A CA bundle update does not take effect, even though the policy is accepted.** `Accepted=True` and `ResolvedRefs=True` on the `BackendTLSPolicy` do not prove Envoy loaded the CA certificates. With Envoy Gateway 1.8.1, malformed PEM can pass those checks while Envoy rejects the TLS configuration with `Failed to load trusted CA certificates from <inline>`:

```bash
kubectl -n envoy-gateway-system logs \
  -l gateway.envoyproxy.io/owning-gateway-name=<gateway> \
  -c envoy --since=10m --tail=-1 \
  | grep -E 'Failed to load trusted CA certificates|CERTIFICATE_VERIFY_FAILED|Secret is not supplied by SDS'
```

If the bundle previously worked, Envoy can retain its last valid trust configuration, so requests may still succeed after an invalid update. A newly referenced invalid bundle can instead fail requests with HTTP 503. Correct the PEM in the ConfigMap, check for new rejected-update messages, and verify a request against the server presenting the new certificate. Valid ConfigMap updates are picked up without restarting Envoy.

**PassthroughModel stuck with `GatewayCRDUnavailable`.** The Envoy AI Gateway CRDs are not installed; the operator requeues every 15 seconds rather than failing outright. `ApplyFailed` means the CRDs exist but the write itself failed. Check the CR and the CRDs:

```bash
kubectl -n nebari-llm-serving-system get passthroughmodel <name> \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} {.reason}: {.message}{"\n"}{end}'
kubectl get crd | grep aigateway
```

Install the AI Gateway (see [Installation](/installation/)) and the next reconcile clears the condition.

**A model id you expected to hit the provider is served locally instead.** Per-LLMModel routes match Host and `x-ai-eg-model`, so they always win over a catch-all PassthroughModel. If a declared provider model id collides with a served model name, the served model wins. Rename or remove the conflicting LLMModel, or declare the provider model under a distinct id.

```bash
# If the model id you expected to reach the provider appears here, that
# LLMModel's route wins over the catch-all. Rename or remove the LLMModel,
# or declare the provider model under a different id.
kubectl get llmmodels -n nebari-llm-serving-system
```
