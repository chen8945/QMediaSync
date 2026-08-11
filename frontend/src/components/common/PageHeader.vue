<script setup lang="ts">
import { computed, useSlots } from 'vue'
import { useRoute } from 'vue-router'
import {
  getPageDescription,
  getPageIcon,
  getPageTitle,
  getPageVariant,
  type PageHeaderVariant,
} from '@/router/pageMeta'
import { getIconComponent } from './iconRegistry'

const props = defineProps<{
  title?: string
  description?: string
  icon?: string
  variant?: PageHeaderVariant
  actionsPosition?: 'start' | 'end'
  actionsLayout?: 'inline' | 'stacked'
  showIdentityOnMobile?: boolean
}>()

defineSlots<{
  actions?: () => unknown
  stats?: () => unknown
}>()

const route = useRoute()
const slots = useSlots()

const resolvedTitle = computed(() => (props.title?.trim() ? props.title : getPageTitle(route.meta)))
const resolvedDescription = computed(() =>
  props.description?.trim() ? props.description : getPageDescription(route.meta),
)
const resolvedIcon = computed(() =>
  props.icon !== undefined ? props.icon.trim() : getPageIcon(route.meta) || route.meta.icon,
)
const resolvedVariant = computed(() => props.variant ?? getPageVariant(route.meta))
const resolvedActionsPosition = computed(() => props.actionsPosition ?? 'end')
const resolvedActionsLayout = computed(() => props.actionsLayout ?? 'inline')
const titleIcon = computed(() => getIconComponent(resolvedIcon.value))
const hasActions = computed(() => Boolean(slots.actions))
const hasStats = computed(() => Boolean(slots.stats))
const hasAside = computed(() => hasActions.value || hasStats.value)

const headerClasses = computed(() => [
  `qms-page-header--${resolvedVariant.value}`,
  `qms-page-header--actions-${resolvedActionsPosition.value}`,
  `qms-page-header--actions-layout-${resolvedActionsLayout.value}`,
  {
    'qms-page-header--show-identity-mobile': props.showIdentityOnMobile,
    'qms-page-header--with-aside': hasAside.value,
    'qms-page-header--with-actions': hasActions.value,
    'qms-page-header--with-stats': hasStats.value,
  },
])
</script>

<template>
  <section class="qms-page-header" :class="headerClasses" data-testid="page-header">
    <div class="qms-page-header__top">
      <div class="qms-page-header__main">
        <div class="qms-page-header__identity">
          <span v-if="resolvedIcon" class="qms-page-header__icon" aria-hidden="true">
            <el-icon><component :is="titleIcon" /></el-icon>
          </span>
          <div class="qms-page-header__copy">
            <h1 class="qms-page-header__title">{{ resolvedTitle }}</h1>
            <p v-if="resolvedDescription" class="qms-page-header__description">
              {{ resolvedDescription }}
            </p>
          </div>
        </div>
      </div>

      <div v-if="$slots.actions" class="qms-page-header__actions">
        <slot name="actions" />
      </div>
    </div>

    <div v-if="$slots.stats" class="qms-page-header__stats">
      <slot name="stats" />
    </div>
  </section>
</template>

<style scoped>
.qms-page-header {
  width: 100%;
  margin-bottom: 20px;
  padding: 0;
  border-bottom: 0;
  background: transparent;
  box-sizing: border-box;
}

.qms-page-header--with-stats {
  margin-bottom: 20px;
}

.qms-page-header__top {
  display: flex;
  align-items: center;
  gap: 20px;
}

.qms-page-header__main {
  min-width: 0;
  flex: 1 1 auto;
}

.qms-page-header__identity {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 12px;
}

.qms-page-header__icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 31px;
  border-radius: 0;
  background: transparent;
  color: var(--el-color-primary, #409eff);
  font-size: 24px;
}

.qms-page-header__copy {
  min-width: 0;
}

