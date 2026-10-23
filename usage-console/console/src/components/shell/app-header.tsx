import { Menu as MenuPrimitive } from '@base-ui/react/menu'
import { Check, ChevronDown, Clock, LogOut, Monitor, Moon, Search, Sun } from 'lucide-react'
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
import { dataMode, type Session, session, signOut } from '@/data/catalog'
import { cn } from '@/lib/utils'
import { type Env, timeRanges, useApp } from '@/state/app-state'
import { NebariLogo } from './nebari-logo'

const headerAction =
  'hover:bg-header-action-hover hover:no-underline focus-visible:ring-offset-0 active:bg-header-action-hover data-[popup-open]:bg-header-action-hover data-[popup-open]:no-underline'

function initials(a: Session['actor']) {
  const words = (a.name ?? a.email.split('@')[0]).split(/[\s._-]+/).filter(Boolean)
  return words
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase())
    .join('')
}

/**
 * The environment the header shows, and whether it's production (it must
 * be unmistakable). Against a control plane it's the one it serves;
 * only the mockup has a switcher.
 */
export function envDisplay(mode: 'api' | 'mock', served: string, mockEnv: Env): { label: string; production: boolean } {
  const name = mode === 'api' ? served : mockEnv
  return { label: name.charAt(0).toUpperCase() + name.slice(1), production: name === 'production' }
}

export function AppHeader() {
  const { env, setEnv, range, setRange, setPaletteOpen } = useApp()
  const { themeMode, setThemeMode } = useTheme()
  const { label: envLabel, production } = envDisplay(dataMode, session.environment, env)
  const actor = session.actor
  // Dev mode: no IdP, every caller is the dev user (dev@localhost, owner).
  const devMode = dataMode === 'api' && session.auth.mode === 'dev'

  return (
    <div className="relative">
      {/* Environment accent: production vs staging must be unmistakable. */}
      <div
        className={cn('h-1 w-full', production ? 'bg-env-production' : 'bg-[repeating-linear-gradient(135deg,var(--env-staging)_0_8px,transparent_8px_16px)]')}
        aria-hidden="true"
      />
      <NavigationMenu className="h-14 justify-between border-border bg-header pl-4 text-header-foreground">
        <div className="flex min-w-0 items-center gap-3">
          <SidebarTrigger className="hover:bg-header-action-hover" />
          <MenuBarBrand href="/" aria-label="Nebari Gateway: go to overview" className="gap-3 text-lg font-semibold">
            <NebariLogo height={32} />
            <span className="h-6 w-px bg-border-strong" aria-hidden="true" />
            <span>Gateway</span>
          </MenuBarBrand>

          {dataMode === 'api' ? (
            // One control plane serves one environment, so there's nothing to switch.
            <span
              className={cn('ml-2 inline-flex h-8 items-center gap-2 rounded-md border px-2.5', production ? 'border-env-production' : 'border-dashed border-env-staging')}
              aria-label={`Tenant ${session.tenant.name}, environment ${session.environment}`}
            >
              <span className="text-sm font-medium text-muted-foreground-strong">{session.tenant.name}</span>
              <span className="text-muted-foreground">/</span>
              <span
                className={cn(
                  'rounded-sm px-1.5 text-xs leading-5 font-semibold',
                  production ? 'bg-env-production text-primary-foreground' : 'border border-dashed border-env-staging text-foreground',
                )}
              >
                {envLabel}
              </span>
            </span>
          ) : (
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger
              variant="ghost"
              className={cn('ml-2 h-8 gap-2 border px-2.5', headerAction, production ? 'border-env-production' : 'border-dashed border-env-staging')}
              aria-label={`Environment: ${env}. Change environment`}
            >
              <span className="text-sm font-medium text-muted-foreground-strong">{session.tenant.name}</span>
              <span className="text-muted-foreground">/</span>
              <span
                className={cn(
                  'rounded-sm px-1.5 text-xs leading-5 font-semibold',
                  production ? 'bg-env-production text-primary-foreground' : 'border border-dashed border-env-staging text-foreground',
                )}
              >
                {envLabel}
              </span>
              <ChevronDown className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuPortal>
              <DropdownMenuContent className="w-64">
                <DropdownMenuGroup>
                  <DropdownMenuGroupLabel className="text-xs tracking-normal normal-case">Tenant {session.tenant.name} · environment</DropdownMenuGroupLabel>
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
          )}
        </div>

        <MenuBarActions className="gap-2">
          <Button
            variant="outline"
            className="hidden w-72 justify-between bg-canvas text-muted-foreground hover:no-underline md:inline-flex"
            onClick={() => setPaletteOpen(true)}
          >
            <span className="inline-flex items-center gap-2">
              <Search className="size-4" />
              Jump to key, model, trace ID
            </span>
            <Kbd>⌘K</Kbd>
          </Button>

          {/* One time range control, shared across pages. */}
          <DropdownMenu modal={false}>
            <DropdownMenuTrigger variant="ghost" className={cn('gap-1.5', headerAction)} aria-label="Time range">
              <Clock className="size-4" />
              <span className="text-sm">{timeRanges.find((t) => t.value === range)?.label}</span>
              <ChevronDown className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuPortal>
              <DropdownMenuContent align="end" className="w-56">
                <DropdownMenuGroup>
                  <DropdownMenuGroupLabel className="text-xs tracking-normal normal-case">Shared by Overview, Traffic and Spend</DropdownMenuGroupLabel>
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
            <DropdownMenuTrigger
              variant="ghost"
              aria-label="Account menu"
              className={cn('h-auto px-2.5 py-1', headerAction)}
            >
              <Avatar>
                <AvatarFallback className="bg-primary font-semibold text-primary-foreground">{initials(actor)}</AvatarFallback>
              </Avatar>
              <span className="hidden lg:inline">{actor.name ?? actor.email}</span>
              <ChevronDown />
            </DropdownMenuTrigger>
            <DropdownMenuPortal>
              <DropdownMenuContent align="end" className="w-[248px] p-2">
                <div className="border-b px-1.5 pb-2">
                  <p className="text-sm font-medium text-foreground">{actor.name ?? actor.email}</p>
                  {actor.name && <p className="text-xs text-muted-foreground">{actor.email}</p>}
                  <p className="text-xs text-muted-foreground">
                    {actor.roles.length ? `Role${actor.roles.length > 1 ? 's' : ''}: ${actor.roles.join(', ')}` : 'No role'}
                  </p>
                  {devMode && <p className="mt-1 text-xs text-muted-foreground">Dev mode · no sign-in ({actor.email} is {actor.roles.join(', ') || 'no role'})</p>}
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
                <DropdownMenuItem
                  disabled={devMode}
                  title={devMode ? 'Dev mode: no identity provider is configured, so there’s nothing to sign out of' : undefined}
                  onClick={() => {
                    if (dataMode === 'api' && !devMode) void signOut()
                  }}
                  className="leading-5 text-sign-out-foreground data-[highlighted]:text-sign-out-foreground"
                >
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
