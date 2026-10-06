import { ArrowDown, ArrowUp, History, Lock, Plus, Save, Send, Trash2, Undo2 } from 'lucide-react'
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
  createRule,
  deleteRule,
  discardRuleDraft,
  planPublish,
  type PublishMode,
  publishRule,
  reorderRules,
  rollbackRule,
  type RuleContent,
  type RulePublishPlan,
  type RuleVersion,
  type RuleView,
  type RuleVocabulary,
  rules as catalogRules,
  saveRuleDraft,
  syncRules,
} from '@/data/catalog'
import { ago, int } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useLive } from '@/state/live'
import { RuleBuilder } from './guardrails-builder'
import { LiveDetectorsTab } from './guardrails-detectors-live'
import { blankApiDraft, contentLines, type Draft, fromContent, lineDiff, modeChip, sameContent, toContent } from './guardrails-model'

// §7.5.7 Guardrails in api mode: the builder saves drafts to the control plane,
// publishes them as immutable versions (monitor mode first), and rolls back
// through the API. It offers only what the engine evaluates; replay isn't
// connected, and the page says so rather than simulating it.

const NEW = 'new'

/** A local edit, and the rule as it was when the edit began (null for a new rule). */
type Edit = { draft: Draft; base: RuleView | null }

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))
const utc = (ms: number) => new Date(ms).toISOString().slice(0, 16).replace('T', ' ') + ' UTC'
const contentOf = (r: RuleView): RuleContent => r.draft ?? r

