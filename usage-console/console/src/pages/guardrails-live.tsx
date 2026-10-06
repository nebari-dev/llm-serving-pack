import { ArrowDown, ArrowUp, History, Lock, Plus, Save, Send, Trash2, TriangleAlert, Undo2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { DiffView } from '@/components/gw/diff-view'
import { PageHeader, Section } from '@/components/gw/page'
import { StateChip } from '@/components/gw/verdict'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import {
  ApiError,
  can,
  createPolicy,
  deletePolicy,
  discardPolicyDraft,
  planPublish,
  type PolicyContent,
  type PolicyPublishPlan,
  type PolicyVersion,
  type PolicyView,
  policies as catalogPolicies,
  type PublishMode,
  publishPolicy,
  reorderPolicies,
  rollbackPolicy,
  type RuleVocabulary,
  savePolicyDraft,
  syncPolicies,
} from '@/data/catalog'
import { ago, int } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useLive } from '@/state/live'
import { PolicyBuilder } from './guardrails-builder'
import { ReplayPane, ReplaySummary } from './guardrails-replay-live'
import { LiveDetectorsTab } from './guardrails-detectors-live'
import { blankPolicyDraft, fromPolicyContent, lineDiff, modeChip, type PolicyDraft, policyLines, ruleProblems, samePolicy, toPolicyContent } from './guardrails-model'

// §7.5.7 Guardrails in api mode, built on §5.2's policies: a policy is an
// ordered list of rules with one mode and one fail mode. The page lists
// policies in evaluation order; opening one shows its rules in order to edit
// as a draft, which publishes as the policy's next immutable version (monitor
// mode first) and rolls back as a unit. It offers only what the engine
// evaluates. Replay runs the builder's rules over recorded traffic with
// Warden's evaluator (guardrails-replay-live.tsx).

const NEW = 'new'

/** A local edit, and the policy as it was when the edit began (null for a new policy). */
type Edit = { draft: PolicyDraft; base: PolicyView | null }

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))
const utc = (ms: number) => new Date(ms).toISOString().slice(0, 16).replace('T', ' ') + ' UTC'
const contentOf = (p: PolicyView): PolicyContent => p.draft ?? p

