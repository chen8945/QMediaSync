import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { getSettingsSaveResult } from './response'
import type { APIResponse, SettingsSaveResult } from './types'

export async function repairDatabase(http: AxiosInstance): Promise<SettingsSaveResult> {
  return getSettingsSaveResult(await http.post<APIResponse<null>>(`${SERVER_URL}/database/repair`))
}
