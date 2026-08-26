<template>
  <!-- 登录页面 -->
  <div v-if="$route.name === 'login'" class="login-layout">
    <router-view />
  </div>

  <!-- 主应用布局 -->
  <div v-else class="common-layout">
    <el-container>
      <!-- 移动端遮罩层 -->
      <div v-if="isMobile && isMenuOpen" class="mobile-overlay" @click="toggleMenu"></div>

      <!-- 侧边栏 -->
      <el-aside
        id="mobile-navigation"
        :width="isMobile ? '250px' : '200px'"
        :class="{
          'mobile-aside': isMobile,
          'mobile-aside-open': isMobile && isMenuOpen,
          'is-menu-scrolling': isMenuScrolling,
        }"
        @wheel.passive="handleMenuWheel"
      >
        <!-- 用户信息 -->
        <div class="user-info">
          <div class="user-avatar">
            <el-icon size="24">
              <User />
            </el-icon>
          </div>
          <div class="user-details">
            <div class="username">{{ authStore.user?.username || '用户' }}</div>
            <el-button link size="small" class="logout-btn" @click="handleLogout">
              退出登录
            </el-button>
          </div>
        </div>

        <el-menu
          ref="menuRef"
          :default-active="$route.path"
          :default-openeds="defaultOpeneds"
          router
          class="el-menu-vertical"
          @select="handleMenuSelect"
        >
          <!-- 遍历一级菜单 -->
          <template v-for="menu in menuItems" :key="menu.path">
            <!-- 如果是子菜单 -->
            <el-sub-menu v-if="menu.children && menu.children.length > 0" :index="menu.path">
              <template #title>
                <el-icon>
                  <component :is="menu.iconComponent" :key="menu.meta.icon" />
                </el-icon>
                <span>{{ menu.meta.title }}</span>
              </template>
              <!-- 遍历子菜单 -->
              <el-menu-item v-for="child in menu.children" :key="child.path" :index="child.path">
                <el-icon>
                  <component :is="child.iconComponent" :key="child.meta.icon" />
                </el-icon>
                <span>{{ child.meta.title }}</span>
              </el-menu-item>
            </el-sub-menu>
            <!-- 如果是普通菜单 -->
            <el-menu-item v-else :index="menu.path">
              <el-icon>
                <component :is="menu.iconComponent" :key="menu.meta.icon" />
              </el-icon>
              <span>{{ menu.meta.title }}</span>
            </el-menu-item>
          </template>
          <!-- 使用帮助 -->
          <el-menu-item index="help" @click="openHelp">
            <el-icon>
              <QuestionFilled />
            </el-icon>
            <span>使用帮助</span>
          </el-menu-item>
        </el-menu>
      </el-aside>

      <!-- 主内容区 -->
      <el-main class="main-content">
        <!-- 移动端顶部栏 -->
        <div v-if="isMobile" class="mobile-header">
          <div class="left-section">
            <el-button
              link
              class="menu-toggle"
              :icon="Menu"
              :aria-label="isMenuOpen ? '关闭菜单' : '打开菜单'"
              :aria-expanded="isMenuOpen"
              aria-controls="mobile-navigation"
              @click="toggleMenu"
            />
            <h1 class="page-title">{{ getCurrentPageTitle() }}</h1>
          </div>
          <el-dropdown class="user-dropdown">
            <el-button
              link
              class="user-btn"
              :icon="User"
              aria-label="打开用户菜单"
              aria-haspopup="menu"
            />
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item>{{ authStore.user?.username }}</el-dropdown-item>
                <el-dropdown-item divided @click="handleLogout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
        <el-alert
          v-if="realtimeActive && !realtimeSupported"
          class="realtime-status-alert"
          title="当前浏览器不支持自动刷新，请手动刷新页面查看最新状态"
          type="warning"
          :closable="false"
          show-icon
        />
        <el-alert
          v-else-if="realtimeActive && realtimeConnectionState === 'reconnecting'"
          class="realtime-status-alert"
          title="实时更新暂时断开，正在重新连接…"
          type="warning"
          :closable="false"
          show-icon
        />
        <router-view v-slot="{ Component, route: routeView }">
          <keep-alive :include="cachedComponentNames">
            <component :is="Component" :key="getRouteViewKey(routeView.name, routeView.fullPath)" />
          </keep-alive>
        </router-view>
      </el-main>
    </el-container>
  </div>

  <!-- 全局备份/恢复进度弹窗 -->
  <el-dialog
    v-model="backupStore.showProgressDialog"
    :title="backupStore.taskType === 'backup' ? '备份进行中' : '数据库恢复中'"
    :width="isMobile ? '90%' : '600px'"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    :show-close="false"
    center
  >
    <div class="backup-progress-content">
      <!-- 进度条 -->
      <el-progress
        :percentage="backupStore.progress?.progress || 0"
        :status="getProgressStatus()"
        :stroke-width="20"
      />

      <!-- 当前步骤 -->
      <div v-if="backupStore.progress?.current_step" class="progress-step">
        <el-icon class="is-loading">
          <Loading />
        </el-icon>
        <span>{{ backupStore.progress.current_step }}</span>
      </div>

      <!-- 表处理进度 -->
      <div v-if="backupStore.progress?.total_tables" class="progress-tables">
        <span
          >已处理：{{ backupStore.progress.processed_tables || 0 }} /
          {{ backupStore.progress.total_tables }} 个表</span
        >
      </div>

      <!-- 时间信息 -->
      <div v-if="backupStore.progress?.elapsed_seconds !== undefined" class="progress-time">
        <div class="time-item">
          <span class="label">已耗时：</span>
          <span class="value">{{ formatDuration(backupStore.progress.elapsed_seconds) }}</span>
        </div>
        <div v-if="backupStore.progress.estimated_seconds" class="time-item">
          <span class="label">预计剩余：</span>
          <span class="value">{{
            formatDuration(
              backupStore.progress.estimated_seconds - backupStore.progress.elapsed_seconds,
            )
          }}</span>
        </div>
      </div>

      <!-- 错误重试提示 -->
      <el-alert
        v-if="backupStore.errorRetryCount > 0"
        :title="`网络异常，正在重试 (${backupStore.errorRetryCount}/${3})…`"
        type="warning"
        :closable="false"
        style="margin-top: 16px"
      />
    </div>
  </el-dialog>
