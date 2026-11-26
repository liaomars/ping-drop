import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  server: {
    proxy: {
      '/ws': {
        target: 'http://127.0.0.1:8080',
        ws: true
      },
      '/api': {
        target: 'http://127.0.0.1:8080'
      }
    }
  },
  test: {
    environment: 'jsdom',
    globals: true
  }
})
