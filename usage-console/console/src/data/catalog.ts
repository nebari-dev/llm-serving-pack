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
export let rules: PolicyRule[] = mock.rules
export let detectors: Detector[] = mock.detectors
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
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(API_BASE + path, {
    ...init,
    headers: { Accept: 'application/json', ...(init?.body ? { 'Content-Type': 'application/json' } : {}), ...init?.headers },
  })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new ApiError(res.status, body?.error?.code ?? 'http_error', body?.error?.message ?? `${res.status} ${res.statusText}`)
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
  const [t, m, b, r, k, bu, ru, d, rc, ts, ss, ch, se] = await Promise.all([
    api<Team[]>('/teams'),
    api<Model[]>('/models'),
    api<Backend[]>('/backends'),
    api<Route[]>('/routes'),
    api<WireKey[]>('/keys'),
    api<Budget[]>('/budgets'),
    api<PolicyRule[]>('/rules'),
    api<Detector[]>('/detectors'),
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
  detectors = d
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
  budgetId?: string
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
