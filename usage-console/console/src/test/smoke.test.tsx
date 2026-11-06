// Smoke test: every route renders against the demo tenant without throwing
// or logging React errors, and the receipt drawer opens from a deep link.
import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import App from '@/App'
import { seedReceipts } from '@/data/catalog'

beforeAll(() => {
  window.matchMedia ??= ((q: string) => ({
    matches: false,
    media: q,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

afterEach(() => cleanup())

const routes = ['/', '/traffic', '/spend', '/keys', '/keys?key=k1']

describe('routes render', () => {
  for (const route of routes) {
    it(route, async () => {
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

  it('opens a receipt from ?receipt=', async () => {
    const r = seedReceipts.find((x) => x.verdict === 'blocked') ?? seedReceipts[0]
    window.history.pushState({}, '', `/traffic?receipt=${r.id}`)
    render(<App />)
    await act(async () => {})
    expect(document.body.textContent).toContain('Usage and cost')
    expect(document.body.textContent).toContain(r.traceId)
  })

  // Overview, Spend and Keys still link to pages this console doesn't have.
  it('sends a path with no page to Overview', async () => {
    window.history.pushState({}, '', '/activity')
    const { container } = render(<App />)
    await act(async () => {})
    expect(window.location.pathname).toBe('/')
    expect(container.querySelector('h1')?.textContent).toBe('Overview')
  })
})
