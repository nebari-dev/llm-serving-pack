// Live check against a running control plane: hydrate from the API, then
// render every route and open a receipt. It writes keys, rules, budgets and
// audit rows, so it runs against the test stack, never the dev one:
//   (cd ../server && scripts/test-stack.sh up) && npm run test:api
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

const base = import.meta.env.VITE_STARGATE_API as string | undefined

/** The open form dialog (toasts have role="dialog" too). */
const formDialog = () =>
  waitFor(() => {
    const d = document.querySelector<HTMLElement>('[data-slot="dialog-content"]')
    expect(d).toBeTruthy()
    return d!
  })
const formDialogClosed = () => waitFor(() => expect(document.querySelector('[data-slot="dialog-content"]')).toBeNull(), { timeout: 5000 })

/** Picks a Select option the way a mouse does: Base UI ignores a click that didn't start with pointerdown on the item. */
const choose = (option: HTMLElement) => {
  fireEvent.pointerDown(option, { pointerType: 'mouse' })
  fireEvent.click(option)
}

/** Records the tab's open streams, and lets a test push events down them. */
class FakeEventSource {
  static open = new Set<FakeEventSource>()
  private listeners = new Map<string, ((e: MessageEvent<string>) => void)[]>()
  readonly url: string
  constructor(url: string) {
    this.url = url
    FakeEventSource.open.add(this)
  }
  addEventListener(type: string, f: (e: MessageEvent<string>) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), f])
  }
  close() {
    FakeEventSource.open.delete(this)
  }
  emit(type: string, data: unknown) {
    for (const f of this.listeners.get(type) ?? []) f(new MessageEvent(type, { data: JSON.stringify(data) }))
  }
}

/** A signed export's files, read from the zip's central directory (Go writes sizes there, not in the local headers). */
async function unzipExport(zip: ArrayBuffer): Promise<Record<string, Uint8Array>> {
  // The suite runs in Node; the app's tsconfig has no Node types, hence the cast.
  const { inflateRawSync } = (await import(/* @vite-ignore */ 'node:' + 'zlib')) as { inflateRawSync: (b: Uint8Array) => Uint8Array }
  const b = new Uint8Array(zip)
  const v = new DataView(zip)
  let eocd = b.length - 22
  while (eocd >= 0 && v.getUint32(eocd, true) !== 0x06054b50) eocd--
  const entries = v.getUint16(eocd + 10, true)
  let p = v.getUint32(eocd + 16, true)
  const out: Record<string, Uint8Array> = {}
  for (let i = 0; i < entries; i++) {
    const method = v.getUint16(p + 10, true)
    const size = v.getUint32(p + 20, true)
    const nameLen = v.getUint16(p + 28, true)
    const extraLen = v.getUint16(p + 30, true)
    const commentLen = v.getUint16(p + 32, true)
    const local = v.getUint32(p + 42, true)
    const name = new TextDecoder().decode(b.subarray(p + 46, p + 46 + nameLen))
    const start = local + 30 + v.getUint16(local + 26, true) + v.getUint16(local + 28, true)
    const data = b.subarray(start, start + size)
    out[name] = method === 8 ? new Uint8Array(inflateRawSync(data)) : data
    p += 46 + nameLen + extraLen + commentLen
  }
  return out
}