</template>

<script setup lang="ts">
import { Loading, Menu, QuestionFilled, User } from '@element-plus/icons-vue'
import {
  ref,
  onUnmounted,
  computed,
  useTemplateRef,
  watch,
  type Component as VueComponent,
} from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useBackupStore } from '@/stores/backup'
import { http } from '@/http/client'
import { useDeviceType } from '@/composables/useDeviceType'
import {
  realtimeActive,
  realtimeConnectionState,
  realtimeSupported,
} from '@/composables/useRealtimeEvents'
import { ElMessage, ElMessageBox, type MenuInstance } from 'element-plus'
import { formatDuration } from '@/utils/timeUtils'
import { getIconComponent } from '@/components/common/iconRegistry'
import type { PageMeta } from '@/router/pageMeta'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const backupStore = useBackupStore()
const { isMobile } = useDeviceType()
const isMenuOpen = ref(false)
const menuRef = useTemplateRef<MenuInstance>('menuRef')

// KeepAlive include 匹配组件名，不匹配路由名
const cachedComponentNames = [
  'AppUploadQueue',
  'AppDownloadQueue',
  'AppSyncRecords',
  'AppScrapeRecords',
  'AppFileManager',
]

const cachedRouteNames = new Set([
  'upload-queue',
  'download-queue',
  'sync-records',
  'scrape-records',
  'file-manager',
])

const getRouteViewKey = (routeName: unknown, fullPath: string) => {
  if (typeof routeName === 'string' && cachedRouteNames.has(routeName)) {
    return routeName
  }
  return fullPath
}

// 定义菜单项类型
interface MenuItem {
  path: string
  name?: string
  meta: {
    title: string
    icon: string
    showInMenu: boolean
    parent?: string
  }
  iconComponent: VueComponent
  children?: MenuItem[]
}

