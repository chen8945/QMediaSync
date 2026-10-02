// 同步任务状态与子状态的展示映射。
// 状态机器值与展示差异见 docs/reference/task-sources.md。
import type { SyncLedgerState, SyncScanResult } from '@/types/syncTaskStream'
import { formatDuration } from '@/utils/timeUtils'

export type SyncTaskStatusTagType = 'info' | 'primary' | 'success' | 'danger' | 'warning'

export const isSyncTaskTerminal = (status?: number) =>
  status === 2 || status === 3 || status === 4 || status === 5 || status === 6

export const isSyncLedgerActive = (status: SyncLedgerState['ledger_status']) =>
  status === 'pending' || status === 'running'

export const isSyncTaskStreamTerminal = (task: { status: number } & SyncLedgerState) =>
  isSyncTaskTerminal(task.status) && !isSyncLedgerActive(task.ledger_status)

export function getSyncLedgerStatusText(status: SyncLedgerState['ledger_status']): string {
  switch (status) {
    case 'not_required':
      return '无需更新记录'
    case 'pending':
      return '等待后台更新记录'
    case 'running':
      return '后台更新记录中'
    case 'completed':
      return '记录更新完成'
    case 'failed':
      return '记录更新失败'
    case 'interrupted':
      return '记录更新中断'
    default:
      return '未记录'
  }
}

export const getSyncGenerationDuration = (start: number, finish?: number | null) =>
  !start ? '-' : finish ? formatDuration(Math.max(0, finish - start)) : '统计中'

export function getSyncTotalDuration(
  start: number,
  finish: number | null | undefined,
  ledger: SyncLedgerState,
): string {
  if (!ledger.ledger_status) return '未记录'
  if (!start) return '-'
  if (isSyncLedgerActive(ledger.ledger_status)) return '统计中'
  if (ledger.ledger_status === 'not_required') return getSyncGenerationDuration(start, finish)
  if (!finish || !ledger.ledger_finished_at) return '未完整统计'
  return formatDuration(Math.max(0, Math.max(finish, ledger.ledger_finished_at) - start))
}

export const getSyncScanSummary = (
  result?: Pick<SyncScanResult, 'succeeded_files' | 'failed_files' | 'skipped_files'> | null,
) =>
  result
    ? `成功 ${result.succeeded_files}，失败 ${result.failed_files}，跳过 ${result.skipped_files}`
    : '未记录'

export const getSyncCleanupText = (status?: SyncScanResult['cleanup_status']) => {
  switch (status) {
    case 'completed':
      return '已完成'
    case 'partial':
      return '仅清理完整范围'
    case 'skipped':
      return '未执行'
    default:
      return '未记录'
  }
}

// 获取状态标签类型
export function getSyncTaskStatusTagType(status: number): SyncTaskStatusTagType {
  switch (status) {
    case 0:
      return 'info' // 待开始
    case 1:
      return 'primary' // 运行中
    case 2:
      return 'success' // 完成
    case 3:
      return 'danger' // 失败
    case 4:
    case 5:
      return 'warning'
    default:
      return 'info'
  }
}

// 获取状态文本
export function getSyncTaskStatusText(status: number): string {
  switch (status) {
    case 0:
      return '待开始'
    case 1:
      return '运行中'
    case 2:
      return '已完成'
    case 3:
      return '失败'
    case 4:
      return '部分完成'
    case 5:
      return '扫描不完整'
    case 6:
      return '已取消'
    default:
      return '未知'
  }
}

// 获取子状态文本
export function getSyncTaskSubStatusText(subStatus: number): string {
  switch (subStatus) {
    case 0:
      return '待开始'
    case 1:
      return '正在处理网盘文件'
    case 2:
      return '正在处理本地文件'
    default:
      return '未知'
  }
}
