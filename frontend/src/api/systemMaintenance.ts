import type { AxiosInstance } from 'axios'
import { SERVER_URL } from '@/const'
import { unwrapResponse } from './response'
import type { APIResponse } from './types'

export async function repairDatabase(http: AxiosInstance): Promise<void> {
  unwrapResponse(await http.post<APIResponse<null>>(`${SERVER_URL}/database/repair`))
}
