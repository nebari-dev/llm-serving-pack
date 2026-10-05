import { CircleCheck, CircleDashed, KeyRound, Power, TriangleAlert, UserPlus } from 'lucide-react'
import { useState } from 'react'
import { PageHeader, Section } from '@/components/gw/page'
import { StateChip } from '@/components/gw/verdict'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/toast'
import { api, dataMode, liveRoutes, type RetentionView, routes, seedIntegrations, seedMembers, seedProviderKeys, seedRetention, seedSummary, session, type Summary } from '@/data/catalog'
import { age, int } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useApp } from '@/state/app-state'
import { useLive } from '@/state/live'

// §7.4 Settings → Providers · Retention · Integrations · Members.
// §9.1 credentials, §4.6 retention tiers, §9.3 Warden kill switch.
// Against the control plane: retention is the receipts database's own jobs,
// capture comes from routes, and the kill switch goes through
// POST /warden/passthrough (which calls Warden and writes the audit row).
// Providers, members and the other integrations have no backend yet.

/** "30 days", "7 years". */
const days = (d: number) => (d >= 365 && d % 365 === 0 ? `${d / 365} years` : `${int(d)} ${d === 1 ? 'day' : 'days'}`)

const roleHelp: Record<string, string> = {
  owner: 'Everything, including members and the kill switch',
  admin: 'Configure providers, routes, keys, policies',
  editor: 'Draft and apply config; cannot publish policies',
  viewer: 'Read-only',
  finance: 'Spend, budgets, exports',
  security: 'Guardrails, content reveal, audit export',
}