export function LiveGuardrailsPage() {
  const [params, setParams] = useSearchParams()
  const live = useLive<RuleView[]>('/rules', catalogRules as RuleView[])
  const vocab = useLive<RuleVocabulary | null>('/rules/vocabulary', null, 300_000)
  // A write's answer shows at once, until the next fetch replaces it.
  const [local, setLocal] = useState<{ from: RuleView[]; rows: RuleView[] } | null>(null)
  const rows = local && local.from === live.data ? local.rows : live.data
  const [edits, setEdits] = useState<Record<string, Edit>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState<RuleView | null>(null)
  const [dialog, setDialog] = useState<null | 'publish' | 'discard' | 'delete'>(null)

  useEffect(() => {
    if (live.loaded) syncRules(live.data)
  }, [live.data, live.loaded])

  // Built once per fetched row, so the builder's row keys hold still between renders.
  const saved = useMemo(() => Object.fromEntries(rows.map((r) => [r.id, fromContent(contentOf(r), r.id)])), [rows])

  const param = params.get('rule')
  const selectedId = param === NEW && edits[NEW] ? NEW : param && rows.some((r) => r.id === param) ? param : (rows[0]?.id ?? null)
  const view = rows.find((r) => r.id === selectedId) ?? null
  const edit = selectedId ? edits[selectedId] : undefined
  const draft = edit?.draft ?? (view ? saved[view.id] : null)
  const isNew = selectedId === NEW
  const dirty = isNew || (!!edit && !!view && !sameContent(toContent(edit.draft), toContent(saved[view.id])))

  const select = (id: string) => {
    const next = new URLSearchParams(params)
    next.set('rule', id)
    setParams(next, { replace: true })
    setError('')
    setConflict(null)
  }
  const dropEdit = (id: string) =>
    setEdits((m) => {
      const { [id]: _, ...rest } = m
      return rest
    })
  const stored = (v: RuleView) => {
    const next = rows.some((r) => r.id === v.id) ? rows.map((r) => (r.id === v.id ? v : r)) : [...rows, v]
    setLocal({ from: live.data, rows: next })
    syncRules(next)
    live.reload()
  }
  const move = async (i: number, by: -1 | 1) => {
    const from = rows.map((r) => r.id)
    const to = from.slice()
    ;[to[i], to[i + by]] = [to[i + by], to[i]]
    setBusy(true)
    try {
      const next = await reorderRules(from, to)
      setLocal({ from: live.data, rows: next })
      syncRules(next)
      toast.add({ title: 'Rules reordered', description: `${rows[i].name} is now #${i + by + 1}. Warden reloaded.`, type: 'success' })
    } catch (e) {
      const stale = e instanceof ApiError && e.status === 409
      toast.add({ title: stale ? 'The order changed' : 'Not reordered', description: stale ? 'Someone changed the rules since you loaded them; this is the current order.' : String(e), type: 'error' })
    } finally {
      setBusy(false)
      live.reload()
    }
  }
  const removed = (id: string) => {
    const next = rows.filter((r) => r.id !== id)
    setLocal({ from: live.data, rows: next })
    syncRules(next)
    live.reload()
  }

  const change = (d: Draft) => {
    if (!selectedId) return
    setEdits((m) => ({ ...m, [selectedId]: { draft: d, base: m[selectedId]?.base ?? view } }))
    setError('')
  }

  const newRule = () => {
    setEdits((m) => ({ ...m, [NEW]: { draft: blankApiDraft(), base: null } }))
    select(NEW)
  }

  const save = async (over?: RuleView) => {
    if (!edit || !selectedId) return
    const c = toContent(edit.draft)
    setBusy(true)
    setError('')
    try {
      const v = edit.base ? await saveRuleDraft(over ?? edit.base, c) : await createRule(c)
      stored(v)
      dropEdit(selectedId)
      select(v.id)
      toast.add({ title: 'Draft saved', description: `${v.name}: nothing changes on the gateway until you publish it.`, type: 'success' })
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && edit.base && e.current) setConflict(e.current as RuleView)
      else if (e instanceof ApiError && e.status === 409) setError(`A rule named ${c.name} already exists. Pick another name.`)
      else if (e instanceof ApiError && e.status === 404) setError('This rule was deleted since you opened it.')
      else setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  const keepTheirs = (theirs: RuleView) => {
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
        description="Author a data-protection rule, save it as a draft, and publish it as a new version — in monitor mode first."
      />
      <Tabs defaultValue="rules" className="gap-0">
        <div className="border-b border-border px-6 pt-2">
          <TabsList variant="underline" className="border-b-0">
            <TabsTab value="rules">Rules</TabsTab>
            <TabsTab value="detectors">Detectors</TabsTab>
            <TabsTab value="versions">Versions</TabsTab>
            <TabsIndicator />
          </TabsList>
        </div>

        <TabsPanel value="rules">
          <div className="grid lg:grid-cols-[17rem_minmax(0,1fr)] xl:grid-cols-[17rem_minmax(0,1fr)_24rem]">
            <nav aria-label="Rules" className="border-b border-border lg:border-r lg:border-b-0">
              <div className="flex items-center justify-between px-4 pt-4 pb-2">
                <h2 className="text-base font-semibold">Policy</h2>
                <span className="text-xs text-muted-foreground">in order</span>
              </div>
              <ol className="flex flex-col border-t border-border">
                {rows.map((r, i) => (
                  <RuleRow
                    key={r.id}
                    rule={r}
                    onUp={i > 0 && !busy ? () => move(i, -1) : undefined}
                    onDown={i < rows.length - 1 && !busy ? () => move(i, 1) : undefined}
                    name={edits[r.id]?.draft.name ?? contentOf(r).name}
                    unsaved={!!edits[r.id] && !sameContent(toContent(edits[r.id].draft), toContent(saved[r.id]))}
                    selected={r.id === selectedId}
                    onSelect={() => select(r.id)}
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
                        <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium">{edits[NEW].draft.name || 'new rule'}</span>
                      </span>
                      <span className="pl-6 text-xs font-medium">Not saved yet</span>
                    </button>
                  </li>
                )}
              </ol>
              <div className="p-3">
                <Button variant="ghost" size="sm" onClick={newRule} disabled={!!edits[NEW] || !can('rules.draft').ok} title={can('rules.draft').reason}>
                  <Plus /> New rule
                </Button>
              </div>
              <p className="px-4 pb-4 text-xs text-muted-foreground">
                Rules run in order; a new rule goes last. The first block wins and stops evaluation; a later reroute overrides an earlier one. Moving a rule applies at once, with an audit row.
              </p>
            </nav>

            <section aria-label="Rule builder" className="min-w-0 border-b border-border px-6 py-4 xl:border-r xl:border-b-0">
              <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <h2 className="text-lg leading-6 font-semibold">Rule builder</h2>
                  <p className="text-sm text-muted-foreground">
                    {isNew
                      ? 'New rule. Nothing is written until you save the draft.'
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
                      <Trash2 /> Delete rule
                    </Button>
                  )}
                  <Button variant="outline" size="sm" onClick={() => void save()} disabled={!dirty || busy || !draft?.failMode || !vocab.data || !!conflict || !can('rules.draft').ok} title={can('rules.draft').reason}>
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
                <DraftConflict theirs={conflict} mine={toContent(edit.draft)} busy={busy} onKeepTheirs={() => keepTheirs(conflict)} onSaveMine={() => void save(conflict)} />
              )}
              {draft && vocab.data ? (
                <RuleBuilder draft={draft} onChange={change} vocab={vocab.data} />
              ) : (
                <p className="text-sm text-muted-foreground">{draft ? 'Loading what rules can refer to…' : 'No rules yet. Start one with New rule.'}</p>
              )}
            </section>

            <aside aria-label="Replay" className="min-w-0 px-5 py-4 lg:col-span-2 xl:col-span-1">
              <h2 className="text-base font-semibold">Replay</h2>
              <p className="mt-2 text-sm text-muted-foreground">
                Replay isn’t connected yet: Warden’s evaluator doesn’t run over stored receipts, so there’s no before-and-after for a draft. Publish in monitor mode
                to record what the rule would do on live traffic without changing any request.
              </p>
            </aside>
          </div>
        </TabsPanel>

        <TabsPanel value="detectors">
          <LiveDetectorsTab onEntitiesChanged={vocab.reload} />
        </TabsPanel>

        <TabsPanel value="versions">
          <LiveVersions rule={isNew ? null : view} onChanged={stored} />
        </TabsPanel>
      </Tabs>

      {dialog === 'publish' && view && (
        <PublishDialog
          rule={view}
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
            stored(await discardRuleDraft(view))
            toast.add({ title: 'Draft discarded', description: view.name, type: 'success' })
          }}
        />
      )}
      {dialog === 'delete' && view && (
        <ConfirmDialog
          title={`Delete ${view.name}?`}
          description={view.version > 0 ? 'The rule leaves the policy. Its published versions stay in history.' : 'The unpublished rule and its draft are deleted.'}
          confirm="Delete rule"
          onClose={() => setDialog(null)}
          run={async () => {
            await deleteRule(view)
            removed(view.id)
            toast.add({ title: 'Rule deleted', description: view.name, type: 'success' })
          }}
        />
      )}
    </div>
  )
}

