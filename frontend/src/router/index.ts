import { createRouter } from 'vue-router'
import { createQMediaSyncHashHistory } from './history'
import { createAsyncRouteComponent } from './asyncRoute'
import { useAuthStore } from '@/stores/auth'
import { http } from '@/http/client'

const AppHome = createAsyncRouteComponent('AppHome', () => import('@/components/AppHome.vue'))
const AppLogin = createAsyncRouteComponent('AppLogin', () => import('@/components/AppLogin.vue'))
const AppUserSettings = createAsyncRouteComponent(
  'AppUserSettings',
  () => import('@/components/AppUserSettings.vue'),
)
const AppStrmSettings = createAsyncRouteComponent(
  'AppStrmSettings',
  () => import('@/components/AppStrmSettings.vue'),
)
const AppEmbySettings = createAsyncRouteComponent(
  'AppEmbySettings',
  () => import('@/components/AppEmbySettings.vue'),
)
const AppSyncRecords = createAsyncRouteComponent(
  'AppSyncRecords',
  () => import('@/components/AppSyncRecords.vue'),
)
const AppSyncTaskDetail = createAsyncRouteComponent(
  'AppSyncTaskDetail',
  () => import('@/components/AppSyncTaskDetail.vue'),
)
const AppSyncDirectories = createAsyncRouteComponent(
  'AppSyncDirectories',
  () => import('@/components/AppSyncDirectories.vue'),
)
const AppSyncDirectoryForm = createAsyncRouteComponent(
  'AppSyncDirectoryForm',
  () => import('@/components/AppSyncDirectoryForm.vue'),
)
const AppCloudAccounts = createAsyncRouteComponent(
  'AppCloudAccounts',
  () => import('@/components/AppCloudAccounts.vue'),
)
const AppThreadSettings = createAsyncRouteComponent(
  'AppThreadSettings',
  () => import('@/components/AppThreadSettings.vue'),
)
const AppLogSettings = createAsyncRouteComponent(
  'AppLogSettings',
  () => import('@/components/AppLogSettings.vue'),
)
const AppTmdbSettings = createAsyncRouteComponent(
  'AppTmdbSettings',
  () => import('@/components/AppTmdbSettings.vue'),
)
const AppAiSettings = createAsyncRouteComponent(
  'AppAiSettings',
  () => import('@/components/AppAiSettings.vue'),
)
const AppCategoryStrategy = createAsyncRouteComponent(
  'AppCategoryStrategy',
  () => import('@/components/AppCategoryStrategy.vue'),
)
const AppScrapePathes = createAsyncRouteComponent(
  'AppScrapePathes',
  () => import('@/components/AppScrapePathes.vue'),
)
const AppScrapePathForm = createAsyncRouteComponent(
  'AppScrapePathForm',
  () => import('@/components/AppScrapePathForm.vue'),
)
const AppScrapeRecords = createAsyncRouteComponent(
  'AppScrapeRecords',
  () => import('@/components/AppScrapeRecords.vue'),
)
const AppUploadQueue = createAsyncRouteComponent(
  'AppUploadQueue',
  () => import('@/components/AppUploadQueue.vue'),
)
const AppDownloadQueue = createAsyncRouteComponent(
  'AppDownloadQueue',
  () => import('@/components/AppDownloadQueue.vue'),
)
const AppNotificationChannels = createAsyncRouteComponent(
  'AppNotificationChannels',
  () => import('@/components/AppNotificationChannels.vue'),
)
const AppApiKeys = createAsyncRouteComponent(
  'AppApiKeys',
  () => import('@/components/AppApiKeys.vue'),
)
const AppLoginSessions = createAsyncRouteComponent(
  'AppLoginSessions',
  () => import('@/components/user-settings/LoginSessions.vue'),
)
const AppFileManager = createAsyncRouteComponent(
  'AppFileManager',
  () => import('@/components/AppFileManager.vue'),
)
const AppUpdate = createAsyncRouteComponent('AppUpdate', () => import('@/components/AppUpdate.vue'))
const AppBackupSettings = createAsyncRouteComponent(
  'AppBackupSettings',
  () => import('@/components/AppBackupSettings.vue'),
)
const AppBackupRecords = createAsyncRouteComponent(
  'AppBackupRecords',
  () => import('@/components/AppBackupRecords.vue'),
)
const AppBackupRestore = createAsyncRouteComponent(
  'AppBackupRestore',
  () => import('@/components/AppBackupRestore.vue'),
)
const AppProxySettings = createAsyncRouteComponent(
  'AppProxySettings',
  () => import('@/components/AppProxySettings.vue'),
)
const AppDatabaseRepair = createAsyncRouteComponent(
  'AppDatabaseRepair',
  () => import('@/components/AppDatabaseRepair.vue'),
)

// 定义路由元信息类型
declare module 'vue-router' {
  interface RouteMeta {
    title: string
    requiresAuth: boolean
    parent?: string
    icon?: string
    showInMenu?: boolean
  }
}

