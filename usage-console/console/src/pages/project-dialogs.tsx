import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { toast } from '@/components/ui/toast'
import {
  ApiError,
  can,
  createProject,
  dataMode,
  deleteProject,
  type Project,
  projectNameTaken,
  projects as catalogProjects,
  renameProject,
  teams,
} from '@/data/catalog'
import { useLive } from '@/state/live'

// Projects (§5.2): a team's grouping of keys, named for people. Receipts,
// budgets and rules name a project by id, so it can be renamed; it can be
// deleted once it has no active key and no budget.

const api = dataMode === 'api'
const teamName = (id: string) => teams.find((t) => t.id === id)?.name ?? id
const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e))

/** The picker's "make one" entry; never a project id. */
export const NEW_PROJECT = ' new'

/** What's wrong with a project name, as the server checks it, or ''. */
export function projectNameError(name: string, team: string, list: Project[], except?: string) {
  const n = name.trim()
  if (!n) return 'Enter a name.'
  if ([...n].length > 80) return 'Use at most 80 characters.'
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(n)) return 'Names can’t contain tabs or line breaks.'
  if (projectNameTaken(list, team, n, except)) return `${teamName(team)} already has a project by this name.`
  return ''
}

/** Every team's projects, re-read while `open` in api mode. */
export const useProjects = (open: boolean) => useLive<Project[]>(open && api ? '/projects' : null, catalogProjects, 60_000)

/**
 * A key's project: one of its team's, or a new one by name. Choosing "New
 * project…" names it; `resolve` creates it (audited) before the key, since
 * the gateway won't make a project from a key any more.
 */
export function useProjectChoice(team: string, open: boolean) {
  const live = useProjects(open)
  const [choice, setChoice] = useState('')
  const [newName, setNewName] = useState('')
  const teamProjects = live.data.filter((p) => p.team === team).sort((a, b) => a.name.localeCompare(b.name))
  const error = choice === NEW_PROJECT ? projectNameError(newName, team, live.data) : choice ? '' : 'Choose a project so spend can be attributed.'
  return {
    live,
    teamProjects,
    choice,
    setChoice,
    newName,
    setNewName,
    error,
    /** The chosen project's id; '' while a new one isn't made yet. */
    projectId: choice === NEW_PROJECT ? '' : choice,
    reset: () => {
      setChoice('')
      setNewName('')
    },
    /** The chosen project's id, creating a new one first. */
    resolve: async () => {
      if (choice !== NEW_PROJECT) return choice
      const p = await createProject(team, newName)
      toast.add({ title: 'Project created', description: `${p.name} on ${teamName(team)}.${api ? ' Recorded in the audit log.' : ''}`, type: 'success' })
      live.reload()
      setChoice(p.id)
      setNewName('')
      return p.id
    },
  }
}

export type ProjectChoice = ReturnType<typeof useProjectChoice>

/** The project select, and the new project's name when "New project…" is chosen. */
export function ProjectFields({ c, team, tried, when = 'along with the key' }: { c: ProjectChoice; team: string; tried: boolean; when?: string }) {
  const bad = tried && !!c.error
  return (
    <>
      <Field invalid={bad && c.choice !== NEW_PROJECT}>
        <FieldLabel>Project</FieldLabel>
        <Select
          items={[...c.teamProjects.map((p) => ({ value: p.id, label: p.name })), { value: NEW_PROJECT, label: 'New project…' }]}
          value={c.choice || null}
          onValueChange={(v) => c.setChoice((v as string) ?? '')}
        >
          <SelectTrigger aria-label="Project" disabled={!c.live.loaded}>
            <SelectValue placeholder={c.teamProjects.length ? 'Choose a project' : 'No projects yet'} />
          </SelectTrigger>
          <SelectContent>
            {c.teamProjects.map((p) => (
              <SelectItem key={p.id} value={p.id}>
                {p.name}
              </SelectItem>
            ))}
            <SelectItem value={NEW_PROJECT}>New project…</SelectItem>
          </SelectContent>
        </Select>
        {!c.teamProjects.length && c.live.loaded && c.choice !== NEW_PROJECT && (
          <FieldDescription>
            {teamName(team)} has no projects yet.{' '}
            <button type="button" className="underline hover:text-foreground" onClick={() => c.setChoice(NEW_PROJECT)}>
              Create one
            </button>
          </FieldDescription>
        )}
        {bad && c.choice !== NEW_PROJECT && <FieldError match>{c.error}</FieldError>}
      </Field>
      {c.choice === NEW_PROJECT && (
        <Field invalid={bad}>
          <FieldLabel>New project name</FieldLabel>
          <Input value={c.newName} onChange={(e) => c.setNewName(e.target.value)} placeholder="Help desk" autoComplete="off" />
          <FieldDescription>
            Created on {teamName(team)} {when}. Any name people will recognise; you can rename it later.
          </FieldDescription>
          {bad && <FieldError match>{c.error}</FieldError>}
        </Field>
      )}
    </>
  )
}

