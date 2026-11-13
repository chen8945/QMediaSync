<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import PageHeader from '@/components/common/PageHeader.vue'
import MediaServerSettings from '@/components/media-library/MediaServerSettings.vue'
import FnosProxySettings from '@/components/media-library/FnosProxySettings.vue'

type MediaTab = 'emby' | 'jellyfin' | 'fnos'
const route = useRoute()
const router = useRouter()
const activeTab = computed<MediaTab>(() =>
  route.hash === '#jellyfin' ? 'jellyfin' : route.hash === '#fnos' ? 'fnos' : 'emby',
)
const visited = reactive(new Set<MediaTab>())
watch(activeTab, (tab) => visited.add(tab), { immediate: true })
const selectTab = (tab: string | number) => {
  if (tab === 'emby' || tab === 'jellyfin' || tab === 'fnos')
    void router.replace({ path: route.path, query: route.query, hash: `#${tab}` })
}
</script>

<template>
  <div class="main-content-container media-library-content">
    <PageHeader />
    <el-tabs :model-value="activeTab" @tab-change="selectTab">
      <el-tab-pane label="Emby" name="emby">
        <MediaServerSettings
          v-if="visited.has('emby')"
          server-type="emby"
          :active="activeTab === 'emby'"
        />
      </el-tab-pane>
      <el-tab-pane label="Jellyfin" name="jellyfin">
        <MediaServerSettings
          v-if="visited.has('jellyfin')"
          server-type="jellyfin"
          :active="activeTab === 'jellyfin'"
        />
      </el-tab-pane>
      <el-tab-pane label="飞牛影视" name="fnos">
        <FnosProxySettings v-if="visited.has('fnos')" :active="activeTab === 'fnos'" />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<style scoped>
.media-library-content {
  min-width: 0;
}
.media-library-content :deep(.el-tabs__content) {
  overflow: visible;
}
</style>
