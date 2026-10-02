// The console's data source. Pages import catalog data from here; which data
// it holds depends on VITE_DATA:
//   mock (default): the seeded fixtures in ./mock, synchronously, as before
//   api:            fetched from the control plane by hydrate() before the app
//                   mounts, then kept live over SSE (see state/app-state)
// Exports are live `let` bindings, so modules that read them at render time,
// or at import time after hydrate(), see the fetched values.
import { ago } from '@/lib/format'
import * as mock from './mock'
import type { ApiKey, Backend, Budget, Change, Degradation, Model, PolicyRule, Receipt, Route, SeriesPoint, SpendPoint, Session, Summary, ChangeImpact, Team } from './mock'

export type * from './mock'

export const dataMode: 'api' | 'mock' = import.meta.env.VITE_DATA === 'api' ? 'api' : 'mock'
export const API_BASE = '/api/v1/demo'

export type Detector = (typeof mock.detectors)[number]

export let teams: Team[] = mock.teams
export let models: Model[] = mock.models
export let modelById: Record<string, Model> = mock.modelById
export let backends: Backend[] = mock.backends
export let routes: Route[] = mock.routes
export let keys: ApiKey[] = mock.keys
export let keyById: Record<string, ApiKey> = mock.keyById
export let budgets: Budget[] = mock.budgets
/** The budget that decides a key's requests (the gateway's rule); see mock.governingBudget. */
export const governingBudget = (k: { team: string; project: string; name: string }) => mock.governingBudget(k, budgets)
export const coveringBudgets = (k: { team: string; project: string; name: string }) => budgets.filter((b) => mock.budgetCovers(b, k))
export let rules: PolicyRule[] = mock.rules
/** Mock-mode Detectors fixtures; api mode reads GET /detectors on the tab. */
export const detectors: Detector[] = mock.detectors
export let seedReceipts: Receipt[] = mock.seedReceipts
export let trafficSeries: SeriesPoint[] = mock.trafficSeries
export let spendSeries: SpendPoint[] = mock.spendSeries
export let changes: Change[] = mock.changes
/** Mock-mode bell items; in api mode the bell lists degradations. */
export const seedNotifications = dataMode === 'api' ? [] : mock.notifications
/** Mock-mode banner conditions; in api mode the banner polls /degradations. */
export const seedDegradations: Degradation[] = dataMode === 'api' ? [] : mock.degradations
export let now: () => number = mock.now
export let session: Session = mock.session
/** Mock-mode Spend callouts. Api mode has no surge rule or savings analysis yet. */
export const seedSpendSurge = dataMode === 'api' ? null : mock.spendSurge
export const seedSavings = dataMode === 'api' ? null : mock.savings
/** Mock-mode fixtures for GET /summary and GET /changes/{id}/impact. */
export const seedSummary: Summary = mock.summary
export const seedChangeImpacts: Record<string, ChangeImpact> = dataMode === 'api' ? {} : mock.changeImpacts
/** Mock-mode Activity readouts and traffic events; api mode reads GET /activity. */
export const seedActivityReadouts: typeof mock.activityReadouts = dataMode === 'api' ? {} : mock.activityReadouts
/** Settings fixtures; api mode has no backend for these yet. */
export const seedProviderKeys = dataMode === 'api' ? [] : mock.providerKeys
export const seedMembers = dataMode === 'api' ? [] : mock.members
export const seedIntegrations = dataMode === 'api' ? [] : mock.integrations
export const seedRetention: mock.RetentionView | null = dataMode === 'api' ? null : mock.retention
export const seedActivityEvents: mock.TrafficEvent[] = dataMode === 'api' ? [] : mock.activityEvents
/** Models fixtures. Api mode reads GET /aliases and GET /pricing; the catalog has no modalities or deprecation dates yet. */
export const seedAliases: mock.AliasView[] = dataMode === 'api' ? [] : mock.aliases
export const seedPricing: mock.PricingView | null = dataMode === 'api' ? null : mock.pricing
export const seedModalities: Record<string, string[]> | null = dataMode === 'api' ? null : mock.modelModalities
export const seedDeprecations: Record<string, string> | null = dataMode === 'api' ? null : mock.modelDeprecations

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  /** On a 409 from a stale If-Match: the resource as it is now (§6). */
  readonly current?: unknown
  constructor(status: number, code: string, message: string, current?: unknown) {
    super(message)
    this.status = status
    this.code = code
    this.current = current
  }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(API_BASE + path, {
    ...init,
    headers: { Accept: 'application/json', ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...init?.headers },
  })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new ApiError(res.status, body?.error?.code ?? 'http_error', body?.error?.message ?? `${res.status} ${res.statusText}`, body?.current)
  }
  return res.json() as Promise<T>
}

/** The control plane sends lastUsedAt (epoch ms); screens show "12s ago". */
export type WireKey = Omit<ApiKey, 'lastUsed'> & { lastUsedAt: number | null }
export const fromWire = (k: WireKey): ApiKey => ({ ...k, lastUsed: k.lastUsedAt ? ago(k.lastUsedAt) : 'never' })