/** Adds a project on a team picked here, e.g. from the budget form, so it can be capped before it has keys. */
export function NewProjectForm({ onCreated, onCancel }: { onCreated: (p: Project) => void; onCancel: () => void }) {
  const live = useProjects(true)
  const [team, setTeam] = useState(teams[0]?.id ?? '')
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const invalid = name ? projectNameError(name, team, live.data) : ''
  const create = async () => {
    if (!name.trim() || invalid || busy) return
    setBusy(true)
    setError('')
    try {
      const p = await createProject(team, name)
      toast.add({ title: 'Project created', description: `${p.name} on ${teamName(team)}.${api ? ' Recorded in the audit log.' : ''}`, type: 'success' })
      onCreated(p)
    } catch (e) {
      setError(e instanceof ApiError && e.status === 409 ? 'That team already has a project by this name; choose it above.' : errorText(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border p-3">
      <div className="grid grid-cols-2 gap-3">
        <Field>
          <FieldLabel>Team</FieldLabel>
          <Select items={teams.map((t) => ({ value: t.id, label: t.name }))} value={team} onValueChange={(v) => v && setTeam(v as string)}>
            <SelectTrigger aria-label="Project team">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {teams.map((t) => (
                <SelectItem key={t.id} value={t.id}>
                  {t.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field invalid={!!invalid}>
          <FieldLabel>Project name</FieldLabel>
          <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Launch" autoComplete="off" />
          {invalid && <FieldError match>{invalid}</FieldError>}
        </Field>
      </div>
      {error && <p className="text-sm text-destructive-foreground">{error}</p>}
      <div className="flex justify-end gap-2">
        <Button variant="outline" size="sm" type="button" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" type="button" disabled={!name.trim() || !!invalid || busy || !can('projects').ok} title={can('projects').reason} onClick={() => void create()}>
          Create project
        </Button>
      </div>
    </div>
  )
}

/** One project's row: its name, or the rename field; and delete, which says why when it's refused. */
function ProjectRow({ p, all, onChanged }: { p: Project; all: Project[]; onChanged: () => void }) {
  const [renaming, setRenaming] = useState(false)
  const [name, setName] = useState(p.name)
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const invalid = renaming ? projectNameError(name, p.team, all, p.id) : ''
  const run = async (fn: () => Promise<unknown>, done: string) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      toast.add({ title: done, type: 'success' })
      setRenaming(false)
      setConfirming(false)
      onChanged()
    } catch (e) {
      // A stale rename or delete: someone else changed it. Show theirs.
      if (e instanceof ApiError && e.status === 409 && e.current) {
        setError('This project changed since you opened the list. It’s reloaded; try again.')
        onChanged()
      } else setError(errorText(e))
    } finally {
      setBusy(false)
    }
  }
  const label = `${p.name} (${teamName(p.team)})`
  return (
    <TableRow>
      <TableCell className="py-2 align-top text-sm">{teamName(p.team)}</TableCell>
      <TableCell className="py-2 align-top">
        {renaming ? (
          <Field invalid={!!invalid}>
            <Input aria-label={`New name for ${label}`} value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" className="h-8" />
            {invalid && <FieldError match>{invalid}</FieldError>}
          </Field>
        ) : (
          <span className="text-sm font-medium">{p.name}</span>
        )}
        {confirming && <p className="mt-1 text-xs text-muted-foreground">Delete {p.name}? Revoked keys keep their history under its name.</p>}
        {error && (
          <p role="alert" className="mt-1 text-xs text-destructive-foreground">
            {error}
          </p>
        )}
      </TableCell>
      <TableCell className="py-2 text-right align-top whitespace-nowrap">
        {renaming ? (
          <span className="inline-flex gap-1">
            <Button size="sm" variant="outline" onClick={() => (setRenaming(false), setName(p.name), setError(''))}>
              Cancel
            </Button>
            <Button size="sm" disabled={!!invalid || busy || name.trim() === p.name} onClick={() => void run(() => renameProject(p, name), 'Project renamed')}>
              Save name
            </Button>
          </span>
        ) : confirming ? (
          <span className="inline-flex gap-1">
            <Button size="sm" variant="outline" onClick={() => (setConfirming(false), setError(''))}>
              Keep
            </Button>
            <Button size="sm" variant="destructive" disabled={busy} onClick={() => void run(() => deleteProject(p), 'Project deleted')}>
              Delete project
            </Button>
          </span>
        ) : (
          <span className="inline-flex gap-1">
            <Button size="sm" variant="ghost" aria-label={`Rename ${label}`} disabled={!can('projects').ok} title={can('projects').reason} onClick={() => (setRenaming(true), setError(''))}>
              Rename
            </Button>
            <Button size="sm" variant="ghost" aria-label={`Delete ${label}`} disabled={!can('projects').ok} title={can('projects').reason} onClick={() => (setConfirming(true), setError(''))}>
              Delete
            </Button>
          </span>
        )}
      </TableCell>
    </TableRow>
  )
}

/** Every team's projects, to add, rename or delete (§5.2). */
export function ManageProjectsDialog({ open, onOpenChange, onChanged }: { open: boolean; onOpenChange: (o: boolean) => void; onChanged?: () => void }) {
  const live = useProjects(open)
  const [adding, setAdding] = useState(false)
  const [, bump] = useState(0)
  const changed = () => {
    live.reload()
    bump((n) => n + 1) // mock mode: the catalog changed in place
    onChanged?.()
  }
  const list = (api ? live.data : catalogProjects).slice().sort((a, b) => teamName(a.team).localeCompare(teamName(b.team)) || a.name.localeCompare(b.name))
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-w-2xl flex-col">
        <DialogHeader>
          <DialogTitle>Projects</DialogTitle>
          <DialogDescription>
            A project groups a team’s keys, so spend and budgets can follow it. Renaming keeps its keys, budgets, rules and history. A project can be deleted once it
            has no active keys and no budget.
          </DialogDescription>
        </DialogHeader>
        <div className="-mx-6 flex min-h-0 flex-col gap-3 overflow-y-auto px-6">
          {adding ? (
            <NewProjectForm
              onCancel={() => setAdding(false)}
              onCreated={() => {
                setAdding(false)
                changed()
              }}
            />
          ) : (
            <div>
              <Button size="sm" variant="outline" disabled={!can('projects').ok} title={can('projects').reason} onClick={() => setAdding(true)}>
                New project
              </Button>
            </div>
          )}
          <Table aria-label="Projects">
            <TableHeader>
              <TableRow>
                <TableHead>Team</TableHead>
                <TableHead>Project</TableHead>
                <TableHead>
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((p) => (
                <ProjectRow key={`${p.id}:${p.name}:${p.etag ?? ''}`} p={p} all={list} onChanged={changed} />
              ))}
            </TableBody>
          </Table>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
