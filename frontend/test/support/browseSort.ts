import type { BrowseScope, BrowseSortOptions } from '@/api/files'

// 服务端能力接口的测试替身；生产代码仅读取接口，不维护来源能力副本。
export function browseSortOptions(source = '115', scope: BrowseScope = 'files'): BrowseSortOptions {
  const directories = scope === 'directories'
  if (source === 'openlist')
    return {
      fields: ['default'],
      folders_first: false,
      default: { sort_by: 'default', sort_order: 'asc' },
    }
  return {
    fields: directories
      ? ['name', 'time', ...(source === '115' ? ['default' as const] : [])]
      : source === '115'
        ? ['name', 'time', 'size', 'type', 'default']
        : ['name', 'time', 'size'],
    folders_first: source === '115' && !directories,
    default: {
      sort_by: 'name',
      sort_order: 'asc',
      ...(source === '115' && !directories ? { folders_first: true } : {}),
    },
  }
}
