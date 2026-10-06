import { useEffect, useMemo, useState } from 'react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { toast } from '@/components/ui/toast'
import {
  ApiError,
  type Budget,
  type BudgetInput,
  type BudgetPreview,
  budgetLabel,
  budgets as catalogBudgets,
  createBudget,
  dataMode,
  deleteBudget,
  fromWire,
  keys as catalogKeys,
  previewBudget,
  type Project,
  projects as catalogProjects,
  teams,
  THROTTLE_PER_MINUTE,
  updateBudget,
  type WireKey,
} from '@/data/catalog'
import { money, unpricedNote } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useLive } from '@/state/live'
import { NewProjectForm } from './project-dialogs'

// §7.5.5 budget writes on Spend: add (scope, monthly cap, what happens at the
// cap), edit (cap and action; the scope is fixed), delete. Every write shows
// its dry run first (§6), and an edit that lost a race shows both versions
// rather than overwriting (§6: a 409 renders as a merge).

const api = dataMode === 'api'

/** A cap in dollars, with cents only when it has them ("$40,000", "$0.01"). */
export const capMoney = (usd: number) => money(usd, Number.isInteger(usd) ? 0 : 2)

type ScopeType = Budget['scopeType']
type OnExceed = Budget['onExceed']

const scopeTypes: { value: ScopeType; label: string; description: string }[] = [
  { value: 'team', label: 'Team', description: 'Every key on the team.' },
  { value: 'project', label: 'Project', description: 'Every key in the project, now or later.' },
  { value: 'key', label: 'Key', description: 'One key.' },
]

const actions: { value: OnExceed; label: string; description: string }[] = [
  { value: 'block', label: 'Block', description: 'New requests get 429 budget_exceeded until the month resets or the cap is raised.' },
  {
    value: 'throttle',
    label: 'Throttle',
    description: api
      ? `Over the cap, each key gets ${THROTTLE_PER_MINUTE} requests a minute. More get 429 budget_throttled, with Retry-After saying when to try again.`
      : 'Requests over the cap are throttled to 10 requests/min.',
  },
  {
    value: 'warn',
    label: 'Warn',
    description: api ? 'Requests over the cap are admitted and their receipts record it. Nothing is enforced.' : 'Owners are warned at the cap. Nothing is enforced.',
  },
]

const actionLabel = (a: OnExceed) => actions.find((x) => x.value === a)!.label

/** Dollars typed as "1,500" or "$0.25" → a number, or null if it isn't a valid cap. */
function parseCap(s: string): number | null {
  const t = s.replace(/[$,\s]/g, '')
  if (!/^\d+(\.\d{1,2})?$/.test(t)) return null
  const n = Number(t)
  return n > 0 && n < 1e12 ? n : null
}

type ScopeOption = { value: string; label: string; detail?: string }

/**
 * The scopes a budget can name, by id: teams, projects (keys or not) and
 * active keys. Projects and keys show by name; a project also shows its team,
 * since two teams can each have one of the same name.
 */
function useScopes(open: boolean) {
  const live = useLive<WireKey[] | null>(open && api ? '/keys' : null, null, 60_000)
  const liveProjects = useLive<Project[]>(open && api ? '/projects' : null, catalogProjects, 60_000)
  const active = (live.data ? live.data.map(fromWire) : catalogKeys).filter((k) => k.status !== 'revoked')
  const teamName = (id: string) => teams.find((t) => t.id === id)?.name ?? id
  const byName = (a: ScopeOption, b: ScopeOption) => a.label.localeCompare(b.label) || (a.detail ?? '').localeCompare(b.detail ?? '')
  const scopes: Record<ScopeType, ScopeOption[]> = {
    team: teams.map((t) => ({ value: t.id, label: t.name })),
    project: liveProjects.data.map((p) => ({ value: p.id, label: p.name, detail: teamName(p.team) })).sort(byName),
    key: active.map((k) => ({ value: k.id, label: k.name })).sort(byName),
  }
  return { loaded: !api || (live.loaded && liveProjects.loaded), reloadProjects: liveProjects.reload, ...scopes }
}

function errorText(e: unknown) {
  return e instanceof Error ? e.message : String(e)
}

function Covers({ names }: { names: string[] }) {
  if (!names.length) return <>Covers no active keys yet.</>
  const shown = names.slice(0, 6)
  return (
    <>
      Covers {names.length} active key{names.length === 1 ? '' : 's'}: {shown.join(', ')}
      {names.length > shown.length && ` and ${names.length - shown.length} more`}.
    </>
  )
}

