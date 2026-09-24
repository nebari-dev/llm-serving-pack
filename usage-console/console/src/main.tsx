import '@fontsource-variable/geist'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import './app.css'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { dataMode, hydrate } from './data/catalog'

const root = createRoot(document.getElementById('root')!)

// In api mode the catalog must be loaded before any page module evaluates,
// since some derive data at import time; App is imported only afterwards.
hydrate()
  .then(() => import('./App'))
  .then(({ default: App }) =>
    root.render(
      <StrictMode>
        <App />
      </StrictMode>,
    ),
  )
  .catch((e: unknown) => {
    root.render(
      <main style={{ padding: 32, fontFamily: 'system-ui', maxWidth: 640 }}>
        <h1 style={{ fontSize: 20, fontWeight: 600 }}>Can't reach the control plane</h1>
        <p>
          The console is in <code>{dataMode}</code> mode and couldn't load <code>/api/v1/demo</code>: {e instanceof Error ? e.message : String(e)}
        </p>
        <p>
          Start it with <code>make dev</code> in <code>server/</code>, or run <code>npm run dev</code> for mock data.
        </p>
      </main>,
    )
  })
