import { browseSortOptions } from '../support/browseSort'
// @vitest-environment happy-dom
import axios from 'axios'
import {
  DOMWrapper,
  enableAutoUnmount,
  flushPromises,
  mount,
  type VueWrapper,
} from '@vue/test-utils'
import { ElDropdown, ElMessage, ElMessageBox, type MessageBoxData } from 'element-plus'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import AppFileManager from '@/components/AppFileManager.vue'
import ResponsivePagination from '@/components/common/ResponsivePagination.vue'
import { httpKey } from '@/http/client'
import { createDeferred } from '../support/deferred'

const envelope = (data: unknown, code = 200, message = '') => ({ code, data, message })
const file = { id: 'file', name: 'movie.mkv', size: 1024, modified_time: 1, is_directory: false }
const secondFile = {
  id: 'file-2',
  name: 'backup.mkv',
  size: 2048,
  modified_time: 2,
  is_directory: false,
}
const filesPage = { list: [file, secondFile], total: 2, page: 1, page_size: 50 }
const directory = { id: '/strm', name: '目标目录', path: '/strm' }

const button = (wrapper: VueWrapper, label: string) =>
  wrapper.findAll('button').find((item) => item.text().trim() === label)!
const click = async (wrapper: VueWrapper, label: string) => {
  await button(wrapper, label).trigger('click')
  await flushPromises()
}
const toggleBatchMode = async (wrapper: VueWrapper) => {
  await wrapper.get('.file-manager-batch-toggle input').setValue(true)
  await flushPromises()
}
const selectRow = async (wrapper: VueWrapper, index: number) => {
  await wrapper.findAll('.el-table__body-wrapper .el-checkbox__original')[index]!.setValue(true)
  await flushPromises()
}
const batchSummary = (wrapper: VueWrapper) => wrapper.get('.file-manager-batch-summary').text()
const renameBox = () =>
  new DOMWrapper(document.body).findAll('.el-message-box').find((box) => box.isVisible())!
const confirmRename = async (name: string) => {
  await renameBox().get('input').setValue(name)
  await renameBox().get('.el-message-box__btns .el-button--primary').trigger('click')
  await flushPromises()
}

const resize = async (width: number) => {
  Object.defineProperty(window, 'innerWidth', { value: width, configurable: true })
  window.dispatchEvent(new Event('resize'))
  await flushPromises()
}

const mountPage = async (
  sourceType = '115',
  parentId = '',
  realDialogs = false,
  initialFilesPage = filesPage,
) => {
  const readReply = vi.fn(
    async (url: string, params?: Record<string, unknown>): Promise<unknown> => {
      void params
      if (url.endsWith('/path/sort-options'))
        return envelope(
          browseSortOptions(
            String(params?.source_type),
            params?.scope === 'directories' ? 'directories' : 'files',
          ),
        )
      if (url.endsWith('/account/list')) return envelope([{ id: 1, source_type: sourceType }])
      if (url.endsWith('/path/files')) return envelope(initialFilesPage)
      if (url.endsWith('/path/list')) return envelope([directory])
      return envelope(null)
    },
  )
  const writeReply = vi.fn(async (): Promise<unknown> => envelope(null))
  const adapter = vi.fn(async (config) => ({
    config,
    status: 200,
    statusText: 'OK',
    headers: {},
    data: config.method === 'get' ? await readReply(config.url, config.params) : await writeReply(),
  }))
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }],
  })
  await router.push('/')
  await router.isReady()
  sessionStorage.setItem(
    'qmediasync-page-state',
    JSON.stringify({
      'file-manager': {
        currentPage: 1,
        pageSize: 50,
        filters: {
          selectedAccountId: 1,
          currentPath: parentId ? 'source' : '',
          pathItems: JSON.stringify(
            parentId ? [{ id: parentId, name: 'source', path: '/source', is_directory: true }] : [],
          ),
        },
        expandedRowKeys: [],
        scrollTop: 0,
      },
    }),
  )
  const wrapper = mount(AppFileManager, {
    attachTo: realDialogs ? document.body : undefined,
    global: {
      plugins: [createPinia(), router],
      provide: { [httpKey]: axios.create({ adapter }) },
      stubs: {
        PageHeader: true,
        ElDialog: realDialogs
          ? false
          : {
              props: ['modelValue', 'title'],
              template:
                '<section v-if="modelValue" role="dialog" :aria-label="title"><slot /><slot name="footer" /></section>',
            },
        ResponsivePagination: true,
      },
    },
  })
  await flushPromises()
  await flushPromises()
  return { wrapper, adapter, readReply, writeReply }
}

const writeCall = (adapter: ReturnType<typeof vi.fn>, urlSuffix: string) => {
  const call = adapter.mock.calls.find(
    ([config]) => config.method === 'post' && config.url?.endsWith(urlSuffix),
  )
  expect(call, `缺少 POST ${urlSuffix} 请求`).toBeTruthy()
  return JSON.parse(call![0].data as string)
}

