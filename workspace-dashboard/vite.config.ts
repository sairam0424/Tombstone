import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [
    tailwindcss(),
    react(),
  ],
  server: {
    port: 3000,
    host: '0.0.0.0',
    proxy: {
      '/api': { target: 'http://localhost:8081', changeOrigin: true },
      '/stream': { target: 'http://localhost:8080', changeOrigin: true, ws: true },
    },
  },
  build: { outDir: 'dist', sourcemap: true },
});
