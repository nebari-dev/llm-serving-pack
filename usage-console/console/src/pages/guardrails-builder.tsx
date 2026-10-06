import { ArrowDown, ArrowUp, Braces, ListTree, Plus, Trash2, TriangleAlert, X } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { CodeBlock, CodeBlockBody } from '@/components/ui/code-block'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { backends, models, projects, type RuleVocabulary, teams } from '@/data/catalog'
import { cn } from '@/lib/utils'
import {
  type Action,
  blankApiRule,
  type Cond,
  type Draft,
  entityOptions,
  fieldDefs,
  type Group,
  type Node,
  type PolicyDraft,
  promptEntities,
  rerouteConflicts,
  routeTargets,
  ruleProblems,
  toJson,
  toPolicyContent,
  uid,
} from './guardrails-model'

// §7.5.7 rule builder / §8 RuleBuilder: nested condition tree, action list,
// keyboard-operable (every control is a native button, select, or input).
// Api mode builds a policy (§5.2): its rules in order, each with the actions
// the engine applies, and the policy's fail mode.

type Opt = { value: string; label: string }
type FieldDef = (typeof fieldDefs)[number]

const apiFieldLabels: Record<string, string> = {
  team: 'Team',
  project: 'Project',
  key: 'Key',
  model: 'Requested model',
  provider: 'Provider',
  'header x-data-region': 'Header x-data-region',
}

/** A project condition names the project by id (§5.1); people read its name and team. */
const projectLabel = (id: string) => {
  const p = projects.find((x) => x.id === id)
  return p ? `${p.name} · ${teams.find((t) => t.id === p.team)?.name ?? p.team}` : id
}

/** Api mode: the fields and values the server's validation accepts, nothing more. */
function apiFieldDefs(v: RuleVocabulary): FieldDef[] {
  const suggest: Record<string, string[] | undefined> = {
    team: teams.map((t) => t.name),
    project: projects.map((p) => p.id),
    model: models.map((m) => m.id),
    provider: [...new Set(backends.map((b) => b.provider))],
  }
  return [
    { value: 'prompt', label: 'Prompt', ops: ['contains entity'], suggestions: v.entities },
    ...v.fields.map((f) => ({
      value: f,
      label: apiFieldLabels[f] ?? f,
      ops: f.startsWith('header ') ? ['equals', 'not equals'] : ['is', 'is not'],
      suggestions: suggest[f],
      labelOf: f === 'project' ? projectLabel : undefined,
    })),
  ]
}