.qms-page-header__title {
  margin: 0;
  color: var(--el-text-color-primary, #303133);
  font-size: 24px;
  font-weight: 600;
  line-height: 1.35;
  overflow-wrap: anywhere;
}

.qms-page-header__description {
  max-width: 760px;
  margin: 4px 0 0;
  color: var(--el-text-color-secondary, #606266);
  font-size: 14px;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.qms-page-header__stats {
  display: flex;
  min-width: 0;
  align-self: flex-start;
  align-items: flex-start;
  justify-content: flex-start;
  margin-top: 12px;
}

.qms-page-header__actions {
  display: flex;
  min-width: 0;
  flex: 0 1 auto;
  max-width: 100%;
  align-items: flex-start;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
}

.qms-page-header__actions :deep(.header-actions) {
  margin-top: 0;
}

.qms-page-header--actions-start .qms-page-header__top {
  align-items: flex-start;
  justify-content: flex-start;
}

.qms-page-header--actions-start .qms-page-header__main {
  order: 2;
}

.qms-page-header--actions-start .qms-page-header__identity {
  min-width: 0;
}

.qms-page-header--actions-start .qms-page-header__actions {
  order: 1;
  justify-content: flex-start;
}

.qms-page-header--actions-layout-stacked .qms-page-header__top {
  flex-direction: column;
  align-items: stretch;
  gap: 8px;
}

.qms-page-header--actions-layout-stacked .qms-page-header__actions {
  order: 1;
  justify-content: flex-start;
}

.qms-page-header--actions-layout-stacked .qms-page-header__main {
  order: 2;
}

.qms-page-header--compact {
  margin-bottom: 12px;
}

.qms-page-header--management .qms-page-header__top {
  gap: 16px;
}

.qms-page-header--settings,
.qms-page-header--detail {
  margin-bottom: 16px;
}

@media (max-width: 768px) {
  .qms-page-header:not(.qms-page-header--with-aside) {
    display: none;
  }

  .qms-page-header--with-aside {
    margin-bottom: 12px;
    padding: 0;
  }

  .qms-page-header__top {
    flex-direction: column;
    align-items: stretch;
    gap: 8px;
  }

  .qms-page-header__main {
    width: 100%;
    flex: none;
  }

  .qms-page-header__identity {
    display: none;
  }

  .qms-page-header:not(.qms-page-header--with-stats) .qms-page-header__main {
    display: none;
  }

  .qms-page-header.qms-page-header--show-identity-mobile {
    display: block;
  }

  .qms-page-header.qms-page-header--show-identity-mobile .qms-page-header__main {
    display: block;
  }

  .qms-page-header.qms-page-header--show-identity-mobile .qms-page-header__identity {
    display: flex;
  }

  .qms-page-header__actions {
    order: -1;
    width: 100%;
    flex: none;
    justify-content: flex-start;
    align-items: flex-start;
    padding-top: 0;
  }

  .qms-page-header--actions-end .qms-page-header__actions {
    padding-top: 0;
  }

  .qms-page-header.qms-page-header--show-identity-mobile .qms-page-header__actions {
    order: 0;
  }

  .qms-page-header__actions > :deep(*) {
    width: auto;
  }

  .qms-page-header__actions :deep(.header-actions) {
    margin-top: 0;
  }

  .qms-page-header__stats {
    width: 100%;
    margin-top: 0;
  }

  .qms-page-header--compact {
    margin-bottom: 8px;
  }

  .qms-page-header__actions :deep(.el-button) {
    max-width: 100%;
  }

  .qms-page-header--actions-start .qms-page-header__actions {
    width: 100%;
    justify-content: flex-start;
    padding-top: 0;
  }

  .qms-page-header--actions-start .qms-page-header__main {
    order: 0;
  }

  .qms-page-header.qms-page-header--actions-layout-stacked .qms-page-header__actions {
    order: 1;
  }

  .qms-page-header.qms-page-header--actions-layout-stacked .qms-page-header__main {
    order: 2;
  }
}
</style>
