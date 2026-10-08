import { describe, expect, it } from 'vitest'
import { envDisplay } from '@/components/shell/app-header'

describe('envDisplay', () => {
  // §7.4: production must be unmistakable. Against a control plane, the look
  // follows the environment it serves, never the mockup's switcher.
  it('follows the control plane’s environment in api mode', () => {
    expect(envDisplay('api', 'production', 'staging')).toEqual({ label: 'Production', production: true })
    expect(envDisplay('api', 'development', 'production')).toEqual({ label: 'Development', production: false })
    expect(envDisplay('api', 'test', 'production')).toEqual({ label: 'Test', production: false })
  })
  it('follows the switcher in mock mode', () => {
    expect(envDisplay('mock', 'development', 'production')).toEqual({ label: 'Production', production: true })
    expect(envDisplay('mock', 'development', 'staging')).toEqual({ label: 'Staging', production: false })
  })
})
