// Seeded synthetic data for the demo tenant (mock mode). Everything here is
// fabricated and deterministic so screens render the same way on every load.

/** throttled: refused for now (429 with Retry-After) by a throttle budget over its cap, not blocked. */
export type Verdict = 'allowed' | 'redacted' | 'rerouted' | 'blocked' | 'truncated' | 'throttled'
// skipped: the response wasn't inspected (never reached, or no inspector in the path)
type InboundVerdict = 'allowed' | 'stripped' | 'blocked' | 'skipped'
type RouteReason = 'alias' | 'policy' | 'fallback' | 'explicit'
/** not_reconciled: nothing is applied or observed. */
type SyncState = 'synced' | 'applying' | 'failed' | 'drift' | 'not_reconciled' | 'pending' | 'no_endpoint'

export interface Team {
  id: string
  name: string
  costCenter: string
}

/**
 * A team's project. Names are for people, unique within a team
 * (ignoring case); receipts, budgets and rules name it by id, so it can be
 * renamed.
 */
export interface Project {
  id: string
  team: string
  name: string
}

export interface ApiKey {
  id: string
  name: string
  prefix: string
  team: string
  /** The project's name. */
  project: string
  projectId: string
  allowedModels: string[]
  allowedRegions: string[]
  expiresAt: string | null
  lastUsed: string
  requests24h: number
  /** Requests per hour over the same rolling 24h, oldest first. */
  hourly24h: number[]
  /** Api mode only; mock mode derives it from the spend fixtures. */
  spend24hUsd?: number
  /** Api mode: requests over the same 24h with no price, which spend24hUsd leaves out. */
  unpriced24h?: number
  status: 'active' | 'revoked'
  /** Who created it. */
  owner: string
}

export interface Model {
  id: string
  display: string
  provider: string
  family: string
  context: number
  /** Api mode: inputs from LiteLLM's entries for the backends serving it; absent when none has one. */
  modalities?: string[]
  /** Api mode: when a backend's provider retires it, from LiteLLM. */
  deprecations?: { backend: string; date: string }[]
}

/** Mock fixtures only: one price per model. In api mode prices are per
 * (model, backend), in PricingView.prices. */
export interface MockModel extends Model {
  inPerM: number
  outPerM: number
  cachedPerM: number
  reasoningPerM: number
}

/** The price a receipt was costed with. Receipts from before per-backend
 * prices carry only the model and its four rates. */
interface CostBasis {
  id: string
  display: string
  provider: string
  backend?: string
  /** When the price row took effect, epoch ms. */
  effectiveFrom?: number
  inPerM: number | null
  cachedPerM: number | null
  cacheWritePerM?: number | null
  outPerM: number | null
  reasoningPerM: number | null
  /** Where each rate came from, keyed input, cachedInput, cacheWrite, output, reasoning. */
  sources?: Partial<Record<RateName, PriceSource>>
  /** Had no price when it arrived; priced once its (model, backend) got one. */
  pricedLater?: boolean
}

export interface TraceStep {
  step: string
  input: string
  outcome: string
  ms: number
  /** throttle: a throttle budget refused the request for now (429, retry later). */
  state: 'ok' | 'warn' | 'fail' | 'throttle' | 'skip'
}

