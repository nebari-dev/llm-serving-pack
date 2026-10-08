import { CircleCheck, CircleDashed, KeyRound, Power, TriangleAlert, UserPlus } from 'lucide-react'
import { type ReactNode, useEffect, useState } from 'react'
import { PageHeader } from '@/components/gw/page'
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from '@/components/ui/accordion'
import { StateChip } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { toast } from '@/components/ui/toast'
import { api, type Backend, can, dataMode, type LiveRoute, liveRoutes, type Member, type RetentionView, routes, seedIntegrations, seedMembers, seedProviderKeys, seedRetention, seedSummary, session, type Summary } from '@/data/catalog'
import { age, ago, int } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useApp } from '@/state/app-state'
import { envDisplay } from '@/components/shell/app-header'
import { useLive } from '@/state/live'
import { Link, useLocation } from 'react-router-dom'

// §7.4 Settings → Providers · Retention · Integrations · Members.
// §9.1 credentials, §4.6 retention tiers, §9.3 Warden kill switch.
// Against the control plane: retention is the receipts database's own jobs,
// capture comes from routes, and the kill switch goes through
// POST /warden/passthrough (which calls Warden and writes the audit row).
// Members are who has signed in (GET /members); roles are assigned in Keycloak.

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
  const shownEnv = envDisplay(dataMode, session.environment, env)
  const live = dataMode === 'api'
  const [killOpen, setKillOpen] = useState(false)
  const sess = useLive(live ? '/session' : null, session, 15_000)
  const warden = sess.data.warden
  const retention = useLive<RetentionView | null>(live ? '/retention' : null, seedRetention, 300_000).data
  const day = useLive<Summary>(live ? '/summary?range=24h' : null, seedSummary, 60_000)
  const [mockPassThrough, setMockPassThrough] = useState(false)
  const [switching, setSwitching] = useState(false)
  const passThrough = live ? !!warden?.passthrough : mockPassThrough
  const killRole = can('killswitch')
  const noSwitch = !live
    ? null
    : !killRole.ok
      ? `${killRole.reason}.`
      : !warden
        ? 'This control plane doesn’t know where Warden is, so there’s no kill switch to flip.'
        : !warden.connected
          ? 'Warden isn’t answering, so the kill switch can’t be reached.'
          : null
  const devMode = live && session.auth.mode === 'dev'
  const groupsUrl = session.auth.groupsUrl
  const groupPrefix = session.auth.groupPrefix ?? 'stargate-'
  const members = useLive<Member[]>(live ? '/members' : null, seedMembers, 60_000)
  const liveRouteList = useLive<LiveRoute[]>(live ? '/routes' : null, liveRoutes, 60_000).data
  const capturing = (live ? liveRouteList : routes).filter((r) => r.captureContent)
  const backendList = useLive<Backend[]>(live ? '/backends' : null, [], 60_000)

  // Sections start closed, each with a one-line summary; a link opens one
  // (/settings#members), and the kill switch opens itself while it's on.
  const { hash } = useLocation()
  const [open, setOpen] = useState<string[]>(() => (hash ? [hash.slice(1)] : []))
  useEffect(() => {
    if (passThrough) setOpen((o) => (o.includes('kill-switch') ? o : [...o, 'kill-switch']))
  }, [passThrough])
  const providersSummary = live
    ? backendList.loaded
      ? `${backendList.data.length} ${backendList.data.length === 1 ? 'backend' : 'backends'}${backendList.data.length ? ` · ${backendList.data.map((b) => b.name).join(', ')}` : ''}`
      : ''
    : `${seedProviderKeys.length} providers`
  const retentionSummary = [
    retention?.hotDays ? `Request details ${retention.hotDays} days` : 'Request details kept',
    retention?.aggregates.every((x) => x.dropAfterDays === null) ? 'totals forever' : 'totals kept',
    capturing.length ? `capture on for ${capturing.length} ${capturing.length === 1 ? 'route' : 'routes'}` : 'no capture',
  ].join(' · ')
  const membersSummary = `${members.data.length} ${members.data.length === 1 ? 'member' : 'members'}${devMode ? ' · dev mode, no sign-in' : ''}`
  const integrationsSummary = live
    ? `${warden?.connected ? 'Warden connected' : 'Warden not connected'} · ${devMode ? 'sign-in, ' : ''}OpenTelemetry and Argo CD not set up`
    : `${seedIntegrations.length + 1} integrations`

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

      <Accordion multiple value={open} onValueChange={(v) => setOpen(v as string[])} className="px-6">
      <SettingsSection id="providers" title="Providers" summary={providersSummary}>
        {live ? (
          <p className="max-w-3xl text-sm text-muted-foreground-strong">
            {backendList.loaded ? `${backendList.data.length} ${backendList.data.length === 1 ? 'backend' : 'backends'}: ${backendList.data.map((b) => b.name).join(', ') || 'none yet'}. ` : 'Loading… '}
            Their keys, connection tests and models are on{' '}
            <Link to="/routing?tab=backends" className="underline">
              Routing → Backends
            </Link>
            . A key is tested once, then sealed: no screen or API ever returns it.
          </p>
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
      </SettingsSection>

      <SettingsSection id="retention" title="Data kept" summary={retentionSummary}>
        <dl className="grid max-w-3xl grid-cols-[11rem_1fr] gap-x-6 gap-y-3 text-sm">
          <dt className="font-medium">Request details</dt>
          <dd>
            {!retention ? (
              <span className="text-muted-foreground">Loading…</span>
            ) : retention.hotDays === null ? (
              <>
                <StateChip tone="degraded">No retention policy</StateChip> Every request’s details are kept indefinitely.
              </>
            ) : (
              <>
                Kept <span className="num font-mono">{days(retention.hotDays)}</span>: who sent each request, the model, tokens, cost and its decision trace.{' '}
                <span className="text-muted-foreground">Replay can look back this far.</span>
              </>
            )}
            {live && retention?.oldestReceiptAt && <div className="text-xs text-muted-foreground">Oldest kept: {new Date(retention.oldestReceiptAt).toISOString().slice(0, 10)}.</div>}
          </dd>
          <dt className="font-medium">Totals for charts</dt>
          <dd>
            {retention?.aggregates.every((x) => x.dropAfterDays === null) ? 'Kept forever' : 'Kept'}: the 5-minute and daily totals that Spend, Overview and Activity draw. No per-request
            detail.
          </dd>
          <dt className="font-medium">Prompts and responses</dt>
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
            <span className="text-xs text-muted-foreground">
              Turn it on or off per route on <Link to="/routing" className="underline">Routing</Link>; it needs the security or admin role. Kept content is masked and dropped after 30 days.
            </span>
          </dd>
        </dl>
      </SettingsSection>

      <SettingsSection id="integrations" title="Integrations" summary={integrationsSummary}>
        <ul className="divide-y divide-border rounded-md border border-border">
          {[
            ...(live
              ? [
                  ...['OTel collector', 'Argo CD'].map((name) => ({ name, detail: 'Not connected yet: the control plane doesn’t report on it.', ok: null })),
                  devMode
                    ? { name: 'Keycloak OIDC', detail: 'Not configured: dev mode, everyone is the dev user. Start the control plane with -oidc-issuer to sign in.', ok: null }
                    : { name: 'Keycloak OIDC', detail: `Signed in as ${session.actor.email} · groups ${groupPrefix}<role> → roles`, ok: true },
                ]
              : seedIntegrations),
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
      </SettingsSection>

      <SettingsSection id="members" title="Members" summary={membersSummary}>
        <div className="mb-3 flex justify-end">
          {groupsUrl ? (
            <Button variant="outline" size="sm" render={<a href={groupsUrl} target="_blank" rel="noreferrer" />}>
              <UserPlus /> Manage in Keycloak
            </Button>
          ) : (
            <Button variant="outline" size="sm" disabled title={devMode ? 'Dev mode: no identity provider is configured.' : 'Roles are assigned in Keycloak.'}>
              <UserPlus /> Manage in Keycloak
            </Button>
          )}
        </div>
        <p className="mb-3 max-w-3xl text-sm text-muted-foreground">
          {devMode ? (
            <>
              No identity provider is configured (dev mode): there’s no sign-in, and every change is made as <span className="font-mono">{session.actor.email}</span>, who is{' '}
              {session.actor.roles.join(', ') || 'no role'}. Start the control plane with <span className="font-mono">-oidc-issuer</span> to sign in with Keycloak.
            </>
          ) : (
            <>
              Roles are assigned in Keycloak, not here: someone in group <span className="font-mono">{groupPrefix}admin</span> is an admin, and so on for each role. This list is who has signed in, with the roles their last sign-in
              carried. Only an owner assigns roles.
            </>
          )}
        </p>
        <table className="w-full text-sm" aria-label="Members">
          <thead className="text-left text-xs text-muted-foreground">
            <tr className="border-b border-border">
              <th className="py-1.5 pr-3 font-medium">Name</th>
              <th className="py-1.5 pr-3 font-medium">Roles</th>
              <th className="py-1.5 pr-3 font-medium">Can</th>
              <th className="py-1.5 text-right font-medium">Last active</th>
            </tr>
          </thead>
          <tbody>
            {members.data.length === 0 && (
              <tr>
                <td colSpan={4} className="py-2 text-xs text-muted-foreground">
                  {members.loaded ? 'Nobody has signed in yet.' : 'Loading…'}
                </td>
              </tr>
            )}
            {members.data.map((m) => (
              <tr key={m.email} className="border-b border-border last:border-0">
                <td className="py-2 pr-3">
                  {m.name || m.email}
                  {m.name && <div className="text-xs text-muted-foreground">{m.email}</div>}
                </td>
                <td className="py-2 pr-3">
                  <span className="inline-flex flex-wrap gap-1">
                    {m.roles.length === 0 && <span className="text-xs text-muted-foreground">no role</span>}
                    {m.roles.map((r) => (
                      <span key={r} className="rounded-sm border border-border bg-muted px-1.5 text-xs leading-5 text-muted-foreground-strong">
                        {r}
                      </span>
                    ))}
                  </span>
                </td>
                <td className="py-2 pr-3 text-xs text-muted-foreground">{m.roles[0] ? roleHelp[m.roles[0]] : 'Nothing until an owner adds them to a group'}</td>
                <td className="py-2 text-right text-xs text-muted-foreground">{ago(m.lastSeenAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {!live && <p className="mt-2 text-xs text-muted-foreground">Service accounts get scoped tokens for CI.</p>}
      </SettingsSection>

      <SettingsSection id="environment" title="Environment" summary={shownEnv.label}>
        <p className="max-w-3xl text-sm text-muted-foreground-strong">
          You are in <span className="font-medium text-foreground">{shownEnv.label}</span>. Production shows a solid accent band across the top of every screen; every other
          environment shows a hatched one, so the two are never told apart by a label alone.{' '}
          {live ? (
            <>
              It’s the environment this control plane serves, set when it starts (<span className="font-mono">-environment</span>).
            </>
          ) : (
            'The accent is set here, per environment.'
          )}
        </p>
        <div className="mt-3 flex flex-wrap gap-6 text-sm">
          <span className="inline-flex items-center gap-2">
            <span className="h-3 w-12 rounded-sm bg-env-production" aria-hidden="true" /> Production · solid
          </span>
          <span className="inline-flex items-center gap-2">
            <span className="h-3 w-12 rounded-sm bg-[repeating-linear-gradient(135deg,var(--env-staging)_0_6px,transparent_6px_12px)]" aria-hidden="true" /> {live ? 'Any other · hatched' : 'Staging · hatched'}
          </span>
        </div>
      </SettingsSection>

      <SettingsSection id="kill-switch" title="Warden kill switch" summary={passThrough ? 'Pass-through is ON: nothing is being checked' : 'Policing normally'}>
        <div className={cn('max-w-3xl rounded-md p-3', passThrough && 'border border-v-blocked-border bg-v-blocked-bg')}>
          <p className="text-sm text-muted-foreground-strong">
            For emergencies. If guardrails are breaking traffic, pass-through lets every request through unchecked: no key checks, rules, redaction or budgets. Receipts keep recording.{' '}
            {live ? 'It takes effect at once, and Warden forgets it if it restarts.' : 'It takes effect on all gateway pods within seconds, without a rollout.'}
          </p>
          <div className="mt-3 flex items-center gap-3">
            <Switch
              checked={passThrough}
              disabled={switching || !!noSwitch}
              onCheckedChange={(on) => (on ? setKillOpen(true) : setPassThrough(false))}
              aria-label="Warden pass-through"
            />
            <span className={cn('text-sm', passThrough ? 'font-medium text-v-blocked-fg' : 'text-foreground')}>{passThrough ? 'Pass-through is on' : 'Policing normally'}</span>
          </div>
          {noSwitch && <p className="mt-2 text-xs text-muted-foreground">{noSwitch}</p>}
        </div>
      </SettingsSection>
    </Accordion>

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

/** One collapsible section: its title and a one-line summary, then its content. */
function SettingsSection({ id, title, summary, children }: { id: string; title: string; summary: string; children: ReactNode }) {
  return (
    <AccordionItem value={id} id={id}>
      <AccordionTrigger headingLevel={2} className="py-4">
        <span className="text-base font-semibold">{title}</span>
        {summary && <span className="ml-3 font-normal text-muted-foreground">{summary}</span>}
      </AccordionTrigger>
      <AccordionContent>
        <div className="text-foreground">{children}</div>
      </AccordionContent>
    </AccordionItem>
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
