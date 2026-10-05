import { CircleCheck, Plus } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { toast } from '@/components/ui/toast'
import { api, ApiError, type Backend, type BackendResult, type ConnectionTest } from '@/data/catalog'
import { ago } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useLive } from '@/state/live'

// Providers in api mode (§7.5.1, §7.5.6, §9.1). A provider is a backend: a
// base URL speaking OpenAI's API and an optional key. The key goes to the
// control plane once, is tested, and is never shown again: the console sees
// its prefix and the last test only. Saving changes the desired state; the
// gateway sends to the provider once routing is applied.

interface Tile {
  id: string
  label: string
  /** Where its API lives, prefilled. */
  url: string
  hint: string
  /** Why it can't be added yet. */
  disabled?: string
}

const tiles: Tile[] = [
  { id: 'OpenAI', label: 'OpenAI', url: 'https://api.openai.com/v1', hint: 'API key' },
  { id: 'Anthropic', label: 'Anthropic', url: 'https://api.anthropic.com/v1', hint: 'API key · OpenAI-compatible endpoint' },
  { id: 'OpenAI-compatible', label: 'OpenAI-compatible', url: '', hint: 'OpenRouter, Together, vLLM, Ollama…' },
  { id: 'Self-hosted', label: 'Self-hosted', url: 'http://localhost:8000/v1', hint: 'Usually no key' },
  { id: 'Bedrock', label: 'Bedrock', url: '', hint: '', disabled: 'Needs AWS cloud credentials, which the console can’t set up yet.' },
  { id: 'Azure', label: 'Azure', url: '', hint: '', disabled: 'Needs Azure cloud credentials, which the console can’t set up yet.' },
  { id: 'Vertex', label: 'Vertex', url: '', hint: '', disabled: 'Needs Google Cloud credentials, which the console can’t set up yet.' },
]

const tileFor = (provider: string) => tiles.find((t) => t.id === provider && !t.disabled)

const splitModels = (s: string) => s.split(/[\s,]+/).filter(Boolean)

/** The last test of a backend, in a line. */
export function LastTest({ t }: { t?: Backend['lastTest'] }) {
  if (!t) return <span className="text-muted-foreground">Not tested</span>
  return (
    <span className={cn(!t.ok && 'text-destructive-foreground')}>
      {t.ok ? 'OK' : 'Failed'} {ago(t.at)}: <span className="font-mono text-xs break-all">{t.message}</span>
    </span>
  )
}

/** A connection test's result, with the models found as buttons that add them. */
function TestResult({ test, models, onAdd }: { test: ConnectionTest; models: string[]; onAdd?: (m: string) => void }) {
  if (!test.ok) {
    return (
      <Alert variant="destructive">
        <AlertTitle>{test.status ? `The provider answered ${test.status}` : 'The provider didn’t answer'}</AlertTitle>
        <AlertDescription>
          <pre className="max-h-40 overflow-auto font-mono text-xs whitespace-pre-wrap">{test.error}</pre>
        </AlertDescription>
      </Alert>
    )
  }
  const shown = test.models.slice(0, 60)
  return (
    <div className="flex flex-col gap-2 rounded-md border border-border bg-muted px-3 py-2 text-sm">
      <p className="flex items-center gap-2">
        <CircleCheck className="size-4 text-v-allowed-fg" aria-hidden="true" />
        Connected in {test.ms}ms. It lists {test.models.length} {test.models.length === 1 ? 'model' : 'models'}
        {onAdd && test.models.length > 0 ? '; add the ones the gateway should serve.' : '.'}
      </p>
      {onAdd && (
        <div className="flex max-h-32 flex-wrap gap-1 overflow-y-auto">
          {shown.map((m) => (
            <Button key={m} type="button" size="xs" variant="outline" disabled={models.includes(m)} aria-label={`Add ${m}`} onClick={() => onAdd(m)} className="font-mono">
              <Plus /> {m}
            </Button>
          ))}
          {test.models.length > shown.length && <span className="text-xs text-muted-foreground">and {test.models.length - shown.length} more; type them in.</span>}
        </div>
      )}
    </div>
  )
}

interface Draft {
  provider: string
  providerName: string
  name: string
  baseUrl: string
  region: string
  models: string
  apiKey: string
}

const toDraft = (b?: Backend): Draft => ({
  provider: b ? (tileFor(b.provider)?.id ?? 'OpenAI-compatible') : '',
  providerName: b && !tileFor(b.provider) ? b.provider : '',
  name: b?.name ?? '',
  baseUrl: b?.endpoint?.baseUrl ?? '',
  region: b?.region ?? '',
  models: b?.models.join(', ') ?? '',
  apiKey: '',
})