// 计算属性：生成菜单数据
const menuItems = computed(() => {
  // 使用 router.options.routes 保持路由定义的顺序
  type RouteConfig = {
    path: string
    name?: string
    meta?: {
      title: string
      requiresAuth: boolean
      parent?: string
      icon?: string
      showInMenu?: boolean
      page?: PageMeta
    }
    redirect?: string
  }
  const routes = router.options.routes as RouteConfig[]
  // 获取所有需要在菜单中显示的路由
  const allRoutes = routes.filter((route) => route.meta?.showInMenu)

  // 构建菜单结构
  const menuMap = new Map<string, MenuItem>()
  const rootMenus: MenuItem[] = []

  // 首先处理所有路由，构建菜单映射
  allRoutes.forEach((route) => {
    // 如果有父菜单
    if (route.meta?.parent) {
      // 确保父菜单存在
      if (!menuMap.has(route.meta.parent)) {
        // 查找父菜单路由
        const parentRoute = routes.find((r) => r.name === route.meta?.parent)
        if (parentRoute && parentRoute.meta) {
          const parentMenuItem: MenuItem = {
            path: parentRoute.path,
            name: typeof parentRoute.name === 'string' ? parentRoute.name : undefined,
            meta: {
              title: parentRoute.meta.title,
              icon: parentRoute.meta.icon || 'Setting',
              showInMenu: parentRoute.meta.showInMenu || false,
              parent: parentRoute.meta.parent,
            },
            iconComponent: getIconComponent(parentRoute.meta.icon),
            children: [],
          }
          menuMap.set(route.meta.parent, parentMenuItem)
          rootMenus.push(parentMenuItem)
        }
      }
      // 将当前路由添加到父菜单的子菜单中
      if (route.meta.parent && menuMap.has(route.meta.parent) && route.meta) {
        const childMenuItem: MenuItem = {
          path: route.path,
          name: typeof route.name === 'string' ? route.name : undefined,
          meta: {
            title: route.meta.title,
            icon: route.meta.icon || 'Setting',
            showInMenu: route.meta.showInMenu || false,
            parent: route.meta.parent,
          },
          iconComponent: getIconComponent(route.meta.icon),
        }
        menuMap.get(route.meta.parent)?.children?.push(childMenuItem)
      }
    } else if (route.meta) {
      // 没有父菜单，是一级菜单
      const routeNameKey = typeof route.name === 'string' ? route.name : ''
      if (!menuMap.has(routeNameKey)) {
        const menuItem: MenuItem = {
          path: route.path,
          name: typeof route.name === 'string' ? route.name : undefined,
          meta: {
            title: route.meta.title,
            icon: route.meta.icon || 'Setting',
            showInMenu: route.meta.showInMenu || false,
            parent: route.meta.parent,
          },
          iconComponent: getIconComponent(route.meta.icon),
          children: [],
        }
        menuMap.set(routeNameKey, menuItem)
        rootMenus.push(menuItem)
      }
    }
  })

  return rootMenus
})

// 切换菜单显示状态
const toggleMenu = () => {
  isMenuOpen.value = !isMenuOpen.value
}

// 滚轮滚动期间关闭菜单命中测试，避免每帧重算 hover 触发重绘；触摸和拖动滚动条不触发 wheel。
// 空闲阈值要覆盖滚轮事件之间的间隔，又不能长到滚动停止后仍吞掉点击。
const MENU_SCROLL_IDLE_MS = 120
const isMenuScrolling = ref(false)
let menuScrollIdleTimer: ReturnType<typeof setTimeout> | undefined

const handleMenuWheel = () => {
  isMenuScrolling.value = true
  clearTimeout(menuScrollIdleTimer)
  menuScrollIdleTimer = setTimeout(() => {
    isMenuScrolling.value = false
  }, MENU_SCROLL_IDLE_MS)
}

// 处理菜单选择，移动端选择后关闭菜单
const handleMenuSelect = () => {
  if (isMobile.value) {
    isMenuOpen.value = false
  }
}

// 处理登出
const handleLogout = async () => {
  try {
    await ElMessageBox.confirm('确定要退出登录吗？', '提示', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning',
    })

    await authStore.logoutWithServer(http)
    ElMessage.success('已退出登录')
    router.replace('/login')
  } catch {
    // 用户取消
  }
}

// 获取当前页面标题
const getCurrentPageTitle = (): string => {
  return typeof route.meta.title === 'string' && route.meta.title.trim() ? route.meta.title : '首页'
}

