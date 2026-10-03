// @vitest-environment happy-dom

import { shallowMount } from '@vue/test-utils'
import { nextTick, shallowRef } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import AppSyncTaskDetail from '@/components/AppSyncTaskDetail.vue'
import type { SyncLedgerState, SyncTask, SyncTaskStatus } from '@/types/syncTaskStream'

const task = shallowRef<SyncTask>()
const baseTask: SyncTask = {
  id: 8,
  sync_path_id: 2,
  created_at: 10,
  updated_at: 20,
  finish_at: 20,
  status: 2,
  sub_status: 2,
  total: 6,
  new_strm: 2,
  new_meta: 0,
  new_upload: 0,
  net_file_start_at: 10,
  net_file_finish_at: 15,
  local_file_start_at: 15,
  local_file_finish_at: 20,
  local_path: '/strm',
  remote_path: '/cloud',
  fail_reason: '',
}
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: '8' } }),
  useRouter: () => ({}),
}))
vi.mock('@/composables/useLogFileActions', () => ({
  useLogFileActions: () => ({ downloadLogFile: vi.fn() }),
}))
vi.mock('@/composables/useSyncTaskStream', () => ({
  useSyncTaskStream: () => ({
    task,
    logs: shallowRef([]),
    terminal: shallowRef(true),
    connected: shallowRef(false),
    logPath: shallowRef(''),
  }),
}))

describe('同步任务失败范围展示', () => {
  it.each<[SyncTaskStatus, string]>([
    [4, '部分完成'],
    [5, '扫描不完整'],
    [6, '已取消'],
  ])('终态 %s 展示实际结果、失败范围和清理保护', (status, label) => {
    task.value = {
      ...baseTask,
      status,
      fail_reason: '目录读取失败',
      scan_result: {
        succeeded_files: 2,
        failed_files: 1,
        skipped_files: 3,
        failures: [{ kind: 'directory', path: '/cloud/locked', reason: '无权读取' }],
        cleanup_status: 'partial',
        cleanup_reason: '保留受影响的目录及后代',
      },
    }
    const wrapper = shallowMount(AppSyncTaskDetail, {
      global: { renderStubDefaultSlot: true, directives: { loading: () => {} } },
    })
    expect(wrapper.text()).toContain(label)
    expect(wrapper.text()).toContain('成功 2，失败 1，跳过 3')
    expect(wrapper.text()).toContain('仅清理完整范围')
    expect(wrapper.text()).toContain('保留受影响的目录及后代')
    expect(wrapper.text()).toContain('/cloud/locked')
    expect(wrapper.text()).toContain('无权读取')
    expect(wrapper.text()).not.toContain('完成任务')
    wrapper.unmount()
  })

  it('历史记录没有结果明细时显示未记录', () => {
    task.value = { ...baseTask }
    const wrapper = shallowMount(AppSyncTaskDetail, {
      global: { renderStubDefaultSlot: true, directives: { loading: () => {} } },
    })
    expect(wrapper.text()).toContain('未记录')
    expect(wrapper.text()).not.toContain('成功 0，失败 0，跳过 0')
    wrapper.unmount()
  })
})

describe('同步任务后台结果与双耗时', () => {
  it.each<[SyncLedgerState, string, string]>([
    [{ ledger_status: 'pending' }, '等待后台更新记录', '统计中'],
    [{ ledger_status: 'running' }, '后台更新记录中', '统计中'],
    [{ ledger_status: 'completed', ledger_finished_at: 28 }, '记录更新完成', '18 秒'],
    [{ ledger_status: 'completed', ledger_finished_at: 18 }, '记录更新完成', '10 秒'],
    [
      { ledger_status: 'failed', ledger_finished_at: 28, ledger_error: '事务写入失败' },
      '记录更新失败',
      '18 秒',
    ],
    [{ ledger_status: 'interrupted', ledger_finished_at: 25 }, '记录更新中断', '15 秒'],
    [{ ledger_status: 'interrupted', ledger_finished_at: null }, '记录更新中断', '未完整统计'],
    [{ ledger_status: 'not_required' }, '无需更新记录', '10 秒'],
    [{ ledger_status: null }, '未记录', '未记录'],
    [{}, '未记录', '未记录'],
  ])('后台 %j 保留固定生成时间并显示实际总耗时', (ledger, label, total) => {
    task.value = { ...baseTask, ...ledger }
    const wrapper = shallowMount(AppSyncTaskDetail, {
      global: { renderStubDefaultSlot: true, directives: { loading: () => {} } },
    })
    const description = (name: string) =>
      wrapper
        .findAll('el-descriptions-item-stub')
        .find((item) => item.attributes('label') === name)!
        .text()
    expect(description('生成耗时')).toBe('10 秒')
    expect(description('文件记录更新')).toBe(label)
    expect(description('总耗时')).toBe(total)
    if (ledger.ledger_error) expect(description('文件记录更新原因')).toBe(ledger.ledger_error)
    wrapper.unmount()
  })

  it('后台更新响应刷新当前详情且不改变生成耗时', async () => {
    task.value = { ...baseTask, ledger_status: 'pending' }
    const wrapper = shallowMount(AppSyncTaskDetail, {
      global: { renderStubDefaultSlot: true, directives: { loading: () => {} } },
    })
    expect(wrapper.text()).toContain('等待后台更新记录')
    task.value = {
      ...baseTask,
      ledger_status: 'failed',
      ledger_finished_at: 28,
      ledger_error: '数据库不可用',
    }
    await nextTick()
    expect(wrapper.text()).toContain('记录更新失败')
    expect(wrapper.text()).toContain('数据库不可用')
    expect(wrapper.text()).toContain('10 秒')
    expect(wrapper.text()).toContain('18 秒')
    wrapper.unmount()
  })
})