export function LiveGuardrailsPage() {
  const [params, setParams] = useSearchParams()
  const live = useLive<PolicyView[]>('/policies', catalogPolicies as PolicyView[])
  const vocab = useLive<RuleVocabulary | null>('/rules/vocabulary', null, 300_000)
  // A write's answer shows at once, until the next fetch replaces it.
  const [local, setLocal] = useState<{ from: PolicyView[]; rows: PolicyView[] } | null>(null)
  const rows = local && local.from === live.data ? local.rows : live.data
  const [edits, setEdits] = useState<Record<string, Edit>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState<PolicyView | null>(null)
  const [dialog, setDialog] = useState<null | 'publish' | 'discard' | 'delete'>(null)

  useEffect(() => {
    if (live.loaded) syncPolicies(live.data)
  }, [live.data, live.loaded])

  // Built once per fetched row, so the builder's row keys hold still between renders.
  const saved = useMemo(() => Object.fromEntries(rows.map((p) => [p.id, fromPolicyContent(contentOf(p), p.id)])), [rows])

  // ?rule= is what links from receipts, Overview and the palette carry: the policy's id.
  const param = params.get('policy') ?? params.get('rule')
  const selectedId = param === NEW && edits[NEW] ? NEW : param && rows.some((p) => p.id === param) ? param : (rows[0]?.id ?? null)
  const view = rows.find((p) => p.id === selectedId) ?? null
  const edit = selectedId ? edits[selectedId] : undefined
  const draft = edit?.draft ?? (view ? saved[view.id] : null)
  const isNew = selectedId === NEW
  const dirty = isNew || (!!edit && !!view && !samePolicy(toPolicyContent(edit.draft), toPolicyContent(saved[view.id])))
  const problems = draft ? draft.rules.flatMap(ruleProblems) : []

  const select = (id: string) => {
    const next = new URLSearchParams(params)
    next.delete('rule')
    next.set('policy', id)
    setParams(next, { replace: true })
    setError('')
    setConflict(null)
  }
  const dropEdit = (id: string) =>
    setEdits((m) => {
      const { [id]: _, ...rest } = m
      return rest
    })
  const stored = (v: PolicyView) => {
    const next = rows.some((p) => p.id === v.id) ? rows.map((p) => (p.id === v.id ? v : p)) : [...rows, v]
    setLocal({ from: live.data, rows: next })
    syncPolicies(next)
    live.reload()
  }
  const move = async (i: number, by: -1 | 1) => {
    const from = rows.map((p) => p.id)
    const to = from.slice()
    ;[to[i], to[i + by]] = [to[i + by], to[i]]
    setBusy(true)
    try {
      const next = await reorderPolicies(from, to)
      setLocal({ from: live.data, rows: next })
      syncPolicies(next)
      toast.add({ title: 'Policies reordered', description: `${rows[i].name} is now #${i + by + 1}. Warden reloaded.`, type: 'success' })
    } catch (e) {
      const stale = e instanceof ApiError && e.status === 409
      toast.add({ title: stale ? 'The order changed' : 'Not reordered', description: stale ? 'Someone changed the policies since you loaded them; this is the current order.' : String(e), type: 'error' })
    } finally {
      setBusy(false)
      live.reload()
    }
  }
  const removed = (id: string) => {
    const next = rows.filter((p) => p.id !== id)
    setLocal({ from: live.data, rows: next })
    syncPolicies(next)
    live.reload()
  }

  const change = (d: PolicyDraft) => {
    if (!selectedId) return
    setEdits((m) => ({ ...m, [selectedId]: { draft: d, base: m[selectedId]?.base ?? view } }))
    setError('')
  }

  const newPolicy = () => {
    setEdits((m) => ({ ...m, [NEW]: { draft: blankPolicyDraft(), base: null } }))
    select(NEW)
  }

  const save = async (over?: PolicyView) => {
    if (!edit || !selectedId) return
    const c = toPolicyContent(edit.draft)
    setBusy(true)
    setError('')
    try {
      const v = edit.base ? await savePolicyDraft(over ?? edit.base, c) : await createPolicy(c)
      stored(v)
      dropEdit(selectedId)
      select(v.id)
      toast.add({ title: 'Draft saved', description: `${v.draft?.name ?? v.name}: nothing changes on the gateway until you publish it.`, type: 'success' })
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && edit.base && e.current) setConflict(e.current as PolicyView)
      else if (e instanceof ApiError && e.status === 409) setError(`A policy named ${c.name} already exists. Pick another name.`)
      else if (e instanceof ApiError && e.status === 404) setError('This policy was deleted since you opened it.')
      else setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  const keepTheirs = (theirs: PolicyView) => {
    stored(theirs)
    dropEdit(theirs.id)
    setConflict(null)
  }

  const undo = () => {
    if (!selectedId) return
    dropEdit(selectedId)
    setError('')
    setConflict(null)
    if (isNew && rows[0]) select(rows[0].id)
  }

  const canPublish = !!view && !dirty && !busy
  const isLive = !!view && (view.mode === 'enforce' || view.mode === 'monitor')
  const hint = !draft
    ? ''
    : !draft.failMode
      ? 'Choose a fail mode to save.'
      : problems.length
        ? 'Fix the rules marked below to save.'
        : dirty
          ? 'Save the draft to publish it.'
          : view && view.version === 0
            ? 'Publishing makes it v1, in monitor mode unless you choose otherwise.'
            : isLive
              ? 'To delete it, disable it first: Publish… → Disabled. That keeps a version recording the change.'
              : ''

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Guardrails"
        description="Policies run in order, each an ordered list of rules. Edit one as a draft and publish it as a new version — in monitor mode first."
      />
      <Tabs defaultValue="policies" className="gap-0">
        <div className="border-b border-border px-6 pt-2">
          <TabsList variant="underline" className="border-b-0">
            <TabsTab value="policies">Policies</TabsTab>
            <TabsTab value="detectors">Detectors</TabsTab>
            <TabsTab value="versions">Versions</TabsTab>
            <TabsIndicator />
          </TabsList>
        </div>

        <TabsPanel value="policies">
          <div className="grid lg:grid-cols-[17rem_minmax(0,1fr)] xl:grid-cols-[17rem_minmax(0,1fr)_22rem]">
            <nav aria-label="Policies" className="border-b border-border lg:border-r lg:border-b-0">
              <div className="flex items-center justify-between px-4 pt-4 pb-2">
                <h2 className="text-base font-semibold">Policies</h2>
                <span className="text-xs text-muted-foreground">in order</span>
              </div>
              <ol className="flex flex-col border-t border-border">
                {rows.map((p, i) => (
                  <PolicyRow
                    key={p.id}
                    policy={p}
                    onUp={i > 0 && !busy ? () => move(i, -1) : undefined}
                    onDown={i < rows.length - 1 && !busy ? () => move(i, 1) : undefined}
                    name={edits[p.id]?.draft.name ?? contentOf(p).name}
                    unsaved={!!edits[p.id] && !samePolicy(toPolicyContent(edits[p.id].draft), toPolicyContent(saved[p.id]))}
                    selected={p.id === selectedId}
                    onSelect={() => select(p.id)}
                  />
                ))}
                {edits[NEW] && (
                  <li className="border-b border-border">
                    <button
                      type="button"
                      onClick={() => select(NEW)}
                      aria-current={isNew || undefined}
                      className={cn(
                        'relative flex w-full flex-col gap-1 px-4 py-2.5 text-left hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none focus-visible:ring-inset',
                        isNew && 'bg-muted before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-primary',
                      )}
                    >
                      <span className="flex items-center gap-2">
                        <span className="w-4" />
                        <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium">{edits[NEW].draft.name || 'new policy'}</span>
                      </span>
                      <span className="pl-6 text-xs font-medium">Not saved yet</span>
                    </button>
                  </li>
                )}
              </ol>
              <div className="p-3">
                <Button variant="ghost" size="sm" onClick={newPolicy} disabled={!!edits[NEW] || !can('rules.draft').ok} title={can('rules.draft').reason}>
                  <Plus /> New policy
                </Button>
              </div>
              <p className="px-4 pb-4 text-xs text-muted-foreground">
                Policies run in this order, and each policy’s rules in theirs; a new policy goes last. The first block wins and stops evaluation; a later reroute overrides
                an earlier one. Moving a policy applies at once, with an audit row.
              </p>
            </nav>

            <section aria-label="Policy builder" className="min-w-0 border-b border-border px-6 py-4 xl:border-r xl:border-b-0">
              <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <h2 className="text-lg leading-6 font-semibold">Policy builder</h2>
                  <p className="text-sm text-muted-foreground">
                    {isNew
                      ? 'New policy. Nothing is written until you save the draft.'
                      : view &&
                        (view.version > 0 ? `v${view.version} is live in ${modeChip[view.mode].label.toLowerCase()} mode.` : 'Never published: Warden skips it until it is.')}
                    {view?.draft && view.version > 0 && ` Draft saved by ${view.draft.updatedBy} ${ago(view.draft.updatedAt)}.`}
                  </p>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  {edit && (
                    <Button variant="ghost" size="sm" onClick={undo} disabled={busy}>
                      <Undo2 /> Undo changes
                    </Button>
                  )}
                  {view?.draft && view.version > 0 && !edit && (
                    <Button variant="ghost" size="sm" onClick={() => setDialog('discard')} disabled={!can('rules.draft').ok} title={can('rules.draft').reason}>
                      Discard draft
                    </Button>
                  )}
                  {view && !isNew && (
                    <Button variant="ghost" size="sm" onClick={() => setDialog('delete')} disabled={isLive || busy || !can('rules.publish').ok} title={can('rules.publish').reason}>
                      <Trash2 /> Delete policy
                    </Button>
                  )}
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void save()}
                    disabled={!dirty || busy || !draft?.failMode || problems.length > 0 || !vocab.data || !!conflict || !can('rules.draft').ok}
                    title={can('rules.draft').reason}
                  >
                    <Save /> Save draft
                  </Button>
                  <Button size="sm" onClick={() => setDialog('publish')} disabled={!canPublish || !can('rules.publish').ok} title={can('rules.publish').reason}>
                    <Send /> Publish…
                  </Button>
                </div>
              </div>
              {hint && <p className="-mt-2 mb-4 text-right text-xs text-muted-foreground">{hint}</p>}
              {error && <p className="mb-4 text-sm text-destructive-foreground">{error}</p>}
              {conflict && edit && (
                <DraftConflict theirs={conflict} mine={toPolicyContent(edit.draft)} busy={busy} onKeepTheirs={() => keepTheirs(conflict)} onSaveMine={() => void save(conflict)} />
              )}
              {view && !edit && view.warnings.length > 0 && <Warnings items={view.warnings} />}
              {draft && vocab.data ? (
                <PolicyBuilder draft={draft} onChange={change} vocab={vocab.data} />
              ) : (
                <p className="text-sm text-muted-foreground">{draft ? 'Loading what rules can refer to…' : 'No policies yet. Start one with New policy.'}</p>
              )}
            </section>

            <ReplayPane
              policyId={isNew || !view ? null : view.id}
              content={draft && draft.failMode && !problems.length ? toPolicyContent(draft) : null}
              blocked={!draft ? 'Open a policy to replay it.' : !draft.failMode ? 'Choose a fail mode to replay.' : ''}
            />
          </div>
        </TabsPanel>

        <TabsPanel value="detectors">
          <LiveDetectorsTab onEntitiesChanged={vocab.reload} />
        </TabsPanel>

        <TabsPanel value="versions">
          <LiveVersions policy={isNew ? null : view} onChanged={stored} />
        </TabsPanel>
      </Tabs>

      {dialog === 'publish' && view && (
        <PublishDialog
          policy={view}
          onClose={() => setDialog(null)}
          onPublished={(v) => {
            stored(v)
            setDialog(null)
          }}
        />
      )}
      {dialog === 'discard' && view && (
        <ConfirmDialog
          title={`Discard the draft of ${view.name}?`}
          description={`The saved draft is deleted. v${view.version} stays live in ${modeChip[view.mode].label.toLowerCase()} mode.`}
          confirm="Discard draft"
          onClose={() => setDialog(null)}
          run={async () => {
            stored(await discardPolicyDraft(view))
            toast.add({ title: 'Draft discarded', description: view.name, type: 'success' })
          }}
        />
      )}
      {dialog === 'delete' && view && (
        <ConfirmDialog
          title={`Delete ${view.name}?`}
          description={view.version > 0 ? 'The policy and its rules stop being evaluated. Its published versions stay in history.' : 'The unpublished policy and its draft are deleted.'}
          confirm="Delete policy"
          onClose={() => setDialog(null)}
          run={async () => {
            await deletePolicy(view)
            removed(view.id)
            toast.add({ title: 'Policy deleted', description: view.name, type: 'success' })
          }}
        />
      )}
    </div>
  )
}

