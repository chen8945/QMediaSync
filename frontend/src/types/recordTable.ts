import type { Component } from 'vue'

export type RecordTableDensity = 'compact' | 'comfortable'
export type RecordColumnPriority = 'primary' | 'secondary' | 'detail'
export type RecordRowKey = string | number

export interface RecordActionPayload<TRow> {
  actionKey: string
  row: TRow
}

export interface RecordDetailField<TRow> {
  key: string
  label: string
  value: (row: TRow) => string | number | null | undefined
  span?: 1 | 2 | 3
  isLongText?: boolean
}

export interface RecordColumn<TRow> {
  key: string
  label: string
  priority: RecordColumnPriority
  minWidth?: number
  width?: number
  align?: 'left' | 'center' | 'right'
  showOverflowTooltip?: boolean
  className?: string
  detailField?: RecordDetailField<TRow>
  render?: Component
}

export interface RecordAction<TRow> {
  key: string
  label: string
  type?: 'primary' | 'success' | 'warning' | 'danger' | 'info'
  icon?: Component
  visible?: (row: TRow) => boolean
  disabled?: (row: TRow) => boolean
}
