// Seeded synthetic data for the demo tenant (mock mode). Everything here is
// fabricated and deterministic so screens render the same way on every load.

/** throttled: refused for now (429, retry later), not blocked. truncated: no token counts reported. */
export type Verdict = 'allowed' | 'blocked' | 'truncated' | 'throttled'

export interface Team {
  id: string
  name: string
  costCenter: string
}

/** A team's project: here, a team's model resource. Receipts name it by id. */
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
  lastUsed: string
  requests24h: number
  /** Requests per hour over the same rolling 24h, oldest first. */
  hourly24h: number[]
  /** Api mode only; mock mode derives it from the spend fixtures. */
  spend24hUsd?: number
  /** Api mode: requests over the same 24h with no price, which spend24hUsd leaves out. */
  unpriced24h?: number
  /** Whose requests the key carries. */
  owner: string
}

export interface Model {
  id: string
  provider: string
}

/** Mock fixtures only: one price per model. */
export interface MockModel extends Model {
  inPerM: number
  outPerM: number
  cachedPerM: number
  reasoningPerM: number
}

export interface Receipt {
  id: string
  traceId: string
  sessionId?: string
  ts: number
  durationMs: number
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
  /** The model resource that served it. */
  backend: string
  inputTokens: number
  cachedInputTokens: number
  outputTokens: number
  reasoningTokens: number
  /** Input tokens written to the provider's prompt cache; part of inputTokens. */
  cacheWriteTokens?: number
  /** null when the model had no price: unknown, not $0. */
  costUsd: number | null
  verdict: Verdict
  status: number
  errorCode?: string
  errorDetail?: string
  inFlight?: boolean
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
  { id: 'gpt-5-mini', provider: 'OpenAI', inPerM: 0.25, outPerM: 2.0, cachedPerM: 0.025, reasoningPerM: 2.0 },
  { id: 'gpt-5.5', provider: 'OpenAI', inPerM: 1.25, outPerM: 10.0, cachedPerM: 0.125, reasoningPerM: 10.0 },
  { id: 'claude-sonnet-5', provider: 'Anthropic', inPerM: 3.0, outPerM: 15.0, cachedPerM: 0.3, reasoningPerM: 15.0 },
  { id: 'claude-opus-4-1', provider: 'Anthropic', inPerM: 15.0, outPerM: 75.0, cachedPerM: 1.5, reasoningPerM: 75.0 },
  { id: 'claude-haiku-4-5', provider: 'Bedrock', inPerM: 1.0, outPerM: 5.0, cachedPerM: 0.1, reasoningPerM: 5.0 },
  { id: 'llama-3.3-70b', provider: 'Self-hosted', inPerM: 0.12, outPerM: 0.3, cachedPerM: 0.12, reasoningPerM: 0.3 },
]

export const modelById: Record<string, MockModel> = Object.fromEntries(models.map((m) => [m.id, m]))

/** Each model's resource, the receipts' backend, and its typical latency. */
const resources: Record<string, { backend: string; p50: number }> = {
  OpenAI: { backend: 'openai', p50: 412 },
  Anthropic: { backend: 'anthropic', p50: 980 },
  Bedrock: { backend: 'bedrock', p50: 640 },
  'Self-hosted': { backend: 'vllm-internal', p50: 220 },
}

interface SeedKey {
  id: string
  name: string
  prefix: string
  team: string
  project: string
  lastUsed: string
  requests24h: number
}

const seedKeys: SeedKey[] = [
  { id: 'k1', name: 'support-bot', prefix: 'ngw_live_7f3a', team: 'support', project: 'helpdesk', lastUsed: '12s ago', requests24h: 18_240 },
  { id: 'k2', name: 'agents-prod', prefix: 'ngw_live_c19e', team: 'agents', project: 'orchestrator', lastUsed: '3s ago', requests24h: 9_812 },
  { id: 'k3', name: 'batch-summarize', prefix: 'ngw_live_02bd', team: 'batch', project: 'nightly-digest', lastUsed: '41s ago', requests24h: 4_406 },
  { id: 'k4', name: 'web-chat', prefix: 'ngw_live_9a0c', team: 'web', project: 'assistant', lastUsed: '1s ago', requests24h: 22_019 },
  { id: 'k5', name: 'research', prefix: 'ngw_live_e55f', team: 'research', project: 'evals', lastUsed: '6m ago', requests24h: 1_204 },
  { id: 'k6', name: 'secops-triage', prefix: 'ngw_live_41d2', team: 'security', project: 'soc', lastUsed: '2h ago', requests24h: 88 },
]

/** The models each mock key calls, for its receipts and its share of spend. */
export const keyModels: Record<string, string[]> = {
  k1: ['gpt-5-mini', 'claude-sonnet-5'],
  k2: ['claude-sonnet-5', 'claude-opus-4-1', 'gpt-5.5'],
  k3: ['gpt-5-mini', 'claude-opus-4-1', 'llama-3.3-70b'],
  k4: ['gpt-5-mini', 'claude-haiku-4-5'],
  k5: ['claude-opus-4-1', 'gpt-5.5', 'claude-sonnet-5'],
  k6: ['llama-3.3-70b', 'claude-haiku-4-5'],
}

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

