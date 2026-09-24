import { BrowserRouter, Route, Routes } from 'react-router-dom'
import { AppLayout } from '@/components/shell/app-layout'
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

export default function App() {
  return (
    <ThemeProvider>
      <BrowserRouter>
        <AppStateProvider>
          <Routes>
            <Route element={<AppLayout />}>
              <Route index element={<OverviewPage />} />
              <Route path="traffic" element={<TrafficPage />} />
              <Route path="spend" element={<SpendPage />} />
              <Route path="models" element={<ModelsPage />} />
              <Route path="routing" element={<RoutingPage />} />
              <Route path="guardrails" element={<GuardrailsPage />} />
              <Route path="keys" element={<KeysPage />} />
              <Route path="activity" element={<ActivityPage />} />
              <Route path="settings" element={<SettingsPage />} />
              <Route path="onboarding" element={<OnboardingPage />} />
            </Route>
          </Routes>
        </AppStateProvider>
      </BrowserRouter>
    </ThemeProvider>
  )
}
