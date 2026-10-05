// Smoke test: every route renders against the demo tenant without throwing
// or logging React errors, and the receipt drawer opens from a deep link.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import App from '@/App'
import { budgets, keys, seedReceipts } from '@/data/catalog'

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

const routes = ['/', '/traffic', '/spend', '/models', '/routing', '/guardrails', '/keys', '/keys?key=k1', '/activity', '/settings', '/onboarding', '/guardrails?rule=r3', '/routing?tab=backends']

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
    expect(document.body.textContent).toContain('Decision trace')
    expect(document.body.textContent).toContain(r.traceId)
  })
})

// Mock mode keeps the budget form working on the fixtures, with no backend.
describe('budgets on Spend', () => {
  it('adds, edits and deletes a budget', async () => {
    const k = keys.find((x) => x.status !== 'revoked' && !budgets.some((b) => b.scopeType === 'key' && b.scope === x.id))!
    window.history.pushState({}, '', '/spend')
    render(<App />)
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: 'Add budget' }))
    let dialog = await formDialog()
    fireEvent.click(within(dialog).getByRole('radio', { name: /^Key/ }))
    fireEvent.click(within(dialog).getByRole('combobox', { name: 'Key' }))
    choose(await screen.findByRole('option', { name: k.name }))
    fireEvent.change(within(dialog).getByLabelText('Monthly cap (USD)'), { target: { value: '250' } })
    fireEvent.click(within(dialog).getByRole('radio', { name: /^Warn/ }))
    await waitFor(() => expect(dialog.textContent).toContain(`Covers 1 active key: ${k.name}`))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create budget' }))
    await formDialogClosed()
    const table = screen.getByRole('table', { name: 'Budgets' })
    expect(table.textContent).toContain(k.name)

    fireEvent.click(within(table).getByRole('button', { name: `Edit budget ${k.name}` }))
    dialog = await formDialog()
    fireEvent.change(within(dialog).getByLabelText('Monthly cap (USD)'), { target: { value: '300' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save changes' }))
    await formDialogClosed()
    expect(budgets.find((b) => b.scope === k.id)?.capUsd).toBe(300)

    fireEvent.click(within(table).getByRole('button', { name: `Delete budget ${k.name}` }))
    dialog = await formDialog()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete budget' }))
    await formDialogClosed()
    expect(budgets.some((b) => b.scope === k.id)).toBe(false)
    expect(table.textContent).not.toContain(k.name)
  })

  it('caps a new project before it has keys', async () => {
    window.history.pushState({}, '', '/spend')
    render(<App />)
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: 'Add budget' }))
    const dialog = await formDialog()
    fireEvent.click(within(dialog).getByRole('radio', { name: /^Project/ }))
    fireEvent.click(within(dialog).getByRole('button', { name: 'New project…' }))
    fireEvent.change(within(dialog).getByLabelText('Project name'), { target: { value: 'smoke-launch' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create project' }))
    await waitFor(() => expect(within(dialog).getByRole('combobox', { name: 'Project' }).textContent).toContain('smoke-launch'))
    fireEvent.change(within(dialog).getByLabelText('Monthly cap (USD)'), { target: { value: '100' } })
    fireEvent.click(within(dialog).getByRole('radio', { name: /^Block/ }))
    await waitFor(() => expect(dialog.textContent).toContain('Covers no active keys yet.'))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create budget' }))
    await formDialogClosed()
    expect(budgets.find((b) => b.scopeType === 'project' && b.scopeName === 'smoke-launch')?.scope).toMatch(/^p-/)
    expect(screen.getByRole('table', { name: 'Budgets' }).textContent).toContain('smoke-launch')
  })
})
