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

// ---- routes (api mode, §4.4) -----------------------------------------------
// Routes are desired state in the control plane; each compiles to one rule of
// the gateway's AIGatewayRoute. Traffic reads them for content capture.

export interface RouteTarget {
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
  etag: string
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
export const coveringBudgets = (k: BudgetKey) => budgets.filter((b) => mock.budgetCovers(b, k))
export const budgetLabel = mock.budgetLabel
export const throttleRate = mock.throttleRate
export const THROTTLE_PER_MINUTE = mock.THROTTLE_PER_MINUTE
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
    const err = new ApiError(res.status, body?.error?.code ?? 'http_error', body?.error?.message ?? `${res.status} ${res.statusText}`, body?.current)
    if (err.status === 401 && err.code === 'unauthenticated' && signedIn) signIn(body?.error?.loginUrl)
    throw err
  }
  return res.json() as Promise<T>
}

// ---- sign-in and roles (backend-decisions §7) ------------------------------
// With an IdP the control plane signs the console in (OIDC code flow, an
// HttpOnly session cookie); without one (dev mode) every caller is the dev
// user, owner. Roles come from Keycloak groups; GET /session says which
// actions the caller may do, and the console disables the rest.

/** Set once hydrate() has a session: a later 401 means it expired mid-use. */
let signedIn = false

/** Sends the browser to Keycloak (via the control plane), coming back to where it is now. */
export function signIn(loginUrl = '/api/auth/login') {
  const next = window.location.pathname + window.location.search
  window.location.assign(`${loginUrl}?next=${encodeURIComponent(next)}`)
}

/** Ends the console session and Keycloak's, then lands back on the console. */
export async function signOut() {
  const res = await fetch(session.auth.signOutUrl ?? '/api/auth/logout', { method: 'POST' })
  const body = (await res.json().catch(() => null)) as { redirect?: string } | null
  window.location.assign(body?.redirect ?? '/')
}

/** "admin or security" */
const orList = (xs: string[]) => (xs.length < 2 ? (xs[0] ?? '') : `${xs.slice(0, -1).join(', ')} or ${xs[xs.length - 1]}`)

/** Whether the caller may do `action`; if not, why, in spec §7.6's words ("Needs role admin or finance"). Mock mode may do everything. */
export function can(action: mock.Action): { ok: boolean; reason?: string } {
  if (dataMode !== 'api') return { ok: true }
  const p = session.permissions?.[action]
  if (!p || p.allowed) return { ok: true }
  return { ok: false, reason: `Needs role ${p.roles.length ? orList(p.roles) : 'owner'}` }
}

/** Whether the caller may revoke, rotate, extend or finish `k`: its owner, or admin. */
export function canManageKey(k: Pick<ApiKey, 'owner'>): { ok: boolean; reason?: string } {
  if (dataMode !== 'api' || k.owner === session.actor.email) return { ok: true }
  const any = can('keys.any')
  return any.ok ? any : { ok: false, reason: `${any.reason}: this key belongs to ${k.owner}` }
}

/** What a signed export (§5.1, §9.2) downloaded: the zip's name, how many receipts and the key that signed them. */
export interface SignedExport {
  filename: string
  count: number
  keyId: string
}

/**
 * POSTs to a receipt export endpoint (/receipts/export?…, /receipts/{id}/export)
 * and saves the zip it returns: receipts.jsonl, its Ed25519 signature, the
 * public key and a README saying how to verify. The server writes the audit
 * row before it answers.
 */
export async function downloadSignedExport(path: string): Promise<SignedExport> {
  const res = await fetch(API_BASE + path, { method: 'POST' })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new ApiError(res.status, body?.error?.code ?? 'http_error', body?.error?.message ?? `${res.status} ${res.statusText}`)
  }
  const filename = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'receipts.zip'
  const url = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
  return { filename, count: Number(res.headers.get('X-Stargate-Export-Count') ?? 0), keyId: res.headers.get('X-Stargate-Signing-Key') ?? '' }
}

