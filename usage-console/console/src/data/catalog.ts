// The console's data source. Pages import catalog data from here; which data
// it holds depends on VITE_DATA:
//   mock (default): the seeded fixtures in ./mock, synchronously, as before
//   api:            fetched from the control plane by hydrate() before the app
//                   mounts, then kept live over SSE (see state/app-state)
// Exports are live `let` bindings, so modules that read them at render time,
// or at import time after hydrate(), see the fetched values.
import { ago } from '@/lib/format'
import * as mock from './mock'
import type { ApiKey, Model, Project, Receipt, SeriesPoint, SpendPoint, Session, Summary, Team } from './mock'

export type * from './mock'

export const dataMode: 'api' | 'mock' = import.meta.env.VITE_DATA === 'api' ? 'api' : 'mock'

export const API_BASE = '/api/v1/demo'

export let teams: Team[] = mock.teams
export let models: Model[] = mock.models
export let modelById: Record<string, Model> = mock.modelById
export let keys: ApiKey[] = mock.keys
export let keyById: Record<string, ApiKey> = mock.keyById
/** Every team's projects. */
export let projects: Project[] = mock.projects
export let seedReceipts: Receipt[] = mock.seedReceipts
export let trafficSeries: SeriesPoint[] = mock.trafficSeries
export let spendSeries: SpendPoint[] = mock.spendSeries
export let now: () => number = mock.now
export let session: Session = mock.session
/** Mock-mode fixture for GET /summary. */
export const seedSummary: Summary = mock.summary
/** Mock only: the fixtures' one price per model. */
export const seedRates: Record<string, mock.MockModel> | null = dataMode === 'api' ? null : mock.modelById

/** GET `path` under the console's API; a failed answer throws its error message. */
async function get(path: string): Promise<Response> {
  const res = await fetch(API_BASE + path, { headers: { Accept: 'application/json' } })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error?.message ?? `${res.status} ${res.statusText}`)
  }
  return res
}

/** GET `path` under the console's API as JSON. */
export async function api<T>(path: string): Promise<T> {
  return (await get(path)).json() as Promise<T>
}

/**
 * GET a page of receipts. `continueBefore` is set when the server stopped
 * looking before it filled the page: older matches may exist before it.
 */
export async function apiReceipts<T>(path: string): Promise<{ rows: T[]; continueBefore: number | null }> {
  const res = await get(path)
  const more = res.headers.get('X-Receipts-Continue-Before')
  return { rows: (await res.json()) as T[], continueBefore: more ? Number(more) : null }
}

// ---- sign-out ---------------------------------------------------------------

/**
 * Signs out at the session's sign-out URL: the gateway's logout path, which
 * ends the gateway session and Keycloak's. It's a page navigation, since the
 * gateway answers with redirects to Keycloak.
 */
export function signOut() {
  if (session.auth.signOutUrl) window.location.assign(session.auth.signOutUrl)
}

/** The control plane sends lastUsedAt (epoch ms); screens show "12s ago". */
export type WireKey = Omit<ApiKey, 'lastUsed'> & { lastUsedAt: number | null }
export const fromWire = (k: WireKey): ApiKey => ({ ...k, lastUsed: k.lastUsedAt ? ago(k.lastUsedAt) : 'never' })

const index = <T,>(xs: T[], id: (x: T) => string) => Object.fromEntries(xs.map((x) => [id(x), x]))

/** Loads the catalog from the control plane. A no-op in mock mode. */
export async function hydrate() {
  if (dataMode !== 'api') return
  session = await api<Session>('/session')
  const [t, pr, m, k, rc, ts, ss] = await Promise.all([
    api<Team[]>('/teams'),
    api<Project[]>('/projects'),
    api<Model[]>('/models'),
    api<WireKey[]>('/keys'),
    api<Receipt[]>('/receipts?limit=240'),
    api<SeriesPoint[]>('/series/traffic?range=24h'),
    api<SpendPoint[]>('/series/spend?days=30'),
  ])
  teams = t
  projects = pr
  models = m
  modelById = index(m, (x) => x.id)
  keys = k.map(fromWire)
  keyById = index(keys, (x) => x.id)
  seedReceipts = rc
  trafficSeries = ts
  spendSeries = ss
  const loadedAt = Date.now()
  now = () => loadedAt
}
