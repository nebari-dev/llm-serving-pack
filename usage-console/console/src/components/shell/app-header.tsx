import { Menu as MenuPrimitive } from '@base-ui/react/menu'
import { Bell, Check, ChevronDown, Clock, LogOut, Monitor, Moon, Search, Sun } from 'lucide-react'
import { Avatar, AvatarFallback } from '@/components/avatar'
import { Kbd } from '@/components/gw/page'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuGroupLabel,
  DropdownMenuItem,
  DropdownMenuPortal,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { MenuBarActions, MenuBarBrand, NavigationMenu } from '@/components/ui/navigation-menu'
import { SidebarTrigger } from '@/components/ui/sidebar'
import { useTheme } from '@/hooks/theme-provider'
import { isThemeMode } from '@/hooks/use-theme-preference'
import { cn } from '@/lib/utils'
import { type Env, timeRanges, useApp } from '@/state/app-state'
import { NebariLogo } from './nebari-logo'

const headerAction =
  'hover:bg-header-action-hover hover:no-underline focus-visible:ring-offset-0 active:bg-header-action-hover data-[popup-open]:bg-header-action-hover data-[popup-open]:no-underline'

const notifications = [
  { id: 1, unread: true, title: 'Budget "support" is over its cap', body: '$13,480 of $12,000 · throttling new requests', when: '4m ago' },
  { id: 2, unread: true, title: 'Drift on backend vllm-internal', body: 'replicas changed 4 → 2 by argocd', when: '3h ago' },
  { id: 3, unread: false, title: 'Rule block-src fired 7× its baseline', body: '96 blocks in 24h, 82 from support', when: '5h ago' },
]

