import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// Split large third-party libraries into their own chunks so the initial app
// bundle is smaller and each vendor caches independently across deploys.
function manualChunks(id) {
  if (!id.includes('node_modules')) return
  if (id.includes('echarts') || id.includes('zrender')) return 'vendor-echarts'
  if (id.includes('mermaid') || id.includes('cytoscape') || id.includes('dagre') || id.includes('elkjs')) return 'vendor-mermaid'
  if (id.includes('katex')) return 'vendor-katex'
  if (id.includes('highlight.js')) return 'vendor-highlight'
  if (id.includes('@xyflow') || id.includes('d3-')) return 'vendor-flow'
  if (id.includes('marked') || id.includes('dompurify')) return 'vendor-markdown'
  if (id.includes('qrcode')) return 'vendor-qrcode'
}

export default defineConfig({
  // Keep the Svelte 4 component constructor API while running the patched
  // Svelte 5 compiler/runtime. The app and its test harness migrate to mount()
  // separately; this compatibility mode does not re-enable the fixed SSR bugs.
  plugins: [svelte()],

  // During 'npm run dev', proxy API and WebSocket to the running gateway.
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:18789', changeOrigin: true },
      '/ws':  { target: 'ws://localhost:18789',  ws: true, changeOrigin: true },
    },
  },

  // Build output goes into the Go embed directory so 'make build' picks it up.
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      output: { manualChunks },
    },
  },
})
