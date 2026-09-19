import { ElMessage } from 'element-plus'

import { parseHttpError, type ParseHttpErrorOptions, type ParsedHttpError } from '@/http/errors'

// 公共错误解析不弹窗；页面需要“记录诊断并提示”时使用此展示层辅助。
export function notifyHttpError(
  error: unknown,
  label: string,
  options: ParseHttpErrorOptions & { messagePrefix?: string } = {},
): ParsedHttpError {
  const parsed = parseHttpError(error, options)
  if (parsed.shouldNotify) {
    console.error(label, parsed.diagnostics)
    ElMessage.error(
      options.messagePrefix && options.messagePrefix !== parsed.message
        ? `${options.messagePrefix}：${parsed.message}`
        : parsed.message,
    )
  }
  return parsed
}