export function AppHeader() {
  const { env, setEnv, range, setRange, setPaletteOpen } = useApp()
  const { themeMode, setThemeMode } = useTheme()
  const unread = notifications.filter((n) => n.unread).length

  return (
    <div className="relative">
      {/* Environment accent (§7.4): production vs staging must be unmistakable. */}
      <div
        className={cn('h-1 w-full', env === 'production' ? 'bg-env-production' : 'bg-[repeating-linear-gradient(135deg,var(--env-staging)_0_8px,transparent_8px_16px)]')}
        aria-hidden="true"
      />
      <NavigationMenu className="h-14 justify-between border-border bg-header pl-4 text-header-foreground">
        <div className="flex min-w-0 items-center gap-3">
          <SidebarTrigger className="hover:bg-header-action-hover" />
          <MenuBarBrand href="/" aria-label="Nebari Gateway — go to overview" className="gap-3 text-lg font-semibold">
            <NebariLogo height={32} />
            <span className="h-6 w-px bg-border-strong" aria-hidden="true" />
            <span>Gateway</span>
          </MenuBarBrand>

          <DropdownMenu modal={false}>
            <DropdownMenuTrigger
              variant="ghost"
              className={cn('ml-2 h-8 gap-2 border px-2.5', headerAction, env === 'production' ? 'border-env-production' : 'border-dashed border-env-staging')}
              aria-label={`Environment: ${env}. Change environment`}
            >
              <span className="text-sm font-medium text-muted-foreground-strong">acme</span>
              <span className="text-muted-foreground">/</span>
              <span
                className={cn(
                  'rounded-sm px-1.5 text-xs leading-5 font-semibold',
                  env === 'production' ? 'bg-env-production text-primary-foreground' : 'border border-dashed border-env-staging text-foreground',
                )}
              >
                {env === 'production' ? 'Production' : 'Staging'}
              </span>
              <ChevronDown className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuPortal>
              <DropdownMenuContent className="w-64">
                <DropdownMenuGroup>
                  <DropdownMenuGroupLabel className="text-xs tracking-normal normal-case">Tenant acme · environment</DropdownMenuGroupLabel>
                  {(['production', 'staging'] as Env[]).map((e) => (
                    <DropdownMenuItem key={e} onClick={() => setEnv(e)} className="justify-between">
                      <span className="inline-flex items-center gap-2">
                        <span
                          className={cn('size-2.5 rounded-full', e === 'production' ? 'bg-env-production' : 'border border-dashed border-env-staging')}
                        />
                        {e === 'production' ? 'Production' : 'Staging'}
                      </span>
                      {env === e && <Check className="size-4" />}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuGroup>
              </DropdownMenuContent>
            </DropdownMenuPortal>
          </DropdownMenu>
        </div>

        <MenuBarActions className="gap-2">
          <Button
            variant="outline"
            className="hidden w-72 justify-between bg-canvas text-muted-foreground hover:no-underline md:inline-flex"
            onClick={() => setPaletteOpen(true)}
          >
            <span className="inline-flex items-center gap-2">
              <Search className="size-4" />
              Jump to key, model, rule, trace ID
            </span>
            <Kbd>⌘K</Kbd>
          </Button>

          {/* One time range control, shared across surfaces (§7.4). */}
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger variant="ghost" className={cn('gap-1.5', headerAction)} aria-label="Time range">
              <Clock className="size-4" />
              <span className="text-sm">{timeRanges.find((t) => t.value === range)?.label}</span>
              <ChevronDown className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuPortal>
              <DropdownMenuContent align="end" className="w-56">
                <DropdownMenuGroup>
                  <DropdownMenuGroupLabel className="text-xs tracking-normal normal-case">Shared by Overview, Traffic, Spend, Activity</DropdownMenuGroupLabel>
                  {timeRanges.map((t) => (
                    <DropdownMenuItem key={t.value} onClick={() => setRange(t.value)} className="justify-between">
                      {t.label}
                      {range === t.value && <Check className="size-4" />}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuGroup>
              </DropdownMenuContent>
            </DropdownMenuPortal>
          </DropdownMenu>

          <DropdownMenu modal={false}>
            <DropdownMenuTrigger variant="ghost" aria-label={`Notifications, ${unread} unread`} className={cn('relative w-8 px-0', headerAction)}>
              <Bell />
              {unread > 0 && (
                <span className="absolute -top-0.5 -right-0.5 z-10 flex h-4 min-w-4 items-center justify-center rounded-full bg-notification-badge px-1 pt-px text-[9px] leading-none font-semibold text-white tabular-nums">
                  {unread}
                </span>
              )}
            </DropdownMenuTrigger>
            <DropdownMenuPortal>
              <DropdownMenuContent align="end" className="max-h-(--available-height) w-[552px] overflow-y-auto p-0">
                {notifications.map((n) => (
                  <DropdownMenuItem key={n.id} className="flex items-start gap-3 rounded-none border-b border-border px-4 py-3 last:border-b-0">
                    <span className={cn('mt-1.5 size-2 shrink-0 rounded-full', n.unread ? 'bg-primary' : 'bg-transparent')} aria-hidden="true" />
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="text-sm font-medium">{n.title}</span>
                      <span className="text-xs text-muted-foreground">{n.body}</span>
                    </span>
                    <span className="text-xs whitespace-nowrap text-muted-foreground">{n.when}</span>
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenuPortal>
          </DropdownMenu>

          <DropdownMenu modal={false}>
            <DropdownMenuTrigger
              variant="ghost"
              aria-label="Account menu"
              className={cn('h-auto px-2.5 py-1', headerAction)}
            >
              <Avatar>
                <AvatarFallback className="bg-primary font-semibold text-primary-foreground">PS</AvatarFallback>
              </Avatar>
              <span className="hidden lg:inline">Priya Shah</span>
              <ChevronDown />
            </DropdownMenuTrigger>
            <DropdownMenuPortal>
              <DropdownMenuContent align="end" className="w-[248px] p-2">
                <div className="border-b px-1.5 pb-2">
                  <p className="text-sm font-medium text-foreground">Priya Shah</p>
                  <p className="text-xs text-muted-foreground">priya@acme.dev · admin</p>
                </div>
                <div className="py-2">
                  <MenuPrimitive.RadioGroup
                    aria-label="Theme"
                    value={themeMode}
                    onValueChange={(value) => {
                      if (isThemeMode(value)) setThemeMode(value)
                    }}
                    className="flex h-[34px] items-center gap-1 rounded-md bg-muted p-1"
                  >
                    {(
                      [
                        ['light', 'Light', Sun],
                        ['dark', 'Dark', Moon],
                        ['system', 'System', Monitor],
                      ] as const
                    ).map(([value, label, Icon]) => (
                      <MenuPrimitive.RadioItem
                        key={value}
                        value={value}
                        aria-label={`${label} mode`}
                        title={`${label} mode`}
                        closeOnClick={false}
                        className={cn(
                          'flex h-auto flex-1 cursor-pointer items-center justify-center gap-1 rounded-sm border border-transparent px-1.5 py-0.5 text-sm font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring',
                          'text-muted-foreground-strong hover:text-foreground',
                          'data-checked:border-border-strong data-checked:bg-card data-checked:text-foreground data-checked:shadow-[0_1px_3px_0_rgba(0,0,0,0.10)]',
                        )}
                      >
                        <Icon className="h-4 w-4" />
                        <span>{label}</span>
                      </MenuPrimitive.RadioItem>
                    ))}
                  </MenuPrimitive.RadioGroup>
                </div>
                <DropdownMenuSeparator />
                <DropdownMenuItem className="leading-5 text-sign-out-foreground data-[highlighted]:text-sign-out-foreground">
                  <LogOut className="size-4 shrink-0" aria-hidden="true" />
                  Sign out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenuPortal>
          </DropdownMenu>
        </MenuBarActions>
      </NavigationMenu>
    </div>
  )
}
