import { Play, Plus, Send } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { DiffView } from '@/components/gw/diff-view'
import { PageHeader } from '@/components/gw/page'
import { StateChip } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Tabs, TabsIndicator, TabsList, TabsPanel, TabsTab } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toast'
import { type PolicyRule, rules as seedRules } from '@/data/mock'
import { int } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useReceipts } from '@/state/app-state'
import { RuleBuilder } from './guardrails-builder'
import { DetectorsTab } from './guardrails-detectors'
import { blankDraft, type Draft, fromRule, hashDraft, lineDiff, toLines } from './guardrails-model'
import { ReplayPane } from './guardrails-replay'
import { VersionsTab } from './guardrails-versions'

// §7.5.7 Guardrails — the differentiating surface. Rules list · rule builder ·
// replay, side by side. Publishing is versioned; monitor mode is the default.

type Mode = PolicyRule['mode']
interface RuleEntry {
  id: string
  ordinal: number
  name: string
  mode: Mode
  version: number
  failMode?: 'open' | 'closed'
  fired24h: number
  baseline7d: number
  description: string
}

const modeChip: Record<Mode, { label: string; className?: string }> = {
  enforce: { label: 'Enforce' },
  monitor: { label: 'Monitor', className: 'border-dashed' },
  draft: { label: 'Draft', className: 'border-dashed bg-transparent' },
}

