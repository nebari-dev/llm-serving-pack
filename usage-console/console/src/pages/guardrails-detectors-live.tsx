import { useState } from 'react'
import { can } from '@/data/catalog'
import { Link } from 'react-router-dom'
import { Section } from '@/components/gw/page'
import { StateChip } from '@/components/gw/verdict'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toast'
import {
  ApiError,
  checkEntity,
  createEntity,
  type CustomEntity,
  deleteEntity,
  type DetectorHit,
  type DetectorVerdict,
  type DetectorView,
  type EntityCheck,
  type EntityInput,
  setVerdict,
  updateEntity,
} from '@/data/catalog'
import { int } from '@/lib/format'
import { useLive } from '@/state/live'
import { modeChip } from './guardrails-model'

// §7.5.7 detectors in api mode: the engine's entity detectors (built-in and
// the tenant's custom entities, §5.3's registry), the live rules that name
// them, what receipts recorded in the last 24 hours, and reviewers' verdicts
// on their hits. The mockup's thresholds have no backend: these are regexes
// with no confidence score.

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))
const utc = (ms: number) => new Date(ms).toISOString().slice(5, 16).replace('T', ' ') + ' UTC'
const lines = (s: string) =>
  s
    .split('\n')
    .map((x) => x.trim())
    .filter(Boolean)

