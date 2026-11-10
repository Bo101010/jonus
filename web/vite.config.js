import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// The build output goes inside the Go package that embeds it: //go:embed can
// only reach files in its own directory, so the conventional web/dist would not
// be visible to the server.
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    // The dev server proxies the API so the frontend uses the same relative
    // URLs in development and in the built binary, where both are one origin.
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:7777',
        changeOrigin: false,
      },
    },
  },
})
