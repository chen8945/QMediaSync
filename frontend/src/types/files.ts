export interface DirInfo {
  modified_time?: number
  id: string
  name: string
  path: string
}

// 文件管理器相关类型定义
export type FileType = 'directory' | 'video' | 'image' | 'nfo' | 'other'
export type FileOperationType =
  'STRM_GENERATE' | 'SCRAPE_ORGANIZE' | 'GENERATE_ED2K' | 'DELETE' | 'RENAME' | 'MOVE' | 'COPY'

// 文件系统项目接口
export interface FileSystemItem {
  id: string
  name: string
  path: string
  type: FileType
  size: number
  modified_time: number
  is_directory: boolean
}