/** §5.3: reroute is last-write-wins, so a conflict is said at authoring time. */
function Warnings({ items }: { items: string[] }) {
  return (
    <div role="alert" aria-label="Reroute conflicts" className="mb-4 flex flex-col gap-1 rounded-md border border-v-degraded-border bg-v-degraded-bg px-3 py-2 text-sm text-v-degraded-fg">
      {items.map((w) => (
        <span key={w} className="flex items-start gap-2">
          <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          {w}
        </span>
      ))}
    </div>
  )
}

function PolicyRow({
  policy,
  name,
  unsaved,
  selected,
  onSelect,
  onUp,
  onDown,
}: {
  policy: PolicyView
  name: string
  unsaved: boolean
  selected: boolean
  onSelect: () => void
  onUp?: () => void
  onDown?: () => void
}) {
  const ratio = policy.baseline7d ? policy.fired24h / policy.baseline7d : 0
  const ruleCount = (policy.version > 0 ? policy.rules : (policy.draft?.rules ?? [])).length
  return (
    <li className="group/rule relative border-b border-border">
      <span className="absolute right-2 bottom-1.5 z-[1] flex gap-0.5 opacity-0 group-focus-within/rule:opacity-100 group-hover/rule:opacity-100">
        <Button variant="ghost" size="icon-xs" aria-label={`Move ${policy.name} up`} disabled={!onUp || !can('rules.publish').ok} title={can('rules.publish').reason} onClick={onUp}>
          <ArrowUp />
        </Button>
        <Button variant="ghost" size="icon-xs" aria-label={`Move ${policy.name} down`} disabled={!onDown || !can('rules.publish').ok} title={can('rules.publish').reason} onClick={onDown}>
          <ArrowDown />
        </Button>
      </span>
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected || undefined}
        className={cn(
          'relative flex w-full flex-col gap-1 px-4 py-2.5 text-left hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none focus-visible:ring-inset',
          selected && 'bg-muted before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-primary',
        )}
      >
        <span className="flex items-center gap-2">
          <span className="num w-4 font-mono text-xs text-muted-foreground">{policy.ordinal}</span>
          <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium">{name}</span>
          <StateChip tone="neutral" className={modeChip[policy.mode].className}>
            {modeChip[policy.mode].label}
          </StateChip>
        </span>
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1 pl-6 text-xs text-muted-foreground">
          <span>
            {ruleCount} {ruleCount === 1 ? 'rule' : 'rules'}
          </span>
          <span>· fail-{policy.failMode}</span>
          {policy.version > 0 && <span>· v{policy.version}</span>}
          {policy.version > 0 && <span className="num font-mono">· {int(policy.fired24h)} / 24h</span>}
          {ratio >= 3 && <StateChip tone="degraded">{Math.round(ratio)}× baseline</StateChip>}
          {policy.draft && policy.version > 0 && !unsaved && <span>· draft saved</span>}
          {unsaved && <span className="font-medium text-foreground">· unsaved changes</span>}
        </span>
      </button>
    </li>
  )
}

