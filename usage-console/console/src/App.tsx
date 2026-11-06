import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AppLayout } from '@/components/shell/app-layout'
import { ThemeProvider } from '@/hooks/theme-provider'
import { KeysPage } from '@/pages/keys'
import { OverviewPage } from '@/pages/overview'
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
              <Route path="keys" element={<KeysPage />} />
              {/* Any other path, such as a link to a page this console doesn't have, goes to Overview. */}
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Routes>
        </AppStateProvider>
      </BrowserRouter>
    </ThemeProvider>
  )
}