function StrSelect({
  value,
  onChange,
  options,
  label,
  className,
}: {
  value: string
  onChange: (v: string) => void
  options: Opt[]
  label: string
  className?: string
}) {
  return (
    <Select value={value} onValueChange={(v) => v != null && onChange(v as string)} items={options}>
      <SelectTrigger aria-label={label} className={cn('h-7 min-h-7 w-auto py-0.5 text-sm', className)}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function Chip({ children, onRemove, label }: { children: React.ReactNode; onRemove: () => void; label: string }) {
  return (
    <span className="inline-flex h-6 items-center gap-1 rounded-sm border border-border bg-muted pr-0.5 pl-1.5 font-mono text-xs text-muted-foreground-strong">
      {children}
      <button
        type="button"
        onClick={onRemove}
        aria-label={`Remove ${label}`}
        className="inline-flex size-4 items-center justify-center rounded-sm hover:bg-canvas focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        <X className="size-3" aria-hidden="true" />
      </button>
    </span>
  )
}

function ValueChips({
  values,
  onChange,
  suggestions,
  freeText,
  label,
  labelOf = (v) => v,
}: {
  values: string[]
  onChange: (v: string[]) => void
  suggestions?: string[]
  freeText?: boolean
  label: string
  labelOf?: (v: string) => string
}) {
  const [text, setText] = useState('')
  const remaining = (suggestions ?? []).filter((s) => !values.includes(s))
  return (
    <span className="flex flex-wrap items-center gap-1">
      {values.map((v) => (
        <Chip key={v} label={labelOf(v)} onRemove={() => onChange(values.filter((x) => x !== v))}>
          {labelOf(v)}
        </Chip>
      ))}
      {freeText || !suggestions ? (
        <input
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && text.trim()) {
              e.preventDefault()
              onChange([...values, text.trim()])
              setText('')
            }
          }}
          placeholder="Add value, Enter"
          aria-label={`Add ${label} value`}
          className="h-6 w-32 rounded-sm border border-dashed border-border-strong bg-transparent px-1.5 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring"
        />
      ) : (
        remaining.length > 0 && (
          <Select value={null} onValueChange={(v) => v != null && onChange([...values, v as string])} items={remaining.map((s) => ({ value: s, label: labelOf(s) }))}>
            <SelectTrigger
              aria-label={`Add ${label} value`}
              className="h-6 min-h-6 w-auto border-dashed bg-transparent py-0 pr-1 pl-1.5 text-xs text-muted-foreground shadow-none"
            >
              <Plus className="size-3" aria-hidden="true" />
              <span>Add</span>
            </SelectTrigger>
            <SelectContent className="min-w-44">
              {remaining.map((s) => (
                <SelectItem key={s} value={s}>
                  {labelOf(s)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )
      )}
    </span>
  )
}

function CondRow({ c, onChange, onRemove, defs }: { c: Cond; onChange: (c: Cond) => void; onRemove: () => void; defs: FieldDef[] }) {
  const def = defs.find((f) => f.value === c.field) ?? defs[0]
  return (
    <div className="flex flex-wrap items-center gap-1.5 py-1">
      <StrSelect
        label="Field"
        value={c.field}
        options={defs.map((f) => ({ value: f.value, label: f.label }))}
        onChange={(field) => {
          const nd = defs.find((f) => f.value === field)!
          onChange({ ...c, field, op: nd.ops[0], value: [] })
        }}
      />
      <StrSelect label="Operator" value={c.op} options={def.ops.map((o) => ({ value: o, label: o }))} onChange={(op) => onChange({ ...c, op })} />
      <ValueChips
        label={def.label}
        values={c.value}
        suggestions={def.suggestions}
        labelOf={def.labelOf}
        freeText={c.op === 'matches regex'}
        onChange={(value) => onChange({ ...c, value })}
      />
      <Button variant="ghost" size="icon-xs" onClick={onRemove} aria-label={`Remove condition ${def.label} ${c.op}`} className="ml-auto">
        <X />
      </Button>
    </div>
  )
}

function GroupEditor({
  g,
  onChange,
  onRemove,
  depth = 0,
  defs = fieldDefs,
  flat = false,
}: {
  g: Group
  onChange: (g: Group) => void
  onRemove?: () => void
  depth?: number
  defs?: FieldDef[]
  /** Api mode: one list, all of which must match. The engine has no groups or "any of". */
  flat?: boolean
}) {
  const set = (i: number, n: Node) => onChange({ ...g, children: g.children.map((x, j) => (j === i ? n : x)) })
  const del = (i: number) => onChange({ ...g, children: g.children.filter((_, j) => j !== i) })
  return (
    <div className={cn('flex flex-col', depth > 0 && 'rounded-md border border-border bg-muted/40 p-2')}>
      <div className="flex items-center gap-2 text-sm">
        <span className="text-muted-foreground">Match</span>
        {flat ? (
          <span>all of</span>
        ) : (
        <StrSelect
          label="Group combinator"
          value={g.combinator}
          options={[
            { value: 'all', label: 'all of' },
            { value: 'any', label: 'any of' },
          ]}
          onChange={(v) => onChange({ ...g, combinator: v as 'all' | 'any' })}
        />
        )}
        {onRemove && (
          <Button variant="ghost" size="xs" onClick={onRemove} className="ml-auto text-muted-foreground">
            Remove group
          </Button>
        )}
      </div>
      <div className="mt-1 ml-2 border-l border-border-strong pl-3">
        {g.children.length === 0 && <p className="py-1 text-xs text-muted-foreground">No conditions. This group matches every request.</p>}
        {g.children.map((n, i) =>
          n.kind === 'group' ? (
            <div key={n.id} className="py-1">
              <GroupEditor g={n} depth={depth + 1} onChange={(ng) => set(i, ng)} onRemove={() => del(i)} />
            </div>
          ) : (
            <CondRow key={n.id} c={n} defs={defs} onChange={(nc) => set(i, nc)} onRemove={() => del(i)} />
          ),
        )}
        <div className="flex gap-1 pt-1">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => onChange({ ...g, children: [...g.children, { kind: 'cond', id: uid('c'), field: flat ? 'team' : 'key.team', op: 'is', value: [] }] })}
          >
            <Plus /> Condition
          </Button>
          {!flat && depth < 2 && (
            <Button
              variant="ghost"
              size="xs"
              onClick={() =>
                onChange({
                  ...g,
                  children: [
                    ...g.children,
                    { kind: 'group', id: uid('g'), combinator: g.combinator === 'all' ? 'any' : 'all', children: [{ kind: 'cond', id: uid('c'), field: 'prompt', op: 'contains entity', value: [] }] },
                  ],
                })
              }
            >
              <Plus /> Group
            </Button>
          )}
        </div>
        {flat && <p className="pt-1 text-xs text-muted-foreground">Groups and “any of” aren’t connected yet: the engine evaluates one list of conditions, all of which must match.</p>}
      </div>
    </div>
  )
}

const actionLabels: Record<Action['type'], string> = { block: 'Block request', redact: 'Redact entities', reroute: 'Route to' }

const newAction = (t: Action['type'], d: Draft, vocab: RuleVocabulary, id = uid('a')): Action =>
  t === 'redact' ? { id, type: 'redact', entities: promptEntities(d), rehydrate: false } : t === 'reroute' ? { id, type: 'reroute', to: vocab.targets[0] ?? '' } : { id, type: 'block', message: '' }

/** Api mode: a rule's actions, at most one of each kind, and only what the engine does with them (§5.3). */
function ApiActions({ draft, vocab, onChange }: { draft: Draft; vocab: RuleVocabulary; onChange: (then: Action[]) => void }) {
  const found = promptEntities(draft)
  const used = new Set(draft.then.map((a) => a.type))
  const free = (['redact', 'reroute', 'block'] as const).filter((t) => !used.has(t))
  const set = (i: number, a: Action) => onChange(draft.then.map((x, j) => (j === i ? a : x)))
  return (
    <div className="flex flex-col gap-1">
      {draft.then.length === 0 && <p className="py-1 text-xs text-muted-foreground">No actions yet.</p>}
      {draft.then.map((a, i) => (
        <div key={a.id} className="flex flex-col gap-1 py-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <StrSelect
              label="Action"
              value={a.type}
              options={(['block', 'redact', 'reroute'] as const).filter((t) => t === a.type || !used.has(t)).map((t) => ({ value: t, label: actionLabels[t] }))}
              onChange={(t) => set(i, newAction(t as Action['type'], draft, vocab, a.id))}
            />
            {a.type === 'reroute' && (
              <StrSelect label="Route target" value={a.to} options={vocab.targets.map((t) => ({ value: t, label: t }))} onChange={(to) => set(i, { ...a, to })} className="font-mono" />
            )}
            {a.type === 'redact' && (
              <label className="ml-2 inline-flex items-center gap-1.5 text-xs text-muted-foreground-strong">
                <Switch checked={a.rehydrate} onCheckedChange={(rehydrate) => set(i, { ...a, rehydrate })} />
                Rehydrate on return
              </label>
            )}
            {draft.then.length > 1 && (
              <Button variant="ghost" size="icon-xs" onClick={() => onChange(draft.then.filter((_, j) => j !== i))} aria-label={`Remove ${actionLabels[a.type]} action`} className="ml-auto">
                <X />
              </Button>
            )}
          </div>
          <p className="text-xs text-muted-foreground">
            {a.type === 'block' && 'Callers get a 403 naming the rule, and the entity when a prompt condition matched one.'}
            {a.type === 'reroute' && 'Sends matching requests to that catalog model, or to a healthy backend in that region.'}
            {a.type === 'redact' &&
              found.length > 0 &&
              `Replaces what the prompt conditions find (${found.join(', ')}) with placeholders before the request leaves. ${
                a.rehydrate ? 'Warden puts the values back in the response, streamed or not, and the receipt counts them.' : 'Placeholders stay in the response.'
              }`}
          </p>
        </div>
      ))}
      {free.length > 0 && (
        <span className="flex flex-wrap gap-1 pt-1">
          {free.map((t) => (
            <Button key={t} variant="ghost" size="xs" aria-label={`Add ${actionLabels[t]} action`} onClick={() => onChange([...draft.then, newAction(t, draft, vocab)])}>
              <Plus /> {actionLabels[t]}
            </Button>
          ))}
        </span>
      )}
    </div>
  )
}

/** Api mode: one rule of a policy, in its place in the order. */
function ApiRuleEditor({
  draft,
  index,
  count,
  vocab,
  onChange,
  onMove,
  onRemove,
}: {
  draft: Draft
  index: number
  count: number
  vocab: RuleVocabulary
  onChange: (d: Draft) => void
  onMove: (by: -1 | 1) => void
  onRemove: () => void
}) {
  const problems = ruleProblems(draft)
  const label = draft.name || `rule ${index + 1}`
  return (
    <section aria-label={`Rule ${index + 1}`} className="flex flex-col gap-3 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-end gap-2">
        <span className="num pb-1.5 font-mono text-xs text-muted-foreground">{index + 1}</span>
        <label className="flex min-w-0 flex-col gap-1">
          <span className="text-xs text-muted-foreground">Rule name</span>
          <input
            value={draft.name}
            onChange={(e) => onChange({ ...draft, name: e.target.value.replace(/\s+/g, '-').toLowerCase() })}
            className="h-8 w-56 rounded-md border border-input bg-background px-2 font-mono text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <span className="ml-auto flex gap-0.5">
          <Button variant="ghost" size="icon-xs" aria-label={`Move ${label} up`} disabled={index === 0} onClick={() => onMove(-1)}>
            <ArrowUp />
          </Button>
          <Button variant="ghost" size="icon-xs" aria-label={`Move ${label} down`} disabled={index === count - 1} onClick={() => onMove(1)}>
            <ArrowDown />
          </Button>
          <Button variant="ghost" size="icon-xs" aria-label={`Remove ${label}`} disabled={count === 1} onClick={onRemove}>
            <Trash2 />
          </Button>
        </span>
      </div>
      <fieldset className="flex flex-col gap-1">
        <legend className="mb-1 text-sm font-semibold">When</legend>
        <GroupEditor g={draft.when} onChange={(when) => onChange({ ...draft, when })} defs={apiFieldDefs(vocab)} flat />
      </fieldset>
      <fieldset className="flex flex-col gap-1">
        <legend className="mb-1 text-sm font-semibold">Then</legend>
        <div className="ml-2 border-l border-border-strong pl-3">
          <ApiActions draft={draft} vocab={vocab} onChange={(then) => onChange({ ...draft, then })} />
        </div>
        {problems.map((p) => (
          <p key={p} className="mt-1 text-xs font-medium text-v-degraded-fg">
            {p}
          </p>
        ))}
      </fieldset>
    </section>
  )
}

/** Fail mode is the policy's (§4.5), and has to be answered before saving. */
function FailModeField({ value, onChange, what }: { value?: 'open' | 'closed'; onChange: (v: 'open' | 'closed') => void; what: string }) {
  return (
    <fieldset className="flex flex-col gap-2 border-t border-border pt-4">
      <legend className="text-base font-semibold">If Warden can't evaluate this {what}</legend>
      <p className="-mt-1 text-xs text-muted-foreground">Control plane unreachable, cache stale, or the 50ms evaluation deadline passes. Required before publishing.</p>
      <RadioGroup value={value ?? null} onValueChange={(v) => onChange(v as 'open' | 'closed')} orientation="horizontal" aria-label="Fail mode" aria-required="true">
        <RadioGroupItem value="closed" description="Reject the request. Default for data protection.">
          Block (fail-closed)
        </RadioGroupItem>
        <RadioGroupItem value="open" description="Let the request through unpoliced, and show a banner.">
          Allow (fail-open)
        </RadioGroupItem>
      </RadioGroup>
      {!value && <p className="text-xs font-medium text-v-degraded-fg">Choose a fail mode to publish.</p>}
    </fieldset>
  )
}

/** Api mode: a policy's name, its rules in evaluation order, and its fail mode, from the server's own vocabulary. */
export function PolicyBuilder({ draft, onChange, vocab }: { draft: PolicyDraft; onChange: (d: PolicyDraft) => void; vocab: RuleVocabulary }) {
  const [asJson, setAsJson] = useState(false)
  const setRule = (i: number, r: Draft) => onChange({ ...draft, rules: draft.rules.map((x, j) => (j === i ? r : x)) })
  const move = (i: number, by: -1 | 1) => {
    const rules = draft.rules.slice()
    ;[rules[i], rules[i + by]] = [rules[i + by], rules[i]]
    onChange({ ...draft, rules })
  }
  const conflicts = rerouteConflicts(draft.rules)
  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <label className="flex min-w-0 flex-col gap-1">
          <span className="text-xs text-muted-foreground">Policy name</span>
          <input
            value={draft.name}
            onChange={(e) => onChange({ ...draft, name: e.target.value.replace(/\s+/g, '-').toLowerCase() })}
            className="h-8 w-56 rounded-md border border-input bg-background px-2 font-mono text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <label className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="text-xs text-muted-foreground">Description</span>
          <input
            value={draft.description}
            onChange={(e) => onChange({ ...draft, description: e.target.value })}
            className="h-8 min-w-48 rounded-md border border-input bg-background px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <Button variant="outline" size="sm" onClick={() => setAsJson((v) => !v)} aria-pressed={asJson}>
          {asJson ? <ListTree /> : <Braces />}
          {asJson ? 'View as builder' : 'View as JSON'}
        </Button>
      </div>

      {asJson ? (
        <div className="flex flex-col gap-2">
          <p className="text-xs text-muted-foreground">Stored shape (read-only). Policies are stored structured; this text is a view of the builder, not the source.</p>
          <CodeBlock code={JSON.stringify(toPolicyContent(draft), null, 2)} className="w-full" showLineNumbers>
            <CodeBlockBody maxLines={24} className="text-xs" />
          </CodeBlock>
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          <div>
            <h3 className="text-base font-semibold">Rules, in order</h3>
            <p className="text-xs text-muted-foreground">
              Each rule’s conditions must all match. The first block wins and stops evaluation, redactions add up, and a later reroute overrides an earlier one.
            </p>
          </div>
          {draft.rules.map((r, i) => (
            <ApiRuleEditor
              key={r.when.id}
              draft={r}
              index={i}
              count={draft.rules.length}
              vocab={vocab}
              onChange={(nr) => setRule(i, nr)}
              onMove={(by) => move(i, by)}
              onRemove={() => onChange({ ...draft, rules: draft.rules.filter((_, j) => j !== i) })}
            />
          ))}
          {conflicts.map((c) => (
            <p key={c} role="alert" className="flex items-start gap-2 rounded-md border border-v-degraded-border bg-v-degraded-bg px-3 py-2 text-sm text-v-degraded-fg">
              <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
              <span>{c}</span>
            </p>
          ))}
          <Button variant="ghost" size="sm" className="w-fit" onClick={() => onChange({ ...draft, rules: [...draft.rules, blankApiRule()] })}>
            <Plus /> Add rule
          </Button>
        </div>
      )}

      <FailModeField value={draft.failMode} onChange={(failMode) => onChange({ ...draft, failMode })} what="policy" />
    </div>
  )
}

function ActionRow({ a, onChange, onRemove }: { a: Action; onChange: (a: Action) => void; onRemove: () => void }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5 py-1">
      <span className="w-20 shrink-0 text-sm font-medium">{a.type === 'redact' ? 'Redact' : a.type === 'reroute' ? 'Route to' : 'Block'}</span>
      {a.type === 'redact' && (
        <>
          <ValueChips label="entity" values={a.entities} suggestions={entityOptions} onChange={(entities) => onChange({ ...a, entities })} />
          <label className="ml-2 inline-flex items-center gap-1.5 text-xs text-muted-foreground-strong">
            <Switch checked={a.rehydrate} onCheckedChange={(rehydrate) => onChange({ ...a, rehydrate })} />
            Rehydrate on return
          </label>
        </>
      )}
      {a.type === 'reroute' && (
        <StrSelect label="Route target" value={a.to} options={routeTargets.map((t) => ({ value: t, label: t }))} onChange={(to) => onChange({ ...a, to })} className="font-mono" />
      )}
      {a.type === 'block' && (
        <input
          value={a.message}
          onChange={(e) => onChange({ ...a, message: e.target.value })}
          aria-label="Message returned to the caller"
          className="h-7 min-w-48 flex-1 rounded-md border border-input bg-background px-2 text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring"
        />
      )}
      <Button variant="ghost" size="icon-xs" onClick={onRemove} aria-label={`Remove ${a.type} action`} className="ml-auto">
        <X />
      </Button>
    </div>
  )
}

