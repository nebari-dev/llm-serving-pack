import { ArrowLeftRight, CircleDollarSign, Gauge, KeyRound } from 'lucide-react'
import { NavLink as RouterLink, useLocation } from 'react-router-dom'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuLabel,
} from '@/components/ui/sidebar'
import { dataMode, session } from '@/data/catalog'

// Pages grouped by the job each one serves:
// watching the request stream, governing money and data.
const groups = [
  {
    label: 'Monitor',
    items: [
      { to: '/', label: 'Overview', icon: Gauge },
      { to: '/traffic', label: 'Traffic', icon: ArrowLeftRight },
    ],
  },
  {
    label: 'Govern',
    items: [
      { to: '/spend', label: 'Spend', icon: CircleDollarSign },
      { to: '/keys', label: 'Keys', icon: KeyRound },
    ],
  },
]

// Mock mode names the mockup's version; against openmeter-api, the builds
// actually running.
function versions() {
  if (dataMode !== 'api') return 'console v0.1'
  return `console ${__CONSOLE_VERSION__} · control plane ${session.versions.controlPlane}`
}

export function AppSidebar() {
  const { pathname } = useLocation()
  const isActive = (to: string) => (to === '/' ? pathname === '/' : pathname.startsWith(to))

  return (
    <Sidebar className="relative h-full rounded-none border-r border-sidebar-border" aria-label="Primary">
      <SidebarContent>
        <div className="flex flex-col gap-4">
          {groups.map((g) => (
            <SidebarGroup key={g.label} role="group" aria-labelledby={`nav-${g.label}`}>
              {/* Sentence case, no all-caps: override nebari's uppercase label. */}
              <SidebarGroupLabel id={`nav-${g.label}`} className="text-xs tracking-normal normal-case">
                {g.label}
              </SidebarGroupLabel>
              <SidebarMenu>
                {g.items.map(({ to, label, icon: Icon }) => (
                  <SidebarMenuItem key={to}>
                    <SidebarMenuButton active={isActive(to)} tooltip={label} render={<RouterLink to={to} />}>
                      <Icon className="size-4 shrink-0" aria-hidden="true" />
                      <SidebarMenuLabel>{label}</SidebarMenuLabel>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroup>
          ))}
        </div>
      </SidebarContent>
      <SidebarFooter className="text-xs text-muted-foreground-strong group-data-[state=collapsed]/sidebar:hidden">
        <div className="flex flex-col gap-0.5 px-2 py-1">
          <span>{dataMode === 'api' ? `${session.tenant.name} · ${session.environment}` : 'Demo tenant · synthetic traffic'}</span>
          <span className="font-mono text-[11px]">{versions()}</span>
        </div>
      </SidebarFooter>
    </Sidebar>
  )
}
