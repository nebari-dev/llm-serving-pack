import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, './src') } },
  // `npm run dev:api` talks to the Go control plane (server/, `make dev`).
  server: { proxy: { '/api': process.env.STARGATE_API ?? 'http://localhost:8080' } },
})
