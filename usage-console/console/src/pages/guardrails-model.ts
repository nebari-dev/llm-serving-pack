import type { PolicyRule, RuleContent } from '@/data/catalog'

// Structured rule model for the builder (§5.3). Rules are stored structured,
// not as text — the JSON and YAML-ish renderings below are views of this.

export type Cond = { kind: 'cond'; id: string; field: string; op: string; value: string[] }
export type Group = { kind: 'group'; id: string; combinator: 'all' | 'any'; children: Node[] }
export type Node = Cond | Group

export type Action =
  | { id: string; type: 'redact'; entities: string[]; rehydrate: boolean }
  | { id: string; type: 'reroute'; to: string }
  | { id: string; type: 'block'; message: string }

export interface Draft {
  ruleId: string | null
  name: string
  description: string
  when: Group
  then: Action[]
  failMode?: 'open' | 'closed'
}

export const modeChip: Record<PolicyRule['mode'], { label: string; className?: string }> = {
  enforce: { label: 'Enforce' },
  monitor: { label: 'Monitor', className: 'border-dashed' },
  draft: { label: 'Draft', className: 'border-dashed bg-transparent' },
  disabled: { label: 'Disabled', className: 'border-dashed bg-transparent text-muted-foreground' },
}

let seq = 0
export const uid = (p = 'n') => `${p}${++seq}`

export const entityOptions = ['email', 'SSN', 'person', 'phone', 'credit card', 'secret', 'private key', 'source code', 'Acme account ID']

export const fieldDefs: { value: string; label: string; ops: string[]; suggestions?: string[] }[] = [
  { value: 'prompt', label: 'Prompt', ops: ['contains entity', 'matches regex'], suggestions: entityOptions },
  { value: 'response', label: 'Response', ops: ['contains entity', 'matches regex'], suggestions: entityOptions },
  { value: 'key.team', label: 'Team', ops: ['is', 'is not'], suggestions: ['support', 'agents', 'batch', 'web', 'research', 'security'] },
  { value: 'key.name', label: 'Key', ops: ['is', 'is not'], suggestions: ['support-bot', 'agents-prod', 'batch-summarize', 'web-chat', 'research', 'secops-triage'] },
  { value: 'model', label: 'Requested model', ops: ['is', 'is not'], suggestions: ['gpt-5-mini', 'gpt-5.5', 'claude-sonnet-5', 'claude-opus-4-1', 'claude-haiku-4-5', 'llama-3.3-70b'] },
  { value: 'provider', label: 'Provider', ops: ['is', 'is not'], suggestions: ['OpenAI', 'Anthropic', 'Bedrock', 'Azure', 'Self-hosted'] },
  { value: 'header.x-data-region', label: 'Header x-data-region', ops: ['equals', 'is not'], suggestions: ['eu', 'us', 'apac'] },
]

export const routeTargets = ['eu-private', 'cheap-summarize', 'default', 'gpt-5-mini', 'llama-3.3-70b']

const fieldFromMock: Record<string, string> = {
  prompt: 'prompt',
  team: 'key.team',
  model: 'model',
  provider: 'provider',
  'header x-data-region': 'header.x-data-region',
}

const opToJson: Record<string, string> = {
  'contains entity': 'contains_entity',
  'matches regex': 'matches',
  is: 'in',
  'is not': 'not_in',
  equals: 'eq',
}

export function fromRule(r: PolicyRule): Draft {
  const when: Group = {
    kind: 'group',
    id: uid('g'),
    combinator: 'all',
    children: r.when.map((c) => ({
      kind: 'cond' as const,
      id: uid('c'),
      field: fieldFromMock[c.field] ?? c.field,
      op: c.op === 'is' ? 'is' : c.op,
      value: c.value,
    })),
  }
  const then: Action[] = r.then.map((a) => {
    if (a.action === 'redact') {
      const [ents, rest = ''] = a.detail.split(' · ')
      return { id: uid('a'), type: 'redact', entities: ents.split(', '), rehydrate: rest.includes('rehydrate on return') }
    }
    if (a.action === 'route to') return { id: uid('a'), type: 'reroute', to: a.detail }
    return { id: uid('a'), type: 'block', message: a.detail }
  })
  return { ruleId: r.id, name: r.name, description: r.description, when, then, failMode: r.failMode }
}

export function blankDraft(): Draft {
  return {
    ruleId: null,
    name: 'new-rule',
    description: '',
    when: {
      kind: 'group',
      id: uid('g'),
      combinator: 'all',
      children: [{ kind: 'cond', id: uid('c'), field: 'prompt', op: 'contains entity', value: ['email'] }],
    },
    then: [{ id: uid('a'), type: 'redact', entities: ['email'], rehydrate: true }],
    failMode: undefined,
  }
}

function groupToJson(g: Group): unknown {
  return {
    [g.combinator]: g.children.map((n) =>
      n.kind === 'group' ? groupToJson(n) : { field: n.field, op: opToJson[n.op] ?? n.op, value: n.value },
    ),
  }
}

