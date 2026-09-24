import {
  Activity,
  ArrowLeftRight,
  Boxes,
  CircleDollarSign,
  Gauge,
  KeyRound,
  Route,
  Settings,
  ShieldCheck,
  Sparkles,
} from 'lucide-react'
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
  SidebarSeparator,
} from '@/components/ui/sidebar'

// §7.4 information architecture, grouped by the job each screen serves:
// watching the request stream, governing money and data, configuring the gateway.
const groups = [
  {
    label: 'Monitor',
    items: [
      { to: '/', label: 'Overview', icon: Gauge },
      { to: '/traffic', label: 'Traffic', icon: ArrowLeftRight },
      { to: '/activity', label: 'Activity', icon: Activity },
    ],
  },
  {
    label: 'Govern',
    items: [
      { to: '/spend', label: 'Spend', icon: CircleDollarSign },
      { to: '/guardrails', label: 'Guardrails', icon: ShieldCheck },
      { to: '/keys', label: 'Keys', icon: KeyRound },
    ],
  },
  {
    label: 'Configure',
    items: [
      { to: '/routing', label: 'Routing', icon: Route },
      { to: '/models', label: 'Models', icon: Boxes },
    ],
  },
]

export function AppSidebar() {
  const { pathname } = useLocation()
  const isActive = (to: string) => (to === '/' ? pathname === '/' : pathname.startsWith(to))

  return (
    <Sidebar className="h-full rounded-none border-r border-sidebar-border" aria-label="Primary">
      <SidebarContent>
        <div className="flex flex-col gap-4">
          {groups.map((g) => (
            <SidebarGroup key={g.label} role="group" aria-labelledby={`nav-${g.label}`}>
              {/* Spec §7.3: sentence case, no all-caps — override nebari's uppercase label. */}
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
        <SidebarSeparator className="my-4" />
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton active={isActive('/settings')} tooltip="Settings" render={<RouterLink to="/settings" />}>
              <Settings className="size-4 shrink-0" aria-hidden="true" />
              <SidebarMenuLabel>Settings</SidebarMenuLabel>
            </SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton active={isActive('/onboarding')} tooltip="Connect a provider" render={<RouterLink to="/onboarding" />}>
              <Sparkles className="size-4 shrink-0" aria-hidden="true" />
              <SidebarMenuLabel>Connect a provider</SidebarMenuLabel>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarContent>
      <SidebarFooter className="text-xs text-muted-foreground-strong group-data-[state=collapsed]/sidebar:hidden">
        <div className="flex flex-col gap-0.5 px-2 py-1">
          <span>Demo tenant · synthetic traffic</span>
          <span className="font-mono text-[11px]">console v0.1 · warden 0.4.2</span>
        </div>
      </SidebarFooter>
    </Sidebar>
  )
}
