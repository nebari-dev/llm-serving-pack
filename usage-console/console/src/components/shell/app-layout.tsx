import { Outlet } from 'react-router-dom'
import { ReceiptDrawer } from '@/components/gw/receipt-drawer'
import { SidebarProvider } from '@/components/ui/sidebar'
import { Toaster } from '@/components/ui/toast'
import { useApp } from '@/state/app-state'
import { AppHeader } from './app-header'
import { AppSidebar } from './app-sidebar'
import { CommandPalette } from './command-palette'
import { DegradationBanner } from './degradation-banner'

export function AppLayout() {
  const { density } = useApp()
  return (
    <SidebarProvider>
      <div className="flex h-dvh flex-col bg-canvas text-foreground" data-density={density}>
        <AppHeader />
        <div className="flex min-h-0 flex-1">
          <AppSidebar />
          <main className="flex min-w-0 flex-1 flex-col overflow-y-auto" id="main">
            <DegradationBanner />
            <Outlet />
          </main>
        </div>
      </div>
      <ReceiptDrawer />
      <CommandPalette />
      <Toaster />
    </SidebarProvider>
  )
}
