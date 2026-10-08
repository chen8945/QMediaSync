// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import ElementPlus, { ElMessage } from 'element-plus'
import { createPinia, disposePinia, getActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import App from '@/App.vue'
import { HttpResponseError } from '@/http/errors'
import { useBackupStore } from '@/stores/backup'
import { useAuthStore } from '@/stores/auth'
import * as authAPI from '@/api/auth'

describe('AppBackupRestore', () => {
  it('仅接受 ZIP 备份，并提示恢复后重启服务', () => {
    const source = readFileSync(
      resolve(process.cwd(), 'src/components/AppBackupRestore.vue'),
      'utf8',
    )

    expect(source).toContain('提示：恢复结束后服务保持暂停，需重启应用服务后重新登录')
    expect(source).toContain('进入维护后，无论恢复成功或失败，都需重启应用服务后重新登录')
    expect(source).toContain('原 config/')
    expect(source).toContain('encryption.key')
    expect(source).toContain('accept=".zip"')
    expect(source).toContain("endsWith('.zip')")
    expect(source).not.toContain('.sql')
    expect(source).toContain('dangerouslyUseHTMLString: true')
    expect(source.indexOf('提示：恢复结束后服务保持暂停')).toBeLessThan(
      source.indexOf('重要提示：当前备份与恢复功能仍在完善'),
    )
  })
})

describe('恢复结果与重启服务交互', () => {
  enableAutoUnmount(afterEach)
  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(authAPI, 'fetchSession').mockResolvedValue({ authenticated: false })
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    for (const method of ['success', 'error', 'warning'] as const)
      vi.spyOn(ElMessage, method).mockImplementation(() => ({ close: vi.fn() }))
  })
  afterEach(() => {
    disposePinia(getActivePinia()!)
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  const setup = async (overrides = {}) => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', name: 'home', component: { template: '<div />' } },
        { path: '/login', name: 'login', component: { template: '<div>登录页面</div>' } },
      ],
    })
    await router.push('/')
    await router.isReady()
    const pinia = createPinia()
    const store = useBackupStore(pinia)
    const wrapper = mount(App, {
      global: {
        plugins: [pinia, router, ElementPlus],
        stubs: {
          ElDialog: {
            props: ['modelValue', 'title'],
            template:
              '<div v-if="modelValue" role="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
          },
        },
      },
    })
    const result = {
      type: 'restore',
      status: 'completed',
      is_running: false,
      restart_required: true,
      restart_supported: true,
      restart_requested: false,
      count: 2,
      total: 2,
      elapsed: 1,
      desc: '',
      error_msg: '',
      ...overrides,
    }
    const http = {
      get: vi.fn().mockResolvedValue({ data: { code: 200, data: result } }),
      post: vi.fn().mockResolvedValue({
        data: { code: 200, data: { ...result, restart_requested: true } },
      }),
    }
    store.startProgressPolling('restore', undefined, http as never, 'test-restore-receipt')
    await vi.advanceTimersByTimeAsync(3000)
    return { wrapper, router, store, http, auth: useAuthStore(pinia) }
  }

  const button = (wrapper: VueWrapper, label: string) =>
    wrapper.findAll('button').find((item) => item.text() === label)

  it.each(['completed', 'failed'])(
    '%s 后点击重启并等待上线，保留原结果，点击重新登录后关闭结果弹窗',
    async (status) => {
      const { wrapper, router, store, http, auth } = await setup({ status })
      const expectedTitle = status === 'failed' ? '数据库恢复失败' : '数据库恢复已完成'
      expect(wrapper.get('[role="dialog"]').text()).toContain(expectedTitle)
      await button(wrapper, '重启服务')!.trigger('click')
      await flushPromises()
      expect(button(wrapper, '正在重启服务')!.attributes('disabled')).toBeDefined()
      expect(wrapper.text()).toContain('正在等待服务重新上线')
      expect(wrapper.text()).toContain(expectedTitle)
      http.get.mockRejectedValue(
        new HttpResponseError({
          status: 401,
          data: { code: 401, error_code: 'RESTORE_RECEIPT_INVALID' },
        }),
      )
      await vi.advanceTimersByTimeAsync(3000)
      expect(wrapper.text()).toContain(expectedTitle)
      expect(wrapper.text()).toContain('服务已重新上线，请重新登录')
      expect(router.currentRoute.value.path).toBe('/')
      expect(store.progress?.status).toBe(status)
      const clearAuth = vi.spyOn(auth, 'clearAuth')
      await button(wrapper, '重新登录')!.trigger('click')
      await flushPromises()
      expect(router.currentRoute.value.path).toBe('/login')
      expect(clearAuth).toHaveBeenCalledOnce()
      expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
      expect(http.post).toHaveBeenCalledTimes(1)
    },
  )

  it('不支持的运行方式显示手动提示，预检失败仅允许关闭', async () => {
    const { wrapper, store } = await setup({ restart_supported: false })
    expect(button(wrapper, '重启服务')).toBeUndefined()
    expect(wrapper.text()).toContain('当前运行方式不支持页面重启')
    expect(button(wrapper, '检查服务状态')).toBeDefined()
    store.restartRequired = false
    store.progress = { running: false, status: 'failed', current_step: '恢复任务失败' }
    await flushPromises()
    expect(button(wrapper, '检查服务状态')).toBeUndefined()
    await button(wrapper, '关闭')!.trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('手动检查服务先保留恢复弹窗，重启且会话可用后才允许重新登录', async () => {
    const { wrapper, router, store, http } = await setup({ restart_supported: false })
    const reload = vi.spyOn(window.location, 'reload').mockImplementation(() => {})
    await button(wrapper, '检查服务状态')!.trigger('click')
    expect(wrapper.text()).toContain('正在检查服务状态')
    expect(button(wrapper, '正在重启服务')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(3000)
    expect(wrapper.text()).toContain('请先重启 QMS 服务后再检查')
    expect(router.currentRoute.value.path).toBe('/')
    expect(store.showProgressDialog).toBe(true)
    expect(reload).not.toHaveBeenCalled()
    expect(http.post).not.toHaveBeenCalled()

    http.get.mockRejectedValue(
      new HttpResponseError({
        status: 401,
        data: { code: 401, error_code: 'RESTORE_RECEIPT_INVALID' },
      }),
    )
    vi.mocked(authAPI.fetchSession).mockRejectedValueOnce(
      new HttpResponseError({
        status: 503,
        data: { code: 503, error_code: 'DATABASE_MAINTENANCE' },
      }),
    )
    await button(wrapper, '检查服务状态')!.trigger('click')
    await vi.advanceTimersByTimeAsync(3000)
    expect(button(wrapper, '重新登录')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(2000)
    expect(button(wrapper, '重新登录')).toBeDefined()
    await button(wrapper, '重新登录')!.trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/login')
    expect(http.post).not.toHaveBeenCalled()
    expect(reload).not.toHaveBeenCalled()
  })

  it('恢复已提交后的收尾失败明确说明数据已提交，重启后仍保留结论', async () => {
    const { wrapper, http } = await setup({
      status: 'failed',
      error_code: 'RESTORE_FAILED',
      error_msg: '恢复任务失败，请查看服务日志。',
      restore_outcome: 'committed',
    })
    expect(wrapper.get('[role="dialog"]').text()).toContain('数据库恢复已提交，后续处理未完成')
    expect(wrapper.text()).not.toContain('已确认回滚')
    await button(wrapper, '重启服务')!.trigger('click')
    await flushPromises()
    http.get.mockRejectedValue(
      new HttpResponseError({
        status: 401,
        data: { code: 401, error_code: 'RESTORE_RECEIPT_INVALID' },
      }),
    )
    await vi.advanceTimersByTimeAsync(3000)
    expect(wrapper.get('[role="dialog"]').text()).toContain('数据库恢复已提交，后续处理未完成')
    expect(button(wrapper, '重新登录')).toBeDefined()
  })
})
