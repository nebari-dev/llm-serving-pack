import '@fontsource-variable/geist'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import './app.css'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { dataMode, hydrate, NoRole, SigningIn, signOut } from './data/catalog'

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
    // On its way to Keycloak: nothing to show.
    if (e instanceof SigningIn) return
    if (e instanceof NoRole) {
      const { actor, auth } = e.session
      root.render(
        <main style={{ padding: 32, fontFamily: 'system-ui', maxWidth: 640 }}>
          <h1 style={{ fontSize: 20, fontWeight: 600 }}>You don’t have a Stargate role yet</h1>
          <p>
            You’re signed in as <code>{actor.email}</code>, but you aren’t in any Stargate group in Keycloak, so there’s nothing you can see here.
          </p>
          <p>
            Ask an owner to add you to a group such as <code>{auth.groupPrefix ?? 'stargate-'}viewer</code>
            {auth.groupsUrl && (
              <>
                {' '}
                (<a href={auth.groupsUrl}>groups in Keycloak</a>)
              </>
            )}
            , then sign in again.
          </p>
          {auth.mode === 'oidc' && (
            <p>
              <button type="button" onClick={() => void signOut()}>
                Sign out
              </button>
            </p>
          )}
        </main>,
      )
      return
    }
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