/** A file from the control plane (the close report PDF), with the name it gives it. Errors as api() does. */
export async function apiFile(path: string): Promise<{ blob: Blob; name: string; type: string }> {
  const res = await fetch(API_BASE + path)
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new ApiError(res.status, body?.error?.code ?? 'http_error', body?.error?.message ?? `${res.status} ${res.statusText}`)
  }
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'download'
  return { blob: await res.blob(), name, type: res.headers.get('Content-Type') ?? '' }
}

// ---- spend savings (api mode, §7.5.5) ---------------------------------------
// GET /spend/savings: requests a cheaper same-family model would plausibly
// have served, priced from raw receipts (the last 30 days). See
// server/internal/api/savings.go for the method.

/** Why a group's other served requests weren't counted, each under its first reason. */
export interface SavingsExcluded {
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

/** hydrate() sent the browser to sign in; nothing to render meanwhile. */
export class SigningIn extends Error {
  constructor() {
    super('Signing in…')
  }
}

/** Signed in, but in no Stargate group: every read is refused. */
export class NoRole extends Error {
  readonly session: Session
  constructor(s: Session) {
    super(`${s.actor.email} has no Stargate role`)
    this.session = s
  }
}

/** Loads the catalog from the control plane. A no-op in mock mode. */
export async function hydrate() {
  if (dataMode !== 'api') return
  // Who's asking first: not signed in goes to Keycloak, and someone with no
  // Stargate role can't read the rest.
  let se: Session
  try {
    se = await api<Session>('/session')
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      signIn()
      throw new SigningIn()
    }
    throw e
  }
  session = se
  signedIn = true
  if (!se.actor.roles?.length) throw new NoRole(se)
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

// ---- key lifecycle ---------------------------------------------------------
// The server generates and hashes secrets in api mode; mock mode keeps the
// mockup's client-side stand-ins so the flows still demo without a backend.

export interface NewKeyInput {
  name: string
  team: string
  /** One of the team's projects; create it first (createProject). */
  projectId: string
  allowedModels: string[]
  allowedRegions: string[]
  expiresAt: string | null
}

function mockSecret() {
  const alphabet = 'abcdefghijklmnopqrstuvwxyz0123456789'
  return 'ngw_live_' + Array.from({ length: 40 }, () => alphabet[Math.floor(Math.random() * alphabet.length)]).join('')
}

/** Records a project, or its new name, in the catalog. */
const storeProject = (p: Project) => {
  projects = projects.some((x) => x.id === p.id) ? projects.map((x) => (x.id === p.id ? p : x)) : [...projects, p]
}

/** Creates a key in one of its team's projects; the server refuses a project the team doesn't have. */
export async function createKey(input: NewKeyInput): Promise<{ key: ApiKey; secret: string }> {
  if (dataMode === 'api') {
    const res = await api<{ key: WireKey; secret: string }>('/keys', { method: 'POST', body: JSON.stringify(input) })
    return { key: fromWire(res.key), secret: res.secret }
  }
  const secret = mockSecret()
  const project = projects.find((p) => p.id === input.projectId && p.team === input.team)
  if (!project) throw new ApiError(400, 'bad_request', `Project ${input.projectId} isn't one of team ${input.team}'s projects.`)
  const key: ApiKey = {
    ...input,
    project: project.name,
    id: 'k' + Math.random().toString(36).slice(2, 7),
    prefix: secret.slice(0, 13),
    lastUsed: 'never',
    requests24h: 0,
    hourly24h: Array(24).fill(0),
    status: 'active',
    owner: session.actor.email,
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

/** Both secrets keep working for `hours` more. The overlap can't end more than 7 days from now. */
export async function extendRotation(k: ApiKey, hours: number): Promise<ApiKey> {
  if (dataMode === 'api') return fromWire(await api<WireKey>(`/keys/${k.id}/rotation/extend`, { method: 'POST', body: JSON.stringify({ hours }) }))
  const from = Math.max(k.rotation?.endsAt ?? 0, Date.now())
  return { ...k, rotation: { ...k.rotation!, endsAt: from + hours * 3_600_000 } }
}

/** Retires the old secret now, instead of when the overlap ends. */
export async function finishRotation(k: ApiKey): Promise<ApiKey> {
  if (dataMode === 'api') return fromWire(await api<WireKey>(`/keys/${k.id}/rotation/finish`, { method: 'POST' }))
  return { ...k, status: 'active', rotation: undefined }
}

/** Projects of `team` other than `except` already using `name`, ignoring case and outer spaces, as the server compares. */
export const projectNameTaken = (list: Project[], team: string, name: string, except?: string) =>
  list.some((x) => x.team === team && x.id !== except && x.name.trim().toLowerCase() === name.trim().toLowerCase())

/** Adds a project to a team, so it can have keys and a budget. A name the team has (ignoring case) is a 409. */
export async function createProject(team: string, name: string): Promise<Project> {
  let p: Project
  name = name.trim()
  if (dataMode === 'api') p = await api<Project>('/projects', { method: 'POST', body: JSON.stringify({ team, name }) })
  else {
    if (projectNameTaken(projects, team, name)) throw new ApiError(409, 'conflict', `Team ${team} already has a project named ${name}.`)
    p = { id: mock.mockProjectId(team, name), team, name }
  }
  storeProject(p)
  return p
}

/** Renames `p` as the caller last saw it. Keys, budgets, rules and receipts follow it by id. */
export async function renameProject(p: Project, name: string): Promise<Project> {
  name = name.trim()
  let next: Project
  if (dataMode === 'api') next = await api<Project>(`/projects/${p.id}`, { method: 'PUT', body: JSON.stringify({ name }), headers: { 'If-Match': p.etag ?? '' } })
  else {
    if (projectNameTaken(projects, p.team, name, p.id)) throw new ApiError(409, 'conflict', `Team ${p.team} already has a project named ${name}.`)
    next = { ...p, name }
  }
  storeProject(next)
  keys = keys.map((k) => (k.projectId === p.id ? { ...k, project: next.name } : k))
  return next
}

/**
 * Deletes `p`. The server refuses (409, saying why) while it has an active
 * key or a budget; mock mode checks the same.
 */
export async function deleteProject(p: Project): Promise<void> {
  if (dataMode === 'api') await api(`/projects/${p.id}`, { method: 'DELETE', headers: { 'If-Match': p.etag ?? '' } })
  else {
    const active = keys.filter((k) => k.projectId === p.id && k.status !== 'revoked')
    if (active.length || budgets.some((b) => b.scopeType === 'project' && b.scope === p.id))
      throw new ApiError(409, 'conflict', `Project ${p.name} still has active keys or a budget. Revoke the keys and delete the budget first.`)
  }
  projects = projects.filter((x) => x.id !== p.id)
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

/** Mock mode's name for a scope, as the control plane fills in scopeName. */
function mockScopeName(input: BudgetInput) {
  if (input.scopeType === 'key') return keys.find((k) => k.id === input.scope)?.name
  if (input.scopeType === 'project') return projects.find((p) => p.id === input.scope)?.name
  return undefined
}

const blankBudget = (input: BudgetInput): Budget => ({ ...input, scopeName: mockScopeName(input), id: '', period: 'monthly', currentUsd: 0, projectedUsd: 0, trailingDailyUsd: 0 })

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

// ---- policies --------------------------------------------------------------

/** What Overview reads of a policy. Mock fixtures (one rule each) fit it too. */
export type PolicySummary = Pick<PolicyRule, 'id' | 'ordinal' | 'name' | 'description' | 'mode' | 'failMode' | 'version' | 'fired24h' | 'baseline7d'>
