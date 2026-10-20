import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AppLayout } from '@/components/shell/app-layout'
import { hiddenPages } from '@/data/catalog'
import { ThemeProvider } from '@/hooks/theme-provider'
import { ActivityPage } from '@/pages/activity'
import { GuardrailsPage } from '@/pages/guardrails'
import { KeysPage } from '@/pages/keys'
import { ModelsPage } from '@/pages/models'
import { OnboardingPage } from '@/pages/onboarding'
import { OverviewPage } from '@/pages/overview'
import { RoutingPage } from '@/pages/routing'
import { SettingsPage } from '@/pages/settings'
import { SpendPage } from '@/pages/spend'
import { TrafficPage } from '@/pages/traffic'
import { AppStateProvider } from '@/state/app-state'

/** A hidden page (VITE_HIDE_PAGES) sends its visitor to Overview. */
const page = (path: string, el: React.ReactElement) => (hiddenPages.has(path) ? <Navigate to="/" replace /> : el)

export default function App() {
  return (
    <ThemeProvider>
      <BrowserRouter>
        <AppStateProvider>
          <Routes>
            <Route element={<AppLayout />}>
              <Route index element={<OverviewPage />} />
              <Route path="traffic" element={page("/traffic", <TrafficPage />)} />
              <Route path="spend" element={page("/spend", <SpendPage />)} />
              <Route path="models" element={page("/models", <ModelsPage />)} />
              <Route path="routing" element={page("/routing", <RoutingPage />)} />
              <Route path="guardrails" element={page("/guardrails", <GuardrailsPage />)} />
              <Route path="keys" element={page("/keys", <KeysPage />)} />
              <Route path="activity" element={page("/activity", <ActivityPage />)} />
              <Route path="settings" element={page("/settings", <SettingsPage />)} />
              <Route path="onboarding" element={page("/onboarding", <OnboardingPage />)} />
            </Route>
          </Routes>
        </AppStateProvider>
      </BrowserRouter>
    </ThemeProvider>
  )
}
