// Seeded synthetic data for the demo tenant (spec §7.5.1). Everything here is
// fabricated and deterministic so screens render the same way on every load.

export type Verdict = 'allowed' | 'redacted' | 'rerouted' | 'blocked' | 'truncated'
export type InboundVerdict = 'allowed' | 'stripped' | 'blocked'
export type RouteReason = 'alias' | 'policy' | 'fallback' | 'explicit'
export type Provenance = 'console' | 'git' | 'adopted'
export type SyncState = 'synced' | 'applying' | 'failed' | 'drift'

export interface Team {
  id: string
  name: string
  costCenter: string
}

export interface ApiKey {
  id: string
  name: string
  prefix: string
  team: string
  project: string
  allowedModels: string[]
  allowedRegions: string[]
  budgetId?: string
  expiresAt: string | null
  lastUsed: string
  requests24h: number
  status: 'active' | 'revoked' | 'rotating'
}

export interface Model {
  id: string
  display: string
  provider: string
  family: string
  context: number
  inPerM: number
  outPerM: number
  cachedPerM: number
  reasoningPerM: number
}

export interface TraceStep {
  step: string
  input: string
  outcome: string
  ms: number
  state: 'ok' | 'warn' | 'fail' | 'skip'
}

export interface RuleEval {
  ruleId: string
  name: string
  version: number
  matched: boolean
  action: string
  ms: number
}

export interface Receipt {
  id: string
  traceId: string
  sessionId?: string
  ts: number
  durationMs: number
  ttftMs?: number
  keyId: string
  keyName: string
  team: string
  project: string
  actor?: string
  requestedModel: string
  resolvedModel: string
  backend: string
  provider: string
  region: string
  routeReason: RouteReason
  fallbackFrom?: string
  inputTokens: number
  cachedInputTokens: number
  outputTokens: number
  reasoningTokens: number
  costUsd: number
  verdict: Verdict
  inboundVerdict: InboundVerdict
  redactions: { type: string; count: number }[]
  rules: RuleEval[]
  status: number
  errorCode?: string
  errorDetail?: string
  requestHash: string
  responseHash: string
  contentCaptured: boolean
  inFlight?: boolean
  trace: TraceStep[]
}

// ---- deterministic PRNG -------------------------------------------------