// 当前页所属一级菜单的 index（即父级路由 path）。以 route.meta.parent 为准，
// 因为存在 /settings/strm 归属 sync、/settings/tmdb 归属 scrape 这类路径前缀与父菜单不一致的路由。
// 只有真正渲染成 el-sub-menu 的一级菜单才可展开，因此要求父菜单存在子菜单。
const activeParentMenuIndex = computed(() => {
  const parentName = route.meta.parent
  if (!parentName) return ''
  const parentMenu = menuItems.value.find((menu) => menu.name === parentName)
  if (!parentMenu?.children?.length) return ''
  return parentMenu.path
})

// el-menu 只在初始化时读取一次 default-openeds，之后不再监听该 prop，
// 因此它只决定首屏（硬刷新、登录后首次进入）的展开状态。
const defaultOpeneds = computed(() => {
  const index = activeParentMenuIndex.value
  return index ? [index] : []
})

// SPA 内跳转时，Element Plus 只能根据 default-active 命中的 el-menu-item 自行展开其祖先子菜单；
// 详情页和表单页这类 showInMenu 为 false 的路由没有对应菜单项，必须由这里补上展开。
watch(activeParentMenuIndex, (index) => {
  if (!index) return
  menuRef.value?.open(index)
})

// 获取进度状态样式
const getProgressStatus = () => {
  if (!backupStore.progress?.status) return undefined
  switch (backupStore.progress.status) {
    case 'completed':
      return 'success'
    case 'failed':
    case 'timeout':
      return 'exception'
    case 'cancelled':
      return 'warning'
    default:
      return undefined
  }
}

const openHelp = () => {
  window.open('https://gitee.com/qicfan/qmediasync/wikis/Home', '_blank')
}

watch(isMobile, (nextIsMobile) => {
  if (!nextIsMobile) {
    isMenuOpen.value = false
  }
})

onUnmounted(() => {
  // 清理轮询定时器
  backupStore.stopProgressPolling()
  clearTimeout(menuScrollIdleTimer)
})
</script>

<style>
#app {
  font-family: Avenir, Helvetica, Arial, sans-serif;
  color: #2c3e50;
  height: 100vh;
  overflow: hidden;
}

.common-layout {
  height: 100vh;
}

.login-layout {
  height: 100vh;
}

.el-container {
  height: 100%;
  position: relative;
}

.el-aside {
  --submenu-fade-in: 0.3s;
  --submenu-fade-out: 0.2s;

  background-color: rgb(244 244 245);
  z-index: 1000;
  display: flex;
  flex-direction: column;
}

.user-info {
  padding: 20px 15px;
  /* border-bottom: 1px solid #6e7072; */
  /* background-color: #9ea2a5; */
  display: flex;
  align-items: center;
  gap: 12px;
}

.user-avatar {
  width: 40px;
  height: 40px;
  border-radius: 50%;
  background-color: #409eff;
  display: flex;
  align-items: center;
  justify-content: center;
  color: white;
  flex-shrink: 0;
}

.user-details {
  flex: 1;
  min-width: 0;
}

.username {
  /* color: #fff; */
  font-size: 14px;
  font-weight: 500;
  margin-bottom: 4px;
  word-break: break-all;
}

.logout-btn {
  color: #909399 !important;
  font-size: 12px;
  padding: 0 !important;
  height: auto !important;
}

.logout-btn:hover {
  color: #ffd04b !important;
}

.el-menu-vertical {
  background-color: rgb(244 244 245);
  /* border-right: none; */
  flex: 1;
  /* 规避 Chromium 缺陷：菜单整体位移后图标 <path> 会被跳过绘制，提升为独立合成层修复 */
  will-change: transform;
}

.el-aside .el-menu-item,
.el-aside .el-sub-menu__title {
  transition: none;
}

.el-aside.is-menu-scrolling .el-menu-vertical {
  pointer-events: none;
}

.el-aside .el-collapse-transition-enter-active {
  transition: none;
  animation: submenu-fade var(--submenu-fade-in) ease-out;
}

.el-aside .el-collapse-transition-leave-active {
  /* 只有 max-height 需要推迟：Element Plus 的内联子菜单没有上下内边距，padding 是 0 到 0 */
  transition: max-height 0s var(--submenu-fade-out);
  animation: submenu-fade var(--submenu-fade-out) ease-out reverse;
}

