// The console's data source. Pages import catalog data from here; which data
// it holds depends on VITE_DATA:
//   mock (default): the seeded fixtures in ./mock, synchronously, as before
//   api:            fetched from the control plane by hydrate() before the app
//                   mounts, then kept live over SSE (see state/app-state)
// Exports are live `let` bindings, so modules that read them at render time,
// or at import time after hydrate(), see the fetched values.
import { ago } from '@/lib/format'
import * as mock from './mock'
import type { ApiKey, Backend, Budget, BudgetKey, Change, Degradation, Model, PolicyRule, Project, Receipt, SeriesPoint, SpendPoint, Session, Summary, Team } from './mock'

// ---- routes (api mode) ------------------------------------------------------
// Routes are desired state in the control plane; each compiles to one rule of
// the gateway's AIGatewayRoute. Traffic reads them for content capture.

interface RouteTarget {
  backend: string
  /** Replaces the requested model (modelNameOverride); empty passes it through. */
  model?: string
  /** Splits traffic between targets; only with more than one. */
  weight?: number
}

export interface LiveRoute {
  name: string
  /** Any of models (exact names, or one "prefix*" or "*"), and every header. */
  match: { models: string[]; headers: { name: string; value: string }[] }
  targets: RouteTarget[]
  /** Tried in order when the targets fail. */
  fallback: RouteTarget[]
  captureContent?: boolean
  /** synced: the gateway runs it as it is; pending: not applied yet; failed: the last apply didn't take. */
  sync: 'synced' | 'pending' | 'failed' | 'not_reconciled'
  /** The AIGatewayRoute rule it compiles to. */
  yaml: string
}

export type * from './mock'

export const dataMode: 'api' | 'mock' = import.meta.env.VITE_DATA === 'api' ? 'api' : 'mock'

export const API_BASE = '/api/v1/demo'

export let teams: Team[] = mock.teams
export let models: Model[] = mock.models
export let modelById: Record<string, Model> = mock.modelById
export let backends: Backend[] = mock.backends
export let liveRoutes: LiveRoute[] = []
export let keys: ApiKey[] = mock.keys
export let keyById: Record<string, ApiKey> = mock.keyById
export let budgets: Budget[] = mock.budgets
/** The budget that decides a key's requests (the gateway's rule); see mock.governingBudget. */
export const governingBudget = (k: BudgetKey) => mock.governingBudget(k, budgets)
export const budgetLabel = mock.budgetLabel
export const throttleRate = mock.throttleRate
/** Every team's projects, keys or not. Forms re-read GET /projects when they open. */
export let projects: Project[] = mock.projects
/** Policies in order (GET /policies), for Overview. Mock mode shows each fixture rule as a policy. */
export let policies: PolicySummary[] = mock.rules
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
/** Mock-mode Spend callouts. Api mode has no surge rule, and reads savings from GET /spend/savings. */
export const seedSpendSurge = dataMode === 'api' ? null : mock.spendSurge
export const seedSavings = dataMode === 'api' ? null : mock.savings
/** Mock-mode fixture for GET /summary. */
export const seedSummary: Summary = mock.summary
/** Mock only: the fixtures' one price per model. Api mode prices per (model, backend) on /pricing. */
export const seedRates: Record<string, mock.MockModel> | null = dataMode === 'api' ? null : mock.modelById

/** GET `path` under the console's API; a failed answer throws its error message. */
export async function api<T>(path: string): Promise<T> {
  const res = await fetch(API_BASE + path, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error?.message ?? `${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

// ---- sign-out ---------------------------------------------------------------

/** Ends the console session and Keycloak's, then lands back on the console. */
export async function signOut() {
  const res = await fetch(session.auth.signOutUrl ?? '/api/auth/logout', { method: 'POST' })
  const body = (await res.json().catch(() => null)) as { redirect?: string } | null
  window.location.assign(body?.redirect ?? '/')
}

// ---- spend savings (api mode) -----------------------------------------------
// GET /spend/savings: requests a cheaper same-family model would plausibly
// have served, over the last 30 days. openmeter-api finds none.

/** Why a group's other served requests weren't counted, each under its first reason. */
interface SavingsExcluded {
  unpriced: number
  keyInactive: number
  longOutput: number
  overContext: number
  targetUnpriced: number
}

/** One group's best move. `alias` when its requests came through one (retargeting moves them); else `key` asked for `model` by name. */
export interface LiveSavingsOpportunity {
  id: string
  alias?: string
  key?: string
  keyId?: string
  model: string
  target: string
  targetBackend: string
  requests: number
  actualUsd: number
  targetUsd: number
  savedUsd: number
  served: number
  excluded: SavingsExcluded
  keys: string[]
  /** Keys that don't allow `target` yet: they'd be refused until it's added. */
  notAllowedKeys: string[]
  notAllowedRequests: number
}

export interface SavingsView {
  from: number
  to: number
  /** Earliest served request in the window; 0 when none. */
  firstAt: number
  days: number
  /** Longest answer (output tokens, reasoning included) counted as short. */
  outputLimit: number
  served: number
  unpriced: number
  /** Ran on a model other than the one asked for (policy, fallback, or an alias retargeted since). */
  rerouted: number
  opportunities: LiveSavingsOpportunity[]
}

/** The control plane sends lastUsedAt (epoch ms); screens show "12s ago". */
export type WireKey = Omit<ApiKey, 'lastUsed'> & { lastUsedAt: number | null }
export const fromWire = (k: WireKey): ApiKey => ({ ...k, lastUsed: k.lastUsedAt ? ago(k.lastUsedAt) : 'never' })

const index = <T,>(xs: T[], id: (x: T) => string) => Object.fromEntries(xs.map((x) => [id(x), x]))

/** Loads the catalog from the control plane. A no-op in mock mode. */
export async function hydrate() {
  if (dataMode !== 'api') return
  session = await api<Session>('/session')
  const [t, pr, m, b, r, k, bu, ru, rc, ts, ss, ch] = await Promise.all([
    api<Team[]>('/teams'),
    api<Project[]>('/projects'),
    api<Model[]>('/models'),
    api<Backend[]>('/backends'),
    api<LiveRoute[]>('/routes'),
    api<WireKey[]>('/keys'),
    api<Budget[]>('/budgets'),
    api<PolicySummary[]>('/policies'),
    api<Receipt[]>('/receipts?limit=240'),
    api<SeriesPoint[]>('/series/traffic?range=24h'),
    api<SpendPoint[]>('/series/spend?days=30'),
    api<Change[]>('/changes'),
  ])
  teams = t
  projects = pr
  models = m
  modelById = index(m, (x) => x.id)
  backends = b
  liveRoutes = r
  keys = k.map(fromWire)
  keyById = index(keys, (x) => x.id)
  budgets = bu
  policies = ru
  seedReceipts = rc
  trafficSeries = ts
  spendSeries = ss
  changes = ch
  const loadedAt = Date.now()
  now = () => loadedAt
}

// ---- policies --------------------------------------------------------------

/** What Overview reads of a policy. Mock fixtures (one rule each) fit it too. */
type PolicySummary = Pick<PolicyRule, 'id' | 'ordinal' | 'name' | 'description' | 'mode' | 'failMode' | 'version' | 'fired24h' | 'baseline7d'>