function mulberry32(seed: number) {
  return () => {
    seed |= 0
    seed = (seed + 0x6d2b79f5) | 0
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export const rand = mulberry32(20260924)
const pick = <T,>(arr: readonly T[], r = rand) => arr[Math.floor(r() * arr.length)]
const hex = (n: number, r = rand) =>
  Array.from({ length: n }, () => Math.floor(r() * 16).toString(16)).join('')

// ---- catalog ------------------------------------------------------------

export const teams: Team[] = [
  { id: 'support', name: 'Support', costCenter: 'CC-4100' },
  { id: 'agents', name: 'Agents platform', costCenter: 'CC-4230' },
  { id: 'batch', name: 'Data batch', costCenter: 'CC-5010' },
  { id: 'web', name: 'Web app', costCenter: 'CC-4100' },
  { id: 'research', name: 'Research', costCenter: 'CC-7000' },
  { id: 'security', name: 'Security', costCenter: 'CC-9100' },
]

export const models: Model[] = [
  { id: 'gpt-5-mini', display: 'GPT-5 mini', provider: 'OpenAI', family: 'gpt-5', context: 400_000, inPerM: 0.25, outPerM: 2.0, cachedPerM: 0.025, reasoningPerM: 2.0 },
  { id: 'gpt-5.5', display: 'GPT-5.5', provider: 'OpenAI', family: 'gpt-5', context: 400_000, inPerM: 1.25, outPerM: 10.0, cachedPerM: 0.125, reasoningPerM: 10.0 },
  { id: 'claude-sonnet-5', display: 'Claude Sonnet 5', provider: 'Anthropic', family: 'claude', context: 1_000_000, inPerM: 3.0, outPerM: 15.0, cachedPerM: 0.3, reasoningPerM: 15.0 },
  { id: 'claude-opus-4-1', display: 'Claude Opus 4.1', provider: 'Anthropic', family: 'claude', context: 200_000, inPerM: 15.0, outPerM: 75.0, cachedPerM: 1.5, reasoningPerM: 75.0 },
  { id: 'claude-haiku-4-5', display: 'Claude Haiku 4.5', provider: 'Bedrock', family: 'claude', context: 200_000, inPerM: 1.0, outPerM: 5.0, cachedPerM: 0.1, reasoningPerM: 5.0 },
  { id: 'llama-3.3-70b', display: 'Llama 3.3 70B', provider: 'Self-hosted', family: 'llama', context: 128_000, inPerM: 0.12, outPerM: 0.3, cachedPerM: 0.12, reasoningPerM: 0.3 },
]

export const modelById = Object.fromEntries(models.map((m) => [m.id, m]))

export interface Backend {
  name: string
  provider: string
  region: string
  provenance: Provenance
  sync: SyncState
  source?: string
  models: string[]
  health: 'healthy' | 'degraded' | 'down'
  p50: number
  errorRate: number
  captureContent?: boolean
}

export const backends: Backend[] = [
  { name: 'openai-prod', provider: 'OpenAI', region: 'us-east', provenance: 'console', sync: 'synced', models: ['gpt-5-mini', 'gpt-5.5'], health: 'healthy', p50: 412, errorRate: 0.2 },
  { name: 'anthropic-prod', provider: 'Anthropic', region: 'us-east', provenance: 'console', sync: 'applying', models: ['claude-sonnet-5', 'claude-opus-4-1'], health: 'degraded', p50: 980, errorRate: 3.1 },
  { name: 'bedrock-eu', provider: 'Bedrock', region: 'eu-central', provenance: 'git', sync: 'synced', source: 'github.com/acme/platform-gitops/blob/main/gateway/backends/bedrock-eu.yaml', models: ['claude-haiku-4-5', 'claude-sonnet-5'], health: 'healthy', p50: 640, errorRate: 0.4 },
  { name: 'vllm-internal', provider: 'Self-hosted', region: 'eu-private', provenance: 'git', sync: 'drift', source: 'github.com/acme/platform-gitops/blob/main/gateway/backends/vllm-internal.yaml', models: ['llama-3.3-70b'], health: 'healthy', p50: 220, errorRate: 0.1, captureContent: true },
  { name: 'azure-openai-eu', provider: 'Azure', region: 'eu-west', provenance: 'adopted', sync: 'failed', models: ['gpt-5-mini'], health: 'down', p50: 0, errorRate: 100 },
]

export interface Route {
  name: string
  match: string
  targets: { model: string; backend: string; weight: number }[]
  fallback: string[]
  provenance: Provenance
  sync: SyncState
  captureContent?: boolean
}

export const routes: Route[] = [
  { name: 'default', match: 'model = *', targets: [{ model: 'claude-sonnet-5', backend: 'anthropic-prod', weight: 100 }], fallback: ['bedrock-eu', 'openai-prod'], provenance: 'console', sync: 'synced' },
  { name: 'cheap-summarize', match: 'model = summarize-*', targets: [{ model: 'gpt-5-mini', backend: 'openai-prod', weight: 100 }], fallback: ['vllm-internal'], provenance: 'console', sync: 'synced' },
  { name: 'eu-private', match: 'header x-data-region = eu', targets: [{ model: 'llama-3.3-70b', backend: 'vllm-internal', weight: 80 }, { model: 'claude-haiku-4-5', backend: 'bedrock-eu', weight: 20 }], fallback: [], provenance: 'git', sync: 'drift', captureContent: true },
  { name: 'research-frontier', match: 'key.team = research', targets: [{ model: 'claude-opus-4-1', backend: 'anthropic-prod', weight: 100 }], fallback: ['openai-prod'], provenance: 'console', sync: 'applying' },
]

export const keys: ApiKey[] = [
  { id: 'k1', name: 'support-bot', prefix: 'ngw_live_7f3a', team: 'support', project: 'helpdesk', allowedModels: ['gpt-5-mini', 'claude-sonnet-5'], allowedRegions: ['us-east', 'eu-central'], budgetId: 'b1', expiresAt: '2027-03-01', lastUsed: '12s ago', requests24h: 18_240, status: 'active' },
  { id: 'k2', name: 'agents-prod', prefix: 'ngw_live_c19e', team: 'agents', project: 'orchestrator', allowedModels: ['claude-sonnet-5', 'claude-opus-4-1', 'gpt-5.5'], allowedRegions: ['us-east'], budgetId: 'b2', expiresAt: '2026-12-31', lastUsed: '3s ago', requests24h: 9_812, status: 'active' },
  { id: 'k3', name: 'batch-summarize', prefix: 'ngw_live_02bd', team: 'batch', project: 'nightly-digest', allowedModels: ['gpt-5-mini', 'claude-opus-4-1', 'llama-3.3-70b'], allowedRegions: ['us-east', 'eu-private'], budgetId: 'b3', expiresAt: '2026-11-15', lastUsed: '41s ago', requests24h: 4_406, status: 'active' },
  { id: 'k4', name: 'web-chat', prefix: 'ngw_live_9a0c', team: 'web', project: 'assistant', allowedModels: ['gpt-5-mini', 'claude-haiku-4-5'], allowedRegions: ['us-east'], budgetId: 'b4', expiresAt: '2027-01-20', lastUsed: '1s ago', requests24h: 22_019, status: 'rotating' },
  { id: 'k5', name: 'research', prefix: 'ngw_live_e55f', team: 'research', project: 'evals', allowedModels: ['claude-opus-4-1', 'gpt-5.5', 'claude-sonnet-5'], allowedRegions: ['us-east'], expiresAt: null, lastUsed: '6m ago', requests24h: 1_204, status: 'active' },
  { id: 'k6', name: 'secops-triage', prefix: 'ngw_live_41d2', team: 'security', project: 'soc', allowedModels: ['llama-3.3-70b', 'claude-haiku-4-5'], allowedRegions: ['eu-private', 'eu-central'], expiresAt: '2026-10-02', lastUsed: '2h ago', requests24h: 88, status: 'active' },
  { id: 'k7', name: 'legacy-intranet', prefix: 'ngw_live_77aa', team: 'web', project: 'intranet', allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2026-08-30', lastUsed: '26d ago', requests24h: 0, status: 'revoked' },
]

export const keyById = Object.fromEntries(keys.map((k) => [k.id, k]))

export interface Budget {
  id: string
  scope: string
  scopeType: 'team' | 'key' | 'project'
  period: 'monthly'
  capUsd: number
  currentUsd: number
  onExceed: 'warn' | 'throttle' | 'block'
  projectedUsd: number
}

export const budgets: Budget[] = [
  { id: 'b1', scope: 'support', scopeType: 'team', period: 'monthly', capUsd: 12_000, currentUsd: 13_480.22, onExceed: 'throttle', projectedUsd: 16_950 },
  { id: 'b2', scope: 'agents', scopeType: 'team', period: 'monthly', capUsd: 40_000, currentUsd: 33_104.9, onExceed: 'block', projectedUsd: 42_600 },
  { id: 'b3', scope: 'batch-summarize', scopeType: 'key', period: 'monthly', capUsd: 8_000, currentUsd: 3_412.07, onExceed: 'warn', projectedUsd: 4_420 },
  { id: 'b4', scope: 'web', scopeType: 'team', period: 'monthly', capUsd: 15_000, currentUsd: 9_870.5, onExceed: 'block', projectedUsd: 12_760 },
  { id: 'b5', scope: 'research', scopeType: 'team', period: 'monthly', capUsd: 20_000, currentUsd: 17_210.0, onExceed: 'warn', projectedUsd: 22_300 },
]

// ---- policies -----------------------------------------------------------

export interface PolicyRule {
  id: string
  ordinal: number
  name: string
  description: string
  mode: 'enforce' | 'monitor' | 'draft'
  failMode: 'open' | 'closed'
  version: number
  when: { field: string; op: string; value: string[] }[]
  then: { action: string; detail: string }[]
  fired24h: number
  baseline7d: number
}

export const rules: PolicyRule[] = [
  {
    id: 'r1', ordinal: 1, name: 'no-pii-out', description: 'Redact customer identifiers before they leave the perimeter.',
    mode: 'enforce', failMode: 'closed', version: 7,
    when: [{ field: 'prompt', op: 'contains entity', value: ['email', 'SSN'] }, { field: 'team', op: 'is not', value: ['security'] }],
    then: [{ action: 'redact', detail: 'email, SSN · rehydrate on return' }, { action: 'route to', detail: 'eu-private' }],
    fired24h: 1_204, baseline7d: 1_130,
  },
  {
    id: 'r2', ordinal: 2, name: 'eu-only', description: 'EU customer traffic must stay in EU regions.',
    mode: 'enforce', failMode: 'closed', version: 3,
    when: [{ field: 'header x-data-region', op: 'equals', value: ['eu'] }],
    then: [{ action: 'route to', detail: 'eu-private' }],
    fired24h: 3_880, baseline7d: 3_702,
  },
  {
    id: 'r3', ordinal: 3, name: 'block-src', description: 'Block proprietary source code and secrets from third-party providers.',
    mode: 'enforce', failMode: 'closed', version: 12,
    when: [{ field: 'prompt', op: 'contains entity', value: ['secret', 'private key', 'source code'] }, { field: 'provider', op: 'is not', value: ['Self-hosted'] }],
    then: [{ action: 'block', detail: 'return 403 with rule id' }],
    fired24h: 96, baseline7d: 14,
  },
  {
    id: 'r4', ordinal: 4, name: 'card-numbers', description: 'Luhn-validated card numbers are redacted everywhere.',
    mode: 'monitor', failMode: 'closed', version: 1,
    when: [{ field: 'prompt', op: 'contains entity', value: ['credit card'] }],
    then: [{ action: 'redact', detail: 'credit card · no rehydrate' }],
    fired24h: 22, baseline7d: 19,
  },
  {
    id: 'r5', ordinal: 5, name: 'cost-guard-opus', description: 'Downgrade long-context batch jobs off Opus.',
    mode: 'enforce', failMode: 'open', version: 2,
    when: [{ field: 'model', op: 'equals', value: ['claude-opus-4-1'] }, { field: 'team', op: 'is', value: ['batch'] }],
    then: [{ action: 'route to', detail: 'gpt-5-mini' }],
    fired24h: 312, baseline7d: 290,
  },
]

export const detectors = [
  { id: 'email', name: 'Email address', kind: 'Built-in · regex', threshold: 0.99, hits24h: 842, fp: 3 },
  { id: 'ssn', name: 'US SSN', kind: 'Built-in · regex + checksum', threshold: 0.95, hits24h: 61, fp: 0 },
  { id: 'person', name: 'Person name', kind: 'Built-in · NER', threshold: 0.82, hits24h: 2_310, fp: 41 },
  { id: 'card', name: 'Credit card', kind: 'Built-in · Luhn', threshold: 1.0, hits24h: 22, fp: 1 },
  { id: 'secret', name: 'API secret', kind: 'Built-in · entropy', threshold: 0.9, hits24h: 88, fp: 12 },
  { id: 'src', name: 'Source code', kind: 'Built-in · classifier', threshold: 0.75, hits24h: 31, fp: 6 },
  { id: 'acct', name: 'Acme account ID', kind: 'Custom · regex  ACME-\\d{8}', threshold: 1.0, hits24h: 407, fp: 0 },
]

// ---- receipts -----------------------------------------------------------

const keyWeights: [string, number][] = [
  ['k1', 30], ['k2', 22], ['k3', 10], ['k4', 30], ['k5', 5], ['k6', 3],
]

function weightedKey(r: () => number) {
  const total = keyWeights.reduce((a, [, w]) => a + w, 0)
  let x = r() * total
  for (const [k, w] of keyWeights) {
    if ((x -= w) < 0) return keyById[k]
  }
  return keyById.k1
}

const backendFor = (model: string) =>
  backends.find((b) => b.models.includes(model) && b.health !== 'down') ?? backends[0]

function cost(m: Model, inTok: number, cached: number, out: number, reasoning: number) {
  return ((inTok - cached) * m.inPerM + cached * m.cachedPerM + out * m.outPerM + reasoning * m.reasoningPerM) / 1_000_000
}

const actors = ['u_4412', 'u_0981', 'u_2231', 'svc-digest', 'u_7713', undefined, undefined]
const entityTypes = ['email', 'person', 'SSN', 'phone', 'Acme account ID']

export function makeReceipt(ts: number, r: () => number = rand, opts: { inFlight?: boolean } = {}): Receipt {
  const key = weightedKey(r)
  let requested = pick(key.allowedModels, r)
  if (key.team === 'batch' && r() < 0.5) requested = 'summarize-digest'
  let resolved = requested === 'summarize-digest' ? 'gpt-5-mini' : requested
  let routeReason: RouteReason = requested === 'summarize-digest' ? 'alias' : 'explicit'

  const roll = r()
  let verdict: Verdict = 'allowed'
  if (roll < 0.035) verdict = 'blocked'
  else if (roll < 0.11) verdict = 'redacted'
  else if (roll < 0.15) verdict = 'rerouted'
  else if (roll < 0.157) verdict = 'truncated'

  let fallbackFrom: string | undefined
  let backend = backendFor(resolved)
  if (backend.name === 'anthropic-prod' && r() < 0.08) {
    fallbackFrom = 'anthropic-prod'
    backend = backends.find((b) => b.name === 'bedrock-eu')!
    routeReason = 'fallback'
    if (!backend.models.includes(resolved)) resolved = 'claude-sonnet-5'
  }
  if (verdict === 'rerouted') {
    backend = backends.find((b) => b.name === 'vllm-internal')!
    resolved = 'llama-3.3-70b'
    routeReason = 'policy'
  }
  const model = modelById[resolved]

  const inputTokens = Math.round(200 + r() ** 2 * (key.team === 'batch' ? 40_000 : 9_000))
  const cachedInputTokens = r() < 0.4 ? Math.round(inputTokens * r() * 0.7) : 0
  const outputTokens = Math.round(40 + r() * (key.team === 'agents' ? 2_400 : 900))
  const reasoningTokens = resolved.startsWith('gpt-5') && r() < 0.5 ? Math.round(r() * 3_000) : 0
  const blocked = verdict === 'blocked'
  const durationMs = blocked ? Math.round(18 + r() * 30) : Math.round(backend.p50 * (0.5 + r() * 1.8) + outputTokens * 0.6)
  const stream = r() < 0.55

  const redactions =
    verdict === 'redacted'
      ? [{ type: pick(entityTypes, r), count: 1 + Math.floor(r() * 4) }, ...(r() < 0.4 ? [{ type: 'person', count: 1 + Math.floor(r() * 3) }] : [])]
      : []

  const blockRule = rules[2]
  const ruleEvals: RuleEval[] = rules
    .filter((x) => x.mode !== 'draft')
    .map((x) => {
      const matched =
        (x.id === 'r1' && verdict === 'redacted') ||
        (x.id === 'r2' && verdict === 'rerouted') ||
        (x.id === 'r3' && blocked) ||
        (x.id === 'r5' && key.team === 'batch' && requested === 'claude-opus-4-1')
      return {
        ruleId: x.id,
        name: x.name,
        version: x.version,
        matched,
        action: matched ? (x.mode === 'monitor' ? 'would ' + x.then[0].action : x.then[0].action) : 'no match',
        ms: +(0.1 + r() * 0.9).toFixed(2),
      }
    })
    .slice(0, blocked ? 3 : undefined)

  const warden = ruleEvals.reduce((a, x) => a + x.ms, 0)
  const status = blocked ? 403 : verdict === 'truncated' ? 200 : r() < 0.012 ? 429 : 200
  const c = blocked ? 0 : cost(model, inputTokens, cachedInputTokens, outputTokens, reasoningTokens)

  const trace: TraceStep[] = [
    { step: 'Identity resolved', input: `Bearer ${key.prefix}…`, outcome: `${key.name} → ${key.team} / ${key.project}`, ms: 0.3, state: 'ok' },
    {
      step: 'Budget checked',
      input: key.budgetId ? `budget ${budgets.find((b) => b.id === key.budgetId)?.scope}` : 'no budget attached',
      outcome: key.budgetId ? (key.team === 'support' ? 'over cap · throttle active, admitted' : 'within cap') : 'skipped',
      ms: 0.1,
      state: key.team === 'support' ? 'warn' : key.budgetId ? 'ok' : 'skip',
    },
    {
      step: 'Rules evaluated',
      input: `${ruleEvals.length} rules · policy v${rules[0].version}`,
      outcome: blocked
        ? `blocked by ${blockRule.name} v${blockRule.version} on entity "secret"`
        : verdict === 'redacted'
          ? `redacted ${redactions.map((x) => `${x.count} ${x.type}`).join(', ')}`
          : verdict === 'rerouted'
            ? 'eu-only matched → route to eu-private'
            : 'no rule matched',
      ms: +warden.toFixed(2),
      state: blocked ? 'fail' : verdict === 'allowed' ? 'ok' : 'warn',
    },
    {
      step: 'Route selected',
      input: `requested ${requested}`,
      outcome: blocked ? 'not reached' : `${resolved} via ${backend.name}${fallbackFrom ? ` (fallback from ${fallbackFrom}: 529 overloaded)` : ''}`,
      ms: blocked ? 0 : 0.2,
      state: blocked ? 'skip' : fallbackFrom ? 'warn' : 'ok',
    },
    {
      step: 'Upstream called',
      input: blocked ? '—' : `${backend.provider} · ${backend.region}${stream ? ' · stream' : ''}`,
      outcome: blocked ? 'not reached' : status === 429 ? '429 rate limited by provider' : `${status} · ${outputTokens} output tokens`,
      ms: blocked ? 0 : durationMs - 2,
      state: blocked ? 'skip' : status === 429 ? 'fail' : 'ok',
    },
    {
      step: 'Response inspected',
      input: blocked ? '—' : 'injection · tool calls · exfil URLs',
      outcome: blocked ? 'not reached' : verdict === 'truncated' ? 'exfil URL pattern at token 612 · stream cut' : 'clean',
      ms: blocked ? 0 : 0.4,
      state: blocked ? 'skip' : verdict === 'truncated' ? 'fail' : 'ok',
    },
  ]

  return {
    id: hex(8, r) + '-' + hex(4, r),
    traceId: hex(32, r),
    sessionId: key.team === 'agents' ? 'sess_' + hex(6, r) : undefined,
    ts,
    durationMs,
    ttftMs: stream && !blocked ? Math.round(120 + r() * 400) : undefined,
    keyId: key.id,
    keyName: key.name,
    team: key.team,
    project: key.project,
    actor: pick(actors, r),
    requestedModel: requested,
    resolvedModel: resolved,
    backend: backend.name,
    provider: backend.provider,
    region: backend.region,
    routeReason,
    fallbackFrom,
    inputTokens,
    cachedInputTokens,
    outputTokens: blocked ? 0 : outputTokens,
    reasoningTokens,
    costUsd: c,
    verdict,
    inboundVerdict: verdict === 'truncated' ? 'blocked' : 'allowed',
    redactions,
    rules: ruleEvals,
    status,
    errorCode: blocked ? 'policy_blocked' : status === 429 ? 'upstream_rate_limited' : undefined,
    errorDetail: blocked
      ? `Rule block-src v${blockRule.version} matched entity "secret" in message[2]. Remove the credential from the prompt, or route through a self-hosted backend.`
      : undefined,
    requestHash: 'sha256:' + hex(64, r),
    responseHash: blocked ? '—' : 'sha256:' + hex(64, r),
    contentCaptured: backend.name === 'vllm-internal',
    inFlight: opts.inFlight,
    trace,
  }
}

const NOW = Date.now()
export const now = () => NOW

export const seedReceipts: Receipt[] = Array.from({ length: 240 }, (_, i) =>
  makeReceipt(NOW - i * 1_400 - Math.floor(rand() * 900)),
)

// ---- time series for overview + spend ------------------------------------

export interface SeriesPoint {
  t: number
  allowed: number
  redacted: number
  rerouted: number
  blocked: number
  truncated: number
}

export const trafficSeries: SeriesPoint[] = Array.from({ length: 48 }, (_, i) => {
  const hour = (i / 2 + 8) % 24
  const diurnal = 0.55 + 0.45 * Math.sin(((hour - 6) / 24) * Math.PI * 2)
  const base = Math.round(2_400 * diurnal + rand() * 300)
  const incident = i >= 38 && i <= 41
  return {
    t: NOW - (47 - i) * 30 * 60_000,
    allowed: Math.round(base * 0.85),
    redacted: Math.round(base * 0.075),
    rerouted: Math.round(base * 0.04),
    blocked: Math.round(base * (incident ? 0.09 : 0.03)),
    truncated: Math.round(base * 0.005),
  }
})

export interface SpendPoint {
  day: string
  byTeam: Record<string, number>
}

export const spendSeries: SpendPoint[] = Array.from({ length: 30 }, (_, i) => {
  const d = new Date(NOW - (29 - i) * 86_400_000)
  const weekend = d.getDay() === 0 || d.getDay() === 6
  const surge = i >= 24 ? 2.9 : 1
  return {
    day: d.toISOString().slice(5, 10),
    byTeam: {
      support: (weekend ? 180 : 310) * surge + rand() * 40,
      agents: (weekend ? 900 : 1_250) + rand() * 180,
      batch: 110 + rand() * 30,
      web: (weekend ? 260 : 340) + rand() * 50,
      research: 420 + rand() * 300,
      security: 6 + rand() * 4,
    },
  }
})

export interface Change {
  id: string
  ts: number
  actor: string
  action: string
  target: string
  targetKind: string
  effect?: string
  effectTone?: 'good' | 'bad' | 'neutral'
  source: 'console' | 'git'
}

export const changes: Change[] = [
  { id: 'c1', ts: NOW - 42 * 60_000, actor: 'priya@acme.dev', action: 'Switched route target', target: 'default → claude-sonnet-5', targetKind: 'Route', effect: 'p50 latency −340ms · cost/request −38%', effectTone: 'good', source: 'console' },
  { id: 'c2', ts: NOW - 3.6 * 3_600_000, actor: 'argocd', action: 'Synced from Git', target: 'vllm-internal', targetKind: 'Backend', effect: 'Drift: replicas 4 → 2 · p95 +610ms on eu-private', effectTone: 'bad', source: 'git' },
  { id: 'c3', ts: NOW - 5.1 * 3_600_000, actor: 'marco@acme.dev', action: 'Published rule', target: 'block-src v12', targetKind: 'Policy', effect: 'Blocks 7× baseline · 82 from support', effectTone: 'bad', source: 'console' },
  { id: 'c4', ts: NOW - 9.4 * 3_600_000, actor: 'dana@acme.dev', action: 'Raised budget cap', target: 'agents $32,000 → $40,000', targetKind: 'Budget', effect: 'No throttled requests since', effectTone: 'good', source: 'console' },
  { id: 'c5', ts: NOW - 20 * 3_600_000, actor: 'priya@acme.dev', action: 'Rotated key', target: 'web-chat', targetKind: 'Key', effect: '61% of traffic on new secret', effectTone: 'neutral', source: 'console' },
  { id: 'c6', ts: NOW - 26 * 3_600_000, actor: 'marco@acme.dev', action: 'Published rule in monitor mode', target: 'card-numbers v1', targetKind: 'Policy', effect: 'Would have redacted 22 requests', effectTone: 'neutral', source: 'console' },
]