const index = <T,>(xs: T[], id: (x: T) => string) => Object.fromEntries(xs.map((x) => [id(x), x]))

/** Loads the catalog from the control plane. A no-op in mock mode. */
export async function hydrate() {
  if (dataMode !== 'api') return
  const [t, m, b, r, k, bu, ru, rc, ts, ss, ch, se] = await Promise.all([
    api<Team[]>('/teams'),
    api<Model[]>('/models'),
    api<Backend[]>('/backends'),
    api<Route[]>('/routes'),
    api<WireKey[]>('/keys'),
    api<Budget[]>('/budgets'),
    api<PolicyRule[]>('/rules'),
    api<Receipt[]>('/receipts?limit=240'),
    api<SeriesPoint[]>('/series/traffic?range=24h'),
    api<SpendPoint[]>('/series/spend?days=30'),
    api<Change[]>('/changes'),
    api<Session>('/session'),
  ])
  teams = t
  models = m
  modelById = index(m, (x) => x.id)
  backends = b
  routes = r
  keys = k.map(fromWire)
  keyById = index(keys, (x) => x.id)
  budgets = bu
  rules = ru
  seedReceipts = rc
  trafficSeries = ts
  spendSeries = ss
  changes = ch
  session = se
  const loadedAt = Date.now()
  now = () => loadedAt
}

// ---- key lifecycle ---------------------------------------------------------
// The server generates and hashes secrets in api mode; mock mode keeps the
// mockup's client-side stand-ins so the flows still demo without a backend.

export interface NewKeyInput {
  name: string
  team: string
  project: string
  allowedModels: string[]
  allowedRegions: string[]
  expiresAt: string | null
}

function mockSecret() {
  const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789'
  return 'ngw_live_' + Array.from({ length: 40 }, () => alphabet[Math.floor(Math.random() * alphabet.length)]).join('')
}

export async function createKey(input: NewKeyInput): Promise<{ key: ApiKey; secret: string }> {
  if (dataMode === 'api') {
    const res = await api<{ key: WireKey; secret: string }>('/keys', { method: 'POST', body: JSON.stringify(input) })
    return { key: fromWire(res.key), secret: res.secret }
  }
  const secret = mockSecret()
  const key: ApiKey = {
    ...input,
    id: 'k' + Math.random().toString(36).slice(2, 7),
    prefix: secret.slice(0, 13),
    lastUsed: 'never',
    requests24h: 0,
    hourly24h: Array(24).fill(0),
    status: 'active',
  }
  return { key, secret }
}

export async function revokeKey(k: ApiKey): Promise<ApiKey> {
  if (dataMode === 'api') return fromWire(await api<WireKey>(`/keys/${k.id}/revoke`, { method: 'POST' }))
  return { ...k, status: 'revoked', requests24h: 0, hourly24h: Array(24).fill(0), rotation: undefined }
}

export async function rotateKey(k: ApiKey, overlapHours: number): Promise<{ key: ApiKey; secret: string }> {
  if (dataMode === 'api') {
    const res = await api<{ key: WireKey; secret: string }>(`/keys/${k.id}/rotate`, { method: 'POST', body: JSON.stringify({ overlapHours }) })
    return { key: fromWire(res.key), secret: res.secret }
  }
  const at = Date.now()
  const rotation = { startedAt: at, startedBy: session.actor.email, endsAt: at + overlapHours * 3_600_000, split: { newShare: 0, oldActors: [] } }
  return { key: { ...k, status: 'rotating', rotation }, secret: mockSecret() }
}

// ---- budgets ---------------------------------------------------------------
// Api mode writes through the control plane (audited, If-Match on edit and
// delete, dryRun for the preview). Mock mode edits the fixtures in memory.

export type BudgetInput = Pick<Budget, 'scopeType' | 'scope' | 'capUsd' | 'onExceed'>
export type BudgetEdit = Pick<Budget, 'capUsd' | 'onExceed'>
/** What a budget write would do (§6 dryRun): its spend now, and the active keys it covers. */
export interface BudgetPreview {
  budget: Budget
  covers: string[]
  overCap: boolean
}

const storeBudget = (b: Budget) => {
  budgets = budgets.some((x) => x.id === b.id) ? budgets.map((x) => (x.id === b.id ? b : x)) : [...budgets, b]
}

/** Keeps the catalog's budgets (read by the key form) in step with a fresh GET /budgets. */
export function syncBudgets(list: Budget[]) {
  budgets = list
}

function mockPreview(b: Budget): BudgetPreview {
  const covers = keys.filter((k) => k.status !== 'revoked' && mock.budgetCovers(b, k)).map((k) => k.name)
  return { budget: b, covers: covers.sort(), overCap: b.currentUsd >= b.capUsd }
}

const blankBudget = (input: BudgetInput): Budget => ({ ...input, id: '', period: 'monthly', currentUsd: 0, projectedUsd: 0, trailingDailyUsd: 0 })

