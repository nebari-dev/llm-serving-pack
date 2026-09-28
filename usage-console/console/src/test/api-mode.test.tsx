// Live check against a running control plane: hydrate from the API, then
// render every route and open a receipt. Opt-in, since it needs `make dev`:
//   VITE_STARGATE_API=http://localhost:8080 VITE_DATA=api npx vitest run src/test/api-mode.test.tsx
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

const base = import.meta.env.VITE_STARGATE_API as string | undefined

describe.skipIf(!base || import.meta.env.VITE_DATA !== 'api')('api mode against a live control plane', () => {
  let App: typeof import('@/App').default
  let catalog: typeof import('@/data/catalog')

  beforeAll(async () => {
    const realFetch = globalThis.fetch
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) =>
      realFetch(typeof input === 'string' && input.startsWith('/') ? base + input : input, init),
    )
    // jsdom has no EventSource; the stream itself is covered by the server.
    vi.stubGlobal('EventSource', class { addEventListener() {} close() {} })
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
    expect(text).toContain('creating and editing them isn’t connected yet')
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
})