describe.skipIf(!base || import.meta.env.VITE_DATA !== 'api')('api mode against a live control plane', () => {
  let App: typeof import('@/App').default
  let catalog: typeof import('@/data/catalog')

  beforeAll(async () => {
    const realFetch = globalThis.fetch
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) =>
      realFetch(typeof input === 'string' && input.startsWith('/') ? base + input : input, init),
    )
    // jsdom has no EventSource; the stream itself is covered by the server.
    vi.stubGlobal('EventSource', FakeEventSource)
    window.matchMedia ??= ((q: string) => ({
      matches: false, media: q, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    })) as unknown as typeof window.matchMedia
    globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver

    catalog = await import('@/data/catalog')
    // Refuse the dev stack: this suite's writes would land in the console's own history.
    const env = (await catalog.api<{ environment: string }>('/session')).environment
    if (env !== 'test') throw new Error(`${base} is the "${env}" control plane; run the suite against the test stack (server/scripts/test-stack.sh up, npm run test:api)`)
    // POST /keys no longer makes a project (decided 2026-10-05): the suite's
    // keys go in an "api-mode-test" project, made on each team if missing.
    for (const t of await catalog.api<{ id: string }[]>('/teams'))
      await catalog
        .api('/projects', { method: 'POST', body: JSON.stringify({ team: t.id, name: 'api-mode-test' }) })
        .catch((e: unknown) => {
          if (!(e instanceof catalog.ApiError && e.status === 409)) throw e
        })
    await catalog.hydrate()
    App = (await import('@/App')).default
  })

  afterEach(() => cleanup())

  it('hydrates the catalog from the API', () => {
    expect(catalog.dataMode).toBe('api')
    expect(catalog.teams.length).toBeGreaterThan(0)
    expect(catalog.seedReceipts.length).toBeGreaterThan(0)
    expect(catalog.trafficSeries).toHaveLength(48)
    expect(catalog.spendSeries).toHaveLength(30)
    expect(catalog.keys.find((k) => k.id === 'k1')?.lastUsed).toMatch(/ago$/)
    // Keys reference a project of their own team (§5.2); seeded ones have one each.
    const k1 = catalog.keys.find((k) => k.id === 'k1')!
    expect(catalog.projects.find((p) => p.id === k1.projectId)).toMatchObject({ team: 'support', name: 'helpdesk' })
    // Revoked keys may be in a project deleted since (their history keeps its name).
    for (const k of catalog.keys.filter((x) => x.status !== 'revoked')) expect(catalog.projects.find((p) => p.id === k.projectId)).toMatchObject({ team: k.team, name: k.project })
    // The seeded key budget names batch-summarize by id.
    expect(catalog.budgets.find((b) => b.id === 'b3')).toMatchObject({ scopeType: 'key', scope: 'k3', scopeName: 'batch-summarize' })
  })

  const routes = ['/', '/traffic', '/spend', '/models', '/routing', '/guardrails', '/keys', '/keys?key=k1', '/activity', '/settings', '/onboarding', '/guardrails?rule=r3', '/routing?tab=backends']
  for (const route of routes) {
    it(`renders ${route}`, async () => {
      const errors: unknown[] = []
      const spy = vi.spyOn(console, 'error').mockImplementation((...a) => errors.push(a))
      window.history.pushState({}, '', route)
      const { container } = render(<App />)
      await act(async () => {})
      expect(container.querySelector('h1')?.textContent).toBeTruthy()
      spy.mockRestore()
      expect(errors).toEqual([])
    })
  }

  it('opens a live receipt, and fetches one outside the loaded window', async () => {
    const r = catalog.seedReceipts[0]
    window.history.pushState({}, '', `/traffic?receipt=${r.id}`)
    render(<App />)
    await act(async () => {})
    expect(document.body.textContent).toContain(r.traceId)
    cleanup()

    const older = await catalog.api<{ id: string; traceId: string }[]>(`/receipts?limit=1&before=${r.ts - 3 * 86_400_000}`)
    window.history.pushState({}, '', `/traffic?receipt=${older[0].id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    expect(document.body.textContent).toContain(older[0].traceId)
    cleanup()

    // A recent served receipt carries the gateway's own time on it.
    const [served] = (await catalog.api<{ id: string; overheadUs?: number }[]>('/receipts?limit=50')).filter((x) => x.overheadUs != null)
    expect(served).toBeDefined()
    window.history.pushState({}, '', `/traffic?receipt=${served.id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    expect(document.body.textContent).toContain(`${(served.overheadUs! / 1000).toFixed(1)}ms gateway overhead`)
  })

  it('filters, pages and counts receipts on the server', async () => {
    type R = { id: string; ts: number; verdict: string; keyId: string; backend: string; costUsd: number | null; cacheWriteTokens: number; costBasis?: { inPerM: number | null; backend?: string }; status: number }
    const blocked = await catalog.api<R[]>('/receipts?limit=50&verdict=blocked&range=7d')
    expect(blocked.length).toBeGreaterThan(0)
    expect(blocked.every((r) => r.verdict === 'blocked')).toBe(true)

    const key = catalog.seedReceipts[0].keyId
    const byKey = await catalog.api<R[]>(`/receipts?limit=20&key=${key}`)
    expect(byKey.every((r) => r.keyId === key)).toBe(true)

    // Paging with before= neither repeats nor skips rows.
    const p1 = await catalog.api<R[]>('/receipts?limit=10')
    const p2 = await catalog.api<R[]>(`/receipts?limit=10&before=${p1.at(-1)!.ts}`)
    expect(p2.every((r) => r.ts < p1.at(-1)!.ts)).toBe(true)
    expect(new Set([...p1, ...p2].map((r) => r.id)).size).toBe(20)

    // range narrows to the window, starting on a 5-minute bucket.
    const hour = await catalog.api<R[]>('/receipts?limit=1000&range=1h')
    const start = Math.floor((Date.now() - 3_600_000) / 300_000) * 300_000
    expect(hour.every((r) => r.ts >= start)).toBe(true)

    const count = await catalog.api<{ count: number | null; since: number }>('/receipts/count?range=1h')
    expect(count.since % 300_000).toBe(0)
    expect(count.count).toBeGreaterThan(0)
    const uncountable = await catalog.api<{ count: number | null; reason: string }>('/receipts/count?range=1h&project=x')
    expect(uncountable.count).toBeNull()
    expect(uncountable.reason).toBeTruthy()

    // Settled, successful receipts carry the price snapshot they were costed
    // with, for the backend that served them; with no price, no cost (not $0).
    const settled = p1.filter((r) => r.status === 200 && !('inFlight' in r))
    for (const r of settled) {
      if (r.costUsd === null) expect(r.costBasis).toBeUndefined()
      else expect(r.costBasis?.inPerM).toBeGreaterThanOrEqual(0)
      expect(r.cacheWriteTokens).toBeGreaterThanOrEqual(0)
    }
    const fresh = settled.find((r) => r.costBasis?.backend)
    if (fresh) expect(fresh.costBasis!.backend).toBe(fresh.backend)
  })

  it('shows the price snapshot, policy mode and what is not connected in the drawer', async () => {
    const [r] = await catalog.api<{ id: string; traceId: string }[]>('/receipts?limit=1&verdict=allowed&range=24h')
    window.history.pushState({}, '', `/traffic?receipt=${r.id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    const text = document.body.textContent ?? ''
    expect(text).toContain(r.traceId)
    // Priced at the snapshot for the backend that served it, or plainly unpriced.
    expect(text).toMatch(/rates (on \S+ )?recorded with this receipt|had no price when this request arrived/)
    expect(text).toContain('Policy mode:')
    expect(text).not.toContain("Signed export isn't connected yet") // built: the drawer exports it signed
    expect(text).not.toContain('model_pricing row effective')
    expect(text).not.toContain('Simulate burst')
  })

  it('checks the budgets covering a key by scope, not only the one it names', async () => {
    // Keys don't name a budget (budget_id is gone); research's team budget governs it.
    const key = catalog.keys.find((k) => k.name === 'research')!
    expect(catalog.keys.filter((k) => 'budgetId' in k).map((k) => k.name)).toEqual([])
    const [r] = await catalog.api<{ id: string; trace: { step: string; input: string }[] }[]>(`/receipts?limit=1&key=${key.id}&range=1h`)
    expect(r.trace.find((s) => s.step === 'Budget checked')?.input).toMatch(/^team budget research · \$[\d.]+ of \$20000$/)
    window.history.pushState({}, '', `/traffic?receipt=${r.id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    expect(document.body.textContent).toContain('team budget research · $')
  })

  // No reconciler exists (§4.4), so routing is read-only and nothing claims a
  // sync state, a reconcile event or a failover it didn't observe.
  it('reports backend health, p50 and errors from receipts, never the seed', async () => {
    type B = { name: string; health: string; p50: number; errorRate: number; requests1h: number }
    const bs = await catalog.api<B[]>('/backends')
    for (const b of bs) expect(['healthy', 'degraded', 'down', 'idle']).toContain(b.health)
    // Nothing routes to azure-openai-eu, so it has no receipts: idle, not the seeded "down" at 100%.
    expect(bs.find((b) => b.name === 'azure-openai-eu')).toMatchObject({ health: 'idle', p50: 0, errorRate: 0, requests1h: 0 })
    expect(bs.map((b) => b.name)).toEqual(expect.arrayContaining(['local', 'openrouter']))

    window.history.pushState({}, '', '/')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    const strip = screen.getByLabelText('System status').textContent ?? ''
    expect(strip).not.toContain('not routed')
    expect(strip).toMatch(/\d+ idle/)
    r.unmount()

    window.history.pushState({}, '', '/routing?tab=backends')
    const r2 = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    await act(async () => {
      fireEvent.click(screen.getAllByRole('button', { name: 'azure-openai-eu' })[0])
      await new Promise((ok) => setTimeout(ok, 200))
    })
    const text = document.body.textContent ?? ''
    expect(text).not.toContain('as configured')
    expect(text).toContain('No requests in the last 15 minutes')
    r2.unmount()
  })

  it('onboards for real: a live backend, a new key, and a request through the gateway that lands as a receipt', async () => {
    window.history.pushState({}, '', '/onboarding')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 400))
    })
    const text = () => document.body.textContent ?? ''
    for (const fake of ['gw.acme.dev', 'Mockup tip', 'ngw_live_7f3a91c4', 'models available']) expect(text()).not.toContain(fake)
    // The backends the control plane has, with their observed health.
    choose(screen.getByRole('radio', { name: /^local/ }))
    expect(text()).toContain('smollm2')
    let keyId = ''
    try {
      // The key goes in one of the team's projects: its "onboarding" project,
      // picked, or made with the key when the team has none by that name.
      await waitFor(() => expect(screen.getByRole('button', { name: 'Create a key for local' })).toHaveProperty('disabled', false), { timeout: 5000 })
      const picked = (screen.getByRole('combobox', { name: 'Project' }).textContent ?? '').replace('▼', '').trim() // the trigger's chevron is text
      expect(picked === 'onboarding' || (picked === 'New project…' && (screen.getByLabelText('New project name') as HTMLInputElement).value === 'onboarding')).toBe(true)
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Create a key for local' }))
      })
      const { gatewayUrl } = await catalog.api<{ gatewayUrl: string }>('/session')
      await waitFor(() => expect(text()).toContain(gatewayUrl), { timeout: 5000 })
      keyId = (await catalog.api<{ id: string; name: string; project: string }[]>('/keys')).find((k) => k.project === 'onboarding' && text().includes(k.name))?.id ?? ''
      expect(keyId).not.toBe('')
      expect(text()).toMatch(/ngw_live_\w{4}/)
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Send a test request for me' }))
      })
      // The real model answers, and the page becomes that request's receipt.
      await waitFor(() => expect(text()).toContain('Your first request'), { timeout: 60_000 })
      expect(text()).toContain('via local')
      expect(text()).toMatch(/smollm2/)
    } finally {
      if (keyId) await catalog.api(`/keys/${keyId}/revoke`, { method: 'POST' })
      r.unmount()
    }
  }, 90_000)

  it('measures gateway overhead p50 from receipts against the 10ms goal, on Overview', async () => {
    type O = { p50Ms: number | null; p95Ms: number | null; samples: number; windowMinutes: number; goalMs: number }
    const o = await catalog.api<O>('/gateway/overhead')
    expect(o).toMatchObject({ windowMinutes: 60, goalMs: 10 })
    expect(o.samples).toBeGreaterThan(0) // trafficgen is running
    expect(o.p50Ms).toBeGreaterThan(0)
    expect(o.p95Ms!).toBeGreaterThanOrEqual(o.p50Ms!)

    window.history.pushState({}, '', '/')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    const strip = screen.getByLabelText('System status').textContent ?? ''
    expect(strip).toMatch(/p50 overhead \d+(\.\d)?ms/)
    expect(strip).not.toContain('2.1ms') // the mockup's number
    r.unmount()
  })

  it('lists server-filtered traffic, with a provider filter from the backends', async () => {
    window.history.pushState({}, '', '/traffic?verdict=blocked')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const rows = [...document.querySelectorAll('tbody tr[aria-rowindex]')]
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.every((tr) => tr.getAttribute('aria-label')?.endsWith('Blocked'))).toBe(true)
    expect(document.body.textContent).toMatch(/\d[\d,]* matching/)
  })

  it('holds one stream per tab, narrowed to Traffic’s filters, and keeps live rows inside the window', async () => {
    const until = Date.now() + 3_600_000
    window.history.pushState({}, '', `/traffic?verdict=blocked&until=${until}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    // Browsers allow six connections per host over HTTP/1.1: one stream per surface ran three tabs out.
    expect(FakeEventSource.open.size).toBe(1)
    const [es] = FakeEventSource.open
    expect(new URL(es.url, base).searchParams.getAll('verdict')).toEqual(['blocked'])

    const seed = { ...catalog.seedReceipts[0], verdict: 'blocked' as const, inFlight: false }
    es.emit('receipt', { ...seed, id: 'sse-now', ts: Date.now(), resolvedModel: 'sse-test-now' })
    es.emit('receipt', { ...seed, id: 'sse-later', ts: until + 1000, resolvedModel: 'sse-test-later' })
    es.emit('dropped', { count: 3 })
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 400))
    })
    const labels = [...document.querySelectorAll('tbody tr[aria-rowindex]')].map((tr) => tr.getAttribute('aria-label') ?? '')
    expect(labels.some((l) => l.includes('sse-test-now'))).toBe(true)
    expect(labels.some((l) => l.includes('sse-test-later'))).toBe(false)
    expect(document.body.textContent).toContain('Missed 3 receipts')

    // Leaving Traffic widens the same tab's stream again.
    cleanup()
    await act(async () => {})
    expect(FakeEventSource.open.size).toBe(1)
    expect([...FakeEventSource.open][0].url).not.toContain('?')
  })

  it('says when the stream samples, and at what rate, while counts stay exact (§7.5.3)', async () => {
    window.history.pushState({}, '', '/traffic?verdict=blocked')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const counted = document.body.textContent?.match(/([\d,]+) matching/)?.[1]
    expect(counted).toBeTruthy()
    const [es] = FakeEventSource.open
    es.emit('sampling', { oneIn: 20, ratePerSec: 812.4, thresholdPerSec: 40 })
    await act(async () => {})
    const banner = () => screen.queryByRole('status', { name: 'Stream sampling' })?.textContent ?? ''
    expect(banner()).toContain('Sampling 1 in 20. Add a filter to see everything matching.')
    expect(banner()).toContain('About 812 requests a second match, more than the 40 a second')
    // The count is the database's, not the sampled rows'.
    expect(document.body.textContent).toContain(`${counted} matching`)

    // Paused, the "N new" pill says the new rows are a sample.
    fireEvent.click(screen.getByRole('button', { name: /^Live/ }))
    await act(async () => {})
    es.emit('receipt', { ...catalog.seedReceipts[0], id: 'sse-sampled', verdict: 'blocked', inFlight: false, ts: Date.now() + 1000 })
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 400))
    })
    expect(document.body.textContent).toContain('1 new (sampled 1 in 20) · click to resume')

    // When it stops, the list says it has gaps until reloaded.
    es.emit('sampling', { oneIn: 1, ratePerSec: 12, thresholdPerSec: 40 })
    await act(async () => {})
    expect(banner()).toContain('The live rows were sampled for a while')
    fireEvent.click(within(screen.getByRole('status', { name: 'Stream sampling' })).getByRole('button', { name: /Reload the list/ }))
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    expect(screen.queryByRole('status', { name: 'Stream sampling' })).toBeNull()
  })

  it('exports a filtered range as signed JSON Lines that verify against the published key, with an audit row (§5.1, §9.2)', async () => {
    type PublicKey = { readonly type: 'public' }
    const { createPublicKey, verify } = (await import(/* @vite-ignore */ 'node:' + 'crypto')) as {
      createPublicKey: (pem: string) => PublicKey
      verify: (alg: null, data: Uint8Array, key: PublicKey, sig: Uint8Array) => boolean
    }
    const key = await catalog.api<{ keyId: string; algorithm: string; publicKeyPem: string }>('/receipts/signing-key')
    expect(key.algorithm).toBe('Ed25519')
    expect(key.keyId).toMatch(/^[0-9a-f]{16}$/)
    expect(key.publicKeyPem).toMatch(/^-----BEGIN PUBLIC KEY-----/)
    expect(key.publicKeyPem).not.toContain('PRIVATE')

    const since = Date.now() - 3_600_000
    const res = await fetch(`${catalog.API_BASE}/receipts/export?since=${since}&verdict=blocked`, { method: 'POST' })
    expect(res.status).toBe(200)
    expect(res.headers.get('Content-Type')).toBe('application/zip')
    expect(res.headers.get('X-Stargate-Signing-Key')).toBe(key.keyId)
    const count = Number(res.headers.get('X-Stargate-Export-Count'))
    const files = await unzipExport(await res.arrayBuffer())
    expect(Object.keys(files).sort()).toEqual(['README.txt', 'receipts.jsonl', 'receipts.jsonl.sig', 'signing-key.pem'])
    const jsonl = files['receipts.jsonl']
    const pub = createPublicKey(key.publicKeyPem)
    expect(verify(null, jsonl, pub, files['receipts.jsonl.sig'])).toBe(true)
    // One changed byte fails.
    const tampered = jsonl.slice()
    tampered[tampered.length - 3] ^= 1
    expect(verify(null, tampered, pub, files['receipts.jsonl.sig'])).toBe(false)
    expect(new TextDecoder().decode(files['README.txt'])).toContain('openssl pkeyutl -verify')

    const lines = new TextDecoder().decode(jsonl).trimEnd().split('\n')
    const head = JSON.parse(lines[0]).export
    expect(head).toMatchObject({ tenant: 'demo', exportedBy: 'dev@localhost', count, keyId: key.keyId, algorithm: 'Ed25519', filter: { since, verdict: ['blocked'] } })
    expect(lines).toHaveLength(count + 1)
    for (const l of lines.slice(1)) {
      const rc = JSON.parse(l)
      expect(rc.verdict).toBe('blocked')
      expect(rc.inFlight).toBeFalsy() // settled receipts only
    }

    // The audit row says who, what and how many; it isn't a config change.
    const [row] = await catalog.api<{ action: string; target: string; actor: string; targetKind: string }[]>('/changes?kind=Receipt&limit=1')
    expect(row).toMatchObject({ action: 'Exported receipts', target: `${count} ${count === 1 ? 'receipt' : 'receipts'}`, actor: 'dev@localhost', targetKind: 'Receipt' })
    const changes = await catalog.api<{ targetKind: string }[]>('/changes?limit=500')
    expect(changes.some((c) => c.targetKind === 'Receipt')).toBe(false)
  })

  it('exports one receipt signed from the drawer, and from Traffic’s header', async () => {
    const saved: string[] = []
    // jsdom has no object URLs; the download itself is the browser's.
    Object.assign(URL, { createObjectURL: () => 'blob:test', revokeObjectURL: () => {} })
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      saved.push(this.download)
    })
    const [r] = await catalog.api<{ id: string }[]>('/receipts?limit=5&verdict=allowed')
    window.history.pushState({}, '', `/traffic?receipt=${r.id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    const drawer = document.querySelector<HTMLElement>('[data-print-receipt]')!
    fireEvent.click(within(drawer).getByRole('button', { name: /Export signed/ }))
    await waitFor(() => expect(document.body.textContent).toContain('Signed receipt exported'))
    expect(saved).toContain(`receipt-${r.id}.zip`)
    const [row] = await catalog.api<{ action: string; target: string }[]>('/changes?kind=Receipt&limit=1')
    expect(row).toMatchObject({ action: 'Exported receipt', target: r.id })
    cleanup()

    window.history.pushState({}, '', '/traffic?verdict=blocked')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    fireEvent.click(screen.getByRole('button', { name: /Export signed/ }))
    await waitFor(() => expect(document.body.textContent).toMatch(/Exported [\d,]+ receipts?, signed/))
    expect(saved.at(-1)).toMatch(/^receipts-demo-\d{8}T\d{6}Z\.zip$/)
    click.mockRestore()
  })

  it('reveals captured content only after writing an audit row, and says when nothing was captured (§7.5.4, §9.2)', async () => {
    type R = { id: string; contentCaptured: boolean }
    // The test stack's backfill runs the dev gateway's engine, which stores
    // content for vllm-internal (capture_content); Agent Router's receipts never carry it.
    let captured: R | undefined
    for (const daysAgo of [0, 1, 3, 6]) {
      const rows = await catalog.api<R[]>(`/receipts?backend=vllm-internal&limit=1000&before=${Date.now() - daysAgo * 86_400_000}`)
      captured = rows.find((x) => x.contentCaptured)
      if (captured) break
    }
    expect(captured).toBeTruthy()
    const [plain] = (await catalog.api<R[]>('/receipts?limit=50')).filter((x) => !x.contentCaptured)

    // Nothing captured: a 409, and no audit row.
    const before = await catalog.api<{ id: string }[]>('/changes?kind=Receipt&limit=1')
    await expect(catalog.api(`/receipts/${plain.id}/reveal`, { method: 'POST' })).rejects.toMatchObject({ status: 409 })
    expect(await catalog.api<{ id: string }[]>('/changes?kind=Receipt&limit=1')).toEqual(before)
    window.history.pushState({}, '', `/traffic?receipt=${plain.id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    expect(document.body.textContent).toContain('Not captured for this request: only its hashes were stored.')
    expect(screen.queryByRole('button', { name: /Reveal content/ })).toBeNull()
    cleanup()

    window.history.pushState({}, '', `/traffic?receipt=${captured!.id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    expect(screen.queryByLabelText('Revealed content')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /Reveal content/ }))
    await waitFor(() => expect(document.body.textContent).toContain('Reveal recorded in the audit log as dev@localhost'))
    expect(screen.getByLabelText('Revealed content').textContent).toContain('[user]')
    const [row] = await catalog.api<{ action: string; target: string; actor: string }[]>('/changes?kind=Receipt&limit=1')
    expect(row).toMatchObject({ action: 'Revealed content', target: captured!.id, actor: 'dev@localhost' })
  })

  it('serves Spend from the aggregates, matching Overview, with its basis', async () => {
    type View = import('@/data/catalog').SpendView
    const [day, overview] = await Promise.all([catalog.api<View>('/spend?range=24h&by=team'), catalog.api<{ current: { spendUsd: number } }>('/summary?range=24h')])
    const sum = day.rows.reduce((a, r) => a + r.spendUsd, 0)
    // Both read receipts_5m over the same rolling window, a moment apart.
    expect(Math.abs(sum - overview.current.spendUsd)).toBeLessThan(Math.max(1, overview.current.spendUsd * 0.01))
    expect(day.trend.bucketMs).toBe(3_600_000)
    expect(day.trend.points).toHaveLength(24)
    expect(day.period.trailingDays).toBeGreaterThan(0)
    expect(day.period.projectedUsd).toBeCloseTo(day.period.monthToDateUsd + day.period.trailingDailyUsd * day.period.remainingDays, 0)

    // Every dimension splits the same total; provider comes from each receipt's backend.
    const week = await Promise.all((['team', 'project', 'key', 'model', 'provider'] as const).map((by) => catalog.api<View>(`/spend?range=7d&by=${by}`)))
    const totals = week.map((v) => v.rows.reduce((a, r) => a + r.spendUsd, 0))
    for (const t of totals) expect(Math.abs(t - totals[0])).toBeLessThan(Math.max(1, totals[0] * 0.01))
    const providers = new Set(catalog.backends.map((b) => b.provider))
    expect(week[4].rows.filter((r) => r.spendUsd > 0).every((r) => providers.has(r.id))).toBe(true)
    expect(week[0].trend.bucketMs).toBe(86_400_000)
    expect(week[0].trend.points).toHaveLength(7)

    const budgets = await catalog.api<import('@/data/catalog').Budget[]>('/budgets')
    for (const b of budgets) expect(b.projectedUsd).toBeCloseTo(b.currentUsd + b.trailingDailyUsd * day.period.remainingDays, 0)

    window.history.pushState({}, '', '/spend')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const text = document.body.textContent ?? ''
    expect(text).toContain('trailing 7-day average')
    // Savings and the close report are connected (their own tests below).
    expect(text).not.toContain("Savings analysis isn't connected yet")
    expect(text).not.toContain('Export PDF for close')
    expect(screen.getByRole('button', { name: 'Download close report' })).toHaveProperty('disabled', false)
    expect(text).not.toContain('isn’t connected yet: the control plane serves budgets read-only')
    expect(screen.getByRole('button', { name: 'Add budget' })).toHaveProperty('disabled', false)
    expect(text).not.toContain('spend is up')
    expect(text).not.toContain('requests/min')
    expect(text).not.toContain('Warns owners')
  })

  it('reports each key’s 24h spend and hourly requests from receipts_5m', async () => {
    type K = import('@/data/catalog').WireKey
    const [keys, byKey] = await Promise.all([catalog.api<K[]>('/keys'), catalog.api<import('@/data/catalog').SpendView>('/spend?range=24h&by=key')])
    const live = keys.filter((k) => k.status !== 'revoked')
    expect(live.some((k) => k.requests24h > 0)).toBe(true)
    for (const k of keys) {
      expect(k.hourly24h).toHaveLength(24)
      // The bins split the same rolling 24h the count covers.
      expect(k.hourly24h!.reduce((a, n) => a + n, 0)).toBe(k.requests24h)
      if (k.status === 'revoked') continue
      // Matches Spend's key breakdown over the same window, a moment apart.
      const row = byKey.rows.find((r) => r.id === k.name)?.spendUsd ?? 0
      expect(Math.abs(k.spend24hUsd! - row)).toBeLessThan(Math.max(0.05, row * 0.02))
    }

    window.history.pushState({}, '', '/keys')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    expect(document.body.textContent).not.toContain('Rotating · 61%')
  })

  it('shows a rotation from rotate_until and the audit log, and what is not recorded', async () => {
    type K = import('@/data/catalog').WireKey
    const { key } = await catalog.api<{ key: K }>('/keys', {
      method: 'POST',
      body: JSON.stringify({ name: `api-mode-test-${Date.now().toString(36)}`, team: catalog.teams[0].id, project: 'api-mode-test', allowedModels: [catalog.models[0].id], allowedRegions: ['us-east'], expiresAt: '2027-01-01' }),
    })
    try {
      const before = Date.now()
      await catalog.api(`/keys/${key.id}/rotate`, { method: 'POST', body: JSON.stringify({ overlapHours: 1 }) })
      const rotated = (await catalog.api<K[]>('/keys')).find((k) => k.id === key.id)!
      expect(rotated.status).toBe('rotating')
      expect(rotated.rotation?.startedBy).toBe(catalog.session.actor.email)
      expect(Math.abs(rotated.rotation!.startedAt! - before)).toBeLessThan(60_000)
      expect(Math.abs(rotated.rotation!.endsAt! - (before + 3_600_000))).toBeLessThan(60_000)
      expect(catalog.keys.find((k) => k.status === 'active')?.rotation).toBeUndefined()

      // The key was made after hydrate(); the page picks it up from /keys.
      window.history.pushState({}, '', `/keys?key=${key.id}`)
      render(<App />)
      await act(async () => {
        await new Promise((ok) => setTimeout(ok, 500))
      })
      let text = document.body.textContent ?? ''
      expect(text).toContain(rotated.name)
      expect(text).toMatch(/Rotating · (58|59)m left/)
      expect(text).toContain('No requests on either secret since the rotation started')
      expect(text).not.toContain('of traffic on the new secret')
      expect(text).not.toContain('web-assistant-7c9')
      expect(text).not.toContain('b81e')
      expect(text).not.toContain('rotation reminders')

      fireEvent.click(screen.getByRole('button', { name: /View rotation/ }))
      await act(async () => {})
      text = document.body.textContent ?? ''
      expect(text).toContain(`Started by ${catalog.session.actor.email}`)
      expect(text).not.toContain('priya@acme.dev')
      expect(screen.getByRole('button', { name: 'Extend overlap 24h' })).toHaveProperty('disabled', false)
      expect(screen.getByRole('button', { name: 'Retire old secret now' })).toHaveProperty('disabled', false)
      expect(text).not.toContain('aren’t connected yet')

      // Retiring asks first, with the old secret's real count; backing out changes nothing.
      fireEvent.click(screen.getByRole('button', { name: 'Retire old secret now' }))
      const d = await formDialog()
      expect(d.textContent).toContain('No requests have used the old secret since the rotation started')
      expect(d.textContent).not.toContain('39%')
      fireEvent.click(within(d).getByRole('button', { name: 'Keep both secrets' }))
      expect(within(d).getByRole('button', { name: 'Retire old secret now' })).toBeTruthy()
      expect((await catalog.api<K[]>('/keys')).find((k) => k.id === key.id)!.status).toBe('rotating')
    } finally {
      await catalog.api(`/keys/${key.id}/revoke`, { method: 'POST' })
    }
  })

  it('shows each Overview change row with the effect Activity computes', async () => {
    type V = import('@/data/catalog').ActivityView
    const v = await catalog.api<V>('/activity?range=7d')
    const seeded = new Set(catalog.changes.map((c) => c.effect).filter(Boolean))
    window.history.pushState({}, '', '/')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const text = document.body.textContent ?? ''
    const rows = catalog.changes.slice(1, 5)
    expect(rows.length).toBeGreaterThan(0)
    for (const c of rows) {
      const computed = v.changes.find((x) => x.id === c.id)
      if (computed) expect(text).toContain(computed.effect)
    }
    for (const s of seeded) expect(text).not.toContain(s)
  })

  it('joins each change to the aggregates around it, with traffic events, on Activity', async () => {
    type V = import('@/data/catalog').ActivityView
    const v = await catalog.api<V>('/activity?range=7d')
    expect(v.until - v.since).toBe(7 * 86_400_000)
    expect(v.changes.length).toBeGreaterThan(0)
    const seeded = new Set(catalog.changes.map((c) => c.effect).filter(Boolean))
    const sum = (xs: number[]) => xs.reduce((a, n) => a + n, 0)
    for (const c of v.changes) {
      const im = c.impact
      expect(c.ts).toBeGreaterThanOrEqual(v.since)
      expect(im.bins).toHaveLength((2 * im.windowMinutes) / 5)
      expect(im.split).toBe(im.windowMinutes / 5)
      // The sparkline splits the same windows the before/after numbers cover.
      expect(sum(im.bins.slice(0, im.split))).toBe(im.before.requests)
      expect(sum(im.bins.slice(im.split))).toBe(im.after.requests)
      expect(['good', 'bad', 'neutral']).toContain(c.effectTone)
      // The effect is computed, never the audit row's stored text.
      expect(seeded.has(c.effect)).toBe(false)
      if (!im.comparable) expect(c.effect).toContain('Too little traffic')
    }
    // The before window is a plain receipts_5m count, the same one Traffic uses.
    const c = v.changes.find((x) => x.impact.before.requests > 0)!
    const n = await catalog.api<{ count: number | null }>(`/receipts/count?since=${c.impact.pivot - c.impact.windowMinutes * 60_000}&before=${c.impact.pivot}`)
    expect(n.count).toBe(c.impact.before.requests)

    const kinds = new Set(['backend_failing', 'backend_recovered', 'budget_80', 'budget_cap'])
    for (const e of v.events) {
      expect(kinds.has(e.kind)).toBe(true)
      expect(e.ts).toBeGreaterThanOrEqual(v.since)
      expect(e.ts).toBeLessThanOrEqual(v.until)
    }

    window.history.pushState({}, '', '/activity?range=7d')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const text = document.body.textContent ?? ''
    expect(text).toContain(v.changes[0].target)
    expect(text).toContain('across the tenant, from the 5-minute aggregates')
    for (const e of v.events) expect(text).toContain(e.title)
    // The mockup's invented readouts and traffic events are gone.
    for (const s of ['p50 latency, route default', 'traffic on new secret', 'anthropic-prod failover began', 'throttle policy engaged', ...seeded]) expect(text).not.toContain(s)
  }, 20_000) // renders the whole Activity page; past 5s when the suite runs alongside traffic

  it('reports the real retention policy and capture routes on Settings', async () => {
    type R = import('@/data/catalog').RetentionView
    const r = await catalog.api<R>('/retention')
    // §4.6: raw receipts are dropped after 30 days and compressed after 7;
    // the daily aggregates have no drop policy.
    expect(r.hotDays).toBe(30)
    expect(r.compressAfterDays).toBe(7)
    expect(r.aggregates.map((c) => c.name)).toContain('receipts_daily')
    for (const c of r.aggregates) expect(c.dropAfterDays).toBeNull()
    expect(r.oldestReceiptAt).toBeLessThan(Date.now())

    const capturing = catalog.liveRoutes.filter((x) => x.captureContent).map((x) => x.name)
    window.history.pushState({}, '', '/settings')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const text = document.body.textContent ?? ''
    expect(text).toContain('30 days')
    expect(text).toContain('Never dropped')
    for (const name of capturing) expect(text).toContain(name)
    expect(text).toContain(capturing.length ? `On for ${capturing.length} route` : 'Off on every route.')
    // Warden's snapshot comes from /session, not the mockup's pods.
    expect(text).toMatch(/Warden config snapshot.*cache age \d/)
    // What has no backend yet says so instead of showing the mockup.
    expect(text).toContain('Not connected yet')
    for (const s of ['Snapshot v1842', 'on 2 of 6 pods', 'sk-proj-…Q7f', 'priya@acme.dev', 'otel-collector.nebari-gateway', 'platform-gitops', '7 years', '1,412', '4,806']) expect(text).not.toContain(s)
  })

  it('shows real aliases, their 24h traffic and per-backend prices on Models', async () => {
    type A = import('@/data/catalog').AliasView
    type P = import('@/data/catalog').PricingView
    const [aliases, pricing] = await Promise.all([catalog.api<A[]>('/aliases'), catalog.api<P>('/pricing')])
    // Seeded: one alias, summarize-* → gpt-5-mini, which the traffic generator uses.
    const summarize = aliases.find((a) => a.alias === 'summarize-*')
    expect(summarize?.target).toBe('gpt-5-mini')
    expect(summarize!.requests24h).toBeGreaterThan(0)
    for (const a of aliases) expect(catalog.modelById[a.target]).toBeDefined()
    // Prices are per (model, backend): every pair a backend serves is listed, priced or not.
    for (const b of catalog.backends) for (const m of b.models) expect(pricing.prices.some((p) => p.model === m && p.backend === b.name)).toBe(true)
    for (const p of pricing.prices.filter((p) => p.priced)) expect(p.effectiveFrom).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(Array.isArray(pricing.changes)).toBe(true)

    const page = async (tab: string, check?: () => void) => {
      window.history.pushState({}, '', `/models?tab=${tab}`)
      const r = render(<App />)
      await act(async () => {
        await new Promise((ok) => setTimeout(ok, 300))
      })
      const text = document.body.textContent ?? ''
      check?.()
      r.unmount()
      return text
    }

    const aliasText = await page('aliases', () => {
      expect(screen.getByRole('button', { name: /New alias/ })).toHaveProperty('disabled', false)
    })
    expect(aliasText).toContain('summarize-*')
    expect(aliasText).toContain(summarize!.requests24h.toLocaleString('en-US'))
    expect(aliasText).toContain('Not recorded')
    expect(aliasText).not.toContain('NaN')
    for (const s of ['31,204', '4,120', 'input tokens < 64k', 'key.team in', 'priya@acme.dev', 'platform-gitops']) expect(aliasText).not.toContain(s)

    const priceText = await page('pricing', () => {
      expect(screen.getByRole('button', { name: /Export CSV/ })).toHaveProperty('disabled', pricing.changes.length === 0)
      expect(screen.getByRole('button', { name: /Sync now/ })).toHaveProperty('disabled', false)
    })
    for (const p of pricing.prices) expect(priceText).toContain(p.backend)
    const unpriced = pricing.prices.filter((p) => !p.priced)
    if (unpriced.length) expect(priceText).toContain('No price')
    if (pricing.prices.some((p) => Object.values(p.rates).some((r) => r?.source === 'litellm'))) expect(priceText).toContain('LiteLLM')
    expect(priceText).toContain('LiteLLM sync')
    if (pricing.sync.lastOkAt) expect(priceText).toMatch(/Last synced/)
    if (pricing.changes.length === 0) expect(priceText).toContain('No price changes yet')
    for (const s of ['Pricing sync not connected yet', 'Seed price', 'catalog sync', 'list price', '2026-08-14', '2026-06-02', 'NaN']) expect(priceText).not.toContain(s)

    const catalogText = await page('catalog')
    for (const m of catalog.models) expect(catalogText).toContain(m.id)
    // Modalities and retirement dates from LiteLLM, by backend; unknown without an entry.
    const live = await catalog.api<import('@/data/catalog').Model[]>('/models')
    for (const m of live) for (const d of m.deprecations ?? []) expect(catalogText).toContain(`${d.date} on ${d.backend}`)
    expect(live.find((m) => m.id === 'smollm2')?.modalities).toBeUndefined()
    expect(live.find((m) => m.id === 'gpt-5-mini')?.modalities).toContain('text')
    expect(catalogText).toContain('Unknown')
    expect(catalogText).not.toContain('Not connected yet')
    expect(catalogText).not.toContain('Deprecated 2026-12-31')
  })

  // Backend writes the console doesn't call yet: each one validates, writes its
  // audit row, refuses an update or delete without If-Match (428), and a
  // stale one with 409. NEW creates an alias: If-None-Match: *.
  const NEW = 'new'
  const status = (p: Promise<unknown>) => p.then(() => 200, (e: { status?: number }) => e.status ?? 0)
  const send = <T,>(method: string, path: string, body?: unknown, ifMatch?: string) =>
    catalog.api<T>(path, {
      method,
      body: body === undefined ? undefined : JSON.stringify(body),
      headers: ifMatch === NEW ? { 'If-None-Match': '*' } : ifMatch ? { 'If-Match': ifMatch } : {},
    })

  it('writes aliases with audit rows, validation and If-Match', async () => {
    type A = import('@/data/catalog').AliasView & { etag: string }
    type C = import('@/data/catalog').Change
    const name = `api-mode-test-${Date.now().toString(36)}`
    const path = `/aliases/${encodeURIComponent(name)}`
    let etag = ''
    try {
      expect(await status(send('PUT', path, { target: 'gpt-5-mini' }))).toBe(428)
      const made = await send<A>('PUT', path, { target: 'gpt-5-mini' }, NEW)
      etag = made.etag
      expect(made).toMatchObject({ alias: name, target: 'gpt-5-mini' })
      expect((await catalog.api<A[]>('/aliases')).find((a) => a.alias === name)?.etag).toBe(made.etag)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({
        action: 'Created alias', target: `${name} → gpt-5-mini`, targetKind: 'Alias', actor: catalog.session.actor.email,
      })

      expect(await status(send('PUT', path, { target: 'gpt-5.5' }, NEW))).toBe(409) // it exists now
      const moved = await send<A>('PUT', path, { target: 'claude-haiku-4-5' }, made.etag)
      etag = moved.etag
      expect(moved.etag).not.toBe(made.etag)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Changed alias target', target: `${name} gpt-5-mini → claude-haiku-4-5` })
      // Written against the version before that one: stale.
      expect(await status(send('PUT', path, { target: 'gpt-5.5' }, made.etag))).toBe(409)
      expect(await status(send('PUT', path, { target: 'no-such-model' }, etag))).toBe(400)
      // A pattern that would capture catalog models is refused.
      expect(await status(send('PUT', `/aliases/${encodeURIComponent('gpt-*')}`, { target: 'gpt-5-mini' }, NEW))).toBe(400)
      expect(await status(send('DELETE', path))).toBe(428)
    } finally {
      await send('DELETE', path, undefined, etag).catch(() => {})
    }
    expect((await catalog.api<A[]>('/aliases')).some((a) => a.alias === name)).toBe(false)
    expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Deleted alias', target: name })
    expect(await status(send('DELETE', path, undefined, etag))).toBe(404)
  })

  const gateway = (import.meta.env.VITE_STARGATE_GATEWAY as string | undefined) ?? 'http://localhost:1975'
  /**
   * Spends past a cent with long gpt-5.5 prompts until Warden (reloading every
   * 5s from the aggregates) refuses one with `want`. Returns the code and the
   * refusal's Retry-After header.
   */
  const spendUntil = async (secret: string, want: 'budget_exceeded' | 'budget_throttled') => {
    const prompt = 'Summarize the following incident log. '.repeat(500)
    // The fake upstream also answers some requests 429; only Warden's carry a
    // budget code. A throttle admits ten a minute past the cap, so allow for those.
    for (let i = 0; i < 60; i++) {
      const res = await fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5.5', max_tokens: 400, messages: [{ role: 'user', content: prompt }] }),
      })
      const code = res.status === 429 ? ((await res.json().catch(() => null)) as { error?: { code?: string } } | null)?.error?.code : undefined
      if (code === want) return { code, retryAfter: res.headers.get('retry-after') }
      await new Promise((ok) => setTimeout(ok, 1000))
    }
    return { code: '', retryAfter: null }
  }
  const spendUntilRefused = async (secret: string) => (await spendUntil(secret, 'budget_exceeded')).code
  const testKey = (name: string) =>
    send<{ key: { id: string }; secret: string }>('POST', '/keys', {
      name, team: 'support', project: 'api-mode-test', allowedModels: ['gpt-5.5'], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
    })

  it('creates, retargets and deletes an alias from Models, with audit rows', async () => {
    type A = import('@/data/catalog').AliasView & { etag: string }
    type C = import('@/data/catalog').Change
    const name = `ui-alias-${Date.now().toString(36)}`
    window.history.pushState({}, '', '/models?tab=aliases')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    try {
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: /New alias/ }))
      })
      let d = await formDialog()
      fireEvent.change(within(d).getByLabelText('Alias'), { target: { value: name } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('combobox', { name: 'Target model' }))
      })
      await act(async () => {
        choose(await screen.findByRole('option', { name: /^smollm2/ }))
      })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Create alias' }))
      })
      await formDialogClosed()
      await waitFor(() => expect(screen.getByText(name)).toBeTruthy(), { timeout: 5000 })
      expect((await catalog.api<A[]>('/aliases')).find((a) => a.alias === name)?.target).toBe('smollm2')
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created alias', target: `${name} → smollm2` })

      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: `Edit ${name}` }))
      })
      d = await formDialog()
      await act(async () => {
        fireEvent.click(within(d).getByRole('combobox', { name: 'Target model' }))
      })
      await act(async () => {
        choose(await screen.findByRole('option', { name: /^gpt-4o-mini/ }))
      })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Save' }))
      })
      await formDialogClosed()
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Changed alias target', target: `${name} smollm2 → gpt-4o-mini` })

      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: `Delete ${name}` }))
      })
      d = await formDialog()
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Delete alias' }))
      })
      await formDialogClosed()
      await waitFor(() => expect(screen.queryByText(name)).toBeNull(), { timeout: 5000 })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Deleted alias', target: name })
    } finally {
      const left = (await catalog.api<A[]>('/aliases')).find((a) => a.alias === name)
      if (left) await catalog.api(`/aliases/${encodeURIComponent(name)}`, { method: 'DELETE', headers: { 'If-Match': left.etag } })
      r.unmount()
    }
  }, 30_000)

  it('writes budgets with dry runs, validation, audit rows and If-Match', async () => {
    type B = import('@/data/catalog').Budget & { etag: string }
    type C = import('@/data/catalog').Change
    const name = `api-mode-budget-${Date.now().toString(36)}`
    const { key } = await testKey(name)
    let id = ''
    let etag = ''
    try {
      // A key budget names the key by id; the API gives its name alongside, for display.
      const draft = { scopeType: 'key', scope: key.id, capUsd: 0.01, onExceed: 'block' }
      const dry = await send<{ dryRun: boolean; budget: B; covers: string[]; overCap: boolean }>('POST', '/budgets?dryRun=true', draft)
      expect(dry).toMatchObject({ dryRun: true, covers: [name], overCap: false, budget: { scope: key.id, scopeName: name, currentUsd: 0 } })
      expect((await catalog.api<B[]>('/budgets')).some((b) => b.scope === key.id)).toBe(false)

      const made = await send<B>('POST', '/budgets', draft)
      etag = made.etag
      id = made.id
      expect(made).toMatchObject({ scopeType: 'key', scope: key.id, scopeName: name, capUsd: 0.01, onExceed: 'block', period: 'monthly' })
      expect((await catalog.api<B[]>('/budgets')).find((b) => b.id === id)).toMatchObject({ scope: key.id, scopeName: name })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created budget', target: `key ${name} · $0.01 monthly, block`, targetKind: 'Budget' })
      expect(await status(send('POST', '/budgets', draft))).toBe(409) // one budget per scope
      for (const bad of [{ ...draft, period: 'weekly' }, { ...draft, capUsd: 0 }, { ...draft, scopeType: 'team', scope: 'nobody' }, { ...draft, onExceed: 'explode' }])
        expect(await status(send('POST', '/budgets', { ...bad, scope: bad.scope + (bad.scopeType === 'key' ? '-x' : '') }))).toBe(400)
      // The key's name isn't a scope any more (it could be renamed or reused).
      expect(await status(send('POST', '/budgets?dryRun=true', { ...draft, scope: name }))).toBe(400)

      expect(await status(send('PATCH', `/budgets/${id}`, { capUsd: 1000 }))).toBe(428)
      const raised = await send<B>('PATCH', `/budgets/${id}`, { capUsd: 1000 }, made.etag)
      etag = raised.etag
      expect(raised.capUsd).toBe(1000)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Raised budget cap', target: `${name} $0.01 → $1,000` })
      expect(await status(send('PATCH', `/budgets/${id}`, { capUsd: 5 }, made.etag))).toBe(409)
      expect(await status(send('PATCH', `/budgets/${id}`, { onExceed: 'explode' }, etag))).toBe(400)
      expect(await status(send('DELETE', `/budgets/${id}`))).toBe(428)
    } finally {
      if (id) await send('DELETE', `/budgets/${id}`, undefined, etag).catch(() => {})
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
    expect((await catalog.api<B[]>('/budgets')).some((b) => b.id === id)).toBe(false)
    expect((await catalog.api<C[]>('/changes')).find((c) => c.targetKind === 'Budget')).toMatchObject({ action: 'Deleted budget', target: `key ${name} · $1,000 monthly` })
  })

  // §11's exit test: a cap set in the UI stops real requests at the gateway,
  // and the blocked ones carry the reason. Then the same budget is edited
  // over a change made meanwhile (§6: a 409 is a merge, not an overwrite) and
  // deleted, all from Spend.
  it('sets a budget cap on Spend that stops spend at the gateway, merges a stale edit and deletes it', async () => {
    type B = import('@/data/catalog').Budget & { etag: string }
    type C = import('@/data/catalog').Change
    type R = { verdict: string; errorCode?: string; trace: { step: string; input: string }[] }
    const name = `api-mode-ui-budget-${Date.now().toString(36)}`
    const { key, secret } = await testKey(name)
    // The form picks the key by name and sends its id.
    const mine = async () => (await catalog.api<B[]>('/budgets')).find((b) => b.scopeType === 'key' && b.scope === key.id)
    try {
      window.history.pushState({}, '', '/spend')
      render(<App />)
      await act(async () => {})
      const add = await screen.findByRole('button', { name: 'Add budget' })
      await waitFor(() => expect(add).toHaveProperty('disabled', false))
      fireEvent.click(add)
      let dialog = await formDialog()
      fireEvent.click(within(dialog).getByRole('radio', { name: /^Key/ }))
      const picker = within(dialog).getByRole('combobox', { name: 'Key' })
      await waitFor(() => expect(picker).toHaveProperty('disabled', false)) // keys load when the form opens
      fireEvent.click(picker)
      choose(await screen.findByRole('option', { name }))
      fireEvent.change(within(dialog).getByLabelText('Monthly cap (USD)'), { target: { value: '0.01' } })
      fireEvent.click(within(dialog).getByRole('radio', { name: /^Block/ }))
      // The dry run says what saving would do before anything is written.
      await waitFor(() => expect(dialog.textContent).toContain(`Covers 1 active key: ${name}`), { timeout: 5000 })
      expect(await mine()).toBeUndefined()
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create budget' }))
      await formDialogClosed()
      const made = await mine()
      expect(made).toMatchObject({ scopeType: 'key', scope: key.id, scopeName: name, capUsd: 0.01, onExceed: 'block', period: 'monthly' })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created budget', target: `key ${name} · $0.01 monthly, block` })
      const table = screen.getByRole('table', { name: 'Budgets' })
      await waitFor(() => expect(table.textContent).toContain(name), { timeout: 5000 })
      expect(table.textContent).toContain('Blocks new requests at $0.01')

      expect(await spendUntilRefused(secret)).toBe('budget_exceeded')
      let blocked: R | undefined
      for (let i = 0; i < 20 && !blocked; i++) {
        blocked = (await catalog.api<R[]>(`/receipts?limit=5&key=${key.id}&verdict=blocked`))[0]
        if (!blocked) await new Promise((ok) => setTimeout(ok, 500))
      }
      expect(blocked?.errorCode).toBe('budget_exceeded')
      expect(blocked!.trace.find((s) => s.step === 'Budget checked')?.input).toMatch(new RegExp(`^key budget ${name} · \\$0\\.\\d\\d of \\$0\\.01$`))

      // Edit: someone else raises the cap to $5 while the form is open.
      fireEvent.click(within(table).getByRole('button', { name: `Edit budget ${name}` }))
      dialog = await formDialog()
      fireEvent.change(within(dialog).getByLabelText('Monthly cap (USD)'), { target: { value: '1000' } })
      await send('PATCH', `/budgets/${made!.id}`, { capUsd: 5 }, made!.etag)
      fireEvent.click(within(dialog).getByRole('button', { name: 'Save changes' }))
      await waitFor(() => expect(dialog.textContent).toContain('changed since you opened it'), { timeout: 5000 })
      expect(dialog.textContent).toMatch(/Cap\s*\$5\s*\$1,000/)
      fireEvent.click(within(dialog).getByRole('button', { name: 'Save mine over theirs' }))
      await formDialogClosed()
      expect((await mine())?.capUsd).toBe(1000)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Raised budget cap', target: `${name} $5 → $1,000` })

      await waitFor(() => expect(table.textContent).toContain('$1,000'), { timeout: 5000 })
      fireEvent.click(within(table).getByRole('button', { name: `Delete budget ${name}` }))
      dialog = await formDialog()
      expect(dialog.textContent).toContain(name)
      fireEvent.click(within(dialog).getByRole('button', { name: 'Delete budget' }))
      await formDialogClosed()
      expect(await mine()).toBeUndefined()
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Deleted budget', target: `key ${name} · $1,000 monthly` })
      await waitFor(() => expect(table.textContent).not.toContain(name), { timeout: 5000 })
    } finally {
      const b = await mine()
      if (b) await send('DELETE', `/budgets/${b.id}`, undefined, b.etag).catch(() => {})
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 120_000)

  // Throttle (spec §11 Phase 4, decided 2026-10-05): while a throttle budget
  // covering a key is over its cap, the key gets 10 requests a minute. The
  // next gets 429 budget_throttled with Retry-After until its next slot, and
  // its receipt says "throttled", not "blocked". A cent cap is soon over with
  // long gpt-5.5 requests; the ten admitted after that take ten seconds here.
  it('throttles a key to 10 requests a minute over a throttle cap, with Retry-After, and says so on Spend, Keys and Traffic', async () => {
    type B = import('@/data/catalog').Budget & { etag: string }
    type R = { id: string; verdict: string; status: number; errorCode?: string; trace: { step: string; input: string; outcome: string; state: string }[] }
    const name = `api-mode-throttle-${Date.now().toString(36)}`
    const { key, secret } = await testKey(name)
    let b: B | undefined
    try {
      b = await send<B>('POST', '/budgets', { scopeType: 'key', scope: key.id, capUsd: 0.01, onExceed: 'throttle' })
      expect(b).toMatchObject({ onExceed: 'throttle', throttlePerMinute: 10 })
      const first = await spendUntil(secret, 'budget_throttled')
      expect(first.code).toBe('budget_throttled')
      // Retry-After is when the key's next slot opens: within the minute.
      expect(Number(first.retryAfter)).toBeGreaterThanOrEqual(1)
      expect(Number(first.retryAfter)).toBeLessThanOrEqual(60)

      let refused: R | undefined
      for (let i = 0; i < 20 && !refused; i++) {
        refused = (await catalog.api<R[]>(`/receipts?limit=5&key=${key.id}&verdict=throttled`)).find((r) => r.errorCode === 'budget_throttled')
        if (!refused) await new Promise((ok) => setTimeout(ok, 500))
      }
      expect(refused).toMatchObject({ verdict: 'throttled', status: 429, errorCode: 'budget_throttled' })
      const step = refused!.trace.find((s) => s.step === 'Budget checked')!
      expect(step.input).toMatch(new RegExp(`^key budget ${name} · \\$\\d+\\.\\d\\d of \\$0\\.01$`))
      expect(step.outcome).toMatch(/^over cap · throttled to 10 a minute per key · refused, next slot in \d+s$/)
      expect(step.state).toBe('throttle')
      // Requests within the rate went through, and their trace says so.
      const admitted = (await catalog.api<R[]>(`/receipts?limit=40&key=${key.id}&verdict=allowed`)).filter((r) =>
        /^over cap · throttled to 10 a minute per key · admitted, \d+ of 10$/.test(r.trace.find((s) => s.step === 'Budget checked')?.outcome ?? ''),
      )
      expect(admitted.length).toBeGreaterThan(0)
      // A throttle isn't a block: Traffic's blocked filter doesn't list it, and the series counts it apart.
      expect((await catalog.api<R[]>(`/receipts?limit=40&key=${key.id}&verdict=blocked`)).some((r) => r.errorCode === 'budget_throttled')).toBe(false)
      const series = await catalog.api<{ throttled: number }[]>('/series/traffic?range=1h')
      expect(series.reduce((n, p) => n + p.throttled, 0)).toBeGreaterThan(0)

      window.history.pushState({}, '', '/spend')
      render(<App />)
      const table = await screen.findByRole('table', { name: 'Budgets' })
      await waitFor(() => expect(table.textContent).toContain(name), { timeout: 5000 })
      const row = within(table).getByRole('link', { name }).closest('tr')!
      await waitFor(() => expect(row.textContent).toContain('Throttling: each key gets 10 requests a minute; more get 429 budget_throttled with Retry-After.'), { timeout: 5000 })
      expect(document.body.textContent).not.toContain('of new requests get 429')

      // Spend re-read the budgets, so the key's page has this one over its cap.
      cleanup()
      window.history.pushState({}, '', `/keys?key=${key.id}`)
      render(<App />)
      await waitFor(() => expect(document.body.textContent).toContain('Over cap · throttled to 10 requests a minute (429, retry later)'), { timeout: 5000 })

      // A receipt says what refused it, with its own verdict.
      cleanup()
      window.history.pushState({}, '', `/traffic?receipt=${refused!.id}`)
      render(<App />)
      await waitFor(() => expect(document.body.textContent).toContain('A budget over its cap throttled this request'), { timeout: 5000 })
      expect(document.body.textContent).toContain('Throttled before the upstream call: nothing was billed.')
      expect(screen.getAllByText('Throttled').length).toBeGreaterThan(0)
    } finally {
      if (b) await send('DELETE', `/budgets/${b.id}`, undefined, b.etag).catch(() => {})
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 150_000)

  // Projects are a table (§5.2, decided 2026-10-05): a project budget can be
  // set up before any key is in it, and names it by id.
  it('creates projects with audit rows and caps one before it has keys', async () => {
    type B = import('@/data/catalog').Budget & { etag: string }
    type C = import('@/data/catalog').Change
    type P = import('@/data/catalog').Project
    type K = { id: string; name: string; team: string; project: string; projectId: string }
    const name = `amt-proj-${Date.now().toString(36)}`
    let b: B | undefined
    const keys: string[] = []
    try {
      const p = await send<P>('POST', '/projects', { team: 'support', name })
      expect(p).toMatchObject({ team: 'support', name })
      expect((await catalog.api<P[]>('/projects')).find((x) => x.id === p.id)).toEqual(p)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created project', target: `support / ${name}`, targetKind: 'Project' })
      expect(await status(send('POST', '/projects', { team: 'support', name }))).toBe(409)
      // Names are for people now (decided 2026-10-05): spaces and capitals are fine; the same name in another case isn't.
      expect(await status(send('POST', '/projects', { team: 'support', name: name.toUpperCase() }))).toBe(409)
      for (const bad of [{ team: 'support', name: '   ' }, { team: 'support', name: 'x'.repeat(81) }, { team: 'support', name: 'tab\there' }, { team: 'nobody', name: name + '-x' }, { name: name + '-y' }])
        expect(await status(send('POST', '/projects', bad))).toBe(400)

      // No keys yet: the budget covers none, and shows the project's name.
      const draft = { scopeType: 'project', scope: p.id, capUsd: 5, onExceed: 'warn' }
      expect(await send('POST', '/budgets?dryRun=true', draft)).toMatchObject({ covers: [], budget: { scope: p.id, scopeName: name, currentUsd: 0 } })
      b = await send<B>('POST', '/budgets', draft)
      expect(b).toMatchObject({ scope: p.id, scopeName: name })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created budget', target: `project ${name} · $5 monthly, warn` })
      expect(await status(send('POST', '/budgets?dryRun=true', { ...draft, scope: name }))).toBe(400) // by id, not name

      // A key in the project joins it, by name or id. A key no longer creates a
      // project: an unknown name is refused with what to do.
      const inIt = await send<{ key: K }>('POST', '/keys', { name: `${name}-k`, team: 'support', project: name, allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01' })
      keys.push(inIt.key.id)
      expect(inIt.key).toMatchObject({ team: 'support', project: name, projectId: p.id })
      const refused = await send('POST', '/keys', { name: `${name}-w`, team: 'web', project: name, allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01' }).catch((e: Error & { status: number }) => e)
      expect(refused).toMatchObject({ status: 400, message: `Team web has no project named "${name}". Create the project first, then the key.` })
      expect(await status(send('POST', '/keys', { name: `${name}-x`, team: 'web', projectId: p.id, allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01' }))).toBe(400) // another team's
      // The same name on another team is another project.
      const web = await send<P>('POST', '/projects', { team: 'web', name })
      const elsewhere = await send<{ key: K }>('POST', '/keys', { name: `${name}-w`, team: 'web', projectId: web.id, allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01' })
      keys.push(elsewhere.key.id)
      expect(elsewhere.key).toMatchObject({ project: name, projectId: web.id })
      expect(web.id).not.toBe(p.id)
      expect((await catalog.api<C[]>('/changes')).slice(0, 2)).toMatchObject([
        { action: 'Created key', target: `${name}-w` },
        { action: 'Created project', target: `web / ${name}` },
      ])
      expect(await send('PATCH', `/budgets/${b.id}?dryRun=true`, { capUsd: 6 })).toMatchObject({ covers: [`${name}-k`] })
      expect(await status(send('POST', '/keys', { name: `${name}-bad`, team: 'support', project: 'No Such Project', allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01' }))).toBe(400)
    } finally {
      if (b) await send('DELETE', `/budgets/${b.id}`, undefined, b.etag).catch(() => {})
      for (const id of keys) await send('POST', `/keys/${id}/revoke`).catch(() => {})
    }
  })

  // Projects can be renamed (If-Match, audited) and deleted once nothing uses
  // them (decided 2026-10-05). Keys, budgets, rules and receipts name a
  // project by id, so a rename carries them along.
  it('renames a project with If-Match and an audit row, and deletes it only without active keys or a budget', async () => {
    type B = import('@/data/catalog').Budget & { etag: string }
    type C = import('@/data/catalog').Change
    type P = import('@/data/catalog').Project & { etag: string }
    type K = { id: string; name: string; project: string; projectId: string }
    const stamp = Date.now().toString(36)
    const before = `Amt Rename ${stamp}`
    const after = `Help desk · ${stamp}`
    const keys: string[] = []
    let b: B | undefined
    try {
      const p = await send<P>('POST', '/projects', { team: 'support', name: `  ${before}  ` })
      expect(p).toMatchObject({ team: 'support', name: before }) // trimmed
      expect(p.etag).toBeTruthy()
      const { key } = await send<{ key: K }>('POST', '/keys', { name: `amt-rename-${stamp}`, team: 'support', projectId: p.id, allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01' })
      keys.push(key.id)
      expect(key).toMatchObject({ project: before, projectId: p.id })

      expect(await status(send('PUT', `/projects/${p.id}`, { name: after }))).toBe(428)
      const renamed = await send<P>('PUT', `/projects/${p.id}`, { name: after }, p.etag)
      expect(renamed).toMatchObject({ id: p.id, team: 'support', name: after })
      expect(renamed.etag).not.toBe(p.etag)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Renamed project', target: `support / ${before} → ${after}`, targetKind: 'Project' })
      expect(await status(send('PUT', `/projects/${p.id}`, { name: 'stale' }, p.etag))).toBe(409)
      expect(await status(send('PUT', `/projects/${p.id}`, { name: ' ' }, renamed.etag))).toBe(400)
      // The key follows the rename; it names the project by id.
      expect((await catalog.api<K[]>('/keys')).find((k) => k.id === key.id)).toMatchObject({ project: after, projectId: p.id })

      // Another of the team's projects can't take the name, in any case.
      const other = await send<P>('POST', '/projects', { team: 'support', name: `Amt Other ${stamp}` })
      expect(await status(send('PUT', `/projects/${other.id}`, { name: after.toUpperCase() }, other.etag))).toBe(409)

      // Delete is refused, saying why, while a key is active or a budget names it.
      const keyed = await send('DELETE', `/projects/${p.id}`, undefined, renamed.etag).catch((e: Error & { status: number }) => e)
      expect(keyed).toMatchObject({ status: 409, message: `Project ${after} still has 1 active key (amt-rename-${stamp}). Revoke the key first.` })
      b = await send<B>('POST', '/budgets', { scopeType: 'project', scope: other.id, capUsd: 5, onExceed: 'warn' })
      const budgeted = await send('DELETE', `/projects/${other.id}`, undefined, other.etag).catch((e: Error & { status: number }) => e)
      expect(budgeted).toMatchObject({ status: 409, message: `Project Amt Other ${stamp} still has a budget. Delete the budget on Spend first.` })

      // Revoked keys don't hold it: their history keeps the project's name.
      await send('POST', `/keys/${key.id}/revoke`)
      expect(await status(send('DELETE', `/projects/${p.id}`))).toBe(428)
      await send('DELETE', `/projects/${p.id}`, undefined, renamed.etag)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Deleted project', target: `support / ${after}`, targetKind: 'Project' })
      expect((await catalog.api<P[]>('/projects')).some((x) => x.id === p.id)).toBe(false)
      // A deleted project takes no new keys, and its name is free again.
      expect(await status(send('POST', '/keys', { name: `amt-rename-${stamp}-2`, team: 'support', projectId: p.id, allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01' }))).toBe(400)
      const again = await send<P>('POST', '/projects', { team: 'support', name: after })
      expect(again.id).not.toBe(p.id)
    } finally {
      if (b) await send('DELETE', `/budgets/${b.id}`, undefined, b.etag).catch(() => {})
      for (const id of keys) await send('POST', `/keys/${id}/revoke`).catch(() => {})
    }
  })

  // §5.1: receipts carry the project's id, so Spend and Traffic group, and
  // rules match, two teams' same-named projects apart.
  it('keeps two teams’ same-named projects apart in receipts, Spend, Traffic and rules', async () => {
    type P = import('@/data/catalog').Project
    type K = { id: string; name: string; projectId: string }
    type R = { id: string; keyId: string; project: string; projectId?: string }
    type Row = { id: string; label: string; sub?: string; requests: number }
    type V = { id: string; etag: string }
    const stamp = Date.now().toString(36)
    const name = `Shared ${stamp}`
    const call = (secret: string) =>
      fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5-mini', max_tokens: 20, messages: [{ role: 'user', content: 'hi' }] }),
      })
    const keys: string[] = []
    let ruleId = ''
    try {
      const support = await send<P>('POST', '/projects', { team: 'support', name })
      const web = await send<P>('POST', '/projects', { team: 'web', name })
      const made = await Promise.all(
        [support, web].map((p) =>
          send<{ key: K; secret: string }>('POST', '/keys', { name: `amt-shared-${p.team}-${stamp}`, team: p.team, projectId: p.id, allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01' }),
        ),
      )
      keys.push(...made.map((m) => m.key.id))
      for (const m of made) expect((await call(m.secret)).status).toBe(200)

      // Each receipt has its own project's id; Traffic's project filter takes the id.
      let mine: R[] = []
      for (let i = 0; i < 30 && mine.length < 2; i++) {
        mine = (await catalog.api<R[]>('/receipts?limit=50&range=15m')).filter((r) => keys.includes(r.keyId))
        if (mine.length < 2) await new Promise((ok) => setTimeout(ok, 500))
      }
      expect(mine.find((r) => r.keyId === made[0].key.id)).toMatchObject({ project: name, projectId: support.id })
      expect(mine.find((r) => r.keyId === made[1].key.id)).toMatchObject({ project: name, projectId: web.id })
      expect((await catalog.api<R[]>(`/receipts?limit=50&range=15m&project=${support.id}`)).map((r) => r.keyId)).toEqual([made[0].key.id])

      // Spend has a row per project, by id, labelled with the name and its team.
      let rows: Row[] = []
      for (let i = 0; i < 30 && rows.length < 2; i++) {
        rows = (await catalog.api<{ rows: Row[] }>('/spend?range=1h&by=project')).rows.filter((r) => r.id === support.id || r.id === web.id)
        if (rows.length < 2) await new Promise((ok) => setTimeout(ok, 500))
      }
      expect(rows.find((r) => r.id === support.id)).toMatchObject({ label: name, sub: catalog.teams.find((t) => t.id === 'support')?.name, requests: 1 })
      expect(rows.find((r) => r.id === web.id)).toMatchObject({ label: name, sub: catalog.teams.find((t) => t.id === 'web')?.name, requests: 1 })

      // And on the page: two rows of the same name; each drills to Traffic by id.
      window.history.pushState({}, '', '/spend')
      render(<App />)
      fireEvent.click(await screen.findByRole('tab', { name: 'Breakdown' }))
      fireEvent.click(screen.getByRole('combobox', { name: 'Group by' }))
      choose(await screen.findByRole('option', { name: 'Project' }))
      const table = await screen.findByRole('table', { name: 'Spend by project' })
      await waitFor(() => expect(within(table).getAllByRole('button', { name: `Open receipts for ${name}` })).toHaveLength(2), { timeout: 5000 })
      fireEvent.click(within(table).getAllByRole('button', { name: `Open receipts for ${name}` })[0])
      await waitFor(() => expect(new URLSearchParams(window.location.search).get('project')).toMatch(new RegExp(`^(${support.id}|${web.id})$`)))
      cleanup()

      // A "project is" rule names the project by id: the web project of the same name isn't in it.
      const rule = { name: `amt-shared-${stamp}`, description: 'api-mode test', failMode: 'closed', when: [{ field: 'project', op: 'is', value: [support.id] }], then: [{ action: 'block' }] }
      expect(await status(send('POST', '/rules', { ...rule, name: `${rule.name}-by-name`, when: [{ field: 'project', op: 'is', value: [name] }] }))).toBe(400)
      const r0 = await send<V>('POST', '/rules', rule)
      ruleId = r0.id
      await send<V>('POST', `/rules/${ruleId}/publish`, { mode: 'enforce' }, r0.etag)
      expect((await call(made[0].secret)).status).toBe(403)
      expect((await call(made[1].secret)).status).toBe(200)
    } finally {
      if (ruleId) {
        const etagNow = async () => (await catalog.api<V[]>('/rules')).find((r) => r.id === ruleId)?.etag
        await send('POST', `/rules/${ruleId}/publish`, { mode: 'disabled' }, await etagNow()).catch(() => {})
        await send('DELETE', `/rules/${ruleId}`, undefined, await etagNow()).catch(() => {})
      }
      for (const id of keys) await send('POST', `/keys/${id}/revoke`).catch(() => {})
    }
  }, 90_000)

  // POST /keys won't make a project, so the key form makes it first, with
  // its own audit row; Keys → Projects renames one and says why a delete is
  // refused.
  it('creates a project from the key form, and renames it and explains a refused delete from Keys → Projects', async () => {
    type C = import('@/data/catalog').Change
    type P = import('@/data/catalog').Project
    type K = { id: string; name: string; project: string; projectId: string }
    const stamp = Date.now().toString(36)
    const name = `Amt Form ${stamp}`
    const keyName = `amt-form-${stamp}`
    const support = catalog.teams.find((t) => t.id === 'support')?.name ?? 'support'
    let keyId = ''
    try {
      window.history.pushState({}, '', '/keys')
      render(<App />)
      fireEvent.click(await screen.findByRole('button', { name: /Create key/ }))
      const form = await formDialog()
      fireEvent.change(within(form).getByLabelText('Name'), { target: { value: keyName } })
      const picker = within(form).getByRole('combobox', { name: 'Project' })
      await waitFor(() => expect(picker).toHaveProperty('disabled', false))
      fireEvent.click(picker)
      choose(await screen.findByRole('option', { name: 'New project…' }))
      fireEvent.change(within(form).getByLabelText('New project name'), { target: { value: name } })
      expect(form.textContent).toContain(`Created on ${support} when you create the key.`)
      fireEvent.click(within(form).getByRole('radio', { name: '30 days' }))
      fireEvent.click(within(form).getByRole('button', { name: 'Create key' }))
      await waitFor(() => expect(document.body.textContent).toContain('Copy your new secret'), { timeout: 5000 })
      const p = (await catalog.api<P[]>('/projects')).find((x) => x.team === 'support' && x.name === name)!
      const k = (await catalog.api<K[]>('/keys')).find((x) => x.name === keyName)!
      keyId = k.id
      expect(k).toMatchObject({ project: name, projectId: p.id })
      expect((await catalog.api<C[]>('/changes')).slice(0, 2)).toMatchObject([
        { action: 'Created key', target: keyName },
        { action: 'Created project', target: `support / ${name}` },
      ])
      const secretStep = await formDialog()
      fireEvent.click(within(secretStep).getByRole('checkbox', { name: /stored this secret/ }))
      fireEvent.click(within(secretStep).getByRole('button', { name: 'Done' }))
      await formDialogClosed()

      // Keys → Projects: rename it, then try to delete it while its key is active.
      fireEvent.click(screen.getByRole('button', { name: /Projects/ }))
      const dialog = await formDialog()
      const label = `${name} (${support})`
      await waitFor(() => expect(within(dialog).getByRole('button', { name: `Rename ${label}` })).toBeTruthy(), { timeout: 5000 })
      fireEvent.click(within(dialog).getByRole('button', { name: `Rename ${label}` }))
      fireEvent.change(within(dialog).getByLabelText(`New name for ${label}`), { target: { value: `${name} renamed` } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Save name' }))
      await waitFor(async () => expect((await catalog.api<P[]>('/projects')).find((x) => x.id === p.id)?.name).toBe(`${name} renamed`), { timeout: 5000 })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Renamed project', target: `support / ${name} → ${name} renamed` })
      const renamed = `${name} renamed (${support})`
      await waitFor(() => expect(within(dialog).getByRole('button', { name: `Delete ${renamed}` })).toBeTruthy(), { timeout: 5000 })
      fireEvent.click(within(dialog).getByRole('button', { name: `Delete ${renamed}` }))
      fireEvent.click(within(dialog).getByRole('button', { name: 'Delete project' }))
      await waitFor(() => expect(dialog.textContent).toContain(`Project ${name} renamed still has 1 active key (${keyName}). Revoke the key first.`), { timeout: 5000 })
      expect((await catalog.api<P[]>('/projects')).some((x) => x.id === p.id)).toBe(true)
    } finally {
      if (keyId) await send('POST', `/keys/${keyId}/revoke`).catch(() => {})
    }
  }, 60_000)
  it('adds a project and its budget from Spend, and picks a team’s project on the key form', async () => {
    type B = import('@/data/catalog').Budget & { etag: string }
    type C = import('@/data/catalog').Change
    type P = import('@/data/catalog').Project
    const name = `amt-ui-proj-${Date.now().toString(36)}`
    const mine = async () => (await catalog.api<B[]>('/budgets')).find((b) => b.scopeType === 'project' && b.scopeName === name)
    let keyId = ''
    try {
      window.history.pushState({}, '', '/spend')
      render(<App />)
      await act(async () => {})
      const add = await screen.findByRole('button', { name: 'Add budget' })
      await waitFor(() => expect(add).toHaveProperty('disabled', false))
      fireEvent.click(add)
      const dialog = await formDialog()
      fireEvent.click(within(dialog).getByRole('radio', { name: /^Project/ }))
      expect(dialog.textContent).toContain('A project can have a budget before it has keys.')
      fireEvent.click(within(dialog).getByRole('button', { name: 'New project…' }))
      fireEvent.change(within(dialog).getByLabelText('Project name'), { target: { value: name } })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create project' }))
      await waitFor(() => expect(within(dialog).getByRole('combobox', { name: 'Project' }).textContent).toContain(name), { timeout: 5000 })
      const p = (await catalog.api<P[]>('/projects')).find((x) => x.name === name)!
      expect(p.team).toBe(catalog.teams[0].id)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created project', target: `${p.team} / ${name}` })
      fireEvent.change(within(dialog).getByLabelText('Monthly cap (USD)'), { target: { value: '5' } })
      fireEvent.click(within(dialog).getByRole('radio', { name: /^Warn/ }))
      await waitFor(() => expect(dialog.textContent).toContain('Covers no active keys yet.'), { timeout: 5000 })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Create budget' }))
      await formDialogClosed()
      expect(await mine()).toMatchObject({ scope: p.id, capUsd: 5, onExceed: 'warn' })
      const table = screen.getByRole('table', { name: 'Budgets' })
      await waitFor(() => expect(table.textContent).toContain(name), { timeout: 5000 })

      // The key form lists the team's projects; the new key joins this one.
      cleanup()
      window.history.pushState({}, '', '/keys')
      render(<App />)
      fireEvent.click(await screen.findByRole('button', { name: /Create key/ }))
      const form = await formDialog()
      fireEvent.change(within(form).getByLabelText('Name'), { target: { value: `${name}-k`.slice(0, 40) } })
      const picker = within(form).getByRole('combobox', { name: 'Project' })
      await waitFor(() => expect(picker).toHaveProperty('disabled', false))
      fireEvent.click(picker)
      choose(await screen.findByRole('option', { name }))
      await waitFor(() => expect(form.textContent).toContain(`project budget ${name}`), { timeout: 5000 })
      fireEvent.click(within(form).getByRole('radio', { name: '30 days' }))
      fireEvent.click(within(form).getByRole('button', { name: 'Create key' }))
      await waitFor(() => expect(document.body.textContent).toContain('Copy your new secret'), { timeout: 5000 })
      const k = (await catalog.api<{ id: string; name: string; projectId: string }[]>('/keys')).find((x) => x.name === `${name}-k`.slice(0, 40))!
      keyId = k.id
      expect(k.projectId).toBe(p.id)
    } finally {
      const b = await mine()
      if (b) await send('DELETE', `/budgets/${b.id}`, undefined, b.etag).catch(() => {})
      if (keyId) await send('POST', `/keys/${keyId}/revoke`).catch(() => {})
    }
  }, 60_000)

  it('splits a rotation’s traffic by secret, extends its overlap and retires the old secret', async () => {
    type K = { id: string; status: string; rotation?: { endsAt: number | null; oldSecretRequests: number | null; newSecretRequests: number | null } | null }
    type C = import('@/data/catalog').Change
    const gateway = (import.meta.env.VITE_STARGATE_GATEWAY as string | undefined) ?? 'http://localhost:1975'
    const call = (secret: string) =>
      fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5-mini', max_tokens: 20, messages: [{ role: 'user', content: 'hi' }] }),
      }).then((r) => r.status)
    const name = `api-mode-rotate-${Date.now().toString(36)}`
    const { key, secret: oldSecret } = await send<{ key: K; secret: string }>('POST', '/keys', {
      name, team: 'support', project: 'api-mode-test', allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
    })
    const keyNow = async () => (await catalog.api<K[]>('/keys')).find((k) => k.id === key.id)!
    try {
      const { secret: newSecret } = await send<{ key: K; secret: string }>('POST', `/keys/${key.id}/rotate`, { overlapHours: 1 })
      expect(await call(oldSecret)).not.toBe(401)
      expect(await call(newSecret)).not.toBe(401)
      expect(await call(newSecret)).not.toBe(401)
      let r: K['rotation']
      for (let i = 0; i < 30; i++) {
        r = (await keyNow()).rotation
        if (r?.oldSecretRequests === 1 && r.newSecretRequests === 2) break
        await new Promise((ok) => setTimeout(ok, 500))
      }
      expect(r).toMatchObject({ oldSecretRequests: 1, newSecretRequests: 2 })

      // The page shows the recorded split, then extends through the dialog.
      window.history.pushState({}, '', `/keys?key=${key.id}`)
      render(<App />)
      await act(async () => {
        await new Promise((ok) => setTimeout(ok, 500))
      })
      let text = document.body.textContent ?? ''
      expect(text).toContain('67% of requests since the rotation started used the new secret')
      expect(text).toMatch(/new secret\s*2 req/)
      expect(text).toMatch(/old secret\s*1 req/)
      fireEvent.click(screen.getByRole('button', { name: /View rotation/ }))
      fireEvent.click(within(await formDialog()).getByRole('button', { name: 'Extend overlap 24h' }))
      await formDialogClosed()
      const extended = await keyNow()
      expect(extended.rotation!.endsAt! - r!.endsAt!).toBeGreaterThan(24 * 3_600_000 - 60_000)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Extended rotation overlap', targetKind: 'Key' })
      expect((await catalog.api<C[]>('/changes'))[0].target).toMatch(new RegExp(`^${name} · until \\d{4}-\\d\\d-\\d\\d \\d\\d:\\d\\d UTC$`))
      // The overlap can't run past 7 days from now.
      expect(await status(send('POST', `/keys/${key.id}/rotation/extend`, { hours: 168 }))).toBe(400)

      // Retire asks first, stating how many requests the old secret served.
      fireEvent.click(screen.getByRole('button', { name: /View rotation/ }))
      let d = await formDialog()
      fireEvent.click(within(d).getByRole('button', { name: 'Retire old secret now' }))
      d = await formDialog()
      expect(d.textContent).toContain('1 request has used the old secret since the rotation started')
      fireEvent.click(within(d).getByRole('button', { name: 'Retire old secret' }))
      await formDialogClosed()
      const retired = await keyNow()
      expect(retired).toMatchObject({ status: 'active' })
      expect(retired.rotation ?? null).toBeNull()
      await act(async () => {
        await new Promise((ok) => setTimeout(ok, 100))
      })
      text = document.body.textContent ?? ''
      expect(text).not.toContain('Rotation in progress')
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Retired old secret', target: name })
      expect(await call(oldSecret)).toBe(401)
      expect(await call(newSecret)).not.toBe(401)
      expect(await status(send('POST', `/keys/${key.id}/rotation/finish`))).toBe(409)
      expect(await status(send('POST', `/keys/${key.id}/rotation/extend`, { hours: 1 }))).toBe(409)
    } finally {
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 60_000)

  type LiveRoute = import('@/data/catalog').LiveRoute
  type RoutingPlan = import('@/data/catalog').RoutingPlan
  /** Applies whatever is pending, so a test starts from the gateway running the desired routing. */
  const applyPending = async () => {
    const plan = await catalog.api<RoutingPlan>('/routing')
    if (plan.changes.length) await send('POST', '/routing/apply', {}, plan.etag)
  }
  /** Deletes the named routes if they're still there, then applies. */
  const dropRoutes = async (...names: string[]) => {
    for (const r of await catalog.api<LiveRoute[]>('/routes')) {
      if (names.includes(r.name)) await send('DELETE', `/routes/${r.name}`, undefined, r.etag)
    }
    await applyPending()
  }

  it('edits routes as desired state with audit rows and If-Match, diffs them against what the gateway runs, and applies them', async () => {
    type C = import('@/data/catalog').Change
    type Rc = { backend: string; resolvedModel: string; requestedModel: string }
    const name = `api-mode-${Date.now().toString(36)}`
    // A support key's gpt-5.5 goes to vllm-internal instead of openai-prod: two
    // header matches outrank the gpt-5 route's one.
    const route = {
      name,
      match: { models: ['gpt-5.5'], headers: [{ name: 'x-stargate-team', value: 'support' }] },
      targets: [{ backend: 'vllm-internal', model: 'llama-3.3-70b' }],
      fallback: [{ backend: 'bedrock-eu', model: 'claude-haiku-4-5' }],
    }
    await applyPending()
    let keyId = ''
    try {
      const before = await catalog.api<RoutingPlan>('/routing')
      expect(before).toMatchObject({ canApply: true, changes: [] })
      expect(before.yaml).toContain('kind: AIGatewayRoute')
      // Seeded routing is what the gateway runs.
      for (const r of await catalog.api<LiveRoute[]>('/routes')) expect(r.sync).toBe('synced')

      const dry = await send<{ dryRun: boolean; yaml: string }>('POST', '/routes?dryRun=true', route)
      expect(dry.dryRun).toBe(true)
      expect(dry.yaml).toContain('value: support')
      expect(dry.yaml).toContain('modelNameOverride: claude-haiku-4-5')
      expect(dry.yaml).toContain('priority: 1')
      expect((await catalog.api<LiveRoute[]>('/routes')).some((r) => r.name === name)).toBe(false)

      expect(await status(send('POST', '/routes', { ...route, targets: [{ backend: 'azure-openai-eu' }] }))).toBe(400)
      expect(await status(send('POST', '/routes', { ...route, match: { models: ['gpt-5.5'], headers: [] } }))).toBe(400) // the gpt-5 route has it
      const made = await send<LiveRoute>('POST', '/routes', route)
      expect(made).toMatchObject({ name, sync: 'pending' })
      expect(made.yaml).toContain('value: support')
      expect(await status(send('POST', '/routes', route))).toBe(409)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({
        action: 'Created route', targetKind: 'Route', actor: catalog.session.actor.email,
        target: `${name} · gpt-5.5 if x-stargate-team = support → vllm-internal as llama-3.3-70b, then bedrock-eu`,
      })

      expect(await status(send('PUT', `/routes/${name}`, route))).toBe(428)
      const moved = await send<LiveRoute>('PUT', `/routes/${name}`, { ...route, fallback: [] }, made.etag)
      expect(moved.etag).not.toBe(made.etag)
      expect(await status(send('PUT', `/routes/${name}`, route, made.etag))).toBe(409)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Changed route', target: `${name} · gpt-5.5 if x-stargate-team = support → vllm-internal as llama-3.3-70b` })

      const plan = await catalog.api<RoutingPlan>('/routing')
      // One change: the route's rule, and Anthropic-style callers' copy of it
      // right after, in aigw-run (or aigw-run-2… once that has its 7 routes).
      expect(plan.changes.map((c) => `${c.change} ${c.kind}/${c.name}`)).toEqual([expect.stringMatching(/^(changed|added) AIGatewayRoute\/aigw-run(-\d+)?$/)])
      expect(plan.changes[0].diff).toMatch(/^\+\s+value: support$/m)
      expect(plan.changes[0].diff).toMatch(/^\+\s+value: anthropic$/m)
      expect(plan.changes[0].diff).not.toMatch(/^-/m)

      expect(await status(send('POST', '/routing/apply', {}))).toBe(428)
      expect(await status(send('POST', '/routing/apply', {}, '"not-the-plan"'))).toBe(409)
      const applied = await send<{ ok: boolean; changes: unknown[] }>('POST', '/routing/apply', {}, plan.etag)
      expect(applied.ok).toBe(true)
      expect((await catalog.api<LiveRoute[]>('/routes')).find((r) => r.name === name)?.sync).toBe('synced')
      expect((await catalog.api<RoutingPlan>('/routing')).changes).toEqual([])
      expect((await catalog.api<RoutingPlan>('/routing')).lastApply).toMatchObject({ ok: true, actor: catalog.session.actor.email })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({
        action: 'Applied routing', targetKind: 'Routing', target: `1 change: ${plan.changes[0].change} AIGatewayRoute ${plan.changes[0].name}`,
      })

      // The gateway now routes by it.
      const made2 = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
        name, team: 'support', project: 'api-mode-test', allowedModels: ['gpt-5.5'], allowedRegions: ['us-east', 'eu-private'], expiresAt: '2027-01-01',
      })
      keyId = made2.key.id
      let res: Response | undefined
      for (let i = 0; i < 20 && res?.status !== 200; i++) {
        if (i) await new Promise((ok) => setTimeout(ok, 1000)) // the key check reloads every 5s
        res = await fetch(`${gateway}/v1/chat/completions`, {
          method: 'POST', headers: { Authorization: `Bearer ${made2.secret}`, 'Content-Type': 'application/json' },
          body: JSON.stringify({ model: 'gpt-5.5', max_tokens: 16, messages: [{ role: 'user', content: 'hi' }] }),
        })
      }
      expect(res?.status).toBe(200)
      await waitFor(async () => {
        const [rc] = await catalog.api<Rc[]>(`/receipts?key=${keyId}&limit=1`)
        expect(rc).toMatchObject({ requestedModel: 'gpt-5.5', backend: 'vllm-internal', resolvedModel: 'llama-3.3-70b' })
      }, { timeout: 20_000, interval: 1000 })
    } finally {
      if (keyId) await send('POST', `/keys/${keyId}/revoke`, {}).catch(() => {})
      await dropRoutes(name)
    }
    expect((await catalog.api<LiveRoute[]>('/routes')).some((r) => r.name === name)).toBe(false)
    expect((await catalog.api<RoutingPlan>('/routing')).changes).toEqual([])
  }, 180_000)

  it('creates, applies, edits and deletes a route on Routing, with the diff before apply and a stale edit refused', async () => {
    type C = import('@/data/catalog').Change
    const name = `ui-route-${Date.now().toString(36)}`
    await applyPending()
    window.history.pushState({}, '', '/routing?tab=routes')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const applyBar = () => screen.getByRole('region', { name: 'Apply to gateway' })
    try {
      // What's running is the desired routing: nothing to apply, and nothing the reconciler can't back up.
      expect(applyBar().textContent).toContain('The gateway runs this routing')
      expect(within(applyBar()).getByRole('button', { name: /Review and apply/ })).toHaveProperty('disabled', true)
      expect(screen.getAllByText('gpt-4o-mini').length).toBeGreaterThan(0)
      const text = document.body.textContent ?? ''
      for (const s of ['No reconciler', 'Drift detected', 'Recent reconcile events', '529 overloaded', 'Adopt into console', 'research-frontier']) expect(text).not.toContain(s)

      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: /New route/ }))
      })
      let d = await formDialog()
      fireEvent.change(within(d).getByLabelText('Name'), { target: { value: name } })
      fireEvent.change(within(d).getByLabelText('Models'), { target: { value: 'claude-haiku-4-5' } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: /Add condition/ }))
      })
      fireEvent.change(within(d).getByLabelText('Header 1'), { target: { value: 'x-stargate-team' } })
      fireEvent.change(within(d).getByLabelText('Header 1 value'), { target: { value: 'ui-test-team' } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('combobox', { name: 'Target 1 backend' }))
      })
      await act(async () => {
        choose(await screen.findByRole('option', { name: 'vllm-internal' }))
      })
      fireEvent.change(within(d).getByLabelText('Target 1 model'), { target: { value: 'llama-3.3-70b' } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: /Preview YAML/ }))
      })
      await waitFor(() => expect(within(d).getByText(/value: ui-test-team/)).toBeTruthy())
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Create route' }))
      })
      await formDialogClosed()
      await waitFor(() => expect(screen.getByRole('heading', { name })).toBeTruthy(), { timeout: 5000 })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created route', target: `${name} · claude-haiku-4-5 if x-stargate-team = ui-test-team → vllm-internal as llama-3.3-70b` })
      const row = () => screen.getByRole('heading', { name }).closest('li')!
      await waitFor(() => expect(row().textContent).toContain('Pending apply'))
      await waitFor(() => expect(applyBar().textContent).toContain('1 change not applied'))

      // Review the CRD diff, then apply it.
      await act(async () => {
        fireEvent.click(within(applyBar()).getByRole('button', { name: /Review and apply 1 change/ }))
      })
      d = await formDialog()
      expect(d.textContent).toContain('AIGatewayRoute/aigw-run')
      expect(d.textContent).toContain('value: ui-test-team')
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Apply to gateway' }))
      })
      await waitFor(() => expect(document.querySelector('[data-slot="dialog-content"]')).toBeNull(), { timeout: 90_000 })
      await waitFor(() => expect(applyBar().textContent).toContain('The gateway runs this routing'), { timeout: 10_000 })
      await waitFor(() => expect(row().textContent).toContain('Synced'))

      // Someone else edits it after the editor opened: saving is refused, then reload shows theirs.
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: `Edit ${name}` }))
      })
      d = await formDialog()
      const theirs = (await catalog.api<LiveRoute[]>('/routes')).find((x) => x.name === name)!
      await send('PUT', `/routes/${name}`, { ...theirs, targets: [{ backend: 'bedrock-eu' }] }, theirs.etag)
      fireEvent.change(within(d).getByLabelText('Target 1 model'), { target: { value: 'claude-sonnet-5' } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Save' }))
      })
      await waitFor(() => expect(d.textContent).toContain('This route changed since you opened it'))
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Load the current version' }))
      })
      await waitFor(() => expect(within(d).getByRole('combobox', { name: 'Target 1 backend' }).textContent).toContain('bedrock-eu'))
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Cancel' }))
      })
      await formDialogClosed()
      // The list picks up their version.
      await waitFor(() => expect(row().textContent).toContain('via bedrock-eu'))

      // Delete it: it leaves the list, and the gateway still runs it until the next apply.
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: `Delete ${name}` }))
      })
      d = await formDialog()
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Delete route' }))
      })
      await formDialogClosed()
      await waitFor(() => expect(screen.queryByRole('heading', { name })).toBeNull(), { timeout: 5000 })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Deleted route' })
      await waitFor(() => expect(applyBar().textContent).toContain('1 change not applied'))
    } finally {
      r.unmount()
      await dropRoutes(name)
    }
  }, 240_000)

  it('shows each backend’s endpoint, its generated YAML and whether the gateway runs it on Routing', async () => {
    type B = { name: string; sync: string; endpoint?: { host: string; port: string } }
    await applyPending()
    const bs = await catalog.api<B[]>('/backends')
    expect(bs.find((b) => b.name === 'openai-prod')).toMatchObject({ sync: 'synced', endpoint: { host: '${STARGATE_HOST:-localhost}', port: '8090' } })
    expect(bs.find((b) => b.name === 'azure-openai-eu')).toMatchObject({ sync: 'no_endpoint' })
    expect(bs.find((b) => b.name === 'azure-openai-eu')?.endpoint).toBeUndefined()

    window.history.pushState({}, '', '/routing?tab=backends')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    try {
      expect(document.body.textContent).toContain('No endpoint')
      await act(async () => {
        fireEvent.click(screen.getAllByRole('button', { name: 'openrouter' })[0])
        await new Promise((ok) => setTimeout(ok, 200))
      })
      const text = document.body.textContent ?? ''
      expect(text).toContain('openrouter.ai:443')
      expect(text).toContain('OPENROUTER_API_KEY')
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: /View generated YAML/ }))
      })
      const d = await formDialog()
      expect(d.textContent).toContain('kind: AIServiceBackend')
      expect(d.textContent).toContain('kind: BackendSecurityPolicy')
      for (const s of ['Adopt into console', 'Drift detected', 'Replicas']) expect(document.body.textContent).not.toContain(s)
    } finally {
      r.unmount()
    }
  })

  // fake-openai's "keyed" backend wants its own provider key (fakellm.KeyedKey,
  // or KeyedKey2: X-Fake-Key says which it got) and answers 401 in OpenAI's
  // words without it.
  const fakeOpenAI = (import.meta.env.VITE_FAKE_OPENAI as string | undefined) ?? 'http://localhost:8090'
  const KEYED_KEY = 'sk-fake-keyed-7d1c0b5e9a2f4e68'
  const KEYED_KEY_2 = 'sk-fake-keyed-2b8e4f1a0c6d3957'
  const WRONG_KEY = 'sk-wrongkey-0000000000000000'
  /** The part of a key past its prefix: in no response, ever (§9.1). */
  const secretPart = (k: string) => k.slice(8)
  /** Records every response body the console's fetch sees until stop(). */
  const recordBodies = () => {
    const prev = globalThis.fetch
    const bodies: string[] = []
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const res = await prev(input, init)
      bodies.push(await res.clone().text().catch(() => ''))
      return res
    }) as typeof fetch
    return { bodies, stop: () => void (globalThis.fetch = prev) }
  }
  type BackendView = import('@/data/catalog').Backend
  type BackendResult = import('@/data/catalog').BackendResult
  type ConnectionTest = import('@/data/catalog').ConnectionTest
  /** Deletes the named backends if they're still there (routes to them first), then applies. */
  const dropBackends = async (...names: string[]) => {
    for (const r of await catalog.api<LiveRoute[]>('/routes')) {
      if ([...r.targets, ...r.fallback].some((t) => names.includes(t.backend))) await send('DELETE', `/routes/${r.name}`, undefined, r.etag).catch(() => {})
    }
    for (const b of await catalog.api<BackendView[]>('/backends')) {
      if (names.includes(b.name)) await send('DELETE', `/backends/${b.name}`, undefined, b.etag).catch(() => {})
    }
    await applyPending()
  }
  /** Calls keyed-echo through the gateway until the key check has the key (it reloads every 5s). */
  const callKeyed = async (secret: string, until: (status: number) => boolean) => {
    let res: Response | undefined
    for (let i = 0; i < 20 && !(res && until(res.status)); i++) {
      if (i) await new Promise((ok) => setTimeout(ok, 1000))
      res = await fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'keyed-echo', max_tokens: 16, messages: [{ role: 'user', content: 'hello keyed' }] }),
      })
    }
    return res!
  }

  it('adds a provider with its key, never returns the key, routes to it through the gateway, replaces the key and deletes it', async () => {
    type C = import('@/data/catalog').Change
    type Rc = { backend: string; resolvedModel: string; status: number }
    const name = `keyed-${Date.now().toString(36)}`
    const ref = `STARGATE_PROVIDER_KEY_${name.toUpperCase().replace(/[^A-Z0-9]/g, '_')}`
    const provider = { name, provider: 'OpenAI-compatible', region: 'local', baseUrl: `${fakeOpenAI}/keyed/v1`, models: ['keyed-echo'] }
    await applyPending()
    const rec = recordBodies()
    let keyId = ''
    try {
      // Test connection before saving: the key is in the request body only.
      const wrong = await send<ConnectionTest>('POST', '/backends/test', { ...provider, apiKey: WRONG_KEY })
      expect(wrong).toMatchObject({ ok: false, status: 401 })
      expect(wrong.error).toContain('Incorrect API key provided')
      const right = await send<ConnectionTest>('POST', '/backends/test', { ...provider, apiKey: KEYED_KEY })
      expect(right).toMatchObject({ ok: true, status: 200 })
      expect(right.models).toContain('keyed-echo')
      expect((await catalog.api<BackendView[]>('/backends')).some((b) => b.name === name)).toBe(false) // a test stores nothing
      expect(await status(send('POST', '/backends/test', { ...provider, provider: 'Bedrock' }))).toBe(400)

      // Save it with the key: Postgres keeps the prefix, the key file the key.
      expect(await status(send('POST', '/backends', { ...provider, provider: 'Bedrock', apiKey: KEYED_KEY }))).toBe(400)
      expect(await status(send('POST', '/backends', { ...provider, models: [] }))).toBe(400)
      // §7.5.1 "Tested once, then sealed": a key that fails its test saves nothing.
      const auditBefore = (await catalog.api<C[]>('/changes?kind=Backend'))[0]
      const refusedKey = (await send('POST', '/backends', { ...provider, apiKey: WRONG_KEY }).catch((e) => e)) as { status: number; code: string; message: string }
      expect(refusedKey).toMatchObject({ status: 422, code: 'key_test_failed' })
      expect(refusedKey.message).toContain('not saved')
      expect(refusedKey.message).toContain('Incorrect API key provided')
      expect((await catalog.api<BackendView[]>('/backends')).some((b) => b.name === name)).toBe(false)
      expect((await catalog.api<C[]>('/changes?kind=Backend'))[0]).toEqual(auditBefore)
      expect((await catalog.api<RoutingPlan>('/routing')).changes).toEqual([])
      const made = await send<BackendResult>('POST', '/backends', { ...provider, apiKey: KEYED_KEY })
      expect(made.backend).toMatchObject({ name, sync: 'pending', key: { prefix: KEYED_KEY.slice(0, 8) }, endpoint: { apiKeyEnv: ref, baseUrl: provider.baseUrl } })
      expect(made.test).toMatchObject({ ok: true })
      expect(made.backend.lastTest).toMatchObject({ ok: true })
      expect(await status(send('POST', '/backends', { ...provider, apiKey: KEYED_KEY }))).toBe(409)
      expect((await catalog.api<C[]>('/changes?kind=Backend'))[0]).toMatchObject({ action: 'Created backend', target: expect.stringContaining(`key ${KEYED_KEY.slice(0, 8)}…`) })
      // A model the catalog didn't know joins it, with no price.
      expect((await catalog.api<{ id: string }[]>('/models')).some((m) => m.id === 'keyed-echo')).toBe(true)
      const pricing = await catalog.api<{ prices: { model: string; backend: string; priced: boolean }[] }>('/pricing')
      expect(pricing.prices.find((p) => p.model === 'keyed-echo' && p.backend === name)).toMatchObject({ priced: false })

      // The plan adds its resources; the Secret names the variable, not the key.
      const plan = await catalog.api<RoutingPlan>('/routing')
      expect(plan.changes.map((c) => `${c.change} ${c.kind}/${c.name}`)).toEqual(
        expect.arrayContaining([`added Backend/${name}`, `added AIServiceBackend/${name}`, `added BackendSecurityPolicy/${name}-key`, `added Secret/${name}-key`]),
      )
      expect(plan.changes.find((c) => c.kind === 'Secret')?.diff).toContain(`\${${ref}:-not-set}`)

      // Route to it; it can't be deleted while routed.
      const route = await send<LiveRoute>('POST', '/routes', { name, match: { models: ['keyed-echo'], headers: [] }, targets: [{ backend: name }], fallback: [] })
      const routed = (await catalog.api<BackendView[]>('/backends')).find((b) => b.name === name)!
      const refused = (await send('DELETE', `/backends/${name}`, undefined, routed.etag).catch((e) => e)) as { status: number; message: string }
      expect(refused).toMatchObject({ status: 409 })
      expect(String(refused.message)).toContain(`route ${name}`)

      await applyPending()
      expect((await catalog.api<BackendView[]>('/backends')).find((b) => b.name === name)?.sync).toBe('synced')
      const k = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
        name, team: 'support', project: 'api-mode-test', allowedModels: ['keyed-echo'], allowedRegions: ['local'], expiresAt: '2027-01-01',
      })
      keyId = k.key.id
      expect((await callKeyed(k.secret, (s) => s === 200)).status).toBe(200)
      await waitFor(async () => {
        const [rc] = await catalog.api<Rc[]>(`/receipts?key=${keyId}&limit=1`)
        expect(rc).toMatchObject({ backend: name, resolvedModel: 'keyed-echo', status: 200 })
      }, { timeout: 20_000, interval: 1000 })

      expect((await callKeyed(k.secret, (s) => s === 200)).headers.get('x-fake-key')).toBe('1')

      // Replacing it with a key that fails its test is refused: nothing changes.
      const before = await catalog.api<RoutingPlan>('/routing')
      const wrongReplace = (await send('PUT', `/backends/${name}/key`, { apiKey: WRONG_KEY }).catch((e) => e)) as { status: number; code: string; message: string }
      expect(wrongReplace).toMatchObject({ status: 422, code: 'key_test_failed' })
      expect(wrongReplace.message).toContain('Incorrect API key provided')
      expect((await catalog.api<BackendView[]>('/backends')).find((b) => b.name === name)).toMatchObject({ sync: 'synced', key: { prefix: KEYED_KEY.slice(0, 8) } })
      expect((await catalog.api<C[]>('/changes?kind=Backend'))[0]).not.toMatchObject({ action: 'Replaced provider key', target: expect.stringContaining(WRONG_KEY.slice(0, 8)) })
      expect((await catalog.api<RoutingPlan>('/routing')).etag).toBe(before.etag)

      // Replace it with another good key: tested at once, pending until applied.
      const replaced = await send<BackendResult>('PUT', `/backends/${name}/key`, { apiKey: KEYED_KEY_2 })
      expect(replaced.test).toMatchObject({ ok: true, status: 200 })
      expect(replaced.backend).toMatchObject({ sync: 'pending', key: { prefix: KEYED_KEY_2.slice(0, 8) }, lastTest: { ok: true } })
      expect((await catalog.api<C[]>('/changes?kind=Backend'))[0]).toMatchObject({ action: 'Replaced provider key', target: `${name} · key ${KEYED_KEY_2.slice(0, 8)}…` })
      const pending = await catalog.api<RoutingPlan>('/routing')
      expect(pending.changes.map((c) => `${c.change} ${c.kind}/${c.name}`)).toEqual([`key replaced Secret/${name}-key`])
      expect(pending.etag).not.toBe(before.etag)
      // §7.1 principle 4: the gateway sends the applied key until the apply
      // restarts it with the new one (the new key is staged, not in the file
      // aigw starts with, so no other restart picks it up either).
      expect((await callKeyed(k.secret, (s) => s === 200)).headers.get('x-fake-key')).toBe('1')
      await send('POST', '/routing/apply', {}, pending.etag)
      expect((await catalog.api<RoutingPlan>('/routing')).changes).toEqual([])
      expect((await callKeyed(k.secret, (s) => s === 200)).headers.get('x-fake-key')).toBe('2')
      expect((await send<BackendResult>('POST', `/backends/${name}/test`)).test).toMatchObject({ ok: true })

      // Edit with If-Match; the key stays as it is.
      const cur = (await catalog.api<BackendView[]>('/backends')).find((b) => b.name === name)!
      expect(await status(send('PUT', `/backends/${name}`, { ...provider, models: ['keyed-echo', 'keyed-echo-2'] }))).toBe(428)
      const edited = await send<BackendView>('PUT', `/backends/${name}`, { ...provider, models: ['keyed-echo', 'keyed-echo-2'] }, cur.etag)
      expect(edited).toMatchObject({ models: ['keyed-echo', 'keyed-echo-2'], key: { prefix: KEYED_KEY.slice(0, 8) } })
      expect(await status(send('PUT', `/backends/${name}`, provider, cur.etag))).toBe(409)
      expect((await catalog.api<C[]>('/changes?kind=Backend'))[0]).toMatchObject({ action: 'Changed backend' })

      // Once no route sends to it, it can be deleted.
      await send('DELETE', `/routes/${name}`, undefined, (await catalog.api<LiveRoute[]>('/routes')).find((r) => r.name === route.name)!.etag)
      expect(await status(send('DELETE', `/backends/${name}`))).toBe(428)
      await send('DELETE', `/backends/${name}`, undefined, edited.etag)
      expect((await catalog.api<BackendView[]>('/backends')).some((b) => b.name === name)).toBe(false)
      expect((await catalog.api<C[]>('/changes?kind=Backend'))[0]).toMatchObject({ action: 'Deleted backend' })
      await applyPending()

      // Nothing the control plane said, in any response, carried a key.
      expect(rec.bodies.length).toBeGreaterThan(20)
      for (const b of rec.bodies) {
        expect(b).not.toContain(secretPart(KEYED_KEY))
        expect(b).not.toContain(secretPart(KEYED_KEY_2))
        expect(b).not.toContain(secretPart(WRONG_KEY))
      }
    } finally {
      rec.stop()
      if (keyId) await send('POST', `/keys/${keyId}/revoke`, {}).catch(() => {})
      await dropBackends(name)
    }
  }, 600_000)

  it('adds a provider on Routing with Test connection, then edits it, replaces its key and deletes it from its drawer', async () => {
    const name = `ui-keyed-${Date.now().toString(36)}`
    await applyPending()
    window.history.pushState({}, '', '/routing?tab=backends')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const text = () => document.body.textContent ?? ''
    try {
      for (const s of ['Adding providers isn’t connected', 'editing them isn’t connected']) expect(document.body.innerHTML).not.toContain(s)
      const add = screen.getByRole('button', { name: /Add provider/ })
      expect(add).toHaveProperty('disabled', false)
      await act(async () => {
        fireEvent.click(add)
      })
      let d = await formDialog()
      // Cloud providers are shown, with why they can't be added yet.
      expect(within(d).getByRole('radio', { name: /Bedrock/ }).hasAttribute('data-disabled')).toBe(true)
      expect(d.textContent).toContain('cloud credentials')
      choose(within(d).getByRole('radio', { name: /OpenAI-compatible/ }))
      fireEvent.change(within(d).getByLabelText('Name'), { target: { value: name } })
      fireEvent.change(within(d).getByLabelText('Base URL'), { target: { value: `${fakeOpenAI}/keyed/v1` } })
      fireEvent.change(within(d).getByLabelText('Region'), { target: { value: 'local' } })
      const keyInput = within(d).getByLabelText('API key') as HTMLInputElement
      expect(keyInput.type).toBe('password')
      fireEvent.change(keyInput, { target: { value: WRONG_KEY } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Test connection' }))
      })
      await waitFor(() => expect(d.textContent).toContain('Incorrect API key provided'), { timeout: 10_000 })
      expect(d.textContent).toContain('401')
      // Saving with the wrong key saves nothing, and says so in the provider's words.
      fireEvent.change(within(d).getByLabelText('Models'), { target: { value: 'keyed-echo' } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Save provider' }))
      })
      await waitFor(() => expect(d.textContent).toContain('The key failed its connection test'), { timeout: 10_000 })
      expect(d.textContent).toContain('not saved')
      expect(d.textContent).toContain('Incorrect API key provided')
      expect((await catalog.api<BackendView[]>('/backends')).some((b) => b.name === name)).toBe(false)
      fireEvent.change(within(d).getByLabelText('Models'), { target: { value: '' } })
      fireEvent.change(keyInput, { target: { value: KEYED_KEY } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Test connection' }))
      })
      await waitFor(() => expect(within(d).getByRole('button', { name: 'Add keyed-echo' })).toBeTruthy(), { timeout: 10_000 })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Add keyed-echo' }))
      })
      expect((within(d).getByLabelText('Models') as HTMLInputElement).value).toBe('keyed-echo')
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Save provider' }))
      })
      await formDialogClosed()
      await waitFor(() => expect(screen.getAllByRole('button', { name }).length).toBeGreaterThan(0), { timeout: 5000 })
      const saved = (await catalog.api<BackendView[]>('/backends')).find((b) => b.name === name)
      expect(saved).toMatchObject({ sync: 'pending', key: { prefix: KEYED_KEY.slice(0, 8) }, lastTest: { ok: true } })

      // The drawer shows the prefix and the last test, never the key.
      await act(async () => {
        fireEvent.click(screen.getAllByRole('button', { name })[0])
        await new Promise((ok) => setTimeout(ok, 200))
      })
      expect(text()).toContain(`${KEYED_KEY.slice(0, 8)}…`)
      expect(text()).toContain('1 model: keyed-echo')
      expect(document.body.innerHTML).not.toContain(secretPart(KEYED_KEY))

      // Replace the key: a key that fails its test is refused and says why;
      // a good one is saved and waits for an apply.
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Replace key' }))
      })
      d = await formDialog()
      const newKey = within(d).getByLabelText('New API key') as HTMLInputElement
      expect(newKey.type).toBe('password')
      fireEvent.change(newKey, { target: { value: WRONG_KEY } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Replace key' }))
      })
      await waitFor(() => expect(d.textContent).toContain('Incorrect API key provided'), { timeout: 10_000 }) // tested once, and it says so
      expect(d.textContent).toContain('The key failed its connection test')
      expect(d.textContent).toContain('not saved')
      expect(within(d).queryByRole('button', { name: 'Done' })).toBeNull()
      expect((await catalog.api<BackendView[]>('/backends')).find((b) => b.name === name)?.key?.prefix).toBe(KEYED_KEY.slice(0, 8))
      fireEvent.change(within(d).getByLabelText('New API key'), { target: { value: KEYED_KEY_2 } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Replace key' }))
      })
      await waitFor(() => expect(within(d).getByRole('button', { name: 'Done' })).toBeTruthy(), { timeout: 10_000 })
      expect(d.textContent).toContain('pending until you apply')
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Done' }))
      })
      await formDialogClosed()

      // Edit its models.
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Edit provider' }))
      })
      d = await formDialog()
      expect(within(d).queryByLabelText('API key')).toBeNull() // keys are replaced on their own
      fireEvent.change(within(d).getByLabelText('Models'), { target: { value: 'keyed-echo, keyed-echo-2' } })
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Save provider' }))
      })
      await formDialogClosed()
      await waitFor(() => expect(text()).toContain('keyed-echo-2'), { timeout: 5000 })

      // Delete it (no route sends to it).
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Delete provider' }))
      })
      d = await formDialog()
      await act(async () => {
        fireEvent.click(within(d).getByRole('button', { name: 'Delete provider' }))
      })
      await formDialogClosed()
      await waitFor(() => expect(screen.queryAllByRole('button', { name })).toHaveLength(0), { timeout: 5000 })
      expect(document.body.innerHTML).not.toContain(secretPart(KEYED_KEY))
      expect(document.body.innerHTML).not.toContain(secretPart(WRONG_KEY))
    } finally {
      r.unmount()
      await dropBackends(name)
    }
  }, 120_000)

  it('onboards with a new provider: tests the key, saves it, routes its models and applies all pending changes, then a key’s first request lands on it', async () => {
    const name = `onb-keyed-${Date.now().toString(36)}`
    const other = `onb-other-${Date.now().toString(36)}`
    await applyPending()
    window.history.pushState({}, '', '/onboarding')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 400))
    })
    const text = () => document.body.textContent ?? ''
    let keyId = ''
    try {
      expect(text()).not.toContain('Adding a provider and its credentials isn’t connected')
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Connect a new provider' }))
      })
      const form = screen.getByRole('form', { name: 'New provider' })
      choose(within(form).getByRole('radio', { name: /OpenAI-compatible/ }))
      fireEvent.change(within(form).getByLabelText('Name'), { target: { value: name } })
      fireEvent.change(within(form).getByLabelText('Base URL'), { target: { value: `${fakeOpenAI}/keyed/v1` } })
      fireEvent.change(within(form).getByLabelText('Region'), { target: { value: 'local' } })
      fireEvent.change(within(form).getByLabelText('API key'), { target: { value: KEYED_KEY } })
      expect(text()).toContain('Tested once, then sealed')
      await act(async () => {
        fireEvent.click(within(form).getByRole('button', { name: 'Test connection' }))
      })
      await waitFor(() => expect(within(form).getByRole('button', { name: 'Add keyed-echo' })).toBeTruthy(), { timeout: 10_000 })
      fireEvent.click(within(form).getByRole('button', { name: 'Add keyed-echo' }))
      await act(async () => {
        fireEvent.click(within(form).getByRole('button', { name: 'Save provider' }))
      })
      // Saved, then its models need a route (a saved change, not yet applied).
      const route = await screen.findByRole('button', { name: `Route keyed-echo to ${name}` }, { timeout: 10_000 })
      await act(async () => {
        fireEvent.click(route)
      })
      // §7.3: the applier applies the whole desired routing, so the button
      // says it applies every pending change, and lists them first,
      // including one that isn't about this provider.
      await send('POST', '/backends', { name: other, provider: 'Self-hosted', region: 'local', baseUrl: `${fakeOpenAI}/vllm-internal/v1`, models: ['llama-3.3-70b'] })
      const plan = await catalog.api<RoutingPlan>('/routing')
      const n = plan.changes.length
      expect(n).toBeGreaterThan(4)
      const apply = await screen.findByRole('button', { name: `Apply all ${n} changes` }, { timeout: 20_000 })
      const pendingList = screen.getByRole('list', { name: 'Pending routing changes' })
      for (const c of plan.changes) expect(pendingList.textContent).toContain(`${c.kind}/${c.name}`)
      expect(pendingList.textContent).toContain(`Backend/${other}`)
      expect(screen.queryByRole('button', { name: /and apply/ })).toBeNull()
      await act(async () => {
        fireEvent.click(apply)
      })
      await waitFor(() => expect(screen.getByRole('button', { name: `Create a key for ${name}` })).toBeTruthy(), { timeout: 120_000 })
      expect((await catalog.api<RoutingPlan>('/routing')).changes).toEqual([])
      expect((await catalog.api<BackendView[]>('/backends')).find((b) => b.name === other)?.sync).toBe('synced')
      expect((await catalog.api<LiveRoute[]>('/routes')).find((x) => x.name === name)).toMatchObject({ sync: 'synced', targets: [{ backend: name }] })
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: `Create a key for ${name}` }))
      })
      const { gatewayUrl } = await catalog.api<{ gatewayUrl: string }>('/session')
      await waitFor(() => expect(text()).toContain(gatewayUrl), { timeout: 5000 })
      keyId = (await catalog.api<{ id: string; name: string; project: string }[]>('/keys')).find((k) => k.project === 'onboarding' && text().includes(k.name))?.id ?? ''
      expect(keyId).not.toBe('')
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Send a test request for me' }))
      })
      await waitFor(() => expect(text()).toContain('Your first request'), { timeout: 60_000 })
      expect(text()).toContain(`via ${name}`)
      expect(document.body.innerHTML).not.toContain(secretPart(KEYED_KEY))
    } finally {
      if (keyId) await catalog.api(`/keys/${keyId}/revoke`, { method: 'POST' })
      r.unmount()
      await dropBackends(name, other)
    }
  }, 300_000)

  // fake-openai's "keyed-anthropic" backend speaks Anthropic's native API
  // (fakellm.AnthropicBackend): x-api-key and anthropic-version, no bearer token.
  const ANTHROPIC_KEY = 'sk-ant-fake-3c9e1f0a6b2d4c87'
  it('tests an Anthropic key on Anthropic’s native API, and refuses one that fails', async () => {
    const name = `anthropic-${Date.now().toString(36)}`
    const provider = { name, provider: 'Anthropic', region: 'local', baseUrl: `${fakeOpenAI}/keyed-anthropic/v1`, models: ['claude-echo'] }
    try {
      const right = await send<ConnectionTest>('POST', '/backends/test', { ...provider, apiKey: ANTHROPIC_KEY })
      expect(right).toMatchObject({ ok: true, status: 200, models: ['claude-echo'] })
      const wrong = await send<ConnectionTest>('POST', '/backends/test', { ...provider, apiKey: WRONG_KEY })
      expect(wrong).toMatchObject({ ok: false, status: 401 })
      expect(wrong.error).toContain('invalid x-api-key')
      // An OpenAI-compatible test (a bearer token) doesn't get in: the test really is native.
      expect(await send<ConnectionTest>('POST', '/backends/test', { ...provider, provider: 'OpenAI-compatible', apiKey: ANTHROPIC_KEY })).toMatchObject({ ok: false, status: 401 })
      const refused = (await send('POST', '/backends', { ...provider, apiKey: WRONG_KEY }).catch((e) => e)) as { status: number; message: string }
      expect(refused).toMatchObject({ status: 422 })
      expect(refused.message).toContain('invalid x-api-key')
      expect(refused.message).not.toContain(secretPart(WRONG_KEY))
      expect((await catalog.api<BackendView[]>('/backends')).some((b) => b.name === name)).toBe(false)
    } finally {
      await dropBackends(name)
    }
  }, 60_000)
  // One Anthropic provider serves both kinds of caller (docs/backend-decisions.md
  // §6): it compiles to its OpenAI-compatible AIServiceBackend, for OpenAI-style
  // callers, and <name>-native (schema Anthropic, AnthropicAPIKey) for
  // Anthropic-style ones, which the Anthropic SDK is with base URL
  // <gateway>/anthropic. aigw v1.1.0 can't translate OpenAI's API to Anthropic's
  // for a direct Anthropic backend, so OpenAI-style callers stay on the
  // compatible endpoint. Needs fake-openai from this branch: keyed-anthropic
  // echoes on both APIs (X-Fake-Received) and takes its key as a bearer token on
  // chat completions.
  it('serves the Anthropic SDK natively through one Anthropic provider, with the key check, Warden and receipts, and OpenAI-style callers still on its compatible endpoint', async () => {
    type V = { id: string; mode: string; version: number; etag: string }
    type P = import('@/data/catalog').PricingView
    type Rc = import('@/data/catalog').Receipt
    type Msg = { id: string; type: string; model: string; content: { type: string; text: string }[]; usage: { input_tokens: number; output_tokens: number } }
    const name = `anthropic-sdk-${Date.now().toString(36)}`
    const provider = { name, provider: 'Anthropic', region: 'local', baseUrl: `${fakeOpenAI}/keyed-anthropic/v1`, models: ['claude-echo'] }
    const email = 'jordan.lee@example.com'
    const messages = (secret: string, body: object, headers: Record<string, string> = {}) =>
      fetch(`${gateway}/anthropic/v1/messages`, {
        method: 'POST',
        headers: { 'x-api-key': secret, 'anthropic-version': '2023-06-01', 'Content-Type': 'application/json', ...headers },
        body: JSON.stringify({ model: 'claude-echo', max_tokens: 64, ...body }),
      })
    const received = (res: Response) => decodeURIComponent((res.headers.get('x-fake-received') ?? '').replace(/\+/g, ' '))
    let keyId = ''
    let ruleId = ''
    await applyPending()
    try {
      // One provider, both APIs: the plan adds the native twin and its x-api-key policy.
      const made = await send<BackendResult>('POST', '/backends', { ...provider, apiKey: ANTHROPIC_KEY })
      expect(made.test).toMatchObject({ ok: true })
      const plan = await catalog.api<RoutingPlan>('/routing')
      expect(plan.changes.map((c) => `${c.change} ${c.kind}/${c.name}`)).toEqual(
        expect.arrayContaining([
          `added AIServiceBackend/${name}`, `added BackendSecurityPolicy/${name}-key`, `added Secret/${name}-key`,
          `added AIServiceBackend/${name}-native`, `added BackendSecurityPolicy/${name}-native-key`,
        ]),
      )
      const native = plan.changes.find((c) => c.name === `${name}-native` && c.kind === 'AIServiceBackend')!.diff
      expect(native).toContain('name: Anthropic')
      expect(native).toContain('prefix: /keyed-anthropic/v1')
      const policy = plan.changes.find((c) => c.name === `${name}-native-key`)!.diff
      expect(policy).toContain('type: AnthropicAPIKey')
      expect(policy).toContain(`name: ${name}-key`) // the same Secret as the OpenAI-compatible twin
      expect((await catalog.api<BackendView[]>('/backends')).find((b) => b.name === name)?.yaml).toContain(`name: ${name}-native`)

      // A route's rule shows where Anthropic-style callers go.
      const route = await send<LiveRoute & { yaml: string }>('POST', '/routes', { name, match: { models: ['claude-echo'], headers: [] }, targets: [{ backend: name }], fallback: [] })
      expect(route.yaml).toContain('value: anthropic')
      expect(route.yaml).toContain(`name: ${name}-native`)
      await applyPending()
      expect((await catalog.api<BackendView[]>('/backends')).find((b) => b.name === name)?.sync).toBe('synced')
      expect((await catalog.api<LiveRoute[]>('/routes')).find((r) => r.name === name)?.sync).toBe('synced')

      // A price, so the receipt's cost can be checked.
      const pair = (await catalog.api<P>('/pricing')).prices.find((p) => p.model === 'claude-echo' && p.backend === name)!
      await send('POST', `/pricing/claude-echo/${name}`, { rates: { input: 3, cachedInput: 0.3, cacheWrite: 3.75, output: 15, reasoning: 15 } }, pair.etag)

      const k = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
        name, team: 'security', project: 'api-mode-test', allowedModels: ['claude-echo'], allowedRegions: ['local'], expiresAt: '2027-01-01',
      })
      keyId = k.key.id

      // The Anthropic SDK's request, the gateway key as x-api-key: it reaches the
      // native fake (a Messages id, not a translated chat completion's), which
      // got the provider key, never the gateway's.
      let res: Response | undefined
      for (let i = 0; i < 20 && res?.status !== 200; i++) {
        if (i) await new Promise((ok) => setTimeout(ok, 1000)) // the key check reloads keys every 5s
        res = await messages(k.secret, { messages: [{ role: 'user', content: 'hello anthropic' }] })
      }
      const text = await res!.text()
      expect(res!.status, text).toBe(200)
      const msg = JSON.parse(text) as Msg
      expect(msg).toMatchObject({ type: 'message', model: 'claude-echo', content: [{ type: 'text', text: 'You said: hello anthropic' }] })
      expect(msg.id).toMatch(/^msg_fake/)
      expect(received(res!)).toBe('You said: hello anthropic')

      // Its receipt: the backend as the console names it, the fake's tokens, the price.
      const rc = await waitFor(async () => {
        const [r] = await catalog.api<Rc[]>(`/receipts?limit=1&key=${keyId}`)
        expect(r).toMatchObject({ backend: name, provider: 'Anthropic', resolvedModel: 'claude-echo', requestedModel: 'claude-echo', status: 200 })
        return r
      }, { timeout: 20_000, interval: 1000 })
      expect(rc.inputTokens).toBe(msg.usage.input_tokens)
      expect(rc.outputTokens).toBe(msg.usage.output_tokens)
      expect(rc.reasoningTokens).toBe(0)
      expect(rc.costUsd).toBeCloseTo((msg.usage.input_tokens * 3 + msg.usage.output_tokens * 15) / 1e6, 9)
      expect(rc.trace.find((t) => t.step === 'Route selected')?.outcome).toContain('Anthropic Messages API')

      // The key check refuses in Anthropic's error shape, so the SDK raises its usual errors.
      const bad = await messages('ngw_live_0000_not_a_key', { messages: [{ role: 'user', content: 'hi' }] })
      expect(bad.status).toBe(401)
      expect(await bad.json()).toMatchObject({ type: 'error', error: { type: 'authentication_error', code: 'invalid_api_key' } })
      const notAllowed = await messages(k.secret, { model: 'gpt-5.5', messages: [{ role: 'user', content: 'hi' }] })
      expect(notAllowed.status).toBe(403)
      expect(await notAllowed.json()).toMatchObject({ type: 'error', error: { type: 'permission_error', code: 'model_not_allowed' } })

      // Warden reads an Anthropic body: a redact-and-rehydrate rule redacts the
      // text block before the provider, and the reply comes back restored, whole
      // and streamed.
      const made2 = await send<V>('POST', '/rules', {
        name, description: 'api-mode Anthropic SDK test', failMode: 'closed',
        when: [{ field: 'prompt', op: 'contains entity', value: ['email'] }, { field: 'key', op: 'is', value: [name] }],
        then: [{ action: 'redact', detail: 'email · rehydrate on return' }],
      })
      ruleId = made2.id
      await send<V>('POST', `/rules/${ruleId}/publish`, { mode: 'enforce' }, made2.etag)
      const ask = { system: [{ type: 'text', text: 'Be brief.' }], messages: [{ role: 'user', content: [{ type: 'text', text: `Write to ${email} today` }] }] }
      const whole = await messages(k.secret, ask)
      expect(whole.status).toBe(200)
      expect(received(whole)).toBe('You said: Write to [EMAIL_1] today') // the provider never saw the address
      expect(((await whole.json()) as Msg).content[0].text).toBe(`You said: Write to ${email} today`)
      const streamed = await messages(k.secret, { ...ask, stream: true })
      expect(streamed.status).toBe(200)
      expect(received(streamed)).toBe('You said: Write to [EMAIL_1] today')
      let reply = ''
      for (const line of (await streamed.text()).split('\n')) {
        if (!line.startsWith('data: ')) continue
        const ev = JSON.parse(line.slice(6)) as { type: string; delta?: { text?: string } }
        if (ev.type === 'content_block_delta') reply += ev.delta?.text ?? ''
      }
      expect(reply).toBe(`You said: Write to ${email} today`)
      await waitFor(async () => {
        const rs = await catalog.api<Rc[]>(`/receipts?limit=10&key=${keyId}`)
        const redacted = rs.filter((r) => r.verdict === 'redacted')
        expect(redacted.length).toBe(2)
        for (const r of redacted) {
          expect(r).toMatchObject({ backend: name, status: 200, redactions: [{ type: 'email', count: 1, rehydrated: 1 }] })
          expect(r.outputTokens).toBeGreaterThan(0) // the stream's usage too
        }
      }, { timeout: 20_000, interval: 1000 })

      // OpenAI-style callers to the same provider still reach its OpenAI-compatible
      // endpoint (a chat completion, the key as a bearer token), redacted the same.
      const chat = await fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${k.secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'claude-echo', messages: [{ role: 'user', content: `Write to ${email} today` }] }),
      })
      const chatText = await chat.text()
      expect(chat.status, chatText).toBe(200)
      const completion = JSON.parse(chatText) as { id: string; object: string; choices: { message: { content: string } }[] }
      expect(completion.object).toBe('chat.completion')
      expect(completion.choices[0].message.content).toBe(`You said: Write to ${email} today`)
      expect(received(chat)).toBe('You said: Write to [EMAIL_1] today')
      await waitFor(async () => {
        const [r] = await catalog.api<Rc[]>(`/receipts?limit=1&key=${keyId}`)
        expect(r).toMatchObject({ backend: name, resolvedModel: 'claude-echo', status: 200, verdict: 'redacted' })
        expect(r.trace.find((t) => t.step === 'Route selected')?.outcome).not.toContain('Anthropic Messages API')
      }, { timeout: 20_000, interval: 1000 })
    } finally {
      if (ruleId) {
        const etagNow = async () => (await catalog.api<V[]>('/rules')).find((r) => r.id === ruleId)?.etag
        await send('POST', `/rules/${ruleId}/publish`, { mode: 'disabled' }, await etagNow()).catch(() => {})
        await send('DELETE', `/rules/${ruleId}`, undefined, await etagNow()).catch(() => {})
      }
      if (keyId) await send('POST', `/keys/${keyId}/revoke`, {}).catch(() => {})
      await dropBackends(name)
    }
  }, 300_000)

  it('lists provider keys by prefix and last test on Settings, not as not connected', async () => {
    window.history.pushState({}, '', '/settings')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    try {
      const providers = screen.getByRole('region', { name: 'Providers' })
      // Only rotation reminders are still unbuilt.
      expect(providers.textContent?.replace('Rotation reminders aren’t connected yet.', '')).not.toContain('connected yet')
      expect(providers.textContent).toContain('openrouter')
      expect(providers.textContent).toContain('OPENROUTER_API_KEY')
    } finally {
      r.unmount()
    }
  })

  it('drafts, publishes, enforces, rolls back and deletes a rule, with versions and audit rows', async () => {
    type V = { id: string; name: string; mode: string; version: number; failMode: string; etag: string; draft: { description: string } | null }
    type C = import('@/data/catalog').Change
    const gateway = (import.meta.env.VITE_STARGATE_GATEWAY as string | undefined) ?? 'http://localhost:1975'
    const keyName = `api-mode-rule-${Date.now().toString(36)}`
    const { key, secret } = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
      name: keyName, team: 'support', project: 'api-mode-test', allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
    })
    const call = async () => {
      const res = await fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5-mini', max_tokens: 20, messages: [{ role: 'user', content: 'hi' }] }),
      })
      const body = (await res.json().catch(() => null)) as { error?: { code?: string; message?: string } } | null
      return { status: res.status, code: body?.error?.code, message: body?.error?.message }
    }
    const latest = async () => (await catalog.api<C[]>('/changes'))[0]
    const name = keyName // one rule per test key, so the rule only ever matches this test's traffic
    const rule = { name, description: 'api-mode test', failMode: 'closed', when: [{ field: 'key', op: 'is', value: [keyName] }], then: [{ action: 'block' }] }
    let id = ''
    try {
      for (const bad of [
        { ...rule, name: 'Not A Slug' },
        { ...rule, when: [{ field: 'prompt', op: 'contains entity', value: ['passport'] }] },
        { ...rule, then: [{ action: 'redact' }] },
        { ...rule, then: [{ action: 'route to', detail: 'mars' }] },
      ]) expect(await status(send('POST', '/rules', bad))).toBe(400)

      const made = await send<V>('POST', '/rules', rule)
      id = made.id
      expect(made).toMatchObject({ mode: 'draft', version: 0, draft: { description: 'api-mode test' } })
      expect(await latest()).toMatchObject({ action: 'Created rule', target: name, targetKind: 'Policy' })
      expect(await status(send('POST', '/rules', rule))).toBe(409) // names are unique

      const dry = await send<{ dryRun: boolean; changes: { field: string; from: unknown; to: unknown }[]; replay: null; note: string }>('POST', `/rules/${id}/publish?dryRun=true`)
      expect(dry.changes).toEqual(expect.arrayContaining([{ field: 'mode', from: 'draft', to: 'monitor' }, { field: 'version', from: 0, to: 1 }]))
      expect(dry.replay).toBeNull()
      expect(dry.note).toMatch(/Replay .* isn't connected yet/)
      expect((await catalog.api<V[]>('/rules')).find((r) => r.id === id)).toMatchObject({ mode: 'draft', version: 0 })

      // First publish: monitor mode by default. Traffic isn't blocked.
      expect(await status(send('POST', `/rules/${id}/publish`))).toBe(428)
      const v1 = await send<V>('POST', `/rules/${id}/publish`, undefined, made.etag)
      expect(v1).toMatchObject({ mode: 'monitor', version: 1, draft: null })
      expect(await latest()).toMatchObject({ action: 'Published rule in monitor mode', target: `${name} v1` })
      // The control plane has Warden reload before the write returns.
      expect((await call()).code).not.toBe('policy_blocked')

      const v2 = await send<V>('POST', `/rules/${id}/publish`, { mode: 'enforce' }, v1.etag)
      expect(v2).toMatchObject({ mode: 'enforce', version: 2 })
      expect(await latest()).toMatchObject({ action: 'Published rule', target: `${name} v2` })
      expect(await call()).toMatchObject({ status: 403, code: 'policy_blocked', message: `Rule ${name} v2 blocks this request.` })

      // A draft edit doesn't touch the live version; a stale write is refused.
      const drafted = await send<V>('PUT', `/rules/${id}/draft`, { ...rule, description: 'edited' }, v2.etag)
      expect(drafted).toMatchObject({ mode: 'enforce', version: 2, draft: { description: 'edited' } })
      expect(await latest()).toMatchObject({ action: 'Edited rule draft', target: name })
      expect(await status(send('PUT', `/rules/${id}/draft`, rule, v2.etag))).toBe(409)

      const v3 = await send<V>('POST', `/rules/${id}/rollback`, { version: 1 }, drafted.etag)
      expect(v3).toMatchObject({ mode: 'monitor', version: 3, draft: { description: 'edited' } })
      expect(await latest()).toMatchObject({ action: 'Rolled back rule', target: `${name} v2 → v1 (as v3)` })
      const versions = await catalog.api<{ version: number; mode: string; publishedBy: string }[]>(`/rules/${id}/versions`)
      expect(versions.map((v) => [v.version, v.mode])).toEqual([[3, 'monitor'], [2, 'enforce'], [1, 'monitor']])
      expect(versions[0].publishedBy).toBe(catalog.session.actor.email)

      expect(await status(send('DELETE', `/rules/${id}`, undefined, v3.etag))).toBe(409) // still live: disable first
      const discarded = await send<V>('DELETE', `/rules/${id}/draft`, undefined, v3.etag)
      expect(await latest()).toMatchObject({ action: 'Discarded rule draft', target: name })
      const v4 = await send<V>('POST', `/rules/${id}/publish`, { mode: 'disabled' }, discarded.etag)
      expect(await latest()).toMatchObject({ action: 'Disabled rule', target: `${name} v4` })
      await send('DELETE', `/rules/${id}`, undefined, v4.etag)
      expect(await latest()).toMatchObject({ action: 'Deleted rule', target: name })
      expect((await catalog.api<V[]>('/rules')).some((r) => r.id === id)).toBe(false)
      expect((await catalog.api<unknown[]>(`/rules/${id}/versions`)).length).toBe(4) // history outlives the rule
      id = ''
    } finally {
      if (id) {
        const etagNow = async () => (await catalog.api<V[]>('/rules')).find((r) => r.id === id)?.etag
        await send('POST', `/rules/${id}/publish`, { mode: 'disabled' }, await etagNow()).catch(() => {})
        await send('DELETE', `/rules/${id}`, undefined, await etagNow()).catch(() => {})
      }
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 90_000)

  it('authors a rule in the Guardrails builder, publishes it, sees it block, merges a stale draft, rolls back and deletes it', async () => {
    type V = { id: string; name: string; mode: string; version: number; etag: string; when: unknown; then: unknown; draft: { description: string } | null }
    type C = import('@/data/catalog').Change
    const name = `api-mode-ui-rule-${Date.now().toString(36)}`
    const { key, secret } = await testKey(name)
    const call = async () => {
      const res = await fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5.5', max_tokens: 20, messages: [{ role: 'user', content: 'hi' }] }),
      })
      return ((await res.json().catch(() => null)) as { error?: { code?: string } } | null)?.error?.code
    }
    const mine = async () => (await catalog.api<V[]>('/rules')).find((r) => r.name === name)
    const latest = async () => (await catalog.api<C[]>('/changes'))[0]
    const settle = () => act(async () => { await new Promise((ok) => setTimeout(ok, 300)) })
    try {
      window.history.pushState({}, '', '/guardrails?rule=r1')
      render(<App />)
      await act(async () => {})
      // What the engine doesn't do is stated, not simulated.
      await screen.findByText(/Replay isn’t connected yet/)
      const builder = screen.getByRole('region', { name: 'Rule builder' })
      // r1 rehydrates on return, and Warden does it.
      await waitFor(() => expect(builder.textContent).toContain('Warden puts the values back in the response'), { timeout: 5000 })
      expect(within(builder).getByRole('switch', { name: 'Rehydrate on return' }).getAttribute('aria-checked')).toBe('true')
      expect(within(builder).queryByRole('button', { name: /Group/ })).toBeNull()
      expect(builder.textContent).toContain('Groups and “any of” aren’t connected yet')

      fireEvent.click(screen.getByRole('button', { name: 'New rule' }))
      await settle()
      fireEvent.change(within(builder).getByLabelText('Rule name'), { target: { value: name } })
      fireEvent.change(within(builder).getByLabelText('Description'), { target: { value: 'api-mode UI test' } })
      fireEvent.click(within(builder).getByRole('combobox', { name: 'Field' }))
      choose(await screen.findByRole('option', { name: 'Key' }))
      const value = within(builder).getByLabelText('Add Key value')
      fireEvent.change(value, { target: { value: name } })
      fireEvent.keyDown(value, { key: 'Enter' })
      expect(within(builder).getByRole('combobox', { name: 'Action' }).textContent).toContain('Block request')
      fireEvent.click(within(builder).getByRole('radio', { name: /Block \(fail-closed\)/ }))
      expect(await mine()).toBeUndefined()
      fireEvent.click(screen.getByRole('button', { name: 'Save draft' }))
      await waitFor(async () => expect(await mine()).toMatchObject({ mode: 'draft', version: 0, draft: { description: 'api-mode UI test' } }), { timeout: 5000 })
      expect(await latest()).toMatchObject({ action: 'Created rule', target: name })
      const made = (await mine())!
      expect(made.when).toEqual([{ field: 'key', op: 'is', value: [name] }])
      expect(made.then).toEqual([{ action: 'block', detail: '' }])

      // Publish shows the server's dry run first, then enforces.
      const publish = screen.getByRole('button', { name: 'Publish…' })
      await waitFor(() => expect(publish).toHaveProperty('disabled', false))
      fireEvent.click(publish)
      let dialog = await formDialog()
      await waitFor(() => expect(dialog.textContent).toMatch(/Replay against recorded traffic isn.t connected yet/), { timeout: 5000 })
      expect(dialog.textContent).toMatch(/Mode\s*draft\s*→\s*monitor/)
      fireEvent.click(within(dialog).getByRole('radio', { name: /^Enforce/ }))
      await waitFor(() => expect(dialog.textContent).toMatch(/Mode\s*draft\s*→\s*enforce/), { timeout: 5000 })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Publish and enforce' }))
      await formDialogClosed()
      expect(await mine()).toMatchObject({ mode: 'enforce', version: 1, draft: null })
      expect(await latest()).toMatchObject({ action: 'Published rule', target: `${name} v1` })
      expect(await call()).toBe('policy_blocked')

      // A mode change alone is a new version.
      await waitFor(() => expect(publish).toHaveProperty('disabled', false))
      fireEvent.click(publish)
      dialog = await formDialog()
      fireEvent.click(within(dialog).getByRole('radio', { name: /^Monitor/ }))
      await waitFor(() => expect(dialog.textContent).toMatch(/Mode\s*enforce\s*→\s*monitor/), { timeout: 5000 })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Publish in monitor mode' }))
      await formDialogClosed()
      expect(await mine()).toMatchObject({ mode: 'monitor', version: 2 })
      expect(await call()).not.toBe('policy_blocked')

      // Someone else saves a draft while this one is open: both are shown.
      await settle()
      fireEvent.change(within(builder).getByLabelText('Description'), { target: { value: 'mine' } })
      const v2 = (await mine())!
      await send('PUT', `/rules/${v2.id}/draft`, { name, description: 'theirs', failMode: 'closed', when: v2.when, then: v2.then }, v2.etag)
      fireEvent.click(screen.getByRole('button', { name: 'Save draft' }))
      const merge = (await screen.findByText('This rule changed since you opened it', {}, { timeout: 5000 })).closest<HTMLElement>('[data-slot="alert"]')!
      expect(merge.textContent).toContain('theirs')
      expect(merge.textContent).toContain('mine')
      fireEvent.click(within(merge).getByRole('button', { name: 'Save mine over theirs' }))
      await waitFor(async () => expect((await mine())?.draft?.description).toBe('mine'), { timeout: 5000 })
      expect(await latest()).toMatchObject({ action: 'Edited rule draft', target: name })
      await settle()
      fireEvent.click(screen.getByRole('button', { name: 'Discard draft' }))
      dialog = await formDialog()
      fireEvent.click(within(dialog).getByRole('button', { name: 'Discard draft' }))
      await formDialogClosed()
      expect((await mine())?.draft).toBeNull()
      expect(await latest()).toMatchObject({ action: 'Discarded rule draft', target: name })

      // Versions: the real history, and rollback publishes v1 again as v3.
      fireEvent.click(screen.getByRole('tab', { name: 'Versions' }))
      await settle()
      const history = await screen.findByRole('list', { name: `Versions of ${name}` }, { timeout: 5000 })
      await waitFor(() => expect(within(history).getAllByRole('button').length).toBe(2), { timeout: 5000 })
      fireEvent.click(within(history).getByRole('button', { name: /^v1/ }))
      fireEvent.click(await screen.findByRole('button', { name: 'Roll back to v1' }))
      dialog = await formDialog()
      fireEvent.click(within(dialog).getByRole('button', { name: 'Roll back to v1' }))
      await formDialogClosed()
      expect(await mine()).toMatchObject({ mode: 'enforce', version: 3 })
      expect(await latest()).toMatchObject({ action: 'Rolled back rule', target: `${name} v2 → v1 (as v3)` })
      expect(await call()).toBe('policy_blocked')
      await waitFor(() => expect(within(history).getAllByRole('button').length).toBe(3), { timeout: 5000 })

      // Only a rule that isn't live can be deleted: disable it, then delete.
      fireEvent.click(screen.getByRole('tab', { name: 'Rules' }))
      await settle()
      // A live rule can't be deleted, and the page says why before anyone clicks.
      expect(screen.getByRole('button', { name: 'Delete rule' })).toHaveProperty('disabled', true)
      expect(screen.getByRole('region', { name: 'Rule builder' }).textContent).toContain('To delete it, disable it first')
      fireEvent.click(screen.getByRole('button', { name: 'Publish…' }))
      dialog = await formDialog()
      fireEvent.click(within(dialog).getByRole('radio', { name: /^Disabled/ }))
      await waitFor(() => expect(dialog.textContent).toMatch(/Mode\s*enforce\s*→\s*disabled/), { timeout: 5000 })
      fireEvent.click(within(dialog).getByRole('button', { name: 'Disable rule' }))
      await formDialogClosed()
      expect(await mine()).toMatchObject({ mode: 'disabled', version: 4 })
      fireEvent.click(await screen.findByRole('button', { name: 'Delete rule' }))
      dialog = await formDialog()
      fireEvent.click(within(dialog).getByRole('button', { name: 'Delete rule' }))
      await formDialogClosed()
      expect(await mine()).toBeUndefined()
      expect(await latest()).toMatchObject({ action: 'Deleted rule', target: name })
      await waitFor(() => expect(screen.getByRole('navigation', { name: 'Rules' }).textContent).not.toContain(name), { timeout: 5000 })
    } finally {
      const r = await mine()
      if (r) {
        const etagNow = async () => (await mine())?.etag
        if (r.version > 0) await send('POST', `/rules/${r.id}/publish`, { mode: 'disabled' }, r.etag).catch(() => {})
        await send('DELETE', `/rules/${r.id}`, undefined, await etagNow()).catch(() => {})
      }
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 120_000)

  // §4.5 step 5 through the real gateway. fake-openai echoes the prompt it
  // received (X-Fake-Echo) in the reply and in X-Fake-Received, which Warden
  // doesn't touch: the provider sees the placeholder, the caller gets the
  // address back, in a JSON reply and a streamed one (four-character chunks,
  // so the placeholder spans events). Only a rule that says so rehydrates.
  it('rehydrates redacted values in the response through the gateway, JSON and streamed, when the rule says so', async () => {
    type V = { id: string; mode: string; version: number; etag: string }
    type R = { redactions: { type: string; count: number; rehydrated?: number }[]; trace: { step: string; outcome: string }[] }
    const name = `api-mode-rehydrate-${Date.now().toString(36)}`
    // The security team: seeded no-pii-out (r1) skips it, so this test's rule is the one that redacts.
    const { key, secret } = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
      name, team: 'security', project: 'api-mode-test', allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
    })
    const email = 'jordan.lee@example.com'
    const ask = async (stream: boolean) => {
      const res = await fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json', 'X-Fake-Echo': '1' },
        body: JSON.stringify({ model: 'gpt-5-mini', stream, messages: [{ role: 'user', content: `Write to ${email} today` }] }),
      })
      const text = await res.text()
      expect(res.status, text).toBe(200)
      const received = decodeURIComponent((res.headers.get('x-fake-received') ?? '').replace(/\+/g, ' '))
      let reply = ''
      if (stream) {
        for (const line of text.split('\n')) {
          if (!line.startsWith('data: ') || line.trim() === 'data: [DONE]') continue
          reply += (JSON.parse(line.slice(6)) as { choices: { delta: { content?: string } }[] }).choices[0]?.delta.content ?? ''
        }
      } else reply = (JSON.parse(text) as { choices: { message: { content: string } }[] }).choices[0].message.content
      return { received, reply }
    }
    const rule = (rehydrate: boolean) => ({
      name, description: 'api-mode rehydration test', failMode: 'closed',
      when: [{ field: 'prompt', op: 'contains entity', value: ['email'] }, { field: 'key', op: 'is', value: [name] }],
      then: [{ action: 'redact', detail: rehydrate ? 'email · rehydrate on return' : 'email' }],
    })
    let id = ''
    try {
      const made = await send<V>('POST', '/rules', rule(true))
      id = made.id
      const v1 = await send<V>('POST', `/rules/${id}/publish`, { mode: 'enforce' }, made.etag)
      expect(v1).toMatchObject({ mode: 'enforce', version: 1 })
      for (const stream of [false, true]) {
        const { received, reply } = await ask(stream)
        expect(received).toBe('You said: Write to [EMAIL_1] today') // the provider never saw the address
        expect(reply).toBe(`You said: Write to ${email} today`) // the caller gets it back
      }
      // Each receipt counts what came back, after the upstream call.
      await waitFor(async () => {
        const rs = await catalog.api<R[]>(`/receipts?limit=10&key=${key.id}`)
        expect(rs.length).toBe(2)
        for (const r of rs) {
          expect(r.redactions).toEqual([{ type: 'email', count: 1, rehydrated: 1 }])
          expect(r.trace.at(-1)).toMatchObject({ step: 'Placeholders rehydrated', outcome: 'restored 1 email' })
        }
      }, { timeout: 15_000, interval: 1000 })

      // Without "rehydrate on return" the placeholder stays in the reply.
      const drafted = await send<V>('PUT', `/rules/${id}/draft`, rule(false), v1.etag)
      const v2 = await send<V>('POST', `/rules/${id}/publish`, undefined, drafted.etag)
      expect(v2).toMatchObject({ mode: 'enforce', version: 2 })
      for (const stream of [false, true]) {
        const { received, reply } = await ask(stream)
        expect(received).toBe('You said: Write to [EMAIL_1] today')
        expect(reply).toBe('You said: Write to [EMAIL_1] today')
      }

      const v3 = await send<V>('POST', `/rules/${id}/publish`, { mode: 'disabled' }, v2.etag)
      await send('DELETE', `/rules/${id}`, undefined, v3.etag)
      id = ''
    } finally {
      if (id) {
        const etagNow = async () => (await catalog.api<V[]>('/rules')).find((r) => r.id === id)?.etag
        await send('POST', `/rules/${id}/publish`, { mode: 'disabled' }, await etagNow()).catch(() => {})
        await send('DELETE', `/rules/${id}`, undefined, await etagNow()).catch(() => {})
      }
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 90_000)

  it('reorders rules on Guardrails against the order the author saw, with an audit row', async () => {
    type R = { id: string; name: string; ordinal: number }
    type C = import('@/data/catalog').Change
    const before = await catalog.api<R[]>('/rules')
    expect(before.length).toBeGreaterThan(1)
    const ids = before.map((r) => r.id)
    const swapped = [ids[1], ids[0], ...ids.slice(2)]
    expect(await status(send('PUT', '/rules/order', { from: swapped, to: ids }))).toBe(409) // not the order now
    expect(await status(send('PUT', '/rules/order', { from: ids, to: ids.slice(1) }))).toBe(400)
    expect(await status(send('PUT', '/rules/order', { from: ids, to: ids }))).toBe(400)

    window.history.pushState({}, '', '/guardrails')
    const r = render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 400))
    })
    let moved = false
    try {
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: `Move ${before[0].name} down` }))
        await new Promise((ok) => setTimeout(ok, 400))
      })
      moved = true
      expect((await catalog.api<R[]>('/rules')).map((x) => x.id)).toEqual(swapped)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({
        action: 'Reordered rules', targetKind: 'Policy', target: `${before[1].name} 2 → 1, ${before[0].name} 1 → 2`,
      })
      expect(screen.getByRole('button', { name: `Move ${before[0].name} up` })).toBeTruthy()
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: `Move ${before[0].name} up` }))
        await new Promise((ok) => setTimeout(ok, 400))
      })
      moved = false
      expect((await catalog.api<R[]>('/rules')).map((x) => x.id)).toEqual(ids)
    } finally {
      if (moved) await send('PUT', '/rules/order', { from: swapped, to: ids })
      r.unmount()
    }
  }, 30_000)

  it('counts real detector hits from receipts on Detectors, with no thresholds or fixtures', async () => {
    type D = { entity: string; kind: string; pattern: string; custom: boolean; usedBy: { rule: string; mode: string; action: string }[]; redactedRequests24h: number; blocked24h: number }
    type V = { id: string; etag: string; version: number }
    const name = `api-mode-detect-${Date.now().toString(36)}`
    const { key, secret } = await testKey(name)
    const call = (content: string) =>
      fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5.5', max_tokens: 20, messages: [{ role: 'user', content }] }),
      }).then((r) => r.status)
    const detectors = async () => Object.fromEntries((await catalog.api<D[]>('/detectors')).map((d) => [d.entity, d]))
    const made: V[] = []
    const rule = async (suffix: string, entity: string, action: string) => {
      const r = await send<V>('POST', '/rules', {
        name: `${name}-${suffix}`, description: 'api-mode test', failMode: 'closed',
        when: [{ field: 'key', op: 'is', value: [name] }, { field: 'prompt', op: 'contains entity', value: [entity] }],
        then: [{ action, detail: action === 'redact' ? entity : '' }],
      })
      made.push(await send<V>('POST', `/rules/${r.id}/publish`, { mode: 'enforce' }, r.etag))
    }
    try {
      await rule('redact', 'email', 'redact')
      await rule('block', 'secret', 'block')
      const before = await detectors()
      // The built-ins; custom entities (another test's, say) are listed too, marked custom.
      expect(Object.values(before).filter((d) => !d.custom).map((d) => d.entity).sort()).toEqual(['Acme account ID', 'SSN', 'credit card', 'email', 'phone', 'private key', 'secret', 'source code'])
      expect(before['credit card'].kind).toBe('regex + Luhn check')
      expect(before.email.usedBy).toContainEqual({ rule: `${name}-redact`, version: 1, mode: 'enforce', action: 'redact' })

      expect(await call('write to jane.doe@example.com today')).toBe(200)
      expect(await call(`my key is sk-${'a'.repeat(24)}`)).toBe(403)
      // The 24h totals move with background traffic and age out, so check this
      // test's own receipts carry what the counts read, and that they're counted.
      type R = { id: string; verdict: string; redactions: { type: string; count: number }[]; errorCode?: string; errorDetail?: string }
      let mine: R[] = []
      for (let i = 0; i < 30 && mine.length < 2; i++) {
        mine = await catalog.api<R[]>(`/receipts?limit=5&key=${key.id}`)
        if (mine.length < 2) await new Promise((ok) => setTimeout(ok, 500))
      }
      expect(mine.find((r) => r.verdict === 'redacted')?.redactions).toEqual([{ type: 'email', count: 1 }])
      expect(mine.find((r) => r.verdict === 'blocked')).toMatchObject({ errorCode: 'policy_blocked', errorDetail: expect.stringContaining('matched entity "secret"') })
      const after = await detectors()
      expect(after.email.redactedRequests24h).toBeGreaterThan(0)
      expect(after.secret.blocked24h).toBeGreaterThan(0)

      window.history.pushState({}, '', '/guardrails')
      render(<App />)
      await act(async () => {})
      fireEvent.click(screen.getByRole('tab', { name: 'Detectors' }))
      const table = await screen.findByRole('table', { name: 'Detectors' }, { timeout: 5000 })
      await waitFor(() => expect(table.textContent).toContain(`${name}-redact`), { timeout: 5000 })
      const email = within(table).getByRole('row', { name: /^email/ })
      expect(email.textContent).toContain('[EMAIL_1]')
      const page = document.body.textContent!
      // No made-up numbers or controls: no thresholds, no fixture queue, no browser-only regex tester.
      expect(screen.queryAllByRole('slider')).toHaveLength(0)
      expect(page).not.toContain('Person name')
      expect(page).not.toContain('dana@acme.dev')
      expect(page).toContain('Monitor-mode matches aren’t counted')
      // Custom entities and false-positive review are connected now.
      expect(page).not.toContain('aren’t connected yet')
      expect(page).not.toContain('isn’t connected yet')
      expect(page).toContain('Receipts keep hashes, not prompts')
      // This test's two hits are waiting for review, newest first.
      const queue = await screen.findByRole('table', { name: 'Detector hits to review' }, { timeout: 5000 })
      const redactedId = mine.find((r) => r.verdict === 'redacted')!.id
      const blockedId = mine.find((r) => r.verdict === 'blocked')!.id
      expect(within(queue).getByRole('button', { name: `Mark email on ${redactedId} a false positive` })).toBeTruthy()
      expect(within(queue).getByRole('button', { name: `Mark secret on ${blockedId} correct` })).toBeTruthy()
    } finally {
      for (const r of made) {
        const now = (await catalog.api<V[]>('/rules')).find((x) => x.id === r.id)
        if (!now) continue
        const off = await send<V>('POST', `/rules/${r.id}/publish`, { mode: 'disabled' }, now.etag).catch(() => now)
        await send('DELETE', `/rules/${r.id}`, undefined, off.etag).catch(() => {})
      }
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 60_000)

  it('adds a custom entity on Detectors that a rule redacts through the gateway, and reviews its hit as a false positive, with audit rows', async () => {
    type D = { entity: string; kind: string; custom: boolean; placeholder: string; usedBy: { rule: string }[]; redactedRequests24h: number; falsePositives30d: number; confirmed30d: number; customEntity?: E }
    type E = { id: string; name: string; pattern: string; label: string; etag: string }
    type V = { id: string; etag: string }
    type H = { receiptId: string; entity: string; action: string; count: number; rules: string[]; etag: string; verdict: { verdict: string; by: string } | null }
    type C = import('@/data/catalog').Change
    const stamp = Date.now().toString(36)
    const name = `api-mode-entity-${stamp}`
    const entity = `badge ${stamp}`
    const label = `BADGE_${stamp.toUpperCase()}`
    const pattern = `\\bBDG-${stamp}-\\d{4}\\b`
    const badge = `BDG-${stamp}-1234`
    const detector = async () => (await catalog.api<D[]>('/detectors')).find((d) => d.entity === entity)

    // Patterns are checked on the server: RE2 only, never empty, not a built-in's name, and the examples must hold.
    const bad = { name: `${entity} x`, pattern, label: `${label}X` }
    expect(await status(send('POST', '/entities', { ...bad, pattern: '[a-z]*' }))).toBe(400) // matches empty text
    expect(await status(send('POST', '/entities', { ...bad, pattern: '(\\w+)\\1' }))).toBe(400) // a backreference isn't RE2
    expect(await status(send('POST', '/entities', { ...bad, pattern: 'x'.repeat(513) }))).toBe(400)
    expect(await status(send('POST', '/entities', { ...bad, name: 'Email' }))).toBe(400) // a built-in's name
    expect(await status(send('POST', '/entities', { ...bad, label: 'EMAIL' }))).toBe(400) // a built-in's placeholder
    expect(await status(send('POST', '/entities', { ...bad, mustMatch: ['no badge here'] }))).toBe(400)
    expect(await status(send('POST', '/entities', { ...bad, mustNotMatch: [badge] }))).toBe(400)
    // A dry run tries it on a sample with the engine's own regex, and saves nothing.
    const dry = await send<{ matches: string[]; redacted: string }>('POST', '/entities?dryRun=true', { name: entity, pattern, label, sample: `badge ${badge} at gate 4` })
    expect(dry).toMatchObject({ matches: [badge], redacted: `badge [${label}_1] at gate 4` })
    expect(await detector()).toBeUndefined()

    let ruleId = ''
    let keyId = ''
    try {
      // Add it on Guardrails → Detectors.
      window.history.pushState({}, '', '/guardrails')
      render(<App />)
      await act(async () => {})
      fireEvent.click(screen.getByRole('tab', { name: 'Detectors' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Add entity' }, { timeout: 5000 }))
      const form = screen.getByRole('form', { name: 'New custom entity' })
      fireEvent.change(within(form).getByLabelText('Name'), { target: { value: entity } })
      fireEvent.change(within(form).getByLabelText('Placeholder label'), { target: { value: label } })
      fireEvent.change(within(form).getByLabelText('Pattern'), { target: { value: pattern } })
      fireEvent.change(within(form).getByLabelText('Must match'), { target: { value: `badge ${badge}` } })
      fireEvent.change(within(form).getByLabelText('Must not match'), { target: { value: `BDG-${stamp}-12\nXBDG-${stamp}-1234` } })
      fireEvent.change(within(form).getByLabelText('Try it on'), { target: { value: `see ${badge}` } })
      fireEvent.click(within(form).getByRole('button', { name: 'Test' }))
      await waitFor(() => expect(within(form).getByLabelText('Pattern test').textContent).toContain(`see [${label}_1]`), { timeout: 5000 })
      fireEvent.click(within(form).getByRole('button', { name: 'Add entity' }))
      const table = await screen.findByRole('table', { name: 'Detectors' })
      await waitFor(() => expect(table.textContent).toContain(`[${label}_1]`), { timeout: 5000 })
      cleanup()

      let made = (await detector())!
      expect(made).toMatchObject({ kind: 'custom regex', custom: true, placeholder: `[${label}_1]`, customEntity: { name: entity, pattern, label } })
      expect((await catalog.api<C[]>('/changes?kind=Detector&limit=1'))[0]).toMatchObject({ action: 'Added custom entity', target: entity, actor: catalog.session.actor.email })
      // Rules can name it: the builder's vocabulary has it, and the rule API takes it.
      expect((await catalog.api<{ entities: string[] }>('/rules/vocabulary')).entities).toContain(entity)
      expect(await status(send('POST', '/entities', { name: entity.toUpperCase(), pattern, label: `${label}Y` }))).toBe(400) // the name is taken, ignoring case

      const { key, secret } = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
        name, team: 'support', project: 'api-mode-test', allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
      })
      keyId = key.id
      const r = await send<V>('POST', '/rules', {
        name, description: 'api-mode custom entity test', failMode: 'closed',
        when: [{ field: 'key', op: 'is', value: [name] }, { field: 'prompt', op: 'contains entity', value: [entity] }],
        then: [{ action: 'redact', detail: entity }],
      })
      ruleId = r.id
      await send<V>('POST', `/rules/${r.id}/publish`, { mode: 'enforce' }, r.etag)

      // Warden reloaded on the writes: the provider sees the placeholder, not the badge.
      const res = await fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json', 'X-Fake-Echo': '1' },
        body: JSON.stringify({ model: 'gpt-5-mini', messages: [{ role: 'user', content: `Badge ${badge} was used at gate 4` }] }),
      })
      expect(res.status, await res.clone().text()).toBe(200)
      expect(decodeURIComponent((res.headers.get('x-fake-received') ?? '').replace(/\+/g, ' '))).toBe(`You said: Badge [${label}_1] was used at gate 4`)
      type R = { id: string; redactions: { type: string; count: number }[] }
      let receipt: R | undefined
      await waitFor(async () => {
        receipt = (await catalog.api<R[]>(`/receipts?limit=5&key=${key.id}`))[0]
        expect(receipt?.redactions).toEqual([{ type: entity, count: 1 }])
      }, { timeout: 15_000, interval: 1000 })
      made = (await detector())!
      expect(made.redactedRequests24h).toBe(1)
      expect(made.usedBy).toContainEqual(expect.objectContaining({ rule: name }))

      // Edits need If-Match, can't rename, and a stale one is refused; a rule naming it blocks delete.
      const ce = made.customEntity!
      expect(await status(send('PUT', `/entities/${ce.id}`, { name: entity, pattern, label }))).toBe(428)
      expect(await status(send('PUT', `/entities/${ce.id}`, { name: `${entity} 2`, pattern, label }, ce.etag))).toBe(400)
      const edited = await send<E>('PUT', `/entities/${ce.id}`, { name: entity, pattern, label, mustMatch: [badge] }, ce.etag)
      expect(edited.etag).not.toBe(ce.etag)
      expect((await catalog.api<C[]>('/changes?kind=Detector&limit=1'))[0]).toMatchObject({ action: 'Changed custom entity', target: entity })
      expect(await status(send('PUT', `/entities/${ce.id}`, { name: entity, pattern, label }, ce.etag))).toBe(409)
      await expect(send('DELETE', `/entities/${ce.id}`, undefined, edited.etag)).rejects.toMatchObject({ status: 409, message: expect.stringContaining(name) })

      // The hit waits for review on Detectors; mark it a false positive there.
      const hits = await catalog.api<H[]>(`/detectors/hits?entity=${encodeURIComponent(entity)}`)
      expect(hits).toEqual([expect.objectContaining({ receiptId: receipt!.id, entity, action: 'redacted', count: 1, rules: [`${name} v1`], verdict: null })])
      window.history.pushState({}, '', '/guardrails')
      render(<App />)
      await act(async () => {})
      fireEvent.click(screen.getByRole('tab', { name: 'Detectors' }))
      expect(document.body.textContent).toContain('the text a detector matched is never stored')
      const queue = await screen.findByRole('table', { name: 'Detector hits to review' }, { timeout: 5000 })
      fireEvent.click(await within(queue).findByRole('button', { name: `Mark ${entity} on ${receipt!.id} a false positive` }, { timeout: 5000 }))
      await waitFor(async () => expect((await detector())?.falsePositives30d).toBe(1), { timeout: 5000 })
      const detectorsTable = screen.getByRole('table', { name: 'Detectors' })
      await waitFor(() => expect(within(detectorsTable).getByRole('row', { name: new RegExp(`^${entity}`) }).textContent).toContain('of 1 reviewed'), { timeout: 5000 })
      // It leaves the unreviewed queue.
      await waitFor(() => expect(within(queue).queryByRole('button', { name: `Mark ${entity} on ${receipt!.id} a false positive` })).toBeNull(), { timeout: 5000 })
      cleanup()
      expect((await catalog.api<C[]>('/changes?kind=Review&limit=1'))[0]).toMatchObject({
        action: 'Marked detector hit a false positive', target: `${entity} on receipt ${receipt!.id}`, actor: catalog.session.actor.email,
      })

      // A verdict is checked against the review state the reviewer saw: the old one is stale now.
      expect(await status(send('POST', '/detectors/hits/verdict', { receiptId: receipt!.id, entity, verdict: 'confirmed' }, hits[0].etag))).toBe(409)
      expect(await status(send('POST', '/detectors/hits/verdict', { receiptId: receipt!.id, entity, verdict: 'confirmed' }))).toBe(428)
      // Only a hit the receipt records can be judged.
      expect(await status(send('POST', '/detectors/hits/verdict', { receiptId: receipt!.id, entity: 'SSN', verdict: 'confirmed' }, hits[0].etag))).toBe(400)
      const [reviewed] = await catalog.api<H[]>(`/detectors/hits?review=all&entity=${encodeURIComponent(entity)}`)
      expect(reviewed.verdict).toMatchObject({ verdict: 'false_positive', by: catalog.session.actor.email })
      const changed = await send<H>('POST', '/detectors/hits/verdict', { receiptId: receipt!.id, entity, verdict: 'confirmed' }, reviewed.etag)
      expect(changed.verdict).toMatchObject({ verdict: 'confirmed' })
      expect(await detector()).toMatchObject({ falsePositives30d: 0, confirmed30d: 1 })
      expect((await catalog.api<C[]>('/changes?kind=Review&limit=1'))[0]).toMatchObject({ action: 'Confirmed detector hit', target: `${entity} on receipt ${receipt!.id}` })

      // Once no rule names it, it can be deleted, with an audit row; rules can't name it after.
      const live = (await catalog.api<V[]>('/rules')).find((x) => x.id === ruleId)!
      const off = await send<V>('POST', `/rules/${ruleId}/publish`, { mode: 'disabled' }, live.etag)
      await send('DELETE', `/rules/${ruleId}`, undefined, off.etag)
      ruleId = ''
      await send('DELETE', `/entities/${ce.id}`, undefined, edited.etag)
      expect((await catalog.api<C[]>('/changes?kind=Detector&limit=1'))[0]).toMatchObject({ action: 'Deleted custom entity', target: entity })
      expect(await detector()).toBeUndefined()
      expect((await catalog.api<{ entities: string[] }>('/rules/vocabulary')).entities).not.toContain(entity)
    } finally {
      if (ruleId) {
        const etagNow = async () => (await catalog.api<V[]>('/rules')).find((x) => x.id === ruleId)?.etag
        await send('POST', `/rules/${ruleId}/publish`, { mode: 'disabled' }, await etagNow()).catch(() => {})
        await send('DELETE', `/rules/${ruleId}`, undefined, await etagNow()).catch(() => {})
      }
      const left = (await detector())?.customEntity
      if (left) await send('DELETE', `/entities/${left.id}`, undefined, left.etag).catch(() => {})
      if (keyId) await send('POST', `/keys/${keyId}/revoke`).catch(() => {})
    }
  }, 90_000)

  it('syncs prices from LiteLLM per (model, backend), with audit rows', async () => {
    type P = import('@/data/catalog').PricingView
    type C = import('@/data/catalog').Change
    const after = await send<P>('POST', '/pricing/sync')
    expect(after.sync.error ?? '').toBe('')
    expect(Date.now() - after.sync.lastOkAt).toBeLessThan(60_000)
    const pair = (m: string, b: string) => after.prices.find((p) => p.model === m && p.backend === b)!
    // A mapped pair follows LiteLLM (unless someone overrode a rate).
    const mini = pair('gpt-5-mini', 'openai-prod')
    expect(mini.litellmKey).toBe('gpt-5-mini')
    expect(mini.priced).toBe(true)
    expect(Object.values(mini.rates).some((r) => r?.source === 'litellm')).toBe(true)
    expect(Object.values(mini.rates).some((r) => r?.source === 'seed')).toBe(false)
    // The same model is priced separately on each backend.
    expect(pair('claude-sonnet-5', 'bedrock-eu').litellmKey).toBe('eu.anthropic.claude-sonnet-5')
    expect(pair('claude-sonnet-5', 'anthropic-prod').litellmKey).toBe('claude-sonnet-5')
    // No LiteLLM entry: the seed price is retired, so it's no price rather than a made-up one.
    for (const [m, b] of [['llama-3.3-70b', 'vllm-internal'], ['claude-opus-4-1', 'anthropic-prod']]) {
      const p = pair(m, b)
      expect(p.litellmKey ?? '').toBe('')
      expect(Object.values(p.rates).some((r) => r?.source === 'seed')).toBe(false)
    }
    // The sync audits as itself. Filtered by kind: the test db's audit log keeps every run's rows.
    expect((await catalog.api<C[]>('/changes?limit=5&kind=Pricing')).every((c) => c.targetKind === 'Pricing')).toBe(true)
    const synced = (await catalog.api<C[]>('/changes?limit=500&kind=Pricing')).filter((c) => c.actor === 'LiteLLM sync')
    expect(synced.length).toBeGreaterThan(0)
    expect(synced.some((c) => c.action === 'Retired model price' && /^llama-3\.3-70b on vllm-internal /.test(c.target))).toBe(true)
    // Spend says how many requests it leaves out for having no price.
    expect(typeof (await catalog.api<{ unpriced: number }>('/spend?range=24h')).unpriced).toBe('number')
  }, 60_000)

  it('overrides one rate, then follows LiteLLM again, with If-Match and audit rows', async () => {
    type P = import('@/data/catalog').PricingView
    type C = import('@/data/catalog').Change
    const [m, b] = ['gpt-5-mini', 'openai-prod']
    const path = `/pricing/${m}/${b}`
    const get = async () => (await catalog.api<P>('/pricing')).prices.find((p) => p.model === m && p.backend === b)!
    const cur = await get()
    expect(cur.rates.input?.source).toBe('litellm')
    const lite = cur.rates.input!.perM
    const output = cur.rates.output!

    expect(await status(send('POST', path, { rates: { input: lite + 0.01 } }))).toBe(428)
    expect(await status(send('POST', path, { rates: { input: lite + 0.01 } }, '"stale"'))).toBe(409)
    expect(await status(send('POST', '/pricing/gpt-5.5/bedrock-eu', { rates: { input: 1 } }, cur.etag))).toBe(404) // not a pair a backend serves
    expect(await status(send('POST', path, { rates: { wholesale: 1 } }, cur.etag))).toBe(400)
    expect(await status(send('POST', path, { rates: { input: -1 } }, cur.etag))).toBe(400)

    let overridden = false
    try {
      const after = await send<P>('POST', path, { rates: { input: lite + 0.01 } }, cur.etag)
      overridden = true
      const p = after.prices.find((x) => x.model === m && x.backend === b)!
      expect(p.rates.input).toEqual({ perM: +(lite + 0.01).toFixed(6), source: 'manual' })
      expect(p.rates.output).toEqual(output) // the other rates keep following LiteLLM
      const row = (await catalog.api<C[]>('/changes'))[0]
      expect(row).toMatchObject({ action: 'Changed model price', targetKind: 'Pricing', actor: 'dev@localhost' })
      expect(row.target).toMatch(new RegExp(`^${m} on ${b} input \\$[\\d.]+ → \\$[\\d.]+ per 1M from `))
      // A sync leaves the override alone.
      const synced = await send<P>('POST', '/pricing/sync')
      expect(synced.prices.find((x) => x.model === m && x.backend === b)!.rates.input?.source).toBe('manual')
    } finally {
      if (overridden) {
        const back = await send<P>('POST', path, { rates: { input: null } }, (await get()).etag)
        expect(back.prices.find((x) => x.model === m && x.backend === b)!.rates.input).toEqual({ perM: lite, source: 'litellm' })
        expect((await catalog.api<C[]>('/changes'))[0].target).toMatch(new RegExp(`^${m} on ${b} input \\$[\\d.]+ → \\$${lite} per 1M from `))
      }
    }
  }, 60_000)

  it('schedules and cancels an effective-dated price change on one backend', async () => {
    type P = import('@/data/catalog').PricingView
    type C = import('@/data/catalog').Change
    const [m, b] = ['gpt-5-mini', 'azure-openai-eu']
    const path = `/pricing/${m}/${b}`
    const pair = (v: P) => v.prices.find((p) => p.model === m && p.backend === b)!
    const before = await catalog.api<P>('/pricing')
    const at = '2099-01-01T00:00:00Z'
    let scheduled: number | undefined
    try {
      const after = await send<P>('POST', path, { rates: { input: 9.99 }, effectiveFrom: at }, pair(before).etag)
      const change = after.changes.find((c) => c.model === m && c.backend === b && c.field === 'Input' && c.scheduled)
      expect(change).toMatchObject({ to: 9.99, source: 'manual', effectiveAt: Date.parse(at), scheduled: true })
      scheduled = change!.effectiveAt
      expect(pair(after).rates).toEqual(pair(before).rates) // today's price is untouched
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Scheduled model price change', targetKind: 'Pricing' })
      expect(await status(send('POST', path, { rates: { input: 1 }, effectiveFrom: '2098-06-01T00:00:00Z' }, pair(after).etag))).toBe(400) // before the scheduled one
      expect(await status(send('POST', path, { rates: { input: 1 }, effectiveFrom: '2020-01-01T00:00:00Z' }, pair(after).etag))).toBe(400) // backdated
    } finally {
      if (scheduled) await send('DELETE', `${path}/${scheduled}`)
    }
    expect((await catalog.api<P>('/pricing')).changes.some((c) => c.model === m && c.backend === b && c.scheduled)).toBe(false)
    expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Cancelled model price change', target: `${m} on ${b} change from 2099-01-01 00:00 UTC` })
    expect(await status(send('DELETE', `${path}/${Date.parse(at)}`))).toBe(404)
  })

  it('points a pair at a LiteLLM key only if the file has it', async () => {
    expect(await status(send('PUT', '/pricing/claude-opus-4-1/anthropic-prod/source', { litellmKey: 'no-such-key' }))).toBe(400)
    expect(await status(send('PUT', '/pricing/gpt-5.5/bedrock-eu/source', { litellmKey: 'gpt-5.5' }))).toBe(404)
  }, 60_000)

  it('flips Warden’s kill switch through the control plane, with audit rows', async () => {
    type S = import('@/data/catalog').Session
    type C = import('@/data/catalog').Change
    const passthrough = async () => (await catalog.api<S>('/session')).warden?.passthrough
    const post = (on: boolean) => catalog.api<{ passthrough: boolean }>('/warden/passthrough', { method: 'POST', body: JSON.stringify({ on }) })
    expect(await passthrough()).toBe(false)
    try {
      window.history.pushState({}, '', '/settings')
      render(<App />)
      await act(async () => {
        await new Promise((ok) => setTimeout(ok, 500))
      })
      fireEvent.click(screen.getByRole('switch', { name: 'Warden pass-through' }))
      const phrase = `pass-through ${catalog.session.environment}`
      fireEvent.change(screen.getByLabelText(/to confirm/), { target: { value: phrase } })
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: /Turn on pass-through/ }))
        await new Promise((ok) => setTimeout(ok, 500))
      })
      expect(await passthrough()).toBe(true)
      expect(document.body.textContent).toContain('Warden is in pass-through.')

      const [on] = await catalog.api<C[]>('/changes')
      expect(on.action).toBe('Turned on Warden pass-through')
      expect(on.actor).toBe(catalog.session.actor.email)
      expect(Date.now() - on.ts).toBeLessThan(60_000)

      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Resume policing' }))
        await new Promise((ok) => setTimeout(ok, 500))
      })
      expect(await passthrough()).toBe(false)
      const [off] = await catalog.api<C[]>('/changes')
      expect(off.action).toBe('Turned off Warden pass-through')

      // Asking for the state Warden is already in changes nothing and writes no row.
      expect((await post(false)).passthrough).toBe(false)
      expect((await catalog.api<C[]>('/changes'))[0].id).toBe(off.id)
    } finally {
      if (await passthrough()) await post(false)
    }
  })

  type PricedReceipt = {
    id: string
    status: number
    ts: number
    costUsd: number | null
    costBasis?: { effectiveFrom: number; inPerM: number | null; pricedLater?: boolean }
    outputTokens: number
    reasoningTokens: number
  }
  /** One echoed chat request through the gateway; X-Fake-Echo makes the fake upstream answer deterministically. */
  const echo = async (secret: string, model: string, headers: Record<string, string> = {}) => {
    const res = await fetch(`${gateway}/v1/chat/completions`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json', 'X-Fake-Echo': '1', ...headers },
      body: JSON.stringify({ model, messages: [{ role: 'user', content: 'hi' }] }),
    })
    const text = await res.text()
    expect(res.status, text).toBe(200)
    return JSON.parse(text) as { usage: { completion_tokens: number; completion_tokens_details?: { reasoning_tokens: number } } }
  }
  const receiptsOf = (keyId: string, n: number) =>
    waitFor(
      async () => {
        const rs = await catalog.api<PricedReceipt[]>(`/receipts?limit=${n}&key=${keyId}`)
        expect(rs.length).toBe(n)
        return rs // newest first
      },
      { timeout: 15_000, interval: 1000 },
    )

  // §5.1: a receipt carries the price in effect when its request started. A
  // change scheduled a few seconds out splits two requests either side of it,
  // the second sent within the 5 seconds a config snapshot used to lag.
  it('prices each receipt at the rate in effect when its request started', async () => {
    type P = import('@/data/catalog').PricingView
    const [m, b] = ['gpt-5-mini', 'openai-prod']
    const path = `/pricing/${m}/${b}`
    const get = async () => (await catalog.api<P>('/pricing')).prices.find((p) => p.model === m && p.backend === b)!
    const cur = await get()
    const was = cur.rates.input!.perM
    const next = +(was + 1).toFixed(6)
    const { key, secret } = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
      name: `api-mode-price-at-${Date.now().toString(36)}`, team: 'support', project: 'api-mode-test', allowedModels: [m], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
    })
    const at = Math.ceil((Date.now() + 6000) / 1000) * 1000
    let set = false
    try {
      await send('POST', path, { rates: { input: next }, effectiveFrom: new Date(at).toISOString() }, cur.etag)
      set = true
      await echo(secret, m) // before the change
      await new Promise((ok) => setTimeout(ok, Math.max(0, at - Date.now()) + 1000))
      await echo(secret, m) // a second after it
      const [after, before] = await receiptsOf(key.id, 2)
      expect(before.ts).toBeLessThan(at)
      expect(after.ts).toBeGreaterThanOrEqual(at)
      expect(before.costBasis).toMatchObject({ inPerM: was })
      expect(before.costBasis!.effectiveFrom).toBeLessThan(at)
      expect(after.costBasis).toMatchObject({ inPerM: next, effectiveFrom: at })
      expect(after.costBasis!.pricedLater ?? false).toBe(false)
    } finally {
      if (set) {
        // Not in effect yet: cancel it. In effect: put input back on LiteLLM.
        if (Date.now() < at) await send('DELETE', `${path}/${at}`).catch(() => {})
        else await send('POST', path, { rates: { input: null } }, (await get()).etag).catch(() => {})
      }
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 60_000)

  // §5.1 and decisions §1: a request whose (model, backend) has no price has
  // no cost. Every total leaves it out and says so; a single cost reads "No
  // price", never $0; Overview lists the pair with a way to price it.
  it('shows unpriced requests as no price and leaves them out of every total, saying so', async () => {
    type P = import('@/data/catalog').PricingView
    type V = import('@/data/catalog').SpendView
    type S = import('@/data/catalog').Summary
    type K = import('@/data/catalog').WireKey
    type B = import('@/data/catalog').Budget & { etag: string }
    // The sync retires llama's seed price on vllm-internal (no LiteLLM entry).
    const prices = await send<P>('POST', '/pricing/sync')
    expect(prices.prices.find((p) => p.model === 'llama-3.3-70b' && p.backend === 'vllm-internal')!.priced).toBe(false)
    const name = `api-mode-unpriced-${Date.now().toString(36)}`
    const { key, secret } = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
      name, team: 'batch', project: 'api-mode-test', allowedModels: ['llama-3.3-70b'], allowedRegions: ['eu-private'], expiresAt: '2027-01-01',
    })
    let budget: B | null = null
    try {
      await echo(secret, 'llama-3.3-70b')
      await echo(secret, 'llama-3.3-70b')
      const rs = await receiptsOf(key.id, 2)
      for (const r of rs) expect(r).toMatchObject({ status: 200, costUsd: null })

      // Spend: in the requests, not the spend, and counted per row.
      const byKey = await catalog.api<V>('/spend?range=1h&by=key')
      expect(byKey.rows.find((r) => r.id === name)).toMatchObject({ requests: 2, unpriced: 2, spendUsd: 0 })
      expect(byKey.unpriced).toBeGreaterThanOrEqual(2)
      expect(byKey.period.unpriced).toBeGreaterThanOrEqual(2)
      expect(byKey.trend.unpriced).toBeGreaterThanOrEqual(2)
      const byModel = await catalog.api<V>('/spend?range=1h&by=model')
      expect(byModel.rows.find((r) => r.id === 'llama-3.3-70b')!.unpriced).toBeGreaterThanOrEqual(2)
      // Overview: the window's total says how many it leaves out, and the pair is listed.
      const summary = await catalog.api<S>('/summary?range=1h')
      expect(summary.current.unpriced).toBeGreaterThanOrEqual(2)
      expect(summary.unpricedPairs!.find((p) => p.model === 'llama-3.3-70b' && p.backend === 'vllm-internal')!.requests).toBeGreaterThanOrEqual(2)
      // Keys: 24h spend leaves them out and counts them.
      expect((await catalog.api<K[]>('/keys')).find((k) => k.id === key.id)).toMatchObject({ spend24hUsd: 0, unpriced24h: 2 })
      // Budgets: not counted against the cap, and counted.
      budget = await send<B>('POST', '/budgets', { scopeType: 'key', scope: key.id, capUsd: 100, onExceed: 'warn' })
      expect((await catalog.api<B[]>('/budgets')).find((x) => x.id === budget!.id)).toMatchObject({ currentUsd: 0, unpricedRequests: 2 })
      // Activity and Overview's featured change: cost per request is over priced requests, or null.
      const activity = await catalog.api<{ changes: { impact: { before: { costPerRequestUsd: number | null; unpriced: number } } }[] }>('/activity?range=1h')
      for (const c of activity.changes) {
        expect(c.impact.before.costPerRequestUsd === null || typeof c.impact.before.costPerRequestUsd === 'number').toBe(true)
        expect(typeof c.impact.before.unpriced).toBe('number')
      }

      const page = async (route: string) => {
        window.history.pushState({}, '', route)
        render(<App />)
        await act(async () => {
          await new Promise((ok) => setTimeout(ok, 800))
        })
        const text = document.body.textContent ?? ''
        cleanup()
        return text
      }
      // Spend's total says what it leaves out.
      expect(await page('/spend')).toMatch(/\d[\d,]* requests have no price and aren't in this total/)
      // Overview: the note under Spend, and the pair in Needs attention with its action.
      const overview = await page('/')
      expect(overview).toMatch(/requests? (has|have) no price and (isn't|aren't) in this total/)
      expect(overview).toContain('llama-3.3-70b on vllm-internal has no price')
      expect(overview).toContain('Set a price')
      // The key's 24h spend reads no price, not $0.
      const keyPage = await page(`/keys?key=${key.id}`)
      expect(keyPage).toContain('No price')
      // Traffic and the receipt drawer. Traffic finds a key by the names the console loaded, so load again.
      await catalog.hydrate()
      expect(await page(`/traffic?key=${name}`)).toContain('No price')
      expect(await page(`/traffic?receipt=${rs[0].id}`)).toContain('had no price when this request arrived')
    } finally {
      if (budget) await send('DELETE', `/budgets/${budget.id}`, undefined, budget.etag).catch(() => {})
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 90_000)

  // Decisions §1, "reasoning tokens may be billed twice": OpenAI counts
  // reasoning inside completion_tokens. X-Fake-Reasoning makes the fake
  // upstream report it that way, so this shows what Agent Router logs.
  it('records what Agent Router logs for an OpenAI reasoning response: reasoning inside output', async () => {
    const { key, secret } = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
      name: `api-mode-reasoning-${Date.now().toString(36)}`, team: 'support', project: 'api-mode-test', allowedModels: ['gpt-5-mini'], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
    })
    try {
      const body = await echo(secret, 'gpt-5-mini', { 'X-Fake-Reasoning': '1000' })
      expect(body.usage.completion_tokens_details?.reasoning_tokens).toBe(1000)
      const visible = body.usage.completion_tokens - 1000
      expect(visible).toBeGreaterThan(0)
      const [r] = await receiptsOf(key.id, 1)
      // llm_output_token is completion_tokens, reasoning included; llm_reasoning_token is the same 1000 again.
      expect(r.outputTokens).toBe(body.usage.completion_tokens)
      expect(r.reasoningTokens).toBe(1000)
      expect(r.outputTokens - r.reasoningTokens).toBe(visible)
      // Reasoning bills once: the visible output at the output rate, the 1000 at the reasoning rate.
      const full = r as unknown as import('@/data/catalog').Receipt
      const b = full.costBasis!
      const writes = full.cacheWriteTokens ?? 0
      const uncached = full.inputTokens - full.cachedInputTokens - writes
      const want = (uncached * b.inPerM! + full.cachedInputTokens * b.cachedPerM! + writes * (b.cacheWritePerM ?? 0) + visible * b.outPerM! + 1000 * b.reasoningPerM!) / 1e6
      expect(full.costUsd).toBeCloseTo(want, 9)
      // The receipt drawer shows the same split and adds up to the receipt's cost.
      window.history.pushState({}, '', `/traffic?receipt=${r.id}`)
      render(<App />)
      await act(async () => {
        await new Promise((ok) => setTimeout(ok, 800))
      })
      const text = document.body.textContent ?? ''
      cleanup()
      // Each line reads label, rate source, tokens, rate, cost.
      expect(text).toMatch(new RegExp(`Output\\D*${visible.toLocaleString('en-US')}\\$`))
      expect(text).toMatch(/Reasoning\D*1,000\$/)
      expect(text).toContain(`Total${(full.inputTokens + full.outputTokens).toLocaleString('en-US')}$`)
    } finally {
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 60_000)

  // §7.5.5 savings: a key asking for gpt-5.5 by name with short answers is a
  // group a cheaper gpt-5 model would plausibly have served. The saving is
  // the receipts' cost less the sibling's price for the same tokens; the key
  // doesn't allow the sibling, so it says that must change first.
  it('finds the saving a cheaper same-family model would have made on short requests, from real receipts, with its method', async () => {
    type V = import('@/data/catalog').SavingsView
    const name = `api-mode-savings-${Date.now().toString(36)}`
    const { key, secret } = await testKey(name) // allows gpt-5.5 only
    try {
      for (let i = 0; i < 3; i++) await echo(secret, 'gpt-5.5')
      const rs = await receiptsOf(key.id, 3)
      for (const r of rs) expect(r.costUsd).toBeGreaterThan(0)
      const actual = rs.reduce((a, r) => a + r.costUsd!, 0)

      const v = await catalog.api<V>('/spend/savings')
      expect(v).toMatchObject({ outputLimit: 1000 })
      expect(v.days).toBeGreaterThan(0)
      expect(v.served).toBeGreaterThanOrEqual(3)
      const o = v.opportunities.find((x) => x.id === `key:${key.id}:gpt-5.5`)!
      expect(o).toMatchObject({ key: name, keyId: key.id, model: 'gpt-5.5', requests: 3, served: 3, keys: [name], notAllowedKeys: [name], notAllowedRequests: 3 })
      expect(o.alias).toBeUndefined()
      // A cheaper model in gpt-5.5's family, on a backend that serves it.
      expect(o.target).not.toBe('gpt-5.5')
      expect(catalog.modelById[o.target].family).toBe(catalog.modelById['gpt-5.5'].family)
      expect(catalog.backends.find((b) => b.name === o.targetBackend)!.models).toContain(o.target)
      // The actual cost is the receipts'; the saving is what the sibling would have cost less.
      expect(o.actualUsd).toBeCloseTo(actual, 5)
      expect(o.targetUsd).toBeGreaterThan(0)
      expect(o.savedUsd).toBeGreaterThan(0)
      expect(o.savedUsd).toBeCloseTo(o.actualUsd - o.targetUsd, 5)
      // Unpriced requests are never a saving: none is counted from an unpriced group.
      for (const x of v.opportunities) expect(x.actualUsd).toBeGreaterThan(x.targetUsd)

      window.history.pushState({}, '', '/spend')
      render(<App />)
      await act(async () => {
        await new Promise((ok) => setTimeout(ok, 800))
      })
      const text = document.body.textContent ?? ''
      cleanup()
      expect(text).not.toContain("Savings analysis isn't connected yet")
      expect(text).toContain(`${name}’s short gpt-5.5 calls moved to ${o.target}`)
      expect(text).toContain('3 of 3 requests counted')
      expect(text).toContain(`${name} doesn’t allow ${o.target} yet`)
      // The method, stated: short answers, the same tokens at the sibling's price then, quality not measured.
      expect(text).toContain('its answer was at most 1,000 output tokens')
      expect(text).toContain('in effect when each request started')
      expect(text).toContain('Quality isn’t measured')
    } finally {
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 60_000)

  // §7.5.5 export: the month's close report is a PDF from the control plane,
  // with the month and its total in it, and each export writes an audit row.
  it('downloads the month’s close report as a PDF with its total, and audits each export', async () => {
    type V = import('@/data/catalog').SpendView
    type C = import('@/data/catalog').Change
    const now = new Date()
    const month = `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, '0')}`
    const monthName = now.toLocaleDateString('en-US', { month: 'long', year: 'numeric', timeZone: 'UTC' })

    const f = await catalog.apiFile(`/spend/close-report?month=${month}`)
    expect(f.type).toBe('application/pdf')
    expect(f.name).toBe(`stargate-close-report-demo-${month}.pdf`)
    // Text is uncompressed WinAnsi, so it reads as latin1.
    const pdf = new TextDecoder('latin1').decode(await f.blob.arrayBuffer())
    expect(pdf.startsWith('%PDF-1.4')).toBe(true)
    expect(pdf.trimEnd().endsWith('%%EOF')).toBe(true)
    expect(pdf).toContain(`(Spend close report: ${monthName}) Tj`)
    expect(pdf).toContain('month to date') // this month is still open
    expect(pdf).toContain('(Price basis) Tj')
    expect(pdf).toContain('(Requests with no price) Tj')
    // The total, from the same aggregates as Spend's month to date, a moment apart.
    const total = /\(Spend\) Tj ET\n[^\n]*\(\$([\d,]+\.\d{2})\) Tj/.exec(pdf)
    expect(total).not.toBeNull()
    const mtd = (await catalog.api<V>('/spend?range=24h')).period.monthToDateUsd
    expect(Math.abs(Number(total![1].replace(/,/g, '')) - mtd)).toBeLessThan(Math.max(1, mtd * 0.01))

    // One audit row per export.
    const exports = async () => (await catalog.api<C[]>('/changes?kind=Export&limit=500')).filter((c) => c.action === 'Exported close report' && c.target === month)
    const audited = await exports()
    expect(audited[0]).toMatchObject({ targetKind: 'Export', actor: catalog.session.actor.email })
    await catalog.apiFile(`/spend/close-report?month=${month}`)
    const again = await exports()
    expect(again.length).toBe(audited.length + 1)
    // A malformed or future month is refused, and writes nothing.
    expect(await status(catalog.apiFile('/spend/close-report?month=nope'))).toBe(400)
    expect(await status(catalog.apiFile(`/spend/close-report?month=${now.getUTCFullYear() + 1}-01`))).toBe(400)
    expect((await catalog.api<C[]>('/changes?kind=Export&limit=1'))[0].id).toBe(again[0].id)

    // From Spend: the control downloads it and says the export is recorded.
    const created = vi.fn(() => 'blob:close-report')
    URL.createObjectURL = created
    URL.revokeObjectURL = vi.fn()
    window.history.pushState({}, '', '/spend')
    render(<App />)
    try {
      fireEvent.click(await screen.findByRole('button', { name: 'Download close report' }))
      await waitFor(() => expect(created).toHaveBeenCalled(), { timeout: 10_000 })
      expect(await screen.findByText('Close report downloaded')).toBeTruthy()
      expect(document.body.textContent).toContain('The export is recorded on Activity.')
    } finally {
      cleanup()
    }
  }, 60_000)
})