function PreviewPanel({ preview, loading, error, input, edit }: { preview: BudgetPreview | null; loading: boolean; error: string; input: BudgetInput | null; edit: boolean }) {
  if (!input) return null
  if (error) return <p className="text-sm text-destructive-foreground">Couldn’t check this budget: {error}</p>
  if (!preview) return <p className="text-sm text-muted-foreground">{loading ? 'Checking what this would do…' : ''}</p>
  const b = preview.budget
  const pct = b.capUsd > 0 ? Math.round((b.currentUsd / b.capUsd) * 100) : 0
  return (
    <div className={cn('flex flex-col gap-2 rounded-md border border-border bg-muted/40 p-3 text-sm', loading && 'opacity-60')} aria-live="polite">
      <span className="font-medium">If you save</span>
      <span>
        <Covers names={preview.covers} />
      </span>
      {(api || edit) && (
        <span className="text-muted-foreground-strong">
          Spent {money(b.currentUsd)} this month, {pct}% of this cap. Projected {money(b.projectedUsd)} by month end at the trailing 7-day average.
          {!!b.unpricedRequests && ` ${unpricedNote(b.unpricedRequests, 'what it has spent')}.`}
        </span>
      )}
      {preview.overCap && (
        <Alert variant={input.onExceed === 'block' ? 'destructive' : 'warning'}>
          <AlertTitle>Already over this cap</AlertTitle>
          <AlertDescription>
            {input.onExceed === 'block'
              ? 'Covered keys get 429 budget_exceeded on their next request after you save.'
              : input.onExceed === 'throttle' && api
                ? `After you save, covered keys get ${THROTTLE_PER_MINUTE} requests a minute each; more get 429 budget_throttled with Retry-After.`
                : 'Requests stay admitted; their receipts record the budget over cap.'}
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}

/** Both versions of a budget whose edit lost a race, side by side (§6). */
function Merge({ theirs, mine }: { theirs: Budget; mine: { capUsd: number; onExceed: OnExceed } }) {
  const rows = [
    { field: 'Cap', now: capMoney(theirs.capUsd), yours: capMoney(mine.capUsd) },
    { field: 'At the cap', now: actionLabel(theirs.onExceed), yours: actionLabel(mine.onExceed) },
  ]
  return (
    <Alert variant="warning">
      <AlertTitle>This budget changed since you opened it</AlertTitle>
      <AlertDescription className="flex flex-col gap-2">
        <span>Nothing was saved. Keep their version, or save yours over it.</span>
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-muted-foreground">
              <th className="font-normal" />
              <th className="font-normal">Now</th>
              <th className="font-normal">Yours</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.field} className={cn(r.now !== r.yours && 'font-medium')}>
                <td>{r.field}</td>
                <td className="num font-mono">{r.now}</td>
                <td className="num font-mono">{r.yours}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </AlertDescription>
    </Alert>
  )
}

/**
 * Add a budget (`budget` null) or edit one. Mount it only while open, so each
 * opening starts from the budget as the table last showed it.
 */
export function BudgetDialog({ budget, onClose, onSaved }: { budget: Budget | null; onClose: () => void; onSaved: () => void }) {
  const [base, setBase] = useState(budget)
  const [scopeType, setScopeType] = useState<ScopeType>(budget?.scopeType ?? 'team')
  const [scope, setScope] = useState(budget?.scope ?? '')
  const [cap, setCap] = useState(budget ? String(budget.capUsd) : '')
  // No default action: what happens at the cap is a choice, not a fallback.
  const [onExceed, setOnExceed] = useState<OnExceed | ''>(budget?.onExceed ?? '')
  const [tried, setTried] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState<Budget | null>(null)
  const [preview, setPreview] = useState<{ for: string; data: BudgetPreview | null; error: string }>({ for: '', data: null, error: '' })
  const [addingProject, setAddingProject] = useState(false)
  const scopes = useScopes(true)

  const capUsd = parseCap(cap)
  const input: BudgetInput | null = scope && capUsd !== null && onExceed ? { scopeType, scope, capUsd, onExceed } : null
  const unchanged = !!base && !!input && input.capUsd === base.capUsd && input.onExceed === base.onExceed
  const taken = useMemo(() => new Set(catalogBudgets.filter((b) => b.id !== base?.id).map((b) => `${b.scopeType}:${b.scope}`)), [base])
  const inputKey = input ? JSON.stringify(input) : ''

  // The dry run, re-asked 300ms after the form settles.
  useEffect(() => {
    if (!input) return
    let live = true
    const t = setTimeout(() => {
      previewBudget(input, base ?? undefined).then(
        (data) => live && setPreview({ for: inputKey, data, error: '' }),
        (e) => live && setPreview({ for: inputKey, data: null, error: errorText(e) }),
      )
    }, 300)
    return () => {
      live = false
      clearTimeout(t)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inputKey, base])

  const save = async (over?: Budget) => {
    setTried(true)
    if (!input || busy || (unchanged && !over)) return
    setBusy(true)
    setError('')
    try {
      const target = over ?? base
      const saved = target ? await updateBudget(target, { capUsd: input.capUsd, onExceed: input.onExceed }) : await createBudget(input)
      const audit = api ? ' Recorded in the audit log.' : ''
      toast.add({
        title: target ? 'Budget saved' : 'Budget created',
        description: `${saved.scopeType} ${budgetLabel(saved)} · ${capMoney(saved.capUsd)} monthly, ${actionLabel(saved.onExceed).toLowerCase()} at the cap.${audit}`,
        type: 'success',
      })
      onSaved()
      onClose()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && base && e.current) setConflict(e.current as Budget)
      else if (e instanceof ApiError && e.status === 409) setError(`This ${scopeType} already has a budget. Edit that one instead.`)
      else if (e instanceof ApiError && e.status === 404) setError('This budget was deleted since you opened it.')
      else setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  const options = scopes[scopeType]
  const scopeLabel = scopeTypes.find((s) => s.value === scopeType)!.label
  const shown = preview.for === inputKey ? preview : { data: null, error: '' }

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      {/* Flex, not the default grid, so the form shrinks under max-h and its body scrolls. */}
      <DialogContent className="flex max-w-xl flex-col">
        <form
          className="flex min-h-0 flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (!conflict) void save()
          }}
        >
          <DialogHeader>
            <DialogTitle>{base ? `Edit budget: ${budgetLabel(base)}` : 'Add budget'}</DialogTitle>
            <DialogDescription>
              A monthly cap in UTC, on month-to-date spend. Every budget covering a key applies, and the strictest one over its cap decides.
            </DialogDescription>
          </DialogHeader>
          <div className="-mx-6 flex min-h-0 flex-col gap-4 overflow-y-auto px-6">
            {base ? (
              <p className="text-sm">
                <span className="text-muted-foreground">Scope</span> {base.scopeType} <span className="font-mono">{budgetLabel(base)}</span> · monthly.{' '}
                <span className="text-muted-foreground">To cap another scope, add a budget there.</span>
              </p>
            ) : (
              <>
                <fieldset className="flex flex-col gap-2">
                  <legend className="mb-1 text-sm font-medium">Applies to</legend>
                  <RadioGroup
                    className="grid grid-cols-3 gap-3"
                    value={scopeType}
                    onValueChange={(v) => {
                      setScopeType(v as ScopeType)
                      setScope('')
                      setAddingProject(false)
                    }}
                  >
                    {scopeTypes.map((s) => (
                      <RadioGroupItem key={s.value} value={s.value} description={s.description}>
                        {s.label}
                      </RadioGroupItem>
                    ))}
                  </RadioGroup>
                </fieldset>
                <Field invalid={tried && !scope}>
                  <FieldLabel>{scopeLabel}</FieldLabel>
                  <Select items={options} value={scope || null} onValueChange={(v) => setScope((v as string) ?? '')}>
                    <SelectTrigger aria-label={scopeLabel} className={cn(scopeType !== 'team' && 'font-mono')} disabled={!scopes.loaded || !options.length}>
                      <SelectValue placeholder={options.length ? `Choose a ${scopeLabel.toLowerCase()}` : `No ${scopeType === 'team' ? 'teams' : scopeType === 'project' ? 'projects' : 'active keys'}`} />
                    </SelectTrigger>
                    <SelectContent>
                      {options.map((o) => {
                        const has = taken.has(`${scopeType}:${o.value}`)
                        return (
                          <SelectItem key={o.value} value={o.value} disabled={has} className={cn(scopeType !== 'team' && 'font-mono')}>
                            {o.label}
                            {o.detail && <span className="font-sans text-xs text-muted-foreground">{o.detail}</span>}
                            {has && <span className="ml-auto font-sans text-xs text-muted-foreground">has a budget</span>}
                          </SelectItem>
                        )
                      })}
                    </SelectContent>
                  </Select>
                  {scopeType === 'project' && !addingProject && (
                    <FieldDescription>
                      A project can have a budget before it has keys.{' '}
                      <button type="button" className="underline hover:text-foreground" onClick={() => setAddingProject(true)}>
                        New project…
                      </button>
                    </FieldDescription>
                  )}
                  {tried && !scope && <FieldError match>Choose what this budget applies to.</FieldError>}
                </Field>
                {scopeType === 'project' && addingProject && (
                  <NewProjectForm
                    onCancel={() => setAddingProject(false)}
                    onCreated={(p) => {
                      scopes.reloadProjects()
                      setScope(p.id)
                      setAddingProject(false)
                    }}
                  />
                )}
              </>
            )}

            <Field invalid={tried && capUsd === null}>
              <FieldLabel>Monthly cap (USD)</FieldLabel>
              <Input value={cap} onChange={(e) => setCap(e.target.value)} inputMode="decimal" placeholder="5,000" className="num font-mono" autoComplete="off" />
              {base && capUsd !== null && capUsd !== base.capUsd && (
                <FieldDescription>
                  {capMoney(base.capUsd)} → {capMoney(capUsd)}
                </FieldDescription>
              )}
              {tried && capUsd === null && <FieldError match>Enter an amount above $0, with at most two decimal places.</FieldError>}
            </Field>

            <fieldset className="flex flex-col gap-2">
              <legend className="mb-1 text-sm font-medium">At the cap</legend>
              <RadioGroup value={onExceed || null} onValueChange={(v) => setOnExceed(v as OnExceed)}>
                {actions.map((a) => (
                  <RadioGroupItem key={a.value} value={a.value} variant="box" description={a.description}>
                    {a.label}
                  </RadioGroupItem>
                ))}
              </RadioGroup>
              {tried && !onExceed && <p className="text-sm text-destructive-foreground">Choose what happens when spend reaches the cap.</p>}
            </fieldset>

            {!conflict && <PreviewPanel preview={shown.data} loading={!!input && preview.for !== inputKey} error={shown.error} input={input} edit={!!base} />}
            {conflict && input && <Merge theirs={conflict} mine={input} />}
            {error && <p className="text-sm text-destructive-foreground">{error}</p>}
          </div>
          <DialogFooter>
            {conflict ? (
              <>
                <Button
                  variant="outline"
                  type="button"
                  onClick={() => {
                    setBase(conflict)
                    setCap(String(conflict.capUsd))
                    setOnExceed(conflict.onExceed)
                    setConflict(null)
                  }}
                >
                  Keep theirs
                </Button>
                <Button type="button" disabled={busy} onClick={() => void save(conflict)}>
                  Save mine over theirs
                </Button>
              </>
            ) : (
              <>
                <Button variant="outline" type="button" onClick={onClose}>
                  Cancel
                </Button>
                <Button type="submit" disabled={busy || unchanged}>
                  {base ? 'Save changes' : 'Create budget'}
                </Button>
              </>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Delete a budget. A budget that changed meanwhile is shown as it is now before it goes. */
export function DeleteBudgetDialog({ budget, onClose, onDeleted }: { budget: Budget; onClose: () => void; onDeleted: () => void }) {
  const [target, setTarget] = useState(budget)
  const [changed, setChanged] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const remove = async () => {
    setBusy(true)
    setError('')
    try {
      await deleteBudget(target)
      toast.add({ title: 'Budget deleted', description: `${target.scopeType} ${budgetLabel(target)}.${api ? ' Recorded in the audit log.' : ''}`, type: 'success' })
      onDeleted()
      onClose()
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && e.current) {
        setTarget(e.current as Budget)
        setChanged(true)
      } else if (e instanceof ApiError && e.status === 404) {
        onDeleted()
        onClose()
      } else setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            Delete the {target.scopeType} budget on <span className="font-mono">{budgetLabel(target)}</span>?
          </DialogTitle>
          <DialogDescription render={<div />} className="flex flex-col gap-2">
            <span className="text-foreground">
              Its {capMoney(target.capUsd)} cap ({actionLabel(target.onExceed).toLowerCase()} at the cap) stops applying
              {api ? ' as soon as the gateway reloads, within seconds' : ''}.
              {target.onExceed === 'block' && target.currentUsd >= target.capUsd && ' Keys it was blocking can spend again unless another budget stops them.'}
            </span>
            <span>Other budgets covering the same keys still apply. Spend already recorded stays in the receipts.</span>
          </DialogDescription>
        </DialogHeader>
        {changed && (
          <Alert variant="warning">
            <AlertTitle>This budget changed since you opened it</AlertTitle>
            <AlertDescription>Nothing was deleted. It now reads as above; delete it anyway?</AlertDescription>
          </Alert>
        )}
        {error && <p className="text-sm text-destructive-foreground">{error}</p>}
        <DialogFooter>
          <Button variant="outline" type="button" onClick={onClose}>
            Keep budget
          </Button>
          <Button variant="destructive" type="button" disabled={busy} onClick={() => void remove()}>
            Delete budget
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