/** The preview of creating `input`, or of editing `existing` to it. */
export async function previewBudget(input: BudgetInput, existing?: Budget): Promise<BudgetPreview> {
  if (dataMode === 'api') {
    const edit: BudgetEdit = { capUsd: input.capUsd, onExceed: input.onExceed }
    const res = existing
      ? await api<BudgetPreview>(`/budgets/${existing.id}?dryRun=true`, { method: 'PATCH', body: JSON.stringify(edit) })
      : await api<BudgetPreview>('/budgets?dryRun=true', { method: 'POST', body: JSON.stringify(input) })
    return { budget: res.budget, covers: res.covers, overCap: res.overCap }
  }
  return mockPreview(existing ? { ...existing, ...input } : blankBudget(input))
}

export async function createBudget(input: BudgetInput): Promise<Budget> {
  let b: Budget
  if (dataMode === 'api') b = await api<Budget>('/budgets', { method: 'POST', body: JSON.stringify(input) })
  else {
    if (budgets.some((x) => x.scopeType === input.scopeType && x.scope === input.scope)) throw new ApiError(409, 'conflict', 'conflict')
    b = { ...blankBudget(input), id: 'b' + Math.random().toString(36).slice(2, 7) }
  }
  storeBudget(b)
  return b
}

/** Edits `b` as the caller last saw it; a 409 ApiError carries the budget as it is now. */
export async function updateBudget(b: Budget, edit: BudgetEdit): Promise<Budget> {
  const next =
    dataMode === 'api'
      ? await api<Budget>(`/budgets/${b.id}`, { method: 'PATCH', body: JSON.stringify(edit), headers: { 'If-Match': b.etag ?? '' } })
      : { ...b, ...edit }
  storeBudget(next)
  return next
}

export async function deleteBudget(b: Budget): Promise<void> {
  if (dataMode === 'api') await api(`/budgets/${b.id}`, { method: 'DELETE', headers: { 'If-Match': b.etag ?? '' } })
  budgets = budgets.filter((x) => x.id !== b.id)
}

// ---- rules -----------------------------------------------------------------
// Api mode only: the Guardrails builder saves drafts, publishes versions and
// rolls back through the control plane. Mock mode keeps the page's own state.

/** What an author writes; mode and version come from publishing. */
export interface RuleContent {
  name: string
  description: string
  failMode: 'open' | 'closed'
  when: PolicyRule['when']
  then: PolicyRule['then']
}
/** A rule as GET /rules shows it: the live version, any saved draft, and the ETag covering both. */
export type RuleView = PolicyRule & { draft: (RuleContent & { updatedAt: number; updatedBy: string }) | null; etag: string }
/** An engine detector (GET /detectors): how it matches, the live rules using it, and its last 24h from receipts. */
export interface DetectorView {
  entity: string
  kind: string
  pattern: string
  placeholder: string
  usedBy: { rule: string; version: number; mode: PolicyRule['mode']; action: string }[]
  redactedRequests24h: number
  redactedMatches24h: number
  blocked24h: number
}
/** What a rule may name (GET /rules/vocabulary): exactly what the server's validation accepts. */
export interface RuleVocabulary {
  entities: string[]
  fields: string[]
  targets: string[]
}
export interface RuleVersion extends RuleContent {
  version: number
  mode: PolicyRule['mode']
  publishedAt: number | null
  publishedBy: string | null
}
export type PublishMode = 'monitor' | 'enforce' | 'disabled'
/** What a publish would leave live (§6 dryRun). Replay isn't connected, and `note` says so. */
export interface RulePublishPlan {
  rule: PolicyRule
  changes: { field: string; from: unknown; to: unknown }[]
  note: string
}

const json = (method: string, body?: unknown, etag?: string): RequestInit => ({
  method,
  body: body === undefined ? undefined : JSON.stringify(body),
  headers: etag ? { 'If-Match': etag } : {},
})

/** Keeps the catalog's rules (read by Overview) in step with a fresh GET /rules. */
export function syncRules(list: RuleView[]) {
  rules = list
}

export const createRule = (c: RuleContent) => api<RuleView>('/rules', json('POST', c))
/** Saves over `r` as the caller last saw it; a 409 ApiError carries the rule as it is now. */
export const saveRuleDraft = (r: RuleView, c: RuleContent) => api<RuleView>(`/rules/${r.id}/draft`, json('PUT', c, r.etag))
export const discardRuleDraft = (r: RuleView) => api<RuleView>(`/rules/${r.id}/draft`, json('DELETE', undefined, r.etag))
export const planPublish = (r: RuleView, mode: PublishMode) => api<RulePublishPlan>(`/rules/${r.id}/publish?dryRun=true`, json('POST', { mode }, r.etag))
export const publishRule = (r: RuleView, mode: PublishMode) => api<RuleView>(`/rules/${r.id}/publish`, json('POST', { mode }, r.etag))
export const rollbackRule = (r: RuleView, version: number) => api<RuleView>(`/rules/${r.id}/rollback`, json('POST', { version }, r.etag))
export const deleteRule = (r: RuleView) => api<{ id: string }>(`/rules/${r.id}`, json('DELETE', undefined, r.etag))
