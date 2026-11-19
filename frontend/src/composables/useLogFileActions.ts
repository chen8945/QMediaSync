import { logDownloadURL, logViewURL } from '@/api/logs'
import { parseHttpError } from '@/http/errors'
import { ElMessage } from 'element-plus'

interface DownloadLogFileOptions {
  emptyMessage?: string
  successMessage?: string
  errorPrefix?: string
}

export function useLogFileActions() {
  const openLogFile = (
    logPath: string,
    download: boolean,
    options: DownloadLogFileOptions = {},
  ) => {
    const normalizedLogPath = logPath.trim()
    if (!normalizedLogPath) {
      ElMessage.error(options.emptyMessage || '日志文件路径为空')
      return
    }

    try {
      const link = document.createElement('a')
      link.href = download ? logDownloadURL(normalizedLogPath) : logViewURL(normalizedLogPath)
      if (download) link.download = normalizedLogPath.split('/').pop() || 'logfile.log'
      link.target = '_blank'
      link.rel = 'noopener'
      document.body.appendChild(link)
      link.click()
      setTimeout(() => {
        document.body.removeChild(link)
      }, 100)
      if (download) ElMessage.success(options.successMessage || '开始下载日志文件')
    } catch (error) {
      const failure = parseHttpError(error, {
        fallbackMessage: options.errorPrefix || (download ? '下载日志失败' : '查看日志失败'),
      })
      if (failure.shouldNotify) ElMessage.error(failure.message)
    }
  }

  return {
    downloadLogFile: (logPath: string, options?: DownloadLogFileOptions) =>
      openLogFile(logPath, true, options),
    viewLogFile: (logPath: string) => openLogFile(logPath, false),
  }
}
