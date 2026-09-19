import axios from 'axios'
import { describe, expect, it } from 'vitest'
import { fetchSystemVersion } from '@/api/systemInfo'
import { HttpResponseError } from '@/http/errors'

const createHTTP = (data: unknown, status = 200) =>
  axios.create({
    adapter: async (config) => ({ status, data, config, headers: {}, statusText: '' }),
  })

describe('运行环境版本 API', () => {
  it('保留无业务包络的版本与环境字段', async () => {
    const version = {
      version: 'v1.0.0',
      build_time: 1,
      date: '2026-01-01',
      isWindows: true,
      isRelease: false,
    }
    await expect(fetchSystemVersion(createHTTP(version))).resolves.toEqual(version)
  })

  it.each([null, { code: 500, message: 'internal error' }, '<html>proxy error</html>'])(
    '错误响应不作为版本信息',
    async (data) => {
      await expect(fetchSystemVersion(createHTTP(data))).rejects.toBeInstanceOf(HttpResponseError)
    },
  )
})
