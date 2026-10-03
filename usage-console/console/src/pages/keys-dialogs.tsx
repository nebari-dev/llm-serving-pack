import { Copy, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/components/ui/toast'
import { type ApiKey, type KeyRotation, coveringBudgets, createKey, dataMode, extendRotation, finishRotation, models, rotateKey, teams } from '@/data/catalog'
import { ago, int } from '@/lib/format'
import { useNow } from '@/state/live'
import { cn } from '@/lib/utils'

// §7.5.8 key lifecycle dialogs: create (expiry required, "never" is an explicit
// choice with a warning), show-once secret with acknowledgement, revoke with
// blast radius and typed confirmation, rotate with an overlap window.

const regions = ['us-east', 'eu-central', 'eu-west', 'eu-private']

/** Shows a secret exactly once. Done stays disabled until the user acknowledges storing it. */
function SecretOnce({ secret, onDone, context }: { secret: string; onDone: () => void; context: string }) {
  const [ack, setAck] = useState(false)
  return (
    <>
      <DialogHeader>
        <DialogTitle>Copy your new secret</DialogTitle>
        <DialogDescription>{context} This is the only time the full secret is shown. The gateway stores a hash; nobody can retrieve it later.</DialogDescription>
      </DialogHeader>
      <div className="flex items-center gap-2 rounded-md border border-border-strong bg-muted p-3">
        <code className="min-w-0 flex-1 font-mono text-sm break-all text-foreground select-all">{secret}</code>
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            void navigator.clipboard?.writeText(secret)
            toast.add({ title: 'Secret copied', type: 'success' })
          }}
        >
          <Copy /> Copy
        </Button>
      </div>
      <Checkbox checked={ack} onCheckedChange={setAck}>
        I've stored this secret somewhere safe. It won't be shown again.
      </Checkbox>
      <DialogFooter>
        <Button disabled={!ack} onClick={onDone}>
          Done
        </Button>
      </DialogFooter>
    </>
  )
}

type Expiry = '30' | '90' | '365' | 'custom' | 'never'