export function SettingsPage() {
  const { env } = useApp()
  const live = dataMode === 'api'
  const [killOpen, setKillOpen] = useState(false)
  const sess = useLive(live ? '/session' : null, session, 15_000)
  const warden = sess.data.warden
  const retention = useLive<RetentionView | null>(live ? '/retention' : null, seedRetention, 300_000).data
  const day = useLive<Summary>(live ? '/summary?range=24h' : null, seedSummary, 60_000)
  const [mockPassThrough, setMockPassThrough] = useState(false)
  const [switching, setSwitching] = useState(false)
  const passThrough = live ? !!warden?.passthrough : mockPassThrough
  const noSwitch = !live ? null : !warden ? 'This control plane doesn’t know where Warden is, so there’s no kill switch to flip.' : !warden.connected ? 'Warden isn’t answering, so the kill switch can’t be reached.' : null
  const capturing = (dataMode === 'api' ? liveRoutes : routes).filter((r) => r.captureContent)

  const setPassThrough = async (on: boolean) => {
    if (!live) {
      setMockPassThrough(on)
    } else {
      setSwitching(true)
      try {
        await api('/warden/passthrough', { method: 'POST', body: JSON.stringify({ on }) })
      } catch (e) {
        toast.add({ title: on ? 'Pass-through wasn’t turned on' : 'Policing didn’t resume', description: (e as Error).message, type: 'error' })
        return
      } finally {
        setSwitching(false)
        sess.reload()
      }
    }
    toast.add(on ? { title: 'Warden is in pass-through', description: 'Audit record written.', type: 'warning' } : { title: 'Warden policing resumed', description: live ? 'Audit record written.' : undefined, type: 'success' })
  }

  return (
    <div className="flex flex-col">
      <PageHeader title="Settings" description={`Tenant ${session.tenant.name} · changes here write audit records like everything else.`} />

      {passThrough && (
        <div role="alert" className="flex items-center gap-3 border-b border-v-blocked-border bg-v-blocked-bg px-6 py-2 text-sm text-v-blocked-fg">
          <Power className="size-4" aria-hidden="true" />
          <span className="font-medium">Warden is in pass-through.</span>
          <span className="text-foreground/80">No policies, redaction, or budget checks are running. Receipts still record traffic.</span>
          <Button size="xs" variant="outline" className="ml-auto" disabled={switching || !!noSwitch} onClick={() => setPassThrough(false)}>
            Resume policing
          </Button>
        </div>
      )}

      <Section
        id="providers"
        title="Providers"
        description="Provider keys never leave the cluster and are never returned by the API — not even to owners. Tested once, then sealed. Never returned to callers."
      >
        {live ? (
          <p className="max-w-3xl text-sm text-muted-foreground">Provider credentials aren’t connected yet: the control plane doesn’t hold or test provider keys, so there’s nothing to list or rotate here.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[48rem] text-sm">
              <thead className="text-left text-xs text-muted-foreground">
                <tr className="border-b border-border">
                  <th className="py-1.5 pr-3 font-medium">Backend</th>
                  <th className="py-1.5 pr-3 font-medium">Credential</th>
                  <th className="py-1.5 pr-3 font-medium">Identifier</th>
                  <th className="py-1.5 pr-3 font-medium">Last tested</th>
                  <th className="py-1.5 pr-3 font-medium">Rotation</th>
                  <th className="py-1.5 font-medium">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {seedProviderKeys.map((p) => {
                  const overdue = p.rotateBy !== null && p.rotateBy < '2026-09-24'
                  return (
                    <tr key={p.backend} className="border-b border-border last:border-0">
                      <td className="py-2 pr-3">
                        <span className="font-mono">{p.backend}</span>
                        <div className="text-xs text-muted-foreground">{p.provider}</div>
                      </td>
                      <td className="py-2 pr-3 text-xs">{p.auth}</td>
                      <td className="py-2 pr-3 font-mono text-xs text-muted-foreground-strong">{p.prefix}</td>
                      <td className={cn('py-2 pr-3 text-xs', p.lastTested === 'failing' && 'text-v-blocked-fg')}>{p.lastTested}</td>
                      <td className="py-2 pr-3">
                        {p.oidc && p.rotateBy === null ? (
                          <span className="text-xs text-muted-foreground">Not needed · short-lived</span>
                        ) : overdue ? (
                          <StateChip tone="degraded" icon={<TriangleAlert className="size-3" aria-hidden="true" />}>
                            Rotate — due {p.rotateBy}
                          </StateChip>
                        ) : (
                          <span className="num font-mono text-xs">by {p.rotateBy}</span>
                        )}
                      </td>
                      <td className="py-2 text-right">
                        {!(p.oidc && p.rotateBy === null) && (
                          <Button variant={overdue ? 'default' : 'ghost'} size="xs" onClick={() => toast.add({ title: `Paste the new ${p.provider} key to rotate ${p.backend}`, type: 'info' })}>
                            <KeyRound /> Replace key
                          </Button>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
        {!live && <p className="mt-2 text-xs text-muted-foreground">Providers without a cloud identity story get a rotation reminder every 90 days.</p>}
      </Section>

      <Section id="retention" title="Retention" description="Receipts store hashes and decision traces, not prompt content, unless a route opts into capture.">
        <dl className="grid max-w-3xl grid-cols-[10rem_1fr] gap-x-6 gap-y-3 text-sm">
          <dt className="font-medium">Hot tier</dt>
          <dd>
            {!retention ? (
              <span className="text-muted-foreground">Loading…</span>
            ) : retention.hotDays === null ? (
              <>
                <StateChip tone="degraded">No retention policy</StateChip> Raw receipts are kept indefinitely.
              </>
            ) : (
              <>
                <span className="num font-mono">{days(retention.hotDays)}</span> — full per-request receipts and decision traces
                {retention.compressAfterDays !== null && <>, compressed after {days(retention.compressAfterDays)}</>}.{' '}
                <span className="text-muted-foreground">This is also the longest window a rule can be replayed against.</span>
              </>
            )}
            {live && retention?.oldestReceiptAt && <div className="text-xs text-muted-foreground">Oldest receipt kept: {new Date(retention.oldestReceiptAt).toISOString().slice(0, 10)}.</div>}
          </dd>
          <dt className="font-medium">Cold tier</dt>
          <dd>
            {retention?.aggregates.map((a) => (
              <div key={a.name}>
                <span className="font-mono">{a.name}</span> — {a.dropAfterDays === null ? 'Never dropped.' : <>kept <span className="num font-mono">{days(a.dropAfterDays)}</span>.</>}
              </div>
            ))}
            {retention && <div className="text-muted-foreground">Aggregates and verdict counts only. No per-request detail.</div>}
          </dd>
          <dt className="font-medium">Content capture</dt>
          <dd className="flex flex-wrap items-center gap-2">
            {capturing.length === 0 ? (
              'Off on every route.'
            ) : (
              <>
                Off by default. On for {capturing.length} {capturing.length === 1 ? 'route' : 'routes'}:
                {capturing.map((r) => (
                  <span key={r.name} className="font-mono">
                    {r.name}
                  </span>
                ))}
                <StateChip tone="degraded" className="font-semibold">
                  Content capture on
                </StateChip>
              </>
            )}
            <span className="text-xs text-muted-foreground">{live ? 'Changing capture isn’t connected yet.' : 'Changing capture requires the security role.'}</span>
          </dd>
        </dl>
      </Section>

      <Section id="integrations" title="Integrations">
        <ul className="divide-y divide-border rounded-md border border-border">
          {[
            ...(live ? ['OTel collector', 'Argo CD', 'Keycloak OIDC'].map((name) => ({ name, detail: 'Not connected yet: the control plane doesn’t report on it.', ok: null })) : seedIntegrations),
            wardenIntegration(warden),
          ].map((i) => (
            <li key={i.name} className="flex items-center gap-3 px-3 py-2.5 text-sm">
              {i.ok === null ? (
                <CircleDashed className="size-4 text-muted-foreground" aria-hidden="true" />
              ) : i.ok ? (
                <CircleCheck className="size-4 text-v-allowed-fg" aria-hidden="true" />
              ) : (
                <TriangleAlert className="size-4 text-v-degraded-fg" aria-hidden="true" />
              )}
              <span className="w-48 font-medium">{i.name}</span>
              <span className={i.ok === null ? 'text-muted-foreground' : 'text-muted-foreground-strong'}>{i.detail}</span>
              <span className="sr-only">{i.ok === null ? 'Not connected' : i.ok ? 'Connected' : 'Degraded'}</span>
            </li>
          ))}
        </ul>
      </Section>

      <Section
        id="members"
        title="Members"
        actions={
          <Button variant="outline" size="sm" disabled={live} title={live ? 'Inviting members isn’t connected yet.' : undefined}>
            <UserPlus /> Invite member
          </Button>
        }
      >
        {live ? (
          <p className="max-w-3xl text-sm text-muted-foreground">
            Members aren’t connected yet: there’s no OIDC sign-in, so every change is made as <span className="font-mono">{session.actor.email}</span>.
          </p>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-muted-foreground">
              <tr className="border-b border-border">
                <th className="py-1.5 pr-3 font-medium">Name</th>
                <th className="py-1.5 pr-3 font-medium">Role</th>
                <th className="py-1.5 pr-3 font-medium">Can</th>
                <th className="py-1.5 text-right font-medium">Last active</th>
              </tr>
            </thead>
            <tbody>
              {seedMembers.map((m) => (
                <tr key={m.email} className="border-b border-border last:border-0">
                  <td className="py-2 pr-3">
                    {m.name}
                    <div className="text-xs text-muted-foreground">{m.email}</div>
                  </td>
                  <td className="py-2 pr-3">
                    <span className="rounded-sm border border-border bg-muted px-1.5 text-xs leading-5 text-muted-foreground-strong">{m.role}</span>
                  </td>
                  <td className="py-2 pr-3 text-xs text-muted-foreground">{roleHelp[m.role]}</td>
                  <td className="py-2 text-right text-xs text-muted-foreground">{m.last}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {!live && <p className="mt-2 text-xs text-muted-foreground">Roles come from Keycloak groups. Service accounts get scoped tokens for CI.</p>}
      </Section>

      <Section id="environment" title="Environment">
        <p className="max-w-3xl text-sm text-muted-foreground-strong">
          You are in <span className="font-medium text-foreground">{env === 'production' ? 'Production' : 'Staging'}</span>. Production shows a solid accent band across the top of every screen; staging shows a hatched one. The accent is set here, per environment, so the
          two are never told apart by a dropdown label alone.
        </p>
        <div className="mt-3 flex flex-wrap gap-6 text-sm">
          <span className="inline-flex items-center gap-2">
            <span className="h-3 w-12 rounded-sm bg-env-production" aria-hidden="true" /> Production · solid
          </span>
          <span className="inline-flex items-center gap-2">
            <span className="h-3 w-12 rounded-sm bg-[repeating-linear-gradient(135deg,var(--env-staging)_0_6px,transparent_6px_12px)]" aria-hidden="true" /> Staging · hatched
          </span>
        </div>
      </Section>

      <Section id="kill-switch" title="Warden kill switch">
        <Alert variant="destructive" className="max-w-3xl">
          <Power />
          <AlertTitle>Put Warden into pass-through.</AlertTitle>
          <AlertDescription>
            <p>
              Every request skips identity checks, rules, redaction, and budget enforcement. Traffic keeps flowing and receipts keep recording.{' '}
              {live
                ? 'Takes effect on Warden immediately, without a rollout. Warden holds it in memory: a restart turns it back off.'
                : 'Takes effect on all gateway pods within seconds, without a rollout.'}
            </p>
            <div className="mt-3 flex items-center gap-3">
              <Switch
                checked={passThrough}
                disabled={switching || !!noSwitch}
                onCheckedChange={(on) => (on ? setKillOpen(true) : setPassThrough(false))}
                aria-label="Warden pass-through"
              />
              <span className="text-sm text-foreground">{passThrough ? 'Pass-through is on' : 'Policing normally'}</span>
            </div>
            {noSwitch && <p className="mt-2 text-xs text-muted-foreground">{noSwitch}</p>}
          </AlertDescription>
        </Alert>
      </Section>

      <KillSwitchDialog
        open={killOpen}
        onOpenChange={setKillOpen}
        phrase={`pass-through ${session.environment}`}
        day={day.loaded ? day.data.current : null}
        busy={switching}
        onConfirm={async () => {
          await setPassThrough(true)
          setKillOpen(false)
        }}
      />
    </div>
  )
}

/** The Warden row under Integrations, from /session. */
function wardenIntegration(w: typeof session.warden): { name: string; detail: string; ok: boolean | null } {
  const name = 'Warden config snapshot'
  if (!w) return { name, detail: 'Not in this control plane’s request path.', ok: null }
  if (!w.connected) return { name, detail: 'Warden isn’t answering.', ok: false }
  const sec = w.snapshotAgeSeconds ?? 0
  return { name, detail: `${w.version ? `Warden ${w.version} · ` : ''}cache age ${age(sec)}${sec > 60 ? ', older than a minute' : ''}`, ok: sec <= 60 }
}

function KillSwitchDialog({
  open,
  onOpenChange,
  onConfirm,
  phrase,
  day,
  busy,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  onConfirm: () => void
  phrase: string
  day: Summary['current'] | null
  busy: boolean
}) {
  const [typed, setTyped] = useState('')
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o)
        if (!o) setTyped('')
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Stop policing all traffic?</DialogTitle>
          <DialogDescription render={<div />} className="flex flex-col gap-2">
            {day ? (
              <p>
                In the last 24 hours Warden blocked <span className="num font-mono text-foreground">{int(day.blocked)}</span> requests and redacted{' '}
                <span className="num font-mono text-foreground">{int(day.redacted)}</span>. With pass-through on, requests like those would reach providers unchanged.
              </p>
            ) : (
              <p>With pass-through on, requests Warden would block or redact reach providers unchanged.</p>
            )}
            <p>Data-protection policies set to fail-closed will not apply.</p>
          </DialogDescription>
        </DialogHeader>
        <Field>
          <FieldLabel>
            Type <span className="font-mono">{phrase}</span> to confirm
          </FieldLabel>
          <Input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" className="font-mono" />
          <FieldDescription>Writes an audit record under your name.</FieldDescription>
        </Field>
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>Keep policing</DialogClose>
          <Button variant="destructive" disabled={typed !== phrase || busy} onClick={onConfirm}>
            <Power /> Turn on pass-through
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