const allWriteCalls = (adapter: ReturnType<typeof vi.fn>, urlSuffix: string) =>
  adapter.mock.calls
    .filter(([config]) => config.method === 'post' && config.url?.endsWith(urlSuffix))
    .map(([config]) => JSON.parse(config.data as string))

// element-plus 的 toggleAllSelection 有 10ms debounce，等待真实定时器触发。
const waitDebounce = async () => {
  await new Promise((resolve) => setTimeout(resolve, 20))
  await flushPromises()
}

const fileAction = async (wrapper: VueWrapper, command: string) => {
  wrapper.findComponent(ElDropdown).vm.$emit('command', command)
  await flushPromises()
}

enableAutoUnmount(afterEach)

describe('AppFileManager 批量操作', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'innerWidth', { value: 1200, configurable: true })
    sessionStorage.clear()
    localStorage.clear()
    vi.restoreAllMocks()
    vi.spyOn(ElMessage, 'error').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessage, 'success').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessage, 'warning').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessage, 'info').mockImplementation(() => ({ close: vi.fn() }))
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as MessageBoxData)
    vi.spyOn(ElMessageBox, 'close').mockImplementation(() => {})
    vi.spyOn(ElMessageBox, 'prompt')
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  afterEach(async () => {
    vi.mocked(ElMessageBox.close).mockRestore()
    ElMessageBox.close()
    await flushPromises()
    sessionStorage.clear()
    localStorage.clear()
  })

  it.each([1200, 600])('宽度 %s 下根目录不显示返回上级入口', async (width) => {
    await resize(width)
    const { wrapper } = await mountPage()
    expect(button(wrapper, '返回上级目录')).toBeUndefined()
  })

  it.each([
    [1200, '115', '100', '101'],
    [600, '115', '100', '101'],
    [1200, 'baidupan', '/source', '/source/child'],
    [600, 'baidupan', '/source', '/source/child'],
    [1200, 'openlist', '/source', '/source/child'],
    [600, 'openlist', '/source', '/source/child'],
  ] as const)(
    '宽度 %s 的 %s 从空子目录逐层返回，导航不计入全选和分页',
    async (width, source, parentId, childId) => {
      await resize(width)
      const child = { ...file, id: childId, name: 'child', is_directory: true }
      const parentPage = { ...filesPage, list: [child], total: 1 }
      const { wrapper, readReply } = await mountPage(source, parentId, false, parentPage)
      expect(button(wrapper, '返回上级目录').element.tagName).toBe('BUTTON')
      expect(wrapper.get('.el-table').text()).not.toContain('返回上级目录')
      expect(wrapper.findComponent(ResponsivePagination).props('total')).toBe(1)
      await toggleBatchMode(wrapper)
      await click(wrapper, '全选')
      await waitDebounce()
      expect(batchSummary(wrapper)).toBe('已选 1 项')

      readReply.mockResolvedValueOnce(envelope({ ...filesPage, list: [], total: 0 }))
      await wrapper.get('.el-table__row').trigger('dblclick')
      await flushPromises()
      expect(readReply).toHaveBeenLastCalledWith(
        expect.stringContaining('/path/files'),
        expect.objectContaining({ path: childId, page: 1 }),
      )
      expect(wrapper.text()).toContain('当前目录为空')
      expect(button(wrapper, '返回上级目录').exists()).toBe(true)
      expect(wrapper.findComponent(ResponsivePagination).props('total')).toBe(0)
      expect(batchSummary(wrapper)).toBe('已选 0 项')

      const pendingParent = createDeferred<unknown>()
      readReply.mockReturnValueOnce(pendingParent.promise)
      await click(wrapper, '返回上级目录')
      expect(readReply).toHaveBeenLastCalledWith(
        expect.stringContaining('/path/files'),
        expect.objectContaining({ path: parentId, page: 1 }),
      )
      const requestCount = readReply.mock.calls.length
      expect(button(wrapper, '返回上级目录').attributes('disabled')).toBeDefined()
      ;(button(wrapper, '返回上级目录').element as HTMLButtonElement).click()
      await flushPromises()
      expect(readReply).toHaveBeenCalledTimes(requestCount)
      pendingParent.resolve(envelope(parentPage))
      await flushPromises()
      expect(button(wrapper, '返回上级目录').attributes('disabled')).toBeUndefined()
      expect(wrapper.findComponent(ResponsivePagination).props('total')).toBe(1)

      await click(wrapper, '返回上级目录')
      expect(readReply).toHaveBeenLastCalledWith(
        expect.stringContaining('/path/files'),
        expect.objectContaining({ path: '', page: 1 }),
      )
      expect(button(wrapper, '返回上级目录')).toBeUndefined()
    },
  )

  it('进入批量模式后展示批量操作栏，全选后按选择数汇总', async () => {
    const { wrapper } = await mountPage()
    expect(wrapper.find('.file-manager-batch-bar').exists()).toBe(false)

    await toggleBatchMode(wrapper)
    expect(wrapper.find('.file-manager-batch-bar').exists()).toBe(true)
    expect(batchSummary(wrapper)).toBe('已选 0 项')
    expect(button(wrapper, '删除')!.attributes('disabled')).toBeDefined()

    await click(wrapper, '全选')
    await waitDebounce()
    expect(batchSummary(wrapper)).toBe('已选 2 项')
  })

  it.each([1200, 600])('宽度 %s 下切换批量模式复用图标与菜单且不请求列表', async (width) => {
    await resize(width)
    const { wrapper, adapter } = await mountPage()
    const icons = wrapper.findAll('.file-item-icon svg').map((icon) => icon.element)
    const menus = wrapper.findAllComponents(ElDropdown).map((menu) => menu.element)
    const checkboxes = wrapper
      .findAll('.el-table__body-wrapper .el-checkbox__original')
      .map((checkbox) => checkbox.element)
    const requestCount = adapter.mock.calls.length
    expect(icons).toHaveLength(2)
    expect(checkboxes).toHaveLength(2)
    expect(menus).toHaveLength(width === 1200 ? 2 : 0)
    expect(wrapper.get('.el-table').classes()).toContain('batch-selection-hidden')

    const expectStableCells = () => {
      wrapper.findAll('.file-item-icon svg').forEach((icon, index) => {
        expect(icon.element).toBe(icons[index])
      })
      wrapper.findAllComponents(ElDropdown).forEach((menu, index) => {
        expect(menu.element).toBe(menus[index])
      })
      wrapper
        .findAll('.el-table__body-wrapper .el-checkbox__original')
        .forEach((checkbox, index) => expect(checkbox.element).toBe(checkboxes[index]))
      expect(adapter.mock.calls).toHaveLength(requestCount)
    }

    await toggleBatchMode(wrapper)
    expect(wrapper.get('.el-table').classes()).not.toContain('batch-selection-hidden')
    expectStableCells()
    await selectRow(wrapper, 0)
    await click(wrapper, '退出批量')
    expect(wrapper.get('.el-table').classes()).toContain('batch-selection-hidden')
    expectStableCells()
    await toggleBatchMode(wrapper)
    expect(batchSummary(wrapper)).toBe('已选 0 项')
    expectStableCells()
  })

  it('复用的行内菜单在刷新后使用当前文件信息', async () => {
    const { wrapper, readReply } = await mountPage()
    readReply.mockResolvedValueOnce(
      envelope({ ...filesPage, list: [{ ...file, name: 'renamed.mkv' }, secondFile] }),
    )
    await click(wrapper, '刷新')
    expect(wrapper.find('[aria-label="操作 renamed.mkv"]').exists()).toBe(true)
    await fileAction(wrapper, 'DELETE')
    expect(ElMessageBox.confirm).toHaveBeenCalledWith(
      '确认删除“renamed.mkv”吗？',
      '确认删除',
      expect.any(Object),
    )
  })

  it('退出批量模式清空选择', async () => {
    const { wrapper } = await mountPage()
    await toggleBatchMode(wrapper)
    await click(wrapper, '全选')
    await waitDebounce()
    expect(batchSummary(wrapper)).toBe('已选 2 项')

    await click(wrapper, '退出批量')
    expect(wrapper.find('.file-manager-batch-bar').exists()).toBe(false)

    await toggleBatchMode(wrapper)
    expect(batchSummary(wrapper)).toBe('已选 0 项')
  })

  it.each([1200, 600])('宽度 %s 下退出批量后忽略尚未执行的全选', async (width) => {
    await resize(width)
    const { wrapper } = await mountPage()
    await toggleBatchMode(wrapper)
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] })
    try {
      await click(wrapper, '全选')
      expect(batchSummary(wrapper)).toBe('已选 0 项')
      await click(wrapper, '退出批量')
      await vi.advanceTimersByTimeAsync(20)
      await flushPromises()
      expect(
        wrapper
          .findAll('.el-table__body-wrapper .el-checkbox__original')
          .some((checkbox) => (checkbox.element as HTMLInputElement).checked),
      ).toBe(false)
      await toggleBatchMode(wrapper)
      expect(batchSummary(wrapper)).toBe('已选 0 项')
    } finally {
      vi.useRealTimers()
    }
  })

  it.each([
    [1200, 600, '移动'],
    [600, 1200, '移动'],
    [1200, 600, '复制'],
    [600, 1200, '复制'],
    [1200, 600, 'STRM 生成'],
    [600, 1200, 'STRM 生成'],
  ] as const)(
    '从 %s 切至 %s 清空真实表格选择并关闭待提交的批量%s弹窗',
    async (from, to, action) => {
      await resize(from)
      const { wrapper, adapter } = await mountPage()
      await toggleBatchMode(wrapper)
      await selectRow(wrapper, 0)
      await click(wrapper, action)
      expect(wrapper.find('[role="dialog"]').exists()).toBe(true)

      await resize(to)

      expect(batchSummary(wrapper)).toBe('已选 0 项')
      expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
      expect(
        wrapper
          .findAll('.el-table__body-wrapper .el-checkbox__original')
          .some((checkbox) => (checkbox.element as HTMLInputElement).checked),
      ).toBe(false)
      for (const label of ['移动', '复制', 'STRM 生成', '删除']) {
        expect(button(wrapper, label).attributes('disabled')).toBeDefined()
      }
      expect(adapter.mock.calls.some(([config]) => config.method === 'post')).toBe(false)

      await selectRow(wrapper, 1)
      await click(wrapper, '删除')
      expect(writeCall(adapter, '/path/delete-batch').file_ids).toEqual(['file-2'])
    },
  )

  it('布局切换关闭待确认的批量删除，晚到确认不能删除隐藏选择', async () => {
    const confirmation = createDeferred<MessageBoxData>()
    vi.mocked(ElMessageBox.confirm).mockReturnValueOnce(confirmation.promise)
    const { wrapper, adapter } = await mountPage()
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, '删除')

    await resize(600)
    expect(ElMessageBox.close).toHaveBeenCalledOnce()
    await resize(1200)
    expect(ElMessageBox.close).toHaveBeenCalledOnce()
    confirmation.resolve('confirm' as MessageBoxData)
    await flushPromises()

    expect(batchSummary(wrapper)).toBe('已选 0 项')
    expect(allWriteCalls(adapter, '/path/delete-batch')).toEqual([])
    expect(ElMessage.success).not.toHaveBeenCalled()
  })

  it('布局切换不丢弃已经提交的批量删除结果', async () => {
    const result = createDeferred<unknown>()
    const { wrapper, adapter, writeReply, readReply } = await mountPage()
    writeReply.mockReturnValueOnce(result.promise)
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, '删除')
    expect(allWriteCalls(adapter, '/path/delete-batch')).toHaveLength(1)

    await resize(600)
    expect(button(wrapper, '全选').attributes('disabled')).toBeDefined()
    result.resolve(envelope(null))
    await flushPromises()

    expect(ElMessage.success).toHaveBeenCalledWith('已删除 1 项')
    expect(readReply.mock.calls.filter(([url]) => url.endsWith('/path/files'))).toHaveLength(2)
    expect(button(wrapper, '全选').attributes('disabled')).toBeUndefined()
  })

  it('批量删除提交选中项并在成功后清空选择刷新列表', async () => {
    const { wrapper, adapter, readReply } = await mountPage()
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    expect(batchSummary(wrapper)).toBe('已选 1 项')

    await click(wrapper, '删除')
    expect(writeCall(adapter, '/path/delete-batch')).toEqual({
      parent_id: '',
      file_ids: ['file'],
      account_id: 1,
    })
    expect(ElMessage.success).toHaveBeenCalledWith('已删除 1 项')
    expect(batchSummary(wrapper)).toBe('已选 0 项')
    const refreshCall = readReply.mock.calls.filter(([url]) => url.endsWith('/path/files'))
    expect(refreshCall.length).toBeGreaterThan(1)
  })

  it('批量删除部分失败时保留错误并重新读取列表，清理已失效选择', async () => {
    const { wrapper, writeReply, readReply, adapter } = await mountPage()
    writeReply.mockResolvedValueOnce(envelope(null, 500, '文件正在使用'))
    readReply.mockResolvedValueOnce(envelope({ ...filesPage, list: [secondFile], total: 1 }))
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await selectRow(wrapper, 1)

    await click(wrapper, '删除')
    expect(ElMessage.error).toHaveBeenCalledWith('文件正在使用')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(batchSummary(wrapper)).toBe('已选 0 项')
    expect(wrapper.text()).not.toContain(file.name)
    expect(wrapper.text()).toContain(secondFile.name)
    expect(readReply.mock.calls.filter(([url]) => url.endsWith('/path/files'))).toHaveLength(2)
    expect(readReply).toHaveBeenLastCalledWith(
      expect.stringContaining('/path/files'),
      expect.objectContaining({ refresh: 1 }),
    )
    expect(allWriteCalls(adapter, '/path/delete-batch')).toHaveLength(1)
  })

  it('取消删除确认不会清理选择或重新加载列表', async () => {
    const { wrapper, readReply, adapter } = await mountPage()
    vi.mocked(ElMessageBox.confirm).mockRejectedValueOnce('cancel')
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, '删除')
    expect(batchSummary(wrapper)).toBe('已选 1 项')
    expect(allWriteCalls(adapter, '/path/delete-batch')).toHaveLength(0)
    expect(readReply.mock.calls.filter(([url]) => url.endsWith('/path/files'))).toHaveLength(1)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('删除在途时切换上下文，旧错误不能刷新或清除新选择', async () => {
    const result = createDeferred<unknown>()
    const { wrapper, writeReply, readReply } = await mountPage()
    writeReply.mockReturnValueOnce(result.promise)
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, '删除')
    await wrapper.get('.account-item').trigger('click')
    await flushPromises()
    await selectRow(wrapper, 1)
    const readCount = readReply.mock.calls.length
    result.resolve(envelope(null, 500, '旧请求失败'))
    await flushPromises()
    expect(batchSummary(wrapper)).toBe('已选 1 项')
    expect(readReply.mock.calls).toHaveLength(readCount)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('删除报错后的刷新也失败时保留两类错误且不重试写入', async () => {
    const { wrapper, writeReply, readReply, adapter } = await mountPage()
    writeReply.mockResolvedValueOnce(envelope(null, 500, '删除中断'))
    readReply.mockResolvedValueOnce(envelope(null, 500, '列表暂不可读'))
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, '删除')
    expect(ElMessage.error).toHaveBeenCalledWith('删除中断')
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('列表暂不可读'))
    expect(batchSummary(wrapper)).toBe('已选 0 项')
    expect(allWriteCalls(adapter, '/path/delete-batch')).toHaveLength(1)
  })

  it('批量移动选择目标目录后提交移动请求', async () => {
    const { wrapper, adapter } = await mountPage()
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 1)

    await click(wrapper, '移动')
    const dialog = wrapper.find('[role="dialog"][aria-label="批量移动"]')
    expect(dialog.exists()).toBe(true)

    await dialog.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')

    expect(writeCall(adapter, '/path/move')).toEqual({
      parent_id: '',
      file_ids: ['file-2'],
      target_parent_id: '/strm',
      account_id: 1,
    })
    expect(ElMessage.success).toHaveBeenCalledWith('移动成功')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it.each([
    ['移动', false],
    ['移动', true],
    ['复制', false],
    ['复制', true],
  ] as const)('%s提交期间不能关闭弹窗，失败=%s 时仍处理结果', async (action, failure) => {
    const result = createDeferred<unknown>()
    const { wrapper, writeReply, readReply, adapter } = await mountPage('115', '123', true)
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, action)
    const dialog = wrapper.get('.el-dialog')
    writeReply.mockReturnValueOnce(result.promise)
    await click(wrapper, `${action}到根目录`)

    await dialog.get('.el-dialog__headerbtn').trigger('click')
    await flushPromises()
    expect(dialog.isVisible()).toBe(true)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(dialog.isVisible()).toBe(true)
    await click(wrapper, '取消')
    expect(dialog.isVisible()).toBe(true)

    result.resolve(
      failure ? envelope(null, 500, '操作中断') : envelope({ status: 'completed', task_ids: [] }),
    )
    await flushPromises()
    expect(dialog.isVisible()).toBe(false)
    expect(batchSummary(wrapper)).toBe('已选 0 项')
    expect(readReply.mock.calls.filter(([url]) => url.endsWith('/path/files'))).toHaveLength(2)
    expect(readReply).toHaveBeenLastCalledWith(
      expect.stringContaining('/path/files'),
      expect.objectContaining({ refresh: 1 }),
    )
    expect(allWriteCalls(adapter, action === '移动' ? '/path/move' : '/path/copy')).toHaveLength(1)
    if (failure) {
      expect(ElMessage.error).toHaveBeenCalledWith('操作中断')
      expect(ElMessage.success).not.toHaveBeenCalled()
    } else {
      expect(ElMessage.success).toHaveBeenCalledWith(`${action}成功`)
    }
  })

  it('移动提交前仍可通过关闭按钮或取消退出弹窗', async () => {
    const { wrapper, adapter, readReply } = await mountPage('115', '123', true)
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, '移动')
    await wrapper.get('.el-dialog__headerbtn').trigger('click')
    await flushPromises()
    expect(wrapper.get('.el-dialog').isVisible()).toBe(false)
    await click(wrapper, '移动')
    await click(wrapper, '取消')
    expect(wrapper.get('.el-dialog').isVisible()).toBe(false)
    expect(allWriteCalls(adapter, '/path/move')).toHaveLength(0)
    expect(readReply.mock.calls.filter(([url]) => url.endsWith('/path/files'))).toHaveLength(1)
    expect(batchSummary(wrapper)).toBe('已选 1 项')
  })

  it('批量移动成功后不发空 source_type 目录请求且批量按钮恢复可用', async () => {
    const { wrapper, readReply } = await mountPage()
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)

    await click(wrapper, '移动')
    const dialog = wrapper.find('[role="dialog"][aria-label="批量移动"]')
    await dialog.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')
    expect(ElMessage.success).toHaveBeenCalledWith('移动成功')

    // 弹窗关闭后目录选择器的来源信息保持快照不变，
    // 不能触发以空 source_type 重新加载目录的请求（会误报“未知的同步源类型”）。
    const directoryCalls = readReply.mock.calls.filter(([url]) => url.endsWith('/path/list'))
    expect(directoryCalls.length).toBeGreaterThan(0)
    for (const [, params] of directoryCalls) {
      expect(String(params?.source_type ?? '')).not.toBe('')
    }

    // 成功后批量操作 loading 必须复位，重新勾选即可继续使用批量按钮。
    await selectRow(wrapper, 1)
    expect(batchSummary(wrapper)).toBe('已选 1 项')
    expect(button(wrapper, '删除')!.attributes('disabled')).toBeUndefined()
    expect(button(wrapper, '移动')!.attributes('disabled')).toBeUndefined()
  })

  it('批量复制选择目标目录后提交复制请求', async () => {
    const { wrapper, adapter } = await mountPage()
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)

    await click(wrapper, '复制')
    const dialog = wrapper.find('[role="dialog"][aria-label="批量复制"]')
    expect(dialog.exists()).toBe(true)

    await dialog.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')

    expect(writeCall(adapter, '/path/copy')).toEqual({
      parent_id: '',
      file_ids: ['file'],
      target_parent_id: '/strm',
      account_id: 1,
    })
    expect(ElMessage.success).toHaveBeenCalledWith('复制成功')
  })

  it.each([
    ['115', '123', '0'],
    ['baidupan', '/source', '/'],
    ['openlist', '/source', '/'],
  ])('%s 的单个移动和批量复制可以从子目录提交到根目录', async (source, parentId, rootId) => {
    const { wrapper, adapter } = await mountPage(source, parentId)
    await fileAction(wrapper, 'MOVE')
    await click(wrapper, '移动到根目录')
    expect(writeCall(adapter, '/path/move')).toEqual({
      account_id: 1,
      parent_id: parentId,
      file_ids: ['file'],
      target_parent_id: rootId,
    })

    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 1)
    await click(wrapper, '复制')
    await click(wrapper, '复制到根目录')
    expect(writeCall(adapter, '/path/copy')).toEqual({
      account_id: 1,
      parent_id: parentId,
      file_ids: ['file-2'],
      target_parent_id: rootId,
    })
  })

  it.each(['移动', '复制'])(
    'OpenList %s失败后清理旧选择，重新选择后可提交异步任务',
    async (action) => {
      const { wrapper, writeReply, readReply, adapter } = await mountPage('openlist', '/source')
      await toggleBatchMode(wrapper)
      await selectRow(wrapper, 0)
      await click(wrapper, action)
      writeReply.mockResolvedValueOnce(envelope(null, 400, 'transfer rejected'))
      await click(wrapper, `${action}到根目录`)
      expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
      expect(batchSummary(wrapper)).toBe('已选 0 项')
      expect(ElMessage.error).toHaveBeenCalledWith('transfer rejected')
      expect(readReply).toHaveBeenLastCalledWith(
        expect.stringContaining('/path/files'),
        expect.objectContaining({ refresh: 1 }),
      )
      expect(allWriteCalls(adapter, action === '移动' ? '/path/move' : '/path/copy')).toHaveLength(
        1,
      )

      await selectRow(wrapper, 0)
      await click(wrapper, action)
      writeReply.mockResolvedValueOnce(envelope({ status: 'submitted', task_ids: ['task-1'] }))
      await click(wrapper, `${action}到根目录`)
      expect(ElMessage.info).toHaveBeenCalledWith(
        '已提交到 OpenList，请查看任务结果，完成后点击刷新更新文件列表',
      )
      expect(ElMessage.success).not.toHaveBeenCalled()
      expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
      expect(batchSummary(wrapper)).toBe('已选 0 项')
    },
  )

  it('批量 STRM 生成逐项提交选中项并显示批量弹窗', async () => {
    const { wrapper, adapter } = await mountPage()
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await selectRow(wrapper, 1)

    await click(wrapper, 'STRM 生成')
    const dialog = wrapper.find('[role="dialog"][aria-label="批量 STRM 生成"]')
    expect(dialog.exists()).toBe(true)
    expect(dialog.text()).toContain('已选')
    expect(dialog.text()).toContain('2 项')
    // 批量模式不展示单项路径预览
    expect(dialog.text()).not.toContain('/strm/movie.mkv')

    await dialog.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')

    const requests = allWriteCalls(adapter, '/sync/manual')
    expect(requests).toEqual([
      { path_id: 'file', target_path: '/strm', account_id: 1 },
      { path_id: 'file-2', target_path: '/strm', account_id: 1 },
    ])
    expect(ElMessage.success).toHaveBeenCalledWith('已提交 2 项 STRM 生成任务')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('批量 STRM 生成部分失败时汇总成功与失败数量', async () => {
    const { wrapper, writeReply, adapter } = await mountPage()
    writeReply.mockResolvedValueOnce(envelope(null, 500, '任务队列已满'))
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await selectRow(wrapper, 1)

    await click(wrapper, 'STRM 生成')
    const dialog = wrapper.find('[role="dialog"][aria-label="批量 STRM 生成"]')
    await dialog.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')

    expect(allWriteCalls(adapter, '/sync/manual')).toHaveLength(2)
    expect(ElMessage.warning).toHaveBeenCalledWith('STRM 生成任务提交：成功 1 项，失败 1 项')
    expect(ElMessage.success).not.toHaveBeenCalled()
    // 部分成功时已入队任务无法撤回，弹窗关闭并结束本次提交
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('批量 STRM 生成全部失败时保留弹窗与目标目录', async () => {
    const { wrapper, writeReply } = await mountPage()
    writeReply.mockResolvedValue(envelope(null, 500, '任务队列已满'))
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await selectRow(wrapper, 1)

    await click(wrapper, 'STRM 生成')
    const dialog = wrapper.find('[role="dialog"][aria-label="批量 STRM 生成"]')
    await dialog.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')

    expect(ElMessage.error).toHaveBeenCalledWith('任务队列已满')
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect(ElMessage.warning).not.toHaveBeenCalled()
    // 与单个提交一致：全部失败保留弹窗和已选目标，便于修正后重试
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    expect(dialog.text()).toContain('2 项')
  })

  it('通过行内菜单移动单个文件', async () => {
    const { wrapper, adapter } = await mountPage()
    await fileAction(wrapper, 'MOVE')

    const dialog = wrapper.find('[role="dialog"][aria-label="移动到"]')
    expect(dialog.exists()).toBe(true)
    expect(dialog.text()).toContain('movie.mkv')

    await dialog.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')

    expect(writeCall(adapter, '/path/move')).toEqual({
      parent_id: '',
      file_ids: ['file'],
      target_parent_id: '/strm',
      account_id: 1,
    })
    expect(ElMessage.success).toHaveBeenCalledWith('移动成功')
  })

  it('通过行内菜单复制单个文件', async () => {
    const { wrapper, adapter } = await mountPage()
    await fileAction(wrapper, 'COPY')

    const dialog = wrapper.find('[role="dialog"][aria-label="复制到"]')
    expect(dialog.exists()).toBe(true)

    await dialog.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await click(wrapper, '选择')

    expect(writeCall(adapter, '/path/copy')).toEqual({
      parent_id: '',
      file_ids: ['file'],
      target_parent_id: '/strm',
      account_id: 1,
    })
    expect(ElMessage.success).toHaveBeenCalledWith('复制成功')
  })

  it('通过行内菜单重命名并提交新名称', async () => {
    const { wrapper, adapter } = await mountPage()
    await fileAction(wrapper, 'RENAME')

    expect(ElMessageBox.prompt).toHaveBeenCalled()
    await confirmRename('renamed.mkv')
    expect(writeCall(adapter, '/path/rename')).toEqual({
      parent_id: '',
      file_id: 'file',
      new_name: 'renamed.mkv',
      account_id: 1,
    })
    expect(ElMessage.success).toHaveBeenCalledWith('重命名成功')
  })

  it.each(['名称冲突', '网络失败'])('重命名%s后保留输入，修正后可提交', async (failure) => {
    const { wrapper, writeReply, adapter } = await mountPage()
    if (failure === '名称冲突') {
      writeReply.mockResolvedValueOnce(envelope(null, 500, '名称已存在'))
    } else {
      writeReply.mockRejectedValueOnce(new Error('Network Error'))
    }
    await fileAction(wrapper, 'RENAME')
    await confirmRename('renamed.mkv')
    expect(ElMessage.error).toHaveBeenCalledOnce()
    expect(ElMessage.success).not.toHaveBeenCalled()
    expect((renameBox().get('input').element as HTMLInputElement).value).toBe('renamed.mkv')
    expect(allWriteCalls(adapter, '/path/rename')).toHaveLength(1)

    await confirmRename('fixed.mkv')
    expect(allWriteCalls(adapter, '/path/rename').map((body) => body.new_name)).toEqual([
      'renamed.mkv',
      'fixed.mkv',
    ])
    expect(ElMessage.success).toHaveBeenCalledWith('重命名成功')
    expect(renameBox()).toBeUndefined()
  })

  it.each([1200, 600])('宽度 %s 下单项复制保留其他批量选择', async (width) => {
    await resize(width)
    const { wrapper } = await mountPage('115', '123')
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 1)
    if (width === 1200) {
      await fileAction(wrapper, 'COPY')
    } else {
      await wrapper.get('.el-table__expand-icon').trigger('click')
      await flushPromises()
      await wrapper
        .get('.file-manager-row-actions')
        .findAll('button')
        .find((b) => b.text() === '复制')!
        .trigger('click')
      await flushPromises()
    }
    await click(wrapper, '复制到根目录')
    expect(batchSummary(wrapper)).toBe('已选 1 项')
    expect(
      (
        wrapper.findAll('.el-table__body-wrapper .el-checkbox__original')[1]!
          .element as HTMLInputElement
      ).checked,
    ).toBe(true)
    await click(wrapper, '刷新')
    expect(batchSummary(wrapper)).toBe('已选 0 项')
  })

  it('移动后仅移除已消失的选中项', async () => {
    const { wrapper, readReply, adapter } = await mountPage('115', '123')
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await selectRow(wrapper, 1)
    await fileAction(wrapper, 'MOVE')
    readReply.mockResolvedValueOnce(envelope({ ...filesPage, list: [secondFile], total: 1 }))
    await click(wrapper, '移动到根目录')
    expect(wrapper.text()).not.toContain(file.name)
    expect(batchSummary(wrapper)).toBe('已选 1 项')
    await click(wrapper, '删除')
    expect(writeCall(adapter, '/path/delete-batch').file_ids).toEqual(['file-2'])
  })

  it('部分移动失败后刷新源列表，清理已失效选择且不重发', async () => {
    const { wrapper, writeReply, readReply, adapter } = await mountPage('115', '123')
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await selectRow(wrapper, 1)
    await click(wrapper, '移动')
    writeReply.mockResolvedValueOnce(envelope(null, 500, '第二项移动失败'))
    readReply.mockResolvedValueOnce(envelope({ ...filesPage, list: [secondFile], total: 1 }))
    await click(wrapper, '移动到根目录')
    expect(ElMessage.error).toHaveBeenCalledWith('第二项移动失败')
    expect(wrapper.text()).not.toContain(file.name)
    expect(batchSummary(wrapper)).toBe('已选 0 项')
    expect(allWriteCalls(adapter, '/path/move')).toHaveLength(1)
  })

  it('移动失败后的刷新再次失败时保留两类错误', async () => {
    const { wrapper, writeReply, readReply } = await mountPage()
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, '移动')
    writeReply.mockResolvedValueOnce(envelope(null, 500, '移动中断'))
    readReply.mockResolvedValueOnce(envelope(null, 500, '列表暂不可读'))
    await click(wrapper, '移动到根目录')
    expect(ElMessage.error).toHaveBeenCalledWith('移动中断')
    expect(ElMessage.error).toHaveBeenCalledWith(expect.stringContaining('列表暂不可读'))
    expect(batchSummary(wrapper)).toBe('已选 0 项')
  })

  it('移动在途时切换账号，旧失败不得刷新或清理新选择', async () => {
    const result = createDeferred<unknown>()
    const { wrapper, writeReply, readReply } = await mountPage()
    writeReply.mockReturnValueOnce(result.promise)
    await toggleBatchMode(wrapper)
    await selectRow(wrapper, 0)
    await click(wrapper, '移动')
    await click(wrapper, '移动到根目录')
    await wrapper.get('.account-item').trigger('click')
    await flushPromises()
    await selectRow(wrapper, 1)
    const readCount = readReply.mock.calls.length
    result.resolve(envelope(null, 500, '旧移动失败'))
    await flushPromises()
    expect(batchSummary(wrapper)).toBe('已选 1 项')
    expect(readReply.mock.calls).toHaveLength(readCount)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })

  it('重命名提交期间拒绝重复确认和关闭，完成后仍提示成功', async () => {
    const result = createDeferred<unknown>()
    const { wrapper, writeReply, adapter } = await mountPage()
    writeReply.mockReturnValueOnce(result.promise)
    await fileAction(wrapper, 'RENAME')
    await confirmRename('renamed.mkv')
    await renameBox().get('input').trigger('keydown', { key: 'Enter' })
    await renameBox().get('.el-message-box__headerbtn').trigger('click')
    await flushPromises()
    expect(renameBox()).toBeDefined()
    expect(allWriteCalls(adapter, '/path/rename')).toHaveLength(1)
    result.resolve(envelope(null))
    await flushPromises()
    expect(ElMessage.success).toHaveBeenCalledWith('重命名成功')
    expect(renameBox()).toBeUndefined()
  })

  it('取消重命名不提交或刷新', async () => {
    const { wrapper, adapter, readReply } = await mountPage()
    await fileAction(wrapper, 'RENAME')
    await renameBox().get('.el-message-box__headerbtn').trigger('click')
    await flushPromises()
    expect(allWriteCalls(adapter, '/path/rename')).toEqual([])
    expect(readReply.mock.calls.filter(([url]) => url.endsWith('/path/files'))).toHaveLength(1)
    expect(ElMessage.error).not.toHaveBeenCalled()
  })
})