export function GuardrailsPage() {
  const [params, setParams] = useSearchParams()
  const receipts = useReceipts()
  const [entries, setEntries] = useState<RuleEntry[]>(() =>
    seedRules.map((r) => ({ id: r.id, ordinal: r.ordinal, name: r.name, mode: r.mode, version: r.version, failMode: r.failMode, fired24h: r.fired24h, baseline7d: r.baseline7d, description: r.description })),
  )
  const [baselines, setBaselines] = useState<Record<string, Draft | null>>(() => Object.fromEntries(seedRules.map((r) => [r.id, fromRule(r)])))
  const [drafts, setDrafts] = useState<Record<string, Draft>>(() => Object.fromEntries(seedRules.map((r) => [r.id, fromRule(r)])))
  const [nonce, setNonce] = useState(0)
  const [publishOpen, setPublishOpen] = useState(false)
  const [publishMode, setPublishMode] = useState<'monitor' | 'enforce'>('monitor')

  const selectedId = params.get('rule') && entries.some((e) => e.id === params.get('rule')) ? params.get('rule')! : entries[0].id
  const select = (id: string) => {
    const next = new URLSearchParams(params)
    next.set('rule', id)
    setParams(next, { replace: true })
  }
  const entry = entries.find((e) => e.id === selectedId)!
  const draft = drafts[selectedId]
  const baseline = baselines[selectedId]
  const dirty = !baseline || hashDraft(baseline) !== hashDraft(draft) || baseline.name !== draft.name
  const canPublish = !!draft.failMode && dirty
  const fpReceiptIds = useMemo(() => receipts.filter((r) => r.verdict === 'redacted').slice(0, 4).map((r) => r.id), [receipts])

  const newRule = () => {
    const id = `new-${entries.length + 1}`
    const d = { ...blankDraft(), ruleId: id, name: `new-rule-${entries.length - seedRules.length + 1}` }
    setEntries((e) => [...e, { id, ordinal: e.length + 1, name: d.name, mode: 'draft', version: 0, fired24h: 0, baseline7d: 0, description: 'Unpublished draft' }])
    setDrafts((m) => ({ ...m, [id]: d }))
    setBaselines((m) => ({ ...m, [id]: null }))
    setPublishMode('monitor')
    select(id)
  }

  const publish = () => {
    const nextVersion = entry.version + 1
    setEntries((es) => es.map((e) => (e.id === selectedId ? { ...e, name: draft.name, mode: publishMode, version: nextVersion, failMode: draft.failMode } : e)))
    setBaselines((m) => ({ ...m, [selectedId]: draft }))
    setPublishOpen(false)
    toast.add({
      title: publishMode === 'monitor' ? 'Rule published in monitor mode' : 'Rule published and enforcing',
      description: `${draft.name} v${nextVersion}. ${publishMode === 'monitor' ? 'Verdicts are recorded; no requests are changed.' : 'Warden applies it on the next snapshot.'}`,
      type: 'success',
    })
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageHeader
        title="Guardrails"
        description="Author a data-protection rule, replay it against recorded traffic, and publish it — in monitor mode first."
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
            {/* 1. Rules list */}
            <nav aria-label="Rules" className="border-b border-border lg:border-r lg:border-b-0">
              <div className="flex items-center justify-between px-4 pt-4 pb-2">
                <h2 className="text-base font-semibold">Policy · acme-default</h2>
                <span className="text-xs text-muted-foreground">in order</span>
              </div>
              <ol className="flex flex-col border-t border-border">
                {entries.map((e) => {
                  const ratio = e.baseline7d ? e.fired24h / e.baseline7d : 0
                  const edited = drafts[e.id] && baselines[e.id] && hashDraft(drafts[e.id]) !== hashDraft(baselines[e.id]!)
                  return (
                    <li key={e.id} className="border-b border-border">
                      <button
                        type="button"
                        onClick={() => select(e.id)}
                        aria-current={e.id === selectedId || undefined}
                        className={cn(
                          'relative flex w-full flex-col gap-1 px-4 py-2.5 text-left hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none focus-visible:ring-inset',
                          e.id === selectedId && 'bg-muted before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-primary',
                        )}
                      >
                        <span className="flex items-center gap-2">
                          <span className="num w-4 font-mono text-xs text-muted-foreground">{e.ordinal}</span>
                          <span className="min-w-0 flex-1 truncate font-mono text-sm font-medium">{drafts[e.id]?.name ?? e.name}</span>
                          <StateChip tone="neutral" className={modeChip[e.mode].className}>
                            {modeChip[e.mode].label}
                          </StateChip>
                        </span>
                        <span className="flex flex-wrap items-center gap-x-2 gap-y-1 pl-6 text-xs text-muted-foreground">
                          <span>{e.failMode ? `fail-${e.failMode}` : 'fail mode unset'}</span>
                          {e.version > 0 && <span>· v{e.version}</span>}
                          {e.baseline7d > 0 && (
                            <span className="num font-mono">
                              · {int(e.fired24h)} / 24h
                            </span>
                          )}
                          {ratio >= 3 && <StateChip tone="degraded">{Math.round(ratio)}× baseline</StateChip>}
                          {edited && <span className="font-medium text-foreground">· unsaved changes</span>}
                        </span>
                      </button>
                    </li>
                  )
                })}
              </ol>
              <div className="p-3">
                <Button variant="ghost" size="sm" onClick={newRule}>
                  <Plus /> New rule
                </Button>
              </div>
              <p className="px-4 pb-4 text-xs text-muted-foreground">
                Rules run in order. The first block wins and stops evaluation; redactions accumulate; the last route wins.
              </p>
            </nav>

            {/* 2. Rule builder */}
            <section aria-label="Rule builder" className="min-w-0 border-b border-border px-6 py-4 xl:border-r xl:border-b-0">
              <div className="mb-4 flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <h2 className="text-lg leading-6 font-semibold">Rule builder</h2>
                  <p className="text-sm text-muted-foreground">
                    {entry.description}
                    {entry.version > 0 && (
                      <>
                        {' '}
                        · editing a draft of v{entry.version}, live in {entry.mode} mode
                      </>
                    )}
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  <Button variant="outline" size="sm" onClick={() => setNonce((n) => n + 1)}>
                    <Play /> Replay
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => {
                      setPublishMode('monitor')
                      setPublishOpen(true)
                    }}
                    disabled={!canPublish}
                  >
                    <Send /> Publish…
                  </Button>
                </div>
              </div>
              {!canPublish && (
                <p className="-mt-2 mb-4 text-right text-xs text-muted-foreground">
                  {!draft.failMode ? 'Choose a fail mode below to publish.' : 'No changes to publish. Edit the rule to create a new version.'}
                </p>
              )}
              <RuleBuilder draft={draft} onChange={(d) => setDrafts((m) => ({ ...m, [selectedId]: d }))} />
            </section>

            {/* 3. Replay */}
            <aside aria-label="Replay" className="min-w-0 px-5 py-4 lg:col-span-2 xl:col-span-1">
              <ReplayPane draft={draft} baseline={baseline} nonce={nonce} />
            </aside>
          </div>
        </TabsPanel>

        <TabsPanel value="detectors">
          <DetectorsTab receiptIds={fpReceiptIds} />
        </TabsPanel>

        <TabsPanel value="versions">
          <VersionsTab />
        </TabsPanel>
      </Tabs>

      <Dialog open={publishOpen} onOpenChange={setPublishOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              Publish {draft.name} v{entry.version + 1}
              {entry.version > 0 && <span className="font-normal text-muted-foreground"> from v{entry.version}</span>}
            </DialogTitle>
            <DialogDescription>Published versions are immutable. You can roll back to v{entry.version || 1} in one step from Versions.</DialogDescription>
          </DialogHeader>
          <div className="flex min-h-0 flex-col gap-4 overflow-y-auto">
            <DiffView
              diff={lineDiff(baseline ? toLines(baseline) : [], toLines(draft))}
              title={`policy/${draft.name}  ${entry.version > 0 ? `v${entry.version} → ` : ''}v${entry.version + 1}`}
            />
            <RadioGroup value={publishMode} onValueChange={(v) => setPublishMode(v as 'monitor' | 'enforce')} aria-label="Publish mode">
              <RadioGroupItem variant="box" value="monitor" description="Evaluates and records verdicts on every request, changes nothing. Recommended for every new version.">
                Monitor mode
              </RadioGroupItem>
              <RadioGroupItem variant="box" value="enforce" description={`Applies the actions immediately. Fail mode: ${draft.failMode === 'open' ? 'allow (fail-open)' : 'block (fail-closed)'}.`}>
                Enforce
              </RadioGroupItem>
            </RadioGroup>
          </div>
          <DialogFooter>
            <DialogClose render={<Button variant="ghost" />}>Keep editing</DialogClose>
            <Button onClick={publish}>{publishMode === 'monitor' ? 'Publish in monitor mode' : 'Publish and enforce'}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
