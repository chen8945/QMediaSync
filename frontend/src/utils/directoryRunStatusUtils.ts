// 同步目录与刮削目录运行状态的展示映射。
// 返回的状态类名（status-running 等）由各页面自身的卡片样式定义。

interface DirectoryRunStatusRow {
  is_running: number
}

// 获取目录运行状态样式类
export function getDirectoryRunStatusClass(row: DirectoryRunStatusRow): string {
  if (row.is_running === 2) return 'status-running'
  if (row.is_running === 1) return 'status-waiting'
  return 'status-idle'
}

// 获取目录运行状态文本
export function getDirectoryRunStatusText(row: DirectoryRunStatusRow): string {
  if (row.is_running === 2) return '运行中'
  if (row.is_running === 1) return '等待中'
  return '空闲'
}