function cost(m: MockModel, inTok: number, cached: number, out: number, reasoning: number) {
  return ((inTok - cached) * m.inPerM + cached * m.cachedPerM + out * m.outPerM + reasoning * m.reasoningPerM) / 1_000_000
}

const actors = ['u_4412', 'u_0981', 'u_2231', 'svc-digest', 'u_7713', undefined, undefined]

export function makeReceipt(ts: number, r: () => number = rand, opts: { inFlight?: boolean } = {}): Receipt {
  const key = weightedKey(r)
  const resolved = pick(keyModels[key.id], r)
  const model = modelById[resolved]
  const resource = resources[model.provider]

  const roll = r()
  const verdict: Verdict = roll < 0.03 ? 'blocked' : roll < 0.034 ? 'throttled' : roll < 0.04 ? 'truncated' : 'allowed'
  const refused = verdict === 'blocked' || verdict === 'throttled'

  const inputTokens = Math.round(200 + r() ** 2 * (key.team === 'batch' ? 40_000 : 9_000))
  const cachedInputTokens = r() < 0.4 ? Math.round(inputTokens * r() * 0.7) : 0
  const outputTokens = Math.round(40 + r() * (key.team === 'agents' ? 2_400 : 900))
  const reasoningTokens = resolved.startsWith('gpt-5') && r() < 0.5 ? Math.round(r() * 3_000) : 0
  const durationMs = refused ? Math.round(18 + r() * 30) : Math.round(resource.p50 * (0.5 + r() * 1.8) + outputTokens * 0.6)
  const status = verdict === 'blocked' ? 403 : verdict === 'throttled' ? 429 : 200

  return {
    id: hex(8, r) + '-' + hex(4, r),
    traceId: hex(32, r),
    sessionId: key.team === 'agents' ? 'sess_' + hex(6, r) : undefined,
    ts,
    durationMs,
    keyId: key.id,
    keyName: key.name,
    team: key.team,
    project: key.project,
    projectId: key.projectId,
    actor: pick(actors, r),
    requestedModel: resolved,
    resolvedModel: resolved,
    backend: resource.backend,
    inputTokens,
    cachedInputTokens,
    outputTokens: refused || verdict === 'truncated' ? 0 : outputTokens,
    reasoningTokens: refused ? 0 : reasoningTokens,
    costUsd: refused ? 0 : verdict === 'truncated' ? null : cost(model, inputTokens, cachedInputTokens, outputTokens, reasoningTokens),
    verdict,
    status,
    errorCode: verdict === 'blocked' ? 'refused' : verdict === 'throttled' ? 'rate_limited' : verdict === 'truncated' ? 'no_usage_reported' : undefined,
    errorDetail: verdict === 'blocked' ? 'The gateway refused this request (HTTP 403).' : verdict === 'truncated' ? 'The gateway reported no token counts for this request.' : undefined,
    inFlight: opts.inFlight,
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
  blocked: number
  truncated: number
  /** Refused for now (429, retry later). */
  throttled: number
}

export const trafficSeries: SeriesPoint[] = Array.from({ length: 48 }, (_, i) => {
  const hour = (i / 2 + 8) % 24
  const diurnal = 0.55 + 0.45 * Math.sin(((hour - 6) / 24) * Math.PI * 2)
  const base = Math.round(2_400 * diurnal + rand() * 300)
  return {
    t: NOW - (47 - i) * 30 * 60_000,
    allowed: Math.round(base * 0.96),
    blocked: Math.round(base * 0.03),
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

// Where the console is and who's using it (GET /session in api mode).
export interface Session {
  tenant: { id: string; name: string }
  environment: string
  /** roles are most powerful first. */
  actor: { email: string; name?: string; roles: string[] }
  /** dev: no sign-in, so nothing to sign out of; oidc: people sign in with Keycloak at the gateway, and signOutUrl is its logout path. */
  auth: { mode: 'dev' | 'oidc'; signOutUrl?: string }
  versions: { controlPlane: string }
}

export const session: Session = {
  tenant: { id: 'acme', name: 'acme' },
  environment: 'production',
  actor: { email: 'priya@acme.dev', name: 'Priya Shah', roles: ['admin'] },
  auth: { mode: 'oidc', signOutUrl: '/logout' },
  versions: { controlPlane: '0.1' },
}

// Overview numbers for a range against the span before it (GET /summary).
interface WindowTotals {
  requests: number
  spendUsd: number
  /** Api mode: served requests with no price, which spendUsd leaves out. */
  unpriced?: number
}

export interface Summary {
  range: string
  from: number
  to: number
  current: WindowTotals
  previous: WindowTotals
  topTeamIncrease: { team: string; deltaUsd: number } | null
}

const seriesRequests = trafficSeries.reduce((a, p) => a + p.allowed + p.blocked + p.truncated + p.throttled, 0)

export const summary: Summary = {
  range: '24h',
  from: NOW - 86_400_000,
  to: NOW,
  current: { requests: seriesRequests, spendUsd: 3_184.62 },
  previous: { requests: Math.round(seriesRequests / 1.062), spendUsd: 3_184.62 / 1.418 },
  topTeamIncrease: { team: 'support', deltaUsd: 940 },
}
