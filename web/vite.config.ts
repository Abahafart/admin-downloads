/// <reference types="vitest" />
import { defineConfig, type Plugin } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { bootTable } from './src/theme/index';
import { bootScript } from './src/theme/boot';

// The daemon serves the built files from web/dist (embedded with go:embed in
// web/embed.go). During `npm run dev`, API calls are proxied to the daemon;
// `npm run dev:mock` (VITE_MOCK=1) uses the in-browser mock instead.
const api = process.env.ADESC_API ?? 'http://127.0.0.1:7979';

/** Inlines the no-flash appearance script (theme/boot.ts) into index.html. */
function adescBoot(): Plugin {
  return {
    name: 'adesc-boot',
    transformIndexHtml(html) {
      const marker = /<!--adesc-boot[\s\S]*?-->/;
      if (!marker.test(html)) throw new Error('index.html: falta el marcador <!--adesc-boot-->');
      return html.replace(marker, `<script>${bootScript(bootTable())}</script>`);
    },
  };
}

export default defineConfig({
  plugins: [adescBoot(), svelte()],
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
  },
  server: {
    proxy: {
      '/api': { target: api, changeOrigin: false },
      '/__adesc': { target: api, changeOrigin: false },
    },
  },
  // Tests run in Node but need Svelte's client runtime for runes in classes.
  resolve: process.env.VITEST ? { conditions: ['browser'] } : undefined,
  test: {
    include: ['src/**/*.test.ts'],
    environment: 'node',
  },
});
