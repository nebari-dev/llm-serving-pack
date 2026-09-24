import { CircleCheck, KeyRound, Power, TriangleAlert, UserPlus } from 'lucide-react'
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
import { cn } from '@/lib/utils'
import { useApp } from '@/state/app-state'

// §7.4 Settings → Providers · Retention · Integrations · Members.
// §9.1 credentials, §4.6 retention tiers, §9.3 Warden kill switch.

const providerKeys = [
  { backend: 'openai-prod', provider: 'OpenAI', auth: 'API key', prefix: 'sk-proj-…Q7f', lastTested: '2026-09-02', rotateBy: '2026-12-01', oidc: false },
  { backend: 'anthropic-prod', provider: 'Anthropic', auth: 'API key', prefix: 'sk-ant-…m2Xa', lastTested: '2026-06-11', rotateBy: '2026-09-11', oidc: false },
  { backend: 'bedrock-eu', provider: 'Bedrock', auth: 'Cloud OIDC (short-lived)', prefix: 'role/gw-bedrock-eu', lastTested: 'on every request', rotateBy: null, oidc: true },
  { backend: 'azure-openai-eu', provider: 'Azure', auth: 'Workload identity', prefix: 'mi-gw-azure-eu', lastTested: 'failing', rotateBy: null, oidc: true },
  { backend: 'vllm-internal', provider: 'Self-hosted', auth: 'mTLS (cert-manager)', prefix: 'CN=gw-vllm', lastTested: '2026-09-20', rotateBy: '2026-11-20', oidc: true },
]

