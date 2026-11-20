import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { kasProxy } from './server/kasProxy'

// Dev and preview servers bind to loopback only: the Studio pilot is local.
export default defineConfig({
  plugins: [react(), kasProxy()],
  server: { host: '127.0.0.1', port: 5174, strictPort: true },
  preview: { host: '127.0.0.1', port: 5175, strictPort: true },
  test: { include: ['src/**/*.test.ts', 'server/**/*.test.ts'] },
})
