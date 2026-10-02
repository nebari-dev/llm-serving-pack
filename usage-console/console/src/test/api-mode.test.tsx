// Live check against a running control plane: hydrate from the API, then
// render every route and open a receipt. Opt-in, since it needs `make dev`:
//   VITE_STARGATE_API=http://localhost:8080 VITE_DATA=api npx vitest run src/test/api-mode.test.tsx
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
  })

  it('filters, pages and counts receipts on the server', async () => {
    type R = { id: string; ts: number; verdict: string; keyId: string; costBasis?: { inPerM: number }; status: number }
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

    // Settled, successful receipts carry the price snapshot they were costed with.
    const ok = p1.find((r) => r.status === 200 && !('inFlight' in r))
    expect(ok?.costBasis?.inPerM).toBeGreaterThanOrEqual(0)
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
    expect(text).toContain('rates recorded with this receipt')
    expect(text).toContain('Policy mode:')
    expect(text).toContain("Signed export isn't connected yet")
    expect(text).not.toContain('model_pricing row effective')
    expect(text).not.toContain('Simulate burst')
  })

  it('checks the budgets covering a key by scope, not only the one it names', async () => {
    // Keys don't name a budget (budget_id is gone); research's team budget governs it.
    const key = catalog.keys.find((k) => k.name === 'research')!
    expect(catalog.keys.filter((k) => 'budgetId' in k).map((k) => k.name)).toEqual([])
    const [r] = await catalog.api<{ id: string; trace: { step: string; input: string }[] }[]>(`/receipts?limit=1&key=${key.id}&range=1h`)
    expect(r.trace.find((s) => s.step === 'Budget checked')?.input).toMatch(/^team budget research · \$\d+ of \$20000$/)
    window.history.pushState({}, '', `/traffic?receipt=${r.id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    expect(document.body.textContent).toContain('team budget research · $')
  })

  // No reconciler exists (§4.4), so routing is read-only and nothing claims a
  // sync state, a reconcile event or a failover it didn't observe.
  it('shows routing read-only, with no reconciler and no fixture states', async () => {
    const [bs, rs] = await Promise.all([catalog.api<{ sync: string }[]>('/backends'), catalog.api<{ sync: string }[]>('/routes')])
    expect(new Set([...bs, ...rs].map((x) => x.sync))).toEqual(new Set(['not_reconciled']))
    const page = async (tab: string, act2?: () => Promise<void>) => {
      window.history.pushState({}, '', `/routing?tab=${tab}`)
      const r = render(<App />)
      await act(async () => {
        await new Promise((ok) => setTimeout(ok, 300))
      })
      await act2?.()
      const text = document.body.textContent ?? ''
      r.unmount()
      return text
    }
    const fixtures = ['Synced', 'Applying', 'Drift detected', 'Reconcile failed', 'Pending apply', 'Recent reconcile events', '529 overloaded', 'timeout after 60s', 'Endpoint', 'Replicas']

    const routesText = await page('routes', async () => {
      expect(screen.queryByRole('button', { name: /Edit route/ })).toBeNull()
      expect(screen.getByRole('button', { name: /Add provider/ })).toHaveProperty('disabled', true)
    })
    expect(routesText).toContain('No reconciler')
    expect(routesText).toContain('Read-only: there’s no reconciler to apply route changes yet.')
    for (const s of fixtures) expect(routesText).not.toContain(s)

    const backendsText = await page('backends', async () => {
      await act(async () => {
        fireEvent.click(screen.getAllByRole('button', { name: catalog.backends[0].name })[0])
        await new Promise((ok) => setTimeout(ok, 200))
      })
      expect(screen.queryByRole('button', { name: /Review changes|Apply changes|Adopt into console/ })).toBeNull()
      expect(screen.queryByRole('button', { name: /View generated YAML/ })).toBeNull()
    })
    expect(backendsText).toContain('No reconciler')
    for (const s of fixtures) expect(backendsText).not.toContain(s)

    const fallbackText = await page('fallback')
    for (const s of ['Recent failovers', '529 overloaded', 'backend not reconciled']) expect(fallbackText).not.toContain(s)
    expect(fallbackText).toContain('Fallback chains')
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
    expect(text).toContain("Savings analysis isn't connected yet")
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
      expect(text).toContain('isn’t recorded yet')
      expect(text).not.toContain('of traffic on the new secret')
      expect(text).not.toContain('web-assistant-7c9')
      expect(text).not.toContain('rotation reminders')

      fireEvent.click(screen.getByRole('button', { name: /View rotation/ }))
      await act(async () => {})
      text = document.body.textContent ?? ''
      expect(text).toContain(`Started by ${catalog.session.actor.email}`)
      expect(text).not.toContain('priya@acme.dev')
      expect(screen.getByRole('button', { name: 'Extend overlap 24h' })).toHaveProperty('disabled', true)
      expect(screen.getByRole('button', { name: 'Retire old secret now' })).toHaveProperty('disabled', true)
      expect(text).toContain('aren’t connected yet')
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
  })

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

    const capturing = catalog.routes.filter((x) => x.captureContent).map((x) => x.name)
    window.history.pushState({}, '', '/settings')
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 500))
    })
    const text = document.body.textContent ?? ''
    expect(text).toContain('30 days')
    expect(text).toContain('Never dropped')
    for (const name of capturing) expect(text).toContain(name)
    expect(text).toContain(`On for ${capturing.length} route`)
    // Warden's snapshot comes from /session, not the mockup's pods.
    expect(text).toMatch(/Warden config snapshot.*cache age \d/)
    // What has no backend yet says so instead of showing the mockup.
    expect(text).toContain('Not connected yet')
    for (const s of ['Snapshot v1842', 'on 2 of 6 pods', 'sk-proj-…Q7f', 'priya@acme.dev', 'otel-collector.nebari-gateway', 'platform-gitops', '7 years', '1,412', '4,806']) expect(text).not.toContain(s)
  })

  it('shows real aliases, their 24h traffic and price history on Models', async () => {
    type A = import('@/data/catalog').AliasView
    type P = import('@/data/catalog').PricingView
    const [aliases, pricing] = await Promise.all([catalog.api<A[]>('/aliases'), catalog.api<P>('/pricing')])
    // Seeded: one alias, summarize-* → gpt-5-mini, which the traffic generator uses.
    const summarize = aliases.find((a) => a.alias === 'summarize-*')
    expect(summarize?.target).toBe('gpt-5-mini')
    expect(summarize!.requests24h).toBeGreaterThan(0)
    for (const a of aliases) expect(catalog.modelById[a.target]).toBeDefined()
    // Every catalog model has a price row in force; the seed has no changes yet.
    for (const m of catalog.models) expect(pricing.effectiveFrom[m.id]).toMatch(/^\d{4}-\d{2}-\d{2}$/)
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

    // Alias writes are a later slice: the button is there, disabled, with the reason.
    const aliasText = await page('aliases', () => {
      const add = screen.getByRole('button', { name: /New alias/ })
      expect(add).toHaveProperty('disabled', true)
      expect(add.getAttribute('title')).toMatch(/connected yet/i)
    })
    expect(aliasText).toContain('summarize-*')
    expect(aliasText).toContain(summarize!.requests24h.toLocaleString('en-US'))
    expect(aliasText).toContain('Not recorded')
    for (const s of ['31,204', '4,120', 'input tokens < 64k', 'key.team in', 'priya@acme.dev', 'platform-gitops']) expect(aliasText).not.toContain(s)

    const priceText = await page('pricing', () => {
      expect(screen.getByRole('button', { name: /Export CSV/ })).toHaveProperty('disabled', true)
    })
    expect(priceText).toContain(pricing.effectiveFrom['gpt-5-mini'])
    if (pricing.changes.length === 0) expect(priceText).toContain('No price changes since')
    expect(priceText).toContain('Pricing sync not connected yet')
    for (const s of ['catalog sync', 'list price', '2026-08-14', '2026-06-02', 'Changed by']) expect(priceText).not.toContain(s)

    const catalogText = await page('catalog')
    for (const m of catalog.models) expect(catalogText).toContain(m.id)
    expect(catalogText).toContain('Not connected yet')
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
  /** Spends past a cent with long gpt-5.5 prompts until Warden (reloading every 5s from the aggregates) refuses one. */
  const spendUntilRefused = async (secret: string) => {
    const prompt = 'Summarize the following incident log. '.repeat(500)
    // The fake upstream also answers some requests 429; only Warden's carry budget_exceeded.
    for (let i = 0; i < 40; i++) {
      const res = await fetch(`${gateway}/v1/chat/completions`, {
        method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5.5', max_tokens: 400, messages: [{ role: 'user', content: prompt }] }),
      })
      const code = res.status === 429 ? ((await res.json().catch(() => null)) as { error?: { code?: string } } | null)?.error?.code : undefined
      if (code === 'budget_exceeded') return code
      await new Promise((ok) => setTimeout(ok, 1000))
    }
    return ''
  }
  const testKey = (name: string) =>
    send<{ key: { id: string }; secret: string }>('POST', '/keys', {
      name, team: 'support', project: 'api-mode-test', allowedModels: ['gpt-5.5'], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
    })

  it('writes budgets with dry runs, validation, audit rows and If-Match', async () => {
    type B = import('@/data/catalog').Budget & { etag: string }
    type C = import('@/data/catalog').Change
    const name = `api-mode-budget-${Date.now().toString(36)}`
    const { key } = await testKey(name)
    let id = ''
    let etag = ''
    try {
      const draft = { scopeType: 'key', scope: name, capUsd: 0.01, onExceed: 'block' }
      const dry = await send<{ dryRun: boolean; budget: B; covers: string[]; overCap: boolean }>('POST', '/budgets?dryRun=true', draft)
      expect(dry).toMatchObject({ dryRun: true, covers: [name], overCap: false, budget: { scope: name, currentUsd: 0 } })
      expect((await catalog.api<B[]>('/budgets')).some((b) => b.scope === name)).toBe(false)

      const made = await send<B>('POST', '/budgets', draft)
      etag = made.etag
      id = made.id
      expect(made).toMatchObject({ scopeType: 'key', scope: name, capUsd: 0.01, onExceed: 'block', period: 'monthly' })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created budget', target: `key ${name} · $0.01 monthly, block`, targetKind: 'Budget' })
      expect(await status(send('POST', '/budgets', draft))).toBe(409) // one budget per scope
      for (const bad of [{ ...draft, period: 'weekly' }, { ...draft, capUsd: 0 }, { ...draft, scopeType: 'team', scope: 'nobody' }, { ...draft, onExceed: 'explode' }])
        expect(await status(send('POST', '/budgets', { ...bad, scope: bad.scope + (bad.scopeType === 'key' ? '-x' : '') }))).toBe(400)

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
    const mine = async () => (await catalog.api<B[]>('/budgets')).find((b) => b.scope === name)
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
      expect(made).toMatchObject({ scopeType: 'key', capUsd: 0.01, onExceed: 'block', period: 'monthly' })
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

      const extended = await send<K>('POST', `/keys/${key.id}/rotation/extend`, { hours: 24 })
      expect(extended.rotation!.endsAt! - r!.endsAt!).toBeGreaterThan(24 * 3_600_000 - 60_000)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Extended rotation overlap', targetKind: 'Key' })
      expect((await catalog.api<C[]>('/changes'))[0].target).toMatch(new RegExp(`^${name} · until \\d{4}-\\d\\d-\\d\\d \\d\\d:\\d\\d UTC$`))
      // The overlap can't run past 7 days from now.
      expect(await status(send('POST', `/keys/${key.id}/rotation/extend`, { hours: 168 }))).toBe(400)

      const retired = await send<K>('POST', `/keys/${key.id}/rotation/finish`)
      expect(retired).toMatchObject({ status: 'active' })
      expect(retired.rotation ?? null).toBeNull()
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Retired old secret', target: name })
      expect(await call(oldSecret)).toBe(401)
      expect(await call(newSecret)).not.toBe(401)
      expect(await status(send('POST', `/keys/${key.id}/rotation/finish`))).toBe(409)
      expect(await status(send('POST', `/keys/${key.id}/rotation/extend`, { hours: 1 }))).toBe(409)
    } finally {
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
  }, 60_000)

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
      await waitFor(() => expect(builder.textContent).toContain('Rehydration isn’t built yet'), { timeout: 5000 })
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
      expect(screen.queryByRole('button', { name: 'Delete rule' })).toBeNull()
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

  it('schedules and cancels an effective-dated price change, with audit rows', async () => {
    type P = Omit<import('@/data/catalog').PricingView, 'changes'> & { changes: { model: string; field: string; to: number; effectiveAt: number; scheduled: boolean }[] }
    type C = import('@/data/catalog').Change
    const m = 'llama-3.3-70b'
    const before = await catalog.api<P>('/pricing')
    const cur = catalog.modelById[m]
    const at = '2099-01-01T00:00:00Z'
    const target = cur.inPerM + 0.01
    let scheduled: number | undefined
    try {
      const after = await send<P>('POST', `/pricing/${m}`, { inPerM: target, effectiveFrom: at })
      const change = after.changes.find((c) => c.model === m && c.field === 'Input' && c.scheduled)
      expect(change).toMatchObject({ to: target, effectiveAt: Date.parse(at), scheduled: true })
      scheduled = change!.effectiveAt
      expect(after.effectiveFrom[m]).toBe(before.effectiveFrom[m]) // today's price is untouched
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Scheduled model price change', targetKind: 'Pricing' })
      expect((await catalog.api<C[]>('/changes'))[0].target).toMatch(new RegExp(`^${m} input \\$[\\d.]+ → \\$[\\d.]+ per 1M from 2099-01-01 00:00 UTC$`))

      expect(await status(send('POST', `/pricing/${m}`, { inPerM: target, effectiveFrom: '2098-06-01T00:00:00Z' }))).toBe(400) // before the scheduled one
      expect(await status(send('POST', `/pricing/${m}`, { inPerM: target, effectiveFrom: '2020-01-01T00:00:00Z' }))).toBe(400) // backdated
      expect(await status(send('POST', `/pricing/${m}`, { inPerM: -1, effectiveFrom: '2099-06-01T00:00:00Z' }))).toBe(400)
      expect(await status(send('POST', '/pricing/no-such-model', { inPerM: 1 }))).toBe(404)
    } finally {
      if (scheduled) await send('DELETE', `/pricing/${m}/${scheduled}`)
    }
    expect((await catalog.api<P>('/pricing')).changes.some((c) => c.model === m && c.scheduled)).toBe(false)
    expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Cancelled model price change', target: `${m} change from 2099-01-01 00:00 UTC` })
    expect(await status(send('DELETE', `/pricing/${m}/${Date.parse(at)}`))).toBe(404)
  })

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
})