@keyframes submenu-fade {
  from {
    opacity: 0;
    transform: translateY(-8px);
  }
}

.main-content {
  padding: 20px;
  background-color: #ffffff;
  overflow-y: auto;
}

.realtime-status-alert {
  margin-bottom: 16px;
}

/* 移动端样式 */
@media (max-width: 768px) {
  .mobile-aside {
    position: fixed !important;
    top: 0;
    left: 0;
    height: 100vh !important;
    transform: translateX(-100%);
    transition: transform 0.22s ease;
    will-change: transform;
    z-index: 1001;
  }

  .mobile-aside-open {
    transform: translateX(0) !important;
  }

  .mobile-overlay {
    position: fixed;
    top: 0;
    left: 0;
    width: 100vw;
    height: 100vh;
    background-color: rgba(0, 0, 0, 0.5);
    z-index: 1000;
  }

  .main-content {
    padding: 10px;
    margin-left: 0 !important;
    width: 100% !important;
  }

  .mobile-header {
    display: flex;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    background-color: #fff;
    padding: 10px 15px;
    margin: -10px -10px 8px -10px;
    box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
    border-radius: 0 0 8px 8px;
  }

  .mobile-header .left-section {
    display: flex;
    align-items: center;
    min-width: 0;
    flex: 1 1 auto;
  }

  .menu-toggle {
    flex: 0 0 auto;
    margin-right: 10px;
    font-size: 18px;
    color: #545c64 !important;
  }

  .page-title {
    min-width: 0;
    margin: 0;
    font-size: 18px;
    font-weight: 500;
    color: #303133;
    flex: 1 1 auto;
    overflow-wrap: anywhere;
    word-break: break-word;
  }

  .user-dropdown {
    flex: 0 0 auto;
    margin-left: 0;
  }

  .user-btn {
    flex: 0 0 auto;
    color: #545c64 !important;
    font-size: 18px;
  }

  .mobile-aside .el-menu-item,
  .mobile-aside .el-sub-menu__title {
    height: 56px !important;
    line-height: 56px !important;
  }

  .mobile-aside .el-menu-item > .el-icon,
  .mobile-aside .el-sub-menu__title > .el-icon:not(.el-sub-menu__icon-arrow) {
    width: 24px;
    margin-right: 12px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .mobile-aside {
    transition: none;
  }

  .el-aside .el-collapse-transition-enter-active,
  .el-aside .el-collapse-transition-leave-active {
    transition: none;
    animation: none;
  }
}

/* 桌面端样式 */
@media (min-width: 769px) {
  .mobile-header {
    display: none;
  }
}

/* 平板端适配 */
@media (min-width: 769px) and (max-width: 1024px) {
  .main-content {
    padding: 15px;
  }
}

/* 小屏移动设备优化 */
@media (max-width: 480px) {
  .mobile-aside {
    width: 280px !important;
  }

  .main-content {
    padding: 8px;
  }

  .mobile-header {
    padding: 8px 12px;
    margin: -8px -8px 8px -8px;
  }

  .page-title {
    font-size: 16px;
  }
}

/* 滚动条样式优化 */
::-webkit-scrollbar {
  width: 6px;
}

::-webkit-scrollbar-track {
  background: #f1f1f1;
}

::-webkit-scrollbar-thumb {
  background: #c1c1c1;
  border-radius: 3px;
}

::-webkit-scrollbar-thumb:hover {
  background: #a1a1a1;
}

/* 备份进度弹窗样式 */
.backup-progress-content {
  padding: 20px 0;
}

.progress-step {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 20px;
  font-size: 14px;
  color: #606266;
}

.progress-tables {
  margin-top: 12px;
  font-size: 13px;
  color: #909399;
  text-align: center;
}

.progress-time {
  display: flex;
  justify-content: space-around;
  margin-top: 16px;
  padding: 12px;
  background-color: #f5f7fa;
  border-radius: 4px;
}

.progress-time .time-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 4px;
}

.progress-time .label {
  font-size: 12px;
  color: #909399;
}

.progress-time .value {
  font-size: 16px;
  font-weight: 600;
  color: #409eff;
}
</style>
