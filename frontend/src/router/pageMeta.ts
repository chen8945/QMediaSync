export type PageHeaderVariant = 'management' | 'compact' | 'settings' | 'detail'

export interface PageMeta {
  title?: string
  description?: string
  icon?: string
  variant?: PageHeaderVariant
}

export interface RouteMetaLike {
  title?: unknown
  page?: PageMeta
}

export const getPageTitle = (meta: RouteMetaLike, fallback = '页面'): string => {
  if (typeof meta.page?.title === 'string' && meta.page.title.trim()) {
    return meta.page.title
  }

  return typeof meta.title === 'string' && meta.title.trim() ? meta.title : fallback
}

export const getPageDescription = (meta: RouteMetaLike): string | undefined => {
  return typeof meta.page?.description === 'string' ? meta.page.description : undefined
}

export const getPageIcon = (meta: RouteMetaLike): string | undefined => {
  return typeof meta.page?.icon === 'string' ? meta.page.icon : undefined
}

export const getPageVariant = (meta: RouteMetaLike): PageHeaderVariant => {
  return meta.page?.variant ?? 'settings'
}
