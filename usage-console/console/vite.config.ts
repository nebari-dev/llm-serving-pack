import { execSync } from 'node:child_process'
import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The console build's name in the version footer: the commit it was built from.
const consoleVersion = (() => {
  try {
    return execSync('git rev-parse --short HEAD', { stdio: ['ignore', 'pipe', 'ignore'] }).toString().trim()
  } catch {
    return 'dev'
  }
})()

export default defineConfig({
  define: { __CONSOLE_VERSION__: JSON.stringify(consoleVersion) },
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, './src') } },
  // `npm run dev:api` talks to the Go control plane (server/, `make dev`).
  server: { proxy: { '/api': process.env.STARGATE_API ?? 'http://localhost:8080' } },
})