export function LiveDetectorsTab({ onEntitiesChanged }: { onEntitiesChanged?: () => void }) {
  const { data, loaded, reload } = useLive<DetectorView[]>('/detectors', [], 60_000)
  // null: no form; 'new': adding; otherwise the entity being edited.
  const [editing, setEditing] = useState<CustomEntity | 'new' | null>(null)
  const changed = () => {
    setEditing(null)
    reload()
    onEntitiesChanged?.()
  }
  return (
    <div>
      <Section
        title="Detectors"
        description="What “Prompt contains entity” looks for. Each is a pattern match on the prompt text: there’s no confidence score, so there’s no threshold to tune."
      >
        {!loaded ? (
          <p className="text-sm text-muted-foreground">Loading detectors…</p>
        ) : (
          <div className="overflow-x-auto">
            <table aria-label="Detectors" className="w-full min-w-[60rem] text-sm">
              <thead>
                <tr className="border-b border-border text-left text-xs text-muted-foreground">
                  <th className="py-1.5 pr-3 font-medium">Entity</th>
                  <th className="py-1.5 pr-3 font-medium">How it matches</th>
                  <th className="py-1.5 pr-3 font-medium">Redacted as</th>
                  <th className="py-1.5 pr-3 font-medium">Used by live rules</th>
                  <th className="py-1.5 pr-3 text-right font-medium">Redacted, 24h</th>
                  <th className="py-1.5 pr-3 text-right font-medium">Blocked, 24h</th>
                  <th className="py-1.5 text-right font-medium">False positives, 30d</th>
                </tr>
              </thead>
              <tbody>
                {data.map((d) => (
                  <tr key={d.entity} className="border-b border-border align-top">
                    <td className="py-2 pr-3 font-medium">
                      {d.entity}
                      {d.customEntity && (
                        <span className="mt-0.5 flex items-center gap-2 text-xs font-normal text-muted-foreground">
                          Custom
                          <button type="button" className="underline hover:text-foreground" aria-label={`Edit ${d.entity}`} disabled={!can('detectors').ok} title={can('detectors').reason} onClick={() => setEditing(d.customEntity!)}>
                            Edit
                          </button>
                        </span>
                      )}
                    </td>
                    <td className="py-2 pr-3">
                      <span className="text-xs text-muted-foreground-strong">{d.kind}</span>
                      <code className="mt-0.5 block max-w-72 truncate font-mono text-xs text-muted-foreground" title={d.pattern}>
                        {d.pattern}
                      </code>
                    </td>
                    <td className="py-2 pr-3 font-mono text-xs">{d.placeholder}</td>
                    <td className="py-2 pr-3">
                      {d.usedBy.length === 0 ? (
                        <span className="text-xs text-muted-foreground">None</span>
                      ) : (
                        <span className="flex flex-col gap-1">
                          {d.usedBy.map((u) => (
                            <span key={u.rule} className="flex flex-wrap items-center gap-1.5 text-xs">
                              <span className="font-mono">
                                {u.rule} v{u.version}
                              </span>
                              <StateChip tone="neutral" className={modeChip[u.mode].className}>
                                {modeChip[u.mode].label}
                              </StateChip>
                              <span className="text-muted-foreground">{u.mode === 'monitor' ? `would ${u.action}` : u.action}</span>
                            </span>
                          ))}
                        </span>
                      )}
                    </td>
                    <td className="num py-2 pr-3 text-right font-mono text-xs">
                      {int(d.redactedRequests24h)}
                      {d.redactedMatches24h > d.redactedRequests24h && <span className="block text-muted-foreground">{int(d.redactedMatches24h)} matches</span>}
                    </td>
                    <td className="num py-2 pr-3 text-right font-mono text-xs">{int(d.blocked24h)}</td>
                    <td className="num py-2 text-right font-mono text-xs">
                      {d.falsePositives30d + d.confirmed30d === 0 ? (
                        <span className="text-muted-foreground">none reviewed</span>
                      ) : (
                        <>
                          {int(d.falsePositives30d)}
                          <span className="block text-muted-foreground">of {int(d.falsePositives30d + d.confirmed30d)} reviewed</span>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <p className="mt-2 text-xs text-muted-foreground">
          Counts are requests in the last 24 hours, from receipts: redactions by entity, and blocks that name the entity they matched. They include rules that were
          enforcing at the time, even if they’ve changed since. Monitor-mode matches aren’t counted: the receipt records “would redact” without the entity. False
          positives are hits a reviewer marked wrong below, out of the hits reviewed, on requests from the last 30 days.
        </p>
      </Section>

      <Section
        title="Custom entities"
        description="Your own entity types, such as internal account or employee IDs. Rules name them in “Prompt contains entity” like the built-ins, and Warden reloads as soon as you save."
        actions={
          editing === null && (
            <Button size="sm" variant="outline" onClick={() => setEditing('new')} disabled={!can('detectors').ok} title={can('detectors').reason}>
              Add entity
            </Button>
          )
        }
      >
        {editing !== null ? (
          <EntityForm key={editing === 'new' ? 'new' : editing.id} existing={editing === 'new' ? undefined : editing} onDone={changed} onCancel={() => setEditing(null)} />
        ) : (
          <p className="text-sm text-muted-foreground">
            {data.some((d) => d.custom) ? 'Custom entities are listed above with the built-ins; choose Edit on one to change or delete it.' : 'No custom entities yet.'}
          </p>
        )}
      </Section>

      <ReviewQueue detectors={data} onReviewed={reload} />
    </div>
  )
}

/** Adds or edits a custom entity: the server checks the pattern (RE2, bounded, never empty) and the examples on every save. */
function EntityForm({ existing, onDone, onCancel }: { existing?: CustomEntity; onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState(existing?.name ?? '')
  const [pattern, setPattern] = useState(existing?.pattern ?? '')
  const [label, setLabel] = useState(existing?.label ?? '')
  const [must, setMust] = useState((existing?.mustMatch ?? []).join('\n'))
  const [mustNot, setMustNot] = useState((existing?.mustNotMatch ?? []).join('\n'))
  const [sample, setSample] = useState('')
  const [tried, setTried] = useState<EntityCheck | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const input = (): EntityInput => ({ name: name.trim(), pattern, label: label.trim(), mustMatch: lines(must), mustNotMatch: lines(mustNot) })
  const run = async (f: () => Promise<void>) => {
    setBusy(true)
    setError('')
    try {
      await f()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.current) setError('Someone changed this entity since you opened it. Cancel and open it again to see their version.')
      else setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const test = () =>
    run(async () => {
      setTried(null)
      setTried(await checkEntity(input(), sample, existing))
    })
  const save = () =>
    run(async () => {
      const e = existing ? await updateEntity(existing, input()) : await createEntity(input())
      toast.add({ title: existing ? 'Custom entity saved' : 'Custom entity added', description: `${e.name}. Recorded in the audit log; Warden reloads now.`, type: 'success' })
      onDone()
    })
  const remove = () =>
    run(async () => {
      await deleteEntity(existing!)
      toast.add({ title: 'Custom entity deleted', description: `${existing!.name}. Recorded in the audit log.`, type: 'success' })
      onDone()
    })
  return (
    <form
      aria-label={existing ? `Edit ${existing.name}` : 'New custom entity'}
      className="flex max-w-3xl flex-col gap-3 rounded-md border border-border p-3"
      onSubmit={(e) => {
        e.preventDefault()
        void save()
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field>
          <FieldLabel>Name</FieldLabel>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="employee ID" autoComplete="off" disabled={!!existing} />
          <FieldDescription>{existing ? 'Rules name it by this, so it can’t change.' : 'How rules name it. Letters, digits, spaces, dashes; it can’t change later.'}</FieldDescription>
        </Field>
        <Field>
          <FieldLabel>Placeholder label</FieldLabel>
          <Input value={label} onChange={(e) => setLabel(e.target.value.toUpperCase())} placeholder="EMPLOYEE" autoComplete="off" />
          <FieldDescription>A redaction writes [{label || 'LABEL'}_1], [{label || 'LABEL'}_2]… in place of each match.</FieldDescription>
        </Field>
      </div>
      <Field>
        <FieldLabel>Pattern</FieldLabel>
        <Input value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder="\bEMP-\d{6}\b" autoComplete="off" spellCheck={false} className="font-mono" />
        <FieldDescription>
          A regular expression in Go’s RE2 syntax: it runs in time proportional to the prompt’s length, so no pattern can hang Warden. No backreferences or lookaround;
          at most 512 characters; it must match at least one character. Add (?i) at the start to ignore case.
        </FieldDescription>
      </Field>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field>
          <FieldLabel>Must match (one per line)</FieldLabel>
          <Textarea aria-label="Must match" value={must} onChange={(e) => setMust(e.target.value)} rows={3} placeholder="badge EMP-004211" />
        </Field>
        <Field>
          <FieldLabel>Must not match (one per line)</FieldLabel>
          <Textarea aria-label="Must not match" value={mustNot} onChange={(e) => setMustNot(e.target.value)} rows={3} placeholder="EMP-42" />
        </Field>
      </div>
      <p className="text-xs text-muted-foreground">
        Examples are checked on every save, and stored with the entity so the next editor sees them: use made-up values, never real ones.
      </p>
      <Field>
        <FieldLabel>Try it on</FieldLabel>
        <Textarea aria-label="Try it on" value={sample} onChange={(e) => setSample(e.target.value)} rows={2} placeholder="Text to test the pattern on. Not saved." />
      </Field>
      {tried && (
        <div aria-label="Pattern test" className="rounded-sm bg-muted px-2 py-1.5 text-xs">
          <p>
            {tried.matches.length === 0 ? 'No matches.' : `${tried.matches.length} ${tried.matches.length === 1 ? 'match' : 'matches'}: `}
            {tried.matches.map((m, i) => (
              <code key={i} className="mr-1.5 font-mono">
                {m}
              </code>
            ))}
          </p>
          {tried.matches.length > 0 && <p className="mt-1 font-mono break-all text-muted-foreground">{tried.redacted}</p>}
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive-foreground">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {existing &&
          (confirmDelete ? (
            <>
              <span className="text-xs text-muted-foreground">Delete {existing.name}? Rules that name it must drop it first.</span>
              <Button size="sm" variant="destructive" type="button" disabled={busy} onClick={() => void remove()}>
                Delete entity
              </Button>
            </>
          ) : (
            <Button size="sm" variant="ghost" type="button" onClick={() => setConfirmDelete(true)}>
              Delete…
            </Button>
          ))}
        <span className="ml-auto" />
        <Button size="sm" variant="outline" type="button" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" variant="outline" type="button" disabled={busy || !pattern} onClick={() => void test()}>
          Test
        </Button>
        <Button size="sm" type="submit" disabled={busy || !name.trim() || !pattern || !label.trim()}>
          {existing ? 'Save entity' : 'Add entity'}
        </Button>
      </div>
    </form>
  )
}

const verdictLabel: Record<DetectorVerdict['verdict'], string> = { false_positive: 'False positive', confirmed: 'Correct' }

/** Recent hits to review, and the reviewer's verdict on each: what false-positive counts are made of. */
function ReviewQueue({ detectors, onReviewed }: { detectors: DetectorView[]; onReviewed: () => void }) {
  const [entity, setEntity] = useState('')
  const [all, setAll] = useState(false)
  const path = `/detectors/hits?limit=200${all ? '&review=all' : ''}${entity ? `&entity=${encodeURIComponent(entity)}` : ''}`
  const { data, loaded, reload } = useLive<DetectorHit[]>(path, [], 30_000)
  const [busy, setBusy] = useState('')
  const mark = async (h: DetectorHit, v: DetectorVerdict['verdict']) => {
    setBusy(h.receiptId + h.entity)
    try {
      await setVerdict(h, v)
      toast.add({ title: v === 'false_positive' ? 'Marked a false positive' : 'Marked correct', description: `${h.entity} on ${h.receiptId}. Recorded in the audit log.`, type: 'success' })
      onReviewed()
    } catch (e) {
      toast.add({
        title: 'Not recorded',
        description: e instanceof ApiError && e.status === 409 ? 'Someone reviewed this hit since the list loaded; it now shows their verdict.' : errorText(e),
        type: 'error',
      })
    } finally {
      setBusy('')
      reload()
    }
  }
  const options = [{ value: '', label: 'All entities' }, ...detectors.map((d) => ({ value: d.entity, label: d.entity }))]
  return (
    <Section
      title="False-positive review"
      description="Recent redactions and blocks, one row per entity per request, for a reviewer to mark correct or a false positive. Each verdict is audited."
    >
      <div className="mb-3 max-w-3xl rounded-sm bg-muted px-3 py-2 text-xs text-muted-foreground">
        <p className="font-medium text-foreground">What you can judge by</p>
        <p className="mt-1">
          Receipts keep hashes, not prompts: the text a detector matched is never stored. Each row shows the entity, how many matches, what was done and by which rule,
          and who sent the request, when and to which model.
        </p>
        <p className="mt-1">
          Where the backend captures content, the receipt also holds the prompt as it was sent on, with each match already replaced by its placeholder ([EMAIL_1]):
          open the receipt and reveal it (that’s audited) to read the words around the match. Blocked requests never reach a backend, so they have no content, and
          most receipts have none; then the metadata is all there is, and a verdict is a judgement on it.
        </p>
      </div>
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <Select items={options} value={entity} onValueChange={(v) => setEntity((v as string) ?? '')}>
          <SelectTrigger aria-label="Entity to review" className="w-48">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((o) => (
              <SelectItem key={o.value || 'all'} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button size="sm" variant={all ? 'outline' : 'secondary'} aria-pressed={!all} onClick={() => setAll(false)}>
          Unreviewed
        </Button>
        <Button size="sm" variant={all ? 'secondary' : 'outline'} aria-pressed={all} onClick={() => setAll(true)}>
          All
        </Button>
        <span className="text-xs text-muted-foreground">Last 7 days, newest first.</span>
      </div>
      {!loaded ? (
        <p className="text-sm text-muted-foreground">Loading hits…</p>
      ) : data.length === 0 ? (
        <p className="text-sm text-muted-foreground">{all ? 'No detector hits in the last 7 days.' : 'Nothing to review: every hit in the last 7 days has a verdict.'}</p>
      ) : (
        <div className="overflow-x-auto">
          <table aria-label="Detector hits to review" className="w-full min-w-[56rem] text-sm">
            <thead>
              <tr className="border-b border-border text-left text-xs text-muted-foreground">
                <th className="py-1.5 pr-3 font-medium">When</th>
                <th className="py-1.5 pr-3 font-medium">Entity</th>
                <th className="py-1.5 pr-3 font-medium">What happened</th>
                <th className="py-1.5 pr-3 font-medium">Request</th>
                <th className="py-1.5 pr-3 font-medium">Content</th>
                <th className="py-1.5 font-medium">Verdict</th>
              </tr>
            </thead>
            <tbody>
              {data.map((h) => (
                <tr key={h.receiptId + h.entity} className="border-b border-border align-top">
                  <td className="py-2 pr-3 font-mono text-xs whitespace-nowrap">{utc(h.ts)}</td>
                  <td className="py-2 pr-3 font-medium">{h.entity}</td>
                  <td className="py-2 pr-3 text-xs">
                    {h.action === 'redacted' ? `Redacted ${h.count} ${h.count === 1 ? 'match' : 'matches'}` : 'Blocked'}
                    {h.rules.length > 0 && <span className="block font-mono text-muted-foreground">{h.rules.join(', ')}</span>}
                  </td>
                  <td className="py-2 pr-3 text-xs">
                    <span className="font-mono">{h.keyName || 'unknown key'}</span>
                    <span className="block text-muted-foreground">
                      {h.team} · {h.model}
                    </span>
                  </td>
                  <td className="py-2 pr-3 text-xs">
                    <Link to={`/traffic?receipt=${h.receiptId}`} className="underline hover:text-foreground">
                      Open receipt
                    </Link>
                    <span className="block text-muted-foreground">{h.contentCaptured ? 'Captured: reveal shows placeholders in context' : 'Hashes only'}</span>
                  </td>
                  <td className="py-2 text-xs">
                    {h.verdict && (
                      <span className="mb-1 block">
                        <span className="font-medium">{verdictLabel[h.verdict.verdict]}</span>
                        <span className="text-muted-foreground"> · {h.verdict.by}</span>
                      </span>
                    )}
                    <span className="flex flex-wrap gap-1.5">
                      {h.verdict?.verdict !== 'false_positive' && (
                        <Button
                          size="xs"
                          variant="outline"
                          disabled={busy === h.receiptId + h.entity || !can('detectors').ok}
                          title={can('detectors').reason}
                          aria-label={`Mark ${h.entity} on ${h.receiptId} a false positive`}
                          onClick={() => void mark(h, 'false_positive')}
                        >
                          False positive
                        </Button>
                      )}
                      {h.verdict?.verdict !== 'confirmed' && (
                        <Button
                          size="xs"
                          variant="outline"
                          disabled={busy === h.receiptId + h.entity || !can('detectors').ok}
                          title={can('detectors').reason}
                          aria-label={`Mark ${h.entity} on ${h.receiptId} correct`}
                          onClick={() => void mark(h, 'confirmed')}
                        >
                          Correct
                        </Button>
                      )}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  )
}