/** §6: a stale save renders as a merge, never a silent overwrite. */
function DraftConflict({ theirs, mine, busy, onKeepTheirs, onSaveMine }: { theirs: PolicyView; mine: PolicyContent; busy: boolean; onKeepTheirs: () => void; onSaveMine: () => void }) {
  const lines = (c: PolicyContent) => [`description: ${c.description}`, ...policyLines(c)]
  return (
    <Alert variant="warning" className="mb-4">
      <AlertTitle>This policy changed since you opened it</AlertTitle>
      <AlertDescription className="flex flex-col gap-2">
        <span>
          Nothing was saved.
          {theirs.draft ? ` ${theirs.draft.updatedBy} saved a draft ${ago(theirs.draft.updatedAt)}.` : ` It is now v${theirs.version}, ${theirs.mode}.`} Keep their version, or
          save yours over it.
        </span>
        <DiffView diff={lineDiff(lines(contentOf(theirs)), lines(mine))} title="theirs → yours" />
        <span className="flex gap-2">
          <Button variant="outline" size="sm" onClick={onKeepTheirs}>
            Keep theirs
          </Button>
          <Button size="sm" onClick={onSaveMine} disabled={busy}>
            Save mine over theirs
          </Button>
        </span>
      </AlertDescription>
    </Alert>
  )
}

