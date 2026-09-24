import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

// Page scaffolding. §7.2 "Structure": borders and rules encode grouping —
// no card-per-section layout. Sections are separated by a rule, not boxed.

export function PageHeader({
  title,
  description,
  actions,
  children,
}: {
  title: string
  description?: ReactNode
  actions?: ReactNode
  children?: ReactNode
}) {
  return (
    <div className="flex flex-col gap-3 border-b border-border px-6 pt-5 pb-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-[28px] leading-9 font-semibold tracking-[-0.2px]">{title}</h1>
          {description && <p className="mt-0.5 max-w-3xl text-sm text-muted-foreground">{description}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </div>
  )
}

export function Section({
  title,
  description,
  actions,
  children,
  className,
  id,
}: {
  title?: ReactNode
  description?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
  id?: string
}) {
  return (
    <section id={id} className={cn('border-b border-border px-6 py-5 last:border-b-0', className)} aria-labelledby={id ? `${id}-h` : undefined}>
      {(title || actions) && (
        <div className="mb-3 flex flex-wrap items-end justify-between gap-2">
          <div>
            {title && (
              <h2 id={id ? `${id}-h` : undefined} className="text-lg leading-6 font-semibold">
                {title}
              </h2>
            )}
            {description && <p className="text-sm text-muted-foreground">{description}</p>}
          </div>
          {actions && <div className="flex items-center gap-2">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  )
}

export function EmptyState({ title, action }: { title: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 px-6 py-12 text-center">
      <p className="text-sm text-muted-foreground-strong">{title}</p>
      {action}
    </div>
  )
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm border border-border bg-canvas px-1 font-mono text-[11px] text-muted-foreground-strong">
      {children}
    </kbd>
  )
}