const routes = [
  {
    path: '/login',
    name: 'login',
    component: AppLogin,
    meta: {
      title: '登录',
      requiresAuth: false,
      showInMenu: false,
    },
  },
  {
    path: '/',
    name: 'home',
    component: AppHome,
    meta: {
      title: '首页',
      requiresAuth: true,
      icon: 'House',
      showInMenu: true,
    },
  },
  {
    path: '/accounts',
    name: 'accounts',
    component: AppCloudAccounts,
    meta: {
      title: '网盘账号',
      requiresAuth: true,
      icon: 'User',
      showInMenu: true,
    },
  },
  {
    path: '/sync',
    name: 'sync',
    redirect: '/sync-directories',
    meta: {
      title: 'STRM 同步',
      requiresAuth: true,
      icon: 'DocumentCopy',
      showInMenu: true,
    },
  },
  {
    path: '/sync-directories',
    name: 'sync-directories',
    component: AppSyncDirectories,
    meta: {
      title: 'STRM 同步目录',
      requiresAuth: true,
      parent: 'sync',
      icon: 'FolderOpened',
      showInMenu: true,
    },
  },
  {
    path: '/sync-directory/add',
    name: 'sync-directory-add',
    component: AppSyncDirectoryForm,
    meta: {
      title: '添加同步目录',
      requiresAuth: true,
      parent: 'sync',
      showInMenu: false,
    },
  },
  {
    path: '/sync-directory/edit/:id',
    name: 'sync-directory-edit',
    component: AppSyncDirectoryForm,
    meta: {
      title: '编辑同步目录',
      requiresAuth: true,
      parent: 'sync',
      showInMenu: false,
    },
  },
  {
    path: '/sync-records',
    name: 'sync-records',
    component: AppSyncRecords,
    meta: {
      title: 'STRM 同步记录',
      requiresAuth: true,
      parent: 'sync',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/settings/strm',
    name: 'settings-strm',
    component: AppStrmSettings,
    meta: {
      title: 'STRM 设置',
      requiresAuth: true,
      parent: 'sync',
      icon: 'Setting',
      showInMenu: true,
    },
  },
  {
    path: '/sync-records/:id',
    name: 'sync-task-detail',
    component: AppSyncTaskDetail,
    meta: {
      title: '同步任务详情',
      requiresAuth: true,
      parent: 'sync',
      showInMenu: false,
    },
  },
  {
    path: '/scrape',
    name: 'scrape',
    redirect: '/scrape-pathes',
    meta: {
      title: '刮削 & 整理',
      requiresAuth: true,
      icon: 'Film',
      showInMenu: true,
    },
  },
  {
    path: '/scrape-pathes',
    name: 'scrape-pathes',
    component: AppScrapePathes,
    meta: {
      title: '刮削目录',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'FolderOpened',
      showInMenu: true,
    },
  },
  {
    path: '/scrape-path/add',
    name: 'scrape-path-add',
    component: AppScrapePathForm,
    meta: {
      title: '添加刮削目录',
      requiresAuth: true,
      parent: 'scrape',
      showInMenu: false,
    },
  },
  {
    path: '/scrape-path/edit/:id',
    name: 'scrape-path-edit',
    component: AppScrapePathForm,
    meta: {
      title: '编辑刮削目录',
      requiresAuth: true,
      parent: 'scrape',
      showInMenu: false,
    },
  },
  {
    path: '/scrape-records',
    name: 'scrape-records',
    component: AppScrapeRecords,
    meta: {
      title: '刮削记录',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/settings/tmdb',
    name: 'settings-tmdb',
    component: AppTmdbSettings,
    meta: {
      title: '刮削设置',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'Film',
      showInMenu: true,
    },
  },
  {
    path: '/settings/ai',
    name: 'settings-ai',
    component: AppAiSettings,
    meta: {
      title: 'AI 识别设置',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'View',
      showInMenu: true,
    },
  },
  {
    path: '/settings/category-strategy',
    name: 'settings-category-strategy',
    component: AppCategoryStrategy,
    meta: {
      title: '二级分类设置',
      requiresAuth: true,
      parent: 'scrape',
      icon: 'Operation',
      showInMenu: true,
    },
  },

  {
    path: '/transfer',
    name: 'transfer',
    redirect: '/upload-queue',
    meta: {
      title: '上传下载',
      requiresAuth: true,
      icon: 'Download',
      showInMenu: true,
    },
  },
  {
    path: '/upload-queue',
    name: 'upload-queue',
    component: AppUploadQueue,
    meta: {
      title: '上传队列',
      requiresAuth: true,
      parent: 'transfer',
      icon: 'Upload',
      showInMenu: true,
    },
  },
  {
    path: '/download-queue',
    name: 'download-queue',
    component: AppDownloadQueue,
    meta: {
      title: '下载队列',
      requiresAuth: true,
      parent: 'transfer',
      icon: 'Download',
      showInMenu: true,
    },
  },
  {
    path: '/file-manager',
    name: 'file-manager',
    component: AppFileManager,
    meta: {
      title: '网盘文件管理',
      requiresAuth: true,
      icon: 'Folder',
      showInMenu: true,
    },
  },
  {
    path: '/database',
    name: 'database',
    redirect: '/database/backup/settings',
    meta: {
      title: '备份管理',
      requiresAuth: true,
      icon: 'DataAnalysis',
      showInMenu: true,
    },
  },
  {
    path: '/database/backup/settings',
    name: 'database-backup-settings',
    component: AppBackupSettings,
    meta: {
      title: '备份设置',
      requiresAuth: true,
      parent: 'database',
      icon: 'Tools',
      showInMenu: true,
    },
  },
  {
    path: '/database/backup/records',
    name: 'database-backup-records',
    component: AppBackupRecords,
    meta: {
      title: '备份记录',
      requiresAuth: true,
      parent: 'database',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/database/backup/restore',
    name: 'database-backup-restore',
    component: AppBackupRestore,
    meta: {
      title: '备份恢复',
      requiresAuth: true,
      parent: 'database',
      icon: 'RefreshLeft',
      showInMenu: true,
    },
  },
  {
    path: '/database/repair',
    name: 'database-repair',
    component: AppDatabaseRepair,
    meta: {
      title: '数据库修复',
      requiresAuth: true,
      parent: 'database',
      icon: 'DataLine',
      showInMenu: true,
    },
  },
  {
    path: '/settings',
    name: 'settings',
    redirect: '/settings/user',
    meta: {
      title: '系统设置',
      requiresAuth: true,
      icon: 'Setting',
      showInMenu: true,
    },
  },
  {
    path: '/settings/user',
    name: 'settings-user',
    component: AppUserSettings,
    meta: {
      title: '用户管理',
      requiresAuth: true,
      parent: 'settings',
      icon: 'UserFilled',
      showInMenu: true,
    },
  },
  {
    path: '/settings/api-keys',
    name: 'settings-api-keys',
    component: AppApiKeys,
    meta: {
      title: 'API Key',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Key',
      showInMenu: true,
    },
  },
  {
    path: '/settings/sessions',
    name: 'settings-sessions',
    component: AppLoginSessions,
    meta: {
      title: '登录设备',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Monitor',
      showInMenu: true,
    },
  },
  {
    path: '/settings/notification',
    name: 'settings-notification',
    component: AppNotificationChannels,
    meta: {
      title: '通知管理',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Promotion',
      showInMenu: true,
    },
  },
  {
    path: '/settings/emby',
    name: 'settings-emby',
    component: AppEmbySettings,
    meta: {
      title: 'Emby',
      requiresAuth: true,
      parent: 'settings',
      icon: 'VideoPlay',
      showInMenu: true,
    },
  },
  {
    path: '/settings/threads',
    name: 'settings-threads',
    component: AppThreadSettings,
    meta: {
      title: '接口速率',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Operation',
      showInMenu: true,
    },
  },
  {
    path: '/settings/log',
    name: 'settings-log',
    component: AppLogSettings,
    meta: {
      title: '日志设置',
      requiresAuth: true,
      parent: 'settings',
      icon: 'List',
      showInMenu: true,
    },
  },
  {
    path: '/settings/proxy',
    name: 'settings-proxy',
    component: AppProxySettings,
    meta: {
      title: '网络代理',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Link',
      showInMenu: true,
    },
  },
  {
    path: '/settings/update',
    name: 'settings-update',
    component: AppUpdate,
    meta: {
      title: '版本更新',
      requiresAuth: true,
      parent: 'settings',
      icon: 'Upload',
      showInMenu: true,
    },
  },
  // 旧路径重定向：保持外部书签、历史记录和旧文档链接可用
  {
    path: '/proxy',
    redirect: '/settings/proxy',
  },
  {
    path: '/settings/database-repair',
    redirect: '/database/repair',
  },
  // 未知路径统一回首页，由首页的 requiresAuth 继续走鉴权守卫
  {
    path: '/:pathMatch(.*)*',
    redirect: '/',
  },
]

const router = createRouter({
  history: createQMediaSyncHashHistory(),
  routes,
})

// 路由守卫
router.beforeEach(async (to) => {
  const authStore = useAuthStore()

  if (!authStore.hasInitialized && authStore.authStatus === 'checking') {
    await authStore.bootstrapAuth(http)
  }

  if (to.meta.requiresAuth && !authStore.isAuthenticated) {
    return {
      name: 'login',
      query: { redirect: to.fullPath },
      replace: true,
    }
  }

  if (to.name === 'login' && authStore.isAuthenticated) {
    return { name: 'home', replace: true }
  }

  return true
})

router.afterEach((to, _from, failure) => {
  if (!failure && to.meta.title) {
    document.title = `${to.meta.title} - QMediaSync`
  }
})

export default router
