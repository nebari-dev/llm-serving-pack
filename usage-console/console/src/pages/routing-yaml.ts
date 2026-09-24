import type { Backend, Route } from '@/data/mock'

// Generated config for the "View generated YAML" / Export affordances (§7.5.6).
// CRD kinds appear here and only here (§7.3).

export interface BackendSpec {
  endpoint: string
  timeout: string
  maxRetries: number
  replicas?: number
}

export const backendSpecs: Record<string, BackendSpec> = {
  'openai-prod': { endpoint: 'https://api.openai.com/v1', timeout: '60s', maxRetries: 2 },
  'anthropic-prod': { endpoint: 'https://api.anthropic.com', timeout: '120s', maxRetries: 1 },
  'bedrock-eu': { endpoint: 'bedrock-runtime.eu-central-1.amazonaws.com', timeout: '90s', maxRetries: 2 },
  'vllm-internal': { endpoint: 'http://vllm.inference.svc.cluster.local:8000', timeout: '30s', maxRetries: 3, replicas: 4 },
  'azure-openai-eu': { endpoint: 'https://acme-eu.openai.azure.com', timeout: '60s', maxRetries: 2 },
}

const schemaFor: Record<string, string> = {
  OpenAI: 'OpenAI',
  Anthropic: 'Anthropic',
  Bedrock: 'AWSBedrock',
  Azure: 'AzureOpenAI',
  'Self-hosted': 'OpenAI',
}

export function backendYaml(b: Backend, spec: BackendSpec = backendSpecs[b.name]) {
  const manager = b.provenance === 'git' ? 'argocd-controller' : 'nebari-gateway-console'
  return `apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIServiceBackend
metadata:
  name: ${b.name}
  namespace: nebari-gateway
  labels:
    gateway.nebari.dev/region: ${b.region}
  # field manager: ${manager}
spec:
  schema:
    name: ${schemaFor[b.provider] ?? 'OpenAI'}
  backendRef:
    name: ${b.name}
    kind: Backend
    group: gateway.envoyproxy.io
  timeouts:
    request: ${spec.timeout}
  retry:
    numRetries: ${spec.maxRetries}${spec.replicas !== undefined ? `\n  # upstream Deployment replicas\n  replicas: ${spec.replicas}` : ''}
---
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: Backend
metadata:
  name: ${b.name}
  namespace: nebari-gateway
spec:
  endpoints:
    - fqdn:
        hostname: ${spec.endpoint.replace(/^https?:\/\//, '').split(/[/:]/)[0]}
        port: ${spec.endpoint.startsWith('http://') ? 8000 : 443}
---
apiVersion: aigateway.envoyproxy.io/v1beta1
kind: BackendSecurityPolicy
metadata:
  name: ${b.name}-credentials
  namespace: nebari-gateway
spec:
  targetRefs:
    - group: aigateway.envoyproxy.io
      kind: AIServiceBackend
      name: ${b.name}
  type: ${b.provider === 'Bedrock' ? 'AWSCredentials' : 'APIKey'}
  ${b.provider === 'Bedrock' ? 'awsCredentials:\n    region: eu-central-1\n    oidcExchangeToken: {}' : `apiKey:\n    secretRef:\n      name: ${b.name}-provider-key`}
`
}

/** Unified diff between two specs of the same backend, as consumed by DiffView. */
export function backendDiff(b: Backend, before: BackendSpec, after: BackendSpec) {
  const a = backendYaml(b, before).split('\n')
  const c = backendYaml(b, after).split('\n')
  const out: string[] = []
  const n = Math.max(a.length, c.length)
  for (let i = 0; i < n; i++) {
    if (a[i] === c[i]) out.push(' ' + (a[i] ?? ''))
    else {
      if (a[i] !== undefined) out.push('-' + a[i])
      if (c[i] !== undefined) out.push('+' + c[i])
    }
  }
  // Trim to a readable hunk: keep 3 lines of context around changes.
  const keep = new Set<number>()
  out.forEach((l, i) => {
    if (l[0] !== ' ') for (let j = i - 3; j <= i + 3; j++) keep.add(j)
  })
  return out.filter((_, i) => keep.has(i)).join('\n')
}

export function routeYaml(r: Route) {
  return `apiVersion: aigateway.envoyproxy.io/v1beta1
kind: AIGatewayRoute
metadata:
  name: ${r.name}
  namespace: nebari-gateway
spec:
  parentRefs:
    - name: nebari-gateway
      kind: Gateway
  rules:
    - matches:
        # ${r.match}
        - headers:
            - type: Exact
              name: x-ai-eg-model
              value: ${r.targets[0]?.model}
      backendRefs:
${r.targets.map((t) => `        - name: ${t.backend}\n          weight: ${t.weight}`).join('\n')}${
    r.fallback.length
      ? `\n      # fallback, in priority order\n${r.fallback.map((f, i) => `        - name: ${f}\n          priority: ${i + 1}`).join('\n')}`
      : ''
  }
`
}
