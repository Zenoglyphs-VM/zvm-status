import {copyFileSync, mkdirSync} from 'node:fs'
import {createRequire} from 'node:module'
import {dirname, resolve} from 'node:path'
import {defineConfig, loadEnv, type Plugin} from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwind from '@tailwindcss/vite'

// Served by the gateway at /status/, beside the API it reads
// at /status/api/. BASE_PATH is honoured so build-sites.ps1 can say so the way
// it does for the other three.
const base = process.env.BASE_PATH ?? '/status/'

// The mark and the pointer light come from @zvm/web in the zvm-core
// submodule at ../zvm-core, as in the explorer. vue is pinned to this app's copy so
// those files do not go looking for one above C:/dev/zenon.
const require = createRequire(import.meta.url)
const peer = (name: string) => dirname(require.resolve(`${name}/package.json`))

// The two latin faces are named in the CSS, so the browser only learns it needs
// them after the stylesheet arrives and is parsed: HTML, then CSS, then fonts,
// three round trips before the headline draws in its own face. Preloading them
// from the HTML fetches them alongside the CSS. Only the latin subsets: the
// others are behind a unicode-range and are never fetched for this page's text.
function preloadFonts(): Plugin {
  return {
    name: 'preload-latin-fonts',
    apply: 'build',
    transformIndexHtml: {
      order: 'post',
      handler(_html, ctx) {
        return Object.keys(ctx.bundle ?? {})
          .filter((f) => /(space-grotesk|jetbrains-mono)-latin-wght-normal-.*\.woff2$/.test(f))
          .map((f) => ({
            tag: 'link',
            attrs: {rel: 'preload', as: 'font', type: 'font/woff2', href: `${base}${f}`, crossorigin: ''},
            injectTo: 'head' as const,
          }))
      },
    },
  }
}

// The gateway serves index.html for any path under /status/; GitHub Pages
// has no rewrite, so the wallet view at rpc/ gets its own copy of the page.
function rpcView(): Plugin {
  return {
    name: 'rpc-view',
    apply: 'build',
    closeBundle() {
      const dist = resolve(__dirname, 'dist')
      mkdirSync(resolve(dist, 'rpc'), {recursive: true})
      copyFileSync(resolve(dist, 'index.html'), resolve(dist, 'rpc', 'index.html'))
    },
  }
}

// The node and origins this page reads are never built in (see .env.example).
// A build without them would ship a page that talks to nothing, so it stops
// here and says what to set.
function requireEnv(env: Record<string, string>, names: string[]) {
  const missing = names.filter((n) => !env[n])
  if (missing.length) {
    throw new Error(`${missing.join(', ')} not set: copy .env.example to .env, or set them in the environment`)
  }
}

export default defineConfig(({mode}) => {
  requireEnv(loadEnv(mode, '..', 'VITE_'), ['VITE_ZVM_NODE', 'VITE_NOM_NODE'])
  return {
    // One .env for the repository, beside the scripts that read it too.
    envDir: '..',
    base,
    resolve: {
      alias: {
        '@zvm/web': resolve(__dirname, '../zvm-core/web/src'),
        vue: peer('vue'),
      },
      dedupe: ['vue'],
    },
    plugins: [vue(), tailwind(), preloadFonts(), rpcView()],
    build: {target: 'es2022', assetsInlineLimit: 4096},
    server: {
      port: 47500,
      // `go run . -origin http://127.0.0.1:49080` in the repo root serves this.
      proxy: {'/status/api': {target: 'http://127.0.0.1:8090', rewrite: (p) => p.replace(/^\/status/, '')}},
    },
  }
})