interface RuleEval {
  ruleId: string
  name: string
  /** The rule's policy (api mode). Receipts from before policies lack them; their rule id is the policy's. */
  policyId?: string
  policy?: string
  /** The policy's version. */
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
  /** Api mode: the gateway's own time before calling the upstream (key check, Warden, Agent Router), µs. */
  overheadUs?: number
  keyId: string
  keyName: string
  team: string
  /** The project's name when the request was made. */
  project: string
  /** Api mode: absent on receipts with no key identity. */
  projectId?: string
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
  /** Input tokens written to the provider's prompt cache; part of inputTokens. */
  cacheWriteTokens?: number
  /** null when the (model, backend) had no price: unknown, not $0. */
  costUsd: number | null
  /** The price row this receipt was costed with; absent on receipts written before it was recorded, and on unpriced ones. */
  costBasis?: CostBasis
  verdict: Verdict
  inboundVerdict: InboundVerdict
  /** How Warden handled the request; absent when nothing evaluated policy. */
  policyMode?: 'enforced' | 'passthrough' | 'fail-open' | 'fail-closed'
  /** rehydrated: how many came back in the response (api mode; absent when none did). */
  redactions: { type: string; count: number; rehydrated?: number }[]
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

const rand = mulberry32(20260924)
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

export const models: MockModel[] = [
  { id: 'gpt-5-mini', display: 'GPT-5 mini', provider: 'OpenAI', family: 'gpt-5', context: 400_000, inPerM: 0.25, outPerM: 2.0, cachedPerM: 0.025, reasoningPerM: 2.0 },
  { id: 'gpt-5.5', display: 'GPT-5.5', provider: 'OpenAI', family: 'gpt-5', context: 400_000, inPerM: 1.25, outPerM: 10.0, cachedPerM: 0.125, reasoningPerM: 10.0 },
  { id: 'claude-sonnet-5', display: 'Claude Sonnet 5', provider: 'Anthropic', family: 'claude', context: 1_000_000, inPerM: 3.0, outPerM: 15.0, cachedPerM: 0.3, reasoningPerM: 15.0 },
  { id: 'claude-opus-4-1', display: 'Claude Opus 4.1', provider: 'Anthropic', family: 'claude', context: 200_000, inPerM: 15.0, outPerM: 75.0, cachedPerM: 1.5, reasoningPerM: 75.0 },
  { id: 'claude-haiku-4-5', display: 'Claude Haiku 4.5', provider: 'Bedrock', family: 'claude', context: 200_000, inPerM: 1.0, outPerM: 5.0, cachedPerM: 0.1, reasoningPerM: 5.0 },
  { id: 'llama-3.3-70b', display: 'Llama 3.3 70B', provider: 'Self-hosted', family: 'llama', context: 128_000, inPerM: 0.12, outPerM: 0.3, cachedPerM: 0.12, reasoningPerM: 0.3 },
]

export const modelById: Record<string, MockModel> = Object.fromEntries(models.map((m) => [m.id, m]))

type RateName = 'input' | 'cachedInput' | 'cacheWrite' | 'output' | 'reasoning'
export type PriceSource = 'seed' | 'litellm' | 'manual'

export interface Backend {
  name: string
  provider: string
  region: string
  sync: SyncState
  models: string[]
  /** idle: no requests in the last 15 minutes (api mode only). */
  health: 'healthy' | 'degraded' | 'down' | 'idle'
  p50: number
  errorRate: number
}

export const backends: Backend[] = [
  { name: 'openai-prod', provider: 'OpenAI', region: 'us-east', sync: 'synced', models: ['gpt-5-mini', 'gpt-5.5'], health: 'healthy', p50: 412, errorRate: 0.2 },
  { name: 'anthropic-prod', provider: 'Anthropic', region: 'us-east', sync: 'applying', models: ['claude-sonnet-5', 'claude-opus-4-1'], health: 'degraded', p50: 980, errorRate: 3.1 },
  { name: 'bedrock-eu', provider: 'Bedrock', region: 'eu-central', sync: 'synced', models: ['claude-haiku-4-5', 'claude-sonnet-5'], health: 'healthy', p50: 640, errorRate: 0.4 },
  { name: 'vllm-internal', provider: 'Self-hosted', region: 'eu-private', sync: 'drift', models: ['llama-3.3-70b'], health: 'healthy', p50: 220, errorRate: 0.1 },
  { name: 'azure-openai-eu', provider: 'Azure', region: 'eu-west', sync: 'failed', models: ['gpt-5-mini'], health: 'down', p50: 0, errorRate: 100 },
]

type SeedKey = Omit<ApiKey, 'hourly24h' | 'projectId' | 'owner'>

const seedKeys: SeedKey[] = [
  { id: 'k1', name: 'support-bot', prefix: 'ngw_live_7f3a', team: 'support', project: 'helpdesk', allowedModels: ['gpt-5-mini', 'claude-sonnet-5'], allowedRegions: ['us-east', 'eu-central'], expiresAt: '2027-03-01', lastUsed: '12s ago', requests24h: 18_240, status: 'active' },
  { id: 'k2', name: 'agents-prod', prefix: 'ngw_live_c19e', team: 'agents', project: 'orchestrator', allowedModels: ['claude-sonnet-5', 'claude-opus-4-1', 'gpt-5.5'], allowedRegions: ['us-east'], expiresAt: '2026-12-31', lastUsed: '3s ago', requests24h: 9_812, status: 'active' },
  { id: 'k3', name: 'batch-summarize', prefix: 'ngw_live_02bd', team: 'batch', project: 'nightly-digest', allowedModels: ['gpt-5-mini', 'claude-opus-4-1', 'llama-3.3-70b'], allowedRegions: ['us-east', 'eu-private'], expiresAt: '2026-11-15', lastUsed: '41s ago', requests24h: 4_406, status: 'active' },
  { id: 'k4', name: 'web-chat', prefix: 'ngw_live_9a0c', team: 'web', project: 'assistant', allowedModels: ['gpt-5-mini', 'claude-haiku-4-5'], allowedRegions: ['us-east'], expiresAt: '2027-01-20', lastUsed: '1s ago', requests24h: 22_019, status: 'active' },
  { id: 'k5', name: 'research', prefix: 'ngw_live_e55f', team: 'research', project: 'evals', allowedModels: ['claude-opus-4-1', 'gpt-5.5', 'claude-sonnet-5'], allowedRegions: ['us-east'], expiresAt: null, lastUsed: '6m ago', requests24h: 1_204, status: 'active' },
  { id: 'k6', name: 'secops-triage', prefix: 'ngw_live_41d2', team: 'security', project: 'soc', allowedModels: ['llama-3.3-70b', 'claude-haiku-4-5'], allowedRegions: ['eu-private', 'eu-central'], expiresAt: '2026-10-02', lastUsed: '2h ago', requests24h: 88, status: 'active' },
  { id: 'k7', name: 'legacy-intranet', prefix: 'ngw_live_77aa', team: 'web', project: 'intranet', allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2026-08-30', lastUsed: '26d ago', requests24h: 0, status: 'revoked' },
]

/** Deterministic-per-key hourly request counts for the last 24h. */
function hourly(k: SeedKey) {
  const seed = [...k.id].reduce((a, c) => a + c.charCodeAt(0), 0)
  const base = k.requests24h / 24
  return Array.from({ length: 24 }, (_, i) => {
    const hour = (new Date().getHours() - 23 + i + 24) % 24
    const diurnal = 0.5 + 0.5 * Math.sin(((hour - 7) / 24) * Math.PI * 2)
    const jitter = 0.85 + ((seed * (i + 3)) % 30) / 100
    return Math.round(base * (0.4 + diurnal) * jitter)
  })
}

/** A mock project's id. */
const mockProjectId = (team: string, name: string) => `p-${team}-${name}`

// Mock keys have a few owners, so the Keys page shows whose each is.
const mockKeyOwners: Record<string, string> = { k2: 'marco@acme.dev', k3: 'sam@acme.dev', k6: 'marco@acme.dev' }
export const keys: ApiKey[] = seedKeys.map((k) => ({ ...k, projectId: mockProjectId(k.team, k.project), hourly24h: hourly(k), owner: mockKeyOwners[k.id] ?? 'priya@acme.dev' }))

/** The seeded keys' projects, one per team and name. */
export const projects: Project[] = [...new Map(keys.map((k) => [k.projectId, { id: k.projectId, team: k.team, name: k.project }])).values()]

export const keyById = Object.fromEntries(keys.map((k) => [k.id, k]))

export interface Budget {
  id: string
  /** A team id, a project id or a key id. */
  scope: string
  /** What to show for the scope: a key's or project's name. See budgetLabel. */
  scopeName?: string
  scopeType: 'team' | 'key' | 'project'
  period: 'monthly'
  capUsd: number
  currentUsd: number
  onExceed: 'warn' | 'throttle' | 'block'
  /** A throttle budget: requests a minute each covered key gets while it's over its cap. */
  throttlePerMinute?: number
  projectedUsd: number
  /** The scope's daily average the projection uses. */
  trailingDailyUsd: number
  /** Api mode: this month's requests in scope with no price, which currentUsd leaves out. */
  unpricedRequests?: number
}

/** Requests a minute per key while a throttle budget is over its cap. */
const THROTTLE_PER_MINUTE = 10

/** Requests a minute each key gets while `b`, a throttle budget, is over its cap. */
export const throttleRate = (b: Pick<Budget, 'throttlePerMinute'>) => b.throttlePerMinute ?? THROTTLE_PER_MINUTE

/** A budget's scope as people read it: the key's or project's name, not its id. */
export const budgetLabel = (b: Pick<Budget, 'scope' | 'scopeName'>) => b.scopeName || b.scope

/** The key fields a budget matches on. A key not created yet has no id. */
export type BudgetKey = { id: string; team: string; projectId: string }

/** Whether a budget applies to a key: its team, its project, or the key itself (both by id). */
function budgetCovers(b: Budget, k: BudgetKey) {
  return b.scopeType === 'team' ? b.scope === k.team : b.scopeType === 'project' ? b.scope === k.projectId : b.scope === k.id
}

/**
 * The budget that decides a key's requests, as the gateway picks it: of every
 * budget covering the key, the strictest over its cap (block, then throttle,
 * then warn), else the one nearest its cap.
 */
export function governingBudget(k: BudgetKey, list: Budget[]): Budget | undefined {
  const severity = { warn: 1, throttle: 2, block: 3 }
  const rank = (b: Budget) => (b.currentUsd >= b.capUsd ? severity[b.onExceed] : 0)
  const ratio = (b: Budget) => (b.capUsd > 0 ? b.currentUsd / b.capUsd : 0)
  return list
    .filter((b) => budgetCovers(b, k))
    .sort((a, b) => rank(b) - rank(a) || ratio(b) - ratio(a) || a.id.localeCompare(b.id))[0]
}

export const budgets: Budget[] = [
  { id: 'b1', scope: 'support', scopeType: 'team', period: 'monthly', capUsd: 12_000, currentUsd: 13_480.22, onExceed: 'throttle', projectedUsd: 16_950, trailingDailyUsd: 0 },
  { id: 'b2', scope: 'agents', scopeType: 'team', period: 'monthly', capUsd: 40_000, currentUsd: 33_104.9, onExceed: 'block', projectedUsd: 42_600, trailingDailyUsd: 0 },
  { id: 'b3', scope: 'k3', scopeName: 'batch-summarize', scopeType: 'key', period: 'monthly', capUsd: 8_000, currentUsd: 3_412.07, onExceed: 'warn', projectedUsd: 4_420, trailingDailyUsd: 0 },
  { id: 'b4', scope: 'web', scopeType: 'team', period: 'monthly', capUsd: 15_000, currentUsd: 9_870.5, onExceed: 'block', projectedUsd: 12_760, trailingDailyUsd: 0 },
  { id: 'b5', scope: 'research', scopeType: 'team', period: 'monthly', capUsd: 20_000, currentUsd: 17_210.0, onExceed: 'warn', projectedUsd: 22_300, trailingDailyUsd: 0 },
]

// ---- policies -----------------------------------------------------------

export interface PolicyRule {
  id: string
  ordinal: number
  name: string
  description: string
  mode: 'enforce' | 'monitor' | 'draft' | 'disabled'
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

/** The trace's budget step, worded as the gateway words it. */
function budgetStep(b: Budget | undefined): TraceStep {
  if (!b) return { step: 'Budget checked', input: 'no budget applies', outcome: 'skipped', ms: 0.1, state: 'skip' }
  const usd = (n: number) => (n < 100 ? `$${n.toFixed(2)}` : `$${Math.round(n)}`)
  const over = b.currentUsd >= b.capUsd
  const outcome = !over ? 'within cap' : b.onExceed === 'block' ? 'over cap · blocked' : b.onExceed === 'throttle' ? 'over cap · throttled to 10 a minute per key · admitted' : 'over cap · warning only'
  return { step: 'Budget checked', input: `${b.scopeType} budget ${budgetLabel(b)} ·${usd(b.currentUsd)} of ${usd(b.capUsd)}`, outcome, ms: 0.1, state: !over ? 'ok' : b.onExceed === 'block' ? 'fail' : 'warn' }
}

function cost(m: MockModel, inTok: number, cached: number, out: number, reasoning: number) {
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
    budgetStep(governingBudget(key, budgets)),
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
    projectId: key.projectId,
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
    costBasis: { ...model },
    verdict,
    inboundVerdict: verdict === 'truncated' ? 'blocked' : blocked || status !== 200 ? 'skipped' : 'allowed',
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
  /** Refused for now by a throttle budget over its cap. */
  throttled: number
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
    throttled: Math.round(base * 0.004),
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

// Budget projections on the Spend page's basis: month to date plus the
// trailing 7-day average for each day left. The key budget takes 42% of its
// team's spend.
{
  const at = new Date(NOW)
  const monthEnd = Date.UTC(at.getUTCFullYear(), at.getUTCMonth() + 1, 1)
  const remaining = (monthEnd - NOW) / 86_400_000
  const avg = (team: string) => spendSeries.slice(-7).reduce((a, d) => a + (d.byTeam[team] ?? 0), 0) / 7
  for (const b of budgets) {
    b.trailingDailyUsd = b.scopeType === 'key' ? avg('batch') * 0.42 : avg(b.scope)
    b.projectedUsd = b.currentUsd + b.trailingDailyUsd * remaining
  }
}

// GET /spend: the Spend page's totals, breakdown, trend and projection for a
// range, grouped by one dimension. Mock mode derives it in pages/spend-data.
/** person: who made the requests, where the control plane can tell (the OpenMeter pack's). */
export type SpendDim = 'team' | 'person' | 'project' | 'key' | 'model' | 'provider'

export interface SpendRow {
  /** What Traffic filters on for this group. */
  id: string
  label: string
  sub?: string
  spendUsd: number
  prevSpendUsd: number
  requests: number
  tokens: number
  /** Api mode: served requests with no price, counted in requests but not in spendUsd. */
  unpriced?: number
  /** Only in mock mode: the aggregates carry no latency. */
  p50Ms?: number
  /** Traffic has no filter for this group (no key identity, or never routed). */
  noDrill?: boolean
}

export interface SpendView {
  range: string
  by: SpendDim
  from: number
  to: number
  prevFrom: number
  rows: SpendRow[]
  /** Api mode: served requests in [from, to) with no price yet, which the totals leave out. */
  unpriced?: number
  trend: {
    bucketMs: number
    points: { t: number; values: Record<string, number> }[]
    /** Every group, ranked by its last 30 days, so colors don't follow the range. */
    order: string[]
    labels: Record<string, string>
    /** Api mode: requests over the charted buckets with no price, which the bars leave out. */
    unpriced?: number
  }
  period: {
    periodStart: number
    periodEnd: number
    monthToDateUsd: number
    trailingDailyUsd: number
    trailingDays: number
    remainingDays: number
    projectedUsd: number
    /** Api mode: this month's requests with no price, which month to date and the projection leave out. */
    unpriced?: number
  }
}

/** The mockup's surge callout: support started surging 6 days ago. */
export const spendSurge = { team: 'Support', ratio: 2.9, since: spendSeries[spendSeries.length - 6].day, key: 'support-bot', model: 'claude-sonnet-5' }

export interface SavingsOpportunity {
  id: string
  /** Headline: "$X/mo if {before}{subject}{after} moved to {target}". */
  before?: string
  subject: string
  after?: string
  target: string
  monthly: number
  basis: string
  receipts: number
  href: string
}

const perRequest = (model: string, inTok: number, outTok: number) => (inTok * modelById[model].inPerM + outTok * modelById[model].outPerM) / 1e6

export const savings: SavingsOpportunity[] = (() => {
  const perDay1 = 412
  const d1 = perRequest('claude-opus-4-1', 12_000, 600) - perRequest('gpt-5-mini', 12_000, 600)
  const perDay2 = 5_900
  const d2 = perRequest('claude-sonnet-5', 1_400, 90) - perRequest('claude-haiku-4-5', 1_400, 90)
  return [
    {
      id: 'o1',
      subject: 'summarize-*',
      target: 'gpt-5-mini',
      monthly: d1 * perDay1 * 30,
      basis: `${(perDay1 * 30).toLocaleString('en-US')} requests in 30 days from batch-summarize call claude-opus-4-1 directly with summarize-shaped prompts (~12k in, <800 out). Same prompts routed through the summarize-* alias already run on gpt-5-mini.`,
      receipts: perDay1 * 30,
      href: '/traffic?key=batch-summarize&model=claude-opus-4-1',
    },
    {
      id: 'o2',
      before: 'short ',
      subject: 'support-bot',
      after: ' classification calls',
      target: 'claude-haiku-4-5',
      monthly: d2 * perDay2 * 30,
      basis: `${(perDay2 * 30).toLocaleString('en-US')} requests in 30 days on claude-sonnet-5 with under 100 output tokens and a fixed system prompt. Same model family; quality not measured — run a shadow comparison before promoting.`,
      receipts: perDay2 * 30,
      href: '/traffic?key=support-bot&model=claude-sonnet-5',
    },
  ]
})()

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

// Banner conditions (GET /degradations in api mode); these are the mock-mode
// fixtures.
export interface Degradation {
  kind: string
  severity: number
  title: string
  detail: string
  to: string
  action: string
  since?: number
}

export const degradations: Degradation[] = [
  {
    kind: 'policy_fail_open',
    severity: 2,
    title: 'Policy cost-guard-opus is running fail-open.',
    detail: 'Warden cannot reach the control plane for anthropic-prod routing hints; requests pass without the cost guard. Cache age 4m 12s.',
    to: '/guardrails',
    action: 'Review policy',
  },
  {
    kind: 'backend_errors',
    severity: 1,
    title: 'anthropic-prod is failing over to bedrock-eu.',
    detail: '8% of Claude traffic since 13:51. Upstream is returning 529 overloaded.',
    to: '/routing',
    action: 'View backend',
  },
]

// Where the console is and who's using it (GET /session in api mode).
export interface Session {
  tenant: { id: string; name: string }
  environment: string
  /** roles are most powerful first. */
  actor: { email: string; name?: string; roles: string[] }
  /** dev: no sign-in, so nothing to sign out of; oidc: people sign in with Keycloak. */
  auth: { mode: 'dev' | 'oidc'; signOutUrl?: string }
  versions: { controlPlane: string }
  /** null when the control plane doesn't know where Warden is. */
  warden: { connected: boolean; version?: string; snapshotAgeSeconds?: number; passthrough: boolean } | null
}

export const session: Session = {
  tenant: { id: 'acme', name: 'acme' },
  environment: 'production',
  actor: { email: 'priya@acme.dev', name: 'Priya Shah', roles: ['admin'] },
  auth: { mode: 'oidc', signOutUrl: '/api/auth/logout' },
  versions: { controlPlane: '0.1' },
  warden: { connected: true, version: '0.4.2', snapshotAgeSeconds: 252, passthrough: false },
}

// Header notifications (mock mode). In api mode the bell lists degradations.
export const notifications = [
  { id: 1, unread: true, title: 'Budget "support" is over its cap', body: '$13,480 of $12,000 · throttling new requests', when: '4m ago' },
  { id: 2, unread: true, title: 'Drift on backend vllm-internal', body: 'replicas changed 4 → 2 by argocd', when: '3h ago' },
  { id: 3, unread: false, title: 'Rule block-src fired 7× its baseline', body: '96 blocks in 24h, 82 from support', when: '5h ago' },
]

// Overview numbers for a range against the span before it (GET /summary).
interface WindowTotals {
  requests: number
  blocked: number
  redacted: number
  spendUsd: number
  /** Api mode: served requests with no price, which spendUsd leaves out. */
  unpriced?: number
}

interface KeyAnomaly {
  keyId: string
  keyName: string
  spendUsd: number
  baselineUsd: number
  ratio: number
  topModel: string
  topModelShare: number
}

export interface Summary {
  range: string
  from: number
  to: number
  current: WindowTotals
  previous: WindowTotals
  topTeamIncrease: { team: string; deltaUsd: number } | null
  keyAnomalies: KeyAnomaly[]
  /** Api mode: (model, backend) pairs that served requests in the range with no price. */
  unpricedPairs?: UnpricedPair[]
}

interface UnpricedPair {
  model: string
  backend: string
  requests: number
  /** The first of them, epoch ms. */
  since: number
}

const seriesTotals = trafficSeries.reduce(
  (a, p) => ({
    requests: a.requests + p.allowed + p.redacted + p.rerouted + p.blocked + p.truncated + p.throttled,
    blocked: a.blocked + p.blocked,
    redacted: a.redacted + p.redacted,
  }),
  { requests: 0, blocked: 0, redacted: 0 },
)

export const summary: Summary = {
  range: '24h',
  from: NOW - 86_400_000,
  to: NOW,
  current: { ...seriesTotals, spendUsd: 3_184.62 },
  previous: {
    requests: Math.round(seriesTotals.requests / 1.062),
    blocked: Math.round(seriesTotals.blocked / 1.235),
    redacted: Math.round(seriesTotals.redacted / 1.235),
    spendUsd: 3_184.62 / 1.418,
  },
  topTeamIncrease: { team: 'support', deltaUsd: 940 },
  keyAnomalies: [{ keyId: 'k1', keyName: 'support-bot', spendUsd: 1_404, baselineUsd: 484, ratio: 2.9, topModel: 'claude-sonnet-5', topModelShare: 0.71 }],
}

// ---- Activity -----------------------------------------------------------------
// GET /activity: each config change with its effect on traffic, and traffic
// events.

interface ActivityAgg {
  requests: number
  /** Over served requests with a price; null when none had one (api mode). */
  costPerRequestUsd: number | null
  /** Api mode: served requests with no price, left out of costPerRequestUsd. */
  unpriced?: number
  errorRate: number
  blockedRedactedShare: number
}

interface ActivityImpact {
  windowMinutes: number
  /** Start of the change's 5-minute bucket, which counts as after. */
  pivot: number
  before: ActivityAgg
  after: ActivityAgg
  /** Requests per 5-minute bucket; the first `split` are before. */
  bins: number[]
  split: number
  comparable: boolean
}

type ActivityChange = Change & { impact: ActivityImpact }

interface TrafficEvent {
  id: string
  ts: number
  kind: string
  title: string
  detail: string
  tone: 'allowed' | 'redacted' | 'rerouted' | 'blocked' | 'degraded' | 'neutral'
  to: string
}

export interface ActivityView {
  since: number
  until: number
  changes: ActivityChange[]
  events: TrafficEvent[]
}

