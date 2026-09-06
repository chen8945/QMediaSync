import { fileURLToPath, URL } from 'node:url'

import type { Plugin } from 'vite'
import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import { ElementPlusResolver } from 'unplugin-vue-components/resolvers'

const elementPlusComponentModules: Record<string, string> = {
  ElAside: 'container',
  ElBreadcrumbItem: 'breadcrumb',
  ElButtonGroup: 'button',
  ElCheckboxGroup: 'checkbox',
  ElCollapseItem: 'collapse',
  ElContainer: 'container',
  ElDescriptionsItem: 'descriptions',
  ElDropdownItem: 'dropdown',
  ElDropdownMenu: 'dropdown',
  ElFormItem: 'form',
  ElLoadingDirective: 'loading',
  ElMain: 'container',
  ElMenuItem: 'menu',
  ElOption: 'select',
  ElRadioButton: 'radio',
  ElRadioGroup: 'radio',
  ElSkeletonItem: 'skeleton',
  ElSubMenu: 'menu',
  ElTabPane: 'tabs',
  ElTableColumn: 'table',
}

const elementPlusRuntimeModules: Record<string, string> = {
  ElButton: 'button',
  ElMessage: 'message',
  ElMessageBox: 'message-box',
  ElTag: 'tag',
}

const toKebab = (name: string) =>
  name
    .replace(/^El/, '')
    .replace(/([a-z0-9])([A-Z])/g, '$1-$2')
    .toLowerCase()

type ElementPlusResolverItem = {
  type: 'component' | 'directive'
  resolve: (name: string) => unknown | Promise<unknown>
}

const createElementPlusResolver = (importStyle: 'css' | false = 'css') => {
  const resolvers = ElementPlusResolver({
    directives: true,
    importStyle,
  }) as ElementPlusResolverItem[]

  return resolvers.map((resolver) => ({
    type: resolver.type,
    async resolve(name: string) {
      const result = (await resolver.resolve(name)) as
        { name?: string; from?: string; sideEffects?: string | string[] } | undefined

      if (result && result.from === 'element-plus/es' && result.name?.startsWith('El')) {
        const moduleName = elementPlusComponentModules[result.name] ?? toKebab(result.name)
        return {
          ...result,
          from: `element-plus/es/components/${moduleName}/index.mjs`,
        }
      }

      return result
    },
  })) as ReturnType<typeof ElementPlusResolver>
}

const elementPlusRuntimeImportPlugin = (): Plugin => ({
  name: 'qms-element-plus-runtime-imports',
  enforce: 'pre',
  transform(code, id) {
    if (!id.includes('/src/') || !/\.(vue|ts)$/.test(id) || !code.includes('element-plus')) {
      return null
    }

    const nextCode = code.replace(
      /import\s*\{([^}]+)\}\s*from\s*['"]element-plus['"]/g,
      (statement, imports: string) => {
        const runtimeImports: string[] = []
        const typeImports: string[] = []
        const passthroughImports: string[] = []

        for (const rawImport of imports.split(',')) {
          const importName = rawImport.trim()

          if (!importName) {
            continue
          }

          if (importName.startsWith('type ')) {
            typeImports.push(importName.replace(/^type\s+/, ''))
            continue
          }

          const moduleName = elementPlusRuntimeModules[importName]
          if (moduleName) {
            runtimeImports.push(
              `import { ${importName} } from 'element-plus/es/components/${moduleName}/index.mjs'`,
            )
          } else {
            passthroughImports.push(importName)
          }
        }

        const importsToWrite = [
          ...runtimeImports,
          passthroughImports.length
            ? `import { ${passthroughImports.join(', ')} } from 'element-plus'`
            : '',
          typeImports.length ? `import type { ${typeImports.join(', ')} } from 'element-plus'` : '',
        ].filter(Boolean)

        return importsToWrite.length ? importsToWrite.join('\n') : statement
      },
    )

    return nextCode === code ? null : nextCode
  },
})

const isAppShellSource = (id: string) => id.includes('/src/App.vue') || id.endsWith('/src/main.ts')

const isStaticallyImportedByAppShell = (
  id: string,
  getModuleInfo: (id: string) => { importers: string[] } | null,
) => {
  const pending = [id]
  const visited = new Set<string>()

  while (pending.length) {
    const current = pending.pop()
    if (!current || visited.has(current)) {
      continue
    }
    visited.add(current)

    for (const importer of getModuleInfo(current)?.importers ?? []) {
      if (isAppShellSource(importer)) {
        return true
      }
      pending.push(importer)
    }
  }

  return false
}

// https://vite.dev/config/
export default defineConfig(({ mode }) => ({
  plugins: [
    vue(),
    elementPlusRuntimeImportPlugin(),
    AutoImport({
      dts: false,
      resolvers: createElementPlusResolver(mode === 'test' ? false : 'css'),
    }),
    Components({
      dts: false,
      resolvers: createElementPlusResolver(mode === 'test' ? false : 'css'),
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:12333',
        changeOrigin: true,
        ws: false,
      },
    },
  },
  test: {
    environment: 'happy-dom',
    include: ['test/**/*.test.{ts,mjs}'],
    // 让真实表单校验使用 Vite 的 CJS 互操作，避免 async-validator 的 default 嵌套导出。
    server: { deps: { inline: ['element-plus'] } },
  },
  build: {
    chunkSizeWarningLimit: 700,
    rollupOptions: {
      output: {
        manualChunks(id, { getModuleInfo }) {
          const normalizedId = id.split('\\').join('/')

          if (normalizedId.includes('/node_modules/vue-router/')) {
            return 'vendor-router'
          }
          if (
            normalizedId.includes('/node_modules/vue/') ||
            normalizedId.includes('/node_modules/@vue/') ||
            normalizedId.includes('/node_modules/pinia/')
          ) {
            return 'vendor-vue'
          }
          if (
            (normalizedId.includes('/node_modules/element-plus/') ||
              normalizedId.includes('/node_modules/@element-plus/')) &&
            isStaticallyImportedByAppShell(id, getModuleInfo)
          ) {
            return 'vendor-element-core'
          }
          if (
            normalizedId.includes('/node_modules/markdown-it/') ||
            normalizedId.includes('/node_modules/@wooorm/') ||
            normalizedId.includes('/node_modules/github-markdown-css/') ||
            normalizedId.includes('/node_modules/starry-night/')
          ) {
            return 'vendor-markdown'
          }
        },
      },
    },
  },
}))
