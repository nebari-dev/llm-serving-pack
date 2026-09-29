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

  it('checks the budgets covering a key by scope, not only the one it names', async () => {
    // research has no budget_id; its team's budget still governs it.
    const key = catalog.keys.find((k) => k.name === 'research')!
    expect(key.budgetId).toBeFalsy()
    const [r] = await catalog.api<{ id: string; trace: { step: string; input: string }[] }[]>(`/receipts?limit=1&key=${key.id}&range=1h`)
    expect(r.trace.find((s) => s.step === 'Budget checked')?.input).toMatch(/^team budget research · \$\d+ of \$20000$/)
    window.history.pushState({}, '', `/traffic?receipt=${r.id}`)
    render(<App />)
    await act(async () => {
      await new Promise((ok) => setTimeout(ok, 300))
    })
    expect(document.body.textContent).toContain('team budget research · $')
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
  // audit row, and refuses a stale If-Match with 409.
  const status = (p: Promise<unknown>) => p.then(() => 200, (e: { status?: number }) => e.status ?? 0)
  const send = <T,>(method: string, path: string, body?: unknown, ifMatch?: string) =>
    catalog.api<T>(path, { method, body: body === undefined ? undefined : JSON.stringify(body), headers: ifMatch ? { 'If-Match': ifMatch } : {} })

  it('writes aliases with audit rows, validation and If-Match', async () => {
    type A = import('@/data/catalog').AliasView & { version: string }
    type C = import('@/data/catalog').Change
    const name = `api-mode-test-${Date.now().toString(36)}`
    const path = `/aliases/${encodeURIComponent(name)}`
    try {
      const made = await send<A>('PUT', path, { target: 'gpt-5-mini' })
      expect(made).toMatchObject({ alias: name, target: 'gpt-5-mini' })
      expect((await catalog.api<A[]>('/aliases')).find((a) => a.alias === name)?.version).toBe(made.version)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({
        action: 'Created alias', target: `${name} → gpt-5-mini`, targetKind: 'Alias', actor: catalog.session.actor.email,
      })

      const moved = await send<A>('PUT', path, { target: 'claude-haiku-4-5' }, made.version)
      expect(moved.version).not.toBe(made.version)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Changed alias target', target: `${name} gpt-5-mini → claude-haiku-4-5` })
      // Written against the version before that one: stale.
      expect(await status(send('PUT', path, { target: 'gpt-5.5' }, made.version))).toBe(409)
      expect(await status(send('PUT', path, { target: 'no-such-model' }))).toBe(400)
      // A pattern that would capture catalog models is refused.
      expect(await status(send('PUT', `/aliases/${encodeURIComponent('gpt-*')}`, { target: 'gpt-5-mini' }))).toBe(400)
    } finally {
      await send('DELETE', path).catch(() => {})
    }
    expect((await catalog.api<A[]>('/aliases')).some((a) => a.alias === name)).toBe(false)
    expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Deleted alias', target: name })
    expect(await status(send('DELETE', path))).toBe(404)
  })

  // §11's exit test: a cap set through the API stops real requests at the
  // gateway, and the blocked ones carry the reason.
  it('writes budgets with dry runs and audit rows, and the gateway enforces the new cap', async () => {
    type B = import('@/data/catalog').Budget & { version: string }
    type C = import('@/data/catalog').Change
    type R = { verdict: string; errorCode?: string; trace: { step: string; input: string }[] }
    const gateway = (import.meta.env.VITE_STARGATE_GATEWAY as string | undefined) ?? 'http://localhost:1975'
    const name = `api-mode-budget-${Date.now().toString(36)}`
    const { key, secret } = await send<{ key: { id: string }; secret: string }>('POST', '/keys', {
      name, team: 'support', project: 'api-mode-test', allowedModels: ['gpt-5.5'], allowedRegions: ['us-east'], expiresAt: '2027-01-01',
    })
    let id = ''
    try {
      const draft = { scopeType: 'key', scope: name, capUsd: 0.01, onExceed: 'block' }
      const dry = await send<{ dryRun: boolean; budget: B; covers: string[]; overCap: boolean }>('POST', '/budgets?dryRun=true', draft)
      expect(dry).toMatchObject({ dryRun: true, covers: [name], overCap: false, budget: { scope: name, currentUsd: 0 } })
      expect((await catalog.api<B[]>('/budgets')).some((b) => b.scope === name)).toBe(false)

      const made = await send<B>('POST', '/budgets', draft)
      id = made.id
      expect(made).toMatchObject({ scopeType: 'key', scope: name, capUsd: 0.01, onExceed: 'block', period: 'monthly' })
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Created budget', target: `key ${name} · $0.01 monthly, block`, targetKind: 'Budget' })
      expect(await status(send('POST', '/budgets', draft))).toBe(409) // one budget per scope
      for (const bad of [{ ...draft, period: 'weekly' }, { ...draft, capUsd: 0 }, { ...draft, scopeType: 'team', scope: 'nobody' }, { ...draft, onExceed: 'explode' }])
        expect(await status(send('POST', '/budgets', { ...bad, scope: bad.scope + (bad.scopeType === 'key' ? '-x' : '') }))).toBe(400)

      // Spend past a cent: long prompts on gpt-5.5, until Warden (reloading every
      // 5s from the aggregates) refuses one.
      const prompt = 'Summarize the following incident log. '.repeat(500)
      // The fake upstream also answers some requests 429; only Warden's carry budget_exceeded.
      let refused = ''
      for (let i = 0; i < 40 && !refused; i++) {
        const res = await fetch(`${gateway}/v1/chat/completions`, {
          method: 'POST', headers: { Authorization: `Bearer ${secret}`, 'Content-Type': 'application/json' },
          body: JSON.stringify({ model: 'gpt-5.5', max_tokens: 400, messages: [{ role: 'user', content: prompt }] }),
        })
        const code = res.status === 429 ? ((await res.json().catch(() => null)) as { error?: { code?: string } } | null)?.error?.code : undefined
        if (code === 'budget_exceeded') refused = code
        else await new Promise((ok) => setTimeout(ok, 1000))
      }
      expect(refused).toBe('budget_exceeded')
      let blocked: R | undefined
      for (let i = 0; i < 20 && !blocked; i++) {
        blocked = (await catalog.api<R[]>(`/receipts?limit=5&key=${key.id}&verdict=blocked`))[0]
        if (!blocked) await new Promise((ok) => setTimeout(ok, 500))
      }
      expect(blocked?.errorCode).toBe('budget_exceeded')
      expect(blocked!.trace.find((s) => s.step === 'Budget checked')?.input).toMatch(new RegExp(`^key budget ${name} · \\$0\\.\\d\\d of \\$0\\.01$`))

      const raised = await send<B>('PATCH', `/budgets/${id}`, { capUsd: 1000 }, made.version)
      expect(raised.capUsd).toBe(1000)
      expect((await catalog.api<C[]>('/changes'))[0]).toMatchObject({ action: 'Raised budget cap', target: `${name} $0.01 → $1,000` })
      expect(await status(send('PATCH', `/budgets/${id}`, { capUsd: 5 }, made.version))).toBe(409)
      expect(await status(send('PATCH', `/budgets/${id}`, { onExceed: 'explode' }))).toBe(400)
    } finally {
      if (id) await send('DELETE', `/budgets/${id}`).catch(() => {})
      await send('POST', `/keys/${key.id}/revoke`).catch(() => {})
    }
    expect((await catalog.api<B[]>('/budgets')).some((b) => b.id === id)).toBe(false)
    expect((await catalog.api<C[]>('/changes')).find((c) => c.targetKind === 'Budget')).toMatchObject({ action: 'Deleted budget', target: `key ${name} · $1,000 monthly` })
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
