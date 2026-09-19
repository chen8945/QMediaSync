import { logDownloadURL } from '@/api/logs'
import { parseHttpError } from '@/http/errors'
import { ElMessage } from 'element-plus'

interface DownloadLogFileOptions {
  emptyMessage?: string
  successMessage?: string
  errorPrefix?: string
}

export function useLogFileActions() {
  const downloadLogFile = (logPath: string, options: DownloadLogFileOptions = {}) => {
    const normalizedLogPath = logPath.trim()
    if (!normalizedLogPath) {
      ElMessage.error(options.emptyMessage || '日志文件路径为空')
      return
    }

    try {
      const downloadUrl = logDownloadURL(normalizedLogPath)
      const link = document.createElement('a')
      link.href = downloadUrl
      link.download = normalizedLogPath.split('/').pop() || 'logfile.log'
      link.target = '_blank'
      document.body.appendChild(link)
      link.click()
      setTimeout(() => {
        document.body.removeChild(link)
      }, 100)
      ElMessage.success(options.successMessage || '开始下载日志文件')
    } catch (error) {
      const failure = parseHttpError(error, {
        fallbackMessage: options.errorPrefix || '下载日志失败',
      })
      if (failure.shouldNotify) ElMessage.error(failure.message)
    }
  }

  return {
    downloadLogFile,
  }
}
