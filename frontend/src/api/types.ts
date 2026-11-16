export interface APIResponse<T> {
  status: number
  data: T
  message?: string
  warnings?: string[]
  error_code?: string
  instance?: string
  field_errors?: APIFieldError[]
}

export interface APIFieldError {
  field: string
  message: string
  client_id?: string
}

export interface SettingsSaveResult {
  warnings: string[]
  message?: string
}
