// ElMessageBox 确认弹窗的取消判定辅助。

/**
 * 判断错误是否为用户取消 ElMessageBox 确认弹窗。
 *
 * 取消时 ElMessageBox 默认 reject `'cancel'`（按钮）或 `'close'`（关闭）；
 * 部分调用点传入自定义 `distinguishCancelAndClose` 或包装错误，这里同时
 * 兼容消息包含“用户取消操作”的包装错误。
 */
export function isMessageBoxCancelError(error: unknown): boolean {
  if (error === 'cancel' || error === 'close') {
    return true
  }

  const errorMessage = error instanceof Error ? error.message : String(error)
  return errorMessage.includes('用户取消操作')
}
