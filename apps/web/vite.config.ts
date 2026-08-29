import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api/core': { target: 'http://127.0.0.1:8080', rewrite: (path) => path.replace(/^\/api\/core/, '') },
      '/api/rag': { target: 'http://127.0.0.1:8000', rewrite: (path) => path.replace(/^\/api\/rag/, '') },
    },
  }
})
