export interface APIResponse<T> {
  code: number
  message: string
  data: T
  error_code?: string
}
