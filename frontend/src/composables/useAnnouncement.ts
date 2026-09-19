import { onMounted, ref } from 'vue'
import { fetchAnnouncements, type Announcement } from '@/api/dashboard'
import { useHttpClient } from '@/http/client'
import { parseHttpError } from '@/http/errors'

export type { Announcement } from '@/api/dashboard'

export function useAnnouncement() {
  const http = useHttpClient()
  const announcementList = ref<Announcement[]>([])
  const announcementLoading = ref(false)

  const loadAnnouncements = async () => {
    try {
      announcementLoading.value = true
      announcementList.value = await fetchAnnouncements(http)
    } catch (error) {
      const parsed = parseHttpError(error, { fallbackMessage: '加载公告列表失败' })
      if (parsed.shouldNotify) console.error('加载公告列表错误：', parsed.diagnostics)
      announcementList.value = []
    } finally {
      announcementLoading.value = false
    }
  }

  onMounted(() => {
    loadAnnouncements()
  })

  return {
    announcementList,
    announcementLoading,
    loadAnnouncements,
  }
}
