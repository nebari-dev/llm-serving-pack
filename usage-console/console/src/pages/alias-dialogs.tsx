import { useState } from 'react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/components/ui/toast'
import { type AliasView, api, ApiError, can, models } from '@/data/catalog'

// §7.4 alias writes (api mode). Create sends If-None-Match: *, edit and
// delete the etag the row was read with; a 409 means someone else changed it.

export type LiveAlias = AliasView & { etag: string }

const path = (alias: string) => `/aliases/${encodeURIComponent(alias)}`

function errorText(e: unknown) {
  if (e instanceof ApiError && e.status === 409) return 'Someone else changed this alias since you opened it. Close and try again on the current version.'
  return e instanceof Error ? e.message : String(e)
}

/**
 * New alias (no `alias`), or a new target for an existing one. `suggested`
 * pre-fills the target (Spend's savings analysis); nothing changes until Save.
 */
export function AliasDialog({ alias, suggested, onClose }: { alias?: LiveAlias; suggested?: string; onClose: (saved: LiveAlias | null) => void }) {
  const [name, setName] = useState(alias?.alias ?? '')
  const [target, setTarget] = useState(suggested ?? alias?.target ?? '')
  const [tried, setTried] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const nameOk = /^[A-Za-z0-9._:/-]+\*?$/.test(name.trim())

  const save = async () => {
    setTried(true)
    if (!nameOk || !target) return
    setBusy(true)
    setError(null)
    try {
      const saved = await api<LiveAlias>(path(name.trim()), {
        method: 'PUT',
        body: JSON.stringify({ target }),
        headers: alias ? { 'If-Match': alias.etag } : { 'If-None-Match': '*' },
      })
      toast.add({ title: alias ? 'Alias retargeted' : 'Alias created', description: `${name.trim()} → ${target}`, type: 'success' })
      onClose({ ...alias, ...saved, requests24h: alias?.requests24h ?? 0 })
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose(null)}>
      <DialogContent className="flex max-w-lg flex-col">
        <form
          className="flex min-h-0 flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <DialogHeader>
            <DialogTitle>{alias ? `Edit ${alias.alias}` : 'New alias'}</DialogTitle>
            <DialogDescription>
              Clients ask for the alias; the gateway resolves it to the target before routing. Applies on the gateway’s next config load, within seconds.
            </DialogDescription>
          </DialogHeader>
          {alias && suggested && suggested !== alias.target && (
            <Alert>
              <AlertTitle>Suggested by Spend’s savings analysis</AlertTitle>
              <AlertDescription>
                <span className="font-mono">{alias.target}</span> → <span className="font-mono">{suggested}</span>. Nothing changes until you save, and saving moves every
                request for <span className="font-mono">{alias.alias}</span>, not only the short ones the estimate counted.
              </AlertDescription>
            </Alert>
          )}
          <Field>
            <FieldLabel>Alias</FieldLabel>
            <Input value={name} onChange={(e) => setName(e.target.value)} disabled={!!alias} placeholder="fast or summarize-*" className="font-mono" autoComplete="off" spellCheck={false} />
            <FieldDescription>A trailing * matches by prefix. An exact alias wins over a pattern.</FieldDescription>
            {tried && !nameOk && <FieldError match>Letters, digits and . _ : / -, with at most one trailing *.</FieldError>}
          </Field>
          <Field>
            <FieldLabel>Target model</FieldLabel>
            <Select items={models.map((m) => ({ value: m.id, label: m.id }))} value={target || null} onValueChange={(v) => setTarget((v as string) ?? '')}>
              <SelectTrigger aria-label="Target model" className="font-mono">
                <SelectValue placeholder="Choose a catalog model" />
              </SelectTrigger>
              <SelectContent>
                {models.map((m) => (
                  <SelectItem key={m.id} value={m.id} className="font-mono">
                    {m.id}
                    <span className="ml-auto font-sans text-xs text-muted-foreground">{m.provider}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {tried && !target && <FieldError match>Choose the model this alias resolves to.</FieldError>}
          </Field>
          {error && (
            <Alert variant="destructive">
              <AlertTitle>Not saved</AlertTitle>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant="outline" type="button" onClick={() => onClose(null)}>
              Cancel
            </Button>
            <Button type="submit" loading={busy} loadingText="Saving…" disabled={!can('routing').ok} title={can('routing').reason}>
              {alias ? 'Save' : 'Create alias'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function DeleteAliasDialog({ alias, onClose }: { alias: LiveAlias; onClose: (deleted: boolean) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const remove = async () => {
    setBusy(true)
    setError(null)
    try {
      await api(path(alias.alias), { method: 'DELETE', headers: { 'If-Match': alias.etag } })
      toast.add({ title: 'Alias deleted', description: alias.alias, type: 'success' })
      onClose(true)
    } catch (e) {
      setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose(false)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete {alias.alias}?</DialogTitle>
          <DialogDescription>
            Requests for <span className="font-mono">{alias.alias}</span> stop resolving to <span className="font-mono">{alias.target}</span>
            {alias.requests24h > 0 && <> ({alias.requests24h.toLocaleString('en-US')} in the last 24h)</>}. A request for a name that isn’t a catalog model or
            alias is refused.
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
            Keep alias
          </Button>
          <Button variant="destructive" onClick={remove} loading={busy} loadingText="Deleting…" disabled={!can('routing').ok} title={can('routing').reason}>
            Delete alias
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