function RuleRow({
  rule,
  name,
  unsaved,
  selected,
  onSelect,
  onUp,
  onDown,
}: {
  rule: RuleView
  name: string
  unsaved: boolean
  selected: boolean
  onSelect: () => void
  onUp?: () => void
  onDown?: () => void
}) {
  const ratio = rule.baseline7d ? rule.fired24h / rule.baseline7d : 0
  return (
    <li className="group/rule relative border-b border-border">
      <span className="absolute right-2 bottom-1.5 z-[1] flex gap-0.5 opacity-0 group-focus-within/rule:opacity-100 group-hover/rule:opacity-100">
        <Button variant="ghost" size="icon-xs" aria-label={`Move ${rule.name} up`} disabled={!onUp || !can('rules.publish').ok} title={can('rules.publish').reason} onClick={onUp}>
          <ArrowUp />
        </Button>
        <Button variant="ghost" size="icon-xs" aria-label={`Move ${rule.name} down`} disabled={!onDown || !can('rules.publish').ok} title={can('rules.publish').reason} onClick={onDown}>
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
          <span className="num w-4 font-mono text-xs text-muted-foreground">{rule.ordinal}</span>
          <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium">{name}</span>
          <StateChip tone="neutral" className={modeChip[rule.mode].className}>
            {modeChip[rule.mode].label}
          </StateChip>
        </span>
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1 pl-6 text-xs text-muted-foreground">
          <span>fail-{rule.failMode}</span>
          {rule.version > 0 && <span>· v{rule.version}</span>}
          {rule.version > 0 && <span className="num font-mono">· {int(rule.fired24h)} / 24h</span>}
          {ratio >= 3 && <StateChip tone="degraded">{Math.round(ratio)}× baseline</StateChip>}
          {rule.draft && rule.version > 0 && !unsaved && <span>· draft saved</span>}
          {unsaved && <span className="font-medium text-foreground">· unsaved changes</span>}
        </span>
      </button>
    </li>
  )
}

