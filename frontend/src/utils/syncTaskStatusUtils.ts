// 同步任务状态与子状态的展示映射。
// 状态机器值与展示差异见 docs/reference/task-sources.md。

export type SyncTaskStatusTagType = 'info' | 'primary' | 'success' | 'danger'

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
