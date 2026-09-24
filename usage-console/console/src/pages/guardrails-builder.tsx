import { Braces, ListTree, Plus, TriangleAlert, X } from 'lucide-react'
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
import { cn } from '@/lib/utils'
import {
  type Action,
  type Cond,
  type Draft,
  entityOptions,
  fieldDefs,
  type Group,
  type Node,
  routeTargets,
  toJson,
  uid,
} from './guardrails-model'

// §7.5.7 rule builder / §8 RuleBuilder: nested condition tree, action list,
// keyboard-operable (every control is a native button, select, or input).

type Opt = { value: string; label: string }

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
}: {
  values: string[]
  onChange: (v: string[]) => void
  suggestions?: string[]
  freeText?: boolean
  label: string
}) {
  const [text, setText] = useState('')
  const remaining = (suggestions ?? []).filter((s) => !values.includes(s))
  return (
    <span className="flex flex-wrap items-center gap-1">
      {values.map((v) => (
        <Chip key={v} label={v} onRemove={() => onChange(values.filter((x) => x !== v))}>
          {v}
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
          <Select value={null} onValueChange={(v) => v != null && onChange([...values, v as string])}>
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
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )
      )}
    </span>
  )
}

function CondRow({ c, onChange, onRemove }: { c: Cond; onChange: (c: Cond) => void; onRemove: () => void }) {
  const def = fieldDefs.find((f) => f.value === c.field) ?? fieldDefs[0]
  return (
    <div className="flex flex-wrap items-center gap-1.5 py-1">
      <StrSelect
        label="Field"
        value={c.field}
        options={fieldDefs.map((f) => ({ value: f.value, label: f.label }))}
        onChange={(field) => {
          const nd = fieldDefs.find((f) => f.value === field)!
          onChange({ ...c, field, op: nd.ops[0], value: [] })
        }}
      />
      <StrSelect label="Operator" value={c.op} options={def.ops.map((o) => ({ value: o, label: o }))} onChange={(op) => onChange({ ...c, op })} />
      <ValueChips
        label={def.label}
        values={c.value}
        suggestions={def.suggestions}
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
}: {
  g: Group
  onChange: (g: Group) => void
  onRemove?: () => void
  depth?: number
}) {
  const set = (i: number, n: Node) => onChange({ ...g, children: g.children.map((x, j) => (j === i ? n : x)) })
  const del = (i: number) => onChange({ ...g, children: g.children.filter((_, j) => j !== i) })
  return (
    <div className={cn('flex flex-col', depth > 0 && 'rounded-md border border-border bg-muted/40 p-2')}>
      <div className="flex items-center gap-2 text-sm">
        <span className="text-muted-foreground">Match</span>
        <StrSelect
          label="Group combinator"
          value={g.combinator}
          options={[
            { value: 'all', label: 'all of' },
            { value: 'any', label: 'any of' },
          ]}
          onChange={(v) => onChange({ ...g, combinator: v as 'all' | 'any' })}
        />
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
            <CondRow key={n.id} c={n} onChange={(nc) => set(i, nc)} onRemove={() => del(i)} />
          ),
        )}
        <div className="flex gap-1 pt-1">
          <Button
            variant="ghost"
            size="xs"
            onClick={() => onChange({ ...g, children: [...g.children, { kind: 'cond', id: uid('c'), field: 'key.team', op: 'is', value: [] }] })}
          >
            <Plus /> Condition
          </Button>
          {depth < 2 && (
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
      </div>
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
            <Switch checked={a.rehydrate} onCheckedChange={(rehydrate) => onChange({ ...a, rehydrate })} aria-label="Rehydrate placeholders in the response" />
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

      <fieldset className="flex flex-col gap-2 border-t border-border pt-4">
        <legend className="text-base font-semibold">If Warden can't evaluate this rule</legend>
        <p className="-mt-1 text-xs text-muted-foreground">Control plane unreachable, cache stale, or the 50ms evaluation deadline passes. Required before publishing.</p>
        <RadioGroup
          value={draft.failMode ?? null}
          onValueChange={(v) => onChange({ ...draft, failMode: v as 'open' | 'closed' })}
          orientation="horizontal"
          aria-label="Fail mode"
          aria-required="true"
        >
          <RadioGroupItem value="closed" description="Reject the request. Default for data protection.">
            Block (fail-closed)
          </RadioGroupItem>
          <RadioGroupItem value="open" description="Let the request through unpoliced, and show a banner.">
            Allow (fail-open)
          </RadioGroupItem>
        </RadioGroup>
        {!draft.failMode && <p className="text-xs font-medium text-v-degraded-fg">Choose a fail mode to publish.</p>}
      </fieldset>
    </div>
  )
}
