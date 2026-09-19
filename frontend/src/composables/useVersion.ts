import { onMounted, ref } from 'vue'
import { fetchSystemVersion, type SystemVersion } from '@/api/systemInfo'
import { useHttpClient } from '@/http/client'
import { parseHttpError } from '@/http/errors'

export type VersionInfo = Pick<SystemVersion, 'version' | 'build_time' | 'date'>

export function useVersion() {
  const http = useHttpClient()
  const versionInfo = ref<VersionInfo | null>(null)
  const versionLoading = ref(true)

  const loadVersionInfo = async () => {
    try {
      versionLoading.value = true
      versionInfo.value = await fetchSystemVersion(http)
    } catch (error) {
      const failure = parseHttpError(error)
      if (failure.shouldNotify) console.error('加载系统版本信息失败：', failure.diagnostics)
      versionInfo.value = null
    } finally {
      versionLoading.value = false
    }
  }

  onMounted(() => {
    loadVersionInfo()
  })

  return {
    versionInfo,
    versionLoading,
    loadVersionInfo,
  }
}
