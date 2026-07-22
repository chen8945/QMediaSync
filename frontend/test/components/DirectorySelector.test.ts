// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import DirectorySelector from '@/components/DirectorySelector.vue'
import { httpKey } from '@/http/client'

const directorySelectorSource = readFileSync(
  resolve(__dirname, '../../src/components/DirectorySelector.vue'),
  'utf-8',
)

const emptyDirectory = {
  id: 'empty',
  name: '空目录',
  path: '/empty',
}

const createDeferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

const mountSelector = (
  get = vi.fn(),
  additionalProps: Record<string, unknown> = {},
  post = vi.fn(),
) =>
  mount(DirectorySelector, {
    props: {
      sourceType: 'local',
      accountId: 0,
      ...additionalProps,
    },
    global: {
      provide: {
        [httpKey]: {
          get,
          post,
        },
      },
    },
  })

describe('DirectorySelector', () => {
  it('有根目录 ID 时从该目录加载初始和刷新列表', async () => {
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: [] } })
    const wrapper = mountSelector(get, {
      rootId: 'source-root',
      rootPath: '/source',
    })

    await flushPromises()

    expect(get).toHaveBeenLastCalledWith(
      expect.any(String),
      expect.objectContaining({
        params: expect.objectContaining({
          parent_id: 'source-root',
          parent_path: '/source',
        }),
      }),
    )

    await wrapper.get('[aria-label="刷新目录"]').trigger('click')
    await flushPromises()

    expect(get).toHaveBeenLastCalledWith(
      expect.any(String),
      expect.objectContaining({
        params: expect.objectContaining({
          parent_id: 'source-root',
          parent_path: '/source',
        }),
      }),
    )
  })

  it('目录上下文变化后忽略较晚返回的旧根目录请求', async () => {
    const oldRootRequest = createDeferred<{
      data: { code: number; data: (typeof emptyDirectory)[] }
    }>()
    const restrictedRootDirectory = {
      id: 'restricted',
      name: '受限目录',
      path: '/source/restricted',
    }
    const restrictedRootRequest = createDeferred<{
      data: { code: number; data: (typeof restrictedRootDirectory)[] }
    }>()
    const get = vi.fn((_url: string, { params }: { params: { parent_id: string } }) =>
      params.parent_id === 'source-root' ? restrictedRootRequest.promise : oldRootRequest.promise,
    )
    const wrapper = mountSelector(get)

    await flushPromises()
    await wrapper.setProps({ rootId: 'source-root', rootPath: '/source' })
    await flushPromises()

    restrictedRootRequest.resolve({ data: { code: 200, data: [restrictedRootDirectory] } })
    await flushPromises()
    oldRootRequest.resolve({ data: { code: 200, data: [emptyDirectory] } })
    await flushPromises()

    expect(wrapper.text()).toContain('受限目录')
    expect(wrapper.text()).not.toContain('空目录')
  })

  it('让可点击面包屑继承与普通面包屑一致的文字排版', () => {
    expect(directorySelectorSource).toMatch(
      /\.breadcrumb-button\s*{[\s\S]*font:\s*inherit;[\s\S]*line-height:\s*inherit;[\s\S]*vertical-align:\s*baseline;/,
    )
  })

  it('允许面包屑换行以保持当前目录可见', () => {
    expect(directorySelectorSource).toContain(`.directory-breadcrumb {
  flex: 1 1 auto;
  min-width: 0;
  white-space: normal;
  overflow-wrap: anywhere;
}`)
    expect(directorySelectorSource).toContain(`.directory-breadcrumb :deep(.el-breadcrumb__item) {
  max-width: 100%;
}`)
    expect(directorySelectorSource).toMatch(
      /\.breadcrumb-button\s*{[\s\S]*max-width:\s*100%;[\s\S]*white-space:\s*normal;[\s\S]*overflow-wrap:\s*anywhere;/,
    )
  })

  it('空目录首次加载后不再重复请求子目录', async () => {
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: { code: 200, data: [emptyDirectory] } })
      .mockResolvedValue({ data: { code: 200, data: [] } })
    const wrapper = mountSelector(get)

    await flushPromises()

    const treeItem = wrapper.get('[role="treeitem"]')
    await treeItem.trigger('click')
    await flushPromises()

    expect(treeItem.attributes('aria-expanded')).toBeUndefined()

    await treeItem.trigger('click')
    await treeItem.trigger('click')
    await flushPromises()

    expect(get).toHaveBeenCalledTimes(2)
  })

  it('展示刷新入口，并在未选择父目录时禁用新建文件夹', async () => {
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: [] } })
    const wrapper = mountSelector(get)

    await flushPromises()

    expect(wrapper.find('[aria-label="刷新目录"]').exists()).toBe(true)
    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('新建文件夹'))
    expect(createButton?.attributes('disabled')).toBeDefined()
  })

  it('空的受限根目录允许新建第一个文件夹', async () => {
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: [] } })
    const wrapper = mountSelector(get, {
      rootId: 'source-root',
      rootPath: '/source',
    })

    await flushPromises()

    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('新建文件夹'))
    expect(createButton?.attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[role="tree"]').find('[role="treeitem"]').exists()).toBe(false)
  })

  it('仅通过面包屑反馈选择位置，并保持简短的新建文案', async () => {
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: [emptyDirectory] } })
    const wrapper = mountSelector(get)

    await flushPromises()
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="selected-directory-path"]').exists()).toBe(false)
    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('新建文件夹'))
    expect(createButton?.text()).toBe('新建文件夹')
  })

  it('将加载反馈固定在目录滚动区域之外', async () => {
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: [] } })
    const wrapper = mountSelector(get)

    await flushPromises()

    const loadingContainer = wrapper.find('[data-testid="directory-loading-container"]')
    expect(loadingContainer.exists()).toBe(true)
    expect(loadingContainer.find('[role="tree"]').exists()).toBe(true)
  })

  it('为新建文件夹弹窗保留移动端边距和完整标签', () => {
    expect(directorySelectorSource).toContain('width="min(400px, calc(100vw - 32px))"')
    expect(directorySelectorSource).toContain('label-position="top"')
  })

  it('刷新后保留仍存在的选择，并在子目录消失时回退到父目录', async () => {
    const parent = { id: 'parent', name: '父目录', path: '/parent' }
    const child = { id: 'child', name: '子目录', path: '/parent/child' }
    let refreshed = false
    const get = vi.fn((_url: string, { params }: { params: { parent_id: string } }) => {
      if (params.parent_id === '') return Promise.resolve({ data: { code: 200, data: [parent] } })
      if (params.parent_id === 'parent') {
        return Promise.resolve({ data: { code: 200, data: refreshed ? [] : [child] } })
      }
      return Promise.resolve({ data: { code: 200, data: [] } })
    })
    const wrapper = mountSelector(get)

    await flushPromises()
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await wrapper.findAll('[role="treeitem"]')[1].trigger('click')
    await flushPromises()

    expect(wrapper.get('.directory-breadcrumb').text()).toContain('子目录')

    await wrapper.get('[aria-label="刷新目录"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('.directory-breadcrumb').text()).toContain('子目录')

    refreshed = true
    await wrapper.get('[aria-label="刷新目录"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('.directory-breadcrumb').text()).toContain('父目录')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toMatchObject(parent)
  })

  it('顶层选中目录在刷新后缺失时清空临时选择', async () => {
    const rootDirectory = { id: 'root-directory', name: '根目录下的目录', path: '/root-directory' }
    let removed = false
    const get = vi.fn(() =>
      Promise.resolve({ data: { code: 200, data: removed ? [] : [rootDirectory] } }),
    )
    const wrapper = mountSelector(get)

    await flushPromises()
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()

    removed = true
    await wrapper.get('[aria-label="刷新目录"]').trigger('click')
    await flushPromises()

    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([null])
  })

  it('刷新已选中目录时重载其直接子目录', async () => {
    const parent = { id: 'parent', name: '父目录', path: '/parent' }
    const initialChild = { id: 'initial-child', name: '旧子目录', path: '/parent/old' }
    const refreshedChild = { id: 'refreshed-child', name: '新子目录', path: '/parent/new' }
    let refreshed = false
    const get = vi.fn((_url: string, { params }: { params: { parent_id: string } }) => {
      if (params.parent_id === '') return Promise.resolve({ data: { code: 200, data: [parent] } })
      return Promise.resolve({
        data: { code: 200, data: refreshed ? [refreshedChild] : [initialChild] },
      })
    })
    const wrapper = mountSelector(get)

    await flushPromises()
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()

    refreshed = true
    await wrapper.get('[aria-label="刷新目录"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('新子目录')
    expect(wrapper.text()).not.toContain('旧子目录')
  })

  it('点击面包屑目录时只切换临时选择并展开该目录', async () => {
    const parent = { id: 'parent', name: '父目录', path: '/parent' }
    const child = { id: 'child', name: '子目录', path: '/parent/child' }
    const get = vi.fn((_url: string, { params }: { params: { parent_id: string } }) => {
      if (params.parent_id === '') return Promise.resolve({ data: { code: 200, data: [parent] } })
      return Promise.resolve({ data: { code: 200, data: [child] } })
    })
    const wrapper = mountSelector(get)

    await flushPromises()
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await wrapper.findAll('[role="treeitem"]')[1].trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="directory-breadcrumb-parent"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toMatchObject(parent)
    expect(wrapper.emitted('select')).toBeUndefined()
  })

  it('目录加载失败时提供行内重试', async () => {
    const get = vi
      .fn()
      .mockResolvedValueOnce({ data: { code: 200, data: [emptyDirectory] } })
      .mockResolvedValueOnce({ data: { code: 500, message: '上游异常', data: [] } })
      .mockResolvedValueOnce({ data: { code: 200, data: [] } })
    const wrapper = mountSelector(get)

    await flushPromises()
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[aria-label="重试加载目录"]').exists()).toBe(true)

    await wrapper.get('[aria-label="重试加载目录"]').trigger('click')
    await flushPromises()

    expect(get).toHaveBeenCalledTimes(3)
    expect(wrapper.find('[aria-label="重试加载目录"]').exists()).toBe(false)
  })

  it('刷新目录期间不允许确认仍在校验的选择', async () => {
    const selectedDirectory = { id: 'selected', name: '待校验目录', path: '/selected' }
    const pendingRefresh = createDeferred<{
      data: { code: number; data: (typeof selectedDirectory)[] }
    }>()
    let rootRequestCount = 0
    const get = vi.fn((_url: string, { params }: { params: { parent_id: string } }) => {
      if (params.parent_id !== '') {
        return Promise.resolve({ data: { code: 200, data: [] } })
      }

      rootRequestCount += 1
      return rootRequestCount === 1
        ? Promise.resolve({ data: { code: 200, data: [selectedDirectory] } })
        : pendingRefresh.promise
    })
    const wrapper = mountSelector(get)

    await flushPromises()
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()
    await wrapper.get('[aria-label="刷新目录"]').trigger('click')
    await flushPromises()

    const selectButton = wrapper.findAll('button').find((button) => button.text().trim() === '选择')
    expect(selectButton?.attributes('disabled')).toBeDefined()

    await selectButton?.trigger('click')
    expect(wrapper.emitted('select')).toBeUndefined()

    pendingRefresh.resolve({ data: { code: 200, data: [] } })
    await flushPromises()
  })

  it('刷新受限根目录期间不允许新建文件夹', async () => {
    const pendingRefresh = createDeferred<{ data: { code: number; data: [] } }>()
    let rootRequestCount = 0
    const get = vi.fn(() => {
      rootRequestCount += 1
      return rootRequestCount === 1
        ? Promise.resolve({ data: { code: 200, data: [] } })
        : pendingRefresh.promise
    })
    const wrapper = mountSelector(get, { rootId: 'source-root', rootPath: '/source' })

    await flushPromises()
    await wrapper.get('[aria-label="刷新目录"]').trigger('click')
    await flushPromises()

    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('新建文件夹'))
    expect(createButton?.attributes('disabled')).toBeDefined()

    pendingRefresh.resolve({ data: { code: 200, data: [] } })
    await flushPromises()
  })

  it('刷新开始后禁止确认已打开的新建文件夹弹窗', async () => {
    const pendingRefresh = createDeferred<{ data: { code: number; data: [] } }>()
    let rootRequestCount = 0
    const get = vi.fn(() => {
      rootRequestCount += 1
      return rootRequestCount === 1
        ? Promise.resolve({ data: { code: 200, data: [] } })
        : pendingRefresh.promise
    })
    const post = vi.fn()
    const wrapper = mountSelector(get, { rootId: 'source-root', rootPath: '/source' }, post)

    await flushPromises()
    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('新建文件夹'))
    await createButton?.trigger('click')
    await flushPromises()

    const createDialog = Array.from(document.body.querySelectorAll<HTMLElement>('.el-dialog')).at(
      -1,
    )
    const confirmButton = Array.from(createDialog?.querySelectorAll('button') || []).find(
      (button) => button.textContent?.includes('确定'),
    )
    expect(confirmButton).toBeDefined()

    await wrapper.get('[aria-label="刷新目录"]').trigger('click')
    await flushPromises()

    expect(confirmButton?.disabled).toBe(true)
    confirmButton?.click()
    expect(post).not.toHaveBeenCalled()

    pendingRefresh.resolve({ data: { code: 200, data: [] } })
    await flushPromises()
  })

  it('新建文件夹请求进行期间不允许启动刷新', async () => {
    const pendingCreate = createDeferred<{ data: { code: number; data: typeof emptyDirectory } }>()
    const get = vi.fn().mockResolvedValue({ data: { code: 200, data: [] } })
    const post = vi.fn().mockReturnValue(pendingCreate.promise)
    const wrapper = mountSelector(get, { rootId: 'source-root', rootPath: '/source' }, post)

    await flushPromises()
    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('新建文件夹'))
    await createButton?.trigger('click')
    await flushPromises()

    const createDialog = Array.from(document.body.querySelectorAll<HTMLElement>('.el-dialog')).at(
      -1,
    )
    const createInput = createDialog?.querySelector<HTMLInputElement>('.el-input__inner')
    const confirmButton = Array.from(createDialog?.querySelectorAll('button') || []).find(
      (button) => button.textContent?.includes('确定'),
    )
    expect(createInput).not.toBeNull()
    expect(confirmButton).toBeDefined()

    createInput!.value = '新建目录'
    createInput!.dispatchEvent(new Event('input', { bubbles: true }))
    await flushPromises()
    confirmButton!.click()

    const refreshButton = wrapper.get('[aria-label="刷新目录"]')
    const refreshButtonElement = refreshButton.element as HTMLButtonElement
    refreshButtonElement.click()
    expect(get).toHaveBeenCalledTimes(1)

    await flushPromises()
    expect(refreshButton.attributes('disabled')).toBeDefined()

    pendingCreate.resolve({ data: { code: 200, data: emptyDirectory } })
    await flushPromises()
  })

  it('新建文件夹后忽略父目录较晚返回的旧子目录列表', async () => {
    const parent = { id: 'parent', name: '父目录', path: '/parent' }
    const createdDirectory = { id: 'created', name: '新建目录', path: '/parent/created' }
    const pendingChildListing = createDeferred<{
      data: { code: number; data: (typeof createdDirectory)[] }
    }>()
    const get = vi.fn((_url: string, { params }: { params: { parent_id: string } }) => {
      if (params.parent_id === '') {
        return Promise.resolve({ data: { code: 200, data: [parent] } })
      }
      return pendingChildListing.promise
    })
    const post = vi.fn().mockResolvedValue({ data: { code: 200, data: createdDirectory } })
    const wrapper = mountSelector(get, {}, post)

    await flushPromises()
    await wrapper.get('[role="treeitem"]').trigger('click')
    await flushPromises()

    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('新建文件夹'))
    await createButton?.trigger('click')
    await flushPromises()

    const createDialog = Array.from(document.body.querySelectorAll<HTMLElement>('.el-dialog')).at(
      -1,
    )
    const createInput = createDialog?.querySelector<HTMLInputElement>('.el-input__inner')
    const confirmButton = Array.from(createDialog?.querySelectorAll('button') || []).find(
      (button) => button.textContent?.includes('确定'),
    )
    expect(createInput).not.toBeNull()
    expect(confirmButton).toBeDefined()

    createInput!.value = '新建目录'
    createInput!.dispatchEvent(new Event('input', { bubbles: true }))
    await flushPromises()
    confirmButton!.click()
    await flushPromises()
    await flushPromises()

    expect(post).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('新建目录')

    pendingChildListing.resolve({ data: { code: 200, data: [] } })
    await flushPromises()

    expect(wrapper.text()).toContain('新建目录')
  })
})