const members = [
  { name: 'Priya Shah', email: 'priya@acme.dev', role: 'admin', last: 'now' },
  { name: 'Dana Okafor', email: 'dana@acme.dev', role: 'finance', last: '2h ago' },
  { name: 'Marco Rossi', email: 'marco@acme.dev', role: 'security', last: '5h ago' },
  { name: 'Lee Tran', email: 'lee@acme.dev', role: 'owner', last: '3d ago' },
  { name: 'Sam Patel', email: 'sam@acme.dev', role: 'editor', last: '1d ago' },
  { name: 'ci-gitops', email: 'service account', role: 'viewer', last: '12m ago' },
]

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
  const [killOpen, setKillOpen] = useState(false)
  const [passThrough, setPassThrough] = useState(false)

  return (
    <div className="flex flex-col">
      <PageHeader title="Settings" description="Tenant acme · changes here write audit records like everything else." />

      {passThrough && (
        <div role="alert" className="flex items-center gap-3 border-b border-v-blocked-border bg-v-blocked-bg px-6 py-2 text-sm text-v-blocked-fg">
          <Power className="size-4" aria-hidden="true" />
          <span className="font-medium">Warden is in pass-through.</span>
          <span className="text-foreground/80">No policies, redaction, or budget checks are running. Receipts still record traffic.</span>
          <Button size="xs" variant="outline" className="ml-auto" onClick={() => (setPassThrough(false), toast.add({ title: 'Warden policing resumed', type: 'success' }))}>
            Resume policing
          </Button>
        </div>
      )}

      <Section
        id="providers"
        title="Providers"
        description="Provider keys never leave the cluster and are never returned by the API — not even to owners. Tested once, then sealed. Never returned to callers."
      >
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
              {providerKeys.map((p) => {
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
        <p className="mt-2 text-xs text-muted-foreground">Providers without a cloud identity story get a rotation reminder every 90 days.</p>
      </Section>

      <Section id="retention" title="Retention" description="Receipts store hashes and decision traces, not prompt content, unless a route opts into capture.">
        <dl className="grid max-w-3xl grid-cols-[10rem_1fr] gap-x-6 gap-y-3 text-sm">
          <dt className="font-medium">Hot tier</dt>
          <dd>
            <span className="num font-mono">30 days</span> — full per-request receipts and decision traces.{' '}
            <span className="text-muted-foreground">This is also the longest window a rule can be replayed against.</span>
          </dd>
          <dt className="font-medium">Cold tier</dt>
          <dd>
            <span className="num font-mono">7 years</span> — daily aggregates and verdict summaries. No per-request detail. Never dropped.
          </dd>
          <dt className="font-medium">Content capture</dt>
          <dd className="flex flex-wrap items-center gap-2">
            Off by default. On for 1 route:
            <span className="font-mono">eu-private</span>
            <StateChip tone="degraded" className="font-semibold">
              Content capture on
            </StateChip>
            <span className="text-xs text-muted-foreground">Changing capture requires the security role.</span>
          </dd>
        </dl>
      </Section>

      <Section id="integrations" title="Integrations">
        <ul className="divide-y divide-border rounded-md border border-border">
          {[
            { name: 'OTel collector', detail: 'otel-collector.nebari-gateway:4317 · 412 receipts/s · lag 0.8s', ok: true },
            { name: 'Argo CD', detail: 'Watching github.com/acme/platform-gitops · 2 backends, 1 route, 1 alias declarative', ok: true },
            { name: 'Keycloak OIDC', detail: 'realm acme · client nebari-gateway-console · groups → roles mapped', ok: true },
            { name: 'Warden config snapshot', detail: 'Snapshot v1842 · cache age 4m 12s on 2 of 6 pods', ok: false },
          ].map((i) => (
            <li key={i.name} className="flex items-center gap-3 px-3 py-2.5 text-sm">
              {i.ok ? <CircleCheck className="size-4 text-v-allowed-fg" aria-hidden="true" /> : <TriangleAlert className="size-4 text-v-degraded-fg" aria-hidden="true" />}
              <span className="w-48 font-medium">{i.name}</span>
              <span className="text-muted-foreground-strong">{i.detail}</span>
              <span className="sr-only">{i.ok ? 'Connected' : 'Degraded'}</span>
            </li>
          ))}
        </ul>
      </Section>

      <Section
        id="members"
        title="Members"
        actions={
          <Button variant="outline" size="sm">
            <UserPlus /> Invite member
          </Button>
        }
      >
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
            {members.map((m) => (
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
        <p className="mt-2 text-xs text-muted-foreground">Roles come from Keycloak groups. Service accounts get scoped tokens for CI.</p>
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
              Every request skips identity checks, rules, redaction, and budget enforcement. Traffic keeps flowing and receipts keep recording. Takes effect on all gateway pods within seconds, without a rollout.
            </p>
            <div className="mt-3 flex items-center gap-3">
              <Switch
                checked={passThrough}
                onCheckedChange={(on) => (on ? setKillOpen(true) : (setPassThrough(false), toast.add({ title: 'Warden policing resumed', type: 'success' })))}
                aria-label="Warden pass-through"
              />
              <span className="text-sm text-foreground">{passThrough ? 'Pass-through is on' : 'Policing normally'}</span>
            </div>
          </AlertDescription>
        </Alert>
      </Section>

      <KillSwitchDialog
        open={killOpen}
        onOpenChange={setKillOpen}
        onConfirm={() => {
          setPassThrough(true)
          setKillOpen(false)
          toast.add({ title: 'Warden is in pass-through', description: 'Audit record written.', type: 'warning' })
        }}
      />
    </div>
  )
}

function KillSwitchDialog({ open, onOpenChange, onConfirm }: { open: boolean; onOpenChange: (o: boolean) => void; onConfirm: () => void }) {
  const [typed, setTyped] = useState('')
  const phrase = 'pass-through production'
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
            <p>
              In the last 24 hours Warden blocked <span className="num font-mono text-foreground">1,412</span> requests and redacted{' '}
              <span className="num font-mono text-foreground">4,806</span>. With pass-through on, all of those would reach providers unchanged — including PII that <span className="font-mono">no-pii-out</span> redacts today.
            </p>
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
          <Button variant="destructive" disabled={typed !== phrase} onClick={onConfirm}>
            <Power /> Turn on pass-through
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