/** Mock mode's builder (the mockup): nested groups, any actions, and the rule's own fail mode. */
export function RuleBuilder({ draft, onChange }: { draft: Draft; onChange: (d: Draft) => void }) {
  const [asJson, setAsJson] = useState(false)
  const reroutes = draft.then.filter((a) => a.type === 'reroute')
  const hasBlock = draft.then.some((a) => a.type === 'block')
  const setAction = (i: number, a: Action) => onChange({ ...draft, then: draft.then.map((x, j) => (j === i ? a : x)) })

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <label className="flex min-w-0 flex-col gap-1">
          <span className="text-xs text-muted-foreground">Rule name</span>
          <input
            value={draft.name}
            onChange={(e) => onChange({ ...draft, name: e.target.value.replace(/\s+/g, '-').toLowerCase() })}
            className="h-8 w-56 rounded-md border border-input bg-background px-2 font-mono text-sm outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <Button variant="outline" size="sm" onClick={() => setAsJson((v) => !v)} aria-pressed={asJson}>
          {asJson ? <ListTree /> : <Braces />}
          {asJson ? 'View as builder' : 'View as JSON'}
        </Button>
      </div>

      {asJson ? (
        <div className="flex flex-col gap-2">
          <p className="text-xs text-muted-foreground">Stored shape (read-only). Rules are stored structured; this text is a view of the builder, not the source.</p>
          <CodeBlock code={JSON.stringify(toJson(draft), null, 2)} className="w-full" showLineNumbers>
            <CodeBlockBody maxLines={24} className="text-xs" />
          </CodeBlock>
        </div>
      ) : (
        <>
          <fieldset className="flex flex-col gap-1">
            <legend className="mb-1 text-base font-semibold">When</legend>
            <GroupEditor g={draft.when} onChange={(when) => onChange({ ...draft, when })} />
          </fieldset>

          <fieldset className="flex flex-col gap-1">
            <legend className="mb-1 text-base font-semibold">Then</legend>
            <div className="ml-2 border-l border-border-strong pl-3">
              {draft.then.length === 0 && <p className="py-1 text-xs text-muted-foreground">No actions. Matching requests are recorded but not changed.</p>}
              {draft.then.map((a, i) => (
                <ActionRow key={a.id} a={a} onChange={(na) => setAction(i, na)} onRemove={() => onChange({ ...draft, then: draft.then.filter((_, j) => j !== i) })} />
              ))}
              <DropdownMenu modal={false}>
                <DropdownMenuTrigger variant="ghost" className="mt-1 h-6 gap-1 px-2 text-xs">
                  <Plus className="size-3.5" /> Action
                </DropdownMenuTrigger>
                <DropdownMenuPortal>
                  <DropdownMenuContent className="min-w-44">
                    <DropdownMenuItem onClick={() => onChange({ ...draft, then: [...draft.then, { id: uid('a'), type: 'redact', entities: [], rehydrate: true }] })}>Redact entities</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => onChange({ ...draft, then: [...draft.then, { id: uid('a'), type: 'reroute', to: 'eu-private' }] })}>Route to backend</DropdownMenuItem>
                    <DropdownMenuItem onClick={() => onChange({ ...draft, then: [...draft.then, { id: uid('a'), type: 'block', message: 'Blocked by policy. See the receipt for the matching rule.' }] })}>
                      Block request
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenuPortal>
              </DropdownMenu>
            </div>
            {reroutes.length > 1 && (
              <p role="alert" className="mt-2 flex items-start gap-2 rounded-md border border-v-degraded-border bg-v-degraded-bg px-3 py-2 text-sm text-v-degraded-fg">
                <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
                <span>
                  Two route actions conflict. Reroute is last-write-wins, so only <span className="font-mono">{(reroutes[reroutes.length - 1] as { to: string }).to}</span> would apply. Remove one.
                </span>
              </p>
            )}
            {hasBlock && draft.then.length > 1 && (
              <p className="mt-2 text-xs text-muted-foreground">Block short-circuits: when it fires, the other actions in this rule don't run.</p>
            )}
          </fieldset>
        </>
      )}

      <FailModeField value={draft.failMode} onChange={(failMode) => onChange({ ...draft, failMode })} what="rule" />
    </div>
  )
}