/**
 * New provider (no `backend`), or an edit of one. The key is asked for on
 * create only; an existing backend's key is replaced on its own
 * (ReplaceKeyDialog). Renders the form's fields and footer; the caller puts
 * it in a dialog or a page.
 */
export function ProviderForm({ backend, onSaved, onCancel, label }: { backend?: Backend; onSaved: (r: BackendResult) => void; onCancel?: () => void; label: string }) {
  const [draft, setDraft] = useState(() => toDraft(backend))
  const [etag, setEtag] = useState(backend?.etag ?? '')
  const [test, setTest] = useState<ConnectionTest | null>(null)
  const [testing, setTesting] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [stale, setStale] = useState<Backend | null>(null)
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))
  const tile = tiles.find((t) => t.id === draft.provider)
  const provider = draft.provider === 'OpenAI-compatible' && draft.providerName.trim() ? draft.providerName.trim() : draft.provider
  const body = () => ({ name: draft.name.trim(), provider, region: draft.region.trim(), baseUrl: draft.baseUrl.trim(), models: splitModels(draft.models), apiKey: draft.apiKey || undefined })

  const runTest = async () => {
    setTesting(true)
    setError(null)
    try {
      // Edit: the saved backend's stored key. New: the key typed here, for this request only.
      setTest(backend ? ((await api<BackendResult>(`/backends/${encodeURIComponent(backend.name)}/test`, { method: 'POST' })).test ?? null) : await api<ConnectionTest>('/backends/test', { method: 'POST', body: JSON.stringify(body()) }))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setTesting(false)
    }
  }
  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      let out: BackendResult
      if (backend) {
        const { apiKey: _, ...rest } = body()
        out = { backend: await api<Backend>(`/backends/${encodeURIComponent(backend.name)}`, { method: 'PUT', body: JSON.stringify(rest), headers: { 'If-Match': etag } }) }
      } else {
        out = await api<BackendResult>('/backends', { method: 'POST', body: JSON.stringify(body()) })
      }
      setDraft((d) => ({ ...d, apiKey: '' }))
      onSaved(out)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.current) setStale(e.current as Backend)
      else setError(e instanceof ApiError && e.status === 409 ? `A backend named ${draft.name.trim()} already exists.` : e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      aria-label={label}
      className="flex min-h-0 flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        void save()
      }}
    >
      <div className="flex max-h-[34rem] min-h-0 flex-col gap-4 overflow-y-auto">
        <RadioGroup
          aria-label="Provider"
          value={draft.provider || null}
          onValueChange={(v) => {
            // Prefill the provider's URL unless one was typed.
            const t = tiles.find((x) => x.id === v)
            const typed = draft.baseUrl && draft.baseUrl !== tile?.url
            set({ provider: v as string, baseUrl: typed ? draft.baseUrl : (t?.url ?? '') })
            setTest(null)
          }}
          className="grid grid-cols-2 gap-2 sm:grid-cols-4"
          orientation="vertical"
        >
          {tiles.map((t) => (
            <RadioGroupItem key={t.id} value={t.id} variant="box" disabled={!!t.disabled} description={t.disabled ?? t.hint} className={(s) => cn(s.checked && 'border-primary bg-card')}>
              {t.label}
            </RadioGroupItem>
          ))}
        </RadioGroup>
        {draft.provider && (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field>
              <FieldLabel>Name</FieldLabel>
              <Input value={draft.name} onChange={(e) => set({ name: e.target.value })} disabled={!!backend} placeholder="together" className="font-mono" autoComplete="off" spellCheck={false} />
              <FieldDescription>The backend’s name in routes and receipts. Lowercase, digits, dots and dashes.</FieldDescription>
            </Field>
            {draft.provider === 'OpenAI-compatible' && (
              <Field>
                <FieldLabel>Provider name</FieldLabel>
                <Input value={draft.providerName} onChange={(e) => set({ providerName: e.target.value })} placeholder="OpenRouter" autoComplete="off" />
                <FieldDescription>Shown on receipts and Spend. Leave empty for “OpenAI-compatible”.</FieldDescription>
              </Field>
            )}
            <Field className="sm:col-span-2">
              <FieldLabel>Base URL</FieldLabel>
              <Input value={draft.baseUrl} onChange={(e) => set({ baseUrl: e.target.value })} placeholder="https://api.together.xyz/v1" className="font-mono" autoComplete="off" spellCheck={false} />
              <FieldDescription>What an OpenAI SDK takes as base_url. localhost means this control plane’s machine.</FieldDescription>
            </Field>
            <Field>
              <FieldLabel>Region</FieldLabel>
              <Input value={draft.region} onChange={(e) => set({ region: e.target.value })} placeholder="us-east" className="font-mono" autoComplete="off" spellCheck={false} />
              <FieldDescription>Where requests to it are processed, for region rules and receipts.</FieldDescription>
            </Field>
            {!backend && (
              <Field>
                <FieldLabel>API key</FieldLabel>
                <Input type="password" value={draft.apiKey} onChange={(e) => set({ apiKey: e.target.value })} placeholder={tile?.id === 'Self-hosted' ? 'None' : 'sk-…'} className="font-mono" autoComplete="off" spellCheck={false} />
                <FieldDescription>Tested once, then sealed. Never returned to callers: the console shows its first characters only.</FieldDescription>
              </Field>
            )}
            <Field className="sm:col-span-2">
              <FieldLabel>Models</FieldLabel>
              <Input value={draft.models} onChange={(e) => set({ models: e.target.value })} placeholder="Test the connection to list them" className="font-mono" autoComplete="off" spellCheck={false} />
              <FieldDescription>The models the gateway serves from it. A model the catalog doesn’t know is added with no price until one is set on Models.</FieldDescription>
            </Field>
            <div className="flex flex-col items-start gap-2 sm:col-span-2">
              <Button type="button" variant="outline" onClick={runTest} loading={testing} loadingText="Testing…" disabled={!draft.baseUrl.trim()}>
                Test connection
              </Button>
              {test && <TestResult test={test} models={splitModels(draft.models)} onAdd={(m) => set({ models: [...splitModels(draft.models), m].join(', ') })} />}
            </div>
          </div>
        )}
      </div>
      {stale && (
        <Alert variant="destructive">
          <AlertTitle>This provider changed since you opened it</AlertTitle>
          <AlertDescription className="flex flex-col items-start gap-2">
            Saving would overwrite that change. Load the current version, then make your edit again.
            <Button
              type="button"
              variant="outline"
              size="xs"
              onClick={() => {
                setDraft(toDraft(stale))
                setEtag(stale.etag ?? '')
                setStale(null)
              }}
            >
              Load the current version
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {error && (
        <Alert variant="destructive">
          <AlertTitle>Not saved</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <div className="flex justify-end gap-2">
        {onCancel && (
          <Button variant="outline" type="button" onClick={onCancel}>
            Cancel
          </Button>
        )}
        <Button type="submit" loading={busy} loadingText="Saving…" disabled={!draft.provider || !!stale}>
          Save provider
        </Button>
      </div>
    </form>
  )
}

/** The provider form in a dialog: Routing's "Add provider" and the drawer's "Edit provider". */
export function ProviderDialog({ backend, onClose }: { backend?: Backend; onClose: (saved: boolean) => void }) {
  return (
    <Dialog open onOpenChange={(o) => !o && onClose(false)}>
      <DialogContent className="flex max-w-3xl flex-col">
        <DialogHeader>
          <DialogTitle>{backend ? `Edit provider ${backend.name}` : 'Add a provider'}</DialogTitle>
          <DialogDescription>
            {backend
              ? 'Saving changes the desired routing; the gateway runs it once you apply. Replace the key from the backend’s details.'
              : 'A base URL speaking OpenAI’s API, and its key. Saving adds the backend to the desired routing; route its models to it, then apply.'}
          </DialogDescription>
        </DialogHeader>
        <ProviderForm
          label={backend ? `Edit ${backend.name}` : 'New provider'}
          backend={backend}
          onCancel={() => onClose(false)}
          onSaved={({ backend: b, test }) => {
            toast.add({
              title: backend ? 'Provider saved' : 'Provider added',
              description: `${b.name} is pending until you apply.${test && !test.ok ? ' Its connection test failed; see its details.' : ''}`,
              type: test && !test.ok ? 'warning' : 'success',
            })
            onClose(true)
          }}
        />
      </DialogContent>
    </Dialog>
  )
}

/** Replaces a backend's key: tested once with the new key, then pending until applied. */
export function ReplaceKeyDialog({ backend, onClose }: { backend: Backend; onClose: (replaced: boolean) => void }) {
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState<BackendResult | null>(null)
  const replace = async () => {
    setBusy(true)
    setError(null)
    try {
      setDone(await api<BackendResult>(`/backends/${encodeURIComponent(backend.name)}/key`, { method: 'PUT', body: JSON.stringify({ apiKey: key }) }))
      setKey('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose(!!done)}>
      <DialogContent className="flex max-w-xl flex-col">
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void replace()
          }}
        >
          <DialogHeader>
            <DialogTitle>Replace {backend.name}’s key</DialogTitle>
            <DialogDescription>
              The gateway keeps sending the current key{backend.key ? ` (${backend.key.prefix}…)` : ''} until you apply routing; the apply restarts it with the new one.
            </DialogDescription>
          </DialogHeader>
          {done ? (
            <>
              <p className="text-sm">
                Replaced: the key is now <span className="font-mono">{done.backend.key?.prefix}…</span>, pending until you apply.
              </p>
              {done.test && <TestResult test={done.test} models={[]} />}
            </>
          ) : (
            <Field>
              <FieldLabel>New API key</FieldLabel>
              <Input type="password" value={key} onChange={(e) => setKey(e.target.value)} className="font-mono" autoComplete="off" spellCheck={false} />
              <FieldDescription>Tested once, then sealed. Never returned to callers.</FieldDescription>
            </Field>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertTitle>Not replaced</AlertTitle>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            {done ? (
              <Button type="button" onClick={() => onClose(true)}>
                Done
              </Button>
            ) : (
              <>
                <Button variant="outline" type="button" onClick={() => onClose(false)}>
                  Cancel
                </Button>
                <Button type="submit" loading={busy} loadingText="Replacing and testing…" disabled={!key}>
                  Replace key
                </Button>
              </>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Deletes a backend; the control plane refuses while a route sends to it. */
export function DeleteBackendDialog({ backend, onClose }: { backend: Backend; onClose: (deleted: boolean) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const remove = async () => {
    setBusy(true)
    setError(null)
    try {
      await api(`/backends/${encodeURIComponent(backend.name)}`, { method: 'DELETE', headers: { 'If-Match': backend.etag ?? '' } })
      toast.add({ title: 'Provider deleted', description: `${backend.name} is pending removal until you apply.`, type: 'success' })
      onClose(true)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose(false)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete provider {backend.name}?</DialogTitle>
          <DialogDescription>
            {backend.sync === 'synced' ? 'The gateway keeps it until the next apply. ' : ''}Its key is removed from the gateway’s key store. Its prices stay, as history.
          </DialogDescription>
        </DialogHeader>
        {error && (
          <Alert variant="destructive">
            <AlertTitle>Not deleted</AlertTitle>
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button variant="outline" type="button" onClick={() => onClose(false)}>
            Keep it
          </Button>
          <Button variant="destructive" onClick={remove} loading={busy} loadingText="Deleting…">
            Delete provider
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** Settings → Providers in api mode: each backend's key by prefix and its last test. */
export function LiveProviderKeys() {
  const live = useLive<Backend[]>('/backends', [], 30_000)
  if (!live.loaded) return <p className="text-sm text-muted-foreground">Loading providers…</p>
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[48rem] text-sm">
        <thead className="text-left text-xs text-muted-foreground">
          <tr className="border-b border-border">
            <th className="py-1.5 pr-3 font-medium">Backend</th>
            <th className="py-1.5 pr-3 font-medium">Key</th>
            <th className="py-1.5 pr-3 font-medium">Last tested</th>
          </tr>
        </thead>
        <tbody>
          {live.data.map((b) => (
            <tr key={b.name} className="border-b border-border last:border-0">
              <td className="py-2 pr-3">
                <span className="font-mono">{b.name}</span>
                <div className="text-xs text-muted-foreground">{b.provider}</div>
              </td>
              <td className="py-2 pr-3 text-xs">
                <KeyText b={b} />
              </td>
              <td className="py-2 pr-3 text-xs">
                <LastTest t={b.lastTest} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mt-2 text-xs text-muted-foreground">
        Add a provider, replace a key or test a connection on{' '}
        <Link to="/routing?tab=backends" className="underline underline-offset-4">
          Routing → Backends
        </Link>
        . Rotation reminders aren’t connected yet.
      </p>
    </div>
  )
}

/** What the console may say about a backend's key: its prefix, or where it's set. */
export function KeyText({ b }: { b: Backend }) {
  const e = b.endpoint
  if (b.key)
    return (
      <span>
        <span className="font-mono">{b.key.prefix}…</span> <span className="text-muted-foreground">set {ago(b.key.setAt)}</span>
      </span>
    )
  if (e?.apiKeyEnv)
    return (
      <span>
        <span className="font-mono">${e.apiKeyEnv}</span> <span className="text-muted-foreground">set outside the console (server/.env)</span>
      </span>
    )
  if (!e) return <span className="text-muted-foreground">No endpoint</span>
  return <span className="text-muted-foreground">None sent</span>
}