export function CreateKeyDialog({ open, onOpenChange, onCreate }: { open: boolean; onOpenChange: (o: boolean) => void; onCreate: (k: ApiKey) => void }) {
  const [step, setStep] = useState<'form' | 'secret'>('form')
  const [secret, setSecret] = useState('')
  const [name, setName] = useState('')
  const [team, setTeam] = useState<string>('support')
  const [project, setProject] = useState('')
  const [allowed, setAllowed] = useState<string[]>(['gpt-5-mini'])
  const [allowedRegions, setAllowedRegions] = useState<string[]>(['us-east'])
  const [expiry, setExpiry] = useState<Expiry | ''>('')
  const [customDate, setCustomDate] = useState('')
  const [neverAck, setNeverAck] = useState(false)
  const [tried, setTried] = useState(false)
  const [busy, setBusy] = useState(false)

  const nameValid = /^[a-z][a-z0-9-]{2,39}$/.test(name)
  const expiryValid = expiry !== '' && (expiry !== 'custom' || !!customDate) && (expiry !== 'never' || neverAck)
  const valid = nameValid && allowed.length > 0 && allowedRegions.length > 0 && expiryValid && project.trim().length > 0

  const reset = () => {
    setStep('form')
    setSecret('')
    setName('')
    setProject('')
    setAllowed(['gpt-5-mini'])
    setAllowedRegions(['us-east'])
    setExpiry('')
    setCustomDate('')
    setNeverAck(false)
    setTried(false)
  }

  const toggle = (list: string[], v: string, on: boolean) => (on ? [...list, v] : list.filter((x) => x !== v))

  const submit = async () => {
    setTried(true)
    if (!valid || busy) return
    const expiresAt =
      expiry === 'never' ? null : expiry === 'custom' ? customDate : new Date(Date.now() + Number(expiry) * 86_400_000).toISOString().slice(0, 10)
    setBusy(true)
    try {
      const res = await createKey({
        name,
        team,
        project: project.trim(),
        allowedModels: allowed,
        allowedRegions,
        expiresAt,
      })
      onCreate(res.key)
      setSecret(res.secret)
      setStep('secret')
    } catch (e) {
      toast.add({ title: 'Could not create key', description: e instanceof Error ? e.message : String(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  // Budgets apply by scope, so the key gets whatever covers its team,
  // project and name; there's nothing to pick.
  const covering = coveringBudgets({ team, project: project.trim(), name })

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        // The secret step can only be left through its acknowledged Done button.
        if (!o && step === 'secret') return
        onOpenChange(o)
        if (!o) reset()
      }}
    >
      <DialogContent className="flex max-w-xl flex-col" showCloseButton={step === 'form'}>
        {step === 'secret' ? (
          <SecretOnce
            secret={secret}
            context={`Key ${name} is active now.`}
            onDone={() => {
              onOpenChange(false)
              toast.add({ title: 'Key created', description: `${name} · ${secret.slice(0, 13)}…`, type: 'success' })
              reset()
            }}
          />
        ) : (
          <form
            className="flex min-h-0 flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              submit()
            }}
          >
            <DialogHeader>
              <DialogTitle>Create gateway key</DialogTitle>
              <DialogDescription>A key identifies one app. Spend, limits and receipts are attributed to its team and project.</DialogDescription>
            </DialogHeader>
            <div className="-mx-6 flex min-h-0 flex-col gap-4 overflow-y-auto px-6">
              <Field invalid={tried && !nameValid}>
                <FieldLabel>Name</FieldLabel>
                <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="support-bot" className="font-mono" />
                <FieldDescription>Lowercase letters, numbers and hyphens. Shown in traffic and receipts.</FieldDescription>
                {tried && !nameValid && <FieldError match>Use 3–40 lowercase letters, numbers or hyphens, starting with a letter.</FieldError>}
              </Field>
              <div className="grid grid-cols-2 gap-3">
                <Field>
                  <FieldLabel>Team</FieldLabel>
                  <Select items={teams.map((t) => ({ value: t.id, label: t.name }))} value={team} onValueChange={(v) => v && setTeam(v)}>
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {teams.map((t) => (
                        <SelectItem key={t.id} value={t.id}>
                          {t.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field invalid={tried && !project.trim()}>
                  <FieldLabel>Project</FieldLabel>
                  <Input value={project} onChange={(e) => setProject(e.target.value)} placeholder="helpdesk" className="font-mono" />
                  {tried && !project.trim() && <FieldError match>Add a project so spend can be attributed.</FieldError>}
                </Field>
              </div>

              <fieldset className="flex flex-col gap-2">
                <legend className="mb-1 text-sm font-medium">Allowed models</legend>
                <div className="grid grid-cols-2 gap-x-4 gap-y-2">
                  {models.map((m) => (
                    <Checkbox key={m.id} checked={allowed.includes(m.id)} onCheckedChange={(on) => setAllowed((l) => toggle(l, m.id, on))} description={m.provider}>
                      <span className="font-mono">{m.id}</span>
                    </Checkbox>
                  ))}
                </div>
                {tried && allowed.length === 0 && <p className="text-sm text-destructive-foreground">Pick at least one model. Requests to anything else are rejected with 403.</p>}
              </fieldset>

              <fieldset className="flex flex-col gap-2">
                <legend className="mb-1 text-sm font-medium">Allowed regions</legend>
                <div className="flex flex-wrap gap-x-5 gap-y-2">
                  {regions.map((r) => (
                    <Checkbox key={r} checked={allowedRegions.includes(r)} onCheckedChange={(on) => setAllowedRegions((l) => toggle(l, r, on))}>
                      <span className="font-mono">{r}</span>
                    </Checkbox>
                  ))}
                </div>
                {tried && allowedRegions.length === 0 && <p className="text-sm text-destructive-foreground">Pick at least one region.</p>}
              </fieldset>

              <div className="flex flex-col gap-1">
                <span className="text-sm font-medium">Budgets</span>
                {covering.length ? (
                  <ul className="text-sm">
                    {covering.map((b) => (
                      <li key={b.id}>
                        {b.scopeType} budget <span className="font-mono">{b.scope}</span> · ${int(b.capUsd)}/mo, {b.onExceed}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="text-sm text-muted-foreground">No budget covers this team or project yet.</p>
                )}
                <p className="text-xs text-muted-foreground">Every budget on the key's team, project or name applies to it. Manage them on Spend.</p>
              </div>

              <fieldset className="flex flex-col gap-2">
                <legend className="mb-1 text-sm font-medium">
                  Expiry <span className="font-normal text-muted-foreground">· required</span>
                </legend>
                <RadioGroup value={expiry} onValueChange={(v) => setExpiry(v as Expiry)} orientation="horizontal" aria-label="Expiry">
                  <RadioGroupItem value="30">30 days</RadioGroupItem>
                  <RadioGroupItem value="90">90 days</RadioGroupItem>
                  <RadioGroupItem value="365">1 year</RadioGroupItem>
                  <RadioGroupItem value="custom">Custom date</RadioGroupItem>
                  <RadioGroupItem value="never">Never</RadioGroupItem>
                </RadioGroup>
                {expiry === 'custom' && (
                  <Input type="date" value={customDate} onChange={(e) => setCustomDate(e.target.value)} aria-label="Expiry date" className="w-48" />
                )}
                {expiry === 'never' && (
                  <Alert variant="warning">
                    <TriangleAlert />
                    <AlertTitle>A key that never expires stays valid if it leaks.</AlertTitle>
                    <AlertDescription className="flex flex-col gap-2">
                      <span>{dataMode === 'api' ? 'Prefer an expiry and rotate before it.' : "You'll get a rotation reminder every 90 days instead. Prefer an expiry and rotate before it."}</span>
                      <Checkbox checked={neverAck} onCheckedChange={setNeverAck}>
                        I understand this key will not expire
                      </Checkbox>
                    </AlertDescription>
                  </Alert>
                )}
                {tried && !expiryValid && (
                  <p className="text-sm text-destructive-foreground">
                    {expiry === '' ? 'Choose when this key expires.' : expiry === 'custom' ? 'Pick an expiry date.' : 'Confirm that this key will not expire.'}
                  </p>
                )}
              </fieldset>
            </div>
            <DialogFooter>
              <Button variant="outline" type="button" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit">Create key</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

export function RevokeKeyDialog({ apiKey, onOpenChange, onRevoke }: { apiKey: ApiKey | null; onOpenChange: (o: boolean) => void; onRevoke: (k: ApiKey) => Promise<void> }) {
  const [typed, setTyped] = useState('')
  const k = apiKey
  return (
    <Dialog
      open={!!k}
      onOpenChange={(o) => {
        onOpenChange(o)
        if (!o) setTyped('')
      }}
    >
      <DialogContent>
        {k && (
          <form
            className="flex flex-col gap-4"
            onSubmit={async (e) => {
              e.preventDefault()
              if (typed !== k.name) return
              try {
                await onRevoke(k)
                toast.add({ title: 'Key revoked', description: `${k.name} now returns 401 invalid_api_key. Recorded in the audit log.`, type: 'success' })
                setTyped('')
              } catch (err) {
                toast.add({ title: 'Could not revoke key', description: err instanceof Error ? err.message : String(err), type: 'error' })
              }
            }}
          >
            <DialogHeader>
              <DialogTitle>
                Revoke <span className="font-mono">{k.name}</span>?
              </DialogTitle>
              <DialogDescription render={<div />} className="flex flex-col gap-2">
                <span className="text-foreground">
                  {k.requests24h > 0
                    ? `This key made ${int(k.requests24h)} requests in the last 24 hours. Revoking stops them now.`
                    : 'This key made no requests in the last 24 hours. Revoking takes effect immediately.'}
                </span>
                <span>
                  Apps using <span className="font-mono">{k.prefix}…</span> get <span className="font-mono">401 invalid_key</span> on their next call. Revocation can't be
                  undone — issue a new key instead.
                </span>
              </DialogDescription>
            </DialogHeader>
            <Field>
              <FieldLabel>
                Type <span className="font-mono">{k.name}</span> to confirm
              </FieldLabel>
              <Input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" className="font-mono" />
            </Field>
            <DialogFooter>
              <Button variant="outline" type="button" onClick={() => onOpenChange(false)}>
                Keep key
              </Button>
              <Button variant="destructive" type="submit" disabled={typed !== k.name}>
                Revoke key
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** Live rotation status: overlap window and traffic migrating from old secret to new. */
/** "28h" or "45m" until `ms`, rounded down so a deadline is never overstated. */
export function timeLeft(ms: number, now = Date.now()) {
  const m = Math.max(0, Math.floor((ms - now) / 60_000))
  return m < 60 ? `${m}m` : `${Math.floor(m / 60)}h`
}

const windowLength = (ms: number) => (ms >= 7 * 86_400_000 - 60_000 ? '7-day' : `${Math.round(ms / 3_600_000)}h`)

/** Api mode's requests by secret since the rotation started; null where the start isn't recorded. */
function secretCounts(r: KeyRotation | null | undefined) {
  if (r?.oldSecretRequests == null || r.newSecretRequests == null) return null
  return { old: r.oldSecretRequests, new: r.newSecretRequests, unrecorded: r.unrecordedRequests ?? 0, total: r.oldSecretRequests + r.newSecretRequests }
}

/** What retiring the old secret now cuts off, from the recorded traffic. */
function retireBlastRadius(k: ApiKey) {
  const split = k.rotation?.split
  if (split) {
    const n = Math.round(k.requests24h * (1 - split.newShare))
    return n === 0 ? 'No requests used the old secret in the last 24 hours.' : `${int(n)} ${n === 1 ? 'request' : 'requests'} used the old secret in the last 24 hours.`
  }
  const c = secretCounts(k.rotation)
  if (!c) return 'This rotation’s start isn’t recorded, so how many requests still use the old secret isn’t known.'
  if (c.old === 0) return 'No requests have used the old secret since the rotation started.'
  return `${int(c.old)} ${c.old === 1 ? 'request has' : 'requests have'} used the old secret since the rotation started.`
}

const maxOverlapMs = 7 * 86_400_000

export function RotationStatus({ apiKey, className }: { apiKey: ApiKey; className?: string }) {
  const now = useNow(60_000)
  const r = apiKey.rotation
  const ends = r?.endsAt ? new Date(r.endsAt) : null
  const split = r?.split
  const counts = secretCounts(r)
  const pct = split ? Math.round(split.newShare * 100) : counts?.total ? Math.round((counts.new / counts.total) * 100) : 0
  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
        {split ? (
          <span>
            <span className="num font-mono font-semibold">{pct}%</span> of traffic on the new secret
          </span>
        ) : counts?.total ? (
          <span>
            <span className="num font-mono font-semibold">{pct}%</span> of requests since the rotation started used the new secret
          </span>
        ) : (
          counts && <span>No requests on either secret since the rotation started</span>
        )}
        <span className="text-xs text-muted-foreground">
          {ends ? (
            <>
              Overlap window ends {ends.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })},{' '}
              {ends.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })} (
              {r?.startedAt ? `${windowLength(ends.getTime() - r.startedAt)} window, ` : ''}
              {timeLeft(ends.getTime(), now)} left)
            </>
          ) : (
            'The overlap window’s end isn’t recorded for this key.'
          )}
        </span>
      </div>
      {!split && counts ? (
        <>
          {counts.total > 0 && (
            <div className="flex h-2 overflow-hidden rounded-full bg-muted" aria-hidden="true">
              <span className="h-full bg-foreground/70" style={{ width: `${pct}%` }} />
            </div>
          )}
          <dl className="grid grid-cols-2 gap-x-4 font-mono text-xs">
            <div className="flex justify-between">
              <dt className="text-muted-foreground">new secret</dt>
              <dd className="num">{int(counts.new)} req</dd>
            </div>
            <div className="flex justify-between">
              <dt className="text-muted-foreground">old secret</dt>
              <dd className="num">{int(counts.old)} req</dd>
            </div>
          </dl>
          <p className="text-xs text-muted-foreground">
            Both secrets work until the window closes.
            {counts.unrecorded > 0 && ` ${int(counts.unrecorded)} more ${counts.unrecorded === 1 ? 'request' : 'requests'} since the start didn’t record which secret ${counts.unrecorded === 1 ? 'it' : 'they'} used.`}
          </p>
        </>
      ) : split ? (
        <>
          <div className="flex h-2 overflow-hidden rounded-full bg-muted" aria-hidden="true">
            <span className="h-full bg-foreground/70" style={{ width: `${pct}%` }} />
          </div>
          <dl className="grid grid-cols-2 gap-x-4 font-mono text-xs">
            <div className="flex justify-between">
              <dt className="text-muted-foreground">new {apiKey.prefix.replace(/.{4}$/, 'b81e')}…</dt>
              <dd className="num">{int(Math.round(apiKey.requests24h * split.newShare))} req / 24h</dd>
            </div>
            <div className="flex justify-between">
              <dt className="text-muted-foreground">old {apiKey.prefix}…</dt>
              <dd className="num">{int(Math.round(apiKey.requests24h * (1 - split.newShare)))} req / 24h</dd>
            </div>
          </dl>
          <p className="text-xs text-muted-foreground">
            Both secrets work until the window closes.
            {split.oldActors.length > 0 && (
              <>
                {' '}
                Old-secret traffic comes from{' '}
                {split.oldActors.map((a, i) => (
                  <span key={a}>
                    {i > 0 && (i === split.oldActors.length - 1 ? ' and ' : ', ')}
                    <span className="font-mono">{a}</span>
                  </span>
                ))}{' '}
                (by actor).
              </>
            )}
          </p>
        </>
      ) : (
        <p className="text-xs text-muted-foreground">
          Both secrets work until the window closes. The rotation’s start isn’t recorded, so the traffic split between the secrets isn’t shown.
        </p>
      )}
    </div>
  )
}

type Overlap = '1' | '24' | '48' | '168'

export function RotateKeyDialog({ apiKey, onOpenChange, onRotate }: { apiKey: ApiKey | null; onOpenChange: (o: boolean) => void; onRotate: (k: ApiKey) => void }) {
  const [overlap, setOverlap] = useState<Overlap>('48')
  const [secret, setSecret] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [retiring, setRetiring] = useState(false)
  const now = useNow(60_000)
  const k = apiKey
  const close = () => {
    onOpenChange(false)
    setSecret(null)
    setRetiring(false)
  }
  const write = async (title: string, f: () => Promise<void>) => {
    setBusy(true)
    try {
      await f()
      close()
    } catch (e) {
      toast.add({ title, description: e instanceof Error ? e.message : String(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  // Extending adds to the current end, and the server refuses an end past 7 days from now.
  const extendTooFar = !!k?.rotation && Math.max(k.rotation.endsAt ?? now, now) + 24 * 3_600_000 - now > maxOverlapMs
  return (
    <Dialog
      open={!!k}
      onOpenChange={(o) => {
        if (!o && secret) return
        if (!o) close()
      }}
    >
      <DialogContent className="max-w-xl" showCloseButton={!secret}>
        {k && secret ? (
          <SecretOnce
            secret={secret}
            context={`New secret for ${k.name}. The old secret keeps working for ${overlap === '168' ? '7 days' : `${overlap}h`}.`}
            onDone={() => {
              toast.add({
                title: 'Rotation started',
                description: dataMode === 'api' ? `Both secrets work on ${k.name} until the overlap window closes.` : `Watch traffic move to the new secret on ${k.name}.`,
                type: 'success',
              })
              close()
            }}
          />
        ) : k && k.status === 'rotating' && retiring ? (
          <>
            <DialogHeader>
              <DialogTitle>
                Retire the old secret of <span className="font-mono">{k.name}</span>?
              </DialogTitle>
              <DialogDescription>
                {retireBlastRadius(k)} From now on the old secret gets 401, and only the new secret works.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setRetiring(false)}>
                Keep both secrets
              </Button>
              <Button
                variant="destructive"
                disabled={busy}
                onClick={() =>
                  write('Could not retire old secret', async () => {
                    onRotate(await finishRotation(k))
                    toast.add({ title: 'Old secret retired', description: `Only the new secret works on ${k.name} now.`, type: 'success' })
                  })
                }
              >
                Retire old secret
              </Button>
            </DialogFooter>
          </>
        ) : k && k.status === 'rotating' ? (
          <>
            <DialogHeader>
              <DialogTitle>
                Rotation in progress · <span className="font-mono">{k.name}</span>
              </DialogTitle>
              <DialogDescription>
                {k.rotation?.startedAt && k.rotation.startedBy
                  ? `Started by ${k.rotation.startedBy} ${ago(k.rotation.startedAt)}.`
                  : 'The audit log has no record of when this rotation started.'}{' '}
                The old secret stops working when the overlap window closes.
              </DialogDescription>
            </DialogHeader>
            <RotationStatus apiKey={k} />
            {extendTooFar && <p className="text-xs text-muted-foreground">The overlap can’t end more than 7 days from now, so it can’t be extended by another 24h.</p>}
            <DialogFooter>
              <Button variant="outline" onClick={close}>
                Close
              </Button>
              <Button
                variant="outline"
                disabled={busy || extendTooFar}
                onClick={() =>
                  write('Could not extend overlap', async () => {
                    const next = await extendRotation(k, 24)
                    onRotate(next)
                    const ends = next.rotation?.endsAt ? new Date(next.rotation.endsAt) : null
                    toast.add({
                      title: 'Overlap extended by 24h',
                      description: ends ? `Both secrets work until ${ends.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}, ${ends.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })}.` : undefined,
                      type: 'success',
                    })
                  })
                }
              >
                Extend overlap 24h
              </Button>
              <Button variant="destructive" disabled={busy} onClick={() => setRetiring(true)}>
                Retire old secret now
              </Button>
            </DialogFooter>
          </>
        ) : k ? (
          <>
            <DialogHeader>
              <DialogTitle>
                Rotate <span className="font-mono">{k.name}</span>
              </DialogTitle>
              <DialogDescription>
                Issues a new secret. Both secrets work during the overlap window, so apps can switch without downtime.
                {dataMode !== 'api' && " You'll see traffic migrate on the key's page."}
              </DialogDescription>
            </DialogHeader>
            <fieldset className="flex flex-col gap-2">
              <legend className="mb-1 text-sm font-medium">Overlap window</legend>
              <RadioGroup value={overlap} onValueChange={(v) => setOverlap(v as Overlap)} orientation="horizontal" aria-label="Overlap window">
                <RadioGroupItem value="1">1 hour</RadioGroupItem>
                <RadioGroupItem value="24">24 hours</RadioGroupItem>
                <RadioGroupItem value="48">48 hours</RadioGroupItem>
                <RadioGroupItem value="168">7 days</RadioGroupItem>
              </RadioGroup>
            </fieldset>
            <DialogFooter>
              <Button variant="outline" onClick={close}>
                Cancel
              </Button>
              <Button
                disabled={busy}
                onClick={async () => {
                  setBusy(true)
                  try {
                    const res = await rotateKey(k, Number(overlap))
                    setSecret(res.secret)
                    onRotate(res.key)
                  } catch (e) {
                    toast.add({ title: 'Could not rotate key', description: e instanceof Error ? e.message : String(e), type: 'error' })
                  } finally {
                    setBusy(false)
                  }
                }}
              >
                Issue new secret
              </Button>
            </DialogFooter>
          </>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}