const changeLabels: Record<string, string> = { name: 'Name', description: 'Description', mode: 'Mode', failMode: 'Fail mode', version: 'Version' }
const confirmLabel: Record<PublishMode, string> = { monitor: 'Publish in monitor mode', enforce: 'Publish and enforce', disabled: 'Disable policy' }

function PublishDialog({ policy, onClose, onPublished }: { policy: PolicyView; onClose: () => void; onPublished: (v: PolicyView) => void }) {
  const [mode, setMode] = useState<PublishMode>('monitor')
  const [plan, setPlan] = useState<{ mode: PublishMode; data?: PolicyPublishPlan; error?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let live = true
    planPublish(policy, mode).then(
      (data) => live && setPlan({ mode, data }),
      (e) => live && setPlan({ mode, error: errorText(e) }),
    )
    return () => {
      live = false
    }
  }, [policy, mode])

  const shown = plan?.mode === mode ? plan : null
  const next = shown?.data?.policy
  const contentChanged = shown?.data?.changes.some((c) => c.field === 'rules' || c.field === 'name' || c.field === 'failMode')

  const publish = async () => {
    setBusy(true)
    setError('')
    try {
      const v = await publishPolicy(policy, mode)
      onPublished(v)
      toast.add({
        title: mode === 'monitor' ? 'Policy published in monitor mode' : mode === 'disabled' ? 'Policy disabled' : 'Policy published and enforcing',
        description: `${v.name} v${v.version}. ${mode === 'monitor' ? 'Verdicts are recorded; no requests are changed.' : mode === 'disabled' ? 'Warden no longer evaluates it.' : 'Warden applies it now.'}`,
        type: 'success',
      })
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? `${e.message}. Close this and review the policy as it is now.` : errorText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-w-2xl flex-col">
        <DialogHeader>
          <DialogTitle>
            {mode === 'disabled' ? `Disable ${policy.name}` : `Publish ${policy.draft?.name ?? policy.name} v${policy.version + 1}`}
            {policy.version > 0 && <span className="font-normal text-muted-foreground"> from v{policy.version}</span>}
          </DialogTitle>
          <DialogDescription>The policy and all its rules publish together. Published versions are immutable; you can roll back to an earlier one from Versions.</DialogDescription>
        </DialogHeader>
        <div className="flex min-h-0 flex-col gap-4 overflow-y-auto">
          <RadioGroup value={mode} onValueChange={(v) => setMode(v as PublishMode)} aria-label="Publish mode">
            <RadioGroupItem variant="box" value="monitor" description="Evaluates and records verdicts on every request, changes nothing. Recommended for every new version.">
              Monitor
            </RadioGroupItem>
            <RadioGroupItem variant="box" value="enforce" description="Applies the actions as soon as the write returns: the control plane has Warden reload.">
              Enforce
            </RadioGroupItem>
            {policy.version > 0 && (
              <RadioGroupItem variant="box" value="disabled" description="Warden stops evaluating its rules. A disabled policy can be deleted.">
                Disabled
              </RadioGroupItem>
            )}
          </RadioGroup>
          {!shown && <p className="text-sm text-muted-foreground">Checking what this would change…</p>}
          {shown?.error && <p className="text-sm text-destructive-foreground">{shown.error}</p>}
          {shown?.data && (
            <>
              <table className="w-full text-sm">
                <caption className="mb-1 text-left text-xs text-muted-foreground">What changes</caption>
                <tbody>
                  {shown.data.changes
                    .filter((c) => changeLabels[c.field])
                    .map((c) => (
                      <tr key={c.field}>
                        <td className="w-28 py-0.5 text-muted-foreground">{changeLabels[c.field]}</td>
                        <td className="py-0.5 font-mono">
                          {String(c.from || '—')} → {String(c.to || '—')}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
              {next && (contentChanged || policy.version === 0) && (
                <DiffView
                  diff={lineDiff(policy.version > 0 ? policyLines(policy) : [], policyLines(next))}
                  title={`policy/${next.name}  ${policy.version > 0 ? `v${policy.version} → ` : ''}v${next.version}`}
                />
              )}
              {shown.data.warnings.length > 0 && <Warnings items={shown.data.warnings} />}
              <div>
                <h3 className="mb-1 text-xs text-muted-foreground">Replay</h3>
                {mode === 'monitor' && <p className="mb-2 text-xs text-muted-foreground">In monitor mode nothing changes yet: these are what it would record, then do once enforced.</p>}
                {mode === 'disabled' ? <p className="text-sm text-muted-foreground">Disabling stops every rule in it; the replay doesn’t apply.</p> : <ReplaySummary replay={shown.data.replay} maxAffected={5} />}
              </div>
            </>
          )}
          {error && <p className="text-sm text-destructive-foreground">{error}</p>}
        </div>
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>Keep editing</DialogClose>
          <Button onClick={() => void publish()} disabled={busy || !shown?.data}>
            {confirmLabel[mode]}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ConfirmDialog({
  title,
  description,
  confirm,
  destructive = true,
  onClose,
  run,
}: {
  title: string
  description: string
  confirm: string
  destructive?: boolean
  onClose: () => void
  run: () => Promise<void>
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {error && <p className="text-sm text-destructive-foreground">{error}</p>}
        <DialogFooter>
          <DialogClose render={<Button variant="ghost" />}>Cancel</DialogClose>
          <Button
            variant={destructive ? 'destructive' : 'default'}
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await run()
                onClose()
              } catch (e) {
                setError(e instanceof ApiError && e.status === 409 ? `${e.message}. Close this and review the policy as it is now.` : errorText(e))
              } finally {
                setBusy(false)
              }
            }}
          >
            {confirm}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** §7.5.7: each published version of a policy, diffable against the one before, and rollback as one audited step. */
function LiveVersions({ policy, onChanged }: { policy: PolicyView | null; onChanged: (v: PolicyView) => void }) {
  const { data, loaded, reload } = useLive<PolicyVersion[]>(policy && policy.version > 0 ? `/policies/${policy.id}/versions` : null, [])
  // A pick holds only while the live version it was made against is live.
  const [pick, setPick] = useState<{ at?: number; version: number } | null>(null)
  const [confirm, setConfirm] = useState(false)
  const liveVersion = policy?.version
  const sel = pick && pick.at === liveVersion ? pick.version : null
  useEffect(() => reload(), [liveVersion, reload])

  if (!policy) return <p className="px-6 py-4 text-sm text-muted-foreground">Save the policy to start its history.</p>
  if (policy.version === 0) return <p className="px-6 py-4 text-sm text-muted-foreground">{policy.name} hasn’t been published yet, so it has no versions.</p>
  if (!loaded) return <p className="px-6 py-4 text-sm text-muted-foreground">Loading versions…</p>

  const i = Math.max(0, data.findIndex((v) => v.version === (sel ?? policy.version)))
  const cur = data[i]
  const prev = data[i + 1]
  if (!cur) return <p className="px-6 py-4 text-sm text-muted-foreground">No versions recorded.</p>

  return (
    <div className="grid min-h-0 lg:grid-cols-[22rem_1fr]">
      <Section title={policy.name} description="A version holds the policy’s rules, in order. Published versions are immutable." className="border-b lg:border-r lg:border-b-0">
        <ol aria-label={`Versions of ${policy.name}`} className="flex flex-col divide-y divide-border border-y border-border">
          {data.map((v) => (
            <li key={v.version}>
              <button
                type="button"
                onClick={() => setPick({ at: liveVersion, version: v.version })}
                aria-current={v.version === cur.version || undefined}
                className={cn(
                  'flex w-full items-start gap-2 px-2 py-2 text-left text-sm hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none focus-visible:ring-inset',
                  v.version === cur.version && 'bg-muted',
                )}
              >
                <span className="num w-8 font-mono font-semibold">v{v.version}</span>
                <span className="flex min-w-0 flex-1 flex-col">
                  <span className="truncate">
                    {modeChip[v.mode].label}, fail-{v.failMode}, {v.rules.length} {v.rules.length === 1 ? 'rule' : 'rules'}
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {v.publishedAt ? `${v.publishedBy ?? 'unknown'} · ${utc(v.publishedAt)}` : 'Published before history was kept'}
                  </span>
                </span>
                {v.version === policy.version && <StateChip tone="allowed">Live</StateChip>}
              </button>
            </li>
          ))}
        </ol>
      </Section>
      <Section
        title={`v${cur.version} against ${prev ? `v${prev.version}` : '—'}`}
        description={
          <span className="inline-flex items-center gap-1.5">
            <Lock className="size-3.5" aria-hidden="true" />
            {cur.publishedAt ? `Published ${utc(cur.publishedAt)} by ${cur.publishedBy ?? 'unknown'}` : 'Published before history was kept'}, in {cur.mode} mode.
          </span>
        }
        actions={
          cur.version !== policy.version && (
            <Button variant="outline" size="sm" onClick={() => setConfirm(true)} disabled={!can('rules.publish').ok} title={can('rules.publish').reason}>
              <History /> Roll back to v{cur.version}
            </Button>
          )
        }
      >
        {prev ? (
          <DiffView diff={lineDiff(versionLines(prev), versionLines(cur))} title={`policy/${policy.name}  v${prev.version} → v${cur.version}`} />
        ) : (
          <p className="text-sm text-muted-foreground">First recorded version.</p>
        )}
      </Section>

      {confirm && (
        <ConfirmDialog
          title={`Roll back ${policy.name} to v${cur.version}?`}
          description={`This publishes v${policy.version + 1} with v${cur.version}'s rules, fail mode and mode (${cur.mode}). v${policy.version} stays in history, and any saved draft is kept.`}
          confirm={`Roll back to v${cur.version}`}
          destructive={false}
          onClose={() => setConfirm(false)}
          run={async () => {
            const v = await rollbackPolicy(policy, cur.version)
            onChanged(v)
            toast.add({ title: `Rolled back to v${cur.version}`, description: `Published as v${v.version}, in ${v.mode} mode.`, type: 'success' })
          }}
        />
      )}
    </div>
  )
}

const versionLines = (v: PolicyVersion) => [`mode: ${v.mode}`, ...policyLines(v)]