export function toJson(d: Draft) {
  return {
    when: groupToJson(d.when),
    then: d.then.map((a) => {
      if (a.type === 'redact') return { action: 'redact', entities: a.entities, rehydrate: a.rehydrate }
      if (a.type === 'reroute') return { action: 'reroute', to: a.to }
      return { action: 'block', message: a.message }
    }),
    else: [],
    fail_mode: d.failMode ?? null,
  }
}

/** Human-readable, line-oriented view used for version diffs. */
export function toLines(d: Draft): string[] {
  const out: string[] = [`rule: ${d.name}`, `fail_mode: ${d.failMode ?? '(unanswered)'}`, 'when:']
  const walk = (g: Group, depth: number) => {
    const pad = '  '.repeat(depth)
    out.push(`${pad}${g.combinator}:`)
    for (const n of g.children) {
      if (n.kind === 'group') walk(n, depth + 1)
      else out.push(`${pad}  - ${n.field} ${n.op} [${n.value.join(', ')}]`)
    }
  }
  walk(d.when, 1)
  out.push('then:')
  for (const a of d.then) {
    if (a.type === 'redact') out.push(`  - redact [${a.entities.join(', ')}]${a.rehydrate ? ' rehydrate' : ''}`)
    else if (a.type === 'reroute') out.push(`  - reroute to ${a.to}`)
    else out.push(`  - block "${a.message}"`)
  }
  return out
}

/** Minimal LCS line diff → unified text with +/- prefixes for DiffView. */
export function lineDiff(a: string[], b: string[]): string {
  const n = a.length
  const m = b.length
  const dp = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
  const out: string[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push(' ' + a[i])
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) out.push('-' + a[i++])
    else out.push('+' + b[j++])
  }
  while (i < n) out.push('-' + a[i++])
  while (j < m) out.push('+' + b[j++])
  return out.join('\n')
}

/** Count leaf conditions, for summaries. */
export function countConds(g: Group): number {
  return g.children.reduce((a, n) => a + (n.kind === 'group' ? countConds(n) : 1), 0)
}

/** Stable small hash so the simulated replay result is deterministic per draft. */
export function hashDraft(d: Draft): number {
  const s = JSON.stringify(toJson(d))
  let h = 2166136261
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619)
  return h >>> 0
}

// ---- api mode --------------------------------------------------------------
// The engine evaluates one flat list of conditions, all of which must match,
// and a rule's first action only (decisions §3). Api-mode drafts keep to that
// shape, and use the engine's field names ("team", not "key.team").

/** Entities the prompt conditions look for: what a redact removes. */
export function promptEntities(d: Draft): string[] {
  const out = new Set<string>()
  for (const n of d.when.children) if (n.kind === 'cond' && n.field === 'prompt' && n.op === 'contains entity') n.value.forEach((v) => out.add(v))
  return [...out]
}

const REHYDRATE = ' · rehydrate on return'

export function fromContent(c: RuleContent, ruleId: string | null): Draft {
  const children: Node[] = c.when.map((w) => ({ kind: 'cond', id: uid('c'), field: w.field, op: w.op, value: w.value }))
  const d: Draft = { ruleId, name: c.name, description: c.description, when: { kind: 'group', id: uid('g'), combinator: 'all', children }, then: [], failMode: c.failMode }
  d.then = c.then.slice(0, 1).map((a): Action => {
    if (a.action === 'redact') return { id: uid('a'), type: 'redact', entities: promptEntities(d), rehydrate: a.detail.includes(REHYDRATE.slice(3)) }
    if (a.action === 'route to') return { id: uid('a'), type: 'reroute', to: a.detail }
    return { id: uid('a'), type: 'block', message: a.detail }
  })
  return d
}

/**
 * The draft as the server stores it. A redact's detail names what it removes;
 * "rehydrate on return" on it is what tells Warden to put the values back in
 * the response (gateway.RehydrateOnReturn).
 */
export function toContent(d: Draft): RuleContent {
  return {
    name: d.name,
    description: d.description,
    failMode: d.failMode ?? 'closed',
    when: d.when.children.flatMap((n) => (n.kind === 'cond' ? [{ field: n.field, op: n.op, value: n.value }] : [])),
    then: d.then.slice(0, 1).map((a) => {
      if (a.type === 'redact') return { action: 'redact', detail: promptEntities(d).join(', ') + (a.rehydrate ? REHYDRATE : '') }
      if (a.type === 'reroute') return { action: 'route to', detail: a.to }
      return { action: 'block', detail: a.message }
    }),
  }
}

export const sameContent = (a: RuleContent, b: RuleContent) => JSON.stringify(a) === JSON.stringify(b)

export function blankApiDraft(): Draft {
  return {
    ruleId: null,
    name: '',
    description: '',
    when: { kind: 'group', id: uid('g'), combinator: 'all', children: [{ kind: 'cond', id: uid('c'), field: 'prompt', op: 'contains entity', value: [] }] },
    then: [{ id: uid('a'), type: 'block', message: '' }],
    failMode: undefined,
  }
}

/** The line view of stored content, for version and publish diffs. */
export const contentLines = (c: RuleContent) => toLines(fromContent(c, null))
