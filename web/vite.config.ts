import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// The tiny CLI go:embeds this build and serves it off the same localhost
// origin as the gRPC-web FlowService, so assets are referenced relatively
// (base './') and everything is bundled into a self-contained dist/.
export default defineConfig({
  plugins: [vue()],
  // Absolute base: the SPA is served from the origin root with history routing,
  // so assets must resolve to /assets/… regardless of route depth. A relative
  // base ('./') breaks nested routes like /flow/:id — the browser requests
  // /flow/assets/… which 404s to the index.html fallback (blank page).
  base: '/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // The editor pulls monaco lazily; keep chunks reasonable and quiet.
    chunkSizeWarningLimit: 2000,
  },
})
