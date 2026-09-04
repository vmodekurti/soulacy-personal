import { defineConfig } from 'vitest/config'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  // Component tests (the walkthrough overlay, the app shell that carries its
  // anchors) import .svelte files directly, so the test runner needs the same
  // compiler the build uses. Without this, `import App from './App.svelte'`
  // reaches Node as raw markup and every component test fails to collect.
  plugins: [svelte({ hot: false })],
  // Without the browser condition, `svelte` resolves to its server entry under
  // Node: components still render DOM, but onMount never fires — so a test can
  // mount a page, see markup, and pass while every lifecycle bug it was written
  // to catch goes unnoticed.
  resolve: { conditions: ['browser'] },
  test: {
    environment: 'node',
    setupFiles: ['./vitest.setup.js'],
    include: ['src/**/*.test.js'],
  },
})