/** §6: a stale save renders as a merge, never a silent overwrite. */
function DraftConflict({ theirs, mine, busy, onKeepTheirs, onSaveMine }: { theirs: RuleView; mine: RuleContent; busy: boolean; onKeepTheirs: () => void; onSaveMine: () => void }) {
  const lines = (c: RuleContent) => [`description: ${c.description}`, ...contentLines(c)]
  return (
    <Alert variant="warning" className="mb-4">
      <AlertTitle>This rule changed since you opened it</AlertTitle>
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
const confirmLabel: Record<PublishMode, string> = { monitor: 'Publish in monitor mode', enforce: 'Publish and enforce', disabled: 'Disable rule' }

function PublishDialog({ rule, onClose, onPublished }: { rule: RuleView; onClose: () => void; onPublished: (v: RuleView) => void }) {
  const [mode, setMode] = useState<PublishMode>('monitor')
  const [plan, setPlan] = useState<{ mode: PublishMode; data?: RulePublishPlan; error?: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let live = true
    planPublish(rule, mode).then(
      (data) => live && setPlan({ mode, data }),
      (e) => live && setPlan({ mode, error: errorText(e) }),
    )
    return () => {
      live = false
    }
  }, [rule, mode])

  const shown = plan?.mode === mode ? plan : null
  const next = shown?.data?.rule
  const contentChanged = shown?.data?.changes.some((c) => c.field === 'when' || c.field === 'then' || c.field === 'name' || c.field === 'failMode')

  const publish = async () => {
    setBusy(true)
    setError('')
    try {
      const v = await publishRule(rule, mode)
      onPublished(v)
      toast.add({
        title: mode === 'monitor' ? 'Rule published in monitor mode' : mode === 'disabled' ? 'Rule disabled' : 'Rule published and enforcing',
        description: `${v.name} v${v.version}. ${mode === 'monitor' ? 'Verdicts are recorded; no requests are changed.' : mode === 'disabled' ? 'Warden no longer evaluates it.' : 'Warden applies it now.'}`,
        type: 'success',
      })
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? `${e.message}. Close this and review the rule as it is now.` : errorText(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="flex max-w-2xl flex-col">
        <DialogHeader>
          <DialogTitle>
            {mode === 'disabled' ? `Disable ${rule.name}` : `Publish ${rule.draft?.name ?? rule.name} v${rule.version + 1}`}
            {rule.version > 0 && <span className="font-normal text-muted-foreground"> from v{rule.version}</span>}
          </DialogTitle>
          <DialogDescription>Published versions are immutable. You can roll back to an earlier one from Versions.</DialogDescription>
        </DialogHeader>
        <div className="flex min-h-0 flex-col gap-4 overflow-y-auto">
          <RadioGroup value={mode} onValueChange={(v) => setMode(v as PublishMode)} aria-label="Publish mode">
            <RadioGroupItem variant="box" value="monitor" description="Evaluates and records verdicts on every request, changes nothing. Recommended for every new version.">
              Monitor
            </RadioGroupItem>
            <RadioGroupItem variant="box" value="enforce" description="Applies the action as soon as the write returns: the control plane has Warden reload.">
              Enforce
            </RadioGroupItem>
            {rule.version > 0 && (
              <RadioGroupItem variant="box" value="disabled" description="Warden stops evaluating it. A disabled rule can be deleted.">
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
              {next && (contentChanged || rule.version === 0) && (
                <DiffView
                  diff={lineDiff(rule.version > 0 ? contentLines(rule) : [], contentLines(next))}
                  title={`policy/${next.name}  ${rule.version > 0 ? `v${rule.version} → ` : ''}v${next.version}`}
                />
              )}
              <p className="text-xs text-muted-foreground">{shown.data.note}</p>
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
                setError(e instanceof ApiError && e.status === 409 ? `${e.message}. Close this and review the rule as it is now.` : errorText(e))
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

/** §7.5.7: each published version, diffable against the one before, and rollback as one audited step. */
function LiveVersions({ rule, onChanged }: { rule: RuleView | null; onChanged: (v: RuleView) => void }) {
  const { data, loaded, reload } = useLive<RuleVersion[]>(rule && rule.version > 0 ? `/rules/${rule.id}/versions` : null, [])
  // A pick holds only while the live version it was made against is live.
  const [pick, setPick] = useState<{ at?: number; version: number } | null>(null)
  const [confirm, setConfirm] = useState(false)
  const liveVersion = rule?.version
  const sel = pick && pick.at === liveVersion ? pick.version : null
  useEffect(() => reload(), [liveVersion, reload])

  if (!rule) return <p className="px-6 py-4 text-sm text-muted-foreground">Save the rule to start its history.</p>
  if (rule.version === 0) return <p className="px-6 py-4 text-sm text-muted-foreground">{rule.name} hasn’t been published yet, so it has no versions.</p>
  if (!loaded) return <p className="px-6 py-4 text-sm text-muted-foreground">Loading versions…</p>

  const i = Math.max(0, data.findIndex((v) => v.version === (sel ?? rule.version)))
  const cur = data[i]
  const prev = data[i + 1]
  if (!cur) return <p className="px-6 py-4 text-sm text-muted-foreground">No versions recorded.</p>

  return (
    <div className="grid min-h-0 lg:grid-cols-[22rem_1fr]">
      <Section title={rule.name} description="Published versions are immutable." className="border-b lg:border-r lg:border-b-0">
        <ol aria-label={`Versions of ${rule.name}`} className="flex flex-col divide-y divide-border border-y border-border">
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
                  <span className="truncate">{modeChip[v.mode].label}, fail-{v.failMode}</span>
                  <span className="text-xs text-muted-foreground">
                    {v.publishedAt ? `${v.publishedBy ?? 'unknown'} · ${utc(v.publishedAt)}` : 'Published before history was kept'}
                  </span>
                </span>
                {v.version === rule.version && <StateChip tone="allowed">Live</StateChip>}
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
          cur.version !== rule.version && (
            <Button variant="outline" size="sm" onClick={() => setConfirm(true)} disabled={!can('rules.publish').ok} title={can('rules.publish').reason}>
              <History /> Roll back to v{cur.version}
            </Button>
          )
        }
      >
        {prev ? (
          <DiffView diff={lineDiff(versionLines(prev), versionLines(cur))} title={`policy/${rule.name}  v${prev.version} → v${cur.version}`} />
        ) : (
          <p className="text-sm text-muted-foreground">First recorded version.</p>
        )}
      </Section>

      {confirm && (
        <ConfirmDialog
          title={`Roll back ${rule.name} to v${cur.version}?`}
          description={`This publishes v${rule.version + 1} with v${cur.version}'s content and mode (${cur.mode}). v${rule.version} stays in history, and any saved draft is kept.`}
          confirm={`Roll back to v${cur.version}`}
          destructive={false}
          onClose={() => setConfirm(false)}
          run={async () => {
            const v = await rollbackRule(rule, cur.version)
            onChanged(v)
            toast.add({ title: `Rolled back to v${cur.version}`, description: `Published as v${v.version}, in ${v.mode} mode.`, type: 'success' })
          }}
        />
      )}
    </div>
  )
}

const versionLines = (v: RuleVersion) => [`mode: ${v.mode}`, ...contentLines(v)]
