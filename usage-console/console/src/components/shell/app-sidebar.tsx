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
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuLabel,
  SidebarSeparator,
} from '@/components/ui/sidebar'

// §7.4 information architecture.
const nav = [
  { to: '/', label: 'Overview', icon: Gauge },
  { to: '/traffic', label: 'Traffic', icon: ArrowLeftRight },
  { to: '/spend', label: 'Spend', icon: CircleDollarSign },
  { to: '/models', label: 'Models', icon: Boxes },
  { to: '/routing', label: 'Routing', icon: Route },
  { to: '/guardrails', label: 'Guardrails', icon: ShieldCheck },
  { to: '/keys', label: 'Keys', icon: KeyRound },
  { to: '/activity', label: 'Activity', icon: Activity },
]

export function AppSidebar() {
  const { pathname } = useLocation()
  const isActive = (to: string) => (to === '/' ? pathname === '/' : pathname.startsWith(to))

  return (
    <Sidebar className="h-full rounded-none border-r border-sidebar-border" aria-label="Primary">
      <SidebarContent>
        <SidebarMenu>
          {nav.map(({ to, label, icon: Icon }) => (
            <SidebarMenuItem key={to}>
              <SidebarMenuButton active={isActive(to)} tooltip={label} render={<RouterLink to={to} />}>
                <Icon className="size-4 shrink-0" aria-hidden="true" />
                <SidebarMenuLabel>{label}</SidebarMenuLabel>
              </SidebarMenuButton>
            </SidebarMenuItem>
          ))}
        </SidebarMenu>
        <SidebarSeparator />
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
